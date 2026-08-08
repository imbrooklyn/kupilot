package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
)

func TestRunDispatchesStartIntents(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"

	tests := []struct {
		name       string
		args       []string
		wantIntent StartIntent
		wantError  string
	}{
		{name: "new Session", wantIntent: StartIntent{Kind: IntentNew}, wantError: "Starting a new Session is unavailable in this development build.\n"},
		{name: "resume picker", args: []string{"resume"}, wantIntent: StartIntent{Kind: IntentResumePicker}, wantError: "Session resume is unavailable in this development build.\n"},
		{name: "resume exact ID", args: []string{"resume", sessionID}, wantIntent: StartIntent{Kind: IntentResumeID, SessionID: sessionID}, wantError: "Session resume is unavailable in this development build.\n"},
		{name: "resume last", args: []string{"resume", "--last"}, wantIntent: StartIntent{Kind: IntentResumeLast}, wantError: "Session resume is unavailable in this development build.\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			calls := 0
			start := func(_ context.Context, got StartIntent) error {
				calls++
				if got != tt.wantIntent {
					t.Fatalf("start intent = %#v, want %#v", got, tt.wantIntent)
				}
				return UnavailableError{}
			}

			code := Run(context.Background(), tt.args, &stdout, &stderr, testBuildInfo(), start)
			if code != ExitUnavailable {
				t.Fatalf("Run() exit code = %d, want %d", code, ExitUnavailable)
			}
			if calls != 1 {
				t.Fatalf("start calls = %d, want 1", calls)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != tt.wantError {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantError)
			}
		})
	}
}

func TestRunShortCircuitsHelpAndVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		wantContent string
	}{
		{name: "root help", args: []string{"help"}, wantContent: "Running kupilot without a subcommand starts a new Session."},
		{name: "resume help", args: []string{"resume", "--help"}, wantContent: "kupilot resume [SESSION_ID | --last]"},
		{name: "version help", args: []string{"help", "version"}, wantContent: "Print non-sensitive build information."},
		{name: "help help", args: []string{"help", "help"}, wantContent: "Show help for a command."},
		{name: "version", args: []string{"version"}, wantContent: "kupilot version=v0.0.0-test commit=0123456789ab built=2026-08-08T00:00:00Z go=go1.25.0 platform=linux/arm64\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			calls := 0
			start := func(context.Context, StartIntent) error {
				calls++
				return nil
			}

			code := Run(context.Background(), tt.args, &stdout, &stderr, testBuildInfo(), start)
			if code != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", code, ExitOK)
			}
			if calls != 0 {
				t.Fatalf("start calls = %d, want 0", calls)
			}
			if !strings.Contains(stdout.String(), tt.wantContent) {
				t.Fatalf("stdout = %q, want content %q", stdout.String(), tt.wantContent)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRootHelpListsOnlyFixedCommands(t *testing.T) {
	t.Parallel()

	help := Help(HelpRoot)
	for _, want := range []string{"  resume ", "  version", "  help   "} {
		if !strings.Contains(help, want) {
			t.Errorf("root help does not contain command entry %q", want)
		}
	}

	forbidden := []string{
		"cwd",
		"workspace",
		"--all",
		"--cd",
		"--api-key",
		"--token",
		"--kubeconfig-content",
		"  exec ",
		"  fork ",
		"  run ",
		"  server ",
		"  sessions ",
	}
	for _, value := range forbidden {
		if strings.Contains(help, value) {
			t.Errorf("root help contains out-of-scope text %q", value)
		}
	}
}

func TestRunUsesStableErrorExitCodes(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("q", 32)
	tests := []struct {
		name       string
		args       []string
		start      StartFunc
		wantCode   int
		wantError  string
		wantCalls  int
		wantAbsent string
	}{
		{
			name:      "usage error",
			args:      []string{"status"},
			start:     func(context.Context, StartIntent) error { return nil },
			wantCode:  ExitUsage,
			wantError: "Error: unknown command\n",
		},
		{
			name:       "sensitive value option",
			args:       []string{"--api-key=" + canary},
			start:      func(context.Context, StartIntent) error { return nil },
			wantCode:   ExitUsage,
			wantError:  "Error: unknown option\n",
			wantAbsent: canary,
		},
		{
			name:       "invalid Session ID",
			args:       []string{"resume", canary},
			start:      func(context.Context, StartIntent) error { return nil },
			wantCode:   ExitUsage,
			wantError:  "Error: session ID must be a valid UUIDv7\n",
			wantAbsent: canary,
		},
		{
			name: "safe internal error",
			start: func(context.Context, StartIntent) error {
				return errors.New(strings.Repeat("x", 24))
			},
			wantCode:   ExitFailure,
			wantError:  "KuPilot could not start.\n",
			wantCalls:  1,
			wantAbsent: strings.Repeat("x", 24),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			calls := 0
			start := func(ctx context.Context, intent StartIntent) error {
				calls++
				return tt.start(ctx, intent)
			}

			code := Run(context.Background(), tt.args, &stdout, &stderr, testBuildInfo(), start)
			if code != tt.wantCode {
				t.Fatalf("Run() exit code = %d, want %d", code, tt.wantCode)
			}
			if calls != tt.wantCalls {
				t.Fatalf("start calls = %d, want %d", calls, tt.wantCalls)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != tt.wantError {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantError)
			}
			if tt.wantAbsent != "" && strings.Contains(stderr.String(), tt.wantAbsent) {
				t.Fatalf("stderr contains unsafe internal error")
			}
		})
	}
}

func TestRunHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	calls := 0
	start := func(context.Context, StartIntent) error {
		calls++
		return nil
	}

	code := Run(ctx, nil, &stdout, &stderr, testBuildInfo(), start)
	if code != ExitInterrupted {
		t.Fatalf("Run() exit code = %d, want %d", code, ExitInterrupted)
	}
	if calls != 0 {
		t.Fatalf("start calls = %d, want 0", calls)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "Interrupted.\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "Interrupted.\n")
	}
}

func testBuildInfo() buildinfo.Info {
	return buildinfo.Info{
		Version:   "v0.0.0-test",
		Commit:    "0123456789ab",
		BuildTime: "2026-08-08T00:00:00Z",
		GoVersion: "go1.25.0",
		GOOS:      "linux",
		GOARCH:    "arm64",
	}
}
