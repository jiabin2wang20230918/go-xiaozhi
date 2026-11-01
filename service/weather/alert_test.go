package weather

import (
	"testing"
	"time"
)

func TestIsPrecipWeather(t *testing.T) {
	kw := DefaultPrecipKeywords()
	cases := []struct {
		text string
		want bool
	}{
		{"多云", false},
		{"小雨", true},
		{"雷阵雨", true},
		{"暴雪", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPrecipWeather(tc.text, kw); got != tc.want {
			t.Fatalf("IsPrecipWeather(%q) = %v want %v", tc.text, got, tc.want)
		}
	}
}

func TestDetectAlertsPrecipLive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := AlertDetectConfig{Cooldown: time.Hour, Speak: true, DisplaySeconds: 8}
	last := map[AlertKind]time.Time{}
	prev := AlertSnapshot{City: "深圳", LiveWeather: "多云", Temperature: 24}
	curr := AlertSnapshot{City: "深圳", LiveWeather: "小雨", Temperature: 22}
	alerts := DetectAlerts(true, prev, curr, cfg, now, last)
	if len(alerts) != 1 || alerts[0].Kind != KindPrecipLive {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestDetectAlertsNoAlertOnFirstSnapshot(t *testing.T) {
	curr := AlertSnapshot{City: "深圳", LiveWeather: "小雨", Temperature: 20}
	alerts := DetectAlerts(false, AlertSnapshot{}, curr, AlertDetectConfig{}, time.Now(), map[AlertKind]time.Time{})
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts on first snapshot, got %+v", alerts)
	}
}

func TestDetectAlertsPrecipForecast(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := AlertDetectConfig{Cooldown: 3 * time.Hour}
	prev := AlertSnapshot{City: "上海", LiveWeather: "晴", DayWeather: "多云", NightWeather: "多云", Temperature: 20}
	curr := AlertSnapshot{City: "上海", LiveWeather: "晴", DayWeather: "小雨", NightWeather: "多云", Temperature: 20}
	alerts := DetectAlerts(true, prev, curr, cfg, now, map[AlertKind]time.Time{})
	var found bool
	for _, a := range alerts {
		if a.Kind == KindPrecipForecast {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected precip_forecast, got %+v", alerts)
	}
}

func TestDetectAlertsCoolDrop(t *testing.T) {
	cfg := AlertDetectConfig{CoolDropC: 5, Cooldown: time.Hour}
	prev := AlertSnapshot{City: "北京", LiveWeather: "晴", Temperature: 18}
	curr := AlertSnapshot{City: "北京", LiveWeather: "晴", Temperature: 12}
	alerts := DetectAlerts(true, prev, curr, cfg, time.Now(), map[AlertKind]time.Time{})
	if len(alerts) != 1 || alerts[0].Kind != KindCoolDrop {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestDetectAlertsCooldown(t *testing.T) {
	cfg := AlertDetectConfig{Cooldown: 3 * time.Hour}
	now := time.Unix(1_700_000_000, 0)
	last := map[AlertKind]time.Time{KindPrecipLive: now.Add(-time.Hour)}
	prev := AlertSnapshot{City: "深圳", LiveWeather: "多云", Temperature: 24}
	curr := AlertSnapshot{City: "深圳", LiveWeather: "大雨", Temperature: 20}
	alerts := DetectAlerts(true, prev, curr, cfg, now, last)
	if len(alerts) != 0 {
		t.Fatalf("cooldown should suppress, got %+v", alerts)
	}
}

func TestAlertNotificationIDStableInBucket(t *testing.T) {
	cooldown := 3 * time.Hour
	t0 := time.Unix(10_800, 0)
	t1 := t0.Add(30 * time.Minute)
	id0 := AlertNotificationID("深圳市", KindCoolDrop, t0, cooldown)
	id1 := AlertNotificationID("深圳市", KindCoolDrop, t1, cooldown)
	if id0 != id1 {
		t.Fatalf("ids differ in same bucket: %q vs %q", id0, id1)
	}
}
