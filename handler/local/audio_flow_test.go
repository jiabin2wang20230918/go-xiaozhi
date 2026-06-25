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

// 每段语音（sentence）开始时必须 reset，否则 position/sent 跨段累积，
// 第二段起 now() 远超 start+position → delay 为负 → 不节流 → 音频瞬间灌入设备缓冲。
// 这正是"第一句流畅、第二句断续"的根因。Python 版每段独立 sendAudio（各自重置）。
func TestAudioFlowControllerResetsBetweenSentences(t *testing.T) {
	// 第一段：发送过程中时间推进（模拟按帧节流耗时）。
	t0 := time.Unix(100, 0)
	now := t0
	flow := newAudioFlowController(config.AudioFlowConf{
		Enabled:            true,
		PreBufferFrames:    1,
		FrameDurationMs:    60,
		SendRateMultiplier: 1.0,
		MaxDelayMs:         100,
	})
	flow.now = func() time.Time { return now }
	var slept time.Duration
	flow.sleep = func(d time.Duration) {
		now = now.Add(d) // 模拟 sleep 推进时钟
		slept += d
	}
	// 发送第一段 5 帧（节流正常）。
	for i := 0; i < 5; i++ {
		flow.beforeBinaryFrame()
	}

	// 段间间隔：时钟推进（如合成/段间延迟），但不 reset 时流控状态仍累积。
	now = now.Add(2 * time.Second)
	slept = 0
	// 不 reset：第二段节流失效——now 远超 start+position，所有帧 delay 为负，不 sleep。
	for i := 0; i < 5; i++ {
		flow.beforeBinaryFrame()
	}
	if slept != 0 {
		t.Fatalf("without reset, second sentence should not throttle (audio floods device), got sleeps=%v", slept)
	}

	// reset 后：第二段重新从 0 节流，首帧之后恢复 sleep。
	flow.reset()
	slept = 0
	now = t0.Add(3 * time.Second) // 任意时刻
	for i := 0; i < 5; i++ {
		flow.beforeBinaryFrame()
	}
	if slept == 0 {
		t.Fatal("after reset, second sentence should throttle again")
	}
}
