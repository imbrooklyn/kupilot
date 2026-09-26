package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestNativeOutputSchemasRemainBoundToInvocation(t *testing.T) {
	for _, protocol := range []domain.ModelAPIProtocol{domain.ModelAPIProtocolChatCompletions, domain.ModelAPIProtocolResponses} {
		for _, invocation := range []string{"answer", "plan", "review", "summary", "missing-reference", "rejected"} {
			t.Run(string(protocol)+"/"+invocation, func(t *testing.T) {
				cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
				cfg.APIProtocol, cfg.StreamingRequired = protocol, protocol == domain.ModelAPIProtocolChatCompletions
				if protocol == domain.ModelAPIProtocolResponses {
					temperature := 0.125
					cfg.Temperature = &temperature
				}
				cfg.ResponseFormat = domain.ModelResponseFormatJSONSchema
				schemaName, schemaText := "kupilot_answer", nativeAnswerSchema
				content := strictTestDiagnosis(`{"answer_markdown":"Hello.","evidence_citations":[],"proposed_actions":[]}`)
				switch invocation {
				case "plan":
					schemaName, schemaText = "kupilot_plan", nativePlanSchema
					content = planResponseJSON("Inspect readiness", "Inspect the exact Pod after confirmation.", "")
				case "review":
					cfg.Role, cfg.ProfileName = domain.ModelRoleApprovalReviewer, "approval-reviewer"
					cfg.StreamingRequired, cfg.ToolCallingRequired = false, false
					schemaName, schemaText = "kupilot_review", nativeReviewerSchema
					content = `{"decision":"approve","risk":"review","rationale":"The bounded policy facts support review."}`
				case "summary":
					schemaName, content = "", "Bounded summary."
				case "missing-reference":
					content = strictTestDiagnosis(`{"answer_markdown":"The Pod is Ready.","evidence_citations":[{"claim":"The Pod is Ready.","claim_type":"current_observation","evidence_ids":[]}],"proposed_actions":[]}`)
				}
				key, err := config.NewSecretValue("synthetic-schema-credential")
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				client, failure := newModelClientForTest(cfg, &key, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					var wire struct {
						ResponseFormat json.RawMessage `json:"response_format"`
						Text           struct {
							Format json.RawMessage `json:"format"`
						} `json:"text"`
						Temperature *float64          `json:"temperature"`
						Tools       []json.RawMessage `json:"tools"`
					}
					if json.Unmarshal(body, &wire) != nil {
						t.Fatal("Invalid native request.")
					}
					format := wire.ResponseFormat
					if protocol == domain.ModelAPIProtocolResponses {
						format = wire.Text.Format
					}
					if schemaName == "" {
						if len(format) != 0 || len(wire.Tools) != 0 {
							t.Fatal("Summary inherited a final-output schema or Tools.")
						}
					} else {
						var native struct {
							Type       string          `json:"type"`
							Name       string          `json:"name"`
							Strict     bool            `json:"strict"`
							Schema     json.RawMessage `json:"schema"`
							JSONSchema json.RawMessage `json:"json_schema"`
						}
						if json.Unmarshal(format, &native) != nil || native.Type != "json_schema" {
							t.Fatal("The explicit native strict format was omitted.")
						}
						if protocol == domain.ModelAPIProtocolChatCompletions {
							if json.Unmarshal(native.JSONSchema, &native) != nil {
								t.Fatal("Invalid Chat Completions schema wrapper.")
							}
						}
						var actual, expected any
						if json.Unmarshal(native.Schema, &actual) != nil || json.Unmarshal([]byte(schemaText), &expected) != nil || !reflect.DeepEqual(actual, expected) || !native.Strict || native.Name != schemaName {
							t.Fatal("Native serialization changed the invocation schema or its constraints.")
						}
						if invocation == "review" && len(wire.Tools) != 0 {
							t.Fatal("Reviewer acquired Tools.")
						}
					}
					if !reflect.DeepEqual(wire.Temperature, cfg.Temperature) {
						t.Fatal("Strict format changed the explicit sampling setting.")
					}
					if invocation == "rejected" {
						response := jsonHTTPResponse(`{"error":{"message":"Unsupported synthetic format."}}`)
						response.StatusCode = http.StatusBadRequest
						return response, nil
					}
					quoted, _ := json.Marshal(content)
					if protocol == domain.ModelAPIProtocolResponses {
						return jsonHTTPResponse(`{"status":"completed","output":[{"type":"message","id":"msg_schema","role":"assistant","content":[{"type":"output_text","text":` + string(quoted) + `,"annotations":[]}]}]}`), nil
					}
					if invocation == "review" || invocation == "summary" {
						return reviewerJSONResponse(content), nil
					}
					bodyText := `data: {"id":"schema-stream","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(quoted) + `},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bodyText))}, nil
				}))
				if failure != nil {
					t.Fatal(failure)
				}
				defer client.close()
				if invocation == "review" {
					reviewer := &Reviewer{client: client}
					result, err := reviewer.Review(t.Context(), validReviewerRequest(), reviewerReservation(time.Second))
					if err != nil || result.Decision != agent.ReviewerDecisionApprove {
						t.Fatal("Strict Reviewer failed.")
					}
				} else if invocation == "summary" {
					message, failure := client.generate(t.Context(), fixtureRequestID, []*schema.Message{schema.SystemMessage("Summarize safely."), schema.UserMessage("Bounded content.")}, agent.CallReservation{RequestBytes: 4096, OutputBytes: 1024})
					if failure != nil || message == nil || message.Content != content {
						t.Fatal("Plain summary failed.")
					}
				} else {
					clock := newTestClock()
					tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
						return emptyToolResult(t, call, clock.Now())
					}}
					runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
					if err != nil {
						t.Fatal(err)
					}
					defer runtime.Close()
					input := testInput(t, clock, agent.DefaultRunBudgetLimits())
					if invocation == "plan" {
						input = withPlanOnlyMode(t, input)
					}
					outcome := runtime.Run(t.Context(), input, newEventRecorder())
					want := domain.InteractionFailure("")
					if invocation == "missing-reference" {
						want = domain.FailureClaimUnsupported
					}
					if invocation == "rejected" {
						want = domain.FailureProviderRequest
					}
					if outcome.Validate(input) != nil || outcome.Diagnostic != want || len(tool.Calls()) != 0 || (want == "" && outcome.Status != domain.AgentRunStatusCompleted) || (want != "" && outcome.Diagnosis != nil) {
						t.Fatalf("Strict format outcome=%s diagnostic=%s; local validation or zero-call rejection changed.", outcome.Status, outcome.Diagnostic)
					}
				}
				if calls != 1 {
					t.Fatal("The native schema path retried or made an extra request.")
				}
			})
		}
	}
}
