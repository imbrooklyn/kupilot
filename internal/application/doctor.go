package application

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	DoctorSchemaVersion           = "kupilot.doctor/v1"
	DoctorRuntimeName             = "eino_adk"
	DoctorRuntimeVersion          = "v0.9.19"
	DoctorOpenAIAdapterName       = "eino_openai"
	DoctorOpenAIAdapterVersion    = "v0.1.13"
	DoctorOpenAIProtocol          = "openai_chat_completions"
	DoctorResponsesAdapterName    = "eino_openai_responses"
	DoctorResponsesAdapterVersion = "v0.2.2"
	DoctorResponsesProtocol       = "openai_responses"
	DoctorLiveConformanceEvidence = "not_run"
)

var ErrDoctorUnavailable = errors.New("local diagnostics are unavailable")

// FeatureAvailability is a fixed local diagnostic state. It never grants
// execution or model authority.
type FeatureAvailability string

const (
	FeatureAvailable   FeatureAvailability = "available"
	FeatureDisabled    FeatureAvailability = "disabled"
	FeatureUnavailable FeatureAvailability = "unavailable"
)

// DoctorFeature identifies one compile-time feature check.
type DoctorFeature struct {
	Name  string              `json:"name"`
	State FeatureAvailability `json:"state"`
}

// DoctorModelCompatibility reports the exact checked-in model boundary without
// probing or constructing it. Live conformance is evidence, never authority.
type DoctorModelCompatibility struct {
	Runtime            string `json:"runtime"`
	RuntimeVersion     string `json:"runtime_version"`
	Adapter            string `json:"adapter"`
	AdapterVersion     string `json:"adapter_version"`
	Protocol           string `json:"protocol"`
	LiveConformance    string `json:"live_conformance"`
	StreamContinuation string `json:"stream_continuation"`
}

// UIDoctorResult is a versioned redacted local-only health projection.
type UIDoctorResult struct {
	LastInteractionFailure domain.InteractionFailure `json:"last_interaction_failure,omitempty"`
	SchemaVersion          string                    `json:"schema_version"`
	ApplicationVersion     string                    `json:"application_version"`
	ConfigurationSchema    string                    `json:"configuration_schema"`
	ProviderKind           string                    `json:"provider_kind"`
	AgentOriginHash        string                    `json:"agent_origin_hash"`
	ModelConfigured        bool                      `json:"model_configured"`
	ModelCompatibility     DoctorModelCompatibility  `json:"model_compatibility"`
	Storage                SessionStorageHealth      `json:"storage"`
	PersistenceDegraded    bool                      `json:"persistence_degraded"`
	Features               []DoctorFeature           `json:"features"`
}

// NewDoctorResult constructs the same typed local projection for the CLI
// short-circuit, which deliberately does not construct a Coordinator.
func NewDoctorResult(version, configSchema string, provider domain.ModelProviderKind, apiProtocol domain.ModelAPIProtocol, originHash string, configured, degraded bool, health SessionStorageHealth) (UIDoctorResult, error) {
	adapter, adapterVersion, protocol, ok := doctorModelBoundary(provider, apiProtocol)
	if !ok {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	result := UIDoctorResult{
		SchemaVersion: DoctorSchemaVersion, ApplicationVersion: version, ConfigurationSchema: configSchema,
		ProviderKind: string(provider), AgentOriginHash: originHash, ModelConfigured: configured,
		ModelCompatibility: DoctorModelCompatibility{
			Runtime: DoctorRuntimeName, RuntimeVersion: DoctorRuntimeVersion,
			Adapter: adapter, AdapterVersion: adapterVersion,
			Protocol: protocol, LiveConformance: DoctorLiveConformanceEvidence,
			StreamContinuation: "protocol_continuation_unavailable",
		},
		Storage: health, PersistenceDegraded: degraded,
		Features: []DoctorFeature{
			{Name: "session_management", State: FeatureAvailable},
			{Name: "manual_compaction", State: FeatureAvailable},
			{Name: "plan_only", State: FeatureAvailable},
			{Name: "evidence_coverage", State: FeatureAvailable},
			{Name: "terminal_delivery", State: FeatureAvailable},
		},
	}
	if result.Validate() != nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	return result, nil
}

func (result UIDoctorResult) Validate() error {
	provider := domain.ModelProviderKind(result.ProviderKind)
	var apiProtocol domain.ModelAPIProtocol
	switch result.ModelCompatibility.Protocol {
	case DoctorOpenAIProtocol:
		apiProtocol = domain.ModelAPIProtocolChatCompletions
	case DoctorResponsesProtocol:
		apiProtocol = domain.ModelAPIProtocolResponses
	default:
		return ErrInvalidUIEvent
	}
	adapter, adapterVersion, protocol, ok := doctorModelBoundary(provider, apiProtocol)
	if result.LastInteractionFailure != "" && !result.LastInteractionFailure.Valid() || result.SchemaVersion != DoctorSchemaVersion || !validDoctorToken(result.ApplicationVersion, 128) ||
		!validDoctorToken(result.ConfigurationSchema, 64) || !ok ||
		!validPrivacyDigest(result.AgentOriginHash) || !result.Storage.valid() ||
		result.ModelCompatibility.Runtime != DoctorRuntimeName || result.ModelCompatibility.RuntimeVersion != DoctorRuntimeVersion ||
		result.ModelCompatibility.Adapter != adapter || result.ModelCompatibility.AdapterVersion != adapterVersion ||
		result.ModelCompatibility.Protocol != protocol ||
		result.ModelCompatibility.LiveConformance != DoctorLiveConformanceEvidence ||
		result.ModelCompatibility.StreamContinuation != "protocol_continuation_unavailable" || len(result.Features) != 5 {
		return ErrInvalidUIEvent
	}
	want := [...]string{"session_management", "manual_compaction", "plan_only", "evidence_coverage", "terminal_delivery"}
	for index, feature := range result.Features {
		if feature.Name != want[index] || feature.State != FeatureAvailable && feature.State != FeatureDisabled && feature.State != FeatureUnavailable {
			return ErrInvalidUIEvent
		}
	}
	return nil
}

func doctorModelBoundary(provider domain.ModelProviderKind, apiProtocol domain.ModelAPIProtocol) (adapter, version, protocol string, ok bool) {
	if provider != domain.ModelProviderOpenAI {
		return "", "", "", false
	}
	switch apiProtocol {
	case "", domain.ModelAPIProtocolChatCompletions:
		return DoctorOpenAIAdapterName, DoctorOpenAIAdapterVersion, DoctorOpenAIProtocol, true
	case domain.ModelAPIProtocolResponses:
		return DoctorResponsesAdapterName, DoctorResponsesAdapterVersion, DoctorResponsesProtocol, true
	default:
		return "", "", "", false
	}
}

func validDoctorToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value)
}

// Doctor performs only the bounded storage query needed for local health. It
// never updates Last active or invokes a model, Tool, cluster, Reviewer,
// approval, process, executor, or notification sink.
func (coordinator *Coordinator) Doctor(ctx context.Context) (UIDoctorResult, error) {
	if coordinator == nil || ctx == nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	if err := coordinator.beginUIOperation(false); err != nil {
		return UIDoctorResult{}, err
	}
	defer coordinator.finishOperation()
	coordinator.mu.Lock()
	manager := coordinator.sessionManager
	version := coordinator.applicationVersion
	configSchema := coordinator.configurationSchema
	provider := coordinator.modelProvider
	apiProtocol := coordinator.modelAPIProtocol
	configured := coordinator.modelRuntime != nil
	degraded := coordinator.persistenceDegraded
	var diagnostic domain.InteractionFailure
	if coordinator.lastResult != nil {
		diagnostic = coordinator.lastResult.Diagnostic
	}
	coordinator.mu.Unlock()
	if manager == nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	health, err := manager.StorageHealth(ctx, coordinator.now())
	if err != nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	result, err := NewDoctorResult(version, configSchema, provider, apiProtocol, coordinator.privacy.OriginHash(), configured, degraded, health)
	if err != nil {
		return UIDoctorResult{}, err
	}
	result.LastInteractionFailure = diagnostic
	return result, nil
}
