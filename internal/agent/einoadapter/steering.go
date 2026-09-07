package einoadapter

import (
	"context"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type steerContextKey struct{}

type claimedSteer struct {
	bridge agent.RunSteeringBridge
	claim  agent.SteerClaim
}

// steeringMiddleware uses the v0.9.19 state rewrite hook only at a real model
// boundary and keeps the commit barrier immediately inside all earlier
// middleware, including summarization.
type steeringMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	state *runState
}

func newSteeringMiddleware(state *runState) adk.ChatModelAgentMiddleware {
	return &steeringMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, state: state}
}

func (middleware *steeringMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if middleware == nil || middleware.state == nil || ctx == nil || state == nil {
		return ctx, state, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	if err := middleware.state.checkScope(ctx); err != nil {
		return ctx, state, err
	}
	bridge := middleware.state.input.Steering()
	if bridge == nil {
		return ctx, state, nil
	}
	claim, found, err := bridge.ClaimSteer(ctx, agent.SteerBoundary{
		RunID: middleware.state.input.RunID(), SessionID: middleware.state.input.SessionID(),
		ScopeGeneration:  middleware.state.input.Scope().Generation,
		PolicyGeneration: middleware.state.input.PolicyGeneration(),
	})
	if err != nil {
		return ctx, state, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	if !found {
		return ctx, state, nil
	}
	if claim.Validate() != nil {
		return ctx, state, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	messages := make([]*schema.Message, len(state.Messages), len(state.Messages)+1)
	copy(messages, state.Messages)
	messages = append(messages, schema.UserMessage(claim.Content))
	state.Messages = messages
	return context.WithValue(ctx, steerContextKey{}, &claimedSteer{bridge: bridge, claim: claim}), state, nil
}

func (middleware *steeringMiddleware) WrapModel(
	_ context.Context,
	model einomodel.BaseChatModel,
	_ *adk.ModelContext,
) (einomodel.BaseChatModel, error) {
	if middleware == nil || middleware.state == nil || model == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	return &steeringModel{state: middleware.state, inner: model}, nil
}

type steeringModel struct {
	state *runState
	inner einomodel.BaseChatModel
}

var _ einomodel.BaseChatModel = (*steeringModel)(nil)

func (model *steeringModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	options ...einomodel.Option,
) (*schema.Message, error) {
	prepared, preparedContext, claimed, err := model.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	message, err := model.inner.Generate(preparedContext, input, options...)
	model.resolveAfterCall(ctx, prepared, claimed, err)
	return message, err
}

func (model *steeringModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	prepared, preparedContext, claimed, err := model.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	stream, err := model.inner.Stream(preparedContext, input, options...)
	model.resolveAfterCall(ctx, prepared, claimed, err)
	return stream, err
}

func (model *steeringModel) prepare(
	ctx context.Context,
	input []*schema.Message,
) (*preparedModelCall, context.Context, *claimedSteer, error) {
	if model == nil || model.state == nil || model.inner == nil || ctx == nil {
		return nil, ctx, nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	claimed, _ := ctx.Value(steerContextKey{}).(*claimedSteer)
	prepared, err := model.state.prepareModelCall(ctx, input)
	if err != nil {
		if claimed != nil {
			// The item was already accepted and claimed for this exact run.
			// A model-preparation failure (including exhausted remaining request
			// budget, cancellation, or stale authority) must preserve it for
			// explicit editing instead of treating it as a new admission reject.
			claimed.bridge.ResolveSteer(context.WithoutCancel(ctx), claimed.claim, agent.SteerResolutionRecovered)
		}
		return nil, ctx, claimed, err
	}
	if claimed != nil {
		if err := claimed.bridge.CommitSteer(ctx, claimed.claim); err != nil {
			return nil, ctx, claimed, failedRuntime(domain.SafeErrorClassPersistenceUnavailable, safeEventRejected, err)
		}
		if err := model.state.recordCommittedSteer(claimed.claim); err != nil {
			return nil, ctx, claimed, err
		}
	}
	return prepared, withPreparedModelCall(ctx, prepared), claimed, nil
}

func (model *steeringModel) resolveAfterCall(ctx context.Context, prepared *preparedModelCall, claimed *claimedSteer, err error) {
	if claimed == nil || prepared == nil || err == nil || !prepared.started.Load() {
		return
	}
	// Commit succeeded before the inner endpoint was entered. A failure after
	// real model I/O began cannot make the durable input editable or
	// automatically retry it.
	claimed.bridge.ResolveSteer(context.WithoutCancel(ctx), claimed.claim, agent.SteerResolutionUnknown)
}
