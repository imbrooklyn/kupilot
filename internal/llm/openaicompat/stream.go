package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"unicode/utf8"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type toolCallAssembly struct {
	id        string
	name      string
	arguments string
}

type streamDecodeFailure string

const (
	streamFailureMessageShape      streamDecodeFailure = "message_shape"
	streamFailureAfterFinish       streamDecodeFailure = "event_after_finish"
	streamFailureCredentialText    streamDecodeFailure = "credential_in_text"
	streamFailureDuplicateFinish   streamDecodeFailure = "duplicate_finish"
	streamFailureUnsupportedFinish streamDecodeFailure = "unsupported_finish"
	streamFailureUsageOrder        streamDecodeFailure = "usage_order"
	streamFailureUsageValue        streamDecodeFailure = "usage_value"
	streamFailureTextLimit         streamDecodeFailure = "text_limit"
	streamFailureToolIndex         streamDecodeFailure = "tool_index"
	streamFailureToolType          streamDecodeFailure = "tool_type"
	streamFailureToolFragment      streamDecodeFailure = "tool_fragment"
	streamFailureToolCredential    streamDecodeFailure = "credential_in_tool"
	streamFailureMissingFinish     streamDecodeFailure = "missing_finish"
	streamFailureFinishContent     streamDecodeFailure = "finish_content_mismatch"
	streamFailureToolIndexGap      streamDecodeFailure = "tool_index_gap"
	streamFailureToolAssembly      streamDecodeFailure = "tool_assembly"
	streamFailureUnsupportedState  streamDecodeFailure = "unsupported_state"
	streamFailureEventLimit        streamDecodeFailure = "event_limit"
	streamFailureNeutralEvent      streamDecodeFailure = "neutral_event"
)

func (failure streamDecodeFailure) Error() string {
	return "model stream decoder rejected " + string(failure)
}

type responseDecoder struct {
	ctx           context.Context
	requestID     domain.ModelRequestID
	consume       agent.ModelStreamConsumer
	credential    *config.SecretValue
	textScanner   credentialScanner
	sequence      int
	pendingFinish *domain.ModelFinishReason
	usageSeen     bool
	sawTool       bool
	textBytes     int
	textFragments [][]byte
	tools         map[int]*toolCallAssembly
	rawFailure    error
}

type responseEnvelope struct {
	Choices []struct {
		Index int `json:"index"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

func validateResponseChunk(
	_ context.Context,
	message *schema.Message,
	rawBody []byte,
	end bool,
) (*schema.Message, error) {
	if end {
		return message, nil
	}
	var envelope responseEnvelope
	if len(rawBody) == 0 || !utf8.Valid(rawBody) || json.Unmarshal(rawBody, &envelope) != nil {
		return nil, errMalformedProviderChunk
	}
	if len(envelope.Choices) > 1 || len(envelope.Choices) == 1 && envelope.Choices[0].Index != 0 {
		return nil, errUnsupportedProviderChunk
	}
	if len(envelope.Choices) == 0 && (len(envelope.Usage) == 0 || bytes.Equal(envelope.Usage, []byte("null"))) {
		return nil, errMalformedProviderChunk
	}
	return message, nil
}

func (decoder *responseDecoder) decode(stream *schema.StreamReader[*schema.Message]) *domain.ModelError {
	defer decoder.discardText()
	for {
		if code, cancelled := contextModelErrorCode(decoder.ctx); cancelled {
			return decoder.modelError(code)
		}
		message, receiveError := stream.Recv()
		if code, cancelled := contextModelErrorCode(decoder.ctx); cancelled {
			return decoder.modelError(code)
		}
		if errors.Is(receiveError, io.EOF) {
			return decoder.complete()
		}
		if receiveError != nil {
			decoder.rawFailure = receiveError
			return decoder.modelError(mapModelStreamError(decoder.ctx, receiveError))
		}
		if modelError := decoder.consumeMessage(message); modelError != nil {
			return modelError
		}
	}
}

func (decoder *responseDecoder) consumeMessage(message *schema.Message) *domain.ModelError {
	if message == nil || message.Role != "" && message.Role != schema.Assistant ||
		len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 ||
		len(message.AssistantGenMultiContent) != 0 || message.Name != "" ||
		message.ToolCallID != "" || message.ToolName != "" || message.ReasoningContent != "" ||
		message.ResponseMeta != nil && message.ResponseMeta.LogProbs != nil {
		return decoder.reject(domain.ModelErrorCodeUnsupportedResponse, streamFailureMessageShape)
	}

	finishReason := ""
	var usage *schema.TokenUsage
	if message.ResponseMeta != nil {
		finishReason = message.ResponseMeta.FinishReason
		usage = message.ResponseMeta.Usage
	}
	if decoder.pendingFinish != nil &&
		(message.Content != "" || len(message.ToolCalls) != 0 || finishReason != "" || usage == nil) {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureAfterFinish)
	}
	if message.Content != "" {
		if decoder.textScanner.Contains(message.Content) {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureCredentialText)
		}
		if modelError := decoder.bufferText(message.Content); modelError != nil {
			return modelError
		}
	}
	for _, fragment := range message.ToolCalls {
		if modelError := decoder.consumeToolFragment(fragment); modelError != nil {
			return modelError
		}
	}
	if finishReason != "" {
		if decoder.pendingFinish != nil {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureDuplicateFinish)
		}
		mapped, ok := mapFinishReason(finishReason)
		if !ok {
			return decoder.reject(domain.ModelErrorCodeUnsupportedResponse, streamFailureUnsupportedFinish)
		}
		switch mapped {
		case domain.ModelFinishReasonToolCalls:
			decoder.discardText()
		case domain.ModelFinishReasonStop, domain.ModelFinishReasonLength:
			if decoder.sawTool {
				return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureFinishContent)
			}
			if modelError := decoder.flushText(); modelError != nil {
				return modelError
			}
		}
		decoder.pendingFinish = &mapped
	}
	if usage != nil {
		if decoder.pendingFinish == nil || decoder.usageSeen {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureUsageOrder)
		}
		neutral := domain.ModelUsage{
			InputTokens:  int64(usage.PromptTokens),
			OutputTokens: int64(usage.CompletionTokens),
			TotalTokens:  int64(usage.TotalTokens),
		}
		if neutral.Validate() != nil {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureUsageValue)
		}
		decoder.usageSeen = true
		if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventUsage, Usage: &neutral}); modelError != nil {
			return modelError
		}
	}
	if message.Content == "" && len(message.ToolCalls) == 0 && finishReason == "" && usage == nil {
		// Some compatible endpoints emit a bounded assistant role or empty
		// delta before content. It carries no authority and produces no neutral
		// event; the raw transport still counts it against stream limits.
		return nil
	}
	return nil
}

func (decoder *responseDecoder) bufferText(fragment string) *domain.ModelError {
	candidate := domain.ModelStreamEvent{
		Sequence:  1,
		Kind:      domain.ModelStreamEventTextDelta,
		TextDelta: fragment,
	}
	if candidate.Validate() != nil {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureNeutralEvent)
	}
	if decoder.textBytes > domain.MaxModelMessageBytes-len(fragment) {
		return decoder.reject(domain.ModelErrorCodeStreamLimitExceeded, streamFailureTextLimit)
	}
	decoder.textBytes += len(fragment)
	decoder.textFragments = append(decoder.textFragments, append([]byte(nil), fragment...))
	return nil
}

func (decoder *responseDecoder) flushText() *domain.ModelError {
	fragments := decoder.textFragments
	decoder.textFragments = nil
	decoder.textBytes = 0
	defer func() {
		for _, fragment := range fragments {
			clear(fragment)
		}
	}()
	for _, fragment := range fragments {
		if modelError := decoder.emit(domain.ModelStreamEvent{
			Kind:      domain.ModelStreamEventTextDelta,
			TextDelta: string(fragment),
		}); modelError != nil {
			return modelError
		}
	}
	return nil
}

func (decoder *responseDecoder) discardText() {
	for _, fragment := range decoder.textFragments {
		clear(fragment)
	}
	decoder.textFragments = nil
	decoder.textBytes = 0
}

func (decoder *responseDecoder) consumeToolFragment(fragment schema.ToolCall) *domain.ModelError {
	if fragment.Index == nil {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolIndex)
	}
	if fragment.Type != "" && fragment.Type != "function" {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolType)
	}
	index := *fragment.Index
	neutral := domain.ModelToolCallFragment{
		Index:             index,
		IDFragment:        fragment.ID,
		NameFragment:      fragment.Function.Name,
		ArgumentsFragment: fragment.Function.Arguments,
	}
	if neutral.Validate() != nil {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolFragment)
	}
	decoder.sawTool = true
	assembly := decoder.tools[index]
	if assembly == nil {
		assembly = &toolCallAssembly{}
		decoder.tools[index] = assembly
	}
	assembly.id += fragment.ID
	assembly.name += fragment.Function.Name
	assembly.arguments += fragment.Function.Arguments
	if len(assembly.id) > domain.MaxModelToolCallIDBytes || len(assembly.name) > domain.MaxModelToolNameBytes ||
		len(assembly.arguments) > domain.MaxModelToolArgumentsBytes {
		return decoder.modelError(domain.ModelErrorCodeStreamLimitExceeded)
	}
	if credentialAppearsInStrings(decoder.credential, assembly.id, assembly.name, assembly.arguments) {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolCredential)
	}

	return decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventToolCallFragment, ToolCallFragment: &neutral})
}

func (decoder *responseDecoder) complete() *domain.ModelError {
	if decoder.pendingFinish == nil {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureMissingFinish)
	}
	switch *decoder.pendingFinish {
	case domain.ModelFinishReasonToolCalls:
		if !decoder.sawTool {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureFinishContent)
		}
		for index := 0; index < len(decoder.tools); index++ {
			assembly := decoder.tools[index]
			if assembly == nil {
				return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolIndexGap)
			}
			call := domain.ModelToolCall{ID: assembly.id, Name: domain.ToolName(assembly.name), ArgumentsJSON: assembly.arguments}
			if call.Validate() != nil {
				return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureToolAssembly)
			}
		}
	case domain.ModelFinishReasonStop, domain.ModelFinishReasonLength:
		if decoder.sawTool {
			return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureFinishContent)
		}
	default:
		return decoder.reject(domain.ModelErrorCodeUnsupportedResponse, streamFailureUnsupportedState)
	}
	completion := domain.ModelCompletion{FinishReason: *decoder.pendingFinish}
	if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventCompleted, Completion: &completion}); modelError != nil {
		return modelError
	}
	decoder.pendingFinish = nil
	return nil
}

func (decoder *responseDecoder) emit(event domain.ModelStreamEvent) *domain.ModelError {
	if code, cancelled := contextModelErrorCode(decoder.ctx); cancelled {
		return decoder.modelError(code)
	}
	if decoder.sequence >= domain.MaxModelStreamEvents {
		return decoder.reject(domain.ModelErrorCodeStreamLimitExceeded, streamFailureEventLimit)
	}
	decoder.sequence++
	event.Sequence = decoder.sequence
	if event.Validate() != nil {
		return decoder.reject(domain.ModelErrorCodeMalformedStream, streamFailureNeutralEvent)
	}
	decoder.consume(event)
	return nil
}

func (decoder *responseDecoder) modelError(code domain.ModelErrorCode) *domain.ModelError {
	return domain.NewModelError(code, domain.ModelOperationStream, string(decoder.requestID))
}

func (decoder *responseDecoder) reject(code domain.ModelErrorCode, failure streamDecodeFailure) *domain.ModelError {
	if decoder.rawFailure == nil {
		decoder.rawFailure = failure
	}
	return decoder.modelError(code)
}

func mapModelStreamError(ctx context.Context, cause error) domain.ModelErrorCode {
	if code, cancelled := contextModelErrorCode(ctx); cancelled {
		return code
	}
	switch {
	case errors.Is(cause, errModelResponseLimitReached):
		return domain.ModelErrorCodeStreamLimitExceeded
	case errors.Is(cause, errUnsupportedResponseMedia), errors.Is(cause, errUnsupportedProviderChunk):
		return domain.ModelErrorCodeUnsupportedResponse
	case errors.Is(cause, errMalformedProviderChunk):
		return domain.ModelErrorCodeMalformedStream
	}
	var apiError *einoopenai.APIError
	if errors.As(cause, &apiError) {
		return mapHTTPStatus(apiError.HTTPStatusCode)
	}
	var networkError net.Error
	if errors.As(cause, &networkError) {
		if networkError.Timeout() {
			return domain.ModelErrorCodeTimeout
		}
		return domain.ModelErrorCodeServiceUnavailable
	}
	return domain.ModelErrorCodeMalformedStream
}

func mapFinishReason(value string) (domain.ModelFinishReason, bool) {
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
