package domain

// InteractionFailure is a fixed, content-free diagnostic reason. It never
// carries authority, a raw cause, or a model-selected message.
type InteractionFailure string

// InteractionStage identifies a fixed boundary in the single Agent pipeline.
type InteractionStage string

const (
	FailureRequestPreflight    InteractionFailure = "request_preflight_rejected"
	FailureRetainedContext     InteractionFailure = "retained_context_rejected"
	FailureSummaryResponse     InteractionFailure = "summary_response_rejected"
	FailureProviderTransport   InteractionFailure = "provider_transport_failed"
	FailureProviderReported    InteractionFailure = "provider_reported_failure"
	FailureProviderProtocol    InteractionFailure = "provider_protocol_unsupported"
	FailureProviderRequest     InteractionFailure = "provider_request_rejected"
	FailureStreamMalformed     InteractionFailure = "stream_malformed"
	FailureStreamDuplicate     InteractionFailure = "stream_finish_duplicate"
	FailureStreamAfterFinish   InteractionFailure = "stream_event_after_finish"
	FailureStreamIncomplete    InteractionFailure = "stream_finish_missing"
	FailureStreamUsage         InteractionFailure = "stream_usage_invalid"
	FailureStreamUnsupported   InteractionFailure = "stream_event_unsupported"
	FailureStopReason          InteractionFailure = "provider_stop_reason_invalid"
	FailureToolSelection       InteractionFailure = "tool_call_malformed"
	FailureToolPolicy          InteractionFailure = "tool_policy_denied"
	FailureToolPairing         InteractionFailure = "tool_pairing_rejected"
	FailureToolResult          InteractionFailure = "tool_result_rejected"
	FailureToolCancelled       InteractionFailure = "tool_cancelled"
	FailureToolTimeout         InteractionFailure = "tool_timed_out"
	FailureEvidenceAcceptance  InteractionFailure = "evidence_acceptance_rejected"
	FailureFinalJSON           InteractionFailure = "final_json_malformed"
	FailureFinalDuplicateField InteractionFailure = "final_field_duplicate"
	FailureFinalUnknownField   InteractionFailure = "final_field_unknown"
	FailureFinalMissingField   InteractionFailure = "final_field_missing"
	FailureFinalNullField      InteractionFailure = "final_field_null"
	FailureFinalSchema         InteractionFailure = "final_schema_unsupported"
	FailureFinalShape          InteractionFailure = "final_shape_invalid"
	FailureFinalLimit          InteractionFailure = "final_limit_exceeded"
	FailureClarification       InteractionFailure = "clarification_invalid"
	FailurePlan                InteractionFailure = "plan_invalid"
	FailureClaimKind           InteractionFailure = "claim_kind_invalid"
	FailureClaimDuplicate      InteractionFailure = "claim_duplicate"
	FailureClaimUnsupported    InteractionFailure = "current_observation_unsupported"
	FailureEvidenceUnknown     InteractionFailure = "evidence_reference_unknown"
	FailureEvidenceDuplicate   InteractionFailure = "evidence_reference_duplicate"
	FailureEvidenceOwnership   InteractionFailure = "evidence_ownership_rejected"
	FailureClaimBinding        InteractionFailure = "claim_binding_rejected"
	FailureRegistryChanged     InteractionFailure = "evidence_registry_changed"
	FailureSensitiveOutput     InteractionFailure = "sensitive_output_blocked"
	FailureBudget              InteractionFailure = "runtime_budget_exhausted"
	FailureCancelled           InteractionFailure = "run_cancelled"
	FailureTimeout             InteractionFailure = "run_timed_out"
	FailureStaleGeneration     InteractionFailure = "generation_stale"
	FailurePersistence         InteractionFailure = "persistence_commit_failed"
	FailureEventAcceptance     InteractionFailure = "application_event_rejected"
	FailureInternal            InteractionFailure = "internal_invariant_failed"
)

// Stage is derived from the closed reason catalog, never from error text.
func (failure InteractionFailure) Stage() InteractionStage {
	switch failure {
	case FailureRequestPreflight:
		return "request_preflight"
	case FailureRetainedContext, FailureSummaryResponse:
		return "retained_context"
	case FailureProviderTransport, FailureProviderProtocol, FailureProviderReported, FailureProviderRequest:
		return "model_invocation"
	case FailureStreamMalformed, FailureStreamUnsupported, FailureStopReason,
		FailureStreamDuplicate, FailureStreamAfterFinish, FailureStreamIncomplete, FailureStreamUsage:
		return "stream_assembly"
	case FailureToolSelection, FailureToolPolicy, FailureToolPairing:
		return "tool_selection"
	case FailureToolResult, FailureToolCancelled, FailureToolTimeout:
		return "tool_execution"
	case FailureEvidenceAcceptance, FailureRegistryChanged:
		return "evidence_acceptance"
	case FailureFinalJSON, FailureFinalDuplicateField, FailureFinalUnknownField,
		FailureFinalMissingField, FailureFinalNullField, FailureFinalSchema,
		FailureFinalShape, FailureFinalLimit, FailureClarification, FailurePlan:
		return "final_decode"
	case FailureClaimKind, FailureClaimDuplicate, FailureClaimUnsupported,
		FailureEvidenceUnknown, FailureEvidenceDuplicate, FailureEvidenceOwnership, FailureClaimBinding:
		return "claim_binding"
	case FailureSensitiveOutput, FailureBudget, FailureCancelled, FailureTimeout, FailureStaleGeneration:
		return "run_guard"
	case FailurePersistence:
		return "persistence"
	case FailureEventAcceptance:
		return "event_acceptance"
	case FailureInternal:
		return "internal"
	default:
		return ""
	}
}

func (failure InteractionFailure) Valid() bool { return failure.Stage() != "" }

// SafeMessage is deliberately assembled only from fixed catalog values.
func (failure InteractionFailure) SafeMessage() string {
	if !failure.Valid() {
		return "The diagnostic runtime failed safely."
	}
	return "The interaction stopped safely at " + string(failure.Stage()) + " (" + string(failure) + ")."
}
