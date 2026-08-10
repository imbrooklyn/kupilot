package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const (
	testRunID        domain.AgentRunID       = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"
	testInvocationID domain.ToolInvocationID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a11"
)

func TestUpdateMaintainsOneEditorAcrossStates(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, keyText("/"))
	if !model.slashMenu.Open() {
		t.Fatal("slash menu did not open")
	}
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	if !model.run.Active {
		t.Fatal("run did not become active")
	}
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/not-a-command"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !model.dialog.Open() {
		t.Fatal("unknown command did not open an error dialog")
	}
	if model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatal("modal added an editor or left the background editor focused")
	}
}

func TestUpdateSubmitsChatThroughDeferredTypedCommand(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Why is the Pod restarting?"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := commandFromCmd(t, cmd)
	if intent.Kind != application.UICommandSubmitQuestion || intent.Text != "Why is the Pod restarting?" ||
		intent.ExpectedScopeGeneration != 7 {
		t.Fatalf("command = %#v", intent)
	}
	if model.composer.Value() != "" {
		t.Fatalf("draft after submit = %q", model.composer.Value())
	}
	if got := model.transcript.Entries(); len(got) != 1 || got[0].Kind != components.EntryUser || got[0].Text != intent.Text {
		t.Fatalf("transcript entries = %#v", got)
	}
}

func TestUpdateEnterAndCtrlJHaveDistinctBehavior(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, keyText("a"))
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := model.composer.Value(); got != "a\n" {
		t.Fatalf("draft after Ctrl+J = %q", got)
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if commandFromCmd(t, cmd).Text != "a\n" || model.composer.Value() != "" {
		t.Fatal("Enter did not submit and clear the multiline draft")
	}

	model, _ = updateModel(t, model, keyText("x"))
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	if cmd != nil || model.composer.Value() != "x" {
		t.Fatal("Tab without a completion changed the draft or returned an action")
	}
}

func TestUpdateComposerHeightPasteHistoryAndInternalScroll(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	if model.composer.Height() != MinComposerRows {
		t.Fatalf("initial height = %d", model.composer.Height())
	}

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "one\ntwo\nthree\nfour\nfive"})
	if model.composer.Height() != 5 {
		t.Fatalf("five-line height = %d", model.composer.Height())
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "second"})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "second" {
		t.Fatalf("first history item = %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "one\ntwo\nthree\nfour\nfive" {
		t.Fatalf("second history item = %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := model.composer.Value(); got != "" {
		t.Fatalf("history did not return to empty draft: %q", got)
	}

	model, _ = updateModel(t, model, tea.PasteMsg{Content: strings.Repeat("line\n", 11) + "last"})
	if model.composer.Height() != MaxComposerRows {
		t.Fatalf("maximum height = %d", model.composer.Height())
	}
	if model.composer.ScrollOffset() == 0 {
		t.Fatal("composer did not scroll internally past eight rows")
	}
}

func TestUpdateSanitizesPasteBeforeRenderState(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	unsafe := "safe\x1b[31mred\x1b[0m\x1b]52;c;ignored\x07\x00\u202esuffix"
	model, _ = updateModel(t, model, tea.PasteMsg{Content: unsafe})
	got := model.composer.Value()
	if strings.Contains(got, "ignored") || containsUnsafeTerminalText(got) {
		t.Fatalf("unsafe paste reached render state: %q", got)
	}
	if !strings.Contains(got, "safered") || !strings.Contains(got, "suffix") {
		t.Fatalf("safe paste content was lost: %q", got)
	}

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: '\u202e', Text: "\u202e"})
	if got := model.composer.Value(); got != "saferedsuffix" {
		t.Fatalf("unsafe key text reached render state: %q", got)
	}
}

func TestUpdateEscapedSlashHistoryPreservesChatMeaning(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "//help"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent := commandFromCmd(t, cmd); intent.Kind != application.UICommandSubmitQuestion || intent.Text != "/help" {
		t.Fatalf("first escaped intent = %#v", intent)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if model.composer.Value() != "//help" {
		t.Fatalf("escaped history draft = %q", model.composer.Value())
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent := commandFromCmd(t, cmd); intent.Kind != application.UICommandSubmitQuestion || intent.Text != "/help" {
		t.Fatalf("replayed escaped intent = %#v", intent)
	}
}

func TestUpdateSlashSelectionDispatchAndDenials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		draft       string
		wantKind    application.UICommandKind
		wantText    string
		wantCommand bool
		wantDialog  bool
	}{
		{name: "literal slash is chat", draft: "//help", wantKind: application.UICommandSubmitQuestion, wantText: "/help", wantCommand: true},
		{name: "multiline slash is chat", draft: "/help\nexplain", wantKind: application.UICommandSubmitQuestion, wantText: "/help\nexplain", wantCommand: true},
		{name: "unknown slash", draft: "/does-not-exist", wantDialog: true},
		{name: "bang syntax", draft: "!kubectl get pods", wantDialog: true},
		{name: "multiline bang syntax", draft: "!command\nargument", wantDialog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			model, _ = updateModel(t, model, tea.PasteMsg{Content: tt.draft})
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			if tt.wantCommand {
				intent := commandFromCmd(t, cmd)
				if intent.Kind != tt.wantKind || intent.Text != tt.wantText {
					t.Fatalf("command = %#v", intent)
				}
			} else if cmd != nil {
				t.Fatal("denied input returned an action")
			}
			if model.dialog.Open() != tt.wantDialog {
				t.Fatalf("dialog open = %v", model.dialog.Open())
			}
			if tt.wantDialog && model.composer.Value() != tt.draft {
				t.Fatalf("denied draft changed to %q", model.composer.Value())
			}
		})
	}
}

func TestUpdateSlashMenuFiltersNavigatesAndCompletes(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/"})
	if got := model.slashMenu.Candidates(); len(got) != MaxSlashCandidates {
		t.Fatalf("initial candidates = %d", len(got))
	}
	model, _ = updateModel(t, model, keyText("r"))
	model, _ = updateModel(t, model, keyText("e"))
	model, _ = updateModel(t, model, keyText("s"))
	if got := model.slashMenu.Candidates(); len(got) == 0 || got[0].Name != "resource" {
		t.Fatalf("filtered candidates = %#v", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	selected := model.slashMenu.Selected()
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if model.slashMenu.Selected() == selected && len(model.slashMenu.Candidates()) > 1 {
		t.Fatal("direction keys did not change selection")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd == nil {
		t.Fatal("Tab did not request typed Resource completion")
	}
	message, ok := cmd().(ApplicationQueryMsg)
	if !ok || message.Query.Kind != application.UICompletionResource {
		t.Fatalf("Tab command = %#v", cmd())
	}
	if got := model.composer.Value(); got != "/resource " {
		t.Fatalf("completed draft = %q", got)
	}
}

func TestUpdateDisabledSlashHasZeroAction(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/cancel"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !model.dialog.Open() || model.composer.Value() != "/cancel" {
		t.Fatal("disabled /cancel did not fail closed")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := commandFromCmd(t, cmd)
	if intent.Kind != application.UICommandCancelRun || intent.RunID != testRunID || intent.ExpectedScopeGeneration != 7 {
		t.Fatalf("cancel command = %#v", intent)
	}
}

func TestSessionAndStatusSlashCommandsDispatchTypedApplicationIntents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		draft    string
		wantKind application.UICommandKind
		wantText string
	}{
		{draft: "/new", wantKind: application.UICommandNewSession},
		{draft: "/rename", wantKind: application.UICommandRenameSession},
		{draft: "/rename Incident review", wantKind: application.UICommandRenameSession, wantText: "Incident review"},
		{draft: "/status", wantKind: application.UICommandShowStatus},
	}
	for _, test := range tests {
		t.Run(test.draft, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			model, _ = updateModel(t, model, tea.PasteMsg{Content: test.draft})
			_, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			command := applicationCommandFromCmd(t, cmd)
			if command.Kind != test.wantKind || command.Text != test.wantText {
				t.Fatalf("command = %#v", command)
			}
		})
	}
}

func TestUpdateActiveRunPreservesDraftAndRejectsLateEvents(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "next question"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.composer.Value() != "next question" {
		t.Fatal("active run submission did not fail closed")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 2, Text: "first",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 2, Text: "duplicate",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 8, Sequence: 3, Text: "stale",
	}})
	if model.run.StreamedText != "first" || model.run.LastSequence != 2 {
		t.Fatalf("run after rejected events = %#v", model.run)
	}

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetResource,
			Purpose:      "Inspect the selected Pod.",
			Status:       application.ToolStepRunning,
		},
	}})
	if len(model.transcript.ToolSteps()) != 1 {
		t.Fatal("Tool step was not placed inline")
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 4,
		ToolStep: &application.ToolStep{
			InvocationID:  testInvocationID,
			Name:          domain.ToolNameGetResource,
			Status:        application.ToolStepSucceeded,
			EvidenceCount: 2,
		},
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 5,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetResource,
			Status:       application.ToolStepRunning,
		},
	}})
	if got := model.transcript.ToolSteps(); len(got) != 1 || got[0].Status != string(application.ToolStepSucceeded) {
		t.Fatalf("terminal Tool step regressed: %#v", got)
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID, ScopeGeneration: 7, Sequence: 6, Text: "Final diagnosis.",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 7, Text: "late",
	}})
	if model.run.Active || !model.run.Terminal || model.run.StreamedText != "Final diagnosis." || model.run.LastSequence != 6 {
		t.Fatalf("terminal run state = %#v", model.run)
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	entries := model.transcript.Entries()
	if len(entries) != 2 || len(entries[0].ToolSteps) != 1 {
		t.Fatalf("historic Tool steps were not retained: %#v", entries)
	}
}

func TestUpdateBoundsCumulativeStreamText(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 2,
		Text: strings.Repeat("x", application.MaxQuestionBytes-1),
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 3, Text: "yz",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 4, Text: "overflow",
	}})
	if len(model.run.StreamedText) != application.MaxQuestionBytes || model.run.LastSequence != 4 {
		t.Fatalf("bounded stream state = bytes %d, sequence %d", len(model.run.StreamedText), model.run.LastSequence)
	}
}

func TestUpdateComposerSoftWrapLimitAndResize(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 20, Height: 16, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true},
	})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: strings.Repeat("x", 80)})
	if model.composer.Height() <= MinComposerRows || model.composer.Height() > MaxComposerRows {
		t.Fatalf("soft-wrapped height = %d", model.composer.Height())
	}

	limited := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true},
	})
	limited, cmd := updateModel(t, limited, tea.PasteMsg{Content: strings.Repeat("x", application.MaxQuestionBytes+1)})
	if cmd != nil || limited.composer.Value() != "" || !limited.dialog.Open() {
		t.Fatal("oversized paste did not fail closed without partial insertion")
	}

	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 40, Height: 12})
	view := model.View()
	if model.width != 40 || model.height != 12 || model.EditorCount() != 1 || view.OnMouse != nil {
		t.Fatalf("resize or mouse invariant failed: size=%dx%d editors=%d", model.width, model.height, model.EditorCount())
	}
}

func newTestModel() Model {
	return NewModel(Config{
		Width:  80,
		Height: 24,
		Theme:  ThemeNoColor,
		Scope: ScopeView{
			Context:    "test-context",
			Namespace:  "test-namespace",
			Generation: 7,
			ReadOnly:   true,
		},
	})
}

func runStartedEvent(sequence int64) application.UIEvent {
	return application.UIEvent{
		Kind:            application.UIEventRunStarted,
		RunID:           testRunID,
		ScopeGeneration: 7,
		Sequence:        sequence,
	}
}

func updateModel(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	result, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return result, cmd
}

func keyText(value string) tea.KeyPressMsg {
	runes := []rune(value)
	return tea.KeyPressMsg{Code: runes[0], Text: value}
}

func commandFromCmd(t *testing.T, cmd tea.Cmd) application.UICommand {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command, got nil")
	}
	msg := cmd()
	intent, ok := msg.(ApplicationCommandMsg)
	if !ok {
		t.Fatalf("command returned %T, want ApplicationCommandMsg", msg)
	}
	if err := intent.Command.Validate(); err != nil {
		t.Fatalf("command validation error = %v", err)
	}
	return intent.Command
}

func assertSingleEditor(t *testing.T, model Model) {
	t.Helper()
	if model.EditorCount() != 1 || model.FocusedEditorCount() != 1 || !model.composer.Focused() {
		t.Fatalf("editor invariant = total %d focused %d", model.EditorCount(), model.FocusedEditorCount())
	}
}
