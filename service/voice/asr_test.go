package voice

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
)

func TestCommandASRHelper(t *testing.T) {
	if os.Getenv("GO_XIAOZHI_ASR_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" && len(args) > i+2 {
			filePath := args[i+1]
			sessionID := args[i+2]
			data, err := os.ReadFile(filePath)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "read wav: %v", err)
				os.Exit(2)
			}
			if len(data) < 4 || string(data[:4]) != "RIFF" {
				_, _ = fmt.Fprint(os.Stderr, "input is not wav")
				os.Exit(3)
			}
			if os.Getenv("GO_XIAOZHI_ASR_MODEL") == "" {
				_, _ = fmt.Fprint(os.Stderr, "missing model env")
				os.Exit(4)
			}
			_, _ = fmt.Fprintf(os.Stdout, " transcript:%s:%s ", sessionID, filepath.Base(filePath))
			os.Exit(0)
		}
	}
	_, _ = fmt.Fprint(os.Stderr, "missing helper args")
	os.Exit(5)
}

func TestFileASRSavesWAVAndReturnsTranscript(t *testing.T) {
	dir := t.TempDir()
	asr := &FileASR{
		OutputDir:  dir,
		Transcript: "hello",
	}

	text, err := asr.Transcribe(context.Background(), "session/1", []AudioFrame{{
		PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1, 2, 3}},
	}})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hello" {
		t.Fatalf("unexpected transcript: %q", text)
	}
	if asr.LastFilePath == "" {
		t.Fatal("expected wav path to be recorded")
	}
	if !strings.HasPrefix(filepath.Base(asr.LastFilePath), "asr_session_1_") {
		t.Fatalf("session id should be sanitized in file name: %s", asr.LastFilePath)
	}
	data, err := os.ReadFile(asr.LastFilePath)
	if err != nil {
		t.Fatalf("read saved wav: %v", err)
	}
	if string(data[:4]) != "RIFF" {
		t.Fatalf("saved file is not wav: %q", data[:4])
	}
}

func TestFileASRNormalizesWAVFormat(t *testing.T) {
	dir := t.TempDir()
	asr := &FileASR{
		OutputDir: dir,
	}
	text, err := asr.Transcribe(context.Background(), "s1", []AudioFrame{{
		PCM: &audio.PCMFrame{
			SampleRate: 24000,
			Channels:   2,
			Samples: []int16{
				100, 300,
				200, 400,
				300, 500,
				400, 600,
			},
		},
	}})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text == "" {
		t.Fatal("expected stub transcript")
	}
	file, err := os.Open(asr.LastFilePath)
	if err != nil {
		t.Fatalf("open saved wav: %v", err)
	}
	defer file.Close()
	_, info, err := audio.ReadWAV(file)
	if err != nil {
		t.Fatalf("read saved wav: %v", err)
	}
	if info.SampleRate != 16000 || info.Channels != 1 {
		t.Fatalf("wav format got sample_rate=%d channels=%d, want 16000/1", info.SampleRate, info.Channels)
	}
}

func TestFileASRDeletesAudioWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	asr := &FileASR{
		OutputDir:   dir,
		DeleteAudio: true,
	}
	_, err := asr.Transcribe(context.Background(), "s1", []AudioFrame{{
		PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: []int16{1}},
	}})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if _, err := os.Stat(asr.LastFilePath); !os.IsNotExist(err) {
		t.Fatalf("expected audio file to be deleted, stat err=%v", err)
	}
}

func TestCommandASRExecutesCommandWithWAVPathAndDeletesAudio(t *testing.T) {
	dir := t.TempDir()
	asr := &CommandASR{
		OutputDir:   dir,
		DeleteAudio: true,
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestCommandASRHelper", "--", "{file}", "{session_id}"},
		Env:         map[string]string{"GO_XIAOZHI_ASR_HELPER": "1", "GO_XIAOZHI_ASR_MODEL": "iic/SenseVoiceSmall"},
	}

	text, err := asr.Transcribe(context.Background(), "session/1", []AudioFrame{{
		PCM: &audio.PCMFrame{SampleRate: 24000, Channels: 2, Samples: []int16{1, 2, 3, 4}},
	}})
	if err != nil {
		t.Fatalf("transcribe command asr: %v", err)
	}
	if !strings.HasPrefix(text, "transcript:session/1:asr_session_1_") {
		t.Fatalf("unexpected command transcript: %q", text)
	}
	if !strings.HasSuffix(text, ".wav") {
		t.Fatalf("expected wav file name in transcript: %q", text)
	}
	if _, err := os.Stat(asr.LastFilePath); !os.IsNotExist(err) {
		t.Fatalf("expected command asr audio file to be deleted, stat err=%v", err)
	}
}

func TestOpenAIASRUploadsWAVAndParsesTranscript(t *testing.T) {
	var gotAuth string
	var gotModel string
	var gotLanguage string
	var gotFormat string
	var gotFilePrefix string
	var uploadedInfo audio.WAVInfo
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLanguage = r.FormValue("language")
		gotFormat = r.FormValue("response_format")
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if len(data) >= 4 {
			gotFilePrefix = string(data[:4])
		}
		_, info, err := audio.ReadWAV(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("read uploaded wav: %v", err)
		}
		uploadedInfo = info
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":" 你好 "}`))
	}))
	defer server.Close()

	asr := &OpenAIASR{
		OutputDir:      t.TempDir(),
		APIURL:         server.URL,
		APIKey:         "secret",
		Model:          "whisper-test",
		Language:       "zh",
		ResponseFormat: "json",
	}
	text, err := asr.Transcribe(context.Background(), "s1", []AudioFrame{{
		PCM: &audio.PCMFrame{SampleRate: 24000, Channels: 2, Samples: []int16{1, 2, 3, 4, 5, 6}},
	}})
	if err != nil {
		t.Fatalf("transcribe openai asr: %v", err)
	}
	if text != "你好" {
		t.Fatalf("unexpected transcript: %q", text)
	}
	if gotAuth != "Bearer secret" || gotModel != "whisper-test" || gotLanguage != "zh" || gotFormat != "json" {
		t.Fatalf("unexpected request auth=%q model=%q language=%q format=%q", gotAuth, gotModel, gotLanguage, gotFormat)
	}
	if gotFilePrefix != "RIFF" {
		t.Fatalf("uploaded file is not wav: %q", gotFilePrefix)
	}
	if uploadedInfo.SampleRate != 16000 || uploadedInfo.Channels != 1 {
		t.Fatalf("uploaded wav format got sample_rate=%d channels=%d, want 16000/1", uploadedInfo.SampleRate, uploadedInfo.Channels)
	}
}

func TestOpenAIASRParsesVerboseJSONResponse(t *testing.T) {
	var gotFormat string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		gotFormat = r.FormValue("response_format")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":" verbose transcript ","language":"zh"}`))
	}))
	defer server.Close()

	asr := &OpenAIASR{
		APIURL:         server.URL,
		Model:          "whisper-test",
		ResponseFormat: "verbose_json",
	}
	text, err := asr.transcribeWAV(context.Background(), "audio.wav", []byte("RIFFdata"))
	if err != nil {
		t.Fatalf("transcribe wav: %v", err)
	}
	if text != " verbose transcript " {
		t.Fatalf("unexpected transcript: %q", text)
	}
	if gotFormat != "verbose_json" {
		t.Fatalf("response_format got %q want verbose_json", gotFormat)
	}
}

func TestOpenAIASRReturnsTextResponseFormatsRaw(t *testing.T) {
	for _, format := range []string{"text", "srt", "vtt"} {
		t.Run(format, func(t *testing.T) {
			body := "1\n00:00:00,000 --> 00:00:01,000\nhello\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatalf("parse multipart: %v", err)
				}
				if got := r.FormValue("response_format"); got != format {
					t.Fatalf("response_format got %q want %q", got, format)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			asr := &OpenAIASR{
				APIURL:         server.URL,
				Model:          "whisper-test",
				ResponseFormat: format,
			}
			text, err := asr.transcribeWAV(context.Background(), "audio.wav", []byte("RIFFdata"))
			if err != nil {
				t.Fatalf("transcribe wav: %v", err)
			}
			if text != body {
				t.Fatalf("raw response got %q want %q", text, body)
			}
		})
	}
}
