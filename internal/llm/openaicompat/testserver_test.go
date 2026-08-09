package openaicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const errorCanaryPlaceholder = "{{ERROR_CANARY}}"

type capturedRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          []byte
}

type streamLimitFixture struct {
	OversizeEventBytes  int `json:"oversize_event_bytes"`
	OversizeStreamBytes int `json:"oversize_stream_bytes"`
	OversizeEventCount  int `json:"oversize_event_count"`
}

type fixtureServer struct {
	server *httptest.Server
	files  map[string][]byte
	limits streamLimitFixture

	mu             sync.Mutex
	requests       []capturedRequest
	errorCanary    string
	redirectTarget string

	requestStarted   chan struct{}
	requestCancelled chan struct{}
	startedOnce      sync.Once
	cancelledOnce    sync.Once
}

func newFixtureServer(t *testing.T, errorCanary string) *fixtureServer {
	t.Helper()

	files := make(map[string][]byte)
	for _, name := range []string{
		"normal.sse",
		"tool-call-fragments.sse",
		"no-usage-eof.sse",
		"malformed.sse",
		"error-401.json",
		"error-429.json",
		"error-500.json",
	} {
		files[name] = readFixtureFile(t, name)
	}
	var limits streamLimitFixture
	if err := json.Unmarshal(readFixtureFile(t, "stream-limits.json"), &limits); err != nil {
		t.Fatalf("decode stream limit fixture: %v", err)
	}
	fixture := &fixtureServer{
		files:            files,
		limits:           limits,
		errorCanary:      errorCanary,
		requestStarted:   make(chan struct{}),
		requestCancelled: make(chan struct{}),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *fixtureServer) endpoint(scenario string) string {
	return fixture.server.URL + "/v1/" + scenario
}

func (fixture *fixtureServer) setRedirectTarget(target string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.redirectTarget = target
}

func (fixture *fixtureServer) capturedRequests() []capturedRequest {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	requests := make([]capturedRequest, len(fixture.requests))
	for index, request := range fixture.requests {
		requests[index] = request
		requests[index].Body = append([]byte(nil), request.Body...)
	}
	return requests
}

func (fixture *fixtureServer) serveHTTP(response http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, int64(domain.MaxModelRequestBytes+1)))
	if err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	fixture.mu.Lock()
	fixture.requests = append(fixture.requests, capturedRequest{
		Method:        request.Method,
		Path:          request.URL.Path,
		Authorization: request.Header.Get("Authorization"),
		ContentType:   request.Header.Get("Content-Type"),
		Body:          append([]byte(nil), body...),
	})
	redirectTarget := fixture.redirectTarget
	fixture.mu.Unlock()

	switch request.URL.Path {
	case "/v1/normal/chat/completions":
		fixture.serveSSE(response, "normal.sse", "request-fixture-normal")
	case "/v1/tool-call-fragments/chat/completions":
		fixture.serveSSE(response, "tool-call-fragments.sse", "request-fixture-tool")
	case "/v1/no-usage-eof/chat/completions":
		fixture.serveSSE(response, "no-usage-eof.sse", "")
	case "/v1/malformed/chat/completions":
		fixture.serveSSE(response, "malformed.sse", "request-fixture-malformed")
	case "/v1/error-401/chat/completions":
		fixture.serveError(response, http.StatusUnauthorized, "error-401.json")
	case "/v1/error-429/chat/completions":
		response.Header().Set("Retry-After", "1")
		fixture.serveError(response, http.StatusTooManyRequests, "error-429.json")
	case "/v1/error-500/chat/completions":
		fixture.serveError(response, http.StatusInternalServerError, "error-500.json")
	case "/v1/oversize-event/chat/completions":
		fixture.serveOversizeEvent(response)
	case "/v1/oversize-stream/chat/completions":
		fixture.serveOversizeStream(response)
	case "/v1/too-many-events/chat/completions":
		fixture.serveTooManyEvents(response)
	case "/v1/cancel/chat/completions", "/v1/timeout/chat/completions":
		fixture.startedOnce.Do(func() { close(fixture.requestStarted) })
		<-request.Context().Done()
		fixture.cancelledOnce.Do(func() { close(fixture.requestCancelled) })
	case "/v1/redirect/chat/completions":
		if redirectTarget == "" {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.Redirect(response, request, redirectTarget, http.StatusTemporaryRedirect)
	default:
		response.WriteHeader(http.StatusNotFound)
	}
}

func (fixture *fixtureServer) serveSSE(response http.ResponseWriter, name, requestID string) {
	content := fixture.files[name]
	response.Header().Set("Content-Type", "text/event-stream")
	if requestID != "" {
		response.Header().Set("X-Request-ID", requestID)
	}
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(content)
}

func (fixture *fixtureServer) serveError(response http.ResponseWriter, status int, name string) {
	content := strings.ReplaceAll(string(fixture.files[name]), errorCanaryPlaceholder, fixture.errorCanary)
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, content)
}

func (fixture *fixtureServer) serveOversizeEvent(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, "data: "+strings.Repeat("x", fixture.limits.OversizeEventBytes)+"\n\n")
}

func (fixture *fixtureServer) serveOversizeStream(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.WriteHeader(http.StatusOK)
	line := ": " + strings.Repeat("x", 1022) + "\n"
	written := 0
	for written <= fixture.limits.OversizeStreamBytes {
		count, err := io.WriteString(response, line)
		written += count
		if err != nil {
			return
		}
	}
}

func (fixture *fixtureServer) serveTooManyEvents(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.WriteHeader(http.StatusOK)
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"
	for index := 0; index < fixture.limits.OversizeEventCount; index++ {
		if _, err := io.WriteString(response, chunk); err != nil {
			return
		}
	}
}

func readFixtureFile(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("..", "..", "..", "testdata", "model", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read model fixture %q: %v", name, err)
	}
	return content
}
