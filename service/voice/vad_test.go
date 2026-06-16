package voice

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/audio"
	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestCommandVADHelper(t *testing.T) {
	if os.Getenv("GO_XIAOZHI_VAD_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" && len(args) > i+1 {
			filePath := args[i+1]
			file, err := os.Open(filePath)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "open wav: %v", err)
				os.Exit(2)
			}
			defer file.Close()
			pcm, info, err := audio.ReadWAV(file)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "read wav: %v", err)
				os.Exit(3)
			}
			if info.SampleRate != 16000 || info.Channels != 1 || len(pcm.Samples) == 0 {
				_, _ = fmt.Fprintf(os.Stderr, "unexpected wav info: %+v samples=%d", info, len(pcm.Samples))
				os.Exit(4)
			}
			_, _ = fmt.Fprint(os.Stdout, os.Getenv("GO_XIAOZHI_VAD_OUTPUT"))
			os.Exit(0)
		}
	}
	_, _ = fmt.Fprint(os.Stderr, "missing helper args")
	os.Exit(5)
}

func TestEnergyVADRejectsSilence(t *testing.T) {
	vad := EnergyVAD{Threshold: 0.01}
	ok, err := vad.HasVoice(context.Background(), AudioFrame{
		PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: make([]int16, 960)},
	})
	if err != nil {
		t.Fatalf("vad: %v", err)
	}
	if ok {
		t.Fatal("silence should not be detected as voice")
	}
}

func TestEnergyVADAcceptsAudiblePCM(t *testing.T) {
	samples := make([]int16, 960)
	for i := range samples {
		samples[i] = 4000
	}
	vad := EnergyVAD{Threshold: 0.01}
	ok, err := vad.HasVoice(context.Background(), AudioFrame{
		PCM: &audio.PCMFrame{SampleRate: 16000, Channels: 1, Samples: samples},
	})
	if err != nil {
		t.Fatalf("vad: %v", err)
	}
	if !ok {
		t.Fatal("audible pcm should be detected as voice")
	}
}

func TestNewVADDefaultsToEnergy(t *testing.T) {
	if _, ok := NewVAD(config.VADConf{}).(EnergyVAD); !ok {
		t.Fatal("default vad should be EnergyVAD")
	}
}

func TestCommandVADExecutesCommandAndParsesProbability(t *testing.T) {
	dir := t.TempDir()
	vad := &CommandVAD{
		OutputDir:  dir,
		Command:    os.Args[0],
		Args:       []string{"-test.run=TestCommandVADHelper", "--", "{file}"},
		Env:        map[string]string{"GO_XIAOZHI_VAD_HELPER": "1", "GO_XIAOZHI_VAD_OUTPUT": "0.73"},
		Threshold:  0.6,
		SampleRate: 16000,
		Channels:   1,
	}

	ok, err := vad.HasVoice(context.Background(), AudioFrame{
		PCM: &audio.PCMFrame{SampleRate: 24000, Channels: 2, Samples: []int16{1000, 1000, 1200, 1200}},
	})
	if err != nil {
		t.Fatalf("command vad: %v", err)
	}
	if !ok {
		t.Fatal("probability above threshold should be voice")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read vad output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected vad temp wav to be removed, found %d entries", len(entries))
	}
}

func TestCommandVADParsesBooleanOutput(t *testing.T) {
	ok, err := parseVADOutput("speech\n", 0.9)
	if err != nil {
		t.Fatalf("parse speech output: %v", err)
	}
	if !ok {
		t.Fatal("speech output should be voice")
	}
	ok, err = parseVADOutput("silence\n", 0.1)
	if err != nil {
		t.Fatalf("parse silence output: %v", err)
	}
	if ok {
		t.Fatal("silence output should not be voice")
	}
}

func TestNewVADSupportsCommandProvider(t *testing.T) {
	vad, ok := NewVAD(config.VADConf{
		Type:       "command",
		Command:    "python3",
		Args:       []string{"silero_bridge.py", "{file}"},
		Env:        map[string]string{"MODEL": "silero"},
		Threshold:  0.42,
		SampleRate: 16000,
		Channels:   1,
	}).(*CommandVAD)
	if !ok {
		t.Fatalf("command config should create CommandVAD, got %T", vad)
	}
	if vad.Command != "python3" || vad.Args[1] != "{file}" || vad.Env["MODEL"] != "silero" || vad.Threshold != 0.42 {
		t.Fatalf("command vad config not preserved: %+v", vad)
	}
}
