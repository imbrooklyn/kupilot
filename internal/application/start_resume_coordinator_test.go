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
			if history.searchCalls != 0 || history.listCalls != 0 || history.resumeCalls != 0 || history.latestCalls != 0 {
				t.Fatalf("history calls = search %d list %d resume %d latest %d, want zero",
					history.searchCalls, history.listCalls, history.resumeCalls, history.latestCalls)
			}
			if maintenance.recoveryCalls != 1 || maintenance.retentionCalls != 1 {
				t.Fatalf("startup maintenance calls = recovery %d retention %d, want 1/1",
					maintenance.recoveryCalls, maintenance.retentionCalls)
			}
		})
	}
}

func TestCoordinatorResolvesStartupScopeWithoutSessionHistory(t *testing.T) {
	t.Parallel()

	preferenceTime := time.Date(2026, 8, 9, 23, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		intent            UIStartIntent
		preference        ScopePreference
		preferenceFound   bool
		preferenceError   error
		missingRemembered bool
		want              domain.ScopeCandidate
		wantLoadCalls     int
		wantDegraded      bool
	}{
		{
			name:       "configured Context wins without inheriting its kubeconfig Namespace",
			intent:     UIStartIntent{Kind: UIStartNew, ConfiguredContext: "saved-context"},
			preference: ScopePreference{Context: "current-context", UpdatedAt: preferenceTime}, preferenceFound: true,
			want: domain.ScopeCandidate{Context: "saved-context", Namespace: DefaultStartupNamespace},
		},
		{
			name:       "remembered Context precedes kubeconfig current Context",
			intent:     UIStartIntent{Kind: UIStartNew},
			preference: ScopePreference{Context: "saved-context", UpdatedAt: preferenceTime}, preferenceFound: true,
			want: domain.ScopeCandidate{Context: "saved-context", Namespace: DefaultStartupNamespace}, wantLoadCalls: 1,
		},
		{
			name:   "kubeconfig current Context is the empty preference fallback",
			intent: UIStartIntent{Kind: UIStartNew},
			want:   domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace}, wantLoadCalls: 1,
		},
		{
			name:       "missing remembered Context falls back to kubeconfig current Context",
			intent:     UIStartIntent{Kind: UIStartNew},
			preference: ScopePreference{Context: "saved-context", UpdatedAt: preferenceTime}, preferenceFound: true,
			missingRemembered: true,
			want:              domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace}, wantLoadCalls: 1,
		},
		{
			name:       "configured Namespace wins with remembered Context",
			intent:     UIStartIntent{Kind: UIStartNew, ConfiguredNamespace: "payments"},
			preference: ScopePreference{Context: "saved-context", UpdatedAt: preferenceTime}, preferenceFound: true,
			want: domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"}, wantLoadCalls: 1,
		},
		{
			name:   "unavailable preference is visible and falls back",
			intent: UIStartIntent{Kind: UIStartNew}, preferenceError: errors.New("preference unavailable"),
			want:          domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace},
			wantLoadCalls: 1, wantDegraded: true,
		},
		{
			name:       "invalid preference is visible and falls back",
			intent:     UIStartIntent{Kind: UIStartNew},
			preference: ScopePreference{Context: "", UpdatedAt: preferenceTime}, preferenceFound: true,
			want:          domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace},
			wantLoadCalls: 1, wantDegraded: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := newUIScopeActionRecorder()
			recorder.missingContext = test.missingRemembered
			manager := newCoordinatorUIScopeManager(t, recorder, nil)
			preferences := &recordingScopePreferenceStore{
				preference: test.preference, found: test.preferenceFound, loadErr: test.preferenceError,
			}
			history := newRecordingSessionResumeStore()
			coordinator := newUIScopeCoordinatorHarnessWithPreferences(t, manager, history, preferences)

			result, err := coordinator.StartUI(context.Background(), test.intent, domain.PrivacyModeStandard)
			if err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			if result.Validate() != nil || result.Session == nil || result.ScopeCandidate == nil ||
				*result.ScopeCandidate != test.want || result.ScopePreferenceDegraded != test.wantDegraded {
				t.Fatalf("StartUI() = %#v, want scope %#v degraded %v", result, test.want, test.wantDegraded)
			}
			if preferences.loadCalls != test.wantLoadCalls {
				t.Fatalf("preference load calls = %d, want %d", preferences.loadCalls, test.wantLoadCalls)
			}
			if history.searchCalls != 0 || history.listCalls != 0 || history.resumeCalls != 0 || history.latestCalls != 0 {
				t.Fatalf("startup queried Session history: %#v", history)
			}
			if recorder.count("contexts") != 1 || recorder.count("create") != 0 || recorder.count("verify") != 0 {
				t.Fatalf("startup candidate actions = %#v", recorder.snapshot())
			}
			if status := coordinator.uiStatus(); status.PersistenceDegraded != test.wantDegraded {
				t.Fatalf("status persistence degraded = %v, want %v", status.PersistenceDegraded, test.wantDegraded)
			}
		})
	}
}

func TestCoordinatorKeepsBareSessionStartupAvailableWithoutKubeconfigContexts(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	recorder.contextsError = errors.New("kubeconfig unavailable")
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	history := newRecordingSessionResumeStore()
	coordinator := newUIScopeCoordinatorHarness(t, manager, history)
	result, err := coordinator.StartUI(context.Background(), UIStartIntent{
		Kind: UIStartNew, ConfiguredNamespace: DefaultStartupNamespace,
	}, domain.PrivacyModeStandard)
	if err != nil || result.Session == nil || result.ScopeCandidate != nil || result.Validate() != nil {
		t.Fatalf("StartUI(without kubeconfig Contexts) = %#v, %v", result, err)
	}
	if recorder.count("contexts") != 1 || recorder.count("create") != 0 || recorder.count("verify") != 0 ||
		history.searchCalls != 0 || history.listCalls != 0 || history.resumeCalls != 0 || history.latestCalls != 0 {
		t.Fatalf("bare unavailable-scope startup actions = %#v, history %#v", recorder.snapshot(), history)
	}
}

func TestCoordinatorStoresOnlySuccessfullyActivatedContexts(t *testing.T) {
	t.Parallel()

	t.Run("successful exact activation stores Context after default Namespace verification", func(t *testing.T) {
		t.Parallel()
		recorder := newUIScopeActionRecorder()
		manager := newCoordinatorUIScopeManager(t, recorder, nil)
		preferences := new(recordingScopePreferenceStore)
		coordinator := newUIScopeCoordinatorHarnessWithPreferences(t, manager, newRecordingSessionResumeStore(), preferences)
		if _, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartResumePicker}, domain.PrivacyModeStandard); err != nil {
			t.Fatalf("StartUI() error = %v", err)
		}
		outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
			Kind: UICommandActivateScope, RequestID: 1,
			Scope: &domain.ScopeCandidate{Context: "saved-context", Namespace: DefaultStartupNamespace},
		})
		if err != nil || outcome.Scope == nil || outcome.Scope.Failure != "" || outcome.Scope.Namespace != DefaultStartupNamespace ||
			outcome.Scope.ScopePreferenceDegraded {
			t.Fatalf("ActivateScope() = %#v, %v", outcome, err)
		}
		if len(preferences.saves) != 1 || preferences.saves[0].Context != "saved-context" ||
			preferences.saves[0].Validate() != nil {
			t.Fatalf("saved preferences = %#v", preferences.saves)
		}
		if recorder.count("create") != 1 || recorder.count("verify") != 1 {
			t.Fatalf("exact activation actions = %#v, want only the exact default Namespace verification", recorder.snapshot())
		}
	})

	for _, test := range []struct {
		name      string
		configure func(*uiScopeActionRecorder)
		command   func() UICommand
		cancel    bool
	}{
		{
			name: "Namespace verification failure",
			configure: func(recorder *uiScopeActionRecorder) {
				recorder.verifyError[DefaultStartupNamespace] = errors.New("forbidden")
			},
			command: func() UICommand {
				return UICommand{Kind: UICommandActivateScope, RequestID: 2,
					Scope: &domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace}}
			},
		},
		{
			name: "stale generation",
			command: func() UICommand {
				return UICommand{Kind: UICommandActivateScope, RequestID: 3, ExpectedScopeGeneration: 9,
					Scope: &domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace}}
			},
		},
		{
			name: "cancelled command",
			command: func() UICommand {
				return UICommand{Kind: UICommandActivateScope, RequestID: 4,
					Scope: &domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace}}
			},
			cancel: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := newUIScopeActionRecorder()
			if test.configure != nil {
				test.configure(recorder)
			}
			manager := newCoordinatorUIScopeManager(t, recorder, nil)
			preferences := new(recordingScopePreferenceStore)
			coordinator := newUIScopeCoordinatorHarnessWithPreferences(t, manager, newRecordingSessionResumeStore(), preferences)
			if _, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartResumePicker}, domain.PrivacyModeStandard); err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			recorder.reset()
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			outcome, err := coordinator.ExecuteUICommand(ctx, test.command())
			if test.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("ExecuteUICommand(cancelled) error = %v", err)
				}
			} else if err != nil || outcome.Scope == nil || outcome.Scope.Failure == "" {
				t.Fatalf("failed ActivateScope() = %#v, %v", outcome, err)
			}
			if len(preferences.saves) != 0 {
				t.Fatalf("failed activation stored preferences %#v", preferences.saves)
			}
			if test.cancel && len(recorder.snapshot()) != 0 {
				t.Fatalf("cancelled activation actions = %#v, want zero", recorder.snapshot())
			}
		})
	}
}

func TestCoordinatorKeepsVerifiedScopeWhenPreferenceWriteFails(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	preferences := &recordingScopePreferenceStore{saveErr: errors.New("write unavailable")}
	coordinator := newUIScopeCoordinatorHarnessWithPreferences(t, manager, newRecordingSessionResumeStore(), preferences)
	if _, err := coordinator.StartUI(context.Background(), UIStartIntent{Kind: UIStartResumePicker}, domain.PrivacyModeStandard); err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandActivateScope, RequestID: 1,
		Scope: &domain.ScopeCandidate{Context: "current-context", Namespace: DefaultStartupNamespace},
	})
	if err != nil || outcome.Scope == nil || outcome.Scope.Failure != "" || !outcome.Scope.ReadOnly ||
		!outcome.Scope.ScopePreferenceDegraded {
		t.Fatalf("ActivateScope(write failure) = %#v, %v", outcome, err)
	}
	if live, ok := manager.CurrentScope(); !ok || live.Context != "current-context" || live.Namespace != DefaultStartupNamespace {
		t.Fatalf("verified scope after preference failure = %#v, %v", live, ok)
	}
	if status := coordinator.uiStatus(); !status.PersistenceDegraded {
		t.Fatal("preference write failure was absent from status")
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
	if store.searchCalls != 1 || store.listCalls != 0 || store.resumeCalls != 0 || store.latestCalls != 0 ||
		store.searchRequest.Filter != "payment" || store.searchRequest.Limit != MaxUIQueryCandidates {
		t.Fatalf("picker history calls = search %d list %d resume %d latest %d request %#v",
			store.searchCalls, store.listCalls, store.resumeCalls, store.latestCalls, store.searchRequest)
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

func TestCoordinatorResumeAcceptanceUsesOnlyCurrentScopeSnapshot(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	current, err := manager.SwitchContext(context.Background(), "current-context", 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	selected := domain.ResourceRef{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "default", Name: "current-api",
		UID: "current-uid", ResourceVersion: "31",
	}
	if err := manager.SelectResource(current, selected); err != nil {
		t.Fatalf("SelectResource() error = %v", err)
	}
	recorder.reset()

	store := newRecordingSessionResumeStore()
	resumedID := domain.SessionID(coordinatorUUID(64))
	store.history = resumedRecordWithResource(
		resumedID, time.Date(2026, 8, 10, 3, 30, 0, 0, time.UTC),
		domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"}, "saved-uid",
	)
	coordinator := newUIScopeCoordinatorHarness(t, manager, store)
	if _, err := coordinator.StartUI(context.Background(), UIStartIntent{
		Kind: UIStartResumeID, SessionID: resumedID,
	}, domain.PrivacyModeStandard); err != nil {
		t.Fatalf("StartUI() error = %v", err)
	}
	recorder.reset()
	request := UIResumeRequest{RequestID: 5, Mode: UIResumeExact, SessionID: resumedID}
	if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
		t.Fatalf("ResumeUI() = %#v, %v", result, err)
	}
	outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID, ExpectedScopeGeneration: current.Generation,
	})
	if err != nil || outcome.Failure != "" || outcome.Scope == nil || outcome.Scope.Failure != "" ||
		outcome.Scope.Context != current.Context || outcome.Scope.Namespace != current.Namespace || outcome.Resource != nil {
		t.Fatalf("resume acceptance = %#v, %v", outcome, err)
	}
	if actions := recorder.snapshot(); len(actions) != 0 {
		t.Fatalf("resume performed operational scope actions: %#v", actions)
	}
	view := manager.View()
	if view.Scope == nil || *view.Scope != current {
		t.Fatalf("resume changed current scope: %#v, want %#v", view, current)
	}
	if selected, selectErr := manager.SelectedResource(current); selectErr != nil || selected != nil {
		t.Fatalf("resume retained Resource authority: %#v, %v", selected, selectErr)
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

func TestCoordinatorResumeAcceptanceRejectsHistoricScopePayloadBeforeIO(t *testing.T) {
	t.Parallel()

	recorder := newUIScopeActionRecorder()
	manager := newCoordinatorUIScopeManager(t, recorder, nil)
	current, err := manager.SwitchContext(context.Background(), "current-context", 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	recorder.reset()
	store := newRecordingSessionResumeStore()
	resumedID := domain.SessionID(coordinatorUUID(81))
	saved := domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"}
	store.history = resumedRecordWithResource(
		resumedID, time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), saved, "saved-uid",
	)
	coordinator := newUIScopeCoordinatorHarness(t, manager, store)
	request := UIResumeRequest{RequestID: 9, Mode: UIResumeExact, SessionID: resumedID}
	if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
		t.Fatalf("ResumeUI() = %#v, %v", result, err)
	}

	if outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID,
		ExpectedScopeGeneration: current.Generation, Scope: &saved,
	}); !errors.Is(err, ErrInvalidUICommand) || outcome != (UICommandOutcome{}) {
		t.Fatalf("scope-bearing acceptance = %#v, %v", outcome, err)
	}
	if coordinator.CurrentUISession() != nil || len(recorder.snapshot()) != 0 {
		t.Fatalf("rejected acceptance changed state or performed I/O: session %#v, actions %#v",
			coordinator.CurrentUISession(), recorder.snapshot())
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
	if recorder.count("contexts") != 1 || recorder.count("create") != 0 || recorder.count("verify") != 0 {
		t.Fatalf("explicit startup candidate actions = %#v", recorder.snapshot())
	}
	recorder.reset()
	request := UIResumeRequest{RequestID: 11, Mode: UIResumeExact, SessionID: resumedID}
	if result, err := coordinator.ResumeUI(context.Background(), request); err != nil || result.Failure != "" {
		t.Fatalf("ResumeUI() = %#v, %v", result, err)
	}

	rejected, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID,
		ExpectedScopeGeneration: current.Generation, Scope: &savedScope,
	})
	if !errors.Is(err, ErrInvalidUICommand) || rejected != (UICommandOutcome{}) ||
		coordinator.CurrentUISession() != nil || len(recorder.snapshot()) != 0 {
		t.Fatalf("saved-scope acceptance = %#v, error = %v, actions = %#v", rejected, err, recorder.snapshot())
	}

	accepted, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandAcceptResume, RequestID: request.RequestID,
		ExpectedScopeGeneration: current.Generation,
	})
	if err != nil || accepted.Failure != "" || accepted.Scope == nil || accepted.Scope.Failure != "" ||
		accepted.Resource != nil || coordinator.CurrentUISession() == nil ||
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
	searchCalls   int
	searchRequest SessionSearchRequest
	listCalls     int
	resumeCalls   int
	latestCalls   int
	candidates    []ResumeSessionRecord
	history       ResumedSessionRecord
	err           error
	renameCalls   int
	renameRecord  RenameSessionRecord
	renameErr     error
}

func newRecordingSessionResumeStore() *recordingSessionResumeStore {
	return new(recordingSessionResumeStore)
}

func (store *recordingSessionResumeStore) SearchResumable(
	_ context.Context,
	request SessionSearchRequest,
) ([]ResumeSessionRecord, error) {
	store.searchCalls++
	store.searchRequest = request
	result := make([]ResumeSessionRecord, 0, min(len(store.candidates), request.Limit))
	for _, candidate := range store.candidates {
		if sessionRecordMatches(candidate, request.Filter) {
			result = append(result, candidate)
			if len(result) == request.Limit {
				break
			}
		}
	}
	return result, store.err
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
	contextsError  error
	verifyError    map[string]error
	actualUID      string
}

func newUIScopeActionRecorder() *uiScopeActionRecorder {
	return &uiScopeActionRecorder{verifyError: make(map[string]error), actualUID: "current-uid"}
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
	if factory.recorder.contextsError != nil {
		return nil, factory.recorder.contextsError
	}
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

type recordingScopePreferenceStore struct {
	mu         sync.Mutex
	preference ScopePreference
	found      bool
	loadErr    error
	saveErr    error
	loadCalls  int
	saves      []ScopePreference
}

func (store *recordingScopePreferenceStore) LoadLastContext(ctx context.Context) (ScopePreference, bool, error) {
	if err := ctx.Err(); err != nil {
		return ScopePreference{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadCalls++
	return store.preference, store.found, store.loadErr
}

func (store *recordingScopePreferenceStore) SaveLastContext(ctx context.Context, preference ScopePreference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saveErr != nil {
		return store.saveErr
	}
	store.saves = append(store.saves, preference)
	store.preference = preference
	store.found = true
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
	return newUIScopeCoordinatorHarnessWithPreferences(t, manager, history, new(recordingScopePreferenceStore))
}

func newUIScopeCoordinatorHarnessWithPreferences(
	t *testing.T,
	manager *ScopeManager,
	history *recordingSessionResumeStore,
	preferences *recordingScopePreferenceStore,
) *Coordinator {
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
		Sessions: persistence, Runs: persistence, Tools: persistence, Audits: persistence, ModelContext: persistence,
		Scope: manager, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: new(recordingUIEvents),
		RunResourcePolicies: coordinatorResourcePolicies{},
		Observer:            RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
		UI: &CoordinatorUIConfig{
			Sessions: history, Search: history, Titles: history, Startup: new(recordingStartupMaintenance), Scopes: manager,
			ScopePreferences: preferences,
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
		Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7,
		ActivatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}}
	identifiers := new(coordinatorIDs)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Sessions: persistence, Runs: persistence, Tools: persistence, Audits: persistence, ModelContext: persistence,
		Scope: scope, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: new(recordingUIEvents),
		RunResourcePolicies: coordinatorResourcePolicies{},
		Observer:            RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
		UI: &CoordinatorUIConfig{Sessions: history, Search: history, Titles: history, Startup: maintenance},
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
