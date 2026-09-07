package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPrivacyReviewDisplaysExactPolicyAndDispatchesTypedDecisions(t *testing.T) {
	manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: new(tuiPrivacyStore), Origin: "https://model.example",
		Now: func() time.Time { return time.UnixMilli(30_000).UTC() },
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	lifecycle := application.SessionLifecycleReview{
		CurrentSession: &application.UISessionState{
			ID: testSessionID, PrivacyMode: domain.PrivacyModeStandard,
		},
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		ReadAuditRetentionDays:         application.ReadAuditRetentionDays,
		WriteAuditRetentionDays:        application.WriteAuditRetentionDays,
		StandardContentUntilDeletion:   true,
	}
	blocked := newTestModel()
	blocked, _ = updateModel(t, blocked, tea.PasteMsg{Content: "Why is the Pod pending?"})
	blocked, cmd := updateModel(t, blocked, tea.KeyPressMsg{Code: tea.KeyEnter})
	submit := applicationCommandFromCmd(t, cmd)
	blocked, _ = updateModel(t, blocked, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandSubmitQuestion, RequestID: submit.RequestID,
		QuestionStart: &application.UIQuestionStartFailure{
			Reason: application.QuestionStartConsentRequired, Recovery: application.QuestionStartRecoverReviewConsent,
			State: application.UIQuestionStartCurrentState{
				SessionID: testSessionID, ScopeState: application.ScopeStateActive, ScopeGeneration: 7,
				Context: "test-context", Namespace: "test-namespace", ReadOnly: true,
				PolicyGeneration: 1, PolicyHealthy: true, RunState: application.QuestionStartRunStateIdle,
				ResourceState: application.QuestionStartResourceNone,
			},
		},
		Privacy: &review,
	}})
	if !blocked.dialog.Open() || blocked.pendingPrivacyID != submit.RequestID ||
		!strings.Contains(blocked.render(), "https://model.example") {
		t.Fatal("first transfer was not blocked on the exact privacy review")
	}
	blocked, cmd = updateModel(t, blocked, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	cancelReview := applicationCommandFromCmd(t, cmd)
	if cancelReview.Kind != application.UICommandCancelPrivacy || cancelReview.RequestID != submit.RequestID {
		t.Fatalf("privacy cancellation command = %#v", cancelReview)
	}

	model := newTestModel()
	model.session = SessionView{ID: testSessionID, Title: "Current Session"}
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/privacy"})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	show := applicationCommandFromCmd(t, cmd)
	if show.Kind != application.UICommandShowPrivacy || show.RequestID == 0 {
		t.Fatalf("privacy show command = %#v", show)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandShowPrivacy, RequestID: show.RequestID, Privacy: &review, Lifecycle: &lifecycle,
	}})
	frame := model.render()
	for _, want := range []string{"https://model.example", application.PrivacyPolicyVersion, "User question", "Container output", "Never eligible"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("privacy frame missing %q", want)
		}
	}
	for _, forbidden := range []string{"user_question", "redacted_container_output", "api-key-sink-canary"} {
		if strings.Contains(frame, forbidden) {
			t.Fatalf("privacy frame contained internal or sensitive value %q", forbidden)
		}
	}

	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'l'})
	toggle := applicationCommandFromCmd(t, cmd)
	if toggle.Kind != application.UICommandToggleLogs || toggle.LogsEnabled == nil || !*toggle.LogsEnabled ||
		toggle.PrivacyRevision != review.Revision {
		t.Fatalf("privacy toggle command = %#v", toggle)
	}
	updated, err := manager.Decide(context.Background(), application.PrivacyActionToggleLogs, review.Revision, toggle.LogsEnabled)
	if err != nil {
		t.Fatalf("ToggleLogs() error = %v", err)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandToggleLogs, RequestID: show.RequestID, Privacy: &updated, Lifecycle: &lifecycle,
	}})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'a'})
	accept := applicationCommandFromCmd(t, cmd)
	if accept.Kind != application.UICommandAcceptPrivacy || accept.PrivacyRevision != updated.Revision {
		t.Fatalf("privacy accept command = %#v", accept)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandAcceptPrivacy, RequestID: show.RequestID,
	}})
	if model.dialog.Open() || model.privacyReview != nil || model.pendingPrivacyID != 0 {
		t.Fatal("accepted privacy review remained authoritative in the TUI")
	}
}

func TestPrivacyLifecycleControlsReuseOneComposerAndRequireDeleteConfirmation(t *testing.T) {
	manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: new(tuiPrivacyStore), Origin: "https://model.example",
		Now: func() time.Time { return time.UnixMilli(31_000).UTC() },
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	privacy, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	lifecycle := application.SessionLifecycleReview{
		CurrentSession:                 &application.UISessionState{ID: testSessionID, Title: "Current Session", PrivacyMode: domain.PrivacyModeStandard},
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		ReadAuditRetentionDays:         application.ReadAuditRetentionDays,
		WriteAuditRetentionDays:        application.WriteAuditRetentionDays,
		StandardContentUntilDeletion:   true,
	}
	model := newTestModel()
	model.session = SessionView{ID: testSessionID, Title: "Current Session"}
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/privacy"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	show := applicationCommandFromCmd(t, cmd)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandShowPrivacy, RequestID: show.RequestID,
		Privacy: &privacy, Lifecycle: &lifecycle,
	}})
	frame := model.render()
	for _, want := range []string{
		"Session storage: history saved", "Operational details: kept for 30 days", "Read and lifecycle audit: 90 days",
		"Approval and write audit: 180 days", "memory-only Sessions cannot be resumed", "not forensic erasure",
		"same as /delete",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("privacy lifecycle frame missing %q", want)
		}
	}
	reviewText := privacyReviewText(privacy, &lifecycle)
	for _, want := range []string{"Consent: Not yet accepted", "User question", "Conversation context", "Kubernetes status", "Container output"} {
		if !strings.Contains(reviewText, want) {
			t.Fatalf("privacy review missing readable label %q: %q", want, reviewText)
		}
	}
	for _, forbidden := range []string{"safe_conversation_context", "resource_names_and_references", "projected_kubernetes_status", "redacted_container_output"} {
		if strings.Contains(reviewText, forbidden) {
			t.Fatalf("privacy review exposed internal category %q: %q", forbidden, reviewText)
		}
	}
	if model.EditorCount() != 1 {
		t.Fatalf("privacy lifecycle editor count = %d", model.EditorCount())
	}
	minimalLifecycle := lifecycle
	minimalSession := *lifecycle.CurrentSession
	minimalSession.PrivacyMode = domain.PrivacyModeMinimal
	minimalLifecycle.CurrentSession = &minimalSession
	minimalText := privacyReviewText(privacy, &minimalLifecycle)
	if !strings.Contains(minimalText, "Session storage: memory only") ||
		strings.Contains(minimalText, "Session storage: history saved") {
		t.Fatalf("minimal persistence impact was not rendered accurately: %q", minimalText)
	}

	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 't'})
	tighten := applicationCommandFromCmd(t, cmd)
	if tighten.Kind != application.UICommandTightenRetention || tighten.Lifecycle == nil ||
		tighten.Lifecycle.RetentionDays == nil || *tighten.Lifecycle.RetentionDays != 14 {
		t.Fatalf("retention command = %#v", tighten)
	}
	lifecycle.OperationalDetailRetentionDays = 14
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandTightenRetention, RequestID: show.RequestID,
		Privacy: &privacy, Lifecycle: &lifecycle,
	}})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'm'})
	mode := applicationCommandFromCmd(t, cmd)
	if mode.Kind != application.UICommandSetPersistenceMode || mode.Lifecycle == nil ||
		mode.Lifecycle.PrivacyMode != domain.PrivacyModeMinimal {
		t.Fatalf("persistence-mode command = %#v", mode)
	}

	model.privacyPending = false
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'd'})
	preview := applicationCommandFromCmd(t, cmd)
	if preview.Kind != application.UICommandPreviewSessionDeletion || !model.dialog.Open() || model.sessionDelete == nil ||
		!strings.Contains(model.render(), "Preparing deletion preview") {
		t.Fatal("delete key did not request an explicit current-Session preview")
	}
	review := testSessionDeletionReview(t, testSessionID, "Current Session", true)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandPreviewSessionDeletion, RequestID: preview.RequestID, DeletionReview: &review,
	}})
	if !strings.Contains(model.render(), "Delete current Session?") {
		t.Fatal("current-Session preview did not open confirmation")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || model.sessionDelete != nil || !model.dialog.Open() {
		t.Fatal("delete cancellation dispatched or failed to restore privacy review")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'd'})
	preview = applicationCommandFromCmd(t, cmd)
	review = testSessionDeletionReview(t, testSessionID, "Current Session", true)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandPreviewSessionDeletion, RequestID: preview.RequestID, DeletionReview: &review,
	}})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
	deleteCommand := applicationCommandFromCmd(t, cmd)
	if deleteCommand.Kind != application.UICommandDeleteSession || deleteCommand.Lifecycle == nil ||
		deleteCommand.Lifecycle.DeletionPlan == nil || deleteCommand.Lifecycle.DeletionPlan.Snapshot.Request.SessionID != testSessionID ||
		!deleteCommand.Lifecycle.Confirmed || deleteCommand.Lifecycle.Confirmation == "" {
		t.Fatalf("delete command = %#v", deleteCommand)
	}
}

func TestSessionDeleteOutcomeChangesCurrentSessionOnlyAfterMatchingCommit(t *testing.T) {
	newPendingModel := func() Model {
		model := newTestModel()
		model.session = SessionView{ID: testSessionID, Title: "Current Session"}
		model.pendingDeleteID = 73
		model.sessionDelete = &sessionDeleteState{
			SessionID: testSessionID, Title: "Current Session", ExpectedCurrent: true, Origin: deleteFromPrivacy,
		}
		model.showDialog("Deleting Session", "Waiting for the deletion transaction.")
		return model
	}

	t.Run("database failure", func(t *testing.T) {
		model, _ := updateModel(t, newPendingModel(), CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteSession, RequestID: 73, Failure: application.UIQueryUnavailable,
		}})
		if model.session.ID != testSessionID || model.pendingDeleteID != 0 || model.sessionDelete != nil ||
			!strings.Contains(model.render(), "Session not deleted") {
			t.Fatalf("failed deletion changed or falsely cleared current Session: %#v", model.session)
		}
	})

	t.Run("application failure", func(t *testing.T) {
		model, _ := updateModel(t, newPendingModel(), ApplicationFailureMsg{
			Command: application.UICommandDeleteSession, RequestID: 73,
		})
		if model.session.ID != testSessionID || model.pendingDeleteID != 0 || model.sessionDelete != nil ||
			!strings.Contains(model.render(), "No partial deletion was reported") {
			t.Fatalf("application deletion failure changed or falsely cleared current Session: %#v", model.session)
		}
	})

	t.Run("mismatched result", func(t *testing.T) {
		model, _ := updateModel(t, newPendingModel(), CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteSession, RequestID: 73,
			Deletion: testSessionDeletionResult(domain.SessionID("0192a6aa-77bc-7def-8123-456789abcdef"), true),
		}})
		if model.session.ID != testSessionID || model.pendingDeleteID != 73 || model.sessionDelete == nil {
			t.Fatal("mismatched committed result changed request-bound deletion state")
		}
	})

	t.Run("committed success", func(t *testing.T) {
		model, _ := updateModel(t, newPendingModel(), CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteSession, RequestID: 73,
			Deletion: testSessionDeletionResult(testSessionID, true),
		}})
		entries := model.transcript.Entries()
		if model.session.ID != "" || model.pendingDeleteID != 0 || model.sessionDelete != nil ||
			len(entries) != 1 || !strings.Contains(entries[0].Text, "not forensic erasure") {
			t.Fatalf("committed deletion did not clear current Session safely: %#v", model.session)
		}
	})
}

func TestPrivacyClearHistoryAndDeleteAllRequireExplicitConfirmation(t *testing.T) {
	manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: new(tuiPrivacyStore), Origin: "https://model.example",
		Now: func() time.Time { return time.UnixMilli(32_000).UTC() },
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	lifecycle := application.SessionLifecycleReview{
		CurrentSession: &application.UISessionState{
			ID: testSessionID, Title: "Current Session", PrivacyMode: domain.PrivacyModeStandard,
		},
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		ReadAuditRetentionDays:         application.ReadAuditRetentionDays,
		WriteAuditRetentionDays:        application.WriteAuditRetentionDays,
		StandardContentUntilDeletion:   true,
	}
	newPrivacyModel := func() Model {
		model := newTestModel()
		model.height = 80
		model.reflow()
		model.session = SessionView{ID: testSessionID, Title: "Current Session"}
		model.pendingPrivacyID = 80
		model.showPrivacyReview(review, &lifecycle)
		return model
	}

	t.Run("clear history", func(t *testing.T) {
		model, cmd := updateModel(t, newPrivacyModel(), tea.KeyPressMsg{Code: 'h'})
		if cmd != nil || model.localDeletion == nil || model.localDeletion.Kind != clearLocalHistory ||
			!strings.Contains(model.render(), "Clear all Session history?") {
			t.Fatal("clear-history confirmation did not disclose preserved preferences")
		}
		model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if cmd != nil || model.localDeletion != nil || !strings.Contains(model.render(), "Privacy and local data") {
			t.Fatal("clear-history cancellation dispatched or did not restore privacy review")
		}
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'h'})
		model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
		command := applicationCommandFromCmd(t, cmd)
		if command.Kind != application.UICommandClearHistory || command.Lifecycle == nil || !command.Lifecycle.Confirmed {
			t.Fatalf("clear-history command = %#v", command)
		}
		model, cmd = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandClearHistory, RequestID: command.RequestID,
			HistoryDeletion: &application.HistoryDeletionResult{SessionsCleared: true, PreferencesPreserved: true},
		}})
		if cmd != nil || model.session.ID != "" || model.localDeletion != nil ||
			!transcriptContains(model, "All Session history was deleted") ||
			!strings.Contains(model.render(), "Session history cleared") {
			t.Fatalf("clear-history result state = %#v", model.session)
		}
	})

	t.Run("delete all preflight denial", func(t *testing.T) {
		model, _ := updateModel(t, newPrivacyModel(), tea.KeyPressMsg{Code: 'x'})
		if model.localDeletion == nil || !strings.Contains(model.render(), "Delete all local database state?") {
			t.Fatal("delete-all confirmation did not disclose its exact file scope")
		}
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
		command := applicationCommandFromCmd(t, cmd)
		if command.Kind != application.UICommandDeleteAllLocalState || command.Lifecycle == nil || !command.Lifecycle.Confirmed {
			t.Fatalf("delete-all command = %#v", command)
		}
		model, cmd = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteAllLocalState, RequestID: command.RequestID,
			Failure: application.UIQueryUnavailable, LocalStateDeletion: &application.LocalStateDeletionResult{},
		}})
		if cmd != nil || model.session.ID != testSessionID || !strings.Contains(model.render(), "Local data not deleted") {
			t.Fatalf("preflight denial state = %#v", model.session)
		}
	})

	t.Run("delete all complete quits", func(t *testing.T) {
		model, _ := updateModel(t, newPrivacyModel(), tea.KeyPressMsg{Code: 'x'})
		model, commandCmd := updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
		command := applicationCommandFromCmd(t, commandCmd)
		model, quit := updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteAllLocalState, RequestID: command.RequestID,
			LocalStateDeletion: &application.LocalStateDeletionResult{StorageClosed: true, Complete: true},
		}})
		if quit != nil || model.session.ID != "" || model.localDeletion != nil ||
			!strings.Contains(model.render(), "Local database state deleted") {
			t.Fatalf("complete delete-all did not clear state and report success: %#v", model.session)
		}
		model, quit = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !commandQuits(quit) || model.quitAfterLocalDeletion {
			t.Fatal("complete delete-all did not quit after acknowledgement")
		}
	})

	t.Run("delete all partial failure is visible before exit", func(t *testing.T) {
		model, _ := updateModel(t, newPrivacyModel(), tea.KeyPressMsg{Code: 'x'})
		model, commandCmd := updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
		command := applicationCommandFromCmd(t, commandCmd)
		model, quit := updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandDeleteAllLocalState, RequestID: command.RequestID,
			Failure:            application.UIQueryUnavailable,
			LocalStateDeletion: &application.LocalStateDeletionResult{StorageClosed: true},
		}})
		if quit != nil || model.session.ID != "" || !strings.Contains(model.render(), "Local database deletion incomplete") {
			t.Fatalf("partial delete-all did not clear state and report failure: %#v", model.session)
		}
		model, quit = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
		if !commandQuits(quit) || model.quitAfterLocalDeletion {
			t.Fatal("partial delete-all did not quit after acknowledgement")
		}
	})
}

type tuiPrivacyStore struct{}

func (*tuiPrivacyStore) LoadPrivacy(context.Context) (application.PrivacyRecord, bool, error) {
	return application.PrivacyRecord{}, false, nil
}

func (*tuiPrivacyStore) SavePrivacy(context.Context, application.PrivacyRecord) error { return nil }

func TestFakeEventStreamCoversDeltaToolCompletionCancellationAndError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		terminal   application.UIEventKind
		text       string
		wantStatus string
	}{
		{name: "completion", terminal: application.UIEventRunCompleted, text: "Final diagnosis.", wantStatus: "completed"},
		{name: "cancellation", terminal: application.UIEventRunCancelled, text: "The diagnostic run was cancelled.", wantStatus: "cancelled"},
		{name: "safe error", terminal: application.UIEventRunFailed, text: "The diagnostic run failed safely.", wantStatus: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			stream := []application.UIEvent{
				runStartedEvent(1),
				{Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "Inspecting "},
				{Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3, ToolStep: &application.ToolStep{
					InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
					Purpose: "Inspect the selected Resource.", Status: application.ToolStepRunning,
				}},
				{Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 4, Text: "current state."},
				{Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 5, ToolStep: &application.ToolStep{
					InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
					Status: application.ToolStepSucceeded, Summary: "Safe projected status.", EvidenceCount: 2,
				}},
				runTerminalEvent(tt.terminal, 6, tt.text),
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
				ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 7, Text: "late",
			}})
			if model.run.StreamedText != tt.text || model.run.LastSequence != 6 {
				t.Fatal("late event changed terminal state")
			}
		})
	}
}

func TestPersistenceDegradedEventRemainsVisibleThroughTerminalState(t *testing.T) {
	t.Parallel()
	model := newTestModel()
	stream := []application.UIEvent{
		runStartedEvent(1),
		{
			Kind: application.UIEventPersistenceDegraded, RunID: testRunID,
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
			Text: "Local persistence is degraded; this run may not be resumable.",
		},
		runTerminalEvent(application.UIEventRunCompleted, 3, "In-memory diagnosis."),
	}
	for _, event := range stream {
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: event})
	}
	if !model.run.PersistenceDegraded || model.run.Status != "completed" ||
		strings.Contains(model.footerView(), "storage") || strings.Contains(model.footerView(), "diagnosis") {
		t.Fatalf("degraded terminal run = %#v; footer=%q", model.run, model.footerView())
	}
	if _, copied := model.transcript.LatestCommittedAssistantFinal(); copied {
		t.Fatal("degraded in-memory answer became eligible for committed-answer copy")
	}
	if matches, err := model.transcript.BeginSearch("In-memory diagnosis."); err != nil || matches != 0 {
		t.Fatalf("degraded in-memory answer entered committed transcript search: matches=%d error=%v", matches, err)
	}
	entries := model.transcript.Entries()
	found := false
	for _, entry := range entries {
		if strings.Contains(entry.Text, "may not be resumable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded notice entries = %#v", entries)
	}
}

func TestAnswerValidationWarningRemainsVisibleWithoutChangingStorageState(t *testing.T) {
	t.Parallel()
	model := newTestModel()
	answer := strings.Repeat("a", application.MaxQuestionBytes+1)
	stream := []application.UIEvent{
		runStartedEvent(1),
		{
			Kind: application.UIEventValidationWarning, RunID: testRunID,
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
			Text: "Kupilot removed unsupported final-answer metadata.",
		},
		runTerminalEvent(application.UIEventRunCompleted, 3, answer),
	}
	for _, event := range stream {
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: event})
	}
	if model.run.PersistenceDegraded || model.run.Status != "completed" || model.run.StreamedText != answer {
		t.Fatalf("warning terminal run = %#v", model.run)
	}
	found := false
	for _, entry := range model.transcript.Entries() {
		if strings.Contains(entry.Text, "unsupported final-answer metadata") {
			found = true
		}
	}
	if !found {
		t.Fatalf("validation warning entries = %#v", model.transcript.Entries())
	}
}

func TestActiveRunEnterSteersAndCancelRemainsAvailable(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "next question"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	steer := applicationCommandFromCmd(t, cmd)
	if steer.Kind != application.UICommandSubmitSteer || steer.Text != "next question" || model.composer.Value() != "" || model.dialog.Open() {
		t.Fatalf("active steer command = %#v", steer)
	}
	model.pendingConversation = nil
	model.composer.SetValue("draft for later")
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	command := applicationCommandFromCmd(t, cmd)
	if command.Kind != application.UICommandCancelRun || command.RunID != testRunID || model.composer.Value() != "draft for later" {
		t.Fatalf("cancel command = %#v", command)
	}
	model, cmd = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(
		application.UIEventRunCancelled, 2, "The diagnostic run was cancelled.",
	)})
	if cmd != nil || model.run.Status != "cancelled" || model.composer.Value() != "draft for later" ||
		!strings.Contains(model.View().Content, "The diagnostic run was cancelled.") {
		t.Fatal("ordinary cancellation exited or discarded the draft")
	}
}

func TestCtrlCCancelsActiveRunThenExitsAfterTerminalEvent(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 16, Height: 7, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 7, ReadOnly: true, Verified: true},
	})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command := applicationCommandFromCmd(t, cmd); command.Kind != application.UICommandCancelRun || !model.quitAfterCancel {
		t.Fatalf("Ctrl+C command = %#v", command)
	}
	model, cmd = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(
		application.UIEventRunCancelled, 2, "The diagnostic run was cancelled.",
	)})
	terminalTranscript := model.TerminalTranscript()
	if !commandQuits(cmd) || model.quitAfterCancel ||
		!strings.Contains(strings.ReplaceAll(terminalTranscript, "\n", " "), "The diagnostic run was cancelled.") {
		t.Fatalf("terminal cancellation did not complete bounded exit: cmd=%T quitAfterCancel=%v run=%#v transcript=%q",
			cmd, model.quitAfterCancel, model.run, terminalTranscript)
	}
}

func TestFocusResizeAndSmallTerminalPreserveKeyboardSafety(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 20, Height: 8, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true, Verified: true},
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
	if !view.ReportFocus || view.AltScreen || view.MouseMode != tea.MouseModeNone ||
		containsUnsafeTerminalText(sanitizeExternalText(view.Content, 0)) {
		t.Fatal("small-terminal View lost focus reporting, terminal ownership, or terminal safety")
	}
	model.run = RunView{}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || model.composer.Value() != "" {
		t.Fatal("first Ctrl+C did not clear the small-terminal draft")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !commandQuits(cmd) {
		t.Fatal("small terminal could not exit")
	}
}
