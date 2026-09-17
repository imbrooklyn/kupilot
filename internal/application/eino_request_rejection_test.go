package application

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestModelClientRequestRejectionRemainsDistinctAndNeverRetries(t *testing.T) {
	for _, status := range []int{400, 422} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const canary = "synthetic-private-provider-error"
			body := &trackingBody{Reader: strings.NewReader(`{"error":{"message":"` + canary + `"}}`)}
			var logs bytes.Buffer
			calls := 0
			credential, err := config.NewSecretValue("synthetic-request-credential")
			if err != nil {
				t.Fatal(err)
			}
			client, modelError := newModelClientForTest(fixtureConfiguration("https://model.example.test/v1", time.Second), &credential, fixtureLogger(&logs), roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
			}))
			if modelError != nil {
				credential.Destroy()
				t.Fatal(modelError)
			}
			clock := newTestClock()
			tool := new(recordingTool)
			adapter, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
			if err != nil {
				client.close()
				t.Fatal(err)
			}
			defer adapter.Close()
			input := testInput(t, clock, agent.DefaultRunBudgetLimits())
			recorder := newEventRecorder()
			outcome := adapter.Run(context.Background(), input, recorder)
			want := domain.NewModelError(domain.ModelErrorCodeRequestRejected, domain.ModelOperationRequest, "test-request")
			if want.Validate() != nil || outcome.Validate(input) != nil || outcome.Status != domain.AgentRunStatusFailed || outcome.Diagnosis != nil ||
				outcome.Diagnostic != domain.FailureProviderRequest || outcome.SafeMessage != want.SafeMessage() ||
				outcome.ErrorClass == nil || *outcome.ErrorClass != domain.SafeErrorClassUnsupported || calls != 1 || len(tool.Calls()) != 0 || !body.closed.Load() {
				t.Fatal("HTTP request rejection lost its exact outcome or one-attempt boundary")
			}
			assertTerminalSequence(t, recorder.Events())
			if strings.Contains(logs.String()+outcome.SafeMessage, canary) ||
				!strings.Contains(logs.String(), `"error_code":"model_request_rejected"`) {
				t.Fatal("request rejection diagnostics lost their safe code")
			}
			for _, cause := range []error{errors.New("synthetic SDK cause"), want} {
				state := &transportRequestState{}
				state.setHTTPStatus(status)
				mapped := mapModelRequestError(context.Background(), cause, state)
				if mapped.code != want.Code() || mapped.cause != modelFailureHTTPStatus || mapped.httpStatus != status {
					t.Fatal("wrapped or untyped provider rejection lost its observed status")
				}
				for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
					ctx, cancel := context.WithCancelCause(context.Background())
					cancel(cancellation)
					mapped := mapModelRequestError(ctx, cause, state)
					code := domain.ModelErrorCodeCancelled
					if cancellation == context.DeadlineExceeded {
						code = domain.ModelErrorCodeTimeout
					}
					if mapped.code != code {
						t.Fatal("HTTP rejection overrode owning cancellation or deadline")
					}
				}
			}
		})
	}
}
