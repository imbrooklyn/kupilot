package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMessageValidationCoversHashNullableScopeAndLimits(t *testing.T) {
	now := time.UnixMilli(2).UTC()
	content := string(rune(0x03bb)) + " safe external text"
	message := Message{
		ID:        "00000000-0000-7000-8000-000000000101",
		SessionID: "00000000-0000-7000-8000-000000000102",
		Role:      MessageRoleUser,
		Content:   content,
		Format:    MessageFormatPlain,
		Status:    MessageStatusCommitted,
		Hash:      MessageContentHash(content),
		CreatedAt: now,
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	withScope := message
	withScope.Scope = &ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 3}
	withScope.Resource = &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"}
	if err := withScope.Validate(); err != nil {
		t.Fatalf("Validate(with scope) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Message)
	}{
		{name: "hash mismatch", mutate: func(value *Message) { value.Hash = strings.Repeat("0", 64) }},
		{name: "resource without scope", mutate: func(value *Message) { value.Resource = withScope.Resource }},
		{name: "resource namespace mismatch", mutate: func(value *Message) {
			value.Scope = &ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 3}
			value.Resource = &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "other-namespace", Name: "sample-pod"}
		}},
		{name: "resource outside allowlist", mutate: func(value *Message) {
			value.Scope = &ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 3}
			value.Resource = &ResourceRef{APIVersion: "v1", Kind: "Secret", Namespace: "test-namespace", Name: "sample-secret"}
		}},
		{name: "oversized content", mutate: func(value *Message) {
			value.Content = strings.Repeat("a", maxMessageContentBytes+1)
			value.Hash = MessageContentHash(value.Content)
		}},
		{name: "invalid role", mutate: func(value *Message) { value.Role = "provider_role" }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := message
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
