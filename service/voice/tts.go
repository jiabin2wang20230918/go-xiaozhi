package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

const openAITTSMaxAttempts = 5

type StubTTS struct {
	SampleRate int
	Channels   int
	FrameSize  int
	DurationMs int
	Frequency  float64
	Amplitude  int16
	Gain       float32
}

type OpenAITTS struct {
	APIURL         string
	APIKey         string
	Model          string
	Voice          string
	ResponseFormat string
	Speed          float64
	SampleRate     int
	Channels       int
	FrameSize      int
	Gain           float32
	HTTPClient     *http.Client
}

type CustomTTS struct {
	URL        string
	Params     map[string]any
	Headers    map[string]string
	Format     string
	SampleRate int
	Channels   int
	FrameSize  int
	Gain       float32
	HTTPClient *http.Client
}

type CommandTTS struct {
	OutputDir   string
	Command     string
	Args        []string
	Env         map[string]string
	Format      string
	SampleRate  int
	Channels    int
	FrameSize   int
	DeleteAudio bool
	Gain        float32
}

func NewTTS(conf config.TTSConf) TTS {
	gain := normalizeTTSGain(conf.Gain)
	switch conf.Type {
	case "", "stub":
		return StubTTS{
			SampleRate: conf.SampleRate,
			Channels:   conf.Channels,
			FrameSize:  conf.FrameSize,
			DurationMs: conf.DurationMs,
			Frequency:  conf.Frequency,
			Amplitude:  conf.Amplitude,
			Gain:       gain,
		}
	case "openai":
		return &OpenAITTS{
			APIURL:         conf.APIURL,
			APIKey:         conf.APIKey,
			Model:          conf.Model,
			Voice:          conf.Voice,
			ResponseFormat: conf.ResponseFormat,
			Speed:          conf.Speed,
			SampleRate:     conf.SampleRate,
			Channels:       conf.Channels,
			FrameSize:      conf.FrameSize,
			Gain:           gain,
		}
	case "custom":
		return &CustomTTS{
			URL:        conf.LegacyURL,
			Params:     conf.Params,
			Headers:    conf.Headers,
			Format:     conf.Format,
			SampleRate: conf.SampleRate,
			Channels:   conf.Channels,
			FrameSize:  conf.FrameSize,
			Gain:       gain,
		}
	case "command":
		return &CommandTTS{
			OutputDir:   conf.OutputDir,
			Command:     conf.Command,
			Args:        append([]string(nil), conf.Args...),
			Env:         cloneStringMap(conf.Env),
			Format:      conf.Format,
			SampleRate:  conf.SampleRate,
			Channels:    conf.Channels,
			FrameSize:   conf.FrameSize,
			DeleteAudio: boolValue(conf.DeleteAudio, true),
			Gain:        gain,
		}
	case "kokoro":
		tts, err := NewSherpaTTS(conf)
		if err != nil {
			log.Printf("sherpa kokoro tts unavailable (%v); falling back to stub tts", err)
			return StubTTS{
				SampleRate: conf.SampleRate,
				Channels:   conf.Channels,
				FrameSize:  conf.FrameSize,
				DurationMs: conf.DurationMs,
				Frequency:  conf.Frequency,
				Amplitude:  conf.Amplitude,
				Gain:       gain,
			}
		}
		return tts
	default:
		return StubTTS{
			SampleRate: conf.SampleRate,
			Channels:   conf.Channels,
			FrameSize:  conf.FrameSize,
			DurationMs: conf.DurationMs,
			Frequency:  conf.Frequency,
			Amplitude:  conf.Amplitude,
			Gain:       gain,
		}
	}
}

// normalizeTTSGain 把缺省/非正的 gain 归一为 1.0（不放大），避免零增益导致静音。
func normalizeTTSGain(gain float32) float32 {
	if gain <= 0 {
		return 1.0
	}
	return gain
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func (t *CommandTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	command := strings.TrimSpace(t.Command)
	if command == "" {
		return nil, fmt.Errorf("tts command is required")
	}
	format := strings.TrimSpace(strings.ToLower(t.Format))
	if format == "" {
		format = "mp3"
	}
	outputPath, err := t.outputPath(sessionID, format)
	if err != nil {
		return nil, err
	}
	if t.DeleteAudio {
		defer func() { _ = os.Remove(outputPath) }()
	}

	args := expandTTSPlaceholders(t.Args, text, sessionID, outputPath)
	cmd := exec.CommandContext(ctx, command, args...)
	if len(t.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range t.Env {
			cmd.Env = append(cmd.Env, key+"="+expandTTSPlaceholder(value, text, sessionID, outputPath))
		}
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if _, err := cmd.Output(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("tts command failed: %w", err)
		}
		return nil, fmt.Errorf("tts command failed: %w: %s", err, msg)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read tts command output: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("tts command output is empty: %s", outputPath)
	}
	return encodeAudioResponseToOpus(ctx, data, format, t.SampleRate, t.Channels, t.FrameSize, t.Gain)
}

func (t *OpenAITTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	apiURL := strings.TrimSpace(t.APIURL)
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1/audio/speech"
	}
	model := strings.TrimSpace(t.Model)
	if model == "" {
		model = "tts-1"
	}
	voice := strings.TrimSpace(t.Voice)
	if voice == "" {
		voice = "alloy"
	}
	format := strings.TrimSpace(t.ResponseFormat)
	if format == "" {
		format = "wav"
	}
	if format != "wav" {
		return nil, fmt.Errorf("unsupported tts response format: %s", format)
	}
	speed := t.Speed
	if speed <= 0 {
		speed = 1.0
	}

	body, err := json.Marshal(openAITTSRequest{
		Model:          model,
		Input:          text,
		Voice:          voice,
		ResponseFormat: format,
		Speed:          speed,
	})
	if err != nil {
		return nil, err
	}
	client := t.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	pcm, err := t.requestWAVWithRetry(ctx, client, apiURL, body)
	if err != nil {
		return nil, err
	}
	pcm.Samples = audio.ApplyGain(pcm.Samples, t.Gain)
	return encodePCMToOpus(pcm, t.SampleRate, t.Channels, t.FrameSize)
}

func (t *CustomTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	audioURL := strings.TrimSpace(t.URL)
	if audioURL == "" {
		return nil, fmt.Errorf("custom tts url is required")
	}
	format := strings.TrimSpace(t.Format)
	if format == "" {
		format = "wav"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	for name, value := range t.Params {
		query.Set(name, customTTSParamValue(value, text))
	}
	req.URL.RawQuery = query.Encode()
	for name, value := range t.Headers {
		if strings.TrimSpace(name) == "" {
			continue
		}
		req.Header.Set(name, value)
	}
	client := t.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("custom tts request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return encodeAudioResponseToOpus(ctx, data, format, t.SampleRate, t.Channels, t.FrameSize, t.Gain)
}

func customTTSParamValue(value any, text string) string {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "{prompt_text}", text)
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func (t *CommandTTS) outputPath(sessionID, format string) (string, error) {
	outputDir := strings.TrimSpace(t.OutputDir)
	if outputDir == "" {
		outputDir = "tmp"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	ext := strings.Trim(strings.TrimSpace(format), ".")
	if ext == "" {
		ext = "mp3"
	}
	fileName := "tts_" + safeSessionID(sessionID) + "_" + uuid.NewString() + "." + ext
	return filepath.Join(outputDir, fileName), nil
}

func expandTTSPlaceholders(values []string, text, sessionID, outputPath string) []string {
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		expanded = append(expanded, expandTTSPlaceholder(value, text, sessionID, outputPath))
	}
	return expanded
}

func expandTTSPlaceholder(value, text, sessionID, outputPath string) string {
	return strings.NewReplacer(
		"{text}", text,
		"{prompt_text}", text,
		"{session_id}", sessionID,
		"{output}", outputPath,
	).Replace(value)
}

func (t *OpenAITTS) requestWAVWithRetry(ctx context.Context, client *http.Client, apiURL string, body []byte) (audio.PCMFrame, error) {
	var lastErr error
	for attempt := 0; attempt < openAITTSMaxAttempts; attempt++ {
		pcm, err := t.requestWAV(ctx, client, apiURL, body)
		if err == nil {
			return pcm, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return audio.PCMFrame{}, fmt.Errorf("tts request cancelled: %w", ctx.Err())
		}
	}
	return audio.PCMFrame{}, fmt.Errorf("tts request failed after %d attempts: %w", openAITTSMaxAttempts, lastErr)
}

func (t *OpenAITTS) requestWAV(ctx context.Context, client *http.Client, apiURL string, body []byte) (audio.PCMFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return audio.PCMFrame{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(t.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(t.APIKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return audio.PCMFrame{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return audio.PCMFrame{}, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	pcm, _, err := audio.ReadWAV(resp.Body)
	if err != nil {
		return audio.PCMFrame{}, err
	}
	return pcm, nil
}

func encodePCMToOpus(pcm audio.PCMFrame, sampleRate int, channels int, frameSize int) ([][]byte, error) {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	pcm = audio.ConvertPCMFrame(pcm, sampleRate, channels)
	if frameSize <= 0 {
		frameSize = sampleRate * 60 / 1000
	}
	encoder, err := audio.NewOpusEncoder(audio.OpusEncoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
	})
	if err != nil {
		return nil, err
	}
	return encoder.Encode(pcm.Samples)
}

func encodeAudioResponseToOpus(ctx context.Context, data []byte, format string, sampleRate int, channels int, frameSize int, gain float32) ([][]byte, error) {
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "wav"
	}
	if format == "wav" {
		pcm, _, err := audio.ReadWAV(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		pcm.Samples = audio.ApplyGain(pcm.Samples, gain)
		return encodePCMToOpus(pcm, sampleRate, channels, frameSize)
	}
	pcm, err := decodeAudioWithFFmpeg(ctx, data, format, sampleRate, channels)
	if err != nil {
		return nil, err
	}
	pcm.Samples = audio.ApplyGain(pcm.Samples, gain)
	return encodePCMToOpus(pcm, sampleRate, channels, frameSize)
}

func decodeAudioWithFFmpeg(ctx context.Context, data []byte, format string, sampleRate int, channels int) (audio.PCMFrame, error) {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if strings.TrimSpace(format) != "" {
		args = append(args, "-f", format)
	}
	args = append(args, "-i", "pipe:0", "-ac", fmt.Sprint(channels), "-ar", fmt.Sprint(sampleRate), "-f", "s16le", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(data)
	raw, err := cmd.Output()
	if err != nil {
		return audio.PCMFrame{}, fmt.Errorf("ffmpeg decode %s audio: %w", format, err)
	}
	if len(raw)%2 != 0 {
		raw = append(raw, 0)
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}
	return audio.PCMFrame{
		SampleRate: sampleRate,
		Channels:   channels,
		Samples:    samples,
	}, nil
}

type openAITTSRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed,omitempty"`
}

func (t StubTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	sampleRate := t.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := t.Channels
	if channels <= 0 {
		channels = 1
	}
	frameSize := t.FrameSize
	if frameSize <= 0 {
		frameSize = sampleRate * 60 / 1000
	}
	durationMs := t.DurationMs
	if durationMs <= 0 {
		durationMs = 180
	}
	frequency := t.Frequency
	if frequency <= 0 {
		frequency = 440
	}
	amplitude := t.Amplitude
	if amplitude <= 0 {
		amplitude = 1200
	}

	samples := audio.ApplyGain(tonePCM(sampleRate, channels, durationMs, frequency, amplitude), t.Gain)
	encoder, err := audio.NewOpusEncoder(audio.OpusEncoderConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		FrameSize:  frameSize,
	})
	if err != nil {
		return nil, err
	}
	return encoder.Encode(samples)
}

func tonePCM(sampleRate, channels, durationMs int, frequency float64, amplitude int16) []int16 {
	total := sampleRate * durationMs / 1000
	samples := make([]int16, 0, total*channels)
	for i := 0; i < total; i++ {
		value := int16(float64(amplitude) * math.Sin(2*math.Pi*frequency*float64(i)/float64(sampleRate)))
		for ch := 0; ch < channels; ch++ {
			samples = append(samples, value)
		}
	}
	return samples
}
