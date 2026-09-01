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
) (*schema.Message, error) {
	if ctx == nil || stream == nil || credential == nil || !credential.IsSet() {
		return nil, errTransportRequestInvalid
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0, 16)
	scanner := credentialScanner{credential: credential}
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
		if err := validateModelChunk(chunk, &scanner, finishSeen); err != nil {
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
		chunks = append(chunks, chunk)
	}
	if !finishSeen || len(chunks) == 0 {
		return nil, errMalformedProviderChunk
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedProviderChunk, err)
	}
	if !assembledModelMessageWithinLimits(message) {
		return nil, errModelResponseLimitReached
	}
	return message, nil
}

func assembledModelMessageWithinLimits(message *schema.Message) bool {
	if message == nil || len(message.Content) > domain.MaxModelMessageBytes ||
		len(message.ToolCalls) > domain.MaxAgentToolCalls {
		return false
	}
	total := len(message.Content)
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

func validateModelChunk(chunk *schema.Message, scanner *credentialScanner, afterFinish bool) error {
	if chunk == nil || chunk.Role != "" && chunk.Role != schema.Assistant ||
		len(chunk.MultiContent) != 0 || len(chunk.UserInputMultiContent) != 0 ||
		len(chunk.AssistantGenMultiContent) != 0 || chunk.Name != "" ||
		chunk.ToolCallID != "" || chunk.ToolName != "" || chunk.ReasoningContent != "" ||
		chunk.ResponseMeta != nil && chunk.ResponseMeta.LogProbs != nil || !admitAndClearEinoMetadata(chunk, scanner) {
		return errUnsupportedProviderChunk
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
	if afterFinish && (chunk.Content != "" || len(chunk.ToolCalls) != 0 || finishReason != "" || usage == nil) {
		return errMalformedProviderChunk
	}
	if finishReason != "" && finishReason != "stop" && finishReason != "tool_calls" && finishReason != "length" {
		return errUnsupportedProviderChunk
	}
	if usage != nil && !validModelUsage(usage) {
		return errMalformedProviderChunk
	}

	chunkBytes := len(chunk.Content)
	if scanner.Contains(chunk.Content) {
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
		if scanner.Contains(call.ID) || scanner.Contains(call.Type) ||
			scanner.Contains(call.Function.Name) || scanner.Contains(call.Function.Arguments) {
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

func admitAndClearEinoMetadata(message *schema.Message, scanner *credentialScanner) bool {
	if message == nil || len(message.Extra) == 0 {
		return true
	}
	value, exists := message.Extra["openai-request-id"]
	reflected := reflect.ValueOf(value)
	if len(message.Extra) != 1 || !exists || !reflected.IsValid() || reflected.Kind() != reflect.String {
		return false
	}
	requestID := reflected.String()
	if !validProviderRequestIDText(requestID) || requestID != "" && scanner.Contains(requestID) {
		return false
	}
	message.Extra = nil
	return true
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
