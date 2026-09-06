package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const testSessionID domain.SessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a20"

func TestStartIntentsProduceOnlyTheirTypedStartupAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		intent     application.UIStartIntent
		wantQuery  application.UICompletionKind
		wantResume application.UIResumeMode
		wantReady  bool
	}{
		{name: "new", intent: application.UIStartIntent{Kind: application.UIStartNew}, wantReady: true},
		{name: "resume picker", intent: application.UIStartIntent{Kind: application.UIStartResumePicker}, wantQuery: application.UICompletionSession},
		{name: "resume ID", intent: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: testSessionID}, wantResume: application.UIResumeExact},
		{name: "resume last", intent: application.UIStartIntent{Kind: application.UIStartResumeLast}, wantResume: application.UIResumeLast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				Width: 80, Height: 24, Theme: ThemeNoColor, StartIntent: tt.intent,
				Scope: ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
			})
			cmd := model.Init()
			if model.startup.Ready != tt.wantReady {
				t.Fatalf("startup ready = %v, want %v", model.startup.Ready, tt.wantReady)
			}
			switch {
			case tt.wantQuery != "":
				message, ok := cmd().(ApplicationQueryMsg)
				if !ok || message.Query.Kind != tt.wantQuery {
					t.Fatalf("startup command = %#v", cmd())
				}
			case tt.wantResume != "":
				message, ok := cmd().(ApplicationResumeMsg)
				if !ok || message.Request.Mode != tt.wantResume {
					t.Fatalf("startup command = %#v", cmd())
				}
			default:
				if cmd != nil {
					t.Fatal("new Session startup queried history")
				}
			}
			assertSingleEditor(t, model)
		})
	}
}

func TestDirectResumeStartIntentsReachReadyOnlyAfterMatchingFakeResult(t *testing.T) {
	t.Parallel()

	resumedID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a22")
	tests := []struct {
		name     string
		intent   application.UIStartIntent
		resultID domain.SessionID
		wantMode application.UIResumeMode
	}{
		{
			name:     "exact ID",
			intent:   application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: resumedID},
			resultID: resumedID, wantMode: application.UIResumeExact,
		},
		{
			name:     "last",
			intent:   application.UIStartIntent{Kind: application.UIStartResumeLast},
			resultID: resumedID, wantMode: application.UIResumeLast,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				Width: 80, Height: 24, Theme: ThemeNoColor, StartIntent: tt.intent,
				Scope: ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
			})
			request := resumeRequestFromCmd(t, model.Init())
			if request.Mode != tt.wantMode || model.startup.Ready {
				t.Fatalf("initial resume state = request %#v ready %v", request, model.startup.Ready)
			}
			resumed := application.UIResumedSession{ResumeRequestID: request.RequestID, Session: application.UISessionCandidate{
				ID: tt.resultID, Title: "Recovered diagnosis", UpdatedAtUnixMillis: 1,
				Context: "current", Namespace: "default", PrivacyMode: domain.PrivacyModeStandard,
			}}
			model, cmd := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
				RequestID: request.RequestID, Mode: request.Mode,
				Session: &resumed,
			}})
			accept := applicationCommandFromCmd(t, cmd)
			if accept.Kind != application.UICommandAcceptResume || model.startup.Ready {
				t.Fatalf("resume acceptance command = %#v, ready = %v", accept, model.startup.Ready)
			}
			model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
				Command: application.UICommandAcceptResume, RequestID: request.RequestID,
				Session: &application.UISessionState{
					ID: tt.resultID, Title: "Recovered diagnosis", PrivacyMode: domain.PrivacyModeStandard, Resumed: true,
				},
				Resumed: &resumed,
				Scope: &application.UIScopeResult{
					RequestID: request.RequestID, ExpectedGeneration: 7, ScopeGeneration: 7,
					Context: "current", Namespace: "default", ReadOnly: true,
				},
			}})
			if !model.startup.Ready || !model.session.Resumed || model.session.ID != tt.resultID || model.scopeConflict.Open() {
				t.Fatalf("resumed state = startup %#v session %#v", model.startup, model.session)
			}
			assertSingleEditor(t, model)
		})
	}
}

func TestTopLevelExplicitScopeOverridesSavedCandidate(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{
			Kind: application.UIStartResumeID, SessionID: testSessionID, ExplicitScope: true,
			ConfiguredContext: "explicit-context",
		},
		Scope: ScopeView{Context: "explicit-context", Namespace: "explicit-namespace", Generation: 7, ReadOnly: true},
	})
	request := resumeRequestFromCmd(t, model.Init())
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
			Context: "saved-context", Namespace: "saved-namespace", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "saved-context", Namespace: "saved-namespace"},
	}
	model, cmd := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
	}})
	command := applicationCommandFromCmd(t, cmd)
	if model.scopeConflict.Open() || command.Kind != application.UICommandAcceptResume || command.Scope != nil ||
		command.ExpectedScopeGeneration != model.scope.Generation {
		t.Fatalf("explicit-scope resume state = conflict %v command %#v", model.scopeConflict.Open(), command)
	}
}

func TestTopLevelResumeFreshlyActivatesUnverifiedStartupScopeBeforeAcceptance(t *testing.T) {
	t.Parallel()

	newModel := func() Model {
		return NewModel(Config{
			Width: 80, Height: 24, Theme: ThemeNoColor,
			StartIntent: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: testSessionID},
			Scope:       ScopeView{Context: "current-context", Namespace: "default"},
		})
	}
	resumeResult := func(t *testing.T, model Model, saved domain.ScopeCandidate) (Model, tea.Cmd) {
		t.Helper()
		request := resumeRequestFromCmd(t, model.Init())
		resumed := application.UIResumedSession{
			ResumeRequestID: request.RequestID,
			Session: application.UISessionCandidate{
				ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
				Context: saved.Context, Namespace: saved.Namespace, PrivacyMode: domain.PrivacyModeStandard,
			},
			SavedScope: &saved,
		}
		return updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
			RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
		}})
	}

	same, command := resumeResult(t, newModel(), domain.ScopeCandidate{Context: "current-context", Namespace: "default"})
	activation := applicationCommandFromCmd(t, command)
	if same.scopeConflict.Open() || activation.Scope == nil || activation.Kind != application.UICommandActivateScope ||
		activation.ExpectedScopeGeneration != 0 || activation.Scope.Context != "current-context" || activation.Scope.Namespace != "default" {
		t.Fatalf("same-scope activation = conflict %v command %#v", same.scopeConflict.Open(), activation)
	}
	same, command = updateModel(t, same, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandActivateScope, RequestID: activation.RequestID,
		Scope: &application.UIScopeResult{
			RequestID: activation.RequestID, ExpectedGeneration: 0, ScopeGeneration: 1,
			Context: "current-context", Namespace: "default", ReadOnly: true,
		},
	}})
	accept := applicationCommandFromCmd(t, command)
	if accept.Kind != application.UICommandAcceptResume || accept.Scope != nil || accept.ExpectedScopeGeneration != 1 {
		t.Fatalf("post-activation acceptance = %#v", accept)
	}

	different, command := resumeResult(t, newModel(), domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"})
	if command != nil || !different.scopeConflict.Open() {
		t.Fatal("different unverified scope did not require confirmation")
	}
	different, command = updateModel(t, different, tea.KeyPressMsg{Code: tea.KeyEnter})
	activation = applicationCommandFromCmd(t, command)
	if activation.Kind != application.UICommandActivateScope || activation.Scope == nil ||
		activation.Scope.Context != "current-context" || activation.Scope.Namespace != "default" {
		t.Fatalf("keep-current activation = %#v", activation)
	}
}

func TestTopLevelResumeScopeActivationFailureCancelsWithoutAcceptance(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: testSessionID},
		Scope:       ScopeView{Context: "current-context", Namespace: "default"},
	})
	request := resumeRequestFromCmd(t, model.Init())
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
			Context: "current-context", Namespace: "default", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "current-context", Namespace: "default"},
	}
	model, command := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
	}})
	activation := applicationCommandFromCmd(t, command)
	model, command = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandActivateScope, RequestID: activation.RequestID,
		Scope: &application.UIScopeResult{
			RequestID: activation.RequestID, ExpectedGeneration: 0, ScopeGeneration: 1,
			Failure: application.UIQueryUnavailable,
		},
	}})
	cancel := applicationCommandFromCmd(t, command)
	if cancel.Kind != application.UICommandCancelResume || cancel.RequestID != request.RequestID ||
		!model.startup.Failed || model.pendingResumed != nil || !model.dialog.Open() || model.session.ID != "" {
		t.Fatalf("failed activation = command %#v startup %#v pending=%v dialog=%v session=%#v",
			cancel, model.startup, model.pendingResumed != nil, model.dialog.Open(), model.session)
	}
}

func TestCtrlCCancelsInTUIResumeScopeConflict(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.session = SessionView{ID: testSessionID, Title: "Current Session"}
	model.composer.RecordSubmission("Current Session question.")
	request := resumeRequestFromCmd(t, model.beginResume(application.UIResumeExact, testSessionID, resumeOriginInTUI))
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
			Context: "saved-context", Namespace: "saved-namespace", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "saved-context", Namespace: "saved-namespace"},
	}
	model, command := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
	}})
	if command != nil || !model.scopeConflict.Open() {
		t.Fatal("resume did not enter the scope-conflict interaction")
	}
	model, command = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	cancel := applicationCommandFromCmd(t, command)
	if cancel.Kind != application.UICommandCancelResume || cancel.RequestID != request.RequestID ||
		model.scopeConflict.Open() || model.pendingResumed != nil || model.session.ID != testSessionID || !model.startup.Ready {
		t.Fatalf("Ctrl+C scope-conflict cancellation = %#v conflict=%v pending=%v session=%q ready=%v",
			cancel, model.scopeConflict.Open(), model.pendingResumed != nil, model.session.ID, model.startup.Ready)
	}
	model.composer.Reset()
	if !model.composer.PreviousHistory() || model.composer.Value() != "Current Session question." {
		t.Fatalf("cancelled resume changed current input history: %q", model.composer.Value())
	}
}

func TestResumeFailureDismissalExitsOnlyTopLevelFlow(t *testing.T) {
	t.Parallel()

	topLevel := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumeLast},
	})
	topRequest := resumeRequestFromCmd(t, topLevel.Init())
	topLevel, _ = updateModel(t, topLevel, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: topRequest.RequestID, Mode: topRequest.Mode, Failure: application.UIQueryUnavailable,
	}})
	if !topLevel.dialog.Open() || !topLevel.startup.Failed {
		t.Fatal("top-level resume failure was not visible")
	}
	topLevel, cmd := updateModel(t, topLevel, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !commandQuits(cmd) || topLevel.startup.Ready {
		t.Fatal("top-level resume failure did not exit after dismissal")
	}

	inTUI := newTestModel()
	inTUI.session = SessionView{ID: testSessionID, Title: "Current Session"}
	inTUI.composer.RecordSubmission("Current Session question.")
	request := resumeRequestFromCmd(t, inTUI.beginResume(
		application.UIResumeExact,
		domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a24"),
		resumeOriginInTUI,
	))
	inTUI, _ = updateModel(t, inTUI, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Failure: application.UIQueryNotResumable,
	}})
	inTUI, cmd = updateModel(t, inTUI, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !inTUI.startup.Ready || inTUI.session.ID != testSessionID {
		t.Fatalf("in-TUI resume failure changed current Session: %#v", inTUI.session)
	}
	inTUI.composer.Reset()
	if !inTUI.composer.PreviousHistory() || inTUI.composer.Value() != "Current Session question." {
		t.Fatalf("failed resume changed current input history: %q", inTUI.composer.Value())
	}
}

func TestCompletionRejectsStaleRequestAndGenerationAndKeepsOneEditor(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.openCompletion(application.UICompletionNamespace, "pay", resumeOriginNone)
	pending := model.pendingCompletion
	if pending.RequestID == 0 {
		t.Fatal("completion request has no identity")
	}

	staleRequest := application.UICompletionResult{
		RequestID: pending.RequestID + 1, Kind: pending.Kind,
		ScopeGeneration: pending.ScopeGeneration,
		Namespaces:      []application.UINamespaceCandidate{{Name: "stale-request"}},
	}
	model, _ = updateModel(t, model, CompletionResultMsg{Result: staleRequest})
	if model.namespacePicker.SourceCount() != 0 {
		t.Fatal("stale request changed Picker state")
	}

	staleGeneration := staleRequest
	staleGeneration.RequestID = pending.RequestID
	staleGeneration.ScopeGeneration++
	staleGeneration.Namespaces[0].Name = "stale-generation"
	model, _ = updateModel(t, model, CompletionResultMsg{Result: staleGeneration})
	if model.namespacePicker.SourceCount() != 0 {
		t.Fatal("stale generation changed Picker state")
	}

	current := staleGeneration
	current.ScopeGeneration = pending.ScopeGeneration
	current.Namespaces[0].Name = "payments"
	model, _ = updateModel(t, model, CompletionResultMsg{Result: current})
	if model.namespacePicker.SourceCount() != 1 {
		t.Fatal("current completion did not update Picker state")
	}
	assertSingleEditor(t, model)
}

func TestResourcePickerBoundsSourceAndVisibleRowsWithoutAnEditor(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.openCompletion(application.UICompletionResource, "", resumeOriginNone)
	pending := model.pendingCompletion
	resources := make([]application.UIResourceCandidate, application.MaxUIQueryCandidates)
	for index := range resources {
		resources[index] = application.UIResourceCandidate{
			APIVersion: "v1", Kind: domain.ResourceKindPod, Namespace: "payments",
			Name: fmt.Sprintf("pod-%02d", index), Status: "Ready",
		}
	}
	model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
		RequestID: pending.RequestID, Kind: pending.Kind,
		ScopeGeneration: pending.ScopeGeneration, Resources: resources,
	}})
	if model.resourcePicker.SourceCount() != application.MaxUIQueryCandidates {
		t.Fatalf("source count = %d", model.resourcePicker.SourceCount())
	}
	if model.resourcePicker.VisibleCount() != MaxPickerCandidates {
		t.Fatalf("visible count = %d", model.resourcePicker.VisibleCount())
	}
	assertSingleEditor(t, model)
}
