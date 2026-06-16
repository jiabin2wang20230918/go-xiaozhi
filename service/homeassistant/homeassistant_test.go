package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestGetStateFormatsPythonResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states/media_player.living" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"state":"playing","attributes":{"media_title":"歌名","volume_level":0.5,"color_temp_kelvin":3000,"rgb_color":[1,2,3],"brightness":80}}`))
	}))
	defer server.Close()

	client := New(config.HomeAssistantConf{BaseURL: server.URL, APIKey: "token"})
	client.HTTPClient = server.Client()

	text, err := client.GetState(context.Background(), "media_player.living")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	for _, want := range []string{
		"设备状态:playing ",
		"正在播放的是:歌名 ",
		"音量是:0.5 ",
		"色温是:3000 ",
		"rgb颜色是:[1,2,3] ",
		"亮度是:80 ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("state response missing %q: %s", want, text)
		}
	}
}

func TestSetStateMapsPythonActions(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/light/turn_on" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(config.HomeAssistantConf{BaseURL: server.URL, APIKey: "token"})
	client.HTTPClient = server.Client()

	text, err := client.SetState(context.Background(), "light.kitchen", map[string]any{
		"type":  "brightness_value",
		"input": float64(80),
	})
	if err != nil {
		t.Fatalf("set state: %v", err)
	}
	if text != "亮度已调整到80" {
		t.Fatalf("unexpected set response: %q", text)
	}
	if body["entity_id"] != "light.kitchen" || body["brightness_pct"] != float64(80) {
		t.Fatalf("unexpected request body: %+v", body)
	}
}

func TestSetStateVolumeSetConvertsPercent(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/media_player/volume_set" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(config.HomeAssistantConf{BaseURL: server.URL})
	client.HTTPClient = server.Client()

	text, err := client.SetState(context.Background(), "media_player.room", map[string]any{
		"type":  "volume_set",
		"input": float64(60),
	})
	if err != nil {
		t.Fatalf("set volume: %v", err)
	}
	if text != "音量已调整到60" {
		t.Fatalf("unexpected volume response: %q", text)
	}
	if body["volume_level"] != 0.6 {
		t.Fatalf("volume_level = %+v", body["volume_level"])
	}
}

func TestPlayMusicUsesMusicAssistantEndpoint(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/music_assistant/play_media" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(config.HomeAssistantConf{BaseURL: server.URL})
	client.HTTPClient = server.Client()

	text, err := client.PlayMusic(context.Background(), "media_player.room", "周杰伦")
	if err != nil {
		t.Fatalf("play music: %v", err)
	}
	if text != "正在播放周杰伦的音乐" {
		t.Fatalf("unexpected play response: %q", text)
	}
	if body["entity_id"] != "media_player.room" || body["media_id"] != "周杰伦" {
		t.Fatalf("unexpected request body: %+v", body)
	}
}
