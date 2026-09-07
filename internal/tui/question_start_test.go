package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func TestQuestionStartTypedFailuresRestoreDraftExactlyOnceWithoutCommittedRows(t *testing.T) {
	tests := []struct {
		name     string
		reason   application.QuestionStartFailureReason
		recovery application.QuestionStartRecoveryAction
		state    func() application.UIQuestionStartCurrentState
		wantText string
	}{
		{
			name: "Session unavailable", reason: application.QuestionStartSessionUnavailable,
			recovery: application.QuestionStartRecoverStartOrResumeSession,
			state: func() application.UIQuestionStartCurrentState {
				state := questionStartTUIState()
				state.SessionID = ""
				return state
			},
			wantText: "Session unavailable",
		},
		{
			name: "scope generation stale", reason: application.QuestionStartScopeGenerationStale,
			recovery: application.QuestionStartRecoverSubmitAgain, state: questionStartTUIState,
			wantText: "Scope changed",
		},
		{
			name: "policy generation stale", reason: application.QuestionStartPolicyGenerationStale,
			recovery: application.QuestionStartRecoverReviewPolicy, state: questionStartTUIState,
			wantText: "Policy changed",
		},
		{
			name: "run starting", reason: application.QuestionStartRunStarting,
			recovery: application.QuestionStartRecoverWait,
			state: func() application.UIQuestionStartCurrentState {
				state := questionStartTUIState()
				state.RunState = application.QuestionStartRunStateStarting
				return state
			},
			wantText: "Another run is starting",
		},
		{
			name: "Application operation active", reason: application.QuestionStartApplicationOperationActive,
			recovery: application.QuestionStartRecoverWait,
			state: func() application.UIQuestionStartCurrentState {
				state := questionStartTUIState()
				state.ApplicationOperationActive = true
				return state
			},
			wantText: "Application busy",
		},
		{
			name: "persistence degraded", reason: application.QuestionStartPersistenceDegraded,
			recovery: application.QuestionStartRecoverRunDoctor,
			state: func() application.UIQuestionStartCurrentState {
				state := questionStartTUIState()
				state.PersistenceDegraded = true
				return state
			},
			wantText: "Storage is degraded",
		},
		{
			name: "precommit persistence failure", reason: application.QuestionStartPrecommitPersistenceFailed,
			recovery: application.QuestionStartRecoverRunDoctor,
			state: func() application.UIQuestionStartCurrentState {
				state := questionStartTUIState()
				state.PersistenceDegraded = true
				return state
			},
			wantText: "Storage is degraded",
		},
		{
			name: "model missing", reason: application.QuestionStartModelConfigurationMissing,
			recovery: application.QuestionStartRecoverConfigureModel, state: questionStartTUIState,
			wantText: "Model required",
		},
		{
			name: "input rejected", reason: application.QuestionStartInputRejected,
			recovery: application.QuestionStartRecoverEditInput, state: questionStartTUIState,
			wantText: "Input not accepted",
		},
		{
			name: "unknown safe failure", reason: application.QuestionStartUnknownSafeFailure,
			recovery: application.QuestionStartRecoverRunDoctor, state: questionStartTUIState,
			wantText: "Question not sent",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const draft = "Explain the current workload state."
			model, request := submitQuestionDraft(t, draft)
			outcome := questionStartTUIFailure(request.RequestID, test.reason, test.recovery, test.state())

			model, command := updateModel(t, model, CommandResultMsg{Result: outcome})
			if command != nil || model.pendingSubmitID != 0 || model.composer.Value() != draft || !model.dialog.Open() {
				t.Fatalf("recovery state = pending %d draft %q dialog %t command %t",
					model.pendingSubmitID, model.composer.Value(), model.dialog.Open(), command != nil)
			}
			dialog := model.dialog.View(80)
			if !strings.Contains(dialog, test.wantText) ||
				strings.Contains(dialog, "Model transfer unavailable") ||
				strings.Contains(dialog, "current safe state") {
				t.Fatalf("safe typed dialog = %q", dialog)
			}
			assertNoQuestionStartCommit(t, model)

			// A duplicate or late delivery outcome cannot restore, append, or
			// otherwise consume the same draft a second time.
			model, duplicateCommand := updateModel(t, model, CommandResultMsg{Result: outcome})
			if duplicateCommand != nil || model.composer.Value() != draft {
				t.Fatalf("duplicate outcome changed recovery: draft=%q command=%t", model.composer.Value(), duplicateCommand != nil)
			}
			assertNoQuestionStartCommit(t, model)
		})
	}
}

func TestQuestionStartScopeAndResourceRecoveryUseBoundedPickerAndRestoreOnCancel(t *testing.T) {
	t.Run("scope", func(t *testing.T) {
		const draft = "Check the active Namespace."
		model, request := submitQuestionDraft(t, draft)
		state := questionStartTUIState()
		state.ScopeState = application.ScopeStateUnavailable
		state.Context = ""
		state.Namespace = ""
		state.ReadOnly = false
		outcome := questionStartTUIFailure(
			request.RequestID,
			application.QuestionStartScopeNotVerified,
			application.QuestionStartRecoverSelectScope,
			state,
		)

		model, command := updateModel(t, model, CommandResultMsg{Result: outcome})
		query := completionQueryFromCmd(t, command)
		if query.Kind != application.UICompletionContext || model.activePicker != application.UICompletionContext ||
			model.questionRecoveryDraft != draft || model.composer.Value() != "/context " {
			t.Fatalf("scope picker recovery = query %#v kind %q saved %q composer %q",
				query, model.activePicker, model.questionRecoveryDraft, model.composer.Value())
		}
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
		if model.pickerOpen() || model.questionRecoveryDraft != "" || model.composer.Value() != draft {
			t.Fatalf("scope cancel recovery = picker %t saved %q composer %q",
				model.pickerOpen(), model.questionRecoveryDraft, model.composer.Value())
		}
		assertNoQuestionStartCommit(t, model)

		// The cancelled query result is stale and cannot reopen the picker.
		model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
			RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
			Contexts: []application.UIContextCandidate{{Name: "late-context"}},
		}})
		if model.pickerOpen() || model.composer.Value() != draft {
			t.Fatal("late scope candidate changed cancelled recovery")
		}
	})

	t.Run("resource", func(t *testing.T) {
		const draft = "Inspect the selected Pod."
		model := newTestModel()
		model.resource = ResourceView{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "old-pod"}
		model, _ = updateModel(t, model, tea.PasteMsg{Content: draft})
		model, submitCommand := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		request := commandFromCmd(t, submitCommand)
		state := questionStartTUIState()
		state.ResourceState = application.QuestionStartResourceStale
		outcome := questionStartTUIFailure(
			request.RequestID,
			application.QuestionStartSelectedResourceStale,
			application.QuestionStartRecoverSelectResource,
			state,
		)

		model, command := updateModel(t, model, CommandResultMsg{Result: outcome})
		query := completionQueryFromCmd(t, command)
		if query.Kind != application.UICompletionResource || model.resource != (ResourceView{}) ||
			model.questionRecoveryDraft != draft || model.composer.Value() != "/resource " {
			t.Fatalf("resource picker recovery = query %#v resource %#v saved %q composer %q",
				query, model.resource, model.questionRecoveryDraft, model.composer.Value())
		}
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
		if model.composer.Value() != draft || model.questionRecoveryDraft != "" {
			t.Fatalf("resource cancel draft = %q saved %q", model.composer.Value(), model.questionRecoveryDraft)
		}
		assertNoQuestionStartCommit(t, model)
	})
}

func TestQuestionStartApplicationActiveResynchronizesBeforeExplicitSteer(t *testing.T) {
	const draft = "Continue with the active diagnosis."
	model, request := submitQuestionDraft(t, draft)
	state := questionStartTUIState()
	state.RunState = application.QuestionStartRunStateRunning
	state.RunID = testRunID
	state.RunScopeGeneration = 7
	state.RunPolicyGeneration = 1
	state.RunSequence = 3
	outcome := questionStartTUIFailure(
		request.RequestID,
		application.QuestionStartRunActive,
		application.QuestionStartRecoverSteerOrQueue,
		state,
	)

	model, tick := updateModel(t, model, CommandResultMsg{Result: outcome})
	if tick == nil || !model.run.Active || model.run.RunID != testRunID || model.run.LastSequence != 3 ||
		model.composer.Value() != draft || !model.dialog.Open() {
		t.Fatalf("active resync = run %#v draft %q dialog %t tick %t", model.run, model.composer.Value(), model.dialog.Open(), tick != nil)
	}
	assertNoQuestionStartCommit(t, model)

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.dialog.Open() || model.composer.Value() != draft {
		t.Fatal("closing the active-run notice consumed the restored draft")
	}
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	steer := commandFromCmd(t, command)
	if steer.Kind != application.UICommandSubmitSteer || steer.RunID != testRunID || steer.Text != draft ||
		steer.ExpectedScopeGeneration != 7 || steer.ExpectedPolicyGeneration != 1 {
		t.Fatalf("explicit steer = %#v", steer)
	}
}

func TestQuestionStartLateOutcomeCannotReplaceNewerPendingDraft(t *testing.T) {
	const draft = "Keep this exact draft."
	model, request := submitQuestionDraft(t, draft)
	late := questionStartTUIFailure(
		request.RequestID+1,
		application.QuestionStartUnknownSafeFailure,
		application.QuestionStartRecoverRunDoctor,
		questionStartTUIState(),
	)
	model, command := updateModel(t, model, CommandResultMsg{Result: late})
	if command != nil || model.pendingSubmitID != request.RequestID || model.composer.Value() != "" || model.dialog.Open() {
		t.Fatalf("late outcome changed pending submit = pending %d draft %q dialog %t",
			model.pendingSubmitID, model.composer.Value(), model.dialog.Open())
	}

	current := questionStartTUIFailure(
		request.RequestID,
		application.QuestionStartInputRejected,
		application.QuestionStartRecoverEditInput,
		questionStartTUIState(),
	)
	model, _ = updateModel(t, model, CommandResultMsg{Result: current})
	if model.composer.Value() != draft || model.pendingSubmitID != 0 {
		t.Fatalf("current outcome recovery = pending %d draft %q", model.pendingSubmitID, model.composer.Value())
	}
	assertNoQuestionStartCommit(t, model)
}

func TestQuestionStartDeliveryFailureRequiresExactSessionAndBothGenerations(t *testing.T) {
	const draft = "Preserve this delivery-bound draft."
	model, request := submitQuestionDraft(t, draft)
	wrongPolicy := ApplicationFailureMsg{
		Message: "synthetic internal detail", Command: application.UICommandSubmitQuestion,
		RequestID: request.RequestID, SessionID: request.SessionID,
		ScopeGeneration: request.ExpectedScopeGeneration, PolicyGeneration: request.ExpectedPolicyGeneration + 1,
	}
	model, command := updateModel(t, model, wrongPolicy)
	if command != nil || model.pendingSubmitID != request.RequestID || model.composer.Value() != "" || model.dialog.Open() {
		t.Fatal("foreign-generation delivery failure changed the pending draft")
	}

	current := wrongPolicy
	current.PolicyGeneration = request.ExpectedPolicyGeneration
	model, command = updateModel(t, model, current)
	if command != nil || model.pendingSubmitID != 0 || model.composer.Value() != draft || !model.dialog.Open() {
		t.Fatalf("exact delivery failure recovery = pending %d draft %q dialog %t",
			model.pendingSubmitID, model.composer.Value(), model.dialog.Open())
	}
	dialog := model.dialog.View(80)
	if !strings.Contains(dialog, "question was not sent") || strings.Contains(dialog, current.Message) {
		t.Fatalf("delivery-safe failure dialog = %q", dialog)
	}
	assertNoQuestionStartCommit(t, model)
}

func submitQuestionDraft(t *testing.T, draft string) (Model, application.UICommand) {
	t.Helper()
	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: draft})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	request := commandFromCmd(t, command)
	if request.SessionID != testSessionID || request.ExpectedScopeGeneration != 7 || request.ExpectedPolicyGeneration != 1 {
		t.Fatalf("question binding = %#v", request)
	}
	return model, request
}

func questionStartTUIState() application.UIQuestionStartCurrentState {
	return application.UIQuestionStartCurrentState{
		SessionID:  testSessionID,
		ScopeState: application.ScopeStateActive, ScopeGeneration: 7,
		Context: "test-context", Namespace: "test-namespace", ReadOnly: true,
		PolicyGeneration: 1, PolicyHealthy: true,
		RunState:      application.QuestionStartRunStateIdle,
		ResourceState: application.QuestionStartResourceNone,
	}
}

func questionStartTUIFailure(
	requestID uint64,
	reason application.QuestionStartFailureReason,
	recovery application.QuestionStartRecoveryAction,
	state application.UIQuestionStartCurrentState,
) application.UICommandOutcome {
	return application.UICommandOutcome{
		Command: application.UICommandSubmitQuestion, RequestID: requestID,
		QuestionStart: &application.UIQuestionStartFailure{Reason: reason, Recovery: recovery, State: state},
	}
}

func assertNoQuestionStartCommit(t *testing.T, model Model) {
	t.Helper()
	for _, entry := range model.transcript.Entries() {
		if entry.Kind == components.EntryUser {
			t.Fatalf("uncommitted question entered transcript: %#v", model.transcript.Entries())
		}
	}
	if history := model.composer.SubmittedHistory(); len(history) != 0 {
		t.Fatalf("uncommitted question entered submitted history: %#v", history)
	}
}
