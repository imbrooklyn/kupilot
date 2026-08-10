package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/domain"
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
			wantError: "Starting a new Session is unavailable.\n",
		},
		{
			name:      "resume unavailable",
			args:      []string{"resume"},
			wantCode:  cli.ExitUnavailable,
			wantError: "Session resume is unavailable.\n",
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

func TestApplicationStartIntentPreservesOnlyExplicitScopeAuthority(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a25"
	tests := []struct {
		name         string
		input        cli.StartIntent
		want         application.UIStartIntent
		wantActivate bool
	}{
		{name: "new", input: cli.StartIntent{Kind: cli.IntentNew}, want: application.UIStartIntent{Kind: application.UIStartNew}, wantActivate: true},
		{name: "picker", input: cli.StartIntent{Kind: cli.IntentResumePicker}, want: application.UIStartIntent{Kind: application.UIStartResumePicker}},
		{name: "exact", input: cli.StartIntent{Kind: cli.IntentResumeID, SessionID: sessionID}, want: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: domain.SessionID(sessionID)}},
		{name: "last", input: cli.StartIntent{Kind: cli.IntentResumeLast}, want: application.UIStartIntent{Kind: application.UIStartResumeLast}},
		{
			name:  "resume with explicit Namespace",
			input: cli.StartIntent{Kind: cli.IntentResumeLast, Options: cli.StartOptions{Namespace: "payments", NamespaceSet: true}},
			want: application.UIStartIntent{
				Kind: application.UIStartResumeLast, ExplicitScope: true, ConfiguredNamespace: "payments",
			},
			wantActivate: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := applicationStartIntent(test.input)
			if err != nil || got != test.want || shouldActivateInitialScope(got) != test.wantActivate {
				t.Fatalf("applicationStartIntent() = %#v, %v; activate = %v", got, err, shouldActivateInitialScope(got))
			}
		})
	}
}
