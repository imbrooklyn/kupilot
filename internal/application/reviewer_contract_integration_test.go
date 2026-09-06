//go:build integration

package application

import "testing"

// TestReviewerApplicationIntegrationContract keeps role consent, absence,
// strict decisions, timeout/error, budget, and late-result invalidation under
// deterministic Application authority. The live eval never executes actions.
func TestReviewerApplicationIntegrationContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "strict recommendation matrix", run: TestApprovalReviewerRecommendationMatrixIsFailClosed},
		{name: "absent or unconsented Reviewer makes zero calls", run: TestApprovalReviewerMissingConsentAndMissingBindingMakeZeroModelCalls},
		{name: "independent role consent", run: TestPrivacyConsentIsIndependentForEachModelRoleAtTheSameOrigin},
		{name: "one-over budget makes zero extra calls", run: TestApprovalReviewerBudgetOneOverEscalatesWithoutAnotherModelOrExecutorCall},
		{name: "persistence error authorizes nothing", run: TestApprovalReviewerPersistenceFailureInvalidatesWithoutDecisionOrExecution},
		{name: "late result after policy change is rejected", run: TestPolicyGenerationChangeCancelsReviewerAndRejectsLateResult},
		{name: "local hard policy precedes Reviewer", run: TestLocalPermissionDenialPrecedesTargetPreparationReviewerAndPersistence},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, current.run)
	}
}
