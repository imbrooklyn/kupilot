package application

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// QuestionStartFailureReason is the closed, delivery-safe reason why an
// ordinary question did not cross the durable run-start barrier.
type QuestionStartFailureReason string

const (
	QuestionStartSessionUnavailable         QuestionStartFailureReason = "session_unavailable"
	QuestionStartScopeNotVerified           QuestionStartFailureReason = "scope_not_verified"
	QuestionStartScopeGenerationStale       QuestionStartFailureReason = "scope_generation_stale"
	QuestionStartSelectedResourceStale      QuestionStartFailureReason = "selected_resource_stale"
	QuestionStartPolicyGenerationStale      QuestionStartFailureReason = "policy_generation_stale"
	QuestionStartPolicySnapshotInvalid      QuestionStartFailureReason = "policy_snapshot_invalid"
	QuestionStartRunActive                  QuestionStartFailureReason = "run_active"
	QuestionStartRunStarting                QuestionStartFailureReason = "run_starting"
	QuestionStartApplicationOperationActive QuestionStartFailureReason = "application_operation_active"
	QuestionStartPersistenceDegraded        QuestionStartFailureReason = "persistence_degraded"
	QuestionStartPrecommitPersistenceFailed QuestionStartFailureReason = "precommit_persistence_failed"
	QuestionStartModelConfigurationMissing  QuestionStartFailureReason = "model_configuration_missing"
	QuestionStartConsentRequired            QuestionStartFailureReason = "consent_required"
	QuestionStartInputRejected              QuestionStartFailureReason = "input_rejected"
	QuestionStartUnknownSafeFailure         QuestionStartFailureReason = "unknown_safe_failure"
)

func (reason QuestionStartFailureReason) valid() bool {
	switch reason {
	case QuestionStartSessionUnavailable,
		QuestionStartScopeNotVerified,
		QuestionStartScopeGenerationStale,
		QuestionStartSelectedResourceStale,
		QuestionStartPolicyGenerationStale,
		QuestionStartPolicySnapshotInvalid,
		QuestionStartRunActive,
		QuestionStartRunStarting,
		QuestionStartApplicationOperationActive,
		QuestionStartPersistenceDegraded,
		QuestionStartPrecommitPersistenceFailed,
		QuestionStartModelConfigurationMissing,
		QuestionStartConsentRequired,
		QuestionStartInputRejected,
		QuestionStartUnknownSafeFailure:
		return true
	default:
		return false
	}
}

// QuestionStartRecoveryAction is an Application-owned next interaction. It is
// presentation guidance only and never grants run, scope, or model authority.
type QuestionStartRecoveryAction string

const (
	QuestionStartRecoverStartOrResumeSession QuestionStartRecoveryAction = "start_or_resume_session"
	QuestionStartRecoverSelectScope          QuestionStartRecoveryAction = "select_scope"
	QuestionStartRecoverSubmitAgain          QuestionStartRecoveryAction = "submit_again"
	QuestionStartRecoverSelectResource       QuestionStartRecoveryAction = "select_resource"
	QuestionStartRecoverReviewPolicy         QuestionStartRecoveryAction = "review_policy"
	QuestionStartRecoverSteerOrQueue         QuestionStartRecoveryAction = "steer_or_queue"
	QuestionStartRecoverWait                 QuestionStartRecoveryAction = "wait"
	QuestionStartRecoverRunDoctor            QuestionStartRecoveryAction = "run_doctor"
	QuestionStartRecoverConfigureModel       QuestionStartRecoveryAction = "configure_model"
	QuestionStartRecoverReviewConsent        QuestionStartRecoveryAction = "review_consent"
	QuestionStartRecoverEditInput            QuestionStartRecoveryAction = "edit_input"
)

func (action QuestionStartRecoveryAction) valid() bool {
	switch action {
	case QuestionStartRecoverStartOrResumeSession,
		QuestionStartRecoverSelectScope,
		QuestionStartRecoverSubmitAgain,
		QuestionStartRecoverSelectResource,
		QuestionStartRecoverReviewPolicy,
		QuestionStartRecoverSteerOrQueue,
		QuestionStartRecoverWait,
		QuestionStartRecoverRunDoctor,
		QuestionStartRecoverConfigureModel,
		QuestionStartRecoverReviewConsent,
		QuestionStartRecoverEditInput:
		return true
	default:
		return false
	}
}

// QuestionStartRunState is the current Application lifecycle relevant to a
// refused question. It does not restore or create run authority.
type QuestionStartRunState string

const (
	QuestionStartRunStateIdle     QuestionStartRunState = "idle"
	QuestionStartRunStateStarting QuestionStartRunState = "starting"
	QuestionStartRunStateRunning  QuestionStartRunState = "running"
)

// QuestionStartResourceState reports only whether the exact delivery
// selection agrees with Application state. It contains no resource identity.
type QuestionStartResourceState string

const (
	QuestionStartResourceNone    QuestionStartResourceState = "none"
	QuestionStartResourceCurrent QuestionStartResourceState = "current"
	QuestionStartResourceStale   QuestionStartResourceState = "stale"
)

// UIQuestionStartCurrentState is the bounded safe state returned with a
// refused question. Context and Namespace are included only for a currently
// independently verified scope; no historic candidate is projected here.
type UIQuestionStartCurrentState struct {
	SessionID                  domain.SessionID
	ScopeState                 ScopeState
	ScopeGeneration            int64
	Context                    string
	Namespace                  string
	ReadOnly                   bool
	PolicyGeneration           domain.PolicyGeneration
	PolicyHealthy              bool
	RunState                   QuestionStartRunState
	RunID                      domain.AgentRunID
	RunScopeGeneration         int64
	RunPolicyGeneration        domain.PolicyGeneration
	RunSequence                int64
	ApplicationOperationActive bool
	PersistenceDegraded        bool
	ResourceState              QuestionStartResourceState
}

// Validate checks the content-free projection grammar.
func (state UIQuestionStartCurrentState) Validate() error {
	if state.SessionID != "" && !state.SessionID.Valid() || state.ScopeGeneration < 0 ||
		(state.PolicyGeneration != 0 && !state.PolicyGeneration.Valid()) ||
		state.PolicyHealthy && !state.PolicyGeneration.Valid() {
		return ErrInvalidUIEvent
	}
	switch state.ScopeState {
	case ScopeStateActive:
		if state.ScopeGeneration < 1 || !domain.ValidContextName(state.Context) ||
			!domain.ValidNamespaceName(state.Namespace) || !state.ReadOnly {
			return ErrInvalidUIEvent
		}
	case ScopeStateUnavailable, ScopeStateActivating, ScopeStateClosed:
		if state.Context != "" || state.Namespace != "" || state.ReadOnly {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	switch state.RunState {
	case QuestionStartRunStateIdle:
		if state.RunID != "" || state.RunScopeGeneration != 0 || state.RunPolicyGeneration != 0 || state.RunSequence != 0 {
			return ErrInvalidUIEvent
		}
	case QuestionStartRunStateStarting:
		if state.RunID != "" || state.RunScopeGeneration != 0 || state.RunPolicyGeneration != 0 || state.RunSequence != 0 {
			return ErrInvalidUIEvent
		}
	case QuestionStartRunStateRunning:
		if !state.RunID.Valid() || state.RunScopeGeneration < 1 || !state.RunPolicyGeneration.Valid() || state.RunSequence < 0 {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	if state.ResourceState != QuestionStartResourceNone && state.ResourceState != QuestionStartResourceCurrent &&
		state.ResourceState != QuestionStartResourceStale {
		return ErrInvalidUIEvent
	}
	if state.ResourceState == QuestionStartResourceCurrent && state.ScopeState != ScopeStateActive {
		return ErrInvalidUIEvent
	}
	return nil
}

// UIQuestionStartFailure is emitted only when an ordinary question was not
// sent. Reason and recovery are validated as one code-owned pair.
type UIQuestionStartFailure struct {
	Reason   QuestionStartFailureReason
	Recovery QuestionStartRecoveryAction
	State    UIQuestionStartCurrentState
}

// Validate checks the fixed reason/recovery pair and safe state projection.
func (failure UIQuestionStartFailure) Validate() error {
	if !failure.Reason.valid() || !failure.Recovery.valid() || failure.State.Validate() != nil {
		return ErrInvalidUIEvent
	}
	allowed := questionStartRecovery(failure.Reason, failure.State)
	if failure.Recovery != allowed {
		return ErrInvalidUIEvent
	}
	return nil
}

func questionStartRecovery(reason QuestionStartFailureReason, state UIQuestionStartCurrentState) QuestionStartRecoveryAction {
	switch reason {
	case QuestionStartSessionUnavailable:
		return QuestionStartRecoverStartOrResumeSession
	case QuestionStartScopeNotVerified:
		return QuestionStartRecoverSelectScope
	case QuestionStartScopeGenerationStale:
		if state.ScopeState == ScopeStateActive {
			return QuestionStartRecoverSubmitAgain
		}
		return QuestionStartRecoverSelectScope
	case QuestionStartSelectedResourceStale:
		return QuestionStartRecoverSelectResource
	case QuestionStartPolicyGenerationStale:
		return QuestionStartRecoverReviewPolicy
	case QuestionStartPolicySnapshotInvalid,
		QuestionStartPersistenceDegraded,
		QuestionStartPrecommitPersistenceFailed,
		QuestionStartUnknownSafeFailure:
		return QuestionStartRecoverRunDoctor
	case QuestionStartRunActive:
		return QuestionStartRecoverSteerOrQueue
	case QuestionStartRunStarting, QuestionStartApplicationOperationActive:
		return QuestionStartRecoverWait
	case QuestionStartModelConfigurationMissing:
		return QuestionStartRecoverConfigureModel
	case QuestionStartConsentRequired:
		return QuestionStartRecoverReviewConsent
	case QuestionStartInputRejected:
		return QuestionStartRecoverEditInput
	default:
		return ""
	}
}

type runStartExpectation struct {
	scopeGeneration  int64
	policyGeneration domain.PolicyGeneration
	resource         *domain.ResourceRef
}

var (
	errQuestionStartScopeGenerationStale  = errors.New("question start scope generation is stale")
	errQuestionStartPolicyGenerationStale = errors.New("question start policy generation is stale")
	errQuestionStartPolicySnapshotInvalid = errors.New("question start policy snapshot is invalid")
	errQuestionStartSelectedResourceStale = errors.New("question start selected resource is stale")
)

func (expectation runStartExpectation) valid() bool {
	return expectation.scopeGeneration > 0 && expectation.policyGeneration.Valid() &&
		(expectation.resource == nil || domain.ValidateLiveResourceRef(*expectation.resource) == nil)
}

func (coordinator *Coordinator) questionStartCurrentState(ctx context.Context, expected *domain.ResourceRef) UIQuestionStartCurrentState {
	state := UIQuestionStartCurrentState{
		ScopeState: ScopeStateUnavailable, RunState: QuestionStartRunStateIdle,
		ResourceState: QuestionStartResourceNone,
	}
	if coordinator == nil {
		return state
	}
	coordinator.mu.Lock()
	if coordinator.currentSession != nil {
		state.SessionID = coordinator.currentSession.ID
	}
	state.PersistenceDegraded = coordinator.persistenceDegraded
	state.ApplicationOperationActive = coordinator.operations != 0
	if coordinator.starting || coordinator.active != nil && coordinator.active.terminal {
		state.RunState = QuestionStartRunStateStarting
	} else if coordinator.active != nil && !coordinator.active.terminal {
		active := coordinator.active
		state.RunState = QuestionStartRunStateRunning
		state.RunID = active.run.ID
		state.RunScopeGeneration = active.run.Scope.Generation
		state.RunPolicyGeneration = active.input.PolicyGeneration()
		if active.bridge != nil {
			state.RunSequence = active.bridge.currentSequence()
		}
	}
	coordinator.mu.Unlock()

	if coordinator.uiScopes != nil {
		view := coordinator.uiScopes.View()
		state.ScopeState = view.State
		state.ScopeGeneration = view.Generation
		if view.State == ScopeStateActive && view.Scope != nil {
			state.Context = view.Scope.Context
			state.Namespace = view.Scope.Namespace
			state.ReadOnly = true
			selected, err := coordinator.uiScopes.SelectedResource(*view.Scope)
			switch {
			case err != nil:
				state.ResourceState = QuestionStartResourceStale
			case sameOptionalResource(selected, expected):
				if selected != nil {
					state.ResourceState = QuestionStartResourceCurrent
				}
			default:
				state.ResourceState = QuestionStartResourceStale
			}
		}
	} else if scope, current := coordinator.scope.CurrentScope(); current {
		state.ScopeState = ScopeStateActive
		state.ScopeGeneration = scope.Generation
		state.Context = scope.Context
		state.Namespace = scope.Namespace
		state.ReadOnly = true
		if expected != nil {
			if domain.ValidateLiveResourceRef(*expected) == nil &&
				domain.ReferenceMatchesWorkingNamespace(*expected, scope.Namespace) {
				state.ResourceState = QuestionStartResourceCurrent
			} else {
				state.ResourceState = QuestionStartResourceStale
			}
		}
	}

	if coordinator.runResourcePolicies != nil {
		_, generation, healthy := coordinator.runResourcePolicies.ResourcePolicySnapshot(ctx)
		if generation.Valid() {
			state.PolicyGeneration = generation
		}
		state.PolicyHealthy = healthy && generation.Valid()
	}
	return state
}

func (coordinator *Coordinator) newQuestionStartFailure(
	ctx context.Context,
	reason QuestionStartFailureReason,
	expected *domain.ResourceRef,
) UIQuestionStartFailure {
	state := coordinator.questionStartCurrentState(ctx, expected)
	return UIQuestionStartFailure{Reason: reason, Recovery: questionStartRecovery(reason, state), State: state}
}

func sameOptionalResource(left, right *domain.ResourceRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (coordinator *Coordinator) precheckQuestionStart(
	ctx context.Context,
	command UICommand,
) (*domain.ResourceRef, *UIQuestionStartFailure) {
	coordinator.mu.Lock()
	closed := coordinator.closed
	currentSession := domain.SessionID("")
	if coordinator.currentSession != nil {
		currentSession = coordinator.currentSession.ID
	}
	starting := coordinator.starting
	active := coordinator.active != nil && !coordinator.active.terminal
	activeCleanup := coordinator.active != nil && coordinator.active.terminal
	operations := coordinator.operations
	persistenceDegraded := coordinator.persistenceDegraded
	runnerMissing := coordinator.runner == nil
	coordinator.mu.Unlock()

	fail := func(reason QuestionStartFailureReason) (*domain.ResourceRef, *UIQuestionStartFailure) {
		failure := coordinator.newQuestionStartFailure(ctx, reason, command.Resource)
		return nil, &failure
	}
	if closed || !currentSession.Valid() || currentSession != command.SessionID {
		return fail(QuestionStartSessionUnavailable)
	}
	if starting {
		return fail(QuestionStartRunStarting)
	}
	if activeCleanup {
		return fail(QuestionStartRunStarting)
	}
	if active {
		return fail(QuestionStartRunActive)
	}
	if operations != 0 {
		return fail(QuestionStartApplicationOperationActive)
	}
	if persistenceDegraded {
		return fail(QuestionStartPersistenceDegraded)
	}
	if runnerMissing {
		return fail(QuestionStartModelConfigurationMissing)
	}

	scope, current := coordinator.scope.CurrentScope()
	if !current || scope.Validate() != nil {
		state := coordinator.questionStartCurrentState(ctx, command.Resource)
		reason := QuestionStartScopeNotVerified
		if state.ScopeGeneration > 0 && state.ScopeGeneration != command.ExpectedScopeGeneration {
			reason = QuestionStartScopeGenerationStale
		}
		failure := UIQuestionStartFailure{Reason: reason, Recovery: questionStartRecovery(reason, state), State: state}
		return nil, &failure
	}
	if scope.Generation != command.ExpectedScopeGeneration {
		return fail(QuestionStartScopeGenerationStale)
	}

	selected := cloneResource(command.Resource)
	if coordinator.uiScopes != nil {
		applicationSelection, err := coordinator.uiScopes.SelectedResource(scope)
		if err != nil || !sameOptionalResource(applicationSelection, command.Resource) {
			return fail(QuestionStartSelectedResourceStale)
		}
		selected = applicationSelection
	}
	if selected != nil && (!domain.ReferenceMatchesWorkingNamespace(*selected, scope.Namespace) ||
		domain.ValidateLiveResourceRef(*selected) != nil) {
		return fail(QuestionStartSelectedResourceStale)
	}

	policy, generation, current := coordinator.runResourcePolicies.ResourcePolicySnapshot(ctx)
	if !current || !generation.Valid() || policy.Validate() != nil {
		return fail(QuestionStartPolicySnapshotInvalid)
	}
	if generation != command.ExpectedPolicyGeneration {
		return fail(QuestionStartPolicyGenerationStale)
	}
	return cloneResource(selected), nil
}

func (coordinator *Coordinator) classifyQuestionStartError(
	ctx context.Context,
	err error,
	expectation runStartExpectation,
) UIQuestionStartFailure {
	state := coordinator.questionStartCurrentState(ctx, expectation.resource)
	reason := QuestionStartUnknownSafeFailure
	switch {
	case errors.Is(err, errQuestionStartScopeGenerationStale):
		reason = QuestionStartScopeGenerationStale
	case errors.Is(err, errQuestionStartPolicyGenerationStale):
		reason = QuestionStartPolicyGenerationStale
	case errors.Is(err, errQuestionStartPolicySnapshotInvalid):
		reason = QuestionStartPolicySnapshotInvalid
	case errors.Is(err, errQuestionStartSelectedResourceStale):
		reason = QuestionStartSelectedResourceStale
	case errors.Is(err, ErrModelUnconfigured):
		reason = QuestionStartModelConfigurationMissing
	case errors.Is(err, ErrConsentRequired):
		reason = QuestionStartConsentRequired
	case errors.Is(err, ErrQuestionRejected):
		reason = QuestionStartInputRejected
	case errors.Is(err, ErrPersistenceUnavailable):
		reason = QuestionStartPrecommitPersistenceFailed
	case errors.Is(err, ErrRunAlreadyActive):
		if state.RunState == QuestionStartRunStateStarting {
			reason = QuestionStartRunStarting
		} else {
			reason = QuestionStartRunActive
		}
	case errors.Is(err, ErrCoordinatorBusy):
		reason = QuestionStartApplicationOperationActive
	case errors.Is(err, ErrScopeUnavailable):
		if state.ScopeGeneration != expectation.scopeGeneration {
			reason = QuestionStartScopeGenerationStale
		} else if state.ResourceState == QuestionStartResourceStale {
			reason = QuestionStartSelectedResourceStale
		} else {
			reason = QuestionStartScopeNotVerified
		}
	case errors.Is(err, ErrCoordinatorClosed), errors.Is(err, ErrCoordinatorDependency):
		coordinator.mu.Lock()
		current := coordinator.currentSession != nil && coordinator.currentSession.ID.Valid()
		coordinator.mu.Unlock()
		if !current || errors.Is(err, ErrCoordinatorClosed) {
			reason = QuestionStartSessionUnavailable
		}
	}
	return UIQuestionStartFailure{Reason: reason, Recovery: questionStartRecovery(reason, state), State: state}
}
