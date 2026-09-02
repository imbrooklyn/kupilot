package einoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	einoReasoningContentKey = "reasoning-content"
	einoRequestIDKey        = "openai-request-id"
)

type responseEnvelope struct {
	Choices []struct {
		Index *int `json:"index"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

// validateResponseChunk observes Eino's decoded OpenAI stream without
// replacing either the provider payload or Eino's message conversion.
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
	if len(envelope.Choices) > 1 || len(envelope.Choices) == 1 &&
		(envelope.Choices[0].Index == nil || *envelope.Choices[0].Index != 0) {
		return nil, errUnsupportedProviderChunk
	}
	if len(envelope.Choices) == 0 && (len(envelope.Usage) == 0 || bytes.Equal(envelope.Usage, []byte("null"))) {
		return nil, errMalformedProviderChunk
	}
	return message, nil
}

// collectModelMessage drains one Eino stream exactly once, applies bounded
// project checks to each decoded chunk, and asks Eino to assemble the final
// message. Kupilot does not reimplement provider delta concatenation here.
func collectModelMessage(
	ctx context.Context,
	stream *schema.StreamReader[*schema.Message],
	credential *config.SecretValue,
	observeContent func(string) error,
) (*schema.Message, error) {
	if ctx == nil || stream == nil || credential == nil || !credential.IsSet() {
		return nil, errTransportRequestInvalid
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0, 16)
	validator := modelStreamValidator{scanner: credentialScanner{credential: credential}}
	finishSeen := false
	usageSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(chunks) >= domain.MaxModelStreamChunks {
			return nil, errModelResponseLimitReached
		}
		if err := validator.validateChunk(chunk, finishSeen); err != nil {
			return nil, err
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			if usageSeen || !finishSeen && chunk.ResponseMeta.FinishReason == "" {
				return nil, errMalformedProviderChunk
			}
			usageSeen = true
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.FinishReason != "" {
			finishSeen = true
		}
		if observeContent != nil && chunk.Content != "" {
			if err := observeContent(chunk.Content); err != nil {
				return nil, err
			}
		}
		chunks = append(chunks, chunk)
	}
	if !finishSeen || len(chunks) == 0 {
		return nil, errMalformedProviderChunk
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedProviderChunk, err)
	}
	if !assembledModelMessageWithinLimits(message, validator.discardedReasoningBytes) {
		return nil, errModelResponseLimitReached
	}
	return message, nil
}

func assembledModelMessageWithinLimits(message *schema.Message, discardedReasoningBytes int) bool {
	if message == nil || len(message.Content) > domain.MaxModelMessageBytes ||
		len(message.ToolCalls) > domain.MaxAgentToolCalls || discardedReasoningBytes < 0 ||
		discardedReasoningBytes > domain.MaxModelMessageBytes-len(message.Content) {
		return false
	}
	total := len(message.Content) + discardedReasoningBytes
	for _, call := range message.ToolCalls {
		if len(call.ID) > domain.MaxModelToolCallIDBytes ||
			len(call.Function.Name) > domain.MaxModelToolNameBytes ||
			len(call.Function.Arguments) > domain.MaxModelToolArgumentsBytes {
			return false
		}
		total += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		if total > domain.MaxModelMessageBytes {
			return false
		}
	}
	return total <= domain.MaxModelMessageBytes
}

type modelStreamValidator struct {
	scanner                 credentialScanner
	discardedReasoningBytes int
}

func (validator *modelStreamValidator) validateChunk(chunk *schema.Message, afterFinish bool) error {
	if validator == nil || chunk == nil || chunk.Role != "" && chunk.Role != schema.Assistant ||
		len(chunk.MultiContent) != 0 || len(chunk.UserInputMultiContent) != 0 ||
		len(chunk.AssistantGenMultiContent) != 0 || chunk.Name != "" ||
		chunk.ToolCallID != "" || chunk.ToolName != "" ||
		chunk.ResponseMeta != nil && chunk.ResponseMeta.LogProbs != nil {
		return errUnsupportedProviderChunk
	}
	reasoningBytes, err := validator.admitAndClearEinoMetadata(chunk)
	if err != nil {
		return err
	}
	if !utf8.ValidString(chunk.Content) {
		return errMalformedProviderChunk
	}
	finishReason := ""
	var usage *schema.TokenUsage
	if chunk.ResponseMeta != nil {
		finishReason = chunk.ResponseMeta.FinishReason
		usage = chunk.ResponseMeta.Usage
	}
	if afterFinish && (chunk.Content != "" || reasoningBytes != 0 || len(chunk.ToolCalls) != 0 || finishReason != "" || usage == nil) {
		return errMalformedProviderChunk
	}
	if finishReason != "" && finishReason != "stop" && finishReason != "tool_calls" && finishReason != "length" {
		return errUnsupportedProviderChunk
	}
	if usage != nil && !validModelUsage(usage) {
		return errMalformedProviderChunk
	}

	if reasoningBytes > domain.MaxModelMessageBytes-validator.discardedReasoningBytes {
		return errModelResponseLimitReached
	}
	validator.discardedReasoningBytes += reasoningBytes
	chunkBytes := len(chunk.Content) + reasoningBytes
	if validator.scanner.Contains(chunk.Content) {
		return errMalformedProviderChunk
	}
	if len(chunk.ToolCalls) > domain.MaxAgentToolCalls {
		return errModelResponseLimitReached
	}
	for _, call := range chunk.ToolCalls {
		if call.Extra != nil || call.Type != "" && call.Type != "function" ||
			call.Index == nil || *call.Index < 0 || *call.Index >= domain.MaxAgentToolCalls ||
			!utf8.ValidString(call.ID) || !utf8.ValidString(call.Type) ||
			!utf8.ValidString(call.Function.Name) || !utf8.ValidString(call.Function.Arguments) {
			return errUnsupportedProviderChunk
		}
		if len(call.ID) > domain.MaxModelToolCallIDBytes ||
			len(call.Function.Name) > domain.MaxModelToolNameBytes ||
			len(call.Function.Arguments) > domain.MaxModelToolArgumentsBytes {
			return errModelResponseLimitReached
		}
		if validator.scanner.Contains(call.ID) || validator.scanner.Contains(call.Type) ||
			validator.scanner.Contains(call.Function.Name) || validator.scanner.Contains(call.Function.Arguments) {
			return errMalformedProviderChunk
		}
		chunkBytes += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		if chunkBytes > domain.MaxModelStreamChunkBytes {
			return errModelResponseLimitReached
		}
	}
	if chunkBytes > domain.MaxModelStreamChunkBytes {
		return errModelResponseLimitReached
	}
	if chunk.Content == "" && len(chunk.ToolCalls) == 0 && finishReason == "" && usage == nil {
		// Compatible endpoints may emit an empty role delta. It carries no
		// authority but still counts against the transport and chunk ceilings.
		return nil
	}
	return nil
}

func (validator *modelStreamValidator) admitAndClearEinoMetadata(message *schema.Message) (int, error) {
	if validator == nil || message == nil {
		return 0, errUnsupportedProviderChunk
	}
	reasoning := message.ReasoningContent
	reasoningValue, hasReasoningMetadata := message.Extra[einoReasoningContentKey]
	if (reasoning == "" && hasReasoningMetadata) || (reasoning != "" && !hasReasoningMetadata) ||
		!utf8.ValidString(reasoning) {
		return 0, errUnsupportedProviderChunk
	}
	expectedMetadata := 0
	if hasReasoningMetadata {
		expectedMetadata++
		reflected := reflect.ValueOf(reasoningValue)
		if !reflected.IsValid() || reflected.Kind() != reflect.String || reflected.String() != reasoning {
			return 0, errUnsupportedProviderChunk
		}
		if validator.scanner.Contains(reasoning) {
			return 0, errMalformedProviderChunk
		}
	}

	requestIDValue, hasRequestID := message.Extra[einoRequestIDKey]
	if hasRequestID {
		expectedMetadata++
		reflected := reflect.ValueOf(requestIDValue)
		if !reflected.IsValid() || reflected.Kind() != reflect.String {
			return 0, errUnsupportedProviderChunk
		}
		requestID := reflected.String()
		if !validProviderRequestIDText(requestID) || requestID != "" && validator.scanner.Contains(requestID) {
			return 0, errMalformedProviderChunk
		}
	}
	if len(message.Extra) != expectedMetadata {
		return 0, errUnsupportedProviderChunk
	}

	message.ReasoningContent = ""
	message.Extra = nil
	return len(reasoning), nil
}

func validProviderRequestIDText(requestID string) bool {
	if len(requestID) > 256 || !utf8.ValidString(requestID) {
		return false
	}
	for _, current := range requestID {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validModelUsage(usage *schema.TokenUsage) bool {
	if usage == nil || usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 ||
		usage.PromptTokens > 1_000_000_000 || usage.CompletionTokens > 1_000_000_000 ||
		usage.TotalTokens > 1_000_000_000 || usage.PromptTokens > usage.TotalTokens ||
		usage.CompletionTokens > usage.TotalTokens ||
		usage.PromptTokens+usage.CompletionTokens != usage.TotalTokens {
		return false
	}
	return usage.PromptTokenDetails.CachedTokens >= 0 &&
		usage.CompletionTokensDetails.ReasoningTokens >= 0
}
