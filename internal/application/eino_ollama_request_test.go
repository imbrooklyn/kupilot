package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestNativeOllamaTemperatureFieldIsAlwaysExplicit(t *testing.T) {
	t.Parallel()
	for _, temperature := range []float64{0, 0.1, 0.2} {
		for _, mode := range []string{"stream", "summary", "review"} {
			t.Run(mode+"/"+jsonNumber(temperature), func(t *testing.T) {
				configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
				configuration.Temperature = testTemperature(temperature)
				if mode == "review" {
					configuration.Role = domain.ModelRoleApprovalReviewer
					configuration.StreamingRequired = false
					configuration.ToolCallingRequired = false
				}
				calls := 0
				client, model := newFixtureOllamaClientWithTransport(t, configuration, fixtureLogger(&bytes.Buffer{}), roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					assertNativeWireTemperature(t, request, temperature)
					media := "application/json"
					if mode == "stream" {
						media = "application/x-ndjson"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{media}}, Body: io.NopCloser(strings.NewReader(`{"model":"fixture-model","message":{"role":"assistant","content":"Available."},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}` + "\n"))}, nil
				}))
				if mode == "stream" {
					message, failure := streamFixture(client, model, context.Background())
					if failure != nil || message == nil || message.Content != "Available." {
						t.Fatalf("stream result = %v / %v", message, failure)
					}
				} else {
					invocation := domain.ModelInvocationAgentSummary
					if mode == "review" {
						invocation = domain.ModelInvocationReview
					}
					message, failure := client.generateNonStreaming(context.Background(), fixtureRequestID, fixtureMessages(), agent.CallReservation{RequestBytes: 4096, OutputBytes: 1024}, 1024, invocation)
					if failure != nil || message == nil || message.Content != "Available." {
						t.Fatalf("generation result = %v / %v", message, failure)
					}
				}
				if calls != 1 {
					t.Fatalf("transport calls = %d, want exactly one", calls)
				}
			})
		}
	}
}

func jsonNumber(value float64) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func assertNativeWireTemperature(t *testing.T, request *http.Request, want float64) {
	t.Helper()
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Options struct {
			Temperature *float64 `json:"temperature"`
			NumPredict  int      `json:"num_predict"`
		} `json:"options"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Options.Temperature == nil || *wire.Options.Temperature != want || wire.Options.NumPredict != 2048 ||
		request.ContentLength != int64(len(payload)) || request.Header.Get("Authorization") != "" {
		t.Fatal("native request lost its explicit setting, output ceiling, framing, or credential policy")
	}
}

func TestNativeOllamaRequestPreservesAllOtherBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
		temperature       float64
	}{
		{"empty", `{"options":{}}`, `{"options":{"temperature":0}}`, 0},
		{"whitespace", "{\"options\":{ \n }}", "{\"options\":{\"temperature\":0 \n }}", 0},
		{"nonempty", ` {"messages":[{"content":"Synthetic input","options":{"temperature":99}}],"options":{"num_predict":2048},"stream":true} `, ` {"messages":[{"content":"Synthetic input","options":{"temperature":99}}],"options":{"temperature":0,"num_predict":2048},"stream":true} `, 0},
		{"present_zero", `{"options":{"temperature":0}}`, `{"options":{"temperature":0}}`, 0},
		{"present_nonzero", `{"options":{"temperature":0.1}}`, `{"options":{"temperature":0.1}}`, 0.1},
		{"escaped", `{"opt\u0069ons":{"temperat\u0075re":0.2}}`, `{"opt\u0069ons":{"temperat\u0075re":0.2}}`, 0.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:11434/api/chat", strings.NewReader(tc.input))
			original := &trackingBody{Reader: request.Body}
			request.Body = original
			request.Header.Set("Content-Length", jsonNumber(float64(len(tc.input))))
			corrected, err := nativeOllamaRequest(request, tc.temperature, 4096)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(corrected.Body)
			_ = corrected.Body.Close()
			replay, err := corrected.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			replayed, _ := io.ReadAll(replay)
			_ = replay.Close()
			if string(body) != tc.want || !bytes.Equal(body, replayed) || corrected.ContentLength != int64(len(tc.want)) ||
				corrected.Header.Get("Content-Length") != "" || !original.closed.Load() || request.ContentLength != int64(len(tc.input)) {
				t.Fatal("request bytes, replay framing, ownership, or source request changed unexpectedly")
			}
		})
	}
}

func TestNativeOllamaRequestDenialsMakeZeroExternalCalls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body  string
		temperature float64
	}{
		{"empty", ``, 0},
		{"array", `[]`, 0},
		{"missing_options", `{}`, 0},
		{"null_options", `{"options":null}`, 0},
		{"array_options", `{"options":[]}`, 0},
		{"duplicate_options", `{"options":{},"options":{}}`, 0},
		{"missing_nonzero", `{"options":{}}`, 0.1},
		{"underflow_missing", `{"options":{}}`, 1e-50},
		{"underflow_zero", `{"options":{"temperature":0}}`, 1e-50},
		{"null_temperature", `{"options":{"temperature":null}}`, 0},
		{"string_temperature", `{"options":{"temperature":"0"}}`, 0},
		{"duplicate_temperature", `{"options":{"temperature":0,"temperature":0}}`, 0},
		{"mismatched_temperature", `{"options":{"temperature":0.1}}`, 0},
		{"malformed_key", `{!}`, 0},
		{"malformed_value", `{"options":!}`, 0},
		{"missing_close", `{"options":{}`, 0},
		{"trailing_value", `{"options":{}} {}`, 0},
		{"trailing_garbage", `{"options":{}} !`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
			configuration.Temperature = testTemperature(tc.temperature)
			calls := 0
			client, _ := newFixtureOllamaClientWithTransport(t, configuration, fixtureLogger(&bytes.Buffer{}), roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected external request")
			}))
			request := nativeGuardedRequest(t, context.Background(), tc.body, 4096)
			response, err := client.client.Transport.RoundTrip(request)
			if response != nil && response.Body != nil {
				defer response.Body.Close()
			}
			if response != nil || !errors.Is(err, errTransportRequestInvalid) || calls != 0 {
				t.Fatalf("response/error/calls = %v/%v/%d", response, err, calls)
			}
		})
	}
}

func nativeGuardedRequest(t *testing.T, ctx context.Context, body string, maximum int) *http.Request {
	t.Helper()
	ctx = context.WithValue(ctx, transportRequestStateKey{}, &transportRequestState{requestLimit: maximum, responseLimit: 4096, responseMode: transportResponseStream})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:11434/api/chat", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	return request
}

func TestNativeOllamaCorrectedRequestByteCeiling(t *testing.T) {
	t.Parallel()
	input := `{"options":{}}`
	finalSize := len(input) + len(`"temperature":0`)
	for _, maximum := range []int{finalSize, finalSize - 1} {
		t.Run(jsonNumber(float64(maximum)), func(t *testing.T) {
			configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
			configuration.Temperature = testTemperature(0)
			calls := 0
			client, _ := newFixtureOllamaClientWithTransport(t, configuration, fixtureLogger(&bytes.Buffer{}), roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(request.Body)
				if request.ContentLength != int64(finalSize) || len(body) != finalSize {
					t.Fatal("incorrect final request size")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}))
			response, err := client.client.Transport.RoundTrip(nativeGuardedRequest(t, context.Background(), input, maximum))
			if maximum == finalSize {
				if err != nil || response == nil || calls != 1 {
					t.Fatalf("exact limit response/error/calls = %v/%v/%d", response, err, calls)
				}
				_ = response.Body.Close()
			} else if response != nil || !errors.Is(err, errModelRequestLimitReached) || calls != 0 {
				t.Fatalf("one-over response/error/calls = %v/%v/%d", response, err, calls)
			}
		})
	}
}

type nativeReadFixture struct {
	reader     io.Reader
	beforeRead func()
	closed     bool
}

func (body *nativeReadFixture) Read(dst []byte) (int, error) {
	if body.beforeRead != nil {
		body.beforeRead()
	}
	return body.reader.Read(dst)
}

func (body *nativeReadFixture) Close() error { body.closed = true; return nil }

func TestNativeOllamaRequestReadAndLifecycleDenials(t *testing.T) {
	t.Parallel()
	readFailure := errors.New("synthetic read failure")
	for _, name := range []string{"read_failure", "cancel_before", "cancel_during", "timeout", "length_mismatch", "actual_one_over"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := `{"options":{}}`
			request := nativeGuardedRequest(t, ctx, input, 4096)
			body := &nativeReadFixture{reader: strings.NewReader(input)}
			request.Body = body
			want := errTransportRequestInvalid
			switch name {
			case "read_failure":
				body.reader = &disconnectingReader{err: readFailure}
			case "cancel_before":
				cancel()
				want = context.Canceled
			case "cancel_during":
				body.beforeRead = cancel
				want = context.Canceled
			case "timeout":
				deadline, stop := context.WithDeadline(request.Context(), time.Unix(1, 0))
				defer stop()
				request = request.WithContext(deadline)
				want = context.DeadlineExceeded
			case "length_mismatch":
				request.ContentLength++
			case "actual_one_over":
				body.reader = strings.NewReader(strings.Repeat(" ", 4097))
				want = errModelRequestLimitReached
			}
			configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
			configuration.Temperature = testTemperature(0)
			calls := 0
			client, _ := newFixtureOllamaClientWithTransport(t, configuration, fixtureLogger(&bytes.Buffer{}), roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected external request")
			}))
			response, err := client.client.Transport.RoundTrip(request)
			if response != nil && response.Body != nil {
				defer response.Body.Close()
			}
			if response != nil || !errors.Is(err, want) || calls != 0 || !body.closed || name == "read_failure" && !errors.Is(err, readFailure) {
				t.Fatalf("response/error/calls/closed = %v/%v/%d/%t", response, err, calls, body.closed)
			}
		})
	}
}
