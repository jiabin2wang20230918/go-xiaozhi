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
