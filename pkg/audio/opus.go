package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"gopkg.in/hraban/opus.v2"
)

type PCMFrame struct {
	SampleRate int
	Channels   int
	Samples    []int16
}

func (f PCMFrame) BytesLE() []byte {
	var buf bytes.Buffer
	for _, sample := range f.Samples {
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

type OpusDecoder struct {
	sampleRate int
	channels   int
	frameSize  int
	gain       float32
	decoder    *opus.Decoder
}

type OpusDecoderConfig struct {
	SampleRate int
	Channels   int
	FrameSize  int
	Gain       float32
}

type OpusEncoder struct {
	sampleRate int
	channels   int
	frameSize  int
	encoder    *opus.Encoder
}

type OpusEncoderConfig struct {
	SampleRate int
	Channels   int
	FrameSize  int
}

func NewOpusEncoder(conf OpusEncoderConfig) (*OpusEncoder, error) {
	if conf.SampleRate <= 0 {
		return nil, fmt.Errorf("invalid sample rate: %d", conf.SampleRate)
	}
	if conf.Channels <= 0 {
		return nil, fmt.Errorf("invalid channels: %d", conf.Channels)
	}
	if conf.FrameSize <= 0 {
		conf.FrameSize = conf.SampleRate * 60 / 1000
	}
	encoder, err := opus.NewEncoder(conf.SampleRate, conf.Channels, opus.AppVoIP)
	if err != nil {
		return nil, err
	}
	return &OpusEncoder{
		sampleRate: conf.SampleRate,
		channels:   conf.Channels,
		frameSize:  conf.FrameSize,
		encoder:    encoder,
	}, nil
}

func (e *OpusEncoder) Encode(samples []int16) ([][]byte, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	frameSamples := e.frameSize * e.channels
	frames := make([][]byte, 0, (len(samples)+frameSamples-1)/frameSamples)
	for start := 0; start < len(samples); start += frameSamples {
		end := start + frameSamples
		frame := make([]int16, frameSamples)
		if end > len(samples) {
			copy(frame, samples[start:])
		} else {
			copy(frame, samples[start:end])
		}
		packet := make([]byte, 1275)
		n, err := e.encoder.Encode(frame, packet)
		if err != nil {
			return nil, err
		}
		frames = append(frames, append([]byte(nil), packet[:n]...))
	}
	return frames, nil
}

func NewOpusDecoder(conf OpusDecoderConfig) (*OpusDecoder, error) {
	if conf.SampleRate <= 0 {
		return nil, fmt.Errorf("invalid sample rate: %d", conf.SampleRate)
	}
	if conf.Channels <= 0 {
		return nil, fmt.Errorf("invalid channels: %d", conf.Channels)
	}
	if conf.FrameSize <= 0 {
		conf.FrameSize = conf.SampleRate * 60 / 1000
	}
	if conf.Gain == 0 {
		conf.Gain = 1
	}
	decoder, err := opus.NewDecoder(conf.SampleRate, conf.Channels)
	if err != nil {
		return nil, err
	}
	return &OpusDecoder{
		sampleRate: conf.SampleRate,
		channels:   conf.Channels,
		frameSize:  conf.FrameSize,
		gain:       conf.Gain,
		decoder:    decoder,
	}, nil
}

func (d *OpusDecoder) Decode(packet []byte) (PCMFrame, error) {
	if len(packet) == 0 {
		return PCMFrame{SampleRate: d.sampleRate, Channels: d.channels}, nil
	}
	pcm := make([]int16, d.frameSize*d.channels)
	n, err := d.decoder.Decode(packet, pcm)
	if err != nil {
		return PCMFrame{}, err
	}
	pcm = pcm[:n*d.channels]
	return PCMFrame{
		SampleRate: d.sampleRate,
		Channels:   d.channels,
		Samples:    ApplyGain(pcm, d.gain),
	}, nil
}

// ApplyGain 缩放 int16 PCM 采样值，用于统一调整输出音量。gain=1 直接返回
// 拷贝（不放大），<1 衰减，>1 放大（按输入峰值自适应封顶增益，保证放大后
// 不超出 int16 范围，避免削波失真；全静音输入直接返回拷贝）。
func ApplyGain(input []int16, gain float32) []int16 {
	if gain == 1 {
		return append([]int16(nil), input...)
	}
	if gain > 1 {
		// 用 int32 累计绝对值峰值，规避 -32768 取负溢出。
		var peak int32
		for _, sample := range input {
			abs := int32(sample)
			if abs < 0 {
				abs = -abs
			}
			if abs > peak {
				peak = abs
			}
		}
		if peak == 0 {
			return append([]int16(nil), input...)
		}
		if maxGain := float32(math.MaxInt16) / float32(peak); gain > maxGain {
			gain = maxGain
		}
	}
	output := make([]int16, len(input))
	for i, sample := range input {
		adjusted := float32(sample) * gain
		// 封顶后理论上不会越界，限幅仅作为 float 舍入的安全网。
		switch {
		case adjusted > math.MaxInt16:
			output[i] = math.MaxInt16
		case adjusted < math.MinInt16:
			output[i] = math.MinInt16
		default:
			output[i] = int16(adjusted)
		}
	}
	return output
}
