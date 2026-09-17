package application

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var (
	errProviderFinishDuplicate = fmt.Errorf("%w: duplicate finish", errMalformedProviderChunk)
	errProviderAfterFinish     = fmt.Errorf("%w: event after finish", errMalformedProviderChunk)
	errProviderFinishMissing   = fmt.Errorf("%w: missing finish", errMalformedProviderChunk)
	errProviderUsage           = fmt.Errorf("%w: invalid usage order", errMalformedProviderChunk)
	errProviderStopReason      = fmt.Errorf("%w: invalid stop reason", errMalformedProviderChunk)
)

// sseOrder observes bounded data records before the pinned Eino ACL coalesces
// empty messages. It never assembles an answer or modifies provider bytes.
type sseOrder struct {
	finished bool
	usage    bool
	done     bool
}

func (order *sseOrder) accept(payload []byte) error {
	payload = bytes.TrimSpace(payload)
	if order.done {
		return errProviderAfterFinish
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		if !order.finished {
			return errProviderFinishMissing
		}
		order.done = true
		return nil
	}
	var envelope struct {
		Choices []struct {
			Index        *int    `json:"index"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return errMalformedProviderChunk
	}
	hasUsage := len(envelope.Usage) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null"))
	if hasUsage {
		var usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		}
		if json.Unmarshal(envelope.Usage, &usage) != nil || usage.Prompt < 0 || usage.Completion < 0 ||
			usage.Total < 0 || usage.Total > 1_000_000_000 || usage.Prompt > usage.Total ||
			usage.Completion > usage.Total || usage.Prompt+usage.Completion != usage.Total {
			return errProviderUsage
		}
	}
	if len(envelope.Choices) == 0 {
		if !order.finished || !hasUsage || order.usage {
			return errProviderUsage
		}
		order.usage = true
		return nil
	}
	if len(envelope.Choices) != 1 || envelope.Choices[0].Index == nil || *envelope.Choices[0].Index != 0 {
		return errUnsupportedProviderChunk
	}
	choice := envelope.Choices[0]
	finish := ""
	if choice.FinishReason != nil {
		finish = *choice.FinishReason
	}
	if order.finished {
		if finish != "" {
			return errProviderFinishDuplicate
		}
		return errProviderAfterFinish
	}
	if finish != "" && finish != "stop" && finish != "tool_calls" && finish != "length" {
		return errProviderStopReason
	}
	if hasUsage {
		if finish == "" || order.usage {
			return errProviderUsage
		}
		order.usage = true
	}
	if finish != "" {
		order.finished = true
	}
	return nil
}
