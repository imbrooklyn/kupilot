package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

var (
	ErrInvalidDataSourceToolDependencies = errors.New("observability data-source Tool dependencies are invalid")
	ErrInvalidDataSourceRead             = errors.New("observability data-source read data is invalid")
	ErrRawLokiSerializationDenied        = errors.New("raw Loki content cannot be serialized")
)

type ObservationPolicyDecision string

const (
	ObservationPolicyAllowed            ObservationPolicyDecision = "allowed"
	ObservationPolicyConsentRequired    ObservationPolicyDecision = "consent_required"
	ObservationPolicyPermissionRequired ObservationPolicyDecision = "permission_required"
	ObservationPolicyDenied             ObservationPolicyDecision = "denied"
)

type ObservationPolicyRequest struct {
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Kind             domain.DataSourceKind
	OriginHash       string
	QueryID          domain.ObservabilityQueryID
}

func (request ObservationPolicyRequest) valid() bool {
	return request.RunID.Valid() && request.SessionID.Valid() && request.Scope.Validate() == nil && request.PolicyGeneration.Valid() &&
		request.Kind.Valid() && len(request.OriginHash) == 64 && request.QueryID.ValidFor(request.Kind)
}

type ObservationDataPolicy interface {
	AuthorizeObservation(context.Context, ObservationPolicyRequest) ObservationPolicyDecision
}

type PrometheusReadRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Policy           domain.DataSourcePolicy
	Reference        domain.ResourceRef
	QueryID          domain.ObservabilityQueryID
	Start            time.Time
	End              time.Time
	Step             time.Duration
	MaxSeries        int
	MaxSamples       int
	LimitBytes       int
}

func (request PrometheusReadRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || request.Policy.Validate() != nil || request.Policy.Kind != domain.DataSourcePrometheus || !request.Policy.Allows(request.QueryID) ||
		domain.ValidateLiveResourceRef(request.Reference) != nil || request.Reference.Kind != "Pod" || !request.Scope.AllowsReference(request.Reference) ||
		!validRequiredUTCTime(request.Start) || !validRequiredUTCTime(request.End) || request.End.Before(request.Start) || request.End.Sub(request.Start) > domain.MaxObservabilityWindow ||
		request.Step < 15*time.Second || request.Step > domain.MaxObservabilityStep || request.MaxSeries < 1 || request.MaxSeries > domain.MaxObservabilitySeries ||
		request.MaxSamples < 1 || request.MaxSamples > domain.MaxObservabilitySamples || request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidDataSourceRead
	}
	return nil
}

type PrometheusSample struct {
	Timestamp time.Time
	Value     float64
}

type PrometheusSeries struct {
	Series  string
	Samples []PrometheusSample
}

type PrometheusObservation struct {
	Reference   domain.ResourceRef
	QueryID     domain.ObservabilityQueryID
	Start       time.Time
	End         time.Time
	Series      []PrometheusSeries
	SourceBytes int
	Partial     bool
	Truncated   bool
}

func (observation PrometheusObservation) Validate(request PrometheusReadRequest) error {
	if request.Validate() != nil || observation.Reference != request.Reference || observation.QueryID != request.QueryID ||
		!observation.Start.Equal(request.Start) || !observation.End.Equal(request.End) || len(observation.Series) > request.MaxSeries ||
		observation.SourceBytes < 0 || observation.SourceBytes > request.LimitBytes {
		return ErrInvalidDataSourceRead
	}
	total := 0
	previous := ""
	for _, series := range observation.Series {
		if !domain.ValidModelText(series.Series, 512, false) || previous != "" && previous >= series.Series {
			return ErrInvalidDataSourceRead
		}
		previous = series.Series
		previousSample := time.Time{}
		for _, sample := range series.Samples {
			total++
			if total > request.MaxSamples || !validRequiredUTCTime(sample.Timestamp) || sample.Timestamp.Before(request.Start) || sample.Timestamp.After(request.End) || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
				return ErrInvalidDataSourceRead
			}
			if !previousSample.IsZero() && !sample.Timestamp.After(previousSample) {
				return ErrInvalidDataSourceRead
			}
			previousSample = sample.Timestamp
		}
	}
	return nil
}

type PrometheusReader interface {
	QueryPrometheus(context.Context, PrometheusReadRequest) (PrometheusObservation, error)
}

type LokiReadRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Policy           domain.DataSourcePolicy
	Reference        domain.ResourceRef
	QueryID          domain.ObservabilityQueryID
	Contains         string
	Start            time.Time
	End              time.Time
	MaxPages         int
	PageLines        int
	MaxLines         int
	LimitBytes       int
}

func (request LokiReadRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || request.Policy.Validate() != nil || request.Policy.Kind != domain.DataSourceLoki || !request.Policy.Allows(request.QueryID) ||
		domain.ValidateLiveResourceRef(request.Reference) != nil || request.Reference.Kind != "Pod" || !request.Scope.AllowsReference(request.Reference) ||
		!validRequiredUTCTime(request.Start) || !validRequiredUTCTime(request.End) || request.End.Before(request.Start) || request.End.Sub(request.Start) > domain.MaxObservabilityWindow ||
		request.MaxPages < 1 || request.MaxPages > domain.MaxObservabilityPages || request.PageLines < 1 || request.PageLines > domain.MaxObservabilityLines ||
		request.MaxLines < 1 || request.MaxLines > domain.MaxObservabilityLines || request.PageLines > request.MaxLines ||
		!domain.ValidModelText(request.Contains, 256, true) ||
		request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidDataSourceRead
	}
	return nil
}

type LokiLineContent struct{ value []byte }

func NewLokiLineContent(value []byte, maximum int) (LokiLineContent, error) {
	if maximum < 1 || maximum > domain.MaxObservabilityBytes || len(value) > maximum || !utf8.Valid(value) {
		return LokiLineContent{}, ErrInvalidDataSourceRead
	}
	return LokiLineContent{value: append([]byte(nil), value...)}, nil
}

func (content LokiLineContent) Len() int             { return len(content.value) }
func (LokiLineContent) String() string               { return "<bounded raw Loki line>" }
func (LokiLineContent) GoString() string             { return "tools.LokiLineContent{<bounded>}" }
func (LokiLineContent) MarshalJSON() ([]byte, error) { return nil, ErrRawLokiSerializationDenied }
func (content LokiLineContent) bytes() []byte        { return append([]byte(nil), content.value...) }

type LokiLineObservation struct {
	Timestamp time.Time
	Series    string
	Content   LokiLineContent
}

type LokiObservation struct {
	Reference   domain.ResourceRef
	QueryID     domain.ObservabilityQueryID
	Start       time.Time
	End         time.Time
	Lines       []LokiLineObservation
	Pages       int
	SourceBytes int
	Partial     bool
	Truncated   bool
}

func (observation LokiObservation) Validate(request LokiReadRequest) error {
	if request.Validate() != nil || observation.Reference != request.Reference || observation.QueryID != request.QueryID ||
		!observation.Start.Equal(request.Start) || !observation.End.Equal(request.End) || observation.Pages < 1 || observation.Pages > request.MaxPages ||
		len(observation.Lines) > request.MaxLines || observation.SourceBytes < 0 || observation.SourceBytes > request.LimitBytes {
		return ErrInvalidDataSourceRead
	}
	for _, line := range observation.Lines {
		if !validRequiredUTCTime(line.Timestamp) || line.Timestamp.Before(request.Start) || line.Timestamp.After(request.End) ||
			!domain.ValidModelText(line.Series, 512, false) || line.Content.Len() > request.LimitBytes {
			return ErrInvalidDataSourceRead
		}
	}
	return nil
}

type LokiReader interface {
	QueryLoki(context.Context, LokiReadRequest) (LokiObservation, error)
}

type DataSourceToolDependencies struct {
	Prometheus  PrometheusReader
	Loki        LokiReader
	ScopeGuard  ScopeGuard
	PolicyGuard PolicyGenerationGuard
	Policy      ObservationDataPolicy
	EvidenceIDs EvidenceIDSource
	Text        LogTextProcessor
	Now         func() time.Time
}

func (dependencies DataSourceToolDependencies) validate() error {
	if dependencies.Prometheus == nil || dependencies.Loki == nil || dependencies.ScopeGuard == nil || dependencies.PolicyGuard == nil ||
		dependencies.Policy == nil || dependencies.EvidenceIDs == nil || dependencies.Text == nil || dependencies.Now == nil || !validRequiredUTCTime(dependencies.Now()) {
		return ErrInvalidDataSourceToolDependencies
	}
	return nil
}

type QueryPrometheusTool struct{ dependencies DataSourceToolDependencies }
type QueryLokiTool struct{ dependencies DataSourceToolDependencies }

var _ agent.Tool = (*QueryPrometheusTool)(nil)
var _ agent.Tool = (*QueryLokiTool)(nil)

func NewQueryPrometheusTool(dependencies DataSourceToolDependencies) (*QueryPrometheusTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidDataSourceToolDependencies
	}
	return &QueryPrometheusTool{dependencies: dependencies}, nil
}
func NewQueryLokiTool(dependencies DataSourceToolDependencies) (*QueryLokiTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidDataSourceToolDependencies
	}
	return &QueryLokiTool{dependencies: dependencies}, nil
}

type prometheusToolArguments struct {
	Namespace     string                      `json:"namespace"`
	PodName       string                      `json:"pod_name"`
	Purpose       string                      `json:"purpose"`
	QueryID       domain.ObservabilityQueryID `json:"query_id"`
	SeriesLimit   int                         `json:"series_limit"`
	StepSeconds   int                         `json:"step_seconds"`
	WindowSeconds int                         `json:"window_seconds"`
}

func decodePrometheusCall(call BoundToolCall, now time.Time) (PrometheusReadRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameQueryPrometheus {
		return PrometheusReadRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments prometheusToolArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return PrometheusReadRequest{}, ErrInvalidCanonicalArguments
	}
	if arguments.WindowSeconds < 60 || arguments.WindowSeconds > int(domain.MaxObservabilityWindow/time.Second) ||
		arguments.StepSeconds < 15 || arguments.StepSeconds > int(domain.MaxObservabilityStep/time.Second) ||
		arguments.SeriesLimit < 1 || arguments.SeriesLimit > domain.MaxObservabilitySeries {
		return PrometheusReadRequest{}, ErrInvalidCanonicalArguments
	}
	policy := call.SourcePolicy()
	window, step := time.Duration(arguments.WindowSeconds)*time.Second, time.Duration(arguments.StepSeconds)*time.Second
	request := PrometheusReadRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Policy: policy, Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: arguments.Namespace, Name: arguments.PodName},
		QueryID: arguments.QueryID, Start: now.Add(-window).UTC(), End: now, Step: step,
		MaxSeries: arguments.SeriesLimit, MaxSamples: call.Ceilings().MaxDataSourceSamples, LimitBytes: call.Ceilings().MaxDataSourceBytes}
	pointsPerSeries := arguments.WindowSeconds/arguments.StepSeconds + 1
	if request.Validate() != nil || window > call.Ceilings().MaxDataSourceWindow ||
		step > call.Ceilings().MaxDataSourceStep || arguments.SeriesLimit > call.Ceilings().MaxDataSourceSeries ||
		pointsPerSeries < 1 || request.MaxSeries > request.MaxSamples/pointsPerSeries {
		return PrometheusReadRequest{}, ErrInvalidCanonicalArguments
	}
	return request, nil
}

type lokiToolArguments struct {
	Contains      string                      `json:"contains"`
	LineLimit     int                         `json:"line_limit"`
	Namespace     string                      `json:"namespace"`
	PodName       string                      `json:"pod_name"`
	Purpose       string                      `json:"purpose"`
	QueryID       domain.ObservabilityQueryID `json:"query_id"`
	WindowSeconds int                         `json:"window_seconds"`
}

func decodeLokiCall(call BoundToolCall, now time.Time) (lokiToolArguments, LokiReadRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameQueryLoki {
		return lokiToolArguments{}, LokiReadRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments lokiToolArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return lokiToolArguments{}, LokiReadRequest{}, ErrInvalidCanonicalArguments
	}
	window := time.Duration(arguments.WindowSeconds) * time.Second
	maxLines := arguments.LineLimit
	pageLines := min(maxLines, 200)
	request := LokiReadRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Policy: call.SourcePolicy(), Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: arguments.Namespace, Name: arguments.PodName},
		QueryID: arguments.QueryID, Contains: arguments.Contains, Start: now.Add(-window).UTC(), End: now, MaxPages: call.Ceilings().MaxDataSourcePages, PageLines: pageLines, MaxLines: maxLines, LimitBytes: call.Ceilings().MaxDataSourceBytes}
	if request.Validate() != nil || window > call.Ceilings().MaxDataSourceWindow ||
		arguments.LineLimit > call.Ceilings().MaxDataSourceLines {
		return lokiToolArguments{}, LokiReadRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}

func authorizeObservation(ctx context.Context, call BoundToolCall, kind domain.DataSourceKind, query domain.ObservabilityQueryID, dependencies DataSourceToolDependencies) domain.SafeErrorClass {
	request := ObservationPolicyRequest{RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Kind: kind, OriginHash: call.SourcePolicy().OriginHash, QueryID: query}
	if !request.valid() {
		return domain.SafeErrorClassInternal
	}
	switch dependencies.Policy.AuthorizeObservation(ctx, request) {
	case ObservationPolicyAllowed:
		return ""
	case ObservationPolicyConsentRequired:
		return domain.SafeErrorClassConsentRequired
	case ObservationPolicyPermissionRequired:
		return domain.SafeErrorClassPolicyDenied
	case ObservationPolicyDenied:
		return domain.SafeErrorClassPolicyDenied
	default:
		return domain.SafeErrorClassInternal
	}
}

func dataSourcePreflight(ctx context.Context, call BoundToolCall, kind domain.DataSourceKind, query domain.ObservabilityQueryID, dependencies DataSourceToolDependencies) domain.SafeErrorClass {
	if ctx == nil {
		return domain.SafeErrorClassInvalidInput
	}
	if ctx.Err() != nil {
		return classifyFailure(ctx, ctx.Err())
	}
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) || !dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return domain.SafeErrorClassStaleScope
	}
	return authorizeObservation(ctx, call, kind, query, dependencies)
}

func (tool *QueryPrometheusTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	started := tool.dependencies.Now()
	request, err := decodePrometheusCall(call, started)
	if err != nil {
		return failedResult(call, started, domain.SafeErrorClassPolicyDenied)
	}
	if class := dataSourcePreflight(ctx, call, domain.DataSourcePrometheus, request.QueryID, tool.dependencies); class != "" {
		return failedResult(call, started, class)
	}
	observation, readErr := tool.dependencies.Prometheus.QueryPrometheus(ctx, request)
	completed := tool.dependencies.Now()
	if !validRequiredUTCTime(completed) || completed.Before(started) {
		return failedResult(call, started, domain.SafeErrorClassInternal)
	}
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) || !tool.dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, completed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, completed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, completed, classifyFailure(ctx, readErr))
	}
	if class := dataSourcePostflight(ctx, call, domain.DataSourcePrometheus, request.QueryID, tool.dependencies); class != "" {
		return failedResult(call, completed, class)
	}
	if observation.Validate(request) != nil || observation.End.After(completed) {
		return failedResult(call, completed, domain.SafeErrorClassInvalidExternalResponse)
	}
	return buildPrometheusResult(call, completed, observation, tool.dependencies)
}

type safePrometheusSample struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}
type safePrometheusSeries struct {
	Samples []safePrometheusSample `json:"samples"`
	Series  string                 `json:"series"`
}
type safePrometheusData struct {
	End             string                      `json:"end"`
	InstructionLike bool                        `json:"instruction_like"`
	Partial         bool                        `json:"partial"`
	QueryID         domain.ObservabilityQueryID `json:"query_id"`
	RedactionCount  int                         `json:"redaction_count"`
	Reference       safeResourceReference       `json:"resource"`
	Series          []safePrometheusSeries      `json:"series"`
	SourceTrust     string                      `json:"source_trust"`
	Start           string                      `json:"start"`
	Truncated       bool                        `json:"truncated"`
}

func buildPrometheusResult(call BoundToolCall, observed time.Time, observation PrometheusObservation, dependencies DataSourceToolDependencies) ToolResult {
	resource, referenceMetadata, err := safeEventReference(security.NewRedactor(), observation.Reference)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	data := safePrometheusData{Start: observation.Start.Format(time.RFC3339Nano), End: observation.End.Format(time.RFC3339Nano), QueryID: observation.QueryID,
		Reference: resource, Series: make([]safePrometheusSeries, 0, len(observation.Series)), SourceTrust: security.UntrustedDataClass,
		Partial: observation.Partial, Truncated: observation.Truncated || referenceMetadata.truncated || referenceMetadata.blocked,
		RedactionCount: referenceMetadata.redactions, InstructionLike: referenceMetadata.instructionLike}
	templates := make([]evidenceTemplate, 0, len(observation.Series))
	warnings := []domain.ToolResultWarning{}
	for _, series := range observation.Series {
		processed, processErr := dependencies.Text.ProcessLines(series.Series, 512)
		if errors.Is(processErr, security.ErrSensitiveOutputBlocked) {
			data.Partial, data.Truncated = true, true
			warnings = appendWarning(warnings, "source_sensitive_content_blocked", "One source series identity was hidden because it may contain sensitive data.")
			continue
		}
		if processErr != nil || processed.Value == "" {
			return failedResult(call, observed, domain.SafeErrorClassInternal)
		}
		projected := safePrometheusSeries{Series: processed.Value, Samples: make([]safePrometheusSample, len(series.Samples))}
		for sampleIndex, sample := range series.Samples {
			projected.Samples[sampleIndex] = safePrometheusSample{Timestamp: sample.Timestamp.Format(time.RFC3339Nano), Value: sample.Value}
		}
		data.Series = append(data.Series, projected)
		data.RedactionCount += processed.RedactionCount
		data.InstructionLike = data.InstructionLike || processed.InstructionLike
		data.Partial = data.Partial || processed.Truncated
		data.Truncated = data.Truncated || processed.Truncated
		fact, cut := boundedEvidenceFact(fmt.Sprintf("Prometheus query template %s returned %d bounded sample(s) for Pod %s series %s over the requested window.", observation.QueryID, len(series.Samples), observation.Reference.Name, processed.Value))
		from, through := observation.Start, observation.End
		templates = append(templates, evidenceTemplate{category: domain.EvidenceCategoryPrometheus, resource: observation.Reference, policyVersion: domain.ObservabilityPolicyVersion,
			fact: fact, sourcePath: "api/v1/query_range#" + string(observation.QueryID), sourceOriginHash: call.SourcePolicy().OriginHash, series: processed.Value,
			observedFrom: &from, observedThrough: &through, severity: stableSeverity(domain.EvidenceSeverityInfo), redactionCount: processed.RedactionCount,
			truncated: cut || data.Truncated, partial: data.Partial})
	}
	return fitDataSourceResult(call, observed, &data, templates, dependencies.EvidenceIDs, data.Partial || data.Truncated, "source_partial", len(data.Series), warnings)
}

func (tool *QueryLokiTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	started := tool.dependencies.Now()
	arguments, request, err := decodeLokiCall(call, started)
	if err != nil {
		return failedResult(call, started, domain.SafeErrorClassPolicyDenied)
	}
	if class := dataSourcePreflight(ctx, call, domain.DataSourceLoki, request.QueryID, tool.dependencies); class != "" {
		return failedResult(call, started, class)
	}
	observation, readErr := tool.dependencies.Loki.QueryLoki(ctx, request)
	completed := tool.dependencies.Now()
	if !validRequiredUTCTime(completed) || completed.Before(started) {
		return failedResult(call, started, domain.SafeErrorClassInternal)
	}
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) || !tool.dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, completed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, completed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, completed, classifyFailure(ctx, readErr))
	}
	if class := dataSourcePostflight(ctx, call, domain.DataSourceLoki, request.QueryID, tool.dependencies); class != "" {
		return failedResult(call, completed, class)
	}
	if observation.Validate(request) != nil || observation.End.After(completed) {
		return failedResult(call, completed, domain.SafeErrorClassInvalidExternalResponse)
	}
	return buildLokiResult(call, completed, arguments, observation, tool.dependencies)
}

func dataSourcePostflight(ctx context.Context, call BoundToolCall, kind domain.DataSourceKind, query domain.ObservabilityQueryID, dependencies DataSourceToolDependencies) domain.SafeErrorClass {
	if class := authorizeObservation(ctx, call, kind, query, dependencies); class != "" {
		return class
	}
	if ctx.Err() != nil {
		return classifyFailure(ctx, ctx.Err())
	}
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) || !dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return domain.SafeErrorClassStaleScope
	}
	return ""
}

type safeLokiLine struct {
	Content   string `json:"content"`
	Series    string `json:"series"`
	Timestamp string `json:"timestamp"`
}
type safeLokiData struct {
	End             string                      `json:"end"`
	InstructionLike bool                        `json:"instruction_like"`
	Lines           []safeLokiLine              `json:"lines"`
	Pages           int                         `json:"pages"`
	QueryID         domain.ObservabilityQueryID `json:"query_id"`
	Reference       safeResourceReference       `json:"resource"`
	RedactionCount  int                         `json:"redaction_count"`
	SourceTrust     string                      `json:"source_trust"`
	Start           string                      `json:"start"`
	Truncated       bool                        `json:"truncated"`
}

func buildLokiResult(call BoundToolCall, observed time.Time, arguments lokiToolArguments, observation LokiObservation, dependencies DataSourceToolDependencies) ToolResult {
	resource, _, err := safeEventReference(logTextAdapter{dependencies.Text}, observation.Reference)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	data := safeLokiData{Start: observation.Start.Format(time.RFC3339Nano), End: observation.End.Format(time.RFC3339Nano), QueryID: observation.QueryID,
		Reference: resource, Lines: []safeLokiLine{}, Pages: observation.Pages, SourceTrust: security.UntrustedDataClass, Truncated: observation.Truncated || observation.Partial}
	redactions, blocked := 0, false
	seriesSet := make(map[string]struct{})
	for _, line := range observation.Lines {
		projectedSeries, seriesErr := dependencies.Text.ProcessLines(line.Series, 512)
		if errors.Is(seriesErr, security.ErrSensitiveOutputBlocked) {
			blocked = true
			data.Truncated = true
			continue
		}
		if seriesErr != nil || projectedSeries.Value == "" {
			return failedResult(call, observed, domain.SafeErrorClassInternal)
		}
		processed, processErr := dependencies.Text.ProcessLines(string(line.Content.bytes()), call.Ceilings().MaxDataSourceBytes)
		if errors.Is(processErr, security.ErrSensitiveOutputBlocked) {
			blocked = true
			data.Truncated = true
			continue
		}
		if processErr != nil {
			return failedResult(call, observed, domain.SafeErrorClassInternal)
		}
		redactions += projectedSeries.RedactionCount + processed.RedactionCount
		data.InstructionLike = data.InstructionLike || projectedSeries.InstructionLike || processed.InstructionLike
		content := processed.Value
		if arguments.Contains != "" && !strings.Contains(content, arguments.Contains) {
			continue
		}
		if content == "" {
			continue
		}
		data.Lines = append(data.Lines, safeLokiLine{Timestamp: line.Timestamp.Format(time.RFC3339Nano), Series: projectedSeries.Value, Content: content})
		seriesSet[projectedSeries.Value] = struct{}{}
		data.Truncated = data.Truncated || projectedSeries.Truncated || processed.Truncated
	}
	data.RedactionCount = redactions
	series := make([]string, 0, len(seriesSet))
	for value := range seriesSet {
		series = append(series, value)
	}
	sort.Strings(series)
	templates := make([]evidenceTemplate, 0, len(series))
	for _, value := range series {
		count := 0
		fingerprintInput := strings.Builder{}
		for _, line := range data.Lines {
			if line.Series == value {
				count++
				fingerprintInput.WriteString(line.Timestamp)
				fingerprintInput.WriteByte('\n')
				fingerprintInput.WriteString(line.Content)
				fingerprintInput.WriteByte('\n')
			}
		}
		fact, cut := boundedEvidenceFact(fmt.Sprintf("Loki query template %s returned %d sanitized line(s) for Pod %s series %s; fingerprint %s.", observation.QueryID, count, observation.Reference.Name, value, domain.SHA256Hex(fingerprintInput.String())))
		from, through := observation.Start, observation.End
		templates = append(templates, evidenceTemplate{category: domain.EvidenceCategoryLoki, resource: observation.Reference, policyVersion: domain.ObservabilityPolicyVersion,
			fact: fact, sourcePath: "loki/api/v1/query_range#" + string(observation.QueryID), sourceOriginHash: call.SourcePolicy().OriginHash, series: value,
			observedFrom: &from, observedThrough: &through, severity: stableSeverity(domain.EvidenceSeverityInfo), redactionCount: redactions,
			truncated: cut || data.Truncated || blocked, partial: observation.Partial || blocked})
	}
	return fitDataSourceResult(call, observed, &data, templates, dependencies.EvidenceIDs, data.Truncated || blocked, "source_partial", len(data.Lines), nil)
}

func fitDataSourceResult(
	call BoundToolCall,
	observed time.Time,
	data any,
	templates []evidenceTemplate,
	ids EvidenceIDSource,
	partial bool,
	reason string,
	returned int,
	warnings []domain.ToolResultWarning,
) ToolResult {
	guard, guardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if guardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	outputTrimmed := false
	for attempts := 0; attempts < 32; attempts++ {
		currentTemplates, currentReturned := dataSourceResultPlan(data, templates, observed, returned)
		currentPartial, currentReason := partial || outputTrimmed, reason
		if len(currentTemplates) > call.Ceilings().MaxEvidenceItems {
			currentTemplates = currentTemplates[:call.Ceilings().MaxEvidenceItems]
			currentPartial, currentReason = true, evidenceLimitReason
		}
		if outputTrimmed {
			currentReason = outputLimitReason
		}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = guard.CanonicalizeJSONObject(raw)
		}
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The source result was reduced deterministically because the fixed Tool output limit was reached.")
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(), ObservedAt: observed,
				Status: domain.ToolResultStatusSuccess, DataJSON: encoded, Evidence: previewEvidence(call, observed, currentTemplates), Warnings: currentWarnings,
				Truncation: domain.ToolResultTruncation{ReturnedCount: currentReturned},
			}
			if currentPartial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Truncated = true
				result.Truncation.Reason = currentReason
				for index := range result.Evidence {
					result.Evidence[index].Partial = true
					result.Evidence[index].Truncated = true
				}
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && guard.Allows(measured.Truncation.ReturnedBytes) {
				evidence, materializeErr := materializeEvidence(ResourceToolDependencies{EvidenceIDs: ids}, call, observed, currentTemplates)
				if materializeErr != nil {
					return failedResult(call, observed, domain.SafeErrorClassInternal)
				}
				if currentPartial {
					for index := range evidence {
						evidence[index].Partial = true
						evidence[index].Truncated = true
					}
				}
				measured.Evidence = evidence
				return finalizePlannedResult(call, observed, measured)
			}
		}
		if !trimDataSourceProjection(data) {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
		}
		outputTrimmed = true
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
}

func dataSourceResultPlan(data any, templates []evidenceTemplate, observed time.Time, fallback int) ([]evidenceTemplate, int) {
	switch projected := data.(type) {
	case *safePrometheusData:
		limit := min(len(templates), len(projected.Series))
		return templates[:limit], len(projected.Series)
	case *safeLokiData:
		return templates, len(projected.Lines)
	case *safeAllPodLogsData:
		result := make([]evidenceTemplate, 0, len(projected.Containers))
		for _, container := range projected.Containers {
			result = append(result, podLogEvidence(container, observed)...)
		}
		return result, len(projected.Containers)
	default:
		return templates, fallback
	}
}

func trimDataSourceProjection(data any) bool {
	switch projected := data.(type) {
	case *safePrometheusData:
		if projected == nil || len(projected.Series) == 0 {
			return false
		}
		changed := false
		for index := range projected.Series {
			samples := projected.Series[index].Samples
			if len(samples) > 1 {
				projected.Series[index].Samples = samples[:(len(samples)+1)/2]
				changed = true
			}
		}
		if !changed {
			projected.Series = projected.Series[:len(projected.Series)-1]
		}
		projected.Partial, projected.Truncated = true, true
		return true
	case *safeLokiData:
		if projected == nil || len(projected.Lines) == 0 {
			return false
		}
		projected.Lines = projected.Lines[:len(projected.Lines)/2]
		projected.Truncated = true
		return true
	case *safeAllPodLogsData:
		if projected == nil || len(projected.Containers) == 0 {
			return false
		}
		changed := false
		for index := range projected.Containers {
			container := &projected.Containers[index]
			lines := logLines(container.Content)
			if len(lines) <= 1 {
				continue
			}
			lines = lines[(len(lines)+1)/2:]
			container.Content = strings.Join(lines, "\n")
			container.LineCount = len(lines)
			container.ByteCount = len(container.Content)
			container.ContentFingerprint = domain.SHA256Hex(container.Content)
			container.Truncated = true
			changed = true
		}
		if !changed {
			projected.Containers = projected.Containers[:len(projected.Containers)-1]
		}
		projected.ContainerCount = len(projected.Containers)
		projected.Truncated = true
		return true
	default:
		return false
	}
}
