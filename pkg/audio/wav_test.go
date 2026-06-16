package audio

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWriteWAV(t *testing.T) {
	var buf bytes.Buffer
	info, err := WriteWAV(&buf, []PCMFrame{{
		SampleRate: 16000,
		Channels:   1,
		Samples:    []int16{1, -1, 2, -2},
	}})
	if err != nil {
		t.Fatalf("write wav: %v", err)
	}
	if info.DataBytes != 8 {
		t.Fatalf("unexpected data bytes: %d", info.DataBytes)
	}
	data := buf.Bytes()
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("missing wav header: %q", data[:12])
	}
	if got := binary.LittleEndian.Uint32(data[24:28]); got != 16000 {
		t.Fatalf("unexpected sample rate: %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[40:44]); got != 8 {
		t.Fatalf("unexpected wav data size: %d", got)
	}
}

func TestWriteWAVRejectsMixedFormat(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteWAV(&buf, []PCMFrame{
		{SampleRate: 16000, Channels: 1, Samples: []int16{1}},
		{SampleRate: 24000, Channels: 1, Samples: []int16{1}},
	})
	if err == nil {
		t.Fatal("expected mixed format error")
	}
}

func TestReadWAVAndConvertPCM(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteWAV(&buf, []PCMFrame{{
		SampleRate: 8000,
		Channels:   2,
		Samples:    []int16{100, 300, 200, 400},
	}})
	if err != nil {
		t.Fatalf("write wav: %v", err)
	}
	frame, info, err := ReadWAV(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read wav: %v", err)
	}
	if info.SampleRate != 8000 || info.Channels != 2 || len(frame.Samples) != 4 {
		t.Fatalf("unexpected wav: frame=%+v info=%+v", frame, info)
	}

	converted := ConvertPCMFrame(frame, 16000, 1)
	if converted.SampleRate != 16000 || converted.Channels != 1 {
		t.Fatalf("unexpected converted format: %+v", converted)
	}
	if len(converted.Samples) < 4 {
		t.Fatalf("expected resampled samples, got %d", len(converted.Samples))
	}
	if converted.Samples[0] != 200 {
		t.Fatalf("expected first mono sample to be average, got %d", converted.Samples[0])
	}
}
