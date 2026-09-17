package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func (client *modelClient) initResponses() error {
	cfg := client.configuration
	retries, store, parallel := 0, false, false
	var temperature *float32
	if cfg.Temperature != nil {
		value := float32(*cfg.Temperature)
		temperature = &value
	}
	truncation := responses.ResponseNewParamsTruncationDisabled
	config := agenticopenai.ResponsesConfig{
		APIKey: einoCredentialPlaceholder, BaseURL: strings.TrimRight(cfg.Endpoint, "/"), HTTPClient: client.client,
		Model: cfg.Model, Temperature: temperature, MaxRetries: &retries, Store: &store,
		ParallelToolCalls: &parallel, Truncation: &truncation,
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if cfg.MaxOutputTokens > 0 {
		config.MaxTokens = &cfg.MaxOutputTokens
	}
	if cfg.ReasoningEffort != "" {
		config.Reasoning = &responses.ReasoningParam{Effort: responses.ReasoningEffort(cfg.ReasoningEffort)}
	}
	model, err := agenticopenai.NewResponsesModel(context.Background(), &config)
	if err != nil {
		return fmt.Errorf("construct native Responses model: %w", err)
	}
	client.responsesModel = model
	client.structuredResponses = model
	if cfg.ResponseFormat == domain.ModelResponseFormatJSONObject {
		config.Text = &responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}}}
		client.structuredResponses, err = agenticopenai.NewResponsesModel(context.Background(), &config)
	}
	return err
}

// responsesHistory translates only admitted safe text. Current-run protocol
// items remain in Eino state and never round-trip through schema.Message.
func responsesHistory(messages []*schema.Message) []*schema.AgenticMessage {
	result := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case schema.System:
			result = append(result, schema.SystemAgenticMessage(message.Content))
		case schema.User:
			result = append(result, schema.UserAgenticMessage(message.Content))
		case schema.Assistant:
			result = append(result, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: message.Content})}})
		}
	}
	return result
}

func responsesText(message *schema.AgenticMessage) string {
	var text strings.Builder
	if message == nil {
		return ""
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			text.WriteString(block.UserInputText.Text)
		}
		if block.AssistantGenText != nil {
			text.WriteString(block.AssistantGenText.Text)
		}
	}
	return text.String()
}

// responsesFinal admits the native terminal metadata before any Tool binding.
func responsesFinal(message *schema.AgenticMessage, maximum int) (string, []agent.ToolSelection, error) {
	if message == nil || message.Role != schema.AgenticRoleTypeAssistant || message.ResponseMeta == nil || message.ResponseMeta.OpenAIExtension == nil {
		return "", nil, failedAt(domain.FailureStreamIncomplete, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	meta := message.ResponseMeta.OpenAIExtension
	switch meta.Status {
	case "completed":
		if meta.Error != nil || meta.IncompleteDetails != nil {
			return "", nil, failedAt(domain.FailureStopReason, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
	case "incomplete":
		if meta.IncompleteDetails != nil && meta.IncompleteDetails.Reason == "max_output_tokens" {
			return "", nil, localRuntimeStop(agent.RunStopStepLimit, domain.SafeErrorClassBudgetExhausted, "The model response reached its fixed output limit.", nil)
		}
		return "", nil, failedAt(domain.FailureStopReason, domain.SafeErrorClassInvalidExternalResponse, nil)
	case "failed", "cancelled":
		return "", nil, failedAt(domain.FailureProviderReported, domain.SafeErrorClassUnavailable, nil)
	default:
		return "", nil, failedAt(domain.FailureStreamIncomplete, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	if usage := message.ResponseMeta.TokenUsage; usage != nil && !validModelUsage(usage) {
		return "", nil, failedAt(domain.FailureStreamUsage, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	data, err := json.Marshal(message)
	if err != nil || len(data) > maximum {
		return "", nil, failedAt(domain.FailureBudget, domain.SafeErrorClassBudgetExhausted, err)
	}
	calls := make([]agent.ToolSelection, 0)
	for _, block := range message.ContentBlocks {
		if block == nil {
			return "", nil, failedAt(domain.FailureStreamMalformed, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText == nil {
				return "", nil, failedAt(domain.FailureStreamMalformed, domain.SafeErrorClassInvalidExternalResponse, nil)
			}
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning == nil {
				return "", nil, failedAt(domain.FailureStreamMalformed, domain.SafeErrorClassInvalidExternalResponse, nil)
			}
		case schema.ContentBlockTypeFunctionToolCall:
			if block.FunctionToolCall == nil {
				return "", nil, failedAt(domain.FailureToolSelection, domain.SafeErrorClassInvalidExternalResponse, nil)
			}
			call := block.FunctionToolCall
			selection := agent.ToolSelection{ID: call.CallID, Name: domain.ToolName(call.Name), ArgumentsJSON: call.Arguments}
			if selection.Validate() != nil {
				return "", nil, failedAt(domain.FailureToolSelection, domain.SafeErrorClassPolicyDenied, nil)
			}
			calls = append(calls, selection)
		default:
			return "", nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
	}
	if len(calls) > domain.MaxAgentToolCalls {
		return "", nil, failedAt(domain.FailureToolSelection, domain.SafeErrorClassPolicyDenied, nil)
	}
	return responsesText(message), calls, nil
}

func (client *modelClient) generateResponses(ctx context.Context, messages []*schema.Message, invocation domain.ModelInvocation, maximum int) (*schema.Message, error) {
	model := client.responsesModel
	if invocation == domain.ModelInvocationReview {
		model = client.structuredResponses
	}
	message, err := model.Generate(ctx, responsesHistory(messages))
	if err != nil {
		return nil, err
	}
	text, calls, err := responsesFinal(message, domain.MaxModelMessageBytes)
	if err != nil {
		return nil, err
	}
	if len(calls) != 0 || !domain.ValidModelText(text, maximum, false) {
		return nil, errMalformedProviderChunk
	}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if credentialAppearsInBytes(client.credential, data) || responsesContainCredential(client.credential, []*schema.AgenticMessage{message}) {
		return nil, errTransportRequestInvalid
	}
	return schema.AssistantMessage(text, nil), nil
}

type responsesMiddleware struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
	state *runState
	call  *responsesInvocation
}

type responsesInvocation struct {
	requestID    domain.ModelRequestID
	transport    *transportRequestState
	cancel       context.CancelFunc
	claimed      *claimedSteer
	contextError func() error
}

func (middleware *responsesMiddleware) closeCall() {
	if middleware.call != nil {
		middleware.call.cancel()
		middleware.call.transport.closeResponseBody()
		middleware.call = nil
	}
}

func (middleware *responsesMiddleware) BeforeModelRewriteState(ctx context.Context, current *adk.TypedChatModelAgentState[*schema.AgenticMessage], _ *adk.TypedModelContext[*schema.AgenticMessage]) (context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage], error) {
	state := middleware.state
	if err := state.checkScope(ctx); err != nil {
		return ctx, current, err
	}
	if current == nil || len(current.Messages) == 0 || len(current.Messages) > maxConversationMessages || validateBoundToolInfosForMode(current.ToolInfos, state.input.Mode()) != nil {
		return ctx, current, failedAt(domain.FailureRetainedContext, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	var claimed *claimedSteer
	if bridge := state.input.Steering(); bridge != nil {
		claim, found, err := bridge.ClaimSteer(ctx, agent.SteerBoundary{RunID: state.input.RunID(), SessionID: state.input.SessionID(), ScopeGeneration: state.input.Scope().Generation, PolicyGeneration: state.input.PolicyGeneration()})
		if err != nil {
			return ctx, current, normalizeFrameworkError(err)
		}
		if found {
			if claim.Validate() != nil {
				return ctx, current, failedAt(domain.FailureInternal, domain.SafeErrorClassInternal, nil)
			}
			claimed = &claimedSteer{bridge: bridge, claim: claim}
			current.Messages = append(current.Messages, schema.UserAgenticMessage(claim.Content))
		}
	}
	deny := func(err error) (context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage], error) {
		if claimed != nil {
			claimed.bridge.ResolveSteer(context.WithoutCancel(ctx), claimed.claim, agent.SteerResolutionRecovered)
		}
		return ctx, current, err
	}
	if err := state.beginStep(ctx); err != nil {
		return deny(err)
	}
	reservation, err := state.budget.ReserveModelCall(ctx)
	if err != nil {
		return deny(runtimeFailureFromBudget(err))
	}
	id, err := state.nextModelRequestID()
	if err != nil {
		return deny(err)
	}
	data, err := json.Marshal(current.Messages)
	if err != nil || len(data) > reservation.RequestBytes {
		return deny(failedAt(domain.FailureBudget, domain.SafeErrorClassBudgetExhausted, err))
	}
	if credentialAppearsInBytes(state.client.credential, data) || responsesContainCredential(state.client.credential, current.Messages) {
		return deny(failedAt(domain.FailureSensitiveOutput, domain.SafeErrorClassSensitiveOutputBlocked, nil))
	}
	if claimed != nil {
		if err := claimed.bridge.CommitSteer(ctx, claimed.claim); err != nil {
			return ctx, current, failedAt(domain.FailurePersistence, domain.SafeErrorClassPersistenceUnavailable, err)
		}
		if err := state.recordCommittedSteer(claimed.claim); err != nil {
			return ctx, current, err
		}
	}
	manifest, err := state.runInputManifest()
	if err != nil {
		return ctx, current, err
	}
	preflight := agent.ModelCallPreflight{Kind: agent.ModelCallAgent, MessageCount: len(current.Messages), MessageBytes: len(data), CurrentInputCount: manifest.Count, CurrentInputDigest: manifest.Digest, ReservedRequestBytes: reservation.RequestBytes, ReservedOutputBytes: reservation.OutputBytes, ReservedStreamBytes: reservation.StreamBytes, ReservedNanoseconds: reservation.Timeout.Nanoseconds(), ReservedCostUnits: reservation.CostUnits}
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventModelStreamStarted, ModelRequestID: &id, ModelPreflight: &preflight}); err != nil {
		return ctx, current, err
	}
	if err := state.checkScope(ctx); err != nil {
		return ctx, current, err
	}
	callCtx, cancel := context.WithTimeout(ctx, reservation.Timeout)
	transport := &transportRequestState{requestLimit: reservation.RequestBytes, responseLimit: reservation.StreamBytes, responseMode: transportResponseJSON}
	state.client.logger.Info(modelRequestLogEvent, "component", "model", "operation", string(domain.ModelOperationRequest), "phase", "started", "request_id", string(id))
	middleware.call = &responsesInvocation{requestID: id, transport: transport, cancel: cancel, claimed: claimed, contextError: callCtx.Err}
	return context.WithValue(callCtx, transportRequestStateKey{}, transport), current, nil
}

func (middleware *responsesMiddleware) AfterModelRewriteState(ctx context.Context, current *adk.TypedChatModelAgentState[*schema.AgenticMessage], _ *adk.TypedModelContext[*schema.AgenticMessage]) (context.Context, *adk.TypedChatModelAgentState[*schema.AgenticMessage], error) {
	defer middleware.closeCall()
	if err := middleware.state.checkScope(ctx); err != nil {
		return ctx, current, err
	}
	if current == nil || len(current.Messages) == 0 || middleware.call == nil {
		return ctx, current, failedAt(domain.FailureInternal, domain.SafeErrorClassInternal, nil)
	}
	message := current.Messages[len(current.Messages)-1]
	data, marshalErr := json.Marshal(message)
	if marshalErr != nil {
		return ctx, current, failedAt(domain.FailureStreamMalformed, domain.SafeErrorClassInvalidExternalResponse, marshalErr)
	}
	if credentialAppearsInBytes(middleware.state.client.credential, data) || responsesContainCredential(middleware.state.client.credential, []*schema.AgenticMessage{message}) {
		return ctx, current, failedAt(domain.FailureSensitiveOutput, domain.SafeErrorClassSensitiveOutputBlocked, nil)
	}
	_, calls, err := responsesFinal(message, domain.MaxModelMessageBytes)
	if err != nil {
		return ctx, current, err
	}
	middleware.state.client.logFinished(middleware.call.requestID, nil, modelFailure{httpStatus: middleware.call.transport.status()}, nil, middleware.call.transport)
	if len(calls) > 0 {
		err = middleware.state.bindToolCalls(ctx, calls)
	}
	if err != nil {
		return ctx, current, err
	}
	// The next model boundary must use the run context, not the expired request timer.
	return ctx, current, nil
}

func (state *runState) runResponsesAgent(ctx context.Context, initial []*schema.Message) (*schema.Message, error) {
	tools, err := newEinoTools(state)
	if err != nil {
		return nil, err
	}
	middleware := &responsesMiddleware{TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{}, state: state}
	defer middleware.closeCall()
	summary, err := state.responsesSummarization(ctx)
	if err != nil {
		return nil, err
	}
	production, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{Name: "kupilot-agent", Instruction: initial[0].Content, Model: state.client.structuredResponses, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}, MaxIterations: state.input.BudgetLimits().ModelCalls, Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{summary, middleware}})
	if err != nil {
		return nil, normalizeFrameworkError(err)
	}
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{Agent: production, EnableStreaming: false})
	iterator := runner.Run(ctx, responsesHistory(initial[1:]))
	var final *schema.AgenticMessage
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			return nil, failedAt(domain.FailureInternal, domain.SafeErrorClassInternal, nil)
		}
		if event.Err != nil {
			var failure *runtimeFailure
			if errors.As(event.Err, &failure) {
				return nil, failure
			}
			if call := middleware.call; call != nil {
				cause := event.Err
				if call.contextError() != nil {
					cause = call.contextError()
				}
				if call.claimed != nil && call.transport.transportEntered() {
					call.claimed.bridge.ResolveSteer(context.WithoutCancel(ctx), call.claimed.claim, agent.SteerResolutionUnknown)
				}
				mapped := state.client.finishWithError(call.requestID, mapModelRequestError(ctx, cause, call.transport), domain.ModelOperationStream, cause, call.transport)
				return nil, runtimeFailureFromModel(mapped)
			}
			return nil, normalizeFrameworkError(event.Err)
		}
		if event.Action != nil || event.Output == nil || event.Output.MessageOutput == nil || event.Output.CustomizedOutput != nil {
			return nil, failedAt(domain.FailureInternal, domain.SafeErrorClassInternal, nil)
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, normalizeFrameworkError(err)
		}
		if message != nil && message.Role == schema.AgenticRoleTypeAssistant {
			final = message
		}
	}
	if final == nil {
		return nil, failedAt(domain.FailureFinalShape, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	text, calls, err := responsesFinal(final, domain.MaxModelMessageBytes)
	if err != nil {
		return nil, err
	}
	if len(calls) != 0 {
		return nil, failedAt(domain.FailureToolPairing, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
	return schema.AssistantMessage(text, nil), nil
}

func (state *runState) responsesSummarization(ctx context.Context) (adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage], error) {
	// Coverage selection uses only the safe retained prefix. Eino's native tail,
	// including reasoning and Tool pairs, is kept intact by the finalizer.
	return summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{
		Model:   &responsesSummaryModel{state: state},
		Trigger: &summarization.TriggerCondition{ContextMessages: domain.SessionContextMessageTrigger, ContextTokens: domain.MaxSessionContextBytes},
		TokenCounter: func(ctx context.Context, input *summarization.TypedTokenCounterInput[*schema.AgenticMessage]) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			total := 0
			for _, msg := range input.Messages {
				total += len(responsesText(msg))
			}
			return total, nil
		},
		GenModelInput: func(ctx context.Context, system, user *schema.AgenticMessage, original []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
			safe := make([]*schema.Message, len(original))
			for i, msg := range original {
				safe[i] = &schema.Message{Role: schema.RoleType(msg.Role), Content: responsesText(msg)}
			}
			selected, err := state.summaryModelInput(ctx, schema.SystemMessage(responsesText(system)), schema.UserMessage(responsesText(user)), safe)
			if err != nil {
				return nil, err
			}
			return responsesHistory(selected), nil
		},
		Finalize: func(_ context.Context, original []*schema.AgenticMessage, summary *schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.summaryPlan == nil || state.summaryPlan.originalCount != len(original) || state.summaryPlan.cutIndex >= len(original) {
				return nil, failedAt(domain.FailureSummaryResponse, domain.SafeErrorClassInternal, nil)
			}
			state.summaryPlan.text = responsesText(summary)
			result := []*schema.AgenticMessage{original[0], schema.UserAgenticMessage(summaryContextPreamble + state.summaryPlan.text)}
			return append(result, original[state.summaryPlan.cutIndex:]...), nil
		},
		Callback: func(ctx context.Context, _, _ adk.TypedChatModelAgentState[*schema.AgenticMessage]) error {
			return state.finishSummary(ctx, adk.ChatModelAgentState{}, adk.ChatModelAgentState{})
		},
	})
}

type responsesSummaryModel struct{ state *runState }

func (model *responsesSummaryModel) Generate(ctx context.Context, input []*schema.AgenticMessage, _ ...einomodel.Option) (*schema.AgenticMessage, error) {
	safe := make([]*schema.Message, len(input))
	for i, msg := range input {
		safe[i] = &schema.Message{Role: schema.RoleType(msg.Role), Content: responsesText(msg)}
	}
	message, err := model.state.callSummaryModel(ctx, safe)
	if err != nil {
		return nil, err
	}
	return responsesHistory([]*schema.Message{message})[0], nil
}
func (model *responsesSummaryModel) Stream(ctx context.Context, input []*schema.AgenticMessage, options ...einomodel.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	message, err := model.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{message}), nil
}

func responsesContainCredential(credential *config.SecretValue, messages []*schema.AgenticMessage) bool {
	for _, message := range messages {
		if message == nil {
			return true
		}
		if credentialAppearsInStrings(credential, responsesText(message)) {
			return true
		}
		for _, block := range message.ContentBlocks {
			if block == nil {
				return true
			}
			if block.Reasoning != nil && credentialAppearsInStrings(credential, block.Reasoning.Text, block.Reasoning.Signature) {
				return true
			}
			if block.FunctionToolCall != nil && credentialAppearsInStrings(credential, block.FunctionToolCall.CallID, block.FunctionToolCall.Name, block.FunctionToolCall.Arguments) {
				return true
			}
		}
	}
	return false
}
