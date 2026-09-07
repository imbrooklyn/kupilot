package application

import "testing"

func TestRecoveryMatrixIsCompleteDistinctAndNeverRetriesAutomatically(t *testing.T) {
	sources := []RecoveryFailureSource{
		RecoveryModelTransport, RecoverySummary, RecoverySQLitePreCommit, RecoverySQLitePostCommit,
		RecoveryToolSource, RecoveryApprovalReviewer, RecoveryNotificationTitle, RecoveryClipboard,
		RecoveryTranscriptSearch, RecoveryEndpointContinuation, RecoveryTerminalShutdown,
	}
	seen := make(map[RecoveryFailureSource]struct{}, len(sources))
	for _, source := range sources {
		policy, err := RecoveryPolicyFor(source)
		if err != nil || policy.validate() != nil {
			t.Fatalf("RecoveryPolicyFor(%q) = %#v, %v", source, policy, err)
		}
		if _, duplicate := seen[source]; duplicate {
			t.Fatalf("duplicate source %q", source)
		}
		seen[source] = struct{}{}
		if policy.QueueAutoDrain || policy.MaximumAutomaticExternalCalls != 0 ||
			policy.ModelRetry != RecoveryRetryDenied && policy.ModelRetry != RecoveryRetryNotApplicable ||
			policy.ToolOrActionRetry != RecoveryRetryDenied && policy.ToolOrActionRetry != RecoveryRetryNotApplicable {
			t.Fatalf("policy admits automatic work: %#v", policy)
		}
	}
	if _, err := RecoveryPolicyFor("unknown"); err == nil {
		t.Fatal("unknown recovery source was accepted")
	}
}

func TestOptionalDeliveryFailuresDoNotBecomeAgentFailures(t *testing.T) {
	for _, source := range []RecoveryFailureSource{
		RecoveryNotificationTitle, RecoveryClipboard, RecoveryTranscriptSearch, RecoveryApprovalReviewer,
	} {
		policy, err := RecoveryPolicyFor(source)
		if err != nil || policy.AgentRunAffected || policy.TerminalReason != nil || policy.Persistence != RecoveryPersistencePreserved {
			t.Fatalf("optional delivery policy %q = %#v, %v", source, policy, err)
		}
	}
}
