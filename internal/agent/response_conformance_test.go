package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const conformanceAnswer = `{"answer_markdown":"Hello.","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`

func TestResponseTextLimitsAndClarificationPresentation(t *testing.T) {
	if text, err := sanitizeDiagnosisText(strings.Repeat("x", maxDiagnosisDraftTextBytes)); err != nil || len(text) != maxDiagnosisDraftTextBytes {
		t.Fatalf("exact text limit = %d, %v", len(text), err)
	}
	if _, err := sanitizeDiagnosisText(strings.Repeat("x", maxDiagnosisDraftTextBytes+1)); InteractionFailureOf(err, "") != domain.FailureFinalLimit {
		t.Fatalf("text one over = %v", err)
	}
	if _, err := sanitizeDiagnosisText(" "); InteractionFailureOf(err, "") != domain.FailureFinalShape {
		t.Fatalf("empty normalized text = %v", err)
	}
	if text, err := processModelMarkdown(strings.Repeat("x", MaxAnswerMarkdownBytes), MaxAnswerMarkdownBytes); err != nil || len(text) != MaxAnswerMarkdownBytes {
		t.Fatalf("exact Markdown limit = %d, %v", len(text), err)
	}
	if _, err := processModelMarkdown(strings.Repeat("x", MaxAnswerMarkdownBytes+1), MaxAnswerMarkdownBytes); InteractionFailureOf(err, "") != domain.FailureFinalLimit {
		t.Fatalf("Markdown one over = %v", err)
	}
	input := testRunInput(t, "Choose the namespace.")
	for _, candidate := range []string{"", " ", "A differently formatted candidate."} {
		wire := strings.Replace(conformanceAnswer, `"Hello."`, fmt.Sprintf("%q", candidate), 1)
		wire = strings.Replace(wire, `"outcome":"answer"`, `"outcome":"needs_user_input"`, 1)
		wire = strings.Replace(wire, `"questions":[]`, `"questions":[{"kind":"choice","prompt":"Which namespace?","choices":[{"label":"Working namespace"},{"label":"Another namespace"}]}]`, 1)
		draft, err := DecodeDiagnosticResponse(wire)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
		if err != nil {
			t.Fatal(err)
		}
		diagnosis, err := ValidateDiagnosis(draft, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1000).UTC()}, registry)
		if err != nil || diagnosis.Clarification == nil || diagnosis.Completeness.StopReason != domain.RunTerminalNeedsUserInput || strings.Contains(diagnosis.AnswerMarkdown, "candidate") || registry.Len() != 0 {
			t.Fatalf("clarification presentation = %#v, %v", diagnosis, err)
		}
	}
}

func assertResponseFailure(t *testing.T, content string, want domain.InteractionFailure) {
	t.Helper()
	_, err := DecodeDiagnosticResponse(content)
	if !errors.Is(err, ErrInvalidDiagnosticResponse) || InteractionFailureOf(err, "") != want ||
		err.Error() != want.SafeMessage() {
		t.Fatalf("failure = %v (%s), want %s", err, InteractionFailureOf(err, ""), want)
	}
}

func TestResponseConformanceRequiredFields(t *testing.T) {
	fields := []struct{ name, value string }{
		{"answer_markdown", `"Hello."`}, {"evidence_citations", `[]`}, {"proposed_actions", `[]`},
		{"response_schema_version", `1`}, {"outcome", `"answer"`}, {"limitations", `[]`}, {"questions", `[]`},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			member := fmt.Sprintf(`"%s":%s`, field.name, field.value)
			missing := strings.Replace(conformanceAnswer, member+",", "", 1)
			if missing == conformanceAnswer {
				missing = strings.Replace(conformanceAnswer, ","+member, "", 1)
			}
			assertResponseFailure(t, missing, domain.FailureFinalMissingField)
			assertResponseFailure(t, strings.Replace(conformanceAnswer, member, fmt.Sprintf(`"%s":null`, field.name), 1), domain.FailureFinalNullField)
			assertResponseFailure(t, strings.Replace(conformanceAnswer, member, member+","+member, 1), domain.FailureFinalDuplicateField)
		})
	}
}

func TestResponseConformanceMalformedAndRetiredFields(t *testing.T) {
	tests := []struct {
		name, content string
		reason        domain.InteractionFailure
	}{
		{"plain", "untrusted-response-canary", domain.FailureFinalJSON},
		{"trailing", conformanceAnswer + " false", domain.FailureFinalJSON},
		{"empty", "", domain.FailureFinalJSON},
		{"invalid UTF8", string([]byte{0xff}), domain.FailureFinalJSON},
		{"top null", "null", domain.FailureFinalNullField},
		{"top array", "[]", domain.FailureFinalShape},
		{"unknown", strings.TrimSuffix(conformanceAnswer, "}") + `,"authority":true}`, domain.FailureFinalUnknownField},
		{"legacy", strings.Replace(conformanceAnswer, `"response_schema_version":1`, `"response_schema_version":3`, 1), domain.FailureFinalSchema},
		{"schema type", strings.Replace(conformanceAnswer, `"response_schema_version":1`, `"response_schema_version":"4"`, 1), domain.FailureFinalShape},
		{"unknown outcome", strings.Replace(conformanceAnswer, `"outcome":"answer"`, `"outcome":"execute"`, 1), domain.FailureFinalShape},
		{"stop suggestion", strings.TrimSuffix(conformanceAnswer, "}") + `,"stop_reason":"completed"}`, domain.FailureFinalUnknownField},
		{"array object", strings.Replace(conformanceAnswer, `"evidence_citations":[]`, `"evidence_citations":{}`, 1), domain.FailureFinalShape},
		{"null item", strings.Replace(conformanceAnswer, `"evidence_citations":[]`, `"evidence_citations":[null]`, 1), domain.FailureFinalNullField},
		{"deep", strings.Repeat("[", maxDiagnosticResponseJSONDepth+2) + "0" + strings.Repeat("]", maxDiagnosticResponseJSONDepth+2), domain.FailureFinalLimit},
		{"byte one over", strings.Repeat("x", domain.MaxModelMessageBytes+1), domain.FailureFinalLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { assertResponseFailure(t, test.content, test.reason) })
	}
}

func responseWithClaims(claims string) string {
	return strings.Replace(conformanceAnswer, `"evidence_citations":[]`, `"evidence_citations":[`+claims+`]`, 1)
}

func TestResponseConformanceClaimIntentAndDerivation(t *testing.T) {
	tests := []struct {
		kind   domain.ClaimKind
		refs   string
		state  domain.ClaimCoverageState
		reason domain.InteractionFailure
	}{
		{domain.ClaimCurrentObservation, `"00000000-0000-7000-8000-000000008006"`, domain.ClaimCoverageVerified, ""},
		{domain.ClaimCurrentObservation, "", "", domain.FailureClaimUnsupported},
		{domain.ClaimInference, "", domain.ClaimCoverageLimited, ""},
		{domain.ClaimInference, `"00000000-0000-7000-8000-000000008006"`, domain.ClaimCoverageSupported, ""},
		{domain.ClaimRecommendation, "", domain.ClaimCoverageLimited, ""},
		{domain.ClaimRecommendation, `"00000000-0000-7000-8000-000000008006"`, domain.ClaimCoverageSupported, ""},
		{domain.ClaimUncertainty, "", domain.ClaimCoverageLimited, ""},
		{domain.ClaimUncertainty, `"00000000-0000-7000-8000-000000008006"`, "", domain.FailureClaimBinding},
		{domain.ClaimUnsupportedObservation, "", domain.ClaimCoverageUnsupported, ""},
		{domain.ClaimUnsupportedObservation, `"00000000-0000-7000-8000-000000008006"`, "", domain.FailureClaimBinding},
		{"unknown", "", "", domain.FailureClaimKind},
	}
	for _, test := range tests {
		t.Run(string(test.kind)+"/"+string(test.state)+"/"+string(test.reason), func(t *testing.T) {
			content := responseWithClaims(fmt.Sprintf(`{"claim":"Bounded claim.","claim_type":%q,"evidence_ids":[%s]}`, test.kind, test.refs))
			if test.reason != "" {
				assertResponseFailure(t, content, test.reason)
				return
			}
			draft, err := DecodeDiagnosticResponse(content)
			if err != nil || len(draft.ClaimCoverage) != 1 || draft.ClaimCoverage[0].Sequence != 1 || draft.ClaimCoverage[0].State != test.state {
				t.Fatalf("derived claim = %#v, %v", draft.ClaimCoverage, err)
			}
		})
	}
	for _, retired := range []string{`"sequence":1`, `"claim_hash":"untrusted-response-canary"`, `"coverage_state":"verified"`} {
		assertResponseFailure(t, responseWithClaims(`{"claim":"Claim.","claim_type":"inference","evidence_ids":[],`+retired+`}`), domain.FailureFinalUnknownField)
	}
}

func TestResponseConformanceNestedFields(t *testing.T) {
	objects := []struct {
		name, object string
		embed        func(string) string
	}{
		{"claim", `{"claim":"Claim.","claim_type":"inference","evidence_ids":[]}`, responseWithClaims},
		{"action", `{"operation":"restart_deployment","reason":"Review.","risk":"Replacement.","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"team-a","name":"sample"},"parameters":null}`, func(value string) string {
			return strings.Replace(conformanceAnswer, `"proposed_actions":[]`, `"proposed_actions":[`+value+`]`, 1)
		}},
		{"question", `{"kind":"free_form","prompt":"Which workload?","choices":[]}`, func(value string) string {
			return strings.Replace(strings.Replace(conformanceAnswer, `"outcome":"answer"`, `"outcome":"needs_user_input"`, 1), `"questions":[]`, `"questions":[`+value+`]`, 1)
		}},
	}
	for _, object := range objects {
		t.Run(object.name, func(t *testing.T) {
			valid := object.embed(object.object)
			if _, err := DecodeDiagnosticResponse(valid); err != nil {
				t.Fatal(err)
			}
			assertResponseFailure(t, object.embed(strings.TrimSuffix(object.object, "}")+`,"unknown":false}`), domain.FailureFinalUnknownField)
			assertResponseFailure(t, object.embed(`{}`), domain.FailureFinalMissingField)
			assertResponseFailure(t, object.embed(`null`), domain.FailureFinalNullField)
			assertResponseFailure(t, object.embed(`[]`), domain.FailureFinalShape)
		})
	}
}

func TestResponseConformanceExactLimitsAndRepresentation(t *testing.T) {
	for _, count := range []int{100, 101} {
		claims := make([]string, count)
		for index := range claims {
			claims[index] = fmt.Sprintf(`{"claim":"Inference %d.","claim_type":"inference","evidence_ids":[]}`, index)
		}
		content := responseWithClaims(strings.Join(claims, ","))
		if count == 101 {
			assertResponseFailure(t, content, domain.FailureFinalLimit)
			continue
		}
		draft, err := DecodeDiagnosticResponse(content)
		if err != nil || len(draft.ClaimCoverage) != 100 || draft.ClaimCoverage[99].Sequence != 100 {
			t.Fatalf("exact claim limit = %d, %v", len(draft.ClaimCoverage), err)
		}
	}
	base, err := DecodeDiagnosticResponse(conformanceAnswer)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		" \n" + conformanceAnswer + "\t ",
		strings.Replace(conformanceAnswer, `"Hello."`, `"\u0048ello."`, 1),
		`{"questions":[],"limitations":[],"outcome":"answer","response_schema_version":1,"proposed_actions":[],"evidence_citations":[],"answer_markdown":"Hello."}`,
	} {
		draft, err := DecodeDiagnosticResponse(content)
		if err != nil || draft.AnswerMarkdown != base.AnswerMarkdown {
			t.Fatalf("presentation changed intent: %#v, %v", draft, err)
		}
	}
}

func TestEmptyAcceptedSourceInvalidatesEvidenceSnapshot(t *testing.T) {
	registry, input, _, _ := claimCoverageRegistry(t)
	snapshot, err := registry.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	call := testBoundCall(t, input, "00000000-0000-7000-8000-000000004099", "sample-pod")
	result := domain.ToolResult{InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(), ObservedAt: time.UnixMilli(1001).UTC(), Status: domain.ToolResultStatusSuccess, DataJSON: `{"items":[]}`}
	if _, err = registry.AcceptToolResult(call, result); err != nil {
		t.Fatal(err)
	}
	if err = registry.seal(snapshot.revision); !errors.Is(err, ErrInvalidEvidenceRegistry) || InteractionFailureOf(err, "") != domain.FailureRegistryChanged {
		t.Fatalf("stale empty-source snapshot = %v", err)
	}
	fresh, err := registry.snapshot()
	if err != nil || fresh.revision != snapshot.revision+1 || len(fresh.sources) != len(snapshot.sources)+1 {
		t.Fatalf("fresh source revision = %#v, %v", fresh, err)
	}
	if err = registry.seal(fresh.revision); err != nil {
		t.Fatal(err)
	}
	if err = registry.seal(fresh.revision); InteractionFailureOf(err, "") != domain.FailureRegistryChanged {
		t.Fatalf("duplicate seal = %v", err)
	}
}
