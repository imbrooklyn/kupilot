package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	errCrossOriginRedirect = errors.New("cross-origin model redirect")
	errTooManyRedirects    = errors.New("too many model redirects")
)

type referenceModel struct {
	configuration domain.ModelConfiguration
	apiKey        string
	client        *http.Client
	logger        *slog.Logger
	withTimeout   func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type wireRequest struct {
	Model           string        `json:"model"`
	Messages        []wireMessage `json:"messages"`
	Tools           []wireTool    `json:"tools"`
	Stream          bool          `json:"stream"`
	StreamOptions   streamOptions `json:"stream_options"`
	Temperature     float64       `json:"temperature"`
	MaxOutputTokens int           `json:"max_tokens"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolContract `json:"function"`
}

type wireToolContract struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type wireChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage,omitempty"`
}

type wireChoice struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type wireDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []wireToolFragment `json:"tool_calls,omitempty"`
}

type wireToolFragment struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function wireToolFunctionFragment `json:"function"`
}

type wireToolFunctionFragment struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type wireUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type toolAssembly struct {
	id        string
	name      string
	arguments string
}

type streamDecoder struct {
	requestID     domain.ModelRequestID
	consume       agent.ModelStreamConsumer
	sequence      int
	eventCount    int
	wireBytes     int
	completed     bool
	pendingFinish *domain.ModelFinishReason
	usageSeen     bool
	sawText       bool
	sawTool       bool
	lastToolIndex int
	tools         map[int]*toolAssembly
}

var _ agent.Model = (*referenceModel)(nil)

func newReferenceModel(configuration domain.ModelConfiguration, apiKey string, logger *slog.Logger) *referenceModel {
	origin, _ := url.Parse(configuration.Origin)
	client := &http.Client{}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
			return errCrossOriginRedirect
		}
		if len(via) >= 4 {
			return errTooManyRedirects
		}
		return nil
	}
	return &referenceModel{
		configuration: configuration,
		apiKey:        apiKey,
		client:        client,
		logger:        logger,
		withTimeout:   context.WithTimeout,
	}
}

func (model *referenceModel) Stream(ctx context.Context, request domain.ModelRequest, consume agent.ModelStreamConsumer) *domain.ModelError {
	if ctx == nil || consume == nil || model.configuration.Validate() != nil || request.Validate() != nil {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}
	body, err := encodeWireRequest(model.configuration, request)
	if err != nil {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest, string(request.ID))
	}
	if len(body) > domain.MaxModelRequestBytes {
		return domain.NewModelError(domain.ModelErrorCodeRequestTooLarge, domain.ModelOperationRequest, string(request.ID))
	}

	model.logger.Info("model request started", "request_id", request.ID)
	requestContext, cancel := model.withTimeout(ctx, model.configuration.RequestTimeout)
	defer cancel()

	endpoint := strings.TrimRight(model.configuration.Endpoint, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return model.finishWithError(request.ID, domain.ModelErrorCodeInvalidRequest, domain.ModelOperationRequest)
	}
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+model.apiKey)

	response, err := model.client.Do(httpRequest)
	if err != nil {
		return model.mapTransportError(requestContext, request.ID, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, int64(domain.MaxModelErrorBodyBytes)))
		return model.mapHTTPStatus(request.ID, response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		return model.finishWithError(request.ID, domain.ModelErrorCodeUnsupportedResponse, domain.ModelOperationStream)
	}

	decoder := streamDecoder{
		requestID:     request.ID,
		consume:       consume,
		lastToolIndex: -1,
		tools:         make(map[int]*toolAssembly),
	}
	if providerRequestID := response.Header.Get("X-Request-ID"); providerRequestID != "" {
		metadata := domain.ModelResponseMetadata{ProviderRequestID: providerRequestID}
		if metadata.Validate() != nil {
			return model.finishWithError(request.ID, domain.ModelErrorCodeMalformedStream, domain.ModelOperationStream)
		}
		if modelError := decoder.emit(domain.ModelStreamEvent{
			Kind:     domain.ModelStreamEventMetadata,
			Metadata: &metadata,
		}); modelError != nil {
			model.logFinished(request.ID, modelError)
			return modelError
		}
	}
	if streamError := decoder.decode(requestContext, response.Body); streamError != nil {
		model.logFinished(request.ID, streamError)
		return streamError
	}
	model.logFinished(request.ID, nil)
	return nil
}

func (model *referenceModel) mapTransportError(ctx context.Context, requestID domain.ModelRequestID, cause error) *domain.ModelError {
	switch {
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return model.finishWithError(requestID, domain.ModelErrorCodeTimeout, domain.ModelOperationRequest)
	case errors.Is(context.Cause(ctx), context.Canceled):
		return model.finishWithError(requestID, domain.ModelErrorCodeCancelled, domain.ModelOperationRequest)
	case errors.Is(cause, errCrossOriginRedirect):
		return model.finishWithError(requestID, domain.ModelErrorCodeRedirectDenied, domain.ModelOperationRequest)
	case errors.Is(cause, errTooManyRedirects):
		return model.finishWithError(requestID, domain.ModelErrorCodeUnsupportedResponse, domain.ModelOperationRequest)
	default:
		return model.finishWithError(requestID, domain.ModelErrorCodeServiceUnavailable, domain.ModelOperationRequest)
	}
}

func (model *referenceModel) mapHTTPStatus(requestID domain.ModelRequestID, status int) *domain.ModelError {
	var code domain.ModelErrorCode
	switch {
	case status == http.StatusUnauthorized:
		code = domain.ModelErrorCodeAuthenticationFailed
	case status == http.StatusForbidden:
		code = domain.ModelErrorCodePermissionDenied
	case status == http.StatusTooManyRequests:
		code = domain.ModelErrorCodeRateLimited
	case status >= 500 && status <= 599:
		code = domain.ModelErrorCodeServiceUnavailable
	default:
		code = domain.ModelErrorCodeUnsupportedResponse
	}
	return model.finishWithError(requestID, code, domain.ModelOperationRequest)
}

func (model *referenceModel) finishWithError(requestID domain.ModelRequestID, code domain.ModelErrorCode, operation domain.ModelOperation) *domain.ModelError {
	modelError := domain.NewModelError(code, operation, string(requestID))
	model.logFinished(requestID, modelError)
	return modelError
}

func (model *referenceModel) logFinished(requestID domain.ModelRequestID, modelError *domain.ModelError) {
	if modelError == nil {
		model.logger.Info("model request finished", "request_id", requestID, "status", "succeeded")
		return
	}
	model.logger.Info(
		"model request finished",
		"request_id", requestID,
		"status", "failed",
		"error_class", modelError.Class(),
		"error_code", modelError.Code(),
	)
}

func encodeWireRequest(configuration domain.ModelConfiguration, request domain.ModelRequest) ([]byte, error) {
	messages := make([]wireMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		mapped := wireMessage{
			Role:       string(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			mapped.ToolCalls = append(mapped.ToolCalls, wireToolCall{
				ID:   call.ID,
				Type: "function",
				Function: wireToolFunction{
					Name:      string(call.Name),
					Arguments: call.ArgumentsJSON,
				},
			})
		}
		messages = append(messages, mapped)
	}

	tools := make([]wireTool, 0, len(request.Tools))
	for _, specification := range request.Tools {
		tools = append(tools, wireTool{
			Type: "function",
			Function: wireToolContract{
				Name:        string(specification.Name),
				Description: specification.Description,
				Parameters:  json.RawMessage(specification.InputSchemaJSON),
				Strict:      true,
			},
		})
	}

	return json.Marshal(wireRequest{
		Model:           configuration.Model,
		Messages:        messages,
		Tools:           tools,
		Stream:          true,
		StreamOptions:   streamOptions{IncludeUsage: true},
		Temperature:     configuration.Temperature,
		MaxOutputTokens: configuration.MaxOutputTokens,
	})
}

func (decoder *streamDecoder) decode(ctx context.Context, body io.Reader) *domain.ModelError {
	reader := bufio.NewReaderSize(body, domain.MaxModelStreamEventBytes+32)
	for {
		line, readError := reader.ReadSlice('\n')
		decoder.wireBytes += len(line)
		if decoder.wireBytes > domain.MaxModelStreamBytes || errors.Is(readError, bufio.ErrBufferFull) {
			return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
		}
		if len(line) > 0 {
			if modelError := decoder.consumeLine(line); modelError != nil {
				return modelError
			}
			if decoder.completed {
				return nil
			}
		}
		if readError == nil {
			continue
		}
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return decoder.modelError(domain.ModelErrorCodeTimeout)
		}
		if errors.Is(context.Cause(ctx), context.Canceled) {
			return decoder.modelError(domain.ModelErrorCodeCancelled)
		}
		if errors.Is(readError, io.EOF) {
			return decoder.complete()
		}
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
}

func (decoder *streamDecoder) consumeLine(raw []byte) *domain.ModelError {
	if !utf8.Valid(raw) {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	line := strings.TrimSuffix(string(raw), "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, ":") {
		return nil
	}
	if !strings.HasPrefix(line, "data:") {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	data := strings.TrimPrefix(line, "data:")
	data = strings.TrimPrefix(data, " ")
	if len(data) > domain.MaxModelStreamEventBytes {
		return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
	}
	decoder.eventCount++
	if decoder.eventCount > domain.MaxModelStreamEvents {
		return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
	}
	if data == "[DONE]" {
		return decoder.complete()
	}

	var chunk wireChunk
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if decoder.pendingFinish != nil && !(len(chunk.Choices) == 0 && chunk.Usage != nil) {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if len(chunk.Choices) == 0 && chunk.Usage == nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if len(chunk.Choices) > 1 {
		return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
	}
	if chunk.Usage != nil {
		if decoder.pendingFinish == nil || decoder.usageSeen {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		usage := domain.ModelUsage{
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
			TotalTokens:  chunk.Usage.TotalTokens,
		}
		if usage.Validate() != nil {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		decoder.usageSeen = true
		if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventUsage, Usage: &usage}); modelError != nil {
			return modelError
		}
	}
	if len(chunk.Choices) == 0 {
		return nil
	}

	choice := chunk.Choices[0]
	if choice.Index != 0 || choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
		return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
	}
	if choice.Delta.Content != "" && len(choice.Delta.ToolCalls) != 0 ||
		choice.Delta.Content != "" && decoder.sawTool ||
		len(choice.Delta.ToolCalls) != 0 && decoder.sawText {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if choice.Delta.Content != "" {
		decoder.sawText = true
		if modelError := decoder.emit(domain.ModelStreamEvent{
			Kind:      domain.ModelStreamEventTextDelta,
			TextDelta: choice.Delta.Content,
		}); modelError != nil {
			return modelError
		}
	}
	for _, fragment := range choice.Delta.ToolCalls {
		if modelError := decoder.consumeToolFragment(fragment); modelError != nil {
			return modelError
		}
	}
	if choice.FinishReason != nil {
		if decoder.pendingFinish != nil {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		finishReason, ok := modelFinishReason(*choice.FinishReason)
		if !ok {
			return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
		}
		decoder.pendingFinish = &finishReason
	}
	return nil
}

func (decoder *streamDecoder) consumeToolFragment(fragment wireToolFragment) *domain.ModelError {
	if fragment.Index < decoder.lastToolIndex || fragment.Type != "" && fragment.Type != "function" {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	decoder.lastToolIndex = fragment.Index
	decoder.sawTool = true
	assembly := decoder.tools[fragment.Index]
	if assembly == nil {
		assembly = &toolAssembly{}
		decoder.tools[fragment.Index] = assembly
	}
	assembly.id += fragment.ID
	assembly.name += fragment.Function.Name
	assembly.arguments += fragment.Function.Arguments
	if len(assembly.id) > domain.MaxModelToolCallIDBytes ||
		len(assembly.name) > domain.MaxModelToolNameBytes ||
		len(assembly.arguments) > domain.MaxModelToolArgumentsBytes {
		return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
	}

	neutral := domain.ModelToolCallFragment{
		Index:             fragment.Index,
		IDFragment:        fragment.ID,
		NameFragment:      fragment.Function.Name,
		ArgumentsFragment: fragment.Function.Arguments,
	}
	if neutral.Validate() != nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if modelError := decoder.emit(domain.ModelStreamEvent{
		Kind:             domain.ModelStreamEventToolCallFragment,
		ToolCallFragment: &neutral,
	}); modelError != nil {
		return modelError
	}
	return nil
}

func (decoder *streamDecoder) complete() *domain.ModelError {
	if decoder.pendingFinish == nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	switch *decoder.pendingFinish {
	case domain.ModelFinishReasonToolCalls:
		if !decoder.sawTool || decoder.sawText {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		for index := 0; index < len(decoder.tools); index++ {
			assembly := decoder.tools[index]
			if assembly == nil {
				return decoder.modelError(domain.ModelErrorCodeMalformedStream)
			}
			call := domain.ModelToolCall{
				ID:            assembly.id,
				Name:          domain.ToolName(assembly.name),
				ArgumentsJSON: assembly.arguments,
			}
			if call.Validate() != nil {
				return decoder.modelError(domain.ModelErrorCodeMalformedStream)
			}
		}
	case domain.ModelFinishReasonStop, domain.ModelFinishReasonLength:
		if decoder.sawTool {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
	default:
		return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
	}
	completion := domain.ModelCompletion{FinishReason: *decoder.pendingFinish}
	if modelError := decoder.emit(domain.ModelStreamEvent{
		Kind:       domain.ModelStreamEventCompleted,
		Completion: &completion,
	}); modelError != nil {
		return modelError
	}
	decoder.completed = true
	decoder.pendingFinish = nil
	return nil
}

func (decoder *streamDecoder) emit(event domain.ModelStreamEvent) *domain.ModelError {
	if decoder.sequence >= domain.MaxModelStreamEvents {
		return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
	}
	decoder.sequence++
	event.Sequence = decoder.sequence
	if event.Validate() != nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	decoder.consume(event)
	return nil
}

func (decoder *streamDecoder) modelError(code domain.ModelErrorCode) *domain.ModelError {
	return domain.NewModelError(code, domain.ModelOperationStream, string(decoder.requestID))
}

func modelFinishReason(value string) (domain.ModelFinishReason, bool) {
	switch value {
	case "stop":
		return domain.ModelFinishReasonStop, true
	case "tool_calls":
		return domain.ModelFinishReasonToolCalls, true
	case "length":
		return domain.ModelFinishReasonLength, true
	default:
		return "", false
	}
}

func TestNeutralModelContractIsMinimalAndRejectsAmbiguity(t *testing.T) {
	t.Parallel()

	modelPort := reflect.TypeOf((*agent.Model)(nil)).Elem()
	if modelPort.NumMethod() != 1 || modelPort.Method(0).Name != "Stream" {
		t.Fatalf("Model port methods = %d, want only Stream", modelPort.NumMethod())
	}

	configuration := domain.ModelConfiguration{
		ProviderKind:        domain.ModelProviderOpenAICompatible,
		Endpoint:            "http://127.0.0.1:8080/v1",
		Origin:              "http://127.0.0.1:8080",
		Model:               "fixture-model",
		APIKeySource:        domain.ModelAPIKeySourceRuntime,
		Temperature:         0.1,
		MaxOutputTokens:     2048,
		RequestTimeout:      domain.MaxModelRequestTimeout,
		StreamingRequired:   true,
		ToolCallingRequired: true,
		TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("valid ModelConfiguration error = %v", err)
	}
	invalidConfigurations := []domain.ModelConfiguration{
		func() domain.ModelConfiguration {
			value := configuration
			value.Endpoint = "http://model.example.test/v1"
			value.Origin = "http://model.example.test"
			return value
		}(),
		func() domain.ModelConfiguration {
			value := configuration
			value.Endpoint += "/../admin"
			return value
		}(),
		func() domain.ModelConfiguration {
			value := configuration
			value.RequestTimeout++
			return value
		}(),
		func() domain.ModelConfiguration {
			value := configuration
			value.ToolCallingRequired = false
			return value
		}(),
	}
	for index, value := range invalidConfigurations {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid ModelConfiguration %d error = nil", index)
		}
	}

	usage := domain.ModelUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	ambiguous := domain.ModelStreamEvent{
		Sequence:  1,
		Kind:      domain.ModelStreamEventTextDelta,
		TextDelta: "text",
		Usage:     &usage,
	}
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("ambiguous ModelStreamEvent error = nil")
	}
	if modelError := domain.NewModelError(domain.ModelErrorCodeCancelled, "unknown", ""); modelError.Validate() != nil || modelError.Class() != domain.SafeErrorClassInternal {
		t.Fatalf("invalid error inputs did not fail closed: %#v", modelError)
	}
}

func TestInvalidNeutralRequestFailsBeforeHTTP(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	var logBuffer bytes.Buffer
	model := newFixtureAdapter(
		t,
		fixtureConfiguration(server.endpoint("normal"), time.Second),
		strings.Repeat("d", 41)+"-generated",
		fixtureLogger(&logBuffer),
	)
	request := fixtureModelRequest()
	request.Tools = request.Tools[:len(request.Tools)-1]
	modelError := model.Stream(context.Background(), request, func(domain.ModelStreamEvent) {})
	if modelError == nil || modelError.Class() != domain.SafeErrorClassInvalidInput {
		t.Fatalf("model error = %#v, want invalid_input", modelError)
	}
	if got := len(server.capturedRequests()); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
	if logBuffer.Len() != 0 {
		t.Fatal("invalid request produced a log entry")
	}
}

func TestCompatibilityFixturesProduceNeutralStreamEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		scenario      string
		wantKinds     []domain.ModelStreamEventKind
		wantText      string
		wantFinish    domain.ModelFinishReason
		wantArguments string
		wantUsage     *domain.ModelUsage
	}{
		{
			name:       "text with optional metadata and usage",
			scenario:   "normal",
			wantKinds:  []domain.ModelStreamEventKind{domain.ModelStreamEventMetadata, domain.ModelStreamEventTextDelta, domain.ModelStreamEventTextDelta, domain.ModelStreamEventUsage, domain.ModelStreamEventCompleted},
			wantText:   "Pod is healthy.",
			wantFinish: domain.ModelFinishReasonStop,
			wantUsage:  &domain.ModelUsage{InputTokens: 21, OutputTokens: 7, TotalTokens: 28},
		},
		{
			name:          "fragmented Tool arguments",
			scenario:      "tool-call-fragments",
			wantKinds:     []domain.ModelStreamEventKind{domain.ModelStreamEventMetadata, domain.ModelStreamEventToolCallFragment, domain.ModelStreamEventToolCallFragment, domain.ModelStreamEventToolCallFragment, domain.ModelStreamEventCompleted},
			wantFinish:    domain.ModelFinishReasonToolCalls,
			wantArguments: `{"kind":"Pod","name":"sample-pod"}`,
		},
		{
			name:       "usage and DONE omitted",
			scenario:   "no-usage-eof",
			wantKinds:  []domain.ModelStreamEventKind{domain.ModelStreamEventTextDelta, domain.ModelStreamEventCompleted},
			wantText:   "Usage is optional.",
			wantFinish: domain.ModelFinishReasonStop,
		},
	}

	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()

			apiCanary := strings.Repeat("a", 41) + "-generated"
			server := newFixtureServer(t, "")
			configuration := fixtureConfiguration(server.endpoint(current.scenario), time.Second)
			var logBuffer bytes.Buffer
			model := newFixtureAdapter(t, configuration, apiCanary, fixtureLogger(&logBuffer))
			request := fixtureModelRequest()
			var events []domain.ModelStreamEvent
			modelError := model.Stream(context.Background(), request, func(event domain.ModelStreamEvent) {
				events = append(events, event)
			})

			assertTerminalContract(t, events, modelError)
			if got := eventKinds(events); fmt.Sprint(got) != fmt.Sprint(current.wantKinds) {
				t.Fatalf("event kinds = %v, want %v", got, current.wantKinds)
			}
			var text, arguments string
			var usage *domain.ModelUsage
			for _, event := range events {
				if err := event.Validate(); err != nil {
					t.Fatalf("event Validate() error = %v", err)
				}
				text += event.TextDelta
				if event.ToolCallFragment != nil {
					arguments += event.ToolCallFragment.ArgumentsFragment
				}
				if event.Usage != nil {
					copy := *event.Usage
					usage = &copy
				}
			}
			if text != current.wantText || arguments != current.wantArguments {
				t.Fatalf("assembled text/arguments = %q / %q, want %q / %q", text, arguments, current.wantText, current.wantArguments)
			}
			if fmt.Sprint(usage) != fmt.Sprint(current.wantUsage) {
				t.Fatalf("usage = %v, want %v", usage, current.wantUsage)
			}
			if got := events[len(events)-1].Completion.FinishReason; got != current.wantFinish {
				t.Fatalf("finish reason = %q, want %q", got, current.wantFinish)
			}
			if strings.Contains(sprintEvents(events), apiCanary) {
				t.Fatal("neutral stream events contain the transport credential")
			}
			assertRequestAndSinkSafety(t, server, apiCanary, "", logBuffer.String(), nil)
		})
	}
}

func TestHTTPErrorFixturesMapToSafeClassesWithoutBodyLeakage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scenario  string
		wantClass domain.SafeErrorClass
		retryable bool
	}{
		{scenario: "error-401", wantClass: domain.SafeErrorClassAuthenticationFailed},
		{scenario: "error-429", wantClass: domain.SafeErrorClassRateLimited, retryable: true},
		{scenario: "error-500", wantClass: domain.SafeErrorClassUnavailable, retryable: true},
	}
	for _, current := range tests {
		current := current
		t.Run(current.scenario, func(t *testing.T) {
			t.Parallel()

			apiCanary := strings.Repeat("h", 37) + "-generated"
			errorCanary := strings.Repeat("e", 43) + "-generated"
			server := newFixtureServer(t, errorCanary)
			var logBuffer bytes.Buffer
			model := newFixtureAdapter(t, fixtureConfiguration(server.endpoint(current.scenario), time.Second), apiCanary, fixtureLogger(&logBuffer))
			var events []domain.ModelStreamEvent
			modelError := model.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
				events = append(events, event)
			})

			assertTerminalContract(t, events, modelError)
			if modelError.Class() != current.wantClass || modelError.Retryable() != current.retryable {
				t.Fatalf("model error = class %q retryable %t, want %q %t", modelError.Class(), modelError.Retryable(), current.wantClass, current.retryable)
			}
			assertRequestAndSinkSafety(t, server, apiCanary, errorCanary, logBuffer.String(), modelError)
		})
	}
}

func TestMalformedAndOversizeFixturesHaveOneClassifiedTerminalError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scenario  string
		wantClass domain.SafeErrorClass
	}{
		{scenario: "malformed", wantClass: domain.SafeErrorClassInvalidExternalResponse},
		{scenario: "oversize-event", wantClass: domain.SafeErrorClassBudgetExhausted},
		{scenario: "oversize-stream", wantClass: domain.SafeErrorClassBudgetExhausted},
		{scenario: "too-many-events", wantClass: domain.SafeErrorClassBudgetExhausted},
	}
	for _, current := range tests {
		current := current
		t.Run(current.scenario, func(t *testing.T) {
			t.Parallel()

			server := newFixtureServer(t, "")
			var logBuffer bytes.Buffer
			model := newFixtureAdapter(t, fixtureConfiguration(server.endpoint(current.scenario), time.Second), strings.Repeat("b", 39)+"-generated", fixtureLogger(&logBuffer))
			var events []domain.ModelStreamEvent
			modelError := model.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
				events = append(events, event)
			})

			assertTerminalContract(t, events, modelError)
			if modelError.Class() != current.wantClass {
				t.Fatalf("model error class = %q, want %q", modelError.Class(), current.wantClass)
			}
		})
	}
}

func TestCancellationAndTimeoutCancelTheFixtureRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scenario  string
		timeout   bool
		wantClass domain.SafeErrorClass
	}{
		{name: "cancel", scenario: "cancel", wantClass: domain.SafeErrorClassCancelled},
		{name: "timeout", scenario: "timeout", timeout: true, wantClass: domain.SafeErrorClassTimeout},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()

			server := newFixtureServer(t, "")
			var logBuffer bytes.Buffer
			model := newFixtureAdapter(t, fixtureConfiguration(server.endpoint(current.scenario), time.Second), strings.Repeat("c", 41)+"-generated", fixtureLogger(&logBuffer))
			ctx, cancelParent := context.WithCancelCause(context.Background())
			trigger := func() { cancelParent(context.Canceled) }
			triggerReady := make(chan func(), 1)
			if current.timeout {
				ctx = context.Background()
				model.withTimeout = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
					requestContext, cancelRequest := context.WithCancelCause(parent)
					triggerReady <- func() { cancelRequest(context.DeadlineExceeded) }
					return requestContext, func() { cancelRequest(context.Canceled) }
				}
			}
			defer cancelParent(context.Canceled)
			result := make(chan *domain.ModelError, 1)
			go func() {
				result <- model.Stream(ctx, fixtureModelRequest(), func(domain.ModelStreamEvent) {})
			}()

			awaitSignal(t, server.requestStarted, "fixture request start")
			if current.timeout {
				trigger = <-triggerReady
			}
			trigger()
			modelError := awaitModelError(t, result)
			if modelError == nil || modelError.Class() != current.wantClass {
				t.Fatalf("model error = %#v, want class %q", modelError, current.wantClass)
			}
			awaitSignal(t, server.requestCancelled, "fixture request cancellation")
		})
	}
}

func TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("r", 47) + "-generated"
	target := newFixtureServer(t, "")
	source := newFixtureServer(t, "")
	source.setRedirectTarget(target.endpoint("normal") + "/chat/completions")
	var logBuffer bytes.Buffer
	model := newFixtureAdapter(t, fixtureConfiguration(source.endpoint("redirect"), time.Second), apiCanary, fixtureLogger(&logBuffer))
	var events []domain.ModelStreamEvent
	modelError := model.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
		events = append(events, event)
	})

	assertTerminalContract(t, events, modelError)
	if modelError.Class() != domain.SafeErrorClassPolicyDenied {
		t.Fatalf("model error class = %q, want %q", modelError.Class(), domain.SafeErrorClassPolicyDenied)
	}
	if got := len(target.capturedRequests()); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
	assertRequestAndSinkSafety(t, source, apiCanary, "", logBuffer.String(), modelError)
}

func TestSerializedRequestBoundaryIsExactAndFailsBeforeHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		wireBytes    int
		wantRequests int
		wantClass    domain.SafeErrorClass
	}{
		{name: "exact limit", wireBytes: domain.MaxModelRequestBytes, wantRequests: 1},
		{name: "one over", wireBytes: domain.MaxModelRequestBytes + 1, wantClass: domain.SafeErrorClassBudgetExhausted},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()

			server := newFixtureServer(t, "")
			configuration := fixtureConfiguration(server.endpoint("normal"), time.Second)
			request := requestWithWireSize(t, configuration, current.wireBytes)
			var logBuffer bytes.Buffer
			model := newFixtureAdapter(t, configuration, strings.Repeat("q", 41)+"-generated", fixtureLogger(&logBuffer))
			var events []domain.ModelStreamEvent
			modelError := model.Stream(context.Background(), request, func(event domain.ModelStreamEvent) {
				events = append(events, event)
			})

			if got := len(server.capturedRequests()); got != current.wantRequests {
				t.Fatalf("HTTP requests = %d, want %d", got, current.wantRequests)
			}
			assertTerminalContract(t, events, modelError)
			if current.wantClass != "" && modelError.Class() != current.wantClass {
				t.Fatalf("model error class = %q, want %q", modelError.Class(), current.wantClass)
			}
		})
	}
}

func fixtureConfiguration(endpoint string, timeout time.Duration) domain.ModelConfiguration {
	parsed, _ := url.Parse(endpoint)
	return domain.ModelConfiguration{
		ProviderKind:        domain.ModelProviderOpenAICompatible,
		Endpoint:            endpoint,
		Origin:              parsed.Scheme + "://" + parsed.Host,
		Model:               "fixture-model",
		APIKeySource:        domain.ModelAPIKeySourceRuntime,
		Temperature:         0.1,
		MaxOutputTokens:     2048,
		RequestTimeout:      timeout,
		StreamingRequired:   true,
		ToolCallingRequired: true,
		TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
}

func fixtureModelRequest() domain.ModelRequest {
	names := []domain.ToolName{
		domain.ToolNameGetResource,
		domain.ToolNameListResources,
		domain.ToolNameGetEvents,
		domain.ToolNameGetPodLogs,
		domain.ToolNameGetPreviousPodLogs,
		domain.ToolNameGetRelatedResources,
	}
	tools := make([]domain.ModelToolSpecification, 0, len(names))
	for _, name := range names {
		tools = append(tools, domain.ModelToolSpecification{
			Name:            name,
			Version:         "fixture-v1",
			Description:     "Exercise the fixed structured Tool compatibility contract.",
			InputSchemaJSON: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
		})
	}
	return domain.ModelRequest{
		ID: "00000000-0000-7000-8000-000000001201",
		Messages: []domain.ModelMessage{
			{Role: domain.ModelMessageRoleSystem, Content: "Use only the supplied structured Tools."},
			{Role: domain.ModelMessageRoleUser, Content: "Diagnose the synthetic fixture."},
		},
		Tools: tools,
	}
}

func requestWithWireSize(t *testing.T, configuration domain.ModelConfiguration, target int) domain.ModelRequest {
	t.Helper()

	request := fixtureModelRequest()
	for len(request.Messages) < 10 {
		request.Messages = append(request.Messages, domain.ModelMessage{Role: domain.ModelMessageRoleUser, Content: "x"})
	}
	body, err := encodeWireRequest(configuration, request)
	if err != nil {
		t.Fatalf("encode baseline request: %v", err)
	}
	remaining := target - len(body)
	for index := range request.Messages {
		capacity := domain.MaxModelMessageBytes - len(request.Messages[index].Content)
		if capacity > remaining {
			capacity = remaining
		}
		if capacity > 0 {
			request.Messages[index].Content += strings.Repeat("x", capacity)
			remaining -= capacity
		}
	}
	if remaining != 0 {
		t.Fatalf("could not construct %d-byte request; %d bytes remain", target, remaining)
	}
	body, err = encodeWireRequest(configuration, request)
	if err != nil {
		t.Fatalf("encode sized request: %v", err)
	}
	if len(body) != target {
		t.Fatalf("sized request bytes = %d, want %d", len(body), target)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("sized request Validate() error = %v", err)
	}
	return request
}

func fixtureLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func eventKinds(events []domain.ModelStreamEvent) []domain.ModelStreamEventKind {
	kinds := make([]domain.ModelStreamEventKind, len(events))
	for index, event := range events {
		kinds[index] = event.Kind
	}
	return kinds
}

func assertTerminalContract(t *testing.T, events []domain.ModelStreamEvent, modelError *domain.ModelError) {
	t.Helper()

	completed := 0
	for index, event := range events {
		if event.Terminal() {
			completed++
			if index != len(events)-1 {
				t.Fatal("terminal stream event was not last")
			}
		}
	}
	if modelError == nil {
		if completed != 1 {
			t.Fatalf("completed terminal events = %d, want 1", completed)
		}
		return
	}
	if err := modelError.Validate(); err != nil {
		t.Fatalf("model error Validate() error = %v", err)
	}
	if completed != 0 {
		t.Fatalf("completed terminal events = %d with terminal error, want 0", completed)
	}
}

func assertRequestAndSinkSafety(t *testing.T, server *fixtureServer, apiCanary, errorCanary, logs string, modelError *domain.ModelError) {
	t.Helper()

	requests := server.capturedRequests()
	if len(requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || !strings.HasSuffix(request.Path, "/chat/completions") {
		t.Fatalf("captured request = %s %s", request.Method, request.Path)
	}
	if request.ContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", request.ContentType)
	}
	if request.Authorization != "Bearer "+apiCanary {
		t.Fatal("Authorization did not reach the configured fixture origin exactly once")
	}
	if strings.Contains(string(request.Body), apiCanary) {
		t.Fatal("model request body contains the transport credential")
	}
	var payload wireRequest
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("decode captured request: %v", err)
	}
	if payload.Model != "fixture-model" || !payload.Stream || !payload.StreamOptions.IncludeUsage ||
		len(payload.Messages) == 0 || len(payload.Tools) != 6 {
		t.Fatal("captured request does not match the fixed streaming Tool profile")
	}
	seenTools := make(map[string]struct{}, len(payload.Tools))
	for _, tool := range payload.Tools {
		if tool.Type != "function" || !tool.Function.Strict || tool.Function.Name == "" || len(tool.Function.Parameters) == 0 {
			t.Fatal("captured request contains a non-strict Tool definition")
		}
		if _, exists := seenTools[tool.Function.Name]; exists {
			t.Fatal("captured request contains a duplicate Tool definition")
		}
		seenTools[tool.Function.Name] = struct{}{}
	}
	unsafeValues := []string{apiCanary, request.Authorization, string(request.Body), errorCanary}
	for _, value := range unsafeValues {
		if value == "" {
			continue
		}
		if strings.Contains(logs, value) {
			t.Fatal("captured logger contains a request credential or body")
		}
		if modelError != nil && strings.Contains(modelError.Error(), value) {
			t.Fatal("safe model error contains a request credential or remote body")
		}
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitModelError(t *testing.T, result <-chan *domain.ModelError) *domain.ModelError {
	t.Helper()

	select {
	case modelError := <-result:
		return modelError
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for model result")
		return nil
	}
}
