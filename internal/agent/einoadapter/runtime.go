package einoadapter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type boundExecution struct {
	call      agent.BoundToolCall
	requested domain.ToolInvocation
	executed  bool
	toolName  domain.ToolName
	modelCall domain.ModelToolCall
}

type runState struct {
	mu sync.Mutex

	input       agent.RunInput
	model       agent.Model
	tools       agent.ToolHandlers
	scopeGuard  agent.RunScopeGuard
	identifiers agent.RunIdentifierSource
	now         func() time.Time
	budget      *agent.RunBudget
	registry    *agent.EvidenceRegistry
	publisher   *agent.EventPublisher

	firstModelRequestID domain.ModelRequestID
	firstModelUsed      bool
	stepPending         bool
	stepEvidence        int
	toolSequence        int
	boundCalls          map[string]*boundExecution
	modelRequestIDs     map[domain.ModelRequestID]struct{}
	toolInvocationIDs   map[domain.ToolInvocationID]struct{}
	toolBatchFailure    error
}

func (state *runState) toolBatchAbort() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.toolBatchFailure
}

func (state *runState) abortToolBatch(err error) {
	if err == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.toolBatchFailure == nil {
		state.toolBatchFailure = err
	}
}

func (state *runState) clearToolBatchAbort() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.toolBatchFailure = nil
}

func (state *runState) publish(ctx context.Context, event agent.RunEvent) error {
	result, err := state.publisher.Publish(ctx, event)
	if err != nil {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	if result == agent.EventSinkRejected {
		return failedRuntime(domain.SafeErrorClassPersistenceUnavailable, safeEventRejected, nil)
	}
	return nil
}

func (state *runState) checkScope(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return normalizeFrameworkError(err)
	}
	if !state.scopeGuard.Current(ctx, state.input.Scope()) {
		return staleRuntime(nil)
	}
	if err := ctx.Err(); err != nil {
		return normalizeFrameworkError(err)
	}
	return nil
}

func (state *runState) nextModelRequestID() (domain.ModelRequestID, error) {
	state.mu.Lock()
	if !state.firstModelUsed {
		state.firstModelUsed = true
		id := state.firstModelRequestID
		if !id.Valid() {
			state.mu.Unlock()
			return "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		if _, duplicate := state.modelRequestIDs[id]; duplicate {
			state.mu.Unlock()
			return "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		state.modelRequestIDs[id] = struct{}{}
		state.mu.Unlock()
		return id, nil
	}
	state.mu.Unlock()
	id, err := state.identifiers.NewModelRequestID()
	if err != nil || !id.Valid() {
		return "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	state.mu.Lock()
	if _, duplicate := state.modelRequestIDs[id]; duplicate {
		state.mu.Unlock()
		return "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	state.modelRequestIDs[id] = struct{}{}
	state.mu.Unlock()
	return id, nil
}

func (state *runState) beginStep(ctx context.Context) error {
	state.mu.Lock()
	pending := state.stepPending
	newEvidence := state.stepEvidence
	if pending {
		state.stepPending = false
		state.stepEvidence = 0
	}
	state.mu.Unlock()
	if pending {
		if err := state.budget.CompleteStep(newEvidence); err != nil {
			return runtimeFailureFromBudget(err)
		}
	}
	if err := state.budget.ReserveStep(ctx); err != nil {
		return runtimeFailureFromBudget(err)
	}
	state.mu.Lock()
	state.stepPending = true
	state.stepEvidence = 0
	state.mu.Unlock()
	return nil
}

func (state *runState) finishStep(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return normalizeFrameworkError(err)
	}
	state.mu.Lock()
	if !state.stepPending {
		state.mu.Unlock()
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	newEvidence := state.stepEvidence
	state.stepPending = false
	state.stepEvidence = 0
	state.mu.Unlock()
	if err := state.budget.CompleteStep(newEvidence); err != nil {
		return runtimeFailureFromBudget(err)
	}
	return nil
}

func (state *runState) addStepEvidence(count int) error {
	if count < 0 {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.stepPending {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	state.stepEvidence += count
	return nil
}

func (state *runState) validateDiagnosis(draft agent.DiagnosisDraft, modelDraft bool) (domain.Diagnosis, error) {
	id, err := state.identifiers.NewDiagnosisID()
	if err != nil || !id.Valid() {
		return domain.Diagnosis{}, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	createdAt := state.now()
	if !validRuntimeTime(createdAt) {
		return domain.Diagnosis{}, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	diagnosis, err := agent.ValidateDiagnosis(draft, agent.DiagnosisMetadata{ID: id, CreatedAt: createdAt}, state.registry)
	if err != nil {
		if !modelDraft {
			return domain.Diagnosis{}, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
		}
		if errors.Is(err, agent.ErrSensitiveModelTextBlocked) {
			return domain.Diagnosis{}, failedRuntime(domain.SafeErrorClassSensitiveOutputBlocked, safeSensitiveModelTextBlocked, err)
		}
		return domain.Diagnosis{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, err)
	}
	return diagnosis, nil
}

func (state *runState) finishDiagnosis(ctx context.Context, diagnosis domain.Diagnosis) agent.RunOutcome {
	if err := ctx.Err(); err != nil {
		return state.finishFailure(ctx, normalizeFrameworkError(err))
	}
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis}); err != nil {
		return state.finishFailure(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return state.finishFailure(ctx, normalizeFrameworkError(err))
	}
	state.budget.Terminate(agent.RunStopCompleted)
	terminalCtx := context.WithoutCancel(ctx)
	_, _ = state.publisher.Publish(terminalCtx, agent.RunEvent{Kind: agent.RunEventRunCompleted})
	outcome := agent.RunOutcome{Status: domain.AgentRunStatusCompleted, Diagnosis: &diagnosis}
	if outcome.Validate(state.input) != nil {
		return internalOutcome()
	}
	return outcome
}

func (state *runState) finishFailure(ctx context.Context, err error) agent.RunOutcome {
	failure := normalizeFailure(ctx, err)
	if failure.localDiagnosis {
		return state.finishLocalDiagnosis(ctx, failure)
	}
	state.budget.Terminate(failure.stopReason)
	event := terminalFailureEvent(failure)
	terminalCtx := context.WithoutCancel(ctx)
	_, _ = state.publisher.Publish(terminalCtx, event)
	class := failure.class
	outcome := agent.RunOutcome{
		Status:      failure.status,
		ErrorClass:  &class,
		SafeMessage: failure.safeMessage,
	}
	if outcome.Validate(state.input) != nil {
		return internalOutcome()
	}
	return outcome
}

func normalizeFailure(ctx context.Context, err error) *runtimeFailure {
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &runtimeFailure{
				status:      domain.AgentRunStatusTimedOut,
				class:       domain.SafeErrorClassTimeout,
				safeMessage: "The diagnostic run reached its time limit.",
				stopReason:  agent.RunStopTimedOut,
				cause:       err,
			}
		case errors.Is(ctx.Err(), context.Canceled):
			return &runtimeFailure{
				status:      domain.AgentRunStatusCancelled,
				class:       domain.SafeErrorClassCancelled,
				safeMessage: "The diagnostic run was cancelled.",
				stopReason:  agent.RunStopCancelled,
				cause:       err,
			}
		}
	}
	var failure *runtimeFailure
	if errors.As(err, &failure) {
		return failure
	}
	var budgetError *agent.RunBudgetError
	if errors.As(err, &budgetError) {
		return runtimeFailureFromBudget(budgetError)
	}
	return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
}

func terminalFailureEvent(failure *runtimeFailure) agent.RunEvent {
	switch failure.status {
	case domain.AgentRunStatusCancelled:
		return agent.RunEvent{Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationOwnerCancelled}
	case domain.AgentRunStatusTimedOut:
		return agent.RunEvent{Kind: agent.RunEventRunTimedOut, TerminationReason: agent.RunTerminationDeadline}
	case domain.AgentRunStatusStaleScope:
		return agent.RunEvent{Kind: agent.RunEventRunStaleScope, TerminationReason: agent.RunTerminationScopeChanged}
	case domain.AgentRunStatusInterrupted:
		return agent.RunEvent{Kind: agent.RunEventRunInterrupted, TerminationReason: agent.RunTerminationInterrupted}
	default:
		return agent.RunEvent{
			Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class:       failure.class,
				SafeMessage: failure.safeMessage,
			},
		}
	}
}

func (state *runState) finishLocalDiagnosis(ctx context.Context, failure *runtimeFailure) agent.RunOutcome {
	if ctx != nil && ctx.Err() != nil {
		failure.localDiagnosis = false
		return state.finishFailure(ctx, failure)
	}
	kind := domain.MissingInformationUnsupported
	switch failure.stopReason {
	case agent.RunStopStepLimit,
		agent.RunStopToolCallLimit,
		agent.RunStopModelCallLimit,
		agent.RunStopToolResultBytes,
		agent.RunStopLogCallLimit:
		kind = domain.MissingInformationTruncated
	}
	draft := agent.DiagnosisDraft{
		MissingInformation: []domain.MissingInformation{{
			Kind:   kind,
			Detail: failure.safeMessage,
			Impact: "The diagnosis is limited to observations accepted before the runtime stopped.",
		}},
	}
	diagnosis, err := state.validateDiagnosis(draft, false)
	if err != nil {
		failure.localDiagnosis = false
		return state.finishFailure(ctx, err)
	}
	return state.finishDiagnosis(ctx, diagnosis)
}
