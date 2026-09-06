package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestCopyUsesOnlyLatestCommittedFinalAndTerminalNativeSink(t *testing.T) {
	model := newTestModel()
	model.terminalClipboard = true
	model.transcript.StartAgent()
	model.transcript.FinishAgent("failed answer must not copy")
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("first committed answer")
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("latest \x1b]0;title canary\aanswer\u202e")
	model.transcript.StartAgent()
	model.transcript.AppendAgent("pending answer must not copy")

	model, command := model.copyLatestCommittedAnswer()
	if command == nil {
		t.Fatal("copy did not emit a terminal-native clipboard command")
	}
	message := command()
	want := "latest answer"
	if fmt.Sprint(message) != want || !strings.Contains(fmt.Sprintf("%T", message), "setClipboardMsg") {
		t.Fatalf("clipboard message = %T %q, want terminal-native %q", message, fmt.Sprint(message), want)
	}
	entries := model.transcript.Entries()
	if len(entries) == 0 || !strings.Contains(entries[len(entries)-1].Text, "sent to the terminal-native clipboard sink") {
		t.Fatal("successful copy did not add its fixed local notice")
	}
	unchanged, readCommand := updateModel(t, model, tea.ClipboardMsg{Content: "clipboard read canary"})
	if readCommand != nil || unchanged.composer.Value() != model.composer.Value() {
		t.Fatal("clipboard read input changed TUI state")
	}
}

func TestCopyUnavailableEmptyAndExactByteLimit(t *testing.T) {
	unsupported := newTestModel()
	unsupported.terminalClipboard = false
	unsupported.transcript.StartAgent()
	unsupported.transcript.FinishCommittedAgent("committed")
	unsupported, command := unsupported.copyLatestCommittedAnswer()
	if command != nil || !unsupported.dialog.Open() || !strings.Contains(unsupported.dialog.View(80), "could not be established") {
		t.Fatalf("unsupported clipboard result = command %t dialog %q", command != nil, unsupported.dialog.View(80))
	}

	empty := newTestModel()
	empty.terminalClipboard = true
	empty, command = empty.copyLatestCommittedAnswer()
	if command != nil || !strings.Contains(empty.dialog.View(80), "no committed assistant final") {
		t.Fatalf("empty clipboard result = command %t dialog %q", command != nil, empty.dialog.View(80))
	}

	exact := newTestModel()
	exact.terminalClipboard = true
	exact.transcript.StartAgent()
	exact.transcript.FinishCommittedAgent(strings.Repeat("x", MaxClipboardAnswerBytes))
	_, command = exact.copyLatestCommittedAnswer()
	if command == nil || len(fmt.Sprint(command())) != MaxClipboardAnswerBytes {
		t.Fatal("exact clipboard byte limit was not accepted")
	}

	over := newTestModel()
	over.terminalClipboard = true
	over.transcript.StartAgent()
	over.transcript.FinishCommittedAgent(strings.Repeat("x", MaxClipboardAnswerBytes+1))
	over, command = over.copyLatestCommittedAnswer()
	if command != nil || !strings.Contains(over.dialog.View(80), "exceeds the exact 65536-byte") {
		t.Fatalf("one-over clipboard result = command %t dialog %q", command != nil, over.dialog.View(80))
	}
}

func TestFindReusesComposerNavigatesAndLeavesNoShutdownState(t *testing.T) {
	model := newTestModel()
	model.transcript.AppendUser("Unicode α needle\nsecond line")
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("Committed needle answer.")
	before := model.TerminalTranscript()
	model.composer.SetValue("preserved ordinary draft")

	model, command := updateModel(t, model, tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if command != nil || !model.searchMode || model.EditorCount() != 1 {
		t.Fatalf("Ctrl+F search state = command %t mode %t editors %d", command != nil, model.searchMode, model.EditorCount())
	}
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "needle"})
	current, total, active := model.transcript.SearchState()
	if !active || current != 1 || total != 2 || !strings.Contains(model.View().Content, "Search match 1/2") {
		t.Fatalf("search result = %d/%d/%t %q", current, total, active, model.View().Content)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if current, _, _ = model.transcript.SearchState(); current != 2 {
		t.Fatalf("next match = %d", current)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if current, _, _ = model.transcript.SearchState(); current != 1 {
		t.Fatalf("previous match = %d", current)
	}
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 8, Height: 7})
	model, _ = updateModel(t, model, tea.MouseWheelMsg{})
	if !model.searchMode || model.EditorCount() != 1 {
		t.Fatal("narrow resize or mouse input escaped the sole search editor")
	}
	narrowTranscript := model.TerminalTranscript()
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.searchMode || model.composer.Value() != "preserved ordinary draft" || before == "" || model.TerminalTranscript() != narrowTranscript ||
		strings.Contains(model.PendingTerminalTranscript(), "Search match") || strings.Contains(model.PendingTerminalTranscript(), "⟦") {
		t.Fatalf("search state reached shutdown transcript: before=%q after=%q pending=%q", before, model.TerminalTranscript(), model.PendingTerminalTranscript())
	}
}

func TestQueueClearRequiresConfirmationAndBindsObservedRevision(t *testing.T) {
	model := newTestModel()
	model.conversationRevision = 9
	model.conversationStatus = application.ConversationInputStatus{Revision: 9, Editable: 3, Queued: 1, Rejected: 1, Recovered: 1}
	model, command := model.executeQueueCommand("clear")
	if command != nil || model.queueClearConfirmation == nil || !model.dialog.Open() ||
		!strings.Contains(model.dialog.View(80), "Remove 3 editable") {
		t.Fatalf("clear confirmation = command %t state %#v dialog %q", command != nil, model.queueClearConfirmation, model.dialog.View(80))
	}
	model, command = updateModel(t, model, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if command != nil || model.queueClearConfirmation != nil || model.dialog.Open() {
		t.Fatal("clear denial did not preserve the queue locally")
	}
	model, _ = model.executeQueueCommand("clear")
	model, command = updateModel(t, model, tea.KeyPressMsg{Code: 'y', Text: "y"})
	clear := applicationCommandFromCmd(t, command)
	if clear.Kind != application.UICommandClearFollowUps || clear.ExpectedQueueRevision != 9 ||
		clear.ExpectedScopeGeneration != 7 || clear.ExpectedPolicyGeneration != 1 || model.pendingConversation == nil {
		t.Fatalf("confirmed clear command/state = %#v/%#v", clear, model.pendingConversation)
	}
}

func TestQueueCancelCompactAndPlanEmitOnlyFixedTypedCommands(t *testing.T) {
	model := newTestModel()
	model.conversationRevision = 4
	model, command := model.executeQueueCommand("cancel " + string(testConversationItemID))
	cancel := applicationCommandFromCmd(t, command)
	if cancel.Kind != application.UICommandCancelFollowUp || cancel.ItemID != testConversationItemID || cancel.ExpectedQueueRevision != 4 {
		t.Fatalf("queue cancel command = %#v", cancel)
	}

	model.pendingConversation = nil
	model, command = model.requestManualCompaction()
	compact := applicationCommandFromCmd(t, command)
	if compact.Kind != application.UICommandCompactContext || compact.ExpectedScopeGeneration != 7 ||
		compact.ExpectedPolicyGeneration != 1 || model.pendingCompactionID != compact.RequestID {
		t.Fatalf("compact command/state = %#v/%d", compact, model.pendingCompactionID)
	}
	busyCompaction, duplicateCompaction := model.requestManualCompaction()
	if duplicateCompaction != nil || !busyCompaction.dialog.Open() || busyCompaction.pendingCompactionID != compact.RequestID {
		t.Fatal("a second manual compaction request was accepted before the correlated result")
	}
	model.closeDialog()

	model, command = model.executePlanCommand("")
	plan := applicationCommandFromCmd(t, command)
	if plan.Kind != application.UICommandArmPlan || plan.RequestID == 0 || model.pendingPlanID != plan.RequestID {
		t.Fatalf("plan command = %#v", plan)
	}
	busy, duplicate := model.executePlanCommand("off")
	if duplicate != nil || !busy.dialog.Open() || busy.pendingPlanID != plan.RequestID {
		t.Fatal("a second plan-mode change was accepted before the correlated result")
	}
	model.pendingPlanID = 0
	model.closeDialog()
	model, command = model.executePlanCommand("off")
	off := applicationCommandFromCmd(t, command)
	if off.Kind != application.UICommandCancelPlan || off.RequestID == 0 || model.pendingPlanID != off.RequestID {
		t.Fatalf("plan off command = %#v", off)
	}
}

func TestPlanModeOutcomeRequiresExactRequestCorrelation(t *testing.T) {
	model := newTestModel()
	model.pendingPlanID = 9
	armed := true
	model.acceptCommandOutcome(application.UICommandOutcome{
		Command: application.UICommandArmPlan, RequestID: 8, PlanArmed: &armed,
	})
	if model.planArmed || model.pendingPlanID != 9 {
		t.Fatal("stale plan-mode result changed delivery state")
	}
	model.acceptCommandOutcome(application.UICommandOutcome{
		Command: application.UICommandArmPlan, RequestID: 9, PlanArmed: &armed,
	})
	if !model.planArmed || model.pendingPlanID != 0 {
		t.Fatal("exact plan-mode result was not accepted")
	}
}

func TestContentFreeTerminalTitlesFollowOnlyAcceptedLifecycle(t *testing.T) {
	now := time.UnixMilli(1_700_000_600_000).UTC()
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor, TerminalStatusTitles: true,
		Scope: ScopeView{Context: "secret-scope-canary", Namespace: "secret-namespace-canary", Generation: 7, ReadOnly: true, Verified: true},
	})
	model.now = func() time.Time { return now }
	if got := model.View().WindowTitle; got != "Kupilot" {
		t.Fatalf("idle title = %q", got)
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "secret-input-canary")})
	if got := model.View().WindowTitle; got != "Kupilot — Working" {
		t.Fatalf("working title = %q", got)
	}
	wrong := application.UIEvent{Kind: application.UIEventRunFailed, RunID: "00000000-0000-7000-8000-000000009999", ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "secret-error-canary"}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: wrong})
	if got := model.View().WindowTitle; got != "Kupilot — Working" {
		t.Fatalf("foreign event title = %q", got)
	}
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	if got := model.View().WindowTitle; got != "Kupilot — Approval needed" {
		t.Fatalf("approval title = %q", got)
	}
	model.clearApproval()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3, Text: "secret-answer-canary",
	}})
	if got := model.View().WindowTitle; got != "Kupilot — Complete" {
		t.Fatalf("completion title = %q", got)
	}
	for _, rejected := range []application.UIEvent{
		{
			Kind: application.UIEventRunFailed, RunID: testRunID,
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3, Text: "duplicate terminal canary",
		},
		{
			Kind: application.UIEventRunFailed, RunID: testRunID,
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 4, Text: "late terminal canary",
		},
	} {
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: rejected})
		if got := model.View().WindowTitle; got != "Kupilot — Complete" {
			t.Fatalf("rejected terminal event changed title to %q", got)
		}
	}
	forbidden := []string{"secret-scope-canary", "secret-namespace-canary", "secret-input-canary", "secret-error-canary", "secret-answer-canary"}
	for _, value := range forbidden {
		if strings.Contains(model.View().WindowTitle, value) {
			t.Fatalf("dynamic content %q reached title %q", value, model.View().WindowTitle)
		}
	}

	disabled := newTestModel()
	if disabled.View().WindowTitle != "" {
		t.Fatalf("disabled title = %q", disabled.View().WindowTitle)
	}
}

func TestFailedAndCancelledTitlesUseTheFixedFailureState(t *testing.T) {
	for _, kind := range []application.UIEventKind{application.UIEventRunFailed, application.UIEventRunCancelled} {
		model := newTestModel()
		model.terminalStatusTitles = true
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
			Kind: kind, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "bounded failure",
		}})
		if got := model.View().WindowTitle; got != "Kupilot — Failed" {
			t.Fatalf("%s title = %q", kind, got)
		}
	}
}

func TestSessionResetClearsPriorTerminalTitleState(t *testing.T) {
	model := newTestModel()
	model.terminalStatusTitles = true
	model.run = RunView{RunID: testRunID, Terminal: true, Status: "completed"}
	if got := model.View().WindowTitle; got != "Kupilot — Complete" {
		t.Fatalf("setup title = %q", got)
	}
	model.resetTranscript()
	if got := model.View().WindowTitle; got != "Kupilot" {
		t.Fatalf("reset title = %q, want idle title", got)
	}
}
