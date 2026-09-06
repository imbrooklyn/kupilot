package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrInvalidManualCompaction = errors.New("manual compaction data is invalid")

// ManualCompactionInput is one immutable Application-authorized summary
// operation. It contains no current question, Tool, or execution authority.
type ManualCompactionInput struct {
	id               domain.CompactionID
	sessionID        domain.SessionID
	conversation     ConversationContext
	scope            domain.ClusterScope
	policyGeneration domain.PolicyGeneration
	profile          string
	originHash       string
	budgetLimits     RunBudgetLimits
}

func NewManualCompactionInput(
	id domain.CompactionID,
	sessionID domain.SessionID,
	conversation ConversationContext,
	scope domain.ClusterScope,
	policyGeneration domain.PolicyGeneration,
	profile string,
	originHash string,
	budgetLimits RunBudgetLimits,
) (ManualCompactionInput, error) {
	input := ManualCompactionInput{
		id: id, sessionID: sessionID, conversation: conversation, scope: scope,
		policyGeneration: policyGeneration, profile: profile, originHash: originHash, budgetLimits: budgetLimits,
	}
	if input.Validate() != nil {
		return ManualCompactionInput{}, ErrInvalidManualCompaction
	}
	return input, nil
}

func (input ManualCompactionInput) Validate() error {
	if !input.id.Valid() || !input.sessionID.Valid() || input.scope.Validate() != nil ||
		!input.policyGeneration.Valid() || !domain.ValidModelToken(input.profile, 128) ||
		!validLowerSHA256Hex(input.originHash) ||
		input.budgetLimits.Validate() != nil {
		return ErrInvalidManualCompaction
	}
	if _, err := NewConversationContextWithCoverage(
		input.sessionID, input.conversation.turns, input.conversation.summary, input.conversation.coverage,
	); err != nil {
		return ErrInvalidManualCompaction
	}
	return nil
}

func validLowerSHA256Hex(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func (input ManualCompactionInput) ID() domain.CompactionID           { return input.id }
func (input ManualCompactionInput) SessionID() domain.SessionID       { return input.sessionID }
func (input ManualCompactionInput) Conversation() ConversationContext { return input.conversation }
func (input ManualCompactionInput) Scope() domain.ClusterScope        { return input.scope }
func (input ManualCompactionInput) PolicyGeneration() domain.PolicyGeneration {
	return input.policyGeneration
}
func (input ManualCompactionInput) Profile() string               { return input.profile }
func (input ManualCompactionInput) OriginHash() string            { return input.originHash }
func (input ManualCompactionInput) BudgetLimits() RunBudgetLimits { return input.budgetLimits }

// ManualCompactionEventKind identifies the two synchronous authorization and
// commit barriers around the sole summary model call.
type ManualCompactionEventKind string

const (
	ManualCompactionStarted ManualCompactionEventKind = "started"
	ManualCompactionReady   ManualCompactionEventKind = "ready"
)

type ManualCompactionEvent struct {
	ID               domain.CompactionID
	SessionID        domain.SessionID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Sequence         int
	Kind             ManualCompactionEventKind
	ModelRequestID   *domain.ModelRequestID
	Summary          *domain.SessionContextSummary
}

func (event ManualCompactionEvent) Validate() error {
	if !event.ID.Valid() || !event.SessionID.Valid() || event.ScopeGeneration < 1 ||
		!event.PolicyGeneration.Valid() || event.Sequence < 1 || event.Sequence > 2 {
		return ErrInvalidManualCompaction
	}
	switch event.Kind {
	case ManualCompactionStarted:
		if event.Sequence != 1 || event.ModelRequestID == nil || !event.ModelRequestID.Valid() || event.Summary != nil {
			return ErrInvalidManualCompaction
		}
	case ManualCompactionReady:
		if event.Sequence != 2 || event.ModelRequestID != nil || event.Summary == nil ||
			event.Summary.Validate() != nil || event.Summary.SessionID != event.SessionID {
			return ErrInvalidManualCompaction
		}
	default:
		return ErrInvalidManualCompaction
	}
	return nil
}

// ManualCompactionEventSink is the Application-owned authority and persistence
// barrier. Rejection prevents the corresponding transition.
type ManualCompactionEventSink interface {
	AcceptManualCompaction(context.Context, ManualCompactionEvent) EventSinkResult
}

type ManualCompactionResult struct {
	Noop    bool
	Summary *domain.SessionContextSummary
}

func (result ManualCompactionResult) Validate(input ManualCompactionInput) error {
	if input.Validate() != nil || result.Noop == (result.Summary != nil) {
		return ErrInvalidManualCompaction
	}
	if result.Summary != nil && (result.Summary.Validate() != nil || result.Summary.SessionID != input.SessionID() ||
		result.Summary.AgentProfile != input.Profile() || result.Summary.AgentOriginHash != input.OriginHash()) {
		return ErrInvalidManualCompaction
	}
	return nil
}

// ContextCompactor invokes the existing Eino summarization handler without
// starting another Agent, Runner, Tool loop, or ordinary model request.
type ContextCompactor interface {
	Compact(context.Context, ManualCompactionInput, ManualCompactionEventSink) (ManualCompactionResult, error)
}
