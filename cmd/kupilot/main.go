package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/llm/openaicompat"
	"github.com/imbrooklyn/kupilot/internal/persistence/filesystem"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	platformlogging "github.com/imbrooklyn/kupilot/internal/platform/logging"
	"github.com/imbrooklyn/kupilot/internal/security"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
	"github.com/imbrooklyn/kupilot/internal/tools"
	"github.com/imbrooklyn/kupilot/internal/tui"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, buildinfo.Read()))
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	info buildinfo.Info,
) int {
	return cli.Run(ctx, args, stdout, stderr, info, func(ctx context.Context, intent cli.StartIntent) error {
		return start(ctx, intent, info, stdout)
	})
}

func start(ctx context.Context, intent cli.StartIntent, info buildinfo.Info, stdout io.Writer) (returnErr error) {
	paths, err := config.SystemPaths()
	if err != nil {
		return err
	}
	loaded, err := config.Load(ctx, config.LoadOptions{
		Paths: paths,
		Overrides: config.Overrides{
			ConfigFile: config.StringOverride{Set: intent.Options.ConfigFileSet, Value: intent.Options.ConfigFile},
			Context:    config.StringOverride{Set: intent.Options.ContextSet, Value: intent.Options.Context},
			Namespace:  config.StringOverride{Set: intent.Options.NamespaceSet, Value: intent.Options.Namespace},
			NoColor:    config.BoolOverride{Set: intent.Options.NoColorSet, Value: intent.Options.NoColor},
		},
	})
	if err != nil {
		return err
	}
	if loaded.Model.Endpoint == "" && loaded.Model.Model == "" {
		return cli.UnavailableError{}
	}
	validatedModel, err := loaded.ValidatedModel()
	if err != nil {
		return err
	}

	composition := &runtimeComposition{}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), domain.MaxAgentRunDuration+15*time.Second)
		defer cancel()
		if closeErr := composition.Close(shutdownContext); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if loaded.Logging.Enabled {
		logSink, openErr := platformlogging.Open(ctx, platformlogging.Options{
			Directory: loaded.Paths.LogDir,
			Level:     configuredLogLevel(loaded.Logging.Level),
		})
		if openErr != nil {
			return openErr
		}
		composition.logSink = logSink
		logger = logSink.Logger
	}
	logger.InfoContext(ctx, platformlogging.EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
		"provider_kind", validatedModel.ProviderKind,
	)

	applicationVersion := info.Version
	if applicationVersion == "" {
		applicationVersion = "dev"
	}
	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: loaded.Paths.StateDir, ApplicationVersion: applicationVersion, CorrelationID: "composition",
	})
	if err != nil {
		return err
	}
	composition.database = database
	sessionRepository := sqlite.NewSessionRepository(database)
	messageRepository := sqlite.NewMessageRepository(database)
	runRepository := sqlite.NewAgentRunRepository(database)
	toolRepository := sqlite.NewToolInvocationRepository(database)
	evidenceRepository := sqlite.NewEvidenceRepository(database)
	diagnosisRepository := sqlite.NewDiagnosisRepository(database)
	auditRepository := sqlite.NewAuditRepository(database)
	retentionRepository := sqlite.NewRetentionRepository(database)
	privacyManager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: sqlite.NewPrivacyRepository(database), Origin: validatedModel.Origin, Now: utcNow,
	})
	if err != nil {
		return err
	}
	sessionService := sessioncontract.NewService(
		sessionRepository, sessionRepository, messageRepository, runRepository, runRepository,
	)
	sessionApplication := &applicationSessionAdapter{service: sessionService, retention: retentionRepository}
	evidenceApplication := &applicationEvidenceDetailAdapter{
		evidence: evidenceRepository, diagnoses: diagnosisRepository,
	}

	loader := kube.NewConfigLoader()
	factory, err := kube.NewClientFactory(loader, configuredExecPolicy(loaded.Kubernetes.ExecCredentials))
	if err != nil {
		return err
	}
	baseGateway, err := kube.NewGateway(factory)
	if err != nil {
		return err
	}
	runtimeGateway, err := kube.NewToolScopeBinding(baseGateway)
	if err != nil {
		return err
	}
	now := utcNow
	scopeManager, err := application.NewScopeManager(runtimeGateway, baseGateway, baseGateway, runtimeGateway, now)
	if err != nil {
		return err
	}
	composition.scope = scopeManager
	identifiers, err := application.NewIdentifierGenerator(now)
	if err != nil {
		return err
	}
	redactor := security.NewRedactor()
	toolHandlers, err := tools.NewReadOnlyToolCatalog(tools.ReadOnlyToolCatalogDependencies{
		Resources: tools.ResourceToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Events: tools.EventToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Logs: tools.LogToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager,
			EvidenceIDs: identifiers, Text: redactor, Policy: privacyLogDataPolicy{privacy: privacyManager}, Now: now,
		},
		Related: tools.RelatedToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
	})
	if err != nil {
		return err
	}

	secretSource := new(config.EnvironmentSecretSource)
	credential, err := secretSource.Read()
	if err != nil {
		return err
	}
	composition.credential = &credential
	modelAdapter, modelErr := openaicompat.New(modelConfiguration(validatedModel), composition.credential, logger)
	if modelErr != nil {
		return modelErr
	}
	composition.model = modelAdapter
	composition.credential = nil
	agentAdapter, err := einoadapter.New(einoadapter.Config{
		Model: modelAdapter, Tools: toolHandlers, ScopeGuard: scopeManager,
		Identifiers: identifiers, Now: now,
	})
	if err != nil {
		return err
	}
	composition.agent = agentAdapter
	deliveryStop := make(chan struct{})
	uiEventSink := &deliveryUIEventSink{events: make(chan application.UIEvent, 64), stopped: deliveryStop}
	coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
		Sessions: sessionRepository, Runs: runRepository, Tools: toolRepository,
		Audits: auditRepository, Scope: scopeManager,
		Runner: agentAdapter, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: redactor, Exports: sessionRepository, ExportFiles: filesystem.NewExportWriter(), ExportText: redactor,
		Privacy: privacyManager, UIEvents: uiEventSink, Observer: slogRunObserver{logger: logger},
		Now: now,
		UI: &application.CoordinatorUIConfig{
			Sessions: sessionApplication, Search: sessionRepository, Titles: sessionApplication, Startup: sessionApplication, Scopes: scopeManager,
			EvidenceDetail: evidenceApplication,
		},
	})
	if err != nil {
		return err
	}
	composition.coordinator = coordinator

	startIntent, err := applicationStartIntent(intent)
	if err != nil {
		return err
	}
	startIntent.ConfiguredContext = loaded.Context
	startIntent.ConfiguredNamespace = loaded.Namespace
	if startIntent.Validate() != nil {
		return application.ErrInvalidUIStartIntent
	}
	startResult, err := coordinator.StartUI(ctx, startIntent, domain.PrivacyModeStandard)
	if err != nil {
		return err
	}
	initialScope := tui.ScopeView{}
	if shouldActivateInitialScope(startIntent) {
		initialScope, err = activateInitialScope(ctx, coordinator, loaded)
		if err != nil {
			return err
		}
	} else if startResult.ScopeCandidate != nil {
		initialScope.Context = startResult.ScopeCandidate.Context
		initialScope.Namespace = startResult.ScopeCandidate.Namespace
	}
	model := tui.NewModel(tui.Config{
		NoColor:     loaded.NoColor,
		StartIntent: startIntent,
		Scope:       initialScope,
		ModelName:   validatedModel.Model,
		PrivacyMode: domain.PrivacyModeStandard,
	})
	if startResult.Session != nil {
		updated, _ := model.Update(tui.CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandNewSession, Session: startResult.Session,
		}})
		var ok bool
		model, ok = updated.(tui.Model)
		if !ok {
			return errors.New("TUI startup state is unavailable")
		}
	}

	requestContext, cancelRequests := context.WithCancel(ctx)
	eventContext, cancelEvents := context.WithCancel(ctx)
	requests := make(chan tea.Msg, 32)
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithOutput(stdout),
		tea.WithFilter(applicationRequestFilter(requests)),
	)
	var workers sync.WaitGroup
	workers.Add(2)
	go pumpApplicationRequests(requestContext, coordinator, program, requests, &workers)
	go pumpApplicationEvents(eventContext, program, uiEventSink.events, deliveryStop, &workers)

	_, runErr := program.Run()
	close(requests)
	cancelRequests()
	shutdownContext, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), domain.MaxAgentRunDuration+15*time.Second)
	shutdownErr := coordinator.Shutdown(shutdownContext)
	cancelShutdown()
	close(deliveryStop)
	cancelEvents()
	workers.Wait()
	return errors.Join(runErr, shutdownErr)
}

type applicationSessionAdapter struct {
	service   *sessioncontract.Service
	retention auditcontract.RetentionCleaner
}

type applicationEvidenceDetailAdapter struct {
	evidence  *sqlite.EvidenceRepository
	diagnoses *sqlite.DiagnosisRepository
}

func (adapter *applicationEvidenceDetailAdapter) ReadDiagnosis(
	ctx context.Context,
	runID domain.AgentRunID,
) (domain.Diagnosis, bool, error) {
	if adapter == nil || adapter.diagnoses == nil {
		return domain.Diagnosis{}, false, application.ErrPersistenceUnavailable
	}
	diagnosis, err := adapter.diagnoses.GetByRunID(ctx, runID)
	if errors.Is(err, sqlite.ErrDiagnosisNotFound) {
		return domain.Diagnosis{}, false, nil
	}
	return diagnosis, err == nil, err
}

func (adapter *applicationEvidenceDetailAdapter) ReadEvidence(
	ctx context.Context,
	evidenceID domain.EvidenceID,
) (domain.Evidence, bool, error) {
	if adapter == nil || adapter.evidence == nil {
		return domain.Evidence{}, false, application.ErrPersistenceUnavailable
	}
	evidence, err := adapter.evidence.GetByID(ctx, evidenceID)
	if errors.Is(err, sqlite.ErrEvidenceNotFound) {
		return domain.Evidence{}, false, nil
	}
	return evidence, err == nil, err
}

func (adapter *applicationSessionAdapter) ListResumable(
	ctx context.Context,
	limit int,
) ([]application.ResumeSessionRecord, error) {
	page, err := adapter.service.ListResumable(ctx, sessioncontract.ResumePageRequest{Limit: limit})
	if err != nil {
		return nil, mapSessionResumeError(err)
	}
	result := make([]application.ResumeSessionRecord, len(page.Sessions))
	for index, candidate := range page.Sessions {
		result[index] = application.ResumeSessionRecord{
			ID: candidate.ID, Title: candidate.Title, UpdatedAt: candidate.UpdatedAt,
			PrivacyMode: candidate.PrivacyMode, LastScope: cloneScope(candidate.LastScope),
		}
	}
	return result, nil
}

func (adapter *applicationSessionAdapter) ResumeByID(
	ctx context.Context,
	id domain.SessionID,
) (application.ResumedSessionRecord, error) {
	history, err := adapter.service.ResumeByID(ctx, id)
	if err != nil {
		return application.ResumedSessionRecord{}, mapSessionResumeError(err)
	}
	return application.ResumedSessionRecord{
		Session: history.Session, Messages: append([]domain.Message(nil), history.Messages...),
	}, nil
}

func (adapter *applicationSessionAdapter) ResumeLatest(ctx context.Context) (application.ResumedSessionRecord, error) {
	candidate, err := adapter.service.GetLatestResumable(ctx)
	if err != nil {
		return application.ResumedSessionRecord{}, mapSessionResumeError(err)
	}
	return adapter.ResumeByID(ctx, candidate.ID)
}

func (adapter *applicationSessionAdapter) Rename(ctx context.Context, record application.RenameSessionRecord) error {
	return adapter.service.Rename(ctx, sessioncontract.RenameSession{
		ID: record.SessionID, Title: record.Title, ExpectedVersion: record.ExpectedVersion, UpdatedAt: record.UpdatedAt,
	})
}

func (adapter *applicationSessionAdapter) RecoverInterrupted(ctx context.Context, recoveredAt time.Time) error {
	_, err := adapter.service.RecoverInterruptedRuns(ctx, recoveredAt)
	return err
}

func (adapter *applicationSessionAdapter) CleanupRetention(ctx context.Context, now time.Time) error {
	_, err := adapter.retention.Cleanup(ctx, auditcontract.CleanupRequest{
		Now: now, OperationalDetailRetentionDays: auditcontract.DefaultOperationalDetailRetentionDays,
		BatchSize: auditcontract.MaxCleanupBatchSize,
	})
	return err
}

func mapSessionResumeError(err error) error {
	switch {
	case errors.Is(err, sessioncontract.ErrSessionNotResumable):
		return application.ErrSessionNotResumable
	case errors.Is(err, sessioncontract.ErrNoResumableSession):
		return application.ErrNoResumableSession
	case errors.Is(err, sessioncontract.ErrSessionUnavailable), errors.Is(err, sessioncontract.ErrSessionNotFound):
		return application.ErrSessionResumeUnavailable
	default:
		return err
	}
}

func cloneScope(scope *domain.ScopeCandidate) *domain.ScopeCandidate {
	if scope == nil {
		return nil
	}
	copy := *scope
	return &copy
}

func applicationStartIntent(intent cli.StartIntent) (application.UIStartIntent, error) {
	explicitScope := intent.Options.ContextSet || intent.Options.NamespaceSet
	configuredContext := ""
	configuredNamespace := ""
	if intent.Options.ContextSet {
		configuredContext = intent.Options.Context
	}
	if intent.Options.NamespaceSet {
		configuredNamespace = intent.Options.Namespace
	}
	base := application.UIStartIntent{
		ExplicitScope: explicitScope, ConfiguredContext: configuredContext, ConfiguredNamespace: configuredNamespace,
	}
	switch intent.Kind {
	case cli.IntentNew:
		base.Kind = application.UIStartNew
		return base, nil
	case cli.IntentResumePicker:
		base.Kind = application.UIStartResumePicker
		return base, nil
	case cli.IntentResumeID:
		result := base
		result.Kind = application.UIStartResumeID
		result.SessionID = domain.SessionID(intent.SessionID)
		if result.Validate() != nil {
			return application.UIStartIntent{}, application.ErrInvalidUIStartIntent
		}
		return result, nil
	case cli.IntentResumeLast:
		base.Kind = application.UIStartResumeLast
		return base, nil
	default:
		return application.UIStartIntent{}, application.ErrInvalidUIStartIntent
	}
}

func shouldActivateInitialScope(intent application.UIStartIntent) bool {
	return intent.Kind == application.UIStartNew || intent.ExplicitScope
}

func activateInitialScope(
	ctx context.Context,
	coordinator *application.Coordinator,
	loaded config.Config,
) (tui.ScopeView, error) {
	contextName := loaded.Context
	if contextName == "" && loaded.Namespace == "" {
		return tui.ScopeView{}, nil
	}
	if contextName == "" {
		completion, err := coordinator.QueryUI(ctx, application.UICompletionQuery{
			RequestID: 1, Kind: application.UICompletionContext, Limit: application.MaxUIQueryCandidates,
		})
		if err != nil || completion.Failure != "" {
			return tui.ScopeView{}, initialScopeError{}
		}
		for _, candidate := range completion.Contexts {
			if candidate.Current {
				contextName = candidate.Name
				break
			}
		}
		if contextName == "" {
			return tui.ScopeView{}, initialScopeError{}
		}
	}
	contextOutcome, err := coordinator.ExecuteUICommand(ctx, application.UICommand{
		Kind: application.UICommandSelectContext, RequestID: 2, Text: contextName,
	})
	if err != nil || contextOutcome.Scope == nil || contextOutcome.Scope.Failure != "" {
		return tui.ScopeView{}, initialScopeError{}
	}
	result := contextOutcome.Scope
	if loaded.Namespace != "" && loaded.Namespace != result.Namespace {
		namespaceOutcome, namespaceErr := coordinator.ExecuteUICommand(ctx, application.UICommand{
			Kind: application.UICommandSelectNamespace, RequestID: 3, Text: loaded.Namespace,
			ExpectedScopeGeneration: result.ScopeGeneration,
		})
		if namespaceErr != nil || namespaceOutcome.Scope == nil || namespaceOutcome.Scope.Failure != "" {
			return tui.ScopeView{}, initialScopeError{}
		}
		result = namespaceOutcome.Scope
	}
	return tui.ScopeView{
		Context: result.Context, Namespace: result.Namespace,
		Generation: result.ScopeGeneration, ReadOnly: result.ReadOnly,
	}, nil
}

type initialScopeError struct{}

func (initialScopeError) Error() string { return "initial Kubernetes scope unavailable" }
func (initialScopeError) SafeMessage() string {
	return "The configured Kubernetes scope could not be verified safely."
}

type deliveryUIEventSink struct {
	events  chan application.UIEvent
	stopped <-chan struct{}
}

func (sink *deliveryUIEventSink) PublishUIEvent(ctx context.Context, event application.UIEvent) error {
	if sink == nil || ctx == nil || event.Validate() != nil {
		return application.ErrInvalidUIEvent
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sink.stopped:
		return context.Canceled
	case sink.events <- event:
		return nil
	}
}

func applicationRequestFilter(requests chan<- tea.Msg) func(tea.Model, tea.Msg) tea.Msg {
	return func(_ tea.Model, message tea.Msg) tea.Msg {
		switch message.(type) {
		case tui.ApplicationCommandMsg, tui.ApplicationQueryMsg, tui.ApplicationResumeMsg, tui.ApplicationEvidenceDetailMsg:
			select {
			case requests <- message:
				return nil
			default:
				return rejectedApplicationRequest(message)
			}
		default:
			return message
		}
	}
}

func rejectedApplicationRequest(message tea.Msg) tui.ApplicationFailureMsg {
	result := tui.ApplicationFailureMsg{Message: "The requested operation is busy."}
	switch request := message.(type) {
	case tui.ApplicationQueryMsg:
		result.RequestID = request.Query.RequestID
		result.ScopeGeneration = request.Query.ScopeGeneration
		result.Query = request.Query.Kind
	case tui.ApplicationResumeMsg:
		result.RequestID = request.Request.RequestID
		result.Resume = request.Request.Mode
	case tui.ApplicationEvidenceDetailMsg:
		result.RequestID = request.Query.RequestID
		result.ScopeGeneration = request.Query.Reference.Scope.Generation
		result.RunID = request.Query.Reference.RunID
		result.Evidence = request.Query.Reference
	case tui.ApplicationCommandMsg:
		result.RequestID = request.Command.RequestID
		result.ScopeGeneration = request.Command.ExpectedScopeGeneration
		result.RunID = request.Command.RunID
		result.Command = request.Command.Kind
	}
	return result
}

func pumpApplicationRequests(
	ctx context.Context,
	consumer tui.ApplicationConsumer,
	program *tea.Program,
	requests <-chan tea.Msg,
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case message, open := <-requests:
			if !open {
				return
			}
			program.Send(tui.DispatchApplication(ctx, consumer, message))
		}
	}
}

func pumpApplicationEvents(
	ctx context.Context,
	program *tea.Program,
	events <-chan application.UIEvent,
	stopped <-chan struct{},
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stopped:
			return
		case event, open := <-events:
			if !open {
				return
			}
			program.Send(tui.ApplicationEventMsg{Event: event})
		}
	}
}

type coordinatorLifecycle interface {
	Shutdown(context.Context) error
}

type waitCloser interface {
	Close()
}

type scopeLifecycle interface {
	Close() error
}

// runtimeComposition owns shutdown in dependency order. It is lifecycle state,
// not a service locator: use cases receive their dependencies explicitly above.
type runtimeComposition struct {
	mu sync.Mutex

	coordinator coordinatorLifecycle
	agent       waitCloser
	model       waitCloser
	credential  *config.SecretValue
	scope       scopeLifecycle
	database    io.Closer
	logSink     io.Closer
	closed      bool
}

func (composition *runtimeComposition) Close(ctx context.Context) error {
	if composition == nil {
		return nil
	}
	composition.mu.Lock()
	defer composition.mu.Unlock()
	if composition.closed {
		return nil
	}
	if composition.coordinator != nil {
		if err := composition.coordinator.Shutdown(ctx); err != nil {
			return err
		}
	}
	if composition.agent != nil {
		composition.agent.Close()
	}
	if composition.model != nil {
		composition.model.Close()
	} else if composition.credential != nil {
		composition.credential.Destroy()
	}
	var closeErrors []error
	if composition.scope != nil {
		closeErrors = append(closeErrors, composition.scope.Close())
	}
	if composition.database != nil {
		closeErrors = append(closeErrors, composition.database.Close())
	}
	if composition.logSink != nil {
		closeErrors = append(closeErrors, composition.logSink.Close())
	}
	composition.closed = true
	return errors.Join(closeErrors...)
}

type privacyLogDataPolicy struct {
	privacy *application.PrivacyManager
}

func (policy privacyLogDataPolicy) AuthorizeLogRead(ctx context.Context, _ tools.LogPolicyRequest) tools.LogPolicyDecision {
	if policy.privacy == nil {
		return tools.LogPolicyDenied
	}
	switch policy.privacy.AuthorizeLogs(ctx) {
	case application.PrivacyLogAllowed:
		return tools.LogPolicyAllowed
	case application.PrivacyLogConsentRequired:
		return tools.LogPolicyConsentRequired
	default:
		return tools.LogPolicyDenied
	}
}

type slogRunObserver struct {
	logger *slog.Logger
}

func (observer slogRunObserver) ObserveRun(ctx context.Context, observation application.RunObservation) {
	if observer.logger == nil {
		return
	}
	outcome := "success"
	if observation.Status == domain.AgentRunStatusCancelled {
		outcome = "cancelled"
	} else if observation.Status.Terminal() && observation.Status != domain.AgentRunStatusCompleted ||
		observation.Kind == application.RunObservationPersistenceDegraded {
		outcome = "failure"
	}
	observer.logger.InfoContext(ctx, platformlogging.EventAgentRun,
		"component", "application",
		"operation", "run_lifecycle",
		"phase", string(observation.Kind),
		"outcome", outcome,
		"scope_generation", observation.ScopeGeneration,
		"degraded", observation.PersistenceDegraded,
	)
}

func modelConfiguration(value config.ModelConfig) domain.ModelConfiguration {
	return domain.ModelConfiguration{
		ProviderKind: domain.ModelProviderOpenAICompatible,
		Endpoint:     value.Endpoint, Origin: value.Origin, Model: value.Model,
		APIKeySource: domain.ModelAPIKeySourceEnvironment,
		Temperature:  value.Temperature, MaxOutputTokens: value.MaxOutputTokens,
		RequestTimeout:    time.Duration(value.RequestTimeoutSeconds) * time.Second,
		StreamingRequired: value.Streaming, ToolCallingRequired: value.ToolCallingRequired,
		TransportPolicy: domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
}

func configuredExecPolicy(value string) kube.ExecCredentialPolicy {
	if value == config.ExecCredentialsDeny {
		return kube.ExecCredentialsDeny
	}
	return kube.ExecCredentialsAllow
}

func configuredLogLevel(value string) slog.Level {
	switch value {
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func utcNow() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}
