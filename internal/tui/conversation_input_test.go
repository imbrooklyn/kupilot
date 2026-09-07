package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const testConversationItemID domain.MessageID = "00000000-0000-7000-8000-000000008001"

func TestActiveConversationKeysRouteEnterTabAndAltUp(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "Initial question.")})

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Steer at the next boundary."})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	steer := applicationCommandFromCmd(t, command)
	if steer.Kind != application.UICommandSubmitSteer || steer.Text != "Steer at the next boundary." ||
		steer.RunID != testRunID || steer.ExpectedScopeGeneration != 7 || steer.ExpectedPolicyGeneration != 1 ||
		model.composer.Value() != "" || model.pendingConversation == nil {
		t.Fatalf("Enter steer state/command = %#v / %#v", model.pendingConversation, steer)
	}
	model = acceptConversationCommandResult(t, model, steer, application.ConversationInputPending, testConversationItemID)

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Queued after this turn."})
	model, command = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	queued := applicationCommandFromCmd(t, command)
	if queued.Kind != application.UICommandEnqueueFollowUp || queued.Text != "Queued after this turn." ||
		model.composer.Value() != "" {
		t.Fatalf("Tab queue command = %#v", queued)
	}
	queueID := domain.MessageID("00000000-0000-7000-8000-000000008002")
	model = acceptConversationCommandResult(t, model, queued, application.ConversationInputQueued, queueID)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		2, queueID, application.ConversationInputQueued, "Queued after this turn.",
	)})

	model, command = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	pop := applicationCommandFromCmd(t, command)
	if pop.Kind != application.UICommandPopFollowUp || pop.Text != "" || model.pendingConversation == nil {
		t.Fatalf("Alt+Up pop command = %#v", pop)
	}
	model = acceptConversationCommandResult(t, model, pop, application.ConversationInputQueued, queueID)
	if model.composer.Value() != "Queued after this turn." || model.pendingConversation != nil {
		t.Fatalf("edited composer/pending = %q/%#v", model.composer.Value(), model.pendingConversation)
	}
}

func TestConversationCommitEntersTranscriptExactlyOnceAndPreservesAgent(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "Initial question.")})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		1, testConversationItemID, application.ConversationInputPending, "Committed steer.",
	)})
	if entries := model.transcript.Entries(); len(entries) != 2 {
		t.Fatalf("pending steer entered transcript: %#v", entries)
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		2, testConversationItemID, application.ConversationInputCommitting, "Committed steer.",
	)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		3, testConversationItemID, application.ConversationInputCommitted, "Committed steer.",
	)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		3, testConversationItemID, application.ConversationInputCommitted, "Committed steer.",
	)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "Final stream.",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(
		application.UIEventRunCompleted, 3, "Final answer.",
	)})
	entries := model.transcript.Entries()
	if len(entries) != 3 || entries[0].Kind != components.EntryUser || entries[0].Text != "Initial question." ||
		entries[1].Kind != components.EntryUser || entries[1].Text != "Committed steer." ||
		entries[2].Kind != components.EntryAgent || entries[2].Text != "Final answer." || entries[2].Streaming {
		t.Fatalf("committed steer transcript = %#v", entries)
	}
}

func TestConversationEventsRejectStaleDuplicateAndWrongAuthority(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "Initial question.")})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		3, testConversationItemID, application.ConversationInputPending, "Current pending input.",
	)})

	stale := conversationEvent(2, testConversationItemID, application.ConversationInputCommitted, "Stale commit.")
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: stale})
	wrongRun := conversationEvent(4, testConversationItemID, application.ConversationInputCommitted, "Wrong run.")
	wrongRun.RunID = "00000000-0000-7000-8000-000000008099"
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: wrongRun})
	wrongScope := conversationEvent(4, testConversationItemID, application.ConversationInputCommitted, "Wrong scope.")
	wrongScope.ScopeGeneration++
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: wrongScope})
	wrongPolicy := conversationEvent(4, testConversationItemID, application.ConversationInputCommitted, "Wrong policy.")
	wrongPolicy.PolicyGeneration++
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: wrongPolicy})
	if model.conversationRevision != 3 || model.conversationStatus.Pending != 1 || len(model.transcript.Entries()) != 2 {
		t.Fatalf("stale or wrong-authority event changed state: revision=%d status=%#v transcript=%#v",
			model.conversationRevision, model.conversationStatus, model.transcript.Entries())
	}

	committed := conversationEvent(4, testConversationItemID, application.ConversationInputCommitted, "Current pending input.")
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: committed})
	duplicate := conversationEvent(5, testConversationItemID, application.ConversationInputCommitted, "Current pending input.")
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: duplicate})
	entries := model.transcript.Entries()
	if model.conversationRevision != 5 || len(entries) != 3 || entries[1].Kind != components.EntryUser ||
		entries[1].Text != "Current pending input." {
		t.Fatalf("accepted/duplicate commit state = revision %d transcript %#v", model.conversationRevision, entries)
	}
}

func TestConversationEditNeverOverwritesComposerOrLocalInteraction(t *testing.T) {
	base := newTestModel()
	base, _ = updateModel(t, base, ApplicationEventMsg{Event: runStartedEvent(1)})
	base, _ = updateModel(t, base, ApplicationEventMsg{Event: conversationEvent(
		1, testConversationItemID, application.ConversationInputRecovered, "Recovered input.",
	)})

	nonempty := base
	nonempty.composer.SetValue("Current draft")
	nonempty, command := updateModel(t, nonempty, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if command != nil || nonempty.composer.Value() != "Current draft" {
		t.Fatal("Alt+Up overwrote a non-empty composer")
	}

	modal := base
	modal.showDialog("Local interaction", "The modal owns this key.")
	modal, command = updateModel(t, modal, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if command != nil || modal.composer.Value() != "" || !modal.dialog.Open() {
		t.Fatal("Alt+Up escaped the active modal")
	}

	picker := base
	picker.composer.SetValue("/")
	picker.syncSuggestionsAfterEdit()
	picker, command = updateModel(t, picker, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if command != nil || !picker.slashMenu.Open() || picker.composer.Value() != "/" {
		t.Fatal("Alt+Up escaped the visible Slash completion")
	}

	emptyPicker := base
	emptyPicker.activePicker = application.UICompletionContext
	emptyPicker.contextPicker.SetLoading()
	emptyPicker, command = updateModel(t, emptyPicker, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if command != nil || !emptyPicker.contextPicker.Open() || emptyPicker.composer.Value() != "" {
		t.Fatal("Alt+Up escaped an active picker with an empty composer")
	}

	reviewer := base
	reviewer.actionPresentation = &actionPresentation{
		RequestID: "00000000-0000-7000-8000-000000008101",
		Digest:    domain.ApprovalDigest(strings.Repeat("a", 64)), Sequence: 2,
	}
	reviewer, command = updateModel(t, reviewer, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if command != nil || reviewer.composer.Value() != "" {
		t.Fatal("Alt+Up escaped active Reviewer supervision")
	}
}

func TestActiveTabKeepsSlashAndBangSyntaxOutOfQueue(t *testing.T) {
	for _, draft := range []string{"/status", "/unknown", "!command"} {
		t.Run(draft, func(t *testing.T) {
			model := newTestModel()
			model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
			model.composer.SetValue(draft)
			model.slashMenu.Close()
			model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
			if command != nil || model.composer.Value() != draft {
				t.Fatalf("active Tab queued local syntax: command=%v draft=%q", command != nil, model.composer.Value())
			}
		})
	}
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.composer.SetValue("//status")
	model.slashMenu.Close()
	_, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	queued := applicationCommandFromCmd(t, command)
	if queued.Kind != application.UICommandEnqueueFollowUp || queued.Text != "/status" {
		t.Fatalf("escaped chat queue command = %#v", queued)
	}
}

func TestCommittedEscapedSteerAndAutomaticSuccessorPreserveOrdinaryChatHistory(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.composer.SetValue("//status")
	model.slashMenu.Close()
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	steer := applicationCommandFromCmd(t, command)
	model = acceptConversationCommandResult(t, model, steer, application.ConversationInputPending, testConversationItemID)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		2, testConversationItemID, application.ConversationInputCommitted, "/status",
	)})
	if !model.composer.PreviousHistory() || model.composer.Value() != "//status" {
		t.Fatalf("committed escaped steer history = %q", model.composer.Value())
	}

	automatic := newTestModel()
	automatic, _ = updateModel(t, automatic, ApplicationEventMsg{Event: runStartedEvent(1, "/status")})
	if !automatic.composer.PreviousHistory() || automatic.composer.Value() != "//status" {
		t.Fatalf("automatic escaped successor history = %q", automatic.composer.Value())
	}
}

func TestConversationPreviewIsBoundedBeforeLayoutAndStatusIsContentFree(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	text := strings.Repeat("bounded text ", 100) + "\nsecond line\nforbidden third line"
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: conversationEvent(
		1, testConversationItemID, application.ConversationInputUnknown, text,
	)})
	working := sanitizeExternalText(model.workingView(), 0)
	if !strings.Contains(working, "Unknown outcome") || strings.Contains(working, "forbidden third line") ||
		len(boundedConversationPreviewText(text)) > maxConversationPreviewTextBytes+8 {
		t.Fatalf("bounded conversation preview = %q", working)
	}
	status := application.UIStatusResult{ConversationInput: model.conversationStatus}
	if strings.Contains(fmt.Sprintf("%#v", status.ConversationInput), "bounded text") {
		t.Fatal("content-bearing preview entered status projection")
	}
}

func acceptConversationCommandResult(
	t *testing.T,
	model Model,
	command application.UICommand,
	state application.ConversationInputState,
	itemID domain.MessageID,
) Model {
	t.Helper()
	projection := application.ConversationInputProjection{
		ItemID: itemID, RunID: command.RunID, State: state, Text: command.Text,
		ContentHash: domain.MessageContentHash(command.Text),
		CreatedAt:   time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Revision: 1,
	}
	if command.Kind == application.UICommandPopFollowUp {
		projection.Text = "Queued after this turn."
		projection.ContentHash = domain.MessageContentHash(projection.Text)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: command.Kind, RequestID: command.RequestID, RunID: command.RunID,
		ConversationInput: &projection,
	}})
	return model
}

func conversationEvent(
	revision int64,
	itemID domain.MessageID,
	state application.ConversationInputState,
	text string,
) application.UIEvent {
	status := application.ConversationInputStatus{
		Revision: revision, Items: 1, Bytes: len(text),
		MaximumItems:     application.MaxConversationInputItems,
		MaximumBytes:     application.MaxConversationInputAggregateBytes,
		MaximumItemBytes: application.MaxConversationInputItemBytes,
	}
	switch state {
	case application.ConversationInputPending:
		status.Pending = 1
	case application.ConversationInputCommitting:
		status.Committing = 1
	case application.ConversationInputCommitted:
		status.Committed = 1
	case application.ConversationInputQueued:
		status.Queued, status.Editable = 1, 1
	case application.ConversationInputRejected:
		status.Rejected, status.Editable = 1, 1
	case application.ConversationInputRecovered:
		status.Recovered, status.Editable = 1, 1
	case application.ConversationInputUnknown:
		status.Unknown = 1
	}
	projection := application.ConversationInputProjection{
		ItemID: itemID, RunID: testRunID, State: state, Text: text,
		ContentHash: domain.MessageContentHash(text),
		CreatedAt:   time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Revision: revision,
	}
	input := application.UIConversationInputEvent{Revision: revision, Status: status, Changed: &projection}
	if state != application.ConversationInputCommitted {
		input.Preview = []application.ConversationInputProjection{projection}
	}
	return application.UIEvent{
		Kind: application.UIEventConversationInput, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, ConversationInput: &input,
	}
}
