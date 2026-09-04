package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// DefaultOperationalDetailRetentionDays is the standard local detail default.
	DefaultOperationalDetailRetentionDays = 30
	// ReadAuditRetentionDays is the fixed lifecycle and read-audit period.
	ReadAuditRetentionDays = 90
	// WriteAuditRetentionDays is the fixed approval and write-audit period.
	WriteAuditRetentionDays        = 180
	maxStoredOperationalDetailDays = 3650
)

const (
	UICommandTightenRetention    UICommandKind = "tighten_retention"
	UICommandSetPersistenceMode  UICommandKind = "set_persistence_mode"
	UICommandDeleteSession       UICommandKind = "delete_session"
	UICommandClearHistory        UICommandKind = "clear_history"
	UICommandDeleteAllLocalState UICommandKind = "delete_all_local_state"
)

var (
	// ErrRetentionWouldWiden rejects a UI request that increases local retention.
	ErrRetentionWouldWiden = errors.New("the retention request would widen local persistence")
	// ErrRetentionSettingConflict reports a stale compare-and-tighten request.
	ErrRetentionSettingConflict = errors.New("the retention setting changed before the requested update")
	// ErrSessionDeletionUnsafe reports an approval state that may be executing.
	ErrSessionDeletionUnsafe = errors.New("the Session cannot be deleted while an approved operation may be executing")
	// ErrHistoryDeletionUnsafe reports an approval state that may be executing.
	ErrHistoryDeletionUnsafe = errors.New("local history cannot be deleted while an approved operation may be executing")
)

// RetentionSettingUpdate is the atomic compare-and-tighten persistence intent.
type RetentionSettingUpdate struct {
	ExpectedDays int
	Days         int
	UpdatedAt    time.Time
}

// Validate checks the Application-owned one-way retention policy and injected time.
func (update RetentionSettingUpdate) Validate() error {
	if !validCoordinatorTime(update.UpdatedAt) {
		return ErrCoordinatorDependency
	}
	if update.ExpectedDays < 0 || update.ExpectedDays > maxStoredOperationalDetailDays ||
		update.Days < 0 || update.Days > DefaultOperationalDetailRetentionDays ||
		update.Days > update.ExpectedDays {
		return ErrRetentionWouldWiden
	}
	return nil
}

// SessionLifecyclePersistence owns typed retention preferences and the atomic
// Session-graph deletion intent. It exposes no SQL, row, or transaction value.
type SessionLifecyclePersistence interface {
	LoadOperationalDetailRetention(context.Context) (days int, found bool, err error)
	TightenOperationalDetailRetention(context.Context, RetentionSettingUpdate) error
	DeleteSessionGraph(context.Context, domain.SessionID) error
	ClearHistory(context.Context) error
}

// LocalStateDeleter owns the bounded removal of the validated database and its
// known sidecars. It exposes no path, SQL handle, or recursive filesystem API.
type LocalStateDeleter interface {
	DeleteAllLocalState(context.Context) (LocalStateDeletionResult, error)
}

// SessionLifecycleIntent is the exclusive payload for one fixed lifecycle command.
type SessionLifecycleIntent struct {
	SessionID       domain.SessionID
	ExpectedCurrent bool
	Confirmed       bool
	RetentionDays   *int
	PrivacyMode     domain.PrivacyMode
}

func (intent SessionLifecycleIntent) validateFor(kind UICommandKind) error {
	switch kind {
	case UICommandTightenRetention:
		if intent.RetentionDays == nil || *intent.RetentionDays < 0 ||
			*intent.RetentionDays > DefaultOperationalDetailRetentionDays ||
			intent.SessionID != "" || intent.ExpectedCurrent || intent.Confirmed || intent.PrivacyMode != "" {
			return ErrInvalidUICommand
		}
	case UICommandSetPersistenceMode:
		if intent.RetentionDays != nil || intent.SessionID != "" || intent.ExpectedCurrent || intent.Confirmed ||
			(intent.PrivacyMode != domain.PrivacyModeStandard && intent.PrivacyMode != domain.PrivacyModeMinimal) {
			return ErrInvalidUICommand
		}
	case UICommandDeleteSession:
		if intent.RetentionDays != nil || intent.PrivacyMode != "" || !intent.SessionID.Valid() || !intent.Confirmed {
			return ErrInvalidUICommand
		}
	case UICommandClearHistory, UICommandDeleteAllLocalState:
		if intent.RetentionDays != nil || intent.PrivacyMode != "" || intent.SessionID != "" ||
			intent.ExpectedCurrent || !intent.Confirmed {
			return ErrInvalidUICommand
		}
	default:
		return ErrInvalidUICommand
	}
	return nil
}

// SessionLifecycleReview is the exact local-persistence state shown in /privacy.
type SessionLifecycleReview struct {
	CurrentSession                 *UISessionState
	OperationalDetailRetentionDays int
	ReadAuditRetentionDays         int
	WriteAuditRetentionDays        int
	StandardContentUntilDeletion   bool
	MinimalSessionsResumable       bool
	DeletionIsForensicErase        bool
}

// Validate rejects forged lifecycle policy projections.
func (review SessionLifecycleReview) Validate() error {
	if review.CurrentSession != nil && !review.CurrentSession.validate() ||
		review.OperationalDetailRetentionDays < 0 || review.OperationalDetailRetentionDays > maxStoredOperationalDetailDays ||
		review.ReadAuditRetentionDays != ReadAuditRetentionDays ||
		review.WriteAuditRetentionDays != WriteAuditRetentionDays ||
		!review.StandardContentUntilDeletion || review.MinimalSessionsResumable || review.DeletionIsForensicErase {
		return ErrInvalidUIEvent
	}
	return nil
}

// SessionDeletionResult identifies one committed graph deletion without content.
type SessionDeletionResult struct {
	SessionID  domain.SessionID
	WasCurrent bool
}

// Validate checks the bounded deletion result.
func (result SessionDeletionResult) Validate() error {
	if !result.SessionID.Valid() {
		return ErrInvalidUIEvent
	}
	return nil
}

// HistoryDeletionResult confirms one committed all-Session deletion while
// explicitly recording that non-history preferences remain.
type HistoryDeletionResult struct {
	SessionsCleared      bool
	PreferencesPreserved bool
}

// Validate checks the fixed successful clear-history result.
func (result HistoryDeletionResult) Validate() error {
	if !result.SessionsCleared || !result.PreferencesPreserved {
		return ErrInvalidUIEvent
	}
	return nil
}

// LocalStateDeletionResult reports whether storage became unavailable and
// whether every validated database file was removed. A closed partial failure
// is distinct from a preflight denial that leaves storage open.
type LocalStateDeletionResult struct {
	StorageClosed bool
	Complete      bool
}

// Validate checks the fixed delete-all result states.
func (result LocalStateDeletionResult) Validate() error {
	if result.Complete && !result.StorageClosed {
		return ErrInvalidUIEvent
	}
	return nil
}

// PrepareSessionDeletion durably cancels every unexecuted approval for one
// Session and rejects deletion while an approval consumption path is active.
func (coordinator *ApprovalCoordinator) PrepareSessionDeletion(ctx context.Context, sessionID domain.SessionID) error {
	if coordinator == nil || ctx == nil || !sessionID.Valid() {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for _, tracked := range coordinator.active {
		if tracked.request.SessionID == sessionID && tracked.consuming {
			return ErrSessionDeletionUnsafe
		}
	}
	return coordinator.closeMatching(ctx, func(tracked trackedApproval) bool {
		return tracked.request.SessionID == sessionID
	}, domain.ApprovalReasonUserCancelled)
}

// PrepareHistoryDeletion durably cancels every unexecuted approval and rejects
// deletion while any approval consumption path is active.
func (coordinator *ApprovalCoordinator) PrepareHistoryDeletion(ctx context.Context) error {
	if coordinator == nil || ctx == nil {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for _, tracked := range coordinator.active {
		if tracked.consuming {
			return ErrHistoryDeletionUnsafe
		}
	}
	return coordinator.closeMatching(ctx, func(trackedApproval) bool { return true }, domain.ApprovalReasonUserCancelled)
}

func (coordinator *Coordinator) sessionLifecycleReview(ctx context.Context) (SessionLifecycleReview, error) {
	var (
		days  int
		found bool
	)
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		var err error
		days, found, err = coordinator.sessions.LoadOperationalDetailRetention(operationContext)
		return err
	}); err != nil {
		return SessionLifecycleReview{}, err
	}
	if !found {
		days = DefaultOperationalDetailRetentionDays
	}
	coordinator.mu.Lock()
	var current *UISessionState
	if coordinator.currentSession != nil {
		current = projectUISession(*coordinator.currentSession, coordinator.currentResumed)
	}
	coordinator.mu.Unlock()
	review := SessionLifecycleReview{
		CurrentSession: current, OperationalDetailRetentionDays: days,
		ReadAuditRetentionDays: ReadAuditRetentionDays, WriteAuditRetentionDays: WriteAuditRetentionDays,
		StandardContentUntilDeletion: true,
	}
	if review.Validate() != nil {
		return SessionLifecycleReview{}, ErrPersistenceUnavailable
	}
	return review, nil
}

func (coordinator *Coordinator) executeRetentionCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	if err := coordinator.requirePrivacyChallenge(command); err != nil {
		return UICommandOutcome{}, err
	}
	review, err := coordinator.sessionLifecycleReview(ctx)
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{}, ErrPersistenceUnavailable
	}
	privacy, err := coordinator.privacy.Review(ctx)
	if err != nil {
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{}, err
	}
	days := *command.Lifecycle.RetentionDays
	if days > review.OperationalDetailRetentionDays {
		return UICommandOutcome{}, ErrRetentionWouldWiden
	}
	updatedAt := coordinator.now()
	update := RetentionSettingUpdate{
		ExpectedDays: review.OperationalDetailRetentionDays,
		Days:         days,
		UpdatedAt:    updatedAt,
	}
	if err := update.Validate(); err != nil {
		return UICommandOutcome{}, err
	}
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.sessions.TightenOperationalDetailRetention(operationContext, update)
	}); err != nil {
		if errors.Is(err, ErrRetentionSettingConflict) || errors.Is(err, ErrRetentionWouldWiden) {
			return UICommandOutcome{}, err
		}
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{}, ErrPersistenceUnavailable
	}
	review.OperationalDetailRetentionDays = days
	return UICommandOutcome{
		Command: command.Kind, RequestID: command.RequestID,
		Privacy: &privacy, Lifecycle: &review,
	}, nil
}

func (coordinator *Coordinator) operationalDetailPersistence(ctx context.Context) (bool, error) {
	var (
		days  int
		found bool
	)
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		var err error
		days, found, err = coordinator.sessions.LoadOperationalDetailRetention(operationContext)
		return err
	}); err != nil {
		return false, err
	}
	if !found {
		days = DefaultOperationalDetailRetentionDays
	}
	if days < 0 || days > maxStoredOperationalDetailDays {
		return false, ErrPersistenceUnavailable
	}
	return days > 0, nil
}

func (coordinator *Coordinator) executePersistenceModeCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	if err := coordinator.beginUIOperation(true); err != nil {
		return UICommandOutcome{}, err
	}
	defer coordinator.finishOperation()
	if err := coordinator.requirePrivacyChallenge(command); err != nil {
		return UICommandOutcome{}, err
	}
	lifecycle, err := coordinator.sessionLifecycleReview(ctx)
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{}, ErrPersistenceUnavailable
	}
	privacy, err := coordinator.privacy.Review(ctx)
	if err != nil {
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{}, err
	}
	session, err := coordinator.createSessionWithinOperation(ctx, CreateSessionCommand{PrivacyMode: command.Lifecycle.PrivacyMode})
	if err != nil {
		return UICommandOutcome{}, err
	}
	if coordinator.uiScopes != nil {
		if scope, current := coordinator.uiScopes.CurrentScope(); current {
			clearScopeResource(coordinator.uiScopes, scope)
		}
	}
	lifecycle.CurrentSession = projectUISession(session, false)
	return UICommandOutcome{
		Command: command.Kind, RequestID: command.RequestID,
		Session: projectUISession(session, false), Privacy: &privacy, Lifecycle: &lifecycle,
	}, nil
}

func (coordinator *Coordinator) requirePrivacyChallenge(command UICommand) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	challenge := coordinator.privacyChallenge
	if challenge == nil || challenge.requestID != command.RequestID || challenge.revision != command.PrivacyRevision {
		return ErrPrivacyReviewStale
	}
	return nil
}

func (coordinator *Coordinator) executeDeleteSessionCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	result := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	intent := *command.Lifecycle
	if err := coordinator.beginUIOperation(false); err != nil {
		return UICommandOutcome{}, err
	}
	coordinator.mu.Lock()
	coordinator.deletingSession = intent.SessionID
	coordinator.mu.Unlock()
	defer func() {
		coordinator.mu.Lock()
		if coordinator.deletingSession == intent.SessionID {
			coordinator.deletingSession = ""
		}
		coordinator.mu.Unlock()
		coordinator.finishOperation()
	}()

	coordinator.mu.Lock()
	current := coordinator.currentSession != nil && coordinator.currentSession.ID == intent.SessionID
	startingCancel := coordinator.startingCancel
	startingDone := coordinator.startingDone
	coordinator.mu.Unlock()
	if current != intent.ExpectedCurrent {
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	if current && startingCancel != nil {
		startingCancel()
		select {
		case <-ctx.Done():
			return UICommandOutcome{}, ctx.Err()
		case <-startingDone:
		}
	}
	if coordinator.approvals != nil {
		if err := coordinator.approvals.PrepareSessionDeletion(ctx, intent.SessionID); err != nil {
			result.Failure = UIQueryUnavailable
			return result, nil
		}
	}
	if current {
		coordinator.mu.Lock()
		state := coordinator.active
		coordinator.mu.Unlock()
		if state != nil && state.run.SessionID == intent.SessionID {
			state.cancel()
			_, waitErr := coordinator.WaitRun(ctx, state.run.ID)
			if waitErr != nil {
				if ctx.Err() != nil {
					return UICommandOutcome{}, ctx.Err()
				}
				result.Failure = UIQueryUnavailable
				return result, nil
			}
		}
	}
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.sessions.DeleteSessionGraph(operationContext, intent.SessionID)
	}); err != nil {
		if ctx.Err() != nil {
			return UICommandOutcome{}, ctx.Err()
		}
		result.Failure = UIQueryUnavailable
		return result, nil
	}

	coordinator.mu.Lock()
	if current && coordinator.currentSession != nil && coordinator.currentSession.ID == intent.SessionID {
		coordinator.currentSession = nil
		coordinator.currentResumed = false
		coordinator.lastDiagnosis = nil
		coordinator.lastEvidence = nil
		coordinator.modelContext.clear()
	}
	if coordinator.pendingResume != nil && coordinator.pendingResume.record.Session.ID == intent.SessionID {
		coordinator.pendingResume = nil
	}
	coordinator.privacyChallenge = nil
	coordinator.mu.Unlock()
	if current && coordinator.uiScopes != nil {
		if scope, active := coordinator.uiScopes.CurrentScope(); active {
			clearScopeResource(coordinator.uiScopes, scope)
		}
	}
	deletion := SessionDeletionResult{SessionID: intent.SessionID, WasCurrent: current}
	result.Deletion = &deletion
	return result, nil
}

func (coordinator *Coordinator) executeHistoryDeletionCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	result := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	if err := coordinator.beginUIOperation(false); err != nil {
		return UICommandOutcome{}, err
	}
	defer coordinator.finishOperation()

	coordinator.mu.Lock()
	startingCancel := coordinator.startingCancel
	startingDone := coordinator.startingDone
	coordinator.mu.Unlock()
	if startingCancel != nil {
		startingCancel()
		select {
		case <-ctx.Done():
			return UICommandOutcome{}, ctx.Err()
		case <-startingDone:
		}
	}
	if coordinator.approvals != nil {
		if err := coordinator.approvals.PrepareHistoryDeletion(ctx); err != nil {
			result.Failure = UIQueryUnavailable
			if command.Kind == UICommandDeleteAllLocalState {
				state := LocalStateDeletionResult{}
				result.LocalStateDeletion = &state
			}
			return result, nil
		}
	}
	coordinator.mu.Lock()
	state := coordinator.active
	coordinator.mu.Unlock()
	if state != nil {
		state.cancel()
		if _, err := coordinator.WaitRun(ctx, state.run.ID); err != nil {
			if ctx.Err() != nil {
				return UICommandOutcome{}, ctx.Err()
			}
			result.Failure = UIQueryUnavailable
			if command.Kind == UICommandDeleteAllLocalState {
				local := LocalStateDeletionResult{}
				result.LocalStateDeletion = &local
			}
			return result, nil
		}
	}

	if command.Kind == UICommandClearHistory {
		if err := coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.sessions.ClearHistory(operationContext)
		}); err != nil {
			if ctx.Err() != nil {
				return UICommandOutcome{}, ctx.Err()
			}
			result.Failure = UIQueryUnavailable
			return result, nil
		}
		coordinator.clearDeletedHistoryState()
		cleared := HistoryDeletionResult{SessionsCleared: true, PreferencesPreserved: true}
		result.HistoryDeletion = &cleared
		return result, nil
	}

	if coordinator.localState == nil {
		local := LocalStateDeletionResult{}
		result.LocalStateDeletion = &local
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	operationContext, cancel := context.WithTimeout(ctx, coordinator.persistenceLimit)
	local, err := coordinator.localState.DeleteAllLocalState(operationContext)
	cancel()
	if local.Validate() != nil || err == nil && !local.Complete {
		local = LocalStateDeletionResult{}
		err = ErrPersistenceUnavailable
	}
	result.LocalStateDeletion = &local
	if err != nil {
		result.Failure = UIQueryUnavailable
	}
	if local.StorageClosed {
		coordinator.clearDeletedHistoryState()
		coordinator.mu.Lock()
		coordinator.persistenceDegraded = true
		coordinator.mu.Unlock()
	}
	return result, nil
}

func (coordinator *Coordinator) clearDeletedHistoryState() {
	coordinator.mu.Lock()
	coordinator.currentSession = nil
	coordinator.currentResumed = false
	coordinator.pendingResume = nil
	coordinator.startupResume = nil
	coordinator.privacyChallenge = nil
	coordinator.lastDiagnosis = nil
	coordinator.lastEvidence = nil
	coordinator.modelContext.clear()
	coordinator.mu.Unlock()
	if coordinator.uiScopes != nil {
		if scope, active := coordinator.uiScopes.CurrentScope(); active {
			clearScopeResource(coordinator.uiScopes, scope)
		}
	}
}
