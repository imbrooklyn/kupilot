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

// RunInput is the immutable, Eino-neutral input for one AgentRun. Its fields
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
	input := RunInput{
		runID:            runID,
		sessionID:        sessionID,
		requestMessageID: requestMessageID,
		question:         question,
		scope:            scope,
		budgetLimits:     budgetLimits,
		promptVersion:    SystemPromptVersion,
		catalogVersion:   ToolCatalogVersion,
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
	question := domain.ModelMessage{Role: domain.ModelMessageRoleUser, Content: input.question}
	if !input.runID.Valid() || !input.sessionID.Valid() || !input.requestMessageID.Valid() ||
		input.scope.Validate() != nil || question.Validate() != nil || input.budgetLimits.Validate() != nil ||
		input.promptVersion != SystemPromptVersion || input.catalogVersion != ToolCatalogVersion {
		return ErrInvalidRunInput
	}
	if input.resource != nil &&
		(domain.ValidateLiveResourceRef(*input.resource) != nil || input.resource.Namespace != input.scope.Namespace) {
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

// AgentRunner is the Eino-neutral single-run facade. Implementations block
// until exactly one terminal outcome, honor ctx, and publish only neutral
// ordered events. The production implementation belongs in einoadapter.
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
		(domain.ModelMessage{Role: domain.ModelMessageRoleSystem, Content: outcome.SafeMessage}).Validate() != nil {
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
