package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	Privacy            *PrivacyManager
	UIEvents           UIEventSink
	Observer           RunObserver
	Now                func() time.Time
	BudgetLimits       agent.RunBudgetLimits
	PersistenceTimeout time.Duration
	UI                 *CoordinatorUIConfig
}

// CoordinatorUIConfig supplies the existing Session and startup consumers
// needed by CLI/TUI composition. Core run tests may omit it when no delivery
// use case is exercised.
type CoordinatorUIConfig struct {
	Sessions  SessionResumeStore
	Titles    SessionTitleStore
	Startup   StartupMaintenance
	Scopes    *ScopeManager
	Approvals *ApprovalCoordinator
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
	mu        sync.Mutex
	startupMu sync.Mutex

	sessions         SessionPersistence
	runs             RunPersistence
	tools            ToolEvidencePersistence
	audits           AuditPersistence
	scope            ActiveScope
	runner           agent.AgentRunner
	identifiers      ApplicationIdentifierSource
	auditIdentifiers AuditIdentifierSource
	questions        QuestionProcessor
	privacy          *PrivacyManager
	uiEvents         UIEventSink
	observer         RunObserver
	now              func() time.Time
	budgetLimits     agent.RunBudgetLimits
	persistenceLimit time.Duration
	resumeSessions   SessionResumeStore
	titles           SessionTitleStore
	startup          StartupMaintenance
	uiScopes         *ScopeManager
	approvals        *ApprovalCoordinator
	startupPrepared  bool
	currentSession   *domain.Session
	currentResumed   bool
	pendingResume    *pendingResume
	startupResume    *startupResumeState
	privacyChallenge *privacyChallenge

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

type pendingResume struct {
	request          UIResumeRequest
	record           ResumedSessionRecord
	projection       UIResumedSession
	explicitScope    bool
	currentCandidate *domain.ScopeCandidate
}

type startupResumeState struct {
	intent           UIStartIntent
	currentCandidate *domain.ScopeCandidate
}

type privacyChallenge struct {
	requestID uint64
	revision  string
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
		config.Privacy == nil || config.Now == nil || !validCoordinatorTime(config.Now()) || limits.Validate() != nil ||
		persistenceLimit <= 0 || persistenceLimit > MaxPersistenceTimeout {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && (config.UI.Sessions == nil || config.UI.Titles == nil || config.UI.Startup == nil) {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && config.UI.Approvals != nil && config.UI.Scopes == nil {
		return nil, ErrCoordinatorDependency
	}
	var resumeSessions SessionResumeStore
	var titles SessionTitleStore
	var startup StartupMaintenance
	var uiScopes *ScopeManager
	var approvals *ApprovalCoordinator
	if config.UI != nil {
		resumeSessions = config.UI.Sessions
		titles = config.UI.Titles
		startup = config.UI.Startup
		uiScopes = config.UI.Scopes
		approvals = config.UI.Approvals
		if approvals != nil && uiScopes.BindApprovalInvalidationHook(approvals) != nil {
			return nil, ErrCoordinatorDependency
		}
	}
	return &Coordinator{
		sessions: config.Sessions, runs: config.Runs, tools: config.Tools,
		audits: config.Audits, scope: config.Scope,
		runner: config.Runner, identifiers: config.Identifiers,
		auditIdentifiers: config.AuditIdentifiers, questions: config.Questions,
		privacy:  config.Privacy,
		uiEvents: config.UIEvents, observer: config.Observer, now: config.Now,
		budgetLimits: limits, persistenceLimit: persistenceLimit,
		resumeSessions: resumeSessions, titles: titles, startup: startup,
		uiScopes:  uiScopes,
		approvals: approvals,
	}, nil
}

// StartUI performs mandatory startup maintenance and applies exactly one fixed
// Session start intent. Resume intents remain queries until delivery emits the
// corresponding explicit request.
func (coordinator *Coordinator) StartUI(
	ctx context.Context,
	intent UIStartIntent,
	privacyMode domain.PrivacyMode,
) (UIStartResult, error) {
	if coordinator == nil || ctx == nil || intent.Validate() != nil ||
		(privacyMode != domain.PrivacyModeStandard && privacyMode != domain.PrivacyModeMinimal) {
		return UIStartResult{}, ErrCoordinatorDependency
	}
	if err := coordinator.prepareStartup(ctx); err != nil {
		return UIStartResult{}, err
	}
	result := UIStartResult{Intent: intent}
	var currentCandidate *domain.ScopeCandidate
	if intent.Kind != UIStartNew && !intent.ExplicitScope && coordinator.uiScopes != nil {
		candidate, resolveErr := coordinator.resolveStartupScopeCandidate(ctx, intent)
		if resolveErr == nil {
			currentCandidate = &candidate
			result.ScopeCandidate = cloneScopeCandidate(currentCandidate)
		} else if contextErr := ctx.Err(); contextErr != nil {
			return UIStartResult{}, contextErr
		}
	}
	coordinator.mu.Lock()
	if intent.Kind == UIStartResumePicker || intent.Kind == UIStartResumeID || intent.Kind == UIStartResumeLast {
		coordinator.startupResume = &startupResumeState{
			intent: intent, currentCandidate: cloneScopeCandidate(currentCandidate),
		}
	} else {
		coordinator.startupResume = nil
	}
	coordinator.mu.Unlock()
	if intent.Kind == UIStartNew {
		session, err := coordinator.CreateSession(ctx, CreateSessionCommand{PrivacyMode: privacyMode})
		if err != nil {
			return UIStartResult{}, err
		}
		result.Session = &UISessionState{
			ID: session.ID, Title: session.Title, PrivacyMode: session.PrivacyMode,
		}
	}
	if result.Validate() != nil {
		return UIStartResult{}, ErrCoordinatorDependency
	}
	return result, nil
}

func (coordinator *Coordinator) resolveStartupScopeCandidate(
	ctx context.Context,
	intent UIStartIntent,
) (domain.ScopeCandidate, error) {
	candidates, err := coordinator.uiScopes.ListContexts(ctx)
	if err != nil {
		return domain.ScopeCandidate{}, err
	}
	contextName := intent.ConfiguredContext
	var selected *ContextCandidate
	for index := range candidates {
		candidate := candidates[index]
		if (contextName != "" && candidate.Name == contextName) || (contextName == "" && candidate.Current) {
			selected = &candidate
			break
		}
	}
	if selected == nil {
		return domain.ScopeCandidate{}, ErrScopeUnavailable
	}
	namespace := intent.ConfiguredNamespace
	if namespace == "" {
		namespace = selected.DefaultNamespace
	}
	result := domain.ScopeCandidate{Context: selected.Name, Namespace: namespace}
	if result.Validate() != nil || !domain.ValidContextName(result.Context) || !domain.ValidNamespaceName(result.Namespace) {
		return domain.ScopeCandidate{}, ErrScopeUnavailable
	}
	return result, nil
}

func (coordinator *Coordinator) prepareStartup(ctx context.Context) error {
	if coordinator.startup == nil {
		return ErrCoordinatorDependency
	}
	coordinator.startupMu.Lock()
	defer coordinator.startupMu.Unlock()
	if coordinator.startupPrepared {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := coordinator.now()
	if !validCoordinatorTime(now) {
		return ErrCoordinatorDependency
	}
	if coordinator.approvals != nil {
		if err := coordinator.approvals.Recover(ctx); err != nil {
			coordinator.markGlobalPersistenceDegraded()
			return fmt.Errorf("%w: approval recovery failed", ErrPersistenceUnavailable)
		}
	}
	if err := coordinator.startup.RecoverInterrupted(ctx, now); err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return fmt.Errorf("%w: startup recovery failed", ErrPersistenceUnavailable)
	}
	if err := coordinator.startup.CleanupRetention(ctx, now); err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return fmt.Errorf("%w: startup retention failed", ErrPersistenceUnavailable)
	}
	coordinator.startupPrepared = true
	return nil
}

// QueryUI executes one bounded typed completion query. Session history is read
// only for the explicit Session completion kind.
func (coordinator *Coordinator) QueryUI(ctx context.Context, query UICompletionQuery) (UICompletionResult, error) {
	if coordinator == nil || ctx == nil || query.Validate() != nil {
		return UICompletionResult{}, ErrInvalidUIQuery
	}
	if err := ctx.Err(); err != nil {
		return UICompletionResult{}, err
	}
	if query.Kind == UICompletionSession {
		if err := coordinator.beginUIOperation(true); err != nil {
			return UICompletionResult{}, err
		}
		defer coordinator.finishOperation()
		if err := coordinator.prepareStartup(ctx); err != nil {
			return UICompletionResult{}, err
		}
	}
	result := UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
	}
	switch query.Kind {
	case UICompletionSession:
		if coordinator.resumeSessions == nil {
			result.Failure = UIQueryUnavailable
			break
		}
		records, err := coordinator.resumeSessions.ListResumable(ctx, query.Limit)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return UICompletionResult{}, contextErr
			}
			result.Failure = UIQueryUnavailable
			break
		}
		result.Sessions = make([]UISessionCandidate, 0, min(len(records), query.Limit))
		for _, record := range records {
			if !record.valid() {
				result.Sessions = nil
				result.Failure = UIQueryUnavailable
				break
			}
			if !sessionRecordMatches(record, query.Filter) {
				continue
			}
			result.Sessions = append(result.Sessions, projectSessionCandidate(record))
			if len(result.Sessions) == query.Limit {
				break
			}
		}
	case UICompletionContext:
		if coordinator.uiScopes == nil {
			result.Failure = UIQueryUnavailable
			break
		}
		candidates, err := coordinator.uiScopes.ListContexts(ctx)
		if err != nil {
			result.Failure = uiFailureCode(err)
			break
		}
		active := coordinator.uiScopes.View()
		result.Contexts = make([]UIContextCandidate, 0, min(len(candidates), query.Limit))
		for _, candidate := range candidates {
			if !uiTextMatches(candidate.Name, query.Filter) {
				continue
			}
			current := candidate.Current
			if active.Scope != nil {
				current = active.Scope.Context == candidate.Name
			}
			result.Contexts = append(result.Contexts, UIContextCandidate{Name: candidate.Name, Current: current})
			if len(result.Contexts) == query.Limit {
				break
			}
		}
	case UICompletionNamespace:
		if coordinator.uiScopes == nil {
			result.Failure = UIQueryUnavailable
			break
		}
		view := coordinator.uiScopes.View()
		if view.State != ScopeStateActive || view.Scope == nil || view.Generation != query.ScopeGeneration {
			result.Failure = UIQueryUnavailable
			break
		}
		list, err := coordinator.uiScopes.ListNamespaces(ctx, *view.Scope, query.Limit)
		if err != nil {
			result.Failure = uiFailureCode(err)
			break
		}
		items := append([]domain.NamespaceSummary(nil), list.Items...)
		if len(items) > query.Limit {
			result.Failure = UIQueryUnavailable
			break
		}
		sort.Slice(items, func(left, right int) bool { return items[left].Name < items[right].Name })
		for _, item := range items {
			if uiTextMatches(item.Name, query.Filter) {
				result.Namespaces = append(result.Namespaces, UINamespaceCandidate{Name: item.Name})
				if len(result.Namespaces) == query.Limit {
					break
				}
			}
		}
	case UICompletionResource:
		if coordinator.uiScopes == nil {
			result.Failure = UIQueryUnavailable
			break
		}
		view := coordinator.uiScopes.View()
		if view.State != ScopeStateActive || view.Scope == nil || view.Generation != query.ScopeGeneration {
			result.Failure = UIQueryUnavailable
			break
		}
		kinds := []domain.ResourceKind{query.ResourceKind}
		if query.ResourceKind == "" {
			kinds = []domain.ResourceKind{
				domain.ResourceKindPod, domain.ResourceKindDeployment, domain.ResourceKindReplicaSet,
				domain.ResourceKindJob, domain.ResourceKindService,
			}
		}
		readCount := 0
		for _, kind := range kinds {
			remaining := query.Limit - readCount
			if remaining == 0 {
				break
			}
			list, err := coordinator.uiScopes.ListResources(ctx, *view.Scope, kind, remaining)
			if err != nil {
				result.Resources = nil
				result.Failure = uiFailureCode(err)
				break
			}
			if len(list.Items) > remaining {
				result.Resources = nil
				result.Failure = UIQueryUnavailable
				break
			}
			readCount += len(list.Items)
			for _, item := range list.Items {
				candidate := projectUIResourceSummary(item)
				if uiResourceMatches(candidate, query.Filter) {
					result.Resources = append(result.Resources, candidate)
				}
			}
		}
		sort.Slice(result.Resources, func(left, right int) bool {
			if result.Resources[left].Kind == result.Resources[right].Kind {
				return result.Resources[left].Name < result.Resources[right].Name
			}
			return result.Resources[left].Kind < result.Resources[right].Kind
		})
		if len(result.Resources) > query.Limit {
			result.Resources = result.Resources[:query.Limit]
		}
	}
	if result.Validate() != nil {
		return UICompletionResult{}, ErrInvalidUIQueryResult
	}
	return result, nil
}

// ResumeUI reconstructs safe history into a request-bound pending candidate.
// It performs no model, Tool, Kubernetes, scope, or current-Session action.
func (coordinator *Coordinator) ResumeUI(ctx context.Context, request UIResumeRequest) (UIResumeResult, error) {
	if coordinator == nil || ctx == nil || request.Validate() != nil || coordinator.resumeSessions == nil {
		return UIResumeResult{}, ErrInvalidUIQuery
	}
	if err := ctx.Err(); err != nil {
		return UIResumeResult{}, err
	}
	if err := coordinator.beginUIOperation(true); err != nil {
		return UIResumeResult{}, err
	}
	defer coordinator.finishOperation()
	if err := coordinator.prepareStartup(ctx); err != nil {
		return UIResumeResult{}, err
	}
	coordinator.mu.Lock()
	explicitScope := false
	var currentCandidate *domain.ScopeCandidate
	if coordinator.startupResume != nil {
		if !resumeRequestMatchesStart(request, coordinator.startupResume.intent) {
			coordinator.mu.Unlock()
			return UIResumeResult{}, ErrInvalidUIQuery
		}
		explicitScope = coordinator.startupResume.intent.ExplicitScope
		currentCandidate = cloneScopeCandidate(coordinator.startupResume.currentCandidate)
		coordinator.startupResume = nil
	}
	coordinator.mu.Unlock()

	var (
		record ResumedSessionRecord
		err    error
	)
	if request.Mode == UIResumeExact {
		record, err = coordinator.resumeSessions.ResumeByID(ctx, request.SessionID)
	} else {
		record, err = coordinator.resumeSessions.ResumeLatest(ctx)
	}
	result := UIResumeResult{RequestID: request.RequestID, Mode: request.Mode}
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return UIResumeResult{}, contextErr
		}
		switch {
		case errors.Is(err, ErrSessionNotResumable):
			result.Failure = UIQueryNotResumable
		case errors.Is(err, ErrSessionResumeUnavailable), errors.Is(err, ErrNoResumableSession):
			result.Failure = UIQueryUnavailable
		default:
			result.Failure = UIQueryUnavailable
		}
		return result, nil
	}
	if !record.valid() || request.Mode == UIResumeExact && record.Session.ID != request.SessionID {
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	projection := projectResumedSession(request.RequestID, record)
	result.Session = &projection
	if result.Validate() != nil {
		return UIResumeResult{}, ErrInvalidUIQueryResult
	}
	coordinator.mu.Lock()
	if coordinator.closed || coordinator.starting || coordinator.active != nil {
		coordinator.mu.Unlock()
		return UIResumeResult{}, ErrCoordinatorBusy
	}
	if coordinator.pendingResume == nil || request.RequestID > coordinator.pendingResume.request.RequestID {
		coordinator.pendingResume = &pendingResume{
			request: request, record: record, projection: projection,
			explicitScope: explicitScope, currentCandidate: currentCandidate,
		}
	}
	coordinator.mu.Unlock()
	return result, nil
}

// CurrentUISession returns a defensive delivery projection. A pending resume
// is intentionally excluded until the user accepts it.
func (coordinator *Coordinator) CurrentUISession() *UISessionState {
	if coordinator == nil {
		return nil
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.currentSession == nil {
		return nil
	}
	return &UISessionState{
		ID: coordinator.currentSession.ID, Title: coordinator.currentSession.Title,
		PrivacyMode: coordinator.currentSession.PrivacyMode, Resumed: coordinator.currentResumed,
	}
}

// ExecuteUICommand coordinates one fixed delivery intent. Scope and resume
// mutations remain request- and generation-bound. Question transfer stays
// fail-closed until the separate privacy flow admits it.
func (coordinator *Coordinator) ExecuteUICommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return UICommandOutcome{}, ErrInvalidUICommand
	}
	if err := ctx.Err(); err != nil {
		return UICommandOutcome{}, err
	}
	switch command.Kind {
	case UICommandNewSession:
		session, err := coordinator.CreateSession(ctx, CreateSessionCommand{PrivacyMode: coordinator.currentPrivacyMode()})
		if err != nil {
			return UICommandOutcome{}, err
		}
		if coordinator.uiScopes != nil {
			if scope, current := coordinator.uiScopes.CurrentScope(); current {
				clearScopeResource(coordinator.uiScopes, scope)
			}
		}
		return UICommandOutcome{Command: command.Kind, Session: projectUISession(session, false)}, nil
	case UICommandCancelRun:
		err := coordinator.CancelRun(ctx, CancelRunCommand{RunID: command.RunID, ScopeGeneration: command.ExpectedScopeGeneration})
		return UICommandOutcome{Command: command.Kind, RunID: command.RunID}, err
	case UICommandSubmitQuestion:
		return coordinator.executeQuestionCommand(ctx, command)
	case UICommandApproveRestart, UICommandRejectRestart, UICommandExpireRestart:
		if coordinator.approvals == nil {
			return UICommandOutcome{}, ErrApprovalUnavailable
		}
		var (
			result UIApprovalResult
			err    error
		)
		if command.Kind == UICommandExpireRestart {
			result, err = coordinator.approvals.ExpireCommand(ctx, command)
		} else {
			result, err = coordinator.approvals.Decide(ctx, command)
		}
		if err != nil && !errors.Is(err, ErrApprovalExpired) && !errors.Is(err, ErrApprovalInvalidated) {
			return UICommandOutcome{}, err
		}
		return UICommandOutcome{
			Command: command.Kind, RequestID: command.RequestID,
			Approval: &result, RunID: result.RunID,
		}, nil
	}

	requireIdle := command.Kind == UICommandAcceptResume
	if err := coordinator.beginUIOperation(requireIdle); err != nil {
		return UICommandOutcome{}, err
	}
	defer coordinator.finishOperation()

	switch command.Kind {
	case UICommandAcceptResume:
		return coordinator.executeResumeAcceptance(ctx, command), nil
	case UICommandCancelResume:
		return coordinator.executeResumeCancellation(command), nil
	case UICommandSelectContext, UICommandSelectNamespace, UICommandActivateScope:
		result := coordinator.executeScopeCommand(ctx, command)
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Scope: &result}, nil
	case UICommandSelectResource:
		result := coordinator.executeResourceCommand(command)
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Resource: &result}, nil
	case UICommandRenameSession:
		return coordinator.executeRenameCommand(ctx, command)
	case UICommandShowStatus:
		status := coordinator.uiStatus()
		return UICommandOutcome{Command: command.Kind, Status: &status}, nil
	case UICommandShowPrivacy, UICommandAcceptPrivacy, UICommandRejectPrivacy,
		UICommandRevokePrivacy, UICommandToggleLogs, UICommandCancelPrivacy:
		return coordinator.executePrivacyCommand(ctx, command)
	case UICommandResumeSession:
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
	default:
		return UICommandOutcome{}, ErrInvalidUICommand
	}
}

func (coordinator *Coordinator) executeQuestionCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	coordinator.mu.Lock()
	var sessionID domain.SessionID
	if coordinator.currentSession != nil {
		sessionID = coordinator.currentSession.ID
	}
	coordinator.mu.Unlock()
	if !sessionID.Valid() {
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
	}
	scope, current := coordinator.scope.CurrentScope()
	if !current || scope.Generation != command.ExpectedScopeGeneration {
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
	}
	var resource *domain.ResourceRef
	if coordinator.uiScopes != nil {
		selected, err := coordinator.uiScopes.SelectedResource(scope)
		if err != nil {
			return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
		}
		resource = selected
	}
	runID, err := coordinator.StartRun(ctx, StartRunCommand{SessionID: sessionID, Question: command.Text, Resource: resource})
	if errors.Is(err, ErrConsentRequired) {
		review, reviewErr := coordinator.openPrivacyChallenge(ctx, command.RequestID)
		if reviewErr != nil {
			return UICommandOutcome{}, reviewErr
		}
		return UICommandOutcome{
			Command: command.Kind, RequestID: command.RequestID,
			Failure: UIQueryConsentRequired, Privacy: &review,
		}, nil
	}
	if errors.Is(err, ErrScopeUnavailable) || errors.Is(err, ErrRunAlreadyActive) || errors.Is(err, ErrCoordinatorBusy) {
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
	}
	if err != nil {
		return UICommandOutcome{}, err
	}
	return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, RunID: runID}, nil
}

func (coordinator *Coordinator) executePrivacyCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	if command.Kind == UICommandShowPrivacy {
		review, err := coordinator.openPrivacyChallenge(ctx, command.RequestID)
		if err != nil {
			return UICommandOutcome{}, err
		}
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Privacy: &review}, nil
	}
	coordinator.mu.Lock()
	challenge := coordinator.privacyChallenge
	if challenge == nil || challenge.requestID != command.RequestID || challenge.revision != command.PrivacyRevision {
		coordinator.mu.Unlock()
		return UICommandOutcome{}, ErrPrivacyReviewStale
	}
	coordinator.privacyChallenge = nil
	var cancel context.CancelFunc
	if command.Kind == UICommandRejectPrivacy || command.Kind == UICommandRevokePrivacy || command.Kind == UICommandToggleLogs {
		if coordinator.active != nil {
			cancel = coordinator.active.cancel
		} else if coordinator.startingCancel != nil {
			cancel = coordinator.startingCancel
		}
	}
	coordinator.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if command.Kind == UICommandCancelPrivacy {
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}, nil
	}
	action := PrivacyActionAccept
	switch command.Kind {
	case UICommandAcceptPrivacy:
		action = PrivacyActionAccept
	case UICommandRejectPrivacy:
		action = PrivacyActionReject
	case UICommandRevokePrivacy:
		action = PrivacyActionRevoke
	case UICommandToggleLogs:
		action = PrivacyActionToggleLogs
	default:
		return UICommandOutcome{}, ErrInvalidUICommand
	}
	review, err := coordinator.privacy.Decide(ctx, action, command.PrivacyRevision, command.LogsEnabled)
	if err != nil {
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		coordinator.cancelActiveForPrivacyFailure()
		return UICommandOutcome{}, err
	}
	result := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	if command.Kind == UICommandToggleLogs {
		coordinator.mu.Lock()
		coordinator.privacyChallenge = &privacyChallenge{requestID: command.RequestID, revision: review.Revision}
		coordinator.mu.Unlock()
		result.Privacy = &review
	}
	return result, nil
}

func (coordinator *Coordinator) openPrivacyChallenge(ctx context.Context, requestID uint64) (PrivacyReview, error) {
	review, err := coordinator.privacy.Review(ctx)
	if err != nil {
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		return PrivacyReview{}, err
	}
	coordinator.mu.Lock()
	coordinator.privacyChallenge = &privacyChallenge{requestID: requestID, revision: review.Revision}
	coordinator.mu.Unlock()
	return review, nil
}

func (coordinator *Coordinator) cancelActiveForPrivacyFailure() {
	coordinator.mu.Lock()
	cancel := coordinator.startingCancel
	if coordinator.active != nil {
		cancel = coordinator.active.cancel
	}
	coordinator.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (coordinator *Coordinator) beginUIOperation(requireIdle bool) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return ErrCoordinatorClosed
	}
	if requireIdle && (coordinator.starting || coordinator.active != nil) {
		return ErrRunAlreadyActive
	}
	if coordinator.operations != 0 {
		return ErrCoordinatorBusy
	}
	coordinator.operationsDone = make(chan struct{})
	coordinator.operations++
	return nil
}

func (coordinator *Coordinator) currentPrivacyMode() domain.PrivacyMode {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.currentSession != nil && coordinator.currentSession.PrivacyMode == domain.PrivacyModeMinimal {
		return domain.PrivacyModeMinimal
	}
	return domain.PrivacyModeStandard
}

func projectUISession(session domain.Session, resumed bool) *UISessionState {
	return &UISessionState{ID: session.ID, Title: session.Title, PrivacyMode: session.PrivacyMode, Resumed: resumed}
}

func (coordinator *Coordinator) executeResumeCancellation(command UICommand) UICommandOutcome {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.pendingResume != nil && coordinator.pendingResume.request.RequestID == command.RequestID {
		coordinator.pendingResume = nil
	}
	return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
}

func (coordinator *Coordinator) executeResumeAcceptance(ctx context.Context, command UICommand) UICommandOutcome {
	outcome := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	coordinator.mu.Lock()
	pending := coordinator.pendingResume
	if pending == nil || pending.request.RequestID != command.RequestID {
		coordinator.mu.Unlock()
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	record := pending.record
	projection := pending.projection
	explicitScope := pending.explicitScope
	currentCandidate := cloneScopeCandidate(pending.currentCandidate)
	coordinator.mu.Unlock()

	if coordinator.uiScopes == nil {
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	view := coordinator.uiScopes.View()
	if view.Generation != command.ExpectedScopeGeneration {
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	savedScope, savedResource := resumeSavedState(record)
	if explicitScope && command.Scope != nil {
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	if command.Scope != nil {
		matchesSaved := savedScope != nil && *command.Scope == *savedScope
		matchesCurrent := currentCandidate != nil && *command.Scope == *currentCandidate
		if !matchesSaved && !matchesCurrent {
			outcome.Failure = UIQueryUnavailable
			return outcome
		}
	}

	scopeResult := coordinator.resumeScopeResult(ctx, command)
	outcome.Scope = &scopeResult
	liveView := coordinator.uiScopes.View()
	if liveView.State == ScopeStateActive && liveView.Scope != nil {
		clearScopeResource(coordinator.uiScopes, *liveView.Scope)
		resourceResult := coordinator.revalidateResumedResource(ctx, command.RequestID, *liveView.Scope, savedScope, savedResource)
		outcome.Resource = &resourceResult
	}

	coordinator.mu.Lock()
	if coordinator.pendingResume == nil || coordinator.pendingResume.request.RequestID != command.RequestID {
		coordinator.mu.Unlock()
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	copy := record.Session
	coordinator.currentSession = &copy
	coordinator.currentResumed = true
	coordinator.pendingResume = nil
	coordinator.mu.Unlock()
	outcome.Session = projectUISession(record.Session, true)
	projectionCopy := projection
	outcome.Resumed = &projectionCopy
	return outcome
}

func resumeRequestMatchesStart(request UIResumeRequest, intent UIStartIntent) bool {
	switch intent.Kind {
	case UIStartResumePicker:
		return request.Mode == UIResumeExact
	case UIStartResumeID:
		return request.Mode == UIResumeExact && request.SessionID == intent.SessionID
	case UIStartResumeLast:
		return request.Mode == UIResumeLast
	default:
		return false
	}
}

func (coordinator *Coordinator) resumeScopeResult(
	ctx context.Context,
	command UICommand,
) UIScopeResult {
	if command.Scope != nil {
		return coordinator.executeScopeCommand(ctx, UICommand{
			Kind: UICommandActivateScope, RequestID: command.RequestID,
			ExpectedScopeGeneration: command.ExpectedScopeGeneration, Scope: command.Scope,
		})
	}
	view := coordinator.uiScopes.View()
	if view.State != ScopeStateActive || view.Scope == nil {
		return failedUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, view, UIQueryUnavailable)
	}
	return successfulUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, *view.Scope)
}

func resumeSavedState(record ResumedSessionRecord) (*domain.ScopeCandidate, *domain.ResourceRef) {
	scope := cloneScopeCandidate(record.Session.LastScope)
	resource := cloneResource(record.Session.SelectedResource)
	for _, message := range record.Messages {
		if message.Scope != nil {
			scope = &domain.ScopeCandidate{Context: message.Scope.Context, Namespace: message.Scope.Namespace}
		}
		if message.Resource != nil {
			resource = cloneResource(message.Resource)
		}
	}
	return scope, resource
}

func (coordinator *Coordinator) revalidateResumedResource(
	ctx context.Context,
	requestID uint64,
	scope domain.ClusterScope,
	savedScope *domain.ScopeCandidate,
	reference *domain.ResourceRef,
) UIResourceSelectionResult {
	result := UIResourceSelectionResult{RequestID: requestID, ScopeGeneration: scope.Generation, Cleared: true}
	if reference == nil {
		return result
	}
	if savedScope == nil || savedScope.Context != scope.Context || savedScope.Namespace != scope.Namespace ||
		reference.Namespace != scope.Namespace {
		result.Cleared = false
		result.Failure = UIQueryUnavailable
		return result
	}
	actual, err := coordinator.uiScopes.GetResource(ctx, scope, *reference)
	if err != nil {
		result.Cleared = false
		result.Failure = uiFailureCode(err)
		return result
	}
	if reference.UID != "" && actual.Reference.UID != reference.UID {
		result.Cleared = false
		result.Failure = UIQueryUnavailable
		return result
	}
	if err := coordinator.uiScopes.SelectResource(scope, actual.Reference); err != nil {
		result.Cleared = false
		result.Failure = uiFailureCode(err)
		return result
	}
	result.Cleared = false
	selected := actual.Reference
	result.Resource = &selected
	return result
}

func (coordinator *Coordinator) executeScopeCommand(ctx context.Context, command UICommand) UIScopeResult {
	if coordinator.uiScopes == nil {
		return UIScopeResult{
			RequestID: command.RequestID, ExpectedGeneration: command.ExpectedScopeGeneration,
			ScopeGeneration: command.ExpectedScopeGeneration, Failure: UIQueryUnavailable,
		}
	}
	view := coordinator.uiScopes.View()
	if view.Generation != command.ExpectedScopeGeneration {
		return failedUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, view, UIQueryUnavailable)
	}
	var (
		scope domain.ClusterScope
		err   error
	)
	switch command.Kind {
	case UICommandSelectContext:
		if view.State == ScopeStateActive && view.Scope != nil && view.Scope.Context == command.Text {
			scope = *view.Scope
		} else {
			scope, err = coordinator.uiScopes.SwitchContext(ctx, command.Text, command.ExpectedScopeGeneration)
		}
	case UICommandSelectNamespace:
		scope, err = coordinator.uiScopes.SwitchNamespace(ctx, command.Text, command.ExpectedScopeGeneration)
	case UICommandActivateScope:
		scope, err = coordinator.activateExactScope(ctx, *command.Scope, command.ExpectedScopeGeneration)
	default:
		err = ErrInvalidUICommand
	}
	if err != nil {
		current := coordinator.uiScopes.View()
		return failedUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, current, uiFailureCode(err))
	}
	return successfulUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, scope)
}

func (coordinator *Coordinator) activateExactScope(
	ctx context.Context,
	target domain.ScopeCandidate,
	expectedGeneration int64,
) (domain.ClusterScope, error) {
	view := coordinator.uiScopes.View()
	if view.State == ScopeStateActive && view.Scope != nil && view.Scope.Context == target.Context {
		if view.Scope.Namespace == target.Namespace {
			return *view.Scope, nil
		}
		return coordinator.uiScopes.SwitchNamespace(ctx, target.Namespace, expectedGeneration)
	}
	scope, err := coordinator.uiScopes.SwitchContext(ctx, target.Context, expectedGeneration)
	if err != nil || scope.Namespace == target.Namespace {
		return scope, err
	}
	result, namespaceErr := coordinator.uiScopes.SwitchNamespace(ctx, target.Namespace, scope.Generation)
	if namespaceErr == nil {
		return result, nil
	}
	_ = invalidatePartialUIScope(coordinator.uiScopes, scope.Generation)
	return domain.ClusterScope{}, namespaceErr
}

func successfulUIScopeResult(requestID uint64, expectedGeneration int64, scope domain.ClusterScope) UIScopeResult {
	return UIScopeResult{
		RequestID: requestID, ExpectedGeneration: expectedGeneration, ScopeGeneration: scope.Generation,
		Context: scope.Context, Namespace: scope.Namespace, ReadOnly: true,
	}
}

func failedUIScopeResult(
	requestID uint64,
	expectedGeneration int64,
	view ScopeView,
	failure UIQueryFailureCode,
) UIScopeResult {
	generation := max(expectedGeneration, view.Generation)
	return UIScopeResult{
		RequestID: requestID, ExpectedGeneration: expectedGeneration,
		ScopeGeneration: generation, Failure: failure,
	}
}

func invalidatePartialUIScope(manager *ScopeManager, expectedGeneration int64) error {
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	if err := manager.checkExpectedGeneration(expectedGeneration); err != nil {
		return err
	}
	generation, client, cancel, err := manager.beginInvalidation()
	if err != nil {
		return err
	}
	manager.finishLocalInvalidation(generation, cancel)
	hookErr := manager.invalidateHook(generation)
	if client != nil {
		client.Close()
	}
	manager.markUnavailable(generation)
	return hookErr
}

func clearScopeResource(manager *ScopeManager, scope domain.ClusterScope) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.currentScopeLocked(scope) {
		return false
	}
	manager.selectedResource = nil
	return true
}

func (coordinator *Coordinator) executeResourceCommand(command UICommand) UIResourceSelectionResult {
	result := UIResourceSelectionResult{
		RequestID: command.RequestID, ScopeGeneration: max(1, command.ExpectedScopeGeneration),
	}
	if coordinator.uiScopes == nil {
		result.Failure = UIQueryUnavailable
		return result
	}
	view := coordinator.uiScopes.View()
	if view.State != ScopeStateActive || view.Scope == nil || view.Generation != command.ExpectedScopeGeneration {
		result.Failure = UIQueryUnavailable
		return result
	}
	result.ScopeGeneration = view.Generation
	if command.Text == "clear" {
		if !clearScopeResource(coordinator.uiScopes, *view.Scope) {
			result.Failure = UIQueryUnavailable
			return result
		}
		result.Cleared = true
		return result
	}
	if err := coordinator.uiScopes.SelectResource(*view.Scope, *command.Resource); err != nil {
		result.Failure = uiFailureCode(err)
		return result
	}
	selected := *command.Resource
	result.Resource = &selected
	return result
}

func (coordinator *Coordinator) executeRenameCommand(ctx context.Context, command UICommand) (UICommandOutcome, error) {
	if coordinator.titles == nil {
		return UICommandOutcome{Command: command.Kind, Failure: UIQueryUnavailable}, nil
	}
	processed, err := coordinator.questions.Process(command.Text, 512)
	if err != nil || (command.Text != "" && processed.Value == "") {
		return UICommandOutcome{Command: command.Kind, Failure: UIQueryUnavailable}, nil
	}
	coordinator.mu.Lock()
	if coordinator.currentSession == nil {
		coordinator.mu.Unlock()
		return UICommandOutcome{Command: command.Kind, Failure: UIQueryUnavailable}, nil
	}
	current := *coordinator.currentSession
	coordinator.mu.Unlock()
	updatedAt := coordinator.now()
	if !validCoordinatorTime(updatedAt) {
		return UICommandOutcome{}, ErrCoordinatorDependency
	}
	err = coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.titles.Rename(operationContext, RenameSessionRecord{
			SessionID: current.ID, Title: processed.Value, ExpectedVersion: current.Version, UpdatedAt: updatedAt,
		})
	})
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		return UICommandOutcome{Command: command.Kind, Failure: UIQueryUnavailable}, nil
	}
	current.Title = processed.Value
	current.Version++
	current.UpdatedAt = updatedAt
	coordinator.mu.Lock()
	coordinator.currentSession = &current
	resumed := coordinator.currentResumed
	coordinator.mu.Unlock()
	return UICommandOutcome{Command: command.Kind, Session: projectUISession(current, resumed)}, nil
}

func (coordinator *Coordinator) uiStatus() UIStatusResult {
	result := UIStatusResult{Session: coordinator.CurrentUISession()}
	if coordinator.uiScopes != nil {
		view := coordinator.uiScopes.View()
		result.ScopeGeneration = view.Generation
		if view.State == ScopeStateActive && view.Scope != nil {
			result.Context = view.Scope.Context
			result.Namespace = view.Scope.Namespace
			result.ReadOnly = true
		}
	}
	coordinator.mu.Lock()
	if coordinator.active != nil && !coordinator.active.terminal {
		result.RunID = coordinator.active.run.ID
		result.RunActive = true
	}
	coordinator.mu.Unlock()
	return result
}

func projectSessionCandidate(record ResumeSessionRecord) UISessionCandidate {
	result := UISessionCandidate{
		ID: record.ID, Title: record.Title, UpdatedAtUnixMillis: record.UpdatedAt.UTC().UnixMilli(),
		PrivacyMode: record.PrivacyMode,
	}
	if record.LastScope != nil {
		result.Context = record.LastScope.Context
		result.Namespace = record.LastScope.Namespace
	}
	return result
}

func projectResumedSession(requestID uint64, record ResumedSessionRecord) UIResumedSession {
	metadata := ResumeSessionRecord{
		ID: record.Session.ID, Title: record.Session.Title, UpdatedAt: record.Session.UpdatedAt,
		PrivacyMode: record.Session.PrivacyMode, LastScope: cloneScopeCandidate(record.Session.LastScope),
	}
	result := UIResumedSession{
		ResumeRequestID: requestID, Session: projectSessionCandidate(metadata),
		SavedScope: cloneScopeCandidate(record.Session.LastScope),
		History:    make([]UIHistoryMessage, 0, len(record.Messages)),
	}
	if record.Session.SelectedResource != nil {
		result.SavedResource = projectUIResourceCandidate(*record.Session.SelectedResource)
	}
	for _, message := range record.Messages {
		result.History = append(result.History, UIHistoryMessage{
			Role: message.Role, Format: message.Format, Content: message.Content,
		})
		if message.Scope != nil {
			result.SavedScope = &domain.ScopeCandidate{Context: message.Scope.Context, Namespace: message.Scope.Namespace}
		}
		if message.Resource != nil {
			result.SavedResource = projectUIResourceCandidate(*message.Resource)
		}
	}
	if result.SavedScope != nil {
		result.Session.Context = result.SavedScope.Context
		result.Session.Namespace = result.SavedScope.Namespace
	}
	return result
}

func cloneScopeCandidate(value *domain.ScopeCandidate) *domain.ScopeCandidate {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func projectUIResourceCandidate(reference domain.ResourceRef) *UIResourceCandidate {
	kind, ok := domain.ResourceKindForReference(reference)
	if !ok {
		return nil
	}
	return &UIResourceCandidate{
		APIVersion: reference.APIVersion, Kind: kind, Namespace: reference.Namespace, Name: reference.Name,
	}
}

func projectUIResourceSummary(summary domain.ResourceSummary) UIResourceCandidate {
	kind, _ := domain.ResourceKindForReference(summary.Reference)
	status := summary.Status.Phase
	if status == "" {
		status = summary.Status.Reason
	}
	if status == "" {
		status = summary.Status.ServiceType
	}
	return UIResourceCandidate{
		APIVersion: summary.Reference.APIVersion,
		Kind:       kind,
		Namespace:  summary.Reference.Namespace,
		Name:       summary.Reference.Name,
		Status:     status,
	}
}

func uiTextMatches(value, filter string) bool {
	return filter == "" || strings.Contains(strings.ToLower(value), strings.ToLower(filter))
}

func uiResourceMatches(candidate UIResourceCandidate, filter string) bool {
	return uiTextMatches(candidate.Name, filter) || uiTextMatches(string(candidate.Kind)+"/"+candidate.Name, filter) ||
		(candidate.Status != "" && uiTextMatches(candidate.Status, filter))
}

func uiFailureCode(err error) UIQueryFailureCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return UIQueryTimeout
	}
	var classified interface{ Class() domain.SafeErrorClass }
	if !errors.As(err, &classified) {
		return UIQueryUnavailable
	}
	switch classified.Class() {
	case domain.SafeErrorClassPermissionDenied, domain.SafeErrorClassPolicyDenied:
		return UIQueryForbidden
	case domain.SafeErrorClassTimeout:
		return UIQueryTimeout
	default:
		return UIQueryUnavailable
	}
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
	coordinator.mu.Lock()
	copy := session
	coordinator.currentSession = &copy
	coordinator.currentResumed = false
	coordinator.pendingResume = nil
	coordinator.startupResume = nil
	coordinator.mu.Unlock()
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
	runContext, cancelRun := context.WithCancel(ctx)
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

	var runID domain.AgentRunID
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

	authorized, privacyErr := coordinator.privacy.AuthorizeModel(runContext)
	if privacyErr != nil {
		if err := runContext.Err(); err != nil {
			return "", err
		}
		coordinator.privacy.FailClosed()
		coordinator.markGlobalPersistenceDegraded()
		return "", ErrPersistenceUnavailable
	}
	if !authorized {
		return "", ErrConsentRequired
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
	bridge, err := newEventBridge(runID, scope.Generation, coordinator.uiEvents)
	if err != nil {
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
	modelAuthorized := true
	var privacyErr error
	if event.Kind == agent.RunEventModelStreamStarted {
		modelAuthorized, privacyErr = coordinator.privacy.AuthorizeModel(ctx)
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
	if !modelAuthorized || privacyErr != nil {
		state.cancel()
		coordinator.mu.Unlock()
		if privacyErr != nil {
			coordinator.privacy.FailClosed()
			_ = coordinator.markRunPersistenceDegraded(ctx, state)
		}
		coordinator.finishPublishing(state)
		return agent.EventSinkRejected
	}
	action, err := coordinator.acceptEventLocked(state, event)
	coordinator.mu.Unlock()
	if err != nil {
		coordinator.finishPublishing(state)
		return agent.EventSinkRejected
	}

	bridgeFailed := false
	if event.Terminal() && coordinator.approvals != nil {
		approvalContext, cancelApproval := context.WithTimeout(ctx, coordinator.persistenceLimit)
		approvalErr := coordinator.approvals.CancelRun(approvalContext, state.run.ID)
		cancelApproval()
		if approvalErr != nil {
			degradedContext, cancelDegraded := context.WithTimeout(ctx, coordinator.persistenceLimit)
			bridgeFailed = coordinator.markRunPersistenceDegraded(degradedContext, state) != nil
			cancelDegraded()
		}
	}
	retentionFailed := false
	if event.Terminal() && coordinator.startup != nil && !state.persistenceBad {
		cleanupErr := coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.startup.CleanupRetention(operationContext, coordinator.now())
		})
		if cleanupErr != nil {
			retentionFailed = true
			bridgeFailed = coordinator.markRunPersistenceDegraded(ctx, state) != nil
		}
	}
	persistenceErr := coordinator.performPersistence(ctx, state, action)
	persistenceFailed := errors.Is(persistenceErr, ErrPersistenceUnavailable)
	if persistenceFailed {
		bridgeFailed = coordinator.markRunPersistenceDegraded(ctx, state) != nil || bridgeFailed
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
	if retentionFailed || persistenceFailed || state.persistenceBad {
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
	if coordinator.active == nil || coordinator.active.terminal || coordinator.active.run.ID != command.RunID ||
		coordinator.active.run.Scope.Generation != command.ScopeGeneration {
		coordinator.mu.Unlock()
		return ErrRunNotActive
	}
	cancel := coordinator.active.cancel
	approvals := coordinator.approvals
	coordinator.mu.Unlock()
	cancel()
	if approvals != nil {
		if err := approvals.CancelRun(ctx, command.RunID); err != nil {
			return err
		}
	}
	return nil
}

// SubmitRestartDeploymentProposal delegates the isolated v0.2 proposal bridge
// to the optional approval coordinator. The v0.1 composition leaves it nil.
func (coordinator *Coordinator) SubmitRestartDeploymentProposal(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	sequence int64,
	intent domain.OperationIntent,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || coordinator.approvals == nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active == nil || coordinator.active.terminal || coordinator.active.run.ID != runID ||
		coordinator.active.publishing || coordinator.active.bridge == nil ||
		coordinator.active.run.SessionID != sessionID || coordinator.active.run.Scope != intent.Scope ||
		sequence != coordinator.active.bridge.sequence+1 {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	request, err := coordinator.approvals.SubmitRestartDeploymentProposal(ctx, runID, sessionID, sequence, intent)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	coordinator.active.bridge.sequence = sequence
	return request, nil
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
