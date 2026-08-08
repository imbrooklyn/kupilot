package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
)

func TestCompositionRoot(t *testing.T) {
	t.Parallel()

	info := buildinfo.Info{
		Version:   "v0.0.0-test",
		Commit:    "0123456789ab",
		BuildTime: "2026-08-08T00:00:00Z",
		GoVersion: "go1.25.0",
		GOOS:      "linux",
		GOARCH:    "amd64",
	}

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOutput string
		wantError  string
	}{
		{
			name:      "new Session unavailable",
			wantCode:  cli.ExitUnavailable,
			wantError: "Starting a new Session is unavailable in this development build.\n",
		},
		{
			name:      "resume unavailable",
			args:      []string{"resume"},
			wantCode:  cli.ExitUnavailable,
			wantError: "Session resume is unavailable in this development build.\n",
		},
		{
			name:       "help short circuit",
			args:       []string{"help"},
			wantCode:   cli.ExitOK,
			wantOutput: "Running kupilot without a subcommand starts a new Session.",
		},
		{
			name:       "version short circuit",
			args:       []string{"--version"},
			wantCode:   cli.ExitOK,
			wantOutput: "kupilot version=v0.0.0-test commit=0123456789ab built=2026-08-08T00:00:00Z go=go1.25.0 platform=linux/amd64",
		},
		{
			name:      "usage error",
			args:      []string{"status"},
			wantCode:  cli.ExitUsage,
			wantError: "Error: unknown command\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer

			code := run(context.Background(), tt.args, &stdout, &stderr, info)
			if code != tt.wantCode {
				t.Fatalf("run() exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantOutput) {
				t.Fatalf("stdout = %q, want content %q", stdout.String(), tt.wantOutput)
			}
			if stderr.String() != tt.wantError {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantError)
			}
		})
	}
}
