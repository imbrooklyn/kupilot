package einoadapter

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const testSteerID domain.MessageID = "00000000-0000-7000-8000-000000008010"

type recordedSteerResolution struct {
	claim      agent.SteerClaim
	resolution agent.SteerResolution
}

type testSteeringBridge struct {
	mu          sync.Mutex
	want        agent.SteerBoundary
	claim       agent.SteerClaim
	offered     bool
	claimed     bool
	claimCalls  []agent.SteerBoundary
	commits     []agent.SteerClaim
	resolutions []recordedSteerResolution
	commitErr   error
	onCommit    func()
}

type multiSteeringBridge struct {
	mu          sync.Mutex
	want        agent.SteerBoundary
	claims      []agent.SteerClaim
	next        int
	commits     []agent.SteerClaim
	resolutions []recordedSteerResolution
}

func (bridge *multiSteeringBridge) ClaimSteer(
	ctx context.Context,
	boundary agent.SteerBoundary,
) (agent.SteerClaim, bool, error) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.SteerClaim{}, false, err
	}
	if boundary != bridge.want {
		return agent.SteerClaim{}, false, agent.ErrInvalidSteeringContract
	}
	if bridge.next >= len(bridge.claims) {
		return agent.SteerClaim{}, false, nil
	}
	claim := bridge.claims[bridge.next]
	bridge.next++
	return claim, true, nil
}

func (bridge *multiSteeringBridge) CommitSteer(ctx context.Context, claim agent.SteerClaim) error {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	bridge.commits = append(bridge.commits, claim)
	return nil
}

func (bridge *multiSteeringBridge) ResolveSteer(
	_ context.Context,
	claim agent.SteerClaim,
	resolution agent.SteerResolution,
) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	bridge.resolutions = append(bridge.resolutions, recordedSteerResolution{claim: claim, resolution: resolution})
}

func (bridge *multiSteeringBridge) snapshot() ([]agent.SteerClaim, []recordedSteerResolution) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return append([]agent.SteerClaim(nil), bridge.commits...),
		append([]recordedSteerResolution(nil), bridge.resolutions...)
}

func newTestSteeringBridge(input agent.RunInput, text string) *testSteeringBridge {
	return &testSteeringBridge{
		want: agent.SteerBoundary{
			RunID: input.RunID(), SessionID: input.SessionID(),
			ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
		},
		claim: agent.SteerClaim{
			ItemID: testSteerID, RunID: input.RunID(), SessionID: input.SessionID(),
			ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
			RunSequence: 1, Content: text, ContentHash: domain.MessageContentHash(text),
		},
	}
}

func (bridge *testSteeringBridge) Offer() {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	bridge.offered = true
	bridge.claimed = false
}

func (bridge *testSteeringBridge) ClaimSteer(
	ctx context.Context,
	boundary agent.SteerBoundary,
) (agent.SteerClaim, bool, error) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	bridge.claimCalls = append(bridge.claimCalls, boundary)
	if err := ctx.Err(); err != nil {
		return agent.SteerClaim{}, false, err
	}
	if boundary != bridge.want {
		return agent.SteerClaim{}, false, agent.ErrInvalidSteeringContract
	}
	if !bridge.offered || bridge.claimed {
		return agent.SteerClaim{}, false, nil
	}
	bridge.claimed = true
	return bridge.claim, true, nil
}

func (bridge *testSteeringBridge) CommitSteer(ctx context.Context, claim agent.SteerClaim) error {
	bridge.mu.Lock()
	if err := ctx.Err(); err != nil {
		bridge.mu.Unlock()
		return err
	}
	bridge.commits = append(bridge.commits, claim)
	onCommit := bridge.onCommit
	err := bridge.commitErr
	bridge.mu.Unlock()
	if onCommit != nil {
		onCommit()
	}
	return err
}

func (bridge *testSteeringBridge) ResolveSteer(
	_ context.Context,
	claim agent.SteerClaim,
	resolution agent.SteerResolution,
) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	bridge.resolutions = append(bridge.resolutions, recordedSteerResolution{claim: claim, resolution: resolution})
}

func (bridge *testSteeringBridge) snapshot() (int, []agent.SteerClaim, []recordedSteerResolution, bool) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return len(bridge.claimCalls), append([]agent.SteerClaim(nil), bridge.commits...),
		append([]recordedSteerResolution(nil), bridge.resolutions...), bridge.offered && !bridge.claimed
}

func withTestSteering(t *testing.T, input agent.RunInput, bridge agent.RunSteeringBridge) agent.RunInput {
	t.Helper()
	bound, err := agent.WithRunSteering(input, bridge)
	if err != nil {
		t.Fatalf("WithRunSteering() error = %v", err)
	}
	return bound
}

func countUserContent(messages []*schema.Message, content string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.User && message.Content == content {
			count++
		}
	}
	return count
}

func TestSteerCommitsBeforeModelIOAndEntersBoundaryExactlyOnce(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	const text = "Also check whether the Pod recently restarted."
	model := &recordingModel{scripts: []modelScript{scriptedChunks(diagnosisChunks(
		`{"answer_markdown":"The additional question was included once.","evidence_citations":[],"proposed_actions":[]}`,
	)...)}}
	bridge := newTestSteeringBridge(input, text)
	bridge.Offer()
	bridge.onCommit = func() {
		if calls := len(model.Requests()); calls != 0 {
			t.Fatalf("model calls before commit barrier = %d, want 0", calls)
		}
	}
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	requests := model.Requests()
	claims, commits, resolutions, pending := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusCompleted || len(requests) != 1 ||
		countUserContent(requests[0].Messages, text) != 1 || claims != 1 || len(commits) != 1 ||
		len(resolutions) != 0 || pending {
		t.Fatalf("outcome/requests/bridge = %#v/%#v/%d/%#v/%#v/%t", outcome, requests, claims, commits, resolutions, pending)
	}
}

func TestSteerCommitFailureMakesZeroModelCalls(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	model := &recordingModel{scripts: []modelScript{scriptedChunks(diagnosisChunks(
		`{"answer_markdown":"This response must not be requested.","evidence_citations":[],"proposed_actions":[]}`,
	)...)}}
	bridge := newTestSteeringBridge(input, "Include one more bounded check.")
	bridge.commitErr = errors.New("synthetic durable commit rejection")
	bridge.Offer()
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	claims, commits, _, _ := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassPersistenceUnavailable || len(model.Requests()) != 0 ||
		claims != 1 || len(commits) != 1 {
		t.Fatalf("outcome/model/bridge = %#v/%d/%d/%#v", outcome, len(model.Requests()), claims, commits)
	}
}

func TestSteerOverRemainingRequestBytesIsRecoveredBeforeCommitOrModelIO(t *testing.T) {
	clock := newTestClock()
	const text = "This correction does not fit in the remaining request bytes."
	limits := agent.DefaultRunBudgetLimits()
	baseline := testInput(t, clock, limits)
	messages, err := newInitialMessages(baseline)
	if err != nil {
		t.Fatalf("newInitialMessages() error = %v", err)
	}
	limits.ModelRequestBytes = minimumModelPayloadBytes(messages) + len(schema.User) + len(text) - 1
	input := testInput(t, clock, limits)
	bridge := newTestSteeringBridge(input, text)
	bridge.Offer()
	input = withTestSteering(t, input, bridge)
	model := &recordingModel{scripts: []modelScript{scriptedChunks(diagnosisChunks(
		`{"answer_markdown":"This response must not be requested.","evidence_citations":[],"proposed_actions":[]}`,
	)...)}}

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	claims, commits, resolutions, pending := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassBudgetExhausted || len(model.Requests()) != 0 ||
		claims != 1 || len(commits) != 0 || len(resolutions) != 1 ||
		resolutions[0].resolution != agent.SteerResolutionRecovered || pending {
		t.Fatalf("outcome/model/bridge = %#v/%d/%d/%#v/%#v/%t",
			outcome, len(model.Requests()), claims, commits, resolutions, pending)
	}
}

func TestSteerTransportFailureAfterCommitIsUnknownWithoutRetry(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	model := &recordingModel{scripts: []modelScript{func(_ context.Context, request recordedModelRequest) ([]*schema.Message, error) {
		return nil, domain.NewModelError(domain.ModelErrorCodeServiceUnavailable, domain.ModelOperationRequest, string(request.ID))
	}}}
	bridge := newTestSteeringBridge(input, "Check recent restart state too.")
	bridge.Offer()
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	_, commits, resolutions, _ := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusFailed || len(model.Requests()) != 1 || len(commits) != 1 ||
		len(resolutions) != 1 || resolutions[0].resolution != agent.SteerResolutionUnknown ||
		resolutions[0].claim.ItemID != testSteerID {
		t.Fatalf("outcome/model/bridge = %#v/%d/%#v/%#v", outcome, len(model.Requests()), commits, resolutions)
	}
}

func TestSteerArrivingDuringActiveWorkWaitsForNextModelBoundary(t *testing.T) {
	tests := []struct {
		name        string
		offerDuring string
	}{
		{name: "model stream", offerDuring: "model"},
		{name: "Tool execution", offerDuring: "tool"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			input := testInput(t, clock, agent.DefaultRunBudgetLimits())
			const text = "Check the restart count at the next safe boundary."
			bridge := newTestSteeringBridge(input, text)
			model := &recordingModel{scripts: []modelScript{
				func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
					if countUserContent(request.Messages, text) != 0 {
						t.Fatal("steer modified the already-started model request")
					}
					if current.offerDuring == "model" {
						bridge.Offer()
					}
					return scriptedChunks(toolCallChunks(resourceCall("call-steer", "sample-pod"))...)(ctx, request)
				},
				func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
					if countUserContent(request.Messages, text) != 1 {
						t.Fatalf("next-boundary steer count = %d, want 1", countUserContent(request.Messages, text))
					}
					pair := -1
					for index := 0; index+1 < len(request.Messages); index++ {
						if len(request.Messages[index].ToolCalls) == 1 && request.Messages[index].ToolCalls[0].ID == "call-steer" {
							pair = index
							break
						}
					}
					if pair < 0 || request.Messages[pair+1].Role != schema.Tool ||
						request.Messages[pair+1].ToolCallID != "call-steer" || request.Messages[len(request.Messages)-1].Content != text {
						t.Fatalf("Tool pair and steer order = %#v", request.Messages)
					}
					return scriptedChunks(diagnosisChunks(
						`{"answer_markdown":"The next boundary included the input.","evidence_citations":[],"proposed_actions":[]}`,
					)...)(ctx, request)
				},
			}}
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				if current.offerDuring == "tool" {
					bridge.Offer()
				}
				return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"phase":"Running"}`)
			}}
			input = withTestSteering(t, input, bridge)

			outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(
				context.Background(), input, newEventRecorder(),
			)
			claims, commits, resolutions, pending := bridge.snapshot()
			if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != 2 || len(tool.Calls()) != 1 ||
				claims != 2 || len(commits) != 1 || len(resolutions) != 0 || pending {
				t.Fatalf("outcome/model/Tool/bridge = %#v/%d/%d/%d/%#v/%#v/%t",
					outcome, len(model.Requests()), len(tool.Calls()), claims, commits, resolutions, pending)
			}
		})
	}
}

func TestSteerArrivingDuringFinalStreamDoesNotModifySentRequest(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	const text = "This must become successor input because no boundary remains."
	bridge := newTestSteeringBridge(input, text)
	model := &recordingModel{scripts: []modelScript{func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
		if countUserContent(request.Messages, text) != 0 {
			t.Fatal("steer modified the final request after it started")
		}
		bridge.Offer()
		return scriptedChunks(diagnosisChunks(
			`{"answer_markdown":"The active response completed normally.","evidence_citations":[],"proposed_actions":[]}`,
		)...)(ctx, request)
	}}}
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	claims, commits, resolutions, pending := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != 1 || claims != 1 ||
		len(commits) != 0 || len(resolutions) != 0 || !pending {
		t.Fatalf("outcome/model/bridge = %#v/%d/%d/%#v/%#v/%t", outcome, len(model.Requests()), claims, commits, resolutions, pending)
	}
}

func TestMultipleSteersEnterEinoStateOnceEachWithoutBreakingToolPair(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	firstText := "First, check the bounded restart count."
	secondText := "Then include the latest bounded phase."
	boundary := agent.SteerBoundary{
		RunID: input.RunID(), SessionID: input.SessionID(),
		ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
	}
	bridge := &multiSteeringBridge{
		want: boundary,
		claims: []agent.SteerClaim{
			{
				ItemID: "00000000-0000-7000-8000-000000008010", RunID: input.RunID(), SessionID: input.SessionID(),
				ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
				RunSequence: 1, Content: firstText, ContentHash: domain.MessageContentHash(firstText),
			},
			{
				ItemID: "00000000-0000-7000-8000-000000008011", RunID: input.RunID(), SessionID: input.SessionID(),
				ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
				RunSequence: 2, Content: secondText, ContentHash: domain.MessageContentHash(secondText),
			},
		},
	}
	model := &recordingModel{scripts: []modelScript{
		func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
			if countUserContent(request.Messages, firstText) != 1 || countUserContent(request.Messages, secondText) != 0 {
				t.Fatalf("first-boundary active inputs = %#v", request.Messages)
			}
			return scriptedChunks(toolCallChunks(resourceCall("call-multiple-steers", "sample-pod"))...)(ctx, request)
		},
		func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
			if countUserContent(request.Messages, firstText) != 1 || countUserContent(request.Messages, secondText) != 1 ||
				request.Messages[len(request.Messages)-1].Content != secondText {
				t.Fatalf("second-boundary active inputs = %#v", request.Messages)
			}
			pair := -1
			for index := 0; index+1 < len(request.Messages); index++ {
				if len(request.Messages[index].ToolCalls) == 1 && request.Messages[index].ToolCalls[0].ID == "call-multiple-steers" {
					pair = index
					break
				}
			}
			if pair < 0 || request.Messages[pair+1].Role != schema.Tool ||
				request.Messages[pair+1].ToolCallID != "call-multiple-steers" {
				t.Fatalf("multiple-steer Tool pair = %#v", request.Messages)
			}
			return scriptedChunks(diagnosisChunks(
				`{"answer_markdown":"Both committed inputs were present once.","evidence_citations":[],"proposed_actions":[]}`,
			)...)(ctx, request)
		},
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"phase":"Running"}`)
	}}
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(context.Background(), input, newEventRecorder())
	commits, resolutions := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != 2 || len(tool.Calls()) != 1 ||
		len(commits) != 2 || commits[0].RunSequence != 1 || commits[1].RunSequence != 2 || len(resolutions) != 0 {
		t.Fatalf("multiple-steer outcome/model/Tool/bridge = %#v/%d/%d/%#v/%#v",
			outcome, len(model.Requests()), len(tool.Calls()), commits, resolutions)
	}
}

func TestSummarizationCompletesBeforeSteerClaimAndMainRequest(t *testing.T) {
	clock := newTestClock()
	input := testInputWithConversation(t, clock, testConversation(t, 160))
	const text = "Include the queued correction after summarizing prior history."
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(&schema.Message{
			Role: schema.Assistant, Content: "Earlier completed turns were summarized without current input.",
			ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
		}),
		scriptedChunks(diagnosisChunks(
			`{"answer_markdown":"The steer followed the summary once.","evidence_citations":[],"proposed_actions":[]}`,
		)...),
	}}
	bridge := newTestSteeringBridge(input, text)
	bridge.Offer()
	bridge.onCommit = func() {
		requests := model.Requests()
		if len(requests) != 1 || countUserContent(requests[0].Messages, text) != 0 {
			t.Fatalf("summary requests before commit = %#v", requests)
		}
	}
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	requests := model.Requests()
	claims, commits, resolutions, _ := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusCompleted || len(requests) != 2 ||
		countUserContent(requests[0].Messages, text) != 0 || countUserContent(requests[1].Messages, text) != 1 ||
		claims != 1 || len(commits) != 1 || len(resolutions) != 0 {
		t.Fatalf("outcome/requests/bridge = %#v/%#v/%d/%#v/%#v", outcome, requests, claims, commits, resolutions)
	}
}

func TestSummaryFailureLeavesSteerUnclaimedAndBlocksMainModel(t *testing.T) {
	clock := newTestClock()
	input := testInputWithConversation(t, clock, testConversation(t, 160))
	bridge := newTestSteeringBridge(input, "This input must remain uncommitted.")
	bridge.Offer()
	model := &recordingModel{scripts: []modelScript{func(_ context.Context, request recordedModelRequest) ([]*schema.Message, error) {
		return nil, domain.NewModelError(domain.ModelErrorCodeServiceUnavailable, domain.ModelOperationRequest, string(request.ID))
	}}}
	input = withTestSteering(t, input, bridge)

	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, newEventRecorder(),
	)
	claims, commits, resolutions, pending := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusFailed || len(model.Requests()) != 1 || claims != 0 ||
		len(commits) != 0 || len(resolutions) != 0 || !pending {
		t.Fatalf("outcome/model/bridge = %#v/%d/%d/%#v/%#v/%t", outcome, len(model.Requests()), claims, commits, resolutions, pending)
	}
}
