//go:build integration

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Natural questions exercise target selection without naming a Tool or teaching
// the model a verdict. All cluster observations are synthetic and ephemeral.
func TestResourceIdentityLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue || os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "3" {
		t.Skip("Requires explicit live model authorization and the three-dollar ceiling.")
	}
	cfg, key, _ := loadLiveModelProfile(t, liveModelTargetPreferred)
	defer key.Destroy()
	if cfg.Model != "gpt-5.6-luna" || cfg.APIProtocol != domain.ModelAPIProtocolResponses {
		t.Skip("The measured price and protocol fixture requires the configured Luna Responses profile.")
	}
	transport := newLiveBudgetTransport(cfg.ProviderKind, 64, 4*1024*1024)
	defer transport.base.CloseIdleConnections()
	usage := &conformanceUsage{}
	clock := newTestClock()
	identifiers, err := NewIdentifierGenerator(func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, question, requested, followUp string
		terminationReason                   string
		candidates                          []string
		ambiguous, logsDenied               bool
	}{
		{
			name: "single candidate and confirmation", requested: "chekout", candidates: []string{"checkout"}, ambiguous: true,
			question: "\u5e2e\u6211\u67e5 chekout \u4e3a\u4ec0\u4e48\u4e00\u76f4\u91cd\u542f\u3002",
			followUp: "\u6211\u8bf4\u7684\u662f Pod checkout\uff0c\u8bf7\u7ee7\u7eed\u67e5\u3002",
		},
		{
			name: "multiple candidates", requested: "bililng", candidates: []string{"billing", "billing-worker"}, ambiguous: true,
			question: "Why does bililng keep restarting?",
		},
		{
			name: "absent old Pod", requested: "invoice-7d9-old", candidates: []string{"invoice-8f2-new"}, ambiguous: true,
			question: "Why did Pod invoice-7d9-old restart?",
		},
		{
			name: "exact name", requested: "checkout", candidates: []string{"checkout"},
			question: "How many restarts does Pod checkout have?",
		},
		{
			name: "unusual exact name exists", requested: "chekout", candidates: []string{"chekout", "checkout"},
			question: "How many restarts does Pod chekout have?",
		},
		{
			name: "blocked logs preserve termination evidence", requested: "billing", candidates: []string{"billing"}, logsDenied: true, terminationReason: "OOMKilled",
			question: "Check why Pod billing is restarting, including its previous logs. Do not change anything.",
		},
		{
			name: "exit code does not confirm OOM", requested: "exit-137", candidates: []string{"exit-137"}, terminationReason: "Error",
			question: "\u8bf7\u68c0\u67e5 shop-test \u7684 Pod exit-137\u3001\u5bb9\u5668 app\uff1a\u65e5\u5fd7\u63d0\u5230 OOM\uff0c\u4e0a\u4e00\u5b9e\u4f8b\u9000\u51fa\u7801\u662f 137\u3002\u8bf7\u6838\u5bf9 Kubernetes \u8bb0\u5f55\u7684\u7ec8\u6b62 reason\uff0c\u8fd9\u80fd\u786e\u8ba4 OOMKilled \u5417\uff1f\u4e0d\u8981\u4fee\u6539\u3002",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			secret, err := key.Clone()
			if err != nil {
				t.Fatal("Credential clone failed.")
			}
			defer secret.Destroy()
			client, failure := newModelClientForTest(cfg, &secret, nil, transport)
			if failure != nil {
				t.Fatal("Configured model preflight failed safely.")
			}
			defer client.close()
			var final string
			client.responsesModel = checkoutObservedModel{AgenticModel: meteredResponsesModel{base: client.responsesModel, usage: usage}, final: &final}
			client.structuredResponses = checkoutObservedModel{AgenticModel: meteredResponsesModel{base: client.structuredResponses, usage: usage}, final: &final}
			confirmed, detailReads, logReads := false, 0, 0
			returnedCandidates := make(map[string]bool)
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				var args struct {
					ResourceType string                  `json:"resource_type"`
					Name         string                  `json:"name"`
					PodName      string                  `json:"pod_name"`
					Filters      []domain.ResourceFilter `json:"filters"`
					Limit        int                     `json:"limit"`
					Resource     struct{ Name string }   `json:"resource"`
				}
				if err := json.Unmarshal([]byte(call.ArgumentsJSON()), &args); err != nil {
					t.Fatal("Invalid synthetic Tool arguments.")
				}
				result := emptyToolResult(t, call, clock.Now())
				addFact := func(name, fact string) {
					id, err := identifiers.NewEvidenceID()
					if err != nil {
						t.Fatal(err)
					}
					item := successfulToolResult(t, call, id, clock.Now(), result.DataJSON).Evidence[0]
					item.Resource.Name, item.Fact = name, fact
					item.Category = domain.EvidenceCategoryResourceStatus
					result.Evidence = append(result.Evidence, item)
				}
				if call.Name() == domain.ToolNameListResources {
					items := make([]struct {
						Name string `json:"name"`
					}, 0)
					if args.ResourceType == "pods" {
						for _, name := range scenario.candidates {
							if !identityFixtureMatches(t, name, args.Filters) {
								continue
							}
							if args.Limit > 0 && len(items) >= args.Limit {
								result.Status = domain.ToolResultStatusPartial
								result.Truncation = domain.ToolResultTruncation{Truncated: true, Reason: "item_limit", ReturnedCount: len(items)}
								break
							}
							items = append(items, struct {
								Name string `json:"name"`
							}{name})
							returnedCandidates[name] = true
							addFact(name, "The bounded Pod list contains "+name+".")
						}
						for index := range result.Evidence {
							result.Evidence[index].Partial = result.Truncation.Truncated
						}
					}
					encoded, err := json.Marshal(struct {
						Items any `json:"items"`
					}{items})
					if err != nil {
						t.Fatal(err)
					}
					result.DataJSON = string(encoded)
					return result
				}
				target := scenario.requested
				if confirmed {
					target = scenario.candidates[0]
				}
				switch call.Name() {
				case domain.ToolNameGetResource:
					if args.Name != target {
						t.Error("The model investigated an unconfirmed replacement target.")
					}
					if args.Name != target || args.ResourceType != "pods" || !slices.Contains(scenario.candidates, args.Name) {
						result.Status = domain.ToolResultStatusError
						result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassNotFound, SafeMessage: "The exact resource was not found in the working Namespace."}
						return result
					}
					detailReads++
					if scenario.terminationReason != "" {
						result.DataJSON = fmt.Sprintf(`{"containers":[{"current":{"reason":"CrashLoopBackOff","state":"waiting"},"last":{"exit_code":137,"reason":%q,"state":"terminated"},"name":"app","ready":false,"restart_count":4}]}`, scenario.terminationReason)
						addFact(target, "Container app is waiting, ready=false, restarts=4, reason CrashLoopBackOff; previous state terminated, reason "+scenario.terminationReason+", exit_code=137.")
					} else {
						result.DataJSON = `{"containers":[{"name":"app","ready":true,"restart_count":0,"state":"running"}]}`
						addFact(target, "Pod "+target+" container app is running and ready with restart_count=0.")
					}
				case domain.ToolNameGetPodLogs, domain.ToolNameGetPreviousPodLogs:
					if scenario.ambiguous && !confirmed || args.PodName != target {
						t.Error("The model read logs before establishing the target.")
					}
					logReads++
					result.Status = domain.ToolResultStatusDenied
					result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassPolicyDenied, SafeMessage: "Container output is disabled for model sharing. This applies to all Pod logs; changing Pods does not bypass it."}
				case domain.ToolNameGetEvents:
					if scenario.ambiguous && !confirmed || args.Resource.Name != target {
						t.Error("The model read Events before establishing the target.")
					}
				case domain.ToolNameGetRelatedResources:
					if scenario.ambiguous && !confirmed || args.Resource.Name != target {
						t.Error("The model followed relationships before establishing the target.")
					}
					// The synthetic Pods have no owner or selector relationships.
				case domain.ToolNameGetPodMetrics:
					if scenario.ambiguous && !confirmed || args.PodName != target {
						t.Error("The model read metrics before establishing the target.")
					}
					result.Status = domain.ToolResultStatusError
					result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassUnsupported, SafeMessage: "The Metrics API is unavailable in this synthetic cluster."}
				default:
					t.Errorf("The model selected a Tool outside this bounded identity fixture: %s", call.Name())
				}
				return result
			}}
			runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: identifiers, now: clock.Now}, client)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
			if err != nil {
				t.Fatal(err)
			}
			limits.ModelCalls, limits.ToolCalls = 8, 10
			limits.ModelRequestTimeout = cfg.RequestTimeout
			scope := domain.ClusterScope{Context: "test-context", Namespace: "shop-test", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}
			input, err := agent.NewRunInput(testRunID, testSessionID, testMessageID, scenario.question, scope, nil, limits)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			run := func(input agent.RunInput) *domain.Diagnosis {
				outcome := runtime.Run(ctx, input, newEventRecorder())
				estimate := float64(usage.input)*0.20/1e6 + float64(usage.output)*1.20/1e6
				names := make([]domain.ToolName, 0, len(tool.Calls()))
				for _, call := range tool.Calls() {
					names = append(names, call.Name())
				}
				t.Logf("Tool kinds: %v", names)
				t.Logf("cumulative_model_calls=%d tool_calls=%d detail_reads=%d log_reads=%d input_tokens=%d output_tokens=%d estimated_usd=%.6f status=%s reason=%s", transport.calls.Load(), len(tool.Calls()), detailReads, logReads, usage.input, usage.output, estimate, outcome.Status, outcome.Diagnostic)
				if outcome.Diagnostic == domain.FailureClaimUnsupported {
					var wire struct {
						Citations []struct {
							Kind string   `json:"claim_type"`
							IDs  []string `json:"evidence_ids"`
						} `json:"evidence_citations"`
					}
					if json.Unmarshal([]byte(final), &wire) == nil {
						for index, citation := range wire.Citations {
							if citation.Kind == string(domain.ClaimCurrentObservation) && len(citation.IDs) == 0 {
								t.Logf("Final current observation %d has no Evidence references; model content is not logged.", index+1)
							}
						}
					}
				}
				if estimate > 3 || outcome.Status != domain.AgentRunStatusCompleted || outcome.Validate(input) != nil || outcome.Diagnosis == nil || len(outcome.Diagnosis.RecommendedActions) != 0 || outcome.Diagnosis.Completeness.StopReason == domain.RunTerminalBudgetExhausted {
					t.Fatal("The read-only quality case did not complete within its budget; no retry was attempted.")
				}
				return outcome.Diagnosis
			}
			diagnosis := run(input)
			if scenario.ambiguous {
				answer := strings.ToLower(diagnosis.AnswerMarkdown)
				asks := strings.ContainsAny(answer, "?\uff1f") || strings.Contains(answer, "confirm") || strings.Contains(answer, "\u786e\u8ba4")
				if !strings.Contains(answer, scenario.requested) || !asks || detailReads != 0 || logReads != 0 || len(tool.Calls()) > 0 && (diagnosis.Clarification != nil || len(diagnosis.MissingInformation) == 0) {
					t.Error("The answer did not preserve the literal name and stop for confirmation with an explicit identity gap.")
				}
				for name := range returnedCandidates {
					if !strings.Contains(answer, name) {
						t.Error("An available candidate was omitted from the identity question.")
					}
				}
				for _, fact := range diagnosis.ConfirmedFacts {
					for _, health := range []string{"restart", "ready", "OOMKilled"} {
						if strings.Contains(fact.Statement, health) {
							t.Error("Candidate discovery became a confirmed health diagnosis.")
						}
					}
				}
			} else if diagnosis.Clarification != nil || detailReads == 0 || len(diagnosis.ConfirmedFacts) == 0 {
				t.Error("An exact target did not proceed to a current observation.")
			}
			if scenario.terminationReason != "" {
				facts := ""
				for _, fact := range diagnosis.ConfirmedFacts {
					facts += fact.Statement + "\n"
				}
				if !strings.Contains(facts, scenario.terminationReason) || !strings.Contains(facts, "137") || !strings.Contains(diagnosis.AnswerMarkdown, scenario.terminationReason) || !strings.Contains(diagnosis.AnswerMarkdown, "137") {
					t.Error("The answer did not preserve the observed termination reason and exit code with same-run citations.")
				}
				if scenario.logsDenied && (logReads == 0 || len(diagnosis.MissingInformation) == 0) {
					t.Error("Requested logs were not checked or their denial was hidden.")
				}
			}
			if scenario.followUp != "" {
				turns := []agent.ConversationTurn{
					{MessageID: testMessageID, RunID: testRunID, Role: domain.MessageRoleUser, Content: scenario.question},
					{MessageID: "00000000-0000-7000-8000-000000009901", RunID: testRunID, RunSequence: 1, Role: domain.MessageRoleAssistant, Content: diagnosis.AnswerMarkdown},
				}
				for index := range turns {
					turns[index].ContentHash = domain.MessageContentHash(turns[index].Content)
				}
				conversation, err := agent.NewConversationContext(testSessionID, turns, nil)
				if err != nil {
					t.Fatal(err)
				}
				input, err = agent.NewRunInputWithContext("00000000-0000-7000-8000-000000009902", testSessionID, "00000000-0000-7000-8000-000000009903", scenario.followUp, scope, nil, limits, conversation)
				if err != nil {
					t.Fatal(err)
				}
				confirmed = true
				diagnosis = run(input)
				if detailReads == 0 || diagnosis.Clarification != nil || len(diagnosis.ConfirmedFacts) == 0 {
					t.Error("User confirmation did not lead to fresh exact-target Evidence.")
				}
			}
		})
		if float64(usage.input)*0.20/1e6+float64(usage.output)*1.20/1e6 > 3 {
			t.Fatal("Authorized price estimate exceeded; remaining cases were not attempted.")
		}
	}
}

func identityFixtureMatches(t *testing.T, name string, filters []domain.ResourceFilter) bool {
	t.Helper()
	for _, filter := range filters {
		// These synthetic Pods have no labels. Name predicates must not return
		// candidates that an exact-name query would have excluded.
		if filter.Field != "name" {
			return false
		}
		matches := false
		switch filter.Operator {
		case domain.ResourceFilterEquals:
			matches = name == filter.Value
		case domain.ResourceFilterNotEquals:
			matches = name != filter.Value
		case domain.ResourceFilterContains:
			matches = strings.Contains(name, filter.Value)
		case domain.ResourceFilterStartsWith:
			matches = strings.HasPrefix(name, filter.Value)
		case domain.ResourceFilterExists:
			matches = true
		default:
			t.Error("Unsupported name predicate in the synthetic identity fixture.")
		}
		if !matches {
			return false
		}
	}
	return true
}
