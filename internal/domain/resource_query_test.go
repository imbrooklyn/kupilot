package domain

import "testing"

func TestBuiltInResourcePolicyCatalogIsValidAndExact(t *testing.T) {
	policies := BuiltInResourcePolicies()
	if got, want := len(policies), 17; got != want {
		t.Fatalf("len(BuiltInResourcePolicies()) = %d, want %d", got, want)
	}
	for _, policy := range policies {
		if err := policy.Validate(); err != nil {
			t.Errorf("policy %q is invalid: %v", policy.Type.ID, err)
		}
	}
	catalog, err := NewResourcePolicyCatalog(ResourcePolicyVersion, policies)
	if err != nil {
		t.Fatalf("NewResourcePolicyCatalog() error = %v", err)
	}
	for _, policy := range policies {
		resolved, found := catalog.Resolve(policy.Type.ID)
		if !found || resolved.Type != policy.Type {
			t.Errorf("Resolve(%q) = (%+v, %t), want exact type %+v", policy.Type.ID, resolved.Type, found, policy.Type)
		}
	}
}

func TestResourceFieldPolicyRejectsSensitiveEvidenceMapping(t *testing.T) {
	field := ResourceFieldPolicy{
		ID: "credential", Path: "spec.credential", Scalar: ResourceScalarString,
		DataClass: ResourceDataSensitive, SelectorSource: ResourceSelectorNone, Evidence: true,
	}
	if err := field.Validate(); err != ErrInvalidResourcePolicy {
		t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidResourcePolicy)
	}
}

func TestResourceFieldPolicyRequiresSensitiveClassForCredentialPaths(t *testing.T) {
	field := ResourceFieldPolicy{
		ID: "credential_ref", Path: "spec.credentialRef", Scalar: ResourceScalarString,
		DataClass: ResourceDataSpec, SelectorSource: ResourceSelectorNone,
	}
	if err := field.Validate(); err != ErrInvalidResourcePolicy {
		t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidResourcePolicy)
	}
	field.DataClass = ResourceDataSensitive
	if err := field.Validate(); err != nil {
		t.Fatalf("sensitive Validate() error = %v", err)
	}
}

func TestResourceFieldPolicyRejectsPathOutsideReviewedProjectionRoots(t *testing.T) {
	field := ResourceFieldPolicy{
		ID: "payload", Path: "arbitrary.payload", Scalar: ResourceScalarString,
		DataClass: ResourceDataSpec, SelectorSource: ResourceSelectorNone,
	}
	if err := field.Validate(); err != ErrInvalidResourcePolicy {
		t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidResourcePolicy)
	}
}

func TestResourcePolicyRejectsForgedCoreTypeAndMalformedAPIKeys(t *testing.T) {
	for _, resourceType := range []ResourceType{
		{ID: "invented", Version: "v1", Resource: "invented", Kind: "Invented", Scope: ResourceScopeNamespaced},
		{ID: "widgets", Group: "example..test", Version: "v1", Resource: "widgets", Kind: "Widget", Scope: ResourceScopeNamespaced},
		{ID: "1widgets", Group: "example.test", Version: "v1", Resource: "widgets", Kind: "Widget", Scope: ResourceScopeNamespaced},
	} {
		if err := resourceType.Validate(); err != ErrInvalidResourceType {
			t.Fatalf("ResourceType.Validate(%#v) error = %v, want %v", resourceType, err, ErrInvalidResourceType)
		}
	}
	field := ResourceFieldPolicy{
		ID: "tenant", Path: "metadata.labels.tenant", Scalar: ResourceScalarString,
		DataClass: ResourceDataMetadata, SelectorSource: ResourceSelectorLabel,
		SelectorKey: "example..test/tenant", Operators: []ResourceFilterOperator{ResourceFilterEquals},
	}
	if err := field.Validate(); err != ErrInvalidResourcePolicy {
		t.Fatalf("malformed label selector Validate() error = %v, want %v", err, ErrInvalidResourcePolicy)
	}
}

func TestResourceSummaryDoesNotReplaceInvalidExplicitTypeWithBuiltInAuthority(t *testing.T) {
	summary := ResourceSummary{
		Type:      ResourceType{ID: "pods", Version: "v1", Resource: "secrets", Kind: "Pod", Scope: ResourceScopeNamespaced, BuiltIn: true},
		Reference: ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
	}
	if summary.EffectiveType() != (ResourceType{}) || summary.Validate() != ErrInvalidResourceSummary {
		t.Fatalf("invalid explicit type was replaced: effective=%#v error=%v", summary.EffectiveType(), summary.Validate())
	}
}

func TestResourceFieldPolicyEnforcesTypedPredicates(t *testing.T) {
	field := ResourceFieldPolicy{
		ID: "replicas", Path: "status.replicas", Scalar: ResourceScalarInteger,
		DataClass: ResourceDataStatus, SelectorSource: ResourceSelectorNone,
		Operators: []ResourceFilterOperator{ResourceFilterEquals, ResourceFilterGreaterThan}, Evidence: true,
	}
	if err := field.Validate(); err != nil {
		t.Fatalf("valid field error = %v", err)
	}
	if !field.AllowsFilter(ResourceFilter{Field: "replicas", Operator: ResourceFilterGreaterThan, Value: "2"}) {
		t.Fatal("valid integer predicate was denied")
	}
	if field.AllowsFilter(ResourceFilter{Field: "replicas", Operator: ResourceFilterGreaterThan, Value: "two"}) {
		t.Fatal("invalid integer predicate was admitted")
	}
	field.Operators = []ResourceFilterOperator{ResourceFilterContains}
	if err := field.Validate(); err != ErrInvalidResourcePolicy {
		t.Fatalf("string operator on integer error = %v, want %v", err, ErrInvalidResourcePolicy)
	}
}
