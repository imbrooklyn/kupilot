// Package openaicompat implements the single bounded model transport admitted
// by the current compatibility contract.
package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einocallbacks "github.com/cloudwego/eino/callbacks"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	maxModelRedirects            = 3
	maxModelResponseHeaderBytes  = 64 * 1024
	einoCredentialPlaceholder    = "kupilot-transport-managed"
	modelRequestLogEvent         = "model_request"
	maxSensitiveErrorLogBytes    = 16 * 1024
	maxSensitiveEndpointLogBytes = 4096
	maxSensitiveModelLogBytes    = 128
	sensitiveRedactionMarker     = "[REDACTED]"
)

type modelFailureCause string

const (
	modelFailureAdapterInvariant     modelFailureCause = "adapter_invariant"
	modelFailureContextCancelled     modelFailureCause = "context_cancelled"
	modelFailureDeadlineExceeded     modelFailureCause = "deadline_exceeded"
	modelFailureHTTPStatus           modelFailureCause = "http_status"
	modelFailureRedirectPolicy       modelFailureCause = "redirect_policy"
	modelFailureResponseMedia        modelFailureCause = "response_media"
	modelFailureStreamLimit          modelFailureCause = "stream_limit"
	modelFailureStreamProtocol       modelFailureCause = "stream_protocol"
	modelFailureTransportTimeout     modelFailureCause = "transport_timeout"
	modelFailureTransportUnavailable modelFailureCause = "transport_unavailable"
	modelFailureTransportValidation  modelFailureCause = "transport_validation"
)

type modelFailure struct {
	code       domain.ModelErrorCode
	cause      modelFailureCause
	httpStatus int
}

// DiagnosticOptions controls explicitly opted-in local model-failure detail.
// Sensitive mode never admits the transport credential or Authorization.
type DiagnosticOptions struct {
	Sensitive bool
}

var (
	errRedirectOriginDenied      = errors.New("model redirect origin denied")
	errRedirectLimitReached      = errors.New("model redirect limit reached")
	errRedirectUnsupported       = errors.New("model redirect unsupported")
	errTransportRequestInvalid   = errors.New("model transport request invalid")
	errUnsupportedResponseMedia  = errors.New("model response media type unsupported")
	errModelResponseLimitReached = errors.New("model response limit reached")
	errMalformedProviderChunk    = errors.New("model provider chunk malformed")
	errUnsupportedProviderChunk  = errors.New("model provider chunk unsupported")
)

// Adapter is the process-local OpenAI-compatible model transport. New takes
// ownership of the credential after successful construction. The composition
// root must cancel and wait for owning requests before calling Close.
type Adapter struct {
	configuration domain.ModelConfiguration
	credential    *config.SecretValue
	model         *einoopenai.ChatModel
	client        *http.Client
	logger        *slog.Logger
	diagnostics   DiagnosticOptions
	withTimeout   func(context.Context, time.Duration) (context.Context, context.CancelFunc)

	lifecycleMu sync.RWMutex
	closed      bool
}

var _ agent.Model = (*Adapter)(nil)

// New validates local configuration and constructs an isolated Eino-backed
// HTTP client. It performs no network request; protocol capability is checked
// strictly on the first admitted Stream call, with no retry or fallback.
func New(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	diagnostics DiagnosticOptions,
) (*Adapter, *domain.ModelError) {
	return newAdapterWithDiagnostics(configuration, credential, logger, nil, diagnostics)
}

func newAdapter(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	baseTransport http.RoundTripper,
) (*Adapter, *domain.ModelError) {
	return newAdapterWithDiagnostics(configuration, credential, logger, baseTransport, DiagnosticOptions{})
}

func newAdapterWithDiagnostics(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	baseTransport http.RoundTripper,
	diagnostics DiagnosticOptions,
) (*Adapter, *domain.ModelError) {
	if configuration.Validate() != nil {
		return nil, capabilityError(domain.ModelErrorCodeInvalidRequest, "model-configuration")
	}
	if credential == nil || !credential.IsSet() {
		return nil, capabilityError(domain.ModelErrorCodeInternal, "model-credential")
	}
	if credentialAppearsInStrings(credential, configuration.Endpoint, configuration.Origin, configuration.Model) {
		return nil, capabilityError(domain.ModelErrorCodeInvalidRequest, "model-configuration")
	}

	transport, ok := prepareTransport(baseTransport)
	if !ok {
		return nil, capabilityError(domain.ModelErrorCodeInvalidRequest, "model-transport")
	}
	origin, err := url.Parse(configuration.Origin)
	if err != nil {
		return nil, capabilityError(domain.ModelErrorCodeInvalidRequest, "model-configuration")
	}
	client := &http.Client{
		Transport: &guardedRoundTripper{
			base:       transport,
			origin:     origin,
			credential: credential,
		},
		CheckRedirect: redirectPolicy(origin),
	}
	// The fixed payload modifier below owns the actual request values. Leaving
	// optional SDK scaffolding parameters unset prevents vendor model-name
	// heuristics from rejecting a valid OpenAI-compatible request before HTTP.
	chatModel, err := einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
		APIKey:     einoCredentialPlaceholder,
		HTTPClient: client,
		BaseURL:    strings.TrimRight(configuration.Endpoint, "/"),
		Model:      configuration.Model,
	})
	if err != nil {
		return nil, capabilityError(domain.ModelErrorCodeInternal, "model-component")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Adapter{
		configuration: configuration,
		credential:    credential,
		model:         chatModel,
		client:        client,
		logger:        logger,
		diagnostics:   diagnostics,
		withTimeout:   context.WithTimeout,
	}, nil
}

func capabilityError(code domain.ModelErrorCode, correlationID string) *domain.ModelError {
	return domain.NewModelError(code, domain.ModelOperationCapability, correlationID)
}

func prepareTransport(base http.RoundTripper) (http.RoundTripper, bool) {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		// Non-standard transports are used only by same-package deterministic
		// tests. The exported constructor always owns a cloned http.Transport.
		return base, true
	}
	clone := transport.Clone()
	if clone.TLSClientConfig != nil {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
		if clone.TLSClientConfig.InsecureSkipVerify {
			return nil, false
		}
	}
	if clone.MaxResponseHeaderBytes == 0 || clone.MaxResponseHeaderBytes > maxModelResponseHeaderBytes {
		clone.MaxResponseHeaderBytes = maxModelResponseHeaderBytes
	}
	clone.DisableCompression = true
	return clone, true
}

func redirectPolicy(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
			request.Header.Del("Authorization")
			return errRedirectOriginDenied
		}
		if request.URL.User != nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" {
			request.Header.Del("Authorization")
			return errRedirectOriginDenied
		}
		if len(via) > maxModelRedirects {
			return errRedirectLimitReached
		}
		if request.Method != http.MethodPost || request.Header.Get("Authorization") == "" {
			return errRedirectUnsupported
		}
		return nil
	}
}

// Close releases the adapter-owned credential and idle transport connections.
// It is idempotent and waits for any in-flight Stream call to return.
func (adapter *Adapter) Close() {
	if adapter == nil {
		return
	}
	adapter.lifecycleMu.Lock()
	defer adapter.lifecycleMu.Unlock()
	if adapter.closed {
		return
	}
	adapter.closed = true
	if adapter.client != nil {
		adapter.client.CloseIdleConnections()
	}
	if adapter.credential != nil {
		adapter.credential.Destroy()
	}
}

// Stream sends one bounded Chat Completions request and synchronously projects
// the Eino stream into ordered project-owned events.
func (adapter *Adapter) Stream(
	ctx context.Context,
	request domain.ModelRequest,
	consume agent.ModelStreamConsumer,
) *domain.ModelError {
	if adapter == nil {
		return domain.NewModelError(domain.ModelErrorCodeInternal, domain.ModelOperationRequest, string(request.ID))
	}
	adapter.lifecycleMu.RLock()
	defer adapter.lifecycleMu.RUnlock()
	if adapter.closed {
		return domain.NewModelError(domain.ModelErrorCodeInternal, domain.ModelOperationRequest, string(request.ID))
	}
	if ctx == nil || consume == nil || request.Validate() != nil {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}
	if modelRequestContainsCredential(adapter.credential, request) {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}

	body, err := marshalWireRequest(adapter.configuration, request)
	if err != nil {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}
	if len(body) > domain.MaxModelRequestBytes {
		return domain.NewModelError(domain.ModelErrorCodeRequestTooLarge, domain.ModelOperationRequest, string(request.ID))
	}
	if credentialAppearsInBytes(adapter.credential, body) {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}
	if code, cancelled := contextModelErrorCode(ctx); cancelled {
		return domain.NewModelError(code, domain.ModelOperationRequest, string(request.ID))
	}

	requestContext, cancel := adapter.withTimeout(ctx, adapter.configuration.RequestTimeout)
	defer cancel()
	state := &transportRequestState{sensitiveDiagnostics: adapter.diagnostics.Sensitive}
	requestContext = context.WithValue(requestContext, transportRequestStateKey{}, state)
	// Kupilot does not install Eino global callbacks. Reinitializing the local
	// callback context prevents caller-owned handlers from observing model data.
	requestContext = einocallbacks.InitCallbacks(requestContext, nil)

	adapter.logger.Info(
		modelRequestLogEvent,
		"component", "model",
		"operation", string(domain.ModelOperationRequest),
		"phase", "started",
		"request_id", string(request.ID),
	)
	stream, err := adapter.model.Stream(
		requestContext,
		mapEinoMessages(request.Messages),
		einoopenai.WithRequestPayloadModifier(fixedRequestPayload(body)),
		einoopenai.WithResponseChunkMessageModifier(validateResponseChunk),
	)
	if err != nil {
		return adapter.finishWithError(
			request.ID,
			mapModelRequestError(requestContext, err, state),
			domain.ModelOperationRequest,
			err,
			state,
		)
	}
	defer func() {
		stream.Close()
		state.closeResponseBody()
	}()

	decoder := responseDecoder{
		ctx:         requestContext,
		requestID:   request.ID,
		consume:     consume,
		credential:  adapter.credential,
		textScanner: credentialScanner{credential: adapter.credential},
		tools:       make(map[int]*toolCallAssembly),
	}
	if providerRequestID := state.requestID(); providerRequestID != "" {
		metadata := domain.ModelResponseMetadata{ProviderRequestID: providerRequestID}
		if metadata.Validate() != nil || credentialAppearsInStrings(adapter.credential, providerRequestID) {
			return adapter.finishWithError(request.ID, modelFailure{
				code: domain.ModelErrorCodeMalformedStream, cause: modelFailureStreamProtocol, httpStatus: state.status(),
			}, domain.ModelOperationStream, nil, state)
		}
		if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventMetadata, Metadata: &metadata}); modelError != nil {
			adapter.logFinished(request.ID, modelError, failureForModelError(modelError, state.status()), nil, state)
			return modelError
		}
	}
	if modelError := decoder.decode(stream); modelError != nil {
		adapter.logFinished(request.ID, modelError, failureForModelError(modelError, state.status()), decoder.rawFailure, state)
		return modelError
	}
	adapter.logFinished(request.ID, nil, modelFailure{httpStatus: state.status()}, nil, state)
	return nil
}

func modelRequestContainsCredential(credential *config.SecretValue, request domain.ModelRequest) bool {
	values := make([]string, 0, 1+len(request.Messages)*3+len(request.Tools)*4)
	values = append(values, string(request.ID))
	for _, message := range request.Messages {
		values = append(values, message.Content, message.ToolCallID)
		for _, call := range message.ToolCalls {
			values = append(values, call.ID, string(call.Name), call.ArgumentsJSON)
		}
	}
	for _, specification := range request.Tools {
		values = append(values, string(specification.Name), specification.Version, specification.Description, specification.InputSchemaJSON)
	}
	return credentialAppearsInStrings(credential, values...)
}

func credentialAppearsInStrings(credential *config.SecretValue, values ...string) bool {
	found := false
	if credential == nil {
		return true
	}
	if err := credential.Use(func(secret string) {
		for _, value := range values {
			if strings.Contains(value, secret) {
				found = true
				return
			}
		}
	}); err != nil {
		return true
	}
	return found
}

func credentialAppearsInBytes(credential *config.SecretValue, value []byte) bool {
	found := false
	if credential == nil {
		return true
	}
	if err := credential.Use(func(secret string) {
		found = bytes.Contains(value, []byte(secret))
	}); err != nil {
		return true
	}
	return found
}

type credentialScanner struct {
	credential *config.SecretValue
	tail       string
}

func (scanner *credentialScanner) Contains(fragment string) bool {
	if scanner == nil || scanner.credential == nil {
		return true
	}
	found := false
	if err := scanner.credential.Use(func(secret string) {
		candidate := scanner.tail + fragment
		found = strings.Contains(candidate, secret)
		keep := len(secret) - 1
		if keep <= 0 {
			scanner.tail = ""
		} else if len(candidate) > keep {
			scanner.tail = candidate[len(candidate)-keep:]
		} else {
			scanner.tail = candidate
		}
	}); err != nil {
		return true
	}
	return found
}

func mapModelRequestError(ctx context.Context, cause error, state *transportRequestState) modelFailure {
	if code, cancelled := contextModelErrorCode(ctx); cancelled {
		return modelFailure{code: code, cause: contextFailureCause(code), httpStatus: observedHTTPStatus(state)}
	}
	switch {
	case errors.Is(cause, errRedirectOriginDenied):
		return modelFailure{
			code: domain.ModelErrorCodeRedirectDenied, cause: modelFailureRedirectPolicy, httpStatus: observedHTTPStatus(state),
		}
	case errors.Is(cause, errRedirectLimitReached), errors.Is(cause, errRedirectUnsupported),
		errors.Is(cause, errTransportRequestInvalid):
		return modelFailure{
			code: domain.ModelErrorCodeUnsupportedResponse, cause: modelFailureTransportValidation, httpStatus: observedHTTPStatus(state),
		}
	case errors.Is(cause, errUnsupportedResponseMedia):
		return modelFailure{
			code: domain.ModelErrorCodeUnsupportedResponse, cause: modelFailureResponseMedia, httpStatus: observedHTTPStatus(state),
		}
	case errors.Is(cause, errUnsupportedProviderChunk):
		return modelFailure{
			code: domain.ModelErrorCodeUnsupportedResponse, cause: modelFailureStreamProtocol, httpStatus: observedHTTPStatus(state),
		}
	case errors.Is(cause, errModelResponseLimitReached):
		return modelFailure{
			code: domain.ModelErrorCodeStreamLimitExceeded, cause: modelFailureStreamLimit, httpStatus: observedHTTPStatus(state),
		}
	case errors.Is(cause, errMalformedProviderChunk):
		return modelFailure{
			code: domain.ModelErrorCodeMalformedStream, cause: modelFailureStreamProtocol, httpStatus: observedHTTPStatus(state),
		}
	}
	if status := observedHTTPStatus(state); status != 0 {
		if status == http.StatusOK {
			return modelFailure{
				code: domain.ModelErrorCodeMalformedStream, cause: modelFailureStreamProtocol, httpStatus: status,
			}
		}
		return modelFailure{code: mapHTTPStatus(status), cause: modelFailureHTTPStatus, httpStatus: status}
	}
	var apiError *einoopenai.APIError
	if errors.As(cause, &apiError) {
		if status := validHTTPStatus(apiError.HTTPStatusCode); status != 0 {
			return modelFailure{
				code: mapHTTPStatus(status), cause: modelFailureHTTPStatus,
				httpStatus: status,
			}
		}
	}
	var networkError net.Error
	if errors.As(cause, &networkError) && networkError.Timeout() {
		return modelFailure{code: domain.ModelErrorCodeTimeout, cause: modelFailureTransportTimeout}
	}
	if state != nil && !state.transportEntered() {
		return modelFailure{code: domain.ModelErrorCodeUnsupportedResponse, cause: modelFailureTransportValidation}
	}
	return modelFailure{code: domain.ModelErrorCodeServiceUnavailable, cause: modelFailureTransportUnavailable}
}

func observedHTTPStatus(state *transportRequestState) int {
	if state == nil {
		return 0
	}
	return state.status()
}

func validHTTPStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func mapHTTPStatus(status int) domain.ModelErrorCode {
	switch {
	case status == http.StatusUnauthorized:
		return domain.ModelErrorCodeAuthenticationFailed
	case status == http.StatusForbidden:
		return domain.ModelErrorCodePermissionDenied
	case status == http.StatusTooManyRequests:
		return domain.ModelErrorCodeRateLimited
	case status >= 500 && status <= 599:
		return domain.ModelErrorCodeServiceUnavailable
	default:
		return domain.ModelErrorCodeUnsupportedResponse
	}
}

func contextFailureCause(code domain.ModelErrorCode) modelFailureCause {
	if code == domain.ModelErrorCodeTimeout {
		return modelFailureDeadlineExceeded
	}
	return modelFailureContextCancelled
}

func failureForModelError(modelError *domain.ModelError, status int) modelFailure {
	if modelError == nil {
		return modelFailure{httpStatus: status}
	}
	failure := modelFailure{code: modelError.Code(), httpStatus: validHTTPStatus(status)}
	switch modelError.Code() {
	case domain.ModelErrorCodeAuthenticationFailed, domain.ModelErrorCodePermissionDenied,
		domain.ModelErrorCodeRateLimited:
		failure.cause = modelFailureHTTPStatus
	case domain.ModelErrorCodeServiceUnavailable:
		if failure.httpStatus != 0 {
			failure.cause = modelFailureHTTPStatus
		} else {
			failure.cause = modelFailureTransportUnavailable
		}
	case domain.ModelErrorCodeUnsupportedResponse, domain.ModelErrorCodeMalformedStream:
		failure.cause = modelFailureStreamProtocol
	case domain.ModelErrorCodeStreamLimitExceeded:
		failure.cause = modelFailureStreamLimit
	case domain.ModelErrorCodeRedirectDenied:
		failure.cause = modelFailureRedirectPolicy
	case domain.ModelErrorCodeTimeout:
		failure.cause = modelFailureDeadlineExceeded
	case domain.ModelErrorCodeCancelled:
		failure.cause = modelFailureContextCancelled
	default:
		failure.cause = modelFailureAdapterInvariant
	}
	return failure
}

func (adapter *Adapter) finishWithError(
	requestID domain.ModelRequestID,
	failure modelFailure,
	operation domain.ModelOperation,
	rawCause error,
	state *transportRequestState,
) *domain.ModelError {
	modelError := domain.NewModelError(failure.code, operation, string(requestID))
	adapter.logFinished(requestID, modelError, failure, rawCause, state)
	return modelError
}

func (adapter *Adapter) logFinished(
	requestID domain.ModelRequestID,
	modelError *domain.ModelError,
	failure modelFailure,
	rawCause error,
	state *transportRequestState,
) {
	if modelError == nil {
		attributes := []any{
			"component", "model",
			"operation", string(domain.ModelOperationRequest),
			"phase", "terminal",
			"outcome", "success",
			"request_id", string(requestID),
		}
		if failure.httpStatus != 0 {
			attributes = append(attributes, "http_status", failure.httpStatus)
		}
		adapter.logger.Info(modelRequestLogEvent, attributes...)
		return
	}
	outcome := "failure"
	if modelError.Class() == domain.SafeErrorClassCancelled {
		outcome = "cancelled"
	}
	attributes := []any{
		"component", "model",
		"operation", string(modelError.Operation()),
		"phase", "terminal",
		"outcome", outcome,
		"request_id", string(requestID),
		"error_class", string(modelError.Class()),
		"error_code", string(modelError.Code()),
		"retryable", modelError.Retryable(),
		"cause", string(failure.cause),
	}
	if failure.httpStatus != 0 {
		attributes = append(attributes, "http_status", failure.httpStatus)
	}
	attributes = append(attributes, adapter.sensitiveFailureAttributes(rawCause, state)...)
	if modelError.Class() == domain.SafeErrorClassCancelled {
		adapter.logger.Warn(modelRequestLogEvent, attributes...)
		return
	}
	adapter.logger.Error(modelRequestLogEvent, attributes...)
}

func (adapter *Adapter) sensitiveFailureAttributes(rawCause error, state *transportRequestState) []any {
	if adapter == nil || !adapter.diagnostics.Sensitive {
		return nil
	}
	attributes := make([]any, 0, 12)
	if endpoint, _ := adapter.sensitiveDiagnosticText(adapter.configuration.Endpoint, maxSensitiveEndpointLogBytes); endpoint != "" {
		attributes = append(attributes, "sensitive_endpoint", endpoint)
	}
	if model, _ := adapter.sensitiveDiagnosticText(adapter.configuration.Model, maxSensitiveModelLogBytes); model != "" {
		attributes = append(attributes, "sensitive_model", model)
	}
	if rawCause != nil {
		detail := fmt.Sprintf("%T: %+v", rawCause, rawCause)
		if detail, truncated := adapter.sensitiveDiagnosticText(detail, maxSensitiveErrorLogBytes); detail != "" {
			attributes = append(attributes,
				"sensitive_error_chain", detail,
				"sensitive_error_truncated", truncated,
			)
		}
	}
	if state != nil {
		body, bodyTruncated := state.providerError()
		if body, textTruncated := adapter.sensitiveDiagnosticText(body, domain.MaxModelErrorBodyBytes); body != "" {
			attributes = append(attributes,
				"sensitive_provider_error_body", body,
				"sensitive_provider_body_truncated", bodyTruncated || textTruncated,
			)
		}
	}
	return attributes
}

func (adapter *Adapter) sensitiveDiagnosticText(value string, maximum int) (string, bool) {
	if adapter == nil || !adapter.diagnostics.Sensitive || value == "" || maximum < 1 || adapter.credential == nil {
		return "", false
	}
	if err := adapter.credential.Use(func(secret string) {
		value = strings.ReplaceAll(value, secret, sensitiveRedactionMarker)
	}); err != nil {
		return "", false
	}
	processed, err := security.NewRedactor().ProcessLines(value, maximum)
	if err != nil {
		return "", false
	}
	return processed.Value, processed.Truncated
}
