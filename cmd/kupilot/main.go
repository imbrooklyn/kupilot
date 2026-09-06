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
	"github.com/imbrooklyn/kupilot/internal/approval"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/executor"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/observability"
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
		_, err := fmt.Fprintln(stdout, "Kupilot cache cleared.")
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
	defer loaded.Credentials.Destroy()
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
			Directory:            loaded.Paths.LogDir,
			Level:                configuredLogLevel(loaded.Logging.Level),
			SensitiveDiagnostics: loaded.Logging.SensitiveDiagnostics,
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
		"provider_kind", loaded.Models.Agent.ProviderKind,
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
	approvalRepository := sqlite.NewApprovalRepository(database)
	retentionRepository := sqlite.NewRetentionRepository(database)
	scopePreferenceRepository := sqlite.NewScopePreferenceRepository(database)
	privacyManager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: sqlite.NewRolePrivacyRepository(database, domain.ModelRoleAgent),
		Role:  domain.ModelRoleAgent, Origin: loaded.Models.Agent.Origin,
		PrometheusOrigin: configuredDataSourceOrigin(loaded.Observability.Prometheus),
		LokiOrigin:       configuredDataSourceOrigin(loaded.Observability.Loki), Now: utcNow,
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

	now := utcNow
	budgetLimits, err := configuredBudgetLimits(loaded.Config)
	if err != nil {
		return err
	}
	loader := kube.NewConfigLoader()
	factory, err := kube.NewClientFactory(
		loader,
		configuredExecPolicy(loaded.Kubernetes.ExecCredentials),
		budgetLimits.ToolRequestTimeout,
	)
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
	scopeManager, err := application.NewScopeManager(
		runtimeGateway, baseGateway, baseGateway, runtimeGateway, now,
		domain.NamespaceAccessPolicy(loaded.Kubernetes.NamespaceAccess),
	)
	if err != nil {
		return err
	}
	composition.scope = scopeManager
	identifiers, err := application.NewIdentifierGenerator(now)
	if err != nil {
		return err
	}
	restarter, err := kube.NewDeploymentRestarter(runtimeGateway, now)
	if err != nil {
		return err
	}
	rolloutObserver, err := kube.NewDeploymentRolloutObserver(runtimeGateway, kube.RolloutObserverConfig{})
	if err != nil {
		return err
	}
	remediator, err := kube.NewRemediator(runtimeGateway, now)
	if err != nil {
		return err
	}
	approvalService, err := approval.NewService(approval.ServiceConfig{
		Clock: compositionApprovalClock{now: now}, Nonces: identifiers,
		Store: approvalRepository, AuditIDs: identifiers,
	})
	if err != nil {
		return err
	}
	permissionManager, err := application.NewPermissionManager(application.PermissionPolicy{
		Profile: application.DefaultPermissionProfile, Generation: 1,
	})
	if err != nil {
		return err
	}
	resourcePolicies, err := loaded.Config.ResourcePolicyCatalog()
	if err != nil {
		return err
	}
	observabilityPolicies, err := loaded.Config.ObservabilityPolicyCatalog()
	if err != nil {
		return err
	}
	remoteDiagnosticsPolicies, err := loaded.Config.RemoteDiagnosticsPolicyCatalog()
	if err != nil {
		return err
	}
	localCommandPolicies, localShellPolicies, err := loaded.Config.LocalExecutionPolicyCatalogs()
	if err != nil {
		return err
	}
	resourceAuthority, err := application.NewCompleteOperationalAuthority(resourcePolicies, observabilityPolicies, remoteDiagnosticsPolicies, permissionManager)
	if err != nil {
		return err
	}
	if err := runtimeGateway.ConfigureResourcePolicyGuard(resourceAuthority); err != nil {
		return err
	}
	var prometheusReader tools.PrometheusReader = observability.DisabledSources{}
	var lokiReader tools.LokiReader = observability.DisabledSources{}
	sourceGuards := observability.RuntimeGuards{Scope: scopeManager, Policy: resourceAuthority}
	if loaded.Observability.Prometheus != nil {
		client, clientErr := observability.NewPrometheusClient(loaded.Observability.Prometheus, loaded.Credentials.Prometheus, nil, sourceGuards)
		if clientErr != nil {
			return clientErr
		}
		composition.prometheus = client
		prometheusReader = client
	}
	if loaded.Observability.Loki != nil {
		client, clientErr := observability.NewLokiClient(loaded.Observability.Loki, loaded.Credentials.Loki, nil, sourceGuards)
		if clientErr != nil {
			return clientErr
		}
		composition.loki = client
		lokiReader = client
	}
	redactor := security.NewRedactor()
	localExecutor, err := executor.NewAdapter(redactor, now)
	if err != nil {
		return err
	}
	localPreparer, err := application.NewLocalActionPreparer(
		localCommandPolicies, localShellPolicies, remediator, localExecutor,
	)
	if err != nil {
		return err
	}
	readPolicy := compositionObservationPolicy{privacy: privacyManager}
	profileWriterBase := loaded.Config
	if loaded.SourceVersion == config.LegacyVersion &&
		profileWriterBase.Models.Agent.MaxOutputTokens == config.LegacyDefaultMaxModelOutputTokens {
		// Preserve the v1 value only for the compatibility runtime. A deliberate
		// v2 save must not republish that historical default as endpoint evidence.
		profileWriterBase.Models.Agent.MaxOutputTokens = 0
	}
	profileWriter := &compositionModelProfileWriter{paths: loaded.Paths, base: profileWriterBase}
	if reviewerCredential := loaded.Credentials.ApprovalReviewer; reviewerCredential != nil &&
		reviewerCredential.Source == config.CredentialSourceFile {
		clone, cloneErr := reviewerCredential.Value.Clone()
		if cloneErr != nil {
			return cloneErr
		}
		profileWriter.reviewerFileCredential = &clone
		composition.profileCredential = &clone
	}
	if sourceCredential := loaded.Credentials.Prometheus; sourceCredential != nil && sourceCredential.Source == config.CredentialSourceFile {
		clone, cloneErr := sourceCredential.Value.Clone()
		if cloneErr != nil {
			return cloneErr
		}
		profileWriter.prometheusFileCredential = &clone
		composition.sourceCredentials = append(composition.sourceCredentials, &clone)
	}
	if sourceCredential := loaded.Credentials.Loki; sourceCredential != nil && sourceCredential.Source == config.CredentialSourceFile {
		clone, cloneErr := sourceCredential.Value.Clone()
		if cloneErr != nil {
			return cloneErr
		}
		profileWriter.lokiFileCredential = &clone
		composition.sourceCredentials = append(composition.sourceCredentials, &clone)
	}
	var reviewerBinding *application.ReviewerModelBinding
	if reviewerProfile := loaded.Models.ApprovalReviewer; reviewerProfile != nil {
		reviewerPrivacy, privacyErr := application.NewPrivacyManager(application.PrivacyManagerConfig{
			Store: sqlite.NewRolePrivacyRepository(database, domain.ModelRoleApprovalReviewer),
			Role:  domain.ModelRoleApprovalReviewer, Origin: reviewerProfile.Origin, Now: utcNow,
		})
		if privacyErr != nil {
			return privacyErr
		}
		reviewerLimits, limitErr := agent.ReviewerBudgetLimitsForProfile(agent.BudgetProfile(loaded.Runtime.BudgetProfile))
		if limitErr != nil {
			return limitErr
		}
		configuredTimeout := time.Duration(reviewerProfile.RequestTimeoutSeconds) * time.Second
		if configuredTimeout < reviewerLimits.RequestTimeout {
			reviewerLimits.RequestTimeout = configuredTimeout
		}
		reviewerBudget, budgetErr := agent.NewReviewerBudget(reviewerLimits)
		if budgetErr != nil {
			return budgetErr
		}
		reviewerBinding = &application.ReviewerModelBinding{
			Profile: reviewerProfile.Name, Model: reviewerProfile.Model,
			Privacy: reviewerPrivacy, Budget: reviewerBudget,
		}
		if loaded.Credentials.ApprovalReviewer != nil && loaded.Credentials.ApprovalReviewer.Value.IsSet() {
			reviewerSecret, cloneErr := loaded.Credentials.ApprovalReviewer.Value.Clone()
			if cloneErr != nil {
				return cloneErr
			}
			reviewer, reviewerErr := einoadapter.NewReviewer(einoadapter.ReviewerConfig{
				ModelConfiguration: modelConfiguration(*reviewerProfile), Credential: &reviewerSecret,
				Logger: logger, Diagnostics: einoadapter.DiagnosticOptions{Sensitive: loaded.Logging.SensitiveDiagnostics},
			})
			if reviewerErr != nil {
				reviewerSecret.Destroy()
				_, _ = fmt.Fprintln(stderr, "Warning: the configured approval reviewer is unavailable; automated review remains disabled.")
			} else {
				composition.reviewer = reviewer
				reviewerBinding.Available = true
				reviewerBinding.Transport = reviewer
			}
		}
	}
	deliveryStop := make(chan struct{})
	uiEventSink := &deliveryUIEventSink{events: make(chan application.UIEvent, 64), stopped: deliveryStop}
	approvalCoordinator, err := application.NewApprovalCoordinator(application.ApprovalCoordinatorConfig{
		Service:            approvalService,
		Persistence:        approvalRepository,
		ResultAudits:       auditRepository,
		Scope:              scopeManager,
		ApprovalIDs:        identifiers,
		AuditIDs:           identifiers,
		UIEvents:           uiEventSink,
		Rollout:            rolloutObserver,
		RestartRevalidator: restarter,
		RestartExecutor:    restarter,
		Remediation:        remediator,
		LocalProcesses:     localExecutor,
		RemoteDiagnostics:  runtimeGateway,
		Observations:       runtimeGateway,
		ObservationPolicy:  observabilityPolicies,
		Permissions:        permissionManager,
		Reviewer:           reviewerBinding,
		Reviews:            approvalRepository,
		Now:                now,
	})
	if err != nil {
		return err
	}
	remoteActionGate, err := application.NewRemoteDiagnosticActionGate(application.RemoteDiagnosticActionGateConfig{
		Service: approvalService, Persistence: approvalRepository, ResultAudits: auditRepository,
		Revalidator: runtimeGateway, Scope: scopeManager, Identifiers: identifiers, Permissions: permissionManager,
		Supervisor: approvalCoordinator, PolicyCatalog: remoteDiagnosticsPolicies, Now: now,
		PersistenceTimeout: application.DefaultPersistenceTimeout,
	})
	if err != nil {
		return err
	}
	toolHandlers, err := tools.NewToolCatalog(tools.ToolCatalogDependencies{
		Resources: tools.ResourceToolDependencies{
			Reader: runtimeGateway, QueryReader: runtimeGateway, ScopeGuard: scopeManager, PolicyGuard: resourceAuthority,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Events: tools.EventToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager, PolicyGuard: resourceAuthority,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Logs: tools.LogToolDependencies{
			Reader: runtimeGateway, Targets: runtimeGateway, Actions: approvalCoordinator,
			ScopeGuard: scopeManager, PolicyGuard: resourceAuthority,
			EvidenceIDs: identifiers, Text: redactor, Policy: readPolicy, Now: now,
		},
		Metrics: tools.MetricToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager, PolicyGuard: resourceAuthority,
			EvidenceIDs: identifiers, Now: now,
		},
		Sources: tools.DataSourceToolDependencies{
			Prometheus: prometheusReader, Loki: lokiReader, Targets: runtimeGateway, Actions: approvalCoordinator,
			ScopeGuard: scopeManager, PolicyGuard: resourceAuthority,
			Policy: readPolicy, EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Related: tools.RelatedToolDependencies{
			Reader: runtimeGateway, ScopeGuard: scopeManager,
			EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
		Remote: &tools.RemoteDiagnosticToolDependencies{
			Resolver: runtimeGateway, Commands: runtimeGateway, DiagnosticPods: runtimeGateway,
			ScopeGuard: scopeManager, PolicyGuard: resourceAuthority, Actions: remoteActionGate,
			OutputPolicy: readPolicy, EvidenceIDs: identifiers, Text: redactor, Now: now,
		},
	})
	if err != nil {
		return err
	}
	modelFactory := &compositionModelFactory{
		base: loaded.Config, tools: toolHandlers, scope: scopeManager,
		identifiers: identifiers, now: now, logger: logger,
	}
	var initialRuntime application.ModelRuntime
	if loaded.Models.Agent.Endpoint != "" && loaded.Models.Agent.Model != "" && loaded.Credentials.Agent.Value.IsSet() {
		setupSecret, secretErr := applicationSecret(&loaded.Credentials.Agent.Value)
		loaded.Credentials.Agent.Value.Destroy()
		if secretErr == nil {
			request := application.ModelSetupRequest{
				RequestID: 1, Endpoint: loaded.Models.Agent.Endpoint, Model: loaded.Models.Agent.Model, Secret: setupSecret,
			}
			initialRuntime, err = modelFactory.BuildModelRuntime(ctx, request)
			setupSecret.Destroy()
		}
		if secretErr != nil || err != nil {
			initialRuntime = nil
			_, _ = fmt.Fprintln(stderr, "Warning: the configured model runtime could not be constructed; use /model to configure it in the TUI.")
		}
	} else {
		loaded.Credentials.Agent.Value.Destroy()
	}
	runtimeTransferred := false
	defer func() {
		if initialRuntime != nil && !runtimeTransferred {
			initialRuntime.Close()
		}
	}()
	coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
		Sessions: sessionRepository, Runs: runRepository, Tools: toolRepository,
		Audits: auditRepository, Scope: scopeManager,
		ModelContext: messageRepository,
		ModelRuntime: initialRuntime, ModelFactory: modelFactory, ModelProfiles: profileWriter,
		ReviewerModel: reviewerBinding,
		Identifiers:   identifiers, AuditIdentifiers: identifiers,
		Questions: redactor, Exports: sessionRepository, ExportFiles: filesystem.NewExportWriter(), ExportText: redactor,
		Privacy: privacyManager, RunResourcePolicies: resourceAuthority, UIEvents: uiEventSink, Observer: slogRunObserver{logger: logger},
		Now: now, BudgetLimits: budgetLimits,
		UI: &application.CoordinatorUIConfig{
			Sessions: sessionApplication, Search: sessionRepository, Titles: sessionApplication, Startup: sessionApplication, Scopes: scopeManager,
			ScopePreferences: scopePreferenceRepository,
			Approvals:        approvalCoordinator, RestartProposals: restarter,
			RemediationProposals: remediator, LocalProposals: localPreparer,
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
	scopePreferenceDegraded := startResult.ScopePreferenceDegraded
	if shouldActivateInitialScope(startIntent) && startResult.ScopeCandidate != nil {
		var activationDegraded bool
		initialScope, activationDegraded, err = activateInitialScope(ctx, coordinator, *startResult.ScopeCandidate)
		if err != nil {
			return err
		}
		scopePreferenceDegraded = scopePreferenceDegraded || activationDegraded
	} else if startResult.ScopeCandidate != nil {
		initialScope.Context = startResult.ScopeCandidate.Context
		initialScope.Namespace = startResult.ScopeCandidate.Namespace
	}
	model := tui.NewModel(tui.Config{
		NoColor:                 loaded.NoColor,
		StartIntent:             startIntent,
		Scope:                   initialScope,
		ModelEndpoint:           loaded.Models.Agent.Endpoint,
		ModelName:               loaded.Models.Agent.Model,
		ModelConfigured:         initialRuntime != nil,
		ModelConfiguredSet:      true,
		PrivacyMode:             domain.PrivacyModeStandard,
		Permission:              approvalCoordinator.UIPermissionSnapshot(),
		ScopePreferenceDegraded: scopePreferenceDegraded,
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
		tui.NewTerminalRuntime(model),
		tea.WithContext(ctx),
		tea.WithOutput(stdout),
		tea.WithFilter(applicationRequestFilter(requestContext, requests)),
	)
	var workers sync.WaitGroup
	workers.Add(2)
	go pumpApplicationRequests(requestContext, coordinator, program, requests, &workers)
	go pumpApplicationEvents(eventContext, program, uiEventSink.events, deliveryStop, &workers)

	finalState, runErr := program.Run()
	cancelRequests()
	close(requests)
	shutdownContext, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), domain.MaxAgentRunDuration+15*time.Second)
	shutdownErr := coordinator.Shutdown(shutdownContext)
	cancelShutdown()
	close(deliveryStop)
	cancelEvents()
	workers.Wait()
	restoreErr := restoreTerminalAfterRuntime(stdout, finalState, runErr)
	return errors.Join(runErr, shutdownErr, restoreErr)
}

func restoreTerminalAfterRuntime(output io.Writer, finalState tea.Model, runErr error) error {
	var finalModel tui.Model
	var pendingTranscript string
	switch state := finalState.(type) {
	case tui.TerminalRuntime:
		finalModel = state.Model()
		pendingTranscript = state.PendingTerminalTranscript()
	case tui.Model:
		finalModel = state
		pendingTranscript = finalModel.PendingTerminalTranscript()
	default:
		return errors.New("TUI final state is unavailable")
	}
	frameHeight := finalModel.TerminalFrameHeight()
	if frameHeight > 0 {
		if _, err := fmt.Fprint(output, "\r"); err != nil {
			return fmt.Errorf("restore terminal frame: %w", err)
		}
		if frameHeight > 1 {
			if _, err := fmt.Fprintf(output, "\x1b[%dA", frameHeight-1); err != nil {
				return fmt.Errorf("restore terminal frame: %w", err)
			}
		}
		if _, err := fmt.Fprint(output, "\x1b[J"); err != nil {
			return fmt.Errorf("restore terminal frame: %w", err)
		}
	}
	if runErr != nil {
		return nil
	}
	if pendingTranscript == "" {
		return nil
	}
	if _, err := fmt.Fprintln(output, pendingTranscript); err != nil {
		return fmt.Errorf("write pending terminal transcript: %w", err)
	}
	return nil
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
	candidate domain.ScopeCandidate,
) (tui.ScopeView, bool, error) {
	contextOutcome, err := coordinator.ExecuteUICommand(ctx, application.UICommand{
		Kind: application.UICommandActivateScope, RequestID: 1, Scope: &candidate,
	})
	if err != nil || contextOutcome.Scope == nil || contextOutcome.Scope.Failure != "" {
		return tui.ScopeView{}, false, initialScopeError{}
	}
	result := contextOutcome.Scope
	return tui.ScopeView{
		Context: result.Context, Namespace: result.Namespace,
		Generation: result.ScopeGeneration, ReadOnly: result.ReadOnly,
	}, result.ScopePreferenceDegraded, nil
}

type initialScopeError struct{}

func (initialScopeError) Error() string { return "initial Kubernetes scope unavailable" }
func (initialScopeError) SafeMessage() string {
	return "The Kubernetes scope could not be verified safely."
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
		case tui.ApplicationCommandMsg, tui.ApplicationModelSetupMsg, tui.ApplicationModelSetupCancelMsg,
			tui.ApplicationQueryMsg, tui.ApplicationResumeMsg, tui.ApplicationEvidenceDetailMsg:
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

func rejectedApplicationRequest(message tea.Msg) tea.Msg {
	if request, ok := message.(tui.ApplicationModelSetupCancelMsg); ok {
		return tui.ModelSetupCancelRejectedMsg{RequestID: request.RequestID}
	}
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

type applicationMessageSender interface {
	Send(tea.Msg)
}

type activeModelSetupRequest struct {
	requestID uint64
	cancel    context.CancelFunc
	result    <-chan tea.Msg
}

func pumpApplicationRequests(
	ctx context.Context,
	consumer tui.ApplicationConsumer,
	program applicationMessageSender,
	requests <-chan tea.Msg,
	workers *sync.WaitGroup,
) {
	defer func() {
		destroyPendingApplicationRequests(requests)
		workers.Done()
	}()
	var active *activeModelSetupRequest
	joinActive := func() {
		if active == nil {
			return
		}
		active.cancel()
		<-active.result
		active = nil
	}
	for {
		var activeResult <-chan tea.Msg
		if active != nil {
			activeResult = active.result
		}
		select {
		case <-ctx.Done():
			joinActive()
			return
		case result := <-activeResult:
			active.cancel()
			active = nil
			program.Send(result)
		case message, open := <-requests:
			if !open {
				joinActive()
				return
			}
			switch request := message.(type) {
			case tui.ApplicationModelSetupCancelMsg:
				if active != nil && active.requestID == request.RequestID {
					active.cancel()
					continue
				}
				program.Send(rejectedApplicationRequest(message))
			case tui.ApplicationModelSetupMsg:
				if active != nil {
					program.Send(rejectedApplicationRequest(message))
					continue
				}
				operationContext, cancel := context.WithCancel(ctx)
				result := make(chan tea.Msg, 1)
				active = &activeModelSetupRequest{
					requestID: request.Request.RequestID,
					cancel:    cancel,
					result:    result,
				}
				go func(operationContext context.Context, message tea.Msg, result chan<- tea.Msg) {
					result <- tui.DispatchApplication(operationContext, consumer, message)
				}(operationContext, message, result)
			default:
				if active != nil {
					program.Send(rejectedApplicationRequest(message))
					continue
				}
				program.Send(tui.DispatchApplication(ctx, consumer, message))
			}
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
	settings.Models.Agent.Endpoint = request.Endpoint
	settings.Models.Agent.Origin = ""
	settings.Models.Agent.Model = request.Model
	if err := config.Validate(&settings); err != nil {
		return nil, err
	}
	credential, err := configurationSecret(request.Secret)
	if err != nil {
		return nil, err
	}
	agentAdapter, err := einoadapter.New(einoadapter.Config{
		ModelConfiguration: modelConfiguration(settings.Models.Agent),
		Credential:         &credential,
		Logger:             factory.logger,
		Diagnostics:        einoadapter.DiagnosticOptions{Sensitive: settings.Logging.SensitiveDiagnostics},
		Tools:              factory.tools,
		ScopeGuard:         factory.scope,
		Identifiers:        factory.identifiers,
		Now:                factory.now,
	})
	if err != nil {
		credential.Destroy()
		return nil, err
	}
	return &compositionModelRuntime{
		agent:   agentAdapter,
		profile: settings.Models.Agent.Name,
		name:    settings.Models.Agent.Model, origin: settings.Models.Agent.Origin,
	}, nil
}

type compositionModelProfileWriter struct {
	paths                    config.Paths
	base                     config.Config
	reviewerFileCredential   *config.SecretValue
	prometheusFileCredential *config.SecretValue
	lokiFileCredential       *config.SecretValue
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
	return config.SaveModelProfilesWithDataSources(ctx, writer.paths, writer.base, config.ModelProfile{
		Endpoint: request.Endpoint, Model: request.Model,
	}, &credential, writer.reviewerFileCredential, writer.prometheusFileCredential, writer.lokiFileCredential)
}

type compositionModelRuntime struct {
	once    sync.Once
	agent   *einoadapter.Adapter
	profile string
	name    string
	origin  string
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

func (runtime *compositionModelRuntime) ProfileName() string {
	if runtime == nil {
		return ""
	}
	return runtime.profile
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

	coordinator       coordinatorLifecycle
	scope             scopeLifecycle
	database          io.Closer
	logSink           io.Closer
	profileCredential *config.SecretValue
	sourceCredentials []*config.SecretValue
	prometheus        *observability.PrometheusClient
	loki              *observability.LokiClient
	reviewer          *einoadapter.Reviewer
	closed            bool
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
	if composition.reviewer != nil {
		composition.reviewer.Close()
	}
	if composition.prometheus != nil {
		composition.prometheus.Close()
	}
	if composition.loki != nil {
		composition.loki.Close()
	}
	if composition.database != nil {
		closeErrors = append(closeErrors, composition.database.Close())
	}
	if composition.logSink != nil {
		closeErrors = append(closeErrors, composition.logSink.Close())
	}
	if composition.profileCredential != nil {
		composition.profileCredential.Destroy()
	}
	for _, credential := range composition.sourceCredentials {
		credential.Destroy()
	}
	composition.closed = true
	return errors.Join(closeErrors...)
}

type compositionObservationPolicy struct {
	privacy *application.PrivacyManager
}

func (policy compositionObservationPolicy) AuthorizeLogRead(ctx context.Context, request tools.LogPolicyRequest) tools.LogPolicyDecision {
	if policy.privacy == nil {
		return tools.LogPolicyDenied
	}
	switch policy.privacy.AuthorizeLogs(ctx) {
	case application.PrivacyLogConsentRequired:
		return tools.LogPolicyConsentRequired
	case application.PrivacyLogDenied:
		return tools.LogPolicyDenied
	}
	return tools.LogPolicyAllowed
}

func (policy compositionObservationPolicy) AuthorizeRemoteOutput(ctx context.Context, _ tools.RemoteOutputPolicyRequest) tools.LogPolicyDecision {
	if policy.privacy == nil {
		return tools.LogPolicyDenied
	}
	switch policy.privacy.AuthorizeContainerOutput(ctx) {
	case application.PrivacyLogAllowed:
		return tools.LogPolicyAllowed
	case application.PrivacyLogConsentRequired:
		return tools.LogPolicyConsentRequired
	default:
		return tools.LogPolicyDenied
	}
}

func (policy compositionObservationPolicy) AuthorizeObservation(ctx context.Context, request tools.ObservationPolicyRequest) tools.ObservationPolicyDecision {
	if policy.privacy == nil {
		return tools.ObservationPolicyDenied
	}
	switch policy.privacy.AuthorizeDataSource(ctx, request.Kind, request.OriginHash) {
	case application.PrivacyLogConsentRequired:
		return tools.ObservationPolicyConsentRequired
	case application.PrivacyLogDenied:
		return tools.ObservationPolicyDenied
	}
	return tools.ObservationPolicyAllowed
}

func configuredDataSourceOrigin(source *config.DataSourceConfig) string {
	if source == nil {
		return ""
	}
	return source.Origin
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

func modelConfiguration(value config.ModelProfileConfig) domain.ModelConfiguration {
	return domain.ModelConfiguration{
		ProfileName: value.Name, Role: domain.ModelRole(value.Role),
		ProviderKind: domain.ModelProviderOpenAICompatible,
		Endpoint:     value.Endpoint, Origin: value.Origin, Model: value.Model,
		APIKeySource:    domain.ModelAPIKeySourceRuntime,
		ReasoningEffort: domain.ModelReasoningEffort(value.ReasoningEffort),
		Temperature:     value.Temperature, MaxOutputTokens: value.MaxOutputTokens,
		RequestTimeout:    time.Duration(value.RequestTimeoutSeconds) * time.Second,
		StreamingRequired: value.Streaming, ToolCallingRequired: value.ToolCallingRequired,
		TransportPolicy: domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
}

func configuredBudgetLimits(value config.Config) (agent.RunBudgetLimits, error) {
	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfile(value.Runtime.BudgetProfile))
	if err != nil {
		return agent.RunBudgetLimits{}, err
	}
	configuredModelTimeout := time.Duration(value.Models.Agent.RequestTimeoutSeconds) * time.Second
	if configuredModelTimeout > 0 && configuredModelTimeout < limits.ModelRequestTimeout {
		limits.ModelRequestTimeout = configuredModelTimeout
	}
	if limits.Validate() != nil {
		return agent.RunBudgetLimits{}, agent.ErrInvalidRunBudget
	}
	return limits, nil
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

type compositionApprovalClock struct {
	now func() time.Time
}

func (clock compositionApprovalClock) Now() time.Time {
	if clock.now == nil {
		return time.Time{}
	}
	return clock.now()
}

func utcNow() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}
