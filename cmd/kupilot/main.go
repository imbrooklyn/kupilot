package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/agent"
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
		return start(ctx, intent, info, stdout, stderr)
	})
}

func start(ctx context.Context, intent cli.StartIntent, info buildinfo.Info, stdout, stderr io.Writer) (returnErr error) {
	paths, err := config.SystemPaths()
	if err != nil {
		return err
	}
	if intent.Kind == cli.IntentCacheClear {
		if err := config.ClearCache(ctx, paths); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, "KuPilot cache cleared.")
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
	defer loaded.Credential.Destroy()
	for _, warning := range loaded.Warnings {
		_, _ = fmt.Fprintln(stderr, "Warning:", warning)
	}
	if err := config.EnsureHome(ctx, loaded.Paths); err != nil {
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
			_, _ = fmt.Fprintln(stderr, "Warning: local operational logging is unavailable and has been disabled for this process.")
		} else {
			composition.logSink = logSink
			logger = logSink.Logger
		}
	}
	logger.InfoContext(ctx, platformlogging.EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
		"provider_kind", loaded.Model.ProviderKind,
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
		Store: sqlite.NewPrivacyRepository(database), Origin: loaded.Model.Origin, Now: utcNow,
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

	modelFactory := &compositionModelFactory{
		base: loaded.Config, tools: toolHandlers, scope: scopeManager,
		identifiers: identifiers, now: now, logger: logger,
	}
	profileWriter := &compositionModelProfileWriter{paths: loaded.Paths, base: loaded.Config}
	var initialRuntime application.ModelRuntime
	if loaded.Model.Endpoint != "" && loaded.Model.Model != "" && loaded.Credential.IsSet() {
		setupSecret, secretErr := applicationSecret(&loaded.Credential)
		loaded.Credential.Destroy()
		if secretErr == nil {
			request := application.ModelSetupRequest{
				RequestID: 1, Endpoint: loaded.Model.Endpoint, Model: loaded.Model.Model, Secret: setupSecret,
			}
			initialRuntime, err = modelFactory.BuildModelRuntime(ctx, request)
			setupSecret.Destroy()
		}
		if secretErr != nil || err != nil {
			initialRuntime = nil
			_, _ = fmt.Fprintln(stderr, "Warning: the configured model runtime could not be constructed; use /model to configure it in the TUI.")
		}
	} else {
		loaded.Credential.Destroy()
	}
	runtimeTransferred := false
	defer func() {
		if initialRuntime != nil && !runtimeTransferred {
			initialRuntime.Close()
		}
	}()
	deliveryStop := make(chan struct{})
	uiEventSink := &deliveryUIEventSink{events: make(chan application.UIEvent, 64), stopped: deliveryStop}
	coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
		Sessions: sessionRepository, Runs: runRepository, Tools: toolRepository,
		Audits: auditRepository, Scope: scopeManager,
		ModelRuntime: initialRuntime, ModelFactory: modelFactory, ModelProfiles: profileWriter,
		Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: redactor, Exports: sessionRepository, ExportFiles: filesystem.NewExportWriter(), ExportText: redactor,
		Privacy: privacyManager, UIEvents: uiEventSink, Observer: slogRunObserver{logger: logger},
		Now: now,
		UI: &application.CoordinatorUIConfig{
			Sessions: sessionApplication, Search: sessionRepository, Titles: sessionApplication, Startup: sessionApplication, Scopes: scopeManager,
			EvidenceDetail: evidenceApplication, LocalState: database,
		},
	})
	if err != nil {
		return err
	}
	composition.coordinator = coordinator
	runtimeTransferred = true

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
		initialScope, err = activateInitialScope(ctx, coordinator, loaded.Config)
		if err != nil {
			return err
		}
	} else if startResult.ScopeCandidate != nil {
		initialScope.Context = startResult.ScopeCandidate.Context
		initialScope.Namespace = startResult.ScopeCandidate.Namespace
	}
	model := tui.NewModel(tui.Config{
		NoColor:            loaded.NoColor,
		StartIntent:        startIntent,
		Scope:              initialScope,
		ModelEndpoint:      loaded.Model.Endpoint,
		ModelName:          loaded.Model.Model,
		ModelConfigured:    initialRuntime != nil,
		ModelConfiguredSet: true,
		PrivacyMode:        domain.PrivacyModeStandard,
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
		tea.WithFilter(applicationRequestFilter(requestContext, requests)),
	)
	var workers sync.WaitGroup
	workers.Add(2)
	go pumpApplicationRequests(requestContext, coordinator, program, requests, &workers)
	go pumpApplicationEvents(eventContext, program, uiEventSink.events, deliveryStop, &workers)

	_, runErr := program.Run()
	cancelRequests()
	close(requests)
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

func applicationRequestFilter(ctx context.Context, requests chan<- tea.Msg) func(tea.Model, tea.Msg) tea.Msg {
	return func(_ tea.Model, message tea.Msg) tea.Msg {
		switch message.(type) {
		case tui.ApplicationCommandMsg, tui.ApplicationModelSetupMsg, tui.ApplicationQueryMsg, tui.ApplicationResumeMsg, tui.ApplicationEvidenceDetailMsg:
			if ctx == nil || ctx.Err() != nil {
				return rejectedApplicationRequest(message)
			}
			select {
			case <-ctx.Done():
				return rejectedApplicationRequest(message)
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
	case tui.ApplicationModelSetupMsg:
		destroyApplicationRequest(request)
		result.RequestID = request.Request.RequestID
		result.ModelSetup = true
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
	defer func() {
		destroyPendingApplicationRequests(requests)
		workers.Done()
	}()
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

func destroyPendingApplicationRequests(requests <-chan tea.Msg) {
	for {
		select {
		case message, open := <-requests:
			if !open {
				return
			}
			destroyApplicationRequest(message)
		default:
			return
		}
	}
}

func destroyApplicationRequest(message tea.Msg) {
	if request, ok := message.(tui.ApplicationModelSetupMsg); ok {
		request.Request.Secret.Destroy()
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

type scopeLifecycle interface {
	Close() error
}

type compositionModelFactory struct {
	base        config.Config
	tools       agent.ToolHandlers
	scope       agent.RunScopeGuard
	identifiers agent.RunIdentifierSource
	now         func() time.Time
	logger      *slog.Logger
}

func (factory *compositionModelFactory) BuildModelRuntime(
	ctx context.Context,
	request application.ModelSetupRequest,
) (application.ModelRuntime, error) {
	if factory == nil || ctx == nil || ctx.Err() != nil || request.Validate() != nil {
		return nil, application.ErrModelSetupInvalid
	}
	settings := factory.base
	settings.Model.Endpoint = request.Endpoint
	settings.Model.Origin = ""
	settings.Model.Model = request.Model
	if err := config.Validate(&settings); err != nil {
		return nil, err
	}
	credential, err := configurationSecret(request.Secret)
	if err != nil {
		return nil, err
	}
	modelAdapter, modelErr := openaicompat.New(modelConfiguration(settings.Model), &credential, factory.logger)
	if modelErr != nil {
		credential.Destroy()
		return nil, modelErr
	}
	agentAdapter, err := einoadapter.New(einoadapter.Config{
		Model: modelAdapter, Tools: factory.tools, ScopeGuard: factory.scope,
		Identifiers: factory.identifiers, Now: factory.now,
	})
	if err != nil {
		modelAdapter.Close()
		return nil, err
	}
	return &compositionModelRuntime{
		agent: agentAdapter, model: modelAdapter,
		name: settings.Model.Model, origin: settings.Model.Origin,
	}, nil
}

type compositionModelProfileWriter struct {
	paths config.Paths
	base  config.Config
}

func (writer *compositionModelProfileWriter) SaveModelProfile(
	ctx context.Context,
	request application.ModelSetupRequest,
) error {
	if writer == nil || request.Validate() != nil {
		return application.ErrModelSetupInvalid
	}
	credential, err := configurationSecret(request.Secret)
	if err != nil {
		return err
	}
	defer credential.Destroy()
	return config.SaveModelProfile(ctx, writer.paths, writer.base, config.ModelProfile{
		Endpoint: request.Endpoint, Model: request.Model,
	}, &credential)
}

type compositionModelRuntime struct {
	once   sync.Once
	agent  *einoadapter.Adapter
	model  *openaicompat.Adapter
	name   string
	origin string
}

func (runtime *compositionModelRuntime) Run(
	ctx context.Context,
	input agent.RunInput,
	sink agent.EventSink,
) agent.RunOutcome {
	if runtime == nil || runtime.agent == nil {
		return agent.RunOutcome{}
	}
	return runtime.agent.Run(ctx, input, sink)
}

func (runtime *compositionModelRuntime) Close() {
	if runtime == nil {
		return
	}
	runtime.once.Do(func() {
		if runtime.agent != nil {
			runtime.agent.Close()
		}
		if runtime.model != nil {
			runtime.model.Close()
		}
	})
}

func (runtime *compositionModelRuntime) ModelName() string {
	if runtime == nil {
		return ""
	}
	return runtime.name
}

func (runtime *compositionModelRuntime) Origin() string {
	if runtime == nil {
		return ""
	}
	return runtime.origin
}

func applicationSecret(secret *config.SecretValue) (*application.ModelSetupSecret, error) {
	var (
		result *application.ModelSetupSecret
		err    error
	)
	if secret == nil {
		return nil, application.ErrModelSetupInvalid
	}
	if useErr := secret.Use(func(value string) { result, err = application.NewModelSetupSecret(value) }); useErr != nil {
		return nil, useErr
	}
	return result, err
}

func configurationSecret(secret *application.ModelSetupSecret) (config.SecretValue, error) {
	var (
		result config.SecretValue
		err    error
	)
	if secret == nil {
		return config.SecretValue{}, application.ErrModelSetupInvalid
	}
	if useErr := secret.Use(func(value string) { result, err = config.NewSecretValue(value) }); useErr != nil {
		return config.SecretValue{}, useErr
	}
	return result, err
}

// runtimeComposition owns shutdown in dependency order. It is lifecycle state,
// not a service locator: use cases receive their dependencies explicitly above.
type runtimeComposition struct {
	mu sync.Mutex

	coordinator coordinatorLifecycle
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
		APIKeySource: domain.ModelAPIKeySourceRuntime,
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
