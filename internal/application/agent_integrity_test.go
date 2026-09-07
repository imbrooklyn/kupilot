package application

import (
	"reflect"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestEveryRunTerminalReasonHasOneFixedSafeNextActionProjection(t *testing.T) {
	reasons := []domain.RunTerminalReason{
		domain.RunTerminalCompleted,
		domain.RunTerminalCancelled,
		domain.RunTerminalTimedOut,
		domain.RunTerminalFailed,
		domain.RunTerminalUnknown,
		domain.RunTerminalRecovered,
		domain.RunTerminalPersistenceDegraded,
		domain.RunTerminalStaleGeneration,
		domain.RunTerminalInsufficientEvidence,
		domain.RunTerminalSourceUnavailable,
		domain.RunTerminalPolicyDenied,
		domain.RunTerminalBudgetExhausted,
		domain.RunTerminalConflictingEvidence,
		domain.RunTerminalPartialResult,
		domain.RunTerminalNeedsUserInput,
	}
	seen := make(map[domain.RunTerminalReason]struct{}, len(reasons))
	for _, reason := range reasons {
		if _, duplicate := seen[reason]; duplicate {
			t.Fatalf("duplicate terminal reason %q", reason)
		}
		seen[reason] = struct{}{}
		first, err := ProjectTerminalOutcome(reason)
		if err != nil || !first.valid() || len(first.NextActions) == 0 || len(first.NextActions) > 3 ||
			len(first.Budget) != 17 {
			t.Fatalf("ProjectTerminalOutcome(%q) = %#v, %v", reason, first, err)
		}
		for _, measure := range first.Budget {
			if measure.Basis != UIBudgetUnavailable || measure.Used != 0 || measure.Limit != 0 {
				t.Fatalf("unbound terminal budget invented evidence for %q: %#v", reason, first.Budget)
			}
		}
		second, err := ProjectTerminalOutcome(reason)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("terminal projection is not deterministic for %q: %#v / %#v / %v", reason, first, second, err)
		}
	}
	if _, err := ProjectTerminalOutcome("model_selected_retry"); err == nil {
		t.Fatal("unknown model-selected terminal reason was accepted")
	}
}

func TestAnswerTerminalReasonPriorityIsTypedAndOrderIndependent(t *testing.T) {
	sources := []domain.AnswerSourceCoverage{
		{
			Sequence: 1, SourceHash: domain.SHA256Hex("partial-source"), SubjectHash: domain.SHA256Hex("partial-subject"),
			State: domain.SourcePartial, Freshness: domain.EvidenceFreshnessUnknown, Conflict: domain.EvidenceConflictNone,
		},
		{
			Sequence: 2, SourceHash: domain.SHA256Hex("conflict-source"), SubjectHash: domain.SHA256Hex("conflict-subject"),
			State: domain.SourceConflicting, Freshness: domain.EvidenceFreshnessUnknown, Conflict: domain.EvidenceConflictDetected,
		},
	}
	reason, err := domain.DeriveAnswerTerminalReason(nil, nil, sources, false)
	if err != nil || reason != domain.RunTerminalConflictingEvidence {
		t.Fatalf("mixed terminal reason = %q, %v", reason, err)
	}
	sources[0], sources[1] = sources[1], sources[0]
	sources[0].Sequence, sources[1].Sequence = 1, 2
	reordered, err := domain.DeriveAnswerTerminalReason(nil, nil, sources, false)
	if err != nil || reordered != reason {
		t.Fatalf("reordered terminal reason = %q, %v; want %q", reordered, err, reason)
	}
}

func TestUnsupportedCoverageIsInsufficientEvidenceNotPolicyOrBudgetAuthority(t *testing.T) {
	limitations := []domain.MissingInformation{{
		Kind: domain.MissingInformationUnsupported, Detail: "The requested observation is unsupported.",
		Impact: "The current state could not be established.",
	}}
	reason, err := domain.DeriveAnswerTerminalReason(nil, limitations, nil, false)
	if err != nil || reason != domain.RunTerminalInsufficientEvidence {
		t.Fatalf("unsupported terminal reason = %q, %v", reason, err)
	}
}

func testModelCallPreflight(kind agent.ModelCallKind) *agent.ModelCallPreflight {
	streamBytes := domain.MaxModelStreamBytes
	outputBytes := domain.MaxModelMessageBytes
	if kind == agent.ModelCallSummary {
		streamBytes = 0
		outputBytes = domain.MaxSessionSummaryBytes
	}
	return &agent.ModelCallPreflight{
		Kind: kind, MessageCount: 2, MessageBytes: 128,
		CurrentInputCount: 1, CurrentInputDigest: domain.SHA256Hex("test-run-input-manifest"),
		ReservedRequestBytes: domain.MaxModelRequestBytes,
		ReservedOutputBytes:  outputBytes, ReservedStreamBytes: streamBytes,
		ReservedNanoseconds: int64(time.Second), ReservedCostUnits: 1,
	}
}

func testModelCallPreflightForInput(input agent.RunInput, kind agent.ModelCallKind, steers ...agent.ConversationTurn) *agent.ModelCallPreflight {
	preflight := testModelCallPreflight(kind)
	manifest, err := agent.BuildRunInputManifest(input, steers)
	if err != nil {
		panic(err)
	}
	preflight.CurrentInputCount = manifest.Count
	preflight.CurrentInputDigest = manifest.Digest
	return preflight
}

func configureTestModelEgress(bridge *eventBridge) {
	bridge.egressBase = &modelEgressBase{
		role: domain.ModelRoleAgent, originHash: domain.SHA256Hex("test-model-origin"), consented: true,
		contextMode: domain.PrivacyModeStandard, runMode: agent.RunModeOrdinary,
		summaryState: UIContextSummaryAbsent, eligibleCategories: []ModelDataCategory{DataCategoryUserQuestion},
		budgetProfile: agent.BudgetProfileBalanced,
	}
	bridge.terminalBudget = NewUIBudgetMeasures(agent.DefaultRunBudgetLimits())
	setBudgetUnavailable(bridge.terminalBudget, UIBudgetQueueItems)
	setBudgetUnavailable(bridge.terminalBudget, UIBudgetQueueBytes)
}

func testTerminalOutcome(reason domain.RunTerminalReason) *UITerminalOutcome {
	value, _ := ProjectTerminalOutcome(reason)
	return &value
}

func testAnswerProvenance(scope int64, policy domain.PolicyGeneration) *UIAnswerProvenance {
	return &UIAnswerProvenance{
		ScopeGeneration: scope, PolicyGeneration: policy, CoverageState: UIAnswerCoverageComplete,
	}
}
