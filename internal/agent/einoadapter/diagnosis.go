package einoadapter

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type diagnosisWire struct {
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
}

func diagnosisDraft(message *schema.Message) (agent.DiagnosisDraft, error) {
	if message == nil || message.Role != schema.Assistant || message.Content == "" ||
		message.ToolCallID != "" || message.ToolName != "" || len(message.ToolCalls) != 0 ||
		unsupportedMessageFields(message) {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	neutral := domain.ModelMessage{Role: domain.ModelMessageRoleAssistant, Content: message.Content}
	if neutral.Validate() != nil || rejectDuplicateJSONKeys(message.Content) != nil {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	decoder := json.NewDecoder(strings.NewReader(message.Content))
	decoder.DisallowUnknownFields()
	var wire diagnosisWire
	if err := decoder.Decode(&wire); err != nil {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	if wire.AnswerMarkdown == nil || wire.EvidenceCitations == nil || wire.ProposedActions == nil {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
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
		target := &domain.ResourceRef{
			APIVersion: action.Target.APIVersion,
			Kind:       action.Target.Kind,
			Namespace:  action.Target.Namespace,
			Name:       action.Target.Name,
		}
		actions[index] = domain.RecommendedAction{
			Operation:     action.Operation,
			Target:        target,
			Action:        action.Reason,
			Risk:          action.Risk,
			Prerequisites: append([]string(nil), action.Prerequisites...),
		}
	}
	return agent.DiagnosisDraft{
		AnswerMarkdown:     *wire.AnswerMarkdown,
		ConfirmedFacts:     citations,
		RecommendedActions: actions,
	}, nil
}

func rejectDuplicateJSONKeys(value string) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return agent.ErrInvalidDiagnosisDraft
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
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
				return agent.ErrInvalidDiagnosisDraft
			}
			if _, duplicate := seen[key]; duplicate {
				return agent.ErrInvalidDiagnosisDraft
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return agent.ErrInvalidDiagnosisDraft
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return agent.ErrInvalidDiagnosisDraft
		}
	default:
		return agent.ErrInvalidDiagnosisDraft
	}
	return nil
}
