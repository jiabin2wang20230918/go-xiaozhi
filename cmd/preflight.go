package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runPreflightChecks() error {
	if err := checkFFmpegInstalled(runCommand); err != nil {
		return err
	}
	return nil
}

func checkFFmpegInstalled(run commandRunner) error {
	if run == nil {
		run = runCommand
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := run(ctx, "ffmpeg", "-version")
	if err != nil {
		return fmt.Errorf("ffmpeg is required but was not found or failed to run: %w", err)
	}
	if !bytes.Contains(bytes.ToLower(output), []byte("ffmpeg version")) {
		return fmt.Errorf("ffmpeg is required but `ffmpeg -version` returned unexpected output")
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	if err != nil {
		return output, err
	}
	return output, nil
}
