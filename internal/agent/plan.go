package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type planResponseWire struct {
	SchemaVersion     *int                    `json:"schema_version"`
	Title             *string                 `json:"title"`
	Steps             *[]domain.PlanStep      `json:"steps"`
	Limitations       *[]string               `json:"limitations"`
	EvidenceCitations *[]evidenceCitationWire `json:"evidence_citations"`
}

// DecodePlanResponse validates the plan-only terminal protocol and renders its
// inert typed value as ordinary Markdown. Unknown fields include action
// proposals and therefore fail closed.
func DecodePlanResponse(content string) (DiagnosisDraft, error) {
	if !domain.ValidModelText(content, domain.MaxModelMessageBytes, false) || rejectDuplicateJSONKeys(content) != nil {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var wire planResponseWire
	if err := decoder.Decode(&wire); err != nil {
		return DiagnosisDraft{}, fmt.Errorf("%w: %w", ErrInvalidDiagnosticResponse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	if wire.SchemaVersion == nil || wire.Title == nil || wire.Steps == nil || wire.Limitations == nil ||
		wire.EvidenceCitations == nil {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	plan := domain.Plan{
		SchemaVersion: *wire.SchemaVersion,
		Title:         *wire.Title,
		Steps:         append([]domain.PlanStep(nil), (*wire.Steps)...),
		Limitations:   append([]string(nil), (*wire.Limitations)...),
	}
	if plan.Validate() != nil {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	confirmed, coverage, err := decodeClaimCoverage(*wire.EvidenceCitations)
	if err != nil {
		return DiagnosisDraft{}, err
	}
	answer, err := RenderPlanMarkdown(plan)
	if err != nil {
		return DiagnosisDraft{}, err
	}
	return DiagnosisDraft{
		AnswerMarkdown: answer, ResponseSchemaVersion: 1, ConfirmedFacts: confirmed, ClaimCoverage: coverage, Plan: &plan,
	}, nil
}

// RenderPlanMarkdown creates the sole visible representation of a validated
// Plan. It cannot add ActionEnvelope or executable fields.
func RenderPlanMarkdown(plan domain.Plan) (string, error) {
	if plan.Validate() != nil {
		return "", ErrInvalidDiagnosticResponse
	}
	var output bytes.Buffer
	output.WriteString("## ")
	output.WriteString(plan.Title)
	output.WriteString("\n\n")
	for _, step := range plan.Steps {
		fmt.Fprintf(&output, "%d. %s\n", step.Sequence, step.Description)
	}
	if len(plan.Limitations) > 0 {
		output.WriteString("\nLimitations:\n\n")
		for _, limitation := range plan.Limitations {
			output.WriteString("- ")
			output.WriteString(limitation)
			output.WriteByte('\n')
		}
	}
	value := strings.TrimSuffix(output.String(), "\n")
	if !domain.ValidModelText(value, domain.MaxPlanBytes, false) {
		return "", ErrInvalidDiagnosticResponse
	}
	return value, nil
}
