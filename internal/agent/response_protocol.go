package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// DiagnosticResponseAnswerField is the first top-level field projected for
	// provisional display before the complete response is validated.
	DiagnosticResponseAnswerField   = "answer_markdown"
	diagnosticResponseSchemaVersion = 1
	maxDiagnosticResponseJSONDepth  = 8
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
	AnswerMarkdown        *string                      `json:"answer_markdown"`
	EvidenceCitations     *[]evidenceCitationWire      `json:"evidence_citations"`
	ProposedActions       *[]proposedActionWire        `json:"proposed_actions"`
	ResponseSchemaVersion *int                         `json:"response_schema_version,omitempty"`
	Outcome               *string                      `json:"outcome,omitempty"`
	Limitations           *[]domain.MissingInformation `json:"limitations,omitempty"`
	Questions             *[]clarificationQuestionWire `json:"questions"`
}

type evidenceCitationWire struct {
	Claim       string               `json:"claim"`
	ClaimType   *domain.ClaimKind    `json:"claim_type"`
	EvidenceIDs *[]domain.EvidenceID `json:"evidence_ids"`
}

type clarificationQuestionWire struct {
	Kind    domain.ClarificationQuestionKind `json:"kind"`
	Prompt  string                           `json:"prompt"`
	Choices []clarificationChoiceWire        `json:"choices"`
}
type clarificationChoiceWire struct {
	Label string `json:"label"`
}

type proposedActionTargetWire struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

type proposedActionWire struct {
	Operation     domain.ActionOperation   `json:"operation"`
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
	limitations := make([]domain.MissingInformation, 0)
	questions := make([]clarificationQuestionWire, 0)
	version := diagnosticResponseSchemaVersion
	outcome := "answer"
	wire := diagnosticResponseWire{
		AnswerMarkdown:        &answer,
		EvidenceCitations:     &citations,
		ProposedActions:       &actions,
		ResponseSchemaVersion: &version,
		Outcome:               &outcome,
		Limitations:           &limitations,
		Questions:             &questions,
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
	if err := validateResponseWire(content, wireAnswer); err != nil {
		return DiagnosisDraft{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var wire diagnosticResponseWire
	if err := decoder.Decode(&wire); err != nil {
		return DiagnosisDraft{}, responseError(domain.FailureFinalShape, err)
	}
	if *wire.ResponseSchemaVersion != diagnosticResponseSchemaVersion {
		return DiagnosisDraft{}, responseError(domain.FailureFinalSchema, nil)
	}
	citations, coverage, err := decodeClaimCoverage(*wire.EvidenceCitations)
	if err != nil {
		return DiagnosisDraft{}, err
	}
	actions := make([]domain.RecommendedAction, len(*wire.ProposedActions))
	for index, action := range *wire.ProposedActions {
		parameters, err := decodeProposedActionParameters(action.Parameters)
		if err != nil {
			return DiagnosisDraft{}, responseError(domain.FailureFinalShape, err)
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
	draft := DiagnosisDraft{
		AnswerMarkdown:        *wire.AnswerMarkdown,
		ResponseSchemaVersion: *wire.ResponseSchemaVersion,
		ConfirmedFacts:        citations,
		RecommendedActions:    actions,
		ClaimCoverage:         coverage,
	}
	draft.SuggestedStopReason = domain.RunTerminalCompleted
	draft.MissingInformation = append([]domain.MissingInformation(nil), (*wire.Limitations)...)
	switch *wire.Outcome {
	case "answer":
		if len(*wire.Questions) != 0 {
			return DiagnosisDraft{}, responseError(domain.FailureFinalShape, nil)
		}
	case "needs_user_input":
		if len(citations) != 0 || len(actions) != 0 || len(coverage) != 0 ||
			len(*wire.Limitations) != 0 {
			return DiagnosisDraft{}, responseError(domain.FailureFinalShape, nil)
		}
		request := domain.ClarificationRequest{
			SchemaVersion: domain.AnswerCompletenessSchemaVersion,
			Questions:     make([]domain.ClarificationQuestion, len(*wire.Questions)),
		}
		for index, question := range *wire.Questions {
			choices := make([]domain.ClarificationChoiceValue, len(question.Choices))
			for choiceIndex, choice := range question.Choices {
				choices[choiceIndex] = domain.ClarificationChoiceValue{ID: strconv.Itoa(choiceIndex + 1), Label: choice.Label}
			}
			request.Questions[index] = domain.ClarificationQuestion{Sequence: index + 1, Kind: question.Kind, Prompt: question.Prompt, Choices: choices}
		}
		if request.Validate() != nil {
			return DiagnosisDraft{}, responseError(domain.FailureClarification, nil)
		}
		// Keep candidate text until the adapter credential and sensitivity guards
		// have checked it. ValidateDiagnosis renders the sanitized typed questions.
		draft.Clarification = &request
		draft.SuggestedStopReason = domain.RunTerminalNeedsUserInput
	default:
		return DiagnosisDraft{}, responseError(domain.FailureFinalShape, nil)
	}
	return draft, nil
}

func decodeClaimCoverage(wire []evidenceCitationWire) ([]domain.ConfirmedFact, []ClaimCoverageDraft, error) {
	citations := make([]domain.ConfirmedFact, 0, len(wire))
	coverage := make([]ClaimCoverageDraft, len(wire))
	for index, citation := range wire {
		if citation.ClaimType == nil || citation.EvidenceIDs == nil {
			return nil, nil, responseError(domain.FailureFinalMissingField, nil)
		}
		state, err := structuralClaimState(*citation.ClaimType, len(*citation.EvidenceIDs))
		if err != nil {
			return nil, nil, err
		}
		coverage[index] = ClaimCoverageDraft{
			Sequence: index + 1, Kind: *citation.ClaimType, Text: citation.Claim,
			EvidenceIDs: append([]domain.EvidenceID(nil), (*citation.EvidenceIDs)...), State: state,
		}
		if *citation.ClaimType == domain.ClaimCurrentObservation {
			citations = append(citations, domain.ConfirmedFact{
				Statement: citation.Claim, EvidenceIDs: append([]domain.EvidenceID(nil), (*citation.EvidenceIDs)...),
			})
		}
	}
	return citations, coverage, nil
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
		return interactionError(domain.FailureFinalLimit, ErrInvalidDiagnosisDraft)
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
				return interactionError(domain.FailureFinalDuplicateField, ErrInvalidDiagnosisDraft)
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

func structuralClaimState(kind domain.ClaimKind, references int) (domain.ClaimCoverageState, error) {
	switch kind {
	case domain.ClaimCurrentObservation:
		if references == 0 {
			return "", responseError(domain.FailureClaimUnsupported, nil)
		}
		return domain.ClaimCoverageVerified, nil
	case domain.ClaimInference, domain.ClaimRecommendation:
		if references > 0 {
			return domain.ClaimCoverageSupported, nil
		}
		return domain.ClaimCoverageLimited, nil
	case domain.ClaimUncertainty:
		if references == 0 {
			return domain.ClaimCoverageLimited, nil
		}
	case domain.ClaimUnsupportedObservation:
		if references == 0 {
			return domain.ClaimCoverageUnsupported, nil
		}
	default:
		return "", responseError(domain.FailureClaimKind, nil)
	}
	return "", responseError(domain.FailureClaimBinding, nil)
}

func responseError(reason domain.InteractionFailure, cause error) error {
	if cause == nil {
		cause = ErrInvalidDiagnosticResponse
	} else {
		cause = fmt.Errorf("%w: %w", ErrInvalidDiagnosticResponse, cause)
	}
	return interactionError(reason, cause)
}
