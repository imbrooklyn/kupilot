package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
)

var (
	testStart = time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	testEnd   = testStart.Add(time.Hour)
)

func TestPrometheusClientUsesExactBoundedRequestAndProjectsWarnings(t *testing.T) {
	t.Parallel()

	credentialText := "generated-prometheus-credential"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/query_range" || request.Header.Get("Accept") != "application/json" ||
			request.Header.Get("User-Agent") != userAgent || request.Header.Get("Authorization") != "Bearer "+credentialText {
			t.Errorf("unexpected Prometheus request: %s %s %#v", request.Method, request.URL.String(), request.Header)
		}
		wantQuery := `topk(2, sum by (container) (rate(container_cpu_usage_seconds_total{namespace="team-a",pod="sample-pod",container!=""}[5m])))`
		query := request.URL.Query()
		if query.Get("query") != wantQuery || query.Get("start") != formatSeconds(testStart) || query.Get("end") != formatSeconds(testEnd) || query.Get("step") != "60" || len(query) != 4 {
			t.Errorf("unexpected Prometheus query: %#v", query)
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = fmt.Fprintf(writer, `{"status":"success","warnings":["one shard was partial"],"data":{"resultType":"matrix","result":[{"metric":{"container":"app","credential":"must-not-project"},"values":[[%d,"1.5"],[%d,"2.25"]]}]}}`, testStart.Unix(), testEnd.Unix())
	}))
	defer server.Close()

	client := newPrometheusTestClient(t, server.URL, credentialText, nil, currentGuards())
	defer client.Close()
	request := prometheusRequest(server.URL)
	observation, err := client.QueryPrometheus(context.Background(), request)
	if err != nil || observation.Validate(request) != nil || calls.Load() != 1 || !observation.Partial || !observation.Truncated ||
		len(observation.Series) != 1 || observation.Series[0].Series != "container=app" || len(observation.Series[0].Samples) != 2 ||
		observation.Series[0].Samples[0].Value != 1.5 || observation.SourceBytes == 0 {
		t.Fatalf("QueryPrometheus() observation/error/calls = %#v/%v/%d", observation, err, calls.Load())
	}
	if strings.Contains(fmt.Sprintf("%#v", observation), "must-not-project") {
		t.Fatal("unallowlisted Prometheus label reached the projected observation")
	}
}

func TestPrometheusProjectionMarksTheServerSeriesCeilingPartial(t *testing.T) {
	t.Parallel()

	request := prometheusRequest("http://127.0.0.1:19094")
	request.MaxSeries = 1
	payload := []byte(fmt.Sprintf(
		`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"container":"app"},"values":[[%d,"1.5"]]}]}}`,
		testEnd.Unix(),
	))
	observation, err := decodePrometheus(payload, request)
	if err != nil || observation.Validate(request) != nil || !observation.Partial || !observation.Truncated || len(observation.Series) != 1 {
		t.Fatalf("decodePrometheus() observation/error = %#v/%v", observation, err)
	}
}

func TestLokiClientUsesExactServerFilterPaginationAndPartialCeiling(t *testing.T) {
	t.Parallel()

	credentialText := "generated-loki-credential"
	var mu sync.Mutex
	requests := make([]url.Values, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.URL.Query())
		index := len(requests)
		mu.Unlock()
		if request.Method != http.MethodGet || request.URL.Path != "/loki/api/v1/query_range" ||
			request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != userAgent || request.Header.Get("Content-Type") != "" ||
			request.Header.Get("Authorization") != "Bearer "+credentialText {
			t.Errorf("unexpected Loki request: %s %s", request.Method, request.URL.String())
		}
		query := request.URL.Query()
		if query.Get("query") != `{namespace="team-a",pod="sample-pod"} |= "error"` || query.Get("direction") != "backward" || query.Get("start") != fmt.Sprint(testStart.UnixNano()) || len(query) != 5 {
			t.Errorf("unexpected Loki query: %#v", query)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch index {
		case 1:
			if query.Get("limit") != "2" || query.Get("end") != fmt.Sprint(testEnd.UnixNano()) {
				t.Errorf("unexpected first Loki page: %#v", query)
			}
			_, _ = fmt.Fprintf(writer, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"container":"app","stream":"stderr","secret":"must-not-project"},"values":[["%d","first error"],["%d","second error"]]}]}}`, testEnd.Add(-time.Minute).UnixNano(), testEnd.Add(-2*time.Minute).UnixNano())
		case 2:
			if query.Get("limit") != "1" || query.Get("end") != fmt.Sprint(testEnd.Add(-2*time.Minute).Add(-time.Nanosecond).UnixNano()) {
				t.Errorf("unexpected second Loki page: %#v", query)
			}
			_, _ = fmt.Fprintf(writer, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"container":"app","stream":"stderr"},"values":[["%d","third error"]]}]}}`, testEnd.Add(-3*time.Minute).UnixNano())
		default:
			t.Errorf("unexpected Loki page %d", index)
		}
	}))
	defer server.Close()

	client := newLokiTestClient(t, server.URL, credentialText, nil, currentGuards())
	defer client.Close()
	request := lokiRequest(server.URL)
	observation, err := client.QueryLoki(context.Background(), request)
	if err != nil || observation.Validate(request) != nil || observation.Pages != 2 || len(observation.Lines) != 3 ||
		!observation.Partial || !observation.Truncated || observation.SourceBytes == 0 || observation.Lines[0].Series != "container=app,stream=stderr" {
		t.Fatalf("QueryLoki() observation/error = %#v/%v", observation, err)
	}
	mu.Lock()
	requestCount := len(requests)
	mu.Unlock()
	if requestCount != 2 || strings.Contains(fmt.Sprintf("%#v", observation), "must-not-project") {
		t.Fatalf("Loki request count/projection = %d/%#v", requestCount, observation)
	}
}

func TestLokiClientFailsClosedAndClosesBodies(t *testing.T) {
	t.Parallel()

	for _, current := range []struct {
		name        string
		status      int
		contentType string
		body        string
		limit       int
		transport   error
		wantClass   domain.SafeErrorClass
	}{
		{name: "permission denied", status: http.StatusForbidden, contentType: "application/json", body: `{}`, limit: 1024, wantClass: domain.SafeErrorClassPermissionDenied},
		{name: "malformed", status: http.StatusOK, contentType: "application/json", body: `{"status":"success"}`, limit: 1024, wantClass: domain.SafeErrorClassInvalidExternalResponse},
		{name: "oversize", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", 65), limit: 64, wantClass: domain.SafeErrorClassBudgetExhausted},
		{name: "timeout", limit: 1024, transport: context.DeadlineExceeded, wantClass: domain.SafeErrorClassTimeout},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			var response *http.Response
			var tracked *trackingBody
			if current.transport == nil {
				response = responseWithBody(current.status, current.contentType, current.body) //nolint:bodyclose // The source client owns closure, asserted below.
				tracked = response.Body.(*trackingBody)
			}
			var calls atomic.Int32
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response, current.transport
			})
			client := newLokiTestClient(t, "http://127.0.0.1:19100", "", &http.Client{Transport: transport, Timeout: time.Second}, currentGuards())
			defer client.Close()
			request := lokiRequest("http://127.0.0.1:19100")
			request.LimitBytes = current.limit
			_, err := client.QueryLoki(context.Background(), request)
			if errorClass(err) != current.wantClass || calls.Load() != 1 {
				t.Fatalf("QueryLoki() error/calls = %v/%d", err, calls.Load())
			}
			if tracked != nil && !tracked.Closed() {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestSourceClientLoopbackDeadlineUsesCancellationBarrier(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	client := newPrometheusTestClient(t, server.URL, "", nil, currentGuards())
	defer client.Close()
	ctx := newBarrierDeadlineContext(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.QueryPrometheus(ctx, prometheusRequest(server.URL))
		result <- err
	}()
	<-started
	ctx.expire()
	if err := <-result; errorClass(err) != domain.SafeErrorClassTimeout {
		t.Fatalf("QueryPrometheus() deadline error = %v", err)
	}
}

func TestSourceClientsFailClosedAndCloseBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  *http.Response
		transport error
		limit     int
		wantClass domain.SafeErrorClass
	}{
		{name: "permission denied", response: responseWithBody(http.StatusForbidden, "application/json", `{}`), limit: 1024, wantClass: domain.SafeErrorClassPermissionDenied},           //nolint:bodyclose // The source client owns closure, asserted below.
		{name: "malformed", response: responseWithBody(http.StatusOK, "application/json", `{"status":"success"}`), limit: 1024, wantClass: domain.SafeErrorClassInvalidExternalResponse}, //nolint:bodyclose // The source client owns closure, asserted below.
		{name: "content type", response: responseWithBody(http.StatusOK, "text/plain", `{}`), limit: 1024, wantClass: domain.SafeErrorClassInvalidExternalResponse},                      //nolint:bodyclose // The source client owns closure, asserted below.
		{name: "oversize", response: responseWithBody(http.StatusOK, "application/json", strings.Repeat("x", 65)), limit: 64, wantClass: domain.SafeErrorClassBudgetExhausted},           //nolint:bodyclose // The source client owns closure, asserted below.
		{name: "timeout", transport: context.DeadlineExceeded, limit: 1024, wantClass: domain.SafeErrorClassTimeout},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			var tracked *trackingBody
			if current.response != nil {
				tracked = current.response.Body.(*trackingBody)
			}
			var calls atomic.Int32
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return current.response, current.transport
			})
			client := newPrometheusTestClient(t, "http://127.0.0.1:19090", "", &http.Client{Transport: transport, Timeout: time.Second}, currentGuards())
			defer client.Close()
			request := prometheusRequest("http://127.0.0.1:19090")
			request.LimitBytes = current.limit
			_, err := client.QueryPrometheus(context.Background(), request)
			if errorClass(err) != current.wantClass || calls.Load() != 1 {
				t.Fatalf("QueryPrometheus() error/calls = %v/%d", err, calls.Load())
			}
			if current.response != nil {
				if !tracked.Closed() {
					t.Fatal("response body was not closed")
				}
			}
		})
	}
}

func TestSourceClientsRejectRedirectAndPreflightWithoutLeakingAuthorization(t *testing.T) {
	t.Parallel()

	credentialText := "generated-redirect-credential"
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+credentialText {
			t.Error("source did not receive the configured credential")
		}
		http.Redirect(writer, request, destination.URL+"/capture", http.StatusFound)
	}))
	defer source.Close()
	client := newPrometheusTestClient(t, source.URL, credentialText, nil, currentGuards())
	defer client.Close()
	_, err := client.QueryPrometheus(context.Background(), prometheusRequest(source.URL))
	if errorClass(err) != domain.SafeErrorClassPolicyDenied || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("redirect error/source/destination calls = %v/%d/%d", err, sourceCalls.Load(), destinationCalls.Load())
	}

	tests := []struct {
		name   string
		ctx    context.Context
		guards RuntimeGuards
		mutate func(*toolcontract.PrometheusReadRequest)
		class  domain.SafeErrorClass
	}{
		{name: "cancelled", ctx: cancelledContext(), guards: currentGuards(), class: domain.SafeErrorClassCancelled},
		{name: "stale scope", ctx: context.Background(), guards: staleGuards(), class: domain.SafeErrorClassStaleScope},
		{name: "origin mismatch", ctx: context.Background(), guards: currentGuards(), mutate: func(request *toolcontract.PrometheusReadRequest) { request.Policy.OriginHash = strings.Repeat("a", 64) }, class: domain.SafeErrorClassPolicyDenied},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			var calls atomic.Int32
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return responseWithBody(http.StatusOK, "application/json", `{}`), nil
			})
			client := newPrometheusTestClient(t, "http://127.0.0.1:19091", "", &http.Client{Transport: transport, Timeout: time.Second}, current.guards)
			defer client.Close()
			request := prometheusRequest("http://127.0.0.1:19091")
			if current.mutate != nil {
				current.mutate(&request)
			}
			_, err := client.QueryPrometheus(current.ctx, request)
			if errorClass(err) != current.class || calls.Load() != 0 {
				t.Fatalf("preflight error/calls = %v/%d", err, calls.Load())
			}
		})
	}
}

func TestLokiClientRejectsRedirectWithoutForwardingAuthorization(t *testing.T) {
	t.Parallel()

	credentialText := "generated-loki-redirect-credential"
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+credentialText {
			t.Error("source did not receive the configured Loki credential")
		}
		http.Redirect(writer, request, destination.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := newLokiTestClient(t, source.URL, credentialText, nil, currentGuards())
	defer client.Close()
	_, err := client.QueryLoki(context.Background(), lokiRequest(source.URL))
	if errorClass(err) != domain.SafeErrorClassPolicyDenied || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("redirect error/source/destination calls = %v/%d/%d", err, sourceCalls.Load(), destinationCalls.Load())
	}
}

func TestSourceClientDiscardsResponseWhenGenerationBecomesStale(t *testing.T) {
	t.Parallel()

	guard := &testGuard{scopeResults: []bool{true, false}, policyCurrent: true}
	body := responseWithBody(http.StatusOK, "application/json", fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"container":"app"},"values":[[%d,"1"]]}]}}`, testEnd.Unix())) //nolint:bodyclose // The source client owns closure, asserted below.
	tracked := body.Body.(*trackingBody)
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return body, nil
	})
	client := newPrometheusTestClient(t, "http://127.0.0.1:19092", "", &http.Client{Transport: transport, Timeout: time.Second}, RuntimeGuards{Scope: guard, Policy: guard})
	defer client.Close()
	_, err := client.QueryPrometheus(context.Background(), prometheusRequest("http://127.0.0.1:19092"))
	if errorClass(err) != domain.SafeErrorClassStaleScope || calls.Load() != 1 || !tracked.Closed() {
		t.Fatalf("late stale error/calls/body = %v/%d/%v", err, calls.Load(), tracked.Closed())
	}
}

func TestSourceClientConfigurationRejectsUnsafeOriginsWithoutNetwork(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"http://example.invalid", "https://user@example.invalid", "https://example.invalid/path",
		"https://example.invalid?query=value", "https://example.invalid/#fragment", "HTTP://127.0.0.1:9090",
	} {
		var calls atomic.Int32
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("must not be called")
		})
		settings := sourceSettings(domain.DataSourcePrometheus, origin, config.DataSourceCredentialNone)
		client, err := NewPrometheusClient(&settings, nil, &http.Client{Transport: transport, Timeout: time.Second}, currentGuards())
		if client != nil || errorClass(err) != domain.SafeErrorClassConfigurationInvalid || calls.Load() != 0 {
			t.Fatalf("unsafe origin %q client/error/calls = %#v/%v/%d", origin, client, err, calls.Load())
		}
	}
}

func TestSourceClientRejectsPolicyExpansionBeforeNetwork(t *testing.T) {
	t.Parallel()

	for _, current := range []struct {
		name   string
		mutate func(*toolcontract.PrometheusReadRequest)
	}{
		{name: "query allowlist", mutate: func(request *toolcontract.PrometheusReadRequest) {
			request.Policy.Queries = []domain.ObservabilityQueryID{domain.QueryPrometheusPodCPUUsage, domain.QueryPrometheusPodMemoryWorkingSet}
		}},
		{name: "timeout", mutate: func(request *toolcontract.PrometheusReadRequest) {
			request.Policy.RequestTimeout = 2 * time.Second
		}},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("must not be called")
			})
			const origin = "http://127.0.0.1:19093"
			client := newPrometheusTestClient(t, origin, "", &http.Client{Transport: transport, Timeout: time.Second}, currentGuards())
			defer client.Close()
			request := prometheusRequest(origin)
			current.mutate(&request)
			_, err := client.QueryPrometheus(context.Background(), request)
			if errorClass(err) != domain.SafeErrorClassPolicyDenied || calls.Load() != 0 {
				t.Fatalf("expanded policy error/calls = %v/%d", err, calls.Load())
			}
		})
	}
}

func newPrometheusTestClient(t *testing.T, origin, credentialText string, httpClient *http.Client, guards RuntimeGuards) *PrometheusClient {
	t.Helper()
	reference := config.DataSourceCredentialNone
	var credential *config.DataSourceCredential
	if credentialText != "" {
		reference = config.DataSourceCredentialPrometheus
		secret, err := config.NewSecretValue(credentialText)
		if err != nil {
			t.Fatalf("NewSecretValue() error = %v", err)
		}
		t.Cleanup(secret.Destroy)
		credential = &config.DataSourceCredential{Kind: domain.DataSourcePrometheus, Reference: reference, Value: secret}
	}
	settings := sourceSettings(domain.DataSourcePrometheus, origin, reference)
	client, err := NewPrometheusClient(&settings, credential, httpClient, guards)
	if err != nil {
		t.Fatalf("NewPrometheusClient() error = %v", err)
	}
	return client
}

func newLokiTestClient(t *testing.T, origin, credentialText string, httpClient *http.Client, guards RuntimeGuards) *LokiClient {
	t.Helper()
	reference := config.DataSourceCredentialNone
	var credential *config.DataSourceCredential
	if credentialText != "" {
		reference = config.DataSourceCredentialLoki
		secret, err := config.NewSecretValue(credentialText)
		if err != nil {
			t.Fatalf("NewSecretValue() error = %v", err)
		}
		t.Cleanup(secret.Destroy)
		credential = &config.DataSourceCredential{Kind: domain.DataSourceLoki, Reference: reference, Value: secret}
	}
	settings := sourceSettings(domain.DataSourceLoki, origin, reference)
	client, err := NewLokiClient(&settings, credential, httpClient, guards)
	if err != nil {
		t.Fatalf("NewLokiClient() error = %v", err)
	}
	return client
}

type barrierDeadlineContext struct {
	context.Context
	done    chan struct{}
	once    sync.Once
	expired atomic.Bool
}

func newBarrierDeadlineContext(parent context.Context) *barrierDeadlineContext {
	return &barrierDeadlineContext{Context: parent, done: make(chan struct{})}
}

func (*barrierDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *barrierDeadlineContext) Done() <-chan struct{}   { return ctx.done }
func (ctx *barrierDeadlineContext) Err() error {
	if ctx.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (ctx *barrierDeadlineContext) expire() {
	ctx.once.Do(func() {
		ctx.expired.Store(true)
		close(ctx.done)
	})
}

func sourceSettings(kind domain.DataSourceKind, origin string, reference config.DataSourceCredentialReference) config.DataSourceConfig {
	query := string(domain.QueryPrometheusPodCPUUsage)
	if kind == domain.DataSourceLoki {
		query = string(domain.QueryLokiPodLogs)
	}
	return config.DataSourceConfig{Endpoint: origin, Origin: origin, CredentialReference: reference, Queries: []string{query}, RequestTimeoutSeconds: 1}
}

func prometheusRequest(origin string) toolcontract.PrometheusReadRequest {
	return toolcontract.PrometheusReadRequest{
		Scope: testScope(), PolicyGeneration: 3,
		Policy:    domain.DataSourcePolicy{Kind: domain.DataSourcePrometheus, OriginHash: domain.SHA256Hex(origin), Queries: []domain.ObservabilityQueryID{domain.QueryPrometheusPodCPUUsage}, RequestTimeout: time.Second},
		Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
		QueryID:   domain.QueryPrometheusPodCPUUsage, Start: testStart, End: testEnd, Step: time.Minute,
		MaxSeries: 2, MaxSamples: 122, LimitBytes: 64 * 1024,
	}
}

func lokiRequest(origin string) toolcontract.LokiReadRequest {
	return toolcontract.LokiReadRequest{
		Scope: testScope(), PolicyGeneration: 3,
		Policy:    domain.DataSourcePolicy{Kind: domain.DataSourceLoki, OriginHash: domain.SHA256Hex(origin), Queries: []domain.ObservabilityQueryID{domain.QueryLokiPodLogs}, RequestTimeout: time.Second},
		Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
		QueryID:   domain.QueryLokiPodLogs, Contains: "error", Start: testStart, End: testEnd,
		MaxPages: 2, PageLines: 2, MaxLines: 3, LimitBytes: 64 * 1024,
	}
}

func testScope() domain.ClusterScope {
	return domain.ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: testStart.Add(-time.Hour)}
}

type testGuard struct {
	mu            sync.Mutex
	scopeResults  []bool
	policyCurrent bool
}

func (guard *testGuard) Current(context.Context, domain.ClusterScope) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if len(guard.scopeResults) == 0 {
		return guard.policyCurrent
	}
	result := guard.scopeResults[0]
	guard.scopeResults = guard.scopeResults[1:]
	return result
}

func (guard *testGuard) CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.policyCurrent
}

func currentGuards() RuntimeGuards {
	guard := &testGuard{policyCurrent: true}
	return RuntimeGuards{Scope: guard, Policy: guard}
}

func staleGuards() RuntimeGuards {
	guard := &testGuard{}
	return RuntimeGuards{Scope: guard, Policy: guard}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	reader io.Reader
	mu     sync.Mutex
	closed bool
}

func (body *trackingBody) Read(value []byte) (int, error) { return body.reader.Read(value) }
func (body *trackingBody) Close() error {
	body.mu.Lock()
	defer body.mu.Unlock()
	body.closed = true
	return nil
}
func (body *trackingBody) Closed() bool {
	body.mu.Lock()
	defer body.mu.Unlock()
	return body.closed
}

func responseWithBody(status int, contentType, value string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       &trackingBody{reader: strings.NewReader(value)},
	}
}

func errorClass(err error) domain.SafeErrorClass {
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) {
		return classified.Class()
	}
	return ""
}
