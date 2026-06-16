package xiaozhi

import (
	"bytes"
	"encoding/json"
	"io"
)

// ClientEventType 客户端事件
type ClientEventType string

const (
	ClientEventTypeRawText      ClientEventType = "raw_text"
	ClientEventTypeHello        ClientEventType = "hello"
	ClientEventTypeListen       ClientEventType = "listen"
	ClientEventTypeAppendBuffer ClientEventType = "append.buffer"
	ClientEventTypeAbort        ClientEventType = "abort"
	ClientEventTypeIot          ClientEventType = "iot"
	ClientEventTypeUnknown      ClientEventType = "unknown"
)

// ClientState 客户端监听状态
type ClientState string

const (
	ClientStateIdle         ClientState = "idle"
	ClientStateListenStart  ClientState = "start"
	ClientStateListenStop   ClientState = "stop"
	ClientStateListenDetect ClientState = "detect" // 用于客户端向服务器告知检测到唤醒词
)

// ClientMode 客户端识别模式
type ClientMode string

const (
	ClientModeAuto      ClientMode = "auto"
	ClientModeManual    ClientMode = "manual"
	ClientModelRealtime ClientMode = "realtime"
)

type ClientEvent interface {
	ClientEventType() ClientEventType
	GetAudioParams() *AudioParams
	GetSessionID() string
}

type ClientEventRawText struct {
	Text string
}

type ClientEventUnknown struct {
	Type ClientEventType
	Raw  []byte
}

type AudioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration"`
}

// ClientEventBase is the base struct for all client events.
type ClientEventBase struct {
	Type        ClientEventType `json:"type"`
	Version     int             `json:"version,omitempty"`
	Transport   string          `json:"transport,omitempty"` // websocket, rtc, iot
	SessionID   string          `json:"session_id,omitempty"`
	AudioParams *AudioParams    `json:"audio_params,omitempty"`
}

// ClientEventHello is the hello event.
type ClientEventHello struct {
	ClientEventBase
}

// {'type': 'hello', 'version': 1, 'transport': 'websocket', 'audio_params': {'format': 'opus', 'sample_rate': 16000, 'channels': 1, 'frame_duration': 20}}

// ClientEventListen is the listen event.
type ClientEventListen struct {
	ClientEventBase
	State ClientState `json:"state"`
	Mode  ClientMode  `json:"mode"`
	Text  string      `json:"text,omitempty"`
}

type ClientEventAppendBuffer struct {
	ClientEventBase
	Bytes []byte `json:"bytes"` // opus编码的二进制数据
}

// {'type': 'listen','state': 'start','mode': 'auto'}   然后客户端开始发送二进制的音频数据
// {'type': 'listen','state':'stop'}   客户端停止发送二进制的音频数据
// {'type': 'listen','state':'detect'}   客户端检测到唤醒词，发送二进制的音频数据

// ClientEventAbort is the abort event.
type ClientEventAbort struct {
	ClientEventBase
	Reason string `json:"reason"`
}

type ClientEventIot struct {
	ClientEventBase
	Descriptors []IotDescriptor `json:"descriptors,omitempty"`
	States      []IotState      `json:"states,omitempty"`
	Data        string          `json:"data,omitempty"`
	Raw         map[string]any  `json:"-"`
}

type IotDescriptor struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Properties  map[string]IotPropertySchema `json:"properties"`
	Methods     map[string]IotMethodSchema   `json:"methods"`
}

func (d *IotDescriptor) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Properties  json.RawMessage `json:"properties"`
		Methods     json.RawMessage `json:"methods"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	properties, err := unmarshalIotProperties(raw.Properties)
	if err != nil {
		return err
	}
	methods, err := unmarshalIotMethods(raw.Methods)
	if err != nil {
		return err
	}
	d.Name = raw.Name
	d.Description = raw.Description
	d.Properties = properties
	d.Methods = methods
	return nil
}

type IotPropertySchema struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type IotMethodSchema struct {
	Name        string                       `json:"name,omitempty"`
	Description string                       `json:"description"`
	Parameters  map[string]IotPropertySchema `json:"parameters"`
}

func unmarshalIotProperties(data json.RawMessage) (map[string]IotPropertySchema, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var byName map[string]IotPropertySchema
	if err := json.Unmarshal(data, &byName); err == nil {
		for name, schema := range byName {
			if schema.Name == "" {
				schema.Name = name
				byName[name] = schema
			}
		}
		return byName, nil
	}
	var list []IotPropertySchema
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	byName = make(map[string]IotPropertySchema, len(list))
	for _, schema := range list {
		if schema.Name == "" {
			continue
		}
		byName[schema.Name] = schema
	}
	return byName, nil
}

func unmarshalIotMethods(data json.RawMessage) (map[string]IotMethodSchema, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var byName map[string]IotMethodSchema
	if err := json.Unmarshal(data, &byName); err == nil {
		for name, schema := range byName {
			if schema.Name == "" {
				schema.Name = name
				byName[name] = schema
			}
		}
		return byName, nil
	}
	var list []IotMethodSchema
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	byName = make(map[string]IotMethodSchema, len(list))
	for _, schema := range list {
		if schema.Name == "" {
			continue
		}
		byName[schema.Name] = schema
	}
	return byName, nil
}

type IotState struct {
	Name  string         `json:"name"`
	State map[string]any `json:"state"`
}

func (e *ClientEventBase) ClientEventType() ClientEventType {
	return e.Type
}
func (e *ClientEventBase) GetAudioParams() *AudioParams {
	return e.AudioParams
}

func (e *ClientEventBase) GetSessionID() string {
	return e.SessionID
}

func (e *ClientEventRawText) ClientEventType() ClientEventType {
	return ClientEventTypeRawText
}

func (e *ClientEventRawText) GetAudioParams() *AudioParams {
	return nil
}

func (e *ClientEventRawText) GetSessionID() string {
	return ""
}

func (e *ClientEventUnknown) ClientEventType() ClientEventType {
	return ClientEventTypeUnknown
}

func (e *ClientEventUnknown) GetAudioParams() *AudioParams {
	return nil
}

func (e *ClientEventUnknown) GetSessionID() string {
	return ""
}

func (e *ClientEventHello) ClientEventType() ClientEventType {
	return ClientEventTypeHello
}

func (e *ClientEventHello) GetAudioParams() *AudioParams {
	return e.AudioParams
}

func (e *ClientEventAppendBuffer) ClientEventType() ClientEventType {
	return ClientEventTypeAppendBuffer
}

func (e *ClientEventAppendBuffer) GetAudioParams() *AudioParams {
	return e.AudioParams
}
func (e *ClientEventHello) GetSessionID() string {
	return e.SessionID
}

func (e *ClientEventListen) ClientEventType() ClientEventType {
	return ClientEventTypeListen
}

func (e *ClientEventListen) GetAudioParams() *AudioParams {
	return e.AudioParams
}

func (e *ClientEventListen) GetSessionID() string {
	return e.SessionID
}

func (e *ClientEventAbort) ClientEventType() ClientEventType {
	return ClientEventTypeAbort
}

func (e *ClientEventAbort) GetAudioParams() *AudioParams {
	return e.AudioParams
}

func (e *ClientEventAbort) GetSessionID() string {
	return e.SessionID
}

func (e *ClientEventIot) ClientEventType() ClientEventType {
	return ClientEventTypeIot
}

func (e *ClientEventIot) GetAudioParams() *AudioParams {
	return e.AudioParams
}

func (e *ClientEventIot) GetSessionID() string {
	return e.SessionID
}

type ClientEventInterface interface {
	ClientEventHello | ClientEventListen | ClientEventAbort | ClientEventIot
}

func unmarshalClientEvent[T ClientEventInterface](data []byte) (*T, error) {
	var t T
	err := json.Unmarshal(data, &t)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// MarshalClientEvent marshals the client event to JSON.
func MarshalClientEvent(event ClientEvent) ([]byte, error) {
	return json.Marshal(event)
}

// UnmarshalClientEvent Unmarshal the server event from the given JSON data.
func UnmarshalClientEvent(data []byte) (ClientEvent, error) {
	if isJSONInteger(data) {
		return &ClientEventRawText{Text: string(data)}, nil
	}
	if !isJSONObject(data) {
		if json.Valid(bytes.TrimSpace(data)) {
			return &ClientEventUnknown{
				Raw: append([]byte(nil), data...),
			}, nil
		}
		return &ClientEventRawText{Text: string(data)}, nil
	}
	var eventType struct {
		Type ClientEventType `json:"type"`
	}
	err := json.Unmarshal(data, &eventType)
	if err != nil {
		return &ClientEventRawText{Text: string(data)}, nil
	}
	switch eventType.Type {
	case ClientEventTypeHello:
		return unmarshalClientEvent[ClientEventHello](data)
	case ClientEventTypeListen:
		return unmarshalClientEvent[ClientEventListen](data)
	case ClientEventTypeAbort:
		return unmarshalClientEvent[ClientEventAbort](data)
	case ClientEventTypeIot:
		ev, err := unmarshalClientEvent[ClientEventIot](data)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(data, &ev.Raw)
		return ev, nil
	default:
		return &ClientEventUnknown{
			Type: eventType.Type,
			Raw:  append([]byte(nil), data...),
		}, nil
	}
}

func isJSONInteger(data []byte) bool {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return false
	}
	var n int64
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&n); err != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func isJSONObject(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{'
}

func UnmarshalClientBinEvent(data []byte) (ClientEvent, error) {
	return &ClientEventAppendBuffer{
		ClientEventBase: ClientEventBase{
			Type: ClientEventTypeAppendBuffer,
		},
		Bytes: data,
	}, nil
}
