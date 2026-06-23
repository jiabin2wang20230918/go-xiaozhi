package voice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

// 若未提供模型，NewSherpaASR 必须返回明确错误（而非 panic），工厂据此回退。
func TestSherpaASRMissingModelReturnsError(t *testing.T) {
	tmp := t.TempDir()
	_, err := NewSherpaASR(config.ASRConf{
		Type:     "sherpa_sensevoice",
		ModelDir: tmp, // 空目录：无 onnx、无 tokens
	})
	if err == nil {
		t.Fatal("expected error when sense voice model is missing")
	}
}

func TestSherpaASRNewVADFactoryFallback(t *testing.T) {
	// 工厂在模型缺失时应回退到 FileASR，且不 panic。
	asr := NewASR(config.ASRConf{
		Type:           "sherpa_sensevoice",
		ModelDir:       t.TempDir(),
		StubTranscript: "收到语音",
	})
	if asr == nil {
		t.Fatal("NewASR returned nil")
	}
	// 回退后应为 *FileASR；用类型断言确认。
	if _, ok := asr.(*FileASR); !ok {
		t.Fatalf("expected fallback to *FileASR, got %T", asr)
	}
}

// TestSherpaASRRecognizesSpeech 仅在环境变量 GO_XIAOZHI_SENSE_VOICE 指向真实
// sherpa SenseVoice 模型目录时运行端到端识别；否则 skip。
func TestSherpaASRRecognizesSpeech(t *testing.T) {
	dir := filepath.Clean(os.Getenv("GO_XIAOZHI_SENSE_VOICE"))
	if dir == "" || !pathExists(filepath.Join(dir, "tokens.txt")) {
		t.Skipf("set GO_XIAOZHI_SENSE_VOICE to a sherpa sense voice model dir to run e2e asr test")
	}
	asr, err := NewSherpaASR(config.ASRConf{
		Type:       "sherpa_sensevoice",
		ModelDir:   dir,
		Language:   "auto",
		SampleRate: 16000,
		Channels:   1,
	})
	if err != nil {
		t.Fatalf("NewSherpaASR: %v", err)
	}
	defer asr.Close()
	// 无 PCM 输入应返回空字符串，不报错。
	text, err := asr.Transcribe(context.Background(), "s1", nil)
	if err != nil {
		t.Fatalf("Transcribe empty: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty transcript for no input, got %q", text)
	}
}
