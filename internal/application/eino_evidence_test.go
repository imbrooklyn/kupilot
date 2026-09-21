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
