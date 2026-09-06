package einoadapter

import (
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func diagnosisDraft(message *schema.Message) (agent.DiagnosisDraft, error) {
	return diagnosisDraftForMode(message, agent.RunModeOrdinary)
}

func diagnosisDraftForMode(message *schema.Message, mode agent.RunMode) (agent.DiagnosisDraft, error) {
	if message == nil || message.Role != schema.Assistant || message.Content == "" ||
		message.ToolCallID != "" || message.ToolName != "" || len(message.ToolCalls) != 0 ||
		unsupportedMessageFields(message) {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	var (
		draft agent.DiagnosisDraft
		err   error
	)
	if mode == agent.RunModePlanOnly {
		draft, err = agent.DecodePlanResponse(message.Content)
	} else {
		draft, err = agent.DecodeDiagnosticResponse(message.Content)
	}
	if err != nil {
		return agent.DiagnosisDraft{}, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, err)
	}
	return draft, nil
}

func diagnosisDraftContainsCredential(credential *config.SecretValue, draft agent.DiagnosisDraft) bool {
	values := []string{draft.AnswerMarkdown}
	for _, fact := range draft.ConfirmedFacts {
		values = append(values, fact.Statement)
		for _, evidenceID := range fact.EvidenceIDs {
			values = append(values, string(evidenceID))
		}
	}
	for _, action := range draft.RecommendedActions {
		values = append(values, string(action.Operation), action.Action, action.Risk)
		values = append(values, action.Prerequisites...)
		if action.Target != nil {
			values = append(values,
				action.Target.APIVersion,
				action.Target.Kind,
				action.Target.Namespace,
				action.Target.Name,
				action.Target.UID,
				action.Target.ResourceVersion,
			)
		}
		if action.Parameters != nil {
			values = append(values, string(action.Parameters.Kind), action.Parameters.Value)
		}
	}
	for _, coverage := range draft.ClaimCoverage {
		values = append(values, coverage.Text, coverage.TextHash, string(coverage.Kind), string(coverage.State))
	}
	if draft.Plan != nil {
		values = append(values, draft.Plan.Title)
		for _, step := range draft.Plan.Steps {
			values = append(values, step.Description)
		}
		values = append(values, draft.Plan.Limitations...)
	}
	return credentialAppearsInStrings(credential, values...)
}
