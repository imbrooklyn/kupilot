package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	MaxReviewerUserIntentBytes       = 8 * 1024
	MaxReviewerActionProjectionBytes = 24 * 1024
	MaxReviewerPolicyFactsBytes      = 8 * 1024
	MaxReviewerRationaleBytes        = 2 * 1024
	MaxReviewerResponseBytes         = 8 * 1024
)

var ErrInvalidReviewerData = errors.New("reviewer data is invalid")

// ReviewerRequest is the smallest transport projection. It carries no
// approval token, executable authority, generation mutation, or executor
// handle. Permission policy owns construction of the normalized action
// projection before this transport may be called.
type ReviewerRequest struct {
	RequestID        domain.ModelRequestID
	UserIntent       string
	NormalizedAction string
	PolicyFacts      string
}

func (request ReviewerRequest) Validate() error {
	if !request.RequestID.Valid() ||
		!domain.ValidModelText(request.UserIntent, MaxReviewerUserIntentBytes, false) ||
		!domain.ValidModelText(request.NormalizedAction, MaxReviewerActionProjectionBytes, false) ||
		!domain.ValidModelText(request.PolicyFacts, MaxReviewerPolicyFactsBytes, false) {
		return ErrInvalidReviewerData
	}
	return nil
}

// ReviewerDecision is one strict, non-authoritative model recommendation.
type ReviewerDecision string

const (
	ReviewerDecisionApprove        ReviewerDecision = "approve"
	ReviewerDecisionDeny           ReviewerDecision = "deny"
	ReviewerDecisionEscalateToUser ReviewerDecision = "escalate_to_user"
)

func (decision ReviewerDecision) Valid() bool {
	return decision == ReviewerDecisionApprove || decision == ReviewerDecisionDeny ||
		decision == ReviewerDecisionEscalateToUser
}

// ReviewerResult is untrusted model output. Application policy must never
// treat this value alone as execution authority.
type ReviewerResult struct {
	Decision  ReviewerDecision
	Risk      domain.RiskClass
	Rationale string
}

func (result ReviewerResult) Validate() error {
	if !result.Decision.Valid() || !result.Risk.Valid() || result.Risk == domain.RiskDeny ||
		!domain.ValidModelText(result.Rationale, MaxReviewerRationaleBytes, false) {
		return ErrInvalidReviewerData
	}
	return nil
}

// ReviewerBudgetLimits is independent from Agent and Agent-summary budgets.
type ReviewerBudgetLimits struct {
	Profile        BudgetProfile
	Calls          int
	RequestBytes   int
	OutputBytes    int
	CostUnits      int
	RequestTimeout time.Duration
}

func ReviewerBudgetLimitsForProfile(profile BudgetProfile) (ReviewerBudgetLimits, error) {
	limits := ReviewerBudgetLimits{
		Profile: profile, RequestBytes: 48 * 1024, OutputBytes: MaxReviewerResponseBytes,
	}
	switch profile {
	case BudgetProfileCompact:
		limits.Calls, limits.CostUnits, limits.RequestTimeout = 2, 2, 15*time.Second
	case BudgetProfileBalanced:
		limits.Calls, limits.CostUnits, limits.RequestTimeout = 8, 8, 30*time.Second
	case BudgetProfileExtended:
		limits.Calls, limits.CostUnits, limits.RequestTimeout = 16, 16, 60*time.Second
	default:
		return ReviewerBudgetLimits{}, ErrInvalidRunBudget
	}
	return limits, nil
}

func (limits ReviewerBudgetLimits) Validate() error {
	maximum, err := ReviewerBudgetLimitsForProfile(limits.Profile)
	if err != nil || limits.Calls < 1 || limits.Calls > maximum.Calls ||
		limits.RequestBytes < 1 || limits.RequestBytes > maximum.RequestBytes ||
		limits.OutputBytes < 1 || limits.OutputBytes > maximum.OutputBytes ||
		limits.CostUnits < 1 || limits.CostUnits > maximum.CostUnits ||
		limits.RequestTimeout <= 0 || limits.RequestTimeout > maximum.RequestTimeout {
		return ErrInvalidRunBudget
	}
	return nil
}

// ReviewerBudget is a small independent atomic reservation counter. It owns
// no Context and performs no I/O.
type ReviewerBudget struct {
	mu        sync.Mutex
	limits    ReviewerBudgetLimits
	calls     int
	costUnits int
}

// ReviewerBudgetSnapshot contains counters only.
type ReviewerBudgetSnapshot struct {
	Limits    ReviewerBudgetLimits
	Calls     int
	CostUnits int
}

func NewReviewerBudget(limits ReviewerBudgetLimits) (*ReviewerBudget, error) {
	if limits.Validate() != nil {
		return nil, ErrInvalidRunBudget
	}
	return &ReviewerBudget{limits: limits}, nil
}

func (budget *ReviewerBudget) Reserve(ctx context.Context) (CallReservation, error) {
	if budget == nil || ctx == nil {
		return CallReservation{}, newRunBudgetError(RunStopInvalidState)
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return CallReservation{}, newRunBudgetError(RunStopTimedOut)
		}
		return CallReservation{}, newRunBudgetError(RunStopCancelled)
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.calls >= budget.limits.Calls {
		return CallReservation{}, newRunBudgetError(RunStopReviewerCallLimit)
	}
	if budget.costUnits >= budget.limits.CostUnits {
		return CallReservation{}, newRunBudgetError(RunStopReviewerCostLimit)
	}
	budget.calls++
	budget.costUnits++
	return CallReservation{
		Timeout: budget.limits.RequestTimeout, RequestBytes: budget.limits.RequestBytes,
		OutputBytes: budget.limits.OutputBytes, CostUnits: 1,
	}, nil
}

func (budget *ReviewerBudget) Snapshot() ReviewerBudgetSnapshot {
	if budget == nil {
		return ReviewerBudgetSnapshot{}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return ReviewerBudgetSnapshot{Limits: budget.limits, Calls: budget.calls, CostUnits: budget.costUnits}
}
