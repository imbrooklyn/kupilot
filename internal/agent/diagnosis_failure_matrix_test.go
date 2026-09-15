package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDiagnosisFailureMatrixKeepsDistinctBoundaryReasons(t *testing.T) {
	clarification := func() *domain.ClarificationRequest {
		return &domain.ClarificationRequest{SchemaVersion: domain.AnswerCompletenessSchemaVersion, Questions: []domain.ClarificationQuestion{{Sequence: 1, Kind: domain.ClarificationChoice, Prompt: "Which namespace?", Choices: []domain.ClarificationChoiceValue{{ID: "1", Label: "Working"}, {ID: "2", Label: "Another"}}}}}
	}
	cases := []struct {
		name   string
		want   domain.InteractionFailure
		mutate func(*DiagnosisDraft, *DiagnosisMetadata, **EvidenceRegistry)
	}{
		{"invalid runtime identifier", domain.FailureInternal, func(_ *DiagnosisDraft, m *DiagnosisMetadata, _ **EvidenceRegistry) { m.ID = "invalid" }},
		{"empty answer", domain.FailureFinalShape, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) { d.AnswerMarkdown = "" }},
		{"missing registry", domain.FailureEvidenceAcceptance, func(_ *DiagnosisDraft, _ *DiagnosisMetadata, r **EvidenceRegistry) { *r = nil }},
		{"missing current support", domain.FailureClaimUnsupported, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.ConfirmedFacts = []domain.ConfirmedFact{{Statement: "Current observation."}}
		}},
		{"duplicate current support", domain.FailureEvidenceDuplicate, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.ConfirmedFacts = []domain.ConfirmedFact{{Statement: "Current observation.", EvidenceIDs: []domain.EvidenceID{testEvidenceID, testEvidenceID}}}
		}},
		{"duplicate hypothesis support", domain.FailureEvidenceDuplicate, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Hypotheses = []domain.Hypothesis{{Statement: "Possible explanation.", SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID, testEvidenceID}, Confidence: domain.DiagnosisConfidenceLow, Falsifier: "A later read disagrees."}}
		}},
		{"clock before observation", domain.FailureInternal, func(_ *DiagnosisDraft, m *DiagnosisMetadata, _ **EvidenceRegistry) {
			m.CreatedAt = time.UnixMilli(999).UTC()
		}},
		{"invalid final hypothesis", domain.FailureFinalShape, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Hypotheses = []domain.Hypothesis{{Statement: "Possible explanation.", Confidence: "unknown", Falsifier: "A later read disagrees."}}
		}},
		{"invalid plan", domain.FailurePlan, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) { d.Plan = &domain.Plan{} }},
		{"plan presentation invariant", domain.FailurePlan, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Plan = &domain.Plan{SchemaVersion: domain.PlanSchemaVersion, Title: "Plan", Steps: []domain.PlanStep{{Sequence: 1, Description: "Read one resource."}}}
		}},
		{"invalid clarification", domain.FailureClarification, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Clarification = &domain.ClarificationRequest{}
		}},
		{"clarification redaction exceeds choice ceiling", domain.FailureClarification, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Clarification = clarification()
			d.Clarification.Questions[0].Choices[0].Label = strings.Repeat("token=x ", 64)
		}},
		{"clarification mixed with claims", domain.FailureClarification, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Clarification = clarification()
			d.SuggestedStopReason = domain.RunTerminalNeedsUserInput
			d.ConfirmedFacts = []domain.ConfirmedFact{{Statement: "Current observation.", EvidenceIDs: []domain.EvidenceID{testEvidenceID}}}
		}},
		{"clarification after accepted read", domain.FailureClarification, func(d *DiagnosisDraft, _ *DiagnosisMetadata, _ **EvidenceRegistry) {
			d.Clarification = clarification()
			d.SuggestedStopReason = domain.RunTerminalNeedsUserInput
		}},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			input := testRunInput(t, "Inspect one resource.")
			registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
			if err != nil {
				t.Fatal(err)
			}
			call := testBoundCall(t, input, testInvocationID, "sample-pod")
			if _, err = registry.AcceptToolResult(call, testToolResult(t, call, testEvidenceID, time.UnixMilli(1000).UTC())); err != nil {
				t.Fatal(err)
			}
			draft := DiagnosisDraft{AnswerMarkdown: "A safe bounded answer."}
			metadata := DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1001).UTC(), PolicyGeneration: 1}
			scenario.mutate(&draft, &metadata, &registry)
			_, err = ValidateDiagnosis(draft, metadata, registry)
			if err == nil || InteractionFailureOf(err, "") != scenario.want || err.Error() != scenario.want.SafeMessage() {
				t.Fatalf("reason = %v; want %s", err, scenario.want)
			}
			if registry != nil {
				if _, err = registry.snapshot(); err != nil {
					t.Fatal("rejected final sealed the registry")
				}
			}
		})
	}
}

func TestPlanWireDerivesOrdinalsAndRejectsStructuralAlternatives(t *testing.T) {
	const valid = `{"schema_version":2,"title":"Plan","steps":[{"description":"Inspect one resource."}],"limitations":[],"evidence_citations":[]}`
	draft, err := DecodePlanResponse(valid)
	if err != nil || draft.Plan == nil || draft.Plan.SchemaVersion != 1 || draft.Plan.Steps[0].Sequence != 1 || draft.ResponseSchemaVersion != 4 {
		t.Fatalf("plan = %#v, %v", draft, err)
	}
	for _, test := range []struct {
		content string
		reason  domain.InteractionFailure
	}{
		{`{`, domain.FailureFinalJSON},
		{strings.Replace(valid, `"schema_version":2`, `"schema_version":1`, 1), domain.FailureFinalSchema},
		{strings.Replace(valid, `"title":"Plan"`, `"title":12`, 1), domain.FailurePlan},
		{strings.Replace(valid, `"title":"Plan"`, `"title":""`, 1), domain.FailurePlan},
		{strings.Replace(valid, `"steps":[{"description":"Inspect one resource."}]`, `"steps":[]`, 1), domain.FailurePlan},
		{strings.Replace(valid, `{"description":"Inspect one resource."}`, `{"description":"Inspect one resource.","sequence":1}`, 1), domain.FailureFinalUnknownField},
		{strings.Replace(valid, `"evidence_citations":[]`, `"evidence_citations":[{"claim":"Current observation.","claim_type":"current_observation","evidence_ids":[]}]`, 1), domain.FailureClaimUnsupported},
	} {
		if _, err = DecodePlanResponse(test.content); !errors.Is(err, ErrInvalidDiagnosticResponse) || InteractionFailureOf(err, "") != test.reason {
			t.Fatalf("plan rejection = %v; want %s", err, test.reason)
		}
	}
	for _, count := range []int{domain.MaxPlanSteps, domain.MaxPlanSteps + 1} {
		steps := make([]string, count)
		for i := range steps {
			steps[i] = fmt.Sprintf(`{"description":"Read resource %d."}`, i+1)
		}
		wire := strings.Replace(valid, `{"description":"Inspect one resource."}`, strings.Join(steps, ","), 1)
		draft, err := DecodePlanResponse(wire)
		if count == domain.MaxPlanSteps {
			if err != nil || len(draft.Plan.Steps) != count || draft.Plan.Steps[count-1].Sequence != count {
				t.Fatalf("exact plan ceiling = %v", err)
			}
		} else if InteractionFailureOf(err, "") != domain.FailureFinalLimit {
			t.Fatalf("one-over plan ceiling = %v", err)
		}
	}
}

func TestFinalAlternativeAndInternalGrammarFailuresRemainTyped(t *testing.T) {
	const final = `{"answer_markdown":"Safe answer.","evidence_citations":[],"proposed_actions":[],"response_schema_version":4,"outcome":"answer","limitations":[],"questions":[]}`
	for _, test := range []struct {
		name, wire string
		reason     domain.InteractionFailure
	}{
		{"answer with questions", strings.Replace(final, `"questions":[]`, `"questions":[{"kind":"free_form","prompt":"Which resource?","choices":[]}]`, 1), domain.FailureFinalShape},
		{"empty clarification", strings.Replace(final, `"outcome":"answer"`, `"outcome":"needs_user_input"`, 1), domain.FailureClarification},
		{"action parameter type", strings.Replace(final, `"proposed_actions":[]`, `"proposed_actions":[{"operation":"restart","reason":"Review.","risk":"Review.","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"team-a","name":"sample"},"parameters":{"kind":7,"value":"1"}}]`, 1), domain.FailureFinalShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeDiagnosticResponse(test.wire)
			if InteractionFailureOf(err, "") != test.reason || !errors.Is(err, ErrInvalidDiagnosticResponse) {
				t.Fatalf("final rejection = %v; want %s", err, test.reason)
			}
		})
	}
	// These direct probes exercise defensive parser returns behind the complete
	// JSON validator. They do not claim malformed input reaches them on the wire.
	for _, raw := range []string{`{,`, `{"answer_markdown":}`} {
		if err := validateResponseObject([]byte(raw), wireAnswer); InteractionFailureOf(err, "") != domain.FailureFinalJSON {
			t.Fatalf("internal grammar rejection = %v", err)
		}
	}
	if fields := responseFields(responseWireShape(255)); fields != nil {
		t.Fatal("unknown internal shape acquired fields")
	}
	if _, _, err := decodeClaimCoverage([]evidenceCitationWire{{Claim: "Observation."}}); InteractionFailureOf(err, "") != domain.FailureFinalMissingField {
		t.Fatalf("missing internal citation fields = %v", err)
	}
	if _, err := sanitizeDiagnosisText("   "); InteractionFailureOf(err, "") != domain.FailureFinalShape {
		t.Fatalf("empty normalized claim = %v", err)
	}
	if _, err := sanitizeDiagnosisText(strings.Repeat("token=x ", maxDiagnosisDraftTextBytes/8)); InteractionFailureOf(err, "") != domain.FailureFinalShape {
		t.Fatalf("normalization cannot silently truncate a claim: %v", err)
	}
}
