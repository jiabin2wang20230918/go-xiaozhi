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
