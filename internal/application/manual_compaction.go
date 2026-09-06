package application

import (
	"context"
	"errors"
	"sync"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	ErrManualCompactionInvalid     = errors.New("manual compaction request is invalid")
	ErrManualCompactionUnavailable = errors.New("manual compaction is unavailable")
	ErrManualCompactionFailed      = errors.New("manual compaction failed safely")
)

// RequestManualCompactionCommand freezes the current Session and policy
// generations observed by delivery. It contains no text or model authority.
type RequestManualCompactionCommand struct {
	SessionID                domain.SessionID
	ExpectedScopeGeneration  int64
	ExpectedPolicyGeneration domain.PolicyGeneration
}

func (command RequestManualCompactionCommand) Validate() error {
	if !command.SessionID.Valid() || command.ExpectedScopeGeneration < 1 ||
		!command.ExpectedPolicyGeneration.Valid() {
		return ErrManualCompactionInvalid
	}
	return nil
}

// UIManualCompactionResult is a bounded content-free result. Noop means the
// retained tail had not crossed the existing exact summary threshold.
type UIManualCompactionResult struct {
	Noop               bool
	CoveredMessages    int
	RecentTailMessages int
}

func (result UIManualCompactionResult) Validate() error {
	if result.CoveredMessages < 0 || result.CoveredMessages > domain.MaxSessionContextMessages ||
		result.RecentTailMessages < 0 || result.RecentTailMessages > domain.MaxSessionContextMessages ||
		result.Noop && result.CoveredMessages != 0 {
		return ErrManualCompactionInvalid
	}
	return nil
}

type activeManualCompaction struct {
	input           agent.ManualCompactionInput
	consentRevision uint64
	mode            domain.PrivacyMode

	mu         sync.Mutex
	started    bool
	ready      bool
	committing bool
	summary    *domain.SessionContextSummary
	err        error
}

type manualCompactionEventSink struct {
	coordinator *Coordinator
	state       *activeManualCompaction
}

func (sink *manualCompactionEventSink) AcceptManualCompaction(
	ctx context.Context,
	event agent.ManualCompactionEvent,
) agent.EventSinkResult {
	if sink == nil || sink.coordinator == nil || sink.state == nil {
		return agent.EventSinkRejected
	}
	return sink.coordinator.acceptManualCompactionEvent(ctx, sink.state, event)
}

func (coordinator *Coordinator) executeManualCompactionCommand(
	ctx context.Context,
	command UICommand,
) (UICommandOutcome, error) {
	coordinator.mu.Lock()
	var sessionID domain.SessionID
	if coordinator.currentSession != nil {
		sessionID = coordinator.currentSession.ID
	}
	coordinator.mu.Unlock()
	result, err := coordinator.RequestManualCompaction(ctx, RequestManualCompactionCommand{
		SessionID: sessionID, ExpectedScopeGeneration: command.ExpectedScopeGeneration,
		ExpectedPolicyGeneration: command.ExpectedPolicyGeneration,
	})
	if err != nil {
		return UICommandOutcome{}, err
	}
	return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Compaction: &result}, nil
}

// RequestManualCompaction invokes the one existing Eino summarization handler
// synchronously. It starts no AgentRun, Tool, approval, Reviewer, or queue
// drain, and it publishes no ordinary run event.
func (coordinator *Coordinator) RequestManualCompaction(
	ctx context.Context,
	command RequestManualCompactionCommand,
) (UIManualCompactionResult, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return UIManualCompactionResult{}, ErrManualCompactionInvalid
	}
	if err := ctx.Err(); err != nil {
		return UIManualCompactionResult{}, err
	}
	if err := coordinator.beginUIOperation(true); err != nil {
		return UIManualCompactionResult{}, err
	}
	defer coordinator.finishOperation()

	coordinator.mu.Lock()
	compactor := coordinator.compactor
	coordinator.mu.Unlock()
	if compactor == nil {
		return UIManualCompactionResult{}, ErrManualCompactionUnavailable
	}
	if coordinator.approvals != nil {
		_, action := coordinator.approvals.Status()
		if action != nil {
			return UIManualCompactionResult{}, ErrCoordinatorBusy
		}
	}
	authorized, privacyErr := coordinator.privacy.AuthorizeModel(ctx)
	if privacyErr != nil {
		if ctx.Err() != nil {
			return UIManualCompactionResult{}, ctx.Err()
		}
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		return UIManualCompactionResult{}, ErrPersistenceUnavailable
	}
	if !authorized {
		return UIManualCompactionResult{}, ErrConsentRequired
	}
	scope, current := coordinator.scope.CurrentScope()
	if !current || scope.Validate() != nil || scope.Generation != command.ExpectedScopeGeneration {
		return UIManualCompactionResult{}, ErrScopeUnavailable
	}
	_, generation, current := coordinator.runResourcePolicies.ResourcePolicySnapshot(ctx)
	if !current || generation != command.ExpectedPolicyGeneration ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, generation) {
		return UIManualCompactionResult{}, ErrManualCompactionUnavailable
	}
	conversation, err := coordinator.conversationForRun(ctx, command.SessionID)
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return UIManualCompactionResult{}, ErrPersistenceUnavailable
	}
	if latest, ok := coordinator.scope.CurrentScope(); !ok || latest != scope ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, generation) {
		return UIManualCompactionResult{}, ErrManualCompactionUnavailable
	}
	if authorized, privacyErr = coordinator.privacy.AuthorizeModel(ctx); privacyErr != nil || !authorized {
		if privacyErr != nil {
			coordinator.privacy.FailClosed()
			coordinator.markGlobalPersistenceDegraded()
			return UIManualCompactionResult{}, ErrPersistenceUnavailable
		}
		return UIManualCompactionResult{}, ErrConsentRequired
	}

	operationID, idErr := coordinator.newCompactionID()
	profile, originHash := coordinator.currentAgentBinding()
	privacy := coordinator.privacy.Snapshot()
	input, inputErr := agent.NewManualCompactionInput(
		operationID, command.SessionID, conversation, scope, generation, profile, originHash, coordinator.budgetLimits,
	)
	if idErr != nil || inputErr != nil || !privacy.Accepted || privacy.Role != domain.ModelRoleAgent ||
		privacy.OriginHash != originHash || privacy.Revision == 0 {
		return UIManualCompactionResult{}, ErrManualCompactionUnavailable
	}
	state := &activeManualCompaction{
		input: input, consentRevision: privacy.Revision, mode: coordinator.currentPrivacyMode(),
	}
	coordinator.mu.Lock()
	if !coordinator.manualCompactionCurrentLocked(ctx, state) {
		coordinator.mu.Unlock()
		return UIManualCompactionResult{}, ErrManualCompactionUnavailable
	}
	coordinator.manualCompaction = state
	coordinator.mu.Unlock()
	defer func() {
		coordinator.mu.Lock()
		if coordinator.manualCompaction == state {
			coordinator.manualCompaction = nil
		}
		coordinator.mu.Unlock()
	}()

	compactResult, compactErr := compactor.Compact(ctx, input, &manualCompactionEventSink{coordinator: coordinator, state: state})
	state.mu.Lock()
	eventErr := state.err
	started := state.started
	ready := state.ready
	var candidate *domain.SessionContextSummary
	if state.summary != nil {
		value := *state.summary
		candidate = &value
	}
	state.mu.Unlock()
	if compactErr != nil || eventErr != nil {
		if ctx.Err() != nil {
			return UIManualCompactionResult{}, ctx.Err()
		}
		if errors.Is(eventErr, ErrPersistenceUnavailable) {
			coordinator.markGlobalPersistenceDegraded()
			return UIManualCompactionResult{}, ErrPersistenceUnavailable
		}
		return UIManualCompactionResult{}, ErrManualCompactionFailed
	}
	if compactResult.Validate(input) != nil || compactResult.Noop && (started || ready || candidate != nil) ||
		compactResult.Summary != nil && (!ready || candidate == nil || *compactResult.Summary != *candidate) {
		return UIManualCompactionResult{}, ErrManualCompactionFailed
	}
	if compactResult.Noop {
		return UIManualCompactionResult{Noop: true, RecentTailMessages: len(conversation.Turns())}, nil
	}
	if err := coordinator.commitManualCompaction(ctx, state, *candidate); err != nil {
		if errors.Is(err, ErrPersistenceUnavailable) {
			coordinator.markGlobalPersistenceDegraded()
		}
		return UIManualCompactionResult{}, err
	}
	return UIManualCompactionResult{
		CoveredMessages:    candidate.CoveredCount,
		RecentTailMessages: len(conversation.Coverage()) - candidate.CoveredCount,
	}, nil
}

func (coordinator *Coordinator) newCompactionID() (domain.CompactionID, error) {
	return coordinator.identifiers.NewCompactionID()
}

func (coordinator *Coordinator) manualCompactionCurrentLocked(
	ctx context.Context,
	state *activeManualCompaction,
) bool {
	if state == nil || coordinator.closed || coordinator.starting || coordinator.active != nil ||
		coordinator.currentSession == nil || coordinator.currentSession.ID != state.input.SessionID() ||
		coordinator.modelContext.sessionID != state.input.SessionID() || !coordinator.modelContext.loaded ||
		coordinator.modelContext.mode != state.mode || coordinator.modelContext.conversationMustEqual(state.input.Conversation()) != nil ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, state.input.PolicyGeneration()) {
		return false
	}
	scope, current := coordinator.scope.CurrentScope()
	privacy := coordinator.privacy.Snapshot()
	profile, originHash := coordinator.currentAgentBindingLocked()
	return current && scope == state.input.Scope() && privacy.Loaded && privacy.Accepted && privacy.Revision == state.consentRevision &&
		privacy.Role == domain.ModelRoleAgent && privacy.OriginHash == state.input.OriginHash() &&
		profile == state.input.Profile() && originHash == state.input.OriginHash()
}

func (state sessionModelContext) conversationMustEqual(expected agent.ConversationContext) error {
	actual, err := state.conversation()
	if err != nil || !conversationContextsEqual(actual, expected) {
		return ErrModelContextUnavailable
	}
	return nil
}

func conversationContextsEqual(left, right agent.ConversationContext) bool {
	leftTurns, rightTurns := left.Turns(), right.Turns()
	leftCoverage, rightCoverage := left.Coverage(), right.Coverage()
	if len(leftTurns) != len(rightTurns) || len(leftCoverage) != len(rightCoverage) {
		return false
	}
	for index := range leftTurns {
		if leftTurns[index] != rightTurns[index] {
			return false
		}
	}
	for index := range leftCoverage {
		if leftCoverage[index] != rightCoverage[index] {
			return false
		}
	}
	leftSummary, rightSummary := left.Summary(), right.Summary()
	return leftSummary == nil && rightSummary == nil || leftSummary != nil && rightSummary != nil && *leftSummary == *rightSummary
}

func (coordinator *Coordinator) acceptManualCompactionEvent(
	ctx context.Context,
	state *activeManualCompaction,
	event agent.ManualCompactionEvent,
) agent.EventSinkResult {
	if ctx == nil || event.Validate() != nil || event.ID != state.input.ID() ||
		event.SessionID != state.input.SessionID() || event.ScopeGeneration != state.input.Scope().Generation ||
		event.PolicyGeneration != state.input.PolicyGeneration() {
		return agent.EventSinkRejected
	}
	if event.Kind == agent.ManualCompactionStarted {
		authorized, err := coordinator.privacy.AuthorizeModel(ctx)
		coordinator.mu.Lock()
		current := coordinator.manualCompaction == state && coordinator.manualCompactionCurrentLocked(ctx, state)
		coordinator.mu.Unlock()
		state.mu.Lock()
		defer state.mu.Unlock()
		if err != nil || !authorized || !current || state.started || state.ready || state.committing || state.summary != nil {
			state.err = ErrManualCompactionUnavailable
			return agent.EventSinkRejected
		}
		state.started = true
		return agent.EventSinkAccepted
	}

	if event.Summary == nil || validateManualSummary(state.input, *event.Summary) != nil {
		state.mu.Lock()
		state.err = ErrManualCompactionFailed
		state.mu.Unlock()
		return agent.EventSinkRejected
	}
	coordinator.mu.Lock()
	current := coordinator.manualCompaction == state && coordinator.manualCompactionCurrentLocked(ctx, state)
	coordinator.mu.Unlock()
	if !current {
		state.mu.Lock()
		state.err = ErrManualCompactionUnavailable
		state.mu.Unlock()
		return agent.EventSinkRejected
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.started || state.ready || state.committing || state.summary != nil {
		state.err = ErrManualCompactionFailed
		return agent.EventSinkRejected
	}
	value := *event.Summary
	state.summary = &value
	state.ready = true
	return agent.EventSinkAccepted
}

func (coordinator *Coordinator) commitManualCompaction(
	ctx context.Context,
	state *activeManualCompaction,
	summary domain.SessionContextSummary,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	authorized, privacyErr := coordinator.privacy.AuthorizeModel(ctx)
	if privacyErr != nil {
		coordinator.privacy.FailClosed()
		return ErrPersistenceUnavailable
	}
	if !authorized {
		return ErrConsentRequired
	}
	state.mu.Lock()
	if !state.started || !state.ready || state.committing || state.summary == nil || *state.summary != summary {
		state.mu.Unlock()
		return ErrManualCompactionFailed
	}
	state.committing = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.committing = false
		state.mu.Unlock()
	}()

	coordinator.mu.Lock()
	current := coordinator.manualCompaction == state && coordinator.manualCompactionCurrentLocked(ctx, state)
	coordinator.mu.Unlock()
	if !current {
		return ErrManualCompactionUnavailable
	}
	if state.mode != domain.PrivacyModeMinimal {
		if coordinator.modelContextStore == nil || coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.modelContextStore.SaveSessionContextSummary(operationContext, summary)
		}) != nil {
			return ErrPersistenceUnavailable
		}
	}
	coordinator.mu.Lock()
	err := coordinator.applyManualSummaryLocked(state.input, summary)
	coordinator.mu.Unlock()
	if err != nil {
		// UI operations serialize every admitted Session, binding, and generation
		// mutation. A failure here is therefore an internal invariant violation,
		// not a recoverable second persistence attempt.
		return ErrManualCompactionFailed
	}
	return nil
}

func validateManualSummary(input agent.ManualCompactionInput, summary domain.SessionContextSummary) error {
	if input.Validate() != nil || summary.Validate() != nil || summary.SessionID != input.SessionID() ||
		summary.AgentProfile != input.Profile() || summary.AgentOriginHash != input.OriginHash() {
		return ErrManualCompactionInvalid
	}
	conversation := input.Conversation()
	coverage := conversation.Coverage()
	previousCovered := 0
	if previous := conversation.Summary(); previous != nil {
		previousCovered = previous.CoveredCount
	}
	if summary.CoveredCount <= previousCovered || summary.CoveredCount > len(coverage) ||
		domain.ValidateSessionContextCoverage(coverage[:summary.CoveredCount]) != nil {
		return ErrManualCompactionInvalid
	}
	digest, coveredBytes, err := domain.SessionContextCoverageDigestItems(coverage[:summary.CoveredCount])
	if err != nil || digest != summary.CoverageDigest || coveredBytes != summary.CoveredBytes ||
		coverage[0].MessageID != summary.CoveredFirstID || coverage[summary.CoveredCount-1].MessageID != summary.CoveredThroughID {
		return ErrManualCompactionInvalid
	}
	return nil
}

func (coordinator *Coordinator) applyManualSummaryLocked(
	input agent.ManualCompactionInput,
	summary domain.SessionContextSummary,
) error {
	if coordinator.modelContext.sessionID != input.SessionID() || !coordinator.modelContext.loaded ||
		validateManualSummary(input, summary) != nil {
		return ErrManualCompactionInvalid
	}
	previousCovered := 0
	if previous := input.Conversation().Summary(); previous != nil {
		previousCovered = previous.CoveredCount
	}
	drop := summary.CoveredCount - previousCovered
	if drop < 1 || drop > len(coordinator.modelContext.turns) {
		return ErrManualCompactionInvalid
	}
	value := summary
	coordinator.modelContext.summary = &value
	coordinator.modelContext.turns = append([]agent.ConversationTurn(nil), coordinator.modelContext.turns[drop:]...)
	coordinator.modelContext.recentTailCount = len(coordinator.modelContext.turns)
	generatedAt := summary.GeneratedAt
	coordinator.modelContext.compressedAt = &generatedAt
	return nil
}
