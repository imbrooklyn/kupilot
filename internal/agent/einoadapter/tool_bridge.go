package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type toolBridge struct {
	state *runState
	name  domain.ToolName
	info  *schema.ToolInfo
}

var _ einotool.InvokableTool = (*toolBridge)(nil)

func newEinoTools(state *runState) ([]einotool.BaseTool, error) {
	specifications := agent.ToolSpecifications()
	tools := make([]einotool.BaseTool, len(specifications))
	for index, specification := range specifications {
		info, err := toolInfo(specification)
		if err != nil {
			return nil, err
		}
		tools[index] = &toolBridge{state: state, name: specification.Name, info: info}
	}
	return tools, nil
}

func (bridge *toolBridge) Info(ctx context.Context) (*schema.ToolInfo, error) {
	if bridge == nil || bridge.state == nil || bridge.info == nil || ctx == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, normalizeFrameworkError(err)
	}
	encoded, err := json.Marshal(bridge.info)
	if err != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	var copied schema.ToolInfo
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	return &copied, nil
}

func (bridge *toolBridge) InvokableRun(ctx context.Context, argumentsInJSON string, options ...einotool.Option) (string, error) {
	if bridge == nil || bridge.state == nil {
		return "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	failBatch := func(err error) (string, error) {
		bridge.state.abortToolBatch(err)
		return "", err
	}
	if ctx == nil || len(options) != 0 {
		return failBatch(failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil))
	}
	callID := compose.GetToolCallID(ctx)
	if callID == "" {
		return failBatch(failedRuntime(domain.SafeErrorClassPolicyDenied, "The cluster-read request correlation is invalid.", nil))
	}
	if err := bridge.state.toolBatchAbort(); err != nil {
		return "", err
	}
	result, err := bridge.state.executeTool(ctx, bridge.name, callID, argumentsInJSON)
	if err != nil {
		bridge.state.abortToolBatch(err)
	}
	return result, err
}

type toolInfoWire struct {
	Name           string          `json:"name"`
	Desc           string          `json:"desc"`
	HasParamsOneOf bool            `json:"has_params_one_of"`
	JSONSchema     json.RawMessage `json:"json_schema"`
}

func toolInfo(specification agent.ToolSpecification) (*schema.ToolInfo, error) {
	if specification.Validate() != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	encoded, err := json.Marshal(toolInfoWire{
		Name:           string(specification.Name),
		Desc:           specification.Description,
		HasParamsOneOf: true,
		JSONSchema:     json.RawMessage(specification.InputSchemaJSON),
	})
	if err != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	var info schema.ToolInfo
	if err := json.Unmarshal(encoded, &info); err != nil || info.ParamsOneOf == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	return &info, nil
}

func validateBoundToolInfos(infos []*schema.ToolInfo) error {
	specifications := agent.ToolSpecifications()
	if len(infos) != len(specifications) {
		return agent.ErrToolPolicyDenied
	}
	for index, specification := range specifications {
		info := infos[index]
		if info == nil || info.Name != string(specification.Name) || info.Desc != specification.Description ||
			info.Extra != nil || info.ParamsOneOf == nil {
			return agent.ErrToolPolicyDenied
		}
		jsonSchema, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return agent.ErrToolPolicyDenied
		}
		encoded, err := json.Marshal(jsonSchema)
		if err != nil || !sameJSON(string(encoded), specification.InputSchemaJSON) {
			return agent.ErrToolPolicyDenied
		}
	}
	return nil
}

func sameJSON(left, right string) bool {
	decode := func(value string) (any, error) {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	leftValue, leftError := decode(left)
	rightValue, rightError := decode(right)
	return leftError == nil && rightError == nil && reflect.DeepEqual(leftValue, rightValue)
}

func (state *runState) bindToolCalls(ctx context.Context, selections []agent.ToolSelection) error {
	if len(selections) == 0 {
		return failedRuntime(domain.SafeErrorClassPolicyDenied, "The model requested an empty cluster-read batch.", nil)
	}
	if err := state.checkScope(ctx); err != nil {
		return err
	}
	state.clearToolBatchAbort()
	state.mu.Lock()
	startSequence := state.toolSequence
	state.mu.Unlock()
	if startSequence+len(selections) > domain.MaxAgentToolCalls {
		return failedRuntime(domain.SafeErrorClassBudgetExhausted, "The model requested more cluster reads than the diagnostic run can represent.", nil)
	}
	entries := make([]*boundExecution, len(selections))
	canonicalSelections := make([]agent.ToolSelection, len(selections))
	policyFeedbackNeeded := false
	seen := make(map[string]struct{}, len(selections))
	seenInvocationIDs := make(map[domain.ToolInvocationID]struct{}, len(selections))
	for index, selection := range selections {
		if _, duplicate := seen[selection.ID]; duplicate {
			return failedRuntime(domain.SafeErrorClassPolicyDenied, "The model repeated a cluster-read request identifier.", nil)
		}
		seen[selection.ID] = struct{}{}
		invocationID, err := state.identifiers.NewToolInvocationID()
		if err != nil || !invocationID.Valid() {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
		}
		if _, duplicate := seenInvocationIDs[invocationID]; duplicate {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		seenInvocationIDs[invocationID] = struct{}{}
		call, err := agent.BindToolCall(state.input, invocationID, selection)
		if err != nil {
			if errors.Is(err, agent.ErrSensitiveModelTextBlocked) {
				return failedRuntime(domain.SafeErrorClassSensitiveOutputBlocked, safeSensitiveModelTextBlocked, err)
			}
			if errors.Is(err, agent.ErrToolArgumentsRejected) {
				return failedRuntime(domain.SafeErrorClassPolicyDenied, "The model requested a cluster read outside the fixed policy.", err)
			}
			if errors.Is(err, agent.ErrToolPolicyDenied) {
				policyFeedbackNeeded = true
				continue
			}
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
		}
		safeSelection := agent.ToolSelection{
			ID: selection.ID, Name: call.Name(), ArgumentsJSON: call.ArgumentsJSON(),
		}
		canonicalSelections[index] = safeSelection
		requestedAt := state.now()
		if !validRuntimeTime(requestedAt) {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		purpose := call.Purpose()
		identity := call.Identity()
		invocation := domain.ToolInvocation{
			ID:              call.InvocationID(),
			RunID:           call.RunID(),
			Sequence:        startSequence + index + 1,
			Name:            call.Name(),
			Version:         call.Version(),
			Purpose:         &purpose,
			Scope:           call.Scope().Snapshot(),
			ArgumentsJSON:   call.ArgumentsJSON(),
			ArgumentsDigest: identity.ArgumentsDigest,
			Status:          domain.ToolInvocationStatusRequested,
			StartedAt:       &requestedAt,
		}
		if invocation.Validate() != nil {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		entries[index] = &boundExecution{
			call:      call,
			requested: invocation,
			toolName:  call.Name(),
			modelCall: safeSelection,
		}
	}
	if policyFeedbackNeeded {
		feedback, err := agent.BuildToolPolicyFeedback()
		if err != nil {
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
		}
		for index, selection := range selections {
			entries[index] = &boundExecution{
				toolName:       selection.Name,
				modelCall:      selection,
				policyFeedback: feedback,
			}
		}
		state.mu.Lock()
		if state.toolSequence != startSequence {
			state.mu.Unlock()
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		for _, execution := range entries {
			if _, exists := state.boundCalls[execution.modelCall.ID]; exists {
				state.mu.Unlock()
				return failedRuntime(domain.SafeErrorClassPolicyDenied, "The model repeated a cluster-read request identifier.", nil)
			}
		}
		for _, execution := range entries {
			state.boundCalls[execution.modelCall.ID] = execution
		}
		state.mu.Unlock()
		return state.checkScope(ctx)
	}
	copy(selections, canonicalSelections)

	state.mu.Lock()
	if state.toolSequence != startSequence {
		state.mu.Unlock()
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	for _, execution := range entries {
		if _, exists := state.boundCalls[execution.modelCall.ID]; exists {
			state.mu.Unlock()
			return failedRuntime(domain.SafeErrorClassPolicyDenied, "The model repeated a cluster-read request identifier.", nil)
		}
		if _, exists := state.toolInvocationIDs[execution.call.InvocationID()]; exists {
			state.mu.Unlock()
			return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
	}
	for _, execution := range entries {
		state.boundCalls[execution.modelCall.ID] = execution
		state.toolInvocationIDs[execution.call.InvocationID()] = struct{}{}
	}
	state.toolSequence += len(entries)
	state.mu.Unlock()
	for _, execution := range entries {
		invocation := execution.requested
		if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallRequested, ToolInvocation: &invocation}); err != nil {
			return err
		}
	}
	return state.checkScope(ctx)
}

func (state *runState) executeTool(ctx context.Context, name domain.ToolName, callID, arguments string) (string, error) {
	state.mu.Lock()
	execution := state.boundCalls[callID]
	if execution == nil || execution.executed || execution.toolName != name {
		state.mu.Unlock()
		return "", failedRuntime(domain.SafeErrorClassPolicyDenied, "The cluster-read request is not bound to this diagnostic run.", nil)
	}
	execution.executed = true
	policyFeedback := execution.policyFeedback
	modelCall := execution.modelCall
	state.mu.Unlock()
	if policyFeedback != "" {
		if modelCall.ID != callID || modelCall.Name != name || modelCall.ArgumentsJSON != arguments {
			return "", failedRuntime(domain.SafeErrorClassPolicyDenied, "The rejected cluster-read arguments changed before local policy feedback.", nil)
		}
		if err := state.checkScope(ctx); err != nil {
			return "", err
		}
		return policyFeedback, nil
	}

	rebound, err := agent.BindToolCall(state.input, execution.call.InvocationID(), agent.ToolSelection{
		ID:            callID,
		Name:          name,
		ArgumentsJSON: arguments,
	})
	if err != nil || rebound.Identity() != execution.call.Identity() ||
		rebound.ArgumentsJSON() != execution.call.ArgumentsJSON() || rebound.ModelCallID() != execution.call.ModelCallID() {
		return "", state.failTool(ctx, execution, domain.SafeErrorClassPolicyDenied, "The cluster-read arguments changed after policy binding.", err)
	}
	if err := state.checkScope(ctx); err != nil {
		return "", state.failToolForRuntime(ctx, execution, err)
	}
	reservation, err := state.budget.ReserveToolCall(ctx, execution.call)
	if err != nil {
		failure := runtimeFailureFromBudget(err)
		return "", state.failToolForRuntime(ctx, execution, failure)
	}
	running := execution.requested
	running.Status = domain.ToolInvocationStatusRunning
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallStarted, ToolInvocation: &running}); err != nil {
		return "", err
	}
	if err := state.checkScope(ctx); err != nil {
		return "", state.failToolForRuntime(ctx, execution, err)
	}
	handler, err := state.tools.Resolve(name)
	if err != nil {
		return "", state.failTool(ctx, execution, domain.SafeErrorClassPolicyDenied, "The requested cluster read is not admitted by the fixed catalog.", err)
	}
	toolCtx, cancel := context.WithTimeout(ctx, reservation.Timeout)
	result := handler.Execute(toolCtx, execution.call)
	toolContextError := toolCtx.Err()
	cancel()
	if err := state.checkScope(ctx); err != nil {
		return "", state.failToolForRuntime(ctx, execution, err)
	}
	if toolContextError != nil {
		return "", state.failToolForRuntime(ctx, execution, normalizeFrameworkError(toolContextError))
	}
	if result.Validate() != nil || result.InvocationID != execution.call.InvocationID() || result.Name != execution.call.Name() ||
		result.Version != execution.call.Version() || result.Scope != execution.call.Scope().Snapshot() {
		return "", state.failTool(ctx, execution, domain.SafeErrorClassInvalidExternalResponse, safeInvalidToolResult, nil)
	}
	content, resultBytes, err := agent.BuildToolResultContent(result)
	if err != nil {
		return "", state.failTool(ctx, execution, domain.SafeErrorClassInvalidExternalResponse, safeInvalidToolResult, err)
	}
	if err := state.budget.CompleteToolCall(execution.call, agent.ToolCallOutcome{ResultBytes: resultBytes, Retryable: result.Retryable()}); err != nil {
		failure := runtimeFailureFromBudget(err)
		return "", state.failToolForRuntime(ctx, execution, failure)
	}
	newEvidence, err := state.registry.AcceptToolResult(execution.call, result)
	if err != nil {
		return "", state.failTool(ctx, execution, domain.SafeErrorClassInvalidExternalResponse, safeInvalidToolResult, err)
	}
	terminal, kind, err := state.completedToolInvocation(execution, result, resultBytes)
	if err != nil {
		return "", err
	}
	if err := state.publish(ctx, agent.RunEvent{Kind: kind, ToolInvocation: &terminal}); err != nil {
		return "", err
	}
	for index := range result.Evidence {
		evidence := result.Evidence[index]
		if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventEvidenceCollected, Evidence: &evidence}); err != nil {
			return "", err
		}
	}
	if err := state.addStepEvidence(newEvidence); err != nil {
		return "", err
	}
	return content, nil
}

func (state *runState) completedToolInvocation(execution *boundExecution, result domain.ToolResult, resultBytes int) (domain.ToolInvocation, agent.RunEventKind, error) {
	finishedAt := state.now()
	if !validRuntimeTime(finishedAt) {
		return domain.ToolInvocation{}, "", failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	invocation := execution.requested
	invocation.FinishedAt = &finishedAt
	invocation.ReturnedBytes = resultBytes
	invocation.EvidenceCount = len(result.Evidence)
	invocation.Truncated = result.Truncation.Truncated
	kind := agent.RunEventToolCallCompleted
	switch result.Status {
	case domain.ToolResultStatusSuccess, domain.ToolResultStatusPartial:
		invocation.Status = domain.ToolInvocationStatusSucceeded
	case domain.ToolResultStatusError:
		invocation.Status = domain.ToolInvocationStatusFailed
		invocation.ErrorClass = &result.Error.Class
		invocation.SafeError = &result.Error.SafeMessage
		kind = agent.RunEventToolCallFailed
	case domain.ToolResultStatusDenied:
		invocation.Status = domain.ToolInvocationStatusDenied
		invocation.ErrorClass = &result.Error.Class
		invocation.SafeError = &result.Error.SafeMessage
		kind = agent.RunEventToolCallDenied
	default:
		return domain.ToolInvocation{}, "", failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidToolResult, nil)
	}
	if invocation.Validate() != nil {
		return domain.ToolInvocation{}, "", failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidToolResult, nil)
	}
	return invocation, kind, nil
}

func (state *runState) failToolForRuntime(ctx context.Context, execution *boundExecution, err error) error {
	failure := normalizeFailure(ctx, err)
	return state.failTool(ctx, execution, failure.class, failure.safeMessage, failure)
}

func (state *runState) failTool(ctx context.Context, execution *boundExecution, class domain.SafeErrorClass, message string, cause error) error {
	finishedAt := state.now()
	if !validRuntimeTime(finishedAt) {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, cause)
	}
	invocation := execution.requested
	invocation.FinishedAt = &finishedAt
	invocation.ErrorClass = &class
	invocation.SafeError = &message
	kind := agent.RunEventToolCallFailed
	switch class {
	case domain.SafeErrorClassCancelled:
		invocation.Status = domain.ToolInvocationStatusCancelled
	case domain.SafeErrorClassPolicyDenied, domain.SafeErrorClassBudgetExhausted:
		invocation.Status = domain.ToolInvocationStatusDenied
		kind = agent.RunEventToolCallDenied
	default:
		invocation.Status = domain.ToolInvocationStatusFailed
	}
	if invocation.Validate() != nil {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, cause)
	}
	eventCtx := ctx
	if ctx.Err() != nil {
		eventCtx = context.WithoutCancel(ctx)
	}
	if err := state.publish(eventCtx, agent.RunEvent{Kind: kind, ToolInvocation: &invocation}); err != nil {
		return err
	}
	var failure *runtimeFailure
	if errors.As(cause, &failure) {
		return failure
	}
	return failedRuntime(class, message, cause)
}
