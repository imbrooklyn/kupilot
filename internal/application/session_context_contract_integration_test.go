//go:build integration

package application

import "testing"

// TestSessionContextApplicationIntegrationContract groups the deterministic
// Application seams that a tagged integration run must exercise. The cases
// remain ordinary tests as well, so the integration target cannot replace the
// default CI authority or create a second Session implementation.
func TestSessionContextApplicationIntegrationContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "two turn completed history", run: TestCoordinatorCarriesOnlyCompletedTurnsIntoLaterAgentRuns},
		{name: "history beyond one page", run: TestCoordinatorLoadsEveryEligiblePageForFreshStandardContext},
		{name: "committed summary and exact recent tail", run: TestCoordinatorLoadsStoredSummaryAndExactTailAndRejectsCorruptCoverage},
		{name: "history read failure makes zero Agent calls", run: TestCoordinatorModelContextReadFailurePreventsAgentCall},
		{name: "minimal process-only context", run: TestMinimalSessionContextStaysInProcessAndOutOfPersistence},
		{name: "summary persistence failure makes zero main calls", run: TestCoordinatorSummaryStorageFailureRejectsBeforeMainModelWork},
		{name: "stale summary is rejected", run: TestCoordinatorRejectsSummaryReturnedAfterScopeGenerationChanged},
		{name: "resume acceptance is local", run: TestCoordinatorSessionPickerAndResumeStayReadOnlyUntilAcceptance},
		{name: "failed resume never falls back", run: TestCoordinatorResumeFailuresRemainDistinctWithoutFallback},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, current.run)
	}
}
