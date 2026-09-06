package einoadapter

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func withPlanOnlyMode(t testing.TB, input agent.RunInput) agent.RunInput {
	t.Helper()
	result, err := agent.WithRunMode(input, agent.RunModePlanOnly)
	if err != nil {
		t.Fatalf("WithRunMode(plan-only) error = %v", err)
	}
	return result
}

func planResponseJSON(title, step string, claim string, evidenceIDs ...domain.EvidenceID) string {
	citations := ""
	if claim != "" {
		ids := make([]string, len(evidenceIDs))
		for index, id := range evidenceIDs {
			ids[index] = fmt.Sprintf("%q", id)
		}
		citations = fmt.Sprintf(
			`{"sequence":1,"claim_type":"current_observation","claim":%q,"claim_hash":%q,"evidence_ids":[%s],"coverage_state":"verified"}`,
			claim, domain.SHA256Hex(claim), strings.Join(ids, ","),
		)
	}
	return fmt.Sprintf(
		`{"schema_version":1,"title":%q,"steps":[{"sequence":1,"description":%q}],"limitations":["This plan carries no execution authority."],"evidence_citations":[%s]}`,
		title, step, citations,
	)
}

func TestPlanOnlyUsesOneAgentWithFixedSafeReadToolsAndTypedResult(t *testing.T) {
	clock := newTestClock()
	const claim = "The projected Pod condition is not Ready."
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(resourceCall("call-plan-read", "sample-pod"))...),
		scriptedChunks(diagnosisChunks(planResponseJSON("Inspect readiness", claim, claim, testEvidenceID))...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"phase":"Pending"}`)
	}}
	input := withPlanOnlyMode(t, testInput(t, clock, agent.DefaultRunBudgetLimits()))
	recorder := newEventRecorder()
	outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(context.Background(), input, recorder)
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Diagnosis.Plan == nil ||
		outcome.Diagnosis.Plan.Title != "Inspect readiness" || len(outcome.Diagnosis.RecommendedActions) != 0 ||
		len(model.Requests()) != 2 || len(tool.Calls()) != 1 {
		t.Fatalf("plan-only outcome/model/Tool = %#v/%d/%d", outcome, len(model.Requests()), len(tool.Calls()))
	}
	wantTools := []string{
		string(domain.ToolNameGetResource), string(domain.ToolNameListResources), string(domain.ToolNameGetEvents),
		string(domain.ToolNameGetPodMetrics), string(domain.ToolNameGetNodeMetrics),
		string(domain.ToolNameGetRelatedResources), string(domain.ToolNameGetClusterOverview),
	}
	if bound := model.BoundToolNames(); len(bound) != 2 || !reflect.DeepEqual(bound[0], wantTools) || !reflect.DeepEqual(bound[1], wantTools) {
		t.Fatalf("plan-only bound Tools = %#v, want %#v", bound, wantTools)
	}
	if outcome.Diagnosis.ClaimCoverage[0].RunID != input.RunID() ||
		outcome.Diagnosis.ClaimCoverage[0].PolicyGeneration != input.PolicyGeneration() ||
		outcome.Diagnosis.ClaimCoverage[0].Scope != input.Scope().Snapshot() {
		t.Fatalf("plan claim provenance = %#v", outcome.Diagnosis.ClaimCoverage)
	}
}

func TestPlanOnlyRejectsNonSafeToolWithoutCallingIt(t *testing.T) {
	clock := newTestClock()
	model := &recordingModel{scripts: []modelScript{scriptedChunks(toolCallChunks(agent.ToolSelection{
		ID: "call-plan-log", Name: domain.ToolNameGetPodLogs,
		ArgumentsJSON: `{"container":"sample-container","container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod","purpose":"Read logs.","search":null,"since_seconds":null,"tail_lines":null}`,
	})...)}}
	tool := &recordingTool{execute: func(context.Context, agent.BoundToolCall) domain.ToolResult {
		t.Fatal("plan-only mode executed a non-safe Tool")
		return domain.ToolResult{}
	}}
	outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(
		context.Background(), withPlanOnlyMode(t, testInput(t, clock, agent.DefaultRunBudgetLimits())), newEventRecorder(),
	)
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassPolicyDenied || len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
		t.Fatalf("non-safe plan Tool outcome/model/Tool = %#v/%d/%d", outcome, len(model.Requests()), len(tool.Calls()))
	}
}

func TestPlanOnlyRejectsMalformedOverLimitAndActionBearingOutput(t *testing.T) {
	overSteps := make([]string, domain.MaxPlanSteps+1)
	for index := range overSteps {
		overSteps[index] = fmt.Sprintf(`{"sequence":%d,"description":"Bounded step."}`, index+1)
	}
	for _, current := range []struct {
		name    string
		content string
	}{
		{name: "malformed", content: `{"schema_version":1}`},
		{name: "over steps", content: fmt.Sprintf(`{"schema_version":1,"title":"Too many","steps":[%s],"limitations":[],"evidence_citations":[]}`, strings.Join(overSteps, ","))},
		{name: "over bytes", content: planResponseJSON("Too large", strings.Repeat("x", domain.MaxPlanStepBytes+1), "")},
		{name: "action bearing", content: strings.TrimSuffix(planResponseJSON("Unsafe", "Do nothing.", ""), "}") + `,"proposed_actions":[]}`},
	} {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{scripts: []modelScript{scriptedChunks(diagnosisChunks(current.content)...)}}
			outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
				context.Background(), withPlanOnlyMode(t, testInput(t, clock, agent.DefaultRunBudgetLimits())), newEventRecorder(),
			)
			if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
				*outcome.ErrorClass != domain.SafeErrorClassInvalidExternalResponse || outcome.Diagnosis != nil || len(model.Requests()) != 1 {
				t.Fatalf("invalid plan outcome = %#v; requests=%d", outcome, len(model.Requests()))
			}
		})
	}
}

func TestPlanOnlySteerCommitsBeforeTheNextModelBoundaryExactlyOnce(t *testing.T) {
	clock := newTestClock()
	input := withPlanOnlyMode(t, testInput(t, clock, agent.DefaultRunBudgetLimits()))
	const steer = "Include one more bounded readiness check in the plan."
	bridge := newTestSteeringBridge(input, steer)
	bridge.Offer()
	model := &recordingModel{scripts: []modelScript{func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
		if countUserContent(request.Messages, input.Question()) != 1 || countUserContent(request.Messages, steer) != 1 {
			t.Fatalf("plan-only current input counts = %#v", request.Messages)
		}
		return scriptedChunks(diagnosisChunks(planResponseJSON("Readiness plan", "Collect bounded readiness Evidence.", ""))...)(ctx, request)
	}}}
	input = withTestSteering(t, input, bridge)
	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(context.Background(), input, newEventRecorder())
	claims, commits, resolutions, pending := bridge.snapshot()
	if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != 1 || claims != 1 ||
		len(commits) != 1 || len(resolutions) != 0 || pending {
		t.Fatalf("plan steer outcome/model/bridge = %#v/%d/%d/%#v/%#v/%t",
			outcome, len(model.Requests()), claims, commits, resolutions, pending)
	}
}
