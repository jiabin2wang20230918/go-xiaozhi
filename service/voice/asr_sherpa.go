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

// SherpaASR wraps sherpa-onnx offline SenseVoice recognizer for local speech
// recognition. It mirrors Python's FunASR(SenseVoiceSmall) provider.
// recognizer 实例可被多个会话共享（模型预加载）；mu 保护 Decode 推理串行化，
// 因为 sherpa Go 绑定未保证 OfflineRecognizer.Decode 的并发安全。
type SherpaASR struct {
	recognizer *sherpa.OfflineRecognizer
	sampleRate int
	channels   int
	language   string
	mu         sync.Mutex
}

// NewSherpaASR loads the SenseVoice model. modelDir must point at the unpacked
// sherpa-onnx-sense-voice-* directory containing model.int8.onnx and tokens.txt.
// Returns an error if the model cannot be loaded; callers should fall back to
// FileASR on error.
func NewSherpaASR(conf config.ASRConf) (*SherpaASR, error) {
	modelDir := strings.TrimSpace(conf.ModelDir)
	modelPath, err := resolveSenseVoiceModel(modelDir)
	if err != nil {
		return nil, err
	}
	tokensPath := filepath.Join(modelDir, "tokens.txt")
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, fmt.Errorf("sense voice tokens.txt not found at %q: %w", tokensPath, err)
	}

	sampleRate := conf.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := conf.Channels
	if channels <= 0 {
		channels = 1
	}
	language := strings.TrimSpace(conf.Language)
	if language == "" {
		// SenseVoice 默认 auto；zh 提示中文场景更稳。
		language = "auto"
	}

	cfg := &sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate},
		ModelConfig: sherpa.OfflineModelConfig{
			SenseVoice: sherpa.OfflineSenseVoiceModelConfig{
				Model:                       modelPath,
				Language:                    language,
				UseInverseTextNormalization: 1,
			},
			Tokens:     tokensPath,
			NumThreads: resolveNumThreads(conf.NumThreads),
			Provider:   "cpu",
			Debug:      0,
		},
		DecodingMethod: "greedy_search",
	}
	recognizer := sherpa.NewOfflineRecognizer(cfg)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create sherpa sense voice recognizer (model load failed)")
	}
	return &SherpaASR{recognizer: recognizer, sampleRate: sampleRate, channels: channels, language: language}, nil
}

// Transcribe implements ASR. It concatenates all PCM frames, resamples to the
// recognizer's expected format, and runs offline recognition in one shot.
func (a *SherpaASR) Transcribe(ctx context.Context, sessionID string, frames []AudioFrame) (string, error) {
	if !hasPCM(frames) {
		return "", nil
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	samples := concatFramesToFloat32(frames, a.sampleRate, a.channels)
	if len(samples) == 0 {
		return "", nil
	}

	stream := sherpa.NewOfflineStream(a.recognizer)
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(a.sampleRate, samples)
	a.mu.Lock()
	a.recognizer.Decode(stream)
	a.mu.Unlock()
	result := stream.GetResult()
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Text), nil
}

// Close releases the underlying C recognizer.
func (a *SherpaASR) Close() {
	if a == nil || a.recognizer == nil {
		return
	}
	sherpa.DeleteOfflineRecognizer(a.recognizer)
	a.recognizer = nil
}

// concatFramesToFloat32 resamples every frame to sampleRate/channels and returns
// the concatenated samples normalized to float32.
func concatFramesToFloat32(frames []AudioFrame, sampleRate, channels int) []float32 {
	var pcm []int16
	for _, frame := range frames {
		if frame.PCM == nil || len(frame.PCM.Samples) == 0 {
			continue
		}
		converted := audio.ConvertPCMFrame(*frame.PCM, sampleRate, channels)
		pcm = append(pcm, converted.Samples...)
	}
	return pcmInt16ToFloat32(pcm)
}

// resolveSenseVoiceModel picks the model onnx under modelDir, preferring
// model.int8.onnx (smaller/faster) over model.onnx.
func resolveSenseVoiceModel(modelDir string) (string, error) {
	if modelDir == "" {
		return "", fmt.Errorf("sense voice model_dir is empty")
	}
	if info, err := os.Stat(modelDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("sense voice model_dir not found: %q", modelDir)
	}
	for _, name := range []string{"model.int8.onnx", "model.onnx"} {
		candidate := filepath.Join(modelDir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no sense voice model onnx found under %q (expected model.int8.onnx or model.onnx)", modelDir)
}
