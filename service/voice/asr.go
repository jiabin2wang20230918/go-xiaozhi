package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

type FileASR struct {
	OutputDir    string
	Transcript   string
	DeleteAudio  bool
	SampleRate   int
	Channels     int
	LastFilePath string
}

type OpenAIASR struct {
	OutputDir      string
	DeleteAudio    bool
	APIURL         string
	APIKey         string
	Model          string
	Language       string
	Prompt         string
	ResponseFormat string
	SampleRate     int
	Channels       int
	LastFilePath   string
	HTTPClient     *http.Client
}

type CommandASR struct {
	OutputDir    string
	DeleteAudio  bool
	Command      string
	Args         []string
	Env          map[string]string
	SampleRate   int
	Channels     int
	LastFilePath string
}

func NewASR(conf config.ASRConf) ASR {
	switch conf.Type {
	case "", "file_stub":
		return &FileASR{
			OutputDir:   conf.OutputDir,
			Transcript:  conf.StubTranscript,
			DeleteAudio: conf.DeleteAudio,
			SampleRate:  conf.SampleRate,
			Channels:    conf.Channels,
		}
	case "openai":
		return &OpenAIASR{
			OutputDir:      conf.OutputDir,
			DeleteAudio:    conf.DeleteAudio,
			APIURL:         conf.APIURL,
			APIKey:         conf.APIKey,
			Model:          conf.Model,
			Language:       conf.Language,
			Prompt:         conf.Prompt,
			ResponseFormat: conf.ResponseFormat,
			SampleRate:     conf.SampleRate,
			Channels:       conf.Channels,
		}
	case "command":
		return &CommandASR{
			OutputDir:   conf.OutputDir,
			DeleteAudio: conf.DeleteAudio,
			Command:     conf.Command,
			Args:        append([]string(nil), conf.Args...),
			Env:         cloneStringMap(conf.Env),
			SampleRate:  conf.SampleRate,
			Channels:    conf.Channels,
		}
	default:
		return &FileASR{
			OutputDir:   conf.OutputDir,
			Transcript:  conf.StubTranscript,
			DeleteAudio: conf.DeleteAudio,
			SampleRate:  conf.SampleRate,
			Channels:    conf.Channels,
		}
	}
}

func (a *CommandASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	if !hasPCM(frames) {
		return "", nil
	}
	fileASR := &FileASR{OutputDir: a.OutputDir, SampleRate: a.SampleRate, Channels: a.Channels}
	path, err := fileASR.SaveAudioToFile(sessionID, frames)
	if err != nil {
		return "", err
	}
	a.LastFilePath = path
	if a.DeleteAudio {
		defer func() { _ = os.Remove(path) }()
	}

	command := strings.TrimSpace(a.Command)
	if command == "" {
		return "", fmt.Errorf("asr command is required")
	}
	args := expandASRPlaceholders(a.Args, path, sessionID)
	cmd := exec.CommandContext(ctx, command, args...)
	if len(a.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range a.Env {
			cmd.Env = append(cmd.Env, key+"="+expandASRPlaceholder(value, path, sessionID))
		}
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(output))
		}
		if msg == "" {
			return "", fmt.Errorf("asr command failed: %w", err)
		}
		return "", fmt.Errorf("asr command failed: %w: %s", err, msg)
	}
	return strings.TrimSpace(string(output)), nil
}

func (a *OpenAIASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	if !hasPCM(frames) {
		return "", nil
	}
	fileASR := &FileASR{OutputDir: a.OutputDir, SampleRate: a.SampleRate, Channels: a.Channels}
	path, err := fileASR.SaveAudioToFile(sessionID, frames)
	if err != nil {
		return "", err
	}
	a.LastFilePath = path
	if a.DeleteAudio {
		defer func() { _ = os.Remove(path) }()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text, err := a.transcribeWAV(ctx, filepath.Base(path), data)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (a *OpenAIASR) transcribeWAV(ctx context.Context, fileName string, data []byte) (string, error) {
	apiURL := strings.TrimSpace(a.APIURL)
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1/audio/transcriptions"
	}
	model := strings.TrimSpace(a.Model)
	if model == "" {
		model = "whisper-1"
	}
	format := strings.TrimSpace(a.ResponseFormat)
	if format == "" {
		format = "json"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", model); err != nil {
		return "", err
	}
	if a.Language != "" {
		if err := writer.WriteField("language", a.Language); err != nil {
			return "", err
		}
	}
	if a.Prompt != "" {
		if err := writer.WriteField("prompt", a.Prompt); err != nil {
			return "", err
		}
	}
	if err := writer.WriteField("response_format", format); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if strings.TrimSpace(a.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.APIKey))
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("asr request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	responseData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	switch format {
	case "text", "srt", "vtt":
		return string(responseData), nil
	case "json", "verbose_json":
		var parsed openAIASRResponse
		if err := json.Unmarshal(responseData, &parsed); err != nil {
			return "", err
		}
		return parsed.Text, nil
	default:
		return "", fmt.Errorf("unsupported asr response_format: %s", format)
	}
}

type openAIASRResponse struct {
	Text string `json:"text"`
}

func (a *FileASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	if !hasPCM(frames) {
		return "", nil
	}
	path, err := a.SaveAudioToFile(sessionID, frames)
	if err != nil {
		return "", err
	}
	a.LastFilePath = path
	if a.DeleteAudio {
		defer func() { _ = os.Remove(path) }()
	}

	transcript := strings.TrimSpace(a.Transcript)
	if transcript == "" {
		transcript = "收到语音"
	}
	return transcript, nil
}

func (a *FileASR) SaveAudioToFile(sessionID string, frames []AudioFrame) (string, error) {
	outputDir := strings.TrimSpace(a.OutputDir)
	if outputDir == "" {
		outputDir = "tmp"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}

	pcmFrames := make([]audio.PCMFrame, 0, len(frames))
	sampleRate := a.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := a.Channels
	if channels <= 0 {
		channels = 1
	}
	for _, frame := range frames {
		if frame.PCM == nil || len(frame.PCM.Samples) == 0 {
			continue
		}
		pcm := *frame.PCM
		pcm.Samples = append([]int16(nil), frame.PCM.Samples...)
		pcmFrames = append(pcmFrames, audio.ConvertPCMFrame(pcm, sampleRate, channels))
	}

	fileName := "asr_" + safeSessionID(sessionID) + "_" + uuid.NewString() + ".wav"
	path := filepath.Join(outputDir, fileName)
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := audio.WriteWAV(file, pcmFrames); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func hasPCM(frames []AudioFrame) bool {
	for _, frame := range frames {
		if frame.PCM != nil && len(frame.PCM.Samples) > 0 {
			return true
		}
	}
	return false
}

func expandASRPlaceholders(values []string, filePath, sessionID string) []string {
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		expanded = append(expanded, expandASRPlaceholder(value, filePath, sessionID))
	}
	return expanded
}

func expandASRPlaceholder(value, filePath, sessionID string) string {
	return strings.NewReplacer(
		"{file}", filePath,
		"{session_id}", sessionID,
	).Replace(value)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func safeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "unknown"
	}
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(sessionID)
}
