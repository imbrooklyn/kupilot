package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	logByteLimitReason      = "byte_limit"
	logLineLimitReason      = "line_limit"
	logWindowLimitReason    = "time_limit"
	logSensitiveWarningCode = "log_sensitive_content_blocked"
)

var (
	// ErrInvalidLogToolDependencies reports an incomplete fixed Pod log
	// dependency set.
	ErrInvalidLogToolDependencies = errors.New("Pod log Tool dependencies are invalid")
	// ErrInvalidPodLogRead reports an invalid narrow log request or source DTO
	// without exposing container output.
	ErrInvalidPodLogRead = errors.New("Pod log Tool read data is invalid")
	// ErrRawPodLogSerializationDenied prevents the bounded source payload from
	// entering a generic JSON or text sink before Tool-local sanitization.
	ErrRawPodLogSerializationDenied = errors.New("raw Pod log content cannot be serialized")
)

// PodLogAvailability is a fixed source state and never a server error string.
type PodLogAvailability string

const (
	PodLogAvailable           PodLogAvailability = "available"
	PodLogNoPreviousInstance  PodLogAvailability = "no_previous_instance"
	PodLogContainerNotRunning PodLogAvailability = "container_not_running"
)

func (availability PodLogAvailability) valid() bool {
	return availability == PodLogAvailable || availability == PodLogNoPreviousInstance || availability == PodLogContainerNotRunning
}

// PodLogReadRequest is one exact Pod/container log read. Scope, current versus
// previous semantics, and every hard limit are runtime-derived.
type PodLogReadRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Pod              domain.ResourceRef
	Namespace        string
	PodName          string
	Container        string
	Previous         bool
	TailLines        int
	SinceSeconds     int
	LimitBytes       int
}

// PodLogContent is a bounded opaque source payload. It exposes only its byte
// count across the adapter boundary and intentionally refuses serialization.
type PodLogContent struct {
	value []byte
}

// NewPodLogContent copies one adapter-bounded payload into its non-renderable
// source wrapper.
func NewPodLogContent(value []byte, maximumBytes int) (PodLogContent, error) {
	if maximumBytes < 1 || maximumBytes > domain.MaxToolResultBytes || len(value) > maximumBytes {
		return PodLogContent{}, ErrInvalidPodLogRead
	}
	return PodLogContent{value: append([]byte(nil), value...)}, nil
}

// Len returns safe accounting metadata without exposing payload bytes.
func (content PodLogContent) Len() int {
	return len(content.value)
}

// String prevents ordinary formatting from rendering source content.
func (PodLogContent) String() string {
	return "<bounded raw Pod log content>"
}

// GoString prevents detailed formatting from rendering source content.
func (PodLogContent) GoString() string {
	return "tools.PodLogContent{<bounded>}"
}

// MarshalJSON rejects accidental generic serialization before sanitization.
func (PodLogContent) MarshalJSON() ([]byte, error) {
	return nil, ErrRawPodLogSerializationDenied
}

func (content PodLogContent) bytes() []byte {
	return append([]byte(nil), content.value...)
}

// Validate rejects cross-scope or expanding log reads before an adapter action.
func (request PodLogReadRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || request.Pod.Validate() != nil || request.Pod.UID == "" || request.Pod.ResourceVersion == "" ||
		request.Pod.APIVersion != "v1" || request.Pod.Kind != "Pod" || request.Pod.Namespace != request.Namespace || request.Pod.Name != request.PodName ||
		!request.Scope.AllowsReference(request.Pod) ||
		request.Container != "" && !domain.ValidResourceName(request.Container) ||
		request.TailLines < 1 || request.TailLines > domain.MaxObservabilityLines ||
		request.SinceSeconds < 60 || request.SinceSeconds > int(domain.MaxObservabilityWindow/time.Second) ||
		request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidPodLogRead
	}
	return nil
}

func (request PodLogReadRequest) validateUnresolved() error {
	pod := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: request.Namespace, Name: request.PodName}
	if request.Pod != (domain.ResourceRef{}) || request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() ||
		domain.ValidateLiveResourceRef(pod) != nil || !request.Scope.AllowsReference(pod) ||
		request.Container != "" && !domain.ValidResourceName(request.Container) ||
		request.TailLines < 1 || request.TailLines > domain.MaxObservabilityLines ||
		request.SinceSeconds < 60 || request.SinceSeconds > int(domain.MaxObservabilityWindow/time.Second) ||
		request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidPodLogRead
	}
	return nil
}

// PodLogObservation is the bounded in-memory source DTO consumed immediately
// by the Tool safety pipeline. Content must never be logged or persisted.
type PodLogObservation struct {
	Pod                   domain.ResourceRef
	Container             string
	InitContainer         bool
	EphemeralContainer    bool
	Previous              bool
	Availability          PodLogAvailability
	RestartCount          int32
	LastTerminationReason ExternalText
	Content               PodLogContent
	Truncated             bool
}

// PodLogsReadRequest selects a deterministic bounded set of Pod containers.
// Container enumeration and continuation remain inside the Kubernetes adapter.
type PodLogsReadRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Pod              domain.ResourceRef
	Namespace        string
	PodName          string
	Previous         bool
	IncludeInit      bool
	IncludeEphemeral bool
	TailLines        int
	SinceSeconds     int
	MaxContainers    int
	LimitBytes       int
}

func (request PodLogsReadRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || request.Pod.Validate() != nil || request.Pod.UID == "" || request.Pod.ResourceVersion == "" ||
		request.Pod.APIVersion != "v1" || request.Pod.Kind != "Pod" || request.Pod.Namespace != request.Namespace || request.Pod.Name != request.PodName ||
		!request.Scope.AllowsReference(request.Pod) ||
		request.TailLines < 1 || request.TailLines > domain.MaxObservabilityLines || request.SinceSeconds < 60 || request.SinceSeconds > int(domain.MaxObservabilityWindow/time.Second) ||
		request.MaxContainers < 1 || request.MaxContainers > domain.MaxObservabilityLogContainers || request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidPodLogRead
	}
	return nil
}

type PodLogsObservation struct {
	Pod           domain.ResourceRef
	Items         []PodLogObservation
	SourceBytes   int
	Partial       bool
	Truncated     bool
	PartialReason string
}

func (observation PodLogsObservation) Validate(request PodLogsReadRequest) error {
	if request.Validate() != nil || observation.Pod != request.Pod || len(observation.Items) > request.MaxContainers ||
		observation.SourceBytes < 0 || observation.SourceBytes > request.LimitBytes || !domain.ValidModelText(observation.PartialReason, 64, true) {
		return ErrInvalidPodLogRead
	}
	for _, item := range observation.Items {
		single := PodLogReadRequest{Scope: request.Scope, PolicyGeneration: request.PolicyGeneration, Pod: request.Pod, Namespace: request.Namespace, PodName: request.PodName, Container: item.Container,
			Previous: request.Previous, TailLines: request.TailLines, SinceSeconds: request.SinceSeconds, LimitBytes: request.LimitBytes}
		if item.Validate(single) != nil {
			return ErrInvalidPodLogRead
		}
	}
	return nil
}

// Validate checks source identity, semantics, and the adapter-side byte bound.
func (observation PodLogObservation) Validate(request PodLogReadRequest) error {
	if request.Validate() != nil || observation.Pod != request.Pod ||
		!domain.ValidResourceName(observation.Container) ||
		request.Container != "" && observation.Container != request.Container ||
		observation.Previous != request.Previous || !observation.Availability.valid() || observation.RestartCount < 0 ||
		!observation.LastTerminationReason.valid(maxProjectedTextBytes) || observation.Content.Len() > request.LimitBytes {
		return ErrInvalidPodLogRead
	}
	if observation.Availability != PodLogAvailable && observation.Content.Len() != 0 {
		return ErrInvalidPodLogRead
	}
	if observation.Availability == PodLogNoPreviousInstance && !request.Previous ||
		observation.Availability == PodLogContainerNotRunning && request.Previous {
		return ErrInvalidPodLogRead
	}
	return nil
}

// PodLogReader is the Tool-owned one-operation Kubernetes log port. It cannot
// follow, stream, select another Namespace, or construct a generic subresource.
type PodLogReader interface {
	ReadPodLog(context.Context, PodLogReadRequest) (PodLogObservation, error)
}

type PodLogsReader interface {
	ReadPodLogs(context.Context, PodLogsReadRequest) (PodLogsObservation, error)
}

// LogTextProcessor preserves normalized line boundaries while applying the
// fixed sensitive-value and terminal-control policy.
type LogTextProcessor interface {
	ProcessLines(string, int) (security.TextResult, error)
}

// LogPolicyDecision is the runtime-bound privacy outcome for one log category
// transfer. The model cannot construct or widen it.
type LogPolicyDecision string

const (
	LogPolicyAllowed         LogPolicyDecision = "allowed"
	LogPolicyConsentRequired LogPolicyDecision = "consent_required"
	LogPolicyDenied          LogPolicyDecision = "denied"
)

// LogPolicyRequest contains only safe run/scope metadata and fixed instance
// semantics; it never contains log content.
type LogPolicyRequest struct {
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Previous         bool
	AllContainers    bool
	Search           bool
}

func (request LogPolicyRequest) valid() bool {
	return request.RunID.Valid() && request.SessionID.Valid() && request.Scope.Validate() == nil && request.PolicyGeneration.Valid()
}

// LogDataPolicy consumes the already-bound runtime privacy policy before any
// Kubernetes log action.
type LogDataPolicy interface {
	AuthorizeLogRead(context.Context, LogPolicyRequest) LogPolicyDecision
}

func classifyLogPolicyDecision(decision LogPolicyDecision) domain.SafeErrorClass {
	switch decision {
	case LogPolicyAllowed:
		return ""
	case LogPolicyConsentRequired:
		return domain.SafeErrorClassConsentRequired
	case LogPolicyDenied:
		return domain.SafeErrorClassPolicyDenied
	default:
		return domain.SafeErrorClassInternal
	}
}

func authorizeLogRead(ctx context.Context, request LogPolicyRequest, dependencies LogToolDependencies) domain.SafeErrorClass {
	class := classifyLogPolicyDecision(dependencies.Policy.AuthorizeLogRead(ctx, request))
	if ctx.Err() != nil {
		return classifyFailure(ctx, ctx.Err())
	}
	return class
}

// LogToolDependencies are immutable stateless dependencies shared by the two
// separately named Pod log handlers.
type LogToolDependencies struct {
	Reader      PodLogReader
	Targets     ObservationTargetResolver
	Actions     ObservationActionGate
	ScopeGuard  ScopeGuard
	PolicyGuard PolicyGenerationGuard
	EvidenceIDs EvidenceIDSource
	Text        LogTextProcessor
	Policy      LogDataPolicy
	Now         func() time.Time
}

func (dependencies LogToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.Targets == nil || dependencies.Actions == nil ||
		dependencies.ScopeGuard == nil || dependencies.PolicyGuard == nil || dependencies.EvidenceIDs == nil ||
		dependencies.Text == nil || dependencies.Policy == nil || dependencies.Now == nil ||
		!validRequiredUTCTime(dependencies.Now()) {
		return ErrInvalidLogToolDependencies
	}
	return nil
}

// GetPodLogsTool returns one bounded, sanitized current container log tail.
type GetPodLogsTool struct {
	dependencies LogToolDependencies
}

var _ agent.Tool = (*GetPodLogsTool)(nil)

// NewGetPodLogsTool validates the immutable dependencies for get_pod_logs.
func NewGetPodLogsTool(dependencies LogToolDependencies) (*GetPodLogsTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidLogToolDependencies
	}
	return &GetPodLogsTool{dependencies: dependencies}, nil
}

// Execute reads only the current container instance.
func (tool *GetPodLogsTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executePodLogTool(ctx, call, false, tool.dependencies)
}

type getPodLogsArguments struct {
	Container        string `json:"container"`
	ContainerMode    string `json:"container_mode"`
	IncludeEphemeral bool   `json:"include_ephemeral"`
	IncludeInit      bool   `json:"include_init"`
	Namespace        string `json:"namespace"`
	PodName          string `json:"pod_name"`
	Purpose          string `json:"purpose"`
	Search           string `json:"search"`
	SinceSeconds     int    `json:"since_seconds"`
	TailLines        int    `json:"tail_lines"`
}

type decodedPodLogCall struct {
	request       PodLogReadRequest
	lineLimited   bool
	windowLimited bool
	arguments     getPodLogsArguments
}

func decodePodLogCall(call BoundToolCall, previous bool) (decodedPodLogCall, error) {
	wantName := domain.ToolNameGetPodLogs
	if previous {
		wantName = domain.ToolNameGetPreviousPodLogs
	}
	if call.Validate() != nil || call.Name() != wantName || call.Version() != agent.ToolCatalogVersion {
		return decodedPodLogCall{}, ErrInvalidCanonicalArguments
	}
	var arguments getPodLogsArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		!domain.ValidResourceName(arguments.PodName) ||
		arguments.Container != "" && !domain.ValidResourceName(arguments.Container) ||
		arguments.TailLines < 1 || arguments.TailLines > domain.MaxObservabilityLines || arguments.SinceSeconds < 60 || arguments.SinceSeconds > int(domain.MaxObservabilityWindow/time.Second) ||
		(arguments.ContainerMode != "single" && arguments.ContainerMode != "all") || arguments.ContainerMode == "all" && arguments.Container != "" ||
		!domain.ValidModelText(arguments.Search, 256, true) {
		return decodedPodLogCall{}, ErrInvalidCanonicalArguments
	}
	effectiveLines := min(arguments.TailLines, call.Ceilings().MaxLogLines, domain.MaxObservabilityLines)
	effectiveWindow := min(arguments.SinceSeconds, max(1, int(call.Ceilings().MaxLogWindow/time.Second)), int(domain.MaxObservabilityWindow/time.Second))
	effectiveBytes := min(domain.MaxObservabilityBytes, call.Ceilings().MaxLogBytes)
	request := PodLogReadRequest{
		Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Namespace: arguments.Namespace, PodName: arguments.PodName, Container: arguments.Container, Previous: previous,
		TailLines: effectiveLines, SinceSeconds: effectiveWindow, LimitBytes: effectiveBytes,
	}
	if request.validateUnresolved() != nil {
		return decodedPodLogCall{}, ErrInvalidCanonicalArguments
	}
	return decodedPodLogCall{
		request:     request,
		arguments:   arguments,
		lineLimited: arguments.TailLines > effectiveLines, windowLimited: arguments.SinceSeconds > effectiveWindow,
	}, nil
}

func executePodLogTool(ctx context.Context, call BoundToolCall, previous bool, dependencies LogToolDependencies) ToolResult {
	if dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(ResourceToolDependencies{Now: dependencies.Now}, call)
	decoded, err := decodePodLogCall(call, previous)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	if ctx == nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidInput)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) || !dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	policyRequest := LogPolicyRequest{
		RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(),
		Previous: previous, AllContainers: decoded.arguments.ContainerMode == "all", Search: decoded.arguments.Search != "",
	}
	if !policyRequest.valid() {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if class := authorizeLogRead(ctx, policyRequest, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	operation := podLogActionOperation(previous, decoded.arguments.ContainerMode == "all", decoded.arguments.Search != "")
	preflight := domain.ObservationActionPreflight{
		RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(),
		PolicyGeneration: call.PolicyGeneration(), Operation: operation,
	}
	if err := dependencies.Actions.PrepareObservationAction(ctx, preflight); err != nil {
		return failedResult(call, observed, classifyFailure(ctx, err))
	}
	targetRequest := ObservationTargetRequest{
		Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Operation: operation,
		Namespace: decoded.request.Namespace, PodName: decoded.request.PodName,
		RequestedContainer: decoded.request.Container, AllContainers: decoded.arguments.ContainerMode == "all",
		IncludeInit: decoded.arguments.IncludeInit, IncludeEphemeral: decoded.arguments.IncludeEphemeral,
	}
	target, err := dependencies.Targets.ResolveObservationTarget(ctx, targetRequest)
	if err != nil {
		return failedResult(call, observed, classifyFailure(ctx, err))
	}
	if target.Validate(targetRequest) != nil || !observationCallCurrent(ctx, call, dependencies.ScopeGuard, dependencies.PolicyGuard) {
		return failedResult(call, observed, observationCurrentFailure(ctx))
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersObservation, Observation: domain.ActionObservationParameters{
		Kind: domain.ActionObservationPodLog, Container: target.Container, Previous: previous,
		AllContainers: decoded.arguments.ContainerMode == "all", IncludeInit: decoded.arguments.IncludeInit,
		IncludeEphemeral: decoded.arguments.IncludeEphemeral, Search: decoded.arguments.Search,
		WindowSeconds: decoded.request.SinceSeconds, TailLines: decoded.request.TailLines,
	}}
	items := 1
	if decoded.arguments.ContainerMode == "all" {
		items = call.Ceilings().MaxLogContainers
	}
	plan := domain.ObservationActionPlan{
		RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(),
		Operation:  operation,
		Target:     domain.ActionTarget{Resource: target.Reference, Subresource: "log", Fingerprint: string(parameters.Digest())},
		Parameters: parameters,
		Limits: domain.ActionLimits{
			Timeout: domain.ObservationKubernetesTimeout, MaximumItems: items,
			MaximumLines: decoded.request.TailLines * items, MaximumBytes: decoded.request.LimitBytes,
			MaximumOutput: call.Ceilings().MaxResultBytes,
		},
		ReasonSummary: call.Purpose(),
	}
	envelope, err := dependencies.Actions.AuthorizeObservationAction(ctx, plan)
	if err != nil {
		return failAfterObservationAuthority(ctx, call, observed, envelope, false, 0, 0, 0, false, classifyFailure(ctx, err), dependencies.Actions)
	}
	if class := observationEnvelopeCurrent(ctx, call, envelope, dependencies.Now, dependencies.ScopeGuard, dependencies.PolicyGuard); class != "" {
		return failAfterObservationAuthority(ctx, call, observed, envelope, false, 0, 0, 0, false, class, dependencies.Actions)
	}
	decoded.request.Pod, decoded.request.Container = target.Reference, target.Container
	if decoded.arguments.ContainerMode == "all" {
		return executeAllPodLogs(ctx, call, observed, decoded, dependencies, policyRequest, envelope)
	}
	readContext, cancel := context.WithTimeout(ctx, envelope.Intent.Limits.Timeout)
	defer cancel()
	observation, readErr := dependencies.Reader.ReadPodLog(readContext, decoded.request)
	if !observationCallCurrent(readContext, call, dependencies.ScopeGuard, dependencies.PolicyGuard) {
		class := observationCurrentFailure(readContext)
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 0, 0, observation.Content.Len(), observation.Truncated, class, dependencies.Actions)
	}
	if readErr != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 0, 0, 0, false, classifyFailure(readContext, readErr), dependencies.Actions)
	}
	if class := authorizeLogRead(readContext, policyRequest, dependencies); class != "" {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 1, 0, observation.Content.Len(), observation.Truncated, class, dependencies.Actions)
	}
	if observation.Validate(decoded.request) != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 0, 0, observation.Content.Len(), observation.Truncated, domain.SafeErrorClassInvalidExternalResponse, dependencies.Actions)
	}
	data, warnings, reason, projectErr := projectPodLog(dependencies, observation, decoded)
	if projectErr != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 1, 0, observation.Content.Len(), observation.Truncated, domain.SafeErrorClassInternal, dependencies.Actions)
	}
	planned, templates := fitPodLogResult(call, observed, data, warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		class := domain.SafeErrorClassBudgetExhausted
		if planned.Error != nil && planned.Error.Class.Valid() {
			class = planned.Error.Class
		}
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 1, data.LineCount, observation.Content.Len(), data.Truncated, class, dependencies.Actions)
	}
	evidence, err := materializeEvidence(
		ResourceToolDependencies{EvidenceIDs: dependencies.EvidenceIDs}, call, observed, templates,
	)
	if err != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, 1, data.LineCount, observation.Content.Len(), data.Truncated, domain.SafeErrorClassInternal, dependencies.Actions)
	}
	if planned.Truncation.Truncated {
		for index := range evidence {
			evidence[index].Truncated = true
			evidence[index].Partial = true
		}
	}
	planned.Evidence = evidence
	if recordObservationSuccess(readContext, envelope, 1, data.LineCount, observation.Content.Len(), data.Truncated, dependencies.Actions) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	return finalizePlannedResult(call, observed, planned)
}

func executeAllPodLogs(ctx context.Context, call BoundToolCall, observed time.Time, decoded decodedPodLogCall, dependencies LogToolDependencies, policyRequest LogPolicyRequest, envelope domain.ActionEnvelope) ToolResult {
	reader, ok := dependencies.Reader.(PodLogsReader)
	if !ok {
		return failAfterObservationAuthority(ctx, call, observed, envelope, false, 0, 0, 0, false, domain.SafeErrorClassUnsupported, dependencies.Actions)
	}
	request := PodLogsReadRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Pod: decoded.request.Pod, Namespace: decoded.request.Namespace, PodName: decoded.request.PodName,
		Previous: decoded.request.Previous, IncludeInit: decoded.arguments.IncludeInit, IncludeEphemeral: decoded.arguments.IncludeEphemeral,
		TailLines: decoded.request.TailLines, SinceSeconds: decoded.request.SinceSeconds, MaxContainers: call.Ceilings().MaxLogContainers, LimitBytes: decoded.request.LimitBytes}
	if request.Validate() != nil {
		return failAfterObservationAuthority(ctx, call, observed, envelope, false, 0, 0, 0, false, domain.SafeErrorClassPolicyDenied, dependencies.Actions)
	}
	readContext, cancel := context.WithTimeout(ctx, envelope.Intent.Limits.Timeout)
	defer cancel()
	observations, readErr := reader.ReadPodLogs(readContext, request)
	if !observationCallCurrent(readContext, call, dependencies.ScopeGuard, dependencies.PolicyGuard) {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, len(observations.Items), 0, observations.SourceBytes, observations.Truncated, observationCurrentFailure(readContext), dependencies.Actions)
	}
	if readErr != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, len(observations.Items), 0, observations.SourceBytes, observations.Truncated, classifyFailure(readContext, readErr), dependencies.Actions)
	}
	if class := authorizeLogRead(readContext, policyRequest, dependencies); class != "" {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, len(observations.Items), 0, observations.SourceBytes, observations.Truncated, class, dependencies.Actions)
	}
	if observations.Validate(request) != nil {
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, len(observations.Items), 0, observations.SourceBytes, observations.Truncated, domain.SafeErrorClassInvalidExternalResponse, dependencies.Actions)
	}
	result := buildAllPodLogsResult(call, observed, decoded, observations, dependencies)
	lines := 0
	for _, item := range observations.Items {
		lines += len(logLines(string(item.Content.bytes())))
	}
	if result.Status == domain.ToolResultStatusDenied || result.Status == domain.ToolResultStatusError {
		class := domain.SafeErrorClassInternal
		if result.Error != nil && result.Error.Class.Valid() {
			class = result.Error.Class
		}
		return failAfterObservationAuthority(readContext, call, observed, envelope, true, len(observations.Items), min(lines, envelope.Intent.Limits.MaximumLines), observations.SourceBytes, observations.Truncated, class, dependencies.Actions)
	}
	if recordObservationSuccess(readContext, envelope, len(observations.Items), min(lines, envelope.Intent.Limits.MaximumLines), observations.SourceBytes, observations.Truncated, dependencies.Actions) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	return result
}

func podLogActionOperation(previous, allContainers, search bool) domain.ActionOperation {
	if search {
		return domain.ActionOperationLogSearch
	}
	if allContainers {
		return domain.ActionOperationLogsAllContainers
	}
	if previous {
		return domain.ActionOperationLogsPrevious
	}
	return domain.ActionOperationLogsCurrent
}

type safePodLogData struct {
	Availability          PodLogAvailability    `json:"availability"`
	ByteCount             int                   `json:"byte_count"`
	Container             string                `json:"container"`
	Content               string                `json:"content"`
	ContentFingerprint    string                `json:"content_fingerprint,omitempty"`
	InitContainer         bool                  `json:"init_container"`
	EphemeralContainer    bool                  `json:"ephemeral_container"`
	Instance              string                `json:"instance"`
	InstructionLike       bool                  `json:"instruction_like"`
	LastTerminationReason string                `json:"last_termination_reason,omitempty"`
	LimitBytes            int                   `json:"limit_bytes"`
	LineCount             int                   `json:"line_count"`
	Pod                   safeResourceReference `json:"pod"`
	RedactionCount        int                   `json:"redaction_count"`
	RestartCount          int32                 `json:"restart_count"`
	Search                string                `json:"search,omitempty"`
	SinceSeconds          int                   `json:"since_seconds"`
	SourceTrust           string                `json:"source_trust"`
	TailLines             int                   `json:"tail_lines"`
	Truncated             bool                  `json:"truncated"`
}

func projectPodLog(
	dependencies LogToolDependencies,
	observation PodLogObservation,
	decoded decodedPodLogCall,
) (safePodLogData, []domain.ToolResultWarning, string, error) {
	pod, metadata, err := safeEventReference(logTextAdapter{dependencies.Text}, observation.Pod)
	if err != nil {
		return safePodLogData{}, nil, "", err
	}
	containerResult, err := dependencies.Text.ProcessLines(observation.Container, maxIdentityTextBytes)
	if err != nil || containerResult.Truncated || containerResult.RedactionCount != 0 || containerResult.Value == "" {
		return safePodLogData{}, nil, "", ErrInvalidPodLogRead
	}
	lastReason, lastMetadata, err := processLogExternalText(dependencies.Text, observation.LastTerminationReason, maxIdentityTextBytes)
	if err != nil {
		return safePodLogData{}, nil, "", err
	}
	metadata.merge(lastMetadata)
	instance := "current"
	if observation.Previous {
		instance = "previous"
	}
	data := safePodLogData{
		Availability: observation.Availability, Container: containerResult.Value, InitContainer: observation.InitContainer,
		EphemeralContainer: observation.EphemeralContainer,
		Instance:           instance, LastTerminationReason: lastReason, LimitBytes: decoded.request.LimitBytes,
		Pod: pod, RestartCount: observation.RestartCount, SinceSeconds: decoded.request.SinceSeconds,
		SourceTrust: security.UntrustedDataClass, TailLines: decoded.request.TailLines, Search: decoded.arguments.Search,
	}
	warnings := []domain.ToolResultWarning{}
	if metadata.blocked {
		warnings = appendWarning(warnings, logSensitiveWarningCode, "One log metadata field was hidden because it may contain sensitive data.")
	}
	if observation.Availability != PodLogAvailable {
		code := string(observation.Availability)
		message := "The selected container instance has no available log excerpt."
		if observation.Availability == PodLogNoPreviousInstance {
			message = "The selected container has no previous instance; current logs were not read as a fallback."
		}
		warnings = appendWarning(warnings, code, message)
		data.RedactionCount = metadata.redactions
		data.InstructionLike = metadata.instructionLike
		return data, warnings, "", nil
	}
	processed, processErr := dependencies.Text.ProcessLines(string(observation.Content.bytes()), decoded.request.LimitBytes)
	blocked := errors.Is(processErr, security.ErrSensitiveOutputBlocked)
	if processErr != nil && !blocked {
		return safePodLogData{}, nil, "", processErr
	}
	if blocked {
		warnings = appendWarning(warnings, logSensitiveWarningCode, "The log excerpt was blocked by the sensitive-output policy.")
		metadata.blocked = true
		metadata.truncated = true
	} else {
		metadata.redactions += processed.RedactionCount
		metadata.truncated = metadata.truncated || processed.Truncated
		metadata.instructionLike = metadata.instructionLike || processed.InstructionLike
		lines := logLines(processed.Value)
		if len(lines) > decoded.request.TailLines {
			lines = lines[len(lines)-decoded.request.TailLines:]
			decoded.lineLimited = true
		}
		if decoded.arguments.Search != "" {
			filtered := lines[:0]
			for _, line := range lines {
				if strings.Contains(line, decoded.arguments.Search) {
					filtered = append(filtered, line)
				}
			}
			lines = filtered
		}
		data.Content = strings.Join(lines, "\n")
		data.LineCount = len(lines)
		data.ByteCount = len(data.Content)
		if data.Content != "" {
			data.ContentFingerprint = domain.SHA256Hex(data.Content)
		}
	}
	data.RedactionCount = metadata.redactions
	data.InstructionLike = metadata.instructionLike
	reason := ""
	switch {
	case observation.Truncated || !blocked && processed.Truncated:
		reason = logByteLimitReason
	case decoded.lineLimited:
		reason = logLineLimitReason
	case decoded.windowLimited:
		reason = logWindowLimitReason
	case metadata.truncated || metadata.blocked:
		reason = fieldLimitReason
	}
	data.Truncated = reason != ""
	return data, warnings, reason, nil
}

type safeAllPodLogsData struct {
	Containers      []safePodLogData      `json:"containers"`
	ContainerCount  int                   `json:"container_count"`
	InstructionLike bool                  `json:"instruction_like"`
	Pod             safeResourceReference `json:"pod"`
	RedactionCount  int                   `json:"redaction_count"`
	Search          string                `json:"search,omitempty"`
	SourceTrust     string                `json:"source_trust"`
	Truncated       bool                  `json:"truncated"`
}

func buildAllPodLogsResult(call BoundToolCall, observed time.Time, decoded decodedPodLogCall, observations PodLogsObservation, dependencies LogToolDependencies) ToolResult {
	data := safeAllPodLogsData{Containers: make([]safePodLogData, 0, len(observations.Items)), ContainerCount: len(observations.Items), Search: decoded.arguments.Search,
		SourceTrust: security.UntrustedDataClass, Truncated: observations.Truncated || observations.Partial}
	templates := []evidenceTemplate{}
	warnings := []domain.ToolResultWarning{}
	for _, observation := range observations.Items {
		itemDecoded := decoded
		itemDecoded.request.Container = observation.Container
		projected, currentWarnings, reason, err := projectPodLog(dependencies, observation, itemDecoded)
		if err != nil {
			return failedResult(call, observed, domain.SafeErrorClassInternal)
		}
		data.Containers = append(data.Containers, projected)
		data.InstructionLike = data.InstructionLike || projected.InstructionLike
		data.RedactionCount += projected.RedactionCount
		data.Truncated = data.Truncated || projected.Truncated || reason != ""
		warnings = append(warnings, currentWarnings...)
		templates = append(templates, podLogEvidence(projected, observed)...)
	}
	if len(data.Containers) > 0 {
		data.Pod = data.Containers[0].Pod
	}
	partial := data.Truncated || observations.Partial
	reason := observations.PartialReason
	if reason == "" && partial {
		reason = "source_partial"
	}
	return fitDataSourceResult(call, observed, &data, templates, dependencies.EvidenceIDs, partial, reason, len(data.Containers), warnings)
}

type logTextAdapter struct {
	processor LogTextProcessor
}

func (adapter logTextAdapter) Process(value string, maximumBytes int) (security.TextResult, error) {
	return adapter.processor.ProcessLines(value, maximumBytes)
}

func processLogExternalText(processor LogTextProcessor, value ExternalText, maximumBytes int) (string, textMetadata, error) {
	processed, err := processor.ProcessLines(value.Value, maximumBytes)
	if errors.Is(err, security.ErrSensitiveOutputBlocked) {
		return "", textMetadata{truncated: true, blocked: true}, nil
	}
	if err != nil {
		return "", textMetadata{}, err
	}
	return processed.Value, textMetadata{
		redactions: processed.RedactionCount, truncated: value.Truncated || processed.Truncated,
		instructionLike: processed.InstructionLike,
	}, nil
}

func logLines(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, "\n")
}

func podLogEvidence(data safePodLogData, observed time.Time) []evidenceTemplate {
	if data.Content == "" || data.Availability != PodLogAvailable {
		return nil
	}
	reference := domainReference(data.Pod)
	instanceLabel := "Current"
	if data.Instance == "previous" {
		instanceLabel = "Previous"
	}
	fact := fmt.Sprintf("%s log excerpt for Pod %s container %s contains %d sanitized line(s) and %d byte(s); fingerprint %s.",
		instanceLabel, reference.Name, data.Container, data.LineCount, data.ByteCount, data.ContentFingerprint)
	fact, factTruncated := boundedEvidenceFact(fact)
	from, through := observed.Add(-time.Duration(data.SinceSeconds)*time.Second).UTC(), observed
	return []evidenceTemplate{{
		category: domain.EvidenceCategoryLogExcerpt, resource: reference, fact: fact,
		sourcePath:    "api/v1/namespaces/" + reference.Namespace + "/pods/" + reference.Name + "/log#" + data.Instance + ":" + data.Container,
		policyVersion: domain.ObservabilityPolicyVersion, observedFrom: &from, observedThrough: &through,
		severity:       stableSeverity(domain.EvidenceSeverityInfo),
		redactionCount: data.RedactionCount, truncated: data.Truncated || factTruncated,
	}}
}

func fitPodLogResult(
	call BoundToolCall,
	observed time.Time,
	data safePodLogData,
	warnings []domain.ToolResultWarning,
	reason string,
) (ToolResult, []evidenceTemplate) {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, guardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if guardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
	}
	outputTrimmed := false
	for attempts := 0; attempts <= 200; attempts++ {
		partial := reason != "" || outputTrimmed
		data.Truncated = data.Truncated || partial
		templates := podLogEvidence(data, observed)
		if len(templates) > call.Ceilings().MaxEvidenceItems {
			templates = templates[:call.Ceilings().MaxEvidenceItems]
		}
		preview := previewEvidence(call, observed, templates)
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The cluster read returned a shorter log tail because the fixed output limit was reached.")
		}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(),
				ObservedAt: observed, Status: domain.ToolResultStatusSuccess, DataJSON: encoded,
				Evidence: preview, Warnings: currentWarnings,
				Truncation: domain.ToolResultTruncation{Truncated: partial, ReturnedCount: data.LineCount},
			}
			if partial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Reason = reason
				if outputTrimmed {
					result.Truncation.Reason = outputLimitReason
				}
				for index := range result.Evidence {
					result.Evidence[index].Truncated = true
					result.Evidence[index].Partial = true
				}
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return measured, templates
			}
		}
		outputTrimmed = true
		lines := logLines(data.Content)
		if len(lines) == 0 {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
		}
		lines = lines[1:]
		data.Content = strings.Join(lines, "\n")
		data.LineCount = len(lines)
		data.ByteCount = len(data.Content)
		data.ContentFingerprint = ""
		if data.Content != "" {
			data.ContentFingerprint = domain.SHA256Hex(data.Content)
		}
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
}
