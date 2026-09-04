package application_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	platformlogging "github.com/imbrooklyn/kupilot/internal/platform/logging"
	"github.com/imbrooklyn/kupilot/internal/security"
	"github.com/imbrooklyn/kupilot/internal/tools"
)

type assuranceBoundary struct {
	err        error
	class      domain.SafeErrorClass
	canaries   []string
	toolResult *domain.ToolResult
}

func TestSecurityAssuranceSafeErrorSourceSinkMatrix(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T) assuranceBoundary
	}{
		{name: "configuration", prepare: prepareConfigurationAssuranceBoundary},
		{name: "Kubernetes", prepare: prepareKubernetesAssuranceBoundary},
		{name: "model", prepare: prepareModelAssuranceBoundary},
		{name: "Tool", prepare: prepareToolAssuranceBoundary},
		{name: "SQLite", prepare: prepareSQLiteAssuranceBoundary},
		{name: "Application", prepare: prepareApplicationAssuranceBoundary},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			boundary := test.prepare(t)
			assertSecurityAssuranceBoundary(t, boundary)
		})
	}
}

func TestSecurityAssuranceSafeErrorCancellationIdentity(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, configErr := config.Load(cancelled, config.LoadOptions{})
	_, kubeErr := kube.NewConfigLoader().Contexts(cancelled)
	modelErr := domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationRequest, "assurance-cancelled")
	stateDirectory := filepath.Join(t.TempDir(), "state")
	_, sqliteErr := sqlite.Open(cancelled, sqlite.OpenOptions{
		StateDir: stateDirectory, ApplicationVersion: "assurance", CorrelationID: "assurance-cancelled",
	})
	ports := &assuranceScopePorts{}
	manager, err := application.NewScopeManager(ports, ports, ports, ports, func() time.Time {
		return time.UnixMilli(1).UTC()
	})
	if err != nil {
		t.Fatalf("NewScopeManager() error = %v", err)
	}
	_, applicationErr := manager.ListContexts(cancelled)
	if got := ports.contextCalls.Load(); got != 0 {
		t.Fatalf("Application adapter calls after local cancellation = %d, want 0", got)
	}

	for name, current := range map[string]error{
		"configuration": configErr,
		"Kubernetes":    kubeErr,
		"model":         modelErr,
		"SQLite":        sqliteErr,
		"Application":   applicationErr,
	} {
		if current == nil {
			t.Fatalf("%s cancellation error = nil", name)
		}
		wrapped := fmt.Errorf("safe boundary: %w", current)
		joined := errors.Join(errors.New("secondary safe failure"), wrapped)
		if !errors.Is(current, context.Canceled) || !errors.Is(wrapped, context.Canceled) || !errors.Is(joined, context.Canceled) {
			t.Fatalf("%s cancellation identity was not retained through wrapping and joining", name)
		}
	}
	if _, err := os.Stat(stateDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled SQLite open changed the state path")
	}

	toolBoundary := prepareCancelledToolBoundary(t, cancelled)
	if toolBoundary.toolResult == nil || toolBoundary.toolResult.Error == nil ||
		toolBoundary.toolResult.Error.Class != domain.SafeErrorClassCancelled {
		t.Fatalf("cancelled Tool result did not retain the stable cancelled class")
	}
}

func prepareConfigurationAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	canary := strings.Repeat("c", 47) + "-generated"
	var unsetCalls atomic.Int32
	source := &config.EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return canary, true },
		Unsetenv: func(string) error {
			unsetCalls.Add(1)
			return errors.Join(fmt.Errorf("nested configuration failure: %w", errors.New(canary)), errors.New("secondary failure"))
		},
	}
	_, err := source.Read()
	if err == nil {
		t.Fatal("EnvironmentSecretSource.Read() error = nil")
	}
	if got := unsetCalls.Load(); got != 1 {
		t.Fatalf("environment unset attempts = %d, want 1", got)
	}
	return assuranceBoundary{err: err, class: domain.SafeErrorClassInternal, canaries: []string{canary}}
}

func prepareKubernetesAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	contentCanary := strings.Repeat("k", 47) + "-generated"
	pathCanary := strings.Repeat("p", 47) + "-generated"
	directory := filepath.Join(t.TempDir(), pathCanary)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("invalid: ["+contentCanary), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("KUBECONFIG", path)
	_, err := kube.NewConfigLoader().Contexts(context.Background())
	if err == nil {
		t.Fatal("ConfigLoader.Contexts() error = nil")
	}
	return assuranceBoundary{
		err: err, class: domain.SafeErrorClassConfigurationInvalid, canaries: []string{contentCanary, pathCanary},
	}
}

func prepareModelAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	credentialCanary := strings.Repeat("a", 47) + "-generated"
	errorCanary := strings.Repeat("m", 47) + "-generated"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+credentialCanary {
			t.Error("model transport did not receive its configured credential")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(errorCanary))
	}))
	defer server.Close()

	source := &config.EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return credentialCanary, true },
		Unsetenv:  func(string) error { return nil },
	}
	credential, err := source.Read()
	if err != nil {
		t.Fatalf("EnvironmentSecretSource.Read() error = %v", err)
	}
	tool := &assuranceNoopTool{}
	adapter, modelErr := einoadapter.New(einoadapter.Config{
		ModelConfiguration: domain.ModelConfiguration{
			ProfileName: "agent", Role: domain.ModelRoleAgent,
			ProviderKind:        domain.ModelProviderOpenAICompatible,
			Endpoint:            server.URL + "/v1",
			Origin:              server.URL,
			Model:               "assurance-model",
			APIKeySource:        domain.ModelAPIKeySourceRuntime,
			Temperature:         0.1,
			MaxOutputTokens:     256,
			RequestTimeout:      time.Second,
			StreamingRequired:   true,
			ToolCallingRequired: true,
			TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
		},
		Credential: &credential,
		Tools: agent.ToolHandlers{
			GetResource: tool, ListResources: tool, GetEvents: tool,
			GetPodLogs: tool, GetPreviousPodLogs: tool,
			GetPodMetrics: tool, GetNodeMetrics: tool, QueryPrometheus: tool, QueryLoki: tool,
			GetRelatedResources: tool, GetClusterOverview: tool,
		},
		ScopeGuard:  assuranceScopeGuard{},
		Identifiers: &assuranceRunIDs{},
		Now:         func() time.Time { return time.UnixMilli(2).UTC() },
	})
	if modelErr != nil {
		credential.Destroy()
		t.Fatalf("einoadapter.New() error = %v", modelErr)
	}
	defer adapter.Close()
	input, err := agent.NewRunInput(
		domain.AgentRunID("00000000-0000-7000-8000-000000000935"),
		domain.SessionID("00000000-0000-7000-8000-000000000936"),
		domain.MessageID("00000000-0000-7000-8000-000000000937"),
		"Report the bounded diagnostic result.", assuranceScope(), nil, agent.DefaultRunBudgetLimits(),
	)
	if err != nil {
		t.Fatalf("agent.NewRunInput() error = %v", err)
	}
	outcome := adapter.Run(context.Background(), input, agent.EventSinkFunc(func(context.Context, agent.RunEvent) agent.EventSinkResult {
		return agent.EventSinkAccepted
	}))
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassUnavailable {
		t.Fatalf("Adapter.Run() outcome = %#v", outcome)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("Tool calls = %d, want 0", got)
	}
	modelErr = domain.NewModelError(
		domain.ModelErrorCodeServiceUnavailable,
		domain.ModelOperationRequest,
		"assurance-model",
	)
	return assuranceBoundary{
		err: modelErr, class: domain.SafeErrorClassUnavailable, canaries: []string{credentialCanary, errorCanary},
	}
}

func prepareToolAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	canary := strings.Repeat("t", 47) + "-generated"
	reader := &assuranceResourceReader{err: errors.Join(
		fmt.Errorf("nested Tool adapter failure: %w", errors.New(canary)),
		errors.New("secondary failure"),
	)}
	tool, err := tools.NewGetResourceTool(tools.ResourceToolDependencies{
		Reader: reader, QueryReader: reader, ScopeGuard: assuranceScopeGuard{}, PolicyGuard: assuranceScopeGuard{}, EvidenceIDs: assuranceEvidenceIDs{},
		Text: security.NewRedactor(), Now: func() time.Time { return time.UnixMilli(2).UTC() },
	})
	if err != nil {
		t.Fatalf("NewGetResourceTool() error = %v", err)
	}
	input, err := agent.NewRunInput(
		domain.AgentRunID("00000000-0000-7000-8000-000000000911"),
		domain.SessionID("00000000-0000-7000-8000-000000000912"),
		domain.MessageID("00000000-0000-7000-8000-000000000913"),
		"Inspect the selected Pod.",
		assuranceScope(),
		&domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
		agent.DefaultRunBudgetLimits(),
	)
	if err != nil {
		t.Fatalf("agent.NewRunInput() error = %v", err)
	}
	call, err := agent.BindToolCall(input,
		domain.ToolInvocationID("00000000-0000-7000-8000-000000000914"),
		agent.ToolSelection{
			ID: "assurance-call", Name: domain.ToolNameGetResource,
			ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect the selected Pod.","resource_type":"pods"}`,
		},
	)
	if err != nil {
		t.Fatalf("agent.BindToolCall() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Error == nil || result.Error.Class != domain.SafeErrorClassInternal {
		t.Fatalf("Tool result did not contain the expected safe error classification")
	}
	if got := reader.calls.Load(); got != 1 {
		t.Fatalf("Tool reader calls = %d, want 1", got)
	}
	return assuranceBoundary{
		err: &assuranceToolError{value: *result.Error}, class: result.Error.Class, canaries: []string{canary}, toolResult: &result,
	}
}

func prepareSQLiteAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	canary := strings.Repeat("s", 47) + "-generated"
	stateDirectory := filepath.Join(t.TempDir(), "uncreated-state")
	_, err := sqlite.Open(context.Background(), sqlite.OpenOptions{
		StateDir: stateDirectory, ApplicationVersion: "assurance", CorrelationID: canary + "\n",
	})
	if err == nil {
		t.Fatal("sqlite.Open() error = nil")
	}
	if _, statErr := os.Stat(stateDirectory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("invalid SQLite metadata changed the state path")
	}
	return assuranceBoundary{
		err: err, class: domain.SafeErrorClassConfigurationInvalid, canaries: []string{canary},
	}
}

func prepareApplicationAssuranceBoundary(t *testing.T) assuranceBoundary {
	t.Helper()
	canary := strings.Repeat("u", 47) + "-generated"
	ports := &assuranceScopePorts{err: errors.Join(
		fmt.Errorf("nested Application adapter failure: %w", errors.New(canary)),
		errors.New("secondary failure"),
	)}
	manager, err := application.NewScopeManager(ports, ports, ports, ports, func() time.Time {
		return time.UnixMilli(1).UTC()
	})
	if err != nil {
		t.Fatalf("NewScopeManager() error = %v", err)
	}
	defer func() { _ = manager.Close() }()
	_, err = manager.ListContexts(context.Background())
	if err == nil {
		t.Fatal("ScopeManager.ListContexts() error = nil")
	}
	if got := ports.contextCalls.Load(); got != 1 {
		t.Fatalf("scope adapter calls = %d, want 1", got)
	}
	if got := ports.otherCalls.Load(); got != 0 {
		t.Fatalf("unrelated scope adapter calls = %d, want 0", got)
	}
	return assuranceBoundary{err: err, class: domain.SafeErrorClassInternal, canaries: []string{canary}}
}

func prepareCancelledToolBoundary(t *testing.T, ctx context.Context) assuranceBoundary {
	t.Helper()
	reader := &assuranceResourceReader{err: errors.New("unreachable reader failure")}
	tool, err := tools.NewGetResourceTool(tools.ResourceToolDependencies{
		Reader: reader, QueryReader: reader, ScopeGuard: assuranceScopeGuard{}, PolicyGuard: assuranceScopeGuard{}, EvidenceIDs: assuranceEvidenceIDs{},
		Text: security.NewRedactor(), Now: func() time.Time { return time.UnixMilli(2).UTC() },
	})
	if err != nil {
		t.Fatalf("NewGetResourceTool() error = %v", err)
	}
	input, err := agent.NewRunInput(
		domain.AgentRunID("00000000-0000-7000-8000-000000000921"),
		domain.SessionID("00000000-0000-7000-8000-000000000922"),
		domain.MessageID("00000000-0000-7000-8000-000000000923"),
		"Inspect the selected Pod.", assuranceScope(), nil, agent.DefaultRunBudgetLimits(),
	)
	if err != nil {
		t.Fatalf("agent.NewRunInput() error = %v", err)
	}
	call, err := agent.BindToolCall(input,
		domain.ToolInvocationID("00000000-0000-7000-8000-000000000924"),
		agent.ToolSelection{
			ID: "assurance-cancelled-call", Name: domain.ToolNameGetResource,
			ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect the selected Pod.","resource_type":"pods"}`,
		},
	)
	if err != nil {
		t.Fatalf("agent.BindToolCall() error = %v", err)
	}
	result := tool.Execute(ctx, call)
	if got := reader.calls.Load(); got != 0 {
		t.Fatalf("Tool reader calls after local cancellation = %d, want 0", got)
	}
	return assuranceBoundary{class: domain.SafeErrorClassCancelled, toolResult: &result}
}

func assertSecurityAssuranceBoundary(t *testing.T, boundary assuranceBoundary) {
	t.Helper()
	if boundary.err == nil || len(boundary.canaries) == 0 {
		t.Fatal("security assurance boundary is incomplete")
	}
	if got := assuranceErrorClass(boundary.err); got != string(boundary.class) {
		t.Fatalf("safe error class = %q, want %q", got, boundary.class)
	}
	wrapped := fmt.Errorf("safe boundary: %w", boundary.err)
	nested := fmt.Errorf("nested safe boundary: %w", wrapped)
	joined := errors.Join(errors.New("secondary safe failure"), nested)
	if !errors.Is(wrapped, boundary.err) || !errors.Is(joined, boundary.err) ||
		assuranceErrorClass(wrapped) != string(boundary.class) || assuranceErrorClass(joined) != string(boundary.class) {
		t.Fatal("wrapping or joining lost safe error identity or classification")
	}

	toolResult := boundary.toolResult
	if toolResult == nil {
		generated := domain.ToolResult{
			InvocationID: domain.ToolInvocationID("00000000-0000-7000-8000-000000000931"),
			Name:         domain.ToolNameGetResource,
			Version:      agent.ToolCatalogVersion,
			Scope:        assuranceScope().Snapshot(),
			ObservedAt:   time.UnixMilli(2).UTC(),
			Status:       domain.ToolResultStatusError,
			DataJSON:     `{}`,
			Error: &domain.ToolResultError{
				Class: boundary.class, SafeMessage: boundary.err.Error(),
			},
		}
		toolResult = &generated
	}
	if toolResult.Validate() != nil {
		t.Fatal("safe Tool sink projection is invalid")
	}
	toolMessage, _, err := agent.BuildToolResultContent(*toolResult)
	if err != nil {
		t.Fatalf("agent.BuildToolResultContent() error = %v", err)
	}
	modelRequest := assuranceModelRequest()
	modelRequest.Messages = append(modelRequest.Messages, toolMessage)
	if len(modelRequest.Tools) != 11 || len(modelRequest.Messages) != 2 {
		t.Fatal("safe model sink projection is invalid")
	}
	for _, message := range modelRequest.Messages {
		if !domain.ValidModelText(message, domain.MaxModelInputMessageBytes, false) {
			t.Fatal("safe model sink projection is invalid")
		}
	}
	for _, specification := range modelRequest.Tools {
		if specification.Validate() != nil {
			t.Fatal("safe model Tool projection is invalid")
		}
	}

	runID := domain.AgentRunID("00000000-0000-7000-8000-000000000932")
	uiEvents := []application.UIEvent{
		{Kind: application.UIEventRunStarted, RunID: runID, ScopeGeneration: 7, Sequence: 1},
		{Kind: application.UIEventRunFailed, RunID: runID, ScopeGeneration: 7, Sequence: 2, Text: boundary.err.Error()},
	}
	for _, event := range uiEvents {
		if event.Validate() != nil {
			t.Fatal("safe Application-to-TUI projection is invalid")
		}
	}
	tuiFrame := renderIntegrationUI(uiEvents)

	class := boundary.class
	operation := "security_assurance"
	detailCode := "safe_error"
	auditEvent := domain.AuditEvent{
		ID:      domain.AuditEventID("00000000-0000-7000-8000-000000000933"),
		Type:    domain.AuditEventPolicyDenied,
		Actor:   domain.AuditActorSystem,
		Outcome: domain.AuditOutcomeFailure,
		Details: domain.AuditDetails{
			Operation: &operation, ErrorClass: &class, DetailCode: &detailCode,
		},
		CorrelationID: "security-assurance",
		OccurredAt:    time.UnixMilli(3).UTC(),
	}
	if auditEvent.Validate() != nil {
		t.Fatal("safe AuditEvent projection is invalid")
	}

	logDirectory := t.TempDir()
	if err := os.Chmod(logDirectory, 0o700); err != nil {
		t.Fatalf("os.Chmod(log directory) error = %v", err)
	}
	logSink, err := platformlogging.Open(context.Background(), platformlogging.Options{Directory: logDirectory})
	if err != nil {
		t.Fatalf("logging.Open() error = %v", err)
	}
	logSink.Logger.ErrorContext(context.Background(), platformlogging.EventStartup,
		"component", "application",
		"operation", "configuration_load",
		"outcome", "failure",
		"error_class", string(boundary.class),
		"body", boundary.err.Error(),
	)
	if err := logSink.Close(); err != nil {
		t.Fatalf("logging.Close() error = %v", err)
	}
	logContent, err := os.ReadFile(filepath.Join(logDirectory, platformlogging.LogFileName))
	if err != nil {
		t.Fatalf("os.ReadFile(log) error = %v", err)
	}

	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", err)
	}
	stateDirectory := filepath.Join(stateRoot, "state")
	database, err := sqlite.Open(context.Background(), sqlite.OpenOptions{
		StateDir: stateDirectory, ApplicationVersion: "assurance", CorrelationID: "security-assurance",
	})
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	if err := sqlite.NewAuditRepository(database).Append(context.Background(), auditEvent); err != nil {
		_ = database.Close()
		t.Fatalf("AuditRepository.Append() error = %v", err)
	}
	openDatabaseFiles := readAssuranceFiles(t, stateDirectory)
	if err := database.Close(); err != nil {
		t.Fatalf("database.Close() error = %v", err)
	}
	closedDatabaseFiles := readAssuranceFiles(t, stateDirectory)

	childEnvironment := config.FilterChildEnvironment([]string{
		"PATH=/usr/bin:/bin",
		config.ModelAPIKeyEnvironmentVariable + "=" + boundary.canaries[0],
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Run(
		context.Background(), nil, &stdout, &stderr, buildinfo.Info{},
		func(context.Context, cli.StartIntent) error { return boundary.err },
	); code != cli.ExitFailure {
		t.Fatalf("cli.Run() exit code = %d, want %d", code, cli.ExitFailure)
	}

	sinks := map[string]string{
		"direct safe error":         fmt.Sprintf("%s\n%q\n%v\n%+v", boundary.err, boundary.err, boundary.err, boundary.err),
		"wrapped safe error":        wrapped.Error(),
		"nested safe error":         nested.Error(),
		"joined safe error":         joined.Error(),
		"Tool result":               fmt.Sprintf("%#v", toolResult),
		"model request":             fmt.Sprintf("%#v", modelRequest),
		"Application events":        fmt.Sprintf("%#v", uiEvents),
		"rendered TUI":              tuiFrame,
		"AuditEvent":                fmt.Sprintf("%#v", auditEvent),
		"ordinary log":              string(logContent),
		"open database files":       openDatabaseFiles,
		"closed database files":     closedDatabaseFiles,
		"child process environment": strings.Join(childEnvironment, "\x00"),
		"CLI stdout":                stdout.String(),
		"CLI stderr":                stderr.String(),
	}
	for _, canary := range boundary.canaries {
		if canary == "" {
			t.Fatal("security assurance canary is empty")
		}
		for name, sink := range sinks {
			if strings.Contains(sink, canary) {
				t.Fatalf("%s contains a source canary", name)
			}
		}
	}
}

type assuranceModelSink struct {
	Messages []string
	Tools    []agent.ToolSpecification
}

func assuranceModelRequest() assuranceModelSink {
	return assuranceModelSink{
		Messages: []string{"Report the bounded diagnostic result."},
		Tools:    agent.ToolSpecifications(),
	}
}

func assuranceScope() domain.ClusterScope {
	return domain.ClusterScope{
		Context: "selected", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.UnixMilli(1).UTC(),
	}
}

func assuranceErrorClass(err error) string {
	var domainClass interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &domainClass) {
		return string(domainClass.Class())
	}
	var configClass interface{ Class() config.ErrorClass }
	if errors.As(err, &configClass) {
		return string(configClass.Class())
	}
	var sqliteClass interface{ Class() sqlite.ErrorClass }
	if errors.As(err, &sqliteClass) {
		return string(sqliteClass.Class())
	}
	return ""
}

func readAssuranceFiles(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	var content strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		value, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("os.ReadFile(database sidecar) error = %v", err)
		}
		content.WriteString(entry.Name())
		content.WriteByte('\n')
		content.Write(value)
	}
	return content.String()
}

type assuranceResourceReader struct {
	calls atomic.Int32
	err   error
}

type assuranceNoopTool struct {
	calls atomic.Int32
}

func (tool *assuranceNoopTool) Execute(context.Context, agent.BoundToolCall) domain.ToolResult {
	tool.calls.Add(1)
	return domain.ToolResult{}
}

type assuranceRunIDs struct {
	counter atomic.Uint64
}

func (ids *assuranceRunIDs) next() string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", 0x940+ids.counter.Add(1))
}

func (ids *assuranceRunIDs) NewModelRequestID() (domain.ModelRequestID, error) {
	return domain.ModelRequestID(ids.next()), nil
}

func (ids *assuranceRunIDs) NewToolInvocationID() (domain.ToolInvocationID, error) {
	return domain.ToolInvocationID(ids.next()), nil
}

func (ids *assuranceRunIDs) NewDiagnosisID() (domain.DiagnosisID, error) {
	return domain.DiagnosisID(ids.next()), nil
}

type assuranceToolError struct {
	value domain.ToolResultError
}

func (err *assuranceToolError) Error() string {
	if err == nil {
		return "The Tool request failed safely."
	}
	return err.value.SafeMessage
}

func (err *assuranceToolError) Class() domain.SafeErrorClass {
	if err == nil {
		return domain.SafeErrorClassInternal
	}
	return err.value.Class
}

func (reader *assuranceResourceReader) ReadResource(context.Context, tools.ResourceReadRequest) (tools.ResourceObservation, error) {
	reader.calls.Add(1)
	return tools.ResourceObservation{}, reader.err
}

func (reader *assuranceResourceReader) ListResources(context.Context, tools.ResourceListRequest) (tools.ResourceObservationList, error) {
	reader.calls.Add(1)
	return tools.ResourceObservationList{}, reader.err
}

func (reader *assuranceResourceReader) QueryResources(context.Context, tools.ResourceQueryRequest) (tools.ResourceQueryObservation, error) {
	reader.calls.Add(1)
	return tools.ResourceQueryObservation{}, reader.err
}

type assuranceScopeGuard struct{}

func (assuranceScopeGuard) Current(context.Context, domain.ClusterScope) bool { return true }

func (assuranceScopeGuard) CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool {
	return true
}

type assuranceEvidenceIDs struct{}

func (assuranceEvidenceIDs) NewEvidenceID() (domain.EvidenceID, error) {
	return domain.EvidenceID("00000000-0000-7000-8000-000000000935"), nil
}

type assuranceScopePorts struct {
	contextCalls atomic.Int32
	otherCalls   atomic.Int32
	err          error
}

func (ports *assuranceScopePorts) Contexts(context.Context) ([]application.ContextCandidate, error) {
	ports.contextCalls.Add(1)
	return nil, ports.err
}

func (ports *assuranceScopePorts) Create(context.Context, string) (application.ScopeClient, error) {
	ports.otherCalls.Add(1)
	return nil, errors.New("unreachable scope client creation")
}

func (ports *assuranceScopePorts) VerifyNamespace(context.Context, application.ScopeClient, string) error {
	ports.otherCalls.Add(1)
	return errors.New("unreachable Namespace verification")
}

func (ports *assuranceScopePorts) ListNamespaces(context.Context, application.ScopeClient, int) (domain.NamespaceList, error) {
	ports.otherCalls.Add(1)
	return domain.NamespaceList{}, errors.New("unreachable Namespace list")
}

func (ports *assuranceScopePorts) GetResource(context.Context, application.ScopeClient, domain.ClusterScope, domain.ResourceRef) (domain.ResourceSummary, error) {
	ports.otherCalls.Add(1)
	return domain.ResourceSummary{}, errors.New("unreachable Resource read")
}

func (ports *assuranceScopePorts) ListResources(context.Context, application.ScopeClient, domain.ClusterScope, domain.ResourceKind, int) (domain.ResourceList, error) {
	ports.otherCalls.Add(1)
	return domain.ResourceList{}, errors.New("unreachable Resource list")
}

func (ports *assuranceScopePorts) InvalidateScope(int64) error {
	ports.otherCalls.Add(1)
	return errors.New("unreachable scope invalidation")
}
