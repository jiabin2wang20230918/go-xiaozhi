package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/92.0.4515.107 Safari/537.36"

type Client struct {
	Config     config.GetWeatherConf
	HTTPClient *http.Client
}

type City struct {
	Name   string
	Adcode string
}

func New(conf config.GetWeatherConf) *Client {
	conf.Normalize()
	return &Client{Config: conf}
}

func (c *Client) Query(ctx context.Context, location string, lang string) (string, error) {
	conf := c.Config
	conf.Normalize()
	if strings.TrimSpace(lang) == "" {
		lang = "zh_CN"
	}
	location = strings.TrimSpace(location)
	if location == "" {
		location = conf.DefaultLocation
	}
	city, ok, err := c.fetchCity(ctx, conf, location)
	if err != nil || !ok {
		return "未找到相关的城市: " + location + "，请确认地点是否正确", nil
	}
	if strings.TrimSpace(city.Adcode) == "" {
		return "无法获取城市编码: " + location, nil
	}
	weather, err := c.fetchWeather(ctx, conf, city.Adcode)
	if err != nil {
		return err.Error(), nil
	}
	return buildReport(lang, city.Name, weather), nil
}

func (c *Client) fetchCity(ctx context.Context, conf config.GetWeatherConf, location string) (City, bool, error) {
	if invalidAPIKey(conf.APIKey) {
		return City{}, false, fmt.Errorf("高德地图API密钥未配置或无效")
	}
	endpoint, err := url.JoinPath(conf.BaseURL, "/v3/geocode/geo")
	if err != nil {
		return City{}, false, err
	}
	values := url.Values{}
	values.Set("address", location)
	values.Set("key", conf.APIKey)
	body, err := c.getJSON(ctx, endpoint+"?"+values.Encode())
	if err != nil {
		return City{}, false, err
	}
	var response struct {
		Status   string `json:"status"`
		Geocodes []struct {
			City   any    `json:"city"`
			Adcode string `json:"adcode"`
		} `json:"geocodes"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return City{}, false, err
	}
	if response.Status != "1" || len(response.Geocodes) == 0 {
		return City{}, false, nil
	}
	geo := response.Geocodes[0]
	name := strings.TrimSpace(cityName(geo.City))
	if name == "" {
		name = location
	}
	return City{Name: name, Adcode: strings.TrimSpace(geo.Adcode)}, true, nil
}

func cityName(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func (c *Client) fetchWeather(ctx context.Context, conf config.GetWeatherConf, adcode string) (weatherResponse, error) {
	endpoint, err := url.JoinPath(conf.BaseURL, "/v3/weather/weatherInfo")
	if err != nil {
		return weatherResponse{}, err
	}
	values := url.Values{}
	values.Set("city", adcode)
	values.Set("key", conf.APIKey)
	values.Set("extensions", "all")
	body, err := c.getJSON(ctx, endpoint+"?"+values.Encode())
	if err != nil {
		return weatherResponse{}, fmt.Errorf("网络错误: %s", err)
	}
	var response weatherResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return weatherResponse{}, fmt.Errorf("数据解析错误: %s", err)
	}
	if response.Status != "1" {
		return weatherResponse{}, fmt.Errorf("天气API错误: %s", response.Info)
	}
	if len(response.Lives) == 0 && len(response.Forecasts) == 0 {
		return weatherResponse{}, fmt.Errorf("天气API返回的数据结构不正确")
	}
	return response, nil
}

func (c *Client) getJSON(ctx context.Context, requestURL string) ([]byte, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取天气信息失败")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

type weatherResponse struct {
	Status    string          `json:"status"`
	Info      string          `json:"info"`
	Lives     []liveWeather   `json:"lives"`
	Forecasts []forecastGroup `json:"forecasts"`
}

type liveWeather struct {
	Weather       string `json:"weather"`
	Temperature   string `json:"temperature"`
	Humidity      string `json:"humidity"`
	WindDirection string `json:"winddirection"`
	WindPower     string `json:"windpower"`
	ReportTime    string `json:"reporttime"`
}

type forecastGroup struct {
	Casts []forecastWeather `json:"casts"`
}

type forecastWeather struct {
	Date         string `json:"date"`
	Week         string `json:"week"`
	DayWeather   string `json:"dayweather"`
	NightWeather string `json:"nightweather"`
	DayTemp      string `json:"daytemp"`
	NightTemp    string `json:"nighttemp"`
	DayWind      string `json:"daywind"`
	NightWind    string `json:"nightwind"`
}

func buildReport(lang string, city string, data weatherResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "根据下列数据，用%s回应用户的查询天气请求：\n%s天气:\n", lang, city)
	if len(data.Lives) > 0 {
		live := data.Lives[0]
		fmt.Fprintf(&b, "实时天气: %s\n", fallback(live.Weather, "未知"))
		fmt.Fprintf(&b, "温度: %s°C\n", fallback(live.Temperature, "未知"))
		fmt.Fprintf(&b, "湿度: %s%%\n", fallback(live.Humidity, "未知"))
		fmt.Fprintf(&b, "风向: %s\n", fallback(live.WindDirection, "未知"))
		fmt.Fprintf(&b, "风力: %s级\n", fallback(live.WindPower, "未知"))
		fmt.Fprintf(&b, "更新时间: %s\n", fallback(live.ReportTime, "未知"))
	} else if len(data.Forecasts) > 0 && len(data.Forecasts[0].Casts) > 0 {
		cast := data.Forecasts[0].Casts[0]
		fmt.Fprintf(&b, "日期: %s (星期%s)\n", cast.Date, fallback(cast.Week, "未知"))
		fmt.Fprintf(&b, "白天天气: %s，温度: %s°C\n", fallback(cast.DayWeather, "未知"), fallback(cast.DayTemp, "未知"))
		fmt.Fprintf(&b, "夜间天气: %s，温度: %s°C\n", fallback(cast.NightWeather, "未知"), fallback(cast.NightTemp, "未知"))
		fmt.Fprintf(&b, "风向: %s，夜间风向: %s\n", fallback(cast.DayWind, "未知"), fallback(cast.NightWind, "未知"))
	}
	b.WriteString("(确保只报告指定单日的天气情况，除非未来会出现异常天气；或者用户明确要求想要了解多日天气，如果未指定，默认报告今天的天气。参数为0的值不需要报告给用户，每次都报告体感温度，根据语境选择合适的参数内容告知用户，并对参数给出相应评价)")
	return b.String()
}

func invalidAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	return key == "" || key == "your_amap_api_key_here"
}

func fallback(value string, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}
