package application

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestMultiReadFinalUsesOnlyExactReturnedEvidence(t *testing.T) {
	for _, mode := range []string{"accepted", "unknown", "invocation", "resource UID", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{}
			for index := range 9 {
				model.scripts = append(model.scripts, scriptedChunks(toolCallChunks(resourceCall(fmt.Sprintf("read-%d", index), fmt.Sprintf("checkout-%d", index)))...))
			}
			model.scripts = append(model.scripts, func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
				var ids []domain.EvidenceID
				var invocation domain.ToolInvocationID
				var uid string
				for _, message := range request.Messages {
					if message.Role != schema.Tool {
						continue
					}
					var envelope struct {
						Result struct {
							Invocation domain.ToolInvocationID `json:"invocation_id"`
							Evidence   []struct {
								ID       domain.EvidenceID `json:"id"`
								Resource struct {
									UID string `json:"uid"`
								} `json:"resource"`
							} `json:"evidence"`
						} `json:"result"`
					}
					if err := json.Unmarshal([]byte(message.Content), &envelope); err != nil {
						t.Fatal(err)
					}
					for _, item := range envelope.Result.Evidence {
						ids = append(ids, item.ID)
						uid = item.Resource.UID
					}
					invocation = envelope.Result.Invocation
				}
				if len(ids) != 18 {
					t.Fatalf("Tool messages retained %d of 18 Evidence entries", len(ids))
				}
				switch mode {
				case "unknown":
					ids[0] = "019965f4-a739-7ce5-b39a-619c09725aff"
				case "invocation":
					ids[0] = domain.EvidenceID(invocation)
				case "resource UID":
					ids[0] = domain.EvidenceID(uid)
				case "duplicate":
					ids[1] = ids[0]
				}
				encoded, err := json.Marshal(ids)
				if err != nil {
					t.Fatal(err)
				}
				final := `{"answer_markdown":"The returned checkout observations show unready Pods; partial Events do not establish a complete cause.\ue200cite\ue202` + string(ids[0]) + `\ue201","evidence_citations":[{"claim":"The returned checkout observations show unready Pods.","claim_type":"current_observation","evidence_ids":` + string(encoded) + `}],"proposed_actions":[]}`
				return scriptedChunks(diagnosisChunks(final)...)(ctx, request)
			})
			sequence := 0
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				sequence++
				id := domain.EvidenceID(fmt.Sprintf("019965f4-a739-7ce5-b39a-%012x", 0x619c09725ad0+2*sequence))
				result := successfulToolResult(t, call, id, clock.Now(), `{"ready":false}`)
				result.Evidence[0].Resource.UID = "019965f4-a739-7ce5-b39a-619c09725a00"
				second := result.Evidence[0]
				second.ID = domain.EvidenceID(fmt.Sprintf("019965f4-a739-7ce5-b39a-%012x", 0x619c09725ad1+2*sequence))
				second.Fingerprint = domain.SHA256Hex(string(second.ID))
				result.Evidence = append(result.Evidence, second)
				if sequence == 9 {
					result.Status = domain.ToolResultStatusPartial
					result.Truncation = domain.ToolResultTruncation{Truncated: true, Reason: "item_limit", ReturnedCount: 2}
					for index := range result.Evidence {
						result.Evidence[index].Partial = true
					}
				}
				return result
			}}
			limits := agent.DefaultRunBudgetLimits()
			limits.ModelCalls = 12
			input := testInput(t, clock, limits)
			recorder := newEventRecorder()
			outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(t.Context(), input, recorder)
			if err := outcome.Validate(input); err != nil {
				t.Fatal(err)
			}
			if len(model.Requests()) != 10 || len(tool.Calls()) != 9 {
				t.Fatal("Final validation retried or lost a read")
			}
			if mode == "accepted" {
				if outcome.Diagnosis != nil && outcome.Diagnosis.AnswerMarkdown != "The returned checkout observations show unready Pods; partial Events do not establish a complete cause." {
					t.Fatal("inline citation reached the accepted answer")
				}
				if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || len(outcome.Diagnosis.ClaimCoverage[0].EvidenceIDs) != 18 || outcome.Diagnosis.Completeness.StopReason != domain.RunTerminalPartialResult {
					t.Fatalf("Accepted partial multi-read result failed: %s", outcome.Diagnostic)
				}
			} else {
				want := domain.FailureEvidenceUnknown
				if mode == "duplicate" {
					want = domain.FailureEvidenceDuplicate
				}
				if outcome.Diagnosis != nil || outcome.Status != domain.AgentRunStatusFailed || outcome.Diagnostic != want {
					t.Fatalf("Invalid reference admitted or misclassified: %s", outcome.Diagnostic)
				}
			}
			assertTerminalSequence(t, recorder.Events())
		})
	}
}

// This checks the protocol and history boundary, not a scripted model's ability
// to recognize a misspelling. Natural target selection is evaluated separately.
func TestCandidateConfirmationUsesFreshEvidence(t *testing.T) {
	for _, historicCitation := range []bool{false, true} {
		t.Run(fmt.Sprintf("historic_citation=%t", historicCitation), func(t *testing.T) {
			clock := newTestClock()
			firstAnswer := `{"answer_markdown":"The list contains checkout, not chekout. Did you mean checkout?","evidence_citations":[{"claim":"The list contains checkout.","claim_type":"current_observation","evidence_ids":["` + agent.ModelEvidenceReference(testEvidenceID) + `"]}],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[{"kind":"absent","detail":"The intended resource has not been confirmed.","impact":"Diagnosis must wait for target confirmation."}],"questions":[]}`
			citation := secondEvidenceID
			if historicCitation {
				citation = testEvidenceID
			}
			model := &recordingModel{scripts: []modelScript{
				scriptedChunks(toolCallChunks(agent.ToolSelection{
					ID: "candidates", Name: domain.ToolNameListResources,
					ArgumentsJSON: `{"filters":null,"format":"list","limit":10,"namespace":null,"purpose":"Check the literal name and available candidates.","resource_type":"pods"}`,
				})...),
				scriptedChunks(diagnosisChunks(firstAnswer)...),
				func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
					if len(request.Messages) != 4 || request.Messages[1].Content != "Why is chekout restarting?" || request.Messages[3].Content != "I mean the Pod checkout. Check it now." {
						t.Fatal("Target confirmation lost the original question or current user intent.")
					}
					history, err := agent.DecodeDiagnosticResponse(request.Messages[2].Content)
					if err != nil || len(history.ClaimCoverage) != 0 || history.Clarification != nil {
						t.Fatal("History restored candidate Evidence or clarification authority.")
					}
					return scriptedChunks(toolCallChunks(resourceCall("confirmed", "checkout"))...)(ctx, request)
				},
				scriptedChunks(diagnosisChunks(`{"answer_markdown":"The confirmed Pod checkout currently has restart_count=0.","evidence_citations":[{"claim":"Pod checkout currently has restart_count=0.","claim_type":"current_observation","evidence_ids":["` + agent.ModelEvidenceReference(citation) + `"]}],"proposed_actions":[]}`)...),
			}}
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				id, fact := testEvidenceID, "The bounded Pod list contains checkout."
				if call.Name() == domain.ToolNameGetResource {
					id, fact = secondEvidenceID, "Pod checkout currently has restart_count=0."
				}
				result := successfulToolResult(t, call, id, clock.Now(), `{"items":[{"name":"checkout","restart_count":0}]}`)
				result.Evidence[0].Resource.Name = "checkout"
				result.Evidence[0].Fact = fact
				return result
			}}
			runtime := testAdapter(t, clock, model, tool, newTestScopeGuard())
			scope := testInput(t, clock, agent.DefaultRunBudgetLimits()).Scope()
			conversation, err := agent.NewConversationContext(testSessionID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			input, err := agent.NewRunInputWithContext(testRunID, testSessionID, testMessageID, "Why is chekout restarting?", scope, nil, agent.DefaultRunBudgetLimits(), conversation)
			if err != nil {
				t.Fatal(err)
			}
			outcome := runtime.Run(t.Context(), input, newEventRecorder())
			if outcome.Validate(input) != nil || outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Diagnosis.Clarification != nil || len(outcome.Diagnosis.MissingInformation) != 1 || len(tool.Calls()) != 1 {
				t.Fatalf("Post-read identity question was rejected or expanded: %s", outcome.Diagnostic)
			}
			turns := []agent.ConversationTurn{
				{MessageID: testMessageID, RunID: testRunID, Role: domain.MessageRoleUser, Content: input.Question()},
				{MessageID: "00000000-0000-7000-8000-000000009901", RunID: testRunID, RunSequence: 1, Role: domain.MessageRoleAssistant, Content: outcome.Diagnosis.AnswerMarkdown},
			}
			for index := range turns {
				turns[index].ContentHash = domain.MessageContentHash(turns[index].Content)
			}
			conversation, err = agent.NewConversationContext(testSessionID, turns, nil)
			if err != nil {
				t.Fatal(err)
			}
			input, err = agent.NewRunInputWithContext("00000000-0000-7000-8000-000000009902", testSessionID, "00000000-0000-7000-8000-000000009903", "I mean the Pod checkout. Check it now.", scope, nil, agent.DefaultRunBudgetLimits(), conversation)
			if err != nil {
				t.Fatal(err)
			}
			outcome = runtime.Run(t.Context(), input, newEventRecorder())
			if outcome.Validate(input) != nil || len(tool.Calls()) != 2 || len(model.Requests()) != 4 {
				t.Fatal("Confirmation retried, skipped a fresh read, or produced an invalid outcome.")
			}
			if historicCitation {
				if outcome.Status != domain.AgentRunStatusFailed || outcome.Diagnosis != nil || outcome.Diagnostic != domain.FailureEvidenceUnknown {
					t.Fatal("Historic candidate Evidence was accepted as a current observation.")
				}
			} else if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || len(outcome.Diagnosis.ConfirmedFacts) != 1 || outcome.Diagnosis.ConfirmedFacts[0].EvidenceIDs[0] != secondEvidenceID {
				t.Fatalf("Fresh confirmed-target Evidence was not accepted: %s", outcome.Diagnostic)
			}
		})
	}
}

func TestEmptyCandidateLookupAnswersWithLimitations(t *testing.T) {
	for _, kind := range []domain.ClaimKind{domain.ClaimUnsupportedObservation, domain.ClaimCurrentObservation} {
		t.Run(string(kind), func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{scripts: []modelScript{
				scriptedChunks(toolCallChunks(agent.ToolSelection{
					ID: "exact-name", Name: domain.ToolNameListResources,
					ArgumentsJSON: `{"filters":[{"field":"name","operator":"equals","value":"chekout"}],"format":"list","limit":10,"namespace":null,"purpose":"Check the literal Pod name.","resource_type":"pods"}`,
				})...),
				scriptedChunks(diagnosisChunks(`{"answer_markdown":"The exact Pod lookup for chekout returned no match in this Namespace. Please confirm its name and kind.","evidence_citations":[{"claim":"The exact lookup returned no candidate Evidence.","claim_type":"` + string(kind) + `","evidence_ids":[]}],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[{"kind":"absent","detail":"The exact lookup returned no candidate Evidence.","impact":"The intended target remains unconfirmed."}],"questions":[]}`)...),
			}}
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				return emptyToolResult(t, call, clock.Now())
			}}
			input, err := agent.NewRunInput(testRunID, testSessionID, testMessageID, "Why is chekout restarting?", testInput(t, clock, agent.DefaultRunBudgetLimits()).Scope(), nil, agent.DefaultRunBudgetLimits())
			if err != nil {
				t.Fatal(err)
			}
			recorder := newEventRecorder()
			outcome := testAdapter(t, clock, model, tool, newTestScopeGuard()).Run(t.Context(), input, recorder)
			if outcome.Validate(input) != nil || len(tool.Calls()) != 1 || len(model.Requests()) != 2 {
				t.Fatal("The empty lookup retried, investigated another target, or produced an invalid outcome.")
			}
			if kind == domain.ClaimCurrentObservation {
				if outcome.Status != domain.AgentRunStatusFailed || outcome.Diagnosis != nil || outcome.Diagnostic != domain.FailureClaimUnsupported {
					t.Fatal("An unsupported current observation was accepted.")
				}
			} else if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Diagnosis.Clarification != nil || len(outcome.Diagnosis.ConfirmedFacts) != 0 || len(outcome.Diagnosis.MissingInformation) == 0 {
				t.Fatalf("The checked identity gap was rejected or gained authority: %s", outcome.Diagnostic)
			}
			assertTerminalSequence(t, recorder.Events())
		})
	}
}
