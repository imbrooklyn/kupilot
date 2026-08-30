package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestActiveRunShowsCorrelatedWorkingStatusAndEscapeInterrupts(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 31, 8, 0, 0, 0, time.UTC)
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "test-context", Namespace: "test-namespace", Generation: 7, ReadOnly: true},
		Now:   func() time.Time { return startedAt },
	})
	model, command := updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	if command == nil || !strings.Contains(model.View().Content, "• Working (0s • esc to interrupt)") {
		t.Fatalf("run start did not schedule and render Working status: %q", model.View().Content)
	}

	model, command = updateModel(t, model, WorkingTickMsg{
		RunID: testRunID, ScopeGeneration: 7, Sequence: 1,
		At: startedAt.Add(time.Minute + 56*time.Second),
	})
	if command == nil || !strings.Contains(model.View().Content, "Working (1m 56s • esc to interrupt)") {
		t.Fatalf("Working status did not advance elapsed time: %q", model.View().Content)
	}

	model, command = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	interrupt := commandFromCmd(t, command)
	if interrupt.Kind != application.UICommandCancelRun || interrupt.RunID != testRunID ||
		interrupt.ExpectedScopeGeneration != 7 {
		t.Fatalf("Escape interrupt command = %#v", interrupt)
	}
}

func TestWorkingTickRejectsStaleAndTerminalMessages(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 31, 8, 0, 0, 0, time.UTC)
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "test-context", Namespace: "test-namespace", Generation: 7, ReadOnly: true},
		Now:   func() time.Time { return startedAt },
	})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	initialFrame := model.workingFrame
	initialAt := model.workingAt
	model, command := updateModel(t, model, WorkingTickMsg{
		RunID:           domain.AgentRunID("0198a46e-7d2a-7d34-9b6f-2df5f45a2aff"),
		ScopeGeneration: 7, Sequence: 1, At: startedAt.Add(time.Second),
	})
	if command != nil || model.workingFrame != initialFrame || !model.workingAt.Equal(initialAt) {
		t.Fatal("stale Working tick changed live render state or rescheduled itself")
	}

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "Ready.",
	}})
	model, command = updateModel(t, model, WorkingTickMsg{
		RunID: testRunID, ScopeGeneration: 7, Sequence: 1,
		At: startedAt.Add(2 * time.Second),
	})
	if command != nil || strings.Contains(model.View().Content, "Working (") {
		t.Fatalf("terminal run accepted or rendered a late Working tick: %q", model.View().Content)
	}
}
