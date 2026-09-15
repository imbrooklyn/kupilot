package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type responseWireShape uint8

const (
	wireScalar responseWireShape = iota
	wireAnswer
	wirePlan
	wireClaim
	wireQuestion
	wireChoice
	wireStep
	wireLimitation
	wireAction
	wireTarget
	wireParameters
)

type responseField struct {
	name     string
	child    responseWireShape
	array    bool
	nullable bool
}

// responseFields describes only the two code-owned final protocols. It is not
// a configurable schema or a Tool/action authority registry.
func responseFields(shape responseWireShape) []responseField {
	switch shape {
	case wireAnswer:
		return []responseField{{"answer_markdown", wireScalar, false, false}, {"evidence_citations", wireClaim, true, false}, {"proposed_actions", wireAction, true, false}, {"response_schema_version", wireScalar, false, false}, {"outcome", wireScalar, false, false}, {"limitations", wireLimitation, true, false}, {"questions", wireQuestion, true, false}}
	case wirePlan:
		return []responseField{{"schema_version", wireScalar, false, false}, {"title", wireScalar, false, false}, {"steps", wireStep, true, false}, {"limitations", wireScalar, true, false}, {"evidence_citations", wireClaim, true, false}}
	case wireClaim:
		return []responseField{{"claim", wireScalar, false, false}, {"claim_type", wireScalar, false, false}, {"evidence_ids", wireScalar, true, false}}
	case wireQuestion:
		return []responseField{{"kind", wireScalar, false, false}, {"prompt", wireScalar, false, false}, {"choices", wireChoice, true, false}}
	case wireChoice:
		return []responseField{{"label", wireScalar, false, false}}
	case wireStep:
		return []responseField{{"description", wireScalar, false, false}}
	case wireLimitation:
		return []responseField{{"kind", wireScalar, false, false}, {"detail", wireScalar, false, false}, {"impact", wireScalar, false, false}}
	case wireAction:
		return []responseField{{"operation", wireScalar, false, false}, {"reason", wireScalar, false, false}, {"risk", wireScalar, false, false}, {"prerequisites", wireScalar, true, false}, {"target", wireTarget, false, false}, {"parameters", wireParameters, false, true}}
	case wireTarget:
		return []responseField{{"api_version", wireScalar, false, false}, {"kind", wireScalar, false, false}, {"namespace", wireScalar, false, false}, {"name", wireScalar, false, false}}
	case wireParameters:
		return []responseField{{"kind", wireScalar, false, false}, {"value", wireScalar, false, false}}
	default:
		return nil
	}
}

func validateResponseWire(content string, shape responseWireShape) error {
	if len(content) > domain.MaxModelMessageBytes {
		return responseError(domain.FailureFinalLimit, nil)
	}
	if !domain.ValidModelText(content, domain.MaxModelMessageBytes, false) {
		return responseError(domain.FailureFinalJSON, nil)
	}
	if err := rejectDuplicateJSONKeys(content); err != nil {
		return responseError(InteractionFailureOf(err, domain.FailureFinalJSON), err)
	}
	return validateResponseObject(json.RawMessage(content), shape)
}

func validateResponseObject(raw json.RawMessage, shape responseWireShape) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return responseError(domain.FailureFinalNullField, nil)
	}
	if shape == wireScalar {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return responseError(domain.FailureFinalShape, err)
	}
	fields := responseFields(shape)
	seen := make([]bool, len(fields))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return responseError(domain.FailureFinalJSON, err)
		}
		index := -1
		for candidate, field := range fields {
			if key == field.name {
				index = candidate
				break
			}
		}
		if index < 0 {
			return responseError(domain.FailureFinalUnknownField, nil)
		}
		seen[index] = true // Duplicate keys were rejected before shape validation.
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return responseError(domain.FailureFinalJSON, err)
		}
		field := fields[index]
		if field.nullable && strings.TrimSpace(string(value)) == "null" {
			continue
		}
		if field.array {
			if err := validateResponseArray(value, field.child); err != nil {
				return err
			}
		} else if err := validateResponseObject(value, field.child); err != nil {
			return err
		}
	}
	for _, present := range seen {
		if !present {
			return responseError(domain.FailureFinalMissingField, nil)
		}
	}
	return nil
}

func validateResponseArray(raw json.RawMessage, child responseWireShape) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return responseError(domain.FailureFinalNullField, nil)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return responseError(domain.FailureFinalShape, err)
	}
	maximum := 100
	switch child {
	case wireAction:
		maximum = 1
	case wireQuestion:
		maximum = domain.MaxClarificationQuestions
	case wireChoice:
		maximum = domain.MaxClarificationChoices
	case wireStep:
		maximum = domain.MaxPlanSteps
	}
	if len(values) > maximum {
		return responseError(domain.FailureFinalLimit, nil)
	}
	for _, value := range values {
		if err := validateResponseObject(value, child); err != nil {
			return err
		}
	}
	return nil
}
