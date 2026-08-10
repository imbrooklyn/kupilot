package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestCoordinatorStartUIKeepsHistoryBehindExplicitResumeActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		intent      UIStartIntent
		wantSession bool
	}{
		{name: "new Session", intent: UIStartIntent{Kind: UIStartNew}, wantSession: true},
		{name: "resume picker", intent: UIStartIntent{Kind: UIStartResumePicker}},
		{name: "resume exact ID", intent: UIStartIntent{Kind: UIStartResumeID, SessionID: domain.SessionID(coordinatorUUID(41))}},
		{name: "resume last", intent: UIStartIntent{Kind: UIStartResumeLast}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			history := newRecordingSessionResumeStore()
			maintenance := new(recordingStartupMaintenance)
			coordinator := newUICoordinatorHarness(t, history, maintenance)
			result, err := coordinator.StartUI(context.Background(), test.intent, domain.PrivacyModeStandard)
			if err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			if result.Intent != test.intent || (result.Session != nil) != test.wantSession {
				t.Fatalf("StartUI() = %#v", result)
			}
			if result.Session != nil && (!result.Session.ID.Valid() || result.Session.Resumed) {
				t.Fatalf("new Session state = %#v", result.Session)
			}
			if history.listCalls != 0 || history.resumeCalls != 0 || history.latestCalls != 0 {
				t.Fatalf("history calls = list %d resume %d latest %d, want zero",
					history.listCalls, history.resumeCalls, history.latestCalls)
			}
			if maintenance.recoveryCalls != 1 || maintenance.retentionCalls != 1 {
				t.Fatalf("startup maintenance calls = recovery %d retention %d, want 1/1",
					maintenance.recoveryCalls, maintenance.retentionCalls)
			}
		})
	}
}

func TestCoordinatorSessionPickerAndResumeStayReadOnlyUntilAcceptance(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)
	resumedID := domain.SessionID(coordinatorUUID(51))
	otherID := domain.SessionID(coordinatorUUID(52))
	store := newRecordingSessionResumeStore()
	store.candidates = []ResumeSessionRecord{
		{ID: resumedID, Title: "Payment diagnosis", UpdatedAt: now, PrivacyMode: domain.PrivacyModeStandard,
			LastScope: &domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"}},
		{ID: otherID, Title: "Queue diagnosis", UpdatedAt: now.Add(-time.Millisecond), PrivacyMode: domain.PrivacyModeStandard},
	}
	store.history = resumedRecord(resumedID, now)
	coordinator := newUICoordinatorHarness(t, store, new(recordingStartupMaintenance))
	if _, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartResumePicker}, domain.PrivacyModeStandard); err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}

	query := UICompletionQuery{
		RequestID: 1, Kind: UICompletionSession, Filter: "payment", Limit: MaxUIQueryCandidates,
	}
	completion, err := coordinator.QueryUI(context.Background(), query)
	if err != nil {
		t.Fatalf("QueryUI() error = %v", err)
	}
	if completion.Validate() != nil || len(completion.Sessions) != 1 || completion.Sessions[0].ID != resumedID {
		t.Fatalf("session completion = %#v", completion)
	}
	if store.listCalls != 1 || store.resumeCalls != 0 || store.latestCalls != 0 {
		t.Fatalf("picker history calls = list %d resume %d latest %d", store.listCalls, store.resumeCalls, store.latestCalls)
	}

	request := UIResumeRequest{RequestID: 2, Mode: UIResumeExact, SessionID: resumedID}
	result, err := coordinator.ResumeUI(context.Background(), request)
	if err != nil {
		t.Fatalf("ResumeUI() error = %v", err)
	}
	if result.Validate() != nil || result.Session == nil || result.Session.Session.ID != resumedID ||
		len(result.Session.History) != 2 || result.Session.ResumeRequestID != request.RequestID {
		t.Fatalf("resume result = %#v", result)
	}
	if current := coordinator.CurrentUISession(); current != nil {
		t.Fatalf("resume query committed current Session %#v before acceptance", current)
	}
}

func TestCoordinatorResumeFailuresRemainDistinctWithoutFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        UIResumeMode
		storeError  error
		wantFailure UIQueryFailureCode
	}{
		{name: "minimal exact ID", mode: UIResumeExact, storeError: ErrSessionNotResumable, wantFailure: UIQueryNotResumable},
		{name: "missing exact ID", mode: UIResumeExact, storeError: ErrSessionResumeUnavailable, wantFailure: UIQueryUnavailable},
		{name: "empty last", mode: UIResumeLast, storeError: ErrNoResumableSession, wantFailure: UIQueryUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newRecordingSessionResumeStore()
			store.err = test.storeError
			coordinator := newUICoordinatorHarness(t, store, new(recordingStartupMaintenance))
			intent := UIStartIntent{Kind: UIStartResumeLast}
			request := UIResumeRequest{RequestID: 1, Mode: test.mode}
			if test.mode == UIResumeExact {
				request.SessionID = domain.SessionID(coordinatorUUID(61))
				intent = UIStartIntent{Kind: UIStartResumeID, SessionID: request.SessionID}
			}
			if _, err := coordinator.StartUI(context.Background(), intent, domain.PrivacyModeStandard); err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			result, err := coordinator.ResumeUI(context.Background(), request)
			if err != nil {
				t.Fatalf("ResumeUI() error = %v", err)
			}
			if result.Validate() != nil || result.Failure != test.wantFailure || result.Session != nil {
				t.Fatalf("ResumeUI() = %#v", result)
			}
			if coordinator.CurrentUISession() != nil {
				t.Fatal("failed resume created or replaced a Session")
			}
		})
	}
}

func TestCoordinatorBindsDirectStartupResumeToItsExactIntent(t *testing.T) {
	t.Parallel()

	store := newRecordingSessionResumeStore()
	expectedID := domain.SessionID(coordinatorUUID(62))
	coordinator := newUICoordinatorHarness(t, store, new(recordingStartupMaintenance))
	if _, err := coordinator.StartUI(context.Background(), UIStartIntent{
		Kind: UIStartResumeID, SessionID: expectedID,
	}, domain.PrivacyModeStandard); err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	_, err := coordinator.ResumeUI(context.Background(), UIResumeRequest{
		RequestID: 1, Mode: UIResumeExact, SessionID: domain.SessionID(coordinatorUUID(63)),
	})
	if !errors.Is(err, ErrInvalidUIQuery) || store.resumeCalls != 0 || store.latestCalls != 0 {
		t.Fatalf("mismatched ResumeUI() error = %v, calls = resume %d latest %d", err, store.resumeCalls, store.latestCalls)
	}
}

func TestCoordinatorActivatesOnlyTheConfirmedLocalStartupScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		savedScope    domain.ScopeCandidate
		chooseCurrent bool
		wantContext   string
		wantNamespace string
		wantGet       int
		wantSelected  bool
	}{
		{
			name: "same local scope", savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "default"},
			wantContext: "current-context", wantNamespace: "default", wantGet: 1, wantSelected: true,
		},
		{
			name: "keep different local scope", savedScope: domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
			chooseCurrent: true, wantContext: "current-context", wantNamespace: "default",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := newUIScopeActionRecorder()
			recorder.actualUID = "saved-uid"
			manager := newCoordinatorUIScopeManager(t, recorder, nil)
			store := newRecordingSessionResumeStore()
			resumedID := domain.SessionID(coordinatorUUID(64))
			store.history = resumedRecordWithResource(
				resumedID, time.Date(2026, 8, 10, 3, 30, 0, 0, time.UTC), test.savedScope, "saved-uid",
			)
			coordinator := newUIScopeCoordinatorHarness(t, manager, store)
			start, err := coordinator.StartUI(context.Background(), UIStartIntent{
				Kind: UIStartResumeID, SessionID: resumedID,
			}, domain.PrivacyModeStandard)
			if err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			currentCandidate := &domain.ScopeCandidate{Context: "current-context", Namespace: "default"}
			if start.ScopeCandidate == nil || *start.ScopeCandidate != *currentCandidate ||
				recorder.count("contexts") != 1 || recorder.count("create") != 0 || recorder.count("verify") != 0 {
				t.Fatalf("local startup candidate = %#v, actions = %#v", start.ScopeCandidate, recorder.snapshot())
			}
			recorder.reset()
			request := UIResumeRequest{RequestID: 5, Mode: UIResumeExact, SessionID: resumedID}
			if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
				t.Fatalf("ResumeUI() = %#v, %v", result, err)
			}
			if len(recorder.snapshot()) != 0 {
				t.Fatalf("history load actions = %#v, want zero", recorder.snapshot())
			}
			selectedScope := &test.savedScope
			if test.chooseCurrent {
				selectedScope = currentCandidate
			}
			outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
				Kind: UICommandAcceptResume, RequestID: request.RequestID,
				ExpectedScopeGeneration: 0, Scope: selectedScope,
			})
			if err != nil || outcome.Failure != "" || outcome.Scope == nil || outcome.Scope.Failure != "" {
				t.Fatalf("resume acceptance = %#v, %v", outcome, err)
			}
			if recorder.count("create") != 1 || recorder.count("verify") != 1 ||
				recorder.count("get-resource") != test.wantGet || recorder.count("agent-run") != 0 {
				t.Fatalf("confirmed scope actions = %#v", recorder.snapshot())
			}
			view := manager.View()
			if view.Scope == nil || view.Scope.Context != test.wantContext || view.Scope.Namespace != test.wantNamespace {
				t.Fatalf("active scope = %#v", view)
			}
			selected, selectErr := manager.SelectedResource(*view.Scope)
			if selectErr != nil || (selected != nil) != test.wantSelected {
				t.Fatalf("SelectedResource() = %#v, %v", selected, selectErr)
			}
		})
	}
}

func TestCoordinatorScopeQueriesAndCommandsAreGenerationBound(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	current, err := manager.SwitchContext(context.Background(), "current-context", 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	current, err = manager.SwitchNamespace(context.Background(), "payments", current.Generation)
	if err != nil {
		t.Fatalf("SwitchNamespace() error = %v", err)
	}
	recorder.reset()
	coordinator := newUIScopeCoordinatorHarness(t, manager, newRecordingSessionResumeStore())

	stale, err := coordinator.QueryUI(context.Background(), UICompletionQuery{
		RequestID: 1, Kind: UICompletionNamespace, ScopeGeneration: current.Generation - 1,
		Limit: MaxUIQueryCandidates,
	})
	if err != nil {
		t.Fatalf("stale QueryUI() error = %v", err)
	}
	if stale.Failure != UIQueryUnavailable || recorder.count("list-namespaces") != 0 {
		t.Fatalf("stale completion = %#v, actions = %#v", stale, recorder.snapshot())
	}

	completion, err := coordinator.QueryUI(context.Background(), UICompletionQuery{
		RequestID: 2, Kind: UICompletionNamespace, Filter: "pay", ScopeGeneration: current.Generation,
		Limit: MaxUIQueryCandidates,
	})
	if err != nil {
		t.Fatalf("QueryUI() error = %v", err)
	}
	if completion.Validate() != nil || !reflect.DeepEqual(completion.Namespaces, []UINamespaceCandidate{{Name: "payments"}}) ||
		recorder.count("list-namespaces") != 1 {
		t.Fatalf("Namespace completion = %#v, actions = %#v", completion, recorder.snapshot())
	}

	recorder.reset()
	outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandSelectNamespace, RequestID: 3, Text: "payments",
		ExpectedScopeGeneration: current.Generation - 1,
	})
	if err != nil {
		t.Fatalf("ExecuteUICommand() error = %v", err)
	}
	if outcome.Scope == nil || outcome.Scope.Failure != UIQueryUnavailable || outcome.Scope.ScopeGeneration != current.Generation ||
		len(recorder.snapshot()) != 0 {
		t.Fatalf("stale command outcome = %#v, actions = %#v", outcome, recorder.snapshot())
	}
}

func TestCoordinatorResourceCompletionBoundsReadsAcrossFixedKinds(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	current, err := manager.SwitchContext(context.Background(), "current-context", 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	recorder.reset()
	coordinator := newUIScopeCoordinatorHarness(t, manager, newRecordingSessionResumeStore())

	result, err := coordinator.QueryUI(context.Background(), UICompletionQuery{
		RequestID: 4, Kind: UICompletionResource, Filter: "does-not-match",
		ScopeGeneration: current.Generation, Limit: 3,
	})
	if err != nil {
		t.Fatalf("QueryUI() error = %v", err)
	}
	if result.Validate() != nil || result.Failure != "" || len(result.Resources) != 0 {
		t.Fatalf("resource completion = %#v", result)
	}
	if calls := recorder.count("list-resources"); calls != 3 {
		t.Fatalf("resource list calls = %d, want 3 total bounded reads", calls)
	}
}

func TestCoordinatorResumeAcceptanceRevalidatesSavedScopeAndResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		savedScope       domain.ScopeCandidate
		savedUID         string
		actualUID        string
		verifyError      map[string]error
		missingContext   bool
		wantScopeFailure UIQueryFailureCode
		wantGeneration   int64
		wantCreate       int
		wantVerify       int
		wantGet          int
		wantSelected     bool
		wantContext      string
		wantNamespace    string
	}{
		{
			name: "same scope", savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "default"},
			savedUID: "old-uid", actualUID: "old-uid", wantGeneration: 1, wantGet: 1, wantSelected: true,
			wantContext: "current-context", wantNamespace: "default",
		},
		{
			name: "different scope", savedScope: domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
			savedUID: "saved-uid", actualUID: "saved-uid", wantGeneration: 2, wantCreate: 1, wantVerify: 1,
			wantGet: 1, wantSelected: true, wantContext: "saved-context", wantNamespace: "payments",
		},
		{
			name: "missing Context", savedScope: domain.ScopeCandidate{Context: "missing-context", Namespace: "payments"},
			savedUID: "saved-uid", actualUID: "saved-uid", missingContext: true, wantScopeFailure: UIQueryUnavailable,
			wantGeneration: 1, wantContext: "current-context", wantNamespace: "default",
		},
		{
			name: "forbidden Namespace", savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "forbidden"},
			savedUID: "saved-uid", actualUID: "saved-uid",
			verifyError:      map[string]error{"forbidden": &testClassifiedError{class: domain.SafeErrorClassPermissionDenied}},
			wantScopeFailure: UIQueryForbidden, wantGeneration: 1, wantVerify: 1,
			wantContext: "current-context", wantNamespace: "default",
		},
		{
			name: "resource UID changed", savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "default"},
			savedUID: "old-uid", actualUID: "replacement-uid", wantGeneration: 1, wantGet: 1,
			wantContext: "current-context", wantNamespace: "default",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := newUIScopeActionRecorder()
			recorder.missingContext = test.missingContext
			recorder.verifyError = test.verifyError
			recorder.actualUID = test.actualUID
			manager := newCoordinatorUIScopeManager(t, recorder, nil)
			initial, err := manager.SwitchContext(context.Background(), "current-context", 0)
			if err != nil {
				t.Fatalf("initial SwitchContext() error = %v", err)
			}
			recorder.reset()

			store := newRecordingSessionResumeStore()
			resumedID := domain.SessionID(coordinatorUUID(81))
			store.history = resumedRecordWithResource(resumedID, time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), test.savedScope, test.savedUID)
			coordinator := newUIScopeCoordinatorHarness(t, manager, store)
			request := UIResumeRequest{RequestID: 9, Mode: UIResumeExact, SessionID: resumedID}
			if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
				t.Fatalf("ResumeUI() = %#v, %v", result, err)
			}
			if len(recorder.snapshot()) != 0 {
				t.Fatalf("resume load actions = %#v, want zero", recorder.snapshot())
			}

			command := UICommand{
				Kind: UICommandAcceptResume, RequestID: request.RequestID,
				ExpectedScopeGeneration: initial.Generation, Scope: &test.savedScope,
			}
			outcome, err := coordinator.ExecuteUICommand(context.Background(), command)
			if err != nil {
				t.Fatalf("ExecuteUICommand() error = %v", err)
			}
			if outcome.Resumed == nil || outcome.Resumed.Session.ID != resumedID || coordinator.CurrentUISession().ID != resumedID {
				t.Fatalf("resume acceptance outcome = %#v", outcome)
			}
			if outcome.Scope == nil || outcome.Scope.Failure != test.wantScopeFailure || outcome.Scope.ScopeGeneration != test.wantGeneration {
				t.Fatalf("scope outcome = %#v", outcome.Scope)
			}
			if recorder.count("create") != test.wantCreate || recorder.count("verify") != test.wantVerify || recorder.count("get-resource") != test.wantGet {
				t.Fatalf("actions = %#v, want create=%d verify=%d get=%d", recorder.snapshot(), test.wantCreate, test.wantVerify, test.wantGet)
			}
			if recorder.count("agent-run") != 0 {
				t.Fatalf("resume acceptance started Agent work: %#v", recorder.snapshot())
			}
			view := manager.View()
			if view.Scope == nil || view.Scope.Context != test.wantContext || view.Scope.Namespace != test.wantNamespace {
				t.Fatalf("scope view = %#v", view)
			}
			selected, selectErr := manager.SelectedResource(*view.Scope)
			if selectErr != nil || (selected != nil) != test.wantSelected {
				t.Fatalf("SelectedResource() = %#v, %v", selected, selectErr)
			}
			if test.wantSelected && selected.UID != test.actualUID {
				t.Fatalf("selected UID = %q, want %q", selected.UID, test.actualUID)
			}
		})
	}
}

func TestCoordinatorExplicitStartupScopeCannotBeReplacedBySavedCandidate(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	current, err := manager.SwitchContext(context.Background(), "current-context", 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	recorder.reset()
	store := newRecordingSessionResumeStore()
	resumedID := domain.SessionID(coordinatorUUID(92))
	savedScope := domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"}
	store.history = resumedRecordWithResource(
		resumedID, time.Date(2026, 8, 10, 5, 30, 0, 0, time.UTC), savedScope, "saved-uid",
	)
	coordinator := newUIScopeCoordinatorHarness(t, manager, store)
	if _, err := coordinator.StartUI(context.Background(), UIStartIntent{
		Kind: UIStartResumeID, SessionID: resumedID, ExplicitScope: true,
		ConfiguredContext: "current-context",
	}, domain.PrivacyModeStandard); err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	request := UIResumeRequest{RequestID: 11, Mode: UIResumeExact, SessionID: resumedID}
	if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
		t.Fatalf("ResumeUI() = %#v, %v", result, err)
	}

	rejected, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID,
		ExpectedScopeGeneration: current.Generation, Scope: &savedScope,
	})
	if err != nil || rejected.Failure != UIQueryUnavailable || coordinator.CurrentUISession() != nil || len(recorder.snapshot()) != 0 {
		t.Fatalf("saved-scope acceptance = %#v, error = %v, actions = %#v", rejected, err, recorder.snapshot())
	}

	accepted, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID,
		ExpectedScopeGeneration: current.Generation,
	})
	if err != nil || accepted.Failure != "" || accepted.Scope == nil || accepted.Scope.Failure != "" ||
		accepted.Resource == nil || accepted.Resource.Failure != UIQueryUnavailable || coordinator.CurrentUISession() == nil ||
		len(recorder.snapshot()) != 0 {
		t.Fatalf("explicit-scope acceptance = %#v, error = %v, actions = %#v", accepted, err, recorder.snapshot())
	}
}

func TestCoordinatorCancelPendingResumeKeepsCurrentSession(t *testing.T) {
	t.Parallel()

	store := newRecordingSessionResumeStore()
	resumedID := domain.SessionID(coordinatorUUID(91))
	store.history = resumedRecord(resumedID, time.Date(2026, 8, 10, 5, 0, 0, 0, time.UTC))
	coordinator := newUICoordinatorHarness(t, store, new(recordingStartupMaintenance))
	created, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartNew}, domain.PrivacyModeStandard)
	if err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	request := UIResumeRequest{RequestID: 10, Mode: UIResumeExact, SessionID: resumedID}
	if _, err := coordinator.ResumeUI(context.Background(), request); err != nil {
		t.Fatalf("ResumeUI() error = %v", err)
	}
	if _, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandCancelResume, RequestID: request.RequestID}); err != nil {
		t.Fatalf("ExecuteUICommand() error = %v", err)
	}
	if current := coordinator.CurrentUISession(); current == nil || current.ID != created.Session.ID {
		t.Fatalf("current Session = %#v, want %#v", current, created.Session)
	}
}

func TestCoordinatorRenamePersistsOnlyProcessedCurrentSessionTitle(t *testing.T) {
	t.Parallel()

	store := newRecordingSessionResumeStore()
	coordinator := newUICoordinatorHarness(t, store, new(recordingStartupMaintenance))
	created, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartNew}, domain.PrivacyModeStandard)
	if err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandRenameSession, Text: "Incident review",
	})
	if err != nil || outcome.Validate() != nil || outcome.Session == nil || outcome.Session.Title != "Incident review" ||
		store.renameCalls != 1 || store.renameRecord.SessionID != created.Session.ID || store.renameRecord.ExpectedVersion != 1 {
		t.Fatalf("rename outcome = %#v, error = %v, record = %#v", outcome, err, store.renameRecord)
	}

	canary := strings.Repeat("p", 16)
	outcome, err = coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandRenameSession, Text: "password=" + canary,
	})
	if err != nil || outcome.Session == nil || strings.Contains(store.renameRecord.Title, canary) ||
		store.renameRecord.Title == "" || store.renameRecord.ExpectedVersion != 2 {
		t.Fatalf("redacted rename outcome = %#v, error = %v, record = %#v", outcome, err, store.renameRecord)
	}
	outcome, err = coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandRenameSession})
	if err != nil || outcome.Session == nil || outcome.Session.Title != "" ||
		store.renameRecord.Title != "" || store.renameRecord.ExpectedVersion != 3 {
		t.Fatalf("cleared rename outcome = %#v, error = %v, record = %#v", outcome, err, store.renameRecord)
	}

	blocked := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\nsynthetic\n" +
		strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
	outcome, err = coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandRenameSession, Text: blocked,
	})
	if err != nil || outcome.Validate() != nil || outcome.Failure != UIQueryUnavailable || store.renameCalls != 3 {
		t.Fatalf("blocked rename outcome = %#v, error = %v, calls = %d", outcome, err, store.renameCalls)
	}
}

type recordingSessionResumeStore struct {
	listCalls    int
	resumeCalls  int
	latestCalls  int
	candidates   []ResumeSessionRecord
	history      ResumedSessionRecord
	err          error
	renameCalls  int
	renameRecord RenameSessionRecord
	renameErr    error
}

func newRecordingSessionResumeStore() *recordingSessionResumeStore {
	return new(recordingSessionResumeStore)
}

func (store *recordingSessionResumeStore) ListResumable(context.Context, int) ([]ResumeSessionRecord, error) {
	store.listCalls++
	return append([]ResumeSessionRecord(nil), store.candidates...), store.err
}

func (store *recordingSessionResumeStore) ResumeByID(context.Context, domain.SessionID) (ResumedSessionRecord, error) {
	store.resumeCalls++
	return store.history, store.err
}

func (store *recordingSessionResumeStore) ResumeLatest(context.Context) (ResumedSessionRecord, error) {
	store.latestCalls++
	return store.history, store.err
}

func (store *recordingSessionResumeStore) Rename(_ context.Context, record RenameSessionRecord) error {
	store.renameCalls++
	store.renameRecord = record
	return store.renameErr
}

type recordingStartupMaintenance struct {
	recoveryCalls  int
	retentionCalls int
	err            error
}

type uiScopeActionRecorder struct {
	mu             sync.Mutex
	actions        []string
	missingContext bool
	verifyError    map[string]error
	actualUID      string
}

func newUIScopeActionRecorder() *uiScopeActionRecorder {
	return &uiScopeActionRecorder{actualUID: "current-uid"}
}

func (recorder *uiScopeActionRecorder) add(action string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.actions = append(recorder.actions, action)
}

func (recorder *uiScopeActionRecorder) reset() {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.actions = nil
}

func (recorder *uiScopeActionRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.actions...)
}

func (recorder *uiScopeActionRecorder) count(action string) int {
	count := 0
	for _, recorded := range recorder.snapshot() {
		if recorded == action {
			count++
		}
	}
	return count
}

type uiScopeClient struct {
	context  ContextCandidate
	recorder *uiScopeActionRecorder
}

func (client *uiScopeClient) Context() ContextCandidate { return client.context }
func (client *uiScopeClient) Close()                    { client.recorder.add("close") }

type uiScopeFactory struct{ recorder *uiScopeActionRecorder }

func (factory *uiScopeFactory) Contexts(context.Context) ([]ContextCandidate, error) {
	factory.recorder.add("contexts")
	result := []ContextCandidate{{Name: "current-context", DefaultNamespace: "default", Current: true}}
	if !factory.recorder.missingContext {
		result = append(result, ContextCandidate{Name: "saved-context", DefaultNamespace: "payments"})
	}
	return result, nil
}

func (factory *uiScopeFactory) Create(_ context.Context, name string) (ScopeClient, error) {
	factory.recorder.add("create")
	for _, candidate := range []ContextCandidate{
		{Name: "current-context", DefaultNamespace: "default", Current: true},
		{Name: "saved-context", DefaultNamespace: "payments"},
	} {
		if candidate.Name == name {
			return &uiScopeClient{context: candidate, recorder: factory.recorder}, nil
		}
	}
	return nil, errors.New("unexpected Context")
}

type uiNamespaceReader struct{ recorder *uiScopeActionRecorder }

func (reader *uiNamespaceReader) VerifyNamespace(_ context.Context, _ ScopeClient, namespace string) error {
	reader.recorder.add("verify")
	return reader.recorder.verifyError[namespace]
}

func (reader *uiNamespaceReader) ListNamespaces(context.Context, ScopeClient, int) (domain.NamespaceList, error) {
	reader.recorder.add("list-namespaces")
	return domain.NamespaceList{Items: []domain.NamespaceSummary{{Name: "default"}, {Name: "payments"}}}, nil
}

type uiResourceService struct{ recorder *uiScopeActionRecorder }

func (service *uiResourceService) GetResource(
	_ context.Context,
	_ ScopeClient,
	_ domain.ClusterScope,
	reference domain.ResourceRef,
) (domain.ResourceSummary, error) {
	service.recorder.add("get-resource")
	reference.UID = service.recorder.actualUID
	reference.ResourceVersion = "27"
	return domain.ResourceSummary{Reference: reference}, nil
}

func (service *uiResourceService) ListResources(
	_ context.Context,
	_ ScopeClient,
	scope domain.ClusterScope,
	kind domain.ResourceKind,
	_ int,
) (domain.ResourceList, error) {
	service.recorder.add("list-resources")
	name := map[domain.ResourceKind]string{
		domain.ResourceKindPod: "payment-pod", domain.ResourceKindDeployment: "payment-deployment",
		domain.ResourceKindReplicaSet: "payment-replicaset", domain.ResourceKindJob: "payment-job",
		domain.ResourceKindService: "payment-service",
	}[kind]
	return domain.ResourceList{Items: []domain.ResourceSummary{{Reference: domain.ResourceRef{
		APIVersion: kind.APIVersion(), Kind: string(kind), Namespace: scope.Namespace, Name: name,
		UID: service.recorder.actualUID, ResourceVersion: "27",
	}}}}, nil
}

type uiScopeInvalidationHook struct{ recorder *uiScopeActionRecorder }

func (hook *uiScopeInvalidationHook) InvalidateScope(int64) error {
	hook.recorder.add("invalidate")
	return nil
}

func newCoordinatorUIScopeManager(t *testing.T, recorder *uiScopeActionRecorder, now func() time.Time) *ScopeManager {
	t.Helper()
	if now == nil {
		now = func() time.Time { return time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC) }
	}
	manager, err := NewScopeManager(
		&uiScopeFactory{recorder: recorder},
		&uiNamespaceReader{recorder: recorder},
		&uiResourceService{recorder: recorder},
		&uiScopeInvalidationHook{recorder: recorder},
		now,
	)
	if err != nil {
		t.Fatalf("NewScopeManager() error = %v", err)
	}
	return manager
}

func newUIScopeCoordinatorHarness(t *testing.T, manager *ScopeManager, history *recordingSessionResumeStore) *Coordinator {
	t.Helper()
	clock := newCoordinatorClock()
	factory, ok := manager.factory.(*uiScopeFactory)
	if !ok {
		t.Fatal("ScopeManager test factory is unavailable")
	}
	runner := runnerFunc(func(_ context.Context, _ agent.RunInput, _ agent.EventSink) agent.RunOutcome {
		factory.recorder.add("agent-run")
		return agent.RunOutcome{}
	})
	persistence := new(memoryCoordinatorPersistence)
	identifiers := new(coordinatorIDs)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Sessions: persistence, Runs: persistence, Tools: persistence, Audits: persistence,
		Scope: manager, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: new(recordingUIEvents),
		Observer: RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
		UI: &CoordinatorUIConfig{
			Sessions: history, Titles: history, Startup: new(recordingStartupMaintenance), Scopes: manager,
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	return coordinator
}

func (maintenance *recordingStartupMaintenance) RecoverInterrupted(context.Context, time.Time) error {
	maintenance.recoveryCalls++
	return maintenance.err
}

func (maintenance *recordingStartupMaintenance) CleanupRetention(context.Context, time.Time) error {
	maintenance.retentionCalls++
	return maintenance.err
}

func newUICoordinatorHarness(
	t *testing.T,
	history *recordingSessionResumeStore,
	maintenance StartupMaintenance,
) *Coordinator {
	t.Helper()
	clock := newCoordinatorClock()
	runner := runnerFunc(func(_ context.Context, _ agent.RunInput, _ agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	})
	persistence := new(memoryCoordinatorPersistence)
	scope := &coordinatorScope{scope: domain.ClusterScope{
		Context: "test-context", Namespace: "team-a", Generation: 7,
		ActivatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}}
	identifiers := new(coordinatorIDs)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Sessions: persistence, Runs: persistence, Tools: persistence, Audits: persistence,
		Scope: scope, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: new(recordingUIEvents),
		Observer: RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
		UI: &CoordinatorUIConfig{Sessions: history, Titles: history, Startup: maintenance},
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	return coordinator
}

func resumedRecord(id domain.SessionID, now time.Time) ResumedSessionRecord {
	runID := domain.AgentRunID(coordinatorUUID(71))
	userID := domain.MessageID(coordinatorUUID(72))
	assistantID := domain.MessageID(coordinatorUUID(73))
	scope := domain.ScopeSnapshot{Context: "saved-context", Namespace: "payments", Generation: 4}
	user := domain.Message{
		ID: userID, SessionID: id, RunID: &runID, Role: domain.MessageRoleUser,
		Content: "Why is the Pod restarting?", Format: domain.MessageFormatPlain,
		Status: domain.MessageStatusCommitted, Scope: &scope,
		Hash: domain.MessageContentHash("Why is the Pod restarting?"), CreatedAt: now,
	}
	answer := "Historic Diagnosis with old Evidence references."
	assistant := domain.Message{
		ID: assistantID, SessionID: id, RunID: &runID, Role: domain.MessageRoleAssistant,
		Content: answer, Format: domain.MessageFormatMarkdown,
		Status: domain.MessageStatusCommitted, Scope: &scope,
		Hash: domain.MessageContentHash(answer), CreatedAt: now.Add(time.Millisecond),
	}
	return ResumedSessionRecord{
		Session: domain.Session{
			ID: id, Title: "Payment diagnosis", Status: domain.SessionStatusActive,
			PrivacyMode: domain.PrivacyModeStandard,
			LastScope:   &domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
			Version:     2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Millisecond),
		},
		Messages: []domain.Message{user, assistant},
	}
}

func resumedRecordWithResource(
	id domain.SessionID,
	now time.Time,
	savedScope domain.ScopeCandidate,
	uid string,
) ResumedSessionRecord {
	record := resumedRecord(id, now)
	reference := domain.ResourceRef{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: savedScope.Namespace,
		Name: "payment-api", UID: uid, ResourceVersion: "19",
	}
	record.Session.LastScope = &savedScope
	record.Session.SelectedResource = &reference
	for index := range record.Messages {
		snapshot := domain.ScopeSnapshot{Context: savedScope.Context, Namespace: savedScope.Namespace, Generation: 4}
		record.Messages[index].Scope = &snapshot
		record.Messages[index].Resource = &reference
	}
	return record
}
