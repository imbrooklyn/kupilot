package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"time"
)

const (
	// SessionContextSummarySchemaVersion changes when durable summary fields change.
	SessionContextSummarySchemaVersion = "session-context-summary/v2"
	// SafeConversationContextPolicyVersion changes when eligible context meaning changes.
	SafeConversationContextPolicyVersion = "safe-conversation-context/2026-09-06.v2"
	// MaxCommittedSteerInputs bounds additional user inputs committed to one run.
	MaxCommittedSteerInputs = 8
	// MaxRunConversationSequence is the final assistant sequence after the
	// initial user input and every admitted steer.
	MaxRunConversationSequence = MaxCommittedSteerInputs + 1
	// MaxSessionContextMessages is the aggregate safe-message selection ceiling.
	MaxSessionContextMessages = 4096
	// MaxSessionHistoryBytes bounds all selected eligible durable content. It is
	// independent from the smaller request/compaction working set.
	MaxSessionHistoryBytes = 4 * 1024 * 1024
	// MaxSessionContextBytes is the uncompressed working-set threshold used to
	// trigger Eino summarization before a main model request.
	MaxSessionContextBytes = 128 * 1024
	// MaxSessionSummaryBytes is the independently bounded durable summary ceiling.
	MaxSessionSummaryBytes = 16 * 1024
)

var ErrInvalidSessionContextSummary = errors.New("Session context summary data is invalid")

// SessionContextSummary is the only durable model-generated context derivative.
// It contains no framework message, prompt, Tool result, or historic authority.
type SessionContextSummary struct {
	SessionID        SessionID
	Text             string
	SummaryHash      string
	SchemaVersion    string
	PolicyVersion    string
	CoveredFirstID   MessageID
	CoveredThroughID MessageID
	CoveredCount     int
	CoveredBytes     int
	CoverageDigest   string
	GeneratedAt      time.Time
	AgentProfile     string
	AgentOriginHash  string
	Truncated        bool
	Degraded         bool
}

// SessionContextCoverageItem is non-content metadata for one eligible ordered
// Message. It cannot restore scope, Evidence, Tool output, or any authority.
type SessionContextCoverageItem struct {
	MessageID    MessageID
	RunID        AgentRunID
	RunSequence  int
	Role         MessageRole
	ContentHash  string
	ContentBytes int
}

// Validate checks one exact coverage element.
func (item SessionContextCoverageItem) Validate() error {
	if !item.MessageID.Valid() || !item.RunID.Valid() || item.RunSequence < 0 || item.RunSequence > MaxRunConversationSequence ||
		(item.Role != MessageRoleUser && item.Role != MessageRoleAssistant) ||
		(item.Role == MessageRoleUser && item.RunSequence > MaxCommittedSteerInputs) ||
		(item.Role == MessageRoleAssistant && item.RunSequence < 1) ||
		!validSHA256Hex(item.ContentHash) || item.ContentBytes < 1 || item.ContentBytes > MaxModelInputMessageBytes {
		return ErrInvalidSessionContextSummary
	}
	return nil
}

// Validate checks the bounded durable derivative and its exact coverage tuple.
func (summary SessionContextSummary) Validate() error {
	if !summary.SessionID.Valid() || !summary.CoveredFirstID.Valid() || !summary.CoveredThroughID.Valid() ||
		!ValidModelText(summary.Text, MaxSessionSummaryBytes, false) ||
		summary.SummaryHash != SHA256Hex(summary.Text) ||
		summary.SchemaVersion != SessionContextSummarySchemaVersion ||
		summary.PolicyVersion != SafeConversationContextPolicyVersion ||
		summary.CoveredCount < 2 || summary.CoveredCount > MaxSessionContextMessages ||
		summary.CoveredBytes < 1 || summary.CoveredBytes > MaxSessionHistoryBytes ||
		!validSHA256Hex(summary.CoverageDigest) || !validSHA256Hex(summary.AgentOriginHash) ||
		!ValidModelToken(summary.AgentProfile, maxModelIdentifierBytes) ||
		!validPersistenceTime(summary.GeneratedAt) {
		return ErrInvalidSessionContextSummary
	}
	return nil
}

// SessionContextCoverageDigest binds an ordered eligible Message prefix without
// copying its content into generic persistence payloads.
func SessionContextCoverageDigest(messages []Message) (string, int, error) {
	if len(messages) == 0 || len(messages) > MaxSessionContextMessages {
		return "", 0, ErrInvalidSessionContextSummary
	}
	sessionID := messages[0].SessionID
	items := make([]SessionContextCoverageItem, len(messages))
	for _, message := range messages {
		if message.Validate() != nil || message.SessionID != sessionID || message.RunID == nil || message.RunSequence == nil ||
			message.Status != MessageStatusCommitted ||
			(message.Role != MessageRoleUser && message.Role != MessageRoleAssistant) {
			return "", 0, ErrInvalidSessionContextSummary
		}
	}
	for index, message := range messages {
		items[index] = SessionContextCoverageItem{
			MessageID: message.ID, RunID: *message.RunID, RunSequence: *message.RunSequence,
			Role: message.Role, ContentHash: message.Hash, ContentBytes: len(message.Content),
		}
	}
	if ValidateSessionContextCoverage(items) != nil {
		return "", 0, ErrInvalidSessionContextSummary
	}
	return SessionContextCoverageDigestItems(items)
}

// ValidateSessionContextCoverage verifies complete, contiguous completed-run
// groups: one or more user inputs followed by exactly one final assistant.
func ValidateSessionContextCoverage(items []SessionContextCoverageItem) error {
	if len(items) == 0 || len(items) > MaxSessionContextMessages {
		return ErrInvalidSessionContextSummary
	}
	seenRuns := make(map[AgentRunID]struct{})
	for index := 0; index < len(items); {
		first := items[index]
		if first.Validate() != nil || first.Role != MessageRoleUser || first.RunSequence != 0 {
			return ErrInvalidSessionContextSummary
		}
		if _, duplicate := seenRuns[first.RunID]; duplicate {
			return ErrInvalidSessionContextSummary
		}
		seenRuns[first.RunID] = struct{}{}
		runID := first.RunID
		sequence := 0
		for index < len(items) && items[index].RunID == runID && items[index].Role == MessageRoleUser {
			item := items[index]
			if item.Validate() != nil || item.RunSequence != sequence || sequence > MaxCommittedSteerInputs {
				return ErrInvalidSessionContextSummary
			}
			sequence++
			index++
		}
		if index >= len(items) || items[index].RunID != runID || items[index].Role != MessageRoleAssistant ||
			items[index].RunSequence != sequence || items[index].Validate() != nil {
			return ErrInvalidSessionContextSummary
		}
		index++
		if index < len(items) && items[index].RunID == runID {
			return ErrInvalidSessionContextSummary
		}
	}
	return nil
}

// SessionContextCoverageDigestItems hashes one ordered content-free coverage list.
func SessionContextCoverageDigestItems(items []SessionContextCoverageItem) (string, int, error) {
	if len(items) == 0 || len(items) > MaxSessionContextMessages {
		return "", 0, ErrInvalidSessionContextSummary
	}
	digest := sha256.New()
	writeCoverageUint64(digest, uint64(len(items)))
	totalBytes := 0
	for _, item := range items {
		if item.Validate() != nil {
			return "", 0, ErrInvalidSessionContextSummary
		}
		totalBytes += item.ContentBytes
		if totalBytes > MaxSessionHistoryBytes {
			return "", 0, ErrInvalidSessionContextSummary
		}
		writeCoverageString(digest, string(item.MessageID))
		writeCoverageString(digest, string(item.RunID))
		writeCoverageUint64(digest, uint64(item.RunSequence))
		writeCoverageString(digest, string(item.Role))
		writeCoverageString(digest, item.ContentHash)
		writeCoverageUint64(digest, uint64(item.ContentBytes))
	}
	return hex.EncodeToString(digest.Sum(nil)), totalBytes, nil
}

func writeCoverageUint64(target hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = target.Write(encoded[:])
}

func writeCoverageString(target hash.Hash, value string) {
	writeCoverageUint64(target, uint64(len(value)))
	_, _ = target.Write([]byte(value))
}
