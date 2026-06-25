package local

import (
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

type audioFlowController struct {
	conf     config.AudioFlowConf
	sent     int
	start    time.Time
	position time.Duration
	now      func() time.Time
	sleep    func(time.Duration)
}

func newAudioFlowController(conf config.AudioFlowConf) *audioFlowController {
	c := &audioFlowController{
		conf:  conf,
		now:   time.Now,
		sleep: time.Sleep,
	}
	if c.conf.PreBufferFrames <= 0 {
		c.conf.PreBufferFrames = 3
	}
	if c.conf.FrameDurationMs <= 0 {
		c.conf.FrameDurationMs = 60
	}
	if c.conf.SendRateMultiplier <= 0 {
		c.conf.SendRateMultiplier = 1.0
	}
	if c.conf.MaxDelayMs <= 0 {
		c.conf.MaxDelayMs = 100
	}
	return c
}

func (c *audioFlowController) beforeBinaryFrame() {
	if c == nil || !c.conf.Enabled {
		return
	}
	if c.sent == 0 {
		c.start = c.now()
	}
	if c.sent >= c.conf.PreBufferFrames {
		rate := c.conf.SendRateMultiplier
		expected := c.start.Add(time.Duration(float64(c.position) / rate))
		if delay := expected.Sub(c.now()); delay > 0 {
			maxDelay := time.Duration(c.conf.MaxDelayMs) * time.Millisecond
			if delay > maxDelay {
				delay = maxDelay
			}
			c.sleep(delay)
		}
		c.position += time.Duration(c.conf.FrameDurationMs) * time.Millisecond
	}
	c.sent++
}

// reset 把流控状态清零。必须在每个 TTS 段（sentence）开始时调用——
// 否则 position/sent 跨段累积，第二段起 now() 远超 start+position，
// 节流失效（delay 为负不 sleep），音频瞬间灌入设备缓冲导致播放断续。
// Python 版每段独立 sendAudio（各自重置 start_time/play_position），此处对齐。
func (c *audioFlowController) reset() {
	if c == nil {
		return
	}
	c.sent = 0
	c.position = 0
	c.start = time.Time{}
}
