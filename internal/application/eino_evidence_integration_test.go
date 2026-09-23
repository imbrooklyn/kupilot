//go:build integration

package application

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type checkoutObservedModel struct {
	einomodel.AgenticModel
	final *string
}

func (model checkoutObservedModel) Generate(ctx context.Context, input []*schema.AgenticMessage, options ...einomodel.Option) (*schema.AgenticMessage, error) {
	message, err := model.AgenticModel.Generate(ctx, input, options...)
	if message != nil {
		*model.final = responsesText(message)
	}
	return message, err
}

// Exercise the real configured model with synthetic Service/Deployment/Pod
// observations. No Kubernetes request or persisted model traffic is involved.
func TestCheckoutEvidenceLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue || os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "3" {
		t.Skip("Requires explicit live model authorization and the three-dollar ceiling.")
	}
	cfg, key, _ := loadLiveModelProfile(t, liveModelTargetPreferred)
	defer key.Destroy()
	if cfg.Model != "gpt-5.6-luna" || cfg.APIProtocol != domain.ModelAPIProtocolResponses {
		t.Skip("The measured price and protocol fixture requires the configured Luna Responses profile.")
	}
	t.Logf("Configured request budget: max_output_tokens=%d timeout=%s; two turns, at most 24 model calls, no Kubernetes requests.", cfg.MaxOutputTokens, cfg.RequestTimeout)
	transport := newLiveBudgetTransport(cfg.ProviderKind, 24, 4*1024*1024)
	client, failure := newModelClientForTest(cfg, key, nil, transport)
	if failure != nil {
		t.Fatal("Configured model preflight failed safely.")
	}
	usage := &conformanceUsage{}
	var final string
	client.responsesModel = checkoutObservedModel{AgenticModel: meteredResponsesModel{base: client.responsesModel, usage: usage}, final: &final}
	client.structuredResponses = checkoutObservedModel{AgenticModel: meteredResponsesModel{base: client.structuredResponses, usage: usage}, final: &final}
	clock := newTestClock()
	identifiers, err := NewIdentifierGenerator(func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		if call.Name() == domain.ToolNameGetPodLogs || call.Name() == domain.ToolNameGetPreviousPodLogs {
			result := emptyToolResult(t, call, clock.Now())
			result.Status = domain.ToolResultStatusDenied
			result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassPolicyDenied, SafeMessage: "The cluster-read request was blocked by the fixed policy."}
			return result
		}
		var args struct {
			ResourceType string                      `json:"resource_type"`
			Name         string                      `json:"name"`
			Resource     struct{ Kind, Name string } `json:"resource"`
		}
		if err := json.Unmarshal([]byte(call.ArgumentsJSON()), &args); err != nil {
			t.Fatal(err)
		}
		kind, api, name := "Pod", "v1", args.Name
		fact := "checkout-a and checkout-b are Running but not Ready; their owner is ReplicaSet checkout-rs."
		data := `{"items":[{"name":"checkout-a","phase":"Running","ready":false,"owner":"checkout-rs"},{"name":"checkout-b","phase":"Running","ready":false,"owner":"checkout-rs"}]}`
		switch args.ResourceType {
		case "services":
			kind, name = "Service", "checkout"
			fact = "checkout Service selects app=checkout and exposes port 8080 targeting port 8080. Its EndpointSlice has zero ready and two not-ready endpoints."
			data = `{"name":"checkout","selector":{"app":"checkout"},"port":8080,"target_port":8080,"ready_endpoints":0,"not_ready_endpoints":2}`
		case "deployments":
			kind, api, name = "Deployment", "apps/v1", "checkout"
			fact = "checkout Deployment has desired=2, updated=2, available=0 and ready=0; its ReplicaSet is checkout-rs."
			data = `{"name":"checkout","replicas":2,"updated":2,"available":0,"ready":0,"replica_set":"checkout-rs"}`
		case "replicasets":
			kind, api, name = "ReplicaSet", "apps/v1", "checkout-rs"
			fact = "checkout-rs has two current replicas, zero ready replicas and is owned by Deployment checkout."
			data = `{"name":"checkout-rs","replicas":2,"ready":0,"owner":"checkout"}`
		}
		if name == "" {
			name = "checkout-a"
		}
		facts := []string{fact}
		if call.Name() == domain.ToolNameGetRelatedResources {
			kind, name = args.Resource.Kind, args.Resource.Name
			switch kind {
			case "Service":
				facts = []string{
					"Service checkout is ClusterIP, port 8080 targeting port 8080.",
					"Pod checkout-a is Running, ready 0/1, ContainersNotReady.",
					"Pod checkout-b is Running, ready 0/1, ContainersNotReady.",
					"Service checkout matched Pod checkout-a using one selector key.",
					"Service checkout matched Pod checkout-b using one selector key.",
					"Service checkout has 0 ready and 2 not-ready EndpointSlice endpoints.",
				}
				data = `{"pods":["checkout-a","checkout-b"],"ready_endpoints":0,"not_ready_endpoints":2}`
			case "Pod":
				facts = []string{
					"Pod " + name + " is Running, ready 0/1, ContainersNotReady.",
					"ReplicaSet checkout-rs has two current replicas and zero ready replicas.",
					"The fixed owner relationship connects Pod " + name + " to ReplicaSet checkout-rs.",
				}
				data = `{"owner":{"kind":"ReplicaSet","name":"checkout-rs"}}`
			case "ReplicaSet", "Deployment":
				api = "apps/v1"
				facts = []string{
					"ReplicaSet checkout-rs has two current replicas and zero ready replicas.",
					"Deployment checkout desired/current/updated=2; ready/available=0; unavailable=2; MinimumReplicasUnavailable.",
					"The fixed owner relationship connects ReplicaSet checkout-rs to Deployment checkout.",
				}
				data = `{"deployment":"checkout","replica_set":"checkout-rs","ready":0,"available":0}`
			default:
				t.Fatal("Unexpected synthetic relationship target kind.")
			}
		} else if call.Name() == domain.ToolNameGetResource && kind == "Pod" {
			facts = []string{
				"Pod " + name + " is Running, ready 0/1, ContainersNotReady.",
				"Pod " + name + " condition PodScheduled=True.",
				"Pod " + name + " condition Initialized=True.",
				"Pod " + name + " condition ContainersReady=False, reason ContainersNotReady.",
				"Pod " + name + " condition Ready=False, reason ContainersNotReady.",
				"Pod " + name + " container app is running, ready=false, restart_count=0.",
				"Pod " + name + " is owned by ReplicaSet checkout-rs.",
				"Pod " + name + " has one ordinary container named app.",
				"Pod " + name + " has no recorded previous container termination.",
			}
		} else if call.Name() == domain.ToolNameGetResource && kind == "Deployment" {
			facts = []string{
				fact,
				"Deployment checkout condition Available=False, reason MinimumReplicasUnavailable.",
				"Deployment checkout condition Progressing=True, reason NewReplicaSetAvailable.",
				"Deployment checkout desired/current/updated replicas=2.",
				"Deployment checkout ready/available replicas=0; unavailable replicas=2.",
			}
		}
		if call.Name() == domain.ToolNameGetEvents {
			kind, name = args.Resource.Kind, args.Resource.Name
			if kind != "Pod" {
				return emptyToolResult(t, call, clock.Now())
			}
			facts = []string{"Pod " + name + " has an Unhealthy Warning: Readiness probe failed. The event gives no specific failure details. Event results are partial."}
			data = `{"events":[{"reason":"Unhealthy","message":"Readiness probe failed:"}]}`
		}
		var canonical map[string]any
		if err := json.Unmarshal([]byte(data), &canonical); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			t.Fatal(err)
		}
		data = string(encoded)
		result := emptyToolResult(t, call, clock.Now())
		result.DataJSON = data
		for _, observedFact := range facts {
			id, idErr := identifiers.NewEvidenceID()
			if idErr != nil {
				t.Fatal(idErr)
			}
			item := successfulToolResult(t, call, id, clock.Now(), data).Evidence[0]
			item.Resource = domain.ResourceRef{APIVersion: api, Kind: kind, Name: name, Namespace: call.Scope().Namespace, UID: "99679a4c-9dc5-4bfe-bd90-62448c29a865"}
			item.Fact = observedFact
			result.Evidence = append(result.Evidence, item)
		}
		if call.Name() == domain.ToolNameGetEvents {
			result.Status = domain.ToolResultStatusPartial
			result.Evidence[0].Partial = true
		}
		return result
	}}
	runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: identifiers, now: clock.Now}, client)
	if err != nil {
		client.close()
		t.Fatal(err)
	}
	defer runtime.Close()
	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
	if err != nil {
		t.Fatal(err)
	}
	limits.ModelCalls = 12
	limits.ModelRequestTimeout = cfg.RequestTimeout
	limits.ToolCalls = 24
	scope := domain.ClusterScope{Context: "test-context", Namespace: "shop-test", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}
	conversation, err := agent.NewConversationContext(testSessionID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	question := "\u0073hop-test \u91cc\u7684 checkout \u8bbf\u95ee\u4e0d\u4e86\u3002\u5e2e\u6211\u6cbf\u7740 Service\u3001\u540e\u7aef Pod\u3001Deployment \u67e5\u4e00\u4e0b\u5361\u5728\u54ea\u4e00\u5c42\u3002\u5148\u4e0d\u8981\u6539\u4efb\u4f55\u4e1c\u897f\u3002"
	input, err := agent.NewRunInputWithContext(testRunID, testSessionID, testMessageID, question, scope, nil, limits, conversation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), liveModelSuiteTimeout)
	defer cancel()
	events := newEventRecorder()
	outcome := runtime.Run(ctx, input, events)
	checkoutReferenceDiagnostics(t, final, events.Events())
	estimate := float64(usage.input)*0.20/1e6 + float64(usage.output)*1.20/1e6
	t.Logf("model_calls=%d tool_calls=%d input_tokens=%d output_tokens=%d estimated_usd=%.6f status=%s reason=%s", transport.calls.Load(), len(tool.Calls()), usage.input, usage.output, estimate, outcome.Status, outcome.Diagnostic)
	if estimate > 3 {
		t.Fatal("Authorized price estimate exceeded.")
	}
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Validate(input) != nil {
		t.Fatal("Checkout Evidence integration failed; no retry was attempted.")
	}
	if len(outcome.Diagnosis.ConfirmedFacts) == 0 || len(tool.Calls()) < 3 || len(outcome.Diagnosis.RecommendedActions) != 0 {
		t.Fatal("The read-only investigation did not establish the three resource layers.")
	}
	for _, kind := range []string{"services", "pods", "deployments"} {
		seen := false
		for _, call := range tool.Calls() {
			seen = seen || strings.Contains(call.ArgumentsJSON(), `"resource_type":"`+kind+`"`)
		}
		if !seen {
			t.Errorf("Missing synthetic resource read: %s", kind)
		}
	}

	// Replay only the safe committed question/answer, as production history does.
	// Old Evidence references must not become authority in the follow-up run.
	turns := []agent.ConversationTurn{
		{MessageID: testMessageID, RunID: testRunID, Role: domain.MessageRoleUser, Content: question},
		{MessageID: "00000000-0000-7000-8000-000000009901", RunID: testRunID, RunSequence: 1, Role: domain.MessageRoleAssistant, Content: outcome.Diagnosis.AnswerMarkdown},
	}
	for i := range turns {
		turns[i].ContentHash = domain.MessageContentHash(turns[i].Content)
	}
	conversation, err = agent.NewConversationContext(testSessionID, turns, nil)
	if err != nil {
		t.Fatal(err)
	}
	followUp := "\u8bf7\u628a\u521a\u624d\u7ed3\u8bba\u7684\u8bc1\u636e\u8bb2\u6e05\u695a\uff1a\u54ea\u4e9b\u76f4\u63a5\u8bf4\u660e\u540e\u7aef\u4e0d\u5c31\u7eea\uff0c\u54ea\u4e9b\u8bf4\u660e Service \u6ca1\u6709 ready endpoint\uff1f\u63a2\u9488\u5177\u4f53\u5931\u8d25\u539f\u56e0\u3001\u5e94\u7528 HTTP \u63a5\u53e3\u548c\u8c03\u7528\u65b9\u7f51\u7edc\u5206\u522b\u786e\u8ba4\u5230\u4ec0\u4e48\u7a0b\u5ea6\uff1f\u6ca1\u6709\u67e5\u5230\u7684\u5c31\u8bf4\u660e\u6ca1\u6709\u67e5\u5230\uff0c\u4e0d\u8981\u4fee\u6539\u3002"
	input, err = agent.NewRunInputWithContext("00000000-0000-7000-8000-000000009902", testSessionID, "00000000-0000-7000-8000-000000009903", followUp, scope, nil, limits, conversation)
	if err != nil {
		t.Fatal(err)
	}
	beforeInput, beforeOutput := usage.input, usage.output
	followUpEvents := newEventRecorder()
	outcome = runtime.Run(ctx, input, followUpEvents)
	checkoutReferenceDiagnostics(t, final, followUpEvents.Events())
	estimate = float64(usage.input)*0.20/1e6 + float64(usage.output)*1.20/1e6
	t.Logf("follow_up_input_tokens=%d follow_up_output_tokens=%d total_model_calls=%d total_tool_calls=%d total_estimated_usd=%.6f status=%s reason=%s", usage.input-beforeInput, usage.output-beforeOutput, transport.calls.Load(), len(tool.Calls()), estimate, outcome.Status, outcome.Diagnostic)
	if estimate > 3 {
		t.Fatal("Authorized price estimate exceeded.")
	}
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Validate(input) != nil ||
		len(outcome.Diagnosis.ConfirmedFacts) == 0 || len(outcome.Diagnosis.RecommendedActions) != 0 ||
		outcome.Diagnosis.Completeness.StopReason == domain.RunTerminalBudgetExhausted {
		t.Fatal("The detailed Evidence follow-up did not complete; no retry was attempted.")
	}
	uncertain := false
	for _, claim := range outcome.Diagnosis.ClaimCoverage {
		uncertain = uncertain || claim.Kind == domain.ClaimUncertainty || claim.Kind == domain.ClaimUnsupportedObservation
	}
	if !uncertain {
		t.Fatal("The follow-up omitted explicit uncertainty for unverified probe details, HTTP or caller networking.")
	}
}

// Report only cardinalities and identifier mismatch classes, never model text,
// arguments, identifiers or source content.
func checkoutReferenceDiagnostics(t *testing.T, final string, events []agent.RunEvent) {
	t.Helper()
	draft, err := agent.DecodeDiagnosticResponse(final)
	if err != nil {
		t.Logf("Reference diagnostics: decode_failure=%s", agent.InteractionFailureOf(err, ""))
		return
	}
	accepted := make(map[domain.EvidenceID]bool)
	invocations := make(map[domain.EvidenceID]bool)
	uids := make(map[domain.EvidenceID]bool)
	for _, event := range events {
		if event.Evidence != nil {
			accepted[domain.EvidenceID(agent.ModelEvidenceReference(event.Evidence.ID))] = true
			uids[domain.EvidenceID(event.Evidence.Resource.UID)] = true
		}
		if event.ToolInvocation != nil {
			invocations[domain.EvidenceID(event.ToolInvocation.ID)] = true
		}
	}
	t.Logf("Reference diagnostics: accepted=%d claims=%d", len(accepted), len(draft.ClaimCoverage))
	for index, claim := range draft.ClaimCoverage {
		for _, id := range claim.EvidenceIDs {
			if accepted[id] {
				continue
			}
			distance := len(id)
			for known := range accepted {
				if len(id) != len(known) {
					continue
				}
				differences := 0
				for i := range len(id) {
					if id[i] != known[i] {
						differences++
					}
				}
				distance = min(distance, differences)
			}
			t.Logf("Unknown reference: claim=%d invocation=%t resource_uid=%t nearest_id_differing_characters=%d", index, invocations[id], uids[id], distance)
		}
	}
}
