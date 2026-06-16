package audio

import (
	"testing"

	"gopkg.in/hraban/opus.v2"
)

func TestOpusDecoderDecodeEncodedFrame(t *testing.T) {
	const sampleRate = 16000
	const channels = 1
	const frameSize = 960

	encoder, err := opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	pcm := make([]int16, frameSize)
	for i := range pcm {
		pcm[i] = int16(i % 200)
	}
	packet := make([]byte, 1275)
	n, err := encoder.Encode(pcm, packet)
	if err != nil {
		t.Fatalf("encode opus: %v", err)
	}

	decoder, err := NewOpusDecoder(OpusDecoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
		Gain:       1,
	})
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	frame, err := decoder.Decode(packet[:n])
	if err != nil {
		t.Fatalf("decode opus: %v", err)
	}
	if frame.SampleRate != sampleRate || frame.Channels != channels {
		t.Fatalf("unexpected pcm format: %+v", frame)
	}
	if len(frame.Samples) == 0 {
		t.Fatal("decoded frame has no samples")
	}
	if len(frame.BytesLE()) != len(frame.Samples)*2 {
		t.Fatalf("unexpected pcm byte length")
	}
}

func TestOpusEncoderEncodeAndDecode(t *testing.T) {
	const sampleRate = 16000
	const channels = 1
	const frameSize = 960

	encoder, err := NewOpusEncoder(OpusEncoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
	})
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	pcm := make([]int16, frameSize*2)
	for i := range pcm {
		pcm[i] = 1000
	}
	packets, err := encoder.Encode(pcm)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("expected 2 packets, got %d", len(packets))
	}

	decoder, err := NewOpusDecoder(OpusDecoderConfig{SampleRate: sampleRate, Channels: channels, FrameSize: frameSize})
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	for _, packet := range packets {
		frame, err := decoder.Decode(packet)
		if err != nil {
			t.Fatalf("decode encoded packet: %v", err)
		}
		if len(frame.Samples) == 0 {
			t.Fatal("decoded packet has no samples")
		}
	}
}
