package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestReviewerUsesOneStrictNonStreamingToolFreeRequest(t *testing.T) {
	t.Parallel()

	credentialCanary := strings.Repeat("r", 43) + "-reviewer"
	var captured []byte
	var calls atomic.Int64
	reviewer := newReviewerForTestWithResponseFormat(t, credentialCanary, domain.ModelResponseFormatJSONObject, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		captured = append([]byte(nil), body...)
		if request.Header.Get("Authorization") != "Bearer "+credentialCanary {
			return nil, errors.New("missing reviewer credential")
		}
		return reviewerJSONResponse(`{"decision":"approve","risk":"review","rationale":"The bounded policy facts support escalation-safe review."}`), nil
	}))
	request := validReviewerRequest()
	result, err := reviewer.Review(context.Background(), request, reviewerReservation(time.Second))
	if err != nil || result.Decision != agent.ReviewerDecisionApprove ||
		result.Risk != domain.RiskReview || result.Rationale != "The bounded policy facts support escalation-safe review." || calls.Load() != 1 {
		t.Fatalf("Review() = %#v/%v, calls %d", result, err, calls.Load())
	}
	var payload struct {
		Stream   *bool             `json:"stream"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if json.Unmarshal(captured, &payload) != nil || len(payload.Tools) != 0 ||
		payload.Stream != nil && *payload.Stream || len(payload.Messages) != 2 ||
		payload.ResponseFormat == nil || payload.ResponseFormat.Type != "json_object" ||
		payload.Messages[0].Role != "system" || payload.Messages[1].Role != "user" ||
		!strings.Contains(payload.Messages[1].Content, request.NormalizedAction) ||
		bytes.Contains(captured, []byte(credentialCanary)) {
		t.Fatalf("reviewer request body violated the fixed projection: %s", captured)
	}
}

func TestNativeOllamaReviewerUsesOneCredentialFreeStructuredRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
	configuration.ProfileName = "approval-reviewer"
	configuration.Role = domain.ModelRoleApprovalReviewer
	configuration.Temperature = testTemperature(0)
	configuration.MaxOutputTokens = 0
	configuration.StreamingRequired = false
	configuration.ToolCallingRequired = false
	client, modelErr := newModelClientForTest(configuration, nil, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Path != "/api/chat" || request.Header.Get("Authorization") != "" {
			return nil, errors.New("native reviewer violated the fixed route")
		}
		body := `{"model":"fixture-model","created_at":"2026-09-15T00:00:00Z","message":{"role":"assistant","content":"{\"decision\":\"approve\",\"risk\":\"review\",\"rationale\":\"The bounded policy facts support review.\"}"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":8}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if modelErr != nil {
		t.Fatalf("newModelClientForTest(native reviewer) error = %v", modelErr)
	}
	reviewer := &Reviewer{client: client}
	t.Cleanup(reviewer.Close)
	result, err := reviewer.Review(context.Background(), validReviewerRequest(), reviewerReservation(time.Second))
	if err != nil || calls.Load() != 1 || result.Decision != agent.ReviewerDecisionApprove ||
		result.Risk != domain.RiskReview {
		t.Fatalf("native Review() = %#v/%v, calls %d", result, err, calls.Load())
	}
}

func TestReviewerRejectsMalformedProseToolAndSensitiveResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "prose", body: "approve because it looks safe"},
		{name: "unknown field", body: `{"decision":"approve","risk":"review","rationale":"bounded","authority":"granted"}`},
		{name: "invalid decision", body: `{"decision":"allow","risk":"review","rationale":"bounded"}`},
		{name: "sensitive rationale", body: `{"decision":"deny","risk":"review","rationale":"-----BEGIN PRIVATE KEY----- blocked -----END PRIVATE KEY-----"}`},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			reviewer := newReviewerForTest(t, strings.Repeat("s", 43)+"-reviewer", roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return reviewerJSONResponse(current.body), nil
			}))
			result, err := reviewer.Review(context.Background(), validReviewerRequest(), reviewerReservation(time.Second))
			var modelErr *domain.ModelError
			if result != (agent.ReviewerResult{}) || !errors.As(err, &modelErr) ||
				modelErr.Code() != domain.ModelErrorCodeMalformedStream || calls.Load() != 1 {
				t.Fatalf("Review() = %#v/%v, calls %d", result, err, calls.Load())
			}
		})
	}

	t.Run("Tool response", func(t *testing.T) {
		t.Parallel()
		credential := strings.Repeat("t", 43) + "-reviewer"
		reviewer := newReviewerForTest(t, credential, roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"id":"response-review","object":"chat.completion","created":1,"model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"forbidden","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
			return jsonHTTPResponse(body), nil
		}))
		_, err := reviewer.Review(context.Background(), validReviewerRequest(), reviewerReservation(time.Second))
		var modelErr *domain.ModelError
		if !errors.As(err, &modelErr) || modelErr.Class() != domain.SafeErrorClassInvalidExternalResponse {
			t.Fatalf("Tool-bearing reviewer response error = %v", err)
		}
	})
}

func TestReviewerCancellationAndTimeoutAreStableAndSingleAttempt(t *testing.T) {
	t.Parallel()

	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		var calls atomic.Int64
		reviewer := newReviewerForTest(t, strings.Repeat("c", 43)+"-reviewer", roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := reviewer.Review(ctx, validReviewerRequest(), reviewerReservation(time.Second))
			done <- err
		}()
		<-started
		cancel()
		err := <-done
		var modelErr *domain.ModelError
		if !errors.As(err, &modelErr) || modelErr.Code() != domain.ModelErrorCodeCancelled || calls.Load() != 1 {
			t.Fatalf("cancelled Review() error/calls = %v/%d", err, calls.Load())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int64
		reviewer := newReviewerForTest(t, strings.Repeat("d", 43)+"-reviewer", roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}))
		_, err := reviewer.Review(context.Background(), validReviewerRequest(), reviewerReservation(time.Millisecond))
		var modelErr *domain.ModelError
		if !errors.As(err, &modelErr) || modelErr.Code() != domain.ModelErrorCodeTimeout || calls.Load() != 1 {
			t.Fatalf("timed-out Review() error/calls = %v/%d", err, calls.Load())
		}
	})
}

func TestReviewerInvalidOrSensitiveInputMakesZeroRequests(t *testing.T) {
	t.Parallel()

	credentialCanary := strings.Repeat("i", 43) + "-reviewer"
	var calls atomic.Int64
	reviewer := newReviewerForTest(t, credentialCanary, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reviewerJSONResponse(`{"decision":"deny","risk":"review","rationale":"bounded"}`), nil
	}))
	invalid := validReviewerRequest()
	invalid.UserIntent = credentialCanary
	if result, err := reviewer.Review(context.Background(), invalid, reviewerReservation(time.Second)); err == nil || result != (agent.ReviewerResult{}) {
		t.Fatalf("sensitive Review() = %#v/%v", result, err)
	}
	invalid = validReviewerRequest()
	invalid.RequestID = ""
	if result, err := reviewer.Review(context.Background(), invalid, reviewerReservation(time.Second)); err == nil || result != (agent.ReviewerResult{}) {
		t.Fatalf("invalid Review() = %#v/%v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("denied reviewer requests = %d, want zero", calls.Load())
	}
}

func newReviewerForTest(t *testing.T, credentialText string, transport http.RoundTripper) *Reviewer {
	return newReviewerForTestWithResponseFormat(t, credentialText, domain.ModelResponseFormatPrompt, transport)
}

func newReviewerForTestWithResponseFormat(
	t *testing.T,
	credentialText string,
	responseFormat domain.ModelResponseFormat,
	transport http.RoundTripper,
) *Reviewer {
	t.Helper()
	credential, err := config.NewSecretValue(credentialText)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	configuration := fixtureConfiguration("https://model.example.test/v1", time.Second)
	configuration.ResponseFormat = responseFormat
	configuration.ProfileName = "approval-reviewer"
	configuration.Role = domain.ModelRoleApprovalReviewer
	configuration.Temperature = testTemperature(0)
	configuration.MaxOutputTokens = 0
	configuration.StreamingRequired = false
	configuration.ToolCallingRequired = false
	client, modelErr := newModelClientForTest(configuration, &credential, nil, transport)
	if modelErr != nil {
		t.Fatalf("newModelClientForTest() error = %v", modelErr)
	}
	reviewer := &Reviewer{client: client}
	t.Cleanup(reviewer.Close)
	return reviewer
}

func validReviewerRequest() agent.ReviewerRequest {
	return agent.ReviewerRequest{
		RequestID:        "00000000-0000-7000-8000-000000008901",
		UserIntent:       "Restart the exact Deployment after review.",
		NormalizedAction: "operation=restart_deployment; target=team-a/api; risk=review",
		PolicyFacts:      "profile=auto-review; deterministic_risk=review; authority=false",
	}
}

func reviewerReservation(timeout time.Duration) agent.CallReservation {
	return agent.CallReservation{
		Timeout: timeout, RequestBytes: 48 * 1024, OutputBytes: agent.MaxReviewerResponseBytes, CostUnits: 1,
	}
}

func reviewerJSONResponse(content string) *http.Response {
	quoted, _ := json.Marshal(content)
	body := `{"id":"response-review","object":"chat.completion","created":1,"model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":` + string(quoted) + `},"finish_reason":"stop"}]}`
	return jsonHTTPResponse(body)
}

func jsonHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
