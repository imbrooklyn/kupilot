package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	relationshipDepthReason   = "relationship_depth_limit"
	relationshipLimitReason   = "relationship_limit"
	relationshipPartialReason = "relationship_partial"
	relatedSensitiveWarning   = "related_sensitive_field_blocked"
)

var (
	// ErrInvalidRelatedToolDependencies reports an incomplete fixed
	// get_related_resources dependency set.
	ErrInvalidRelatedToolDependencies = errors.New("get_related_resources Tool dependencies are invalid")
)

// RelatedToolDependencies are immutable stateless dependencies for the one
// fixed relationship handler.
type RelatedToolDependencies struct {
	Reader      RelatedResourceReader
	ScopeGuard  ScopeGuard
	EvidenceIDs EvidenceIDSource
	Text        TextProcessor
	Now         func() time.Time
}

func (dependencies RelatedToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.ScopeGuard == nil || dependencies.EvidenceIDs == nil ||
		dependencies.Text == nil || dependencies.Now == nil {
		return ErrInvalidRelatedToolDependencies
	}
	now := dependencies.Now()
	if !validRequiredUTCTime(now) {
		return ErrInvalidRelatedToolDependencies
	}
	return nil
}

func (dependencies RelatedToolDependencies) resourceDependencies() ResourceToolDependencies {
	return ResourceToolDependencies{
		ScopeGuard: dependencies.ScopeGuard, EvidenceIDs: dependencies.EvidenceIDs,
		Text: dependencies.Text, Now: dependencies.Now,
	}
}

// GetRelatedResourcesTool follows only code-defined, directed, bounded
// relationships and returns a deterministic safe graph with same-run Evidence.
type GetRelatedResourcesTool struct {
	dependencies RelatedToolDependencies
}

var _ agent.Tool = (*GetRelatedResourcesTool)(nil)

// NewGetRelatedResourcesTool validates the immutable relationship dependencies.
func NewGetRelatedResourcesTool(dependencies RelatedToolDependencies) (*GetRelatedResourcesTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidRelatedToolDependencies
	}
	return &GetRelatedResourcesTool{dependencies: dependencies}, nil
}

// Execute applies scope denial, source projection, redaction, hard graph and
// byte limits, and Evidence creation in that order.
func (tool *GetRelatedResourcesTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(tool.dependencies.resourceDependencies(), call)
	arguments, request, depthLimited, err := decodeRelatedResourcesCall(call)
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
	graph, readErr := tool.dependencies.Reader.ReadRelatedResources(ctx, request)
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, observed, classifyFailure(ctx, readErr))
	}
	if graph.Validate(request) != nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	data, warnings, metadata, projectErr := tool.project(graph, arguments, request)
	if projectErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	reason := ""
	if len(graph.Gaps) > 0 {
		reason = relationshipPartialReason
	}
	if depthLimited {
		reason = relationshipDepthReason
	}
	if graph.Truncated {
		reason = relationshipLimitReason
	}
	if reason == "" && (metadata.truncated || metadata.blocked) {
		reason = fieldLimitReason
	}
	planned, templates := fitRelatedResult(call, observed, data, warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		return planned
	}
	evidence, err := materializeEvidence(tool.dependencies.resourceDependencies(), call, observed, templates)
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

type safeRelatedReference struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ReferenceOnly   bool   `json:"reference_only"`
	ResourceVersion string `json:"resource_version,omitempty"`
	UID             string `json:"uid,omitempty"`
}

type safeRelatedNode struct {
	Fetched   bool                 `json:"fetched"`
	Hop       int                  `json:"hop"`
	Resource  safeRelatedReference `json:"resource"`
	Status    safeResourceStatus   `json:"status"`
	Truncated bool                 `json:"truncated"`

	source         domain.ResourceRef
	redactionCount int
}

type safeRelatedEdge struct {
	From                safeRelatedReference  `json:"from"`
	Hop                 int                   `json:"hop"`
	NotReadyEndpoints   *int                  `json:"not_ready_endpoints,omitempty"`
	ReadyEndpoints      *int                  `json:"ready_endpoints,omitempty"`
	Relation            RelatedRelation       `json:"relation"`
	SelectorFingerprint string                `json:"selector_fingerprint,omitempty"`
	SelectorKeyCount    int                   `json:"selector_key_count,omitempty"`
	To                  *safeRelatedReference `json:"to,omitempty"`
	Truncated           bool                  `json:"truncated"`

	source domain.ResourceRef
}

type safeRelatedGap struct {
	Class    domain.SafeErrorClass `json:"class"`
	From     safeRelatedReference  `json:"from"`
	Hop      int                   `json:"hop"`
	Relation RelatedRelation       `json:"relation"`
}

type getRelatedResourcesData struct {
	Depth             int                  `json:"depth"`
	Edges             []safeRelatedEdge    `json:"edges"`
	Gaps              []safeRelatedGap     `json:"gaps"`
	Include           []RelatedInclude     `json:"include"`
	InstructionLike   bool                 `json:"instruction_like"`
	OriginalEdgeCount int                  `json:"original_edge_count"`
	OriginalNodeCount int                  `json:"original_node_count"`
	RedactionCount    int                  `json:"redaction_count"`
	ReturnedEdgeCount int                  `json:"returned_edge_count"`
	ReturnedNodeCount int                  `json:"returned_node_count"`
	Root              safeRelatedReference `json:"root"`
	SourceTrust       string               `json:"source_trust"`
	Nodes             []safeRelatedNode    `json:"nodes"`
	Truncated         bool                 `json:"truncated"`
}

func (tool *GetRelatedResourcesTool) project(
	graph RelatedObservationGraph,
	arguments getRelatedResourcesArguments,
	request RelatedReadRequest,
) (getRelatedResourcesData, []domain.ToolResultWarning, textMetadata, error) {
	nodes := append([]RelatedNodeObservation(nil), graph.Nodes...)
	edges := append([]RelatedEdgeObservation(nil), graph.Edges...)
	gaps := append([]RelatedGapObservation(nil), graph.Gaps...)
	sort.Slice(nodes, func(left, right int) bool { return relatedNodeSortKey(nodes[left]) < relatedNodeSortKey(nodes[right]) })
	sort.Slice(edges, func(left, right int) bool { return relatedEdgeSortKey(edges[left]) < relatedEdgeSortKey(edges[right]) })
	sort.Slice(gaps, func(left, right int) bool { return relatedGapSortKey(gaps[left]) < relatedGapSortKey(gaps[right]) })

	data := getRelatedResourcesData{
		Depth: request.Depth, Edges: []safeRelatedEdge{}, Gaps: []safeRelatedGap{},
		Include: append([]RelatedInclude(nil), arguments.Include...), Nodes: []safeRelatedNode{},
		OriginalEdgeCount: len(edges), OriginalNodeCount: len(nodes), SourceTrust: security.UntrustedDataClass,
		Truncated: graph.Truncated,
	}
	metadata := textMetadata{truncated: graph.Truncated}
	warnings := []domain.ToolResultWarning{}
	references := make(map[string]safeRelatedReference, len(nodes))
	projector := &GetResourceTool{dependencies: ResourceToolDependencies{Text: tool.dependencies.Text}}
	for _, node := range nodes {
		reference, current, err := safeRelatedReferenceValue(tool.dependencies.Text, node.Reference)
		if err != nil {
			return getRelatedResourcesData{}, nil, textMetadata{}, err
		}
		metadata.merge(current)
		references[node.Reference.key()] = reference
		safeNode := safeRelatedNode{
			Fetched: node.Fetched, Hop: node.Hop, Resource: reference,
			Truncated: node.Resource.Truncated || current.truncated || current.blocked,
		}
		if node.Fetched {
			status, statusMetadata, err := projector.safeStatus(node.Resource)
			if err != nil {
				return getRelatedResourcesData{}, nil, textMetadata{}, err
			}
			metadata.merge(statusMetadata)
			safeNode.Status = status
			safeNode.source = safeRelatedResourceReference(reference)
			safeNode.redactionCount = statusMetadata.redactions + current.redactions
			safeNode.Truncated = safeNode.Truncated || statusMetadata.truncated || statusMetadata.blocked
		}
		if current.blocked {
			warnings = appendWarning(warnings, relatedSensitiveWarning, "One related-resource identity field was hidden because it may contain sensitive data.")
		}
		data.Nodes = append(data.Nodes, safeNode)
	}
	root, exists := references[graph.Root.key()]
	if !exists {
		return getRelatedResourcesData{}, nil, textMetadata{}, ErrInvalidRelatedRead
	}
	data.Root = root
	for _, edge := range edges {
		from, fromExists := references[edge.From.key()]
		if !fromExists {
			return getRelatedResourcesData{}, nil, textMetadata{}, ErrInvalidRelatedRead
		}
		safeEdge := safeRelatedEdge{
			From: from, Hop: edge.Hop, Relation: edge.Relation,
			SelectorFingerprint: edge.SelectorFingerprint, SelectorKeyCount: edge.SelectorKeyCount,
			Truncated: edge.Truncated,
		}
		if edge.ToPresent {
			to, toExists := references[edge.To.key()]
			if !toExists {
				return getRelatedResourcesData{}, nil, textMetadata{}, ErrInvalidRelatedRead
			}
			safeEdge.To = &to
		}
		if edge.Relation == RelatedRelationServiceEndpoints {
			ready, notReady := edge.ReadyEndpoints, edge.NotReadyEndpoints
			safeEdge.ReadyEndpoints = &ready
			safeEdge.NotReadyEndpoints = &notReady
		}
		sourceReference := safeRelatedResourceReference(from)
		if _, allowed := domain.ResourceKindForReference(sourceReference); allowed && domain.ValidateLiveResourceRef(sourceReference) == nil {
			safeEdge.source = sourceReference
		} else {
			safeEdge.source = safeRelatedResourceReference(root)
		}
		metadata.truncated = metadata.truncated || edge.Truncated
		data.Edges = append(data.Edges, safeEdge)
	}
	for _, gap := range gaps {
		from, exists := references[gap.From.key()]
		if !exists {
			return getRelatedResourcesData{}, nil, textMetadata{}, ErrInvalidRelatedRead
		}
		data.Gaps = append(data.Gaps, safeRelatedGap{Class: gap.Class, From: from, Hop: gap.Hop, Relation: gap.Relation})
		warnings = appendWarning(warnings, "related_"+string(gap.Class), relatedGapMessage(gap.Class))
	}
	if graph.Truncated {
		warnings = appendWarning(warnings, relationshipLimitReason, "The relationship graph reached a fixed traversal or source limit.")
	}
	data.ReturnedNodeCount = len(data.Nodes)
	data.ReturnedEdgeCount = len(data.Edges)
	data.RedactionCount = metadata.redactions
	data.InstructionLike = metadata.instructionLike
	data.Truncated = data.Truncated || metadata.truncated || metadata.blocked
	return data, warnings, metadata, nil
}

func safeRelatedReferenceValue(processor TextProcessor, reference RelatedReference) (safeRelatedReference, textMetadata, error) {
	result := safeRelatedReference{
		APIVersion: reference.APIVersion, Kind: reference.Kind, Name: reference.Name,
		Namespace: reference.Namespace, ReferenceOnly: reference.ReferenceOnly,
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
		processed, current, err := processExternalText(processor, ExternalText{Value: field.source}, maxIdentityTextBytes)
		if err != nil {
			return safeRelatedReference{}, textMetadata{}, err
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

func safeRelatedResourceReference(reference safeRelatedReference) domain.ResourceRef {
	return domain.ResourceRef{
		APIVersion:      reference.APIVersion,
		Kind:            reference.Kind,
		Namespace:       reference.Namespace,
		Name:            reference.Name,
		UID:             reference.UID,
		ResourceVersion: reference.ResourceVersion,
	}
}

func relatedEvidence(data getRelatedResourcesData) []evidenceTemplate {
	templates := make([]evidenceTemplate, 0, len(data.Nodes)+len(data.Edges))
	for _, node := range data.Nodes {
		if !node.Fetched || node.source.Validate() != nil {
			continue
		}
		fact, factTruncated := boundedEvidenceFact(resourceStatusFact(safeResourceReference{
			APIVersion: node.Resource.APIVersion, Kind: node.Resource.Kind, Name: node.Resource.Name,
			Namespace: node.Resource.Namespace, ResourceVersion: node.Resource.ResourceVersion, UID: node.Resource.UID,
		}, node.Status))
		templates = append(templates, evidenceTemplate{
			category: domain.EvidenceCategoryResourceStatus, resource: node.source, fact: fact,
			sourcePath: "projected.related.nodes.status", severity: resourceStatusSeverity(node.source.Kind, node.Status),
			redactionCount: node.redactionCount, truncated: node.Truncated || factTruncated,
		})
	}
	for _, edge := range data.Edges {
		fact := ""
		category := domain.EvidenceCategoryResourceStatus
		sourcePath := "projected.related.edges"
		switch edge.Relation {
		case RelatedRelationOwnerReference:
			if edge.To == nil {
				continue
			}
			fact = fmt.Sprintf("The fixed owner relationship connects %s %s to %s %s.", edge.From.Kind, edge.From.Name, edge.To.Kind, edge.To.Name)
			category = domain.EvidenceCategoryOwner
			sourcePath += ".owner_reference"
		case RelatedRelationSelectorMatch:
			if edge.To == nil {
				continue
			}
			fact = fmt.Sprintf("%s %s matched %s %s using %d selector key(s); selector fingerprint %s.",
				edge.From.Kind, edge.From.Name, edge.To.Kind, edge.To.Name, edge.SelectorKeyCount, edge.SelectorFingerprint)
			category = domain.EvidenceCategoryServiceEndpoint
			sourcePath += ".selector_match"
		case RelatedRelationServiceEndpoints:
			if edge.ReadyEndpoints == nil || edge.NotReadyEndpoints == nil {
				continue
			}
			fact = fmt.Sprintf("Service %s has %d ready and %d not-ready EndpointSlice endpoint(s).",
				edge.From.Name, *edge.ReadyEndpoints, *edge.NotReadyEndpoints)
			category = domain.EvidenceCategoryServiceEndpoint
			sourcePath += ".service_endpoints"
		}
		fact, factTruncated := boundedEvidenceFact(fact)
		templates = append(templates, evidenceTemplate{
			category: category, resource: edge.source, fact: fact, sourcePath: sourcePath,
			severity: relatedEdgeSeverity(edge), truncated: edge.Truncated || factTruncated,
		})
	}
	return templates
}

func relatedEdgeSeverity(edge safeRelatedEdge) *domain.EvidenceSeverity {
	if edge.Relation == RelatedRelationServiceEndpoints && edge.ReadyEndpoints != nil && *edge.ReadyEndpoints == 0 {
		return stableSeverity(domain.EvidenceSeverityWarning)
	}
	return stableSeverity(domain.EvidenceSeverityInfo)
}

func fitRelatedResult(
	call BoundToolCall,
	observed time.Time,
	data getRelatedResourcesData,
	warnings []domain.ToolResultWarning,
	reason string,
) (ToolResult, []evidenceTemplate) {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, guardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if guardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
	}
	outputTrimmed := false
	maximumAttempts := len(data.Nodes) + len(data.Edges) + len(data.Gaps) + 1
	for attempts := 0; attempts <= maximumAttempts; attempts++ {
		partial := reason != "" || outputTrimmed
		data.Truncated = data.Truncated || partial
		data.ReturnedNodeCount = len(data.Nodes)
		data.ReturnedEdgeCount = len(data.Edges)
		templates := relatedEvidence(data)
		if len(templates) > call.Ceilings().MaxEvidenceItems {
			data.Truncated = true
			templates = relatedEvidence(data)
			templates = templates[:call.Ceilings().MaxEvidenceItems]
			partial = true
			reason = evidenceLimitReason
		}
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The cluster read returned a deterministic relationship subset because the fixed output limit was reached.")
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
				Evidence: previewEvidence(call, observed, templates), Warnings: currentWarnings,
				Truncation: domain.ToolResultTruncation{ReturnedCount: len(data.Nodes) + len(data.Edges)},
			}
			if partial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Truncated = true
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
				return measured, templates
			}
		}
		outputTrimmed = true
		if !reduceRelatedOutput(&data) {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
		}
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), nil
}

func reduceRelatedOutput(data *getRelatedResourcesData) bool {
	if len(data.Nodes) > 1 {
		removed := data.Nodes[len(data.Nodes)-1].Resource
		data.Nodes = data.Nodes[:len(data.Nodes)-1]
		filteredEdges := data.Edges[:0]
		for _, edge := range data.Edges {
			if sameSafeRelatedReference(edge.From, removed) || edge.To != nil && sameSafeRelatedReference(*edge.To, removed) {
				continue
			}
			filteredEdges = append(filteredEdges, edge)
		}
		data.Edges = filteredEdges
		filteredGaps := data.Gaps[:0]
		for _, gap := range data.Gaps {
			if sameSafeRelatedReference(gap.From, removed) {
				continue
			}
			filteredGaps = append(filteredGaps, gap)
		}
		data.Gaps = filteredGaps
		return true
	}
	if len(data.Edges) > 0 {
		data.Edges = data.Edges[:len(data.Edges)-1]
		return true
	}
	if len(data.Gaps) > 0 {
		data.Gaps = data.Gaps[:len(data.Gaps)-1]
		return true
	}
	return false
}

func sameSafeRelatedReference(left, right safeRelatedReference) bool {
	return left.APIVersion == right.APIVersion && left.Kind == right.Kind && left.Namespace == right.Namespace &&
		left.Name == right.Name && left.UID == right.UID
}

func relatedNodeSortKey(node RelatedNodeObservation) string {
	return fmt.Sprintf("%02d\x00%s", node.Hop, node.Reference.key())
}

func relatedEdgeSortKey(edge RelatedEdgeObservation) string {
	return fmt.Sprintf("%02d\x00%s\x00%s\x00%s", edge.Hop, edge.Relation, edge.From.key(), edge.To.key())
}

func relatedGapSortKey(gap RelatedGapObservation) string {
	return fmt.Sprintf("%02d\x00%s\x00%s\x00%s", gap.Hop, gap.Relation, gap.From.key(), gap.Class)
}

func relatedGapMessage(class domain.SafeErrorClass) string {
	switch class {
	case domain.SafeErrorClassPermissionDenied:
		return "Kubernetes denied one admitted relationship read; the graph is partial."
	case domain.SafeErrorClassNotFound:
		return "One related object was not found during the bounded snapshot."
	case domain.SafeErrorClassUnsupported:
		return "One existing relationship cannot be expanded by the fixed relationship policy."
	case domain.SafeErrorClassPolicyDenied:
		return "One relationship branch was stopped by the fixed policy."
	case domain.SafeErrorClassConflict:
		return "One related object changed during the bounded snapshot."
	case domain.SafeErrorClassRateLimited:
		return "Kubernetes limited one admitted relationship read; the graph is partial."
	case domain.SafeErrorClassUnavailable:
		return "Kubernetes could not provide one admitted relationship branch."
	default:
		return "One admitted relationship branch is unavailable."
	}
}
