package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckFFmpegInstalled(t *testing.T) {
	tests := []struct {
		name    string
		run     commandRunner
		wantErr string
	}{
		{
			name: "installed",
			run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "ffmpeg" {
					t.Fatalf("command name = %q, want ffmpeg", name)
				}
				if len(args) != 1 || args[0] != "-version" {
					t.Fatalf("args = %v, want [-version]", args)
				}
				return []byte("ffmpeg version 6.1"), nil
			},
		},
		{
			name: "command fails",
			run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return nil, errors.New("executable file not found")
			},
			wantErr: "ffmpeg is required",
		},
		{
			name: "unexpected output",
			run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("not the expected tool"), nil
			},
			wantErr: "unexpected output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFFmpegInstalled(tt.run)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkFFmpegInstalled() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkFFmpegInstalled() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkFFmpegInstalled() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
