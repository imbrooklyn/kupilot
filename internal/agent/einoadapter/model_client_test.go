package einoadapter

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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const fixtureRequestID domain.ModelRequestID = "00000000-0000-7000-8000-000000001201"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)
	return nil
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

func fixtureConfiguration(endpoint string, timeout time.Duration) domain.ModelConfiguration {
	parsed, _ := url.Parse(endpoint)
	return domain.ModelConfiguration{
		ProviderKind:        domain.ModelProviderOpenAICompatible,
		Endpoint:            endpoint,
		Origin:              parsed.Scheme + "://" + parsed.Host,
		Model:               "fixture-model",
		APIKeySource:        domain.ModelAPIKeySourceRuntime,
		Temperature:         0.1,
		MaxOutputTokens:     2048,
		RequestTimeout:      timeout,
		StreamingRequired:   true,
		ToolCallingRequired: true,
		TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
}

func fixtureLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func fixtureMessages() []*schema.Message {
	return []*schema.Message{
		schema.SystemMessage("Use only the supplied structured Tools."),
		schema.UserMessage("Diagnose the synthetic fixture."),
	}
}

func fixtureToolInfos(t *testing.T) []*schema.ToolInfo {
	t.Helper()
	specifications := agent.ToolSpecifications()
	infos := make([]*schema.ToolInfo, len(specifications))
	for index, specification := range specifications {
		info, err := toolInfo(specification)
		if err != nil {
			t.Fatalf("toolInfo(%q) error = %v", specification.Name, err)
		}
		infos[index] = info
	}
	return infos
}

func newFixtureModelClient(
	t *testing.T,
	configuration domain.ModelConfiguration,
	apiKey string,
	logger *slog.Logger,
) (*modelClient, einomodel.ToolCallingChatModel) {
	t.Helper()
	credential, err := config.NewSecretValue(apiKey)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	client, modelError := newModelClient(configuration, &credential, logger, DiagnosticOptions{})
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newModelClient() error = %v", modelError)
	}
	t.Cleanup(client.close)
	bound, err := client.withTools(fixtureToolInfos(t))
	if err != nil {
		t.Fatalf("withTools() error = %v", err)
	}
	return client, bound
}

func newFixtureModelClientWithTransport(
	t *testing.T,
	configuration domain.ModelConfiguration,
	apiKey string,
	logger *slog.Logger,
	transport http.RoundTripper,
) (*modelClient, einomodel.ToolCallingChatModel) {
	t.Helper()
	credential, err := config.NewSecretValue(apiKey)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	client, modelError := newModelClientForTest(configuration, &credential, logger, transport)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newModelClientForTest() error = %v", modelError)
	}
	t.Cleanup(client.close)
	bound, err := client.withTools(fixtureToolInfos(t))
	if err != nil {
		t.Fatalf("withTools() error = %v", err)
	}
	return client, bound
}

func streamFixture(
	client *modelClient,
	model einomodel.ToolCallingChatModel,
	ctx context.Context,
) (*schema.Message, *domain.ModelError) {
	return client.stream(ctx, fixtureRequestID, model, fixtureMessages(), nil)
}

func TestModelClientObservesEachValidatedSSEContentFragmentBeforeAssembly(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.endpoint("normal"), time.Second),
		strings.Repeat("o", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	var observed []string
	message, modelError := client.stream(
		context.Background(),
		fixtureRequestID,
		model,
		fixtureMessages(),
		func(fragment string) error {
			observed = append(observed, fragment)
			return nil
		},
	)
	if modelError != nil || message == nil || message.Content != "Pod is healthy." {
		t.Fatalf("streamed message/error = %#v / %#v", message, modelError)
	}
	if len(observed) != 2 || observed[0] != "Pod " || observed[1] != "is healthy." {
		t.Fatalf("observed SSE content fragments = %#v", observed)
	}
}

func TestModelClientDeliversSSEFragmentBeforeProviderCompletes(t *testing.T) {
	firstObserved := make(chan struct{})
	releaseProvider := make(chan struct{})
	var observedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: {\"id\":\"response-live\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Pod \"},\"finish_reason\":null}]}\n\n")
		flusher, ok := response.(http.Flusher)
		if !ok {
			return
		}
		flusher.Flush()
		select {
		case <-releaseProvider:
		case <-request.Context().Done():
			return
		}
		_, _ = io.WriteString(response, "data: {\"id\":\"response-live\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"is healthy.\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(response, "data: {\"id\":\"response-live\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(response, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.URL+"/v1", time.Second),
		strings.Repeat("l", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	type result struct {
		message *schema.Message
		failure *domain.ModelError
	}
	resultReady := make(chan result, 1)
	go func() {
		message, failure := client.stream(
			context.Background(), fixtureRequestID, model, fixtureMessages(),
			func(fragment string) error {
				if fragment == "Pod " {
					observedOnce.Do(func() { close(firstObserved) })
				}
				return nil
			},
		)
		resultReady <- result{message: message, failure: failure}
	}()

	select {
	case <-firstObserved:
	case <-time.After(time.Second):
		close(releaseProvider)
		t.Fatal("the first flushed SSE fragment was not delivered while the provider remained open")
	}
	select {
	case early := <-resultReady:
		close(releaseProvider)
		t.Fatalf("model stream completed before the provider released its terminal chunks: %#v", early)
	default:
	}
	close(releaseProvider)
	select {
	case completed := <-resultReady:
		if completed.failure != nil || completed.message == nil || completed.message.Content != "Pod is healthy." {
			t.Fatalf("completed live stream = %#v / %#v", completed.message, completed.failure)
		}
	case <-time.After(time.Second):
		t.Fatal("the model stream did not complete after the provider was released")
	}
}

func TestModelClientUsesEinoRequestAndStreamOnceForGPT5CompatibleIdentifier(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	configuration := fixtureConfiguration(server.endpoint("reasoning-none"), time.Second)
	configuration.Model = "gpt-5.6-luna"
	configuration.ReasoningEffort = domain.ModelReasoningEffortNone
	apiCanary := strings.Repeat("k", 43) + "-generated"
	var logBuffer bytes.Buffer
	client, model := newFixtureModelClient(t, configuration, apiCanary, fixtureLogger(&logBuffer))

	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil {
		t.Fatalf("stream error = %v", modelError)
	}
	if message.Content != "Pod is healthy." || message.ResponseMeta == nil ||
		message.ResponseMeta.FinishReason != "stop" || message.ResponseMeta.Usage == nil ||
		message.ResponseMeta.Usage.TotalTokens != 28 {
		t.Fatalf("assembled Eino message = %#v", message)
	}
	requests := server.capturedRequests()
	if len(requests) != 1 {
		t.Fatalf("HTTP requests = %d, want 1", len(requests))
	}
	if requests[0].Method != http.MethodPost || requests[0].Path != "/v1/reasoning-none/chat/completions" ||
		requests[0].ContentType != "application/json" {
		t.Fatalf("HTTP request target = %s %s (%q)", requests[0].Method, requests[0].Path, requests[0].ContentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatalf("decode Eino request: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	streamOptions, _ := payload["stream_options"].(map[string]any)
	if payload["model"] != configuration.Model || payload["reasoning_effort"] != "none" ||
		payload["temperature"] != configuration.Temperature ||
		payload["max_tokens"] != float64(configuration.MaxOutputTokens) || payload["stream"] != true ||
		streamOptions["include_usage"] != true || len(tools) != len(agent.ToolSpecifications()) {
		t.Fatalf("Eino request contract = %#v", payload)
	}
	for index, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if function["name"] != string(agent.ToolSpecifications()[index].Name) ||
			function["parameters"] == nil || function["strict"] != nil {
			t.Fatalf("Eino Tool %d = %#v", index, tool)
		}
	}
	if requests[0].Authorization != "Bearer "+apiCanary ||
		bytes.Contains(requests[0].Body, []byte(apiCanary)) ||
		strings.Contains(logBuffer.String(), apiCanary) ||
		strings.Contains(logBuffer.String(), einoCredentialPlaceholder) {
		t.Fatal("credential or placeholder crossed an unauthorized sink")
	}
}

func TestModelClientAcceptsOptionalUsageAndTerminalEOF(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.endpoint("no-usage-eof"), time.Second),
		strings.Repeat("u", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil || message == nil || message.Content != "Usage is optional." ||
		message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "stop" ||
		message.ResponseMeta.Usage != nil {
		t.Fatalf("optional-usage message/error = %#v / %#v", message, modelError)
	}
}

func TestModelClientAcceptsOutputCeilingSizedFragmentedStream(t *testing.T) {
	t.Parallel()

	const (
		formerWireLimit  = 256 * 1024
		formerEventLimit = 1024
	)
	firstChunk := `data: {"id":"response-long","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","content":"x"},"finish_reason":null}]}` + "\n\n"
	contentChunk := `data: {"id":"response-long","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"
	finishChunk := `data: {"id":"response-long","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	streamBody := firstChunk + strings.Repeat(contentChunk, domain.MaxModelOutputTokens-1) + finishChunk + "data: [DONE]\n\n"
	if len(streamBody) <= formerWireLimit || domain.MaxModelOutputTokens <= formerEventLimit {
		t.Fatal("long-stream fixture does not cross both former transport ceilings")
	}
	if len(streamBody) > domain.MaxModelStreamBytes || domain.MaxModelOutputTokens+2 > domain.MaxModelStreamChunks {
		t.Fatal("long-stream fixture exceeds the current bounded transport contract")
	}

	body := &trackingBody{Reader: strings.NewReader(streamBody)}
	configuration := fixtureConfiguration("https://model.example.test/v1", time.Second)
	configuration.MaxOutputTokens = domain.MaxModelOutputTokens
	client, model := newFixtureModelClientWithTransport(
		t,
		configuration,
		strings.Repeat("l", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body,
			}, nil
		}),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil || message == nil || message.Content != strings.Repeat("x", domain.MaxModelOutputTokens) ||
		message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "stop" {
		t.Fatalf("long fragmented message/error = %#v / %#v", message, modelError)
	}
	if !body.closed.Load() {
		t.Fatal("long fragmented response body was not closed")
	}
}

func TestModelClientDiscardsBoundedReasoningContent(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	var logBuffer bytes.Buffer
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.endpoint("reasoning-content"), time.Second),
		strings.Repeat("r", 43)+"-generated",
		fixtureLogger(&logBuffer),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil {
		t.Fatalf("stream error = %v", modelError)
	}
	if message == nil || message.Content != "Pod is healthy." || len(message.ToolCalls) != 0 || message.ReasoningContent != "" ||
		message.Extra != nil || message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "stop" ||
		message.ResponseMeta.Usage == nil || message.ResponseMeta.Usage.TotalTokens != 32 {
		t.Fatalf("reasoning-content message = %#v", message)
	}
	if strings.Contains(message.Content, "reasoning-output-canary") ||
		strings.Contains(logBuffer.String(), "reasoning-output-canary") {
		t.Fatal("discarded reasoning reached an answer, Tool, or log sink")
	}
}

func TestModelClientConstructionIsLocalAndCancelledContextMakesZeroHTTPCalls(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("l", 43) + "-generated"
	credential, err := config.NewSecretValue(apiCanary)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	var calls atomic.Int32
	client, modelError := newModelClientForTest(
		fixtureConfiguration("https://model.example.test/v1", time.Second),
		&credential,
		fixtureLogger(&bytes.Buffer{}),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected model request")
		}),
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newModelClientForTest() error = %v", modelError)
	}
	if calls.Load() != 0 {
		t.Fatalf("constructor HTTP calls = %d, want 0", calls.Load())
	}
	model, err := client.withTools(fixtureToolInfos(t))
	if err != nil {
		client.close()
		t.Fatalf("withTools() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	message, modelError := streamFixture(client, model, ctx)
	if message != nil || modelError == nil || modelError.Class() != domain.SafeErrorClassCancelled {
		client.close()
		t.Fatalf("cancelled message/error = %#v / %#v", message, modelError)
	}
	if calls.Load() != 0 {
		client.close()
		t.Fatalf("cancelled HTTP calls = %d, want 0", calls.Load())
	}
	client.close()
	client.close()
	if credential.IsSet() {
		t.Fatal("closed model client retained its credential")
	}
}

func TestModelClientRejectsOversizedEinoPayloadBeforeHTTP(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client, model := newFixtureModelClientWithTransport(
		t,
		fixtureConfiguration("https://model.example.test/v1", time.Second),
		strings.Repeat("o", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected model request")
		}),
	)
	message, modelError := client.stream(
		context.Background(),
		fixtureRequestID,
		model,
		[]*schema.Message{schema.UserMessage(strings.Repeat("x", domain.MaxModelRequestBytes))},
		nil,
	)
	if message != nil || modelError == nil || modelError.Code() != domain.ModelErrorCodeRequestTooLarge {
		t.Fatalf("oversized message/error = %#v / %#v", message, modelError)
	}
	if calls.Load() != 0 {
		t.Fatalf("oversized request HTTP calls = %d, want 0", calls.Load())
	}
}

func TestModelClientAllowsVerifiedPrivateHTTPS(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("p", 47) + "-generated"
	normalStream := readFixtureFile(t, "normal.sse")
	var authorization atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorization.Store(request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(normalStream)
	}))
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client, model := newFixtureModelClientWithTransport(
		t,
		fixtureConfiguration(server.URL+"/v1", time.Second),
		apiCanary,
		fixtureLogger(&bytes.Buffer{}),
		transport,
	)
	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil || message == nil || message.Content != "Pod is healthy." {
		t.Fatalf("private HTTPS message/error = %#v / %#v", message, modelError)
	}
	if value, _ := authorization.Load().(string); value != "Bearer "+apiCanary {
		t.Fatal("verified private HTTPS request did not receive the transport credential")
	}
}

func TestModelClientPreservesPostAndCredentialAcrossSameOriginRedirect(t *testing.T) {
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

	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.URL+"/v1/redirect", time.Second),
		apiCanary,
		fixtureLogger(&bytes.Buffer{}),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if modelError != nil || message == nil || message.Content != "Pod is healthy." {
		t.Fatalf("redirect message/error = %#v / %#v", message, modelError)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodPost {
		t.Fatalf("redirect methods = %v, want two POST requests", methods)
	}
	for _, value := range authorizations {
		if value != "Bearer "+apiCanary {
			t.Fatal("same-origin redirect did not preserve the transport credential")
		}
	}
}

func TestModelClientAssemblesCompatibleToolStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scenario string
		count    int
		args     []string
	}{
		{scenario: "tool-call-fragments", count: 1, args: []string{`{"kind":"Pod","name":"sample-pod"}`}},
		{scenario: "interleaved-tool-calls", count: 2, args: []string{`{"kind":"Pod","name":"sample-pod"}`, `{"kind":"Pod","name":"sample-pod"}`}},
		{scenario: "noncanonical-tool-arguments", count: 1, args: []string{` { "purpose": "Inspect cluster counts.", "limit": 20 } `}},
		{scenario: "commentary-tool-call", count: 1, args: []string{`{"kind":"Pod","name":"sample-pod"}`}},
	}
	for _, current := range tests {
		current := current
		t.Run(current.scenario, func(t *testing.T) {
			t.Parallel()
			server := newFixtureServer(t, "")
			client, model := newFixtureModelClient(
				t,
				fixtureConfiguration(server.endpoint(current.scenario), time.Second),
				strings.Repeat("t", 43)+"-generated",
				fixtureLogger(&bytes.Buffer{}),
			)
			message, modelError := streamFixture(client, model, context.Background())
			if modelError != nil {
				t.Fatalf("stream error = %v", modelError)
			}
			if message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "tool_calls" ||
				len(message.ToolCalls) != current.count {
				t.Fatalf("assembled Tool message = %#v", message)
			}
			for index, call := range message.ToolCalls {
				if call.Index == nil || *call.Index != index || call.Function.Arguments != current.args[index] {
					t.Fatalf("Tool call %d = %#v, want arguments %q", index, call, current.args[index])
				}
			}
		})
	}
}

func TestModelClientEnforcesAssembledResponseBounds(t *testing.T) {
	t.Parallel()

	textFragment, err := json.Marshal(strings.Repeat("x", domain.MaxModelStreamChunkBytes/3))
	if err != nil {
		t.Fatal(err)
	}
	firstTextChunk := `data: {"id":"response-text-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(textFragment) + `},"finish_reason":null}]}` + "\n\n"
	textChunk := `data: {"id":"response-text-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"content":` + string(textFragment) + `},"finish_reason":null}]}` + "\n\n"
	textStream := firstTextChunk + strings.Repeat(textChunk, 6) + `data: {"id":"response-text-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

	argumentFragment, err := json.Marshal(strings.Repeat("a", domain.MaxModelToolArgumentsBytes/2+1))
	if err != nil {
		t.Fatal(err)
	}
	toolStream := `data: {"id":"response-tool-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-limit","type":"function","function":{"name":"get_resource","arguments":` + string(argumentFragment) + `}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"response-tool-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(argumentFragment) + `}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"response-tool-limit","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"

	for _, current := range []struct {
		name string
		body string
	}{
		{name: "assistant content", body: textStream},
		{name: "Tool arguments", body: toolStream},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			body := &trackingBody{Reader: strings.NewReader(current.body)}
			client, model := newFixtureModelClientWithTransport(
				t,
				fixtureConfiguration("https://model.example.test/v1", time.Second),
				strings.Repeat("z", 43)+"-generated",
				fixtureLogger(&bytes.Buffer{}),
				roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       body,
					}, nil
				}),
			)
			message, modelError := streamFixture(client, model, context.Background())
			if message != nil || modelError == nil || modelError.Code() != domain.ModelErrorCodeStreamLimitExceeded {
				t.Fatalf("bounded response message/error = %#v / %#v", message, modelError)
			}
			if !body.closed.Load() {
				t.Fatal("bounded response body was not closed")
			}
		})
	}
}

func TestModelClientMapsHTTPAndStreamFailuresSafely(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scenario string
		class    domain.SafeErrorClass
		code     domain.ModelErrorCode
	}{
		{scenario: "error-400", class: domain.SafeErrorClassUnsupported, code: domain.ModelErrorCodeUnsupportedResponse},
		{scenario: "error-401", class: domain.SafeErrorClassAuthenticationFailed, code: domain.ModelErrorCodeAuthenticationFailed},
		{scenario: "error-429", class: domain.SafeErrorClassRateLimited, code: domain.ModelErrorCodeRateLimited},
		{scenario: "error-500", class: domain.SafeErrorClassUnavailable, code: domain.ModelErrorCodeServiceUnavailable},
		{scenario: "malformed", class: domain.SafeErrorClassInvalidExternalResponse, code: domain.ModelErrorCodeMalformedStream},
		{scenario: "oversize-event", class: domain.SafeErrorClassBudgetExhausted, code: domain.ModelErrorCodeStreamLimitExceeded},
		{scenario: "oversize-stream", class: domain.SafeErrorClassBudgetExhausted, code: domain.ModelErrorCodeStreamLimitExceeded},
		{scenario: "too-many-events", class: domain.SafeErrorClassBudgetExhausted, code: domain.ModelErrorCodeStreamLimitExceeded},
	}
	for _, current := range tests {
		current := current
		t.Run(current.scenario, func(t *testing.T) {
			t.Parallel()
			errorCanary := strings.Repeat("e", 43) + "-generated"
			apiCanary := strings.Repeat("a", 43) + "-generated"
			server := newFixtureServer(t, errorCanary)
			var logBuffer bytes.Buffer
			client, model := newFixtureModelClient(
				t,
				fixtureConfiguration(server.endpoint(current.scenario), time.Second),
				apiCanary,
				fixtureLogger(&logBuffer),
			)
			message, modelError := streamFixture(client, model, context.Background())
			if message != nil || modelError == nil || modelError.Validate() != nil ||
				modelError.Class() != current.class || modelError.Code() != current.code {
				t.Fatalf("message/error = %#v / %#v", message, modelError)
			}
			for _, sink := range []string{modelError.Error(), modelError.SafeMessage(), logBuffer.String()} {
				if strings.Contains(sink, errorCanary) || strings.Contains(sink, apiCanary) {
					t.Fatal("raw provider or credential canary reached a safe sink")
				}
			}
		})
	}
}

func TestModelClientCancellationStopsTheOwnedRequest(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.endpoint("cancel"), time.Second),
		strings.Repeat("c", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *domain.ModelError, 1)
	go func() {
		_, modelError := streamFixture(client, model, ctx)
		result <- modelError
	}()
	select {
	case <-server.requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	cancel()
	select {
	case modelError := <-result:
		if modelError == nil || modelError.Class() != domain.SafeErrorClassCancelled {
			t.Fatalf("cancel error = %#v", modelError)
		}
	case <-time.After(time.Second):
		t.Fatal("model request did not stop")
	}
	select {
	case <-server.requestCancelled:
	case <-time.After(time.Second):
		t.Fatal("fixture request did not observe cancellation")
	}
}

func TestModelClientDeadlineCauseStopsTheOwnedRequest(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "")
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(server.endpoint("timeout"), time.Second),
		strings.Repeat("d", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan *domain.ModelError, 1)
	go func() {
		_, modelError := streamFixture(client, model, ctx)
		result <- modelError
	}()
	select {
	case <-server.requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	cancel(context.DeadlineExceeded)
	select {
	case modelError := <-result:
		if modelError == nil || modelError.Class() != domain.SafeErrorClassTimeout {
			t.Fatalf("deadline error = %#v", modelError)
		}
	case <-time.After(time.Second):
		t.Fatal("model request did not stop")
	}
	select {
	case <-server.requestCancelled:
	case <-time.After(time.Second):
		t.Fatal("fixture request did not observe deadline cancellation")
	}
}

func TestModelClientDeniesCrossOriginRedirect(t *testing.T) {
	t.Parallel()

	target := newFixtureServer(t, "")
	source := newFixtureServer(t, "")
	source.setRedirectTarget(target.endpoint("normal") + "/chat/completions")
	client, model := newFixtureModelClient(
		t,
		fixtureConfiguration(source.endpoint("redirect"), time.Second),
		strings.Repeat("r", 43)+"-generated",
		fixtureLogger(&bytes.Buffer{}),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if message != nil || modelError == nil || modelError.Code() != domain.ModelErrorCodeRedirectDenied {
		t.Fatalf("redirect message/error = %#v / %#v", message, modelError)
	}
	if len(target.capturedRequests()) != 0 {
		t.Fatalf("redirect target requests = %d, want 0", len(target.capturedRequests()))
	}
}

func TestModelClientRejectsInsecureTLSAndOwnsCredential(t *testing.T) {
	t.Parallel()

	configuration := fixtureConfiguration("https://model.example.test/v1", time.Second)
	credential, err := config.NewSecretValue(strings.Repeat("s", 43) + "-generated")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	insecure := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // Deliberate denial fixture.
	client, modelError := newModelClientForTest(configuration, &credential, nil, insecure)
	if client != nil || modelError == nil || modelError.Code() != domain.ModelErrorCodeInvalidRequest {
		t.Fatalf("insecure TLS client/error = %#v / %#v", client, modelError)
	}
	if !credential.IsSet() {
		t.Fatal("failed construction destroyed caller-owned credential")
	}

	safeCredential, err := config.NewSecretValue(strings.Repeat("v", 43) + "-generated")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	client, modelError = newModelClientForTest(configuration, &safeCredential, nil, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic transport failure")
	}))
	if modelError != nil {
		t.Fatalf("safe construction error = %v", modelError)
	}
	client.close()
	if safeCredential.IsSet() {
		t.Fatal("closed model client retained its credential")
	}
	credential.Destroy()
}

func TestModelClientClosesResponseBodyOnEveryTerminalPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantCode    domain.ModelErrorCode
	}{
		{name: "success", status: http.StatusOK, contentType: "text/event-stream", body: string(readFixtureFile(t, "normal.sse"))},
		{name: "HTTP error", status: http.StatusInternalServerError, contentType: "application/json", body: `{"error":{"message":"rejected"}}`, wantCode: domain.ModelErrorCodeServiceUnavailable},
		{name: "unsupported media", status: http.StatusOK, contentType: "application/json", body: `{}`, wantCode: domain.ModelErrorCodeUnsupportedResponse},
		{name: "malformed stream", status: http.StatusOK, contentType: "text/event-stream", body: "data: malformed\\n\\n", wantCode: domain.ModelErrorCodeMalformedStream},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			body := &trackingBody{Reader: strings.NewReader(current.body)}
			client, model := newFixtureModelClientWithTransport(
				t,
				fixtureConfiguration("https://model.example.test/v1", time.Second),
				strings.Repeat("b", 43)+"-generated",
				fixtureLogger(&bytes.Buffer{}),
				roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: current.status,
						Header:     http.Header{"Content-Type": []string{current.contentType}},
						Body:       body,
					}, nil
				}),
			)
			message, modelError := streamFixture(client, model, context.Background())
			if current.wantCode == "" {
				if modelError != nil || message == nil {
					t.Fatalf("success message/error = %#v / %#v", message, modelError)
				}
			} else if message != nil || modelError == nil || modelError.Code() != current.wantCode {
				t.Fatalf("failure message/error = %#v / %#v, want %q", message, modelError, current.wantCode)
			}
			if !body.closed.Load() {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestModelClientBoundsAndDiscardsHTTPErrorBody(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("a", 47) + "-generated"
	errorCanary := strings.Repeat("e", 49) + "-generated"
	body := &countingBody{reader: strings.NewReader(errorCanary + strings.Repeat("z", domain.MaxModelErrorBodyBytes*2))}
	var logBuffer bytes.Buffer
	client, model := newFixtureModelClientWithTransport(
		t,
		fixtureConfiguration("https://model.example.test/v1", time.Second),
		apiCanary,
		fixtureLogger(&logBuffer),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       body,
			}, nil
		}),
	)
	message, modelError := streamFixture(client, model, context.Background())
	if message != nil || modelError == nil || modelError.Class() != domain.SafeErrorClassUnavailable {
		t.Fatalf("HTTP error message/error = %#v / %#v", message, modelError)
	}
	if body.read.Load() > int64(domain.MaxModelErrorBodyBytes) {
		t.Fatalf("error body bytes read = %d, want at most %d", body.read.Load(), domain.MaxModelErrorBodyBytes)
	}
	if !body.closed.Load() {
		t.Fatal("HTTP error body was not closed")
	}
	for _, value := range []string{modelError.Error(), modelError.SafeMessage(), logBuffer.String()} {
		if strings.Contains(value, apiCanary) || strings.Contains(value, errorCanary) {
			t.Fatal("HTTP error or credential canary reached a safe sink")
		}
	}
}

func TestModelClientBoundsAndRedactsOptInSensitiveDiagnostics(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("q", 47) + "-generated"
	providerCanary := strings.Repeat("v", 49) + "-generated"
	otherCredentialCanary := "Bearer " + strings.Repeat("w", 49) + "-generated"
	unsafeControl := "\x1b[31m\u202e"
	providerBody := `{"error":{"message":"` + providerCanary + " " + apiCanary + " " + otherCredentialCanary + unsafeControl + strings.Repeat("z", domain.MaxModelErrorBodyBytes*2) + `"}}`
	credential, err := config.NewSecretValue(apiCanary)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	var logBuffer bytes.Buffer
	client, modelError := newModelClientWithTransport(
		fixtureConfiguration("https://model.example.test/v1", time.Second),
		&credential,
		fixtureLogger(&logBuffer),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(providerBody)),
			}, nil
		}),
		DiagnosticOptions{Sensitive: true},
	)
	if modelError != nil {
		credential.Destroy()
		t.Fatalf("newModelClientWithTransport() error = %v", modelError)
	}
	t.Cleanup(client.close)
	model, err := client.withTools(fixtureToolInfos(t))
	if err != nil {
		t.Fatalf("withTools() error = %v", err)
	}
	message, modelError := streamFixture(client, model, context.Background())
	if message != nil || modelError == nil || modelError.Class() != domain.SafeErrorClassUnsupported {
		t.Fatalf("sensitive diagnostic message/error = %#v / %#v", message, modelError)
	}
	content := logBuffer.String()
	for _, expected := range []string{
		`"sensitive_endpoint"`, `"sensitive_model"`, `"sensitive_error_chain"`,
		`"sensitive_provider_error_body"`, `"sensitive_provider_body_truncated":true`,
		providerCanary, sensitiveRedactionMarker,
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("sensitive model diagnostic does not contain %q: %s", expected, content)
		}
	}
	for _, prohibited := range []string{apiCanary, otherCredentialCanary, `\u001b`, `\u202e`, "\u202e"} {
		if strings.Contains(content, prohibited) {
			t.Fatalf("sensitive model diagnostic contains prohibited text %q", prohibited)
		}
	}
}

func TestModelClientBlocksCredentialInProviderMetadata(t *testing.T) {
	t.Parallel()

	apiCanary := strings.Repeat("m", 47) + "-generated"
	normalStream := string(readFixtureFile(t, "normal.sse"))
	tests := []struct {
		name      string
		requestID string
		body      string
	}{
		{name: "response header", requestID: apiCanary, body: normalStream},
		{name: "decoded response identifier", body: strings.ReplaceAll(normalStream, "response-fixture-normal", apiCanary)},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			body := &trackingBody{Reader: strings.NewReader(current.body)}
			var logBuffer bytes.Buffer
			client, model := newFixtureModelClientWithTransport(
				t,
				fixtureConfiguration("https://model.example.test/v1", time.Second),
				apiCanary,
				fixtureLogger(&logBuffer),
				roundTripFunc(func(*http.Request) (*http.Response, error) {
					header := http.Header{"Content-Type": []string{"text/event-stream"}}
					if current.requestID != "" {
						header.Set("X-Request-ID", current.requestID)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body}, nil
				}),
			)
			message, modelError := streamFixture(client, model, context.Background())
			if message != nil || modelError == nil {
				t.Fatalf("metadata message/error = %#v / %#v", message, modelError)
			}
			if !body.closed.Load() {
				t.Fatal("metadata failure did not close the response body")
			}
			for _, value := range []string{modelError.Error(), modelError.SafeMessage(), logBuffer.String()} {
				if strings.Contains(value, apiCanary) {
					t.Fatal("provider metadata reflected the credential into a safe sink")
				}
			}
		})
	}
}

func TestCollectModelMessageBlocksSplitCredential(t *testing.T) {
	t.Parallel()

	credential, err := config.NewSecretValue("split-credential-canary")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	defer credential.Destroy()
	stream := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "split-credential-"},
		{Role: schema.Assistant, Content: "canary", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	})
	message, err := collectModelMessage(context.Background(), stream, &credential, nil)
	if message != nil || !errors.Is(err, errMalformedProviderChunk) {
		t.Fatalf("credential stream message/error = %#v / %v", message, err)
	}
}

func TestModelClientRejectsUnsafeReasoningContent(t *testing.T) {
	t.Parallel()

	newValidator := func(t *testing.T, secret string) *modelStreamValidator {
		t.Helper()
		credential, err := config.NewSecretValue(secret)
		if err != nil {
			t.Fatalf("NewSecretValue() error = %v", err)
		}
		t.Cleanup(credential.Destroy)
		return &modelStreamValidator{scanner: credentialScanner{credential: &credential}}
	}
	reasoningChunk := func(value string) *schema.Message {
		return &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: value,
			Extra:            map[string]any{einoReasoningContentKey: value},
		}
	}

	t.Run("metadata missing", func(t *testing.T) {
		validator := newValidator(t, "test-model-credential")
		chunk := reasoningChunk("bounded reasoning")
		chunk.Extra = nil
		if err := validator.validateChunk(chunk, false); !errors.Is(err, errUnsupportedProviderChunk) {
			t.Fatalf("missing metadata error = %v", err)
		}
	})
	t.Run("metadata mismatch", func(t *testing.T) {
		validator := newValidator(t, "test-model-credential")
		chunk := reasoningChunk("bounded reasoning")
		chunk.Extra[einoReasoningContentKey] = "different reasoning"
		if err := validator.validateChunk(chunk, false); !errors.Is(err, errUnsupportedProviderChunk) {
			t.Fatalf("mismatched metadata error = %v", err)
		}
	})
	t.Run("unknown metadata", func(t *testing.T) {
		validator := newValidator(t, "test-model-credential")
		chunk := reasoningChunk("bounded reasoning")
		chunk.Extra["unknown-provider-field"] = "untrusted"
		if err := validator.validateChunk(chunk, false); !errors.Is(err, errUnsupportedProviderChunk) {
			t.Fatalf("unknown metadata error = %v", err)
		}
	})
	t.Run("after finish", func(t *testing.T) {
		validator := newValidator(t, "test-model-credential")
		if err := validator.validateChunk(reasoningChunk("late reasoning"), true); !errors.Is(err, errMalformedProviderChunk) {
			t.Fatalf("post-finish reasoning error = %v", err)
		}
	})
	t.Run("split credential", func(t *testing.T) {
		validator := newValidator(t, "reasoning-secret")
		if err := validator.validateChunk(reasoningChunk("reasoning-"), false); err != nil {
			t.Fatalf("first reasoning fragment error = %v", err)
		}
		if err := validator.validateChunk(reasoningChunk("secret"), false); !errors.Is(err, errMalformedProviderChunk) {
			t.Fatalf("credential reasoning error = %v", err)
		}
	})
	t.Run("cumulative limit", func(t *testing.T) {
		validator := newValidator(t, "test-model-credential")
		fragment := strings.Repeat("r", domain.MaxModelStreamChunkBytes/2)
		for index := 0; index < domain.MaxModelMessageBytes/len(fragment); index++ {
			if err := validator.validateChunk(reasoningChunk(fragment), false); err != nil {
				t.Fatalf("bounded reasoning fragment %d error = %v", index, err)
			}
		}
		if err := validator.validateChunk(reasoningChunk("x"), false); !errors.Is(err, errModelResponseLimitReached) {
			t.Fatalf("reasoning limit error = %v", err)
		}
	})
}

func TestCollectModelMessageRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	credential, err := config.NewSecretValue("test-model-credential")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	defer credential.Destroy()
	stream := schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		Content:      string([]byte{0xff}),
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
	}})
	message, err := collectModelMessage(context.Background(), stream, &credential, nil)
	if message != nil || !errors.Is(err, errMalformedProviderChunk) {
		t.Fatalf("invalid UTF-8 message/error = %#v / %v", message, err)
	}
}

func TestCollectModelMessageRejectsMissingToolIndex(t *testing.T) {
	t.Parallel()

	credential, err := config.NewSecretValue("test-model-credential")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	defer credential.Destroy()
	stream := schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call-missing-index",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "get_resource",
				Arguments: `{"resource":{"kind":"Pod","name":"sample-pod"},"purpose":"Inspect the selected Pod."}`,
			},
		}},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
	}})
	message, err := collectModelMessage(context.Background(), stream, &credential, nil)
	if message != nil || !errors.Is(err, errUnsupportedProviderChunk) {
		t.Fatalf("missing-index message/error = %#v / %v", message, err)
	}
}

func TestCollectModelMessageRequiresOneUsageChunkAfterFinish(t *testing.T) {
	t.Parallel()

	usage := func() *schema.TokenUsage {
		return &schema.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	}
	for _, current := range []struct {
		name   string
		chunks []*schema.Message
	}{
		{
			name: "before finish",
			chunks: []*schema.Message{
				{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: usage()}},
				{Role: schema.Assistant, Content: "done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
			},
		},
		{
			name: "duplicate",
			chunks: []*schema.Message{
				{Role: schema.Assistant, Content: "done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
				{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: usage()}},
				{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: usage()}},
			},
		},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			credential, err := config.NewSecretValue("test-model-credential")
			if err != nil {
				t.Fatalf("NewSecretValue() error = %v", err)
			}
			defer credential.Destroy()
			message, err := collectModelMessage(
				context.Background(),
				schema.StreamReaderFromArray(current.chunks),
				&credential,
				nil,
			)
			if message != nil || !errors.Is(err, errMalformedProviderChunk) {
				t.Fatalf("usage-order message/error = %#v / %v", message, err)
			}
		})
	}
}

func TestCollectModelMessageDoesNotObserveStreamLevelInvalidChunk(t *testing.T) {
	t.Parallel()

	credential, err := config.NewSecretValue("test-model-credential")
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	defer credential.Destroy()
	observed := false
	message, err := collectModelMessage(
		context.Background(),
		schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "This chunk has invalid stream-level ordering.",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
			}},
		}}),
		&credential,
		func(string) error {
			observed = true
			return nil
		},
	)
	if message != nil || !errors.Is(err, errMalformedProviderChunk) || observed {
		t.Fatalf("invalid stream-level chunk message/error/observed = %#v / %v / %t", message, err, observed)
	}
}

func TestValidateResponseChunkRequiresExplicitZeroChoiceIndex(t *testing.T) {
	t.Parallel()

	message := schema.AssistantMessage("synthetic", nil)
	for _, current := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "zero", raw: `{"choices":[{"index":0}]}`, ok: true},
		{name: "missing", raw: `{"choices":[{}]}`},
		{name: "nonzero", raw: `{"choices":[{"index":1}]}`},
		{name: "multiple", raw: `{"choices":[{"index":0},{"index":0}]}`},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			result, err := validateResponseChunk(context.Background(), message, []byte(current.raw), false)
			if current.ok {
				if err != nil || result != message {
					t.Fatalf("valid chunk result/error = %#v / %v", result, err)
				}
				return
			}
			if result != nil || !errors.Is(err, errUnsupportedProviderChunk) {
				t.Fatalf("invalid chunk result/error = %#v / %v", result, err)
			}
		})
	}
}
