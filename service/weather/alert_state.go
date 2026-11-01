package weather

import (
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

type alertStateEntry struct {
	hasPrev  bool
	prev     AlertSnapshot
	lastFire map[AlertKind]time.Time
}

// EvaluateAlerts runs differential detection for a city and updates internal state.
// locationKey should match the cache key used for QuerySnapshot (trimmed query location or city name).
func (c *Client) EvaluateAlerts(locationKey string, curr *AlertSnapshot, conf config.GetWeatherConf, now time.Time) []AlertResult {
	if curr == nil {
		return nil
	}
	cfg := alertDetectConfigFrom(conf)
	c.alertMu.Lock()
	defer c.alertMu.Unlock()
	if c.alertState == nil {
		c.alertState = make(map[string]*alertStateEntry)
	}
	entry := c.alertState[locationKey]
	if entry == nil {
		entry = &alertStateEntry{lastFire: make(map[AlertKind]time.Time)}
		c.alertState[locationKey] = entry
	}
	alerts := DetectAlerts(entry.hasPrev, entry.prev, *curr, cfg, now, entry.lastFire)
	entry.prev = *curr
	entry.hasPrev = true
	return alerts
}

func alertDetectConfigFrom(conf config.GetWeatherConf) AlertDetectConfig {
	conf.Normalize()
	cooldown := time.Duration(conf.AlertCooldownSeconds) * time.Second
	if conf.AlertCooldownSeconds <= 0 {
		cooldown = 3 * time.Hour
	}
	coolDrop := conf.AlertCoolDropC
	if coolDrop <= 0 {
		coolDrop = 5
	}
	display := conf.AlertDisplaySeconds
	if display <= 0 {
		display = 8
	}
	return AlertDetectConfig{
		CoolDropC:      coolDrop,
		Cooldown:       cooldown,
		PrecipKeywords: conf.AlertPrecipKeywords,
		Speak:          conf.AlertSpeak,
		DisplaySeconds: display,
	}
}

// alertMu and alertState are on Client (see weather.go).
