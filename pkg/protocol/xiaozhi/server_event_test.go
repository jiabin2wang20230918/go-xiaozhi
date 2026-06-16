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
