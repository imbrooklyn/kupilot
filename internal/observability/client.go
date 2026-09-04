package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
)

const userAgent = "kupilot/0.5"

type sourceClient struct {
	kind        domain.DataSourceKind
	origin      *url.URL
	policy      domain.DataSourcePolicy
	credential  *config.SecretValue
	http        *http.Client
	scopeGuard  toolcontract.ScopeGuard
	policyGuard toolcontract.PolicyGenerationGuard
	closeOnce   sync.Once
}

type PrometheusClient struct{ source *sourceClient }
type LokiClient struct{ source *sourceClient }

type RuntimeGuards struct {
	Scope  toolcontract.ScopeGuard
	Policy toolcontract.PolicyGenerationGuard
}

var _ toolcontract.PrometheusReader = (*PrometheusClient)(nil)
var _ toolcontract.LokiReader = (*LokiClient)(nil)

// DisabledSources occupies the two fixed handler slots without owning a
// network client. Strict binding rejects disabled-source calls before these
// methods are reachable.
type DisabledSources struct{}

func (DisabledSources) QueryPrometheus(context.Context, toolcontract.PrometheusReadRequest) (toolcontract.PrometheusObservation, error) {
	return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassPolicyDenied, "prometheus_disabled")
}

func (DisabledSources) QueryLoki(context.Context, toolcontract.LokiReadRequest) (toolcontract.LokiObservation, error) {
	return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassPolicyDenied, "loki_disabled")
}

func NewPrometheusClient(settings *config.DataSourceConfig, credential *config.DataSourceCredential, client *http.Client, guards RuntimeGuards) (*PrometheusClient, error) {
	source, err := newSourceClient(domain.DataSourcePrometheus, settings, credential, client, guards)
	if err != nil {
		return nil, err
	}
	return &PrometheusClient{source: source}, nil
}

func NewLokiClient(settings *config.DataSourceConfig, credential *config.DataSourceCredential, client *http.Client, guards RuntimeGuards) (*LokiClient, error) {
	source, err := newSourceClient(domain.DataSourceLoki, settings, credential, client, guards)
	if err != nil {
		return nil, err
	}
	return &LokiClient{source: source}, nil
}

func newSourceClient(kind domain.DataSourceKind, settings *config.DataSourceConfig, credential *config.DataSourceCredential, client *http.Client, guards RuntimeGuards) (*sourceClient, error) {
	if settings == nil || !kind.Valid() || guards.Scope == nil || guards.Policy == nil || settings.Origin == "" || settings.Endpoint != settings.Origin || settings.RequestTimeoutSeconds < 1 || settings.RequestTimeoutSeconds > config.MaxDataSourceTimeoutSeconds {
		return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_configuration_invalid")
	}
	origin, err := url.Parse(settings.Origin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_origin_invalid")
	}
	hostname := strings.ToLower(origin.Hostname())
	loopback := hostname == "localhost"
	if address := net.ParseIP(hostname); address != nil {
		loopback = address.IsLoopback()
	}
	if origin.Scheme != "https" && (origin.Scheme != "http" || !loopback) ||
		origin.String() != settings.Origin {
		return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_origin_invalid")
	}
	queries := make([]domain.ObservabilityQueryID, len(settings.Queries))
	for index, query := range settings.Queries {
		queries[index] = domain.ObservabilityQueryID(query)
	}
	configuredPolicy := domain.DataSourcePolicy{
		Kind: kind, OriginHash: domain.SHA256Hex(settings.Origin), Queries: queries,
		RequestTimeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
	}
	if configuredPolicy.Validate() != nil {
		return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_policy_invalid")
	}
	transportClient := client
	if transportClient == nil {
		transportClient = &http.Client{Transport: http.DefaultTransport, Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second}
	} else {
		copy := *transportClient
		transportClient = &copy
		if transportClient.Timeout <= 0 || transportClient.Timeout > time.Duration(settings.RequestTimeoutSeconds)*time.Second {
			transportClient.Timeout = time.Duration(settings.RequestTimeoutSeconds) * time.Second
		}
	}
	transportClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirectDenied }
	var owned *config.SecretValue
	if settings.CredentialReference != config.DataSourceCredentialNone {
		if credential == nil || credential.Kind != kind || credential.Reference != settings.CredentialReference || !credential.Value.IsSet() {
			return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_credential_invalid")
		}
		clone, cloneErr := credential.Value.Clone()
		if cloneErr != nil {
			return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_credential_invalid")
		}
		owned = &clone
	} else if credential != nil && (credential.Kind != kind || credential.Reference != config.DataSourceCredentialNone || credential.Value.IsSet()) {
		return nil, safeError(domain.SafeErrorClassConfigurationInvalid, "observability_credential_unexpected")
	}
	return &sourceClient{kind: kind, origin: origin, policy: configuredPolicy.Copy(), credential: owned, http: transportClient, scopeGuard: guards.Scope, policyGuard: guards.Policy}, nil
}

func (client *sourceClient) allows(policy domain.DataSourcePolicy) bool {
	if client == nil || client.policy.Validate() != nil || policy.Validate() != nil ||
		client.policy.Kind != policy.Kind || client.policy.OriginHash != policy.OriginHash ||
		client.policy.RequestTimeout != policy.RequestTimeout || len(client.policy.Queries) != len(policy.Queries) {
		return false
	}
	for index := range policy.Queries {
		if client.policy.Queries[index] != policy.Queries[index] {
			return false
		}
	}
	return true
}

func (client *sourceClient) current(ctx context.Context, scope domain.ClusterScope, generation domain.PolicyGeneration) bool {
	return client != nil && client.scopeGuard != nil && client.policyGuard != nil && client.scopeGuard.Current(ctx, scope) && client.policyGuard.CurrentPolicyGeneration(ctx, generation)
}

func (client *PrometheusClient) Close() {
	if client != nil && client.source != nil {
		client.source.close()
	}
}
func (client *LokiClient) Close() {
	if client != nil && client.source != nil {
		client.source.close()
	}
}
func (client *sourceClient) close() {
	if client == nil {
		return
	}
	client.closeOnce.Do(func() {
		if client.credential != nil {
			client.credential.Destroy()
		}
		if closer, ok := client.http.Transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	})
}

func (client *sourceClient) endpoint(path string, query url.Values) (string, error) {
	if client == nil || client.origin == nil || !strings.HasPrefix(path, "/") {
		return "", safeError(domain.SafeErrorClassInternal, "observability_client_invalid")
	}
	target := *client.origin
	target.Path, target.RawPath, target.RawQuery = path, "", query.Encode()
	if target.Scheme != client.origin.Scheme || target.Host != client.origin.Host {
		return "", safeError(domain.SafeErrorClassPolicyDenied, "observability_origin_mismatch")
	}
	return target.String(), nil
}

func (client *sourceClient) get(ctx context.Context, path string, query url.Values, maximum int) ([]byte, error) {
	if ctx == nil || maximum < 1 || maximum > domain.MaxObservabilityBytes {
		return nil, safeError(domain.SafeErrorClassInvalidInput, "observability_request_invalid")
	}
	target, err := client.endpoint(path, query)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, safeError(domain.SafeErrorClassInternal, "observability_request_invalid")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if client.credential != nil {
		if err := client.credential.Use(func(value string) { request.Header.Set("Authorization", "Bearer "+value) }); err != nil {
			return nil, safeError(domain.SafeErrorClassAuthenticationFailed, "observability_credential_unavailable")
		}
	}
	response, rawErr := client.http.Do(request)
	if rawErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, classifyRequestError(ctx, rawErr)
	}
	if response == nil || response.Body == nil {
		return nil, safeError(domain.SafeErrorClassInvalidExternalResponse, "observability_response_invalid")
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, int64(maximum)+1))
	closeErr := response.Body.Close()
	if ctx.Err() != nil {
		return nil, classifyRequestError(ctx, ctx.Err())
	}
	if readErr != nil || closeErr != nil {
		return nil, classifyRequestError(ctx, errors.Join(readErr, closeErr))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyStatus(response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if len(payload) > maximum {
		return nil, safeError(domain.SafeErrorClassBudgetExhausted, "observability_response_limit")
	}
	if !strings.HasPrefix(contentType, "application/json") {
		return nil, safeError(domain.SafeErrorClassInvalidExternalResponse, "observability_content_type_invalid")
	}
	return payload, nil
}

func (client *PrometheusClient) QueryPrometheus(ctx context.Context, request toolcontract.PrometheusReadRequest) (toolcontract.PrometheusObservation, error) {
	if client == nil || client.source == nil || request.Validate() != nil || !client.source.allows(request.Policy) || client.source.kind != domain.DataSourcePrometheus {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassPolicyDenied, "prometheus_request_denied")
	}
	if ctx == nil {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidInput, "prometheus_request_invalid")
	}
	if ctx.Err() != nil {
		return toolcontract.PrometheusObservation{}, classifyRequestError(ctx, ctx.Err())
	}
	if !client.source.current(ctx, request.Scope, request.PolicyGeneration) {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassStaleScope, "prometheus_policy_stale")
	}
	query, err := prometheusQuery(request)
	if err != nil {
		return toolcontract.PrometheusObservation{}, err
	}
	values := url.Values{}
	values.Set("end", formatSeconds(request.End))
	values.Set("query", query)
	values.Set("start", formatSeconds(request.Start))
	values.Set("step", strconv.FormatInt(int64(request.Step/time.Second), 10))
	payload, err := client.source.get(ctx, "/api/v1/query_range", values, request.LimitBytes)
	if err != nil {
		return toolcontract.PrometheusObservation{}, err
	}
	if !client.source.current(ctx, request.Scope, request.PolicyGeneration) {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassStaleScope, "prometheus_policy_stale")
	}
	return decodePrometheus(payload, request)
}

func prometheusQuery(request toolcontract.PrometheusReadRequest) (string, error) {
	selector := `namespace=` + strconv.Quote(request.Reference.Namespace) + `,pod=` + strconv.Quote(request.Reference.Name)
	limit := strconv.Itoa(request.MaxSeries)
	var expression string
	switch request.QueryID {
	case domain.QueryPrometheusPodCPUUsage:
		expression = `sum by (container) (rate(container_cpu_usage_seconds_total{` + selector + `,container!=""}[5m]))`
	case domain.QueryPrometheusPodMemoryWorkingSet:
		expression = `sum by (container) (container_memory_working_set_bytes{` + selector + `,container!=""})`
	case domain.QueryPrometheusPodNetworkReceiveRate:
		expression = `sum by (interface) (rate(container_network_receive_bytes_total{` + selector + `}[5m]))`
	case domain.QueryPrometheusPodNetworkTransmitRate:
		expression = `sum by (interface) (rate(container_network_transmit_bytes_total{` + selector + `}[5m]))`
	default:
		return "", safeError(domain.SafeErrorClassPolicyDenied, "prometheus_query_denied")
	}
	return `topk(` + limit + `, ` + expression + `)`, nil
}

type prometheusResponse struct {
	Status   string   `json:"status"`
	Warnings []string `json:"warnings"`
	Data     struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string   `json:"metric"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodePrometheus(payload []byte, request toolcontract.PrometheusReadRequest) (toolcontract.PrometheusObservation, error) {
	var response prometheusResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&response) != nil || decoder.Decode(&struct{}{}) != io.EOF || response.Status != "success" || response.Data.ResultType != "matrix" || len(response.Data.Result) > request.MaxSeries {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_response_invalid")
	}
	// The code-owned query uses topk as its server-side series ceiling. A full
	// result at that ceiling may have omitted lower-ranked series, so preserve
	// that uncertainty as visible partial state even when the server emits no
	// warning.
	seriesCeilingReached := len(response.Data.Result) == request.MaxSeries
	result := toolcontract.PrometheusObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End, SourceBytes: len(payload), Series: make([]toolcontract.PrometheusSeries, 0, len(response.Data.Result)), Partial: len(response.Warnings) > 0 || seriesCeilingReached, Truncated: len(response.Warnings) > 0 || seriesCeilingReached}
	total := 0
	for _, rawSeries := range response.Data.Result {
		series, ok := projectedSeries(rawSeries.Metric)
		if !ok || len(rawSeries.Values) > request.MaxSamples-total {
			return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_response_invalid")
		}
		projected := toolcontract.PrometheusSeries{Series: series, Samples: make([]toolcontract.PrometheusSample, 0, len(rawSeries.Values))}
		for _, value := range rawSeries.Values {
			if len(value) != 2 {
				return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_sample_invalid")
			}
			var timestamp float64
			var number string
			if json.Unmarshal(value[0], &timestamp) != nil || json.Unmarshal(value[1], &number) != nil {
				return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_sample_invalid")
			}
			parsed, err := strconv.ParseFloat(number, 64)
			seconds, fraction := mathModf(timestamp)
			when := time.Unix(seconds, fraction).UTC()
			if err != nil || mathInvalid(parsed) || when.Before(request.Start) || when.After(request.End) {
				return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_sample_invalid")
			}
			projected.Samples = append(projected.Samples, toolcontract.PrometheusSample{Timestamp: when, Value: parsed})
			total++
		}
		result.Series = append(result.Series, projected)
	}
	sort.Slice(result.Series, func(i, j int) bool { return result.Series[i].Series < result.Series[j].Series })
	if result.Validate(request) != nil {
		return toolcontract.PrometheusObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "prometheus_response_invalid")
	}
	return result, nil
}

func projectedSeries(labels map[string]string) (string, bool) {
	for _, key := range []string{"container", "interface"} {
		if value := labels[key]; value != "" {
			if !safeIdentity(value, 253) {
				return "", false
			}
			return key + "=" + value, true
		}
	}
	return "aggregate", true
}

func safeIdentity(value string, maximum int) bool {
	if !domain.ValidModelText(value, maximum, false) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (client *LokiClient) QueryLoki(ctx context.Context, request toolcontract.LokiReadRequest) (toolcontract.LokiObservation, error) {
	if client == nil || client.source == nil || request.Validate() != nil || !client.source.allows(request.Policy) || client.source.kind != domain.DataSourceLoki {
		return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassPolicyDenied, "loki_request_denied")
	}
	if ctx == nil {
		return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassInvalidInput, "loki_request_invalid")
	}
	if ctx.Err() != nil {
		return toolcontract.LokiObservation{}, classifyRequestError(ctx, ctx.Err())
	}
	if !client.source.current(ctx, request.Scope, request.PolicyGeneration) {
		return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassStaleScope, "loki_policy_stale")
	}
	query := `{namespace=` + strconv.Quote(request.Reference.Namespace) + `,pod=` + strconv.Quote(request.Reference.Name) + `}`
	if request.Contains != "" {
		query += ` |= ` + strconv.Quote(request.Contains)
	}
	result := toolcontract.LokiObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End, Lines: []toolcontract.LokiLineObservation{}}
	cursor := request.End
	seen := make(map[string]struct{})
	complete := false
	for result.Pages < request.MaxPages && len(result.Lines) < request.MaxLines {
		if !client.source.current(ctx, request.Scope, request.PolicyGeneration) {
			return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassStaleScope, "loki_policy_stale")
		}
		remainingBytes := request.LimitBytes - result.SourceBytes
		if remainingBytes < 1 {
			result.Partial, result.Truncated = true, true
			break
		}
		pageLimit := min(request.PageLines, request.MaxLines-len(result.Lines))
		values := url.Values{}
		values.Set("direction", "backward")
		values.Set("end", strconv.FormatInt(cursor.UnixNano(), 10))
		values.Set("limit", strconv.Itoa(pageLimit))
		values.Set("query", query)
		values.Set("start", strconv.FormatInt(request.Start.UnixNano(), 10))
		payload, err := client.source.get(ctx, "/loki/api/v1/query_range", values, remainingBytes)
		if err != nil {
			var classified interface{ Class() domain.SafeErrorClass }
			if len(result.Lines) > 0 && errors.As(err, &classified) && classified.Class() == domain.SafeErrorClassBudgetExhausted {
				result.Partial, result.Truncated = true, true
				break
			}
			return toolcontract.LokiObservation{}, err
		}
		if !client.source.current(ctx, request.Scope, request.PolicyGeneration) {
			return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassStaleScope, "loki_policy_stale")
		}
		page, rawCount, oldest, err := decodeLokiPage(payload, request, cursor, pageLimit, seen)
		if err != nil {
			return toolcontract.LokiObservation{}, err
		}
		result.Pages++
		result.SourceBytes += len(payload)
		result.Lines = append(result.Lines, page...)
		if rawCount < pageLimit || oldest.IsZero() {
			complete = true
			break
		}
		if !oldest.After(request.Start) {
			complete = true
			break
		}
		cursor = oldest.Add(-time.Nanosecond)
	}
	if !complete && (len(result.Lines) >= request.MaxLines || result.Pages >= request.MaxPages && len(result.Lines) > 0) {
		result.Partial, result.Truncated = true, true
	}
	if result.Pages == 0 {
		result.Pages = 1
	}
	if result.Validate(request) != nil {
		return toolcontract.LokiObservation{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_response_invalid")
	}
	return result, nil
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodeLokiPage(payload []byte, request toolcontract.LokiReadRequest, cursor time.Time, limit int, seen map[string]struct{}) ([]toolcontract.LokiLineObservation, int, time.Time, error) {
	var response lokiResponse
	if json.Unmarshal(payload, &response) != nil || response.Status != "success" || response.Data.ResultType != "streams" {
		return nil, 0, time.Time{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_response_invalid")
	}
	result := make([]toolcontract.LokiLineObservation, 0, limit)
	oldest := time.Time{}
	rawCount := 0
	for _, stream := range response.Data.Result {
		series, ok := projectedLokiSeries(stream.Stream)
		if !ok {
			return nil, 0, time.Time{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_stream_invalid")
		}
		for _, value := range stream.Values {
			rawCount++
			if rawCount > limit || len(value) != 2 {
				return nil, 0, time.Time{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_line_limit_invalid")
			}
			nanoseconds, err := strconv.ParseInt(value[0], 10, 64)
			when := time.Unix(0, nanoseconds).UTC()
			if err != nil || when.Before(request.Start) || when.After(request.End) || when.After(cursor) {
				return nil, 0, time.Time{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_line_invalid")
			}
			if oldest.IsZero() || when.Before(oldest) {
				oldest = when
			}
			key := value[0] + "\x00" + series + "\x00" + value[1]
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			content, err := toolcontract.NewLokiLineContent([]byte(value[1]), request.LimitBytes)
			if err != nil {
				return nil, 0, time.Time{}, safeError(domain.SafeErrorClassInvalidExternalResponse, "loki_line_invalid")
			}
			result = append(result, toolcontract.LokiLineObservation{Timestamp: when, Series: series, Content: content})
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if !result[left].Timestamp.Equal(result[right].Timestamp) {
			return result[left].Timestamp.After(result[right].Timestamp)
		}
		return result[left].Series < result[right].Series
	})
	return result, rawCount, oldest, nil
}

func projectedLokiSeries(labels map[string]string) (string, bool) {
	parts := make([]string, 0, 2)
	for _, key := range []string{"container", "stream"} {
		if value := labels[key]; value != "" {
			if !safeIdentity(value, 253) {
				return "", false
			}
			parts = append(parts, key+"="+value)
		}
	}
	if len(parts) == 0 {
		return "pod", true
	}
	return strings.Join(parts, ","), true
}

func formatSeconds(value time.Time) string {
	return strconv.FormatFloat(float64(value.UnixNano())/1e9, 'f', 3, 64)
}
func mathModf(value float64) (int64, int64) {
	whole, fraction := math.Modf(value)
	return int64(whole), int64(fraction * 1e9)
}
func mathInvalid(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }
