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
	manager, err := NewSessionManager(&sessionManagementStoreFake{schema: 1}, clock.Now)
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
	result, err := NewDoctorResult("v0.1.0", "v1", domain.ModelProviderOpenAI, domain.ModelAPIProtocolChatCompletions, domain.SHA256Hex("origin"), true, false,
		SessionStorageHealth{SchemaRevision: 1})
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
	if _, err := NewDoctorResult("v0.1.0", "v1", domain.ModelProviderOpenAI, domain.ModelAPIProtocolChatCompletions, "not-a-digest", false, false,
		SessionStorageHealth{SchemaRevision: 1}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("unsafe origin error = %v", err)
	}
	if _, err := NewDoctorResult("v0.1.0", "v1", domain.ModelProviderOpenAI, domain.ModelAPIProtocolChatCompletions, domain.SHA256Hex("origin"), false, false,
		SessionStorageHealth{SchemaRevision: 1, SessionCount: 1, FutureActivity: 1}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("invalid storage error = %v", err)
	}
}

func TestDoctorReportsConfiguredNativeProtocolWithoutModelCalls(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("doctor invoked the model")
		return agent.RunOutcome{}
	}))
	manager, err := NewSessionManager(&sessionManagementStoreFake{schema: 1}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.sessionManager = manager
	for _, test := range []struct {
		api                        domain.ModelAPIProtocol
		adapter, version, protocol string
	}{
		{domain.ModelAPIProtocolChatCompletions, DoctorOpenAIAdapterName, DoctorOpenAIAdapterVersion, DoctorOpenAIProtocol},
		{domain.ModelAPIProtocolResponses, DoctorResponsesAdapterName, DoctorResponsesAdapterVersion, DoctorResponsesProtocol},
	} {
		coordinator.modelAPIProtocol = test.api
		result, err := coordinator.Doctor(context.Background())
		if err != nil || result.Validate() != nil || result.ModelCompatibility.Adapter != test.adapter || result.ModelCompatibility.AdapterVersion != test.version || result.ModelCompatibility.Protocol != test.protocol {
			t.Fatalf("doctor for %s = %#v, %v", test.api, result, err)
		}
		result.ModelCompatibility.Protocol = DoctorResponsesProtocol
		result.ModelCompatibility.Adapter = DoctorOpenAIAdapterName
		if !errors.Is(result.Validate(), ErrInvalidUIEvent) {
			t.Fatal("doctor accepted mismatched adapter and protocol")
		}
	}
	if _, err := NewDoctorResult("v0.1.0", "v1", domain.ModelProviderOpenAI, "unknown", domain.SHA256Hex("origin"), true, false, SessionStorageHealth{SchemaRevision: 1}); !errors.Is(err, ErrDoctorUnavailable) {
		t.Fatalf("unknown API protocol = %v", err)
	}
}
