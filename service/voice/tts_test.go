package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestCommandTTSHelper(t *testing.T) {
	if os.Getenv("GO_XIAOZHI_TTS_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" && len(args) > i+3 {
			text := args[i+1]
			sessionID := args[i+2]
			outputPath := args[i+3]
			if text == "" || sessionID == "" || os.Getenv("GO_XIAOZHI_TTS_VOICE") == "" {
				_, _ = fmt.Fprint(os.Stderr, "missing helper input")
				os.Exit(2)
			}
			file, err := os.Create(outputPath)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "create output: %v", err)
				os.Exit(3)
			}
			_, err = audio.WriteWAV(file, []audio.PCMFrame{{
				SampleRate: 8000,
				Channels:   1,
				Samples:    []int16{0, 1000, -1000, 0, 1000, -1000, 0, 1000},
			}})
			closeErr := file.Close()
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write wav: %v", err)
				os.Exit(4)
			}
			if closeErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "close wav: %v", closeErr)
				os.Exit(5)
			}
			os.Exit(0)
		}
	}
	_, _ = fmt.Fprint(os.Stderr, "missing helper args")
	os.Exit(6)
}

func TestStubTTSSynthesizesDecodableOpus(t *testing.T) {
	tts := StubTTS{
		SampleRate: 16000,
		Channels:   1,
		FrameSize:  960,
		DurationMs: 120,
		Amplitude:  1200,
	}
	packets, err := tts.Synthesize(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}

	decoder, err := audio.NewOpusDecoder(audio.OpusDecoderConfig{SampleRate: 16000, Channels: 1, FrameSize: 960})
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	frame, err := decoder.Decode(packets[0])
	if err != nil {
		t.Fatalf("decode synthesized packet: %v", err)
	}
	if len(frame.Samples) == 0 {
		t.Fatal("decoded synthesized packet has no samples")
	}
}

func TestNewCommandTTSDeleteAudioDefaultsToCleanup(t *testing.T) {
	tts, ok := NewTTS(config.TTSConf{Type: "command", Command: "tts-helper"}).(*CommandTTS)
	if !ok {
		t.Fatalf("new tts type = %T, want *CommandTTS", tts)
	}
	if !tts.DeleteAudio {
		t.Fatal("command tts should delete generated audio by default")
	}
}

func TestNewCommandTTSHonorsDeleteAudioConfig(t *testing.T) {
	deleteAudio := false
	tts, ok := NewTTS(config.TTSConf{
		Type:        "command",
		Command:     "tts-helper",
		DeleteAudio: &deleteAudio,
	}).(*CommandTTS)
	if !ok {
		t.Fatalf("new tts type = %T, want *CommandTTS", tts)
	}
	if tts.DeleteAudio {
		t.Fatal("command tts should preserve generated audio when delete_audio=false")
	}
}

func TestOpenAITTSSynthesizesWAVResponseToOpus(t *testing.T) {
	var gotAuth string
	var gotRequest openAITTSRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var wav bytes.Buffer
		_, err := audio.WriteWAV(&wav, []audio.PCMFrame{{
			SampleRate: 8000,
			Channels:   1,
			Samples:    []int16{0, 1000, -1000, 0, 1000, -1000, 0, 1000},
		}})
		if err != nil {
			t.Fatalf("write wav: %v", err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav.Bytes())
	}))
	defer server.Close()

	tts := &OpenAITTS{
		APIURL:         server.URL,
		APIKey:         "secret",
		Model:          "tts-test",
		Voice:          "alloy",
		ResponseFormat: "wav",
		Speed:          1.25,
		SampleRate:     16000,
		Channels:       1,
		FrameSize:      960,
	}
	packets, err := tts.Synthesize(context.Background(), "s1", "你好")
	if err != nil {
		t.Fatalf("synthesize openai tts: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("unexpected auth: %q", gotAuth)
	}
	if gotRequest.Model != "tts-test" || gotRequest.Input != "你好" || gotRequest.ResponseFormat != "wav" {
		t.Fatalf("unexpected request: %+v", gotRequest)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
	decoder, err := audio.NewOpusDecoder(audio.OpusDecoderConfig{SampleRate: 16000, Channels: 1, FrameSize: 960})
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	frame, err := decoder.Decode(packets[0])
	if err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	if len(frame.Samples) == 0 {
		t.Fatal("decoded packet has no samples")
	}
}

func TestOpenAITTSRetriesTransientFailuresLikePythonProvider(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, fmt.Sprintf("temporary failure %d", attempts), http.StatusInternalServerError)
			return
		}
		var wav bytes.Buffer
		_, err := audio.WriteWAV(&wav, []audio.PCMFrame{{
			SampleRate: 16000,
			Channels:   1,
			Samples:    []int16{0, 1000, -1000, 0},
		}})
		if err != nil {
			t.Fatalf("write wav: %v", err)
		}
		_, _ = w.Write(wav.Bytes())
	}))
	defer server.Close()

	tts := &OpenAITTS{
		APIURL:         server.URL,
		Model:          "tts-test",
		Voice:          "alloy",
		ResponseFormat: "wav",
		SampleRate:     16000,
		Channels:       1,
		FrameSize:      960,
	}
	packets, err := tts.Synthesize(context.Background(), "s1", "你好")
	if err != nil {
		t.Fatalf("synthesize with retries: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts got %d want 3", attempts)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets after retry")
	}
}

func TestOpenAITTSReturnsLastFailureAfterRetryBudget(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "still failing", http.StatusBadGateway)
	}))
	defer server.Close()

	tts := &OpenAITTS{
		APIURL:         server.URL,
		Model:          "tts-test",
		Voice:          "alloy",
		ResponseFormat: "wav",
	}
	_, err := tts.Synthesize(context.Background(), "s1", "你好")
	if err == nil {
		t.Fatal("expected retry exhaustion error")
	}
	if attempts != openAITTSMaxAttempts {
		t.Fatalf("attempts got %d want %d", attempts, openAITTSMaxAttempts)
	}
	if got := err.Error(); !strings.Contains(got, "failed after 5 attempts") || !strings.Contains(got, "status=502") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCustomTTSSendsPythonCompatibleGETAndConvertsWAVToOpus(t *testing.T) {
	var gotAuth string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.Query().Get("text")
		if got := r.URL.Query().Get("speaker"); got != "jok" {
			t.Fatalf("speaker query got %q", got)
		}
		var wav bytes.Buffer
		_, err := audio.WriteWAV(&wav, []audio.PCMFrame{{
			SampleRate: 8000,
			Channels:   1,
			Samples:    []int16{0, 1000, -1000, 0, 1000, -1000, 0, 1000},
		}})
		if err != nil {
			t.Fatalf("write wav: %v", err)
		}
		_, _ = w.Write(wav.Bytes())
	}))
	defer server.Close()

	tts := &CustomTTS{
		URL: server.URL,
		Params: map[string]any{
			"text":    "{prompt_text}",
			"speaker": "jok",
		},
		Headers: map[string]string{
			"Authorization": "Bearer custom-token",
		},
		Format:     "wav",
		SampleRate: 16000,
		Channels:   1,
		FrameSize:  960,
	}
	packets, err := tts.Synthesize(context.Background(), "s1", "你好")
	if err != nil {
		t.Fatalf("synthesize custom tts: %v", err)
	}
	if gotAuth != "Bearer custom-token" {
		t.Fatalf("authorization header got %q", gotAuth)
	}
	if gotQuery != "你好" {
		t.Fatalf("text query got %q", gotQuery)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
}

func TestCustomTTSUsesFFmpegForNonWAVFormats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not real mp3"))
	}))
	defer server.Close()

	tts := &CustomTTS{URL: server.URL, Format: "mp3"}
	_, err := tts.Synthesize(context.Background(), "s1", "你好")
	if err == nil {
		t.Fatal("expected invalid mp3 decode error")
	}
	if !strings.Contains(err.Error(), "ffmpeg decode mp3 audio") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCommandTTSExecutesCommandAndConvertsOutputToOpus(t *testing.T) {
	dir := t.TempDir()
	tts := &CommandTTS{
		OutputDir:   dir,
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestCommandTTSHelper", "--", "{text}", "{session_id}", "{output}"},
		Env:         map[string]string{"GO_XIAOZHI_TTS_HELPER": "1", "GO_XIAOZHI_TTS_VOICE": "zh-CN-XiaoxiaoNeural"},
		Format:      "wav",
		SampleRate:  16000,
		Channels:    1,
		FrameSize:   960,
		DeleteAudio: true,
	}

	packets, err := tts.Synthesize(context.Background(), "session/1", "你好")
	if err != nil {
		t.Fatalf("synthesize command tts: %v", err)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected command tts output to be removed, found %d entries in %s", len(entries), filepath.Base(dir))
	}
	decoder, err := audio.NewOpusDecoder(audio.OpusDecoderConfig{SampleRate: 16000, Channels: 1, FrameSize: 960})
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	frame, err := decoder.Decode(packets[0])
	if err != nil {
		t.Fatalf("decode command tts packet: %v", err)
	}
	if len(frame.Samples) == 0 {
		t.Fatal("decoded command tts packet has no samples")
	}
}

func TestCommandTTSPreservesOutputWhenDeleteAudioDisabled(t *testing.T) {
	dir := t.TempDir()
	tts := &CommandTTS{
		OutputDir:   dir,
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestCommandTTSHelper", "--", "{text}", "{session_id}", "{output}"},
		Env:         map[string]string{"GO_XIAOZHI_TTS_HELPER": "1", "GO_XIAOZHI_TTS_VOICE": "zh-CN-XiaoxiaoNeural"},
		Format:      "wav",
		SampleRate:  16000,
		Channels:    1,
		FrameSize:   960,
		DeleteAudio: false,
	}

	packets, err := tts.Synthesize(context.Background(), "session/1", "你好")
	if err != nil {
		t.Fatalf("synthesize command tts: %v", err)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected command tts output to be preserved, found %d entries in %s", len(entries), filepath.Base(dir))
	}
	if entries[0].IsDir() {
		t.Fatalf("expected preserved output file, got directory %s", entries[0].Name())
	}
}
