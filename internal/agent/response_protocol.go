package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// DiagnosticResponseAnswerField is the first top-level field projected for
	// provisional display before the complete response is validated.
	DiagnosticResponseAnswerField  = "answer_markdown"
	maxDiagnosticResponseJSONDepth = 8
)

var (
	// ErrInvalidDiagnosticResponse reports a malformed or unsupported final
	// response without exposing model content.
	ErrInvalidDiagnosticResponse = errors.New("diagnostic response protocol data is invalid")
	// ErrInvalidHistoricalAssistantResponse reports a retained visible answer
	// that cannot be represented safely for the next Eino request.
	ErrInvalidHistoricalAssistantResponse = errors.New("historical assistant response cannot be represented")
)

type diagnosticResponseWire struct {
	AnswerMarkdown    *string                 `json:"answer_markdown"`
	EvidenceCitations *[]evidenceCitationWire `json:"evidence_citations"`
	ProposedActions   *[]proposedActionWire   `json:"proposed_actions"`
}

type evidenceCitationWire struct {
	Claim       string              `json:"claim"`
	EvidenceIDs []domain.EvidenceID `json:"evidence_ids"`
}

type proposedActionTargetWire struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

type proposedActionWire struct {
	Operation     domain.ApprovalOperation `json:"operation"`
	Reason        string                   `json:"reason"`
	Risk          string                   `json:"risk"`
	Prerequisites []string                 `json:"prerequisites"`
	Target        proposedActionTargetWire `json:"target"`
	Parameters    json.RawMessage          `json:"parameters"`
}

// EncodeHistoricalAssistantResponse reconstructs the response envelope from
// one retained visible answer. Historic Evidence and actions are deliberately
// absent so replay cannot restore either kind of authority.
func EncodeHistoricalAssistantResponse(answer string) (string, error) {
	citations := make([]evidenceCitationWire, 0)
	actions := make([]proposedActionWire, 0)
	wire := diagnosticResponseWire{
		AnswerMarkdown:    &answer,
		EvidenceCitations: &citations,
		ProposedActions:   &actions,
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wire); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidHistoricalAssistantResponse, err)
	}
	content := strings.TrimSuffix(buffer.String(), "\n")
	if !domain.ValidModelText(content, domain.MaxModelMessageBytes, false) {
		return "", ErrInvalidHistoricalAssistantResponse
	}
	return content, nil
}

// DecodeDiagnosticResponse applies the single project-owned final response
// protocol. Eino and provider types never enter this contract.
func DecodeDiagnosticResponse(content string) (DiagnosisDraft, error) {
	if !domain.ValidModelText(content, domain.MaxModelMessageBytes, false) {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	if err := rejectDuplicateJSONKeys(content); err != nil {
		return DiagnosisDraft{}, fmt.Errorf("%w: %w", ErrInvalidDiagnosticResponse, err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var wire diagnosticResponseWire
	if err := decoder.Decode(&wire); err != nil {
		return DiagnosisDraft{}, fmt.Errorf("%w: %w", ErrInvalidDiagnosticResponse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	if wire.AnswerMarkdown == nil || wire.EvidenceCitations == nil || wire.ProposedActions == nil {
		return DiagnosisDraft{}, ErrInvalidDiagnosticResponse
	}
	citations := make([]domain.ConfirmedFact, len(*wire.EvidenceCitations))
	for index, citation := range *wire.EvidenceCitations {
		citations[index] = domain.ConfirmedFact{
			Statement:   citation.Claim,
			EvidenceIDs: append([]domain.EvidenceID(nil), citation.EvidenceIDs...),
		}
	}
	actions := make([]domain.RecommendedAction, len(*wire.ProposedActions))
	for index, action := range *wire.ProposedActions {
		parameters, err := decodeProposedActionParameters(action.Parameters)
		if err != nil {
			return DiagnosisDraft{}, fmt.Errorf("%w: %w", ErrInvalidDiagnosticResponse, err)
		}
		target := &domain.ResourceRef{
			APIVersion: action.Target.APIVersion,
			Kind:       action.Target.Kind,
			Namespace:  action.Target.Namespace,
			Name:       action.Target.Name,
		}
		actions[index] = domain.RecommendedAction{
			Operation:     action.Operation,
			Target:        target,
			Parameters:    parameters,
			Action:        action.Reason,
			Risk:          action.Risk,
			Prerequisites: append([]string(nil), action.Prerequisites...),
		}
	}
	return DiagnosisDraft{
		AnswerMarkdown:     *wire.AnswerMarkdown,
		ConfirmedFacts:     citations,
		RecommendedActions: actions,
	}, nil
}

func decodeProposedActionParameters(raw json.RawMessage) (*domain.ProposedActionParameters, error) {
	if len(raw) == 0 {
		return nil, ErrInvalidDiagnosisDraft
	}
	if string(raw) == "null" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var parameters domain.ProposedActionParameters
	if err := decoder.Decode(&parameters); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidDiagnosisDraft
	}
	return &parameters, nil
}

func rejectDuplicateJSONKeys(value string) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidDiagnosisDraft
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxDiagnosticResponseJSONDepth {
		return ErrInvalidDiagnosisDraft
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidDiagnosisDraft
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrInvalidDiagnosisDraft
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrInvalidDiagnosisDraft
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrInvalidDiagnosisDraft
		}
	default:
		return ErrInvalidDiagnosisDraft
	}
	return nil
}
