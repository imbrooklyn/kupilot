package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const maxMessageContentBytes = 65536

// ErrInvalidMessage reports a Message invariant failure without echoing data.
var ErrInvalidMessage = errors.New("Message data is invalid")

// MessageID is an opaque application-generated UUIDv7 Message identifier.
type MessageID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id MessageID) Valid() bool {
	return validUUIDv7(string(id))
}

// MessageRole is a project-owned conversation role, not a provider role.
type MessageRole string

const (
	MessageRoleUser         MessageRole = "user"
	MessageRoleAssistant    MessageRole = "assistant"
	MessageRoleSystemNotice MessageRole = "system_notice"
)

// MessageFormat declares how already-safe content is presented.
type MessageFormat string

const (
	MessageFormatPlain    MessageFormat = "plain"
	MessageFormatMarkdown MessageFormat = "markdown"
)

// MessageStatus distinguishes committed content from explicit safe placeholders.
type MessageStatus string

const (
	MessageStatusCommitted   MessageStatus = "committed"
	MessageStatusInterrupted MessageStatus = "interrupted"
	MessageStatusRedacted    MessageStatus = "redacted"
)

// Message contains only bounded content that has already passed the safety pipeline.
type Message struct {
	ID        MessageID
	SessionID SessionID
	RunID     *AgentRunID
	Role      MessageRole
	Content   string
	Format    MessageFormat
	Status    MessageStatus
	Scope     *ScopeSnapshot
	Resource  *ResourceRef
	Hash      string
	CreatedAt time.Time
}

// Validate checks the complete safe Message persistence contract.
func (message Message) Validate() error {
	if !message.ID.Valid() || !message.SessionID.Valid() ||
		(message.RunID != nil && !message.RunID.Valid()) ||
		(message.Role != MessageRoleUser && message.Role != MessageRoleAssistant && message.Role != MessageRoleSystemNotice) ||
		(message.Format != MessageFormatPlain && message.Format != MessageFormatMarkdown) ||
		(message.Status != MessageStatusCommitted && message.Status != MessageStatusInterrupted && message.Status != MessageStatusRedacted) ||
		!validBoundedText(message.Content, 1, maxMessageContentBytes) ||
		message.Hash != MessageContentHash(message.Content) ||
		!validDurableTime(message.CreatedAt) {
		return ErrInvalidMessage
	}
	if message.Scope != nil {
		if err := message.Scope.Validate(); err != nil {
			return ErrInvalidMessage
		}
	}
	if message.Resource != nil {
		if message.Scope == nil ||
			message.Resource.Namespace != message.Scope.Namespace ||
			message.Resource.Validate() != nil {
			return ErrInvalidMessage
		}
	}
	return nil
}

// MessageContentHash returns the lowercase SHA-256 digest of safe Message content.
func MessageContentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
