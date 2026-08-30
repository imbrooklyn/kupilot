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

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	maxEventMessageBytes      = 2048
	maxEventIdentityBytes     = 256
	eventUIDDegradedReason    = "uid_filter_degraded"
	eventSensitiveWarningCode = "event_sensitive_field_blocked"
)

var (
	// ErrInvalidEventToolDependencies reports an incomplete fixed get_events
	// dependency set.
	ErrInvalidEventToolDependencies = errors.New("get_events Tool dependencies are invalid")
	// ErrInvalidEventRead reports an invalid narrow Event request or source
	// projection without exposing Event content.
	ErrInvalidEventRead = errors.New("Event Tool read data is invalid")
)

// EventReadRequest is one exact policy-admitted Event observation. The
// target, cutoff, and limit are runtime-derived and contain no raw selector.
type EventReadRequest struct {
	Scope     domain.ClusterScope
	Reference domain.ResourceRef
	NotBefore time.Time
	Limit     int
}

// Validate rejects generic, cross-Namespace, and expanding Event reads before
// an adapter action.
func (request EventReadRequest) Validate() error {
	kind, allowed := domain.ResourceKindForReference(request.Reference)
	if request.Scope.Validate() != nil || !allowed || !kind.Valid() ||
		domain.ValidateLiveResourceRef(request.Reference) != nil || !request.Scope.AllowsReference(request.Reference) ||
		request.NotBefore.IsZero() || request.NotBefore.Location() != time.UTC || request.NotBefore.UnixMilli() < 0 ||
		request.Limit < 1 || request.Limit > 50 {
		return ErrInvalidEventRead
	}
	return nil
}

// EventObservation is the source-allowlisted adapter DTO for one related
// Kubernetes Event. It is never serialized directly as model data.
type EventObservation struct {
	Type                ExternalText
	Reason              ExternalText
	Message             ExternalText
	Involved            domain.ResourceRef
	ReportingSource     ExternalText
	ReportingController ExternalText
	FirstObservedAt     time.Time
	LastObservedAt      time.Time
	Count               int32
	Truncated           bool
}

// EventObservationList is one bounded adapter result before Tool-local
// redaction, deterministic aggregation, and output limiting.
type EventObservationList struct {
	Target            domain.ResourceRef
	Items             []EventObservation
	UIDFilterDegraded bool
	Truncated         bool
}

// Validate checks the complete source projection against the exact request.
func (list EventObservationList) Validate(request EventReadRequest) error {
	if request.Validate() != nil || domain.ValidateLiveResourceRef(list.Target) != nil ||
		!sameEventTarget(request.Reference, list.Target) || len(list.Items) > 50 {
		return ErrInvalidEventRead
	}
	for _, item := range list.Items {
		if item.Count < 1 || domain.ValidateLiveResourceRef(item.Involved) != nil ||
			!sameEventTarget(list.Target, item.Involved) ||
			!item.Type.valid(maxProjectedTextBytes) || !item.Reason.valid(maxProjectedTextBytes) ||
			!item.Message.valid(maxProjectedTextBytes) || !item.ReportingSource.valid(maxProjectedTextBytes) ||
			!item.ReportingController.valid(maxProjectedTextBytes) ||
			!validRequiredUTCTime(item.FirstObservedAt) || !validRequiredUTCTime(item.LastObservedAt) ||
			item.LastObservedAt.Before(item.FirstObservedAt) {
			return ErrInvalidEventRead
		}
	}
	return nil
}

func validRequiredUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0
}

func sameEventTarget(request, result domain.ResourceRef) bool {
	if request.APIVersion != result.APIVersion || request.Kind != result.Kind ||
		request.Namespace != result.Namespace || request.Name != result.Name {
		return false
	}
	return request.UID == "" || request.UID == result.UID
}

// EventReader is the Tool-owned one-operation Kubernetes Event port. It
// exposes no client, GVR, selector, pagination token, or write method.
type EventReader interface {
	ReadEvents(context.Context, EventReadRequest) (EventObservationList, error)
}

// EventToolDependencies are immutable stateless dependencies for get_events.
type EventToolDependencies struct {
	Reader      EventReader
	ScopeGuard  ScopeGuard
	EvidenceIDs EvidenceIDSource
	Text        TextProcessor
	Now         func() time.Time
}

func (dependencies EventToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.ScopeGuard == nil || dependencies.EvidenceIDs == nil ||
		dependencies.Text == nil || dependencies.Now == nil {
		return ErrInvalidEventToolDependencies
	}
	now := dependencies.Now()
	if !validRequiredUTCTime(now) {
		return ErrInvalidEventToolDependencies
	}
	return nil
}

// GetEventsTool returns bounded, sanitized recent Events and deterministic
// same-run Evidence for one fixed resource target.
type GetEventsTool struct {
	dependencies EventToolDependencies
}

var _ agent.Tool = (*GetEventsTool)(nil)

// NewGetEventsTool validates the immutable dependencies for get_events.
func NewGetEventsTool(dependencies EventToolDependencies) (*GetEventsTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidEventToolDependencies
	}
	return &GetEventsTool{dependencies: dependencies}, nil
}

// Execute applies deny, projection, redaction, limits, and Evidence creation in
// that order around one narrow Event read.
func (tool *GetEventsTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(ResourceToolDependencies{Now: tool.dependencies.Now}, call)
	arguments, request, err := decodeGetEventsCall(call, observed)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	if ctx == nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidInput)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	observations, readErr := tool.dependencies.Reader.ReadEvents(ctx, request)
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, observed, classifyFailure(ctx, readErr))
	}
	if observations.Validate(request) != nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	data, templates, warnings, reason, projectErr := tool.project(observations, arguments, request)
	if projectErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	templates, evidenceLimited := limitEvidenceTemplates(call, templates)
	if evidenceLimited {
		reason = evidenceLimitReason
		if len(data.Items) > len(templates) {
			data.Items = data.Items[:len(templates)]
			data.ReturnedCount = len(data.Items)
		}
	}
	planned := fitEventResult(call, observed, data, previewEvidence(call, observed, templates), warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		return planned
	}
	if len(planned.Evidence) > len(templates) {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	evidence, err := materializeEvidence(
		ResourceToolDependencies{EvidenceIDs: tool.dependencies.EvidenceIDs},
		call,
		observed,
		templates[:len(planned.Evidence)],
	)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if planned.Truncation.Truncated {
		for index := range evidence {
			evidence[index].Truncated = true
		}
	}
	planned.Evidence = evidence
	return finalizePlannedResult(call, observed, planned)
}

type eventResourceArgument struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	UID        string `json:"uid,omitempty"`
}

type getEventsArguments struct {
	Limit        int                   `json:"limit"`
	Purpose      string                `json:"purpose"`
	Resource     eventResourceArgument `json:"resource"`
	SinceSeconds int                   `json:"since_seconds"`
}

func decodeGetEventsCall(call BoundToolCall, observed time.Time) (getEventsArguments, EventReadRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameGetEvents || call.Version() != agent.ToolCatalogVersion {
		return getEventsArguments{}, EventReadRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments getEventsArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		arguments.Limit < 1 || arguments.Limit > 50 || arguments.SinceSeconds < 60 || arguments.SinceSeconds > 86400 {
		return getEventsArguments{}, EventReadRequest{}, ErrInvalidCanonicalArguments
	}
	reference := domain.ResourceRef{
		APIVersion: arguments.Resource.APIVersion,
		Kind:       arguments.Resource.Kind,
		Namespace:  arguments.Resource.Namespace,
		Name:       arguments.Resource.Name,
		UID:        arguments.Resource.UID,
	}
	notBefore := observed.Add(-time.Duration(arguments.SinceSeconds) * time.Second).UTC()
	if notBefore.UnixMilli() < 0 {
		notBefore = time.UnixMilli(0).UTC()
	}
	request := EventReadRequest{
		Scope:     call.Scope(),
		Reference: reference,
		NotBefore: notBefore,
		Limit:     min(arguments.Limit, call.Ceilings().MaxEventItems, 50),
	}
	if request.Validate() != nil {
		return getEventsArguments{}, EventReadRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}

type safeEventItem struct {
	Count               int32  `json:"count"`
	FirstObservedAt     string `json:"first_observed_at"`
	LastObservedAt      string `json:"last_observed_at"`
	Message             string `json:"message,omitempty"`
	Reason              string `json:"reason,omitempty"`
	ReportingController string `json:"reporting_controller,omitempty"`
	ReportingSource     string `json:"reporting_source,omitempty"`
	Type                string `json:"type"`

	redactionCount int
	truncated      bool
	firstObserved  time.Time
	lastObserved   time.Time
}

type getEventsData struct {
	InstructionLike   bool                  `json:"instruction_like"`
	Items             []safeEventItem       `json:"items"`
	NotBefore         string                `json:"not_before"`
	RedactionCount    int                   `json:"redaction_count"`
	Resource          safeResourceReference `json:"resource"`
	ReturnedCount     int                   `json:"returned_count"`
	SinceSeconds      int                   `json:"since_seconds"`
	SourceTrust       string                `json:"source_trust"`
	Truncated         bool                  `json:"truncated"`
	UIDFilterDegraded bool                  `json:"uid_filter_degraded"`
}

func (tool *GetEventsTool) project(
	observations EventObservationList,
	arguments getEventsArguments,
	request EventReadRequest,
) (getEventsData, []evidenceTemplate, []domain.ToolResultWarning, string, error) {
	resource, resourceMetadata, err := safeEventReference(tool.dependencies.Text, observations.Target)
	if err != nil {
		return getEventsData{}, nil, nil, "", err
	}
	data := getEventsData{
		Items:             []safeEventItem{},
		NotBefore:         request.NotBefore.Format(time.RFC3339Nano),
		Resource:          resource,
		SinceSeconds:      arguments.SinceSeconds,
		SourceTrust:       security.UntrustedDataClass,
		UIDFilterDegraded: observations.UIDFilterDegraded,
	}
	metadata := resourceMetadata
	warnings := []domain.ToolResultWarning{}
	if resourceMetadata.blocked {
		warnings = appendWarning(warnings, eventSensitiveWarningCode, "One Kubernetes Event identity field was hidden because it may contain sensitive data.")
	}
	aggregated := make(map[string]safeEventItem, len(observations.Items))
	for _, observation := range observations.Items {
		if observation.LastObservedAt.Before(request.NotBefore) {
			continue
		}
		item, current, projectErr := tool.safeEvent(observation)
		if projectErr != nil {
			return getEventsData{}, nil, nil, "", projectErr
		}
		metadata.merge(current)
		if current.blocked {
			warnings = appendWarning(warnings, eventSensitiveWarningCode, "One Kubernetes Event field was hidden because it may contain sensitive data.")
		}
		key := strings.Join([]string{item.Type, item.Reason, item.Message, item.ReportingSource, item.ReportingController}, "\x00")
		if existing, exists := aggregated[key]; exists {
			total := int64(existing.Count) + int64(item.Count)
			if total > math.MaxInt32 {
				total = math.MaxInt32
				existing.truncated = true
			}
			existing.Count = int32(total)
			if item.firstObserved.Before(existing.firstObserved) {
				existing.firstObserved = item.firstObserved
				existing.FirstObservedAt = item.FirstObservedAt
			}
			if item.lastObserved.After(existing.lastObserved) {
				existing.lastObserved = item.lastObserved
				existing.LastObservedAt = item.LastObservedAt
			}
			existing.redactionCount += item.redactionCount
			existing.truncated = existing.truncated || item.truncated
			aggregated[key] = existing
			continue
		}
		aggregated[key] = item
	}
	for _, item := range aggregated {
		data.Items = append(data.Items, item)
	}
	sort.Slice(data.Items, func(left, right int) bool {
		if !data.Items[left].lastObserved.Equal(data.Items[right].lastObserved) {
			return data.Items[left].lastObserved.After(data.Items[right].lastObserved)
		}
		if !data.Items[left].firstObserved.Equal(data.Items[right].firstObserved) {
			return data.Items[left].firstObserved.After(data.Items[right].firstObserved)
		}
		return eventSortKey(data.Items[left]) < eventSortKey(data.Items[right])
	})
	reason := ""
	if len(data.Items) > request.Limit {
		data.Items = data.Items[:request.Limit]
		reason = itemLimitReason
	}
	if observations.Truncated {
		reason = itemLimitReason
	}
	if observations.UIDFilterDegraded {
		warnings = appendWarning(warnings, eventUIDDegradedReason, "The Event read could not apply an exact target UID filter.")
		if reason == "" {
			reason = eventUIDDegradedReason
		}
	}
	data.ReturnedCount = len(data.Items)
	data.RedactionCount = metadata.redactions
	data.InstructionLike = metadata.instructionLike
	data.Truncated = observations.Truncated || metadata.truncated || metadata.blocked || reason != ""
	if reason == "" && data.Truncated {
		reason = fieldLimitReason
	}
	reference := domainReference(data.Resource)
	templates := make([]evidenceTemplate, 0, len(data.Items))
	for _, item := range data.Items {
		fact := fmt.Sprintf("%s Event %s for %s %s was observed %d time(s), most recently at %s",
			nonEmpty(item.Type, "Unknown"), nonEmpty(item.Reason, "Unknown"), reference.Kind, reference.Name, item.Count, item.LastObservedAt)
		if item.Message != "" {
			fact += ": " + item.Message
		}
		fact += "."
		fact, factTruncated := boundedEvidenceFact(fact)
		severity := domain.EvidenceSeverityInfo
		if strings.EqualFold(item.Type, "Warning") {
			severity = domain.EvidenceSeverityWarning
		}
		templates = append(templates, evidenceTemplate{
			category:       domain.EvidenceCategoryEvent,
			resource:       reference,
			fact:           fact,
			sourcePath:     "projected.events",
			severity:       stableSeverity(severity),
			redactionCount: item.redactionCount,
			truncated:      item.truncated || factTruncated,
		})
	}
	return data, templates, warnings, reason, nil
}

func (tool *GetEventsTool) safeEvent(observation EventObservation) (safeEventItem, textMetadata, error) {
	result := safeEventItem{
		Count:           observation.Count,
		FirstObservedAt: observation.FirstObservedAt.Format(time.RFC3339Nano),
		LastObservedAt:  observation.LastObservedAt.Format(time.RFC3339Nano),
		truncated:       observation.Truncated,
		firstObserved:   observation.FirstObservedAt,
		lastObserved:    observation.LastObservedAt,
	}
	metadata := textMetadata{truncated: observation.Truncated}
	fields := []struct {
		source ExternalText
		limit  int
		target *string
	}{
		{source: observation.Type, limit: maxEventIdentityBytes, target: &result.Type},
		{source: observation.Reason, limit: maxEventIdentityBytes, target: &result.Reason},
		{source: observation.Message, limit: maxEventMessageBytes, target: &result.Message},
		{source: observation.ReportingSource, limit: maxEventIdentityBytes, target: &result.ReportingSource},
		{source: observation.ReportingController, limit: maxEventIdentityBytes, target: &result.ReportingController},
	}
	for _, field := range fields {
		value, current, err := processExternalText(tool.dependencies.Text, field.source, field.limit)
		if err != nil {
			return safeEventItem{}, textMetadata{}, err
		}
		*field.target = value
		metadata.merge(current)
	}
	result.Type = nonEmpty(result.Type, "Unknown")
	result.redactionCount = metadata.redactions
	result.truncated = metadata.truncated || metadata.blocked
	return result, metadata, nil
}

func processExternalText(processor TextProcessor, value ExternalText, maximumBytes int) (string, textMetadata, error) {
	processed, err := processor.Process(value.Value, maximumBytes)
	if errors.Is(err, security.ErrSensitiveOutputBlocked) {
		return "", textMetadata{truncated: true, blocked: true}, nil
	}
	if err != nil {
		return "", textMetadata{}, err
	}
	return processed.Value, textMetadata{
		redactions:      processed.RedactionCount,
		truncated:       value.Truncated || processed.Truncated,
		instructionLike: processed.InstructionLike,
	}, nil
}

func safeEventReference(processor TextProcessor, reference domain.ResourceRef) (safeResourceReference, textMetadata, error) {
	result := safeResourceReference{
		APIVersion: reference.APIVersion,
		Kind:       reference.Kind,
		Name:       reference.Name,
		Namespace:  reference.Namespace,
	}
	metadata := textMetadata{}
	fields := []struct {
		source string
		target *string
	}{
		{source: reference.UID, target: &result.UID},
		{source: reference.ResourceVersion, target: &result.ResourceVersion},
	}
	for _, field := range fields {
		value, current, err := processExternalText(processor, ExternalText{Value: field.source}, maxEventIdentityBytes)
		if err != nil {
			return safeResourceReference{}, textMetadata{}, err
		}
		metadata.merge(current)
		if current.redactions > 0 || current.truncated || current.blocked {
			metadata.truncated = true
			continue
		}
		*field.target = value
	}
	return result, metadata, nil
}

func eventSortKey(item safeEventItem) string {
	return strings.Join([]string{item.Type, item.Reason, item.Message, item.ReportingSource, item.ReportingController, item.FirstObservedAt}, "\x00")
}

func fitEventResult(
	call BoundToolCall,
	observed time.Time,
	data getEventsData,
	evidence []domain.Evidence,
	warnings []domain.ToolResultWarning,
	reason string,
) ToolResult {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, guardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if guardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	outputTrimmed := false
	for attempts := 0; attempts <= 50; attempts++ {
		partial := reason != "" || outputTrimmed
		data.Truncated = data.Truncated || partial
		data.ReturnedCount = len(data.Items)
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The cluster read returned a deterministic Event subset because the fixed output limit was reached.")
		}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(),
				ObservedAt: observed, Status: domain.ToolResultStatusSuccess, DataJSON: encoded,
				Evidence: append([]domain.Evidence(nil), evidence...), Warnings: currentWarnings,
				Truncation: domain.ToolResultTruncation{Truncated: partial, ReturnedCount: len(data.Items)},
			}
			if partial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Reason = reason
				if outputTrimmed {
					result.Truncation.Reason = outputLimitReason
				}
				for index := range result.Evidence {
					result.Evidence[index].Truncated = true
				}
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return measured
			}
		}
		outputTrimmed = true
		if len(data.Items) == 0 {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
		}
		data.Items = data.Items[:len(data.Items)-1]
		if len(evidence) > len(data.Items) {
			evidence = evidence[:len(data.Items)]
		}
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
}
