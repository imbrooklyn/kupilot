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

type responseDecoder struct {
	ctx           context.Context
	requestID     domain.ModelRequestID
	consume       agent.ModelStreamConsumer
	credential    *config.SecretValue
	textScanner   credentialScanner
	sequence      int
	pendingFinish *domain.ModelFinishReason
	usageSeen     bool
	sawText       bool
	sawTool       bool
	lastToolIndex int
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
		return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
	}

	finishReason := ""
	var usage *schema.TokenUsage
	if message.ResponseMeta != nil {
		finishReason = message.ResponseMeta.FinishReason
		usage = message.ResponseMeta.Usage
	}
	if decoder.pendingFinish != nil &&
		(message.Content != "" || len(message.ToolCalls) != 0 || finishReason != "" || usage == nil) {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if message.Content != "" && len(message.ToolCalls) != 0 ||
		message.Content != "" && decoder.sawTool ||
		len(message.ToolCalls) != 0 && decoder.sawText {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	if message.Content != "" {
		if decoder.textScanner.Contains(message.Content) {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		decoder.sawText = true
		if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventTextDelta, TextDelta: message.Content}); modelError != nil {
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
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		mapped, ok := mapFinishReason(finishReason)
		if !ok {
			return decoder.modelError(domain.ModelErrorCodeUnsupportedResponse)
		}
		decoder.pendingFinish = &mapped
	}
	if usage != nil {
		if decoder.pendingFinish == nil || decoder.usageSeen {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		neutral := domain.ModelUsage{
			InputTokens:  int64(usage.PromptTokens),
			OutputTokens: int64(usage.CompletionTokens),
			TotalTokens:  int64(usage.TotalTokens),
		}
		if neutral.Validate() != nil {
			return decoder.modelError(domain.ModelErrorCodeMalformedStream)
		}
		decoder.usageSeen = true
		if modelError := decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventUsage, Usage: &neutral}); modelError != nil {
			return modelError
		}
	}
	if message.Content == "" && len(message.ToolCalls) == 0 && finishReason == "" && usage == nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	return nil
}

func (decoder *responseDecoder) consumeToolFragment(fragment schema.ToolCall) *domain.ModelError {
	if fragment.Index == nil || *fragment.Index < decoder.lastToolIndex || fragment.Type != "" && fragment.Type != "function" {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	index := *fragment.Index
	decoder.lastToolIndex = index
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
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}

	neutral := domain.ModelToolCallFragment{
		Index:             index,
		IDFragment:        fragment.ID,
		NameFragment:      fragment.Function.Name,
		ArgumentsFragment: fragment.Function.Arguments,
	}
	if neutral.Validate() != nil {
		return decoder.modelError(domain.ModelErrorCodeMalformedStream)
	}
	return decoder.emit(domain.ModelStreamEvent{Kind: domain.ModelStreamEventToolCallFragment, ToolCallFragment: &neutral})
}

func (decoder *responseDecoder) complete() *domain.ModelError {
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
			call := domain.ModelToolCall{ID: assembly.id, Name: domain.ToolName(assembly.name), ArgumentsJSON: assembly.arguments}
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

func (decoder *responseDecoder) modelError(code domain.ModelErrorCode) *domain.ModelError {
	return domain.NewModelError(code, domain.ModelOperationStream, string(decoder.requestID))
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
