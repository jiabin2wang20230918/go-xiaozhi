package xiaozhi

import (
	"encoding/json"
	"testing"
)

func TestServerEventHelloJSONMatchesPythonProtocol(t *testing.T) {
	hello := &ServerEventHello{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeHello,
			SessionId: "session-1",
		},
		Version:   1,
		Transport: "websocket",
		AudioParams: AudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	encoded, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	want := `{"type":"hello","session_id":"session-1","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`
	if string(encoded) != want {
		t.Fatalf("hello json = %s, want %s", encoded, want)
	}
}

func TestServerEventTTSJSONMatchesPythonProtocol(t *testing.T) {
	event := &ServerEventTTS{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeTTS,
			SessionId: "session-1",
		},
		State: ServerTTSStateStart,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal tts start: %v", err)
	}
	if string(encoded) != `{"type":"tts","session_id":"session-1","state":"start"}` {
		t.Fatalf("tts start json = %s", encoded)
	}

	event.State = ServerTTSStateSentenceStart
	event.Text = "你好"
	encoded, err = json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal tts sentence_start: %v", err)
	}
	if string(encoded) != `{"type":"tts","session_id":"session-1","state":"sentence_start","text":"你好"}` {
		t.Fatalf("tts sentence_start json = %s", encoded)
	}
}

func TestServerEventHelloIncludesServerTimeWhenSet(t *testing.T) {
	hello := &ServerEventHello{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeHello,
			SessionId: "session-1",
		},
		Version:     1,
		Transport:   "websocket",
		AudioParams: AudioParams{Format: "opus", SampleRate: 16000, Channels: 1, FrameDuration: 60},
		ServerTime:  1793334000,
	}
	encoded, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	want := `{"type":"hello","session_id":"session-1","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60},"server_time":1793334000}`
	if string(encoded) != want {
		t.Fatalf("hello json = %s, want %s", encoded, want)
	}
}

func TestServerEventNotificationJSONMatchesProtocol(t *testing.T) {
	event := &ServerEventNotification{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeNotification,
			SessionId: "session-1",
		},
		ID:             "reminder-20261030-1500",
		Category:       NotificationCategoryReminder,
		Title:          "项目会议",
		Content:        "15分钟后开始项目会议。",
		TriggerAt:      1793334000,
		Priority:       NotificationPriorityNormal,
		Speak:          true,
		DisplaySeconds: 15,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	want := `{"type":"notification","session_id":"session-1","id":"reminder-20261030-1500","category":"reminder","title":"项目会议","content":"15分钟后开始项目会议。","trigger_at":1793334000,"priority":"normal","speak":true,"display_seconds":15}`
	if string(encoded) != want {
		t.Fatalf("notification json = %s, want %s", encoded, want)
	}
}

func TestServerEventNotificationOmitsEmptyOptionalFields(t *testing.T) {
	event := &ServerEventNotification{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeNotification,
			SessionId: "session-1",
		},
		ID:       "system-1",
		Category: NotificationCategorySystem,
		Content:  "系统消息。",
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	want := `{"type":"notification","session_id":"session-1","id":"system-1","category":"system","content":"系统消息。","speak":false}`
	if string(encoded) != want {
		t.Fatalf("notification json = %s, want %s", encoded, want)
	}
}

func TestServerEventWeatherJSONMatchesProtocol(t *testing.T) {
	event := &ServerEventWeather{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeWeather,
			SessionId: "session-1",
		},
		City:        "深圳",
		Weather:     "多云",
		Temperature: 24,
		FeelsLike:   25,
		Humidity:    68,
		Wind:        "东北风 2级",
		ReportTime:  "2026-10-30T14:30:00+08:00",
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal weather: %v", err)
	}
	want := `{"type":"weather","session_id":"session-1","city":"深圳","weather":"多云","temperature":24,"feels_like":25,"humidity":68,"wind":"东北风 2级","report_time":"2026-10-30T14:30:00+08:00"}`
	if string(encoded) != want {
		t.Fatalf("weather json = %s, want %s", encoded, want)
	}

	minimal := &ServerEventWeather{
		ServerEventBase: ServerEventBase{Type: ServerEventTypeWeather, SessionId: "session-1"},
		City:            "深圳",
		Weather:         "多云",
		Temperature:     24,
	}
	encoded, err = json.Marshal(minimal)
	if err != nil {
		t.Fatalf("marshal minimal weather: %v", err)
	}
	wantMinimal := `{"type":"weather","session_id":"session-1","city":"深圳","weather":"多云","temperature":24}`
	if string(encoded) != wantMinimal {
		t.Fatalf("minimal weather json = %s, want %s", encoded, wantMinimal)
	}
}

func TestIsServerEventAcceptsNotificationAndWeather(t *testing.T) {
	notification := &ServerEventNotification{
		ServerEventBase: ServerEventBase{Type: ServerEventTypeNotification, SessionId: "s"},
		ID:              "n1",
		Category:        NotificationCategoryReminder,
		Content:         "c",
	}
	if _, ok := IsServerEvent(notification); !ok {
		t.Fatal("IsServerEvent should accept notification")
	}
	weather := &ServerEventWeather{
		ServerEventBase: ServerEventBase{Type: ServerEventTypeWeather, SessionId: "s"},
		City:            "深圳",
		Weather:         "多云",
		Temperature:     24,
	}
	if _, ok := IsServerEvent(weather); !ok {
		t.Fatal("IsServerEvent should accept weather")
	}
	if _, ok := IsServerEvent(&ServerEventSTT{ServerEventBase: ServerEventBase{Type: ServerEventTypeSTT}}); !ok {
		t.Fatal("IsServerEvent should still accept stt")
	}
	if _, ok := IsServerEvent("raw text"); ok {
		t.Fatal("IsServerEvent should reject non-server-event values")
	}
}

func TestServerEventSTTAndLLMJSONMatchPythonProtocol(t *testing.T) {
	stt := &ServerEventSTT{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeSTT,
			SessionId: "session-1",
		},
		Text: "你好",
	}
	encoded, err := json.Marshal(stt)
	if err != nil {
		t.Fatalf("marshal stt: %v", err)
	}
	if string(encoded) != `{"type":"stt","session_id":"session-1","text":"你好"}` {
		t.Fatalf("stt json = %s", encoded)
	}

	llm := &ServerEventLLM{
		ServerEventBase: ServerEventBase{
			Type:      ServerEventTypeLLM,
			SessionId: "session-1",
		},
		Text:    "😊",
		Emotion: "happy",
	}
	encoded, err = json.Marshal(llm)
	if err != nil {
		t.Fatalf("marshal llm: %v", err)
	}
	if string(encoded) != `{"type":"llm","session_id":"session-1","text":"😊","emotion":"happy"}` {
		t.Fatalf("llm json = %s", encoded)
	}
}
