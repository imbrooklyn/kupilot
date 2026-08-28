package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	"github.com/imbrooklyn/kupilot/internal/tui"
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

func TestCompositionRootCacheClearShortCircuitsOrdinaryStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KUPILOT_HOME", home)
	configPath := filepath.Join(home, "config.yaml")
	cachePath := filepath.Join(home, "cache")
	statePath := filepath.Join(home, "state")
	if err := os.WriteFile(configPath, []byte("invalid: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cachePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, "entry"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "preserve"), []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"cache", "clear"}, &stdout, &stderr, buildinfo.Info{})
	if code != cli.ExitOK || stdout.String() != "KuPilot cache cleared.\n" || stderr.String() != "" {
		t.Fatalf("cache clear = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if entries, err := os.ReadDir(cachePath); err != nil || len(entries) != 0 {
		t.Fatalf("cache entries = %v, %v", entries, err)
	}
	if content, err := os.ReadFile(configPath); err != nil || string(content) != "invalid: [" {
		t.Fatal("cache clear initialized or changed configuration")
	}
	if content, err := os.ReadFile(filepath.Join(statePath, "preserve")); err != nil || string(content) != "state" {
		t.Fatal("cache clear initialized or changed state")
	}
}

func TestApplicationRequestFilterRoutesEvidenceDetailAndRejectsOverflowSafely(t *testing.T) {
	t.Parallel()

	reference := application.UIEvidenceReference{
		EvidenceID: "0198a46e-7d2a-7d34-9b6f-2df5f45a2a31",
		RunID:      "0198a46e-7d2a-7d34-9b6f-2df5f45a2a32",
		Scope: domain.ScopeSnapshot{
			Context: "test-context", Namespace: "test-namespace", Generation: 7,
		},
		Sequence: 3, State: application.UIEvidenceDetailAvailable,
	}
	request := tui.ApplicationEvidenceDetailMsg{Query: application.UIEvidenceDetailQuery{
		RequestID: 9, Reference: reference,
	}}
	requests := make(chan tea.Msg, 1)
	filter := applicationRequestFilter(context.Background(), requests)
	if result := filter(nil, request); result != nil {
		t.Fatalf("routed Evidence detail result = %#v", result)
	}
	if routed := (<-requests).(tui.ApplicationEvidenceDetailMsg); routed.Query != request.Query {
		t.Fatalf("routed Evidence detail request = %#v", routed)
	}

	requests <- tui.ApplicationQueryMsg{}
	failure, ok := filter(nil, request).(tui.ApplicationFailureMsg)
	if !ok || failure.RequestID != request.Query.RequestID || failure.Evidence != reference ||
		failure.RunID != reference.RunID || failure.ScopeGeneration != reference.Scope.Generation {
		t.Fatalf("overflow Evidence failure identity = %#v", failure)
	}
}

func TestApplicationRequestFilterAndDrainDestroyRejectedModelSecrets(t *testing.T) {
	t.Parallel()

	secret, err := application.NewModelSetupSecret("generated-filter-key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requests := make(chan tea.Msg, 1)
	message := tui.ApplicationModelSetupMsg{Request: application.ModelSetupRequest{
		RequestID: 11, Endpoint: "https://model.example.test/v1", Model: "diagnostic-model", Secret: secret,
	}}
	failure, ok := applicationRequestFilter(ctx, requests)(nil, message).(tui.ApplicationFailureMsg)
	if !ok || !failure.ModelSetup || failure.RequestID != 11 || secret.IsSet() {
		t.Fatalf("cancelled filter result = %#v secret set=%v", failure, secret.IsSet())
	}

	queuedSecret, err := application.NewModelSetupSecret("generated-queued-key")
	if err != nil {
		t.Fatal(err)
	}
	requests <- tui.ApplicationModelSetupMsg{Request: application.ModelSetupRequest{
		RequestID: 12, Endpoint: "https://model.example.test/v1", Model: "diagnostic-model", Secret: queuedSecret,
	}}
	close(requests)
	destroyPendingApplicationRequests(requests)
	if queuedSecret.IsSet() {
		t.Fatal("queued model setup secret survived request drain")
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
