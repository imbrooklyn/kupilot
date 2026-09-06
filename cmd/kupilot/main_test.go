package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	"github.com/imbrooklyn/kupilot/internal/tui"
)

func TestConfiguredBudgetLimitsSelectsProfileAndHonorsTighterModelTimeout(t *testing.T) {
	value := config.Defaults()
	value.Runtime.BudgetProfile = config.BudgetProfileExtended
	limits, err := configuredBudgetLimits(value)
	if err != nil || limits.Profile != agent.BudgetProfileExtended || limits.RunDuration != 30*time.Minute ||
		limits.ModelRequestTimeout != 300*time.Second {
		t.Fatalf("configuredBudgetLimits(extended) = %#v, %v", limits, err)
	}
	value.Models.Agent.RequestTimeoutSeconds = 30
	limits, err = configuredBudgetLimits(value)
	if err != nil || limits.ModelRequestTimeout != 30*time.Second || limits.Profile != agent.BudgetProfileExtended {
		t.Fatalf("configuredBudgetLimits(tightened) = %#v, %v", limits, err)
	}
	value.Runtime.BudgetProfile = "unlimited"
	if _, err := configuredBudgetLimits(value); !errors.Is(err, agent.ErrInvalidRunBudget) {
		t.Fatalf("configuredBudgetLimits(unknown) error = %v", err)
	}
}

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
	if code != cli.ExitOK || stdout.String() != "Kupilot cache cleared.\n" || stderr.String() != "" {
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

func TestTerminalRuntimeCleanupWritesOnlyPendingSafeHistory(t *testing.T) {
	t.Parallel()

	model := tui.NewModel(tui.Config{
		Width: 80, Height: 24, Theme: tui.ThemeNoColor,
		Scope: tui.ScopeView{Context: "test-context", Namespace: "test-namespace", Generation: 1, ReadOnly: true, Verified: true},
	})
	model = updateTUIModel(t, model, tea.PasteMsg{Content: "How many Nodes are Ready?"})
	model = updateTUIModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	runID := domain.AgentRunID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a10")
	model = updateTUIModel(t, model, tui.ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunStarted, RunID: runID, ScopeGeneration: 1, PolicyGeneration: 1, Sequence: 1,
		Text: "How many Nodes are Ready?",
	}})
	model = updateTUIModel(t, model, tui.ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: runID, ScopeGeneration: 1, PolicyGeneration: 1, Sequence: 2,
		Text: "Three Nodes are Ready.",
	}})

	var output bytes.Buffer
	if err := restoreTerminalAfterRuntime(&output, model, nil); err != nil {
		t.Fatalf("restoreTerminalAfterRuntime() error = %v", err)
	}
	if !strings.HasPrefix(output.String(), "\r\x1b[") ||
		strings.Count(output.String(), "How many Nodes are Ready?") != 1 ||
		strings.Count(output.String(), "Three Nodes are Ready.") != 1 ||
		strings.Contains(output.String(), "Ask a question") || strings.Contains(output.String(), "supervised") {
		t.Fatalf("completed terminal transcript = %q", output.String())
	}

	output.Reset()
	if err := restoreTerminalAfterRuntime(&output, model, context.Canceled); err != nil ||
		strings.Contains(output.String(), "How many Nodes are Ready?") ||
		strings.Contains(output.String(), "Three Nodes are Ready.") ||
		!strings.HasSuffix(output.String(), "\x1b[J") {
		t.Fatalf("interrupted runtime did not limit output to live-frame cleanup: error=%v output=%q", err, output.String())
	}

	writeErr := errors.New("synthetic terminal write failure")
	if err := restoreTerminalAfterRuntime(terminalErrorWriter{err: writeErr}, model, nil); !errors.Is(err, writeErr) {
		t.Fatalf("terminal write failure = %v, want wrapped synthetic error", err)
	}
}

type terminalErrorWriter struct{ err error }

func (writer terminalErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestTerminalStatusTitleSlotIsBoundedAndRestoredExactlyOnce(t *testing.T) {
	var output bytes.Buffer
	restore, err := beginTerminalStatusTitlesOnWriter(&output, true)
	if err != nil || output.String() != terminalSaveTitleSlot {
		t.Fatalf("begin title slot = %q, %v", output.String(), err)
	}
	if err = restore(); err != nil {
		t.Fatalf("restore title slot error = %v", err)
	}
	if err = restore(); err != nil {
		t.Fatalf("duplicate restore title slot error = %v", err)
	}
	if output.String() != terminalSaveTitleSlot+terminalRestoreTitleSlot {
		t.Fatalf("title slot bytes = %q", output.String())
	}
	for _, forbidden := range []string{"Session", "resource", "Evidence", "error detail", "credential"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("dynamic content %q reached title slot bytes", forbidden)
		}
	}
}

func TestTerminalStatusTitleSlotDisabledNonTerminalAndWriteFailure(t *testing.T) {
	var output bytes.Buffer
	restore, err := beginTerminalStatusTitlesOnWriter(&output, false)
	if err != nil || restore == nil || restore() != nil || output.Len() != 0 {
		t.Fatalf("disabled title slot = bytes %q restore %v error %v", output.String(), restore != nil, err)
	}
	writeErr := errors.New("synthetic title write failure")
	if _, err = beginTerminalStatusTitlesOnWriter(terminalErrorWriter{err: writeErr}, true); !errors.Is(err, writeErr) {
		t.Fatalf("title save failure = %v", err)
	}
	if supported := terminalClipboardSupported(&output, func(string) string { return "kitty" }); supported {
		t.Fatal("non-terminal writer was treated as clipboard-capable")
	}
	if supported := terminalStatusTitlesSupported(&output, func(string) string { return "xterm-256color" }); supported {
		t.Fatal("non-terminal writer was treated as title-capable")
	}
}

func TestTerminalStatusTitleEnvironmentSupportIsConservative(t *testing.T) {
	lookup := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	for _, current := range []struct {
		name   string
		values map[string]string
		want   bool
	}{
		{name: "xterm", values: map[string]string{"TERM": "xterm-256color"}, want: true},
		{name: "known program", values: map[string]string{"TERM_PROGRAM": "WezTerm"}, want: true},
		{name: "tmux denied", values: map[string]string{"TERM": "xterm-256color", "TMUX": "active"}},
		{name: "screen denied", values: map[string]string{"TERM_PROGRAM": "iTerm.app", "STY": "active"}},
		{name: "unknown", values: map[string]string{"TERM": "vt100"}},
	} {
		t.Run(current.name, func(t *testing.T) {
			if got := terminalStatusTitleEnvironmentSupported(lookup(current.values)); got != current.want {
				t.Fatalf("terminal title support = %t, want %t", got, current.want)
			}
		})
	}
}

func updateTUIModel(t *testing.T, model tui.Model, message tea.Msg) tui.Model {
	t.Helper()
	next, _ := model.Update(message)
	updated, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("TUI update returned %T, want tui.Model", next)
	}
	return updated
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

func TestApplicationRequestFilterRoutesAndCorrelatesModelSetupCancellation(t *testing.T) {
	t.Parallel()

	requests := make(chan tea.Msg, 1)
	filter := applicationRequestFilter(context.Background(), requests)
	cancellation := tui.ApplicationModelSetupCancelMsg{RequestID: 17}
	if result := filter(nil, cancellation); result != nil {
		t.Fatalf("routed cancellation result = %#v", result)
	}
	if routed := (<-requests).(tui.ApplicationModelSetupCancelMsg); routed != cancellation {
		t.Fatalf("routed cancellation = %#v", routed)
	}
	requests <- tui.ApplicationQueryMsg{}
	rejected, ok := filter(nil, cancellation).(tui.ModelSetupCancelRejectedMsg)
	if !ok || rejected.RequestID != cancellation.RequestID {
		t.Fatalf("overflow cancellation rejection = %#v", rejected)
	}
}

func TestApplicationRequestPumpCancelsInFlightModelSetupAndDestroysSecret(t *testing.T) {
	t.Parallel()

	consumer := &cancellableSetupConsumer{started: make(chan struct{}), cancelled: make(chan struct{})}
	sender := &recordingApplicationSender{messages: make(chan tea.Msg, 1)}
	requests := make(chan tea.Msg, 2)
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(1)
	go pumpApplicationRequests(ctx, consumer, sender, requests, &workers)

	secret, err := application.NewModelSetupSecret("generated-pump-cancel-key")
	if err != nil {
		t.Fatal(err)
	}
	requests <- tui.ApplicationModelSetupMsg{Request: application.ModelSetupRequest{
		RequestID: 18, Endpoint: "https://model.example.test/v1", Model: "diagnostic-model", Secret: secret,
	}}
	<-consumer.started
	requests <- tui.ApplicationModelSetupCancelMsg{RequestID: 18}
	<-consumer.cancelled
	message := <-sender.messages
	failure, ok := message.(tui.ApplicationFailureMsg)
	if !ok || !failure.ModelSetup || failure.RequestID != 18 || secret.IsSet() {
		t.Fatalf("cancelled setup result = %#v secret set=%v", message, secret.IsSet())
	}

	cancel()
	close(requests)
	workers.Wait()
}

type recordingApplicationSender struct {
	messages chan tea.Msg
}

func (sender *recordingApplicationSender) Send(message tea.Msg) {
	sender.messages <- message
}

type cancellableSetupConsumer struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (consumer *cancellableSetupConsumer) ConfigureModel(ctx context.Context, _ application.ModelSetupRequest) (application.ModelSetupResult, error) {
	close(consumer.started)
	<-ctx.Done()
	close(consumer.cancelled)
	return application.ModelSetupResult{}, ctx.Err()
}

func (*cancellableSetupConsumer) QueryUI(context.Context, application.UICompletionQuery) (application.UICompletionResult, error) {
	return application.UICompletionResult{}, errors.New("unexpected query")
}

func (*cancellableSetupConsumer) QueryEvidenceDetail(context.Context, application.UIEvidenceDetailQuery) (application.UIEvidenceDetailResult, error) {
	return application.UIEvidenceDetailResult{}, errors.New("unexpected Evidence query")
}

func (*cancellableSetupConsumer) ResumeUI(context.Context, application.UIResumeRequest) (application.UIResumeResult, error) {
	return application.UIResumeResult{}, errors.New("unexpected resume")
}

func (*cancellableSetupConsumer) ExecuteUICommand(context.Context, application.UICommand) (application.UICommandOutcome, error) {
	return application.UICommandOutcome{}, errors.New("unexpected command")
}

func TestApplicationStartIntentMapsOnlyExplicitScopeSelection(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a25"
	tests := []struct {
		name  string
		input cli.StartIntent
		want  application.UIStartIntent
	}{
		{name: "new", input: cli.StartIntent{Kind: cli.IntentNew}, want: application.UIStartIntent{Kind: application.UIStartNew}},
		{name: "picker", input: cli.StartIntent{Kind: cli.IntentResumePicker}, want: application.UIStartIntent{Kind: application.UIStartResumePicker}},
		{name: "exact", input: cli.StartIntent{Kind: cli.IntentResumeID, SessionID: sessionID}, want: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: domain.SessionID(sessionID)}},
		{name: "last", input: cli.StartIntent{Kind: cli.IntentResumeLast}, want: application.UIStartIntent{Kind: application.UIStartResumeLast}},
		{
			name:  "resume with explicit Namespace",
			input: cli.StartIntent{Kind: cli.IntentResumeLast, Options: cli.StartOptions{Namespace: "payments", NamespaceSet: true}},
			want: application.UIStartIntent{
				Kind: application.UIStartResumeLast, ExplicitScope: true, ConfiguredNamespace: "payments",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := applicationStartIntent(test.input)
			if err != nil || got != test.want {
				t.Fatalf("applicationStartIntent() = %#v, %v", got, err)
			}
		})
	}
}
