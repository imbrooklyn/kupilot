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
	const want = `{"answer_markdown":"Earlier visible answer.","evidence_citations":[],"proposed_actions":[]}`
	if content != want {
		t.Fatalf("encoded history = %q, want %q", content, want)
	}
	draft, err := DecodeDiagnosticResponse(content)
	if err != nil {
		t.Fatalf("DecodeDiagnosticResponse() error = %v", err)
	}
	if draft.AnswerMarkdown != "Earlier visible answer." || len(draft.ConfirmedFacts) != 0 ||
		len(draft.RecommendedActions) != 0 {
		t.Fatalf("decoded historic draft = %#v", draft)
	}
}

func TestDiagnosticResponseProtocolDecodesTypedAction(t *testing.T) {
	t.Parallel()

	content := `{"answer_markdown":"Scale only after review.","evidence_citations":[],"proposed_actions":[{"operation":"scale_workload","reason":"Restore capacity.","risk":"Changes replicas.","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api"},"parameters":{"kind":"replicas","value":"3"}}]}`
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
}

func TestDiagnosticResponseProtocolRejectsMalformedRepresentations(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"plain text":           "Earlier visible answer.",
		"duplicate key":        `{"answer_markdown":"first","answer_markdown":"second","evidence_citations":[],"proposed_actions":[]}`,
		"missing array":        `{"answer_markdown":"answer","evidence_citations":[]}`,
		"null array":           `{"answer_markdown":"answer","evidence_citations":null,"proposed_actions":[]}`,
		"unknown member":       `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[],"authority":true}`,
		"trailing content":     `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[]} trailing`,
		"excessive nesting":    strings.Repeat("[", maxDiagnosticResponseJSONDepth+2) + "0" + strings.Repeat("]", maxDiagnosticResponseJSONDepth+2),
		"missing parameters":   `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[{"operation":"restart_deployment","reason":"reason","risk":"risk","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api"}}]}`,
		"unknown target field": `{"answer_markdown":"answer","evidence_citations":[],"proposed_actions":[{"operation":"restart_deployment","reason":"reason","risk":"risk","prerequisites":[],"target":{"api_version":"apps/v1","kind":"Deployment","namespace":"payments","name":"api","uid":"forbidden"},"parameters":null}]}`,
	}
	tests["missing Evidence ID array"] = fmt.Sprintf(
		`{"answer_markdown":"answer","evidence_citations":[{"sequence":1,"claim":"Current state is unknown.","claim_type":"uncertainty","claim_hash":%q,"coverage_state":"limited"}],"proposed_actions":[]}`,
		domain.SHA256Hex("Current state is unknown."),
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
