package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

// GetResourceTool returns one fixed diagnostic projection and deterministic
// same-run Evidence. It never serializes a Kubernetes object.
type GetResourceTool struct {
	dependencies ResourceToolDependencies
}

var _ agent.Tool = (*GetResourceTool)(nil)

// NewGetResourceTool validates the immutable dependencies for get_resource.
func NewGetResourceTool(dependencies ResourceToolDependencies) (*GetResourceTool, error) {
	if dependencies.validateQuery() != nil {
		return nil, ErrInvalidResourceToolDependencies
	}
	return &GetResourceTool{dependencies: dependencies}, nil
}

// Execute applies scope gates around one narrow reader action, then projects,
// sanitizes, limits, and derives Evidence in that order.
func (tool *GetResourceTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validateQuery() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(tool.dependencies, call)
	_, request, err := decodeGetResourceCall(call)
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
	if !tool.dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	queryResult, readErr := tool.dependencies.QueryReader.QueryResources(ctx, request)
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if !tool.dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, observed, classifyFailure(ctx, readErr))
	}
	if queryResult.Validate(request) != nil || len(queryResult.Items) != 1 {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	observation := queryResult.Items[0]
	if observation.Summary.Reference.Name != request.Query.Name || observation.Summary.Reference.Namespace != request.Query.Namespace ||
		observation.Summary.EffectiveType() != request.Policy.Type {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	data, templates, warnings, limited, err := tool.project(observation, request.Policy, request.Detail)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	reason := ""
	if queryResult.Page.Partial {
		reason = queryResult.Page.Reason
	} else if limited {
		reason = fieldLimitReason
	}
	templates, evidenceLimited := limitEvidenceTemplates(call, templates)
	if evidenceLimited {
		reason = evidenceLimitReason
	}
	planned := fitGetResourceResult(call, observed, data, previewEvidence(call, observed, templates), warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		return planned
	}
	if len(planned.Evidence) > len(templates) {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	evidence, err := materializeEvidence(tool.dependencies, call, observed, templates[:len(planned.Evidence)])
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if planned.Truncation.Truncated {
		for index := range evidence {
			evidence[index].Truncated = true
			evidence[index].Partial = true
		}
	}
	planned.Evidence = evidence
	return finalizePlannedResult(call, observed, planned)
}

type safeResourceReference struct {
	APIVersion      string `json:"api_version"`
	Generation      *int64 `json:"generation,omitempty"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resource_version,omitempty"`
	UID             string `json:"uid,omitempty"`
}

type safeLabel struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type safeCondition struct {
	LastTransitionTime string `json:"last_transition_time,omitempty"`
	Message            string `json:"message,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Status             string `json:"status"`
	Type               string `json:"type"`

	redactionCount int
	truncated      bool
}

type safeContainerState struct {
	ExitCode   *int32 `json:"exit_code,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Message    string `json:"message,omitempty"`
	Reason     string `json:"reason,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	State      string `json:"state,omitempty"`

	redactionCount int
	truncated      bool
}

type safeContainer struct {
	Current      safeContainerState `json:"current"`
	Image        string             `json:"image,omitempty"`
	Init         bool               `json:"init"`
	Last         safeContainerState `json:"last"`
	Name         string             `json:"name"`
	Ready        bool               `json:"ready"`
	RestartCount int32              `json:"restart_count"`

	redactionCount int
	truncated      bool
}

type safeOwner struct {
	APIVersion    string `json:"api_version"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	ReferenceOnly bool   `json:"reference_only"`
	UID           string `json:"uid,omitempty"`
}

type safeServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	Protocol   string `json:"protocol"`
	TargetPort string `json:"target_port,omitempty"`

	redactionCount int
	truncated      bool
}

type safeResourceStatus struct {
	Active                *int32 `json:"active,omitempty"`
	ActiveDeadlineSeconds *int64 `json:"active_deadline_seconds,omitempty"`
	Available             *int32 `json:"available,omitempty"`
	BackoffLimit          *int32 `json:"backoff_limit,omitempty"`
	Current               *int32 `json:"current,omitempty"`
	Desired               *int32 `json:"desired,omitempty"`
	Failed                *int32 `json:"failed,omitempty"`
	NodeName              string `json:"node_name,omitempty"`
	ObservedGeneration    *int64 `json:"observed_generation,omitempty"`
	Parallelism           *int32 `json:"parallelism,omitempty"`
	Phase                 string `json:"phase,omitempty"`
	Ready                 *int32 `json:"ready,omitempty"`
	Reason                string `json:"reason,omitempty"`
	Revision              string `json:"revision,omitempty"`
	SchedulingConstraints *int32 `json:"scheduling_constraint_count,omitempty"`
	SelectorKeyCount      *int32 `json:"selector_key_count,omitempty"`
	ServiceType           string `json:"service_type,omitempty"`
	Succeeded             *int32 `json:"succeeded,omitempty"`
	Unavailable           *int32 `json:"unavailable,omitempty"`
	Updated               *int32 `json:"updated,omitempty"`

	redactionCount int
	truncated      bool
}

type safeResourceField struct {
	Class  domain.ResourceDataClass  `json:"class"`
	Field  string                    `json:"field"`
	Path   string                    `json:"source_path"`
	Scalar domain.ResourceScalarType `json:"scalar"`
	Value  *string                   `json:"value,omitempty"`
}

type getResourceData struct {
	Conditions      []safeCondition       `json:"conditions"`
	Containers      []safeContainer       `json:"containers"`
	CreatedAt       string                `json:"created_at,omitempty"`
	Detail          ResourceDetail        `json:"detail"`
	Fields          []safeResourceField   `json:"fields"`
	InstructionLike bool                  `json:"instruction_like"`
	LabelCount      int                   `json:"label_count"`
	Labels          []safeLabel           `json:"labels"`
	Owners          []safeOwner           `json:"owners"`
	RedactionCount  int                   `json:"redaction_count"`
	Resource        safeResourceReference `json:"resource"`
	ResourceType    string                `json:"resource_type"`
	APIResource     string                `json:"api_resource"`
	ResourceScope   domain.ResourceScope  `json:"resource_scope"`
	ServicePorts    []safeServicePort     `json:"service_ports"`
	SourceTrust     string                `json:"source_trust"`
	Status          safeResourceStatus    `json:"status"`
	Truncated       bool                  `json:"truncated"`
}

type textMetadata struct {
	redactions      int
	truncated       bool
	instructionLike bool
	blocked         bool
}

func (tool *GetResourceTool) project(
	observation ResourceObservation,
	policy domain.ResourcePolicy,
	detail ResourceDetail,
) (getResourceData, []evidenceTemplate, []domain.ToolResultWarning, bool, error) {
	data := getResourceData{
		Conditions:    []safeCondition{},
		Containers:    []safeContainer{},
		Detail:        detail,
		Fields:        []safeResourceField{},
		LabelCount:    observation.LabelCount,
		Labels:        []safeLabel{},
		Owners:        []safeOwner{},
		ServicePorts:  []safeServicePort{},
		SourceTrust:   security.UntrustedDataClass,
		Truncated:     observation.Truncated,
		ResourceType:  observation.Summary.EffectiveType().ID,
		APIResource:   observation.Summary.EffectiveType().Resource,
		ResourceScope: observation.Summary.EffectiveType().Scope,
	}
	if !observation.Summary.CreatedAt.IsZero() {
		data.CreatedAt = observation.Summary.CreatedAt.Format(time.RFC3339Nano)
	}
	warnings := []domain.ToolResultWarning{}
	metadata := textMetadata{}
	resource, currentMetadata, err := tool.safeResourceReference(observation)
	if err != nil {
		return getResourceData{}, nil, nil, false, err
	}
	data.Resource = resource
	metadata.merge(currentMetadata)
	if currentMetadata.blocked {
		warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
	}
	status, currentMetadata, err := tool.safeStatus(observation)
	if err != nil {
		return getResourceData{}, nil, nil, false, err
	}
	data.Status = status
	metadata.merge(currentMetadata)
	fields, currentMetadata, err := tool.safeFields(observation.Fields)
	if err != nil {
		return getResourceData{}, nil, nil, false, err
	}
	data.Fields = fields
	metadata.merge(currentMetadata)
	if currentMetadata.blocked {
		warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
	}
	for _, label := range observation.Labels {
		value, current, processErr := tool.safeText(label.Value, maxIdentityTextBytes)
		if processErr != nil {
			return getResourceData{}, nil, nil, false, processErr
		}
		metadata.merge(current)
		if current.blocked {
			warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
			continue
		}
		data.Labels = append(data.Labels, safeLabel{Key: label.Key, Value: value})
	}
	for _, owner := range observation.Summary.Owners {
		projected, current, processErr := tool.safeOwner(owner)
		if processErr != nil {
			return getResourceData{}, nil, nil, false, processErr
		}
		metadata.merge(current)
		if current.blocked {
			warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
		}
		data.Owners = append(data.Owners, projected)
	}
	if detail == ResourceDetailDiagnostic {
		for _, condition := range observation.Conditions {
			projected, current, processErr := tool.safeCondition(condition)
			if processErr != nil {
				return getResourceData{}, nil, nil, false, processErr
			}
			metadata.merge(current)
			if current.blocked {
				warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
			}
			data.Conditions = append(data.Conditions, projected)
		}
		for _, container := range observation.Containers {
			projected, current, processErr := tool.safeContainer(container)
			if processErr != nil {
				return getResourceData{}, nil, nil, false, processErr
			}
			metadata.merge(current)
			if current.blocked {
				warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
			}
			data.Containers = append(data.Containers, projected)
		}
		for _, port := range observation.ServicePorts {
			projected, current, processErr := tool.safeServicePort(port)
			if processErr != nil {
				return getResourceData{}, nil, nil, false, processErr
			}
			metadata.merge(current)
			if current.blocked {
				warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
			}
			data.ServicePorts = append(data.ServicePorts, projected)
		}
	}
	sortSafeResourceDetails(&data)
	data.InstructionLike = metadata.instructionLike
	data.RedactionCount = metadata.redactions
	data.Truncated = data.Truncated || metadata.truncated || metadata.blocked
	templates := getResourceEvidence(data, domainReference(data.Resource))
	for index := range templates {
		templates[index].resourceType = observation.Summary.EffectiveType()
	}
	for _, field := range data.Fields {
		policyField, admitted := policy.Field(field.Field)
		if field.Value == nil || !admitted || !policyField.Evidence {
			continue
		}
		fact, truncated := boundedEvidenceFact(fmt.Sprintf("The projected %s %s field %s is %s.", data.Resource.Kind, data.Resource.Name, field.Field, *field.Value))
		templates = append(templates, evidenceTemplate{
			category: domain.EvidenceCategoryResourceStatus, resourceType: observation.Summary.EffectiveType(),
			resource: domainReference(data.Resource), fact: fact, sourcePath: field.Path,
			severity: stableSeverity(domain.EvidenceSeverityInfo), truncated: truncated,
		})
	}
	return data, templates, warnings, data.Truncated || evidenceTemplatesTruncated(templates), nil
}

func sortSafeResourceDetails(data *getResourceData) {
	sort.Slice(data.Labels, func(left, right int) bool {
		return data.Labels[left].Key < data.Labels[right].Key
	})
	sort.SliceStable(data.Conditions, func(left, right int) bool {
		leftKey := conditionSortKey(data.Conditions[left])
		rightKey := conditionSortKey(data.Conditions[right])
		return leftKey < rightKey
	})
	sort.SliceStable(data.Containers, func(left, right int) bool {
		return containerSortKey(data.Containers[left]) < containerSortKey(data.Containers[right])
	})
	sort.SliceStable(data.ServicePorts, func(left, right int) bool {
		leftKey := fmt.Sprintf("%s\x00%05d\x00%s\x00%s", data.ServicePorts[left].Name, data.ServicePorts[left].Port, data.ServicePorts[left].Protocol, data.ServicePorts[left].TargetPort)
		rightKey := fmt.Sprintf("%s\x00%05d\x00%s\x00%s", data.ServicePorts[right].Name, data.ServicePorts[right].Port, data.ServicePorts[right].Protocol, data.ServicePorts[right].TargetPort)
		return leftKey < rightKey
	})
}

func conditionSortKey(value safeCondition) string {
	return strings.Join([]string{value.Type, value.Status, value.Reason, value.Message, value.LastTransitionTime}, "\x00")
}

func containerSortKey(value safeContainer) string {
	encoded, _ := json.Marshal(value)
	if value.Init {
		return "0\x00" + string(encoded)
	}
	return "1\x00" + string(encoded)
}

func (metadata *textMetadata) merge(other textMetadata) {
	metadata.redactions += other.redactions
	metadata.truncated = metadata.truncated || other.truncated
	metadata.instructionLike = metadata.instructionLike || other.instructionLike
	metadata.blocked = metadata.blocked || other.blocked
}

func (tool *GetResourceTool) safeText(value ExternalText, maximumBytes int) (string, textMetadata, error) {
	processed, err := tool.dependencies.Text.Process(value.Value, maximumBytes)
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

func (tool *GetResourceTool) safeFields(fields []ResourceFieldObservation) ([]safeResourceField, textMetadata, error) {
	result := make([]safeResourceField, 0, len(fields))
	metadata := textMetadata{}
	for _, field := range fields {
		projected := safeResourceField{Class: field.DataClass, Field: field.Field, Path: field.Path, Scalar: field.Scalar}
		if field.Present {
			value, current, err := tool.safeText(field.Value, maxProjectedTextBytes)
			if err != nil {
				return nil, textMetadata{}, err
			}
			metadata.merge(current)
			if current.blocked {
				continue
			}
			projected.Value = &value
		}
		result = append(result, projected)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Field < result[right].Field })
	return result, metadata, nil
}

func (tool *GetResourceTool) safeResourceReference(observation ResourceObservation) (safeResourceReference, textMetadata, error) {
	reference := observation.Summary.Reference
	result := safeResourceReference{
		APIVersion: reference.APIVersion,
		Generation: int64Pointer(observation.Generation),
		Kind:       reference.Kind,
		Name:       reference.Name,
		Namespace:  reference.Namespace,
	}
	metadata := textMetadata{}
	fields := []struct {
		source string
		target *string
	}{
		{source: reference.ResourceVersion, target: &result.ResourceVersion},
		{source: reference.UID, target: &result.UID},
	}
	for _, field := range fields {
		processed, current, err := tool.safeText(ExternalText{Value: field.source}, maxIdentityTextBytes)
		if err != nil {
			return safeResourceReference{}, textMetadata{}, err
		}
		metadata.merge(current)
		if current.redactions > 0 || current.truncated || current.blocked {
			metadata.truncated = true
			continue
		}
		*field.target = processed
	}
	return result, metadata, nil
}

func (tool *GetResourceTool) safeOwner(owner domain.ResourceOwner) (safeOwner, textMetadata, error) {
	result := safeOwner{
		APIVersion:    owner.APIVersion,
		Kind:          owner.Kind,
		Name:          owner.Name,
		ReferenceOnly: owner.ReferenceOnly,
	}
	uid, metadata, err := tool.safeText(ExternalText{Value: owner.UID}, maxIdentityTextBytes)
	if err != nil {
		return safeOwner{}, textMetadata{}, err
	}
	if metadata.redactions > 0 || metadata.truncated || metadata.blocked {
		metadata.truncated = true
		return result, metadata, nil
	}
	result.UID = uid
	return result, metadata, nil
}

func (tool *GetResourceTool) safeStatus(observation ResourceObservation) (safeResourceStatus, textMetadata, error) {
	status := safeResourceStatus{
		Active:                countPointer(observation.Summary.Status.Active),
		ActiveDeadlineSeconds: int64Pointer(observation.Status.ActiveDeadlineSeconds),
		Available:             countPointer(observation.Summary.Status.Available),
		BackoffLimit:          countPointer(observation.Status.BackoffLimit),
		Current:               countPointer(observation.Status.Current),
		Desired:               countPointer(observation.Summary.Status.Desired),
		Failed:                countPointer(observation.Summary.Status.Failed),
		ObservedGeneration:    int64Pointer(observation.Status.ObservedGeneration),
		Parallelism:           countPointer(observation.Status.Parallelism),
		Ready:                 countPointer(observation.Summary.Status.Ready),
		SchedulingConstraints: countPointer(observation.Status.SchedulingConstraints),
		SelectorKeyCount:      countPointer(observation.Status.SelectorKeyCount),
		Succeeded:             countPointer(observation.Summary.Status.Succeeded),
		Unavailable:           countPointer(observation.Status.Unavailable),
		Updated:               countPointer(observation.Status.Updated),
	}
	metadata := textMetadata{}
	fields := []struct {
		source ExternalText
		limit  int
		target *string
	}{
		{source: ExternalText{Value: observation.Summary.Status.Phase}, limit: maxIdentityTextBytes, target: &status.Phase},
		{source: ExternalText{Value: observation.Summary.Status.Reason}, limit: maxIdentityTextBytes, target: &status.Reason},
		{source: ExternalText{Value: observation.Summary.Status.ServiceType}, limit: maxIdentityTextBytes, target: &status.ServiceType},
		{source: observation.Status.NodeName, limit: maxIdentityTextBytes, target: &status.NodeName},
		{source: observation.Status.Revision, limit: maxIdentityTextBytes, target: &status.Revision},
	}
	for _, field := range fields {
		value, current, err := tool.safeText(field.source, field.limit)
		if err != nil {
			return safeResourceStatus{}, textMetadata{}, err
		}
		*field.target = value
		metadata.merge(current)
	}
	status.redactionCount = metadata.redactions
	status.truncated = metadata.truncated || metadata.blocked
	return status, metadata, nil
}

func (tool *GetResourceTool) safeCondition(value ConditionObservation) (safeCondition, textMetadata, error) {
	result := safeCondition{}
	metadata := textMetadata{}
	fields := []struct {
		source ExternalText
		limit  int
		target *string
	}{
		{source: value.Type, limit: maxIdentityTextBytes, target: &result.Type},
		{source: value.Status, limit: maxIdentityTextBytes, target: &result.Status},
		{source: value.Reason, limit: maxIdentityTextBytes, target: &result.Reason},
		{source: value.Message, limit: maxConditionTextBytes, target: &result.Message},
	}
	for _, field := range fields {
		processed, current, err := tool.safeText(field.source, field.limit)
		if err != nil {
			return safeCondition{}, textMetadata{}, err
		}
		*field.target = processed
		metadata.merge(current)
	}
	if !value.LastTransitionTime.IsZero() {
		result.LastTransitionTime = value.LastTransitionTime.Format(time.RFC3339Nano)
	}
	result.redactionCount = metadata.redactions
	result.truncated = metadata.truncated || metadata.blocked
	return result, metadata, nil
}

func (tool *GetResourceTool) safeContainer(value ContainerObservation) (safeContainer, textMetadata, error) {
	result := safeContainer{Init: value.Init, Ready: value.Ready, RestartCount: value.RestartCount}
	metadata := textMetadata{}
	name, current, err := tool.safeText(value.Name, maxIdentityTextBytes)
	if err != nil {
		return safeContainer{}, textMetadata{}, err
	}
	result.Name = nonEmpty(name, "projected-container")
	metadata.merge(current)
	image, current, err := tool.safeText(value.Image, maxImageTextBytes)
	if err != nil {
		return safeContainer{}, textMetadata{}, err
	}
	result.Image = image
	metadata.merge(current)
	result.Current, current, err = tool.safeContainerState(value.Current)
	if err != nil {
		return safeContainer{}, textMetadata{}, err
	}
	metadata.merge(current)
	result.Last, current, err = tool.safeContainerState(value.Last)
	if err != nil {
		return safeContainer{}, textMetadata{}, err
	}
	metadata.merge(current)
	result.redactionCount = metadata.redactions
	result.truncated = metadata.truncated || metadata.blocked
	return result, metadata, nil
}

func (tool *GetResourceTool) safeContainerState(value ContainerStateObservation) (safeContainerState, textMetadata, error) {
	result := safeContainerState{ExitCode: countPointer(value.ExitCode)}
	metadata := textMetadata{}
	fields := []struct {
		source ExternalText
		target *string
	}{
		{source: value.State, target: &result.State},
		{source: value.Reason, target: &result.Reason},
		{source: value.Message, target: &result.Message},
	}
	for _, field := range fields {
		processed, current, err := tool.safeText(field.source, maxContainerTextBytes)
		if err != nil {
			return safeContainerState{}, textMetadata{}, err
		}
		*field.target = processed
		metadata.merge(current)
	}
	if !value.StartedAt.IsZero() {
		result.StartedAt = value.StartedAt.Format(time.RFC3339Nano)
	}
	if !value.FinishedAt.IsZero() {
		result.FinishedAt = value.FinishedAt.Format(time.RFC3339Nano)
	}
	result.redactionCount = metadata.redactions
	result.truncated = metadata.truncated || metadata.blocked
	return result, metadata, nil
}

func (tool *GetResourceTool) safeServicePort(value ServicePortObservation) (safeServicePort, textMetadata, error) {
	result := safeServicePort{Port: value.Port}
	metadata := textMetadata{}
	fields := []struct {
		source ExternalText
		target *string
	}{
		{source: value.Name, target: &result.Name},
		{source: value.Protocol, target: &result.Protocol},
		{source: value.TargetPort, target: &result.TargetPort},
	}
	for _, field := range fields {
		processed, current, err := tool.safeText(field.source, maxIdentityTextBytes)
		if err != nil {
			return safeServicePort{}, textMetadata{}, err
		}
		*field.target = processed
		metadata.merge(current)
	}
	result.Protocol = nonEmpty(result.Protocol, "TCP")
	result.redactionCount = metadata.redactions
	result.truncated = metadata.truncated || metadata.blocked
	return result, metadata, nil
}

func getResourceEvidence(data getResourceData, reference domain.ResourceRef) []evidenceTemplate {
	statusFact, statusFactTruncated := boundedEvidenceFact(resourceStatusFact(data.Resource, data.Status))
	templates := []evidenceTemplate{
		{
			category:       domain.EvidenceCategoryResourceStatus,
			resource:       reference,
			fact:           statusFact,
			sourcePath:     "projected.status",
			severity:       resourceStatusSeverity(reference.Kind, data.Status),
			redactionCount: data.Status.redactionCount,
			truncated:      data.Status.truncated || statusFactTruncated,
		},
	}
	for _, condition := range data.Conditions {
		fact := fmt.Sprintf("Condition %s is %s", nonEmpty(condition.Type, "Unknown"), nonEmpty(condition.Status, "Unknown"))
		if condition.Reason != "" {
			fact += " with reason " + condition.Reason
		}
		if condition.Message != "" {
			fact += ": " + condition.Message
		}
		fact += "."
		fact, factTruncated := boundedEvidenceFact(fact)
		templates = append(templates, evidenceTemplate{
			category:       domain.EvidenceCategoryCondition,
			resource:       reference,
			fact:           fact,
			sourcePath:     "projected.status.conditions",
			severity:       conditionSeverity(condition),
			redactionCount: condition.redactionCount,
			truncated:      condition.truncated || factTruncated,
		})
	}
	for _, container := range data.Containers {
		state := nonEmpty(container.Current.State, "unknown")
		fact := fmt.Sprintf("Container %s is %s, ready=%t, restarts=%d", container.Name, state, container.Ready, container.RestartCount)
		if container.Current.Reason != "" {
			fact += ", reason " + container.Current.Reason
		}
		if container.Current.ExitCode != nil {
			fact += fmt.Sprintf(", exit_code=%d", *container.Current.ExitCode)
		}
		if container.Image != "" {
			fact += ", image " + container.Image
		}
		if container.Last.State != "" || container.Last.Reason != "" || container.Last.ExitCode != nil {
			fact += "; previous state " + nonEmpty(container.Last.State, "unknown")
			if container.Last.Reason != "" {
				fact += ", reason " + container.Last.Reason
			}
			if container.Last.ExitCode != nil {
				fact += fmt.Sprintf(", exit_code=%d", *container.Last.ExitCode)
			}
		}
		fact += "."
		fact, factTruncated := boundedEvidenceFact(fact)
		templates = append(templates, evidenceTemplate{
			category:       domain.EvidenceCategoryContainerState,
			resource:       reference,
			fact:           fact,
			sourcePath:     "projected.status.container_statuses",
			severity:       containerSeverity(container),
			redactionCount: container.redactionCount,
			truncated:      container.truncated || factTruncated,
		})
	}
	if len(data.ServicePorts) > 0 {
		fact, redactionCount, truncated := servicePortsFact(reference, data.ServicePorts)
		fact, factTruncated := boundedEvidenceFact(fact)
		templates = append(templates, evidenceTemplate{
			category:       domain.EvidenceCategoryResourceStatus,
			resource:       reference,
			fact:           fact,
			sourcePath:     "projected.spec.ports",
			severity:       stableSeverity(domain.EvidenceSeverityInfo),
			redactionCount: redactionCount,
			truncated:      truncated || factTruncated,
		})
	}
	for _, owner := range data.Owners {
		fact := fmt.Sprintf("%s %s is controlled by %s %s.", reference.Kind, reference.Name, owner.Kind, owner.Name)
		if owner.ReferenceOnly {
			fact = fmt.Sprintf("%s %s carries a reference-only %s owner %s.", reference.Kind, reference.Name, owner.Kind, owner.Name)
		}
		fact, factTruncated := boundedEvidenceFact(fact)
		templates = append(templates, evidenceTemplate{
			category:   domain.EvidenceCategoryOwner,
			resource:   reference,
			fact:       fact,
			sourcePath: "projected.metadata.controller_owner",
			severity:   stableSeverity(domain.EvidenceSeverityInfo),
			truncated:  factTruncated,
		})
	}
	return templates
}

func fitGetResourceResult(
	call BoundToolCall,
	observed time.Time,
	data getResourceData,
	evidence []domain.Evidence,
	warnings []domain.ToolResultWarning,
	reason string,
) ToolResult {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, completeGuardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if completeGuardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	outputTrimmed := false
	for attempts := 0; attempts < 512; attempts++ {
		partial := reason != "" || outputTrimmed
		data.Truncated = data.Truncated || partial
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The cluster read returned a deterministic subset because the fixed output limit was reached.")
		}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID: call.InvocationID(),
				Name:         call.Name(),
				Version:      call.Version(),
				Scope:        call.Scope().Snapshot(),
				ObservedAt:   observed,
				Status:       domain.ToolResultStatusSuccess,
				DataJSON:     encoded,
				Evidence:     append([]domain.Evidence(nil), evidence...),
				Warnings:     currentWarnings,
				Truncation: domain.ToolResultTruncation{
					Truncated:     partial,
					ReturnedCount: 1,
				},
			}
			if partial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Reason = reason
				if outputTrimmed {
					result.Truncation.Reason = outputLimitReason
				}
				for index := range result.Evidence {
					result.Evidence[index].Truncated = true
					result.Evidence[index].Partial = true
				}
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return measured
			}
		}
		outputTrimmed = true
		if reduceGetResourceOutput(&data, &evidence) {
			continue
		}
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
}

func reduceGetResourceOutput(data *getResourceData, evidence *[]domain.Evidence) bool {
	for index := len(data.Conditions) - 1; index >= 0; index-- {
		if data.Conditions[index].Message != "" {
			data.Conditions[index].Message = ""
			return true
		}
	}
	for index := len(data.Containers) - 1; index >= 0; index-- {
		container := &data.Containers[index]
		switch {
		case container.Current.Message != "":
			container.Current.Message = ""
			return true
		case container.Last.Message != "":
			container.Last.Message = ""
			return true
		case container.Image != "":
			container.Image = ""
			return true
		}
	}
	if len(*evidence) > 0 {
		*evidence = (*evidence)[:len(*evidence)-1]
		return true
	}
	if len(data.Labels) > 0 {
		data.Labels = data.Labels[:len(data.Labels)-1]
		return true
	}
	if len(data.ServicePorts) > 0 {
		data.ServicePorts = data.ServicePorts[:len(data.ServicePorts)-1]
		return true
	}
	if len(data.Containers) > 0 {
		data.Containers = data.Containers[:len(data.Containers)-1]
		return true
	}
	if len(data.Conditions) > 0 {
		data.Conditions = data.Conditions[:len(data.Conditions)-1]
		return true
	}
	return false
}

func sameRequestedResource(request, result domain.ResourceRef) bool {
	return request.APIVersion == result.APIVersion && request.Kind == result.Kind && request.Namespace == result.Namespace && request.Name == result.Name
}

func domainReference(reference safeResourceReference) domain.ResourceRef {
	return domain.ResourceRef{
		APIVersion:      reference.APIVersion,
		Kind:            reference.Kind,
		Name:            reference.Name,
		Namespace:       reference.Namespace,
		ResourceVersion: reference.ResourceVersion,
		UID:             reference.UID,
	}
}

func countPointer(value domain.OptionalCount) *int32 {
	if !value.Present {
		return nil
	}
	copied := value.Value
	return &copied
}

func int64Pointer(value OptionalInt64) *int64 {
	if !value.Present {
		return nil
	}
	copied := value.Value
	return &copied
}

func resourceStatusFact(reference safeResourceReference, status safeResourceStatus) string {
	parts := []string{fmt.Sprintf("%s %s was observed", reference.Kind, reference.Name)}
	if reference.Generation != nil {
		parts = append(parts, fmt.Sprintf("generation %d", *reference.Generation))
	}
	if status.Phase != "" {
		parts = append(parts, "phase "+status.Phase)
	}
	if status.Ready != nil && status.Desired != nil {
		parts = append(parts, fmt.Sprintf("ready %d of %d", *status.Ready, *status.Desired))
	} else {
		parts = appendOptionalCount(parts, "ready", status.Ready)
	}
	if status.Available != nil && status.Desired != nil {
		parts = append(parts, fmt.Sprintf("available %d of %d", *status.Available, *status.Desired))
	} else {
		parts = appendOptionalCount(parts, "available", status.Available)
	}
	if status.Desired != nil && status.Ready == nil && status.Available == nil {
		parts = appendOptionalCount(parts, "desired", status.Desired)
	}
	parts = appendOptionalCount(parts, "current", status.Current)
	parts = appendOptionalCount(parts, "updated", status.Updated)
	parts = appendOptionalCount(parts, "unavailable", status.Unavailable)
	parts = appendOptionalCount(parts, "active", status.Active)
	parts = appendOptionalCount(parts, "succeeded", status.Succeeded)
	if status.Failed != nil {
		parts = append(parts, fmt.Sprintf("failed %d", *status.Failed))
	}
	parts = appendOptionalCount(parts, "parallelism", status.Parallelism)
	parts = appendOptionalCount(parts, "backoff limit", status.BackoffLimit)
	parts = appendOptionalCount(parts, "selector keys", status.SelectorKeyCount)
	parts = appendOptionalCount(parts, "scheduling constraints", status.SchedulingConstraints)
	if status.ActiveDeadlineSeconds != nil {
		parts = append(parts, fmt.Sprintf("active deadline %d seconds", *status.ActiveDeadlineSeconds))
	}
	if status.ObservedGeneration != nil {
		parts = append(parts, fmt.Sprintf("observed generation %d", *status.ObservedGeneration))
	}
	if status.ServiceType != "" {
		parts = append(parts, "service type "+status.ServiceType)
	}
	if status.NodeName != "" {
		parts = append(parts, "node "+status.NodeName)
	}
	if status.Revision != "" {
		parts = append(parts, "revision "+status.Revision)
	}
	if status.Reason != "" {
		parts = append(parts, "reason "+status.Reason)
	}
	return strings.Join(parts, "; ") + "."
}

func appendOptionalCount(parts []string, label string, value *int32) []string {
	if value == nil {
		return parts
	}
	return append(parts, fmt.Sprintf("%s %d", label, *value))
}

func servicePortsFact(reference domain.ResourceRef, ports []safeServicePort) (string, int, bool) {
	parts := make([]string, 0, len(ports))
	redactionCount := 0
	truncated := false
	for _, port := range ports {
		value := fmt.Sprintf("%s port %d", port.Protocol, port.Port)
		if port.Name != "" {
			value += " (" + port.Name + ")"
		}
		if port.TargetPort != "" {
			value += " targeting " + port.TargetPort
		}
		parts = append(parts, value)
		redactionCount += port.redactionCount
		truncated = truncated || port.truncated
	}
	fact := fmt.Sprintf("Service %s exposes %s.", reference.Name, strings.Join(parts, "; "))
	return fact, redactionCount, truncated
}

func resourceStatusSeverity(kind string, status safeResourceStatus) *domain.EvidenceSeverity {
	if status.Reason != "" || status.Failed != nil && *status.Failed > 0 ||
		status.Ready != nil && status.Desired != nil && *status.Ready < *status.Desired ||
		status.Available != nil && status.Desired != nil && *status.Available < *status.Desired {
		return stableSeverity(domain.EvidenceSeverityWarning)
	}
	if kind == string(domain.ResourceKindPod) && status.Phase != "" && status.Phase != "Running" && status.Phase != "Succeeded" ||
		kind == string(domain.ResourceKindJob) && status.Phase == "Failed" {
		return stableSeverity(domain.EvidenceSeverityWarning)
	}
	return stableSeverity(domain.EvidenceSeverityInfo)
}

func conditionSeverity(condition safeCondition) *domain.EvidenceSeverity {
	if strings.EqualFold(condition.Status, "false") || strings.Contains(strings.ToLower(condition.Reason), "fail") ||
		strings.Contains(strings.ToLower(condition.Reason), "backoff") {
		return stableSeverity(domain.EvidenceSeverityWarning)
	}
	return stableSeverity(domain.EvidenceSeverityInfo)
}

func containerSeverity(container safeContainer) *domain.EvidenceSeverity {
	reason := strings.ToLower(container.Current.Reason + " " + container.Last.Reason)
	if strings.Contains(reason, "oomkilled") || strings.Contains(reason, "crashloopbackoff") {
		return stableSeverity(domain.EvidenceSeverityCritical)
	}
	if !container.Ready || container.RestartCount > 0 || container.Current.Reason != "" {
		return stableSeverity(domain.EvidenceSeverityWarning)
	}
	return stableSeverity(domain.EvidenceSeverityInfo)
}
