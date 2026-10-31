package xiaozhi

import (
	"encoding/json"
)

type ServerEventType string

const (
	ServerEventTypeHello        ServerEventType = "hello"
	ServerEventTypeSTT          ServerEventType = "stt"
	ServerEventTypeLLM          ServerEventType = "llm"
	ServerEventTypeTTS          ServerEventType = "tts"
	ServerEventTypeIot          ServerEventType = "iot"
	ServerEventTypeNotification ServerEventType = "notification"
	ServerEventTypeWeather      ServerEventType = "weather"
)

// NotificationCategory 通知类别（见 docs 方案 §7.2）。
type NotificationCategory string

const (
	NotificationCategoryReminder     NotificationCategory = "reminder"
	NotificationCategoryWeatherAlert NotificationCategory = "weather_alert"
	NotificationCategorySystem       NotificationCategory = "system"
)

// NotificationPriority 通知优先级。
type NotificationPriority string

const (
	NotificationPriorityLow    NotificationPriority = "low"
	NotificationPriorityNormal NotificationPriority = "normal"
	NotificationPriorityHigh   NotificationPriority = "high"
)

type ServerTTSState string

const (
	ServerTTSStateStart         ServerTTSState = "start"
	ServerTTSStateStop          ServerTTSState = "stop"
	ServerTTSStateSentenceStart ServerTTSState = "sentence_start"
	ServerTTSStateSentenceEnd   ServerTTSState = "sentence_end"
)

type ServerEvent interface {
	GetType() ServerEventType
}

type ServerEventBase struct {
	Type      ServerEventType `json:"type"`
	SessionId string          `json:"session_id"`
}

type ServerEventHello struct {
	ServerEventBase
	Version     int         `json:"version"`
	Transport   string      `json:"transport"`
	AudioParams AudioParams `json:"audio_params"`
	// ServerTime 服务端当前时间（epoch 秒），供设备校准本地时钟；0 表示不携带。
	ServerTime int64 `json:"server_time,omitempty"`
}

func (e *ServerEventHello) GetType() ServerEventType {
	return ServerEventTypeHello
}

// {'type': 'hello', 'version': 1, 'transport': 'websocket', 'audio_params': {'format': 'opus', 'sample_rate': 24000, 'channels': 1, 'frame_duration': 20}, 'session_id': '9842a257'}

type ServerEventSTT struct {
	ServerEventBase
	Text string `json:"text"`
}

func (e *ServerEventSTT) GetType() ServerEventType {
	return ServerEventTypeSTT
}

// {'type': 'stt', 'text': 'are youOK。', 'session_id': '9842a257'}

type ServerEventLLM struct {
	ServerEventBase
	Text    string `json:"text"`
	Emotion string `json:"emotion"`
}

func (e *ServerEventLLM) GetType() ServerEventType {
	return ServerEventTypeLLM
}

// {'type': 'llm', 'text': '😊', 'emotion': 'happy', 'session_id': '9842a257'}

type ServerEventTTS struct {
	ServerEventBase
	State ServerTTSState `json:"state"`
	Text  string         `json:"text,omitempty"`
}

func (e *ServerEventTTS) GetType() ServerEventType {
	return ServerEventTypeTTS
}

// {'type': 'tts', 'state': 'start', 'session_id': '9842a257'}
// {'type': 'tts', 'state': 'stop', 'session_id': '9842a257'}
// {'type': 'tts', 'state': 'sentence_start', 'text': '有什么好玩的事吗？', 'session_id': '9842a257'}
// {'type': 'tts', 'state': 'sentence_end', 'text': '有什么好玩的事吗？', 'session_id': '9842a257'}

type ServerEventInterface interface {
	ServerEventHello | ServerEventSTT | ServerEventLLM | ServerEventTTS
}

type ServerEventIotCommand struct {
	Name       string         `json:"name"`
	Method     string         `json:"method"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type ServerEventIot struct {
	Type     ServerEventType         `json:"type"`
	Commands []ServerEventIotCommand `json:"commands,omitempty"`
}

func (e *ServerEventIot) GetType() ServerEventType {
	return ServerEventTypeIot
}

// ServerEventNotification 是服务端主动下发的通知/提醒（Grok Bot 经 /v1/notify 触发）。
// 提醒的 SoR 与调度在 Grok Bot，本事件只是投递载荷（见 docs 方案 §7.2）。
type ServerEventNotification struct {
	ServerEventBase
	ID             string               `json:"id"`
	Category       NotificationCategory `json:"category"`
	Title          string               `json:"title,omitempty"`
	Content        string               `json:"content"`
	TriggerAt      int64                `json:"trigger_at,omitempty"`
	Priority       NotificationPriority `json:"priority,omitempty"`
	Speak          bool                 `json:"speak"`
	DisplaySeconds int                  `json:"display_seconds,omitempty"`
}

func (e *ServerEventNotification) GetType() ServerEventType {
	return ServerEventTypeNotification
}

// {'type': 'notification', 'session_id': '...', 'id': 'reminder-20261030-1500', 'category': 'reminder', 'title': '项目会议', 'content': '15分钟后开始项目会议。', 'trigger_at': 1793334000, 'priority': 'normal', 'speak': true, 'display_seconds': 15}

// ServerEventWeather 是服务端下发的结构化天气快照（Dashboard 用）。
type ServerEventWeather struct {
	ServerEventBase
	City        string `json:"city"`
	Weather     string `json:"weather"`
	Temperature int    `json:"temperature"`
	FeelsLike   int    `json:"feels_like,omitempty"`
	Humidity    int    `json:"humidity,omitempty"`
	Wind        string `json:"wind,omitempty"`
	ReportTime  string `json:"report_time,omitempty"`
}

func (e *ServerEventWeather) GetType() ServerEventType {
	return ServerEventTypeWeather
}

// {'type': 'weather', 'session_id': '...', 'city': '深圳', 'weather': '多云', 'temperature': 24, 'feels_like': 25, 'humidity': 68, 'wind': '东北风 2级', 'report_time': '2026-10-30T14:30:00+08:00'}

func unmarshalServerEvent[T ServerEventInterface](data []byte) (*T, error) {
	var t T
	err := json.Unmarshal(data, &t)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func IsServerEvent(event any) (ServerEvent, bool) {
	ev, ok := event.(ServerEvent)
	if !ok {
		return nil, false
	}
	switch ev.GetType() {
	case ServerEventTypeHello:
		return event.(*ServerEventHello), true
	case ServerEventTypeSTT:
		return event.(*ServerEventSTT), true
	case ServerEventTypeLLM:
		return event.(*ServerEventLLM), true
	case ServerEventTypeTTS:
		return event.(*ServerEventTTS), true
	case ServerEventTypeIot:
		return event.(*ServerEventIot), true
	case ServerEventTypeNotification:
		return event.(*ServerEventNotification), true
	case ServerEventTypeWeather:
		return event.(*ServerEventWeather), true
	default:
		return nil, false
	}
}
