package application

import (
	"context"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const MaxUIEvidenceProjectionBytes = 512

var uiEvidenceAddressCandidate = regexp.MustCompile(`[0-9A-Fa-f:.\[\]]+`)

// EvidenceDetailReader is the consumer-owned retained-detail query used by
// Application. It exposes project-owned values and distinguishes absence from
// adapter failure without leaking repository errors.
type EvidenceDetailReader interface {
	ReadDiagnosis(context.Context, domain.AgentRunID) (domain.Diagnosis, bool, error)
	ReadEvidence(context.Context, domain.EvidenceID) (domain.Evidence, bool, error)
}

// UIEvidenceDetailState is one safe terminal state for a cited observation.
type UIEvidenceDetailState string

const (
	UIEvidenceDetailAvailable   UIEvidenceDetailState = "available"
	UIEvidenceDetailPartial     UIEvidenceDetailState = "partial"
	UIEvidenceDetailExpired     UIEvidenceDetailState = "expired"
	UIEvidenceDetailUnavailable UIEvidenceDetailState = "unavailable"
)

func (state UIEvidenceDetailState) valid() bool {
	switch state {
	case UIEvidenceDetailAvailable, UIEvidenceDetailPartial, UIEvidenceDetailExpired, UIEvidenceDetailUnavailable:
		return true
	default:
		return false
	}
}

// UIEvidenceSensitiveFilter reports whether the fixed sensitive-value filter
// replaced content in the already-safe projection.
type UIEvidenceSensitiveFilter string

const (
	UIEvidenceSensitiveFilterNotApplied UIEvidenceSensitiveFilter = "not_applied"
	UIEvidenceSensitiveFilterApplied    UIEvidenceSensitiveFilter = "applied"
)

func (state UIEvidenceSensitiveFilter) valid() bool {
	return state == UIEvidenceSensitiveFilterNotApplied || state == UIEvidenceSensitiveFilterApplied
}

// UIEvidenceReference binds one transcript citation to its exact run, historic
// scope, and UI sequence. It carries no observation content.
type UIEvidenceReference struct {
	EvidenceID     domain.EvidenceID
	RunID          domain.AgentRunID
	Scope          domain.ScopeSnapshot
	Sequence       int64
	State          UIEvidenceDetailState
	ClaimSequences []int
	ClaimKinds     []domain.ClaimKind
}

// Validate checks complete citation correlation and a fixed display state.
func (reference UIEvidenceReference) Validate() error {
	if !reference.EvidenceID.Valid() || !reference.RunID.Valid() || reference.Scope.Validate() != nil ||
		!domain.ValidContextName(reference.Scope.Context) || !domain.ValidNamespaceName(reference.Scope.Namespace) ||
		reference.Scope.Generation < 1 || reference.Sequence < 1 || reference.Sequence > 4096 || !reference.State.valid() {
		return ErrInvalidUIEvent
	}
	if len(reference.ClaimSequences) != len(reference.ClaimKinds) {
		return ErrInvalidUIEvent
	}
	previous := 0
	for index, sequence := range reference.ClaimSequences {
		if sequence < 1 || sequence > 100 || sequence <= previous || !validUIClaimKind(reference.ClaimKinds[index]) {
			return ErrInvalidUIEvent
		}
		previous = sequence
	}
	return nil
}

func validUIClaimKind(kind domain.ClaimKind) bool {
	switch kind {
	case domain.ClaimCurrentObservation, domain.ClaimInference, domain.ClaimRecommendation,
		domain.ClaimUncertainty, domain.ClaimUnsupportedObservation:
		return true
	default:
		return false
	}
}

// UIEvidenceDetailQuery requests only the safe detail for one accepted
// transcript citation.
type UIEvidenceDetailQuery struct {
	RequestID uint64
	Reference UIEvidenceReference
}

// Validate checks request and citation correlation.
func (query UIEvidenceDetailQuery) Validate() error {
	if query.RequestID == 0 || query.Reference.Validate() != nil {
		return ErrInvalidUIQuery
	}
	return nil
}

// UIEvidenceResource excludes UID, resource version, annotations, addresses,
// and object content from the Evidence detail surface.
type UIEvidenceResource struct {
	Type       domain.ResourceType
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func (resource UIEvidenceResource) valid(scope domain.ScopeSnapshot) bool {
	reference := domain.ResourceRef{
		APIVersion: resource.APIVersion,
		Kind:       resource.Kind,
		Namespace:  resource.Namespace,
		Name:       resource.Name,
	}
	return scope.Validate() == nil && resource.Type.Validate() == nil &&
		domain.ValidateResourceRefForType(reference, resource.Type) == nil
}

// UIEvidenceDetail is an allowlisted, bounded derivative. It can never carry a
// raw Tool result, Kubernetes object, log payload, or model response.
type UIEvidenceDetail struct {
	Category         domain.EvidenceCategory
	SourcePath       string
	Resource         UIEvidenceResource
	PolicyVersion    string
	PolicyGeneration domain.PolicyGeneration
	ObservedAt       time.Time
	Truncated        bool
	SensitiveFilter  UIEvidenceSensitiveFilter
	Projection       string
}

func (detail UIEvidenceDetail) valid(reference UIEvidenceReference) bool {
	resource := domain.ResourceRef{
		APIVersion: detail.Resource.APIVersion,
		Kind:       detail.Resource.Kind,
		Namespace:  detail.Resource.Namespace,
		Name:       detail.Resource.Name,
	}
	return detail.Category.Valid() && allowedUIEvidenceSourcePath(detail.Category, resource, detail.PolicyVersion, detail.SourcePath) &&
		detail.Resource.valid(reference.Scope) && validCoordinatorTime(detail.ObservedAt) &&
		detail.ObservedAt.Location() == time.UTC &&
		detail.ObservedAt.Equal(detail.ObservedAt.UTC().Truncate(time.Millisecond)) &&
		detail.SensitiveFilter.valid() && validUIBoundedText(detail.Projection, 1, MaxUIEvidenceProjectionBytes) &&
		validEvidencePolicyBinding(detail.PolicyVersion, detail.PolicyGeneration)
}

// UIEvidenceDetailResult echoes every asynchronous identity. Expired and
// unavailable states intentionally carry no detail payload.
type UIEvidenceDetailResult struct {
	RequestID uint64
	Reference UIEvidenceReference
	Detail    *UIEvidenceDetail
}

// Validate checks request identity and the exclusive safe result shape.
func (result UIEvidenceDetailResult) Validate() error {
	if result.RequestID == 0 || result.Reference.Validate() != nil {
		return ErrInvalidUIQueryResult
	}
	switch result.Reference.State {
	case UIEvidenceDetailExpired, UIEvidenceDetailUnavailable:
		if result.Detail != nil {
			return ErrInvalidUIQueryResult
		}
	case UIEvidenceDetailAvailable:
		if result.Detail == nil || !result.Detail.valid(result.Reference) || result.Detail.Truncated {
			return ErrInvalidUIQueryResult
		}
	case UIEvidenceDetailPartial:
		if result.Detail == nil || !result.Detail.valid(result.Reference) || !result.Detail.Truncated {
			return ErrInvalidUIQueryResult
		}
	default:
		return ErrInvalidUIQueryResult
	}
	return nil
}

// QueryEvidenceDetail resolves one citation through accepted in-memory data or
// the retained safe repositories. It never reads a raw payload source.
func (coordinator *Coordinator) QueryEvidenceDetail(
	ctx context.Context,
	query UIEvidenceDetailQuery,
) (UIEvidenceDetailResult, error) {
	if coordinator == nil || ctx == nil || query.Validate() != nil {
		return UIEvidenceDetailResult{}, ErrInvalidUIQuery
	}
	if err := ctx.Err(); err != nil {
		return UIEvidenceDetailResult{}, err
	}
	if err := coordinator.beginUIOperation(false); err != nil {
		return UIEvidenceDetailResult{}, err
	}
	defer coordinator.finishOperation()

	result := UIEvidenceDetailResult{RequestID: query.RequestID, Reference: query.Reference}
	diagnosis, evidence, found := coordinator.inMemoryEvidenceDetail(query.Reference)
	if !found {
		var outcome evidenceLookupOutcome
		diagnosis, evidence, outcome = coordinator.retainedEvidenceDetail(ctx, query.Reference)
		if err := ctx.Err(); err != nil {
			return UIEvidenceDetailResult{}, err
		}
		switch outcome {
		case evidenceLookupExpired:
			result.Reference.State = UIEvidenceDetailExpired
			return result, nil
		case evidenceLookupUnavailable:
			result.Reference.State = UIEvidenceDetailUnavailable
			return result, nil
		case evidenceLookupFound:
		default:
			return UIEvidenceDetailResult{}, ErrInvalidUIQueryResult
		}
	}

	detail, state, ok := coordinator.projectEvidenceDetail(diagnosis, evidence)
	if !ok {
		result.Reference.State = UIEvidenceDetailUnavailable
		return result, nil
	}
	result.Reference.State = state
	result.Detail = &detail
	if result.Validate() != nil {
		return UIEvidenceDetailResult{}, ErrInvalidUIQueryResult
	}
	return result, nil
}

type evidenceLookupOutcome uint8

const (
	evidenceLookupFound evidenceLookupOutcome = iota + 1
	evidenceLookupExpired
	evidenceLookupUnavailable
)

func (coordinator *Coordinator) retainedEvidenceDetail(
	ctx context.Context,
	reference UIEvidenceReference,
) (domain.Diagnosis, domain.Evidence, evidenceLookupOutcome) {
	if coordinator.evidenceDetails == nil {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupUnavailable
	}
	diagnosis, found, err := coordinator.evidenceDetails.ReadDiagnosis(ctx, reference.RunID)
	if err != nil {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupUnavailable
	}
	if !found {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupExpired
	}
	if diagnosis.Validate() != nil || diagnosis.RunID != reference.RunID || diagnosis.Scope != reference.Scope ||
		!diagnosisReferencesEvidence(diagnosis, reference.EvidenceID) {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupUnavailable
	}
	if diagnosis.EvidenceDetailsState == domain.EvidenceDetailExpired {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupExpired
	}
	evidence, found, err := coordinator.evidenceDetails.ReadEvidence(ctx, reference.EvidenceID)
	if err != nil {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupUnavailable
	}
	if !found {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupExpired
	}
	if evidence.Validate() != nil || evidence.ID != reference.EvidenceID || evidence.RunID != reference.RunID ||
		evidence.Scope != reference.Scope {
		return domain.Diagnosis{}, domain.Evidence{}, evidenceLookupUnavailable
	}
	return diagnosis, evidence, evidenceLookupFound
}

func (coordinator *Coordinator) inMemoryEvidenceDetail(
	reference UIEvidenceReference,
) (domain.Diagnosis, domain.Evidence, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active != nil && coordinator.active.terminal && coordinator.active.diagnosis != nil &&
		coordinator.active.diagnosis.RunID == reference.RunID && coordinator.active.diagnosis.Scope == reference.Scope &&
		diagnosisReferencesEvidence(*coordinator.active.diagnosis, reference.EvidenceID) {
		if evidence, found := evidenceFromTools(coordinator.active.tools, reference.EvidenceID); found {
			return cloneDiagnosis(*coordinator.active.diagnosis), cloneEvidence(evidence), true
		}
	}
	if coordinator.lastDiagnosis == nil || coordinator.lastDiagnosis.RunID != reference.RunID ||
		coordinator.lastDiagnosis.Scope != reference.Scope || !diagnosisReferencesEvidence(*coordinator.lastDiagnosis, reference.EvidenceID) {
		return domain.Diagnosis{}, domain.Evidence{}, false
	}
	evidence, found := coordinator.lastEvidence[reference.EvidenceID]
	if !found {
		return domain.Diagnosis{}, domain.Evidence{}, false
	}
	return cloneDiagnosis(*coordinator.lastDiagnosis), cloneEvidence(evidence), true
}

func (coordinator *Coordinator) projectEvidenceDetail(
	diagnosis domain.Diagnosis,
	evidence domain.Evidence,
) (UIEvidenceDetail, UIEvidenceDetailState, bool) {
	if diagnosis.Validate() != nil || evidence.Validate() != nil || evidence.RunID != diagnosis.RunID ||
		evidence.Scope != diagnosis.Scope || !diagnosisReferencesEvidence(diagnosis, evidence.ID) ||
		evidence.SourcePath == nil ||
		!allowedUIEvidenceSourcePath(evidence.Category, evidence.Resource, evidence.PolicyVersion, *evidence.SourcePath) {
		return UIEvidenceDetail{}, "", false
	}
	addressFiltered, addressRedactions := filterUIEvidenceAddresses(evidence.Fact)
	processed, err := coordinator.questions.Process(addressFiltered, MaxUIEvidenceProjectionBytes)
	if err != nil || processed.Value == "" {
		return UIEvidenceDetail{}, "", false
	}
	filter := UIEvidenceSensitiveFilterNotApplied
	if evidence.RedactionCount > 0 || processed.RedactionCount > 0 || addressRedactions > 0 {
		filter = UIEvidenceSensitiveFilterApplied
	}
	truncated := evidence.Truncated || evidence.Partial || processed.Truncated
	state := UIEvidenceDetailAvailable
	if truncated {
		state = UIEvidenceDetailPartial
	}
	resourceType := evidence.ResourceType
	if resourceType == (domain.ResourceType{}) {
		kind, found := domain.ResourceKindForReference(evidence.Resource)
		if !found {
			return UIEvidenceDetail{}, "", false
		}
		resourceType = domain.BuiltInResourceType(kind)
	}
	detail := UIEvidenceDetail{
		Category:   evidence.Category,
		SourcePath: *evidence.SourcePath,
		Resource: UIEvidenceResource{
			Type:       resourceType,
			APIVersion: evidence.Resource.APIVersion,
			Kind:       evidence.Resource.Kind,
			Namespace:  evidence.Resource.Namespace,
			Name:       evidence.Resource.Name,
		},
		PolicyVersion: evidence.PolicyVersion, PolicyGeneration: evidence.PolicyGeneration,
		ObservedAt: evidence.ObservedAt.UTC().Truncate(time.Millisecond),
		Truncated:  truncated, SensitiveFilter: filter, Projection: processed.Value,
	}
	if !detail.valid(UIEvidenceReference{Scope: evidence.Scope}) {
		return UIEvidenceDetail{}, "", false
	}
	return detail, state, true
}

func filterUIEvidenceAddresses(value string) (string, int) {
	count := 0
	filtered := uiEvidenceAddressCandidate.ReplaceAllStringFunc(value, func(candidate string) string {
		withoutPunctuation := strings.TrimRight(candidate, ".")
		punctuation := candidate[len(withoutPunctuation):]
		trimmed := strings.Trim(withoutPunctuation, "[]")
		if address, err := netip.ParseAddr(trimmed); err == nil && address.IsValid() {
			count++
			return "[FILTERED]" + punctuation
		}
		if address, err := netip.ParseAddrPort(withoutPunctuation); err == nil && address.IsValid() {
			count++
			return "[FILTERED]" + punctuation
		}
		return candidate
	})
	return filtered, count
}

func diagnosisReferencesEvidence(diagnosis domain.Diagnosis, evidenceID domain.EvidenceID) bool {
	ids := diagnosis.ReferencedEvidenceIDs()
	for _, id := range ids {
		if id == evidenceID {
			return true
		}
	}
	return false
}

func evidenceFromTools(
	tools map[domain.ToolInvocationID]*pendingTool,
	evidenceID domain.EvidenceID,
) (domain.Evidence, bool) {
	for _, pending := range tools {
		if pending == nil {
			continue
		}
		for _, evidence := range pending.evidence {
			if evidence.ID == evidenceID {
				return evidence, true
			}
		}
	}
	return domain.Evidence{}, false
}

func allowedUIEvidenceSourcePath(
	category domain.EvidenceCategory,
	resource domain.ResourceRef,
	policyVersion string,
	source string,
) bool {
	if policyVersion == domain.RemoteDiagnosticsPolicyVersion {
		switch category {
		case domain.EvidenceCategoryRemoteCommand:
			return resource.APIVersion == "v1" && resource.Kind == "Pod" && resource.Namespace != "" &&
				source == "api/v1/namespaces/"+resource.Namespace+"/pods/"+resource.Name+"/exec"
		case domain.EvidenceCategoryContainerFile:
			if resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace == "" {
				return false
			}
			_, err := domain.ContainerFilePathComponents(source)
			return err == nil
		case domain.EvidenceCategoryDiagnosticPod:
			return resource.APIVersion == "v1" && resource.Kind == "Service" && resource.Namespace != "" &&
				source == "api/v1/namespaces/"+resource.Namespace+"/services/"+resource.Name+"#diagnostic_pod"
		default:
			return false
		}
	}
	if policyVersion == domain.ObservabilityPolicyVersion {
		switch category {
		case domain.EvidenceCategoryEvent:
			expected := "api/v1/events"
			if resource.Namespace != "" {
				expected = "api/v1/namespaces/" + resource.Namespace + "/events"
			}
			return source == expected
		case domain.EvidenceCategoryLogExcerpt:
			prefix := "api/v1/namespaces/" + resource.Namespace + "/pods/" + resource.Name + "/log#"
			if resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace == "" || !strings.HasPrefix(source, prefix) {
				return false
			}
			instanceAndContainer := strings.TrimPrefix(source, prefix)
			instance, container, found := strings.Cut(instanceAndContainer, ":")
			return found && (instance == "current" || instance == "previous") && domain.ValidResourceName(container)
		case domain.EvidenceCategoryMetricSnapshot:
			expected := ""
			switch {
			case resource.APIVersion == "v1" && resource.Kind == "Node" && resource.Namespace == "":
				expected = "apis/metrics.k8s.io/v1beta1/nodes/" + resource.Name
			case resource.APIVersion == "v1" && resource.Kind == "Pod" && resource.Namespace != "":
				expected = "apis/metrics.k8s.io/v1beta1/namespaces/" + resource.Namespace + "/pods/" + resource.Name
			}
			return expected != "" && source == expected
		case domain.EvidenceCategoryPrometheus:
			if resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace == "" {
				return false
			}
			for _, queryID := range []domain.ObservabilityQueryID{
				domain.QueryPrometheusPodCPUUsage,
				domain.QueryPrometheusPodMemoryWorkingSet,
				domain.QueryPrometheusPodNetworkReceiveRate,
				domain.QueryPrometheusPodNetworkTransmitRate,
			} {
				if source == "api/v1/query_range#"+string(queryID) {
					return true
				}
			}
			return false
		case domain.EvidenceCategoryLoki:
			return resource.APIVersion == "v1" && resource.Kind == "Pod" && resource.Namespace != "" &&
				source == "loki/api/v1/query_range#"+string(domain.QueryLokiPodLogs)
		default:
			return false
		}
	}
	if policyVersion != "" && policyVersion != domain.ResourcePolicyVersion {
		return false
	}
	switch source {
	case "projected.status",
		"projected.status.conditions",
		"projected.status.container_statuses",
		"projected.spec.ports",
		"projected.metadata.controller_owner",
		"projected.events",
		"projected.logs.current",
		"projected.logs.previous",
		"projected.related.nodes.status",
		"projected.related.edges.owner_reference",
		"projected.related.edges.selector_match",
		"projected.related.edges.service_endpoints":
		return category != domain.EvidenceCategoryMetricSnapshot && category != domain.EvidenceCategoryPrometheus &&
			category != domain.EvidenceCategoryLoki
	default:
		return category != domain.EvidenceCategoryMetricSnapshot && category != domain.EvidenceCategoryPrometheus &&
			category != domain.EvidenceCategoryLoki && domain.ValidResourceProjectionPath(source) &&
			!domain.ResourceProjectionPathRequiresSensitiveClass(source)
	}
}

func validEvidencePolicyBinding(version string, generation domain.PolicyGeneration) bool {
	if version == "" {
		return generation == 0
	}
	return generation.Valid() && (version == domain.ResourcePolicyVersion || version == domain.ObservabilityPolicyVersion || version == domain.RemoteDiagnosticsPolicyVersion)
}

func uiEvidenceState(state domain.EvidenceDetailState) UIEvidenceDetailState {
	switch state {
	case domain.EvidenceDetailPartial:
		return UIEvidenceDetailPartial
	case domain.EvidenceDetailExpired:
		return UIEvidenceDetailExpired
	default:
		return UIEvidenceDetailAvailable
	}
}

func projectUIEvidenceReferences(diagnosis domain.Diagnosis, sequence int64) []UIEvidenceReference {
	ids := diagnosis.ReferencedEvidenceIDs()
	result := make([]UIEvidenceReference, 0, len(ids))
	for _, id := range ids {
		claims := make([]int, 0, len(diagnosis.ClaimCoverage))
		claimKinds := make([]domain.ClaimKind, 0, len(diagnosis.ClaimCoverage))
		for _, claim := range diagnosis.ClaimCoverage {
			for _, claimEvidenceID := range claim.EvidenceIDs {
				if claimEvidenceID == id {
					claims = append(claims, claim.Sequence)
					claimKinds = append(claimKinds, claim.Kind)
					break
				}
			}
		}
		result = append(result, UIEvidenceReference{
			EvidenceID:     id,
			RunID:          diagnosis.RunID,
			Scope:          diagnosis.Scope,
			Sequence:       sequence,
			State:          uiEvidenceState(diagnosis.EvidenceDetailsState),
			ClaimSequences: claims,
			ClaimKinds:     claimKinds,
		})
	}
	return result
}
