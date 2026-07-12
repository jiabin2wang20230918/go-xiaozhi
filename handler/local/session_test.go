package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
	xiaozhiapi "github.com/xdimtech/go-xiaozhi/pkg/protocol/xiaozhi"
	"github.com/xdimtech/go-xiaozhi/service/homeassistant"
	"github.com/xdimtech/go-xiaozhi/service/mcp"
	"github.com/xdimtech/go-xiaozhi/service/news"
	"github.com/xdimtech/go-xiaozhi/service/search"
	"github.com/xdimtech/go-xiaozhi/service/voice"
	"github.com/xdimtech/go-xiaozhi/service/weather"
	"gopkg.in/hraban/opus.v2"
	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_XIAOZHI_HANDLER_MCP_HELPER") == "1" {
		runHandlerMCPHelper()
		return
	}
	dir, err := os.MkdirTemp("", "go-xiaozhi-handler-local-test-*")
	if err != nil {
		panic(err)
	}
	defaults := *config.Get()
	defaults.ASR.OutputDir = dir
	defaults.ASR.DeleteAudio = true
	restore := config.Replace(defaults)
	code := m.Run()
	restore()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestNewHandlerSendsHello(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{DeviceID: "device-1", ClientID: "client-1"})
	t.Cleanup(func() { _ = h.Close(context.Background()) })

	event := <-h.Recv(context.Background())
	hello, ok := event.(*xiaozhiapi.ServerEventHello)
	if !ok {
		t.Fatalf("expected hello event, got %T", event)
	}
	if hello.Type != xiaozhiapi.ServerEventTypeHello {
		t.Fatalf("unexpected event type: %s", hello.Type)
	}
	if hello.SessionId == "" {
		t.Fatal("session_id must be set")
	}
	if hello.Version != 1 {
		t.Fatalf("expected hello version 1, got %d", hello.Version)
	}
	if hello.Transport == "" || hello.AudioParams.Format == "" {
		t.Fatalf("missing protocol fields: %+v", hello)
	}
	encoded, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if !json.Valid(encoded) || !containsJSONField(encoded, `"version":1`) {
		t.Fatalf("hello json must include version: %s", encoded)
	}
	if h.client.DeviceID != "device-1" || h.client.ClientID != "client-1" {
		t.Fatalf("client metadata not preserved: %+v", h.client)
	}
}

func TestHelloResponseUsesConfiguredAudioParamsLikePython(t *testing.T) {
	defaults := *config.Get()
	defaults.Xiaozhi.Format = "opus"
	defaults.Xiaozhi.SampleRate = 16000
	defaults.Xiaozhi.Channels = 1
	defaults.Xiaozhi.FrameDuration = 60
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err := h.handleHello(context.Background(), &xiaozhiapi.ClientEventHello{
		ClientEventBase: xiaozhiapi.ClientEventBase{
			AudioParams: &xiaozhiapi.AudioParams{
				Format:        "opus",
				SampleRate:    24000,
				Channels:      1,
				FrameDuration: 20,
			},
		},
	})
	if err != nil {
		t.Fatalf("handle hello: %v", err)
	}
	hello := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventHello)
	if hello.AudioParams.SampleRate != 16000 || hello.AudioParams.FrameDuration != 60 {
		t.Fatalf("hello should use configured audio params: %+v", hello.AudioParams)
	}
	if hello.Version != 1 {
		t.Fatalf("expected hello version 1, got %d", hello.Version)
	}
}

func TestRawTextEventIsEchoed(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err, quit := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventRawText{Text: "123"})
	if err != nil {
		t.Fatalf("dispatch raw text: %v", err)
	}
	if quit {
		t.Fatal("raw text should not close the connection")
	}
	event := <-h.Recv(context.Background())
	if event != "123" {
		t.Fatalf("expected raw text echo, got %#v", event)
	}
}

func TestNewHandlerWithMemoryReturnsInitError(t *testing.T) {
	_, err := NewHandlerWithMemory(context.Background(), ClientInfo{DeviceID: "device-1"}, failingMemory{err: errors.New("broken memory")})
	if err == nil {
		t.Fatal("expected memory init error")
	}
}

func TestNewHandlerFallbackReturnsClosedHandlerOnInitializationFailure(t *testing.T) {
	defaults := *config.Get()
	defaults.Private = config.PrivateConfigConf{Enabled: true, Path: t.TempDir()}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{DeviceID: "device-1"})
	if h == nil {
		t.Fatal("handler must not be nil")
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("close fallback handler: %v", err)
	}
	if _, ok := <-h.Recv(context.Background()); ok {
		t.Fatal("fallback handler receive channel should be closed")
	}
}

func TestNewHandlerLoadsPrivateDeviceConfig(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), ".private_config.yaml")
	privateData := map[string]config.DevicePrivateConf{
		"device-1": {
			Owner:  "alice",
			Prompt: "private prompt",
			LLM:    &config.LLMConf{Type: "echo", Echo: config.EchoLLMConf{WelcomeMessage: "private hello"}},
			TTS:    &config.TTSConf{Type: "stub", SampleRate: 16000, Channels: 1},
			ASR:    &config.ASRConf{Type: "file_stub", StubTranscript: "private transcript"},
		},
	}
	encoded, err := yaml.Marshal(privateData)
	if err != nil {
		t.Fatalf("marshal private fixture: %v", err)
	}
	if err := os.WriteFile(privatePath, encoded, 0o644); err != nil {
		t.Fatalf("write private fixture: %v", err)
	}
	defaults := *config.Get()
	defaults.Private = config.PrivateConfigConf{Enabled: true, Path: privatePath}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{DeviceID: "device-1"})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if !h.runtime.Enabled {
		t.Fatal("private runtime should be enabled")
	}
	if h.runtime.Local.Prompt != "private prompt" {
		t.Fatalf("prompt got %q", h.runtime.Local.Prompt)
	}
	if h.runtime.ASR.StubTranscript != "private transcript" {
		t.Fatalf("asr config got %+v", h.runtime.ASR)
	}

	loaded := map[string]config.DevicePrivateConf{}
	data, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatalf("read private config: %v", err)
	}
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal private config: %v", err)
	}
	if loaded["device-1"].LastChatTime == 0 {
		t.Fatal("last_chat_time should be updated for owned device")
	}
}

func TestNewHandlerDoesNotLoadPrivateConfigByClientIDWhenDeviceIDMissing(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), ".private_config.yaml")
	privateData := map[string]config.DevicePrivateConf{
		"client-1": {
			Owner:  "alice",
			Prompt: "client private prompt",
			LLM:    &config.LLMConf{Type: "echo"},
			TTS:    &config.TTSConf{Type: "stub", SampleRate: 16000, Channels: 1},
			ASR:    &config.ASRConf{Type: "file_stub", StubTranscript: "client transcript"},
		},
	}
	encoded, err := yaml.Marshal(privateData)
	if err != nil {
		t.Fatalf("marshal private fixture: %v", err)
	}
	if err := os.WriteFile(privatePath, encoded, 0o644); err != nil {
		t.Fatalf("write private fixture: %v", err)
	}
	defaults := *config.Get()
	defaults.Private = config.PrivateConfigConf{Enabled: true, Path: privatePath}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{ClientID: "client-1"})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if h.runtime.Enabled {
		t.Fatal("private runtime should stay disabled without device-id")
	}
	if h.runtime.Local.Prompt == "client private prompt" {
		t.Fatalf("prompt got %q", h.runtime.Local.Prompt)
	}

	loaded := map[string]config.DevicePrivateConf{}
	data, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatalf("read private config: %v", err)
	}
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal private config: %v", err)
	}
	if loaded["client-1"].LastChatTime != 0 {
		t.Fatal("last_chat_time should not be updated without device-id")
	}
}

func TestNewHandlerDoesNotCreatePrivateConfigByClientIDWhenDeviceIDMissing(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), ".private_config.yaml")
	defaults := *config.Get()
	defaults.Private = config.PrivateConfigConf{Enabled: true, Path: privatePath}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{ClientID: "client-1"})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if h.runtime.Enabled {
		t.Fatal("private runtime should stay disabled without device-id")
	}
	if h.runtime.Device.AuthCode != "" {
		t.Fatalf("auth code should not be generated without device-id: %q", h.runtime.Device.AuthCode)
	}

	data, err := os.ReadFile(privatePath)
	if !os.IsNotExist(err) {
		t.Fatalf("private config file should not be created without device-id, data=%q err=%v", data, err)
	}
}

func TestAbortSendsTTSStop(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err, quit := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAbort{})
	if err != nil {
		t.Fatalf("dispatch abort: %v", err)
	}
	if quit {
		t.Fatal("abort should not close the websocket")
	}

	event := <-h.Recv(context.Background())
	tts, ok := event.(*xiaozhiapi.ServerEventTTS)
	if !ok {
		t.Fatalf("expected tts event, got %T", event)
	}
	if tts.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", tts.State)
	}
}

func TestUnknownClientEventIsIgnoredLikePython(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err, quit := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventUnknown{
		Type: "future",
		Raw:  []byte(`{"type":"future"}`),
	})
	if err != nil || quit {
		t.Fatalf("unknown event got err=%v quit=%v", err, quit)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("unknown event should not emit response, got %T", event)
	default:
	}
}

func TestJSONStringClientEventIsIgnored(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	event, err := h.UnmarshalClientTextEvent([]byte(`"plain ping"`))
	if err != nil {
		t.Fatalf("unmarshal json string: %v", err)
	}
	err, quit := h.DispatchClientEvent(context.Background(), event)
	if err != nil || quit {
		t.Fatalf("json string event got err=%v quit=%v", err, quit)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("json string event should not emit response, got %T", event)
	default:
	}
}

func TestListenDetectTextRunsTextPipeline(t *testing.T) {
	event, err := xiaozhiapi.UnmarshalClientEvent([]byte(`{"type":"listen","state":"detect","text":" 你好，小智！"}`))
	if err != nil {
		t.Fatalf("unmarshal listen detect: %v", err)
	}
	listen, ok := event.(*xiaozhiapi.ClientEventListen)
	if !ok {
		t.Fatalf("expected listen event, got %T", event)
	}
	if listen.Text != " 你好，小智！" {
		t.Fatalf("listen text not preserved: %q", listen.Text)
	}

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "should-not-run"},
		fakeLLM{text: []string{"检测回复。"}},
		voice.SilentTTS{},
	)
	err, _ = h.DispatchClientEvent(context.Background(), listen)
	if err != nil {
		t.Fatalf("dispatch listen detect: %v", err)
	}
	assertSTTStartSequence(t, h, "你好小智")
}

func TestListenDetectTextResumesAudioReception(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &sequentialLLM{responses: [][]string{{"文本回复。"}, {"语音回复。"}}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "后续语音"},
		llm,
		captureSegmentTTS{},
	)
	h.sleep = func(time.Duration) {}

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenDetect,
		Text:  "小智",
	})
	if err != nil {
		t.Fatalf("dispatch listen detect: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	drainUntilTTSState(t, h, xiaozhiapi.ServerTTSStateStop)

	err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	})
	if err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	for i := 0; i < 15; i++ {
		err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrame(t),
		})
		if err != nil {
			t.Fatalf("dispatch audio: %v", err)
		}
	}
	err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	})
	if err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}

	assertSTTStartSequence(t, h, "后续语音")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "语音回复" {
		t.Fatalf("unexpected resumed audio response: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("expected two llm calls, got %d", llm.calls)
	}
}

func TestListenDetectTextIgnoresPythonYeahSpecialCase(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "should-not-run"},
		llm,
		voice.SilentTTS{},
	)

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenDetect,
		Text:  "Yeah",
	})
	if err != nil {
		t.Fatalf("dispatch listen detect: %v", err)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("Yeah special case should not emit events, got %T", event)
	default:
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called, got %d calls", llm.calls)
	}
}

func TestExitCommandSendsSTTAndClosesWithoutLLM(t *testing.T) {
	defaults := *config.Get()
	defaults.Intent.ExitCommands = []string{"再见"}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "再见"},
		llm,
		voice.SilentTTS{},
	)

	if err := h.processText(context.Background(), "再见！"); err != nil {
		t.Fatalf("process exit command: %v", err)
	}
	assertSTTStartSequence(t, h, "再见")
	if _, ok := <-h.Recv(context.Background()); ok {
		t.Fatal("handler channel should be closed after exit command")
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called, got %d calls", llm.calls)
	}
	if len(h.history) != 0 {
		t.Fatalf("exit command should not be saved as dialogue: %+v", h.history)
	}
}

func TestExitCommandNormalizationMatchesPythonRemovePunctuation(t *testing.T) {
	defaults := *config.Get()
	defaults.Intent.ExitCommands = []string{"再见", "GOODBYE"}
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if !h.isExitCommand("再见?") {
		t.Fatal("python punctuation removal should match question mark")
	}
	if !h.isExitCommand("再见~") {
		t.Fatal("python punctuation removal should match tilde")
	}
	if h.isExitCommand("再见（吧）") {
		t.Fatal("text content inside punctuation should not be discarded")
	}
	if h.isExitCommand("goodbye") {
		t.Fatal("python direct exit matching is case-sensitive")
	}
}

func TestMutedWakeupWordSendsSTTAndTTSStopWithoutLLM(t *testing.T) {
	enableGreeting := false
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.EnableGreeting = &enableGreeting
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "小智"},
		llm,
		voice.SilentTTS{},
	)

	if err := h.processText(context.Background(), "小智。"); err != nil {
		t.Fatalf("process wakeup word: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	tts := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if tts.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", tts.State)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("muted wakeup word should not emit more events, got %T", event)
	default:
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called, got %d calls", llm.calls)
	}
	if len(h.history) != 0 {
		t.Fatalf("muted wakeup word should not be saved as dialogue: %+v", h.history)
	}
}

func TestListenDetectMutedWakeupWordSendsSTTAndTTSStopWithoutLLM(t *testing.T) {
	enableGreeting := false
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.EnableGreeting = &enableGreeting
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "should-not-run"},
		llm,
		voice.SilentTTS{},
	)

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenDetect,
		Text:  "小智。",
	})
	if err != nil {
		t.Fatalf("dispatch listen detect: %v", err)
	}

	assertSTTStartSequence(t, h, "小智")
	tts := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if tts.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", tts.State)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("muted wakeup detect should not emit more events, got %T", event)
	default:
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called, got %d calls", llm.calls)
	}
	if len(h.history) != 0 {
		t.Fatalf("muted wakeup detect should not be saved as dialogue: %+v", h.history)
	}
}

func TestWakeupWordResponseCacheSendsCachedAudioWithoutLLM(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "wakeup_words.opus")
	if err := os.WriteFile(cachePath, []byte("cached-opus"), 0o644); err != nil {
		t.Fatalf("write wakeup cache: %v", err)
	}
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.WakeupResponseCache = true
	defaults.Intent.WakeupResponseCacheDir = dir
	defaults.Intent.WakeupResponseCacheText = "欢迎回来"
	defaults.Intent.WakeupResponseCacheMinSize = 0
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "小智"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "小智。"); err != nil {
		t.Fatalf("process wakeup cache: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "欢迎回来" {
		t.Fatalf("unexpected sentence_start: %+v", sentenceStart)
	}
	audioFrame := (<-h.Recv(context.Background())).([]byte)
	if string(audioFrame) != "cached-opus" {
		t.Fatalf("unexpected cached audio frame: %q", audioFrame)
	}
	sentenceEnd := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd || sentenceEnd.Text != "欢迎回来" {
		t.Fatalf("unexpected sentence_end: %+v", sentenceEnd)
	}
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", stop.State)
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called for cached wakeup response, got %d calls", llm.calls)
	}
	if len(h.history) != 0 {
		t.Fatalf("cached wakeup response should not be saved as dialogue: %+v", h.history)
	}
}

func TestWakeupWordResponseCacheAllowsSmallCustomCacheWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "my_wakeup_words.opus")
	if err := os.WriteFile(cachePath, []byte("tiny-custom-cache"), 0o644); err != nil {
		t.Fatalf("write wakeup cache: %v", err)
	}
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.WakeupResponseCache = true
	defaults.Intent.WakeupResponseCacheDir = dir
	defaults.Intent.WakeupResponseCacheText = "自定义欢迎"
	defaults.Intent.WakeupResponseCacheMinSize = 0
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "小智"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "小智。"); err != nil {
		t.Fatalf("process wakeup custom cache: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "自定义欢迎" {
		t.Fatalf("unexpected sentence text: %q", sentenceStart.Text)
	}
	audioFrame := (<-h.Recv(context.Background())).([]byte)
	if string(audioFrame) != "tiny-custom-cache" {
		t.Fatalf("unexpected cached audio frame: %q", audioFrame)
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called for custom cached wakeup response, got %d calls", llm.calls)
	}
}

func TestWakeupWordResponseCacheSplitsLengthPrefixedOpus(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "wakeup_words.opus")
	var cache bytes.Buffer
	_ = binary.Write(&cache, binary.BigEndian, uint16(6))
	cache.WriteString("frame1")
	_ = binary.Write(&cache, binary.BigEndian, uint16(6))
	cache.WriteString("frame2")
	if err := os.WriteFile(cachePath, cache.Bytes(), 0o644); err != nil {
		t.Fatalf("write wakeup cache: %v", err)
	}
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.WakeupResponseCache = true
	defaults.Intent.WakeupResponseCacheDir = dir
	defaults.Intent.WakeupResponseCacheText = "欢迎回来"
	defaults.Intent.WakeupResponseCacheMinSize = 0
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "小智"},
		&countingLLM{text: []string{"不应该回复"}},
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "小智。"); err != nil {
		t.Fatalf("process wakeup cache: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	<-h.Recv(context.Background()) // sentence_start
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "frame1" {
		t.Fatalf("first cached frame = %q", got)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "frame2" {
		t.Fatalf("second cached frame = %q", got)
	}
	sentenceEnd := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd {
		t.Fatalf("expected sentence_end, got %+v", sentenceEnd)
	}
}

func TestWakeupWordResponseCachePrefersCustomCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wakeup_words.opus"), []byte("default-cache"), 0o644); err != nil {
		t.Fatalf("write default cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my_wakeup_words.opus"), []byte("custom-cache"), 0o644); err != nil {
		t.Fatalf("write custom cache: %v", err)
	}
	file, ok := wakeupResponseCacheFile(config.IntentConf{
		WakeupResponseCacheDir:     dir,
		WakeupResponseCacheMinSize: 0,
	})
	if !ok {
		t.Fatal("expected wakeup cache file")
	}
	if filepath.Base(file) != "my_wakeup_words.opus" {
		t.Fatalf("expected custom cache to be preferred, got %s", file)
	}
}

func TestWakeupWordResponseCacheMissFallsBackToLLM(t *testing.T) {
	defaults := *config.Get()
	defaults.Intent.WakeupWords = []string{"小智"}
	defaults.Intent.WakeupResponseCache = true
	defaults.Intent.WakeupResponseCacheDir = t.TempDir()
	defaults.Intent.WakeupResponseCacheMinSize = 0
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"正常回复。"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "小智"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "小智。"); err != nil {
		t.Fatalf("process wakeup cache miss: %v", err)
	}
	assertSTTStartSequence(t, h, "小智")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "正常回复" {
		t.Fatalf("unexpected fallback sentence: %q", sentenceStart.Text)
	}
	if llm.calls != 1 {
		t.Fatalf("llm should be called on cache miss, got %d calls", llm.calls)
	}
}

func TestIotEventKeepsDescriptorAndStateShape(t *testing.T) {
	raw := []byte(`{
		"type": "iot",
		"descriptors": [{
			"name": "Lamp",
			"description": "台灯",
			"properties": {"power": {"type": "boolean", "description": "开关"}},
			"methods": {"SetPower": {"description": "设置开关", "parameters": {"value": {"type": "boolean", "description": "目标状态"}}}}
		}],
		"states": [{"name": "Lamp", "state": {"power": true}}]
	}`)

	event, err := xiaozhiapi.UnmarshalClientEvent(raw)
	if err != nil {
		t.Fatalf("unmarshal iot event: %v", err)
	}
	iot, ok := event.(*xiaozhiapi.ClientEventIot)
	if !ok {
		t.Fatalf("expected iot event, got %T", event)
	}
	if len(iot.Descriptors) != 1 || iot.Descriptors[0].Name != "Lamp" {
		t.Fatalf("descriptor not preserved: %+v", iot.Descriptors)
	}
	if got := iot.States[0].State["power"]; got != true {
		t.Fatalf("state not preserved: %#v", got)
	}

	encoded, err := json.Marshal(&xiaozhiapi.ServerEventIot{
		Type: xiaozhiapi.ServerEventTypeIot,
		Commands: []xiaozhiapi.ServerEventIotCommand{{
			Name:       "Lamp",
			Method:     "SetPower",
			Parameters: map[string]any{"value": false},
		}},
	})
	if err != nil {
		t.Fatalf("marshal iot command: %v", err)
	}
	if string(encoded) != `{"type":"iot","commands":[{"name":"Lamp","method":"SetPower","parameters":{"value":false}}]}` {
		t.Fatalf("unexpected iot command json: %s", encoded)
	}
}

func TestIotToolsQueryStateAndSendCommand(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Lamp",
			Description: "台灯",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"power": {Type: "boolean", Description: "开关"},
			},
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetPower": {
					Description: "设置开关",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"value": {Type: "boolean", Description: "目标状态"},
					},
				},
			},
		}},
		States: []xiaozhiapi.IotState{{Name: "Lamp", State: map[string]any{"power": true}}},
	})
	if err != nil {
		t.Fatalf("dispatch iot: %v", err)
	}

	tools := h.IotTools()
	if len(tools) != 2 {
		t.Fatalf("expected two iot tools, got %+v", tools)
	}
	result, err := h.InvokeIotTool(context.Background(), "get_lamp_power", map[string]any{
		"response_success": "电源状态：{value}",
		"response_failure": "查询失败",
	})
	if err != nil {
		t.Fatalf("invoke query tool: %v", err)
	}
	if result.Message != "电源状态：true" {
		t.Fatalf("unexpected query result: %+v", result)
	}

	result, err = h.InvokeIotTool(context.Background(), "lamp_setpower", map[string]any{
		"value":            false,
		"response_success": "设置为{value}",
		"response_failure": "设置失败",
	})
	if err != nil {
		t.Fatalf("invoke control tool: %v", err)
	}
	if result.Command == nil {
		t.Fatal("control tool should produce command")
	}
	event := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventIot)
	if len(event.Commands) != 1 || event.Commands[0].Name != "Lamp" || event.Commands[0].Method != "SetPower" {
		t.Fatalf("unexpected iot command event: %+v", event)
	}
	if got := event.Commands[0].Parameters["value"]; got != false {
		t.Fatalf("unexpected command parameter: %#v", got)
	}
}

func TestIotDescriptorArrayShapeBuildsTools(t *testing.T) {
	event, err := xiaozhiapi.UnmarshalClientEvent([]byte(`{
		"type": "iot",
		"descriptors": [{
			"name": "Lamp",
			"description": "台灯",
			"properties": [{"name": "power", "type": "boolean", "description": "开关"}],
			"methods": [{
				"name": "SetPower",
				"description": "设置开关",
				"parameters": {"value": {"type": "boolean", "description": "目标状态"}}
			}]
		}],
		"states": [{"name": "Lamp", "state": {"power": true}}]
	}`))
	if err != nil {
		t.Fatalf("unmarshal iot array descriptor: %v", err)
	}
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	err, _ = h.DispatchClientEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("dispatch iot array descriptor: %v", err)
	}

	result, err := h.InvokeIotTool(context.Background(), "get_lamp_power", map[string]any{
		"response_success": "电源状态：{value}",
		"response_failure": "查询失败",
	})
	if err != nil {
		t.Fatalf("invoke query tool: %v", err)
	}
	if result.Message != "电源状态：true" {
		t.Fatalf("unexpected query result: %+v", result)
	}
}

func TestIotDescriptorInitializesPythonDefaultStateWithoutStateEvent(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Speaker",
			Description: "扬声器",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"volume": {Type: "number", Description: "音量"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("handle iot descriptor: %v", err)
	}
	result, err := h.InvokeIotTool(context.Background(), "get_speaker_volume", map[string]any{
		"response_success": "音量是{value}",
		"response_failure": "查询失败",
	})
	if err != nil {
		t.Fatalf("invoke default state query: %v", err)
	}
	if !result.OK || result.Data != "0" || result.Message != "音量是0" {
		t.Fatalf("unexpected default state query result: %+v", result)
	}
}

func TestVoicePipelineToolCallSendsIotCommand(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Lamp",
			Description: "台灯",
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetPower": {
					Description: "设置开关",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"value": {Type: "boolean", Description: "目标状态"},
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("handle iot: %v", err)
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "关灯"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{ID: "call_1", Name: "lamp_setpower", Arguments: "{\"value\":false,\"response_success\":\"已关灯\"}"}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "关灯")
	commandEvent := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventIot)
	if len(commandEvent.Commands) != 1 || commandEvent.Commands[0].Name != "Lamp" {
		t.Fatalf("unexpected iot command: %+v", commandEvent)
	}
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "已关灯" {
		t.Fatalf("unexpected tool speech: %q", sentenceStart.Text)
	}
}

func TestProcessTextUsesPlainLLMWhenIntentModeIsNoIntent(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Mode = "nointent"
	llm := &modeAwareLLM{
		plain: []string{"普通回复。"},
		tool:  []voice.LLMEvent{{ToolCall: &voice.ToolCall{ID: "call_1", Name: "get_time", Arguments: "{}"}}},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "几点了"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "几点了"); err != nil {
		t.Fatalf("process text: %v", err)
	}

	assertSTTStartSequence(t, h, "几点了")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "普通回复" {
		t.Fatalf("unexpected plain response: %q", sentenceStart.Text)
	}
	if llm.plainCalls != 1 {
		t.Fatalf("plain llm calls got %d want 1", llm.plainCalls)
	}
	if llm.toolCalls != 0 {
		t.Fatalf("tool llm should not be called in nointent mode, got %d", llm.toolCalls)
	}
}

func TestProcessTextUsesToolsWhenIntentModeIsFunctionCall(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Mode = "function_call"
	llm := &modeAwareLLM{
		plain: []string{"不应该使用。"},
		tool:  []voice.LLMEvent{{Content: "工具模式回复。"}},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "你好"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "你好"); err != nil {
		t.Fatalf("process text: %v", err)
	}

	assertSTTStartSequence(t, h, "你好")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "工具模式回复" {
		t.Fatalf("unexpected tool response: %q", sentenceStart.Text)
	}
	if llm.toolCalls != 1 {
		t.Fatalf("tool llm calls got %d want 1", llm.toolCalls)
	}
	if llm.plainCalls != 0 {
		t.Fatalf("plain llm should not be called in function_call mode, got %d", llm.plainCalls)
	}
}

func TestProcessTextUsesLegacyIntentLLMToolBeforePlainChat(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Mode = "intent_llm"
	llm := &modeAwareLLM{
		plain: []string{"不应该使用。"},
		tool:  []voice.LLMEvent{{ToolCall: &voice.ToolCall{ID: "call_1", Name: "get_time", Arguments: "{}"}}},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "几点了"},
		llm,
		captureSegmentTTS{},
	)
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.FixedZone("UTC+8", 8*60*60))
	}

	if err := h.processText(context.Background(), "几点了"); err != nil {
		t.Fatalf("process text: %v", err)
	}

	assertSTTStartSequence(t, h, "几点了")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "当前日期: 2026-06-15，当前时间: 08:09:10， 星期一" {
		t.Fatalf("unexpected intent tool response: %q", sentenceStart.Text)
	}
	if llm.toolCalls != 1 {
		t.Fatalf("intent llm tool calls got %d want 1", llm.toolCalls)
	}
	if llm.plainCalls != 0 {
		t.Fatalf("plain llm should not be called when legacy intent handles tool, got %d", llm.plainCalls)
	}
}

func TestProcessTextIntentLLMContinueChatFallsBackToPlainLLM(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Mode = "intent_llm"
	llm := &modeAwareLLM{
		plain: []string{"普通回复。"},
		tool:  []voice.LLMEvent{{ToolCall: &voice.ToolCall{ID: "call_1", Name: "continue_chat", Arguments: "{}"}}},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "聊聊天"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "聊聊天"); err != nil {
		t.Fatalf("process text: %v", err)
	}

	assertSTTStartSequence(t, h, "聊聊天")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "普通回复" {
		t.Fatalf("unexpected fallback response: %q", sentenceStart.Text)
	}
	if llm.toolCalls != 1 {
		t.Fatalf("intent llm should be called once before fallback, got %d", llm.toolCalls)
	}
	if llm.plainCalls != 1 {
		t.Fatalf("plain llm fallback calls got %d want 1", llm.plainCalls)
	}
}

func TestVoiceToolsIncludeNecessaryToolsAndConfiguredPlugins(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Functions = []string{"play_music", "get_news", "get_weather", "baidu_search", "hass_get_state", "hass_set_state", "hass_play_music"}

	tools := h.voiceTools()
	if len(tools) == 0 || tools[0].Name != "handle_exit_intent" {
		t.Fatalf("first voice tool should be handle_exit_intent, got %+v", tools)
	}
	if len(tools) < 2 || tools[1].Name != "plugin_loader" {
		t.Fatalf("second voice tool should be plugin_loader, got %+v", tools)
	}
	for _, want := range []string{"play_music", "get_news", "get_weather", "baidu_search", "hass_get_state", "hass_set_state", "hass_play_music", "change_role"} {
		if !strings.Contains(tools[1].Description, want) {
			t.Fatalf("plugin_loader description missing %q: %s", want, tools[1].Description)
		}
	}
	if len(tools) < 3 || tools[2].Name != "get_time" {
		t.Fatalf("third voice tool should be get_time, got %+v", tools)
	}
	if len(tools) < 4 || tools[3].Name != "get_lunar" {
		t.Fatalf("fourth voice tool should be get_lunar, got %+v", tools)
	}
	if len(tools) < 5 || tools[4].Name != "handle_device" {
		t.Fatalf("fifth voice tool should be handle_device, got %+v", tools)
	}
	if len(tools) < 6 || tools[5].Name != "play_music" {
		t.Fatalf("sixth voice tool should be play_music, got %+v", tools)
	}
	if len(tools) < 7 || tools[6].Name != "get_news" {
		t.Fatalf("seventh voice tool should be get_news, got %+v", tools)
	}
	if len(tools) < 8 || tools[7].Name != "get_weather" {
		t.Fatalf("eighth voice tool should be get_weather, got %+v", tools)
	}
	if got := tools[0].Parameters.Properties["say_goodbye"].Type; got != "string" {
		t.Fatalf("say_goodbye schema got %q want string", got)
	}
	if got := tools[0].Parameters.Required; len(got) != 1 || got[0] != "say_goodbye" {
		t.Fatalf("required got %v want [say_goodbye]", got)
	}
	if got := tools[5].Parameters.Properties["song_name"].Type; got != "string" {
		t.Fatalf("song_name schema got %q want string", got)
	}
	if got := tools[2].Parameters.Type; got != "object" {
		t.Fatalf("get_time schema type got %q want object", got)
	}
	if len(tools) < 9 || tools[8].Name != "baidu_search" {
		t.Fatalf("ninth voice tool should be baidu_search, got %+v", tools)
	}
	if len(tools) < 10 || tools[9].Name != "hass_get_state" {
		t.Fatalf("tenth voice tool should be hass_get_state, got %+v", tools)
	}
	if len(tools) < 11 || tools[10].Name != "hass_set_state" {
		t.Fatalf("eleventh voice tool should be hass_set_state, got %+v", tools)
	}
	if len(tools) < 12 || tools[11].Name != "hass_play_music" {
		t.Fatalf("twelfth voice tool should be hass_play_music, got %+v", tools)
	}
	if got := tools[12].Parameters.Required; len(got) != 2 || got[0] != "role" || got[1] != "role_name" {
		t.Fatalf("change_role required got %v want [role role_name]", got)
	}
	if got := tools[4].Parameters.Properties["device_type"].Type; got != "string" {
		t.Fatalf("handle_device device_type schema got %q want string", got)
	}
	if got := tools[6].Parameters.Required; len(got) != 1 || got[0] != "lang" {
		t.Fatalf("get_news required got %v want [lang]", got)
	}
	if got := tools[7].Parameters.Required; len(got) != 1 || got[0] != "lang" {
		t.Fatalf("get_weather required got %v want [lang]", got)
	}
	if got := tools[8].Parameters.Required; len(got) != 2 || got[0] != "query" || got[1] != "lang" {
		t.Fatalf("baidu_search required got %v want [query lang]", got)
	}
	if got := tools[9].Parameters.Required; len(got) != 1 || got[0] != "entity_id" {
		t.Fatalf("hass_get_state required got %v want [entity_id]", got)
	}
	if got := tools[10].Parameters.Properties["state"].Required; len(got) != 1 || got[0] != "type" {
		t.Fatalf("hass_set_state state required got %v want [type]", got)
	}
}

func TestVoiceToolsHideUnconfiguredOptionalPlugins(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Functions = nil

	names := toolNames(h.voiceTools())
	if containsString(names, "play_music") || containsString(names, "get_news") || containsString(names, "get_weather") || containsString(names, "baidu_search") || containsString(names, "hass_get_state") {
		t.Fatalf("optional plugins should be hidden until configured, got %v", names)
	}
	for _, want := range []string{"handle_exit_intent", "plugin_loader", "get_time", "get_lunar", "handle_device"} {
		if !containsString(names, want) {
			t.Fatalf("necessary tool %s missing from %v", want, names)
		}
	}
}

func TestAppendHomeAssistantDevicesToPrompt(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Local.Prompt = "base prompt"
	h.runtime.Intent.Functions = []string{"hass_get_state"}
	h.runtime.Plugins.HomeAssistant.Devices = []string{"客厅,音箱,media_player.room"}

	h.appendHomeAssistantDevicesToPrompt()
	if !strings.Contains(h.runtime.Local.Prompt, "下面是我家智能设备，可以通过homeassistant控制") {
		t.Fatalf("home assistant prompt missing header: %q", h.runtime.Local.Prompt)
	}
	if !strings.Contains(h.runtime.Local.Prompt, "客厅,音箱,media_player.room") {
		t.Fatalf("home assistant prompt missing device: %q", h.runtime.Local.Prompt)
	}
}

func toolNames(tools []voice.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestHandleIotIgnoresDescriptorsOutsideFunctionCallModeLikePython(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Mode = "nointent"

	if err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Lamp",
			Description: "台灯",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"power": {Type: "boolean", Description: "开关"},
			},
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetPower": {
					Description: "设置开关",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"value": {Type: "boolean", Description: "目标状态"},
					},
				},
			},
		}},
		States: []xiaozhiapi.IotState{{Name: "Lamp", State: map[string]any{"power": true}}},
	}); err != nil {
		t.Fatalf("handle iot: %v", err)
	}
	if tools := h.IotTools(); len(tools) != 0 {
		t.Fatalf("iot descriptors should not register tools when function_call mode is disabled: %+v", tools)
	}
	if _, ok := h.iotRegistry.GetState("Lamp", "power"); ok {
		t.Fatal("iot state should not be created for ignored descriptor")
	}
}

func TestHandleExitIntentToolSpeaksGoodbyeAndClosesAfterStop(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "我先走了"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_exit",
			Name:      "handle_exit_intent",
			Arguments: `{"say_goodbye":"下次再聊"}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "我先走了")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "下次再聊" {
		t.Fatalf("unexpected goodbye sentence: %+v", sentenceStart)
	}
	<-h.Recv(context.Background()) // audio
	<-h.Recv(context.Background()) // sentence_end
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", stop.State)
	}
	if _, ok := <-h.Recv(context.Background()); ok {
		t.Fatal("handler channel should close after exit intent response")
	}
}

func TestUnknownToolCallSpeaksPythonNotFoundMessage(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "调用未知功能"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_missing",
			Name:      "missing_tool",
			Arguments: `{}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process missing tool: %v", err)
	}
	assertSTTStartSequence(t, h, "调用未知功能")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "没有找到对应的函数" {
		t.Fatalf("unexpected missing tool speech: %+v", sentenceStart)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "没有找到对应的函数" {
		t.Fatalf("unexpected missing tool audio: %q", got)
	}
	sentenceEnd := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd || sentenceEnd.Text != "没有找到对应的函数" {
		t.Fatalf("unexpected missing tool sentence_end: %+v", sentenceEnd)
	}
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected stop after missing tool speech, got %+v", stop)
	}
	if len(h.history) != 2 ||
		h.history[0].Role != "user" || h.history[0].Content != "调用未知功能" ||
		h.history[1].Role != "assistant" || h.history[1].Content != "没有找到对应的函数" {
		t.Fatalf("missing tool should be saved as python assistant reply: %+v", h.history)
	}
}

func TestUnknownMCPToolCallSpeaksPythonNotFoundMessage(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	if h.mcpManager != nil {
		_ = h.mcpManager.Close()
	}
	h.mcpManager = mcp.NewManager(config.MCPConf{})
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "调用未知 MCP 功能"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_missing_mcp",
			Name:      "mcp_missing",
			Arguments: `{}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process missing mcp tool: %v", err)
	}
	assertSTTStartSequence(t, h, "调用未知 MCP 功能")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "没有找到对应的函数" {
		t.Fatalf("unexpected missing mcp tool speech: %+v", sentenceStart)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "没有找到对应的函数" {
		t.Fatalf("unexpected missing mcp tool audio: %q", got)
	}
	<-h.Recv(context.Background()) // sentence_end
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected stop after missing mcp tool speech, got %+v", stop)
	}
}

func TestPlayMusicToolSendsLocalMusicAudio(t *testing.T) {
	musicDir := t.TempDir()
	var p3 bytes.Buffer
	p3.Write([]byte{1, 0})
	_ = binary.Write(&p3, binary.BigEndian, uint16(6))
	p3.WriteString("frame1")
	p3.Write([]byte{1, 0})
	_ = binary.Write(&p3, binary.BigEndian, uint16(6))
	p3.WriteString("frame2")
	if err := os.WriteFile(filepath.Join(musicDir, "两只老虎.p3"), p3.Bytes(), 0o644); err != nil {
		t.Fatalf("write music p3: %v", err)
	}

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Plugins.PlayMusic = config.PlayMusicConf{
		MusicDir: musicDir,
		MusicExt: []string{".p3"},
	}

	msg, err := h.handlePlayMusicTool(context.Background(), map[string]any{"song_name": "两只"})
	if err != nil {
		t.Fatalf("play music tool: %v", err)
	}
	if msg != "正在为您播放音乐" {
		t.Fatalf("unexpected tool message: %q", msg)
	}
	assertSTTStartSequence(t, h, "正在播放两只老虎.p3")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "两只老虎.p3" {
		t.Fatalf("unexpected music sentence_start: %+v", sentenceStart)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "frame1" {
		t.Fatalf("first music frame = %q", got)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "frame2" {
		t.Fatalf("second music frame = %q", got)
	}
	sentenceEnd := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd {
		t.Fatalf("expected sentence_end, got %+v", sentenceEnd)
	}
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected stop, got %+v", stop)
	}
}

func TestVoicePipelinePlayMusicToolSpeaksPythonCompatibleAckAfterMusic(t *testing.T) {
	musicDir := t.TempDir()
	var p3 bytes.Buffer
	p3.Write([]byte{1, 0})
	_ = binary.Write(&p3, binary.BigEndian, uint16(6))
	p3.WriteString("frame1")
	if err := os.WriteFile(filepath.Join(musicDir, "歌.p3"), p3.Bytes(), 0o644); err != nil {
		t.Fatalf("write music p3: %v", err)
	}

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Plugins.PlayMusic = config.PlayMusicConf{
		MusicDir: musicDir,
		MusicExt: []string{".p3"},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "播放音乐"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_music",
			Name:      "play_music",
			Arguments: `{"song_name":"歌"}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "播放音乐")
	assertSTTStartSequence(t, h, "正在播放歌.p3")
	<-h.Recv(context.Background()) // sentence_start
	<-h.Recv(context.Background()) // music frame
	<-h.Recv(context.Background()) // sentence_end
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected music stop, got %+v", stop)
	}
	ackStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if ackStart.State != xiaozhiapi.ServerTTSStateSentenceStart || ackStart.Text != "正在为您播放音乐" {
		t.Fatalf("unexpected play music ack: %+v", ackStart)
	}
	if got := string((<-h.Recv(context.Background())).([]byte)); got != "正在为您播放音乐" {
		t.Fatalf("unexpected ack audio frame: %q", got)
	}
	ackEnd := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if ackEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd || ackEnd.Text != "正在为您播放音乐" {
		t.Fatalf("unexpected play music ack end: %+v", ackEnd)
	}
	finalStop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if finalStop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected final stop after ack, got %+v", finalStop)
	}
}

func TestGetTimeToolReturnsPythonCompatibleTimeText(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.Local)
	}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_time",
		Name:      "get_time",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("execute get_time: %v", err)
	}
	if message.Content != "当前日期: 2026-06-15，当前时间: 08:09:10， 星期一" {
		t.Fatalf("unexpected time content: %q", message.Content)
	}
}

func TestGetTimeToolResultFeedsSecondLLMCall(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.Local)
	}
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_time", Name: "get_time", Arguments: `{}`}}},
			{{Content: "现在是上午八点九分。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "现在几点"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "现在几点")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "现在是上午八点九分" {
		t.Fatalf("unexpected time reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("get_time should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].Content != "当前日期: 2026-06-15，当前时间: 08:09:10， 星期一" {
		t.Fatalf("second llm call missing time tool content: %+v", secondHistory)
	}
}

func TestGetLunarToolReturnsPromptForSecondLLMCall(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.Local)
	}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_lunar",
		Name:      "get_lunar",
		Arguments: `{"query":"生肖和星座"}`,
	})
	if err != nil {
		t.Fatalf("execute get_lunar: %v", err)
	}
	if !message.RequireLLM {
		t.Fatalf("get_lunar should require second llm call")
	}
	for _, want := range []string{
		"根据以下信息回应用户的查询请求，并提供与生肖和星座相关的信息",
		"当前公历日期: 2026-06-15，当前时间: 08:09:10，星期一",
		"生肖: 属马",
		"星座: 双子座",
	} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("lunar content missing %q: %s", want, message.Content)
		}
	}
}

func TestGetLunarToolUsesConfiguredCommand(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.Local)
	}
	h.runtime.Intent.LunarCommand = "/bin/sh"
	h.runtime.Intent.LunarArgs = []string{"-c", "printf '农历桥: %s %s %s' \"$1\" \"$2\" \"$3\"", "lunar", "{query}", "{date}", "{time}"}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_lunar",
		Name:      "get_lunar",
		Arguments: `{"query":"宜忌"}`,
	})
	if err != nil {
		t.Fatalf("execute get_lunar: %v", err)
	}
	if message.Content != "农历桥: 宜忌 2026-06-15 08:09:10" {
		t.Fatalf("unexpected command lunar content: %q", message.Content)
	}
	if !message.RequireLLM {
		t.Fatalf("get_lunar should require second llm call")
	}
}

func TestGetLunarToolFallsBackWhenConfiguredCommandReturnsEmpty(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.now = func() time.Time {
		return time.Date(2026, 6, 15, 8, 9, 10, 0, time.Local)
	}
	h.runtime.Intent.LunarCommand = "/bin/sh"
	h.runtime.Intent.LunarArgs = []string{"-c", "true"}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_lunar",
		Name:      "get_lunar",
		Arguments: `{"query":"生肖"}`,
	})
	if err != nil {
		t.Fatalf("execute get_lunar: %v", err)
	}
	if !strings.Contains(message.Content, "根据以下信息回应用户的查询请求，并提供与生肖相关的信息") {
		t.Fatalf("expected fallback lunar content, got: %q", message.Content)
	}
}

func TestPluginLoaderUpdatesOptionalTools(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.Intent.Functions = nil

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_plugin",
		Name:      "plugin_loader",
		Arguments: `{"oper":"load","name":"get_weather"}`,
	})
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	if message.Content != "get_weather插件加载成功" {
		t.Fatalf("unexpected load response: %q", message.Content)
	}
	if !containsString(toolNames(h.voiceTools()), "get_weather") {
		t.Fatalf("get_weather should be exposed after plugin load")
	}

	message, err = h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_plugin",
		Name:      "plugin_loader",
		Arguments: `{"oper":"unload","name":"get_weather"}`,
	})
	if err != nil {
		t.Fatalf("unload plugin: %v", err)
	}
	if message.Content != "get_weather插件卸载成功" {
		t.Fatalf("unexpected unload response: %q", message.Content)
	}
	if containsString(toolNames(h.voiceTools()), "get_weather") {
		t.Fatalf("get_weather should be hidden after plugin unload")
	}
}

func TestGetNewsToolResultFeedsSecondLLMCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss><channel><item>
<title>测试新闻</title>
<link>https://example.com/news</link>
<description>新闻摘要</description>
<pubDate>Mon, 15 Jun 2026 08:00:00 GMT</pubDate>
</item></channel></rss>`))
	}))
	defer server.Close()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.newsClient = news.New(config.GetNewsConf{
		DefaultRSSURL: server.URL,
		CategoryURLs:  map[string]string{"finance": server.URL},
	})
	h.newsClient.HTTPClient = server.Client()
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_news", Name: "get_news", Arguments: `{"category":"财经","lang":"zh_CN"}`}}},
			{{Content: "这是一条测试新闻。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "播报财经新闻"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "播报财经新闻")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "这是一条测试新闻" {
		t.Fatalf("unexpected news reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("get_news should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || !strings.Contains(secondHistory[len(secondHistory)-1].Content, "新闻标题: 测试新闻") {
		t.Fatalf("second llm call missing news tool content: %+v", secondHistory)
	}
}

func TestGetWeatherToolResultFeedsSecondLLMCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/geo":
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"city":"杭州市","adcode":"330100"}]}`))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status":"1","forecasts":[{"casts":[{"date":"2026-06-15","week":"1","dayweather":"多云","nightweather":"小雨","daytemp":"30","nighttemp":"22","daywind":"东","nightwind":"北"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.weatherClient = weather.New(config.GetWeatherConf{
		APIKey:  "test-key",
		BaseURL: server.URL,
	})
	h.weatherClient.HTTPClient = server.Client()
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_weather", Name: "get_weather", Arguments: `{"location":"杭州","lang":"zh_CN"}`}}},
			{{Content: "杭州今天多云，夜里有小雨。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "杭州天气"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "杭州天气")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "杭州今天多云，夜里有小雨" {
		t.Fatalf("unexpected weather reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("get_weather should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || !strings.Contains(secondHistory[len(secondHistory)-1].Content, "杭州市天气:") {
		t.Fatalf("second llm call missing weather tool content: %+v", secondHistory)
	}
}

func TestBaiduSearchToolResultFeedsSecondLLMCall(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s":
			_, _ = w.Write([]byte(`<html><body><div class="result"><h3><a href="` + serverURL + `/page">搜索标题</a></h3><div class="c-abstract">搜索摘要</div></div></body></html>`))
		case "/page":
			_, _ = w.Write([]byte(`<html><body><article><p>搜索正文</p></article></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.searchClient = search.New(config.BaiduSearchConf{
		Enabled:          true,
		SearchURL:        server.URL + "/s",
		MaxResults:       10,
		MaxExtractPages:  3,
		MaxContentLength: 1000,
	})
	h.searchClient.HTTPClient = server.Client()
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_search", Name: "baidu_search", Arguments: `{"query":"小智","lang":"zh_CN","num_results":1}`}}},
			{{Content: "找到一条关于小智的结果。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "搜索小智"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "搜索小智")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "找到一条关于小智的结果" {
		t.Fatalf("unexpected search reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("baidu_search should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || !strings.Contains(secondHistory[len(secondHistory)-1].Content, "百度搜索结果 - 关键词: 小智") {
		t.Fatalf("second llm call missing search tool content: %+v", secondHistory)
	}
}

func TestMCPToolResultFeedsSecondLLMCall(t *testing.T) {
	manager := mcp.NewManager(config.MCPConf{
		Enabled: true,
		Servers: map[string]config.MCPServerConf{
			"test": {
				Command: os.Args[0],
				Args:    []string{"-test.run=TestMCPToolResultFeedsSecondLLMCall"},
				Env:     map[string]string{"GO_XIAOZHI_HANDLER_MCP_HELPER": "1"},
			},
		},
	})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mcp: %v", err)
	}
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	if h.mcpManager != nil {
		_ = h.mcpManager.Close()
	}
	h.mcpManager = manager
	t.Cleanup(func() { _ = manager.Close() })
	names := toolNames(h.voiceTools())
	if !containsString(names, "mcp_echo") {
		t.Fatalf("mcp_echo missing from voice tools: %v", names)
	}
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_mcp", Name: "mcp_echo", Arguments: `{"text":"hello"}`}}},
			{{Content: "MCP 返回了 hello。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "调用 MCP"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "调用 MCP")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "MCP 返回了 hello" {
		t.Fatalf("unexpected mcp reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("mcp tool should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].Content != "echo: hello" {
		t.Fatalf("second llm call missing mcp content: %+v", secondHistory)
	}
}

func TestMCPToolErrorFeedsSecondLLMCall(t *testing.T) {
	manager := mcp.NewManager(config.MCPConf{
		Enabled: true,
		Servers: map[string]config.MCPServerConf{
			"test": {
				Command: os.Args[0],
				Args:    []string{"-test.run=TestMCPToolErrorFeedsSecondLLMCall"},
				Env:     map[string]string{"GO_XIAOZHI_HANDLER_MCP_HELPER": "1"},
			},
		},
	})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize mcp: %v", err)
	}
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	if h.mcpManager != nil {
		_ = h.mcpManager.Close()
	}
	h.mcpManager = manager
	t.Cleanup(func() { _ = manager.Close() })
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_mcp", Name: "mcp_echo", Arguments: `{"text":"fail"}`}}},
			{{Content: "MCP 工具调用失败了。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "调用 MCP 出错"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "调用 MCP 出错")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "MCP 工具调用失败了" {
		t.Fatalf("unexpected mcp error reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("mcp tool error should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].Content != "Error calling tool echo: boom" {
		t.Fatalf("second llm call missing mcp error content: %+v", secondHistory)
	}
}

func TestHassGetStateToolResultFeedsSecondLLMCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states/media_player.room" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"state":"playing","attributes":{"media_title":"歌名","volume_level":0.5}}`))
	}))
	defer server.Close()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.haClient = homeassistant.New(config.HomeAssistantConf{BaseURL: server.URL, APIKey: "token"})
	h.haClient.HTTPClient = server.Client()
	llm := &sequentialToolLLM{
		responses: [][]voice.LLMEvent{
			{{ToolCall: &voice.ToolCall{ID: "call_ha", Name: "hass_get_state", Arguments: `{"entity_id":"media_player.room"}`}}},
			{{Content: "音箱正在播放歌名。"}},
		},
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "音箱状态"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "音箱状态")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "音箱正在播放歌名" {
		t.Fatalf("unexpected hass state reply: %q", sentenceStart.Text)
	}
	if llm.calls != 2 {
		t.Fatalf("hass_get_state should request llm again after tool result, got %d calls", llm.calls)
	}
	secondHistory := llm.histories[1]
	if len(secondHistory) == 0 || !strings.Contains(secondHistory[len(secondHistory)-1].Content, "设备状态:playing") {
		t.Fatalf("second llm call missing hass state content: %+v", secondHistory)
	}
}

func TestHassPlayMusicToolSpeaksDirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/music_assistant/play_media" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.haClient = homeassistant.New(config.HomeAssistantConf{BaseURL: server.URL})
	h.haClient.HTTPClient = server.Client()
	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_ha_music",
		Name:      "hass_play_music",
		Arguments: `{"entity_id":"media_player.room","media_content_id":"周杰伦"}`,
	})
	if err != nil {
		t.Fatalf("hass play music: %v", err)
	}
	if message.RequireLLM {
		t.Fatalf("hass_play_music should be direct response")
	}
	if message.Content != "正在播放周杰伦的音乐" {
		t.Fatalf("unexpected hass play response: %q", message.Content)
	}
}

func TestChangeRoleToolUpdatesPromptAndSpeaksResult(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.history = []voice.Message{
		{Role: "system", Content: "old prompt"},
		{Role: "user", Content: "你好"},
	}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_role",
		Name:      "change_role",
		Arguments: `{"role":"英语老师","role_name":"Alice"}`,
	})
	if err != nil {
		t.Fatalf("execute change_role: %v", err)
	}
	if message.Content != "切换角色成功,我是英语老师" {
		t.Fatalf("unexpected role change response: %q", message.Content)
	}
	if !strings.Contains(h.runtime.Local.Prompt, "Alice(Lily)的英语老师") {
		t.Fatalf("prompt not updated: %q", h.runtime.Local.Prompt)
	}
	if h.history[0].Role != "system" || h.history[0].Content != h.runtime.Local.Prompt {
		t.Fatalf("system history not updated: %+v", h.history)
	}
}

func TestChangeRoleToolDoesNotInsertSystemHistoryLikePython(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.history = []voice.Message{{Role: "user", Content: "你好"}}

	message, err := h.executeToolCall(context.Background(), voice.ToolCall{
		ID:        "call_role",
		Name:      "change_role",
		Arguments: `{"role":"英语老师","role_name":"Alice"}`,
	})
	if err != nil {
		t.Fatalf("execute change_role: %v", err)
	}
	if message.Content != "切换角色成功,我是英语老师" {
		t.Fatalf("unexpected role change response: %q", message.Content)
	}
	if len(h.history) != 1 || h.history[0].Role != "user" {
		t.Fatalf("change_system_prompt should not insert system history like Python: %+v", h.history)
	}
}

func TestChangeRoleDongbeiPromptKeepsPythonFixedName(t *testing.T) {
	prompt, ok := rolePrompt("东北妹子", "Alice")
	if !ok {
		t.Fatal("expected dongbei role prompt")
	}
	if !strings.Contains(prompt, "我是小冰，一个搞笑的00后东北妹子") {
		t.Fatalf("dongbei prompt should keep Python fixed name 小冰: %q", prompt)
	}
	if strings.Contains(prompt, "我是Alice，一个搞笑的00后东北妹子") {
		t.Fatalf("dongbei prompt should not replace initialization name with role_name: %q", prompt)
	}
}

func TestVoicePipelineChangeRoleToolUsesNewPromptForLaterChat(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "切换成英语老师"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_role",
			Name:      "change_role",
			Arguments: `{"role":"英语老师","role_name":"Alice"}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process role change: %v", err)
	}
	assertSTTStartSequence(t, h, "切换成英语老师")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "切换角色成功,我是英语老师" {
		t.Fatalf("unexpected role change speech: %q", sentenceStart.Text)
	}
	<-h.Recv(context.Background()) // audio
	<-h.Recv(context.Background()) // sentence_end
	<-h.Recv(context.Background()) // stop

	llm := &captureHistoryLLM{chunks: []string{"好的。"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "开始练习"},
		llm,
		captureSegmentTTS{},
	)
	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process after role change: %v", err)
	}
	if len(llm.history) == 0 || llm.history[0].Role != "system" || !strings.Contains(llm.history[0].Content, "Alice(Lily)的英语老师") {
		t.Fatalf("later llm call did not use changed prompt: %+v", llm.history)
	}
}

func TestHandleDeviceToolGetsAndSetsSpeakerVolume(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Speaker",
			Description: "扬声器",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"volume": {Type: "number", Description: "音量"},
			},
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetVolume": {
					Description: "设置音量",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"volume": {Type: "number", Description: "音量"},
					},
				},
			},
		}},
		States: []xiaozhiapi.IotState{{Name: "Speaker", State: map[string]any{"volume": float64(45)}}},
	})
	if err != nil {
		t.Fatalf("handle iot: %v", err)
	}

	content, err := h.handleDeviceTool(map[string]any{"device_type": "Speaker", "action": "get"})
	if err != nil {
		t.Fatalf("get volume: %v", err)
	}
	if content != "当前音量45" {
		t.Fatalf("unexpected get response: %q", content)
	}

	content, err = h.handleDeviceTool(map[string]any{"device_type": "Speaker", "action": "raise"})
	if err != nil {
		t.Fatalf("raise volume: %v", err)
	}
	if content != "音量已调整到55" {
		t.Fatalf("unexpected raise response: %q", content)
	}
	command := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventIot)
	if len(command.Commands) != 1 || command.Commands[0].Name != "Speaker" || command.Commands[0].Method != "SetVolume" {
		t.Fatalf("unexpected volume command: %+v", command)
	}
	if got := command.Commands[0].Parameters["volume"]; got != 55 {
		t.Fatalf("volume command got %#v want 55", got)
	}
}

func TestHandleDeviceToolClampsScreenBrightness(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	if err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Screen",
			Description: "屏幕",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"brightness": {Type: "number", Description: "亮度"},
			},
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetBrightness": {
					Description: "设置亮度",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"brightness": {Type: "number", Description: "亮度"},
					},
				},
			},
		}},
		States: []xiaozhiapi.IotState{{Name: "Screen", State: map[string]any{"brightness": 95}}},
	}); err != nil {
		t.Fatalf("handle iot: %v", err)
	}

	content, err := h.handleDeviceTool(map[string]any{"device_type": "Screen", "action": "set", "value": float64(150)})
	if err != nil {
		t.Fatalf("set brightness: %v", err)
	}
	if content != "亮度已调整到100" {
		t.Fatalf("unexpected brightness response: %q", content)
	}
	command := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventIot)
	if got := command.Commands[0].Parameters["brightness"]; got != 100 {
		t.Fatalf("brightness command got %#v want 100", got)
	}
}

func TestVoicePipelineHandleDeviceToolSendsCommandAndSpeaksResult(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	if err := h.handleIot(context.Background(), &xiaozhiapi.ClientEventIot{
		Descriptors: []xiaozhiapi.IotDescriptor{{
			Name:        "Speaker",
			Description: "扬声器",
			Properties: map[string]xiaozhiapi.IotPropertySchema{
				"volume": {Type: "number", Description: "音量"},
			},
			Methods: map[string]xiaozhiapi.IotMethodSchema{
				"SetVolume": {
					Description: "设置音量",
					Parameters: map[string]xiaozhiapi.IotPropertySchema{
						"volume": {Type: "number", Description: "音量"},
					},
				},
			},
		}},
		States: []xiaozhiapi.IotState{{Name: "Speaker", State: map[string]any{"volume": 20}}},
	}); err != nil {
		t.Fatalf("handle iot: %v", err)
	}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "调大音量"},
		&fakeToolLLM{events: []voice.LLMEvent{{ToolCall: &voice.ToolCall{
			ID:        "call_device",
			Name:      "handle_device",
			Arguments: `{"device_type":"Speaker","action":"raise"}`,
		}}}},
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	assertSTTStartSequence(t, h, "调大音量")
	command := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventIot)
	if len(command.Commands) != 1 || command.Commands[0].Method != "SetVolume" {
		t.Fatalf("unexpected device command: %+v", command)
	}
	if got := command.Commands[0].Parameters["volume"]; got != 30 {
		t.Fatalf("volume command got %#v want 30", got)
	}
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "音量已调整到30" {
		t.Fatalf("unexpected handle_device speech: %q", sentenceStart.Text)
	}
}

func TestListenStopRunsVoicePipeline(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	// 注入确定性 pipeline：ASR 回 "收到语音"，EchoLLM(echo) 回 "我听到了：收到语音"
	// 切成两段（"我听到了：" / "收到语音"），captureSegmentTTS 把每段文本转为非空音频帧。
	// 不依赖默认配置（默认 pipeline 可能连真实 OpenAI，导致输出不确定）。
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "收到语音"},
		voice.EchoLLM{EchoTranscripts: true},
		captureSegmentTTS{},
	)
	h.sleep = func(time.Duration) {}

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	})
	if err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	for i := 0; i < 15; i++ {
		err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrame(t),
		})
		if err != nil {
			t.Fatalf("dispatch audio: %v", err)
		}
	}
	err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	})
	if err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}

	assertSTTStartSequence(t, h, "收到语音")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart {
		t.Fatalf("expected sentence_start, got %s", sentenceStart.State)
	}
	if sentenceStart.Text != "我听到了：" {
		t.Fatalf("unexpected tts text: %q", sentenceStart.Text)
	}
	sentenceEnd, sawAudio := drainUntilTTSState(t, h, xiaozhiapi.ServerTTSStateSentenceEnd)
	if sentenceEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd {
		t.Fatalf("expected sentence_end, got %s", sentenceEnd.State)
	}
	if !sawAudio {
		t.Fatal("expected binary audio frame between sentence_start and sentence_end")
	}
	sawSecondAudio := false
	var stop *xiaozhiapi.ServerEventTTS
	for {
		event := <-h.Recv(context.Background())
		if audioFrame, ok := event.([]byte); ok {
			if len(audioFrame) == 0 {
				t.Fatal("binary audio frame is empty")
			}
			sawSecondAudio = true
			continue
		}
		tts, ok := event.(*xiaozhiapi.ServerEventTTS)
		if !ok {
			t.Fatalf("unexpected event before stop: %T", event)
		}
		if tts.State == xiaozhiapi.ServerTTSStateStop {
			stop = tts
			break
		}
	}
	if !sawSecondAudio {
		t.Fatal("expected second segment binary audio")
	}
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", stop.State)
	}
}

// 流式发送：第一段在整段回复（含第二段）合成完毕之前就已下发到 writeQ。
// 证明首音延迟从"全部合成时间"降到"首段合成时间"。
func TestProcessTextStreamsFirstSegmentBeforeFullSynthesis(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	gate := make(chan struct{})
	tts := &gatedSegmentTTS{gate: gate}
	// 默认无 intent mode → 走 Respond（非 tools），echo/off 配置无关。
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "你好"},
		fakeLLM{text: []string{"第一句。", "第二句。"}},
		tts,
	)
	h.sleep = func(time.Duration) {}

	// processText 在 goroutine 里跑；第二段合成会阻塞在 gate 上。
	go func() {
		_ = h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}})
	}()

	assertSTTStartSequence(t, h, "你好")
	// 第一段的 sentence_start + audio 应已抵达——此时第二段仍阻塞在合成中。
	start := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if start.State != xiaozhiapi.ServerTTSStateSentenceStart || start.Text != "第一句" {
		t.Fatalf("expected first sentence_start (第一句), got state=%s text=%q", start.State, start.Text)
	}
	audioFrame := (<-h.Recv(context.Background())).([]byte)
	if string(audioFrame) != "第一句" {
		t.Fatalf("expected first segment audio, got %q", string(audioFrame))
	}
	// 放行第二段，让 pipeline 收尾。
	close(gate)
}

// 流式取消：用户打断（handleAbort 递增 speechSeq）后，回调返回 errSpeechCanceled，
// pipeline 立即停止合成后续段。证明取消现在能停掉进行中的合成，而非仅跳过发送。
func TestAbortStopsStreamingSynthesis(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	tts := &countingSegmentTTS{}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "你好"},
		fakeLLM{text: []string{"第一句。", "第二句。", "第三句。"}},
		tts,
	)
	h.sleep = func(time.Duration) {}

	var abortOnce sync.Once
	h.testAfterWrite = func(event any) {
		frame, ok := event.([]byte)
		if !ok || string(frame) != "第一句" {
			return
		}
		abortOnce.Do(func() {
			if err := h.handleAbort(context.Background(), &xiaozhiapi.ClientEventAbort{}); err != nil {
				t.Errorf("handleAbort: %v", err)
			}
		})
	}

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	// abort 在第一段发送后触发：onSegment 在"合成之后"调用，故第二段可能已合成完，
	// 但回调返回 errSpeechCanceled 后 pipeline 不再合成第三段。
	// 关键断言：第三段绝不被合成（取消能停掉进行中的合成链）。
	tts.mu.Lock()
	defer tts.mu.Unlock()
	for _, txt := range tts.texts {
		if txt == "第三句" {
			t.Fatalf("third segment must not be synthesized after abort, got %v", tts.texts)
		}
	}
}

// listen/start 在上一轮语音仍在途（流式发送中）时，必须中止它：递增 speechSeq、
// 发 TTS Stop（让设备清空缓冲），并停止后续段合成。这解决了"第二句断断续续"——
// 上一轮残余音频不再和本轮交错。无语音在途时则不发空 stop。
func TestListenStartAbortsInFlightStreamingSpeech(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	gate := make(chan struct{})
	tts := &gatedSegmentTTS{gate: gate}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "你好"},
		fakeLLM{text: []string{"第一句。", "第二句。"}},
		tts,
	)
	h.sleep = func(time.Duration) {}

	// 上一轮回复在 goroutine 里跑；第二段合成阻塞在 gate 上 → speaking 保持 true。
	go func() {
		_ = h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}})
	}()

	assertSTTStartSequence(t, h, "你好")
	// 第一段完整序列 sentence_start → audio → sentence_end 已抵达
	// （speaking=true，第二段仍阻塞在合成中，上一轮在途）。
	firstEnd, _ := drainUntilTTSState(t, h, xiaozhiapi.ServerTTSStateSentenceEnd)
	if firstEnd.State != xiaozhiapi.ServerTTSStateSentenceEnd {
		t.Fatalf("expected first sentence_end, got %s", firstEnd.State)
	}

	// 用户开始说话（设备发 listen/start）。此时应中止上一轮在途语音。
	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeAuto,
	}); err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	// 应收到 TTS Stop（通知设备清空缓冲）。
	stop := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop after listen/start aborted in-flight speech, got %s", stop.State)
	}
	// 放行阻塞的第二段，让上一轮 goroutine 收尾（回调已被取消，不再发送）。
	close(gate)
}

// 对照：无语音在途时 listen/start 不应发空 TTS Stop（避免设备困惑）。
func TestListenStartDoesNotEmitStopWhenNoSpeechInFlight(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	}); err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	// 无语音在途：writeQ 应为空，不发 stop。
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("expected no event when no speech in flight, got %T", event)
	default:
	}
}

func TestAudioReceptionResumesBeforeSpeechPlaybackFinishes(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	asr := &sequentialASR{texts: []string{"第一句", "第二句"}}
	llm := &sequentialLLM{responses: [][]string{{"第一段回复。"}, {"第二段回复。"}}}
	h.pipeline = voice.NewPipeline(asr, llm, captureSegmentTTS{})
	h.sleep = func(time.Duration) {}

	var injectOnce sync.Once
	h.testAfterWrite = func(event any) {
		frame, ok := event.([]byte)
		if !ok || string(frame) != "第一段回复" {
			return
		}
		injectOnce.Do(func() {
			if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
				State: xiaozhiapi.ClientStateListenStart,
				Mode:  xiaozhiapi.ClientModeManual,
			}); err != nil {
				t.Errorf("dispatch second listen start: %v", err)
			}
			for i := 0; i < 15; i++ {
				if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
					Bytes: validOpusFrame(t),
				}); err != nil {
					t.Errorf("dispatch second audio: %v", err)
					return
				}
			}
			if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
				State: xiaozhiapi.ClientStateListenStop,
			}); err != nil {
				t.Errorf("dispatch second listen stop: %v", err)
			}
		})
	}

	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	}); err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	for i := 0; i < 15; i++ {
		if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrame(t),
		}); err != nil {
			t.Fatalf("dispatch first audio: %v", err)
		}
	}
	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	}); err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}

	assertSTTStartSequence(t, h, "第一句")
	firstStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if firstStart.Text != "第一段回复" {
		t.Fatalf("first response text = %q", firstStart.Text)
	}
	sawSecondSTT := false
	sawSecondSentence := false
	for i := 0; i < 20; i++ {
		event := <-h.Recv(context.Background())
		switch ev := event.(type) {
		case *xiaozhiapi.ServerEventSTT:
			if ev.Text == "第二句" {
				sawSecondSTT = true
			}
		case *xiaozhiapi.ServerEventTTS:
			if ev.State == xiaozhiapi.ServerTTSStateSentenceStart && ev.Text == "第二段回复" {
				sawSecondSentence = true
			}
		}
		if sawSecondSTT && sawSecondSentence {
			break
		}
	}
	if !sawSecondSTT {
		t.Fatal("expected second utterance STT while first playback was still active")
	}
	if !sawSecondSentence {
		t.Fatal("expected second utterance speech response")
	}
	if asr.calls != 2 {
		t.Fatalf("expected two asr calls, got %d", asr.calls)
	}
}

func TestListenStopProcessingDoesNotBlockAbort(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	asrStarted := make(chan struct{})
	releaseASR := make(chan struct{})
	h.pipeline = voice.NewPipeline(
		&blockingASR{started: asrStarted, release: releaseASR, text: "稍后回复"},
		fakeLLM{text: []string{"不应阻塞打断。"}},
		captureSegmentTTS{},
	)

	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	}); err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	for i := 0; i < 15; i++ {
		if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrame(t),
		}); err != nil {
			t.Fatalf("dispatch audio: %v", err)
		}
	}
	returned := make(chan error, 1)
	go func() {
		err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
			State: xiaozhiapi.ClientStateListenStop,
		})
		returned <- err
	}()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("dispatch listen stop: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("listen stop dispatch should not block on ASR/LLM/TTS")
	}
	select {
	case <-asrStarted:
	case <-time.After(time.Second):
		t.Fatal("expected background ASR to start")
	}
	if err := h.handleAbort(context.Background(), &xiaozhiapi.ClientEventAbort{}); err != nil {
		t.Fatalf("handle abort: %v", err)
	}
	stop := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected abort tts stop, got %s", stop.State)
	}
	close(releaseASR)
}

func TestShortAudioDoesNotRunVoicePipeline(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	})
	if err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
		Bytes: validOpusFrame(t),
	})
	if err != nil {
		t.Fatalf("dispatch audio: %v", err)
	}
	err, _ = h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	})
	if err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}

	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("short audio should not emit response event, got %T", event)
	default:
	}
}

func TestAutoModeSilentAudioDoesNotRunVoicePipeline(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	for i := 0; i < 20; i++ {
		err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrameWithAmplitude(t, 0),
		})
		if err != nil {
			t.Fatalf("dispatch silent audio: %v", err)
		}
	}
	err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	})
	if err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}

	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("silent audio should not emit response event, got %T", event)
	default:
	}
}

func TestASRPunctuationOnlyTranscriptDoesNotRunTextPipeline(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"不应该回复"}}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "！！！"},
		llm,
		voice.SilentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("punctuation-only transcript should not emit events, got %T", event)
	default:
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called, got %d calls", llm.calls)
	}
}

func TestNoVoiceTimeoutSpeaksPromptAndClosesAfterChat(t *testing.T) {
	defaults := *config.Get()
	defaults.Local.NoVoicePrompt = "结束提示"
	restore := config.Replace(defaults)
	defer restore()

	h := NewHandler(context.Background(), ClientInfo{})
	<-h.Recv(context.Background())
	h.sleep = func(time.Duration) {}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "should-not-run"},
		fakeLLM{text: []string{"好的。"}},
		captureSegmentTTS{},
	)

	if err := h.processNoVoiceTimeout(context.Background()); err != nil {
		t.Fatalf("process no voice timeout: %v", err)
	}
	assertSTTStartSequence(t, h, "结束提示")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.State != xiaozhiapi.ServerTTSStateSentenceStart || sentenceStart.Text != "好的" {
		t.Fatalf("unexpected sentence_start: %+v", sentenceStart)
	}
	<-h.Recv(context.Background()) // audio
	<-h.Recv(context.Background()) // sentence_end
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", stop.State)
	}
	if _, ok := <-h.Recv(context.Background()); ok {
		t.Fatal("handler channel should close after no voice timeout chat")
	}
}

func TestSpeechResponseUsesSingleTTSStartAndStopForMultipleSegments(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.sleep = func(time.Duration) {}
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "hello"},
		fakeLLM{text: []string{"第一句。", "第二句。"}},
		voice.SilentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}

	assertSTTStartSequence(t, h, "hello")
	states := []xiaozhiapi.ServerTTSState{}
	texts := []string{}
	for i := 0; i < 5; i++ {
		event := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
		states = append(states, event.State)
		texts = append(texts, event.Text)
	}
	want := []xiaozhiapi.ServerTTSState{
		xiaozhiapi.ServerTTSStateSentenceStart,
		xiaozhiapi.ServerTTSStateSentenceEnd,
		xiaozhiapi.ServerTTSStateSentenceStart,
		xiaozhiapi.ServerTTSStateSentenceEnd,
		xiaozhiapi.ServerTTSStateStop,
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state[%d] got %s want %s; all=%v", i, states[i], want[i], states)
		}
	}
	if texts[0] != "第一句" || texts[2] != "第二句" {
		t.Fatalf("unexpected sentence texts: %v", texts)
	}
}

func TestTTSStopNotifyAudioIsSentBeforeStop(t *testing.T) {
	notifyPath := filepath.Join(t.TempDir(), "tts_notify.opus")
	if err := os.WriteFile(notifyPath, []byte("notify-frame"), 0o644); err != nil {
		t.Fatalf("write notify audio: %v", err)
	}

	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.TTS.StopNotify = config.NotifyConf{Enabled: true, Path: notifyPath}

	if err := h.sendSpeechResponse([]voice.SpeechSegment{{
		Text:  "回复",
		Audio: [][]byte{[]byte("speech-frame")},
	}}, h.speechSeq.Load()); err != nil {
		t.Fatalf("send speech response: %v", err)
	}

	<-h.Recv(context.Background()) // sentence_start
	<-h.Recv(context.Background()) // speech audio
	<-h.Recv(context.Background()) // sentence_end
	notify := (<-h.Recv(context.Background())).([]byte)
	if string(notify) != "notify-frame" {
		t.Fatalf("unexpected notify frame: %q", notify)
	}
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected stop after notify audio, got %s", stop.State)
	}
}

func TestEmptySpeechAudioStillSendsSentenceEventsAndStop(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())

	if err := h.sendSpeechResponse([]voice.SpeechSegment{{
		Text:  "空音频",
		Audio: nil,
	}}, h.speechSeq.Load()); err != nil {
		t.Fatalf("send speech response: %v", err)
	}

	start := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if start.State != xiaozhiapi.ServerTTSStateSentenceStart || start.Text != "空音频" {
		t.Fatalf("unexpected sentence_start: %+v", start)
	}
	end := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if end.State != xiaozhiapi.ServerTTSStateSentenceEnd || end.Text != "空音频" {
		t.Fatalf("unexpected sentence_end: %+v", end)
	}
	stop := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected tts stop, got %s", stop.State)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("empty speech audio should not emit binary frames, got %T", event)
	default:
	}
}

func TestLoadNotifyAudioEncodesWAVToOpusFrames(t *testing.T) {
	var wav bytes.Buffer
	_, err := audio.WriteWAV(&wav, []audio.PCMFrame{{
		SampleRate: 16000,
		Channels:   1,
		Samples:    make([]int16, 960),
	}})
	if err != nil {
		t.Fatalf("write wav: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tts_notify.wav")
	if err := os.WriteFile(path, wav.Bytes(), 0o644); err != nil {
		t.Fatalf("write notify wav: %v", err)
	}

	frames, err := loadCachedAudio(path, config.TTSConf{SampleRate: 16000, Channels: 1, FrameSize: 960})
	if err != nil {
		t.Fatalf("load notify wav: %v", err)
	}
	if len(frames) == 0 || len(frames[0]) == 0 {
		t.Fatalf("expected opus frames from wav, got %+v", frames)
	}
}

func TestLoadCachedAudioSplitsP3Packets(t *testing.T) {
	var p3 bytes.Buffer
	p3.Write([]byte{1, 0})
	_ = binary.Write(&p3, binary.BigEndian, uint16(6))
	p3.WriteString("frame1")
	p3.Write([]byte{1, 0})
	_ = binary.Write(&p3, binary.BigEndian, uint16(6))
	p3.WriteString("frame2")
	path := filepath.Join(t.TempDir(), "notify.p3")
	if err := os.WriteFile(path, p3.Bytes(), 0o644); err != nil {
		t.Fatalf("write p3: %v", err)
	}

	frames, err := loadCachedAudio(path, config.TTSConf{})
	if err != nil {
		t.Fatalf("load p3: %v", err)
	}
	if len(frames) != 2 || string(frames[0]) != "frame1" || string(frames[1]) != "frame2" {
		t.Fatalf("unexpected p3 frames: %+v", frames)
	}
}

func TestSentenceDelayUsesBaseAndLongSentenceExtra(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.runtime.SentenceDelay = config.SentenceDelayConf{
		Enabled:             true,
		BaseDelayMs:         200,
		Dynamic:             true,
		LengthThreshold:     3,
		LongSentenceExtraMs: 300,
	}
	var sleeps []time.Duration
	h.sleep = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}

	if err := h.sendSpeechResponse([]voice.SpeechSegment{
		{Text: "很长的一句", Audio: [][]byte{[]byte("a")}},
		{Text: "短句", Audio: [][]byte{[]byte("b")}},
	}, h.speechSeq.Load()); err != nil {
		t.Fatalf("send speech response: %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 500*time.Millisecond {
		t.Fatalf("unexpected sentence delays: %v", sleeps)
	}
}

func TestAbortCancelsInFlightSpeechResponse(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	var abortOnce sync.Once
	h.testAfterWrite = func(event any) {
		frame, ok := event.([]byte)
		if !ok || string(frame) != "frame-1" {
			return
		}
		abortOnce.Do(func() {
			if err := h.handleAbort(context.Background(), &xiaozhiapi.ClientEventAbort{}); err != nil {
				t.Errorf("handle abort: %v", err)
			}
		})
	}

	seq := h.speechSeq.Load()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.sendSpeechResponse([]voice.SpeechSegment{{
			Text: "长句",
			Audio: [][]byte{
				[]byte("frame-1"),
				[]byte("frame-2"),
			},
		}}, seq)
	}()

	start := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if start.State != xiaozhiapi.ServerTTSStateSentenceStart {
		t.Fatalf("expected sentence_start, got %s", start.State)
	}
	frame := (<-h.Recv(context.Background())).([]byte)
	if string(frame) != "frame-1" {
		t.Fatalf("unexpected first frame: %q", frame)
	}
	stop := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if stop.State != xiaozhiapi.ServerTTSStateStop {
		t.Fatalf("expected abort tts stop, got %s", stop.State)
	}
	end := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if end.State != xiaozhiapi.ServerTTSStateSentenceEnd || end.Text != "长句" {
		t.Fatalf("expected python-compatible sentence_end after abort stop, got %+v", end)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("send speech response: %v", err)
	}
	select {
	case event := <-h.Recv(context.Background()):
		t.Fatalf("canceled speech should not emit more events, got %T", event)
	default:
	}
}

func TestUnboundPrivateDeviceSpeaksAuthCodeWithoutCallingLLM(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{DeviceID: "device-1"})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	llm := &countingLLM{text: []string{"normal reply"}}
	h.runtime.Enabled = true
	h.runtime.Device.Owner = ""
	h.runtime.Device.AuthCode = "123456"
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "hello"},
		llm,
		captureSegmentTTS{},
	)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}

	assertSTTStartSequence(t, h, "hello")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "请在后台输入验证码：1 2 3 4 5 6" {
		t.Fatalf("unexpected auth prompt: %q", sentenceStart.Text)
	}
	if llm.calls != 0 {
		t.Fatalf("llm should not be called for unbound devices, got %d calls", llm.calls)
	}
	if len(h.history) != 0 {
		t.Fatalf("unbound auth prompt should not be saved as dialogue: %+v", h.history)
	}
}

func TestTrimSTTDisplayTextMatchesPythonEdgeTrim(t *testing.T) {
	tests := map[string]string{
		" 小智。":        "小智",
		"　小智　":        "小智",
		"\n\t你好\r\n":  "你好",
		"😊你好，小智！":     "你好，小智",
		"hello-world": "hello-world",
		"！！":          "",
	}
	for input, want := range tests {
		if got := trimSTTDisplayText(input); got != want {
			t.Fatalf("trimSTTDisplayText(%q)=%q want %q", input, got, want)
		}
	}
}

func TestRemovePunctuationLikePython(t *testing.T) {
	tests := map[string]string{
		" 你好，小智！": "你好小智",
		"全角　空格":   "全角空格",
		"a-b.c":   "abc",
		"Yeah":    "",
		"你好😊":     "你好😊",
		"再见（吧）":   "再见吧",
	}
	for input, want := range tests {
		if got := removePunctuationLikePython(input); got != want {
			t.Fatalf("removePunctuationLikePython(%q)=%q want %q", input, got, want)
		}
	}
}

func TestProcessTextSavesRawAssistantTextLikePythonDialogue(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	<-h.Recv(context.Background())
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "unused"},
		fakeLLM{text: []string{"**你好**，[看这里](https://example.com)。"}},
		captureSegmentTTS{},
	)

	if err := h.processText(context.Background(), "用户语音"); err != nil {
		t.Fatalf("process text: %v", err)
	}
	assertSTTStartSequence(t, h, "用户语音")
	sentenceStart := (<-h.Recv(context.Background())).(*xiaozhiapi.ServerEventTTS)
	if sentenceStart.Text != "你好，看这里" {
		t.Fatalf("tts should use cleaned text, got %q", sentenceStart.Text)
	}

	want := []voice.Message{
		{Role: "user", Content: "用户语音"},
		{Role: "assistant", Content: "**你好**，[看这里](https://example.com)。"},
	}
	if len(h.history) != len(want) {
		t.Fatalf("history length got %d want %d: %+v", len(h.history), len(want), h.history)
	}
	for i := range want {
		if h.history[i].Role != want[i].Role || h.history[i].Content != want[i].Content {
			t.Fatalf("history[%d] got %+v want %+v", i, h.history[i], want[i])
		}
	}
}

func TestAssistantHistoryContentEmptyForSideEffectOnlyResponse(t *testing.T) {
	content := assistantHistoryContent(voice.Response{
		Transcript: "播放音乐",
		Emotion:    "happy",
	})
	if content != "" {
		t.Fatalf("side-effect-only response should not create assistant history content: %q", content)
	}
}

func TestCloseSavesDialogueMemory(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{DeviceID: "device-1"})
	<-h.Recv(context.Background())
	mem := &fakeMemory{}
	h.memory = mem
	h.pipeline = voice.NewPipeline(
		fakeASR{text: "hello"},
		fakeLLM{text: []string{"reply"}},
		voice.SilentTTS{},
	).WithMemory(mem)

	if err := h.processUtterance(context.Background(), []voice.AudioFrame{{PCM: pcmFrame()}}); err != nil {
		t.Fatalf("process utterance: %v", err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("close handler: %v", err)
	}

	want := []voice.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "reply"},
	}
	if len(mem.saved) != len(want) {
		t.Fatalf("saved memory length got %d want %d: %+v", len(mem.saved), len(want), mem.saved)
	}
	for i := range want {
		if mem.saved[i].Role != want[i].Role || mem.saved[i].Content != want[i].Content {
			t.Fatalf("saved[%d] got %+v want %+v", i, mem.saved[i], want[i])
		}
	}
}

func TestCloseWhileBackgroundProcessingDoesNotPanic(t *testing.T) {
	h := NewHandler(context.Background(), ClientInfo{})
	<-h.Recv(context.Background())
	asrStarted := make(chan struct{})
	releaseASR := make(chan struct{})
	h.pipeline = voice.NewPipeline(
		&blockingASR{started: asrStarted, release: releaseASR, text: "关闭后文本"},
		fakeLLM{text: []string{"关闭后回复。"}},
		captureSegmentTTS{},
	)

	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStart,
		Mode:  xiaozhiapi.ClientModeManual,
	}); err != nil {
		t.Fatalf("dispatch listen start: %v", err)
	}
	for i := 0; i < 15; i++ {
		if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventAppendBuffer{
			Bytes: validOpusFrame(t),
		}); err != nil {
			t.Fatalf("dispatch audio: %v", err)
		}
	}
	if err, _ := h.DispatchClientEvent(context.Background(), &xiaozhiapi.ClientEventListen{
		State: xiaozhiapi.ClientStateListenStop,
	}); err != nil {
		t.Fatalf("dispatch listen stop: %v", err)
	}
	select {
	case <-asrStarted:
	case <-time.After(time.Second):
		t.Fatal("expected background ASR to start")
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("close handler: %v", err)
	}
	close(releaseASR)
	time.Sleep(10 * time.Millisecond)
}

type fakeASR struct {
	text string
}

func (a fakeASR) Transcribe(ctx context.Context, sessionID string, frames []voice.AudioFrame) (string, error) {
	return a.text, nil
}

type sequentialASR struct {
	texts []string
	calls int
}

func (a *sequentialASR) Transcribe(ctx context.Context, sessionID string, frames []voice.AudioFrame) (string, error) {
	var text string
	if a.calls < len(a.texts) {
		text = a.texts[a.calls]
	}
	a.calls++
	return text, nil
}

type blockingASR struct {
	started chan struct{}
	release chan struct{}
	text    string
	once    sync.Once
}

func (a *blockingASR) Transcribe(ctx context.Context, sessionID string, frames []voice.AudioFrame) (string, error) {
	a.once.Do(func() {
		close(a.started)
	})
	select {
	case <-a.release:
		return a.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type fakeLLM struct {
	text []string
}

func (l fakeLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	ch := make(chan string, len(l.text))
	for _, text := range l.text {
		ch <- text
	}
	close(ch)
	return ch, nil
}

type countingLLM struct {
	text  []string
	calls int
}

func (l *countingLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	l.calls++
	ch := make(chan string, len(l.text))
	for _, text := range l.text {
		ch <- text
	}
	close(ch)
	return ch, nil
}

type captureHistoryLLM struct {
	chunks  []string
	history []voice.Message
}

func (l *captureHistoryLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	l.history = append([]voice.Message(nil), history...)
	ch := make(chan string, len(l.chunks))
	for _, chunk := range l.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

type sequentialLLM struct {
	responses [][]string
	calls     int
}

func (l *sequentialLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	var text []string
	if l.calls < len(l.responses) {
		text = l.responses[l.calls]
	}
	l.calls++
	ch := make(chan string, len(text))
	for _, part := range text {
		ch <- part
	}
	close(ch)
	return ch, nil
}

type fakeToolLLM struct {
	events []voice.LLMEvent
}

func (l *fakeToolLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (l *fakeToolLLM) RespondWithTools(ctx context.Context, sessionID string, history []voice.Message, tools []voice.Tool) (<-chan voice.LLMEvent, error) {
	ch := make(chan voice.LLMEvent, len(l.events))
	for _, event := range l.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

type modeAwareLLM struct {
	plain      []string
	tool       []voice.LLMEvent
	plainCalls int
	toolCalls  int
}

func (l *modeAwareLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	l.plainCalls++
	ch := make(chan string, len(l.plain))
	for _, text := range l.plain {
		ch <- text
	}
	close(ch)
	return ch, nil
}

func (l *modeAwareLLM) RespondWithTools(ctx context.Context, sessionID string, history []voice.Message, tools []voice.Tool) (<-chan voice.LLMEvent, error) {
	l.toolCalls++
	ch := make(chan voice.LLMEvent, len(l.tool))
	for _, event := range l.tool {
		ch <- event
	}
	close(ch)
	return ch, nil
}

type sequentialToolLLM struct {
	responses [][]voice.LLMEvent
	histories [][]voice.Message
	calls     int
}

func (l *sequentialToolLLM) Respond(ctx context.Context, sessionID string, history []voice.Message) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (l *sequentialToolLLM) RespondWithTools(ctx context.Context, sessionID string, history []voice.Message, tools []voice.Tool) (<-chan voice.LLMEvent, error) {
	l.histories = append(l.histories, append([]voice.Message(nil), history...))
	var events []voice.LLMEvent
	if l.calls < len(l.responses) {
		events = l.responses[l.calls]
	}
	l.calls++
	ch := make(chan voice.LLMEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

type captureSegmentTTS struct{}

func (captureSegmentTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	return [][]byte{[]byte(text)}, nil
}

// countingSegmentTTS 记录每次合成的文本（计数），用于断言流式取消后是否还合成了后续段。
type countingSegmentTTS struct {
	mu    sync.Mutex
	texts []string
	gate  chan struct{} // 可选：第二段起阻塞于此直到关闭
}

func (t *countingSegmentTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	t.mu.Lock()
	t.texts = append(t.texts, text)
	count := len(t.texts)
	t.mu.Unlock()
	// 第二段起阻塞在 gate 上，直到测试放行——用于证明首段在后续段合成完前已发送。
	if count >= 2 && t.gate != nil {
		select {
		case <-t.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return [][]byte{[]byte(text)}, nil
}

// gatedSegmentTTS 第一段立即返回，第二段阻塞在 gate 上直到关闭。
// 用于证明流式发送：首段能在整段回复（含第二段）合成完毕之前抵达 writeQ。
type gatedSegmentTTS struct {
	gate chan struct{}
	once sync.Once
}

func (t *gatedSegmentTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	if text == "第二句" {
		select {
		case <-t.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return [][]byte{[]byte(text)}, nil
}

type fakeMemory struct {
	query string
	saved []voice.Message
}

func (m *fakeMemory) Init(ctx context.Context, roleID string) error {
	return nil
}

func (m *fakeMemory) Query(ctx context.Context, query string) (string, error) {
	m.query = query
	return "", nil
}

func (m *fakeMemory) Save(ctx context.Context, messages []voice.Message) (string, error) {
	m.saved = append([]voice.Message(nil), messages...)
	return "", nil
}

func (m *fakeMemory) Close(ctx context.Context) error {
	return nil
}

type failingMemory struct {
	err error
}

func (m failingMemory) Init(ctx context.Context, roleID string) error {
	return m.err
}

func (m failingMemory) Query(ctx context.Context, query string) (string, error) {
	return "", m.err
}

func (m failingMemory) Save(ctx context.Context, messages []voice.Message) (string, error) {
	return "", m.err
}

func (m failingMemory) Close(ctx context.Context) error {
	return nil
}

func pcmFrame() *audio.PCMFrame {
	return &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1, 2, 3}}
}

func containsJSONField(data []byte, field string) bool {
	return strings.Contains(string(data), field)
}

func assertSTTStartSequence(t *testing.T, h *Handler, text string) {
	t.Helper()

	stt := recvAs[*xiaozhiapi.ServerEventSTT](t, h)
	if stt.Text != text {
		t.Fatalf("unexpected stt text: %q", stt.Text)
	}
	llm := recvAs[*xiaozhiapi.ServerEventLLM](t, h)
	if llm.Text != "😊" || llm.Emotion != "happy" {
		t.Fatalf("unexpected llm event: %+v", llm)
	}
	start := recvAs[*xiaozhiapi.ServerEventTTS](t, h)
	if start.State != xiaozhiapi.ServerTTSStateStart {
		t.Fatalf("expected tts start, got %s", start.State)
	}
}

func recvAs[T any](t *testing.T, h *Handler) T {
	t.Helper()

	select {
	case event, ok := <-h.Recv(context.Background()):
		if !ok {
			var zero T
			t.Fatalf("handler channel closed while waiting for %T", zero)
		}
		typed, ok := event.(T)
		if !ok {
			var zero T
			t.Fatalf("expected %T, got %T", zero, event)
		}
		return typed
	case <-time.After(time.Second):
		var zero T
		t.Fatalf("timed out waiting for %T", zero)
		return zero
	}
}

func drainUntilTTSState(t *testing.T, h *Handler, state xiaozhiapi.ServerTTSState) (*xiaozhiapi.ServerEventTTS, bool) {
	t.Helper()

	sawAudio := false
	for {
		select {
		case event := <-h.Recv(context.Background()):
			if audioFrame, ok := event.([]byte); ok {
				if len(audioFrame) == 0 {
					t.Fatal("binary audio frame is empty")
				}
				sawAudio = true
				continue
			}
			tts, ok := event.(*xiaozhiapi.ServerEventTTS)
			if !ok {
				t.Fatalf("unexpected event while waiting for %s: %T", state, event)
			}
			if tts.State == state {
				return tts, sawAudio
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for tts state %s", state)
		}
	}
}

func validOpusFrame(t *testing.T) []byte {
	t.Helper()
	return validOpusFrameWithAmplitude(t, 4000)
}

func validOpusFrameWithAmplitude(t *testing.T, amplitude int16) []byte {
	t.Helper()

	encoder, err := opus.NewEncoder(16000, 1, opus.AppVoIP)
	if err != nil {
		t.Fatalf("new opus encoder: %v", err)
	}
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = amplitude
	}
	buf := make([]byte, 1275)
	n, err := encoder.Encode(pcm, buf)
	if err != nil {
		t.Fatalf("encode opus frame: %v", err)
	}
	return append([]byte(nil), buf[:n]...)
}

func runHandlerMCPHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		switch req.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "Echo text",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string"},
							},
							"required": []string{"text"},
						},
					}},
				},
			})
		case "tools/call":
			args, _ := req.Params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			if text == "fail" {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"message": "boom"},
				})
				continue
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("echo: %s", text)}},
				},
			})
		}
	}
	os.Exit(0)
}
