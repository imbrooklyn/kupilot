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
	Sessions            SessionPersistence
	Runs                RunPersistence
	RunInputs           RunInputPersistence
	Tools               ToolEvidencePersistence
	Audits              AuditPersistence
	Scope               ActiveScope
	Runner              agent.AgentRunner
	ModelRuntime        ModelRuntime
	ModelFactory        ModelRuntimeFactory
	ModelProfiles       ModelProfileWriter
	ReviewerModel       *ReviewerModelBinding
	Identifiers         ApplicationIdentifierSource
	AuditIdentifiers    AuditIdentifierSource
	Questions           QuestionProcessor
	Exports             SessionExportReader
	ExportFiles         ExportFileWriter
	ExportText          ExportTextProcessor
	Privacy             *PrivacyManager
	RunResourcePolicies RunResourcePolicySource
	ModelContext        ModelContextPersistence
	UIEvents            UIEventSink
	Observer            RunObserver
	Now                 func() time.Time
	BudgetLimits        agent.RunBudgetLimits
	PersistenceTimeout  time.Duration
	UI                  *CoordinatorUIConfig
}

// CoordinatorUIConfig supplies the existing Session and startup consumers
// needed by CLI/TUI composition. Core run tests may omit it when no delivery
// use case is exercised.
type CoordinatorUIConfig struct {
	Sessions             SessionResumeStore
	Search               SessionSearchReader
	Titles               SessionTitleStore
	Startup              StartupMaintenance
	Scopes               *ScopeManager
	ScopePreferences     ScopePreferenceStore
	Approvals            *ApprovalCoordinator
	RestartProposals     RestartDeploymentProposalPreparer
	RemediationProposals RemediationProposalPreparer
	LocalProposals       LocalActionProposalPreparer
	EvidenceDetail       EvidenceDetailReader
	LocalState           LocalStateDeleter
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

	sessions             SessionPersistence
	runs                 RunPersistence
	runInputs            RunInputPersistence
	tools                ToolEvidencePersistence
	audits               AuditPersistence
	scope                ActiveScope
	runner               agent.AgentRunner
	modelRuntime         ModelRuntime
	modelFactory         ModelRuntimeFactory
	modelProfiles        ModelProfileWriter
	reviewerModel        *ReviewerModelBinding
	identifiers          ApplicationIdentifierSource
	auditIdentifiers     AuditIdentifierSource
	questions            QuestionProcessor
	privacy              *PrivacyManager
	runResourcePolicies  RunResourcePolicySource
	modelContextStore    ModelContextPersistence
	uiEvents             UIEventSink
	observer             RunObserver
	now                  func() time.Time
	budgetLimits         agent.RunBudgetLimits
	persistenceLimit     time.Duration
	resumeSessions       SessionResumeStore
	sessionSearch        SessionSearchReader
	titles               SessionTitleStore
	startup              StartupMaintenance
	uiScopes             *ScopeManager
	scopePreferences     ScopePreferenceStore
	approvals            *ApprovalCoordinator
	restartProposals     RestartDeploymentProposalPreparer
	remediationProposals RemediationProposalPreparer
	localProposals       LocalActionProposalPreparer
	evidenceDetails      EvidenceDetailReader
	localState           LocalStateDeleter
	exports              SessionExportReader
	exportFiles          ExportFileWriter
	exportText           ExportTextProcessor
	startupPrepared      bool
	currentSession       *domain.Session
	currentResumed       bool
	pendingResume        *pendingResume
	startupResume        *startupResumeState
	privacyChallenge     *privacyChallenge
	modelContext         sessionModelContext

	closed                  bool
	persistenceDegraded     bool
	scopePreferenceDegraded bool
	starting                bool
	startingCancel          context.CancelFunc
	startingDone            chan struct{}
	operations              int
	operationsDone          chan struct{}
	deletingSession         domain.SessionID
	active                  *activeRun
	lastResult              *RunResult
	lastDiagnosis           *domain.Diagnosis
	lastEvidence            map[domain.EvidenceID]domain.Evidence
	conversationInputs      conversationInputQueue
}

type activeRun struct {
	input                    agent.RunInput
	runner                   agent.AgentRunner
	run                      domain.AgentRun
	cancel                   context.CancelFunc
	done                     chan struct{}
	bridge                   *eventBridge
	tools                    map[domain.ToolInvocationID]*pendingTool
	diagnosis                *domain.Diagnosis
	persistOperationalDetail bool
	minimalPersistence       bool
	toolResultBytes          int
	logCalls                 int
	metricCalls              int
	dataSourceCalls          int
	remoteExecCalls          int
	localProcessCalls        int
	summaryCalls             int
	committedInputs          []agent.ConversationTurn

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
	request        UIResumeRequest
	record         ResumedSessionRecord
	projection     UIResumedSession
	scopeConfirmed bool
}

type startupResumeState struct {
	intent UIStartIntent
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
	persistSessionSummary
)

type persistenceAction struct {
	kind       persistenceActionKind
	invocation domain.ToolInvocation
	evidence   []domain.Evidence
	event      agent.RunEvent
	audit      auditSpec
	summary    *domain.SessionContextSummary
}

type auditSpec struct {
	eventType  domain.AuditEventType
	actor      domain.AuditActor
	outcome    domain.AuditOutcome
	errorClass *domain.SafeErrorClass
	toolName   *domain.ToolName
	sequence   *int
}

// NewCoordinator validates the complete operational runtime composition
// without performing persistence, model, Tool, Kubernetes, logging, or UI I/O.
func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	limits := config.BudgetLimits
	if limits == (agent.RunBudgetLimits{}) {
		limits = agent.DefaultRunBudgetLimits()
	}
	persistenceLimit := config.PersistenceTimeout
	if persistenceLimit == 0 {
		persistenceLimit = DefaultPersistenceTimeout
	}
	runner := config.Runner
	if config.ModelRuntime != nil {
		if runner != nil {
			return nil, ErrCoordinatorDependency
		}
		runner = config.ModelRuntime
	}
	if (config.ModelFactory == nil) != (config.ModelProfiles == nil) {
		return nil, ErrCoordinatorDependency
	}
	if config.Sessions == nil || config.Runs == nil || config.Tools == nil ||
		config.Audits == nil || config.Scope == nil || runner == nil && config.ModelFactory == nil || config.Identifiers == nil ||
		config.AuditIdentifiers == nil || config.Questions == nil || config.UIEvents == nil || config.Observer == nil ||
		config.Privacy == nil || config.RunResourcePolicies == nil || config.Now == nil || !validCoordinatorTime(config.Now()) || limits.Validate() != nil ||
		persistenceLimit <= 0 || persistenceLimit > MaxPersistenceTimeout {
		return nil, ErrCoordinatorDependency
	}
	if config.ReviewerModel != nil && (!config.ReviewerModel.valid() ||
		config.ReviewerModel.Budget.Snapshot().Limits.Profile != limits.Profile) {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && (config.UI.Sessions == nil || config.UI.Search == nil || config.UI.Titles == nil || config.UI.Startup == nil) {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && config.UI.Approvals != nil && config.UI.Scopes == nil {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && (config.UI.Scopes == nil) != (config.UI.ScopePreferences == nil) {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && (config.UI.Approvals == nil) != (config.UI.RestartProposals == nil) {
		return nil, ErrCoordinatorDependency
	}
	if config.UI != nil && config.UI.Approvals == nil &&
		(config.UI.RemediationProposals != nil || config.UI.LocalProposals != nil) {
		return nil, ErrCoordinatorDependency
	}
	runInputs := config.RunInputs
	if runInputs == nil {
		runInputs, _ = config.Runs.(RunInputPersistence)
	}
	exportDependencies := 0
	if config.Exports != nil {
		exportDependencies++
	}
	if config.ExportFiles != nil {
		exportDependencies++
	}
	if config.ExportText != nil {
		exportDependencies++
	}
	if exportDependencies != 0 && exportDependencies != 3 {
		return nil, ErrCoordinatorDependency
	}
	var resumeSessions SessionResumeStore
	var sessionSearch SessionSearchReader
	var titles SessionTitleStore
	var startup StartupMaintenance
	var uiScopes *ScopeManager
	var scopePreferences ScopePreferenceStore
	var approvals *ApprovalCoordinator
	var restartProposals RestartDeploymentProposalPreparer
	var remediationProposals RemediationProposalPreparer
	var localProposals LocalActionProposalPreparer
	var evidenceDetails EvidenceDetailReader
	var localState LocalStateDeleter
	if config.UI != nil {
		resumeSessions = config.UI.Sessions
		sessionSearch = config.UI.Search
		titles = config.UI.Titles
		startup = config.UI.Startup
		uiScopes = config.UI.Scopes
		scopePreferences = config.UI.ScopePreferences
		approvals = config.UI.Approvals
		restartProposals = config.UI.RestartProposals
		remediationProposals = config.UI.RemediationProposals
		localProposals = config.UI.LocalProposals
		evidenceDetails = config.UI.EvidenceDetail
		localState = config.UI.LocalState
		if approvals != nil && uiScopes.BindApprovalInvalidationHook(approvals) != nil {
			return nil, ErrCoordinatorDependency
		}
	}
	coordinator := &Coordinator{
		sessions: config.Sessions, runs: config.Runs, runInputs: runInputs, tools: config.Tools,
		audits: config.Audits, scope: config.Scope,
		runner: runner, modelRuntime: config.ModelRuntime,
		modelFactory: config.ModelFactory, modelProfiles: config.ModelProfiles, reviewerModel: config.ReviewerModel,
		identifiers:      config.Identifiers,
		auditIdentifiers: config.AuditIdentifiers, questions: config.Questions,
		privacy: config.Privacy, runResourcePolicies: config.RunResourcePolicies, modelContextStore: config.ModelContext,
		uiEvents: config.UIEvents, observer: config.Observer, now: config.Now,
		budgetLimits: limits, persistenceLimit: persistenceLimit,
		resumeSessions: resumeSessions, sessionSearch: sessionSearch, titles: titles, startup: startup,
		uiScopes:             uiScopes,
		scopePreferences:     scopePreferences,
		approvals:            approvals,
		restartProposals:     restartProposals,
		remediationProposals: remediationProposals,
		localProposals:       localProposals,
		evidenceDetails:      evidenceDetails,
		localState:           localState,
		exports:              config.Exports,
		exportFiles:          config.ExportFiles,
		exportText:           config.ExportText,
	}
	if uiScopes != nil {
		if err := uiScopes.BindConversationInputInvalidationHook(coordinator); err != nil {
			return nil, ErrCoordinatorDependency
		}
	}
	return coordinator, nil
}

// StartUI performs mandatory startup maintenance, activates a new or explicitly
// selected startup scope, and applies exactly one fixed Session start intent.
// Session history remains behind a later explicit resume request.
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
	if coordinator.uiScopes != nil {
		candidate, preferenceDegraded, resolveErr := coordinator.resolveStartupScopeCandidate(ctx, intent)
		result.ScopePreferenceDegraded = preferenceDegraded
		if resolveErr == nil {
			if intent.Kind == UIStartNew || intent.ExplicitScope {
				view := coordinator.uiScopes.View()
				scope, activateErr := coordinator.activateExactScope(ctx, candidate, view.Generation)
				if activateErr != nil {
					if contextErr := ctx.Err(); contextErr != nil {
						return UIStartResult{}, contextErr
					}
					// The candidate carries no authority after failed independent
					// activation. Delivery opens the ordinary scope picker instead
					// of silently starting under another Context or Namespace.
					result.ScopeCandidate = cloneScopeCandidate(&candidate)
				} else {
					result.Scope = &UIStartupScope{
						Context: scope.Context, Namespace: scope.Namespace,
						Generation: scope.Generation, ReadOnly: true,
					}
					if coordinator.saveScopePreference(ctx, scope.Context) != nil {
						result.ScopePreferenceDegraded = true
					}
				}
			} else {
				result.ScopeCandidate = cloneScopeCandidate(&candidate)
			}
		} else if contextErr := ctx.Err(); contextErr != nil {
			return UIStartResult{}, contextErr
		} else if candidate, ok := configuredStartupCandidate(intent); ok {
			result.ScopeCandidate = &candidate
		}
		result.ScopeGeneration = coordinator.uiScopes.View().Generation
	}
	coordinator.setScopePreferenceDegraded(result.ScopePreferenceDegraded)
	coordinator.mu.Lock()
	if intent.Kind == UIStartResumePicker || intent.Kind == UIStartResumeID || intent.Kind == UIStartResumeLast {
		coordinator.startupResume = &startupResumeState{intent: intent}
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

func configuredStartupCandidate(intent UIStartIntent) (domain.ScopeCandidate, bool) {
	if intent.ConfiguredContext == "" {
		return domain.ScopeCandidate{}, false
	}
	namespace := intent.ConfiguredNamespace
	if namespace == "" {
		namespace = DefaultStartupNamespace
	}
	candidate := domain.ScopeCandidate{Context: intent.ConfiguredContext, Namespace: namespace}
	return candidate, candidate.Validate() == nil
}

// ConfigureModel owns the single-runtime replacement sequence used by the TUI.
// The transient credential is destroyed on every outcome.
func (coordinator *Coordinator) ConfigureModel(ctx context.Context, request ModelSetupRequest) (ModelSetupResult, error) {
	if request.Secret != nil {
		defer request.Secret.Destroy()
	}
	if coordinator == nil || ctx == nil || request.Validate() != nil {
		return ModelSetupResult{}, ErrModelSetupInvalid
	}
	if err := ctx.Err(); err != nil {
		return ModelSetupResult{}, err
	}
	if err := coordinator.beginUIOperation(false); err != nil {
		return ModelSetupResult{}, err
	}
	defer coordinator.finishOperation()

	coordinator.mu.Lock()
	factory := coordinator.modelFactory
	profiles := coordinator.modelProfiles
	closed := coordinator.closed
	coordinator.mu.Unlock()
	if closed || factory == nil || profiles == nil {
		return ModelSetupResult{}, ErrModelSetupFailed
	}
	replacement, err := factory.BuildModelRuntime(ctx, request)
	if err != nil || replacement == nil || !validModelSetupText(replacement.ModelName(), MaxModelSetupNameBytes) ||
		!validPrivacyOrigin(replacement.Origin()) {
		if replacement != nil {
			replacement.Close()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return ModelSetupResult{}, contextErr
		}
		return ModelSetupResult{}, ErrModelSetupFailed
	}
	if contextErr := ctx.Err(); contextErr != nil {
		replacement.Close()
		return ModelSetupResult{}, contextErr
	}
	coordinator.mu.Lock()
	currentRuntime := coordinator.modelRuntime
	approvals := coordinator.approvals
	coordinator.mu.Unlock()
	originChanged := currentRuntime == nil || currentRuntime.Origin() != replacement.Origin()
	if originChanged && approvals != nil {
		if err := approvals.InvalidateModelOrigin(ctx); err != nil {
			replacement.Close()
			return ModelSetupResult{}, ErrModelSetupFailed
		}
	}
	coordinator.publishConversationInvalidation(ctx)
	if err := coordinator.stopRunForModelSetup(ctx); err != nil {
		replacement.Close()
		return ModelSetupResult{}, err
	}
	if err := ctx.Err(); err != nil {
		replacement.Close()
		return ModelSetupResult{}, err
	}
	if request.Persist {
		if err := profiles.SaveModelProfile(ctx, request); err != nil {
			replacement.Close()
			if contextErr := ctx.Err(); contextErr != nil {
				return ModelSetupResult{}, contextErr
			}
			return ModelSetupResult{}, ErrModelSetupFailed
		}
	}
	// A successful durable save is the commit point for persisted setup. Finish
	// the privacy-origin and runtime swap after it so disk and process state do
	// not diverge when cancellation races that commit.
	if err := coordinator.privacy.ReconfigureOrigin(replacement.Origin()); err != nil {
		replacement.Close()
		return ModelSetupResult{}, ErrModelSetupFailed
	}

	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		replacement.Close()
		return ModelSetupResult{}, ErrCoordinatorClosed
	}
	previous := coordinator.modelRuntime
	previousProfile := ""
	previousOrigin := ""
	if previous != nil {
		previousOrigin = previous.Origin()
		if named, ok := previous.(namedModelRuntime); ok {
			previousProfile = named.ProfileName()
		}
	}
	coordinator.modelRuntime = replacement
	coordinator.runner = replacement
	coordinator.privacyChallenge = nil
	replacementProfile := "agent"
	if named, ok := replacement.(namedModelRuntime); ok {
		replacementProfile = named.ProfileName()
	}
	if coordinator.currentSession != nil &&
		(previousOrigin != replacement.Origin() || previousProfile != replacementProfile) {
		loaded := coordinator.currentSession.PrivacyMode == domain.PrivacyModeMinimal
		coordinator.modelContext = newSessionModelContext(*coordinator.currentSession, loaded)
	}
	coordinator.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	result := ModelSetupResult{
		RequestID: request.RequestID, Model: replacement.ModelName(),
		Origin: replacement.Origin(), Persisted: request.Persist,
	}
	if result.Validate() != nil {
		return ModelSetupResult{}, ErrModelSetupFailed
	}
	return result, nil
}

func (coordinator *Coordinator) stopRunForModelSetup(ctx context.Context) error {
	for {
		coordinator.mu.Lock()
		startingCancel := coordinator.startingCancel
		startingDone := coordinator.startingDone
		state := coordinator.active
		coordinator.mu.Unlock()
		if startingCancel == nil && state == nil {
			return nil
		}
		if startingCancel != nil {
			startingCancel()
		}
		if state != nil {
			state.cancel()
		}
		var done <-chan struct{}
		if startingDone != nil {
			done = startingDone
		} else if state != nil {
			done = state.done
		}
		if done == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
}

func (coordinator *Coordinator) resolveStartupScopeCandidate(
	ctx context.Context,
	intent UIStartIntent,
) (domain.ScopeCandidate, bool, error) {
	contextName := intent.ConfiguredContext
	remembered := false
	preferenceDegraded := false
	if contextName == "" {
		var (
			preference ScopePreference
			found      bool
		)
		err := coordinator.persist(ctx, func(operationContext context.Context) error {
			var loadErr error
			preference, found, loadErr = coordinator.scopePreferences.LoadLastContext(operationContext)
			return loadErr
		})
		if err != nil || found && preference.Validate() != nil {
			preferenceDegraded = true
		} else if found {
			contextName = preference.Context
			remembered = true
		}
	}
	candidates, err := coordinator.uiScopes.ListContexts(ctx)
	if err != nil {
		return domain.ScopeCandidate{}, preferenceDegraded, err
	}
	var selected *ContextCandidate
	for index := range candidates {
		candidate := candidates[index]
		if (contextName != "" && candidate.Name == contextName) || (contextName == "" && candidate.Current) {
			selected = &candidate
			break
		}
	}
	if selected == nil && remembered {
		for index := range candidates {
			if candidates[index].Current {
				selected = &candidates[index]
				break
			}
		}
	}
	if selected == nil {
		return domain.ScopeCandidate{}, preferenceDegraded, ErrScopeUnavailable
	}
	namespace := intent.ConfiguredNamespace
	if namespace == "" {
		namespace = DefaultStartupNamespace
	}
	result := domain.ScopeCandidate{Context: selected.Name, Namespace: namespace}
	if result.Validate() != nil || !domain.ValidContextName(result.Context) || !domain.ValidNamespaceName(result.Namespace) {
		return domain.ScopeCandidate{}, preferenceDegraded, ErrScopeUnavailable
	}
	return result, preferenceDegraded, nil
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
		if coordinator.sessionSearch == nil {
			result.Failure = UIQueryUnavailable
			break
		}
		request := SessionSearchRequest{Filter: strings.TrimSpace(query.Filter), Limit: query.Limit}
		if request.Validate() != nil {
			result.Failure = UIQueryUnavailable
			break
		}
		records, err := coordinator.sessionSearch.SearchResumable(ctx, request)
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
	explicitStartupScope := false
	coordinator.mu.Lock()
	if coordinator.startupResume != nil {
		if !resumeRequestMatchesStart(request, coordinator.startupResume.intent) {
			coordinator.mu.Unlock()
			return UIResumeResult{}, ErrInvalidUIQuery
		}
		explicitStartupScope = coordinator.startupResume.intent.ExplicitScope
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
	if err := coordinator.attachHistoryEvidence(ctx, record, &projection); err != nil {
		return UIResumeResult{}, err
	}
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
			request: request, record: record, projection: projection, scopeConfirmed: explicitStartupScope,
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
	case UICommandSubmitSteer, UICommandEnqueueFollowUp, UICommandPopFollowUp:
		return coordinator.executeConversationInputCommand(ctx, command)
	case UICommandSetPersistenceMode:
		return coordinator.executePersistenceModeCommand(ctx, command)
	case UICommandDeleteSession:
		return coordinator.executeDeleteSessionCommand(ctx, command)
	case UICommandClearHistory, UICommandDeleteAllLocalState:
		return coordinator.executeHistoryDeletionCommand(ctx, command)
	case UICommandExportSession:
		return coordinator.executeExportSessionCommand(ctx, command)
	case UICommandApproveAction, UICommandRejectAction, UICommandCancelAction, UICommandExpireAction:
		if coordinator.approvals == nil {
			return UICommandOutcome{}, ErrApprovalUnavailable
		}
		var (
			result UIApprovalResult
			err    error
		)
		if command.Kind == UICommandExpireAction {
			result, err = coordinator.approvals.ExpireCommand(ctx, command)
		} else if command.Kind == UICommandCancelAction {
			result, err = coordinator.approvals.CancelAction(ctx, command)
		} else {
			result, err = coordinator.approvals.Decide(ctx, command)
			if err == nil && command.Kind == UICommandApproveAction && result.State == domain.ApprovalStateApproved {
				result, err = coordinator.approvals.ConsumeApprovedAction(ctx, command)
			}
		}
		if err != nil && !errors.Is(err, ErrApprovalExpired) && !errors.Is(err, ErrApprovalInvalidated) &&
			!errors.Is(err, ErrApprovalExecutionFailed) && !errors.Is(err, ErrApprovalPatchOutcomeUnknown) &&
			!errors.Is(err, ErrApprovalRolloutTimedOut) && !errors.Is(err, ErrApprovalRolloutFailed) &&
			!errors.Is(err, ErrApprovalRolloutUnavailable) && !errors.Is(err, ErrApprovalResultAuditUnavailable) &&
			!errors.Is(err, ErrApprovalActionFailed) && !errors.Is(err, ErrApprovalActionOutcomeUnknown) &&
			!errors.Is(err, ErrApprovalActionVerificationFailed) {
			return UICommandOutcome{}, err
		}
		return UICommandOutcome{
			Command: command.Kind, RequestID: command.RequestID,
			Approval: &result, RunID: result.RunID,
		}, nil
	}

	requireIdle := command.Kind == UICommandAcceptResume || command.Kind == UICommandTightenRetention
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
		if result.Failure == "" {
			coordinator.confirmPendingResumeScope(command, result)
		}
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Scope: &result}, nil
	case UICommandSelectResource:
		result := coordinator.executeResourceCommand(command)
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Resource: &result}, nil
	case UICommandRenameSession:
		return coordinator.executeRenameCommand(ctx, command)
	case UICommandShowStatus:
		status := coordinator.uiStatus()
		return UICommandOutcome{Command: command.Kind, Status: &status}, nil
	case UICommandShowPermissions, UICommandChangePermission, UICommandCreateSessionRule:
		if coordinator.approvals == nil {
			return UICommandOutcome{}, ErrPermissionUnavailable
		}
		beforePermission, _ := coordinator.approvals.Status()
		changed, ruleCreated := false, false
		var err error
		switch command.Kind {
		case UICommandChangePermission:
			err = coordinator.approvals.ReconfigurePermission(ctx, command)
			changed = err == nil
		case UICommandCreateSessionRule:
			err = coordinator.approvals.CreateSessionRule(ctx, command)
			ruleCreated = err == nil
		}
		afterPermission, _ := coordinator.approvals.Status()
		if command.Kind != UICommandShowPermissions && beforePermission.PolicyGeneration.Valid() &&
			afterPermission.PolicyGeneration.Valid() &&
			afterPermission.PolicyGeneration != beforePermission.PolicyGeneration {
			coordinator.cancelRunAfterPermissionChange()
		}
		if err != nil {
			return UICommandOutcome{}, err
		}
		status := coordinator.uiStatus()
		permissions := UIPermissionsResult{
			RequestID: command.RequestID, Permission: status.Permission,
			Reviewer: status.ReviewerModel, Action: status.Action,
			Changed: changed, RuleCreated: ruleCreated,
		}
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Permissions: &permissions}, nil
	case UICommandShowPrivacy, UICommandAcceptPrivacy, UICommandRejectPrivacy,
		UICommandRevokePrivacy, UICommandToggleLogs, UICommandCancelPrivacy:
		return coordinator.executePrivacyCommand(ctx, command)
	case UICommandTightenRetention:
		return coordinator.executeRetentionCommand(ctx, command)
	case UICommandResumeSession:
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryUnavailable}, nil
	default:
		return UICommandOutcome{}, ErrInvalidUICommand
	}
}

func (coordinator *Coordinator) executeConversationInputCommand(
	ctx context.Context,
	command UICommand,
) (UICommandOutcome, error) {
	coordinator.mu.Lock()
	var sessionID domain.SessionID
	if coordinator.currentSession != nil {
		sessionID = coordinator.currentSession.ID
	}
	active := coordinator.active
	current := active != nil && !active.terminal && active.run.ID == command.RunID &&
		active.run.SessionID == sessionID && active.run.Scope.Generation == command.ExpectedScopeGeneration &&
		active.input.PolicyGeneration() == command.ExpectedPolicyGeneration
	coordinator.mu.Unlock()
	if !sessionID.Valid() || !current {
		return UICommandOutcome{}, ErrConversationInputUnavailable
	}

	var (
		projection ConversationInputProjection
		err        error
	)
	switch command.Kind {
	case UICommandSubmitSteer:
		projection, err = coordinator.SubmitSteer(ctx, SubmitSteerCommand{
			RunID: command.RunID, SessionID: sessionID,
			ExpectedScopeGeneration:  command.ExpectedScopeGeneration,
			ExpectedPolicyGeneration: command.ExpectedPolicyGeneration,
			Text:                     command.Text,
		})
	case UICommandEnqueueFollowUp:
		projection, err = coordinator.EnqueueFollowUp(ctx, EnqueueFollowUpCommand{
			RunID: command.RunID, SessionID: sessionID,
			ExpectedScopeGeneration:  command.ExpectedScopeGeneration,
			ExpectedPolicyGeneration: command.ExpectedPolicyGeneration,
			Text:                     command.Text,
		})
	case UICommandPopFollowUp:
		projection, err = coordinator.PopLastConversationInput(ctx, PopConversationInputCommand{
			SessionID: sessionID, RunID: command.RunID,
			ExpectedScopeGeneration:  command.ExpectedScopeGeneration,
			ExpectedPolicyGeneration: command.ExpectedPolicyGeneration,
		})
	default:
		err = ErrInvalidUICommand
	}
	if err != nil {
		return UICommandOutcome{}, err
	}
	return UICommandOutcome{
		Command: command.Kind, RequestID: command.RequestID, RunID: command.RunID,
		ConversationInput: &projection,
	}, nil
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
	if errors.Is(err, ErrModelUnconfigured) {
		return UICommandOutcome{Command: command.Kind, RequestID: command.RequestID, Failure: UIQueryModelRequired}, nil
	}
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
		lifecycle, err := coordinator.sessionLifecycleReview(ctx)
		if err != nil {
			coordinator.mu.Lock()
			if coordinator.privacyChallenge != nil && coordinator.privacyChallenge.requestID == command.RequestID &&
				coordinator.privacyChallenge.revision == review.Revision {
				coordinator.privacyChallenge = nil
			}
			coordinator.mu.Unlock()
			coordinator.markGlobalPersistenceDegraded()
			return UICommandOutcome{}, ErrPersistenceUnavailable
		}
		return UICommandOutcome{
			Command: command.Kind, RequestID: command.RequestID,
			Privacy: &review, Lifecycle: &lifecycle,
		}, nil
	}
	coordinator.mu.Lock()
	challenge := coordinator.privacyChallenge
	if challenge == nil || challenge.requestID != command.RequestID || challenge.revision != command.PrivacyRevision {
		coordinator.mu.Unlock()
		return UICommandOutcome{}, ErrPrivacyReviewStale
	}
	coordinator.mu.Unlock()
	var lifecycle *SessionLifecycleReview
	if command.Kind == UICommandToggleLogs {
		loaded, lifecycleErr := coordinator.sessionLifecycleReview(ctx)
		if lifecycleErr != nil {
			coordinator.markGlobalPersistenceDegraded()
			return UICommandOutcome{}, ErrPersistenceUnavailable
		}
		lifecycle = &loaded
	}
	coordinator.mu.Lock()
	challenge = coordinator.privacyChallenge
	if challenge == nil || challenge.requestID != command.RequestID || challenge.revision != command.PrivacyRevision {
		coordinator.mu.Unlock()
		return UICommandOutcome{}, ErrPrivacyReviewStale
	}
	coordinator.privacyChallenge = nil
	coordinator.mu.Unlock()
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
		coordinator.publishConversationInvalidation(ctx)
		coordinator.cancelActiveForPrivacyFailure()
		return UICommandOutcome{}, err
	}
	if command.Kind == UICommandRejectPrivacy || command.Kind == UICommandRevokePrivacy || command.Kind == UICommandToggleLogs {
		coordinator.publishConversationInvalidation(ctx)
		coordinator.cancelActiveForPrivacyFailure()
	}
	result := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	if command.Kind == UICommandToggleLogs {
		coordinator.mu.Lock()
		coordinator.privacyChallenge = &privacyChallenge{requestID: command.RequestID, revision: review.Revision}
		coordinator.mu.Unlock()
		result.Privacy = &review
		result.Lifecycle = lifecycle
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
	scopeConfirmed := pending.scopeConfirmed
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
	var scopeResult UIScopeResult
	if view.State == ScopeStateActive && view.Scope != nil {
		if !scopeConfirmed && record.Session.LastScope != nil &&
			(view.Scope.Context != record.Session.LastScope.Context || view.Scope.Namespace != record.Session.LastScope.Namespace) {
			outcome.Failure = UIQueryUnavailable
			scopeResult = failedUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, view, UIQueryUnavailable)
			outcome.Scope = &scopeResult
			return outcome
		}
		clearScopeResource(coordinator.uiScopes, *view.Scope)
		scopeResult = successfulUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, *view.Scope)
	} else {
		scopeResult = failedUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, view, UIQueryUnavailable)
		outcome.Failure = UIQueryUnavailable
	}
	outcome.Scope = &scopeResult
	if outcome.Failure != "" {
		return outcome
	}

	coordinator.mu.Lock()
	if coordinator.pendingResume == nil || coordinator.pendingResume.request.RequestID != command.RequestID {
		coordinator.mu.Unlock()
		outcome.Failure = UIQueryUnavailable
		return outcome
	}
	if coordinator.approvals != nil {
		coordinator.mu.Unlock()
		if err := coordinator.approvals.BindSessionAuthority(ctx, record.Session.ID); err != nil {
			outcome.Failure = UIQueryUnavailable
			return outcome
		}
		coordinator.mu.Lock()
		if coordinator.pendingResume == nil || coordinator.pendingResume.request.RequestID != command.RequestID {
			coordinator.mu.Unlock()
			outcome.Failure = UIQueryUnavailable
			return outcome
		}
	}
	copy := record.Session
	coordinator.currentSession = &copy
	coordinator.currentResumed = true
	coordinator.conversationInputs.reset()
	coordinator.modelContext = newSessionModelContext(copy, false)
	coordinator.lastDiagnosis = nil
	coordinator.lastEvidence = nil
	coordinator.pendingResume = nil
	coordinator.mu.Unlock()
	if _, contextErr := coordinator.conversationForRun(ctx, copy.ID); contextErr != nil {
		coordinator.mu.Lock()
		if coordinator.modelContext.sessionID == copy.ID {
			coordinator.modelContext.storageHealthy = false
		}
		coordinator.mu.Unlock()
	}
	outcome.Session = projectUISession(record.Session, true)
	projectionCopy := projection
	outcome.Resumed = &projectionCopy
	return outcome
}

func (coordinator *Coordinator) confirmPendingResumeScope(command UICommand, result UIScopeResult) {
	if coordinator == nil || result.Failure != "" || result.RequestID != command.RequestID {
		return
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.pendingResume != nil && coordinator.pendingResume.request.RequestID == command.RequestID {
		coordinator.pendingResume.scopeConfirmed = true
	}
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
	result := successfulUIScopeResult(command.RequestID, command.ExpectedScopeGeneration, scope)
	if command.Kind != UICommandSelectNamespace && coordinator.saveScopePreference(ctx, scope.Context) != nil {
		coordinator.setScopePreferenceDegraded(true)
		result.ScopePreferenceDegraded = true
	} else if command.Kind != UICommandSelectNamespace {
		coordinator.setScopePreferenceDegraded(false)
	}
	return result
}

func (coordinator *Coordinator) saveScopePreference(ctx context.Context, contextName string) error {
	preference := ScopePreference{
		Context:   contextName,
		UpdatedAt: coordinator.now().UTC().Truncate(time.Millisecond),
	}
	if preference.Validate() != nil {
		return ErrPersistenceUnavailable
	}
	return coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.scopePreferences.SaveLastContext(operationContext, preference)
	})
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
	return coordinator.uiScopes.ActivateScope(ctx, target, expectedGeneration)
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
	limits := coordinator.budgetLimits
	reviewerLimits, _ := agent.ReviewerBudgetLimitsForProfile(limits.Profile)
	resourcePolicies, _, _ := coordinator.runResourcePolicies.ResourcePolicySnapshot(context.Background())
	observabilityPolicies := domain.DisabledObservabilityPolicyCatalog()
	if source, ok := coordinator.runResourcePolicies.(RunObservabilityPolicySource); ok {
		if snapshot, _, current := source.ObservabilityPolicySnapshot(context.Background()); current {
			observabilityPolicies = snapshot
		}
	}
	_, prometheusEnabled := observabilityPolicies.Resolve(domain.DataSourcePrometheus)
	_, lokiEnabled := observabilityPolicies.Resolve(domain.DataSourceLoki)
	remoteDiagnostics := domain.DisabledRemoteDiagnosticsPolicyCatalog()
	if source, ok := coordinator.runResourcePolicies.(RunRemoteDiagnosticsPolicySource); ok {
		if snapshot, _, current := source.RemoteDiagnosticsPolicySnapshot(context.Background()); current {
			remoteDiagnostics = snapshot
		}
	}
	_, containerFileEnabled := remoteDiagnostics.ContainerFile()
	localCommands := domain.DisabledLocalCommandPolicyCatalog()
	localShells := domain.DisabledLocalShellPolicyCatalog()
	if coordinator.localProposals != nil {
		localCommands = coordinator.localProposals.CommandCatalog()
		localShells = coordinator.localProposals.ShellCatalog()
	}
	result := UIStatusResult{
		Session: coordinator.CurrentUISession(), CapabilityCatalogVersion: agent.ToolCatalogVersion,
		ResourcePolicyVersion: resourcePolicies.Version(), ResourceTypeCount: len(resourcePolicies.Entries()),
		ObservabilityPolicyVersion: observabilityPolicies.Version(), PrometheusEnabled: prometheusEnabled, LokiEnabled: lokiEnabled,
		RemoteDiagnosticsPolicyVersion: remoteDiagnostics.Version(), PodExecPolicyCount: len(remoteDiagnostics.PodExecPolicies()), DiagnosticPodPolicyCount: len(remoteDiagnostics.DiagnosticPodPolicies()), ContainerFileReadEnabled: containerFileEnabled,
		LocalExecutionPolicyVersion: localCommands.Version(), LocalCommandPolicyCount: len(localCommands.Policies()), LocalShellPolicyCount: len(localShells.Policies()),
		Budget: UIBudgetStatus{
			ModelEvidenceBasis: ModelBudgetEvidenceBasis,
			Profile:            limits.Profile, RunMilliseconds: limits.RunDuration.Milliseconds(),
			RemainingMilliseconds: limits.RunDuration.Milliseconds(),
			StepsMaximum:          limits.Steps, ToolCallsMaximum: limits.ToolCalls, ModelCallsMaximum: limits.ModelCalls,
			ModelCostUnitsMaximum: limits.ModelCostUnits,
			SummaryCallsMaximum:   limits.SummaryCalls, SummaryCostUnitsMaximum: limits.SummaryCostUnits,
			ReviewerCallsMaximum: reviewerLimits.Calls, ReviewerCostUnitsMaximum: reviewerLimits.CostUnits,
			ToolResultBytesMaximum: limits.RunToolResultBytes, LogCallsMaximum: limits.LogCalls,
			LogContainersMaximum: limits.LogContainers, LogBytesMaximum: limits.LogBytes,
			EventPagesMaximum: limits.EventPages, EventPageItemsMaximum: limits.EventPageItems,
			EventPageBytesMaximum: limits.EventPageBytes, EventBytesMaximum: limits.EventBytes,
			MetricCallsMaximum: limits.MetricCalls, MetricContainersMaximum: limits.MetricContainers, MetricBytesMaximum: limits.MetricBytes,
			DataSourceCallsMaximum: limits.DataSourceCalls, DataSourcePagesMaximum: limits.DataSourcePages,
			RemoteExecCallsMaximum:   limits.RemoteExecCalls,
			LocalProcessCallsMaximum: limits.LocalProcessCalls,
			DataSourceSeriesMaximum:  limits.DataSourceSeries, DataSourceSamplesMaximum: limits.DataSourceSamples,
			DataSourceLinesMaximum: limits.DataSourceLines, DataSourceBytesMaximum: limits.DataSourceBytes,
			DataSourceWindowMillis: limits.DataSourceWindow.Milliseconds(), DataSourceStepMillis: limits.DataSourceStep.Milliseconds(),
			ResourcePagesMaximum: limits.ResourcePages, ResourcePageItemsMaximum: limits.ResourcePageItems,
			ResourcePageBytesMaximum: limits.ResourcePageBytes,
			ResourceScannedMaximum:   limits.ResourceScannedItems, ResourceReturnedMaximum: limits.ResourceReturnedItems,
			ResourceBytesMaximum: limits.ResourceBytes,
		},
	}
	if coordinator.approvals != nil {
		permission, action := coordinator.approvals.Status()
		result.Permission = projectUIPermissionStatus(permission)
		result.Action = action
	}
	if coordinator.uiScopes != nil {
		view := coordinator.uiScopes.View()
		result.ScopeGeneration = view.Generation
		if view.State == ScopeStateActive && view.Scope != nil {
			result.Context = view.Scope.Context
			result.Namespace = view.Scope.Namespace
			result.NamespaceAccess = view.Scope.NamespaceAccess
			result.ReadOnly = true
		}
	}
	coordinator.mu.Lock()
	result.PersistenceDegraded = coordinator.persistenceDegraded || coordinator.scopePreferenceDegraded
	result.ConversationInput = coordinator.conversationInputStatusLocked()
	if coordinator.active != nil && !coordinator.active.terminal {
		state := coordinator.active
		result.RunID = state.run.ID
		result.RunActive = true
		result.PersistenceDegraded = result.PersistenceDegraded || state.persistenceBad
		result.Budget.StepsUsed = state.run.StepCount
		result.Budget.ToolCallsUsed = state.run.ToolCallCount
		result.Budget.ModelCallsUsed = state.run.ModelRequestCount
		result.Budget.ModelCostUnitsUsed = state.run.ModelRequestCount
		result.Budget.SummaryCallsUsed = state.summaryCalls
		result.Budget.SummaryCostUnitsUsed = state.summaryCalls
		result.Budget.ToolResultBytesUsed = state.toolResultBytes
		result.Budget.LogCallsUsed = state.logCalls
		result.Budget.MetricCallsUsed = state.metricCalls
		result.Budget.DataSourceCallsUsed = state.dataSourceCalls
		result.Budget.RemoteExecCallsUsed = state.remoteExecCalls
		result.Budget.LocalProcessCallsUsed = state.localProcessCalls
		if state.run.StartedAt != nil {
			elapsed := coordinator.now().Sub(*state.run.StartedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed > limits.RunDuration {
				elapsed = limits.RunDuration
			}
			result.Budget.ElapsedMilliseconds = elapsed.Milliseconds()
			result.Budget.RemainingMilliseconds = limits.RunDuration.Milliseconds() - result.Budget.ElapsedMilliseconds
		}
	}
	if coordinator.currentSession != nil {
		contextState := coordinator.modelContext
		result.ModelContext = UIModelContextStatus{
			Mode: contextState.mode, EligibleMessages: len(contextState.coverage), EligibleBytes: contextState.eligibleBytes,
			RecentTailMessages: contextState.recentTailCount, SummaryCallsMaximum: limits.SummaryCalls,
			StorageHealthy: contextState.storageHealthy,
		}
		if contextState.summary != nil {
			result.ModelContext.Compressed = true
			result.ModelContext.CompressedAtUnixMillis = contextState.summary.GeneratedAt.UnixMilli()
			result.ModelContext.CoveredThroughMessageID = contextState.summary.CoveredThroughID
		}
		if coordinator.active != nil && !coordinator.active.terminal {
			result.ModelContext.SummaryCallsUsed = coordinator.active.summaryCalls
		}
	}
	if coordinator.modelRuntime != nil {
		profile, originHash := coordinator.currentAgentBindingLocked()
		privacy := coordinator.privacy.Snapshot()
		result.AgentModel = UIModelRoleStatus{
			Role: domain.ModelRoleAgent, Profile: profile, OriginHash: originHash,
			Configured: true, Available: true, Consented: privacy.Accepted,
		}
	} else {
		result.AgentModel.Role = domain.ModelRoleAgent
	}
	if coordinator.reviewerModel != nil && coordinator.reviewerModel.valid() {
		privacy := coordinator.reviewerModel.Privacy.Snapshot()
		budget := coordinator.reviewerModel.Budget.Snapshot()
		result.ReviewerModel = UIModelRoleStatus{
			Role: domain.ModelRoleApprovalReviewer, Profile: coordinator.reviewerModel.Profile,
			OriginHash: privacy.OriginHash, Configured: true, Available: coordinator.reviewerModel.Available,
			Consented: privacy.Accepted && coordinator.reviewerModel.Available,
		}
		result.Budget.ReviewerCallsUsed = budget.Calls
		result.Budget.ReviewerCostUnitsUsed = budget.CostUnits
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
		projected := UIHistoryMessage{
			Role: message.Role, Format: message.Format, Content: message.Content,
		}
		if message.RunID != nil {
			projected.RunID = *message.RunID
		}
		result.History = append(result.History, projected)
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

func (coordinator *Coordinator) attachHistoryEvidence(
	ctx context.Context,
	record ResumedSessionRecord,
	projection *UIResumedSession,
) error {
	if coordinator.evidenceDetails == nil || projection == nil {
		return nil
	}
	for index, message := range record.Messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if index >= len(projection.History) || message.Role != domain.MessageRoleAssistant ||
			message.RunID == nil || message.Scope == nil {
			continue
		}
		diagnosis, found, err := coordinator.evidenceDetails.ReadDiagnosis(ctx, *message.RunID)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			continue
		}
		if !found || diagnosis.Validate() != nil || diagnosis.RunID != *message.RunID || diagnosis.Scope != *message.Scope {
			continue
		}
		references := projectUIEvidenceReferences(diagnosis, int64(index+1))
		valid := true
		for _, reference := range references {
			if reference.Validate() != nil {
				valid = false
				break
			}
		}
		if valid {
			projection.History[index].EvidenceReferences = references
		}
	}
	return nil
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
	if err := coordinator.beginUIOperation(true); err != nil {
		return domain.Session{}, err
	}
	defer coordinator.finishOperation()
	return coordinator.createSessionWithinOperation(ctx, command)
}

func (coordinator *Coordinator) createSessionWithinOperation(
	ctx context.Context,
	command CreateSessionCommand,
) (domain.Session, error) {
	coordinator.mu.Lock()
	persistenceDegraded := coordinator.persistenceDegraded
	coordinator.mu.Unlock()
	if persistenceDegraded {
		return domain.Session{}, ErrPersistenceUnavailable
	}
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
	if coordinator.approvals != nil {
		if err := coordinator.approvals.BindSessionAuthority(ctx, session.ID); err != nil {
			return domain.Session{}, ErrPermissionUnavailable
		}
	}
	coordinator.mu.Lock()
	copy := session
	coordinator.currentSession = &copy
	coordinator.currentResumed = false
	coordinator.conversationInputs.reset()
	coordinator.modelContext = newSessionModelContext(copy, true)
	coordinator.lastDiagnosis = nil
	coordinator.lastEvidence = nil
	coordinator.pendingResume = nil
	coordinator.startupResume = nil
	coordinator.mu.Unlock()
	return session, nil
}

// StartRun durably binds the safe request and running metadata before starting
// the sole owned Agent goroutine.
func (coordinator *Coordinator) StartRun(ctx context.Context, command StartRunCommand) (domain.AgentRunID, error) {
	return coordinator.startRun(ctx, command, "")
}

func (coordinator *Coordinator) startRun(
	ctx context.Context,
	command StartRunCommand,
	reservedMessageID domain.MessageID,
) (domain.AgentRunID, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return "", ErrCoordinatorDependency
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runContext, cancelRun := context.WithCancel(ctx)
	minimalPersistence := false
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
	if coordinator.runner == nil {
		coordinator.mu.Unlock()
		cancelRun()
		return "", ErrModelUnconfigured
	}
	runner := coordinator.runner
	if coordinator.currentSession != nil {
		if coordinator.currentSession.ID != command.SessionID {
			coordinator.mu.Unlock()
			cancelRun()
			return "", ErrCoordinatorDependency
		}
		minimalPersistence = coordinator.currentSession.PrivacyMode == domain.PrivacyModeMinimal
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

	persistOperationalDetail := false
	if !minimalPersistence {
		var retentionErr error
		persistOperationalDetail, retentionErr = coordinator.operationalDetailPersistence(runContext)
		if retentionErr != nil {
			coordinator.markGlobalPersistenceDegraded()
			return "", retentionErr
		}
	}

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
	if !current || scope.Validate() != nil || command.Resource != nil && !domain.ReferenceMatchesWorkingNamespace(*command.Resource, scope.Namespace) {
		return "", ErrScopeUnavailable
	}
	conversation, contextErr := coordinator.conversationForRun(runContext, command.SessionID)
	if contextErr != nil {
		coordinator.markGlobalPersistenceDegraded()
		return "", ErrPersistenceUnavailable
	}
	if currentScope, stillCurrent := coordinator.scope.CurrentScope(); !stillCurrent || currentScope != scope {
		return "", ErrScopeUnavailable
	}
	if authorized, privacyErr = coordinator.privacy.AuthorizeModel(runContext); privacyErr != nil || !authorized {
		if privacyErr != nil {
			coordinator.privacy.FailClosed()
			coordinator.markGlobalPersistenceDegraded()
			return "", ErrPersistenceUnavailable
		}
		return "", ErrConsentRequired
	}
	resourcePolicies, policyGeneration, policyCurrent := coordinator.runResourcePolicies.ResourcePolicySnapshot(runContext)
	if !policyCurrent || resourcePolicies.Validate() != nil || !policyGeneration.Valid() {
		return "", ErrCoordinatorDependency
	}
	observabilityPolicies := domain.DisabledObservabilityPolicyCatalog()
	if source, ok := coordinator.runResourcePolicies.(RunObservabilityPolicySource); ok {
		var observabilityGeneration domain.PolicyGeneration
		observabilityPolicies, observabilityGeneration, policyCurrent = source.ObservabilityPolicySnapshot(runContext)
		if !policyCurrent || observabilityPolicies.Validate() != nil || observabilityGeneration != policyGeneration {
			return "", ErrCoordinatorDependency
		}
	}
	remoteDiagnosticsPolicies := domain.DisabledRemoteDiagnosticsPolicyCatalog()
	if source, ok := coordinator.runResourcePolicies.(RunRemoteDiagnosticsPolicySource); ok {
		var remoteGeneration domain.PolicyGeneration
		remoteDiagnosticsPolicies, remoteGeneration, policyCurrent = source.RemoteDiagnosticsPolicySnapshot(runContext)
		if !policyCurrent || remoteDiagnosticsPolicies.Validate() != nil || remoteGeneration != policyGeneration {
			return "", ErrCoordinatorDependency
		}
	}
	localCommands := domain.DisabledLocalCommandPolicyCatalog()
	localShells := domain.DisabledLocalShellPolicyCatalog()
	if coordinator.localProposals != nil {
		localCommands = coordinator.localProposals.CommandCatalog()
		localShells = coordinator.localProposals.ShellCatalog()
		if localCommands.Validate() != nil || localShells.Validate() != nil {
			return "", ErrCoordinatorDependency
		}
	}
	runID, runErr := coordinator.identifiers.NewAgentRunID()
	messageID := reservedMessageID
	var messageErr error
	if messageID == "" {
		messageID, messageErr = coordinator.identifiers.NewMessageID()
	} else if !messageID.Valid() {
		messageErr = ErrConversationInputInvalid
	}
	startedAt := coordinator.now()
	if runErr != nil || messageErr != nil || !runID.Valid() || !messageID.Valid() || !validCoordinatorTime(startedAt) {
		return "", ErrCoordinatorDependency
	}
	input, err := agent.NewRunInputWithExecutionPolicyContext(
		runID, command.SessionID, messageID, processed.Value, scope, command.Resource, coordinator.budgetLimits, conversation,
		resourcePolicies, observabilityPolicies, remoteDiagnosticsPolicies, localCommands, localShells, policyGeneration,
	)
	if err != nil {
		return "", ErrCoordinatorDependency
	}
	input, err = agent.WithRunSteering(input, coordinator)
	if err != nil {
		return "", ErrCoordinatorDependency
	}
	if !coordinator.runResourcePolicies.CurrentPolicyGeneration(runContext, policyGeneration) {
		return "", ErrCoordinatorDependency
	}
	bridge, err := newEventBridge(runID, scope.Generation, policyGeneration, coordinator.uiEvents, processed.Value)
	if err != nil {
		return "", ErrCoordinatorDependency
	}
	state := &activeRun{
		input: input, runner: runner, cancel: cancelRun, done: make(chan struct{}), bridge: bridge,
		tools: make(map[domain.ToolInvocationID]*pendingTool), persistOperationalDetail: persistOperationalDetail,
		minimalPersistence: minimalPersistence,
	}
	requestMessage := domain.Message{
		ID: messageID, SessionID: command.SessionID, RunID: &runID,
		RunSequence: intPointer(0),
		Role:        domain.MessageRoleUser, Content: processed.Value, Format: domain.MessageFormatPlain,
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
	coordinator.mu.Lock()
	state := coordinator.active
	if state == nil || state.publishing || state.terminal || event.RunID != state.run.ID ||
		event.ScopeGeneration != state.run.Scope.Generation || event.Sequence != state.lastAgentSequence+1 ||
		state.lastAgentSequence == 0 && event.Kind != agent.RunEventRunStarted ||
		state.lastAgentSequence > 0 && event.Kind == agent.RunEventRunStarted ||
		runEventRequiresCurrentScope(event.Kind) && !coordinator.runScopeCurrentLocked(state) ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, state.input.PolicyGeneration()) {
		coordinator.mu.Unlock()
		return agent.EventSinkRejected
	}
	coordinator.mu.Unlock()
	modelAuthorized := true
	var privacyErr error
	if event.Kind == agent.RunEventModelStreamStarted || event.Kind == agent.RunEventSummaryStarted {
		modelAuthorized, privacyErr = coordinator.privacy.AuthorizeModel(ctx)
	}
	coordinator.mu.Lock()
	state = coordinator.active
	if state == nil || state.publishing || state.terminal || event.RunID != state.run.ID ||
		event.ScopeGeneration != state.run.Scope.Generation || event.Sequence != state.lastAgentSequence+1 ||
		state.lastAgentSequence == 0 && event.Kind != agent.RunEventRunStarted ||
		state.lastAgentSequence > 0 && event.Kind == agent.RunEventRunStarted ||
		runEventRequiresCurrentScope(event.Kind) && !coordinator.runScopeCurrentLocked(state) ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, state.input.PolicyGeneration()) {
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
	if event.Terminal() && event.Kind != agent.RunEventRunCompleted && coordinator.approvals != nil {
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
	if event.Kind == agent.RunEventSummaryReady && persistenceErr != nil {
		bridgeFailed = true
	}
	if persistenceFailed {
		bridgeFailed = coordinator.markRunPersistenceDegraded(ctx, state) != nil || bridgeFailed
	} else if persistenceErr != nil {
		bridgeFailed = true
	}
	if err := state.bridge.accept(ctx, event); err != nil {
		bridgeFailed = true
	} else if event.Kind == agent.RunEventDiagnosisReady {
		// A model-proposed action is not authority. The trusted preparer first
		// re-reads the exact Deployment and derives every digest-bound identity
		// field locally; inability to prepare an action does not invalidate the
		// otherwise valid answer.
		_ = coordinator.prepareActionProposal(ctx, state)
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

func runEventRequiresCurrentScope(kind agent.RunEventKind) bool {
	switch kind {
	case agent.RunEventRunFailed, agent.RunEventRunCancelled, agent.RunEventRunTimedOut,
		agent.RunEventRunStaleScope, agent.RunEventRunInterrupted:
		return false
	default:
		return true
	}
}

func (coordinator *Coordinator) runScopeCurrentLocked(state *activeRun) bool {
	if coordinator == nil || coordinator.scope == nil || state == nil {
		return false
	}
	current, ok := coordinator.scope.CurrentScope()
	return ok && current.Snapshot() == state.run.Scope
}

func (coordinator *Coordinator) prepareRestartProposal(ctx context.Context, state *activeRun) error {
	if coordinator == nil || coordinator.approvals == nil || coordinator.restartProposals == nil ||
		state == nil || state.diagnosis == nil || state.bridge == nil {
		return nil
	}
	var proposed *domain.RecommendedAction
	for index := range state.diagnosis.RecommendedActions {
		action := &state.diagnosis.RecommendedActions[index]
		if action.Operation != domain.ApprovalOperationRestartDeployment {
			continue
		}
		if proposed != nil {
			return ErrApprovalUnavailable
		}
		proposed = action
	}
	if proposed == nil {
		return nil
	}
	if proposed.Target == nil || proposed.Target.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		proposed.Target.Kind != domain.RestartDeploymentTargetKind ||
		proposed.Target.Namespace != state.run.Scope.Namespace || proposed.Target.UID != "" ||
		proposed.Target.ResourceVersion != "" {
		return ErrApprovalUnavailable
	}
	policy, liveScope, err := coordinator.approvals.PrepareRestartActionPolicy(state.run.SessionID, state.run.Scope)
	if err != nil {
		return err
	}
	preparedTarget, err := coordinator.restartProposals.PrepareRestartDeploymentProposal(
		ctx,
		state.run.Scope,
		*proposed.Target,
		proposed.Action,
	)
	if err != nil || preparedTarget.Validate() != nil || preparedTarget.Resource.Name != proposed.Target.Name {
		return ErrApprovalUnavailable
	}
	intent, err := NewRestartDeploymentActionIntent(policy, liveScope, preparedTarget, proposed.Action)
	if err != nil || intent.ValidateRestartDeployment() != nil || intent.Scope != state.run.Scope ||
		intent.Operation != domain.ActionOperationRestartDeployment ||
		intent.Target.Resource.Name != proposed.Target.Name || intent.ReasonSummary != proposed.Action {
		return ErrApprovalUnavailable
	}
	sequence := state.bridge.sequence + 1
	request, err := coordinator.approvals.SubmitRestartDeploymentProposal(
		ctx,
		state.run.ID,
		state.run.SessionID,
		sequence,
		intent,
	)
	if err != nil {
		return err
	}
	if request.Validate() != nil || request.RunID != state.run.ID || request.SessionID != state.run.SessionID ||
		request.Intent != intent || request.State != domain.ApprovalStatePending {
		return ErrApprovalUnavailable
	}
	state.bridge.sequence = sequence
	return nil
}

func (coordinator *Coordinator) prepareActionProposal(ctx context.Context, state *activeRun) error {
	if coordinator == nil || state == nil || state.diagnosis == nil {
		return nil
	}
	var proposed *domain.RecommendedAction
	for index := range state.diagnosis.RecommendedActions {
		action := &state.diagnosis.RecommendedActions[index]
		if action.Operation == "" {
			continue
		}
		if proposed != nil {
			return ErrApprovalUnavailable
		}
		proposed = action
	}
	if proposed == nil {
		return nil
	}
	switch proposed.Operation {
	case domain.ActionOperationRestartDeployment:
		return coordinator.prepareRestartProposal(ctx, state)
	case domain.ActionOperationScaleWorkload,
		domain.ActionOperationRollbackDeployment,
		domain.ActionOperationDeleteOwnedPod,
		domain.ActionOperationCordonNode,
		domain.ActionOperationUncordonNode,
		domain.ActionOperationDrainNode:
		return coordinator.prepareRemediationProposal(ctx, state, *proposed)
	case domain.ActionOperationRestrictedLocalArgv, domain.ActionOperationShell:
		if err := coordinator.reserveLocalProcessProposal(state); err != nil {
			return err
		}
		return coordinator.prepareLocalProposal(ctx, state, *proposed)
	default:
		return ErrApprovalUnavailable
	}
}

// reserveLocalProcessProposal atomically consumes the run's sole local-process
// opportunity before Namespace or filesystem inspection. The approved request
// is one-time, so one reservation can result in at most one process attempt.
func (coordinator *Coordinator) reserveLocalProcessProposal(state *activeRun) error {
	if coordinator == nil || state == nil {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.budgetLimits.LocalProcessCalls < 1 || state.localProcessCalls >= coordinator.budgetLimits.LocalProcessCalls {
		return ErrApprovalUnavailable
	}
	state.localProcessCalls++
	return nil
}

func (coordinator *Coordinator) prepareRemediationProposal(
	ctx context.Context,
	state *activeRun,
	proposed domain.RecommendedAction,
) error {
	if coordinator.approvals == nil || coordinator.remediationProposals == nil || state.bridge == nil || proposed.Target == nil {
		return ErrApprovalUnavailable
	}
	risk := domain.RiskReview
	if proposed.Operation == domain.ActionOperationRollbackDeployment || proposed.Operation == domain.ActionOperationDrainNode {
		risk = domain.RiskCritical
	}
	policy, liveScope, err := coordinator.approvals.PrepareActionPolicy(
		state.run.SessionID, state.run.Scope, proposed.Operation,
		domain.CapabilityEffectClusterMutation, risk, true,
	)
	if err != nil {
		return err
	}
	replicaTarget, revision := int64(0), int64(0)
	if proposed.Parameters != nil {
		value, ok := proposed.Parameters.Int64()
		if !ok {
			return ErrApprovalUnavailable
		}
		if proposed.Operation == domain.ActionOperationScaleWorkload {
			replicaTarget = value
		} else {
			revision = value
		}
	}
	plan, err := coordinator.remediationProposals.PrepareRemediationAction(ctx, RemediationProposalRequest{
		RunID: state.run.ID, SessionID: state.run.SessionID, Scope: liveScope,
		PolicyGeneration: policy.Generation, Operation: proposed.Operation,
		Target: *proposed.Target, ReplicaTarget: replicaTarget, Revision: revision,
		ReasonSummary: proposed.Action,
	})
	if err != nil || plan.Validate() != nil || plan.RunID != state.run.ID || plan.SessionID != state.run.SessionID ||
		plan.Scope.Snapshot() != state.run.Scope || plan.Target.Resource.Name != proposed.Target.Name {
		return ErrApprovalUnavailable
	}
	sequence := state.bridge.sequence + 1
	request, err := coordinator.approvals.SubmitRemediationAction(ctx, sequence, policy, plan)
	if err != nil {
		return err
	}
	if request.Validate() != nil || request.RunID != state.run.ID || request.SessionID != state.run.SessionID ||
		request.State != domain.ApprovalStatePending || !plan.MatchesIntent(request.Intent) {
		return ErrApprovalUnavailable
	}
	state.bridge.sequence = sequence
	return nil
}

func (coordinator *Coordinator) prepareLocalProposal(
	ctx context.Context,
	state *activeRun,
	proposed domain.RecommendedAction,
) error {
	if coordinator.approvals == nil || coordinator.localProposals == nil || state.bridge == nil || proposed.Target == nil ||
		proposed.Parameters == nil || proposed.Target.Name != state.run.Scope.Namespace {
		return ErrApprovalUnavailable
	}
	policyID := proposed.Parameters.Value
	risk := domain.RiskCritical
	if proposed.Operation == domain.ActionOperationRestrictedLocalArgv {
		policy, found := coordinator.localProposals.CommandPolicy(policyID)
		if !found || policy.Validate() != nil {
			return ErrPermissionDenied
		}
		risk = policy.Risk()
	} else if policy, found := coordinator.localProposals.ShellPolicy(policyID); !found || policy.Validate() != nil {
		return ErrPermissionDenied
	}
	permission, liveScope, err := coordinator.approvals.PrepareActionPolicy(
		state.run.SessionID, state.run.Scope, proposed.Operation,
		domain.CapabilityEffectLocalExecute, risk, true,
	)
	if err != nil {
		return err
	}
	sequence := state.bridge.sequence + 1
	var request domain.ApprovalRequest
	if proposed.Operation == domain.ActionOperationRestrictedLocalArgv {
		plan, prepareErr := coordinator.localProposals.PrepareCommand(
			ctx, state.run.ID, state.run.SessionID, liveScope, permission.Generation, policyID, proposed.Action,
		)
		if prepareErr != nil || plan.Validate() != nil {
			return ErrApprovalUnavailable
		}
		request, err = coordinator.approvals.SubmitLocalCommandAction(ctx, sequence, permission, plan)
	} else {
		plan, prepareErr := coordinator.localProposals.PrepareShell(
			ctx, state.run.ID, state.run.SessionID, liveScope, permission.Generation, policyID, proposed.Action,
		)
		if prepareErr != nil || plan.Validate() != nil {
			return ErrApprovalUnavailable
		}
		request, err = coordinator.approvals.SubmitLocalShellAction(ctx, sequence, permission, plan)
	}
	if err != nil {
		return err
	}
	if request.Validate() != nil || request.RunID != state.run.ID || request.SessionID != state.run.SessionID ||
		request.State != domain.ApprovalStatePending || request.Intent.Parameters.PolicyID != policyID {
		return ErrApprovalUnavailable
	}
	state.bridge.sequence = sequence
	return nil
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

// cancelRunAfterPermissionChange runs only after PermissionManager has
// advanced policy generation and invalidated dependent reviews, approvals,
// rules, and actions. The old run observes cancellation through its existing
// owner and cannot emit current-generation UI authority.
func (coordinator *Coordinator) cancelRunAfterPermissionChange() {
	if coordinator == nil {
		return
	}
	coordinator.publishConversationInvalidation(context.Background())
	coordinator.mu.Lock()
	var cancel context.CancelFunc
	if coordinator.active != nil && !coordinator.active.terminal {
		cancel = coordinator.active.cancel
	}
	coordinator.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SubmitRestartDeploymentProposal delegates the supervised proposal bridge to
// the optional approval coordinator. A composition without supervised actions
// leaves it nil.
func (coordinator *Coordinator) SubmitRestartDeploymentProposal(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	sequence int64,
	intent domain.OperationIntent,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || coordinator.approvals == nil || ctx == nil || ctx.Err() != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.deletingSession == sessionID || coordinator.active == nil || coordinator.active.terminal || coordinator.active.run.ID != runID ||
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
				coordinator.mu.Lock()
				runtime := coordinator.modelRuntime
				coordinator.modelRuntime = nil
				if runtime != nil {
					coordinator.runner = nil
				}
				coordinator.mu.Unlock()
				if runtime != nil {
					runtime.Close()
				}
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
		if state.run.ModelRequestCount > state.input.BudgetLimits().ModelCalls || state.run.StepCount > state.input.BudgetLimits().Steps {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		return persistenceAction{kind: appendAuditOnly, audit: auditSpec{
			eventType: domain.AuditEventModelRequested,
			actor:     domain.AuditActorAgent, outcome: domain.AuditOutcomeSuccess,
		}}, nil
	case agent.RunEventSummaryStarted:
		state.summaryCalls++
		if state.summaryCalls > state.input.BudgetLimits().SummaryCalls {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		return persistenceAction{kind: appendAuditOnly, audit: auditSpec{
			eventType: domain.AuditEventModelRequested,
			actor:     domain.AuditActorAgent, outcome: domain.AuditOutcomeSuccess,
		}}, nil
	case agent.RunEventSummaryReady:
		if coordinator.validateSummaryForRunLocked(state, *event.Summary) != nil {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		summary := *event.Summary
		return persistenceAction{kind: persistSessionSummary, summary: &summary}, nil
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
		if state.run.ToolCallCount > state.input.BudgetLimits().ToolCalls {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		if event.ToolInvocation.Name == domain.ToolNameGetPodLogs || event.ToolInvocation.Name == domain.ToolNameGetPreviousPodLogs {
			state.logCalls += event.ExternalCallCost
			if state.logCalls > state.input.BudgetLimits().LogCalls {
				return persistenceAction{}, ErrInvalidAgentEvent
			}
		}
		if event.ToolInvocation.Name == domain.ToolNameGetPodMetrics || event.ToolInvocation.Name == domain.ToolNameGetNodeMetrics {
			state.metricCalls += event.ExternalCallCost
			if state.metricCalls > state.input.BudgetLimits().MetricCalls {
				return persistenceAction{}, ErrInvalidAgentEvent
			}
		}
		if event.ToolInvocation.Name == domain.ToolNameQueryPrometheus || event.ToolInvocation.Name == domain.ToolNameQueryLoki {
			state.dataSourceCalls += event.ExternalCallCost
			if state.dataSourceCalls > state.input.BudgetLimits().DataSourceCalls {
				return persistenceAction{}, ErrInvalidAgentEvent
			}
		}
		if event.ToolInvocation.Name == domain.ToolNamePodExec || event.ToolInvocation.Name == domain.ToolNameReadContainerFile || event.ToolInvocation.Name == domain.ToolNameRunDiagnosticPod {
			state.remoteExecCalls += event.ExternalCallCost
			if state.remoteExecCalls > state.input.BudgetLimits().RemoteExecCalls {
				return persistenceAction{}, ErrInvalidAgentEvent
			}
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
		if terminal.ReturnedBytes > state.input.BudgetLimits().RunToolResultBytes-state.toolResultBytes {
			return persistenceAction{}, ErrInvalidAgentEvent
		}
		state.toolResultBytes += terminal.ReturnedBytes
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
			len(pending.evidence) >= pending.terminal.EvidenceCount ||
			event.Evidence.PolicyGeneration != state.input.PolicyGeneration() ||
			!runInputAllowsEvidence(state.input, *event.Evidence) {
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

func runInputAllowsEvidence(input agent.RunInput, evidence domain.Evidence) bool {
	resourceType := evidence.ResourceType
	if resourceType == (domain.ResourceType{}) {
		kind, found := domain.ResourceKindForReference(evidence.Resource)
		if !found {
			return false
		}
		resourceType = domain.BuiltInResourceType(kind)
	}
	policy, found := input.ResourcePolicies().Resolve(resourceType.ID)
	return found && policy.Type == resourceType && input.Scope().AllowsResourceReference(resourceType, evidence.Resource)
}

func (coordinator *Coordinator) performPersistence(ctx context.Context, state *activeRun, action persistenceAction) error {
	switch action.kind {
	case persistNone:
		return nil
	case persistToolEvidence:
		if state.minimalPersistence {
			return nil
		}
		if !state.persistOperationalDetail {
			return coordinator.appendRunAudit(ctx, state, action.audit)
		}
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
		if state.minimalPersistence && !action.audit.eventType.AllowedInMinimalPersistence() {
			return nil
		}
		return coordinator.appendRunAudit(ctx, state, action.audit)
	case persistSessionSummary:
		if action.summary == nil {
			return ErrPersistenceUnavailable
		}
		if !state.minimalPersistence {
			if coordinator.modelContextStore == nil {
				return ErrPersistenceUnavailable
			}
			if err := coordinator.persist(ctx, func(operationContext context.Context) error {
				return coordinator.modelContextStore.SaveSessionContextSummary(operationContext, *action.summary)
			}); err != nil {
				return err
			}
		}
		return coordinator.acceptStoredSummary(state, *action.summary)
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
	diagnosis := cloneDiagnosis(*state.diagnosis)
	if !state.persistOperationalDetail && len(diagnosis.ReferencedEvidenceIDs()) > 0 {
		diagnosis.EvidenceDetailsState = domain.EvidenceDetailExpired
	}
	message := domain.Message{
		ID: messageID, SessionID: run.SessionID, RunID: &run.ID,
		RunSequence: intPointer(1 + len(state.committedInputs)),
		Role:        domain.MessageRoleAssistant, Content: diagnosis.AnswerMarkdown,
		Format: domain.MessageFormatMarkdown, Status: domain.MessageStatusCommitted,
		Scope: scopeSnapshotPointer(run.Scope), Resource: cloneResource(run.Resource),
		Hash: domain.MessageContentHash(diagnosis.AnswerMarkdown), CreatedAt: finishedAt,
	}
	if message.Validate() != nil {
		return ErrPersistenceUnavailable
	}
	if state.minimalPersistence {
		if err := coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.runs.FinishWithAudit(operationContext, run, audit)
		}); err != nil {
			return err
		}
	} else {
		if err := coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.runs.CompleteWithAudit(operationContext, diagnosis, message, run, audit)
		}); err != nil {
			return err
		}
	}
	if err := coordinator.recordCompletedConversation(state, message.ID, diagnosis.AnswerMarkdown); err != nil {
		coordinator.mu.Lock()
		if coordinator.modelContext.sessionID == run.SessionID {
			coordinator.modelContext.storageHealthy = false
		}
		coordinator.mu.Unlock()
	}
	return nil
}

func intPointer(value int) *int {
	return &value
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

func (coordinator *Coordinator) setScopePreferenceDegraded(degraded bool) {
	coordinator.mu.Lock()
	coordinator.scopePreferenceDegraded = degraded
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) executeRun(ctx context.Context, state *activeRun) {
	outcome := state.runner.Run(ctx, state.input, coordinator)
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
	clean := state.terminal && state.terminalStatus == domain.AgentRunStatusCompleted && !state.persistenceBad &&
		outcome.Validate(state.input) == nil && outcome.Status == state.terminalStatus
	inputEvent, successor := coordinator.finishConversationInputRunLocked(state, clean)
	result := RunResult{
		RunID: state.run.ID, Status: state.terminalStatus,
		PersistenceDegraded: state.persistenceBad,
	}
	coordinator.lastResult = &result
	if state.terminalStatus == domain.AgentRunStatusCompleted && state.diagnosis != nil {
		diagnosis := cloneDiagnosis(*state.diagnosis)
		coordinator.lastDiagnosis = &diagnosis
		referenceIDs := diagnosis.ReferencedEvidenceIDs()
		referenced := make(map[domain.EvidenceID]struct{}, len(referenceIDs))
		for _, evidenceID := range referenceIDs {
			referenced[evidenceID] = struct{}{}
		}
		coordinator.lastEvidence = make(map[domain.EvidenceID]domain.Evidence, len(referenced))
		for _, pending := range state.tools {
			if pending == nil {
				continue
			}
			for _, evidence := range pending.evidence {
				if _, cited := referenced[evidence.ID]; !cited {
					continue
				}
				coordinator.lastEvidence[evidence.ID] = cloneEvidence(evidence)
			}
		}
	}
	if coordinator.active == state {
		coordinator.active = nil
	}
	close(state.done)
	coordinator.mu.Unlock()
	if inputEvent != nil {
		_ = coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), *inputEvent)
	}
	if successor != nil {
		coordinator.startQueuedSuccessor(context.WithoutCancel(ctx), successor)
	}
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
		Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The diagnostic run failed safely."},
	}
	terminalContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), coordinator.persistenceLimit)
	defer cancel()
	if err := coordinator.persistTerminal(terminalContext, state, event, terminalAudit(event)); err != nil {
		_ = coordinator.markRunPersistenceDegraded(terminalContext, state)
	}
	_ = state.bridge.forceFailed(terminalContext, "The diagnostic run failed safely.")
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
	if value.ObservedFrom != nil {
		current := *value.ObservedFrom
		copy.ObservedFrom = &current
	}
	if value.ObservedThrough != nil {
		current := *value.ObservedThrough
		copy.ObservedThrough = &current
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
		if value.RecommendedActions[index].Target != nil {
			target := *value.RecommendedActions[index].Target
			copy.RecommendedActions[index].Target = &target
		}
		if value.RecommendedActions[index].Parameters != nil {
			parameters := *value.RecommendedActions[index].Parameters
			copy.RecommendedActions[index].Parameters = &parameters
		}
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
