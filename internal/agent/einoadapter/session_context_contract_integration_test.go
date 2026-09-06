//go:build integration

package einoadapter

import "testing"

// TestSessionContextEinoIntegrationContract runs the exact-message scripted
// model contract through the pinned Eino ADK boundary. It deliberately reuses
// the production adapter tests instead of introducing another memory loop.
func TestSessionContextEinoIntegrationContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "current question exactly once", run: TestAdapterPassesOrderedSessionContextAndCurrentQuestionExactlyOnce},
		{name: "committed summary replay is not repeated", run: TestAdapterReplaysVerifiedSummaryAndTailWithoutResummarizingCoveredPrefix},
		{name: "message threshold and recent tail", run: TestEinoSummarizationMessageThresholdAndRecentTail},
		{name: "byte threshold and one over", run: TestEinoSummarizationByteThreshold},
		{name: "summary failure makes zero main calls", run: TestEinoSummaryFailurePreventsMainModelRequest},
		{name: "summary cancellation and timeout", run: TestEinoSummaryHonorsCancellationAndReservedTimeout},
		{name: "stale summary is discarded", run: TestEinoSummaryDiscardsOutputAfterScopeBecomesStale},
		{name: "summary sink rejection makes zero main calls", run: TestEinoSummarySinkRejectionPreventsMainModelRequest},
		{name: "current-run Tool pair survives compaction", run: TestEinoSummarizationPreservesCurrentRunToolCallResultPair},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, current.run)
	}
}
