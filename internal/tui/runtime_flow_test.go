package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestFakeEventStreamCoversDeltaToolCompletionCancellationAndError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		terminal   application.UIEventKind
		text       string
		wantStatus string
	}{
		{name: "completion", terminal: application.UIEventRunCompleted, text: "Final diagnosis.", wantStatus: "completed"},
		{name: "cancellation", terminal: application.UIEventRunCancelled, text: "The AgentRun was cancelled.", wantStatus: "cancelled"},
		{name: "safe error", terminal: application.UIEventRunFailed, text: "The AgentRun failed safely.", wantStatus: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			stream := []application.UIEvent{
				runStartedEvent(1),
				{Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 2, Text: "Inspecting "},
				{Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 3, ToolStep: &application.ToolStep{
					InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
					Purpose: "Inspect the selected Resource.", Status: application.ToolStepRunning,
				}},
				{Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 4, Text: "current state."},
				{Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 5, ToolStep: &application.ToolStep{
					InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
					Status: application.ToolStepSucceeded, Summary: "Safe projected status.", EvidenceCount: 2,
				}},
				{Kind: tt.terminal, RunID: testRunID, ScopeGeneration: 7, Sequence: 6, Text: tt.text},
			}
			for _, event := range stream {
				model, _ = updateModel(t, model, ApplicationEventMsg{Event: event})
			}
			if model.run.Active || !model.run.Terminal || model.run.Status != tt.wantStatus || model.run.StreamedText != tt.text {
				t.Fatalf("terminal run = %#v", model.run)
			}
			steps := model.transcript.ToolSteps()
			if len(steps) != 1 || steps[0].Status != "succeeded" || steps[0].EvidenceCount != 2 {
				t.Fatalf("Tool steps = %#v", steps)
			}
			model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
				Kind: application.UIEventTextDelta, RunID: testRunID,
				ScopeGeneration: 7, Sequence: 7, Text: "late",
			}})
			if model.run.StreamedText != tt.text || model.run.LastSequence != 6 {
				t.Fatal("late event changed terminal state")
			}
		})
	}
}

func TestActiveRunDraftCanBeEditedButOnlyCancelCanDispatch(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "next question"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.composer.Value() != "next question" || !model.dialog.Open() {
		t.Fatal("active run accepted steer or queue input")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	command := applicationCommandFromCmd(t, cmd)
	if command.Kind != application.UICommandCancelRun || command.RunID != testRunID || model.composer.Value() != "next question" {
		t.Fatalf("cancel command = %#v", command)
	}
	model, cmd = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCancelled, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "The AgentRun was cancelled.",
	}})
	if cmd != nil || model.run.Status != "cancelled" || model.composer.Value() != "next question" {
		t.Fatal("ordinary cancellation exited or discarded the draft")
	}
}

func TestCtrlCCancelsActiveRunThenExitsAfterTerminalEvent(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 16, Height: 7, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 7, ReadOnly: true},
	})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command := applicationCommandFromCmd(t, cmd); command.Kind != application.UICommandCancelRun || !model.quitAfterCancel {
		t.Fatalf("Ctrl+C command = %#v", command)
	}
	model, cmd = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCancelled, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "The AgentRun was cancelled.",
	}})
	if !commandQuits(cmd) || model.quitAfterCancel {
		t.Fatal("terminal cancellation did not complete bounded exit")
	}
}

func TestFocusResizeAndSmallTerminalPreserveKeyboardSafety(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 20, Height: 8, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true},
	})
	model, _ = updateModel(t, model, tea.BlurMsg{})
	if model.FocusedEditorCount() != 0 {
		t.Fatal("terminal blur left the composer focused")
	}
	model, _ = updateModel(t, model, keyText("x"))
	if model.composer.Value() != "" {
		t.Fatal("blurred terminal input reached the composer")
	}
	model, _ = updateModel(t, model, tea.FocusMsg{})
	if model.FocusedEditorCount() != 1 {
		t.Fatal("terminal focus did not restore the sole composer")
	}
	model, _ = updateModel(t, model, tea.PasteMsg{Content: strings.Repeat("wide-\u754c ", 20)})
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 16, Height: 7})
	if model.EditorCount() != 1 || model.composer.Height() < MinComposerRows || model.composer.Height() > MaxComposerRows {
		t.Fatalf("small-terminal editor state = count %d, height %d", model.EditorCount(), model.composer.Height())
	}
	view := model.View()
	if !view.ReportFocus || !view.AltScreen || containsUnsafeTerminalText(sanitizeExternalText(view.Content, 0)) {
		t.Fatal("small-terminal View lost focus reporting, alternate screen, or terminal safety")
	}
	model.run = RunView{}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !commandQuits(cmd) {
		t.Fatal("small terminal could not exit")
	}
}
