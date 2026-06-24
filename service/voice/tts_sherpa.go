package voice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// SherpaTTS wraps sherpa-onnx offline Kokoro TTS for local, offline speech
// synthesis. It mirrors the pattern of SherpaASR / SherpaVAD: the model is
// loaded once per session and Synthesize runs inference in one shot.
type SherpaTTS struct {
	tts          *sherpa.OfflineTts
	sampleRate   int
	channels     int
	frameSize    int
	sid          int
	speed        float32
	silenceScale float32
}

// NewSherpaTTS loads the Kokoro model. modelDir must point at the unpacked
// kokoro-multi-lang-v1_* directory containing model.onnx, voices.bin,
// tokens.txt, espeak-ng-data/ and the lexicon-*.txt files. Returns an error if
// the model cannot be loaded; callers should fall back to StubTTS on error.
func NewSherpaTTS(conf config.TTSConf) (*SherpaTTS, error) {
	modelDir := strings.TrimSpace(conf.ModelDir)
	if modelDir == "" {
		return nil, fmt.Errorf("kokoro model_dir is empty")
	}
	if info, err := os.Stat(modelDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("kokoro model_dir not found: %q", modelDir)
	}
	modelPath := filepath.Join(modelDir, "model.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("kokoro model.onnx not found at %q: %w", modelPath, err)
	}
	tokensPath := firstExisting([]string{
		strings.TrimSpace(conf.Voices),
		filepath.Join(modelDir, "tokens.txt"),
	})
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, fmt.Errorf("kokoro tokens.txt not found at %q: %w", tokensPath, err)
	}
	voicesPath := firstExisting([]string{
		strings.TrimSpace(conf.Voices),
		filepath.Join(modelDir, "voices.bin"),
	})
	if _, err := os.Stat(voicesPath); err != nil {
		return nil, fmt.Errorf("kokoro voices.bin not found at %q: %w", voicesPath, err)
	}
	dataDir := firstExistingDir([]string{
		strings.TrimSpace(conf.DataDir),
		filepath.Join(modelDir, "espeak-ng-data"),
	})
	if dataDir == "" {
		return nil, fmt.Errorf("kokoro espeak-ng-data not found under %q", modelDir)
	}
	lexicon := strings.TrimSpace(conf.Lexicon)
	if lexicon == "" {
		// kokoro-multi-lang-v1_* 自带 lexicon-us-en.txt, lexicon-zh.txt；拼接成逗号分隔。
		var found []string
		for _, name := range []string{"lexicon-us-en.txt", "lexicon-zh.txt"} {
			if p := filepath.Join(modelDir, name); fileExists(p) {
				found = append(found, p)
			}
		}
		lexicon = strings.Join(found, ",")
	}

	sampleRate := conf.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := conf.Channels
	if channels <= 0 {
		channels = 1
	}
	frameSize := conf.FrameSize
	if frameSize <= 0 {
		frameSize = sampleRate * 60 / 1000
	}

	speed := float32(conf.Speed)
	if speed <= 0 {
		speed = 1.0
	}
	silenceScale := float32(conf.SilenceScale)
	if silenceScale <= 0 {
		silenceScale = 0.2 // 与官方示例一致，句间停顿稍短，更连贯
	}

	cfg := &sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Kokoro: sherpa.OfflineTtsKokoroModelConfig{
				Model:       modelPath,
				Voices:      voicesPath,
				Tokens:      tokensPath,
				DataDir:     dataDir,
				Lexicon:     lexicon,
				Lang:        strings.TrimSpace(conf.Lang),
				LengthScale: speed, // Kokoro 用 LengthScale 控制语速：>1 慢，<1 快
			},
			NumThreads: 1,
			Debug:      0,
		},
		MaxNumSentences: 1,
		SilenceScale:    silenceScale,
	}
	tts := sherpa.NewOfflineTts(cfg)
	if tts == nil {
		return nil, fmt.Errorf("failed to create sherpa kokoro tts (model load failed)")
	}
	return &SherpaTTS{
		tts:          tts,
		sampleRate:   sampleRate,
		channels:     channels,
		frameSize:    frameSize,
		sid:          conf.Sid,
		speed:        speed,
		silenceScale: silenceScale,
	}, nil
}

// Synthesize implements TTS. It runs offline Kokoro inference on the text and
// returns the generated audio as Opus-encoded frames, resampled to the
// pipeline's expected sample rate.
func (t *SherpaTTS) Synthesize(ctx context.Context, sessionID string, text string) ([][]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	generated := t.tts.GenerateWithConfig(text, &sherpa.GenerationConfig{
		Sid:          t.sid,
		Speed:        t.speed,
		SilenceScale: t.silenceScale,
	}, nil)
	if generated == nil || len(generated.Samples) == 0 {
		return nil, fmt.Errorf("kokoro tts produced no audio for %q", text)
	}

	pcm := audio.PCMFrame{
		SampleRate: generated.SampleRate,
		Channels:   1,
		Samples:    float32ToInt16PCM(generated.Samples),
	}
	return encodePCMToOpus(pcm, t.sampleRate, t.channels, t.frameSize)
}

// Close releases the underlying C resource. Safe to call multiple times.
func (t *SherpaTTS) Close() {
	if t == nil || t.tts == nil {
		return
	}
	sherpa.DeleteOfflineTts(t.tts)
	t.tts = nil
}

// float32ToInt16PCM denormalizes [-1, 1] float32 samples back to int16 PCM for
// downstream Opus encoding / resampling.
func float32ToInt16PCM(samples []float32) []int16 {
	if len(samples) == 0 {
		return nil
	}
	out := make([]int16, len(samples))
	for i, s := range samples {
		v := int(s * 32767.0)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

func resolveKokoroFile(modelDir, override, defaultName, _ string) string {
	return firstExisting([]string{
		strings.TrimSpace(override),
		filepath.Join(modelDir, defaultName),
	})
}

func firstExisting(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fileExists(p) {
			return p
		}
	}
	// 都不存在时返回最后一个非空，便于上层报错带上候选路径。
	for i := len(paths) - 1; i >= 0; i-- {
		if paths[i] != "" {
			return paths[i]
		}
	}
	return ""
}

func firstExistingDir(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
