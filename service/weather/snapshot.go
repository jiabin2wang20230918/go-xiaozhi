package weather

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// WeatherSnapshot 是结构化天气快照（docs 方案 §7.3 的 weather 服务端事件载荷）。
type WeatherSnapshot struct {
	City        string `json:"city"`
	Weather     string `json:"weather"`
	Temperature int    `json:"temperature"`
	FeelsLike   int    `json:"feels_like,omitempty"`
	Humidity    int    `json:"humidity,omitempty"`
	Wind        string `json:"wind,omitempty"`
	ReportTime  string `json:"report_time,omitempty"`
}

const defaultSnapshotTTL = 30 * time.Minute

// ClearSnapshotCache drops in-memory snapshot cache (for tests).
func (c *Client) ClearSnapshotCache() {
	c.cacheMu.Lock()
	c.cache = nil
	c.cacheMu.Unlock()
}

// QuerySnapshot 查询某城市的实时天气并返回结构化快照。
// 复用 fetchCity/fetchWeather（与 Query 同一条高德接口路径），
// 按城市缓存 30 分钟，避免 Dashboard 轮询打满 API 配额。
// includeForecast 为 true 时额外拉取当日 day/night 预报（供 weather_alert 检测）。
func (c *Client) QuerySnapshot(ctx context.Context, location string, includeForecast bool) (*WeatherSnapshot, *AlertSnapshot, error) {
	location = strings.TrimSpace(location)
	c.cacheMu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*snapshotEntry)
	}
	cacheKey := snapshotCacheKey(location, includeForecast)
	cached, ok := c.cache[cacheKey]
	if ok && time.Since(cached.at) < defaultSnapshotTTL {
		c.cacheMu.Unlock()
		snap := *cached.snapshot
		var alertSnap *AlertSnapshot
		if cached.alertSnap != nil {
			copy := *cached.alertSnap
			alertSnap = &copy
		}
		return &snap, alertSnap, nil
	}
	c.cacheMu.Unlock()

	snap, alertSnap, err := c.fetchSnapshot(ctx, location, includeForecast)
	if err != nil {
		return nil, nil, err
	}
	c.cacheMu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*snapshotEntry)
	}
	c.cache[cacheKey] = &snapshotEntry{snapshot: snap, alertSnap: alertSnap, at: time.Now()}
	c.cacheMu.Unlock()
	return snap, alertSnap, nil
}

func snapshotCacheKey(location string, includeForecast bool) string {
	if includeForecast {
		return location + "|forecast"
	}
	return location
}

type snapshotEntry struct {
	snapshot  *WeatherSnapshot
	alertSnap *AlertSnapshot
	at        time.Time
}

func (c *Client) fetchSnapshot(ctx context.Context, location string, includeForecast bool) (*WeatherSnapshot, *AlertSnapshot, error) {
	conf := c.Config
	conf.Normalize()
	location = strings.TrimSpace(location)
	if location == "" {
		location = conf.DefaultLocation
	}
	city, ok, err := c.fetchCity(ctx, conf, location)
	if err != nil || !ok {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, &LocationError{Location: location}
	}

	var forecastResp weatherResponse
	var forecastErr error
	if includeForecast {
		forecastResp, forecastErr = c.fetchWeather(ctx, conf, city.Adcode)
	}

	buildAlertFromCast := func(cast forecastWeather) *AlertSnapshot {
		return &AlertSnapshot{
			City:         city.Name,
			LiveWeather:  "",
			DayWeather:   cast.DayWeather,
			NightWeather: cast.NightWeather,
		}
	}

	// 高德的实时天气必须用 extensions=base（"all" 只返回 forecasts）。
	// 实时接口失败或为空时退回当日预报，保证 Dashboard 仍有数据。
	resp, liveErr := c.fetchWeatherWithExtensions(ctx, conf, city.Adcode, "base")
	if liveErr == nil && len(resp.Lives) > 0 {
		live := resp.Lives[0]
		snap := &WeatherSnapshot{
			City:        city.Name,
			Weather:     fallback(live.Weather, "未知"),
			Temperature: atoi(live.Temperature),
			Humidity:    atoi(live.Humidity),
			ReportTime:  live.ReportTime,
		}
		if wind := joinWind(live.WindDirection, live.WindPower); wind != "" {
			snap.Wind = wind
		}
		var alertSnap *AlertSnapshot
		if includeForecast {
			alertSnap = &AlertSnapshot{
				City:         city.Name,
				LiveWeather:  snap.Weather,
				Temperature:  snap.Temperature,
			}
			if forecastErr == nil && len(forecastResp.Forecasts) > 0 && len(forecastResp.Forecasts[0].Casts) > 0 {
				cast := forecastResp.Forecasts[0].Casts[0]
				alertSnap.DayWeather = cast.DayWeather
				alertSnap.NightWeather = cast.NightWeather
			}
		}
		return snap, alertSnap, nil
	}

	// 回退：实时天气不可用时用当日预报，保证 Dashboard 仍有数据。
	if len(forecastResp.Forecasts) == 0 {
		var err error
		forecastResp, err = c.fetchWeather(ctx, conf, city.Adcode)
		if err != nil {
			if liveErr != nil {
				return nil, nil, liveErr
			}
			return nil, nil, err
		}
	}
	if len(forecastResp.Forecasts) == 0 || len(forecastResp.Forecasts[0].Casts) == 0 {
		return nil, nil, &LocationError{Location: location}
	}
	cast := forecastResp.Forecasts[0].Casts[0]
	snap := &WeatherSnapshot{
		City:        city.Name,
		Weather:     fallback(cast.DayWeather, fallback(cast.NightWeather, "未知")),
		Temperature: atoi(cast.DayTemp),
		ReportTime:  forecastResp.Forecasts[0].ReportTime,
	}
	if wind := joinWind(cast.DayWind, ""); wind != "" {
		snap.Wind = wind
	}
	var alertSnap *AlertSnapshot
	if includeForecast {
		alertSnap = buildAlertFromCast(cast)
		alertSnap.LiveWeather = snap.Weather
		alertSnap.Temperature = snap.Temperature
	}
	return snap, alertSnap, nil
}

// joinWind 拼装形如「东北风 2级」的风向风力描述；缺少任一部分时降级。
func joinWind(direction, power string) string {
	direction = strings.TrimSpace(direction)
	power = strings.TrimSpace(power)
	switch {
	case direction == "" && power == "":
		return ""
	case power == "":
		return direction
	case direction == "":
		return power + "级"
	default:
		return direction + " " + power + "级"
	}
}

// LocationError 表示城市定位失败（无 geocode 结果或无实时天气）。
type LocationError struct {
	Location string
}

func (e *LocationError) Error() string {
	return "未找到相关城市的实时天气: " + e.Location
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
