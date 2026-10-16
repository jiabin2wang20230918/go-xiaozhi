package voice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// NewSherpaMatchaTTS loads the matcha-icefall-zh-en acoustic package plus an
// external vocoder (vocos-16khz-univ.onnx). modelDir must contain
// model-steps-3.onnx, lexicon.txt, tokens.txt, espeak-ng-data/, and the
// phone/date/number FSTs. Returns an error if the model cannot be loaded;
// callers should fall back to StubTTS on error.
func NewSherpaMatchaTTS(conf config.TTSConf) (*SherpaTTS, error) {
	modelDir := strings.TrimSpace(conf.ModelDir)
	if modelDir == "" {
		return nil, fmt.Errorf("matcha model_dir is empty")
	}
	if info, err := os.Stat(modelDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("matcha model_dir not found: %q", modelDir)
	}

	acousticPath := firstExisting([]string{
		strings.TrimSpace(conf.Model),
		filepath.Join(modelDir, "model-steps-3.onnx"),
		filepath.Join(modelDir, "model.onnx"),
	})
	if _, err := os.Stat(acousticPath); err != nil {
		return nil, fmt.Errorf("matcha acoustic model not found at %q: %w", acousticPath, err)
	}

	vocoderPath := firstExisting([]string{
		strings.TrimSpace(conf.Vocoder),
		strings.TrimSpace(conf.VocoderPath),
		filepath.Join(modelDir, "vocos-16khz-univ.onnx"),
		filepath.Join(filepath.Dir(modelDir), "vocos-16khz-univ.onnx"),
		filepath.Join("models", "vocos-16khz-univ.onnx"),
	})
	if _, err := os.Stat(vocoderPath); err != nil {
		return nil, fmt.Errorf("matcha vocoder not found at %q: %w", vocoderPath, err)
	}

	lexiconPath := firstExisting([]string{
		strings.TrimSpace(conf.Lexicon),
		filepath.Join(modelDir, "lexicon.txt"),
	})
	if _, err := os.Stat(lexiconPath); err != nil {
		return nil, fmt.Errorf("matcha lexicon.txt not found at %q: %w", lexiconPath, err)
	}

	tokensPath := firstExisting([]string{
		filepath.Join(modelDir, "tokens.txt"),
	})
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, fmt.Errorf("matcha tokens.txt not found at %q: %w", tokensPath, err)
	}

	dataDir := firstExistingDir([]string{
		strings.TrimSpace(conf.DataDir),
		filepath.Join(modelDir, "espeak-ng-data"),
	})
	if dataDir == "" {
		return nil, fmt.Errorf("matcha espeak-ng-data not found under %q", modelDir)
	}

	ruleFsts := resolveMatchaRuleFsts(modelDir)

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
		silenceScale = 0.2
	}
	gain := normalizeTTSGain(conf.Gain)

	cfg := &sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Matcha: sherpa.OfflineTtsMatchaModelConfig{
				AcousticModel: acousticPath,
				Vocoder:       vocoderPath,
				Lexicon:       lexiconPath,
				Tokens:        tokensPath,
				DataDir:       dataDir,
				NoiseScale:    0.667,
				LengthScale:   speed,
			},
			NumThreads: resolveNumThreads(conf.NumThreads),
			Debug:      0,
		},
		RuleFsts:        ruleFsts,
		MaxNumSentences: 1,
		SilenceScale:    silenceScale,
	}
	tts := sherpa.NewOfflineTts(cfg)
	if tts == nil {
		return nil, fmt.Errorf("failed to create sherpa matcha tts (model load failed)")
	}
	return &SherpaTTS{
		tts:          tts,
		sampleRate:   sampleRate,
		channels:     channels,
		frameSize:    frameSize,
		sid:          conf.Sid,
		speed:        speed,
		silenceScale: silenceScale,
		gain:         gain,
		backend:      "matcha",
	}, nil
}

func resolveMatchaRuleFsts(modelDir string) string {
	names := []string{"phone-zh.fst", "date-zh.fst", "number-zh.fst"}
	var found []string
	for _, name := range names {
		p := filepath.Join(modelDir, name)
		if fileExists(p) {
			found = append(found, p)
		}
	}
	return strings.Join(found, ",")
}
