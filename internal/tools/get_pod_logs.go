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
	Scope        domain.ClusterScope
	Namespace    string
	PodName      string
	Container    string
	Previous     bool
	TailLines    int
	SinceSeconds int
	LimitBytes   int
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
	pod := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: request.Namespace, Name: request.PodName}
	if request.Scope.Validate() != nil || domain.ValidateLiveResourceRef(pod) != nil || !request.Scope.AllowsReference(pod) ||
		request.Container != "" && !domain.ValidResourceName(request.Container) ||
		request.TailLines < 1 || request.TailLines > 200 || request.SinceSeconds < 1 || request.SinceSeconds > 900 ||
		request.LimitBytes < 1 || request.LimitBytes > domain.MaxToolResultBytes {
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
	Previous              bool
	Availability          PodLogAvailability
	RestartCount          int32
	LastTerminationReason ExternalText
	Content               PodLogContent
	Truncated             bool
}

// Validate checks source identity, semantics, and the adapter-side byte bound.
func (observation PodLogObservation) Validate(request PodLogReadRequest) error {
	if request.Validate() != nil || domain.ValidateLiveResourceRef(observation.Pod) != nil ||
		observation.Pod.APIVersion != "v1" || observation.Pod.Kind != "Pod" ||
		observation.Pod.Namespace != request.Namespace || observation.Pod.Name != request.PodName ||
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
	RunID    domain.AgentRunID
	Scope    domain.ClusterScope
	Previous bool
}

func (request LogPolicyRequest) valid() bool {
	return request.RunID.Valid() && request.Scope.Validate() == nil
}

// LogDataPolicy consumes the already-bound runtime privacy policy before any
// Kubernetes log action.
type LogDataPolicy interface {
	AuthorizeLogRead(context.Context, LogPolicyRequest) LogPolicyDecision
}

// LogToolDependencies are immutable stateless dependencies shared by the two
// separately named Pod log handlers.
type LogToolDependencies struct {
	Reader      PodLogReader
	ScopeGuard  ScopeGuard
	EvidenceIDs EvidenceIDSource
	Text        LogTextProcessor
	Policy      LogDataPolicy
	Now         func() time.Time
}

func (dependencies LogToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.ScopeGuard == nil || dependencies.EvidenceIDs == nil ||
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
	Container    string `json:"container,omitempty"`
	Namespace    string `json:"namespace"`
	PodName      string `json:"pod_name"`
	Purpose      string `json:"purpose"`
	SinceSeconds int    `json:"since_seconds"`
	TailLines    int    `json:"tail_lines"`
}

type decodedPodLogCall struct {
	request       PodLogReadRequest
	lineLimited   bool
	windowLimited bool
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
		arguments.TailLines < 1 || arguments.TailLines > 200 || arguments.SinceSeconds < 60 || arguments.SinceSeconds > 3600 {
		return decodedPodLogCall{}, ErrInvalidCanonicalArguments
	}
	effectiveLines := min(arguments.TailLines, call.Ceilings().MaxLogLines, 200)
	effectiveWindow := min(arguments.SinceSeconds, max(1, int(call.Ceilings().MaxLogWindow/time.Second)), 900)
	effectiveBytes := min(domain.MaxToolResultBytes, call.Ceilings().MaxResultBytes)
	request := PodLogReadRequest{
		Scope: call.Scope(), Namespace: arguments.Namespace, PodName: arguments.PodName, Container: arguments.Container, Previous: previous,
		TailLines: effectiveLines, SinceSeconds: effectiveWindow, LimitBytes: effectiveBytes,
	}
	if request.Validate() != nil {
		return decodedPodLogCall{}, ErrInvalidCanonicalArguments
	}
	return decodedPodLogCall{
		request:     request,
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
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	policyRequest := LogPolicyRequest{RunID: call.RunID(), Scope: call.Scope(), Previous: previous}
	if !policyRequest.valid() {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	decision := dependencies.Policy.AuthorizeLogRead(ctx, policyRequest)
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	switch decision {
	case LogPolicyAllowed:
	case LogPolicyConsentRequired:
		return failedResult(call, observed, domain.SafeErrorClassConsentRequired)
	case LogPolicyDenied:
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	default:
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	observation, readErr := dependencies.Reader.ReadPodLog(ctx, decoded.request)
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, observed, classifyFailure(ctx, readErr))
	}
	if observation.Validate(decoded.request) != nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	data, warnings, reason, projectErr := projectPodLog(dependencies, observation, decoded)
	if projectErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	planned, templates := fitPodLogResult(call, observed, data, warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		return planned
	}
	evidence, err := materializeEvidence(
		ResourceToolDependencies{EvidenceIDs: dependencies.EvidenceIDs}, call, observed, templates,
	)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if planned.Truncation.Truncated {
		for index := range evidence {
			evidence[index].Truncated = true
		}
	}
	planned.Evidence = evidence
	return finalizePlannedResult(call, observed, planned)
}

type safePodLogData struct {
	Availability          PodLogAvailability    `json:"availability"`
	ByteCount             int                   `json:"byte_count"`
	Container             string                `json:"container"`
	Content               string                `json:"content"`
	ContentFingerprint    string                `json:"content_fingerprint,omitempty"`
	InitContainer         bool                  `json:"init_container"`
	Instance              string                `json:"instance"`
	InstructionLike       bool                  `json:"instruction_like"`
	LastTerminationReason string                `json:"last_termination_reason,omitempty"`
	LimitBytes            int                   `json:"limit_bytes"`
	LineCount             int                   `json:"line_count"`
	Pod                   safeResourceReference `json:"pod"`
	RedactionCount        int                   `json:"redaction_count"`
	RestartCount          int32                 `json:"restart_count"`
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
		Instance: instance, LastTerminationReason: lastReason, LimitBytes: decoded.request.LimitBytes,
		Pod: pod, RestartCount: observation.RestartCount, SinceSeconds: decoded.request.SinceSeconds,
		SourceTrust: security.UntrustedDataClass, TailLines: decoded.request.TailLines,
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

func podLogEvidence(data safePodLogData) []evidenceTemplate {
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
	return []evidenceTemplate{{
		category: domain.EvidenceCategoryLogExcerpt, resource: reference, fact: fact,
		sourcePath: "projected.logs." + data.Instance, severity: stableSeverity(domain.EvidenceSeverityInfo),
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
		templates := podLogEvidence(data)
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
