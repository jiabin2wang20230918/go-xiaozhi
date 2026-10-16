package voice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestSherpaMatchaTTSMissingModelReturnsError(t *testing.T) {
	tmp := t.TempDir()
	_, err := NewSherpaMatchaTTS(config.TTSConf{
		Type:     "matcha",
		ModelDir: tmp,
	})
	if err == nil {
		t.Fatal("expected error when matcha model is missing")
	}
}

func TestSherpaMatchaTTSFactoryFallback(t *testing.T) {
	tts := NewTTS(config.TTSConf{
		Type:       "matcha",
		ModelDir:   t.TempDir(),
		SampleRate: 16000,
		Channels:   1,
	})
	if tts == nil {
		t.Fatal("NewTTS returned nil")
	}
	if _, ok := tts.(StubTTS); !ok {
		t.Fatalf("expected fallback to StubTTS, got %T", tts)
	}
}

// TestSherpaMatchaTTSSynthesizesSpeech runs end-to-end only when
// GO_XIAOZHI_MATCHA points at the unpacked acoustic package and vocoder is set.
func TestSherpaMatchaTTSSynthesizesSpeech(t *testing.T) {
	dir := filepath.Clean(os.Getenv("GO_XIAOZHI_MATCHA"))
	acoustic := filepath.Join(dir, "model-steps-3.onnx")
	if dir == "" || !pathExists(acoustic) {
		t.Skipf("set GO_XIAOZHI_MATCHA to a sherpa matcha model dir to run e2e tts test")
	}
	vocoder := os.Getenv("GO_XIAOZHI_MATCHA_VOCODER")
	tts, err := NewSherpaMatchaTTS(config.TTSConf{
		Type:        "matcha",
		ModelDir:    dir,
		Vocoder:     vocoder,
		SampleRate:  16000,
		Channels:    1,
		FrameSize:   960,
		Sid:         0,
	})
	if err != nil {
		t.Fatalf("NewSherpaMatchaTTS: %v", err)
	}
	defer tts.Close()

	frames, err := tts.Synthesize(context.Background(), "s1", "你好，我是小智。")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("expected non-empty opus frames for matcha synthesis")
	}
}
