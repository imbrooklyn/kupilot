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

// ResourceKind is one directly addressable v0.1 target Kind.
type ResourceKind string

const (
	ResourceKindPod        ResourceKind = "Pod"
	ResourceKindDeployment ResourceKind = "Deployment"
	ResourceKindReplicaSet ResourceKind = "ReplicaSet"
	ResourceKindJob        ResourceKind = "Job"
	ResourceKindService    ResourceKind = "Service"
)

// Valid reports whether the Kind is directly addressable in v0.1.
func (kind ResourceKind) Valid() bool {
	switch kind {
	case ResourceKindPod,
		ResourceKindDeployment,
		ResourceKindReplicaSet,
		ResourceKindJob,
		ResourceKindService:
		return true
	default:
		return false
	}
}

// APIVersion returns the one code-defined stable API version for the Kind.
func (kind ResourceKind) APIVersion() string {
	switch kind {
	case ResourceKindPod, ResourceKindService:
		return "v1"
	case ResourceKindDeployment, ResourceKindReplicaSet:
		return "apps/v1"
	case ResourceKindJob:
		return "batch/v1"
	default:
		return ""
	}
}

// ResourceKindForReference returns the fixed Kind only when API version and
// Kind form one admitted direct target.
func ResourceKindForReference(reference ResourceRef) (ResourceKind, bool) {
	kind := ResourceKind(reference.Kind)
	return kind, kind.Valid() && reference.APIVersion == kind.APIVersion()
}

// ClusterScope is immutable live scope authority for one process generation.
// It contains no client, endpoint, credential, path, context.Context, or callback.
type ClusterScope struct {
	Context     string
	Namespace   string
	Generation  int64
	ActivatedAt time.Time
}

// Validate checks a complete live scope without consulting external state.
func (scope ClusterScope) Validate() error {
	if !ValidContextName(scope.Context) || !ValidNamespaceName(scope.Namespace) ||
		scope.Generation < 1 || scope.ActivatedAt.IsZero() ||
		scope.ActivatedAt.Location() != time.UTC || scope.ActivatedAt.UnixMilli() < 0 {
		return ErrInvalidClusterScope
	}
	return nil
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
	if reference.Validate() != nil || !ValidNamespaceName(reference.Namespace) || !ValidResourceName(reference.Name) ||
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
		if owner.APIVersion == "apps/v1" && owner.Kind == "StatefulSet" {
			return owner.ReferenceOnly
		}
		return !owner.ReferenceOnly &&
			(owner.APIVersion == "apps/v1" && owner.Kind == "ReplicaSet" ||
				owner.APIVersion == "batch/v1" && owner.Kind == "Job")
	case ResourceKindReplicaSet:
		return !owner.ReferenceOnly && owner.APIVersion == "apps/v1" && owner.Kind == "Deployment"
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

// ResourceList is a bounded deterministic current-Namespace result.
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
