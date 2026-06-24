package voice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// 若未提供模型，NewSherpaTTS 必须返回明确错误（而非 panic），工厂据此回退。
func TestSherpaTTSMissingModelReturnsError(t *testing.T) {
	tmp := t.TempDir()
	_, err := NewSherpaTTS(config.TTSConf{
		Type:     "kokoro",
		ModelDir: tmp, // 空目录：无 model.onnx
	})
	if err == nil {
		t.Fatal("expected error when kokoro model is missing")
	}
}

func TestSherpaTTSFactoryFallback(t *testing.T) {
	// 工厂在模型缺失时应回退到 StubTTS，且不 panic。
	tts := NewTTS(config.TTSConf{
		Type:       "kokoro",
		ModelDir:   t.TempDir(),
		SampleRate: 16000,
		Channels:   1,
	})
	if tts == nil {
		t.Fatal("NewTTS returned nil")
	}
	// 回退后应为 StubTTS（值类型）；用类型断言确认。
	if _, ok := tts.(StubTTS); !ok {
		t.Fatalf("expected fallback to StubTTS, got %T", tts)
	}
}

// TestSherpaTTSSynthesizesSpeech 仅在环境变量 GO_XIAOZHI_KOKORO 指向真实
// sherpa Kokoro 模型目录时运行端到端合成；否则 skip。
func TestSherpaTTSSynthesizesSpeech(t *testing.T) {
	dir := filepath.Clean(os.Getenv("GO_XIAOZHI_KOKORO"))
	if dir == "" || !pathExists(filepath.Join(dir, "model.onnx")) {
		t.Skipf("set GO_XIAOZHI_KOKORO to a sherpa kokoro model dir to run e2e tts test")
	}
	tts, err := NewSherpaTTS(config.TTSConf{
		Type:       "kokoro",
		ModelDir:   dir,
		SampleRate: 16000,
		Channels:   1,
		FrameSize:  960,
	})
	if err != nil {
		t.Fatalf("NewSherpaTTS: %v", err)
	}
	defer tts.Close()

	// 空文本应返回 nil 帧，不报错。
	frames, err := tts.Synthesize(context.Background(), "s1", "")
	if err != nil {
		t.Fatalf("Synthesize empty: %v", err)
	}
	if frames != nil {
		t.Errorf("expected nil frames for empty text, got %d", len(frames))
	}

	// 合成中文文本应产出非空 Opus 帧。
	frames, err = tts.Synthesize(context.Background(), "s1", "你好，我是小智。")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("expected non-empty opus frames for kokoro synthesis")
	}
}
