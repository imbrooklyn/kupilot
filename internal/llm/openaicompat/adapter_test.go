package openaicompat

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	io.Reader
	closed atomic.Bool
}

type countingBody struct {
	reader io.Reader
	read   atomic.Int64
	closed atomic.Bool
}

func (body *countingBody) Read(target []byte) (int, error) {
	count, err := body.reader.Read(target)
	body.read.Add(int64(count))
	return count, err
}

func (body *countingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func newFixtureAdapter(
	t *testing.T,
	configuration domain.ModelConfiguration,
	apiKey string,
	logger *slog.Logger,
) *Adapter {
	t.Helper()
	credential := newFixtureCredential(t, apiKey)
	adapter, modelError := New(configuration, credential, logger)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("New() error = %v", modelError)
	}
	t.Cleanup(adapter.Close)
	return adapter
}

func newFixtureCredential(t *testing.T, apiKey string) *config.SecretValue {
	t.Helper()
	source := &config.EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return apiKey, true },
		Unsetenv:  func(string) error { return nil },
	}
	credential, err := source.Read()
	if err != nil {
		t.Fatalf("read fixture credential: %v", err)
	}
	return &credential
}

func TestAdapterConstructionIsLocalAndCloseOwnsCredential(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("l", 41) + "-generated"
	credential := newFixtureCredential(t, apiCanary)
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	})
	configuration := fixtureConfiguration("https://10.0.0.8/v1", time.Second)
	adapter, modelError := newAdapter(configuration, credential, nil, transport)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newAdapter() error = %v", modelError)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("constructor HTTP calls = %d, want 0", got)
	}

	adapter.Close()
	adapter.Close()
	if credential.IsSet() {
		t.Fatal("credential remains set after Adapter.Close")
	}
	if modelError := adapter.Stream(context.Background(), fixtureModelRequest(), func(domain.ModelStreamEvent) {}); modelError == nil || modelError.Class() != domain.SafeErrorClassInternal {
		t.Fatalf("Stream() after Close error = %#v, want internal", modelError)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("HTTP calls after Close = %d, want 0", got)
	}
}

func TestAdapterDoesNotExposeModelDataToCallerCallbacks(t *testing.T) {
	t.Parallel()

	body := &trackingBody{Reader: bytes.NewReader(readFixtureFile(t, "normal.sse"))}
	credential := newFixtureCredential(t, strings.Repeat("b", 41)+"-generated")
	adapter, modelError := newAdapter(
		fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
		credential,
		fixtureLogger(&bytes.Buffer{}),
		roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
				Request:    request,
			}, nil
		}),
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newAdapter() error = %v", modelError)
	}
	defer adapter.Close()

	var callbackCalls atomic.Int32
	handler := einocallbacks.NewHandlerBuilder().OnStartFn(
		func(ctx context.Context, _ *einocallbacks.RunInfo, _ einocallbacks.CallbackInput) context.Context {
			callbackCalls.Add(1)
			return ctx
		},
	).Build()
	ctx := einocallbacks.InitCallbacks(context.Background(), nil, handler)
	modelError = adapter.Stream(ctx, fixtureModelRequest(), func(domain.ModelStreamEvent) {})
	if modelError != nil {
		t.Fatalf("Stream() error = %v", modelError)
	}
	if got := callbackCalls.Load(); got != 0 {
		t.Fatalf("caller callback calls = %d, want 0", got)
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestAdapterCloseWaitsForOwnedStream(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("w", 43) + "-generated"
	credential := newFixtureCredential(t, apiCanary)
	started := make(chan struct{})
	release := make(chan struct{})
	body := &trackingBody{Reader: bytes.NewReader(readFixtureFile(t, "normal.sse"))}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
				Request:    request,
			}, nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	adapter, modelError := newAdapter(
		fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
		credential,
		fixtureLogger(&bytes.Buffer{}),
		transport,
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newAdapter() error = %v", modelError)
	}

	streamResult := make(chan *domain.ModelError, 1)
	go func() {
		streamResult <- adapter.Stream(context.Background(), fixtureModelRequest(), func(domain.ModelStreamEvent) {})
	}()
	awaitSignal(t, started, "model request start")
	closeReturned := make(chan struct{})
	go func() {
		adapter.Close()
		close(closeReturned)
	}()
	returnedEarly := false
	select {
	case <-closeReturned:
		returnedEarly = true
	default:
	}

	close(release)
	if modelError := awaitModelError(t, streamResult); modelError != nil {
		t.Fatalf("Stream() error = %v", modelError)
	}
	awaitSignal(t, closeReturned, "adapter close")
	if returnedEarly {
		t.Fatal("Adapter.Close returned before the owned Stream completed")
	}
	if credential.IsSet() {
		t.Fatal("credential remains set after Adapter.Close")
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed before Adapter.Close returned")
	}
}

func TestAdapterRejectsInsecureTLSOverride(t *testing.T) {
	t.Parallel()

	credential := newFixtureCredential(t, strings.Repeat("i", 43)+"-generated")
	defer credential.Destroy()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Intentionally rejected by the constructor.
	}
	adapter, modelError := newAdapter(
		fixtureConfiguration("https://model.example.test/v1", time.Second),
		credential,
		nil,
		transport,
	)
	if adapter != nil || modelError == nil || modelError.Class() != domain.SafeErrorClassInvalidInput {
		t.Fatalf("newAdapter() = %#v, %#v; want nil invalid_input", adapter, modelError)
	}
	if !credential.IsSet() {
		t.Fatal("failed construction consumed the caller-owned credential")
	}
}

func TestAdapterHonorsCancellationBeforeTransportAndBetweenBufferedEvents(t *testing.T) {
	t.Parallel()

	t.Run("before transport", func(t *testing.T) {
		t.Parallel()

		credential := newFixtureCredential(t, strings.Repeat("k", 43)+"-generated")
		var calls atomic.Int32
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&bytes.Buffer{}),
			roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("unexpected request")
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		modelError = adapter.Stream(ctx, fixtureModelRequest(), func(domain.ModelStreamEvent) {})
		if modelError == nil || modelError.Class() != domain.SafeErrorClassCancelled {
			t.Fatalf("Stream() error = %#v, want cancelled", modelError)
		}
		if got := calls.Load(); got != 0 {
			t.Fatalf("transport calls = %d, want 0", got)
		}
	})

	t.Run("between buffered events", func(t *testing.T) {
		t.Parallel()

		body := &trackingBody{Reader: bytes.NewReader(readFixtureFile(t, "normal.sse"))}
		credential := newFixtureCredential(t, strings.Repeat("j", 43)+"-generated")
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&bytes.Buffer{}),
			roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       body,
					Request:    request,
				}, nil
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		ctx, cancel := context.WithCancel(context.Background())
		var events []domain.ModelStreamEvent
		modelError = adapter.Stream(ctx, fixtureModelRequest(), func(event domain.ModelStreamEvent) {
			events = append(events, event)
			cancel()
		})
		if modelError == nil || modelError.Class() != domain.SafeErrorClassCancelled {
			t.Fatalf("Stream() error = %#v, want cancelled", modelError)
		}
		if len(events) != 1 || events[0].Kind != domain.ModelStreamEventTextDelta {
			t.Fatalf("events = %#v, want one text delta before cancellation", events)
		}
		if !body.closed.Load() {
			t.Fatal("cancelled buffered response body was not closed")
		}
	})
}

func TestPrivateHTTPSUsesCertificateAndHostnameVerification(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("p", 47) + "-generated"
	normalStream := readFixtureFile(t, "normal.sse")
	var authorizationMu sync.Mutex
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorizationMu.Lock()
		authorization = request.Header.Get("Authorization")
		authorizationMu.Unlock()
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("X-Request-ID", "request-private-https")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(normalStream)
	}))
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	credential := newFixtureCredential(t, apiCanary)
	adapter, modelError := newAdapter(
		fixtureConfiguration(server.URL+"/v1", time.Second),
		credential,
		fixtureLogger(&bytes.Buffer{}),
		transport,
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newAdapter() error = %v", modelError)
	}
	defer adapter.Close()

	var events []domain.ModelStreamEvent
	modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
		events = append(events, event)
	})
	assertTerminalContract(t, events, modelError)
	authorizationMu.Lock()
	defer authorizationMu.Unlock()
	if authorization != "Bearer "+apiCanary {
		t.Fatal("verified private HTTPS request did not receive the configured Authorization header")
	}
}

func TestAdapterClosesResponseBodiesOnEveryTerminalPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantClass   domain.SafeErrorClass
	}{
		{
			name:        "success",
			status:      http.StatusOK,
			contentType: "text/event-stream",
			body:        string(readFixtureFile(t, "normal.sse")),
		},
		{
			name:        "HTTP error",
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"error":{"message":"body-canary"}}`,
			wantClass:   domain.SafeErrorClassUnavailable,
		},
		{
			name:        "unsupported media type",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"content":"body-canary"}`,
			wantClass:   domain.SafeErrorClassUnsupported,
		},
		{
			name:        "malformed stream",
			status:      http.StatusOK,
			contentType: "text/event-stream",
			body:        string(readFixtureFile(t, "malformed.sse")),
			wantClass:   domain.SafeErrorClassInvalidExternalResponse,
		},
	}

	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()

			body := &trackingBody{Reader: strings.NewReader(current.body)}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: current.status,
					Header:     http.Header{"Content-Type": []string{current.contentType}},
					Body:       body,
					Request:    request,
				}, nil
			})
			credential := newFixtureCredential(t, strings.Repeat("b", 43)+"-generated")
			adapter, modelError := newAdapter(
				fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
				credential,
				fixtureLogger(&bytes.Buffer{}),
				transport,
			)
			if modelError != nil {
				credential.Destroy()
				t.Fatalf("newAdapter() error = %v", modelError)
			}
			defer adapter.Close()

			var events []domain.ModelStreamEvent
			modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
				events = append(events, event)
			})
			assertTerminalContract(t, events, modelError)
			if current.wantClass != "" && modelError.Class() != current.wantClass {
				t.Fatalf("model error class = %q, want %q", modelError.Class(), current.wantClass)
			}
			if !body.closed.Load() {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestUnsupportedFirstUseFailsWithoutProbeOrFallback(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("f", 41) + "-generated"
	errorCanary := strings.Repeat("x", 45) + "-generated"
	credential := newFixtureCredential(t, apiCanary)
	var calls atomic.Int32
	var capturedAuthorization string
	body := &trackingBody{Reader: strings.NewReader(errorCanary)}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		capturedAuthorization = request.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    request,
		}, nil
	})
	var logBuffer bytes.Buffer
	adapter, modelError := newAdapter(
		fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
		credential,
		fixtureLogger(&logBuffer),
		transport,
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newAdapter() error = %v", modelError)
	}
	defer adapter.Close()
	if got := calls.Load(); got != 0 {
		t.Fatalf("preflight HTTP calls = %d, want 0", got)
	}

	var events []domain.ModelStreamEvent
	modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
		events = append(events, event)
	})
	assertTerminalContract(t, events, modelError)
	if modelError.Class() != domain.SafeErrorClassUnsupported {
		t.Fatalf("model error class = %q, want unsupported", modelError.Class())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("first-use HTTP calls = %d, want exactly 1", got)
	}
	if capturedAuthorization != "Bearer "+apiCanary {
		t.Fatal("first-use request did not receive the configured Authorization header")
	}
	for name, value := range map[string]string{
		"safe error": modelError.Error(),
		"logs":       logBuffer.String(),
		"events":     sprintEvents(events),
	} {
		if strings.Contains(value, apiCanary) || strings.Contains(value, errorCanary) {
			t.Fatalf("%s contains a credential or response-body canary", name)
		}
	}
	if !body.closed.Load() {
		t.Fatal("unsupported response body was not closed")
	}
}

func TestCredentialCanaryIsBlockedFromRequestAndResponseValues(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("v", 43) + "-generated"
	t.Run("request content", func(t *testing.T) {
		t.Parallel()

		credential := newFixtureCredential(t, apiCanary)
		var calls atomic.Int32
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&bytes.Buffer{}),
			roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("unexpected request")
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		request := fixtureModelRequest()
		request.Messages[1].Content = apiCanary
		modelError = adapter.Stream(context.Background(), request, func(domain.ModelStreamEvent) {})
		if modelError == nil || modelError.Class() != domain.SafeErrorClassInvalidInput {
			t.Fatalf("model error = %#v, want invalid_input", modelError)
		}
		if got := calls.Load(); got != 0 {
			t.Fatalf("HTTP calls = %d, want 0", got)
		}
	})

	t.Run("response metadata", func(t *testing.T) {
		t.Parallel()

		body := &trackingBody{Reader: bytes.NewReader(readFixtureFile(t, "normal.sse"))}
		credential := newFixtureCredential(t, apiCanary)
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&bytes.Buffer{}),
			roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type": []string{"text/event-stream"},
						"X-Request-Id": []string{apiCanary},
					},
					Body:    body,
					Request: request,
				}, nil
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		var events []domain.ModelStreamEvent
		modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
			events = append(events, event)
		})
		assertTerminalContract(t, events, modelError)
		if modelError.Class() != domain.SafeErrorClassInvalidExternalResponse {
			t.Fatalf("model error class = %q, want invalid_external_response", modelError.Class())
		}
		if strings.Contains(sprintEvents(events), apiCanary) {
			t.Fatal("neutral metadata contains the credential canary")
		}
		if !body.closed.Load() {
			t.Fatal("response body was not closed")
		}
	})

	t.Run("fragmented response text", func(t *testing.T) {
		t.Parallel()

		first := apiCanary[:len(apiCanary)/2]
		second := apiCanary[len(apiCanary)/2:]
		firstJSON, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		secondJSON, err := json.Marshal(second)
		if err != nil {
			t.Fatal(err)
		}
		stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(firstJSON) + "},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":" + string(secondJSON) + "},\"finish_reason\":null}]}\n\n"
		body := &trackingBody{Reader: strings.NewReader(stream)}
		credential := newFixtureCredential(t, apiCanary)
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&bytes.Buffer{}),
			roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       body,
					Request:    request,
				}, nil
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		var events []domain.ModelStreamEvent
		modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
			events = append(events, event)
		})
		assertTerminalContract(t, events, modelError)
		if modelError.Class() != domain.SafeErrorClassInvalidExternalResponse {
			t.Fatalf("model error class = %q, want invalid_external_response", modelError.Class())
		}
		if strings.Contains(sprintEvents(events), apiCanary) {
			t.Fatal("neutral text events assemble to the credential canary")
		}
		if !body.closed.Load() {
			t.Fatal("response body was not closed")
		}
	})
}

func TestHTTPAndTransportErrorCanariesAreBoundedAndDiscarded(t *testing.T) {
	t.Parallel()

	t.Run("HTTP body", func(t *testing.T) {
		t.Parallel()

		apiCanary := strings.Repeat("a", 47) + "-generated"
		errorCanary := strings.Repeat("e", 49) + "-generated"
		body := &countingBody{reader: strings.NewReader(errorCanary + strings.Repeat("z", domain.MaxModelErrorBodyBytes*2))}
		credential := newFixtureCredential(t, apiCanary)
		var logBuffer bytes.Buffer
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&logBuffer),
			roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       body,
					Request:    request,
				}, nil
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(domain.ModelStreamEvent) {})
		if modelError == nil || modelError.Class() != domain.SafeErrorClassUnavailable {
			t.Fatalf("model error = %#v, want unavailable", modelError)
		}
		if got := body.read.Load(); got > int64(domain.MaxModelErrorBodyBytes) {
			t.Fatalf("error body bytes read = %d, want at most %d", got, domain.MaxModelErrorBodyBytes)
		}
		if !body.closed.Load() {
			t.Fatal("HTTP error body was not closed")
		}
		for name, value := range map[string]string{"safe error": modelError.Error(), "logs": logBuffer.String()} {
			if strings.Contains(value, apiCanary) || strings.Contains(value, errorCanary) {
				t.Fatalf("%s contains an HTTP canary", name)
			}
		}
	})

	t.Run("transport cause", func(t *testing.T) {
		t.Parallel()

		apiCanary := strings.Repeat("t", 47) + "-generated"
		causeCanary := strings.Repeat("c", 49) + "-generated"
		credential := newFixtureCredential(t, apiCanary)
		var logBuffer bytes.Buffer
		adapter, modelError := newAdapter(
			fixtureConfiguration("http://127.0.0.1:8080/v1", time.Second),
			credential,
			fixtureLogger(&logBuffer),
			roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(causeCanary)
			}),
		)
		if modelError != nil {
			credential.Destroy()
			t.Fatalf("newAdapter() error = %v", modelError)
		}
		defer adapter.Close()

		modelError = adapter.Stream(context.Background(), fixtureModelRequest(), func(domain.ModelStreamEvent) {})
		if modelError == nil || modelError.Class() != domain.SafeErrorClassUnavailable {
			t.Fatalf("model error = %#v, want unavailable", modelError)
		}
		for name, value := range map[string]string{"safe error": modelError.Error(), "logs": logBuffer.String()} {
			if strings.Contains(value, apiCanary) || strings.Contains(value, causeCanary) {
				t.Fatalf("%s contains a transport canary", name)
			}
		}
	})
}

func TestSameOriginTemporaryRedirectPreservesBoundedPostAndAuthorization(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("s", 43) + "-generated"
	normalStream := readFixtureFile(t, "normal.sse")
	var mu sync.Mutex
	var methods []string
	var authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		methods = append(methods, request.Method)
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		mu.Unlock()
		switch request.URL.Path {
		case "/v1/redirect/chat/completions":
			http.Redirect(response, request, "/v1/final/chat/completions", http.StatusTemporaryRedirect)
		case "/v1/final/chat/completions":
			response.Header().Set("Content-Type", "text/event-stream")
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(normalStream)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := newFixtureAdapter(
		t,
		fixtureConfiguration(server.URL+"/v1/redirect", time.Second),
		apiCanary,
		fixtureLogger(&bytes.Buffer{}),
	)
	var events []domain.ModelStreamEvent
	modelError := adapter.Stream(context.Background(), fixtureModelRequest(), func(event domain.ModelStreamEvent) {
		events = append(events, event)
	})
	assertTerminalContract(t, events, modelError)

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodPost {
		t.Fatalf("redirect methods = %v, want two POST requests", methods)
	}
	for _, authorization := range authorizations {
		if authorization != "Bearer "+apiCanary {
			t.Fatal("same-origin redirect did not preserve the transport credential")
		}
	}
}

func sprintEvents(events []domain.ModelStreamEvent) string {
	var builder strings.Builder
	for _, event := range events {
		builder.WriteString(string(event.Kind))
		builder.WriteString(event.TextDelta)
		if event.Metadata != nil {
			builder.WriteString(event.Metadata.ProviderRequestID)
		}
		if event.ToolCallFragment != nil {
			builder.WriteString(event.ToolCallFragment.IDFragment)
			builder.WriteString(event.ToolCallFragment.NameFragment)
			builder.WriteString(event.ToolCallFragment.ArgumentsFragment)
		}
	}
	return builder.String()
}
