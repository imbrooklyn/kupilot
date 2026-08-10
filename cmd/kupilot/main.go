package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/llm/openaicompat"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	platformlogging "github.com/imbrooklyn/kupilot/internal/platform/logging"
	"github.com/imbrooklyn/kupilot/internal/security"
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
		return start(ctx, intent, info)
	})
}

func start(ctx context.Context, intent cli.StartIntent, info buildinfo.Info) (returnErr error) {
	if intent.Kind != cli.IntentNew {
		return cli.UnavailableError{}
	}
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
	runRepository := sqlite.NewAgentRunRepository(database)
	toolRepository := sqlite.NewToolInvocationRepository(database)
	auditRepository := sqlite.NewAuditRepository(database)

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
			EvidenceIDs: identifiers, Text: redactor, Policy: denyLogDataPolicy{}, Now: now,
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
	coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
		Sessions: sessionRepository, Runs: runRepository, Tools: toolRepository,
		Audits: auditRepository, Scope: scopeManager,
		Runner: agentAdapter, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: redactor, UIEvents: disabledUIEventSink{}, Observer: slogRunObserver{logger: logger},
		Now: now,
	})
	if err != nil {
		return err
	}
	composition.coordinator = coordinator

	// Delivery remains intentionally disabled in this slice. Constructing the
	// pure TUI model verifies the neutral boundary without starting Bubble Tea,
	// querying resume history, or activating a Kubernetes scope.
	_ = tui.NewModel(tui.Config{
		NoColor:     loaded.NoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartNew},
		ModelName:   validatedModel.Model,
		PrivacyMode: domain.PrivacyModeStandard,
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return cli.UnavailableError{}
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

type disabledUIEventSink struct{}

func (disabledUIEventSink) PublishUIEvent(ctx context.Context, event application.UIEvent) error {
	if ctx == nil || ctx.Err() != nil {
		return context.Canceled
	}
	return event.Validate()
}

type denyLogDataPolicy struct{}

func (denyLogDataPolicy) AuthorizeLogRead(context.Context, tools.LogPolicyRequest) tools.LogPolicyDecision {
	return tools.LogPolicyDenied
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
