// Package tools implements KuPilot's fixed read-only diagnostic Tool handlers
// over consumer-owned, scope-bound Kubernetes read ports.
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	maxProjectedLabels        = 8
	maxProjectedConditions    = 20
	maxProjectedContainers    = 50
	maxProjectedServicePorts  = 20
	maxProjectedTextBytes     = 8 * 1024
	maxConditionTextBytes     = 1024
	maxContainerTextBytes     = 1024
	maxEvidenceFactBytes      = 2048
	maxIdentityTextBytes      = 256
	maxImageTextBytes         = 2048
	maxResultWarningCount     = 50
	fieldLimitReason          = "field_limit"
	itemLimitReason           = "item_limit"
	evidenceLimitReason       = "evidence_limit"
	outputLimitReason         = "output_limit"
	sensitiveFieldWarningCode = "sensitive_field_blocked"
)

var (
	// ErrInvalidResourceToolDependencies reports an incomplete fixed handler
	// dependency set.
	ErrInvalidResourceToolDependencies = errors.New("resource Tool dependencies are invalid")
	// ErrInvalidResourceRead reports an invalid narrow reader request or
	// projected response without exposing source content.
	ErrInvalidResourceRead = errors.New("resource Tool read data is invalid")
	// ErrInvalidCanonicalArguments reports arguments that no longer match the
	// runtime-bound canonical Tool call.
	ErrInvalidCanonicalArguments = errors.New("canonical resource Tool arguments are invalid")
	// ErrInvalidEvidenceSource reports an invalid or duplicate generated
	// Evidence identifier.
	ErrInvalidEvidenceSource = errors.New("Evidence identifier source returned invalid data")
	// ErrToolOutputLimit reports a complete result that cannot fit the injected
	// result ceiling without deterministic truncation.
	ErrToolOutputLimit = errors.New("resource Tool output exceeds its fixed limit")
	// ErrInvalidRelatedRead reports an invalid fixed relationship request or
	// source projection without exposing Kubernetes object content.
	ErrInvalidRelatedRead = errors.New("related-resource Tool read data is invalid")
)

// BoundToolCall is the S13/S14 runtime-owned immutable call. This alias keeps
// one authority contract while making handler signatures concise.
type BoundToolCall = agent.BoundToolCall

// ToolResult is the shared safe ephemeral envelope defined by Domain.
type ToolResult = domain.ToolResult

// Evidence is the shared same-run observation contract defined by Domain.
type Evidence = domain.Evidence

// ResourceDetail is the fixed projection depth admitted by get_resource.
type ResourceDetail string

const (
	ResourceDetailSummary    ResourceDetail = "summary"
	ResourceDetailDiagnostic ResourceDetail = "diagnostic"
)

func (detail ResourceDetail) valid() bool {
	return detail == ResourceDetailSummary || detail == ResourceDetailDiagnostic
}

// ResourceReadRequest is one exact current-Namespace read. Scope and ceilings
// are runtime-derived rather than model fields.
type ResourceReadRequest struct {
	Scope     domain.ClusterScope
	Reference domain.ResourceRef
	Detail    ResourceDetail
}

// Validate checks one fixed direct target before any reader action.
func (request ResourceReadRequest) Validate() error {
	kind, allowed := domain.ResourceKindForReference(request.Reference)
	if request.Scope.Validate() != nil || !allowed || !kind.Valid() || !request.Detail.valid() ||
		domain.ValidateLiveResourceRef(request.Reference) != nil || request.Reference.Namespace != request.Scope.Namespace {
		return ErrInvalidResourceRead
	}
	return nil
}

// ResourceListRequest is one bounded selector-free namespaced list.
type ResourceListRequest struct {
	Scope domain.ClusterScope
	Kind  domain.ResourceKind
	Limit int
}

// Validate rejects generic, all-Namespace, and expanding list requests.
func (request ResourceListRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.Kind.Valid() || request.Limit < 1 || request.Limit > domain.MaxResourceSummaries {
		return ErrInvalidResourceRead
	}
	return nil
}

// ResourceReader is the Tool-owned two-operation Kubernetes read port. It
// exposes no client, selector, GVR, pagination token, or write method.
type ResourceReader interface {
	ReadResource(context.Context, ResourceReadRequest) (ResourceObservation, error)
	ListResources(context.Context, ResourceListRequest) (ResourceObservationList, error)
}

// ScopeGuard checks the complete immutable scope before and after each reader
// call. Application may implement this port without crossing into Tools.
type ScopeGuard interface {
	Current(context.Context, domain.ClusterScope) bool
}

// EvidenceIDSource supplies Application-generated UUIDv7 Evidence identifiers.
type EvidenceIDSource interface {
	NewEvidenceID() (domain.EvidenceID, error)
}

// TextProcessor is the narrow consumer-owned safety port for projected
// Kubernetes text.
type TextProcessor interface {
	Process(string, int) (security.TextResult, error)
}

// ResourceToolDependencies are immutable stateless dependencies shared by the
// two resource handlers. Context is never stored here.
type ResourceToolDependencies struct {
	Reader      ResourceReader
	ScopeGuard  ScopeGuard
	EvidenceIDs EvidenceIDSource
	Text        TextProcessor
	Now         func() time.Time
}

func (dependencies ResourceToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.ScopeGuard == nil || dependencies.EvidenceIDs == nil ||
		dependencies.Text == nil || dependencies.Now == nil {
		return ErrInvalidResourceToolDependencies
	}
	now := dependencies.Now()
	if now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return ErrInvalidResourceToolDependencies
	}
	return nil
}

// ExternalText is bounded projected source text that remains untrusted until
// the Tool-local safety pipeline processes it.
type ExternalText struct {
	Value     string
	Truncated bool
}

func (value ExternalText) valid(maximumBytes int) bool {
	return len(value.Value) <= maximumBytes && utf8.ValidString(value.Value)
}

// LabelObservation contains only one code-allowlisted metadata label.
type LabelObservation struct {
	Key   string
	Value ExternalText
}

// ConditionObservation is one source-allowlisted condition projection.
type ConditionObservation struct {
	Type               ExternalText
	Status             ExternalText
	Reason             ExternalText
	Message            ExternalText
	LastTransitionTime time.Time
}

// ContainerStateObservation excludes environment, volume, credential, and raw
// object fields.
type ContainerStateObservation struct {
	State      ExternalText
	Reason     ExternalText
	Message    ExternalText
	ExitCode   domain.OptionalCount
	StartedAt  time.Time
	FinishedAt time.Time
}

// ContainerObservation is one bounded current or init-container status.
type ContainerObservation struct {
	Name         ExternalText
	Image        ExternalText
	Ready        bool
	RestartCount int32
	Init         bool
	Current      ContainerStateObservation
	Last         ContainerStateObservation
}

// OptionalInt64 distinguishes an absent policy value from zero.
type OptionalInt64 struct {
	Value   int64
	Present bool
}

func (value OptionalInt64) valid() bool {
	return value.Present && value.Value >= 0 || !value.Present && value.Value == 0
}

// ResourceDiagnosticStatus contains only the additional fixed fields not
// represented by the S10 cross-Kind summary.
type ResourceDiagnosticStatus struct {
	Current               domain.OptionalCount
	Updated               domain.OptionalCount
	Unavailable           domain.OptionalCount
	Parallelism           domain.OptionalCount
	BackoffLimit          domain.OptionalCount
	SelectorKeyCount      domain.OptionalCount
	SchedulingConstraints domain.OptionalCount
	ObservedGeneration    OptionalInt64
	ActiveDeadlineSeconds OptionalInt64
	NodeName              ExternalText
	Revision              ExternalText
}

// ServicePortObservation excludes addresses, session data, and arbitrary spec.
type ServicePortObservation struct {
	Name       ExternalText
	Protocol   ExternalText
	Port       int32
	TargetPort ExternalText
}

// ResourceObservation is the reader DTO. It is not the model DTO and cannot be
// serialized directly as a Tool result.
type ResourceObservation struct {
	Summary      domain.ResourceSummary
	Generation   OptionalInt64
	LabelCount   int
	Labels       []LabelObservation
	Status       ResourceDiagnosticStatus
	Conditions   []ConditionObservation
	Containers   []ContainerObservation
	ServicePorts []ServicePortObservation
	Truncated    bool
}

// ResourceObservationList is one bounded reader response before model filtering.
type ResourceObservationList struct {
	Items     []ResourceObservation
	Truncated bool
}

// RelatedInclude is one code-defined relationship family. The model cannot
// supply an arbitrary edge or selector.
type RelatedInclude string

const (
	RelatedIncludeOwners           RelatedInclude = "owners"
	RelatedIncludePods             RelatedInclude = "pods"
	RelatedIncludeReplicaSets      RelatedInclude = "replica_sets"
	RelatedIncludeServiceEndpoints RelatedInclude = "service_endpoints"
	RelatedIncludeServices         RelatedInclude = "services"
)

func (include RelatedInclude) validFor(kind domain.ResourceKind) bool {
	switch kind {
	case domain.ResourceKindDeployment:
		return include == RelatedIncludePods || include == RelatedIncludeReplicaSets
	case domain.ResourceKindReplicaSet:
		return include == RelatedIncludeOwners || include == RelatedIncludePods
	case domain.ResourceKindPod:
		return include == RelatedIncludeOwners || include == RelatedIncludeServiceEndpoints || include == RelatedIncludeServices
	case domain.ResourceKindJob:
		return include == RelatedIncludePods
	case domain.ResourceKindService:
		return include == RelatedIncludePods || include == RelatedIncludeServiceEndpoints
	default:
		return false
	}
}

// RelatedRelation is one fixed graph edge kind.
type RelatedRelation string

const (
	RelatedRelationOwnerReference   RelatedRelation = "owner_reference"
	RelatedRelationSelectorMatch    RelatedRelation = "selector_match"
	RelatedRelationServiceEndpoints RelatedRelation = "service_endpoints"
)

func (relation RelatedRelation) valid() bool {
	return relation == RelatedRelationOwnerReference || relation == RelatedRelationSelectorMatch ||
		relation == RelatedRelationServiceEndpoints
}

// RelatedReadRequest is one bounded traversal rooted at an allowlisted direct
// target. Scope, traversal ceilings, and selector construction are runtime
// owned and never model-controlled.
type RelatedReadRequest struct {
	Scope     domain.ClusterScope
	Reference domain.ResourceRef
	Depth     int
	Includes  []RelatedInclude
	MaxNodes  int
	MaxEdges  int
}

// Validate rejects generic, cross-Namespace, expanding, or non-canonical
// relationship reads before an adapter action.
func (request RelatedReadRequest) Validate() error {
	kind, allowed := domain.ResourceKindForReference(request.Reference)
	if request.Scope.Validate() != nil || !allowed || domain.ValidateLiveResourceRef(request.Reference) != nil ||
		request.Reference.Namespace != request.Scope.Namespace || request.Depth < 1 || request.Depth > 2 ||
		request.MaxNodes < 1 || request.MaxNodes > 25 || request.MaxEdges < 1 || request.MaxEdges > 40 ||
		len(request.Includes) == 0 || len(request.Includes) > 3 {
		return ErrInvalidRelatedRead
	}
	previous := ""
	for _, include := range request.Includes {
		current := string(include)
		if !include.validFor(kind) || previous != "" && current <= previous {
			return ErrInvalidRelatedRead
		}
		previous = current
	}
	return nil
}

// RelatedReference is a bounded graph identity. ReferenceOnly values originate
// only from an already-read owner reference and must never be fetched.
type RelatedReference struct {
	APIVersion      string
	Kind            string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	ReferenceOnly   bool
}

func (reference RelatedReference) valid(scope domain.ClusterScope) bool {
	if reference.Namespace != scope.Namespace || !domain.ValidNamespaceName(reference.Namespace) ||
		!domain.ValidResourceName(reference.Name) || !validRelatedAPIVersion(reference.APIVersion) ||
		!validRelatedKind(reference.Kind) || !validRelatedOptionalIdentity(reference.UID, 256) ||
		!validRelatedOptionalIdentity(reference.ResourceVersion, 256) {
		return false
	}
	if reference.ReferenceOnly {
		return reference.ResourceVersion == "" && !forbiddenRelatedReference(reference.APIVersion, reference.Kind)
	}
	_, allowed := domain.ResourceKindForReference(reference.resourceRef())
	return allowed
}

func (reference RelatedReference) resourceRef() domain.ResourceRef {
	return domain.ResourceRef{
		APIVersion: reference.APIVersion, Kind: reference.Kind, Namespace: reference.Namespace,
		Name: reference.Name, UID: reference.UID, ResourceVersion: reference.ResourceVersion,
	}
}

func (reference RelatedReference) key() string {
	return strings.Join([]string{reference.APIVersion, reference.Kind, reference.Namespace, reference.Name}, "\x00")
}

// RelatedNodeObservation is one fetched safe resource projection or one
// unfetched owner identity already carried by a fetched object.
type RelatedNodeObservation struct {
	Reference RelatedReference
	Resource  ResourceObservation
	Fetched   bool
	Hop       int
}

func (node RelatedNodeObservation) valid(request RelatedReadRequest) bool {
	if node.Hop < 0 || node.Hop > request.Depth || !node.Reference.valid(request.Scope) {
		return false
	}
	if !node.Fetched {
		return node.Reference.ReferenceOnly && zeroResourceObservation(node.Resource)
	}
	return !node.Reference.ReferenceOnly && node.Resource.validate() == nil &&
		node.Reference.resourceRef() == node.Resource.Summary.Reference
}

// RelatedEdgeObservation contains no raw selector. Selector matches expose
// only a key count and a deterministic one-way fingerprint; EndpointSlice
// relationships expose counts and never addresses.
type RelatedEdgeObservation struct {
	From                RelatedReference
	To                  RelatedReference
	ToPresent           bool
	Relation            RelatedRelation
	Hop                 int
	SelectorKeyCount    int
	SelectorFingerprint string
	ReadyEndpoints      int
	NotReadyEndpoints   int
	Truncated           bool
}

func (edge RelatedEdgeObservation) valid(request RelatedReadRequest) bool {
	if !edge.Relation.valid() || edge.Hop < 1 || edge.Hop > request.Depth || !edge.From.valid(request.Scope) ||
		edge.ReadyEndpoints < 0 || edge.NotReadyEndpoints < 0 {
		return false
	}
	switch edge.Relation {
	case RelatedRelationOwnerReference:
		return edge.ToPresent && edge.To.valid(request.Scope) && edge.SelectorKeyCount == 0 &&
			edge.SelectorFingerprint == "" && edge.ReadyEndpoints == 0 && edge.NotReadyEndpoints == 0
	case RelatedRelationSelectorMatch:
		return edge.ToPresent && edge.To.valid(request.Scope) && edge.SelectorKeyCount > 0 && edge.SelectorKeyCount <= 64 &&
			validSHA256Fingerprint(edge.SelectorFingerprint) && edge.ReadyEndpoints == 0 && edge.NotReadyEndpoints == 0
	case RelatedRelationServiceEndpoints:
		return !edge.ToPresent && edge.To == (RelatedReference{}) && edge.From.Kind == string(domain.ResourceKindService) &&
			edge.SelectorKeyCount == 0 && edge.SelectorFingerprint == ""
	default:
		return false
	}
}

func (edge RelatedEdgeObservation) key() string {
	return strings.Join([]string{string(edge.Relation), edge.From.key(), edge.To.key()}, "\x00")
}

// RelatedGapObservation is one safe missing branch. It contains only a stable
// class and code-defined relation, never a vendor or server error string.
type RelatedGapObservation struct {
	From     RelatedReference
	Relation RelatedRelation
	Class    domain.SafeErrorClass
	Hop      int
}

func (gap RelatedGapObservation) valid(request RelatedReadRequest) bool {
	if !gap.From.valid(request.Scope) || !gap.Relation.valid() || gap.Hop < 1 || gap.Hop > request.Depth {
		return false
	}
	switch gap.Class {
	case domain.SafeErrorClassPermissionDenied,
		domain.SafeErrorClassNotFound,
		domain.SafeErrorClassConflict,
		domain.SafeErrorClassUnsupported,
		domain.SafeErrorClassPolicyDenied,
		domain.SafeErrorClassRateLimited,
		domain.SafeErrorClassUnavailable:
		return true
	default:
		return false
	}
}

// RelatedObservationGraph is the complete source-allowlisted adapter result
// before Tool-local redaction, output fitting, and Evidence creation.
type RelatedObservationGraph struct {
	Root      RelatedReference
	Nodes     []RelatedNodeObservation
	Edges     []RelatedEdgeObservation
	Gaps      []RelatedGapObservation
	Truncated bool
}

// Validate checks identity, bounds, uniqueness, edge closure, and acyclicity
// against the exact runtime-derived request.
func (graph RelatedObservationGraph) Validate(request RelatedReadRequest) error {
	if request.Validate() != nil || !graph.Root.valid(request.Scope) || graph.Root.ReferenceOnly ||
		!sameRelatedTarget(request.Reference, graph.Root.resourceRef()) || len(graph.Nodes) < 1 ||
		len(graph.Nodes) > request.MaxNodes || len(graph.Edges) > request.MaxEdges || len(graph.Gaps) > request.MaxEdges {
		return ErrInvalidRelatedRead
	}
	nodes := make(map[string]RelatedNodeObservation, len(graph.Nodes))
	rootFound := false
	for _, node := range graph.Nodes {
		if !node.valid(request) {
			return ErrInvalidRelatedRead
		}
		key := node.Reference.key()
		if _, duplicate := nodes[key]; duplicate {
			return ErrInvalidRelatedRead
		}
		nodes[key] = node
		if key == graph.Root.key() {
			rootFound = node.Reference == graph.Root && node.Hop == 0 && node.Fetched
		}
	}
	if !rootFound {
		return ErrInvalidRelatedRead
	}
	edges := make(map[string]struct{}, len(graph.Edges))
	adjacency := make(map[string][]string, len(graph.Nodes))
	incoming := make(map[string]int, len(graph.Nodes))
	for _, edge := range graph.Edges {
		if !edge.valid(request) {
			return ErrInvalidRelatedRead
		}
		fromKey := edge.From.key()
		fromNode, exists := nodes[fromKey]
		if !exists || fromNode.Reference != edge.From || !fromNode.Fetched {
			return ErrInvalidRelatedRead
		}
		if edge.ToPresent {
			toKey := edge.To.key()
			toNode, exists := nodes[toKey]
			if !exists || toNode.Reference != edge.To || fromNode.Hop+1 != edge.Hop || toNode.Hop != edge.Hop ||
				edge.Relation == RelatedRelationSelectorMatch && !toNode.Fetched {
				return ErrInvalidRelatedRead
			}
			adjacency[fromKey] = append(adjacency[fromKey], toKey)
			incoming[toKey]++
		} else if fromNode.Hop+1 != edge.Hop {
			return ErrInvalidRelatedRead
		}
		key := edge.key()
		if _, duplicate := edges[key]; duplicate {
			return ErrInvalidRelatedRead
		}
		edges[key] = struct{}{}
	}
	for key := range nodes {
		if key != graph.Root.key() && incoming[key] == 0 {
			return ErrInvalidRelatedRead
		}
	}
	gaps := make(map[string]struct{}, len(graph.Gaps))
	for _, gap := range graph.Gaps {
		if !gap.valid(request) {
			return ErrInvalidRelatedRead
		}
		fromNode, exists := nodes[gap.From.key()]
		if !exists || fromNode.Reference != gap.From || !fromNode.Fetched {
			return ErrInvalidRelatedRead
		}
		key := strings.Join([]string{gap.From.key(), string(gap.Relation), string(gap.Class)}, "\x00")
		if _, duplicate := gaps[key]; duplicate {
			return ErrInvalidRelatedRead
		}
		gaps[key] = struct{}{}
	}
	if relatedGraphHasCycle(adjacency) {
		return ErrInvalidRelatedRead
	}
	return nil
}

// RelatedResourceReader is the Tool-owned one-operation relationship port. It
// exposes no client, GVR, raw selector, discovery, Watch, or write method.
type RelatedResourceReader interface {
	ReadRelatedResources(context.Context, RelatedReadRequest) (RelatedObservationGraph, error)
}

func sameRelatedTarget(request, result domain.ResourceRef) bool {
	return request.APIVersion == result.APIVersion && request.Kind == result.Kind &&
		request.Namespace == result.Namespace && request.Name == result.Name &&
		(request.UID == "" || request.UID == result.UID)
}

func zeroResourceObservation(observation ResourceObservation) bool {
	return observation.Summary.Reference == (domain.ResourceRef{}) && observation.Summary.CreatedAt.IsZero() &&
		observation.Summary.Status == (domain.ResourceStatus{}) && len(observation.Summary.Owners) == 0 &&
		observation.Generation == (OptionalInt64{}) &&
		observation.LabelCount == 0 && len(observation.Labels) == 0 && observation.Status == (ResourceDiagnosticStatus{}) &&
		len(observation.Conditions) == 0 && len(observation.Containers) == 0 && len(observation.ServicePorts) == 0 && !observation.Truncated
}

func validRelatedIdentity(value string, maximumBytes int) bool {
	return value != "" && len(value) <= maximumBytes && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, func(current rune) bool { return unicode.IsControl(current) || unicode.In(current, unicode.Cf) })
}

func validRelatedAPIVersion(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 2 {
		return false
	}
	for index, part := range parts {
		if !validRelatedAPISegment(part, index == 0 && len(parts) == 2) {
			return false
		}
	}
	return true
}

func validRelatedAPISegment(value string, allowDot bool) bool {
	if value == "" || !asciiLowerOrDigit(value[0]) || !asciiLowerOrDigit(value[len(value)-1]) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		current := value[index]
		if asciiLowerOrDigit(current) || current == '-' || allowDot && current == '.' {
			continue
		}
		return false
	}
	return true
}

func validRelatedKind(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		current := value[index]
		if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
			continue
		}
		return false
	}
	return true
}

func forbiddenRelatedReference(apiVersion, kind string) bool {
	return apiVersion == "v1" && (kind == "Secret" || kind == "ConfigMap")
}

func asciiLowerOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func validRelatedOptionalIdentity(value string, maximumBytes int) bool {
	return value == "" || validRelatedIdentity(value, maximumBytes)
}

func validSHA256Fingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func relatedGraphHasCycle(adjacency map[string][]string) bool {
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(adjacency))
	var visit func(string) bool
	visit = func(node string) bool {
		switch states[node] {
		case visiting:
			return true
		case visited:
			return false
		}
		states[node] = visiting
		for _, next := range adjacency[node] {
			if visit(next) {
				return true
			}
		}
		states[node] = visited
		return false
	}
	for node := range adjacency {
		if visit(node) {
			return true
		}
	}
	return false
}

func (observation ResourceObservation) validate() error {
	if observation.Summary.Validate() != nil || observation.LabelCount < len(observation.Labels) || observation.LabelCount < 0 ||
		len(observation.Labels) > maxProjectedLabels || len(observation.Conditions) > maxProjectedConditions ||
		len(observation.Containers) > maxProjectedContainers || len(observation.ServicePorts) > maxProjectedServicePorts ||
		!observation.Generation.valid() || !optionalCountValid(observation.Status.Current) || !optionalCountValid(observation.Status.Updated) ||
		!optionalCountValid(observation.Status.Unavailable) || !optionalCountValid(observation.Status.Parallelism) ||
		!optionalCountValid(observation.Status.BackoffLimit) || !optionalCountValid(observation.Status.SelectorKeyCount) ||
		!optionalCountValid(observation.Status.SchedulingConstraints) || !observation.Status.ObservedGeneration.valid() ||
		!observation.Status.ActiveDeadlineSeconds.valid() || !observation.Status.NodeName.valid(maxIdentityTextBytes) ||
		!observation.Status.Revision.valid(maxProjectedTextBytes) {
		return ErrInvalidResourceRead
	}
	seenLabels := make(map[string]struct{}, len(observation.Labels))
	for _, label := range observation.Labels {
		if !allowedDiagnosticLabel(label.Key) || !label.Value.valid(maxProjectedTextBytes) {
			return ErrInvalidResourceRead
		}
		if _, exists := seenLabels[label.Key]; exists {
			return ErrInvalidResourceRead
		}
		seenLabels[label.Key] = struct{}{}
	}
	for _, condition := range observation.Conditions {
		if !condition.Type.valid(maxProjectedTextBytes) || !condition.Status.valid(maxProjectedTextBytes) ||
			!condition.Reason.valid(maxProjectedTextBytes) || !condition.Message.valid(maxProjectedTextBytes) ||
			!validOptionalUTCTime(condition.LastTransitionTime) {
			return ErrInvalidResourceRead
		}
	}
	for _, container := range observation.Containers {
		if !container.Name.valid(maxProjectedTextBytes) || container.Name.Value == "" || !container.Image.valid(maxProjectedTextBytes) ||
			container.RestartCount < 0 || !container.Current.valid() || !container.Last.valid() {
			return ErrInvalidResourceRead
		}
	}
	for _, port := range observation.ServicePorts {
		if port.Port < 1 || port.Port > 65535 || !port.Name.valid(maxProjectedTextBytes) ||
			!port.Protocol.valid(maxProjectedTextBytes) || !port.TargetPort.valid(maxProjectedTextBytes) {
			return ErrInvalidResourceRead
		}
	}
	return nil
}

// Validate checks the complete project-owned reader DTO before a sink.
func (observation ResourceObservation) Validate() error {
	return observation.validate()
}

func (state ContainerStateObservation) valid() bool {
	return state.State.valid(maxProjectedTextBytes) && state.Reason.valid(maxProjectedTextBytes) &&
		state.Message.valid(maxProjectedTextBytes) && optionalCountValid(state.ExitCode) &&
		validOptionalUTCTime(state.StartedAt) && validOptionalUTCTime(state.FinishedAt)
}

func (list ResourceObservationList) validate(request ResourceListRequest) error {
	if request.Validate() != nil || len(list.Items) > request.Limit {
		return ErrInvalidResourceRead
	}
	seenNames := make(map[string]struct{}, len(list.Items))
	for _, item := range list.Items {
		kind, allowed := domain.ResourceKindForReference(item.Summary.Reference)
		if item.validate() != nil || !allowed || kind != request.Kind || item.Summary.Reference.Namespace != request.Scope.Namespace {
			return ErrInvalidResourceRead
		}
		if _, exists := seenNames[item.Summary.Reference.Name]; exists {
			return ErrInvalidResourceRead
		}
		seenNames[item.Summary.Reference.Name] = struct{}{}
	}
	return nil
}

// Validate checks a bounded list against the exact request that produced it.
func (list ResourceObservationList) Validate(request ResourceListRequest) error {
	return list.validate(request)
}

func optionalCountValid(value domain.OptionalCount) bool {
	return value.Present && value.Value >= 0 || !value.Present && value.Value == 0
}

func validOptionalUTCTime(value time.Time) bool {
	return value.IsZero() || value.Location() == time.UTC && value.UnixMilli() >= 0
}

func allowedDiagnosticLabel(key string) bool {
	switch key {
	case "app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/component",
		"pod-template-hash",
		"controller-uid",
		"job-name",
		"batch.kubernetes.io/controller-uid",
		"batch.kubernetes.io/job-name":
		return true
	default:
		return false
	}
}

type classifiedError interface {
	error
	Class() domain.SafeErrorClass
}

func classifyFailure(ctx context.Context, raw error) domain.SafeErrorClass {
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return domain.SafeErrorClassTimeout
		case errors.Is(ctx.Err(), context.Canceled):
			return domain.SafeErrorClassCancelled
		}
	}
	if errors.Is(raw, context.DeadlineExceeded) {
		return domain.SafeErrorClassTimeout
	}
	if errors.Is(raw, context.Canceled) {
		return domain.SafeErrorClassCancelled
	}
	var classified classifiedError
	if errors.As(raw, &classified) && classified.Class().Valid() {
		return classified.Class()
	}
	return domain.SafeErrorClassInternal
}

func safeFailureDefinition(class domain.SafeErrorClass) (string, bool) {
	switch class {
	case domain.SafeErrorClassInvalidInput:
		return "The cluster-read request is invalid.", false
	case domain.SafeErrorClassConfigurationInvalid:
		return "The Kubernetes client configuration is invalid.", false
	case domain.SafeErrorClassConsentRequired:
		return "Model data-transfer consent is required.", false
	case domain.SafeErrorClassAuthenticationFailed:
		return "Kubernetes authentication failed.", false
	case domain.SafeErrorClassPermissionDenied:
		return "Kubernetes denied the requested read.", false
	case domain.SafeErrorClassNotFound:
		return "The requested Kubernetes object was not found.", false
	case domain.SafeErrorClassConflict:
		return "The Kubernetes object changed during the bounded read.", true
	case domain.SafeErrorClassUnsupported:
		return "The requested Kubernetes observation is unsupported.", false
	case domain.SafeErrorClassPolicyDenied:
		return "The cluster-read request was blocked by the fixed policy.", false
	case domain.SafeErrorClassStaleScope:
		return "The Kubernetes context or namespace changed before the read completed.", false
	case domain.SafeErrorClassBudgetExhausted:
		return "The cluster read reached a fixed output or item limit.", false
	case domain.SafeErrorClassRateLimited:
		return "Kubernetes temporarily limited the request rate.", true
	case domain.SafeErrorClassUnavailable:
		return "Kubernetes is temporarily unavailable.", true
	case domain.SafeErrorClassTimeout:
		return "The Kubernetes read reached its time limit.", true
	case domain.SafeErrorClassCancelled:
		return "The Kubernetes read was cancelled.", false
	case domain.SafeErrorClassSensitiveOutputBlocked:
		return "A Kubernetes field was hidden because it may contain sensitive data.", false
	case domain.SafeErrorClassInvalidExternalResponse:
		return "Kubernetes returned fields that KuPilot could not display safely.", false
	case domain.SafeErrorClassPersistenceUnavailable:
		return "Required local persistence is unavailable.", false
	case domain.SafeErrorClassInternal:
		return "The cluster read failed safely.", false
	default:
		return "The cluster read failed safely.", false
	}
}

func observedAt(dependencies ResourceToolDependencies, call BoundToolCall) time.Time {
	current := dependencies.Now()
	activation := call.Scope().ActivatedAt
	if current.IsZero() || current.Location() != time.UTC || current.UnixMilli() < 0 || current.Before(activation) {
		return ceilToUTCMillisecond(activation)
	}
	current = time.UnixMilli(current.UnixMilli()).UTC()
	if current.Before(activation) {
		return ceilToUTCMillisecond(activation)
	}
	return current
}

func ceilToUTCMillisecond(value time.Time) time.Time {
	milliseconds := value.UnixMilli()
	result := time.UnixMilli(milliseconds).UTC()
	if result.Before(value) {
		result = time.UnixMilli(milliseconds + 1).UTC()
	}
	return result
}

func failedResult(call BoundToolCall, observed time.Time, class domain.SafeErrorClass) ToolResult {
	message, retryable := safeFailureDefinition(class)
	status := domain.ToolResultStatusError
	if class == domain.SafeErrorClassPolicyDenied || class == domain.SafeErrorClassBudgetExhausted || class == domain.SafeErrorClassInvalidInput {
		status = domain.ToolResultStatusDenied
	}
	result := ToolResult{
		InvocationID: call.InvocationID(),
		Name:         call.Name(),
		Version:      call.Version(),
		Scope:        call.Scope().Snapshot(),
		ObservedAt:   observed,
		Status:       status,
		DataJSON:     "{}",
		Evidence:     []domain.Evidence{},
		Warnings:     []domain.ToolResultWarning{},
		Error: &domain.ToolResultError{
			Class:       class,
			Retryable:   retryable,
			SafeMessage: message,
		},
	}
	measured, err := measureResult(call, result)
	if err == nil {
		result = measured
	}
	return result
}

func measureResult(call BoundToolCall, result ToolResult) (ToolResult, error) {
	previous := -1
	for attempts := 0; attempts < 8; attempts++ {
		_, current, err := agent.BuildToolResultMessage(call.ModelCallID(), result)
		if err != nil {
			return ToolResult{}, fmt.Errorf("measure ToolResult: %w", ErrToolOutputLimit)
		}
		if current == previous || current == result.Truncation.ReturnedBytes {
			result.Truncation.ReturnedBytes = current
			if result.Validate() != nil {
				return ToolResult{}, ErrInvalidResourceRead
			}
			return result, nil
		}
		previous = current
		result.Truncation.ReturnedBytes = current
	}
	return ToolResult{}, ErrToolOutputLimit
}

type evidenceTemplate struct {
	category       domain.EvidenceCategory
	resource       domain.ResourceRef
	fact           string
	sourcePath     string
	severity       *domain.EvidenceSeverity
	redactionCount int
	truncated      bool
}

func materializeEvidence(
	dependencies ResourceToolDependencies,
	call BoundToolCall,
	observed time.Time,
	templates []evidenceTemplate,
) ([]domain.Evidence, error) {
	if len(templates) > call.Ceilings().MaxEvidenceItems {
		return nil, ErrInvalidEvidenceSource
	}
	result := make([]domain.Evidence, 0, len(templates))
	seen := make(map[domain.EvidenceID]struct{}, len(templates))
	for index := range templates {
		template := templates[index]
		identifier, err := dependencies.EvidenceIDs.NewEvidenceID()
		if err != nil || !identifier.Valid() {
			return nil, ErrInvalidEvidenceSource
		}
		if _, exists := seen[identifier]; exists {
			return nil, ErrInvalidEvidenceSource
		}
		seen[identifier] = struct{}{}
		result = append(result, evidenceFromTemplate(identifier, call, observed, template))
	}
	return result, nil
}

func limitEvidenceTemplates(call BoundToolCall, templates []evidenceTemplate) ([]evidenceTemplate, bool) {
	limit := min(len(templates), call.Ceilings().MaxEvidenceItems)
	return templates[:limit], limit < len(templates)
}

// previewEvidence uses fixed-width non-authoritative identifiers only to
// measure the complete envelope. Accepted Evidence IDs are allocated after the
// byte and item plan is final.
func previewEvidence(call BoundToolCall, observed time.Time, templates []evidenceTemplate) []domain.Evidence {
	result := make([]domain.Evidence, len(templates))
	for index, template := range templates {
		identifier := domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1))
		result[index] = evidenceFromTemplate(identifier, call, observed, template)
	}
	return result
}

func evidenceFromTemplate(
	identifier domain.EvidenceID,
	call BoundToolCall,
	observed time.Time,
	template evidenceTemplate,
) domain.Evidence {
	var sourcePath *string
	if template.sourcePath != "" {
		copied := template.sourcePath
		sourcePath = &copied
	}
	return domain.Evidence{
		ID:             identifier,
		RunID:          call.RunID(),
		InvocationID:   call.InvocationID(),
		Category:       template.category,
		Scope:          call.Scope().Snapshot(),
		Resource:       template.resource,
		Fact:           template.fact,
		SourcePath:     sourcePath,
		Severity:       template.severity,
		RedactionCount: template.redactionCount,
		Truncated:      template.truncated,
		Fingerprint:    domain.SHA256Hex(string(template.category) + "\n" + template.fact + "\n" + template.sourcePath),
		ObservedAt:     observed,
	}
}

func finalizePlannedResult(call BoundToolCall, observed time.Time, result ToolResult) ToolResult {
	measured, err := measureResult(call, result)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	guard, err := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if err != nil || !guard.Allows(measured.Truncation.ReturnedBytes) {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	return measured
}

func stableSeverity(value domain.EvidenceSeverity) *domain.EvidenceSeverity {
	copied := value
	return &copied
}

func appendWarning(warnings []domain.ToolResultWarning, code, message string) []domain.ToolResultWarning {
	for _, warning := range warnings {
		if warning.Code == code {
			return warnings
		}
	}
	if len(warnings) == maxResultWarningCount {
		return warnings
	}
	return append(warnings, domain.ToolResultWarning{Code: code, SafeMessage: message})
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boundedEvidenceFact(value string) (string, bool) {
	if len(value) <= maxEvidenceFactBytes {
		return value, false
	}
	var builder strings.Builder
	builder.Grow(maxEvidenceFactBytes)
	for _, current := range value {
		width := utf8.RuneLen(current)
		if width < 0 || builder.Len()+width > maxEvidenceFactBytes {
			break
		}
		builder.WriteRune(current)
	}
	return strings.TrimSpace(builder.String()), true
}

func evidenceTemplatesTruncated(templates []evidenceTemplate) bool {
	for _, template := range templates {
		if template.truncated {
			return true
		}
	}
	return false
}
