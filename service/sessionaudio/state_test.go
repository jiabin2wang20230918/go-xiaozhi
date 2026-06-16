package sessionaudio

import (
	"context"
	"testing"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	"github.com/xdimtech/go-xiaozhi/service/voice"
)

type scriptedVAD struct {
	values []bool
}

func (v *scriptedVAD) HasVoice(ctx context.Context, frame voice.AudioFrame) (bool, error) {
	if len(v.values) == 0 {
		return false, nil
	}
	value := v.values[0]
	v.values = v.values[1:]
	return value, nil
}

type passthroughDecoder struct{}

func (passthroughDecoder) Decode(packet []byte) (audio.PCMFrame, error) {
	samples := make([]int16, len(packet))
	for i, b := range packet {
		samples[i] = int16(b)
	}
	return audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: samples}, nil
}

func TestManualStopRequiresMinimumFrames(t *testing.T) {
	state := NewWithDecoder(voice.NonEmptyVAD{}, passthroughDecoder{}, config.AudioSessionConf{MinFrames: 3, PreBufferFrames: 2})
	state.SetMode("manual")
	state.Start()

	if result, err := state.Push(context.Background(), []byte{1}); err != nil || result.Ready {
		t.Fatalf("unexpected push result: %+v err=%v", result, err)
	}
	if result := state.Stop(); result.Ready {
		t.Fatalf("short utterance should not be ready: %+v", result)
	}
}

func TestManualStopReturnsFramesWhenReady(t *testing.T) {
	state := NewWithDecoder(voice.NonEmptyVAD{}, passthroughDecoder{}, config.AudioSessionConf{MinFrames: 3, PreBufferFrames: 2})
	state.SetMode("manual")
	state.Start()
	for i := 0; i < 3; i++ {
		if _, err := state.Push(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatalf("push frame: %v", err)
		}
	}

	result := state.Stop()
	if !result.Ready {
		t.Fatal("expected utterance to be ready")
	}
	if len(result.Frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(result.Frames))
	}
}

func TestAutoModeKeepsOnlyRecentPreBufferBeforeSpeech(t *testing.T) {
	state := NewWithDecoder(&scriptedVAD{values: []bool{false, false, false, true, true}}, passthroughDecoder{}, config.AudioSessionConf{
		MinFrames:       4,
		PreBufferFrames: 2,
	})
	for i := 0; i < 5; i++ {
		if _, err := state.Push(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatalf("push frame: %v", err)
		}
	}

	result := state.Stop()
	if !result.Ready {
		t.Fatal("expected utterance to be ready")
	}
	if len(result.Frames) != 4 {
		t.Fatalf("expected 2 prebuffer + 2 speech frames, got %d", len(result.Frames))
	}
	if got := result.Frames[0].Opus[0]; got != 1 {
		t.Fatalf("expected oldest retained prebuffer frame to be 1, got %d", got)
	}
}

func TestAutoModeStopsAfterSilenceDuration(t *testing.T) {
	state := NewWithDecoder(&scriptedVAD{values: []bool{true, true, false}}, passthroughDecoder{}, config.AudioSessionConf{
		MinFrames:     3,
		SilenceStopMs: 700,
	})
	now := time.Unix(100, 0)
	state.now = func() time.Time { return now }

	if result, err := state.Push(context.Background(), []byte{1}); err != nil || result.Ready {
		t.Fatalf("first voice push got %+v err=%v", result, err)
	}
	now = now.Add(100 * time.Millisecond)
	if result, err := state.Push(context.Background(), []byte{2}); err != nil || result.Ready {
		t.Fatalf("second voice push got %+v err=%v", result, err)
	}
	now = now.Add(700 * time.Millisecond)
	result, err := state.Push(context.Background(), []byte{3})
	if err != nil {
		t.Fatalf("silent push: %v", err)
	}
	if !result.Ready {
		t.Fatal("expected auto utterance to finish after silence")
	}
	if len(result.Frames) != 3 {
		t.Fatalf("expected voice frames plus trailing silence, got %d", len(result.Frames))
	}
}

func TestManualStartPreservesRecentPreBuffer(t *testing.T) {
	state := NewWithDecoder(&scriptedVAD{values: []bool{false, false}}, passthroughDecoder{}, config.AudioSessionConf{
		MinFrames:       3,
		PreBufferFrames: 2,
	})
	for i := 0; i < 2; i++ {
		if _, err := state.Push(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatalf("push prebuffer frame: %v", err)
		}
	}
	state.SetMode("manual")
	state.Start()
	if _, err := state.Push(context.Background(), []byte{9}); err != nil {
		t.Fatalf("push speech frame: %v", err)
	}
	result := state.Stop()
	if !result.Ready {
		t.Fatal("expected utterance to include preserved prebuffer")
	}
	if len(result.Frames) != 3 {
		t.Fatalf("expected 2 prebuffer + 1 speech frame, got %d", len(result.Frames))
	}
	if result.Frames[0].Opus[0] != 0 || result.Frames[1].Opus[0] != 1 || result.Frames[2].Opus[0] != 9 {
		t.Fatalf("unexpected frame order: %+v", result.Frames)
	}
}

func TestDetectPausesAudioUntilResume(t *testing.T) {
	state := NewWithDecoder(voice.NonEmptyVAD{}, passthroughDecoder{}, config.AudioSessionConf{MinFrames: 1})
	state.Detect()
	result, err := state.Push(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("push while paused: %v", err)
	}
	if result.Ready {
		t.Fatal("paused ASR should not produce ready result")
	}

	state.Resume()
	state.Start()
	if _, err := state.Push(context.Background(), []byte{1}); err != nil {
		t.Fatalf("push after resume: %v", err)
	}
	if result := state.Stop(); !result.Ready {
		t.Fatal("expected ready after resume")
	}
}

func TestAutoModeNoVoiceTimeout(t *testing.T) {
	state := NewWithDecoder(&scriptedVAD{values: []bool{false, false, false}}, passthroughDecoder{}, config.AudioSessionConf{
		PreBufferFrames:     2,
		NoVoiceCloseSeconds: 1,
	})
	now := time.Unix(100, 0)
	state.now = func() time.Time { return now }

	result, err := state.Push(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("first silent push: %v", err)
	}
	if result.NoVoiceTimeout {
		t.Fatal("first silent frame should only start timeout clock")
	}
	now = now.Add(1500 * time.Millisecond)
	result, err = state.Push(context.Background(), []byte{2})
	if err != nil {
		t.Fatalf("second silent push: %v", err)
	}
	if !result.NoVoiceTimeout {
		t.Fatal("expected no voice timeout")
	}
	result, err = state.Push(context.Background(), []byte{3})
	if err != nil {
		t.Fatalf("push after timeout: %v", err)
	}
	if result.Ready || result.NoVoiceTimeout {
		t.Fatalf("asr should be paused after no voice timeout: %+v", result)
	}
}
