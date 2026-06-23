package voice

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// sherpaVADModel 解析 silero_vad.onnx 的路径，覆盖以下来源：
//   - 环境变量 GO_XIAOZHI_SILERO_VAD
//   - 仓库根 models/silero_vad.onnx（含软链）
//   - Python 项目 models/snakers4_silero-vad/.../silero_vad.onnx
func sherpaVADModel(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("GO_XIAOZHI_SILERO_VAD"); env != "" && pathExists(env) {
		return env
	}
	repoRoot := func() string {
		_, file, _, _ := runtime.Caller(0)
		return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	}()
	pyRoot := filepath.Clean(filepath.Join(repoRoot, "..", "xiaozhi-esp32-server", "main", "xiaozhi-server"))
	for _, p := range []string{
		filepath.Join(repoRoot, "models", "silero_vad.onnx"),
		filepath.Join(pyRoot, "models", "snakers4_silero-vad", "src", "silero_vad", "data", "silero_vad.onnx"),
	} {
		if pathExists(p) {
			return p
		}
	}
	t.Skipf("silero_vad.onnx not found; set GO_XIAOZHI_SILERO_VAD or create models/silero_vad.onnx")
	return ""
}

func TestSherpaVADLoadsAndDetectsSpeech(t *testing.T) {
	model := sherpaVADModel(t)
	vad, err := NewSherpaVAD(config.VADConf{
		Type:       "silero",
		ModelDir:   model,
		Threshold:  0.5,
		SampleRate: 16000,
		Channels:   1,
	})
	if err != nil {
		t.Fatalf("NewSherpaVAD: %v", err)
	}
	defer vad.Close()
	if vad.vad == nil {
		t.Fatal("vad detector is nil")
	}

	ctx := context.Background()
	// 静音帧：全 0，SileroVAD 应明确判定为无语音
	silence := makeSilenceFrame(16000, 1, 16)
	hasVoice, err := vad.HasVoice(ctx, silence)
	if err != nil {
		t.Fatalf("HasVoice silence: %v", err)
	}
	if hasVoice {
		t.Errorf("silence frame should not be detected as voice")
	}

	// 持续高能量帧：验证检测器在连续输入下稳定运行、不 panic。
	// 注意：纯音不是人声，SileroVAD 不保证判定为语音，因此这里只断言
	// "无错误运行"，是否检测到语音仅作日志参考。
	speech := makeToneFrame(16000, 1, 16, 220, 0.6)
	for i := 0; i < 8; i++ {
		if _, err := vad.HasVoice(ctx, speech); err != nil {
			t.Fatalf("HasVoice speech iter %d: %v", i, err)
		}
	}
	t.Logf("sherpa silero vad ran 8 iterations without error (model loaded ok)")
}

func makeSilenceFrame(sampleRate, channels, ms int) AudioFrame {
	n := sampleRate * ms / 1000
	return AudioFrame{PCM: &audio.PCMFrame{
		SampleRate: sampleRate,
		Channels:   channels,
		Samples:    make([]int16, n),
	}}
}

func makeToneFrame(sampleRate, channels, ms int, freq, amplitude float64) AudioFrame {
	n := sampleRate * ms / 1000
	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		v := amplitude * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
		samples[i] = int16(v * math.MaxInt16)
	}
	return AudioFrame{PCM: &audio.PCMFrame{
		SampleRate: sampleRate,
		Channels:   channels,
		Samples:    samples,
	}}
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	return false
}
