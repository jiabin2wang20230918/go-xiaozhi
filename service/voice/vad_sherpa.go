package voice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// SherpaVAD wraps sherpa-onnx Silero VAD for local, model-based voice activity
// detection. It mirrors Python's SileroVAD provider.
type SherpaVAD struct {
	vad        *sherpa.VoiceActivityDetector
	sampleRate int
	channels   int
	mu         sync.Mutex
}

// NewSherpaVAD loads the Silero VAD model. modelDir may be either a directory
// containing silero_vad.onnx or the .onnx file path itself. Returns an error if
// the model cannot be loaded; callers should fall back to EnergyVAD on error.
func NewSherpaVAD(conf config.VADConf) (*SherpaVAD, error) {
	modelPath := resolveSileroModelPath(conf.ModelDir)
	if modelPath == "" {
		return nil, fmt.Errorf("silero vad model not found under %q", conf.ModelDir)
	}

	sampleRate := conf.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := conf.Channels
	if channels <= 0 {
		channels = 1
	}

	threshold := float32(conf.Threshold)
	if threshold <= 0 {
		threshold = 0.5
	}
	minSilence := float32(conf.MinSilenceDurationMs) / 1000.0
	if minSilence <= 0 {
		minSilence = 1.0 // 与 Python SileroVAD min_silence_duration_ms=1000 对齐
	}
	maxSpeech := float32(conf.MaxSpeechDurationS)
	if maxSpeech <= 0 {
		maxSpeech = 20.0
	}

	cfg := &sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              modelPath,
			Threshold:          threshold,
			MinSilenceDuration: minSilence,
			MinSpeechDuration:  0,
			WindowSize:         512,
			MaxSpeechDuration:  maxSpeech,
		},
		SampleRate: sampleRate,
		NumThreads: resolveNumThreads(conf.NumThreads),
		Provider:   "cpu",
		Debug:      0,
	}
	// bufferSizeInSeconds: VAD 内部环形缓冲秒数，给到 60s 足够覆盖一句长语音。
	vad := sherpa.NewVoiceActivityDetector(cfg, 60)
	if vad == nil {
		return nil, fmt.Errorf("failed to create sherpa silero vad (model load failed)")
	}
	return &SherpaVAD{vad: vad, sampleRate: sampleRate, channels: channels}, nil
}

// HasVoice implements VAD. It feeds the PCM frame into the detector and reports
// whether speech is currently detected. Opus-only frames are treated as voice
// presence (consistent with EnergyVAD/CommandVAD behavior).
func (v *SherpaVAD) HasVoice(ctx context.Context, frame AudioFrame) (bool, error) {
	if frame.PCM == nil || len(frame.PCM.Samples) == 0 {
		return len(frame.Opus) > 0, nil
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	pcm := audio.ConvertPCMFrame(*frame.PCM, v.sampleRate, v.channels)
	samples := pcmInt16ToFloat32(pcm.Samples)
	if len(samples) == 0 {
		return false, nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.vad.AcceptWaveform(samples)
	return v.vad.IsSpeech(), nil
}

// Close releases the underlying C resource. Safe to call multiple times.
func (v *SherpaVAD) Close() {
	if v == nil || v.vad == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vad != nil {
		sherpa.DeleteVoiceActivityDetector(v.vad)
		v.vad = nil
	}
}

// resolveSileroModelPath accepts either a direct path to silero_vad.onnx or a
// directory that contains it. Returns "" if nothing usable is found.
func resolveSileroModelPath(modelDir string) string {
	dir := strings.TrimSpace(modelDir)
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		return dir // already a file path
	}
	candidate := filepath.Join(dir, "silero_vad.onnx")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
