package domain

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// ResourcePolicyVersion is the code-owned policy contract shared by every
	// built-in read and by exact configured custom-resource entries.
	ResourcePolicyVersion = "kupilot-resource-policy-v1"

	MaxResourcePolicyEntries = 64
	MaxResourceFields        = 32
	MaxResourceFilters       = 8
	MaxResourceQueryPages    = 8
	MaxResourcePageItems     = 100
	MaxResourcePageBytes     = 1 * 1024 * 1024
	MaxResourceQueryItems    = 500
	MaxResourceQueryBytes    = 4 * 1024 * 1024

	maxResourceTypeIDBytes  = 63
	maxAPIGroupBytes        = 253
	maxAPIVersionPartBytes  = 63
	maxAPIResourceBytes     = 253
	maxResourceFieldIDBytes = 63
	maxResourcePathBytes    = 512
	maxResourceFilterBytes  = 512
)

var (
	ErrInvalidResourceType   = errors.New("ResourceType data is invalid")
	ErrInvalidResourcePolicy = errors.New("resource policy data is invalid")
	ErrInvalidResourceQuery  = errors.New("ResourceQuery data is invalid")
	ErrInvalidResourcePage   = errors.New("ResourcePage data is invalid")
	ErrResourcePolicyDenied  = errors.New("the resource request is denied by policy")
)

// ResourceScope is the API-declared scope of one exact resource type.
type ResourceScope string

const (
	ResourceScopeNamespaced ResourceScope = "namespaced"
	ResourceScopeCluster    ResourceScope = "cluster"
)

func (scope ResourceScope) Valid() bool {
	return scope == ResourceScopeNamespaced || scope == ResourceScopeCluster
}

// ResourceVerb is a read-only Kubernetes API verb. No write verb is part of
// this contract.
type ResourceVerb string

const (
	ResourceVerbGet  ResourceVerb = "get"
	ResourceVerbList ResourceVerb = "list"
)

func (verb ResourceVerb) Valid() bool {
	return verb == ResourceVerbGet || verb == ResourceVerbList
}

// ResourceView selects one code-defined presentation over an admitted read.
// Describe is a normalized projection, never a kubectl subprocess.
type ResourceView string

const (
	ResourceViewSummary  ResourceView = "summary"
	ResourceViewDescribe ResourceView = "describe"
	ResourceViewList     ResourceView = "list"
	ResourceViewCount    ResourceView = "count"
	ResourceViewTable    ResourceView = "table"
)

func (view ResourceView) Valid() bool {
	switch view {
	case ResourceViewSummary, ResourceViewDescribe, ResourceViewList, ResourceViewCount, ResourceViewTable:
		return true
	default:
		return false
	}
}

// ResourceDataClass records why one exact projected field is eligible. A
// sensitive declaration is valid policy metadata, but a caller still needs a
// separately admitted sensitive-read path before it can be requested.
type ResourceDataClass string

const (
	ResourceDataMetadata  ResourceDataClass = "metadata"
	ResourceDataStatus    ResourceDataClass = "status"
	ResourceDataSpec      ResourceDataClass = "spec"
	ResourceDataSensitive ResourceDataClass = "sensitive"
)

func (class ResourceDataClass) Valid() bool {
	return class == ResourceDataMetadata || class == ResourceDataStatus || class == ResourceDataSpec || class == ResourceDataSensitive
}

// ResourceScalarType is the closed set representable by a CRD projection.
// Collections and arbitrary objects are deliberately absent.
type ResourceScalarType string

const (
	ResourceScalarString    ResourceScalarType = "string"
	ResourceScalarInteger   ResourceScalarType = "integer"
	ResourceScalarDecimal   ResourceScalarType = "decimal"
	ResourceScalarBoolean   ResourceScalarType = "boolean"
	ResourceScalarTimestamp ResourceScalarType = "timestamp"
)

func (kind ResourceScalarType) Valid() bool {
	switch kind {
	case ResourceScalarString, ResourceScalarInteger, ResourceScalarDecimal, ResourceScalarBoolean, ResourceScalarTimestamp:
		return true
	default:
		return false
	}
}

// ResourceSelectorSource identifies the only server-side selector families
// the adapter may build from typed predicates.
type ResourceSelectorSource string

const (
	ResourceSelectorNone  ResourceSelectorSource = "none"
	ResourceSelectorField ResourceSelectorSource = "field"
	ResourceSelectorLabel ResourceSelectorSource = "label"
)

func (source ResourceSelectorSource) Valid() bool {
	return source == ResourceSelectorNone || source == ResourceSelectorField || source == ResourceSelectorLabel
}

// ResourceFilterOperator is interpreted by deterministic code. It is never
// concatenated as an arbitrary selector or expression.
type ResourceFilterOperator string

const (
	ResourceFilterEquals      ResourceFilterOperator = "equals"
	ResourceFilterNotEquals   ResourceFilterOperator = "not_equals"
	ResourceFilterContains    ResourceFilterOperator = "contains"
	ResourceFilterStartsWith  ResourceFilterOperator = "starts_with"
	ResourceFilterExists      ResourceFilterOperator = "exists"
	ResourceFilterGreaterThan ResourceFilterOperator = "greater_than"
	ResourceFilterLessThan    ResourceFilterOperator = "less_than"
)

func (operator ResourceFilterOperator) Valid() bool {
	switch operator {
	case ResourceFilterEquals, ResourceFilterNotEquals, ResourceFilterContains, ResourceFilterStartsWith,
		ResourceFilterExists, ResourceFilterGreaterThan, ResourceFilterLessThan:
		return true
	default:
		return false
	}
}

// ResourceType is the exact API identity behind one model-visible local ID.
// A model selects ID only; group/version/resource and scope come from policy.
type ResourceType struct {
	ID       string
	Group    string
	Version  string
	Resource string
	Kind     string
	Scope    ResourceScope
	BuiltIn  bool
}

func (resourceType ResourceType) Validate() error {
	if !validResourcePolicyToken(resourceType.ID, maxResourceTypeIDBytes, false) ||
		!validResourcePolicyToken(resourceType.Version, maxAPIVersionPartBytes, false) ||
		!validResourcePolicyToken(resourceType.Resource, maxAPIResourceBytes, false) ||
		!validResourceKind(resourceType.Kind) || !resourceType.Scope.Valid() ||
		resourceType.Group != "" && !validAPIGroup(resourceType.Group) {
		return ErrInvalidResourceType
	}
	if resourceType.BuiltIn {
		kind := ResourceKind(resourceType.Kind)
		if !kind.Valid() || resourceType != BuiltInResourceType(kind) {
			return ErrInvalidResourceType
		}
	} else if resourceType.Group == "" && resourceType != secretMetadataResourceType() {
		return ErrInvalidResourceType
	}
	return nil
}

// APIVersion returns Kubernetes' canonical apiVersion spelling.
func (resourceType ResourceType) APIVersion() string {
	if resourceType.Group == "" {
		return resourceType.Version
	}
	return resourceType.Group + "/" + resourceType.Version
}

func (resourceType ResourceType) Namespaced() bool {
	return resourceType.Validate() == nil && resourceType.Scope == ResourceScopeNamespaced
}

func (resourceType ResourceType) ClusterScoped() bool {
	return resourceType.Validate() == nil && resourceType.Scope == ResourceScopeCluster
}

// BuiltInResourceType returns the exact code-owned REST identity for one
// reviewed stable built-in Kind.
func BuiltInResourceType(kind ResourceKind) ResourceType {
	resourceType := ResourceType{Kind: string(kind), Version: "v1", BuiltIn: true}
	if kind.Namespaced() {
		resourceType.Scope = ResourceScopeNamespaced
	} else if kind.ClusterScoped() {
		resourceType.Scope = ResourceScopeCluster
	}
	switch kind {
	case ResourceKindNamespace:
		resourceType.ID, resourceType.Resource = "namespaces", "namespaces"
	case ResourceKindNode:
		resourceType.ID, resourceType.Resource = "nodes", "nodes"
	case ResourceKindPod:
		resourceType.ID, resourceType.Resource = "pods", "pods"
	case ResourceKindService:
		resourceType.ID, resourceType.Resource = "services", "services"
	case ResourceKindPersistentVolumeClaim:
		resourceType.ID, resourceType.Resource = "persistent-volume-claims", "persistentvolumeclaims"
	case ResourceKindPersistentVolume:
		resourceType.ID, resourceType.Resource = "persistent-volumes", "persistentvolumes"
	case ResourceKindConfigMap:
		resourceType.ID, resourceType.Resource = "config-maps", "configmaps"
	case ResourceKindDeployment:
		resourceType.ID, resourceType.Group, resourceType.Resource = "deployments", "apps", "deployments"
	case ResourceKindReplicaSet:
		resourceType.ID, resourceType.Group, resourceType.Resource = "replica-sets", "apps", "replicasets"
	case ResourceKindStatefulSet:
		resourceType.ID, resourceType.Group, resourceType.Resource = "stateful-sets", "apps", "statefulsets"
	case ResourceKindDaemonSet:
		resourceType.ID, resourceType.Group, resourceType.Resource = "daemon-sets", "apps", "daemonsets"
	case ResourceKindJob:
		resourceType.ID, resourceType.Group, resourceType.Resource = "jobs", "batch", "jobs"
	case ResourceKindCronJob:
		resourceType.ID, resourceType.Group, resourceType.Resource = "cron-jobs", "batch", "cronjobs"
	case ResourceKindIngress:
		resourceType.ID, resourceType.Group, resourceType.Resource = "ingresses", "networking.k8s.io", "ingresses"
	case ResourceKindHorizontalPodAutoscaler:
		resourceType.ID, resourceType.Group, resourceType.Version, resourceType.Resource = "horizontal-pod-autoscalers", "autoscaling", "v2", "horizontalpodautoscalers"
	case ResourceKindPodDisruptionBudget:
		resourceType.ID, resourceType.Group, resourceType.Resource = "pod-disruption-budgets", "policy", "poddisruptionbudgets"
	default:
		return ResourceType{}
	}
	return resourceType
}

// ResourceFieldPolicy binds a model-visible field ID to one exact scalar path
// and its only permitted selector semantics.
type ResourceFieldPolicy struct {
	ID             string
	Path           string
	Scalar         ResourceScalarType
	DataClass      ResourceDataClass
	SelectorSource ResourceSelectorSource
	SelectorKey    string
	Operators      []ResourceFilterOperator
	Evidence       bool
}

func (field ResourceFieldPolicy) Validate() error {
	if !validResourceFieldID(field.ID) || !ValidResourceProjectionPath(field.Path) ||
		!field.Scalar.Valid() || !field.DataClass.Valid() || !field.SelectorSource.Valid() ||
		len(field.Operators) > 4 || field.DataClass == ResourceDataSensitive && field.Evidence ||
		resourceFieldRequiresSensitiveClass(field) && field.DataClass != ResourceDataSensitive {
		return ErrInvalidResourcePolicy
	}
	if field.SelectorSource == ResourceSelectorNone {
		if field.SelectorKey != "" {
			return ErrInvalidResourcePolicy
		}
	} else {
		if !validSelectorKey(field.SelectorKey, field.SelectorSource) ||
			field.SelectorSource == ResourceSelectorField && field.Path != field.SelectorKey ||
			field.SelectorSource == ResourceSelectorLabel && field.Path != "metadata.labels."+field.ID {
			return ErrInvalidResourcePolicy
		}
	}
	previous := ResourceFilterOperator("")
	for _, operator := range field.Operators {
		if !operator.Valid() || previous != "" && string(operator) <= string(previous) ||
			!resourceOperatorValidForScalar(field.Scalar, operator) ||
			field.SelectorSource == ResourceSelectorField && operator != ResourceFilterEquals && operator != ResourceFilterNotEquals ||
			field.SelectorSource == ResourceSelectorLabel && operator != ResourceFilterEquals && operator != ResourceFilterNotEquals && operator != ResourceFilterExists {
			return ErrInvalidResourcePolicy
		}
		previous = operator
	}
	return nil
}

// AllowsFilter checks one typed predicate against this exact field policy.
// It performs no selector construction and grants no catalog authority.
func (field ResourceFieldPolicy) AllowsFilter(filter ResourceFilter) bool {
	if field.Validate() != nil || filter.Validate() != nil || filter.Field != field.ID ||
		field.DataClass == ResourceDataSensitive || !resourceOperatorAllowed(field.Operators, filter.Operator) {
		return false
	}
	if filter.Operator == ResourceFilterExists || field.Scalar == ResourceScalarString {
		return true
	}
	switch field.Scalar {
	case ResourceScalarInteger:
		_, err := strconv.ParseInt(filter.Value, 10, 64)
		return err == nil
	case ResourceScalarDecimal:
		value, err := strconv.ParseFloat(filter.Value, 64)
		return err == nil && !math.IsInf(value, 0) && !math.IsNaN(value)
	case ResourceScalarBoolean:
		return filter.Value == "true" || filter.Value == "false"
	case ResourceScalarTimestamp:
		_, err := time.Parse(time.RFC3339Nano, filter.Value)
		return err == nil
	default:
		return false
	}
}

func resourceFieldRequiresSensitiveClass(field ResourceFieldPolicy) bool {
	values := append([]string{field.ID}, strings.Split(field.Path, ".")...)
	for index, value := range values {
		normalized := strings.Map(func(current rune) rune {
			if current >= 'A' && current <= 'Z' {
				return current + ('a' - 'A')
			}
			if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
				return current
			}
			return -1
		}, value)
		for _, marker := range []string{"secret", "credential", "password", "token", "apikey", "privatekey", "clientkey", "accesskey", "authref", "certificateref"} {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
		if normalized == "env" || normalized == "envfrom" || normalized == "environment" ||
			normalized == "envvar" || normalized == "envvars" || normalized == "envvalue" || normalized == "envvalues" ||
			normalized == "environmentvariable" || normalized == "environmentvariables" || normalized == "environmentvalue" || normalized == "environmentvalues" ||
			index == 1 && (normalized == "data" || normalized == "binarydata" || normalized == "stringdata") {
			return true
		}
	}
	return false
}

// ResourceQueryLimits are independent per-call ceilings. They are intersected
// with the immutable run profile before a Kubernetes request.
type ResourceQueryLimits struct {
	MaxPages    int
	PageItems   int
	PageBytes   int
	MaxItems    int
	MaxBytes    int
	MaxReturned int
}

func (limits ResourceQueryLimits) Validate() error {
	if limits.MaxPages < 1 || limits.MaxPages > MaxResourceQueryPages ||
		limits.PageItems < 1 || limits.PageItems > MaxResourcePageItems ||
		limits.PageBytes < 1 || limits.PageBytes > MaxResourcePageBytes ||
		limits.MaxItems < 1 || limits.MaxItems > MaxResourceQueryItems ||
		limits.MaxBytes < 1 || limits.MaxBytes > MaxResourceQueryBytes ||
		limits.MaxReturned < 1 || limits.MaxReturned > MaxResourceSummaries ||
		limits.PageItems > limits.MaxItems || limits.PageBytes > limits.MaxBytes || limits.MaxReturned > limits.MaxItems {
		return ErrInvalidResourceQuery
	}
	return nil
}

// ResourcePolicy is one exact locally owned read entry. Fields, predicates,
// verbs, and ceilings are all explicit; discovery cannot add another entry.
type ResourcePolicy struct {
	Type   ResourceType
	Verbs  []ResourceVerb
	Fields []ResourceFieldPolicy
	Limits ResourceQueryLimits
}

func (policy ResourcePolicy) Validate() error {
	if policy.Type.Validate() != nil || len(policy.Verbs) == 0 || len(policy.Verbs) > 2 ||
		len(policy.Fields) == 0 || len(policy.Fields) > MaxResourceFields || policy.Limits.Validate() != nil {
		return ErrInvalidResourcePolicy
	}
	previousVerb := ResourceVerb("")
	for _, verb := range policy.Verbs {
		if !verb.Valid() || previousVerb != "" && string(verb) <= string(previousVerb) {
			return ErrInvalidResourcePolicy
		}
		previousVerb = verb
	}
	seenIDs := make(map[string]struct{}, len(policy.Fields))
	seenPaths := make(map[string]struct{}, len(policy.Fields))
	for _, field := range policy.Fields {
		if field.Validate() != nil {
			return ErrInvalidResourcePolicy
		}
		if _, exists := seenIDs[field.ID]; exists {
			return ErrInvalidResourcePolicy
		}
		if _, exists := seenPaths[field.Path]; exists {
			return ErrInvalidResourcePolicy
		}
		seenIDs[field.ID] = struct{}{}
		seenPaths[field.Path] = struct{}{}
	}
	return nil
}

func (policy ResourcePolicy) AllowsVerb(verb ResourceVerb) bool {
	for _, allowed := range policy.Verbs {
		if allowed == verb {
			return true
		}
	}
	return false
}

func (policy ResourcePolicy) Field(id string) (ResourceFieldPolicy, bool) {
	for _, field := range policy.Fields {
		if field.ID == id {
			return cloneResourceFieldPolicy(field), true
		}
	}
	return ResourceFieldPolicy{}, false
}

// Copy returns a defensive value suitable for crossing a project boundary.
func (policy ResourcePolicy) Copy() ResourcePolicy {
	return cloneResourcePolicy(policy)
}

// ResourcePolicyCatalog is an immutable, ordered snapshot. It contains no
// clients, callbacks, credentials, Context, or model-originated authority.
type ResourcePolicyCatalog struct {
	version string
	entries []ResourcePolicy
}

func NewResourcePolicyCatalog(version string, entries []ResourcePolicy) (ResourcePolicyCatalog, error) {
	if version != ResourcePolicyVersion || len(entries) == 0 || len(entries) > MaxResourcePolicyEntries {
		return ResourcePolicyCatalog{}, ErrInvalidResourcePolicy
	}
	copyEntries := make([]ResourcePolicy, len(entries))
	seenIDs := make(map[string]struct{}, len(entries))
	seenAPIs := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if entry.Validate() != nil {
			return ResourcePolicyCatalog{}, ErrInvalidResourcePolicy
		}
		apiKey := strings.Join([]string{entry.Type.Group, entry.Type.Version, entry.Type.Resource}, "\x00")
		if _, exists := seenIDs[entry.Type.ID]; exists {
			return ResourcePolicyCatalog{}, ErrInvalidResourcePolicy
		}
		if _, exists := seenAPIs[apiKey]; exists {
			return ResourcePolicyCatalog{}, ErrInvalidResourcePolicy
		}
		seenIDs[entry.Type.ID] = struct{}{}
		seenAPIs[apiKey] = struct{}{}
		copyEntries[index] = cloneResourcePolicy(entry)
	}
	sort.Slice(copyEntries, func(left, right int) bool { return copyEntries[left].Type.ID < copyEntries[right].Type.ID })
	return ResourcePolicyCatalog{version: version, entries: copyEntries}, nil
}

func (catalog ResourcePolicyCatalog) Validate() error {
	_, err := NewResourcePolicyCatalog(catalog.version, catalog.entries)
	return err
}

func (catalog ResourcePolicyCatalog) Version() string { return catalog.version }

func (catalog ResourcePolicyCatalog) Entries() []ResourcePolicy {
	result := make([]ResourcePolicy, len(catalog.entries))
	for index, entry := range catalog.entries {
		result[index] = cloneResourcePolicy(entry)
	}
	return result
}

func (catalog ResourcePolicyCatalog) Resolve(id string) (ResourcePolicy, bool) {
	for _, entry := range catalog.entries {
		if entry.Type.ID == id {
			return cloneResourcePolicy(entry), true
		}
	}
	return ResourcePolicy{}, false
}

// BuiltInResourcePolicies returns the reviewed stable built-in read catalog.
// The returned policies are independent copies and may be combined with exact
// configured CRD entries before a run is frozen.
func BuiltInResourcePolicies() []ResourcePolicy {
	kinds := []ResourceKind{
		ResourceKindNamespace,
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
		ResourceKindPodDisruptionBudget,
	}
	result := make([]ResourcePolicy, 0, len(kinds))
	for _, kind := range kinds {
		resourceType := BuiltInResourceType(kind)
		fields := []ResourceFieldPolicy{
			builtInField("name", "metadata.name", ResourceScalarString, ResourceDataMetadata, ResourceSelectorField, "metadata.name", ResourceFilterEquals, ResourceFilterNotEquals),
			builtInField("created_at", "metadata.creationTimestamp", ResourceScalarTimestamp, ResourceDataMetadata, ResourceSelectorNone, ""),
		}
		if resourceType.Namespaced() {
			fields = append(fields,
				builtInField("namespace", "metadata.namespace", ResourceScalarString, ResourceDataMetadata, ResourceSelectorField, "metadata.namespace", ResourceFilterEquals, ResourceFilterNotEquals),
			)
		}
		fields = append(fields,
			builtInField("app_name", "metadata.labels.app_name", ResourceScalarString, ResourceDataMetadata, ResourceSelectorLabel, "app.kubernetes.io/name", ResourceFilterEquals, ResourceFilterExists, ResourceFilterNotEquals),
		)
		fields = append(fields, builtInOperationalFields(kind)...)
		result = append(result, ResourcePolicy{
			Type:   resourceType,
			Verbs:  []ResourceVerb{ResourceVerbGet, ResourceVerbList},
			Fields: fields,
			Limits: ResourceQueryLimits{
				MaxPages: MaxResourceQueryPages, PageItems: MaxResourcePageItems, PageBytes: MaxResourcePageBytes,
				MaxItems: MaxResourceQueryItems, MaxBytes: MaxResourceQueryBytes,
				MaxReturned: MaxResourceSummaries,
			},
		})
	}
	result = append(result, ResourcePolicy{
		Type:  secretMetadataResourceType(),
		Verbs: []ResourceVerb{ResourceVerbGet, ResourceVerbList},
		Fields: []ResourceFieldPolicy{
			builtInField("name", "metadata.name", ResourceScalarString, ResourceDataMetadata, ResourceSelectorField, "metadata.name", ResourceFilterEquals, ResourceFilterNotEquals),
			builtInField("created_at", "metadata.creationTimestamp", ResourceScalarTimestamp, ResourceDataMetadata, ResourceSelectorNone, ""),
			builtInField("namespace", "metadata.namespace", ResourceScalarString, ResourceDataMetadata, ResourceSelectorField, "metadata.namespace", ResourceFilterEquals, ResourceFilterNotEquals),
		},
		Limits: ResourceQueryLimits{
			MaxPages: MaxResourceQueryPages, PageItems: MaxResourcePageItems, PageBytes: MaxResourcePageBytes,
			MaxItems: MaxResourceQueryItems, MaxBytes: MaxResourceQueryBytes,
			MaxReturned: MaxResourceSummaries,
		},
	})
	return result
}

func builtInOperationalFields(kind ResourceKind) []ResourceFieldPolicy {
	stringStatus := func(id, path string) ResourceFieldPolicy {
		return builtInField(id, path, ResourceScalarString, ResourceDataStatus, ResourceSelectorNone, "",
			ResourceFilterContains, ResourceFilterEquals, ResourceFilterNotEquals, ResourceFilterStartsWith)
	}
	integerStatus := func(id, path string) ResourceFieldPolicy {
		return builtInField(id, path, ResourceScalarInteger, ResourceDataStatus, ResourceSelectorNone, "",
			ResourceFilterEquals, ResourceFilterGreaterThan, ResourceFilterLessThan, ResourceFilterNotEquals)
	}
	integerSpec := func(id, path string) ResourceFieldPolicy {
		return builtInField(id, path, ResourceScalarInteger, ResourceDataSpec, ResourceSelectorNone, "",
			ResourceFilterEquals, ResourceFilterGreaterThan, ResourceFilterLessThan, ResourceFilterNotEquals)
	}
	switch kind {
	case ResourceKindNamespace, ResourceKindPersistentVolumeClaim, ResourceKindPersistentVolume:
		return []ResourceFieldPolicy{stringStatus("phase", "status.phase")}
	case ResourceKindNode:
		return []ResourceFieldPolicy{
			builtInField("unschedulable", "spec.unschedulable", ResourceScalarBoolean, ResourceDataSpec, ResourceSelectorNone, "", ResourceFilterEquals, ResourceFilterNotEquals),
		}
	case ResourceKindPod:
		return []ResourceFieldPolicy{
			stringStatus("phase", "status.phase"), stringStatus("reason", "status.reason"),
			builtInField("node_name", "spec.nodeName", ResourceScalarString, ResourceDataSpec, ResourceSelectorNone, "", ResourceFilterEquals, ResourceFilterNotEquals),
		}
	case ResourceKindService:
		return []ResourceFieldPolicy{builtInField("service_type", "spec.type", ResourceScalarString, ResourceDataSpec, ResourceSelectorNone, "", ResourceFilterEquals, ResourceFilterNotEquals)}
	case ResourceKindConfigMap:
		return nil
	case ResourceKindDeployment, ResourceKindReplicaSet:
		return []ResourceFieldPolicy{
			integerSpec("desired", "spec.replicas"), integerStatus("ready", "status.readyReplicas"),
			integerStatus("available", "status.availableReplicas"), integerStatus("unavailable", "status.unavailableReplicas"),
			integerStatus("observed_generation", "status.observedGeneration"),
		}
	case ResourceKindStatefulSet:
		return []ResourceFieldPolicy{
			integerSpec("desired", "spec.replicas"), integerStatus("ready", "status.readyReplicas"),
			integerStatus("current", "status.currentReplicas"), integerStatus("updated", "status.updatedReplicas"),
		}
	case ResourceKindDaemonSet:
		return []ResourceFieldPolicy{
			integerStatus("desired", "status.desiredNumberScheduled"), integerStatus("ready", "status.numberReady"),
			integerStatus("available", "status.numberAvailable"), integerStatus("unavailable", "status.numberUnavailable"),
			integerStatus("current", "status.currentNumberScheduled"), integerStatus("updated", "status.updatedNumberScheduled"),
		}
	case ResourceKindJob:
		return []ResourceFieldPolicy{
			integerSpec("desired", "spec.completions"), integerStatus("active", "status.active"),
			integerStatus("succeeded", "status.succeeded"), integerStatus("failed", "status.failed"),
		}
	case ResourceKindCronJob:
		return []ResourceFieldPolicy{
			builtInField("suspended", "spec.suspend", ResourceScalarBoolean, ResourceDataSpec, ResourceSelectorNone, "", ResourceFilterEquals, ResourceFilterNotEquals),
		}
	case ResourceKindIngress:
		return nil
	case ResourceKindHorizontalPodAutoscaler:
		return []ResourceFieldPolicy{
			integerSpec("minimum", "spec.minReplicas"), integerSpec("maximum", "spec.maxReplicas"),
			integerStatus("current", "status.currentReplicas"), integerStatus("desired", "status.desiredReplicas"),
		}
	case ResourceKindPodDisruptionBudget:
		return []ResourceFieldPolicy{
			integerStatus("current_healthy", "status.currentHealthy"), integerStatus("desired_healthy", "status.desiredHealthy"),
			integerStatus("disruptions_allowed", "status.disruptionsAllowed"), integerStatus("expected", "status.expectedPods"),
		}
	default:
		return nil
	}
}

// DefaultResourcePolicyCatalog returns the built-in-only startup catalog.
func DefaultResourcePolicyCatalog() ResourcePolicyCatalog {
	catalog, _ := NewResourcePolicyCatalog(ResourcePolicyVersion, BuiltInResourcePolicies())
	return catalog
}

// ResourceFilter is one typed predicate over a policy field ID. Value is data,
// never a selector fragment.
type ResourceFilter struct {
	Field    string
	Operator ResourceFilterOperator
	Value    string
}

func (filter ResourceFilter) Validate() error {
	if !validResourceFieldID(filter.Field) || !filter.Operator.Valid() ||
		len(filter.Value) > maxResourceFilterBytes || !utf8.ValidString(filter.Value) || strings.TrimSpace(filter.Value) != filter.Value ||
		filter.Operator == ResourceFilterExists && filter.Value != "" || filter.Operator != ResourceFilterExists && filter.Value == "" {
		return ErrInvalidResourceQuery
	}
	for _, current := range filter.Value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) || resourceBidirectionalControl(current) {
			return ErrInvalidResourceQuery
		}
	}
	return nil
}

// ResourceQuery is the complete project-owned request crossing Agent, Tools,
// and Kubernetes boundaries. Raw selectors and continuation tokens are absent.
type ResourceQuery struct {
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	PolicyVersion    string
	Type             ResourceType
	Verb             ResourceVerb
	View             ResourceView
	Namespace        string
	AllNamespaces    bool
	Name             string
	Filters          []ResourceFilter
	Limits           ResourceQueryLimits
}

func (query ResourceQuery) Validate() error {
	if query.Scope.Validate() != nil || !query.PolicyGeneration.Valid() || query.PolicyVersion != ResourcePolicyVersion ||
		query.Type.Validate() != nil || !query.Verb.Valid() || !query.View.Valid() || query.Limits.Validate() != nil ||
		len(query.Filters) > MaxResourceFilters {
		return ErrInvalidResourceQuery
	}
	if query.Type.ClusterScoped() {
		if query.Namespace != "" || query.AllNamespaces {
			return ErrInvalidResourceQuery
		}
	} else if query.AllNamespaces {
		if query.Namespace != "" || query.Scope.NamespaceAccess != NamespaceAccessAll {
			return ErrInvalidResourceQuery
		}
	} else if !ValidNamespaceName(query.Namespace) || query.Scope.NamespaceAccess == NamespaceAccessCurrent && query.Namespace != query.Scope.Namespace {
		return ErrInvalidResourceQuery
	}
	if query.Verb == ResourceVerbGet {
		if !ValidResourceName(query.Name) || len(query.Filters) != 0 || query.Limits.MaxPages != 1 ||
			query.Limits.PageItems != 1 || query.Limits.MaxItems != 1 || query.Limits.MaxReturned != 1 ||
			query.View != ResourceViewSummary && query.View != ResourceViewDescribe {
			return ErrInvalidResourceQuery
		}
	} else if query.Name != "" || query.View != ResourceViewList && query.View != ResourceViewCount && query.View != ResourceViewTable {
		return ErrInvalidResourceQuery
	}
	seen := make(map[string]struct{}, len(query.Filters))
	for _, filter := range query.Filters {
		if filter.Validate() != nil {
			return ErrInvalidResourceQuery
		}
		key := filter.Field + "\x00" + string(filter.Operator)
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidResourceQuery
		}
		seen[key] = struct{}{}
	}
	return nil
}

// AllowsQuery performs the final local policy intersection without I/O.
func (policy ResourcePolicy) AllowsQuery(query ResourceQuery) bool {
	if policy.Validate() != nil || query.Validate() != nil || policy.Type != query.Type ||
		!policy.AllowsVerb(query.Verb) || query.Limits.MaxPages > policy.Limits.MaxPages ||
		query.Limits.PageItems > policy.Limits.PageItems || query.Limits.PageBytes > policy.Limits.PageBytes ||
		query.Limits.MaxItems > policy.Limits.MaxItems ||
		query.Limits.MaxBytes > policy.Limits.MaxBytes || query.Limits.MaxReturned > policy.Limits.MaxReturned {
		return false
	}
	for _, filter := range query.Filters {
		field, found := policy.Field(filter.Field)
		if !found || !field.AllowsFilter(filter) {
			return false
		}
	}
	return true
}

// ResourcePage carries only projected summaries and bounded provenance. A
// continuation token never crosses the Kubernetes adapter boundary.
type ResourcePage struct {
	Type          ResourceType
	Items         []ResourceSummary
	PagesRead     int
	ScannedItems  int
	MatchedItems  int
	ObservedBytes int
	Partial       bool
	Truncated     bool
	MoreAvailable bool
	Reason        string
}

func (page ResourcePage) Validate(query ResourceQuery) error {
	if query.Validate() != nil || page.Type != query.Type || page.PagesRead < 1 || page.PagesRead > query.Limits.MaxPages ||
		page.ScannedItems < page.MatchedItems || page.ScannedItems > query.Limits.MaxItems ||
		page.MatchedItems < len(page.Items) ||
		page.ObservedBytes < 0 || page.ObservedBytes > query.Limits.MaxBytes || len(page.Items) > query.Limits.MaxReturned ||
		page.MatchedItems > len(page.Items) && !page.Partial ||
		page.Partial != page.Truncated || page.MoreAvailable && !page.Partial || page.Partial != (page.Reason != "") {
		return ErrInvalidResourcePage
	}
	if page.Reason != "" && page.Reason != "page_limit" && page.Reason != "item_limit" && page.Reason != "byte_limit" &&
		page.Reason != "return_limit" && page.Reason != "field_limit" {
		return ErrInvalidResourcePage
	}
	seen := make(map[string]struct{}, len(page.Items))
	for _, item := range page.Items {
		if item.Validate() != nil || item.EffectiveType() != query.Type {
			return ErrInvalidResourcePage
		}
		if query.Type.ClusterScoped() && item.Reference.Namespace != "" ||
			query.Type.Namespaced() && !query.AllNamespaces && item.Reference.Namespace != query.Namespace ||
			query.Type.Namespaced() && query.AllNamespaces && !query.Scope.AllowsResourceReference(query.Type, item.Reference) {
			return ErrInvalidResourcePage
		}
		key := item.Reference.Namespace + "\x00" + item.Reference.Name
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidResourcePage
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateResourceRefForType validates a live reference against an exact API
// identity without granting that identity catalog membership.
func ValidateResourceRefForType(reference ResourceRef, resourceType ResourceType) error {
	if resourceType.Validate() != nil || reference.APIVersion != resourceType.APIVersion() || reference.Kind != resourceType.Kind ||
		!ValidResourceName(reference.Name) || !validSafeOptionalText(reference.UID, maxResourceUIDBytes) ||
		!validSafeOptionalText(reference.ResourceVersion, maxResourceVersionBytes) ||
		resourceType.Namespaced() && !ValidNamespaceName(reference.Namespace) || resourceType.ClusterScoped() && reference.Namespace != "" {
		return ErrInvalidResourceRef
	}
	return nil
}

// AllowsResourceReference applies the frozen Namespace policy to an exact
// type/reference pair. It does not consult discovery or RBAC.
func (scope ClusterScope) AllowsResourceReference(resourceType ResourceType, reference ResourceRef) bool {
	if scope.Validate() != nil || ValidateResourceRefForType(reference, resourceType) != nil {
		return false
	}
	return resourceType.ClusterScoped() || scope.NamespaceAccess == NamespaceAccessAll || reference.Namespace == scope.Namespace
}

func resourceOperatorAllowed(operators []ResourceFilterOperator, wanted ResourceFilterOperator) bool {
	for _, operator := range operators {
		if operator == wanted {
			return true
		}
	}
	return false
}

func cloneResourceFieldPolicy(field ResourceFieldPolicy) ResourceFieldPolicy {
	field.Operators = append([]ResourceFilterOperator(nil), field.Operators...)
	return field
}

func cloneResourcePolicy(policy ResourcePolicy) ResourcePolicy {
	policy.Verbs = append([]ResourceVerb(nil), policy.Verbs...)
	policy.Fields = append([]ResourceFieldPolicy(nil), policy.Fields...)
	for index := range policy.Fields {
		policy.Fields[index] = cloneResourceFieldPolicy(policy.Fields[index])
	}
	return policy
}

func validResourcePolicyToken(value string, maximum int, allowDot bool) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || value[0] < 'a' || value[0] > 'z' || value[len(value)-1] == '-' {
		return false
	}
	for _, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' || allowDot && current == '.' {
			continue
		}
		return false
	}
	return true
}

func validResourceFieldID(value string) bool {
	if value == "" || len(value) > maxResourceFieldIDBytes || !utf8.ValidString(value) || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, current := range []byte(value[1:]) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '_' {
			continue
		}
		return false
	}
	return true
}

func validAPIGroup(value string) bool {
	if value == "" || len(value) > maxAPIGroupBytes || !utf8.ValidString(value) {
		return false
	}
	for _, segment := range strings.Split(value, ".") {
		if !validDNSLabel(segment) {
			return false
		}
	}
	return true
}

func validDNSLabel(value string) bool {
	if value == "" || len(value) > 63 || !asciiLowerAlphaNumeric(value[0]) || !asciiLowerAlphaNumeric(value[len(value)-1]) {
		return false
	}
	if len(value) > 2 {
		for _, current := range []byte(value[1 : len(value)-1]) {
			if !asciiLowerAlphaNumeric(current) && current != '-' {
				return false
			}
		}
	}
	return true
}

func asciiLowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func builtInField(
	id string,
	path string,
	scalar ResourceScalarType,
	class ResourceDataClass,
	selector ResourceSelectorSource,
	selectorKey string,
	operators ...ResourceFilterOperator,
) ResourceFieldPolicy {
	sort.Slice(operators, func(left, right int) bool { return operators[left] < operators[right] })
	return ResourceFieldPolicy{
		ID: id, Path: path, Scalar: scalar, DataClass: class,
		SelectorSource: selector, SelectorKey: selectorKey, Operators: operators,
		Evidence: class == ResourceDataStatus,
	}
}

func validResourceKind(value string) bool {
	if value == "" || len(value) > maxKindBytes || !utf8.ValidString(value) || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, current := range []byte(value[1:]) {
		if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
			continue
		}
		return false
	}
	return true
}

func validResourcePath(value string) bool {
	if value == "" || len(value) > maxResourcePathBytes || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" || len(segment) > 63 {
			return false
		}
		for _, current := range []byte(segment) {
			if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' || current == '_' {
				continue
			}
			return false
		}
	}
	return true
}

// ValidResourceProjectionPath reports whether a bounded scalar source path is
// safe to expose as content-free Evidence provenance. It grants no read
// authority and does not validate catalog membership.
func ValidResourceProjectionPath(value string) bool {
	return validResourcePath(value) &&
		(strings.HasPrefix(value, "metadata.") || strings.HasPrefix(value, "status.") || strings.HasPrefix(value, "spec."))
}

// ResourceProjectionPathRequiresSensitiveClass reports whether a projected
// path is credential- or environment-shaped and therefore cannot enter the
// broad-read Evidence or display path.
func ResourceProjectionPathRequiresSensitiveClass(value string) bool {
	return resourceFieldRequiresSensitiveClass(ResourceFieldPolicy{ID: "field", Path: value})
}

func validSelectorKey(value string, source ResourceSelectorSource) bool {
	if source == ResourceSelectorField {
		return value == "metadata.name" || value == "metadata.namespace"
	}
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value || strings.ContainsAny(value, ",=()! ") {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) || resourceBidirectionalControl(current) {
			return false
		}
	}
	parts := strings.Split(value, "/")
	if len(parts) > 2 || len(parts) == 2 && !validAPIGroup(parts[0]) {
		return false
	}
	name := parts[len(parts)-1]
	if name == "" || len(name) > 63 || !asciiAlphaNumeric(name[0]) || !asciiAlphaNumeric(name[len(name)-1]) {
		return false
	}
	if len(name) > 2 {
		for _, current := range []byte(name[1 : len(name)-1]) {
			if !asciiAlphaNumeric(current) && current != '-' && current != '_' && current != '.' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func resourceOperatorValidForScalar(scalar ResourceScalarType, operator ResourceFilterOperator) bool {
	switch operator {
	case ResourceFilterContains, ResourceFilterStartsWith:
		return scalar == ResourceScalarString
	case ResourceFilterGreaterThan, ResourceFilterLessThan:
		return scalar == ResourceScalarInteger || scalar == ResourceScalarDecimal || scalar == ResourceScalarTimestamp
	case ResourceFilterEquals, ResourceFilterNotEquals, ResourceFilterExists:
		return true
	default:
		return false
	}
}

func secretMetadataResourceType() ResourceType {
	return ResourceType{
		ID: "secrets", Version: "v1", Resource: "secrets", Kind: "Secret",
		Scope: ResourceScopeNamespaced,
	}
}
