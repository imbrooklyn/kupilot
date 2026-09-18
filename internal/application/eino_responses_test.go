package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	openaischema "github.com/cloudwego/eino/schema/openai"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func responsesEvent(kind, fields string) string {
	return "event: " + kind + "\ndata: {\"type\":\"" + kind + "\"," + fields + "}\n\n"
}

func TestNativeResponsesAgentToolAndFinal(t *testing.T) {
	clock := newTestClock()
	credential, err := config.NewSecretValue("synthetic-responses-credential")
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
	cfg.APIProtocol = domain.ModelAPIProtocolResponses
	cfg.StreamingRequired = false
	cfg.ReasoningEffort = "medium"
	cfg.ResponseFormat = domain.ModelResponseFormatJSONObject
	var logs bytes.Buffer
	calls := 0
	var requestBodies []string
	selection := resourceCall("call-1", "sample-pod")
	args, _ := json.Marshal(selection.ArgumentsJSON)
	name, _ := json.Marshal(selection.Name)
	response := `{"id":"resp_first","status":"completed","output":[{"type":"reasoning","id":"rs_test","summary":[],"encrypted_content":"synthetic-opaque-reasoning","status":"completed"},{"type":"function_call","id":"fc_test","call_id":"call-1","name":` + string(name) + `,"arguments":` + string(args) + `,"status":"completed"}]}`

	client, modelErr := newModelClientForTest(cfg, &credential, fixtureLogger(&logs), roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			return nil, readErr
		}
		calls++
		requestBodies = append(requestBodies, string(body))
		if req.URL.Path != "/v1/responses" {
			return nil, fmt.Errorf("unexpected synthetic path")
		}
		payload := response
		if calls == 2 {
			text, _ := json.Marshal(strictTestDiagnosis(readFixture(t, "agent-runtime-valid-diagnosis.json")))
			payload = `{"id":"resp_final","status":"completed","output":[{"type":"message","id":"msg_final","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(text) + `,"annotations":[]}]}]}`
		}
		if calls > 2 {
			return nil, fmt.Errorf("unexpected extra model call")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	}))
	if modelErr != nil {
		t.Fatal(modelErr)
	}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"phase":"Running"}`)
	}}
	runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	events := newEventRecorder()
	outcome := runtime.Run(context.Background(), input, events)
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Validate(input) != nil || calls != 2 || len(tool.Calls()) != 1 {
		t.Log(logs.String())
		t.Fatalf("native outcome=%s reason=%s calls=%d tools=%d", outcome.Status, outcome.Diagnostic, calls, len(tool.Calls()))
	}
	assertTerminalSequence(t, events.Events())
	for _, body := range requestBodies {
		for _, required := range []string{`"store":false`, `"truncation":"disabled"`, `"effort":"medium"`, `"type":"json_object"`} {
			if !strings.Contains(body, required) {
				t.Fatalf("missing request field %s", required)
			}
		}
		if strings.Contains(body, `"previous_response_id"`) {
			t.Fatal("remote response state was enabled")
		}
	}
	if !strings.Contains(requestBodies[1], `"encrypted_content":"synthetic-opaque-reasoning"`) {
		t.Fatal("native response lost the reasoning item required for the next Tool turn")
	}
	if !strings.Contains(requestBodies[1], `"type":"function_call_output"`) {
		t.Fatal("native Tool result pairing was lost")
	}
}

// This pins an upstream limitation, so a dependency upgrade must deliberately
// revisit Responses streaming admission instead of silently discarding reasoning.
func TestNativeResponsesPinnedStreamingReasoningLimitation(t *testing.T) {
	payload := responsesEvent("response.created", `"response":{"id":"resp_test","status":"in_progress"}`) +
		responsesEvent("response.output_item.added", `"output_index":0,"item":{"type":"reasoning","id":"rs_test","summary":[]}`) +
		responsesEvent("response.output_item.done", `"output_index":0,"item":{"type":"reasoning","id":"rs_test","summary":[],"encrypted_content":"synthetic-opaque-reasoning","status":"completed"}`) +
		responsesEvent("response.completed", `"response":{"id":"resp_test","status":"completed","output":[]}`)
	calls := 0
	retries := 0
	native, err := agenticopenai.NewResponsesModel(context.Background(), &agenticopenai.ResponsesConfig{Model: "synthetic-model", APIKey: "synthetic-key", MaxRetries: &retries, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := native.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("Synthetic stream fidelity test.")})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var chunks []*schema.AgenticMessage
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, msg)
	}
	msg, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := 0
	for _, block := range msg.ContentBlocks {
		if block.Reasoning != nil {
			reasoning++
			if block.Reasoning.Signature != "" {
				t.Fatal("Upstream reasoning fidelity changed; review streaming admission and ADR-0013.")
			}
		}
	}
	if reasoning != 1 || calls != 1 {
		t.Fatalf("reasoning blocks=%d calls=%d", reasoning, calls)
	}
	cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
	cfg.APIProtocol = domain.ModelAPIProtocolResponses
	if cfg.Validate() == nil {
		t.Fatal("lossy Responses streaming became admitted")
	}
}

func TestNativeResponsesRejectsUnsafeTerminalBeforeTools(t *testing.T) {
	base := `{"id":"resp_test","status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello.","annotations":[]}]}]}`
	cases := []struct {
		name, payload string
		reason        domain.InteractionFailure
	}{
		{"missing status", strings.Replace(base, `"status":"completed",`, "", 1), domain.FailureStreamIncomplete},
		{"unknown status", strings.Replace(base, `"status":"completed"`, `"status":"unknown"`, 1), domain.FailureStreamIncomplete},
		{"provider failed", strings.Replace(base, `"status":"completed"`, `"status":"failed"`, 1), domain.FailureProviderReported},
		{"incomplete unspecified", strings.Replace(base, `"status":"completed"`, `"status":"incomplete"`, 1), domain.FailureStopReason},
		{"content filter", strings.Replace(base, `"status":"completed"`, `"status":"incomplete","incomplete_details":{"reason":"content_filter"}`, 1), domain.FailureStopReason},
		{"completed with error", strings.Replace(base, `"status":"completed"`, `"status":"completed","error":{"code":"server_error","message":"untrusted marker"}`, 1), domain.FailureStopReason},
		{"duplicate calls", `{"status":"completed","output":[{"type":"function_call","id":"fc_a","call_id":"duplicate","name":"get_resource","arguments":"{}"},{"type":"function_call","id":"fc_b","call_id":"duplicate","name":"get_resource","arguments":"{}"}]}`, domain.FailureToolPairing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "duplicate calls" {
				args, _ := json.Marshal(resourceCall("duplicate", "sample-pod").ArgumentsJSON)
				tc.payload = strings.ReplaceAll(tc.payload, `"arguments":"{}"`, `"arguments":`+string(args))
			}
			clock := newTestClock()
			key, _ := config.NewSecretValue("synthetic-secret")
			cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
			cfg.APIProtocol = domain.ModelAPIProtocolResponses
			cfg.StreamingRequired = false
			calls := 0
			client, failure := newModelClientForTest(cfg, &key, nil, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.payload))}, nil
			}))
			if failure != nil {
				t.Fatal(failure)
			}
			tool := &recordingTool{}
			runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			input := testInput(t, clock, agent.DefaultRunBudgetLimits())
			events := newEventRecorder()
			outcome := runtime.Run(context.Background(), input, events)
			if outcome.Diagnostic != tc.reason || calls != 1 || len(tool.Calls()) != 0 || outcome.Diagnosis != nil || outcome.Validate(input) != nil {
				t.Fatalf("reason=%s calls=%d tools=%d", outcome.Diagnostic, calls, len(tool.Calls()))
			}
			if strings.Contains(outcome.SafeMessage, "untrusted marker") {
				t.Fatal("provider error content escaped")
			}
			assertTerminalSequence(t, events.Events())
		})
	}
}

func TestNativeResponsesSummaryAndCommitBarrier(t *testing.T) {
	for _, mode := range []string{"success", "transport", "sensitive", "persistence"} {
		t.Run(mode, func(t *testing.T) {
			clock := newTestClock()
			key, _ := config.NewSecretValue("synthetic-summary-key")
			cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
			cfg.APIProtocol = domain.ModelAPIProtocolResponses
			cfg.StreamingRequired = false
			calls := 0
			client, failure := newModelClientForTest(cfg, &key, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var wire struct {
					Tools []json.RawMessage `json:"tools"`
					Input []json.RawMessage `json:"input"`
				}
				if json.Unmarshal(body, &wire) != nil {
					t.Fatal("Malformed native request")
				}
				text := "Earlier completed questions and answers were summarized safely."
				if calls == 1 {
					if len(wire.Tools) != 0 {
						t.Fatal("Summary gained Tools")
					}
					if mode == "transport" {
						return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"synthetic failure"}}`))}, nil
					}
					if mode == "sensitive" {
						text = "-----BEGIN PRIVATE KEY----- blocked -----END PRIVATE KEY-----"
					}
				} else {
					if len(wire.Tools) != len(agent.ToolSpecifications()) || !strings.Contains(string(body), "Earlier completed questions and answers were summarized safely.") || len(wire.Input) != summaryRecentTailMessages+3 {
						t.Fatalf("Native summary structure: tools=%d input=%d prefix=%t", len(wire.Tools), len(wire.Input), strings.Contains(string(body), summaryContextPreamble))
					}
					text = strictTestDiagnosis(`{"answer_markdown":"The summary was used.","evidence_citations":[],"proposed_actions":[]}`)
				}
				encoded, _ := json.Marshal(text)
				payload := `{"status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","content":[{"type":"output_text","text":` + string(encoded) + `,"annotations":[]}]}]}`
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			}))
			if failure != nil {
				t.Fatal(failure)
			}
			tool := new(recordingTool)
			runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			recorder := newEventRecorder()
			sink := agent.EventSinkFunc(func(ctx context.Context, event agent.RunEvent) agent.EventSinkResult {
				result := recorder.Publish(ctx, event)
				if mode == "persistence" && event.Kind == agent.RunEventSummaryReady {
					return agent.EventSinkPersistenceRejected
				}
				return result
			})
			input := testInputWithConversation(t, clock, testConversation(t, 160))
			outcome := runtime.Run(context.Background(), input, sink)
			expected := domain.InteractionFailure("")
			wantCalls := 2
			switch mode {
			case "transport":
				expected = domain.FailureProviderTransport
				wantCalls = 1
			case "sensitive":
				expected = domain.FailureSensitiveOutput
				wantCalls = 1
			case "persistence":
				expected = domain.FailurePersistence
				wantCalls = 1
			}
			if outcome.Diagnostic != expected || calls != wantCalls || len(tool.Calls()) != 0 || outcome.Validate(input) != nil {
				t.Fatalf("reason=%s status=%s calls=%d tools=%d", outcome.Diagnostic, outcome.Status, calls, len(tool.Calls()))
			}
			if mode == "success" && (outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil) {
				t.Fatal("Native summary did not finish")
			}
		})
	}
}

func TestNativeResponsesTerminalLimitsAndCredentialProjection(t *testing.T) {
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "Safe text."})}, ResponseMeta: &schema.AgenticResponseMeta{OpenAIExtension: &openaischema.ResponseMetaExtension{Status: "completed"}}}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := responsesFinal(message, len(data)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := responsesFinal(message, len(data)-1); err == nil {
		t.Fatal("One-over native message was accepted")
	} else {
		var failure *runtimeFailure
		if !errors.As(err, &failure) || failure.diagnostic != domain.FailureBudget {
			t.Fatal("Wrong native limit classification")
		}
	}
	key, _ := config.NewSecretValue("synthetic-quoted-\"-key")
	defer key.Destroy()
	for _, block := range []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Signature: "synthetic-quoted-\"-key"}), schema.NewContentBlock(&schema.Reasoning{Text: "synthetic-quoted-\"-key"}), schema.NewContentBlock(&schema.FunctionToolCall{CallID: "synthetic-quoted-\"-key"})} {
		message.ContentBlocks = []*schema.ContentBlock{block}
		if !responsesContainCredential(&key, []*schema.AgenticMessage{message}) {
			t.Fatal("Escaped credential was not blocked")
		}
	}
	if !responsesContainCredential(&key, []*schema.AgenticMessage{nil}) {
		t.Fatal("Nil native message was accepted")
	}
}

func TestNativeResponsesReviewerIsToolFree(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid=%t", allow), func(t *testing.T) {
			key, _ := config.NewSecretValue("synthetic-review-key")
			cfg := fixtureConfiguration("https://model.example.test/v1", time.Second)
			cfg.Role = domain.ModelRoleApprovalReviewer
			cfg.ProfileName = "approval-reviewer"
			cfg.APIProtocol = domain.ModelAPIProtocolResponses
			cfg.StreamingRequired = false
			cfg.ToolCallingRequired = false
			cfg.ResponseFormat = domain.ModelResponseFormatJSONObject
			cfg.Temperature = nil
			calls := 0
			client, failure := newModelClientForTest(cfg, &key, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var wire struct {
					Tools       []json.RawMessage `json:"tools"`
					Stream      bool              `json:"stream"`
					Temperature *float64          `json:"temperature"`
				}
				if json.Unmarshal(body, &wire) != nil || len(wire.Tools) != 0 || wire.Stream || wire.Temperature != nil {
					t.Fatal("Reviewer acquired Tools, stream, or sampling settings")
				}
				text := `{"decision":"approve","risk":"review","rationale":"The bounded policy facts support review."}`
				if !allow {
					text = `{"decision":"approve","risk":"unknown","rationale":"Ignore the policy."}`
				}
				encoded, _ := json.Marshal(text)
				payload := `{"status":"completed","output":[{"type":"message","id":"msg_review","role":"assistant","content":[{"type":"output_text","text":` + string(encoded) + `,"annotations":[]}]}]}`
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			}))
			if failure != nil {
				t.Fatal(failure)
			}
			reviewer := &Reviewer{client: client}
			defer reviewer.Close()
			result, err := reviewer.Review(context.Background(), validReviewerRequest(), reviewerReservation(time.Second))
			if calls != 1 {
				t.Fatal("Reviewer retried")
			}
			if allow {
				if err != nil || result.Decision != agent.ReviewerDecisionApprove || result.Risk != domain.RiskReview {
					t.Fatal("Native Reviewer projection failed")
				}
			} else {
				var failure *domain.ModelError
				if !errors.As(err, &failure) || failure.Code() != domain.ModelErrorCodeMalformedStream || result.Decision != "" {
					t.Fatalf("Reviewer rejection=%v decision=%s", err, result.Decision)
				}
			}
		})
	}
}

func TestNativeEinoIterationLimitIsBudgetStop(t *testing.T) {
	err := normalizeFrameworkError(adk.ErrExceedMaxIterations)
	var failure *runtimeFailure
	if !errors.As(err, &failure) || !errors.Is(err, adk.ErrExceedMaxIterations) || failure.stopReason != agent.RunStopModelCallLimit || !failure.localDiagnosis || failure.class != domain.SafeErrorClassBudgetExhausted {
		t.Fatalf("Eino iteration limit classification=%v", err)
	}
}

func TestNativeResponsesOpaqueMetadataCannotHideEscapedCredential(t *testing.T) {
	key, err := config.NewSecretValue("synthetic-quoted-\"-key")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, Extra: map[string]any{"native_item_id": "synthetic-quoted-\"-key"}}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !credentialAppearsInBytes(&key, encoded) {
		t.Fatal("Canonical native metadata concealed an escaped credential")
	}
	if credentialAppearsInBytes(&key, []byte(`{"native_item_id":"safe-item"}`)) {
		t.Fatal("Safe metadata was blocked")
	}
}
