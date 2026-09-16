package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashCompletionAvailability(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		active    bool
		editable  int
		draft     string
		wantOrder []string
		wantDraft string
	}{
		{name: "quit precedes unavailable queue", draft: "/qu", wantOrder: []string{"quit", "queue"}, wantDraft: "/quit"},
		{name: "available matches retain order", editable: 1, draft: "/qu", wantOrder: []string{"queue", "quit"}, wantDraft: "/queue "},
		{name: "queue precedes busy quit", active: true, editable: 1, draft: "/qu", wantOrder: []string{"queue", "quit"}, wantDraft: "/queue "},
		{name: "all unavailable preserve input", active: true, draft: "/qu", wantOrder: []string{"queue", "quit"}, wantDraft: "/qu"},
		{name: "only unavailable match", draft: "/que", wantOrder: []string{"queue"}, wantDraft: "/que"},
		{name: "no match", draft: "/does-not-exist", wantDraft: "/does-not-exist"},
		{name: "alias", draft: "/exi", wantOrder: []string{"quit"}, wantDraft: "/quit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			model.run.Active = test.active
			model.conversationStatus.Editable = test.editable
			model, cmd := updateModel(t, model, tea.PasteMsg{Content: test.draft})
			if cmd != nil {
				t.Fatal("filtering dispatched an action")
			}
			var names []string
			for _, candidate := range model.slashMenu.Candidates() {
				names = append(names, candidate.Name)
			}
			if !slices.Equal(names, test.wantOrder) {
				t.Fatalf("candidates = %v, want %v", names, test.wantOrder)
			}
			before := len(model.transcript.Entries())
			model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab})
			if cmd != nil || model.composer.Value() != test.wantDraft || model.dialog.Open() || len(model.transcript.Entries()) != before {
				t.Fatalf("completion = %q, command = %v, dialog = %v", model.composer.Value(), cmd != nil, model.dialog.Open())
			}
		})
	}
}

func TestSlashAvailabilityRankingPrecedesRowLimit(t *testing.T) {
	t.Parallel()
	model := newTestModel()
	model.run.Active = true
	model.scope.Switching = true
	model.pendingSubmitID = 1
	model.composer.SetValue("/")
	model.syncSlashMenu()
	want := []string{"help", "status", "doctor", "cancel", "model", "context", "namespace", "resource"}
	var names []string
	for _, candidate := range model.slashMenu.Candidates() {
		names = append(names, candidate.Name)
	}
	if !slices.Equal(names, want) {
		t.Fatalf("bounded order = %v, want %v", names, want)
	}
}

func TestSlashCompletionRechecksAvailabilityAndPreservesSelection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		beforeEditable int
		afterEditable  int
		active         bool
		wantDraft      string
	}{
		{name: "selected queue becomes unavailable", beforeEditable: 1, wantDraft: "/quit"},
		{name: "selected quit remains available after reorder", afterEditable: 1, wantDraft: "/quit"},
		{name: "all matches become unavailable", active: true, wantDraft: "/qu"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel()
			model.conversationStatus.Editable = test.beforeEditable
			model, _ = updateModel(t, model, tea.PasteMsg{Content: "/qu"})
			model.conversationStatus.Editable = test.afterEditable
			model.run.Active = test.active
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab})
			if cmd != nil || model.composer.Value() != test.wantDraft {
				t.Fatalf("stale completion = %q, command = %v", model.composer.Value(), cmd != nil)
			}
		})
	}
}

func TestSlashMenuRefreshesOnApplicationStateChange(t *testing.T) {
	t.Parallel()
	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/qu"})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	if _, ok := model.slashMenu.SelectedCandidate(); ok {
		t.Fatal("busy commands retained a selection")
	}
	for _, candidate := range model.slashMenu.Candidates() {
		if !candidate.Disabled || candidate.Reason == "" {
			t.Fatalf("stale availability: %#v", candidate)
		}
	}
}

func TestSlashNavigationSkipsUnavailableAndExplicitSubmitStillRejects(t *testing.T) {
	t.Parallel()
	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/qu"})
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyTab, Mod: tea.ModShift}} {
		var cmd tea.Cmd
		model, cmd = updateModel(t, model, key)
		selected, ok := model.slashMenu.SelectedCandidate()
		if cmd != nil || !ok || selected.Name != "quit" || model.composer.Value() != "/qu" {
			t.Fatalf("navigation selected %#v", selected)
		}
	}
	model.composer.SetValue("/queue")
	model.syncSlashMenu()
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.composer.Value() != "/queue" || !model.dialog.Open() || !strings.Contains(model.dialog.View(120), "There is no editable queued, rejected, or recovered input.") {
		t.Fatal("explicit unavailable command did not retain its denial")
	}
}
