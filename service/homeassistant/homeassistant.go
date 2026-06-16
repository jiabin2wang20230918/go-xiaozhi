package homeassistant

import (
	"bytes"
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

type Client struct {
	Config     config.HomeAssistantConf
	HTTPClient *http.Client
}

func New(conf config.HomeAssistantConf) *Client {
	return &Client{Config: conf}
}

func (c *Client) GetState(ctx context.Context, entityID string) (string, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return "切换失败，错误码: 404", nil
	}
	data, status, err := c.request(ctx, http.MethodGet, "/api/states/"+entityID, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return fmt.Sprintf("切换失败，错误码: %d", status), nil
	}
	var response struct {
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("设备状态:")
	b.WriteString(response.State)
	b.WriteString(" ")
	appendAttr := func(key string, prefix string) {
		if value, ok := response.Attributes[key]; ok {
			b.WriteString(prefix)
			b.WriteString(formatAny(value))
			b.WriteString(" ")
		}
	}
	appendAttr("media_title", "正在播放的是:")
	appendAttr("volume_level", "音量是:")
	appendAttr("color_temp_kelvin", "色温是:")
	appendAttr("rgb_color", "rgb颜色是:")
	appendAttr("brightness", "亮度是:")
	return b.String(), nil
}

func (c *Client) SetState(ctx context.Context, entityID string, state map[string]any) (string, error) {
	entityID = strings.TrimSpace(entityID)
	parts := strings.Split(entityID, ".")
	if len(parts) <= 1 || strings.TrimSpace(parts[0]) == "" {
		return "执行失败，错误的设备id", nil
	}
	domain := parts[0]
	actionType, _ := state["type"].(string)
	actionType = strings.TrimSpace(actionType)
	action, description, arg, value, ok := mapAction(domain, actionType, state)
	if !ok {
		return fmt.Sprintf("%s %s功能尚未支持", domain, actionType), nil
	}
	payload := map[string]any{"entity_id": entityID}
	if arg != "" {
		payload[arg] = value
	}
	_, status, err := c.request(ctx, http.MethodPost, "/api/services/"+domain+"/"+action, payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		return description, nil
	}
	return fmt.Sprintf("设置失败，错误码: %d", status), nil
}

func (c *Client) PlayMusic(ctx context.Context, entityID string, mediaContentID string) (string, error) {
	if strings.TrimSpace(mediaContentID) == "" {
		mediaContentID = "random"
	}
	payload := map[string]any{
		"entity_id": entityID,
		"media_id":  mediaContentID,
	}
	_, status, err := c.request(ctx, http.MethodPost, "/api/services/music_assistant/play_media", payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		return fmt.Sprintf("正在播放%s的音乐", mediaContentID), nil
	}
	return fmt.Sprintf("音乐播放失败，错误码: %d", status), nil
}

func mapAction(domain string, actionType string, state map[string]any) (action string, description string, arg string, value any, ok bool) {
	switch actionType {
	case "turn_on":
		description = "设备已打开"
		switch domain {
		case "cover":
			action = "open_cover"
		case "vacuum":
			action = "start"
		default:
			action = "turn_on"
		}
	case "turn_off":
		description = "设备已关闭"
		switch domain {
		case "cover":
			action = "close_cover"
		case "vacuum":
			action = "stop"
		default:
			action = "turn_off"
		}
	case "brightness_up":
		description, action, arg, value = "灯光已调亮", "turn_on", "brightness_step_pct", 10
	case "brightness_down":
		description, action, arg, value = "灯光已调暗", "turn_on", "brightness_step_pct", -10
	case "brightness_value":
		input := state["input"]
		description, action, arg, value = "亮度已调整到"+formatAny(input), "turn_on", "brightness_pct", input
	case "set_color":
		rgb := state["rgb_color"]
		description, action, arg, value = "颜色已调整到"+formatAny(rgb), "turn_on", "rgb_color", rgb
	case "set_kelvin":
		input := state["input"]
		description, action, arg, value = "色温已调整到"+formatAny(input)+"K", "turn_on", "kelvin", input
	case "volume_up":
		description, action = "音量已调大", "volume_up"
	case "volume_down":
		description, action = "音量已调小", "volume_down"
	case "volume_set":
		input := state["input"]
		description, action, arg = "音量已调整到"+formatAny(input), "volume_set", "volume_level"
		value = input
		if number, ok := numberValue(input); ok && number >= 1 {
			value = number / 100
		}
	case "volume_mute":
		description, action, arg, value = "设备已静音", "volume_mute", "is_volume_muted", state["is_muted"]
	case "pause":
		description, action = "设备已暂停", "pause"
		switch domain {
		case "media_player":
			action = "media_pause"
		case "cover":
			action = "stop_cover"
		case "vacuum":
			action = "pause"
		}
	case "continue":
		description = "设备已继续"
		switch domain {
		case "media_player":
			action = "media_play"
		case "vacuum":
			action = "start"
		default:
			action = actionType
		}
	default:
		return "", "", "", nil, false
	}
	return action, description, arg, value, true
}

func (c *Client) request(ctx context.Context, method string, path string, payload any) ([]byte, int, error) {
	base := strings.TrimRight(c.Config.BaseURL, "/")
	if base == "" {
		return nil, 0, fmt.Errorf("home assistant base_url is required")
	}
	requestURL, err := url.JoinPath(base, path)
	if err != nil {
		return nil, 0, err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.Config.APIKey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func formatAny(value any) string {
	if number, ok := numberValue(value); ok {
		if number == float64(int(number)) {
			return fmt.Sprint(int(number))
		}
		return fmt.Sprint(number)
	}
	data, err := json.Marshal(value)
	if err == nil {
		text := string(data)
		if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
			return text[1 : len(text)-1]
		}
		return text
	}
	return fmt.Sprint(value)
}
