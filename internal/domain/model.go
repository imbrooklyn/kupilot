package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxModelRequestBytes bounds the serialized request at the transport boundary.
	MaxModelRequestBytes = 256 * 1024
	// MaxModelStreamBytes bounds all wire bytes consumed for one response stream.
	MaxModelStreamBytes = 256 * 1024
	// MaxModelStreamEventBytes bounds one neutral event payload. Adapters apply
	// the same or a lower limit before mapping transport events.
	MaxModelStreamEventBytes = 64 * 1024
	// MaxModelStreamEvents bounds emitted neutral events in one response stream.
	MaxModelStreamEvents = 1024
	// MaxModelInputMessageBytes bounds system, user, and Tool content sent to a model.
	MaxModelInputMessageBytes = maxMessageContentBytes
	// MaxModelMessageBytes bounds one assembled assistant response. The parsed
	// answer and complete Diagnosis have additional, narrower semantic ceilings.
	MaxModelMessageBytes = maxAssistantMessageContentBytes
	// MaxModelToolArgumentsBytes bounds one assembled structured Tool argument object.
	MaxModelToolArgumentsBytes = maxToolArgumentsBytes
	// MaxModelToolCallIDBytes bounds an external Tool call correlation value.
	MaxModelToolCallIDBytes = 256
	// MaxModelToolNameBytes bounds a fixed Tool name and its streamed fragments.
	MaxModelToolNameBytes = 128
	// MaxModelErrorBodyBytes bounds discarded HTTP error-body reads.
	MaxModelErrorBodyBytes = 4096
	// MaxModelRequestTimeout is the accepted per-request ceiling.
	MaxModelRequestTimeout = 300 * time.Second

	// A maximum-size conversation contains the two initial messages, one
	// assistant turn per model call, and one result per admitted Tool call.
	maxModelMessages             = 2 + MaxAgentModelCalls + MaxAgentToolCalls
	maxModelToolSpecifications   = 7
	maxModelToolSchemaBytes      = 16 * 1024
	maxModelToolDescriptionBytes = 1024
	maxModelCorrelationIDBytes   = 128
	maxModelErrorMessageBytes    = 1024
	maxModelReportedTokens       = int64(1_000_000_000)
)

var (
	// ErrInvalidModelConfiguration reports a non-secret configuration invariant failure.
	ErrInvalidModelConfiguration = errors.New("ModelConfiguration data is invalid")
	// ErrInvalidModelRequest reports an invalid neutral request without echoing content.
	ErrInvalidModelRequest = errors.New("model request data is invalid")
	// ErrInvalidModelStreamEvent reports an invalid or ambiguous neutral stream event.
	ErrInvalidModelStreamEvent = errors.New("model stream event data is invalid")
	// ErrInvalidModelError reports an invalid safe model error projection.
	ErrInvalidModelError = errors.New("model error data is invalid")
)

// ModelAPIKeySource is a non-secret source category, never the credential value.
type ModelAPIKeySource string

const (
	// ModelAPIKeySourceRuntime means the opaque value has already been selected
	// from the admitted file, environment, or interactive source.
	ModelAPIKeySourceRuntime ModelAPIKeySource = "runtime"
)

// ModelTransportPolicy names the fixed endpoint and redirect behavior.
type ModelTransportPolicy string

const (
	// ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP requires normal HTTPS
	// verification, permits HTTP only on explicit loopback, and binds redirects
	// and Authorization to the configured canonical origin.
	ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP ModelTransportPolicy = "verified_https_or_loopback_http_same_origin"
)

// ModelReasoningEffort is the optional fixed Chat Completions reasoning mode.
// The empty value omits the provider field; none explicitly disables reasoning.
type ModelReasoningEffort string

const (
	ModelReasoningEffortOmitted ModelReasoningEffort = ""
	ModelReasoningEffortNone    ModelReasoningEffort = "none"
)

// ModelConfiguration contains only validated, serializable, non-sensitive
// settings. It contains no credential, header, client, callback, or SDK value.
type ModelConfiguration struct {
	ProviderKind        ModelProviderKind
	Endpoint            string
	Origin              string
	Model               string
	ReasoningEffort     ModelReasoningEffort
	APIKeySource        ModelAPIKeySource
	Temperature         float64
	MaxOutputTokens     int
	RequestTimeout      time.Duration
	StreamingRequired   bool
	ToolCallingRequired bool
	TransportPolicy     ModelTransportPolicy
}

// Validate checks the fixed model profile without accepting a credential.
func (configuration ModelConfiguration) Validate() error {
	if configuration.ProviderKind != ModelProviderOpenAICompatible ||
		configuration.APIKeySource != ModelAPIKeySourceRuntime ||
		configuration.TransportPolicy != ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP ||
		!configuration.StreamingRequired || !configuration.ToolCallingRequired ||
		!validModelEndpoint(configuration.Endpoint, configuration.Origin) ||
		!validModelIdentifier(configuration.Model) ||
		configuration.ReasoningEffort != ModelReasoningEffortOmitted && configuration.ReasoningEffort != ModelReasoningEffortNone ||
		math.IsNaN(configuration.Temperature) || math.IsInf(configuration.Temperature, 0) ||
		configuration.Temperature < 0 || configuration.Temperature > 0.2 ||
		configuration.MaxOutputTokens < 1 || configuration.MaxOutputTokens > 8192 ||
		configuration.RequestTimeout <= 0 || configuration.RequestTimeout > MaxModelRequestTimeout {
		return ErrInvalidModelConfiguration
	}
	return nil
}

// ModelMessageRole is a neutral request role, not a provider SDK role.
type ModelMessageRole string

const (
	ModelMessageRoleSystem    ModelMessageRole = "system"
	ModelMessageRoleUser      ModelMessageRole = "user"
	ModelMessageRoleAssistant ModelMessageRole = "assistant"
	ModelMessageRoleTool      ModelMessageRole = "tool"
)

// ModelMessage is bounded content that has already passed the model-egress
// safety pipeline. Structured assistant calls and Tool results remain typed.
type ModelMessage struct {
	Role       ModelMessageRole
	Content    string
	ToolCallID string
	ToolCalls  []ModelToolCall
}

// Validate checks one neutral model request message.
func (message ModelMessage) Validate() error {
	switch message.Role {
	case ModelMessageRoleSystem, ModelMessageRoleUser:
		if !validModelText(message.Content, MaxModelInputMessageBytes, true) || message.Content == "" || message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			return ErrInvalidModelRequest
		}
	case ModelMessageRoleAssistant:
		if !validModelText(message.Content, MaxModelMessageBytes, true) || message.ToolCallID != "" ||
			(message.Content == "") == (len(message.ToolCalls) == 0) {
			return ErrInvalidModelRequest
		}
		seen := make(map[string]struct{}, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.Validate() != nil {
				return ErrInvalidModelRequest
			}
			if _, exists := seen[call.ID]; exists {
				return ErrInvalidModelRequest
			}
			seen[call.ID] = struct{}{}
		}
	case ModelMessageRoleTool:
		if !validModelText(message.Content, MaxModelInputMessageBytes, true) || message.Content == "" || !validModelToken(message.ToolCallID, MaxModelToolCallIDBytes) || len(message.ToolCalls) != 0 {
			return ErrInvalidModelRequest
		}
	default:
		return ErrInvalidModelRequest
	}
	return nil
}

// ModelToolCall is one complete neutral structured selection used when a Tool
// result is sent in a later request.
type ModelToolCall struct {
	ID            string
	Name          ToolName
	ArgumentsJSON string
}

// Validate checks a complete Tool selection without accepting scope or limits.
func (call ModelToolCall) Validate() error {
	if !validModelToken(call.ID, MaxModelToolCallIDBytes) ||
		!call.Name.Valid() ||
		!validCanonicalToolArguments(call.ArgumentsJSON) {
		return ErrInvalidModelRequest
	}
	return nil
}

// ModelToolSpecification is one fixed, versioned, strict Tool definition.
type ModelToolSpecification struct {
	Name            ToolName
	Version         string
	Description     string
	InputSchemaJSON string
}

// Validate checks a strict canonical object schema without exposing a generic map.
func (specification ModelToolSpecification) Validate() error {
	if !specification.Name.Valid() ||
		!validModelToken(specification.Version, maxToolVersionBytes) ||
		!validModelText(specification.Description, maxModelToolDescriptionBytes, false) ||
		!validStrictModelToolSchema(specification.InputSchemaJSON) {
		return ErrInvalidModelRequest
	}
	return nil
}

// ModelRequest is one bounded neutral request. Endpoint, credential, Context,
// Namespace, deadlines, and transport controls cannot be supplied through it.
type ModelRequest struct {
	ID       ModelRequestID
	Messages []ModelMessage
	Tools    []ModelToolSpecification
}

// Validate checks the complete fixed-catalog request contract.
func (request ModelRequest) Validate() error {
	if !request.ID.Valid() || len(request.Messages) == 0 || len(request.Messages) > maxModelMessages ||
		len(request.Tools) != maxModelToolSpecifications {
		return ErrInvalidModelRequest
	}
	for _, message := range request.Messages {
		if message.Validate() != nil {
			return ErrInvalidModelRequest
		}
	}
	wanted := map[ToolName]bool{
		ToolNameGetResource:         true,
		ToolNameListResources:       true,
		ToolNameGetEvents:           true,
		ToolNameGetPodLogs:          true,
		ToolNameGetPreviousPodLogs:  true,
		ToolNameGetRelatedResources: true,
		ToolNameGetClusterOverview:  true,
	}
	for _, specification := range request.Tools {
		if specification.Validate() != nil || !wanted[specification.Name] {
			return ErrInvalidModelRequest
		}
		delete(wanted, specification.Name)
	}
	if len(wanted) != 0 {
		return ErrInvalidModelRequest
	}
	return nil
}

// ModelToolCallFragment preserves one indexed structured fragment without
// interpreting partial JSON as a Tool call.
type ModelToolCallFragment struct {
	Index             int
	IDFragment        string
	NameFragment      string
	ArgumentsFragment string
}

// Validate checks one bounded fragment. A complete call is validated separately.
func (fragment ModelToolCallFragment) Validate() error {
	if fragment.Index < 0 || fragment.Index >= maxToolCalls ||
		fragment.IDFragment == "" && fragment.NameFragment == "" && fragment.ArgumentsFragment == "" ||
		fragment.IDFragment != "" && !validModelToken(fragment.IDFragment, MaxModelToolCallIDBytes) ||
		fragment.NameFragment != "" && !validToolNameFragment(fragment.NameFragment) ||
		len(fragment.ArgumentsFragment) > MaxModelToolArgumentsBytes || !utf8.ValidString(fragment.ArgumentsFragment) {
		return ErrInvalidModelStreamEvent
	}
	return nil
}

// ModelUsage is optional, bounded provider-reported token metadata.
type ModelUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// Validate checks non-negative, internally consistent token counts.
func (usage ModelUsage) Validate() error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 ||
		usage.InputTokens > maxModelReportedTokens || usage.OutputTokens > maxModelReportedTokens ||
		usage.TotalTokens > maxModelReportedTokens ||
		usage.InputTokens > math.MaxInt64-usage.OutputTokens ||
		usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return ErrInvalidModelStreamEvent
	}
	return nil
}

// ModelResponseMetadata contains only optional, validated request metadata.
type ModelResponseMetadata struct {
	ProviderRequestID string
}

// Validate checks the bounded allowlisted response metadata.
func (metadata ModelResponseMetadata) Validate() error {
	if !validModelToken(metadata.ProviderRequestID, maxProviderRequestIDBytes) {
		return ErrInvalidModelStreamEvent
	}
	return nil
}

// ModelFinishReason is one supported neutral completion reason.
type ModelFinishReason string

const (
	ModelFinishReasonStop      ModelFinishReason = "stop"
	ModelFinishReasonToolCalls ModelFinishReason = "tool_calls"
	ModelFinishReasonLength    ModelFinishReason = "length"
)

// ModelCompletion is the sole successful terminal stream payload.
type ModelCompletion struct {
	FinishReason ModelFinishReason
}

// Validate checks the supported completion set.
func (completion ModelCompletion) Validate() error {
	switch completion.FinishReason {
	case ModelFinishReasonStop, ModelFinishReasonToolCalls, ModelFinishReasonLength:
		return nil
	default:
		return ErrInvalidModelStreamEvent
	}
}

// ModelStreamEventKind identifies one neutral stream payload.
type ModelStreamEventKind string

const (
	ModelStreamEventMetadata         ModelStreamEventKind = "metadata"
	ModelStreamEventTextDelta        ModelStreamEventKind = "text_delta"
	ModelStreamEventToolCallFragment ModelStreamEventKind = "tool_call_fragment"
	ModelStreamEventUsage            ModelStreamEventKind = "usage"
	ModelStreamEventCompleted        ModelStreamEventKind = "completed"
)

// ModelStreamEvent contains exactly one payload. Failed streams return one
// ModelError from the consumer-owned port instead of emitting a second terminal.
type ModelStreamEvent struct {
	Sequence         int
	Kind             ModelStreamEventKind
	Metadata         *ModelResponseMetadata
	TextDelta        string
	ToolCallFragment *ModelToolCallFragment
	Usage            *ModelUsage
	Completion       *ModelCompletion
}

// Validate rejects ambiguous, unbounded, or provider-shaped events.
func (event ModelStreamEvent) Validate() error {
	if event.Sequence < 1 || event.Sequence > MaxModelStreamEvents {
		return ErrInvalidModelStreamEvent
	}
	payloads := 0
	if event.Metadata != nil {
		payloads++
	}
	if event.TextDelta != "" {
		payloads++
	}
	if event.ToolCallFragment != nil {
		payloads++
	}
	if event.Usage != nil {
		payloads++
	}
	if event.Completion != nil {
		payloads++
	}
	if payloads != 1 {
		return ErrInvalidModelStreamEvent
	}
	switch event.Kind {
	case ModelStreamEventMetadata:
		if event.Metadata == nil || event.Metadata.Validate() != nil {
			return ErrInvalidModelStreamEvent
		}
	case ModelStreamEventTextDelta:
		if !validModelText(event.TextDelta, MaxModelStreamEventBytes, false) {
			return ErrInvalidModelStreamEvent
		}
	case ModelStreamEventToolCallFragment:
		if event.ToolCallFragment == nil || event.ToolCallFragment.Validate() != nil {
			return ErrInvalidModelStreamEvent
		}
	case ModelStreamEventUsage:
		if event.Usage == nil || event.Usage.Validate() != nil {
			return ErrInvalidModelStreamEvent
		}
	case ModelStreamEventCompleted:
		if event.Completion == nil || event.Completion.Validate() != nil {
			return ErrInvalidModelStreamEvent
		}
	default:
		return ErrInvalidModelStreamEvent
	}
	return nil
}

// Terminal reports whether the event is the sole successful terminal kind.
func (event ModelStreamEvent) Terminal() bool {
	return event.Kind == ModelStreamEventCompleted
}

// ModelOperation is a code-defined model boundary operation.
type ModelOperation string

const (
	ModelOperationRequest    ModelOperation = "model_request"
	ModelOperationStream     ModelOperation = "model_stream"
	ModelOperationCapability ModelOperation = "model_capability"
)

// ModelErrorCode identifies one stable safe model failure.
type ModelErrorCode string

const (
	ModelErrorCodeInvalidRequest       ModelErrorCode = "model_request_invalid"
	ModelErrorCodeRequestTooLarge      ModelErrorCode = "model_request_too_large"
	ModelErrorCodeAuthenticationFailed ModelErrorCode = "model_authentication_failed"
	ModelErrorCodePermissionDenied     ModelErrorCode = "model_permission_denied"
	ModelErrorCodeRateLimited          ModelErrorCode = "model_rate_limited"
	ModelErrorCodeServiceUnavailable   ModelErrorCode = "model_service_unavailable"
	ModelErrorCodeUnsupportedResponse  ModelErrorCode = "model_response_unsupported"
	ModelErrorCodeMalformedStream      ModelErrorCode = "model_stream_invalid"
	ModelErrorCodeStreamLimitExceeded  ModelErrorCode = "model_stream_limit_exceeded"
	ModelErrorCodeRedirectDenied       ModelErrorCode = "model_redirect_denied"
	ModelErrorCodeTimeout              ModelErrorCode = "model_request_timeout"
	ModelErrorCodeCancelled            ModelErrorCode = "model_request_cancelled"
	ModelErrorCodeInternal             ModelErrorCode = "model_internal"
)

// ModelError contains only code-defined safe fields. Raw transport errors,
// headers, URLs, and response bodies are intentionally absent.
type ModelError struct {
	class         SafeErrorClass
	code          ModelErrorCode
	operation     ModelOperation
	retryable     bool
	safeMessage   string
	correlationID string
}

// NewModelError constructs one stable safe model failure classification.
func NewModelError(code ModelErrorCode, operation ModelOperation, correlationID string) *ModelError {
	class, retryable, message, ok := modelErrorDefinition(code)
	if !ok || !validModelOperation(operation) {
		code = ModelErrorCodeInternal
		class, retryable, message, _ = modelErrorDefinition(code)
		operation = ModelOperationRequest
	}
	if !validModelToken(correlationID, maxModelCorrelationIDBytes) {
		correlationID = "model-request"
	}
	return &ModelError{
		class:         class,
		code:          code,
		operation:     operation,
		retryable:     retryable,
		safeMessage:   message,
		correlationID: correlationID,
	}
}

// Error returns only the locally selected safe message and stable code.
func (modelError *ModelError) Error() string {
	if modelError == nil {
		return "The model operation failed. (model_internal)"
	}
	return modelError.safeMessage + " (" + string(modelError.code) + ")"
}

// Is preserves cancellation and deadline semantics without retaining a cause.
func (modelError *ModelError) Is(target error) bool {
	return modelError != nil &&
		(modelError.class == SafeErrorClassCancelled && target == context.Canceled ||
			modelError.class == SafeErrorClassTimeout && target == context.DeadlineExceeded)
}

// Class returns the accepted stable safe error class.
func (modelError *ModelError) Class() SafeErrorClass {
	if modelError == nil {
		return SafeErrorClassInternal
	}
	return modelError.class
}

// Code returns the code-defined safe model error identifier.
func (modelError *ModelError) Code() ModelErrorCode {
	if modelError == nil {
		return ModelErrorCodeInternal
	}
	return modelError.code
}

// Operation returns the code-defined boundary operation.
func (modelError *ModelError) Operation() ModelOperation {
	if modelError == nil {
		return ModelOperationRequest
	}
	return modelError.operation
}

// Retryable reports classification only; it never schedules a retry.
func (modelError *ModelError) Retryable() bool {
	return modelError != nil && modelError.retryable
}

// SafeMessage returns bounded, locally selected text with no raw cause.
func (modelError *ModelError) SafeMessage() string {
	if modelError == nil {
		return "The model operation failed safely."
	}
	return modelError.safeMessage
}

// CorrelationID returns the local bounded correlation value.
func (modelError *ModelError) CorrelationID() string {
	if modelError == nil {
		return "model-request"
	}
	return modelError.correlationID
}

// Validate checks that every exposed field matches the code-defined projection.
func (modelError *ModelError) Validate() error {
	if modelError == nil {
		return ErrInvalidModelError
	}
	class, retryable, message, ok := modelErrorDefinition(modelError.code)
	if !ok || modelError.class != class || modelError.retryable != retryable || modelError.safeMessage != message ||
		!validModelOperation(modelError.operation) ||
		!validModelToken(modelError.correlationID, maxModelCorrelationIDBytes) ||
		!validModelText(modelError.safeMessage, maxModelErrorMessageBytes, false) {
		return ErrInvalidModelError
	}
	return nil
}

func modelErrorDefinition(code ModelErrorCode) (SafeErrorClass, bool, string, bool) {
	switch code {
	case ModelErrorCodeInvalidRequest:
		return SafeErrorClassInvalidInput, false, "The model request did not satisfy the bounded contract.", true
	case ModelErrorCodeRequestTooLarge:
		return SafeErrorClassBudgetExhausted, false, "The model request exceeded its byte limit.", true
	case ModelErrorCodeAuthenticationFailed:
		return SafeErrorClassAuthenticationFailed, false, "The model endpoint rejected its transport credential.", true
	case ModelErrorCodePermissionDenied:
		return SafeErrorClassPermissionDenied, false, "The model endpoint denied the admitted request.", true
	case ModelErrorCodeRateLimited:
		return SafeErrorClassRateLimited, true, "The model endpoint rate-limited the admitted request.", true
	case ModelErrorCodeServiceUnavailable:
		return SafeErrorClassUnavailable, true, "The configured model endpoint is unavailable.", true
	case ModelErrorCodeUnsupportedResponse:
		return SafeErrorClassUnsupported, false, "The model endpoint does not satisfy the supported compatibility profile.", true
	case ModelErrorCodeMalformedStream:
		return SafeErrorClassInvalidExternalResponse, false, "The model endpoint returned an invalid response stream.", true
	case ModelErrorCodeStreamLimitExceeded:
		return SafeErrorClassBudgetExhausted, false, "The model response stream exceeded a fixed limit.", true
	case ModelErrorCodeRedirectDenied:
		return SafeErrorClassPolicyDenied, false, "The model endpoint attempted a redirect outside the configured origin.", true
	case ModelErrorCodeTimeout:
		return SafeErrorClassTimeout, false, "The model request reached its deadline.", true
	case ModelErrorCodeCancelled:
		return SafeErrorClassCancelled, false, "The model request was cancelled.", true
	case ModelErrorCodeInternal:
		return SafeErrorClassInternal, false, "The model operation failed safely.", true
	default:
		return "", false, "", false
	}
}

func validModelOperation(operation ModelOperation) bool {
	return operation == ModelOperationRequest || operation == ModelOperationStream || operation == ModelOperationCapability
}

func validModelEndpoint(endpoint, origin string) bool {
	if endpoint == "" || origin == "" || len(endpoint) > 2048 || len(origin) > 2048 ||
		strings.TrimSpace(endpoint) != endpoint || strings.TrimSpace(origin) != origin {
		return false
	}
	parsedEndpoint, endpointError := url.Parse(endpoint)
	parsedOrigin, originError := url.Parse(origin)
	if endpointError != nil || originError != nil || !parsedEndpoint.IsAbs() || !parsedOrigin.IsAbs() ||
		parsedEndpoint.Opaque != "" || parsedOrigin.Opaque != "" ||
		parsedEndpoint.User != nil || parsedOrigin.User != nil ||
		parsedEndpoint.RawQuery != "" || parsedOrigin.RawQuery != "" ||
		parsedEndpoint.ForceQuery || parsedOrigin.ForceQuery ||
		parsedEndpoint.Fragment != "" || parsedOrigin.Fragment != "" ||
		parsedEndpoint.RawPath != "" || parsedOrigin.RawPath != "" ||
		parsedEndpoint.Host == "" || parsedOrigin.Host == "" ||
		parsedOrigin.Path != "" ||
		parsedEndpoint.Scheme != parsedOrigin.Scheme || parsedEndpoint.Host != parsedOrigin.Host {
		return false
	}
	if parsedEndpoint.Scheme != "https" && parsedEndpoint.Scheme != "http" {
		return false
	}
	if origin != parsedOrigin.Scheme+"://"+parsedOrigin.Host ||
		parsedEndpoint.Hostname() != strings.ToLower(parsedEndpoint.Hostname()) ||
		strings.Contains(parsedEndpoint.Path, "//") || hasModelTraversalSegment(parsedEndpoint.Path) {
		return false
	}
	for _, current := range parsedEndpoint.Path {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	if parsedEndpoint.Scheme == "http" {
		hostname := parsedEndpoint.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return false
		}
	}
	return true
}

func validModelIdentifier(value string) bool {
	if value == "" || len(value) > maxModelIdentifierBytes {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || current >= '0' && current <= '9' {
			continue
		}
		switch current {
		case '.', '_', '-', '/', ':':
		default:
			return false
		}
	}
	return true
}

func validModelToken(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validToolNameFragment(value string) bool {
	if len(value) > MaxModelToolNameBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 'a' || current > 'z' {
			if current != '_' {
				return false
			}
		}
	}
	return true
}

func validModelText(value string, maximumBytes int, allowEmpty bool) bool {
	if len(value) > maximumBytes || !utf8.ValidString(value) || !allowEmpty && value == "" {
		return false
	}
	for _, current := range value {
		if current == '\n' || current == '\t' {
			continue
		}
		if unicode.IsControl(current) || isModelBidirectionalControl(current) {
			return false
		}
	}
	return true
}

func isModelBidirectionalControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}

func validStrictModelToolSchema(value string) bool {
	if !validBoundedText(value, 2, maxModelToolSchemaBytes) || !json.Valid([]byte(value)) {
		return false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(value)) != nil || compact.String() != value {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &fields) != nil {
		return false
	}
	canonical, err := json.Marshal(fields)
	if err != nil || string(canonical) != value {
		return false
	}
	var rootType string
	if json.Unmarshal(fields["type"], &rootType) != nil || rootType != "object" {
		return false
	}
	return validStrictModelSchemaNode(fields)
}

func validStrictModelSchemaNode(fields map[string]json.RawMessage) bool {
	for _, unsupported := range []string{
		"allOf", "dependentRequired", "dependentSchemas", "if", "not", "then", "else", "uniqueItems",
	} {
		if _, exists := fields[unsupported]; exists {
			return false
		}
	}

	if schemaIncludesType(fields["type"], "object") {
		var additionalProperties bool
		if json.Unmarshal(fields["additionalProperties"], &additionalProperties) != nil || additionalProperties {
			return false
		}
		var properties map[string]json.RawMessage
		if json.Unmarshal(fields["properties"], &properties) != nil {
			return false
		}
		var required []string
		if json.Unmarshal(fields["required"], &required) != nil || len(required) != len(properties) {
			return false
		}
		seen := make(map[string]bool, len(required))
		for _, name := range required {
			if seen[name] {
				return false
			}
			if _, exists := properties[name]; !exists {
				return false
			}
			seen[name] = true
		}
		for _, raw := range properties {
			var child map[string]json.RawMessage
			if json.Unmarshal(raw, &child) != nil || !validStrictModelSchemaNode(child) {
				return false
			}
		}
	} else if _, exists := fields["properties"]; exists {
		return false
	}

	if raw, exists := fields["items"]; exists {
		var child map[string]json.RawMessage
		if json.Unmarshal(raw, &child) != nil || !validStrictModelSchemaNode(child) {
			return false
		}
	}
	if raw, exists := fields["anyOf"]; exists {
		var choices []json.RawMessage
		if json.Unmarshal(raw, &choices) != nil || len(choices) == 0 {
			return false
		}
		for _, choice := range choices {
			var child map[string]json.RawMessage
			if json.Unmarshal(choice, &child) != nil || !validStrictModelSchemaNode(child) {
				return false
			}
		}
	}
	return true
}

func schemaIncludesType(raw json.RawMessage, wanted string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == wanted
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, current := range multiple {
		if current == wanted {
			return true
		}
	}
	return false
}

func hasModelTraversalSegment(value string) bool {
	if value == "" {
		return false
	}
	if path.Clean(value) != value {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
