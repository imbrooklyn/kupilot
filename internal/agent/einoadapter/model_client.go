package einoadapter

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

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einocallbacks "github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

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
	errModelRequestLimitReached  = errors.New("model request limit reached")
	errTransportRequestInvalid   = errors.New("model transport request invalid")
	errUnsupportedResponseMedia  = errors.New("model response media type unsupported")
	errModelResponseLimitReached = errors.New("model response limit reached")
	errMalformedProviderChunk    = errors.New("model provider chunk malformed")
	errUnsupportedProviderChunk  = errors.New("model provider chunk unsupported")
)

// modelClient owns the one Eino OpenAI component and its guarded transport.
// It is private to the sole Eino boundary and never becomes a second Agent
// model port.
type modelClient struct {
	configuration domain.ModelConfiguration
	credential    *config.SecretValue
	model         einomodel.ToolCallingChatModel
	client        *http.Client
	logger        *slog.Logger
	diagnostics   DiagnosticOptions
}

// newModelClient validates local configuration and constructs an isolated
// Eino-backed HTTP client without performing a network request.
func newModelClient(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	diagnostics DiagnosticOptions,
) (*modelClient, *domain.ModelError) {
	return newModelClientWithTransport(configuration, credential, logger, nil, diagnostics)
}

func newModelClientForTest(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	baseTransport http.RoundTripper,
) (*modelClient, *domain.ModelError) {
	return newModelClientWithTransport(configuration, credential, logger, baseTransport, DiagnosticOptions{})
}

func newModelClientWithTransport(
	configuration domain.ModelConfiguration,
	credential *config.SecretValue,
	logger *slog.Logger,
	baseTransport http.RoundTripper,
	diagnostics DiagnosticOptions,
) (*modelClient, *domain.ModelError) {
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
	var maximum *int
	if configuration.MaxOutputTokens > 0 {
		configuredMaximum := configuration.MaxOutputTokens
		maximum = &configuredMaximum
	}
	chatModel, err := einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
		APIKey:     einoCredentialPlaceholder,
		HTTPClient: client,
		BaseURL:    strings.TrimRight(configuration.Endpoint, "/"),
		Model:      configuration.Model,
		MaxTokens:  maximum,
		// The pinned OpenAI client rejects temperature before transport for
		// identifiers beginning with gpt-5, even for compatible endpoints that
		// admit the configured field. Eino's fixed ExtraFields path keeps Eino
		// as the serializer without allowing SDK model-name inference to change
		// Kupilot's typed request contract.
		ExtraFields: map[string]any{
			"temperature": configuration.Temperature,
		},
		ReasoningEffort: einoopenai.ReasoningEffortLevel(configuration.ReasoningEffort),
	})
	if err != nil {
		return nil, capabilityError(domain.ModelErrorCodeInternal, "model-component")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &modelClient{
		configuration: configuration,
		credential:    credential,
		model:         chatModel,
		client:        client,
		logger:        logger,
		diagnostics:   diagnostics,
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

// close releases the boundary-owned credential and idle transport connections.
// Adapter.Close waits for admitted runs before calling it.
func (client *modelClient) close() {
	if client == nil {
		return
	}
	if client.client != nil {
		client.client.CloseIdleConnections()
	}
	if client.credential != nil {
		client.credential.Destroy()
	}
}

func (client *modelClient) withTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if client == nil || client.model == nil || validateBoundToolInfos(tools) != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	bound, err := client.model.WithTools(tools)
	if err != nil {
		return nil, failedRuntime(domain.SafeErrorClassUnsupported, "The configured model cannot accept the fixed Tool catalog.", err)
	}
	return bound, nil
}

// stream sends one Eino-generated request and returns one assembled Eino
// assistant message. It observes the generated payload without replacing it.
func (client *modelClient) stream(
	ctx context.Context,
	requestID domain.ModelRequestID,
	model einomodel.ToolCallingChatModel,
	messages []*schema.Message,
	observeContent func(string) error,
) (*schema.Message, *domain.ModelError) {
	return client.streamBounded(ctx, requestID, model, messages, agent.CallReservation{
		RequestBytes: domain.MaxModelRequestBytes,
		StreamBytes:  domain.MaxModelStreamBytes,
	}, observeContent)
}

func (client *modelClient) streamBounded(
	ctx context.Context,
	requestID domain.ModelRequestID,
	model einomodel.ToolCallingChatModel,
	messages []*schema.Message,
	reservation agent.CallReservation,
	observeContent func(string) error,
) (*schema.Message, *domain.ModelError) {
	if client == nil || ctx == nil || model == nil || !requestID.Valid() || len(messages) == 0 ||
		reservation.RequestBytes < 1 || reservation.RequestBytes > domain.MaxModelRequestBytes ||
		reservation.StreamBytes < 1 || reservation.StreamBytes > domain.MaxModelStreamBytes ||
		einoMessagesContainCredential(client.credential, messages) {
		return nil, domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(requestID))
	}
	if code, cancelled := contextModelErrorCode(ctx); cancelled {
		return nil, domain.NewModelError(code, domain.ModelOperationRequest, string(requestID))
	}

	state := &transportRequestState{
		sensitiveDiagnostics: client.diagnostics.Sensitive,
		responseMode:         transportResponseStream,
		requestLimit:         reservation.RequestBytes,
		responseLimit:        reservation.StreamBytes,
	}
	requestContext := context.WithValue(ctx, transportRequestStateKey{}, state)
	// Kupilot does not install Eino global callbacks. Reinitializing the local
	// callback context prevents caller-owned handlers from observing model data.
	requestContext = einocallbacks.InitCallbacks(requestContext, nil)

	client.logger.Info(
		modelRequestLogEvent,
		"component", "model",
		"operation", string(domain.ModelOperationRequest),
		"phase", "started",
		"request_id", string(requestID),
	)
	stream, err := model.Stream(
		requestContext,
		messages,
		einoopenai.WithRequestPayloadModifier(client.observeRequestPayload(reservation.RequestBytes)),
		einoopenai.WithResponseChunkMessageModifier(validateResponseChunk),
	)
	if err != nil {
		return nil, client.finishWithError(
			requestID,
			mapModelRequestError(requestContext, err, state),
			domain.ModelOperationRequest,
			err,
			state,
		)
	}
	defer state.closeResponseBody()
	message, err := collectModelMessage(requestContext, stream, client.credential, observeContent)
	if err != nil {
		return nil, client.finishWithError(
			requestID,
			mapModelRequestError(requestContext, err, state),
			domain.ModelOperationStream,
			err,
			state,
		)
	}
	client.logFinished(requestID, nil, modelFailure{httpStatus: state.status()}, nil, state)
	return message, nil
}

// generate performs the middleware-owned non-streaming Agent-profile summary
// request through the same Eino component and guarded origin transport.
func (client *modelClient) generate(
	ctx context.Context,
	requestID domain.ModelRequestID,
	messages []*schema.Message,
	reservation agent.CallReservation,
) (*schema.Message, *domain.ModelError) {
	return client.generateNonStreaming(ctx, requestID, messages, reservation, domain.MaxSessionSummaryBytes, domain.ModelInvocationAgentSummary)
}

func (client *modelClient) generateNonStreaming(
	ctx context.Context,
	requestID domain.ModelRequestID,
	messages []*schema.Message,
	reservation agent.CallReservation,
	maximumOutput int,
	invocation domain.ModelInvocation,
) (*schema.Message, *domain.ModelError) {
	if client == nil || ctx == nil || client.model == nil || !requestID.Valid() || len(messages) == 0 ||
		reservation.RequestBytes < 1 || reservation.RequestBytes > domain.MaxModelRequestBytes ||
		maximumOutput < 1 || maximumOutput > domain.MaxModelMessageBytes ||
		reservation.OutputBytes < 1 || reservation.OutputBytes > maximumOutput || !invocation.Valid() ||
		client.configuration.Role == domain.ModelRoleAgent && invocation != domain.ModelInvocationAgentSummary ||
		client.configuration.Role == domain.ModelRoleApprovalReviewer && invocation != domain.ModelInvocationReview ||
		einoMessagesContainCredential(client.credential, messages) {
		return nil, domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(requestID))
	}
	if code, cancelled := contextModelErrorCode(ctx); cancelled {
		return nil, domain.NewModelError(code, domain.ModelOperationRequest, string(requestID))
	}
	state := &transportRequestState{
		sensitiveDiagnostics: client.diagnostics.Sensitive,
		responseMode:         transportResponseJSON,
		requestLimit:         reservation.RequestBytes,
		responseLimit:        maxSummaryResponseBytes,
	}
	requestContext := context.WithValue(ctx, transportRequestStateKey{}, state)
	requestContext = einocallbacks.InitCallbacks(requestContext, nil)
	client.logger.Info(modelRequestLogEvent,
		"component", "model", "operation", string(domain.ModelOperationRequest),
		"phase", "started", "request_id", string(requestID), "invocation", string(invocation),
	)
	message, err := client.model.Generate(
		requestContext,
		messages,
		einoopenai.WithRequestPayloadModifier(client.observeRequestPayload(reservation.RequestBytes)),
		einoopenai.WithResponseMessageModifier(validateResponseMessage),
	)
	defer state.closeResponseBody()
	if err != nil {
		return nil, client.finishWithError(requestID, mapModelRequestError(requestContext, err, state), domain.ModelOperationRequest, err, state)
	}
	if err := admitAndClearNonStreamingMetadata(message, client.credential); err != nil {
		return nil, client.finishWithError(
			requestID,
			mapModelRequestError(requestContext, err, state),
			domain.ModelOperationRequest,
			err,
			state,
		)
	}
	client.logFinished(requestID, nil, modelFailure{httpStatus: state.status()}, nil, state)
	return message, nil
}

func (client *modelClient) observeRequestPayload(maximum int) einoopenai.RequestPayloadModifier {
	return func(_ context.Context, _ []*schema.Message, body []byte) ([]byte, error) {
		if maximum < 1 || maximum > domain.MaxModelRequestBytes || len(body) > maximum {
			return nil, errModelRequestLimitReached
		}
		if credentialAppearsInBytes(client.credential, body) {
			return nil, errTransportRequestInvalid
		}
		return body, nil
	}
}

func einoMessagesContainCredential(credential *config.SecretValue, messages []*schema.Message) bool {
	values := make([]string, 0, len(messages)*6)
	for _, message := range messages {
		if message == nil {
			return true
		}
		values = append(values, message.Content, message.ToolCallID, message.ToolName, message.Name, message.ReasoningContent)
		for _, call := range message.ToolCalls {
			values = append(values, call.ID, call.Type, call.Function.Name, call.Function.Arguments)
		}
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
	var projected *domain.ModelError
	if errors.As(cause, &projected) && projected.Validate() == nil {
		return failureForModelError(projected, observedHTTPStatus(state))
	}
	if state.responseLimitReached() {
		return modelFailure{
			code: domain.ModelErrorCodeStreamLimitExceeded, cause: modelFailureStreamLimit, httpStatus: observedHTTPStatus(state),
		}
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
	case errors.Is(cause, errModelRequestLimitReached):
		return modelFailure{
			code: domain.ModelErrorCodeRequestTooLarge, cause: modelFailureTransportValidation, httpStatus: observedHTTPStatus(state),
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

func (client *modelClient) finishWithError(
	requestID domain.ModelRequestID,
	failure modelFailure,
	operation domain.ModelOperation,
	rawCause error,
	state *transportRequestState,
) *domain.ModelError {
	modelError := domain.NewModelError(failure.code, operation, string(requestID))
	client.logFinished(requestID, modelError, failure, rawCause, state)
	return modelError
}

func (client *modelClient) logFinished(
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
		client.logger.Info(modelRequestLogEvent, attributes...)
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
	attributes = append(attributes, client.sensitiveFailureAttributes(rawCause, state)...)
	if modelError.Class() == domain.SafeErrorClassCancelled {
		client.logger.Warn(modelRequestLogEvent, attributes...)
		return
	}
	client.logger.Error(modelRequestLogEvent, attributes...)
}

func (client *modelClient) sensitiveFailureAttributes(rawCause error, state *transportRequestState) []any {
	if client == nil || !client.diagnostics.Sensitive {
		return nil
	}
	attributes := make([]any, 0, 12)
	if endpoint, _ := client.sensitiveDiagnosticText(client.configuration.Endpoint, maxSensitiveEndpointLogBytes); endpoint != "" {
		attributes = append(attributes, "sensitive_endpoint", endpoint)
	}
	if model, _ := client.sensitiveDiagnosticText(client.configuration.Model, maxSensitiveModelLogBytes); model != "" {
		attributes = append(attributes, "sensitive_model", model)
	}
	if rawCause != nil {
		detail := fmt.Sprintf("%T: %+v", rawCause, rawCause)
		if detail, truncated := client.sensitiveDiagnosticText(detail, maxSensitiveErrorLogBytes); detail != "" {
			attributes = append(attributes,
				"sensitive_error_chain", detail,
				"sensitive_error_truncated", truncated,
			)
		}
	}
	if state != nil {
		body, bodyTruncated := state.providerError()
		if body, textTruncated := client.sensitiveDiagnosticText(body, domain.MaxModelErrorBodyBytes); body != "" {
			attributes = append(attributes,
				"sensitive_provider_error_body", body,
				"sensitive_provider_body_truncated", bodyTruncated || textTruncated,
			)
		}
	}
	return attributes
}

func (client *modelClient) sensitiveDiagnosticText(value string, maximum int) (string, bool) {
	if client == nil || !client.diagnostics.Sensitive || value == "" || maximum < 1 || client.credential == nil {
		return "", false
	}
	if err := client.credential.Use(func(secret string) {
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
