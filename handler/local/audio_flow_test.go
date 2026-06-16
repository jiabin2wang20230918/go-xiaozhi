package local

import (
	"testing"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestAudioFlowControllerPrebuffersThenThrottles(t *testing.T) {
	now := time.Unix(100, 0)
	var sleeps []time.Duration
	flow := newAudioFlowController(config.AudioFlowConf{
		Enabled:            true,
		PreBufferFrames:    3,
		FrameDurationMs:    60,
		SendRateMultiplier: 1.0,
		MaxDelayMs:         100,
	})
	flow.now = func() time.Time { return now }
	flow.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }

	for i := 0; i < 4; i++ {
		flow.beforeBinaryFrame()
	}
	if len(sleeps) != 0 {
		t.Fatalf("first throttled frame should not sleep, got %v", sleeps)
	}
	flow.beforeBinaryFrame()
	if len(sleeps) != 1 || sleeps[0] != 60*time.Millisecond {
		t.Fatalf("expected 60ms sleep after prebuffer, got %v", sleeps)
	}
}

func TestAudioFlowControllerCapsDelay(t *testing.T) {
	now := time.Unix(100, 0)
	var sleeps []time.Duration
	flow := newAudioFlowController(config.AudioFlowConf{
		Enabled:            true,
		PreBufferFrames:    1,
		FrameDurationMs:    200,
		SendRateMultiplier: 1.0,
		MaxDelayMs:         50,
	})
	flow.now = func() time.Time { return now }
	flow.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }

	flow.beforeBinaryFrame()
	flow.beforeBinaryFrame()
	flow.beforeBinaryFrame()

	if len(sleeps) == 0 {
		t.Fatal("expected throttling sleep")
	}
	for _, sleep := range sleeps {
		if sleep > 50*time.Millisecond {
			t.Fatalf("sleep should be capped at 50ms, got %v", sleeps)
		}
	}
}
