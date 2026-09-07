package application

import (
	"errors"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDoctorCompatibilityProjectionIsPinnedTypedAndTamperEvident(t *testing.T) {
	result, err := NewDoctorResult("dev", "v2", domain.SHA256Hex("origin"), true, false,
		SessionStorageHealth{SchemaRevision: 16})
	if err != nil {
		t.Fatalf("NewDoctorResult() error = %v", err)
	}
	if result.ModelCompatibility.Runtime != DoctorRuntimeName ||
		result.ModelCompatibility.RuntimeVersion != DoctorRuntimeVersion ||
		result.ModelCompatibility.Adapter != DoctorModelAdapterName ||
		result.ModelCompatibility.AdapterVersion != DoctorModelAdapterVersion ||
		result.ModelCompatibility.Protocol != DoctorModelProtocol ||
		result.ModelCompatibility.LiveConformance != DoctorLiveConformanceEvidence ||
		result.ModelCompatibility.StreamContinuation != "protocol_continuation_unavailable" {
		t.Fatalf("model compatibility = %#v", result.ModelCompatibility)
	}

	mutations := []func(*UIDoctorResult){
		func(value *UIDoctorResult) { value.ModelCompatibility.RuntimeVersion = "v0.0.0" },
		func(value *UIDoctorResult) { value.ModelCompatibility.AdapterVersion = "v0.0.0" },
		func(value *UIDoctorResult) { value.ModelCompatibility.Protocol = "unknown" },
		func(value *UIDoctorResult) { value.ModelCompatibility.LiveConformance = "passed" },
		func(value *UIDoctorResult) { value.ModelCompatibility.StreamContinuation = "available" },
	}
	for index, mutate := range mutations {
		candidate := result
		mutate(&candidate)
		if !errors.Is(candidate.Validate(), ErrInvalidUIEvent) {
			t.Fatalf("mutation %d was accepted", index)
		}
	}
}

func TestDoctorRejectsUnsafeOriginAndInvalidStorageProjection(t *testing.T) {
	if _, err := NewDoctorResult("dev", "v2", "not-a-digest", false, false,
		SessionStorageHealth{SchemaRevision: 16}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("unsafe origin error = %v", err)
	}
	if _, err := NewDoctorResult("dev", "v2", domain.SHA256Hex("origin"), false, false,
		SessionStorageHealth{SchemaRevision: 16, SessionCount: 1, FutureActivity: 1}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("invalid storage error = %v", err)
	}
}
