package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	// MaxToolResultBytes is the complete serialized ceiling for one ToolResult.
	MaxToolResultBytes = 64 * 1024
	// MaxEvidenceItemsPerResult is the Evidence item ceiling for one ToolResult.
	MaxEvidenceItemsPerResult = 100

	maxToolVersionBytes       = 128
	maxToolPurposeBytes       = 1024
	maxToolArgumentsBytes     = 8192
	maxSafeSummaryBytes       = 4096
	maxSafeErrorBytes         = 4096
	maxSafeErrorClassBytes    = 64
	maxToolResultBytes        = MaxToolResultBytes
	maxEvidencePerInvocation  = MaxEvidenceItemsPerResult
	maxModelIdentifierBytes   = 128
	maxProviderRequestIDBytes = 256
	maxToolResultWarnings     = 50
	maxToolWarningCodeBytes   = 64
)

var (
	// ErrInvalidToolInvocation reports an invalid safe ToolInvocation derivative.
	ErrInvalidToolInvocation = errors.New("ToolInvocation data is invalid")
	// ErrInvalidModelRequestMetadata reports invalid metadata without exposing model content.
	ErrInvalidModelRequestMetadata = errors.New("model request metadata is invalid")
	// ErrInvalidToolResult reports an invalid ephemeral safe Tool result.
	ErrInvalidToolResult = errors.New("ToolResult data is invalid")
)

// ToolInvocationID is an opaque application-generated UUIDv7 identifier.
type ToolInvocationID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id ToolInvocationID) Valid() bool {
	return validUUIDv7(string(id))
}

// ToolName is one fixed model-visible built-in capability name.
type ToolName string

const (
	ToolNameGetResource         ToolName = "get_resource"
	ToolNameListResources       ToolName = "list_resources"
	ToolNameGetEvents           ToolName = "get_events"
	ToolNameGetPodLogs          ToolName = "get_pod_logs"
	ToolNameGetPreviousPodLogs  ToolName = "get_previous_pod_logs"
	ToolNameGetRelatedResources ToolName = "get_related_resources"
	ToolNameGetClusterOverview  ToolName = "get_cluster_overview"
)

// Valid reports whether the Tool belongs to the complete current catalog.
func (name ToolName) Valid() bool {
	switch name {
	case ToolNameGetResource,
		ToolNameListResources,
		ToolNameGetEvents,
		ToolNameGetPodLogs,
		ToolNameGetPreviousPodLogs,
		ToolNameGetRelatedResources,
		ToolNameGetClusterOverview:
		return true
	default:
		return false
	}
}

// SafeErrorClass is a stable project-owned failure class, never raw adapter text.
type SafeErrorClass string

const (
	SafeErrorClassInvalidInput            SafeErrorClass = "invalid_input"
	SafeErrorClassConfigurationInvalid    SafeErrorClass = "configuration_invalid"
	SafeErrorClassConsentRequired         SafeErrorClass = "consent_required"
	SafeErrorClassAuthenticationFailed    SafeErrorClass = "authentication_failed"
	SafeErrorClassPermissionDenied        SafeErrorClass = "permission_denied"
	SafeErrorClassNotFound                SafeErrorClass = "not_found"
	SafeErrorClassConflict                SafeErrorClass = "conflict"
	SafeErrorClassUnsupported             SafeErrorClass = "unsupported"
	SafeErrorClassPolicyDenied            SafeErrorClass = "policy_denied"
	SafeErrorClassStaleScope              SafeErrorClass = "stale_scope"
	SafeErrorClassBudgetExhausted         SafeErrorClass = "budget_exhausted"
	SafeErrorClassRateLimited             SafeErrorClass = "rate_limited"
	SafeErrorClassUnavailable             SafeErrorClass = "unavailable"
	SafeErrorClassTimeout                 SafeErrorClass = "timeout"
	SafeErrorClassCancelled               SafeErrorClass = "cancelled"
	SafeErrorClassSensitiveOutputBlocked  SafeErrorClass = "sensitive_output_blocked"
	SafeErrorClassInvalidExternalResponse SafeErrorClass = "invalid_external_response"
	SafeErrorClassPersistenceUnavailable  SafeErrorClass = "persistence_unavailable"
	SafeErrorClassInternal                SafeErrorClass = "internal"
)

// Valid reports whether the class is in the accepted stable catalog.
func (class SafeErrorClass) Valid() bool {
	if !validBoundedText(string(class), 1, maxSafeErrorClassBytes) {
		return false
	}
	switch class {
	case SafeErrorClassInvalidInput,
		SafeErrorClassConfigurationInvalid,
		SafeErrorClassConsentRequired,
		SafeErrorClassAuthenticationFailed,
		SafeErrorClassPermissionDenied,
		SafeErrorClassNotFound,
		SafeErrorClassConflict,
		SafeErrorClassUnsupported,
		SafeErrorClassPolicyDenied,
		SafeErrorClassStaleScope,
		SafeErrorClassBudgetExhausted,
		SafeErrorClassRateLimited,
		SafeErrorClassUnavailable,
		SafeErrorClassTimeout,
		SafeErrorClassCancelled,
		SafeErrorClassSensitiveOutputBlocked,
		SafeErrorClassInvalidExternalResponse,
		SafeErrorClassPersistenceUnavailable,
		SafeErrorClassInternal:
		return true
	default:
		return false
	}
}

// ToolInvocationStatus is the durable state of a bounded Tool call derivative.
type ToolInvocationStatus string

const (
	ToolInvocationStatusRequested ToolInvocationStatus = "requested"
	ToolInvocationStatusRunning   ToolInvocationStatus = "running"
	ToolInvocationStatusSucceeded ToolInvocationStatus = "succeeded"
	ToolInvocationStatusFailed    ToolInvocationStatus = "failed"
	ToolInvocationStatusCancelled ToolInvocationStatus = "cancelled"
	ToolInvocationStatusDenied    ToolInvocationStatus = "denied"
)

// Terminal reports whether no later ToolInvocation state is admitted.
func (status ToolInvocationStatus) Terminal() bool {
	return status == ToolInvocationStatusSucceeded ||
		status == ToolInvocationStatusFailed ||
		status == ToolInvocationStatusCancelled ||
		status == ToolInvocationStatusDenied
}

// ToolInvocation contains only canonical safe metadata and no ToolResult body.
type ToolInvocation struct {
	ID              ToolInvocationID
	RunID           AgentRunID
	Sequence        int
	Name            ToolName
	Version         string
	Purpose         *string
	Scope           ScopeSnapshot
	ArgumentsJSON   string
	ArgumentsDigest string
	Status          ToolInvocationStatus
	ErrorClass      *SafeErrorClass
	SafeError       *string
	ResultSummary   *string
	ReturnedBytes   int
	EvidenceCount   int
	Truncated       bool
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// ToolResultStatus is the outcome of one bounded read-only Tool call.
type ToolResultStatus string

const (
	ToolResultStatusSuccess ToolResultStatus = "success"
	ToolResultStatusPartial ToolResultStatus = "partial"
	ToolResultStatusError   ToolResultStatus = "error"
	ToolResultStatusDenied  ToolResultStatus = "denied"
)

// ToolResultError is a stable safe failure projection. It contains no raw cause.
type ToolResultError struct {
	Class       SafeErrorClass `json:"class"`
	Retryable   bool           `json:"retryable"`
	SafeMessage string         `json:"safe_message"`
}

func (toolError ToolResultError) valid() bool {
	if !toolError.Class.Valid() || !validSafeToolText(toolError.SafeMessage, maxSafeErrorBytes) {
		return false
	}
	if !toolError.Retryable {
		return true
	}
	switch toolError.Class {
	case SafeErrorClassConflict, SafeErrorClassRateLimited, SafeErrorClassUnavailable, SafeErrorClassTimeout:
		return true
	default:
		return false
	}
}

// ToolResultWarning is one bounded code-defined warning with safe text.
type ToolResultWarning struct {
	Code        string `json:"code"`
	SafeMessage string `json:"safe_message"`
}

func (warning ToolResultWarning) valid() bool {
	return validSafeToolToken(warning.Code, maxToolWarningCodeBytes) &&
		validSafeToolText(warning.SafeMessage, maxSafeErrorBytes)
}

// ToolResultTruncation reports only bounded counts and a safe reason. It never
// contains discarded content.
type ToolResultTruncation struct {
	Truncated     bool   `json:"truncated"`
	Reason        string `json:"reason,omitempty"`
	OriginalCount *int   `json:"original_count,omitempty"`
	ReturnedCount int    `json:"returned_count"`
	ReturnedBytes int    `json:"returned_bytes"`
}

func (truncation ToolResultTruncation) valid() bool {
	if truncation.ReturnedCount < 0 || truncation.ReturnedBytes < 0 || truncation.ReturnedBytes > MaxToolResultBytes ||
		truncation.OriginalCount != nil && *truncation.OriginalCount < truncation.ReturnedCount {
		return false
	}
	if truncation.Truncated {
		return validSafeToolToken(truncation.Reason, maxToolWarningCodeBytes)
	}
	return truncation.Reason == "" && truncation.OriginalCount == nil
}

// ToolResult is an ephemeral, project-owned safe envelope. DataJSON is the
// canonical serialization of a Tool-specific safe DTO, not a raw source object
// and never a persistence payload.
type ToolResult struct {
	InvocationID ToolInvocationID
	Name         ToolName
	Version      string
	Scope        ScopeSnapshot
	ObservedAt   time.Time
	Status       ToolResultStatus
	DataJSON     string
	Evidence     []Evidence
	// ResourceSummaries is the local-only safe projection used to render
	// list_resources results deterministically. It is excluded from the model
	// envelope and persistence.
	ResourceSummaries []ResourceSummary
	Warnings          []ToolResultWarning
	Truncation        ToolResultTruncation
	Error             *ToolResultError
}

// Validate checks the safe ToolResult envelope and all Evidence provenance that
// can be decided without the owning AgentRun registry.
func (result ToolResult) Validate() error {
	if !result.InvocationID.Valid() || !result.Name.Valid() ||
		!validModelToken(result.Version, maxToolVersionBytes) ||
		!validToolScopeSnapshot(result.Scope) || !validPersistenceTime(result.ObservedAt) ||
		!validCanonicalSafeJSONObject(result.DataJSON, MaxToolResultBytes) ||
		len(result.Evidence) > MaxEvidenceItemsPerResult || len(result.Warnings) > maxToolResultWarnings ||
		!result.Truncation.valid() || !validToolResultResourceSummaries(result) {
		return ErrInvalidToolResult
	}
	switch result.Status {
	case ToolResultStatusSuccess:
		if result.Error != nil || result.Truncation.Truncated {
			return ErrInvalidToolResult
		}
	case ToolResultStatusPartial:
		if result.Error != nil && !result.Error.valid() {
			return ErrInvalidToolResult
		}
	case ToolResultStatusError, ToolResultStatusDenied:
		if result.Error == nil || !result.Error.valid() || len(result.Evidence) != 0 {
			return ErrInvalidToolResult
		}
	default:
		return ErrInvalidToolResult
	}
	seenEvidence := make(map[EvidenceID]struct{}, len(result.Evidence))
	for _, evidence := range result.Evidence {
		if evidence.Validate() != nil || evidence.InvocationID != result.InvocationID ||
			evidence.Scope != result.Scope || evidence.ObservedAt.After(result.ObservedAt) {
			return ErrInvalidToolResult
		}
		if _, exists := seenEvidence[evidence.ID]; exists {
			return ErrInvalidToolResult
		}
		seenEvidence[evidence.ID] = struct{}{}
	}
	for _, warning := range result.Warnings {
		if !warning.valid() {
			return ErrInvalidToolResult
		}
	}
	return nil
}

func validToolResultResourceSummaries(result ToolResult) bool {
	if len(result.ResourceSummaries) == 0 {
		return true
	}
	if result.Name != ToolNameListResources && result.Name != ToolNameGetClusterOverview ||
		len(result.ResourceSummaries) != len(result.Evidence) ||
		len(result.ResourceSummaries) > MaxResourceSummaries {
		return false
	}
	seen := make(map[ResourceRef]struct{}, len(result.ResourceSummaries))
	for index, summary := range result.ResourceSummaries {
		if summary.Validate() != nil ||
			result.Evidence[index].Category != EvidenceCategoryResourceStatus ||
			result.Evidence[index].Resource != summary.Reference {
			return false
		}
		if _, duplicate := seen[summary.Reference]; duplicate {
			return false
		}
		seen[summary.Reference] = struct{}{}
	}
	return true
}

// Retryable reports the stable Tool error classification only. It never
// schedules a retry.
func (result ToolResult) Retryable() bool {
	return result.Error != nil && result.Error.Retryable
}

// Validate checks the complete persistence-safe ToolInvocation shape.
func (invocation ToolInvocation) Validate() error {
	if !invocation.ID.Valid() || !invocation.RunID.Valid() ||
		invocation.Sequence < 1 || invocation.Sequence > maxToolCalls ||
		!invocation.Name.Valid() ||
		!validModelToken(invocation.Version, maxToolVersionBytes) ||
		!validToolScopeSnapshot(invocation.Scope) ||
		!validCanonicalToolArguments(invocation.ArgumentsJSON) ||
		invocation.ArgumentsDigest != SHA256Hex(invocation.ArgumentsJSON) ||
		invocation.ReturnedBytes < 0 || invocation.ReturnedBytes > maxToolResultBytes ||
		invocation.EvidenceCount < 0 || invocation.EvidenceCount > maxEvidencePerInvocation ||
		invocation.StartedAt == nil || !validPersistenceTime(*invocation.StartedAt) {
		return ErrInvalidToolInvocation
	}
	if invocation.Purpose != nil && !validModelText(*invocation.Purpose, maxToolPurposeBytes, false) ||
		invocation.SafeError != nil && !validModelText(*invocation.SafeError, maxSafeErrorBytes, false) ||
		invocation.ResultSummary != nil && !validModelText(*invocation.ResultSummary, maxSafeSummaryBytes, false) ||
		invocation.ErrorClass != nil && !invocation.ErrorClass.Valid() {
		return ErrInvalidToolInvocation
	}
	if invocation.Status.Terminal() {
		if invocation.FinishedAt == nil || !validPersistenceTime(*invocation.FinishedAt) || invocation.FinishedAt.Before(*invocation.StartedAt) {
			return ErrInvalidToolInvocation
		}
	} else if invocation.Status == ToolInvocationStatusRequested || invocation.Status == ToolInvocationStatusRunning {
		if invocation.FinishedAt != nil || invocation.ErrorClass != nil || invocation.SafeError != nil ||
			invocation.ResultSummary != nil || invocation.ReturnedBytes != 0 || invocation.EvidenceCount != 0 || invocation.Truncated {
			return ErrInvalidToolInvocation
		}
	} else {
		return ErrInvalidToolInvocation
	}
	if invocation.Status == ToolInvocationStatusSucceeded {
		if invocation.ErrorClass != nil || invocation.SafeError != nil {
			return ErrInvalidToolInvocation
		}
	} else if invocation.Status.Terminal() && (invocation.ErrorClass == nil || invocation.SafeError == nil) {
		return ErrInvalidToolInvocation
	}
	return nil
}

// ModelRequestID is an opaque application-generated UUIDv7 identifier.
type ModelRequestID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id ModelRequestID) Valid() bool {
	return validUUIDv7(string(id))
}

// ModelProviderKind is the fixed model-provider contract.
type ModelProviderKind string

const ModelProviderOpenAICompatible ModelProviderKind = "openai_compatible"

// ModelRequestStatus is one safe request lifecycle state.
type ModelRequestStatus string

const (
	ModelRequestStatusRequested ModelRequestStatus = "requested"
	ModelRequestStatusRunning   ModelRequestStatus = "running"
	ModelRequestStatusSucceeded ModelRequestStatus = "succeeded"
	ModelRequestStatusFailed    ModelRequestStatus = "failed"
	ModelRequestStatusCancelled ModelRequestStatus = "cancelled"
	ModelRequestStatusTimedOut  ModelRequestStatus = "timed_out"
)

// Terminal reports whether no later metadata transition is admitted.
func (status ModelRequestStatus) Terminal() bool {
	return status == ModelRequestStatusSucceeded ||
		status == ModelRequestStatusFailed ||
		status == ModelRequestStatusCancelled ||
		status == ModelRequestStatusTimedOut
}

// ModelRequestMetadata excludes prompts, response bodies, headers, and streams.
type ModelRequestMetadata struct {
	ID                  ModelRequestID
	RunID               AgentRunID
	Sequence            int
	ProviderKind        ModelProviderKind
	EndpointOriginHash  *string
	Model               string
	Status              ModelRequestStatus
	ErrorClass          *SafeErrorClass
	ProviderRequestID   *string
	PromptVersion       string
	PromptFingerprint   string
	ResponseFingerprint *string
	InputTokens         *int64
	OutputTokens        *int64
	LatencyMilliseconds *int64
	StartedAt           time.Time
	FinishedAt          *time.Time
}

// Validate checks bounded metadata without accepting any model body field.
func (request ModelRequestMetadata) Validate() error {
	if !request.ID.Valid() || !request.RunID.Valid() ||
		request.Sequence < 1 || request.Sequence > maxModelRequests ||
		request.ProviderKind != ModelProviderOpenAICompatible ||
		!validBoundedText(request.Model, 1, maxModelIdentifierBytes) ||
		!validBoundedText(request.PromptVersion, 1, maxPromptVersionBytes) ||
		!validSHA256Hex(request.PromptFingerprint) ||
		!validPersistenceTime(request.StartedAt) ||
		request.EndpointOriginHash != nil && !validSHA256Hex(*request.EndpointOriginHash) ||
		request.ProviderRequestID != nil && !validBoundedText(*request.ProviderRequestID, 1, maxProviderRequestIDBytes) ||
		request.ResponseFingerprint != nil && !validSHA256Hex(*request.ResponseFingerprint) ||
		request.ErrorClass != nil && !request.ErrorClass.Valid() ||
		request.InputTokens != nil && *request.InputTokens < 0 ||
		request.OutputTokens != nil && *request.OutputTokens < 0 ||
		request.LatencyMilliseconds != nil && *request.LatencyMilliseconds < 0 {
		return ErrInvalidModelRequestMetadata
	}
	if request.Status.Terminal() {
		if request.FinishedAt == nil || !validPersistenceTime(*request.FinishedAt) || request.FinishedAt.Before(request.StartedAt) {
			return ErrInvalidModelRequestMetadata
		}
	} else if request.Status == ModelRequestStatusRequested || request.Status == ModelRequestStatusRunning {
		if request.FinishedAt != nil || request.ResponseFingerprint != nil || request.InputTokens != nil ||
			request.OutputTokens != nil || request.LatencyMilliseconds != nil || request.ErrorClass != nil {
			return ErrInvalidModelRequestMetadata
		}
	} else {
		return ErrInvalidModelRequestMetadata
	}
	if request.Status == ModelRequestStatusSucceeded {
		if request.ErrorClass != nil {
			return ErrInvalidModelRequestMetadata
		}
	} else if request.Status.Terminal() && request.ErrorClass == nil {
		return ErrInvalidModelRequestMetadata
	}
	return nil
}

func validPersistenceTime(value time.Time) bool {
	return validDurableTime(value) && value.Location() == time.UTC && value.Nanosecond()%int(time.Millisecond) == 0
}

// SHA256Hex returns the lowercase digest of an independently eligible derivative.
func SHA256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, current := range []byte(value) {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func validCanonicalToolArguments(value string) bool {
	if !validBoundedText(value, 2, maxToolArgumentsBytes) || value[0] != '{' || value[len(value)-1] != '}' || !json.Valid([]byte(value)) {
		return false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(value)); err != nil || compact.String() != value {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false
	}
	fields, ok := decoded.(map[string]any)
	if !ok {
		return false
	}
	canonical, err := json.Marshal(fields)
	if err != nil || string(canonical) != value {
		return false
	}
	items := 0
	return !containsToolAuthorityField(fields) && validSafeJSONValue(fields, 0, &items)
}

func containsToolAuthorityField(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if containsToolAuthorityField(item) {
				return true
			}
		}
	case map[string]any:
		for name, item := range typed {
			switch strings.ToLower(name) {
			case "context", "scope", "endpoint", "credential", "credentials",
				"api_key", "token", "kubeconfig", "deadline", "timeout", "gvr",
				"group_version_resource", "raw_selector", "max_bytes", "max_items",
				"max_result_bytes", "max_evidence_items":
				return true
			}
			if containsToolAuthorityField(item) {
				return true
			}
		}
	}
	return false
}

func validCanonicalSafeJSONObject(value string, maximumBytes int) bool {
	if !validBoundedText(value, 2, maximumBytes) || !json.Valid([]byte(value)) || value[0] != '{' || value[len(value)-1] != '}' {
		return false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(value)); err != nil || compact.String() != value {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return false
	}
	canonical, err := json.Marshal(object)
	if err != nil || string(canonical) != value {
		return false
	}
	items := 0
	return validSafeJSONValue(object, 0, &items)
}

func validSafeJSONValue(value any, depth int, items *int) bool {
	if depth > 16 || *items >= 10_000 {
		return false
	}
	(*items)++
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return true
	case string:
		return validModelText(typed, MaxToolResultBytes, true)
	case []any:
		for _, item := range typed {
			if !validSafeJSONValue(item, depth+1, items) {
				return false
			}
		}
		return true
	case map[string]any:
		for key, item := range typed {
			if !validModelText(key, 256, false) || !validSafeJSONValue(item, depth+1, items) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validSafeToolText(value string, maximumBytes int) bool {
	return validModelText(value, maximumBytes, false)
}

func validSafeToolToken(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '_' || current == '-' {
			continue
		}
		return false
	}
	return true
}

func validToolScopeSnapshot(scope ScopeSnapshot) bool {
	return scope.Validate() == nil && ValidContextName(scope.Context) &&
		ValidNamespaceName(scope.Namespace) && scope.Generation >= 1
}
