package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	maxToolVersionBytes       = 128
	maxToolPurposeBytes       = 1024
	maxToolArgumentsBytes     = 8192
	maxSafeSummaryBytes       = 4096
	maxSafeErrorBytes         = 4096
	maxSafeErrorClassBytes    = 64
	maxToolResultBytes        = 65536
	maxEvidencePerInvocation  = 100
	maxModelIdentifierBytes   = 128
	maxProviderRequestIDBytes = 256
)

var (
	// ErrInvalidToolInvocation reports an invalid safe ToolInvocation derivative.
	ErrInvalidToolInvocation = errors.New("ToolInvocation data is invalid")
	// ErrInvalidModelRequestMetadata reports invalid metadata without exposing model content.
	ErrInvalidModelRequestMetadata = errors.New("model request metadata is invalid")
)

// ToolInvocationID is an opaque application-generated UUIDv7 identifier.
type ToolInvocationID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id ToolInvocationID) Valid() bool {
	return validUUIDv7(string(id))
}

// ToolName is one fixed model-visible v0.1 Tool name.
type ToolName string

const (
	ToolNameGetResource         ToolName = "get_resource"
	ToolNameListResources       ToolName = "list_resources"
	ToolNameGetEvents           ToolName = "get_events"
	ToolNameGetPodLogs          ToolName = "get_pod_logs"
	ToolNameGetPreviousPodLogs  ToolName = "get_previous_pod_logs"
	ToolNameGetRelatedResources ToolName = "get_related_resources"
)

// Valid reports whether the Tool belongs to the complete v0.1 catalog.
func (name ToolName) Valid() bool {
	switch name {
	case ToolNameGetResource,
		ToolNameListResources,
		ToolNameGetEvents,
		ToolNameGetPodLogs,
		ToolNameGetPreviousPodLogs,
		ToolNameGetRelatedResources:
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

// Validate checks the complete persistence-safe ToolInvocation shape.
func (invocation ToolInvocation) Validate() error {
	if !invocation.ID.Valid() || !invocation.RunID.Valid() ||
		invocation.Sequence < 1 || invocation.Sequence > maxToolCalls ||
		!invocation.Name.Valid() ||
		!validBoundedText(invocation.Version, 1, maxToolVersionBytes) ||
		invocation.Scope.Validate() != nil ||
		!validCanonicalToolArguments(invocation.ArgumentsJSON) ||
		invocation.ArgumentsDigest != SHA256Hex(invocation.ArgumentsJSON) ||
		invocation.ReturnedBytes < 0 || invocation.ReturnedBytes > maxToolResultBytes ||
		invocation.EvidenceCount < 0 || invocation.EvidenceCount > maxEvidencePerInvocation ||
		invocation.StartedAt == nil || !validPersistenceTime(*invocation.StartedAt) {
		return ErrInvalidToolInvocation
	}
	if invocation.Purpose != nil && !validBoundedText(*invocation.Purpose, 1, maxToolPurposeBytes) ||
		invocation.SafeError != nil && !validBoundedText(*invocation.SafeError, 1, maxSafeErrorBytes) ||
		invocation.ResultSummary != nil && !validBoundedText(*invocation.ResultSummary, 1, maxSafeSummaryBytes) ||
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

// ModelProviderKind is the fixed v0.1 model-provider contract.
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return false
	}
	canonical, err := json.Marshal(fields)
	if err != nil || string(canonical) != value {
		return false
	}
	for name := range fields {
		switch strings.ToLower(name) {
		case "context", "namespace", "scope", "endpoint", "credential", "credentials",
			"api_key", "token", "kubeconfig", "deadline", "timeout", "limit", "max_bytes", "max_items":
			return false
		}
	}
	return true
}
