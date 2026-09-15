package application

import (
	"context"
	"errors"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDoctorReadsOnlyFixedCurrentProcessInteractionFailure(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Error("doctor called model runtime")
		return agent.RunOutcome{}
	}))
	manager, err := NewSessionManager(&sessionManagementStoreFake{schema: 17}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.sessionManager = manager
	for _, failure := range []domain.InteractionFailure{"", domain.FailureEvidenceUnknown} {
		if failure != "" {
			coordinator.lastResult = &RunResult{Diagnostic: failure}
		}
		result, err := coordinator.Doctor(context.Background())
		if err != nil || result.Validate() != nil || result.LastInteractionFailure != failure {
			t.Fatalf("doctor = %#v, %v", result, err)
		}
	}
	coordinator.applicationVersion = ""
	if _, err := coordinator.Doctor(context.Background()); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("invalid local metadata = %v", err)
	}
}

func TestDoctorCompatibilityProjectionIsPinnedTypedAndTamperEvident(t *testing.T) {
	result, err := NewDoctorResult("dev", "v2", domain.ModelProviderOpenAI, domain.SHA256Hex("origin"), true, false,
		SessionStorageHealth{SchemaRevision: 16})
	if err != nil {
		t.Fatalf("NewDoctorResult() error = %v", err)
	}
	if result.ModelCompatibility.Runtime != DoctorRuntimeName ||
		result.ModelCompatibility.RuntimeVersion != DoctorRuntimeVersion ||
		result.ModelCompatibility.Adapter != DoctorOpenAIAdapterName ||
		result.ModelCompatibility.AdapterVersion != DoctorOpenAIAdapterVersion ||
		result.ModelCompatibility.Protocol != DoctorOpenAIProtocol ||
		result.ModelCompatibility.LiveConformance != DoctorLiveConformanceEvidence ||
		result.ModelCompatibility.StreamContinuation != "protocol_continuation_unavailable" {
		t.Fatalf("model compatibility = %#v", result.ModelCompatibility)
	}

	mutations := []func(*UIDoctorResult){
		func(value *UIDoctorResult) { value.LastInteractionFailure = "external-content-canary" },
		func(value *UIDoctorResult) { value.ModelCompatibility.RuntimeVersion = "v0.0.0" },
		func(value *UIDoctorResult) { value.ModelCompatibility.AdapterVersion = "v0.0.0" },
		func(value *UIDoctorResult) { value.ModelCompatibility.Protocol = "unknown" },
		func(value *UIDoctorResult) { value.ModelCompatibility.LiveConformance = "passed" },
		func(value *UIDoctorResult) { value.ModelCompatibility.StreamContinuation = "available" },
	}
	result.LastInteractionFailure = domain.FailureEvidenceUnknown
	if err := result.Validate(); err != nil {
		t.Fatalf("fixed diagnostic was rejected: %v", err)
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
	if _, err := NewDoctorResult("dev", "v2", domain.ModelProviderOpenAI, "not-a-digest", false, false,
		SessionStorageHealth{SchemaRevision: 16}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("unsafe origin error = %v", err)
	}
	if _, err := NewDoctorResult("dev", "v2", domain.ModelProviderOpenAI, domain.SHA256Hex("origin"), false, false,
		SessionStorageHealth{SchemaRevision: 16, SessionCount: 1, FutureActivity: 1}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("invalid storage error = %v", err)
	}
}

func TestDoctorProjectsNativeOllamaBoundary(t *testing.T) {
	result, err := NewDoctorResult("dev", "v1", domain.ModelProviderOllama, domain.SHA256Hex("origin"), true, false,
		SessionStorageHealth{SchemaRevision: 17})
	if err != nil {
		t.Fatalf("NewDoctorResult() error = %v", err)
	}
	if result.ProviderKind != "ollama" || result.ModelCompatibility.Adapter != DoctorOllamaAdapterName ||
		result.ModelCompatibility.AdapterVersion != DoctorOllamaAdapterVersion || result.ModelCompatibility.Protocol != DoctorOllamaProtocol {
		t.Fatalf("ollama compatibility = %#v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
