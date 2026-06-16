package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

type WAVInfo struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	DataBytes     int
}

func ReadWAV(r io.Reader) (PCMFrame, WAVInfo, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return PCMFrame{}, WAVInfo{}, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return PCMFrame{}, WAVInfo{}, fmt.Errorf("invalid wav header")
	}

	offset := 12
	var info WAVInfo
	var audioFormat uint16
	var pcmData []byte
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		offset += 8
		if chunkSize < 0 || offset+chunkSize > len(data) {
			return PCMFrame{}, WAVInfo{}, fmt.Errorf("invalid wav chunk size")
		}
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return PCMFrame{}, WAVInfo{}, fmt.Errorf("invalid fmt chunk")
			}
			audioFormat = binary.LittleEndian.Uint16(data[offset : offset+2])
			info.Channels = int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(data[offset+14 : offset+16]))
		case "data":
			pcmData = data[offset : offset+chunkSize]
			info.DataBytes = chunkSize
		}
		offset += chunkSize
		if chunkSize%2 == 1 {
			offset++
		}
	}
	if audioFormat != 1 {
		return PCMFrame{}, WAVInfo{}, fmt.Errorf("unsupported wav format: %d", audioFormat)
	}
	if info.SampleRate <= 0 || info.Channels <= 0 {
		return PCMFrame{}, WAVInfo{}, fmt.Errorf("invalid wav format")
	}
	if info.BitsPerSample != 16 {
		return PCMFrame{}, WAVInfo{}, fmt.Errorf("unsupported bits per sample: %d", info.BitsPerSample)
	}
	if len(pcmData)%2 != 0 {
		return PCMFrame{}, WAVInfo{}, fmt.Errorf("invalid pcm data size")
	}

	samples := make([]int16, len(pcmData)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcmData[i*2:]))
	}
	return PCMFrame{SampleRate: info.SampleRate, Channels: info.Channels, Samples: samples}, info, nil
}

func ConvertPCMFrame(frame PCMFrame, sampleRate int, channels int) PCMFrame {
	if sampleRate <= 0 {
		sampleRate = frame.SampleRate
	}
	if channels <= 0 {
		channels = frame.Channels
	}
	samples := frame.Samples
	if frame.Channels > 1 && channels == 1 {
		samples = mixToMono(samples, frame.Channels)
	} else if frame.Channels == 1 && channels > 1 {
		samples = duplicateChannels(samples, channels)
	}
	currentChannels := channels
	if frame.Channels == channels {
		currentChannels = frame.Channels
	}
	if frame.SampleRate != sampleRate {
		samples = resampleLinear(samples, frame.SampleRate, sampleRate, currentChannels)
	}
	return PCMFrame{SampleRate: sampleRate, Channels: channels, Samples: samples}
}

func mixToMono(samples []int16, channels int) []int16 {
	if channels <= 1 {
		return append([]int16(nil), samples...)
	}
	out := make([]int16, 0, len(samples)/channels)
	for i := 0; i+channels <= len(samples); i += channels {
		sum := 0
		for ch := 0; ch < channels; ch++ {
			sum += int(samples[i+ch])
		}
		out = append(out, int16(sum/channels))
	}
	return out
}

func duplicateChannels(samples []int16, channels int) []int16 {
	if channels <= 1 {
		return append([]int16(nil), samples...)
	}
	out := make([]int16, 0, len(samples)*channels)
	for _, sample := range samples {
		for ch := 0; ch < channels; ch++ {
			out = append(out, sample)
		}
	}
	return out
}

func resampleLinear(samples []int16, fromRate int, toRate int, channels int) []int16 {
	if fromRate <= 0 || toRate <= 0 || fromRate == toRate || channels <= 0 || len(samples) == 0 {
		return append([]int16(nil), samples...)
	}
	inFrames := len(samples) / channels
	outFrames := int(math.Ceil(float64(inFrames) * float64(toRate) / float64(fromRate)))
	out := make([]int16, outFrames*channels)
	for i := 0; i < outFrames; i++ {
		pos := float64(i) * float64(fromRate) / float64(toRate)
		left := int(math.Floor(pos))
		right := left + 1
		if right >= inFrames {
			right = inFrames - 1
		}
		frac := pos - float64(left)
		for ch := 0; ch < channels; ch++ {
			a := float64(samples[left*channels+ch])
			b := float64(samples[right*channels+ch])
			out[i*channels+ch] = int16(a + (b-a)*frac)
		}
	}
	return out
}

func WriteWAV(w io.Writer, frames []PCMFrame) (WAVInfo, error) {
	if len(frames) == 0 {
		return WAVInfo{}, fmt.Errorf("no pcm frames")
	}

	info := WAVInfo{
		SampleRate:    frames[0].SampleRate,
		Channels:      frames[0].Channels,
		BitsPerSample: 16,
	}
	if info.SampleRate <= 0 {
		return WAVInfo{}, fmt.Errorf("invalid sample rate: %d", info.SampleRate)
	}
	if info.Channels <= 0 {
		return WAVInfo{}, fmt.Errorf("invalid channels: %d", info.Channels)
	}

	for _, frame := range frames {
		if frame.SampleRate != info.SampleRate || frame.Channels != info.Channels {
			return WAVInfo{}, fmt.Errorf("mixed pcm format")
		}
		info.DataBytes += len(frame.Samples) * 2
	}

	if _, err := w.Write([]byte("RIFF")); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(36+info.DataBytes)); err != nil {
		return WAVInfo{}, err
	}
	if _, err := w.Write([]byte("WAVEfmt ")); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(16)); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(1)); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(info.Channels)); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(info.SampleRate)); err != nil {
		return WAVInfo{}, err
	}
	byteRate := info.SampleRate * info.Channels * info.BitsPerSample / 8
	if err := binary.Write(w, binary.LittleEndian, uint32(byteRate)); err != nil {
		return WAVInfo{}, err
	}
	blockAlign := info.Channels * info.BitsPerSample / 8
	if err := binary.Write(w, binary.LittleEndian, uint16(blockAlign)); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(info.BitsPerSample)); err != nil {
		return WAVInfo{}, err
	}
	if _, err := w.Write([]byte("data")); err != nil {
		return WAVInfo{}, err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(info.DataBytes)); err != nil {
		return WAVInfo{}, err
	}

	for _, frame := range frames {
		if _, err := w.Write(frame.BytesLE()); err != nil {
			return WAVInfo{}, err
		}
	}
	return info, nil
}
