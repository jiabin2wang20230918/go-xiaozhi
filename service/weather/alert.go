package weather

import (
	"fmt"
	"strings"
	"time"
)

// AlertKind identifies a weather mutation alert type.
type AlertKind string

const (
	KindPrecipLive     AlertKind = "precip_live"
	KindPrecipForecast AlertKind = "precip_forecast"
	KindCoolDrop       AlertKind = "cool_drop"
)

// AlertSnapshot holds live + today forecast fields used for differential detection.
type AlertSnapshot struct {
	City         string
	LiveWeather  string
	Temperature  int
	DayWeather   string
	NightWeather string
}

// AlertDetectConfig controls thresholds and cooldown for mutation alerts.
type AlertDetectConfig struct {
	CoolDropC      float64
	Cooldown       time.Duration
	PrecipKeywords []string
	Speak          bool
	DisplaySeconds int
}

// AlertResult is a device notification payload (category=weather_alert).
type AlertResult struct {
	ID             string
	Kind           AlertKind
	Title          string
	Content        string
	Speak          bool
	DisplaySeconds int
}

// DefaultPrecipKeywords matches common Amap live/forecast weather text for rain/snow.
func DefaultPrecipKeywords() []string {
	return []string{"雨", "雪", "雷", "雹", "冻雨", "阵雨", "暴雪", "雷雨", "冰雹"}
}

// IsPrecipWeather reports whether text describes precipitation (rain/snow/thunder/hail).
func IsPrecipWeather(text string, keywords []string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	kws := keywords
	if len(kws) == 0 {
		kws = DefaultPrecipKeywords()
	}
	for _, kw := range kws {
		kw = strings.TrimSpace(kw)
		if kw != "" && strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// AlertNotificationID builds a stable id: weather-alert-{city}-{kind}-{timebucket}.
func AlertNotificationID(city string, kind AlertKind, now time.Time, cooldown time.Duration) string {
	city = strings.TrimSpace(city)
	if city == "" {
		city = "unknown"
	}
	bucket := int64(0)
	if cooldown > 0 {
		bucket = now.Unix() / int64(cooldown.Seconds())
	}
	return fmt.Sprintf("weather-alert-%s-%s-%d", sanitizeIDPart(city), kind, bucket)
}

func sanitizeIDPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "x"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}

// DetectAlerts compares previous and current snapshots and returns alerts to emit.
// lastFired maps alert kind to the last emission time for cooldown; it is updated in place.
// When hasPrev is false, no alerts fire (baseline snapshot only).
func DetectAlerts(
	hasPrev bool,
	prev, curr AlertSnapshot,
	cfg AlertDetectConfig,
	now time.Time,
	lastFired map[AlertKind]time.Time,
) []AlertResult {
	if curr.City == "" {
		curr.City = prev.City
	}
	keywords := cfg.PrecipKeywords
	if len(keywords) == 0 {
		keywords = DefaultPrecipKeywords()
	}
	if !hasPrev {
		return nil
	}

	var out []AlertResult
	try := func(kind AlertKind, title, content string) {
		if cfg.Cooldown > 0 {
			if t, ok := lastFired[kind]; ok && now.Sub(t) < cfg.Cooldown {
				return
			}
		}
		lastFired[kind] = now
		out = append(out, AlertResult{
			ID:             AlertNotificationID(curr.City, kind, now, cfg.Cooldown),
			Kind:           kind,
			Title:          title,
			Content:        content,
			Speak:          cfg.Speak,
			DisplaySeconds: cfg.DisplaySeconds,
		})
	}

	liveWasDry := !IsPrecipWeather(prev.LiveWeather, keywords)
	liveNowPrecip := IsPrecipWeather(curr.LiveWeather, keywords)
	if liveWasDry && liveNowPrecip {
		try(KindPrecipLive, "天气突变", fmt.Sprintf("%s开始出现%s，请注意出行。", curr.City, curr.LiveWeather))
	}

	liveStillDry := !liveNowPrecip
	if liveStillDry {
		dayNew := IsPrecipWeather(curr.DayWeather, keywords) && !IsPrecipWeather(prev.DayWeather, keywords)
		nightNew := IsPrecipWeather(curr.NightWeather, keywords) && !IsPrecipWeather(prev.NightWeather, keywords)
		if dayNew || nightNew {
			var parts []string
			if dayNew {
				parts = append(parts, "白天"+curr.DayWeather)
			}
			if nightNew {
				parts = append(parts, "夜间"+curr.NightWeather)
			}
			try(KindPrecipForecast, "降水预报", fmt.Sprintf("%s预报将%s，当前仍无降水。", curr.City, strings.Join(parts, "、")))
		}
	}

	if cfg.CoolDropC > 0 && prev.Temperature > 0 && curr.Temperature > 0 {
		drop := prev.Temperature - curr.Temperature
		if float64(drop) >= cfg.CoolDropC {
			try(KindCoolDrop, "降温提醒", fmt.Sprintf("%s气温下降%d℃，当前%d℃。", curr.City, drop, curr.Temperature))
		}
	}

	return out
}
