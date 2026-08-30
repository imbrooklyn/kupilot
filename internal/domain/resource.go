package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxResourceSummaries is the non-expandable item ceiling for one bounded
	// Namespace or resource summary read.
	MaxResourceSummaries = 50

	maxResourceStatusBytes = 256
	maxResourceOwners      = 1
)

var (
	// ErrInvalidClusterScope reports an invalid live scope value.
	ErrInvalidClusterScope = errors.New("ClusterScope data is invalid")
	// ErrInvalidNamespaceList reports an invalid bounded Namespace projection.
	ErrInvalidNamespaceList = errors.New("Namespace list data is invalid")
	// ErrInvalidResourceSummary reports an invalid bounded resource projection.
	ErrInvalidResourceSummary = errors.New("resource summary data is invalid")
)

// NamespaceAccessPolicy is the immutable per-run policy for namespaced reads.
// It never changes Context authority and never treats an empty Namespace as an
// all-Namespace request.
type NamespaceAccessPolicy string

const (
	NamespaceAccessCurrent NamespaceAccessPolicy = "current"
	NamespaceAccessAll     NamespaceAccessPolicy = "all"
)

// Valid reports whether the policy is one code-defined value.
func (policy NamespaceAccessPolicy) Valid() bool {
	return policy == NamespaceAccessCurrent || policy == NamespaceAccessAll
}

// ResourceKind is one directly addressable built-in target Kind.
type ResourceKind string

const (
	ResourceKindNamespace               ResourceKind = "Namespace"
	ResourceKindNode                    ResourceKind = "Node"
	ResourceKindPod                     ResourceKind = "Pod"
	ResourceKindService                 ResourceKind = "Service"
	ResourceKindPersistentVolumeClaim   ResourceKind = "PersistentVolumeClaim"
	ResourceKindPersistentVolume        ResourceKind = "PersistentVolume"
	ResourceKindConfigMap               ResourceKind = "ConfigMap"
	ResourceKindDeployment              ResourceKind = "Deployment"
	ResourceKindReplicaSet              ResourceKind = "ReplicaSet"
	ResourceKindStatefulSet             ResourceKind = "StatefulSet"
	ResourceKindDaemonSet               ResourceKind = "DaemonSet"
	ResourceKindJob                     ResourceKind = "Job"
	ResourceKindCronJob                 ResourceKind = "CronJob"
	ResourceKindIngress                 ResourceKind = "Ingress"
	ResourceKindHorizontalPodAutoscaler ResourceKind = "HorizontalPodAutoscaler"
	ResourceKindPodDisruptionBudget     ResourceKind = "PodDisruptionBudget"
)

// Valid reports whether the Kind is directly addressable by the built-in
// operational capability catalog.
func (kind ResourceKind) Valid() bool {
	switch kind {
	case ResourceKindNamespace,
		ResourceKindNode,
		ResourceKindPod,
		ResourceKindService,
		ResourceKindPersistentVolumeClaim,
		ResourceKindPersistentVolume,
		ResourceKindConfigMap,
		ResourceKindDeployment,
		ResourceKindReplicaSet,
		ResourceKindStatefulSet,
		ResourceKindDaemonSet,
		ResourceKindJob,
		ResourceKindCronJob,
		ResourceKindIngress,
		ResourceKindHorizontalPodAutoscaler,
		ResourceKindPodDisruptionBudget:
		return true
	default:
		return false
	}
}

// APIVersion returns the one code-defined stable API version for the Kind.
func (kind ResourceKind) APIVersion() string {
	switch kind {
	case ResourceKindNamespace, ResourceKindNode, ResourceKindPod, ResourceKindService,
		ResourceKindPersistentVolumeClaim, ResourceKindPersistentVolume, ResourceKindConfigMap:
		return "v1"
	case ResourceKindDeployment, ResourceKindReplicaSet, ResourceKindStatefulSet, ResourceKindDaemonSet:
		return "apps/v1"
	case ResourceKindJob, ResourceKindCronJob:
		return "batch/v1"
	case ResourceKindIngress:
		return "networking.k8s.io/v1"
	case ResourceKindHorizontalPodAutoscaler:
		return "autoscaling/v2"
	case ResourceKindPodDisruptionBudget:
		return "policy/v1"
	default:
		return ""
	}
}

// Namespaced reports whether the Kubernetes resource is Namespace-scoped.
func (kind ResourceKind) Namespaced() bool {
	if !kind.Valid() {
		return false
	}
	switch kind {
	case ResourceKindNamespace, ResourceKindNode, ResourceKindPersistentVolume:
		return false
	default:
		return true
	}
}

// ClusterScoped reports whether the Kubernetes resource is cluster-scoped.
func (kind ResourceKind) ClusterScoped() bool {
	return kind.Valid() && !kind.Namespaced()
}

// ResourceKindForReference returns the fixed Kind only when API version and
// Kind form one admitted direct target.
func ResourceKindForReference(reference ResourceRef) (ResourceKind, bool) {
	kind := ResourceKind(reference.Kind)
	return kind, kind.Valid() && reference.APIVersion == kind.APIVersion()
}

// ReferenceMatchesWorkingNamespace reports whether a selectable or durable
// reference belongs to the working Namespace, or is correctly cluster-scoped.
// It does not authorize cross-Namespace runtime reads.
func ReferenceMatchesWorkingNamespace(reference ResourceRef, namespace string) bool {
	kind, ok := ResourceKindForReference(reference)
	return ok && ValidNamespaceName(namespace) &&
		(kind.ClusterScoped() && reference.Namespace == "" || kind.Namespaced() && reference.Namespace == namespace)
}

// ClusterScope is immutable live scope authority for one process generation.
// It contains no client, endpoint, credential, path, context.Context, or callback.
type ClusterScope struct {
	Context         string
	Namespace       string
	NamespaceAccess NamespaceAccessPolicy
	Generation      int64
	ActivatedAt     time.Time
}

// Validate checks a complete live scope without consulting external state.
func (scope ClusterScope) Validate() error {
	if !ValidContextName(scope.Context) || !ValidNamespaceName(scope.Namespace) || !scope.NamespaceAccess.Valid() ||
		scope.Generation < 1 || scope.ActivatedAt.IsZero() ||
		scope.ActivatedAt.Location() != time.UTC || scope.ActivatedAt.UnixMilli() < 0 {
		return ErrInvalidClusterScope
	}
	return nil
}

// AllowsReference reports whether one already-valid reference is admitted by
// this immutable live scope. Cluster-scoped references are independent of the
// working Namespace; namespaced references remain governed by the frozen
// current/all policy.
func (scope ClusterScope) AllowsReference(reference ResourceRef) bool {
	kind, ok := ResourceKindForReference(reference)
	if scope.Validate() != nil || !ok || ValidateLiveResourceRef(reference) != nil {
		return false
	}
	return kind.ClusterScoped() || scope.NamespaceAccess == NamespaceAccessAll || reference.Namespace == scope.Namespace
}

// AllowsAllNamespaces reports whether an explicit bounded all-Namespace LIST
// is permitted for a namespaced Kind.
func (scope ClusterScope) AllowsAllNamespaces(kind ResourceKind) bool {
	return scope.Validate() == nil && kind.Namespaced() && scope.NamespaceAccess == NamespaceAccessAll
}

// Snapshot returns persistence-safe historic metadata without activation time.
func (scope ClusterScope) Snapshot() ScopeSnapshot {
	return ScopeSnapshot{
		Context:    scope.Context,
		Namespace:  scope.Namespace,
		Generation: scope.Generation,
	}
}

// ValidContextName reports whether a safe local Context display name can be
// used in a live scope. It does not claim that kubeconfig still contains it.
func ValidContextName(value string) bool {
	if value == "" || len(value) > maxContextBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) || resourceBidirectionalControl(current) {
			return false
		}
	}
	return true
}

// ValidNamespaceName enforces one non-empty DNS-1123 label. An empty value is
// never interpreted as all Namespaces.
func ValidNamespaceName(value string) bool {
	if value == "" || len(value) > maxNamespaceBytes || !lowerAlphaNumericByte(value[0]) || !lowerAlphaNumericByte(value[len(value)-1]) {
		return false
	}
	for _, current := range []byte(value) {
		if lowerAlphaNumericByte(current) || current == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidResourceName enforces one bounded DNS-1123 subdomain.
func ValidResourceName(value string) bool {
	if value == "" || len(value) > maxResourceNameBytes || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !ValidNamespaceName(label) {
			return false
		}
	}
	return true
}

// ValidateLiveResourceRef checks the stricter current-scope identity contract.
func ValidateLiveResourceRef(reference ResourceRef) error {
	if reference.Validate() != nil || !ValidResourceName(reference.Name) ||
		!validSafeOptionalText(reference.UID, maxResourceUIDBytes) ||
		!validSafeOptionalText(reference.ResourceVersion, maxResourceVersionBytes) {
		return ErrInvalidResourceRef
	}
	return nil
}

// NamespaceSummary is the complete safe Namespace picker projection.
type NamespaceSummary struct {
	Name string
}

// NamespaceList is a bounded deterministic Namespace result.
type NamespaceList struct {
	Items     []NamespaceSummary
	Truncated bool
}

// Validate checks item bounds, names, and deterministic uniqueness.
func (list NamespaceList) Validate() error {
	if len(list.Items) > MaxResourceSummaries {
		return ErrInvalidNamespaceList
	}
	seen := make(map[string]struct{}, len(list.Items))
	for _, item := range list.Items {
		if !ValidNamespaceName(item.Name) {
			return ErrInvalidNamespaceList
		}
		if _, exists := seen[item.Name]; exists {
			return ErrInvalidNamespaceList
		}
		seen[item.Name] = struct{}{}
	}
	return nil
}

// OptionalCount distinguishes an unknown count from a legitimate zero.
type OptionalCount struct {
	Value   int32
	Present bool
}

// Count returns one present non-negative count value.
func Count(value int32) OptionalCount {
	return OptionalCount{Value: value, Present: true}
}

func (value OptionalCount) valid() bool {
	return value.Present && value.Value >= 0 || !value.Present && value.Value == 0
}

// ResourceStatus is a fixed cross-Kind summary, not an object status body.
type ResourceStatus struct {
	Phase       string
	Reason      string
	ServiceType string
	Ready       OptionalCount
	Desired     OptionalCount
	Available   OptionalCount
	Active      OptionalCount
	Succeeded   OptionalCount
	Failed      OptionalCount
}

func (status ResourceStatus) valid() bool {
	return validSafeOptionalText(status.Phase, maxResourceStatusBytes) &&
		validSafeOptionalText(status.Reason, maxResourceStatusBytes) &&
		validSafeOptionalText(status.ServiceType, maxResourceStatusBytes) &&
		status.Ready.valid() && status.Desired.valid() && status.Available.valid() &&
		status.Active.valid() && status.Succeeded.valid() && status.Failed.valid()
}

// ResourceOwner is one allowlisted controller reference projected from an
// already-read object. ReferenceOnly forbids a follow-up query.
type ResourceOwner struct {
	APIVersion    string
	Kind          string
	Name          string
	UID           string
	ReferenceOnly bool
}

func (owner ResourceOwner) validFor(child ResourceKind) bool {
	if !ValidResourceName(owner.Name) || !validSafeOptionalText(owner.UID, maxResourceUIDBytes) {
		return false
	}
	switch child {
	case ResourceKindPod:
		return !owner.ReferenceOnly &&
			(owner.APIVersion == "apps/v1" && (owner.Kind == "ReplicaSet" || owner.Kind == "StatefulSet" || owner.Kind == "DaemonSet") ||
				owner.APIVersion == "batch/v1" && owner.Kind == "Job")
	case ResourceKindReplicaSet:
		return !owner.ReferenceOnly && owner.APIVersion == "apps/v1" && owner.Kind == "Deployment"
	case ResourceKindJob:
		return !owner.ReferenceOnly && owner.APIVersion == "batch/v1" && owner.Kind == "CronJob"
	default:
		return false
	}
}

// ResourceSummary is the safe bounded projection returned by current-scope
// GET and LIST operations. It contains no labels, annotations, selectors,
// environment, volumes, addresses, messages, or raw Kubernetes values.
type ResourceSummary struct {
	Reference ResourceRef
	CreatedAt time.Time
	Status    ResourceStatus
	Owners    []ResourceOwner
}

// Validate checks the complete projection and its fixed owner relationships.
func (summary ResourceSummary) Validate() error {
	kind, ok := ResourceKindForReference(summary.Reference)
	if !ok || ValidateLiveResourceRef(summary.Reference) != nil || !summary.Status.valid() || len(summary.Owners) > maxResourceOwners {
		return ErrInvalidResourceSummary
	}
	if !summary.CreatedAt.IsZero() && (summary.CreatedAt.Location() != time.UTC || summary.CreatedAt.UnixMilli() < 0) {
		return ErrInvalidResourceSummary
	}
	for _, owner := range summary.Owners {
		if !owner.validFor(kind) {
			return ErrInvalidResourceSummary
		}
	}
	return nil
}

// ResourceList is a bounded deterministic typed-read result. Item references
// carry their exact Namespace, or none for cluster-scoped Kinds.
type ResourceList struct {
	Items     []ResourceSummary
	Truncated bool
}

// Validate checks every safe item and the non-expandable item ceiling.
func (list ResourceList) Validate() error {
	if len(list.Items) > MaxResourceSummaries {
		return ErrInvalidResourceSummary
	}
	seen := make(map[ResourceRef]struct{}, len(list.Items))
	for _, item := range list.Items {
		if item.Validate() != nil {
			return ErrInvalidResourceSummary
		}
		if _, exists := seen[item.Reference]; exists {
			return ErrInvalidResourceSummary
		}
		seen[item.Reference] = struct{}{}
	}
	return nil
}

func lowerAlphaNumericByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func validSafeOptionalText(value string, maximumBytes int) bool {
	if value == "" {
		return true
	}
	if len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) || resourceBidirectionalControl(current) {
			return false
		}
	}
	return true
}

func resourceBidirectionalControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}
