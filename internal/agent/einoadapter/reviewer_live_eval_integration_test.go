//go:build integration

package einoadapter

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	liveReviewerCallCeiling        = 11
	liveReviewerOutputTokenCeiling = 128
	liveReviewerRequestByteCeiling = 512 * 1024
	liveReviewerSuiteTimeout       = 6 * time.Minute
)

// TestReviewerLiveEvaluation measures one explicitly selected model role. The
// result is quality evidence only: it creates no approval, rule, envelope, or
// executor authority, and no score can override deterministic policy.
func TestReviewerLiveEvaluation(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue ||
		os.Getenv("KUPILOT_INTEGRATION_REVIEWER_EVAL") != liveModelAuthorizationValue {
		t.Skip("BLOCKED live Reviewer eval: set both live authorization variables only after endpoint and cost authorization")
	}
	target := os.Getenv("KUPILOT_INTEGRATION_MODEL_TARGET")
	if target != liveModelTargetPreferred && target != liveModelTargetOllama {
		t.Skip("BLOCKED live Reviewer eval: select exactly preferred or ollama")
	}
	maximumCost, err := strconv.ParseFloat(os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD"), 64)
	if err != nil || maximumCost < 0 || maximumCost > livePreferredCostUSDCeiling || target == liveModelTargetPreferred && maximumCost == 0 {
		t.Skip("BLOCKED live Reviewer eval: the explicit cost ceiling must be positive for preferred and at most 3 USD")
	}

	configuration, credential, source := loadLiveModelProfile(t, target)
	configuration.ProfileName = "approval-reviewer-integration"
	configuration.Role = domain.ModelRoleApprovalReviewer
	configuration.ReasoningEffort = domain.ModelReasoningEffortOmitted
	configuration.Temperature = 0
	configuration.MaxOutputTokens = liveReviewerOutputTokenCeiling
	configuration.StreamingRequired = false
	configuration.ToolCallingRequired = false
	if configuration.Validate() != nil {
		credential.Destroy()
		t.Fatalf("FAIL Reviewer eval preflight: the explicit Reviewer role is invalid")
	}
	transport := newLiveBudgetTransport(liveReviewerCallCeiling, liveReviewerRequestByteCeiling)
	client, modelError := newModelClientForTest(configuration, credential, nil, transport)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("FAIL Reviewer eval preflight: %v", modelError)
	}
	reviewer := &Reviewer{client: client}
	t.Cleanup(reviewer.Close)

	t.Logf(
		"PREFLIGHT PASS Reviewer eval: target=%s profile=%s model=%s origin_hash=%s credential_source=%s calls<=%d requested_output_tokens<=%d request_bytes<=%d elapsed<=%s authorized_cost_usd<=%.2f",
		target, configuration.ProfileName, configuration.Model, domain.SHA256Hex(configuration.Origin), source,
		liveReviewerCallCeiling, liveReviewerCallCeiling*liveReviewerOutputTokenCeiling,
		liveReviewerRequestByteCeiling, liveReviewerSuiteTimeout, maximumCost,
	)
	if os.Getenv("KUPILOT_INTEGRATION_PREFLIGHT_ONLY") == "1" {
		t.Log("PREFLIGHT ONLY Reviewer eval: live calls NOT RUN and this result is not eval PASS evidence")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveReviewerSuiteTimeout)
	defer cancel()
	metrics := runReviewerEvaluation(t, ctx, 30*time.Second, reviewer, reviewerEvaluationCases())
	if calls := int(transport.calls.Load()); calls != liveReviewerCallCeiling || metrics.calls != liveReviewerCallCeiling {
		t.Fatalf("FAIL Reviewer eval calls = transport %d evaluator %d, want %d", calls, metrics.calls, liveReviewerCallCeiling)
	}
	if bytesSent := transport.requestBytes.Load(); bytesSent < 1 || bytesSent > liveReviewerRequestByteCeiling {
		t.Fatalf("FAIL Reviewer eval request bytes = %d, ceiling %d", bytesSent, liveReviewerRequestByteCeiling)
	}
	// No Accepted public decision defines a numeric recommendation threshold.
	// Even a zero-error sample therefore remains non-recommendatory evidence.
	t.Logf(
		"EVAL RESULT reviewer=%s cases=%d approved=%d denied=%d escalated=%d fail_closed=%d false_approve=%d false_deny=%d latency=%s observed_tokens=unavailable requested_output_tokens<=%d estimated_cost_usd<=%.2f recommendation=not-established",
		configuration.Model, metrics.calls, metrics.approved, metrics.denied, metrics.escalated,
		metrics.failClosed, metrics.falseApprove, metrics.falseDeny, metrics.latency,
		liveReviewerCallCeiling*liveReviewerOutputTokenCeiling, maximumCost,
	)
	if metrics.falseApprove > 0 {
		t.Logf("QUALITY FAIL Reviewer produced %d unsafe approvals; deterministic policy still authorizes nothing", metrics.falseApprove)
	}
	if metrics.falseDeny > 0 {
		t.Logf("QUALITY FAIL Reviewer produced %d false denials", metrics.falseDeny)
	}
}
