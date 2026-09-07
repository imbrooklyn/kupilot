package application

import (
	"context"
	"errors"
	"unicode/utf8"
)

const (
	DoctorSchemaVersion           = "kupilot.doctor/v1"
	DoctorRuntimeName             = "eino_adk"
	DoctorRuntimeVersion          = "v0.9.19"
	DoctorModelAdapterName        = "eino_openai"
	DoctorModelAdapterVersion     = "v0.1.13"
	DoctorModelProtocol           = "openai_compatible_chat_completions"
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
	SchemaVersion       string                   `json:"schema_version"`
	ApplicationVersion  string                   `json:"application_version"`
	ConfigurationSchema string                   `json:"configuration_schema"`
	ProviderKind        string                   `json:"provider_kind"`
	AgentOriginHash     string                   `json:"agent_origin_hash"`
	ModelConfigured     bool                     `json:"model_configured"`
	ModelCompatibility  DoctorModelCompatibility `json:"model_compatibility"`
	Storage             SessionStorageHealth     `json:"storage"`
	PersistenceDegraded bool                     `json:"persistence_degraded"`
	Features            []DoctorFeature          `json:"features"`
}

// NewDoctorResult constructs the same typed local projection for the CLI
// short-circuit, which deliberately does not construct a Coordinator.
func NewDoctorResult(version, configSchema, originHash string, configured, degraded bool, health SessionStorageHealth) (UIDoctorResult, error) {
	result := UIDoctorResult{
		SchemaVersion: DoctorSchemaVersion, ApplicationVersion: version, ConfigurationSchema: configSchema,
		ProviderKind: "openai_compatible", AgentOriginHash: originHash, ModelConfigured: configured,
		ModelCompatibility: DoctorModelCompatibility{
			Runtime: DoctorRuntimeName, RuntimeVersion: DoctorRuntimeVersion,
			Adapter: DoctorModelAdapterName, AdapterVersion: DoctorModelAdapterVersion,
			Protocol: DoctorModelProtocol, LiveConformance: DoctorLiveConformanceEvidence,
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
	if result.SchemaVersion != DoctorSchemaVersion || !validDoctorToken(result.ApplicationVersion, 128) ||
		!validDoctorToken(result.ConfigurationSchema, 64) || result.ProviderKind != "openai_compatible" ||
		!validPrivacyDigest(result.AgentOriginHash) || !result.Storage.valid() ||
		result.ModelCompatibility.Runtime != DoctorRuntimeName || result.ModelCompatibility.RuntimeVersion != DoctorRuntimeVersion ||
		result.ModelCompatibility.Adapter != DoctorModelAdapterName || result.ModelCompatibility.AdapterVersion != DoctorModelAdapterVersion ||
		result.ModelCompatibility.Protocol != DoctorModelProtocol ||
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
	configured := coordinator.modelRuntime != nil
	degraded := coordinator.persistenceDegraded
	coordinator.mu.Unlock()
	if manager == nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	health, err := manager.StorageHealth(ctx, coordinator.now())
	if err != nil {
		return UIDoctorResult{}, ErrDoctorUnavailable
	}
	return NewDoctorResult(version, configSchema, coordinator.privacy.OriginHash(), configured, degraded, health)
}
