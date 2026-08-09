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
	ConfirmedFacts     *[]domain.ConfirmedFact      `json:"confirmed_facts"`
	Hypotheses         *[]domain.Hypothesis         `json:"hypotheses"`
	MissingInformation *[]domain.MissingInformation `json:"missing_information"`
	RecommendedActions *[]domain.RecommendedAction  `json:"recommended_actions"`
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
	if err := decoder.Decode(&trailing); err != io.EOF || wire.ConfirmedFacts == nil || wire.Hypotheses == nil ||
		wire.MissingInformation == nil || wire.RecommendedActions == nil {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	return agent.DiagnosisDraft{
		ConfirmedFacts:     append([]domain.ConfirmedFact(nil), (*wire.ConfirmedFacts)...),
		Hypotheses:         append([]domain.Hypothesis(nil), (*wire.Hypotheses)...),
		MissingInformation: append([]domain.MissingInformation(nil), (*wire.MissingInformation)...),
		RecommendedActions: append([]domain.RecommendedAction(nil), (*wire.RecommendedActions)...),
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
