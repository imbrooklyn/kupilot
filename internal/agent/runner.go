// Package agent owns project-defined single-Agent policy, runtime contracts,
// and consumer-owned ports.
package agent

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	// ErrInvalidRunInput reports an invalid immutable Agent input without
	// echoing user, scope, or resource data.
	ErrInvalidRunInput = errors.New("Agent RunInput data is invalid")
	// ErrInvalidRunOutcome reports an invalid or nonterminal runner outcome.
	ErrInvalidRunOutcome = errors.New("Agent RunOutcome data is invalid")
)

// RunInput is the immutable project-owned input for one AgentRun. Its fields
// are private so collection and pointer aliases cannot mutate active authority.
type RunInput struct {
	runID            domain.AgentRunID
	sessionID        domain.SessionID
	requestMessageID domain.MessageID
	question         string
	scope            domain.ClusterScope
	resource         *domain.ResourceRef
	budgetLimits     RunBudgetLimits
	promptVersion    string
	catalogVersion   string
	conversation     ConversationContext
}

// ConversationTurn is the minimal safe prior-turn projection supplied to
// Eino. Historic scope, resources, Evidence, Tools, and authority are absent.
type ConversationTurn struct {
	MessageID   domain.MessageID
	Role        domain.MessageRole
	Content     string
	ContentHash string
}

func (turn ConversationTurn) validate() error {
	if !turn.MessageID.Valid() || (turn.Role != domain.MessageRoleUser && turn.Role != domain.MessageRoleAssistant) ||
		!domain.ValidModelText(turn.Content, domain.MaxModelInputMessageBytes, false) ||
		turn.ContentHash != domain.MessageContentHash(turn.Content) {
		return ErrInvalidRunInput
	}
	return nil
}

// ConversationContext is one bounded representation of all retained eligible
// prior turns, optionally preceded by one verified durable summary.
type ConversationContext struct {
	turns    []ConversationTurn
	summary  *domain.SessionContextSummary
	coverage []domain.SessionContextCoverageItem
}

// NewConversationContext validates and defensively copies retained context.
func NewConversationContext(sessionID domain.SessionID, turns []ConversationTurn, summary *domain.SessionContextSummary) (ConversationContext, error) {
	coverage := make([]domain.SessionContextCoverageItem, len(turns))
	for index, turn := range turns {
		coverage[index] = domain.SessionContextCoverageItem{
			MessageID: turn.MessageID, Role: turn.Role, ContentHash: turn.ContentHash, ContentBytes: len(turn.Content),
		}
	}
	return NewConversationContextWithCoverage(sessionID, turns, summary, coverage)
}

// NewConversationContextWithCoverage retains content-free metadata for the
// summarized prefix so later compaction can advance without replaying content.
func NewConversationContextWithCoverage(sessionID domain.SessionID, turns []ConversationTurn, summary *domain.SessionContextSummary, coverage []domain.SessionContextCoverageItem) (ConversationContext, error) {
	if !sessionID.Valid() || len(turns) > domain.MaxSessionContextMessages {
		return ConversationContext{}, ErrInvalidRunInput
	}
	copyContext := ConversationContext{
		turns:    append([]ConversationTurn(nil), turns...),
		coverage: append([]domain.SessionContextCoverageItem(nil), coverage...),
	}
	totalBytes := 0
	for index, turn := range copyContext.turns {
		if turn.validate() != nil {
			return ConversationContext{}, ErrInvalidRunInput
		}
		wantRole := domain.MessageRoleUser
		if index%2 == 1 {
			wantRole = domain.MessageRoleAssistant
		}
		if turn.Role != wantRole {
			return ConversationContext{}, ErrInvalidRunInput
		}
		totalBytes += len(turn.Content)
		if totalBytes > domain.MaxSessionHistoryBytes {
			return ConversationContext{}, ErrInvalidRunInput
		}
	}
	if summary != nil {
		if summary.Validate() != nil || summary.SessionID != sessionID {
			return ConversationContext{}, ErrInvalidRunInput
		}
		value := *summary
		copyContext.summary = &value
	}
	covered := 0
	if copyContext.summary != nil {
		covered = copyContext.summary.CoveredCount
	}
	if covered%2 != 0 || len(copyContext.turns)%2 != 0 || len(copyContext.coverage) != covered+len(copyContext.turns) ||
		len(copyContext.coverage) > domain.MaxSessionContextMessages {
		return ConversationContext{}, ErrInvalidRunInput
	}
	for index, item := range copyContext.coverage {
		if item.Validate() != nil {
			return ConversationContext{}, ErrInvalidRunInput
		}
		wantRole := domain.MessageRoleUser
		if index%2 == 1 {
			wantRole = domain.MessageRoleAssistant
		}
		if item.Role != wantRole {
			return ConversationContext{}, ErrInvalidRunInput
		}
		if index >= covered {
			turn := copyContext.turns[index-covered]
			if item.MessageID != turn.MessageID || item.Role != turn.Role || item.ContentHash != turn.ContentHash ||
				item.ContentBytes != len(turn.Content) {
				return ConversationContext{}, ErrInvalidRunInput
			}
		}
	}
	if copyContext.summary != nil {
		prefix := copyContext.coverage[:covered]
		digest, bytes, err := domain.SessionContextCoverageDigestItems(prefix)
		if err != nil || digest != copyContext.summary.CoverageDigest || bytes != copyContext.summary.CoveredBytes ||
			prefix[0].MessageID != copyContext.summary.CoveredFirstID || prefix[len(prefix)-1].MessageID != copyContext.summary.CoveredThroughID {
			return ConversationContext{}, ErrInvalidRunInput
		}
		totalBytes += bytes
	}
	if totalBytes > domain.MaxSessionHistoryBytes {
		return ConversationContext{}, ErrInvalidRunInput
	}
	return copyContext, nil
}

// Turns returns a defensive ordered copy.
func (context ConversationContext) Turns() []ConversationTurn {
	return append([]ConversationTurn(nil), context.turns...)
}

// Summary returns a defensive copy of the optional verified derivative.
func (context ConversationContext) Summary() *domain.SessionContextSummary {
	if context.summary == nil {
		return nil
	}
	value := *context.summary
	return &value
}

// Coverage returns content-free metadata for the full retained eligible order.
func (context ConversationContext) Coverage() []domain.SessionContextCoverageItem {
	return append([]domain.SessionContextCoverageItem(nil), context.coverage...)
}

// NewRunInput validates and defensively copies one frozen run policy snapshot.
// The question must already have passed the Application-owned egress pipeline.
func NewRunInput(
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	requestMessageID domain.MessageID,
	question string,
	scope domain.ClusterScope,
	resource *domain.ResourceRef,
	budgetLimits RunBudgetLimits,
) (RunInput, error) {
	conversation, err := NewConversationContext(sessionID, nil, nil)
	if err != nil {
		return RunInput{}, err
	}
	return NewRunInputWithContext(runID, sessionID, requestMessageID, question, scope, resource, budgetLimits, conversation)
}

// NewRunInputWithContext freezes one already-selected Session representation.
func NewRunInputWithContext(
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	requestMessageID domain.MessageID,
	question string,
	scope domain.ClusterScope,
	resource *domain.ResourceRef,
	budgetLimits RunBudgetLimits,
	conversation ConversationContext,
) (RunInput, error) {
	input := RunInput{
		runID:            runID,
		sessionID:        sessionID,
		requestMessageID: requestMessageID,
		question:         question,
		scope:            scope,
		budgetLimits:     budgetLimits,
		promptVersion:    SystemPromptVersion,
		catalogVersion:   ToolCatalogVersion,
		conversation:     conversation,
	}
	if resource != nil {
		copied := *resource
		input.resource = &copied
	}
	if err := input.Validate(); err != nil {
		return RunInput{}, err
	}
	return input, nil
}

// Validate checks the complete frozen input without consulting live state.
func (input RunInput) Validate() error {
	if !input.runID.Valid() || !input.sessionID.Valid() || !input.requestMessageID.Valid() ||
		input.scope.Validate() != nil || !domain.ValidModelText(input.question, domain.MaxModelInputMessageBytes, false) ||
		input.budgetLimits.Validate() != nil ||
		input.promptVersion != SystemPromptVersion || input.catalogVersion != ToolCatalogVersion {
		return ErrInvalidRunInput
	}
	if _, err := NewConversationContextWithCoverage(input.sessionID, input.conversation.turns, input.conversation.summary, input.conversation.coverage); err != nil {
		return ErrInvalidRunInput
	}
	if input.resource != nil &&
		(domain.ValidateLiveResourceRef(*input.resource) != nil || !domain.ReferenceMatchesWorkingNamespace(*input.resource, input.scope.Namespace)) {
		return ErrInvalidRunInput
	}
	return nil
}

// RunID returns the frozen AgentRun identity.
func (input RunInput) RunID() domain.AgentRunID { return input.runID }

// SessionID returns the frozen Session identity.
func (input RunInput) SessionID() domain.SessionID { return input.sessionID }

// RequestMessageID returns the user Message that created the run.
func (input RunInput) RequestMessageID() domain.MessageID { return input.requestMessageID }

// Question returns the already-safe current user question.
func (input RunInput) Question() string { return input.question }

// Scope returns the immutable live scope value copied for this run.
func (input RunInput) Scope() domain.ClusterScope { return input.scope }

// Resource returns a defensive copy of the optional selected ResourceRef.
func (input RunInput) Resource() *domain.ResourceRef {
	if input.resource == nil {
		return nil
	}
	resource := *input.resource
	return &resource
}

// BudgetLimits returns the frozen, non-expanding runtime limits.
func (input RunInput) BudgetLimits() RunBudgetLimits { return input.budgetLimits }

// PromptVersion returns the code-defined System Prompt version.
func (input RunInput) PromptVersion() string { return input.promptVersion }

// CatalogVersion returns the code-defined fixed Tool catalog version.
func (input RunInput) CatalogVersion() string { return input.catalogVersion }

// Conversation returns a defensive copy of prior untrusted Session context.
func (input RunInput) Conversation() ConversationContext {
	context, _ := NewConversationContextWithCoverage(input.sessionID, input.conversation.turns, input.conversation.summary, input.conversation.coverage)
	return context
}

// AgentRunner is the active consumer-owned single-run port. Implementations
// block until exactly one terminal outcome, honor ctx, and publish only
// project-owned ordered events. Production composition binds einoadapter.
type AgentRunner interface {
	Run(context.Context, RunInput, EventSink) RunOutcome
}

// RunScopeGuard is the Application-supplied freshness gate for the immutable
// ClusterScope. Implementations return false without performing model or Tool
// I/O when the exact scope generation is no longer current.
type RunScopeGuard interface {
	Current(context.Context, domain.ClusterScope) bool
}

// RunIdentifierSource supplies Application-generated durable UUIDv7 values to
// one AgentRun. The runner validates every value before it becomes metadata.
type RunIdentifierSource interface {
	NewModelRequestID() (domain.ModelRequestID, error)
	NewToolInvocationID() (domain.ToolInvocationID, error)
	NewDiagnosisID() (domain.DiagnosisID, error)
}

// RunOutcome is the sole synchronous terminal value returned by AgentRunner.
// Raw adapter and framework errors are intentionally absent.
type RunOutcome struct {
	Status      domain.AgentRunStatus
	Diagnosis   *domain.Diagnosis
	ErrorClass  *domain.SafeErrorClass
	SafeMessage string
}

// Validate checks terminal uniqueness and binds a successful Diagnosis to the
// immutable run and scope.
func (outcome RunOutcome) Validate(input RunInput) error {
	if input.Validate() != nil || !outcome.Status.Terminal() {
		return ErrInvalidRunOutcome
	}
	if outcome.Status == domain.AgentRunStatusCompleted {
		if outcome.Diagnosis == nil || outcome.ErrorClass != nil || outcome.SafeMessage != "" ||
			outcome.Diagnosis.Validate() != nil || outcome.Diagnosis.RunID != input.RunID() ||
			outcome.Diagnosis.Scope != input.Scope().Snapshot() {
			return ErrInvalidRunOutcome
		}
		return nil
	}
	if outcome.Diagnosis != nil || outcome.ErrorClass == nil || !outcome.ErrorClass.Valid() ||
		!domain.ValidModelText(outcome.SafeMessage, domain.MaxModelInputMessageBytes, false) {
		return ErrInvalidRunOutcome
	}
	switch outcome.Status {
	case domain.AgentRunStatusCancelled:
		if *outcome.ErrorClass != domain.SafeErrorClassCancelled {
			return ErrInvalidRunOutcome
		}
	case domain.AgentRunStatusTimedOut:
		if *outcome.ErrorClass != domain.SafeErrorClassTimeout {
			return ErrInvalidRunOutcome
		}
	case domain.AgentRunStatusStaleScope:
		if *outcome.ErrorClass != domain.SafeErrorClassStaleScope {
			return ErrInvalidRunOutcome
		}
	case domain.AgentRunStatusFailed, domain.AgentRunStatusInterrupted:
	default:
		return ErrInvalidRunOutcome
	}
	return nil
}
