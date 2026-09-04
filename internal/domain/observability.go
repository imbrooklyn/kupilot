package domain

import (
	"errors"
	"sort"
	"time"
)

const (
	// ObservabilityPolicyVersion identifies the code-owned source/query policy
	// frozen into every AgentRun.
	ObservabilityPolicyVersion = "kupilot.observability-policy/2026-09-05.v1"

	MaxObservabilityQueryTemplates = 8
	MaxObservabilityLogContainers  = 16
	MaxMetricContainers            = 50
	MaxObservabilitySeries         = 100
	MaxObservabilitySamples        = 1000
	MaxObservabilityLines          = 1000
	MaxObservabilityPages          = 8
	MaxObservabilityBytes          = 4 * 1024 * 1024
	MaxObservabilityWindow         = 24 * time.Hour
	MaxObservabilityStep           = 15 * time.Minute
)

var ErrInvalidObservabilityPolicy = errors.New("observability policy data is invalid")

// DataSourceKind is one optional, independently configured external source.
type DataSourceKind string

const (
	DataSourcePrometheus DataSourceKind = "prometheus"
	DataSourceLoki       DataSourceKind = "loki"
)

func (kind DataSourceKind) Valid() bool {
	return kind == DataSourcePrometheus || kind == DataSourceLoki
}

// ObservabilityQueryID selects one code-owned query template. It is not raw
// PromQL or LogQL and cannot identify an endpoint.
type ObservabilityQueryID string

const (
	QueryPrometheusPodCPUUsage            ObservabilityQueryID = "pod_cpu_usage"
	QueryPrometheusPodMemoryWorkingSet    ObservabilityQueryID = "pod_memory_working_set"
	QueryPrometheusPodNetworkReceiveRate  ObservabilityQueryID = "pod_network_receive_rate"
	QueryPrometheusPodNetworkTransmitRate ObservabilityQueryID = "pod_network_transmit_rate"
	QueryLokiPodLogs                      ObservabilityQueryID = "pod_logs"
)

func (id ObservabilityQueryID) ValidFor(kind DataSourceKind) bool {
	switch kind {
	case DataSourcePrometheus:
		return id == QueryPrometheusPodCPUUsage || id == QueryPrometheusPodMemoryWorkingSet ||
			id == QueryPrometheusPodNetworkReceiveRate || id == QueryPrometheusPodNetworkTransmitRate
	case DataSourceLoki:
		return id == QueryLokiPodLogs
	default:
		return false
	}
}

// DataSourcePolicy is the safe immutable portion of one optional source. The
// canonical origin itself and credential remain adapter-confined; only its
// digest is supplied to the Agent runtime.
type DataSourcePolicy struct {
	Kind           DataSourceKind
	OriginHash     string
	Queries        []ObservabilityQueryID
	RequestTimeout time.Duration
}

func (policy DataSourcePolicy) Validate() error {
	if !policy.Kind.Valid() || !validSHA256Hex(policy.OriginHash) ||
		len(policy.Queries) < 1 || len(policy.Queries) > MaxObservabilityQueryTemplates ||
		policy.RequestTimeout <= 0 || policy.RequestTimeout > MaxModelRequestTimeout {
		return ErrInvalidObservabilityPolicy
	}
	seen := make(map[ObservabilityQueryID]struct{}, len(policy.Queries))
	for index, query := range policy.Queries {
		if !query.ValidFor(policy.Kind) || index > 0 && policy.Queries[index-1] >= query {
			return ErrInvalidObservabilityPolicy
		}
		if _, duplicate := seen[query]; duplicate {
			return ErrInvalidObservabilityPolicy
		}
		seen[query] = struct{}{}
	}
	return nil
}

func (policy DataSourcePolicy) Allows(query ObservabilityQueryID) bool {
	if policy.Validate() != nil {
		return false
	}
	index := sort.Search(len(policy.Queries), func(index int) bool { return policy.Queries[index] >= query })
	return index < len(policy.Queries) && policy.Queries[index] == query
}

func (policy DataSourcePolicy) Copy() DataSourcePolicy {
	policy.Queries = append([]ObservabilityQueryID(nil), policy.Queries...)
	return policy
}

func (policy DataSourcePolicy) empty() bool {
	return policy.Kind == "" && policy.OriginHash == "" && len(policy.Queries) == 0 && policy.RequestTimeout == 0
}

// ObservabilityPolicyCatalog freezes the two fixed optional source slots.
// A zero policy means that source is disabled; no dynamically named source is
// accepted.
type ObservabilityPolicyCatalog struct {
	version    string
	prometheus DataSourcePolicy
	loki       DataSourcePolicy
}

func NewObservabilityPolicyCatalog(prometheus, loki DataSourcePolicy) (ObservabilityPolicyCatalog, error) {
	for kind, policy := range map[DataSourceKind]DataSourcePolicy{
		DataSourcePrometheus: prometheus,
		DataSourceLoki:       loki,
	} {
		if policy.empty() {
			continue
		}
		if policy.Kind != kind || policy.Validate() != nil {
			return ObservabilityPolicyCatalog{}, ErrInvalidObservabilityPolicy
		}
	}
	return ObservabilityPolicyCatalog{
		version: ObservabilityPolicyVersion, prometheus: prometheus.Copy(), loki: loki.Copy(),
	}, nil
}

func DisabledObservabilityPolicyCatalog() ObservabilityPolicyCatalog {
	catalog, _ := NewObservabilityPolicyCatalog(DataSourcePolicy{}, DataSourcePolicy{})
	return catalog
}

func (catalog ObservabilityPolicyCatalog) Validate() error {
	if catalog.version != ObservabilityPolicyVersion {
		return ErrInvalidObservabilityPolicy
	}
	_, err := NewObservabilityPolicyCatalog(catalog.prometheus, catalog.loki)
	return err
}

func (catalog ObservabilityPolicyCatalog) Version() string { return catalog.version }

func (catalog ObservabilityPolicyCatalog) Resolve(kind DataSourceKind) (DataSourcePolicy, bool) {
	if catalog.Validate() != nil || !kind.Valid() {
		return DataSourcePolicy{}, false
	}
	policy := catalog.prometheus
	if kind == DataSourceLoki {
		policy = catalog.loki
	}
	if policy.empty() {
		return DataSourcePolicy{}, false
	}
	return policy.Copy(), true
}

func (catalog ObservabilityPolicyCatalog) EnabledKinds() []DataSourceKind {
	result := make([]DataSourceKind, 0, 2)
	if _, found := catalog.Resolve(DataSourcePrometheus); found {
		result = append(result, DataSourcePrometheus)
	}
	if _, found := catalog.Resolve(DataSourceLoki); found {
		result = append(result, DataSourceLoki)
	}
	return result
}

// NormalizedResourceUsage contains only portable integer quantities. Kubernetes
// Quantity parsing remains in internal/kube.
type NormalizedResourceUsage struct {
	CPUMilli    int64
	MemoryBytes int64
}

func (usage NormalizedResourceUsage) Validate() error {
	if usage.CPUMilli < 0 || usage.MemoryBytes < 0 {
		return ErrInvalidObservabilityPolicy
	}
	return nil
}
