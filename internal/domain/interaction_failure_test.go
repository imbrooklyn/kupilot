package domain

import (
	"strings"
	"testing"
)

func TestInteractionFailuresAreClosedContentFreeBoundaryValues(t *testing.T) {
	groups := []struct {
		stage   InteractionStage
		reasons []InteractionFailure
	}{
		{"request_preflight", []InteractionFailure{FailureRequestPreflight}},
		{"retained_context", []InteractionFailure{FailureRetainedContext, FailureSummaryResponse}},
		{"model_invocation", []InteractionFailure{FailureProviderTransport, FailureProviderProtocol, FailureProviderReported, FailureProviderRequest}},
		{"stream_assembly", []InteractionFailure{FailureStreamMalformed, FailureStreamUnsupported, FailureStopReason, FailureStreamDuplicate, FailureStreamAfterFinish, FailureStreamIncomplete, FailureStreamUsage}},
		{"tool_selection", []InteractionFailure{FailureToolSelection, FailureToolPolicy, FailureToolPairing}},
		{"tool_execution", []InteractionFailure{FailureToolResult, FailureToolCancelled, FailureToolTimeout}},
		{"evidence_acceptance", []InteractionFailure{FailureEvidenceAcceptance, FailureRegistryChanged}},
		{"final_decode", []InteractionFailure{FailureFinalJSON, FailureFinalDuplicateField, FailureFinalUnknownField, FailureFinalMissingField, FailureFinalNullField, FailureFinalSchema, FailureFinalShape, FailureFinalLimit, FailureClarification, FailurePlan}},
		{"claim_binding", []InteractionFailure{FailureClaimKind, FailureClaimDuplicate, FailureClaimUnsupported, FailureEvidenceUnknown, FailureEvidenceDuplicate, FailureEvidenceOwnership, FailureClaimBinding}},
		{"run_guard", []InteractionFailure{FailureSensitiveOutput, FailureBudget, FailureCancelled, FailureTimeout, FailureStaleGeneration}},
		{"persistence", []InteractionFailure{FailurePersistence}},
		{"event_acceptance", []InteractionFailure{FailureEventAcceptance}},
		{"internal", []InteractionFailure{FailureInternal}},
	}
	seen := make(map[InteractionFailure]bool)
	for _, group := range groups {
		for _, reason := range group.reasons {
			if seen[reason] || !reason.Valid() || reason.Stage() != group.stage {
				t.Fatalf("invalid or overlapping boundary: %s", reason)
			}
			seen[reason] = true
			message := reason.SafeMessage()
			if len(message) > 160 || !strings.Contains(message, string(group.stage)) || !strings.Contains(message, string(reason)) || strings.ContainsAny(message, "\n\r\x1b") {
				t.Fatalf("unsafe fixed projection: %q", message)
			}
		}
	}
	for _, reason := range []InteractionFailure{"", "provider supplied text", "https://synthetic.invalid/private?token=canary", "\x1b[2J"} {
		if reason.Valid() || reason.Stage() != "" || reason.SafeMessage() != "The diagnostic runtime failed safely." {
			t.Fatal("unknown reason escaped into diagnostics")
		}
	}
	if !strings.Contains(FailureEvidenceUnknown.SafeMessage(), "The model cited Evidence not accepted in this run") {
		t.Fatal("unknown reference failure does not explain the model citation error")
	}
}
