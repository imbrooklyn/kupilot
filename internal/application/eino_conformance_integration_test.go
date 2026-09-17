//go:build integration

package application

import (
	"sync"

	"github.com/cloudwego/eino/schema"
)

type conformanceUsage struct {
	mu                                                sync.Mutex
	responses, input, output                          int
	reasoningTokens, reasoningBlocks, encryptedBlocks int
}

func (usage *conformanceUsage) observe(message *schema.Message) {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil || message.ResponseMeta.Usage.TotalTokens == 0 {
		return
	}
	usage.mu.Lock()
	defer usage.mu.Unlock()
	usage.responses++
	usage.input += message.ResponseMeta.Usage.PromptTokens
	usage.output += message.ResponseMeta.Usage.CompletionTokens
}
