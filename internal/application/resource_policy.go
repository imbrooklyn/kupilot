package application

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrResourceReadAuthorityInvalid = errors.New("resource read authority is invalid")

// RunResourcePolicySource provides one immutable catalog/generation snapshot
// and the no-I/O freshness check used before model, Tool, and Kubernetes work.
type RunResourcePolicySource interface {
	ResourcePolicySnapshot(context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool)
	CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool
}

type RunObservabilityPolicySource interface {
	ObservabilityPolicySnapshot(context.Context) (domain.ObservabilityPolicyCatalog, domain.PolicyGeneration, bool)
}

type RunRemoteDiagnosticsPolicySource interface {
	RemoteDiagnosticsPolicySnapshot(context.Context) (domain.RemoteDiagnosticsPolicyCatalog, domain.PolicyGeneration, bool)
}

// ResourceReadAuthority combines the process-fixed resource catalog with the
// Application-owned live permission generation. It owns no client or I/O.
type ResourceReadAuthority struct {
	catalog           domain.ResourcePolicyCatalog
	observability     domain.ObservabilityPolicyCatalog
	remoteDiagnostics domain.RemoteDiagnosticsPolicyCatalog
	permissions       *PermissionManager
}

func NewResourceReadAuthority(catalog domain.ResourcePolicyCatalog, permissions *PermissionManager) (*ResourceReadAuthority, error) {
	return NewOperationalReadAuthority(catalog, domain.DisabledObservabilityPolicyCatalog(), permissions)
}

func NewOperationalReadAuthority(catalog domain.ResourcePolicyCatalog, observability domain.ObservabilityPolicyCatalog, permissions *PermissionManager) (*ResourceReadAuthority, error) {
	return NewCompleteOperationalAuthority(catalog, observability, domain.DisabledRemoteDiagnosticsPolicyCatalog(), permissions)
}

// NewCompleteOperationalAuthority binds all model-visible catalog snapshots to
// one Application-owned policy generation.
func NewCompleteOperationalAuthority(catalog domain.ResourcePolicyCatalog, observability domain.ObservabilityPolicyCatalog, remoteDiagnostics domain.RemoteDiagnosticsPolicyCatalog, permissions *PermissionManager) (*ResourceReadAuthority, error) {
	if catalog.Validate() != nil || observability.Validate() != nil || remoteDiagnostics.Validate() != nil || permissions == nil {
		return nil, ErrResourceReadAuthorityInvalid
	}
	if _, healthy := permissions.Policy(); !healthy {
		return nil, ErrResourceReadAuthorityInvalid
	}
	return &ResourceReadAuthority{catalog: catalog, observability: observability, remoteDiagnostics: remoteDiagnostics, permissions: permissions}, nil
}

func (authority *ResourceReadAuthority) RemoteDiagnosticsPolicySnapshot(ctx context.Context) (domain.RemoteDiagnosticsPolicyCatalog, domain.PolicyGeneration, bool) {
	if ctx == nil || ctx.Err() != nil || authority == nil || authority.remoteDiagnostics.Validate() != nil || authority.permissions == nil {
		return domain.RemoteDiagnosticsPolicyCatalog{}, 0, false
	}
	policy, healthy := authority.permissions.Policy()
	if !policy.Generation.Valid() {
		return domain.RemoteDiagnosticsPolicyCatalog{}, 0, false
	}
	file, found := authority.remoteDiagnostics.ContainerFile()
	var filePointer *domain.ContainerFilePolicy
	if found {
		filePointer = &file
	}
	copy, err := domain.NewRemoteDiagnosticsPolicyCatalog(authority.remoteDiagnostics.PodExecPolicies(), filePointer, authority.remoteDiagnostics.DiagnosticPodPolicies())
	if err != nil {
		return domain.RemoteDiagnosticsPolicyCatalog{}, 0, false
	}
	return copy, policy.Generation, healthy
}

func (authority *ResourceReadAuthority) ResourcePolicySnapshot(ctx context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	if ctx == nil || ctx.Err() != nil || authority == nil || authority.catalog.Validate() != nil || authority.permissions == nil {
		return domain.ResourcePolicyCatalog{}, 0, false
	}
	policy, healthy := authority.permissions.Policy()
	if !policy.Generation.Valid() {
		return domain.ResourcePolicyCatalog{}, 0, false
	}
	copy, err := domain.NewResourcePolicyCatalog(authority.catalog.Version(), authority.catalog.Entries())
	if err != nil {
		return domain.ResourcePolicyCatalog{}, 0, false
	}
	return copy, policy.Generation, healthy
}

func (authority *ResourceReadAuthority) ObservabilityPolicySnapshot(ctx context.Context) (domain.ObservabilityPolicyCatalog, domain.PolicyGeneration, bool) {
	if ctx == nil || ctx.Err() != nil || authority == nil || authority.observability.Validate() != nil || authority.permissions == nil {
		return domain.ObservabilityPolicyCatalog{}, 0, false
	}
	policy, healthy := authority.permissions.Policy()
	if !policy.Generation.Valid() {
		return domain.ObservabilityPolicyCatalog{}, 0, false
	}
	prometheus, _ := authority.observability.Resolve(domain.DataSourcePrometheus)
	loki, _ := authority.observability.Resolve(domain.DataSourceLoki)
	copy, err := domain.NewObservabilityPolicyCatalog(prometheus, loki)
	if err != nil {
		return domain.ObservabilityPolicyCatalog{}, 0, false
	}
	return copy, policy.Generation, healthy
}

func (authority *ResourceReadAuthority) CurrentPolicyGeneration(ctx context.Context, generation domain.PolicyGeneration) bool {
	if ctx == nil || ctx.Err() != nil || authority == nil || authority.permissions == nil || !generation.Valid() {
		return false
	}
	policy, healthy := authority.permissions.Policy()
	return healthy && policy.Generation == generation
}
