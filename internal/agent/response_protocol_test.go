package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestHistoricalAssistantResponseUsesDiagnosticProtocolWithoutAuthority(t *testing.T) {
	t.Parallel()

	content, err := EncodeHistoricalAssistantResponse("Earlier visible answer.")
	if err != nil {
		t.Fatalf("EncodeHistoricalAssistantResponse() error = %v", err)
	}
	const want = `{"answer_markdown":"Earlier visible answer.","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`
	if content != want {
		t.Fatalf("encoded history = %q, want %q", content, want)
	}
	draft, err := DecodeDiagnosticResponse(content)
	if err != nil {
		t.Fatalf("current retained response was rejected: %v", err)
	}
	if draft.AnswerMarkdown != "Earlier visible answer." || draft.ResponseSchemaVersion != diagnosticResponseSchemaVersion ||
		len(draft.ConfirmedFacts) != 0 || len(draft.RecommendedActions) != 0 || len(draft.ClaimCoverage) != 0 ||
		len(draft.MissingInformation) != 0 || draft.Clarification != nil {
		t.Fatalf("retained response restored authority: %#v", draft)
	}
}

func TestDiagnosticResponseProtocolDecodesTypedAction(t *testing.T) {
	t.Parallel()

	content := `{"answer_markdown":"Scale only after review.","evidence_citations":[],"proposed_actions":[{"operation":"scale_workload","reason":"Restore capacity.","risk":"Changes replicas.","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api"},"parameters":{"kind":"replicas","value":"3"}}],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`
	draft, err := DecodeDiagnosticResponse(content)
	if err != nil {
		t.Fatalf("DecodeDiagnosticResponse() error = %v", err)
	}
	if len(draft.RecommendedActions) != 1 {
		t.Fatalf("decoded actions = %#v", draft.RecommendedActions)
	}
	action := draft.RecommendedActions[0]
	if action.Operation != domain.ActionOperationScaleWorkload || action.Target == nil ||
		action.Target.Namespace != "payments" || action.Parameters == nil ||
		action.Parameters.Kind != domain.ProposedActionParameterReplicas || action.Parameters.Value != "3" {
		t.Fatalf("decoded action = %#v", action)
	}
	if draft.ResponseSchemaVersion != 1 {
		t.Fatalf("response schema version = %d", draft.ResponseSchemaVersion)
	}
}

func TestDiagnosticResponseProtocolDecodesClaimWithoutModelHash(t *testing.T) {
	t.Parallel()

	const content = `{"answer_markdown":"The Pod is not Ready.","evidence_citations":[{"claim":"The projected Pod condition is not Ready.","claim_type":"current_observation","evidence_ids":["00000000-0000-7000-8000-000000008006"]}],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`
	draft, err := DecodeDiagnosticResponse(content)
	if err != nil {
		t.Fatalf("DecodeDiagnosticResponse() error = %v", err)
	}
	if draft.ResponseSchemaVersion != 1 || len(draft.ClaimCoverage) != 1 ||
		draft.ClaimCoverage[0].Text != "The projected Pod condition is not Ready." {
		t.Fatalf("decoded claim = %#v", draft)
	}
}

func TestDiagnosticResponseProtocolDecodesTypedClarification(t *testing.T) {
	t.Parallel()

	content := `{"answer_markdown":"More information is needed:\n\n1. Which workload should be inspected?\n   - api: API\n   - worker: Worker","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"needs_user_input","limitations":[],"questions":[{"kind":"choice","prompt":"Which workload should be inspected?","choices":[{"label":"API"},{"label":"Worker"}]}]}`
	draft, err := DecodeDiagnosticResponse(content)
	if err != nil {
		t.Fatalf("DecodeDiagnosticResponse() error = %v", err)
	}
	if draft.Clarification == nil || len(draft.Clarification.Questions) != 1 ||
		draft.SuggestedStopReason != domain.RunTerminalNeedsUserInput || draft.ResponseSchemaVersion != 1 {
		t.Fatalf("decoded clarification = %#v", draft)
	}
}

func TestDiagnosticResponseProtocolRejectsMalformedRepresentations(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"plain text":                      "Earlier visible answer.",
		"duplicate key":                   `{"answer_markdown":"first","answer_markdown":"second","evidence_citations":[],"proposed_actions":[]}`,
		"missing array":                   `{"answer_markdown":"answer","evidence_citations":[]}`,
		"null array":                      `{"answer_markdown":"answer","evidence_citations":null,"proposed_actions":[]}`,
		"unknown member":                  `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[],"authority":true}`,
		"trailing content":                `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[]} trailing`,
		"excessive nesting":               strings.Repeat("[", maxDiagnosticResponseJSONDepth+2) + "0" + strings.Repeat("]", maxDiagnosticResponseJSONDepth+2),
		"missing parameters":              `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[{"operation":"restart_deployment","reason":"reason","risk":"risk","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api"}}]}`,
		"unknown target field":            `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[{"operation":"restart_deployment","reason":"reason","risk":"risk","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api","uid":"forbidden"},"parameters":null}]}`,
		"model-owned budget reason":       `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","stop_reason":"budget_exhausted","limitations":[],"questions":[]}`,
		"clarification limitation":        `{"answer_markdown":"More information is needed:\n\n1. What should be inspected?","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"needs_user_input","limitations":[{"kind":"absent","detail":"Missing.","impact":"Limited."}],"questions":[{"kind":"free_form","prompt":"What should be inspected?","choices":[]}]}`,
		"clarification question one over": `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"needs_user_input","limitations":[],"questions":[{"kind":"free_form","prompt":"One?","choices":[]},{"kind":"free_form","prompt":"Two?","choices":[]},{"kind":"free_form","prompt":"Three?","choices":[]},{"kind":"free_form","prompt":"Four?","choices":[]}]}`,
	}
	tests["missing Evidence ID array"] = fmt.Sprintf(
		`{"answer_markdown":"answer","evidence_citations":[{"claim":%q,"claim_type":"uncertainty"}],"proposed_actions":[]}`,
		"Current state is unknown.",
	)
	tests["legacy model claim hash"] = fmt.Sprintf(
		`{"answer_markdown":"answer","evidence_citations":[{"claim":%q,"claim_type":"uncertainty","claim_hash":%q,"evidence_ids":[]}],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`,
		"Current state is unknown.", domain.SHA256Hex("Current state is unknown."),
	)
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeDiagnosticResponse(content); !errors.Is(err, ErrInvalidDiagnosticResponse) {
				t.Fatalf("DecodeDiagnosticResponse() error = %v", err)
			}
		})
	}
}

func TestHistoricalAssistantResponseFailsClosedWithoutEchoingContent(t *testing.T) {
	t.Parallel()

	canary := "protocol-canary-value"
	_, err := EncodeHistoricalAssistantResponse(canary + strings.Repeat("x", domain.MaxModelMessageBytes))
	if !errors.Is(err, ErrInvalidHistoricalAssistantResponse) {
		t.Fatalf("EncodeHistoricalAssistantResponse() error = %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("safe error exposed retained content: %q", err)
	}
}
