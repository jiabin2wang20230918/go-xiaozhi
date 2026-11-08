package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestQueryBuildsPythonCompatibleForecastPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/geo":
			if got := r.URL.Query().Get("address"); got != "杭州" {
				t.Fatalf("address = %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"city":"杭州市","adcode":"330100"}]}`))
		case "/v3/weather/weatherInfo":
			if got := r.URL.Query().Get("city"); got != "330100" {
				t.Fatalf("city = %q", got)
			}
			if got := r.URL.Query().Get("extensions"); got != "all" {
				t.Fatalf("extensions = %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"1","forecasts":[{"casts":[{"date":"2026-06-15","week":"1","dayweather":"多云","nightweather":"小雨","daytemp":"30","nighttemp":"22","daywind":"东","nightwind":"北"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.GetWeatherConf{
		APIKey:          "test-key",
		DefaultLocation: "广州",
		BaseURL:         server.URL,
	})
	client.HTTPClient = server.Client()

	text, err := client.Query(context.Background(), "杭州", "zh_CN")
	if err != nil {
		t.Fatalf("query weather: %v", err)
	}
	for _, want := range []string{
		"根据下列数据，用zh_CN回应用户的查询天气请求",
		"杭州市天气:",
		"日期: 2026-06-15 (星期1)",
		"白天天气: 多云，温度: 30°C",
		"夜间天气: 小雨，温度: 22°C",
		"风向: 东，夜间风向: 北",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("weather prompt missing %q: %s", want, text)
		}
	}
}

func TestQueryBuildsLiveWeatherPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/geo":
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"city":"广州市","adcode":"440100"}]}`))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status":"1","lives":[{"weather":"晴","temperature":"31","humidity":"60","winddirection":"南","windpower":"3","reporttime":"2026-06-15 08:00:00"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.GetWeatherConf{APIKey: "test-key", BaseURL: server.URL})
	client.HTTPClient = server.Client()

	text, err := client.Query(context.Background(), "", "zh_CN")
	if err != nil {
		t.Fatalf("query weather: %v", err)
	}
	for _, want := range []string{
		"广州市天气:",
		"实时天气: 晴",
		"温度: 31°C",
		"湿度: 60%",
		"更新时间: 2026-06-15 08:00:00",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("live weather prompt missing %q: %s", want, text)
		}
	}
}

func TestQuerySnapshotReturnsStructuredLiveWeather(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/v3/geocode/geo":
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"city":"深圳市","adcode":"440300"}]}`))
		case "/v3/weather/weatherInfo":
			// 实时天气必须走 extensions=base；"all" 只返回 forecasts（高德行为）。
			if got := r.URL.Query().Get("extensions"); got != "base" {
				t.Errorf("live snapshot must request extensions=base, got %q", got)
				_, _ = w.Write([]byte(`{"status":"1","forecasts":[{"casts":[{"date":"2026-10-30","dayweather":"多云","daytemp":"24","nighttemp":"20","daywind":"北"}]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"1","lives":[{"weather":"多云","temperature":"24","humidity":"68","winddirection":"东北","windpower":"2","reporttime":"2026-10-30 14:30:00"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.GetWeatherConf{APIKey: "test-key", BaseURL: server.URL})
	client.HTTPClient = server.Client()

	snap, _, err := client.QuerySnapshot(context.Background(), "深圳", false)
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if snap.City != "深圳市" || snap.Weather != "多云" || snap.Temperature != 24 || snap.Humidity != 68 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Wind != "东北 2级" {
		t.Fatalf("wind got %q", snap.Wind)
	}
	if snap.ReportTime != "2026-10-30 14:30:00" {
		t.Fatalf("report_time got %q", snap.ReportTime)
	}

	// 30 分钟缓存：重复查询不再命中 HTTP。
	snap2, _, err := client.QuerySnapshot(context.Background(), "深圳", false)
	if err != nil {
		t.Fatalf("cached query: %v", err)
	}
	if snap2.Temperature != 24 {
		t.Fatalf("cached snapshot changed: %+v", snap2)
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls (geocode+weather) due to cache, got %d", calls)
	}

	// 不同城市走独立缓存。
	if _, _, err := client.QuerySnapshot(context.Background(), "深圳", false); err != nil {
		t.Fatalf("repeat query: %v", err)
	}
}

func TestQuerySnapshotUnknownLocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/geo":
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.GetWeatherConf{APIKey: "test-key", BaseURL: server.URL})
	client.HTTPClient = server.Client()
	if _, _, err := client.QuerySnapshot(context.Background(), "不存在市", false); err == nil {
		t.Fatal("expected error for unknown location")
	}
}

func TestQueryInvalidAPIKeyMatchesPythonFailure(t *testing.T) {
	client := New(config.GetWeatherConf{APIKey: "your_amap_api_key_here", DefaultLocation: "广州"})
	text, err := client.Query(context.Background(), "", "zh_CN")
	if err != nil {
		t.Fatalf("query weather: %v", err)
	}
	if text != "未找到相关的城市: 广州，请确认地点是否正确" {
		t.Fatalf("unexpected invalid key response: %q", text)
	}
}

func TestQuerySnapshotFallsBackToForecastWhenLiveUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/geo":
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"city":"深圳市","adcode":"440300"}]}`))
		case "/v3/weather/weatherInfo":
			if r.URL.Query().Get("extensions") == "base" {
				// 实时接口无 lives（例如该时段不可用）。
				_, _ = w.Write([]byte(`{"status":"1","count":"0","lives":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"1","forecasts":[{"city":"深圳市","reporttime":"2026-10-30 22:30:43","casts":[{"date":"2026-10-30","week":"5","dayweather":"多云","nightweather":"晴","daytemp":"31","nighttemp":"23","daywind":"北","nightwind":"东北"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.GetWeatherConf{APIKey: "test-key", BaseURL: server.URL})
	client.HTTPClient = server.Client()

	snap, _, err := client.QuerySnapshot(context.Background(), "深圳", false)
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if snap.City != "深圳市" || snap.Weather != "多云" || snap.Temperature != 31 {
		t.Fatalf("forecast fallback snapshot: %+v", snap)
	}
	if snap.Wind != "北" {
		t.Fatalf("wind got %q want 北", snap.Wind)
	}
	if snap.ReportTime != "2026-10-30 22:30:43" {
		t.Fatalf("report_time got %q", snap.ReportTime)
	}
	if snap.FeelsLike != 0 || snap.Humidity != 0 {
		t.Fatalf("forecast fallback must not invent humidity/feels_like: %+v", snap)
	}
}

func TestJoinWind(t *testing.T) {
	cases := map[[2]string]string{
		{"东北", "2"}:    "东北 2级",
		{"东北", ""}:     "东北",
		{"", "2"}:      "2级",
		{"", ""}:       "",
		{" 东 ", " 3 "}: "东 3级",
	}
	for in, want := range cases {
		if got := joinWind(in[0], in[1]); got != want {
			t.Fatalf("joinWind(%q,%q) = %q want %q", in[0], in[1], got, want)
		}
	}
}
