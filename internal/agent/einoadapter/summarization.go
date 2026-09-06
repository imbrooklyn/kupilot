package einoadapter

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	summaryMessageTrigger     = 160
	summaryRecentTailMessages = 16
)

type summaryPlan struct {
	originalCount int
	cutIndex      int
	coveredCount  int
	text          string
	degraded      bool
}

// summaryChatModel reserves and authorizes the Agent-profile summary call
// while Eino's summarization middleware remains the summary generator.
type summaryChatModel struct {
	state *runState
}

var _ einomodel.BaseChatModel = (*summaryChatModel)(nil)

func (model *summaryChatModel) Generate(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	if model == nil || model.state == nil || len(options) != 0 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	return model.state.callSummaryModel(ctx, input)
}

func (model *summaryChatModel) Stream(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := model.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (state *runState) newSummarizationMiddleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error) {
	if state == nil || ctx == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	handler, err := summarization.New(ctx, &summarization.Config{
		Model: &summaryChatModel{state: state},
		Trigger: &summarization.TriggerCondition{
			ContextMessages: summaryMessageTrigger,
			ContextTokens:   domain.MaxSessionContextBytes,
		},
		// Exact endpoint tokenization is not available. This counter deliberately
		// uses UTF-8 bytes as a conservative resource trigger, never as a token or
		// cost estimate, instead of Eino's undocumented character estimate.
		TokenCounter: func(ctx context.Context, input *summarization.TokenCounterInput) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if input == nil {
				return 0, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
			}
			total := 0
			for _, message := range input.Messages {
				if message == nil {
					return 0, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
				}
				total += len(message.Content)
				if total > domain.MaxSessionHistoryBytes {
					return total, nil
				}
			}
			return total, nil
		},
		GenModelInput: state.summaryModelInput,
		Finalize:      state.finalizeSummary,
		Callback:      state.finishSummary,
	})
	if err != nil {
		return nil, normalizeFrameworkError(err)
	}
	return handler, nil
}

func (state *runState) summaryModelInput(
	ctx context.Context,
	systemInstruction *schema.Message,
	userInstruction *schema.Message,
	original []*schema.Message,
) ([]*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, normalizeFrameworkError(err)
	}
	if state == nil || systemInstruction == nil || userInstruction == nil ||
		systemInstruction.Role != schema.System || userInstruction.Role != schema.User {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	conversation := state.input.Conversation()
	turns := conversation.Turns()
	covered := 0
	historyStart := 1
	if prior := conversation.Summary(); prior != nil {
		covered = prior.CoveredCount
		historyStart++
	}
	if len(turns) <= summaryRecentTailMessages || len(original) < historyStart+len(turns)+1 ||
		!matchesInitialContext(original, conversation, state.input.Question()) {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	cutTurns := len(turns) - summaryRecentTailMessages
	cutTurns -= cutTurns % 2
	if cutTurns < 2 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	cutIndex := historyStart + cutTurns
	covered += cutTurns
	coverage := conversation.Coverage()
	if covered > len(coverage) || covered%2 != 0 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	modelInput := make([]*schema.Message, 0, cutIndex-historyStart+3)
	modelInput = append(modelInput, systemInstruction)
	if historyStart == 2 {
		modelInput = append(modelInput, original[1])
	}
	modelInput = append(modelInput, original[historyStart:cutIndex]...)
	modelInput = append(modelInput, userInstruction)
	if err := validateSummaryConversation(modelInput); err != nil {
		return nil, err
	}
	state.mu.Lock()
	state.summaryPlan = &summaryPlan{
		originalCount: len(original), cutIndex: cutIndex, coveredCount: covered,
	}
	state.mu.Unlock()
	return cloneEinoMessages(modelInput), nil
}

func matchesInitialContext(original []*schema.Message, conversation agent.ConversationContext, question string) bool {
	if len(original) == 0 || original[0] == nil || original[0].Role != schema.System {
		return false
	}
	position := 1
	if summary := conversation.Summary(); summary != nil {
		if position >= len(original) || original[position] == nil || original[position].Role != schema.User ||
			original[position].Content != summaryContextPreamble+summary.Text {
			return false
		}
		position++
	}
	for _, turn := range conversation.Turns() {
		content := turn.Content
		if turn.Role == domain.MessageRoleAssistant {
			var err error
			content, err = historicalAssistantContent(turn.Content)
			if err != nil {
				return false
			}
		}
		if position >= len(original) || original[position] == nil || original[position].Content != content {
			return false
		}
		want := schema.User
		if turn.Role == domain.MessageRoleAssistant {
			want = schema.Assistant
		}
		if original[position].Role != want || unsupportedMessageFields(original[position]) || len(original[position].ToolCalls) != 0 {
			return false
		}
		position++
	}
	return position < len(original) && original[position] != nil && original[position].Role == schema.User &&
		original[position].Content == question
}

func validateSummaryConversation(messages []*schema.Message) error {
	if len(messages) < 3 || len(messages) > domain.MaxSessionContextMessages+3 {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	total := 0
	for index, message := range messages {
		contentLimit := domain.MaxModelInputMessageBytes
		if message != nil && message.Role == schema.Assistant {
			contentLimit = domain.MaxModelMessageBytes
		}
		if message == nil || unsupportedMessageFields(message) || len(message.ToolCalls) != 0 ||
			message.ToolCallID != "" || message.ToolName != "" ||
			(index == 0 && message.Role != schema.System) ||
			(index > 0 && message.Role != schema.User && message.Role != schema.Assistant) ||
			!domain.ValidModelText(message.Content, contentLimit, false) {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		total += len(message.Content)
		if total > domain.MaxSessionContextBytes+domain.MaxModelInputMessageBytes {
			return failedRuntime(domain.SafeErrorClassBudgetExhausted, "The conversation summary request exceeded its fixed input limit.", nil)
		}
	}
	return nil
}

func (state *runState) callSummaryModel(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	if state == nil || state.client == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	reservation, err := state.budget.ReserveSummaryCall(ctx)
	if err != nil {
		return nil, runtimeFailureFromBudget(err)
	}
	requestID, err := state.nextModelRequestID()
	if err != nil {
		return nil, err
	}
	if err := validateSummaryConversation(messages); err != nil {
		return nil, err
	}
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryStarted, ModelRequestID: &requestID}); err != nil {
		return nil, err
	}
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	modelCtx, cancel := context.WithTimeout(ctx, reservation.Timeout)
	message, modelError := state.client.generate(modelCtx, requestID, messages, reservation)
	contextError := modelCtx.Err()
	cancel()
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	if contextError != nil {
		return nil, normalizeFrameworkError(contextError)
	}
	if modelError != nil {
		return nil, runtimeFailureFromModel(modelError)
	}
	return state.acceptSummaryMessage(message, reservation.OutputBytes)
}

func (state *runState) acceptSummaryMessage(message *schema.Message, maximum int) (*schema.Message, error) {
	if message == nil || message.Role != "" && message.Role != schema.Assistant || len(message.ToolCalls) != 0 ||
		unsupportedSummaryResponseFields(message) || maximum < 1 || maximum > domain.MaxSessionSummaryBytes ||
		!domain.ValidModelText(message.Content, maximum, false) ||
		message.ResponseMeta != nil && message.ResponseMeta.FinishReason != "" && message.ResponseMeta.FinishReason != "stop" ||
		credentialAppearsInStrings(state.client.credential, message.Content) {
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	processed, err := security.NewRedactor().ProcessLines(message.Content, maximum)
	if errors.Is(err, security.ErrSensitiveOutputBlocked) {
		return nil, failedRuntime(domain.SafeErrorClassSensitiveOutputBlocked, safeSensitiveModelTextBlocked, err)
	}
	if err != nil || processed.Truncated || processed.Value == "" {
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, err)
	}
	state.mu.Lock()
	if state.summaryPlan == nil {
		state.mu.Unlock()
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	state.summaryPlan.degraded = processed.RedactionCount > 0
	state.mu.Unlock()
	return schema.AssistantMessage(processed.Value, nil), nil
}

func unsupportedSummaryResponseFields(message *schema.Message) bool {
	return len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 ||
		len(message.AssistantGenMultiContent) != 0 || message.Name != "" || message.ToolCallID != "" ||
		message.ToolName != "" || message.ReasoningContent != "" || message.Extra != nil ||
		message.ResponseMeta != nil && message.ResponseMeta.LogProbs != nil
}

func (state *runState) finalizeSummary(_ context.Context, original []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
	if state == nil || summary == nil || summary.Role != schema.Assistant ||
		!domain.ValidModelText(summary.Content, domain.MaxSessionSummaryBytes, false) {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	state.mu.Lock()
	if state.summaryPlan == nil || state.summaryPlan.originalCount != len(original) ||
		state.summaryPlan.cutIndex <= 1 || state.summaryPlan.cutIndex >= len(original) {
		state.mu.Unlock()
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	plan := *state.summaryPlan
	state.summaryPlan.text = summary.Content
	state.mu.Unlock()
	final := make([]*schema.Message, 0, 2+len(original)-plan.cutIndex)
	final = append(final, original[0], schema.UserMessage(summaryContextPreamble+summary.Content))
	final = append(final, original[plan.cutIndex:]...)
	return final, nil
}

func (state *runState) finishSummary(ctx context.Context, _ adk.ChatModelAgentState, _ adk.ChatModelAgentState) error {
	state.mu.Lock()
	if state.summaryPlan == nil || state.summaryPlan.text == "" {
		state.mu.Unlock()
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	plan := *state.summaryPlan
	state.summaryPlan = nil
	state.mu.Unlock()
	coverage := state.input.Conversation().Coverage()
	if plan.coveredCount < 2 || plan.coveredCount > len(coverage) {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	digest, coveredBytes, err := domain.SessionContextCoverageDigestItems(coverage[:plan.coveredCount])
	if err != nil {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	generatedAt := state.now()
	summary := domain.SessionContextSummary{
		SessionID:        state.input.SessionID(),
		Text:             plan.text,
		SummaryHash:      domain.SHA256Hex(plan.text),
		SchemaVersion:    domain.SessionContextSummarySchemaVersion,
		PolicyVersion:    domain.SafeConversationContextPolicyVersion,
		CoveredFirstID:   coverage[0].MessageID,
		CoveredThroughID: coverage[plan.coveredCount-1].MessageID,
		CoveredCount:     plan.coveredCount,
		CoveredBytes:     coveredBytes,
		CoverageDigest:   digest,
		GeneratedAt:      generatedAt,
		AgentProfile:     state.profileName,
		AgentOriginHash:  state.originHash,
		Degraded:         plan.degraded,
	}
	if summary.Validate() != nil {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	return state.publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryReady, Summary: &summary})
}

func cloneEinoMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, len(messages))
	for index, message := range messages {
		if message == nil {
			continue
		}
		value := *message
		value.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		result[index] = &value
	}
	return result
}
