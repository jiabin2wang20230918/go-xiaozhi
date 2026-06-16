package voice

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/xdimtech/go-xiaozhi/pkg/audio"
)

type EnergyVAD struct {
	Threshold float64
}

type CommandVAD struct {
	OutputDir  string
	Command    string
	Args       []string
	Env        map[string]string
	Threshold  float64
	SampleRate int
	Channels   int
}

func (v EnergyVAD) HasVoice(ctx context.Context, frame AudioFrame) (bool, error) {
	if frame.PCM == nil || len(frame.PCM.Samples) == 0 {
		return len(frame.Opus) > 0 && v.Threshold <= 0, nil
	}
	threshold := v.Threshold
	if threshold <= 0 {
		threshold = 0.01
	}
	return rms(frame.PCM.Samples) >= threshold, nil
}

func (v *CommandVAD) HasVoice(ctx context.Context, frame AudioFrame) (bool, error) {
	if frame.PCM == nil || len(frame.PCM.Samples) == 0 {
		return len(frame.Opus) > 0 && v.threshold() <= 0, nil
	}
	command := strings.TrimSpace(v.Command)
	if command == "" {
		return false, fmt.Errorf("vad command is required")
	}
	path, err := v.saveFrame(frame)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(path) }()

	args := expandVADPlaceholders(v.Args, path)
	cmd := exec.CommandContext(ctx, command, args...)
	if len(v.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range v.Env {
			cmd.Env = append(cmd.Env, key+"="+expandVADPlaceholder(value, path))
		}
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return false, fmt.Errorf("vad command failed: %w", err)
		}
		return false, fmt.Errorf("vad command failed: %w: %s", err, msg)
	}
	return parseVADOutput(string(output), v.threshold())
}

func (v *CommandVAD) saveFrame(frame AudioFrame) (string, error) {
	outputDir := strings.TrimSpace(v.OutputDir)
	if outputDir == "" {
		outputDir = "tmp"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	pcm := *frame.PCM
	pcm.Samples = append([]int16(nil), frame.PCM.Samples...)
	sampleRate := v.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := v.Channels
	if channels <= 0 {
		channels = 1
	}
	pcm = audio.ConvertPCMFrame(pcm, sampleRate, channels)
	path := filepath.Join(outputDir, "vad_"+uuid.NewString()+".wav")
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := audio.WriteWAV(file, []audio.PCMFrame{pcm}); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (v *CommandVAD) threshold() float64 {
	if v.Threshold > 0 {
		return v.Threshold
	}
	return 0.5
}

func parseVADOutput(output string, threshold float64) (bool, error) {
	value := strings.TrimSpace(strings.ToLower(output))
	if value == "" {
		return false, fmt.Errorf("vad command returned empty output")
	}
	fields := strings.Fields(value)
	if len(fields) > 0 {
		value = fields[len(fields)-1]
	}
	switch value {
	case "1", "true", "yes", "voice", "speech":
		return true, nil
	case "0", "false", "no", "silence":
		return false, nil
	}
	probability, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false, fmt.Errorf("parse vad command output %q: %w", output, err)
	}
	return probability >= threshold, nil
}

func expandVADPlaceholders(values []string, filePath string) []string {
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		expanded = append(expanded, expandVADPlaceholder(value, filePath))
	}
	return expanded
}

func expandVADPlaceholder(value, filePath string) string {
	return strings.NewReplacer("{file}", filePath).Replace(value)
}

func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		normalized := float64(sample) / math.MaxInt16
		sum += normalized * normalized
	}
	return math.Sqrt(sum / float64(len(samples)))
}
