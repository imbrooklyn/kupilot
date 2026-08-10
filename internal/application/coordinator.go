package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	DefaultPersistenceTimeout = 5 * time.Second
	MaxPersistenceTimeout     = 10 * time.Second
)

var (
	ErrCoordinatorDependency  = errors.New("Application Coordinator dependencies are invalid")
	ErrCoordinatorClosed      = errors.New("the Application Coordinator is closed")
	ErrCoordinatorBusy        = errors.New("another Application operation is active")
	ErrRunAlreadyActive       = errors.New("another AgentRun is active")
	ErrRunNotActive           = errors.New("the requested AgentRun is not active")
	ErrScopeUnavailable       = errors.New("an exact verified Kubernetes scope is unavailable")
	ErrQuestionRejected       = errors.New("the diagnostic question was rejected by local safety policy")
	ErrPersistenceUnavailable = errors.New("local persistence is unavailable")
	ErrInvalidAgentEvent      = errors.New("the Agent event was rejected")
	ErrApplicationRunFailed   = errors.New("the AgentRun failed safely")
)

// CoordinatorConfig contains only consumer-owned ports and neutral Agent
// contracts. No adapter, framework, client, transport, or database type crosses
// this boundary.
type CoordinatorConfig struct {
	Sessions           SessionPersistence
	Runs               RunPersistence
	Tools              ToolEvidencePersistence
	Audits             AuditPersistence
	Scope              ActiveScope
	Runner             agent.AgentRunner
	Identifiers        ApplicationIdentifierSource
	AuditIdentifiers   AuditIdentifierSource
	Questions          QuestionProcessor
	UIEvents           UIEventSink
	Observer           RunObserver
	Now                func() time.Time
	BudgetLimits       agent.RunBudgetLimits
	PersistenceTimeout time.Duration
}

// RunResult is the bounded in-memory terminal result used by shutdown and
// delivery coordination. Diagnosis content remains in the event stream.
type RunResult struct {
	RunID               domain.AgentRunID
	Status              domain.AgentRunStatus
	PersistenceDegraded bool
}

// Coordinator is the sole process-local Session and AgentRun orchestrator. It
// owns at most one run goroutine, its cancellation function, and its wait path.
type Coordinator struct {
	mu sync.Mutex

	sessions         SessionPersistence
	runs             RunPersistence
	tools            ToolEvidencePersistence
	audits           AuditPersistence
	scope            ActiveScope
	runner           agent.AgentRunner
	identifiers      ApplicationIdentifierSource
	auditIdentifiers AuditIdentifierSource
	questions        QuestionProcessor
	uiEvents         UIEventSink
	observer         RunObserver
	now              func() time.Time
	budgetLimits     agent.RunBudgetLimits
	persistenceLimit time.Duration

	closed              bool
	persistenceDegraded bool
	starting            bool
	startingCancel      context.CancelFunc
	startingDone        chan struct{}
	operations          int
	operationsDone      chan struct{}
	active              *activeRun
	lastResult          *RunResult
}

type activeRun struct {
	input     agent.RunInput
	run       domain.AgentRun
	cancel    context.CancelFunc
	done      chan struct{}
	bridge    *eventBridge
	tools     map[domain.ToolInvocationID]*pendingTool
	diagnosis *domain.Diagnosis

	lastAgentSequence int64
	publishing        bool
	terminal          bool
	terminalStatus    domain.AgentRunStatus
	persistenceBad    bool
}

type pendingTool struct {
	current   domain.ToolInvocation
	terminal  *domain.ToolInvocation
	evidence  []domain.Evidence
	persisted bool
	audit     auditSpec
}

type persistenceActionKind uint8

const (
	persistNone persistenceActionKind = iota
	persistToolEvidence
	persistTerminal
	appendAuditOnly
)

type persistenceAction struct {
	kind       persistenceActionKind
	invocation domain.ToolInvocation
	evidence   []domain.Evidence
	event      agent.RunEvent
	audit      auditSpec
}

type auditSpec struct {
	eventType  domain.AuditEventType
	actor      domain.AuditActor
	outcome    domain.AuditOutcome
	errorClass *domain.SafeErrorClass
	toolName   *domain.ToolName
	sequence   *int
}

// NewCoordinator validates the complete read-only runtime composition without
// performing persistence, model, Tool, Kubernetes, logging, or UI I/O.
func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	limits := config.BudgetLimits
	if limits == (agent.RunBudgetLimits{}) {
		limits = agent.DefaultRunBudgetLimits()
	}
	persistenceLimit := config.PersistenceTimeout
	if persistenceLimit == 0 {
		persistenceLimit = DefaultPersistenceTimeout
	}
	if config.Sessions == nil || config.Runs == nil || config.Tools == nil ||
		config.Audits == nil || config.Scope == nil || config.Runner == nil || config.Identifiers == nil ||
		config.AuditIdentifiers == nil || config.Questions == nil || config.UIEvents == nil || config.Observer == nil ||
		config.Now == nil || !validCoordinatorTime(config.Now()) || limits.Validate() != nil ||
		persistenceLimit <= 0 || persistenceLimit > MaxPersistenceTimeout {
		return nil, ErrCoordinatorDependency
	}
	return &Coordinator{
		sessions: config.Sessions, runs: config.Runs, tools: config.Tools,
		audits: config.Audits, scope: config.Scope,
		runner: config.Runner, identifiers: config.Identifiers,
		auditIdentifiers: config.AuditIdentifiers, questions: config.Questions,
		uiEvents: config.UIEvents, observer: config.Observer, now: config.Now,
		budgetLimits: limits, persistenceLimit: persistenceLimit,
	}, nil
}

// CreateSession persists one new shell and its fixed audit record. It never
// queries history, activates scope, or starts model or Tool I/O.
func (coordinator *Coordinator) CreateSession(ctx context.Context, command CreateSessionCommand) (domain.Session, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return domain.Session{}, ErrCoordinatorDependency
	}
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return domain.Session{}, ErrCoordinatorClosed
	}
	if coordinator.persistenceDegraded {
		coordinator.mu.Unlock()
		return domain.Session{}, ErrPersistenceUnavailable
	}
	if coordinator.starting || coordinator.active != nil {
		coordinator.mu.Unlock()
		return domain.Session{}, ErrRunAlreadyActive
	}
	if coordinator.operations != 0 {
		coordinator.mu.Unlock()
		return domain.Session{}, ErrCoordinatorBusy
	}
	if coordinator.operations == 0 {
		coordinator.operationsDone = make(chan struct{})
	}
	coordinator.operations++
	coordinator.mu.Unlock()
	defer coordinator.finishOperation()

	id, err := coordinator.identifiers.NewSessionID()
	createdAt := coordinator.now()
	if err != nil || !id.Valid() || !validCoordinatorTime(createdAt) {
		return domain.Session{}, ErrCoordinatorDependency
	}
	session := domain.Session{
		ID: id, Status: domain.SessionStatusActive, PrivacyMode: command.PrivacyMode,
		Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if session.Validate() != nil {
		return domain.Session{}, ErrCoordinatorDependency
	}
	audit, err := coordinator.newSessionAudit(session.ID, auditSpec{
		eventType: domain.AuditEventSessionCreated,
		actor:     domain.AuditActorUser,
		outcome:   domain.AuditOutcomeSuccess,
	})
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return domain.Session{}, ErrPersistenceUnavailable
	}
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.sessions.CreateWithAudit(operationContext, session, audit)
	}); err != nil {
		if !errors.Is(err, ErrPersistenceUnavailable) {
			return domain.Session{}, err
		}
		coordinator.markGlobalPersistenceDegraded()
		return domain.Session{}, ErrPersistenceUnavailable
	}
	return session, nil
}

// StartRun durably binds the safe request and running metadata before starting
// the sole owned Agent goroutine.
func (coordinator *Coordinator) StartRun(ctx context.Context, command StartRunCommand) (domain.AgentRunID, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return "", ErrCoordinatorDependency
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	processed, err := coordinator.questions.Process(command.Question, MaxQuestionBytes)
	if err != nil || processed.Value == "" || len(processed.Value) > MaxQuestionBytes {
		return "", ErrQuestionRejected
	}
	scope, current := coordinator.scope.CurrentScope()
	if !current || scope.Validate() != nil || command.Resource != nil && command.Resource.Namespace != scope.Namespace {
		return "", ErrScopeUnavailable
	}
	runID, runErr := coordinator.identifiers.NewAgentRunID()
	messageID, messageErr := coordinator.identifiers.NewMessageID()
	startedAt := coordinator.now()
	if runErr != nil || messageErr != nil || !runID.Valid() || !messageID.Valid() || !validCoordinatorTime(startedAt) {
		return "", ErrCoordinatorDependency
	}
	input, err := agent.NewRunInput(
		runID, command.SessionID, messageID, processed.Value, scope, command.Resource, coordinator.budgetLimits,
	)
	if err != nil {
		return "", ErrCoordinatorDependency
	}
	runContext, cancelRun := context.WithCancel(ctx)
	bridge, err := newEventBridge(runID, scope.Generation, coordinator.uiEvents)
	if err != nil {
		cancelRun()
		return "", ErrCoordinatorDependency
	}
	state := &activeRun{
		input: input, cancel: cancelRun, done: make(chan struct{}), bridge: bridge,
		tools: make(map[domain.ToolInvocationID]*pendingTool),
	}
	requestMessage := domain.Message{
		ID: messageID, SessionID: command.SessionID, RunID: &runID,
		Role: domain.MessageRoleUser, Content: processed.Value, Format: domain.MessageFormatPlain,
		Status: domain.MessageStatusCommitted, Scope: scopeSnapshotPointer(scope.Snapshot()),
		Resource: cloneResource(command.Resource), Hash: domain.MessageContentHash(processed.Value), CreatedAt: startedAt,
	}
	state.run = domain.AgentRun{
		ID: runID, SessionID: command.SessionID, RequestMessageID: messageID,
		Status: domain.AgentRunStatusRunning, Scope: scope.Snapshot(), Resource: cloneResource(command.Resource),
		PromptVersion: input.PromptVersion(), ToolCatalogVersion: input.CatalogVersion(), StartedAt: &startedAt,
	}
	if requestMessage.Validate() != nil || state.run.Validate() != nil {
		cancelRun()
		return "", ErrCoordinatorDependency
	}

	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		cancelRun()
		return "", ErrCoordinatorClosed
	}
	if coordinator.persistenceDegraded {
		coordinator.mu.Unlock()
		cancelRun()
		return "", ErrPersistenceUnavailable
	}
	if coordinator.starting || coordinator.active != nil {
		coordinator.mu.Unlock()
		cancelRun()
		return "", ErrRunAlreadyActive
	}
	if coordinator.operations != 0 {
		coordinator.mu.Unlock()
		cancelRun()
		return "", ErrCoordinatorBusy
	}
	coordinator.starting = true
	coordinator.startingCancel = cancelRun
	coordinator.startingDone = make(chan struct{})
	startingDone := coordinator.startingDone
	coordinator.mu.Unlock()

	bound := false
	launched := false
	defer func() {
		if launched {
			return
		}
		if bound {
			coordinator.scope.UnbindRun(runID)
		}
		cancelRun()
		coordinator.finishStarting(startingDone)
	}()
	if err := coordinator.scope.BindRun(scope, runID, cancelRun); err != nil {
		return "", ErrScopeUnavailable
	}
	bound = true
	startAudit, err := coordinator.newRunAudit(state, auditSpec{
		eventType: domain.AuditEventRunStarted,
		actor:     domain.AuditActorUser,
		outcome:   domain.AuditOutcomeSuccess,
	})
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return "", ErrPersistenceUnavailable
	}
	if err := coordinator.persist(runContext, func(operationContext context.Context) error {
		return coordinator.runs.BeginWithAudit(operationContext, requestMessage, state.run, startAudit)
	}); err != nil {
		if !errors.Is(err, ErrPersistenceUnavailable) {
			return "", err
		}
		coordinator.markGlobalPersistenceDegraded()
		return "", ErrPersistenceUnavailable
	}

	coordinator.mu.Lock()
	coordinator.active = state
	coordinator.starting = false
	coordinator.startingCancel = nil
	coordinator.startingDone = nil
	close(startingDone)
	closedDuringStart := coordinator.closed
	coordinator.mu.Unlock()
	launched = true
	if closedDuringStart {
		cancelRun()
	}
	coordinator.observe(runContext, RunObservation{
		Kind: RunObservationStarted, RunID: runID, ScopeGeneration: scope.Generation,
		Status: domain.AgentRunStatusRunning,
	})
	go coordinator.executeRun(runContext, state)
	return runID, nil
}

// Publish synchronously validates and coordinates one ordered neutral Agent
// event. Persistence failure degrades the active run; identity, ordering, or UI
// bridge failure rejects the stream.
func (coordinator *Coordinator) Publish(ctx context.Context, event agent.RunEvent) agent.EventSinkResult {
	if coordinator == nil || ctx == nil || event.Validate() != nil {
		return agent.EventSinkRejected
	}
	coordinator.mu.Lock()
	state := coordinator.active
	if state == nil || state.publishing || state.terminal || event.RunID != state.run.ID ||
		event.ScopeGeneration != state.run.Scope.Generation || event.Sequence != state.lastAgentSequence+1 ||
		state.lastAgentSequence == 0 && event.Kind != agent.RunEventRunStarted ||
		state.lastAgentSequence > 0 && event.Kind == agent.RunEventRunStarted {
		coordinator.mu.Unlock()
		return agent.EventSinkRejected
	}
	state.publishing = true
	state.lastAgentSequence = event.Sequence
	action, err := coordinator.acceptEventLocked(state, event)
	coordinator.mu.Unlock()
	if err != nil {
		coordinator.finishPublishing(state)
		return agent.EventSinkRejected
	}

	persistenceErr := coordinator.performPersistence(ctx, state, action)
	persistenceFailed := errors.Is(persistenceErr, ErrPersistenceUnavailable)
	bridgeFailed := false
	if persistenceFailed {
		bridgeFailed = coordinator.markRunPersistenceDegraded(ctx, state) != nil
	} else if persistenceErr != nil {
		bridgeFailed = true
	}
	if err := state.bridge.accept(ctx, event); err != nil {
		bridgeFailed = true
	}
	if event.Terminal() {
		coordinator.observe(ctx, RunObservation{
			Kind: RunObservationTerminal, RunID: state.run.ID,
			ScopeGeneration: state.run.Scope.Generation, Status: state.terminalStatus,
			PersistenceDegraded: state.persistenceBad,
		})
	}
	coordinator.finishPublishing(state)
	if bridgeFailed {
		return agent.EventSinkRejected
	}
	if persistenceFailed || state.persistenceBad {
		return agent.EventSinkDegraded
	}
	return agent.EventSinkAccepted
}

// CancelRun cancels only the exact active run and generation. Completion is
// observed through WaitRun or Shutdown.
func (coordinator *Coordinator) CancelRun(ctx context.Context, command CancelRunCommand) error {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return ErrRunNotActive
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active == nil || coordinator.active.terminal || coordinator.active.run.ID != command.RunID ||
		coordinator.active.run.Scope.Generation != command.ScopeGeneration {
		return ErrRunNotActive
	}
	coordinator.active.cancel()
	return nil
}

// WaitRun waits for the exact active or most recently completed run without
// owning a goroutine or channel closure.
func (coordinator *Coordinator) WaitRun(ctx context.Context, runID domain.AgentRunID) (RunResult, error) {
	if coordinator == nil || ctx == nil || !runID.Valid() {
		return RunResult{}, ErrRunNotActive
	}
	coordinator.mu.Lock()
	if coordinator.lastResult != nil && coordinator.lastResult.RunID == runID {
		result := *coordinator.lastResult
		coordinator.mu.Unlock()
		return result, nil
	}
	if coordinator.active == nil || coordinator.active.run.ID != runID {
		coordinator.mu.Unlock()
		return RunResult{}, ErrRunNotActive
	}
	done := coordinator.active.done
	coordinator.mu.Unlock()
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case <-done:
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.lastResult == nil || coordinator.lastResult.RunID != runID {
		return RunResult{}, ErrRunNotActive
	}
	return *coordinator.lastResult, nil
}

// Shutdown prevents new starts, cancels any starting or active run, and waits
// for Application-owned run work to terminate before Agent or model closure.
func (coordinator *Coordinator) Shutdown(ctx context.Context) error {
	if coordinator == nil {
		return nil
	}
	if ctx == nil {
		return ErrCoordinatorDependency
	}
	for {
		coordinator.mu.Lock()
		coordinator.closed = true
		startingCancel := coordinator.startingCancel
		startingDone := coordinator.startingDone
		state := coordinator.active
		operationsDone := coordinator.operationsDone
		coordinator.mu.Unlock()
		if startingCancel != nil {
			startingCancel()
		}
		if state != nil {
			state.cancel()
		}
		if startingDone != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-startingDone:
			}
			continue
		}
		if state == nil {
			if operationsDone == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-operationsDone:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-state.done:
		}
	}
}

func (coordinator *Coordinator) acceptEventLocked(state *activeRun, event agent.RunEvent) (persistenceAction, error) {
	switch event.Kind {
	case agent.RunEventRunStarted:
		return persistenceAction{}, nil
	case agent.RunEventModelStreamStarted:
		state.run.ModelRequestCount++
		state.run.StepCount++
		if state.run.ModelRequestCount > domain.MaxAgentModelCalls || state.run.StepCount > domain.MaxAgentSteps {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		return persistenceAction{kind: appendAuditOnly, audit: auditSpec{
			eventType: domain.AuditEventModelRequested,
			actor:     domain.AuditActorAgent, outcome: domain.AuditOutcomeSuccess,
		}}, nil
	case agent.RunEventTextDelta:
		return persistenceAction{}, nil
	case agent.RunEventToolCallRequested:
		if event.ToolInvocation.Scope != state.run.Scope {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		if _, exists := state.tools[event.ToolInvocation.ID]; exists {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		state.run.ToolCallCount++
		if state.run.ToolCallCount > domain.MaxAgentToolCalls {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		invocation := cloneInvocation(*event.ToolInvocation)
		state.tools[invocation.ID] = &pendingTool{current: invocation}
		name, sequence := invocation.Name, invocation.Sequence
		return persistenceAction{kind: appendAuditOnly, audit: auditSpec{
			eventType: domain.AuditEventToolRequested,
			actor:     domain.AuditActorAgent, outcome: domain.AuditOutcomeSuccess,
			toolName: &name, sequence: &sequence,
		}}, nil
	case agent.RunEventToolCallStarted:
		pending := state.tools[event.ToolInvocation.ID]
		if pending == nil || pending.terminal != nil || !sameInvocationIdentity(pending.current, *event.ToolInvocation) ||
			pending.current.Status != domain.ToolInvocationStatusRequested {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		pending.current = cloneInvocation(*event.ToolInvocation)
		return persistenceAction{}, nil
	case agent.RunEventToolCallCompleted, agent.RunEventToolCallFailed, agent.RunEventToolCallDenied:
		pending := state.tools[event.ToolInvocation.ID]
		if pending == nil || pending.terminal != nil || !sameInvocationIdentity(pending.current, *event.ToolInvocation) ||
			pending.current.Status != domain.ToolInvocationStatusRequested && pending.current.Status != domain.ToolInvocationStatusRunning {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		terminal := cloneInvocation(*event.ToolInvocation)
		pending.terminal = &terminal
		pending.audit = toolTerminalAudit(event)
		if terminal.EvidenceCount == 0 {
			pending.persisted = true
			return persistenceAction{
				kind: persistToolEvidence, invocation: terminal, audit: pending.audit,
			}, nil
		}
		return persistenceAction{}, nil
	case agent.RunEventEvidenceCollected:
		pending := state.tools[event.Evidence.InvocationID]
		if event.Evidence.Scope != state.run.Scope || pending == nil || pending.terminal == nil || pending.persisted ||
			len(pending.evidence) >= pending.terminal.EvidenceCount {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		for _, existing := range pending.evidence {
			if existing.ID == event.Evidence.ID {
				return persistenceAction{}, ErrInvalidAgentEvent
			}
		}
		pending.evidence = append(pending.evidence, cloneEvidence(*event.Evidence))
		if len(pending.evidence) != pending.terminal.EvidenceCount {
			return persistenceAction{}, nil
		}
		pending.persisted = true
		return persistenceAction{
			kind: persistToolEvidence, invocation: cloneInvocation(*pending.terminal),
			evidence: cloneEvidenceItems(pending.evidence), audit: pending.audit,
		}, nil
	case agent.RunEventDiagnosisReady:
		if event.Diagnosis.Scope != state.run.Scope || state.diagnosis != nil || !allToolsPersisted(state.tools) {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		diagnosis := cloneDiagnosis(*event.Diagnosis)
		state.diagnosis = &diagnosis
		return persistenceAction{}, nil
	case agent.RunEventRunCompleted, agent.RunEventRunFailed, agent.RunEventRunCancelled,
		agent.RunEventRunTimedOut, agent.RunEventRunStaleScope, agent.RunEventRunInterrupted:
		status := terminalStatus(event.Kind)
		if !status.Terminal() || event.Kind == agent.RunEventRunCompleted && state.diagnosis == nil || !allToolsPersisted(state.tools) {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		state.terminal = true
		state.terminalStatus = status
		return persistenceAction{kind: persistTerminal, event: event, audit: terminalAudit(event)}, nil
	default:
		return persistenceAction{}, ErrInvalidAgentEvent
	}
}

func (coordinator *Coordinator) performPersistence(ctx context.Context, state *activeRun, action persistenceAction) error {
	switch action.kind {
	case persistNone:
		return nil
	case persistToolEvidence:
		audit, auditErr := coordinator.newRunAudit(state, action.audit)
		if auditErr != nil {
			return ErrPersistenceUnavailable
		}
		return coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.tools.SaveWithAudit(operationContext, action.invocation, action.evidence, audit)
		})
	case persistTerminal:
		return coordinator.persistTerminal(ctx, state, action.event, action.audit)
	case appendAuditOnly:
		return coordinator.appendRunAudit(ctx, state, action.audit)
	default:
		return ErrPersistenceUnavailable
	}
}

func (coordinator *Coordinator) persistTerminal(
	ctx context.Context,
	state *activeRun,
	event agent.RunEvent,
	auditSpecification auditSpec,
) error {
	finishedAt := coordinator.now()
	if !validCoordinatorTime(finishedAt) {
		return ErrPersistenceUnavailable
	}
	if state.run.StartedAt != nil && finishedAt.Before(*state.run.StartedAt) {
		finishedAt = *state.run.StartedAt
	}
	run := state.run
	run.Status = state.terminalStatus
	run.PersistenceDegraded = state.persistenceBad
	run.FinishedAt = &finishedAt
	run.TerminationReason = terminationReason(event)
	if run.Validate() != nil {
		return ErrPersistenceUnavailable
	}
	audit, err := coordinator.newRunAudit(state, auditSpecification)
	if err != nil {
		return ErrPersistenceUnavailable
	}
	if run.Status != domain.AgentRunStatusCompleted || state.persistenceBad {
		return coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.runs.FinishWithAudit(operationContext, run, audit)
		})
	}
	messageID, err := coordinator.identifiers.NewMessageID()
	if err != nil || !messageID.Valid() || state.diagnosis == nil {
		return ErrPersistenceUnavailable
	}
	message := domain.Message{
		ID: messageID, SessionID: run.SessionID, RunID: &run.ID,
		Role: domain.MessageRoleAssistant, Content: state.diagnosis.AnswerMarkdown,
		Format: domain.MessageFormatMarkdown, Status: domain.MessageStatusCommitted,
		Scope: scopeSnapshotPointer(run.Scope), Resource: cloneResource(run.Resource),
		Hash: domain.MessageContentHash(state.diagnosis.AnswerMarkdown), CreatedAt: finishedAt,
	}
	if message.Validate() != nil {
		return ErrPersistenceUnavailable
	}
	return coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.runs.CompleteWithAudit(operationContext, *state.diagnosis, message, run, audit)
	})
}

func (coordinator *Coordinator) newSessionAudit(
	sessionID domain.SessionID,
	specification auditSpec,
) (domain.AuditEvent, error) {
	id, err := coordinator.auditIdentifiers.NewAuditEventID()
	occurredAt := coordinator.now()
	if err != nil || !id.Valid() || !validCoordinatorTime(occurredAt) {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, Type: specification.eventType,
		Actor: specification.actor, Outcome: specification.outcome, OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	return event, nil
}

func (coordinator *Coordinator) newRunAudit(
	state *activeRun,
	specification auditSpec,
) (domain.AuditEvent, error) {
	if state == nil || specification.eventType == "" {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	id, err := coordinator.auditIdentifiers.NewAuditEventID()
	occurredAt := coordinator.now()
	if err != nil || !id.Valid() || !validCoordinatorTime(occurredAt) {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	sessionID, runID, scope := state.run.SessionID, state.run.ID, state.run.Scope
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: specification.eventType,
		Actor: specification.actor, Outcome: specification.outcome, Scope: &scope,
		Subject: cloneResource(state.run.Resource), OccurredAt: occurredAt,
		Details: domain.AuditDetails{
			ErrorClass: specification.errorClass, ToolName: specification.toolName, Sequence: specification.sequence,
		},
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	return event, nil
}

func (coordinator *Coordinator) appendRunAudit(ctx context.Context, state *activeRun, specification auditSpec) error {
	event, err := coordinator.newRunAudit(state, specification)
	if err != nil {
		return err
	}
	return coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.audits.Append(operationContext, event)
	})
}

func (coordinator *Coordinator) markRunPersistenceDegraded(ctx context.Context, state *activeRun) error {
	coordinator.mu.Lock()
	first := !state.persistenceBad
	state.persistenceBad = true
	state.run.PersistenceDegraded = true
	coordinator.persistenceDegraded = true
	coordinator.mu.Unlock()
	if !first {
		return nil
	}
	coordinator.observe(ctx, RunObservation{
		Kind: RunObservationPersistenceDegraded, RunID: state.run.ID,
		ScopeGeneration: state.run.Scope.Generation, Status: domain.AgentRunStatusRunning,
		PersistenceDegraded: true,
	})
	_ = coordinator.appendRunAudit(ctx, state, auditSpec{
		eventType: domain.AuditEventPersistenceDegraded,
		actor:     domain.AuditActorSystem, outcome: domain.AuditOutcomeFailure,
	})
	return state.bridge.persistenceDegraded(ctx)
}

func (coordinator *Coordinator) markGlobalPersistenceDegraded() {
	coordinator.mu.Lock()
	coordinator.persistenceDegraded = true
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) executeRun(ctx context.Context, state *activeRun) {
	outcome := coordinator.runner.Run(ctx, state.input, coordinator)
	coordinator.mu.Lock()
	terminal := state.terminal
	terminalStatus := state.terminalStatus
	coordinator.mu.Unlock()
	if !terminal {
		coordinator.forceFailedTerminal(ctx, state)
	} else if outcome.Validate(state.input) != nil || outcome.Status != terminalStatus {
		// The accepted terminal event remains authoritative. No second terminal
		// or external operation is permitted for a mismatched adapter outcome.
	}
	coordinator.scope.UnbindRun(state.run.ID)
	coordinator.mu.Lock()
	result := RunResult{
		RunID: state.run.ID, Status: state.terminalStatus,
		PersistenceDegraded: state.persistenceBad,
	}
	coordinator.lastResult = &result
	if coordinator.active == state {
		coordinator.active = nil
	}
	close(state.done)
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) forceFailedTerminal(ctx context.Context, state *activeRun) {
	coordinator.mu.Lock()
	if state.terminal {
		coordinator.mu.Unlock()
		return
	}
	state.terminal = true
	state.terminalStatus = domain.AgentRunStatusFailed
	coordinator.mu.Unlock()
	class := domain.SafeErrorClassInternal
	event := agent.RunEvent{
		Kind:    agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The AgentRun failed safely."},
	}
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), coordinator.persistenceLimit)
	defer cancel()
	if err := coordinator.persistTerminal(terminalContext, state, event, terminalAudit(event)); err != nil {
		_ = coordinator.markRunPersistenceDegraded(terminalContext, state)
	}
	_ = state.bridge.forceFailed(terminalContext, "The AgentRun failed safely.")
	coordinator.observe(terminalContext, RunObservation{
		Kind: RunObservationTerminal, RunID: state.run.ID,
		ScopeGeneration: state.run.Scope.Generation, Status: domain.AgentRunStatusFailed,
		PersistenceDegraded: state.persistenceBad,
	})
}

func (coordinator *Coordinator) finishStarting(done chan struct{}) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.startingDone != done {
		return
	}
	coordinator.starting = false
	coordinator.startingCancel = nil
	coordinator.startingDone = nil
	close(done)
}

func (coordinator *Coordinator) finishOperation() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.operations < 1 {
		return
	}
	coordinator.operations--
	if coordinator.operations == 0 && coordinator.operationsDone != nil {
		close(coordinator.operationsDone)
		coordinator.operationsDone = nil
	}
}

func (coordinator *Coordinator) finishPublishing(state *activeRun) {
	coordinator.mu.Lock()
	state.publishing = false
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) persist(ctx context.Context, operation func(context.Context) error) error {
	if ctx == nil || operation == nil {
		return ErrPersistenceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, coordinator.persistenceLimit)
	defer cancel()
	if err := operation(operationContext); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}
	return nil
}

func (coordinator *Coordinator) observe(ctx context.Context, observation RunObservation) {
	if observation.valid() {
		coordinator.observer.ObserveRun(ctx, observation)
	}
}

func terminalStatus(kind agent.RunEventKind) domain.AgentRunStatus {
	switch kind {
	case agent.RunEventRunCompleted:
		return domain.AgentRunStatusCompleted
	case agent.RunEventRunFailed:
		return domain.AgentRunStatusFailed
	case agent.RunEventRunCancelled:
		return domain.AgentRunStatusCancelled
	case agent.RunEventRunTimedOut:
		return domain.AgentRunStatusTimedOut
	case agent.RunEventRunStaleScope:
		return domain.AgentRunStatusStaleScope
	case agent.RunEventRunInterrupted:
		return domain.AgentRunStatusInterrupted
	default:
		return ""
	}
}

func terminalAudit(event agent.RunEvent) auditSpec {
	specification := auditSpec{actor: domain.AuditActorAgent, outcome: domain.AuditOutcomeFailure}
	switch event.Kind {
	case agent.RunEventRunCompleted:
		specification.eventType = domain.AuditEventRunCompleted
		specification.outcome = domain.AuditOutcomeSuccess
	case agent.RunEventRunFailed:
		specification.eventType = domain.AuditEventRunFailed
		if event.Failure != nil {
			class := event.Failure.Class
			specification.errorClass = &class
		}
	case agent.RunEventRunCancelled:
		specification.eventType = domain.AuditEventRunCancelled
	case agent.RunEventRunTimedOut:
		specification.eventType = domain.AuditEventRunTimedOut
	case agent.RunEventRunStaleScope:
		specification.eventType = domain.AuditEventRunStaleScope
	case agent.RunEventRunInterrupted:
		specification.eventType = domain.AuditEventRunInterrupted
	}
	return specification
}

func toolTerminalAudit(event agent.RunEvent) auditSpec {
	invocation := event.ToolInvocation
	name, sequence := invocation.Name, invocation.Sequence
	specification := auditSpec{
		eventType: domain.AuditEventToolCompleted, actor: domain.AuditActorAgent,
		outcome: domain.AuditOutcomeSuccess, toolName: &name, sequence: &sequence,
	}
	if event.Kind == agent.RunEventToolCallDenied {
		specification.eventType = domain.AuditEventToolDenied
		specification.outcome = domain.AuditOutcomeDenied
	}
	if event.Kind == agent.RunEventToolCallFailed {
		specification.outcome = domain.AuditOutcomeFailure
	}
	if invocation.ErrorClass != nil {
		class := *invocation.ErrorClass
		specification.errorClass = &class
	}
	return specification
}

func terminationReason(event agent.RunEvent) *string {
	if event.Kind == agent.RunEventRunCompleted {
		return nil
	}
	value := string(event.TerminationReason)
	if event.Kind == agent.RunEventRunFailed {
		value = "agent_run_failed"
	}
	return &value
}

func allToolsPersisted(values map[domain.ToolInvocationID]*pendingTool) bool {
	for _, value := range values {
		if value == nil || value.terminal == nil || !value.persisted {
			return false
		}
	}
	return true
}

func sameInvocationIdentity(left, right domain.ToolInvocation) bool {
	return left.ID == right.ID && left.RunID == right.RunID && left.Sequence == right.Sequence &&
		left.Name == right.Name && left.Version == right.Version && optionalStringEqual(left.Purpose, right.Purpose) &&
		left.Scope == right.Scope && left.ArgumentsJSON == right.ArgumentsJSON && left.ArgumentsDigest == right.ArgumentsDigest &&
		optionalTimeEqual(left.StartedAt, right.StartedAt)
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func scopeSnapshotPointer(value domain.ScopeSnapshot) *domain.ScopeSnapshot {
	copy := value
	return &copy
}

func cloneResource(value *domain.ResourceRef) *domain.ResourceRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInvocation(value domain.ToolInvocation) domain.ToolInvocation {
	copy := value
	if value.Purpose != nil {
		current := *value.Purpose
		copy.Purpose = &current
	}
	if value.ErrorClass != nil {
		current := *value.ErrorClass
		copy.ErrorClass = &current
	}
	if value.SafeError != nil {
		current := *value.SafeError
		copy.SafeError = &current
	}
	if value.ResultSummary != nil {
		current := *value.ResultSummary
		copy.ResultSummary = &current
	}
	if value.StartedAt != nil {
		current := *value.StartedAt
		copy.StartedAt = &current
	}
	if value.FinishedAt != nil {
		current := *value.FinishedAt
		copy.FinishedAt = &current
	}
	return copy
}

func cloneEvidence(value domain.Evidence) domain.Evidence {
	copy := value
	if value.SourcePath != nil {
		current := *value.SourcePath
		copy.SourcePath = &current
	}
	if value.Severity != nil {
		current := *value.Severity
		copy.Severity = &current
	}
	return copy
}

func cloneEvidenceItems(values []domain.Evidence) []domain.Evidence {
	result := make([]domain.Evidence, len(values))
	for index := range values {
		result[index] = cloneEvidence(values[index])
	}
	return result
}

func cloneDiagnosis(value domain.Diagnosis) domain.Diagnosis {
	copy := value
	copy.ConfirmedFacts = append([]domain.ConfirmedFact(nil), value.ConfirmedFacts...)
	for index := range copy.ConfirmedFacts {
		copy.ConfirmedFacts[index].EvidenceIDs = append([]domain.EvidenceID(nil), value.ConfirmedFacts[index].EvidenceIDs...)
	}
	copy.Hypotheses = append([]domain.Hypothesis(nil), value.Hypotheses...)
	for index := range copy.Hypotheses {
		copy.Hypotheses[index].SupportingEvidenceIDs = append([]domain.EvidenceID(nil), value.Hypotheses[index].SupportingEvidenceIDs...)
	}
	copy.MissingInformation = append([]domain.MissingInformation(nil), value.MissingInformation...)
	copy.RecommendedActions = append([]domain.RecommendedAction(nil), value.RecommendedActions...)
	for index := range copy.RecommendedActions {
		copy.RecommendedActions[index].Prerequisites = append([]string(nil), value.RecommendedActions[index].Prerequisites...)
	}
	copy.ValidationWarnings = append([]string(nil), value.ValidationWarnings...)
	if value.ObservedFrom != nil {
		current := *value.ObservedFrom
		copy.ObservedFrom = &current
	}
	if value.ObservedTo != nil {
		current := *value.ObservedTo
		copy.ObservedTo = &current
	}
	return copy
}

func validCoordinatorTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Nanosecond()%int(time.Millisecond) == 0
}
