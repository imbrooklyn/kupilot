package domain

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ProposedActionParameterKind is the sole model-visible typed parameter for a
// proposed action. It never carries argv, object bodies, shell text, or
// execution identity.
type ProposedActionParameterKind string

const (
	ProposedActionParameterReplicas ProposedActionParameterKind = "replicas"
	ProposedActionParameterRevision ProposedActionParameterKind = "revision"
	ProposedActionParameterPolicyID ProposedActionParameterKind = "policy_id"
)

// ProposedActionParameters binds one canonical string value. Runtime parses
// it only after the operation-specific schema has selected its meaning.
type ProposedActionParameters struct {
	Kind  ProposedActionParameterKind `json:"kind"`
	Value string                      `json:"value"`
}

func (parameters ProposedActionParameters) validate(operation ActionOperation) bool {
	if parameters.Value == "" || strings.TrimSpace(parameters.Value) != parameters.Value {
		return false
	}
	switch operation {
	case ActionOperationScaleWorkload:
		value, err := strconv.ParseInt(parameters.Value, 10, 32)
		return parameters.Kind == ProposedActionParameterReplicas && err == nil && value >= 0 && strconv.FormatInt(value, 10) == parameters.Value
	case ActionOperationRollbackDeployment:
		value, err := strconv.ParseInt(parameters.Value, 10, 64)
		return parameters.Kind == ProposedActionParameterRevision && err == nil && value > 0 && strconv.FormatInt(value, 10) == parameters.Value
	case ActionOperationRestrictedLocalArgv, ActionOperationShell:
		return parameters.Kind == ProposedActionParameterPolicyID && validLocalPolicyID(parameters.Value)
	default:
		return false
	}
}

// Int64 returns the exact canonical integer carried by a replicas or revision
// proposal. Validation must precede use.
func (parameters ProposedActionParameters) Int64() (int64, bool) {
	if parameters.Kind != ProposedActionParameterReplicas && parameters.Kind != ProposedActionParameterRevision {
		return 0, false
	}
	value, err := strconv.ParseInt(parameters.Value, 10, 64)
	return value, err == nil && strconv.FormatInt(value, 10) == parameters.Value
}

const (
	maxDiagnosisBytes        = 131072
	maxDiagnosisItems        = 100
	maxDiagnosisTextBytes    = 16384
	maxDiagnosisWarningBytes = 4096
)

// ErrInvalidDiagnosis reports an invalid structured Diagnosis derivative.
var ErrInvalidDiagnosis = errors.New("Diagnosis data is invalid")

// DiagnosisID is an opaque application-generated UUIDv7 Diagnosis identifier.
type DiagnosisID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id DiagnosisID) Valid() bool {
	return validUUIDv7(string(id))
}

// ConfirmedFact is historic text whose support remains bound to Evidence IDs.
type ConfirmedFact struct {
	Statement   string       `json:"statement"`
	EvidenceIDs []EvidenceID `json:"evidence_ids"`
}

// DiagnosisConfidence is bounded model self-assessment, never proof.
type DiagnosisConfidence string

const (
	DiagnosisConfidenceLow    DiagnosisConfidence = "low"
	DiagnosisConfidenceMedium DiagnosisConfidence = "medium"
	DiagnosisConfidenceHigh   DiagnosisConfidence = "high"
)

func (confidence DiagnosisConfidence) valid() bool {
	return confidence == DiagnosisConfidenceLow || confidence == DiagnosisConfidenceMedium || confidence == DiagnosisConfidenceHigh
}

// Hypothesis remains an inference and includes a way to disprove it.
type Hypothesis struct {
	Statement             string              `json:"statement"`
	SupportingEvidenceIDs []EvidenceID        `json:"supporting_evidence_ids"`
	Confidence            DiagnosisConfidence `json:"confidence"`
	Falsifier             string              `json:"falsifier"`
}

// MissingInformationKind is a stable reason why an observation is unavailable.
type MissingInformationKind string

const (
	MissingInformationAbsent                 MissingInformationKind = "absent"
	MissingInformationForbidden              MissingInformationKind = "forbidden"
	MissingInformationUnsupported            MissingInformationKind = "unsupported"
	MissingInformationStale                  MissingInformationKind = "stale"
	MissingInformationConflicting            MissingInformationKind = "conflicting"
	MissingInformationTruncated              MissingInformationKind = "truncated"
	MissingInformationSensitiveOutputBlocked MissingInformationKind = "sensitive_output_blocked"
)

func (kind MissingInformationKind) valid() bool {
	switch kind {
	case MissingInformationAbsent,
		MissingInformationForbidden,
		MissingInformationUnsupported,
		MissingInformationStale,
		MissingInformationConflicting,
		MissingInformationTruncated,
		MissingInformationSensitiveOutputBlocked:
		return true
	default:
		return false
	}
}

// MissingInformation records a visible gap and its diagnostic impact.
type MissingInformation struct {
	Kind   MissingInformationKind `json:"kind"`
	Detail string                 `json:"detail"`
	Impact string                 `json:"impact"`
}

// RecommendedAction is retained as the durable compatibility name for a
// bounded proposed action. Operation and Target are populated by the v0.4
// protocol; legacy rows may contain only explanatory text.
type RecommendedAction struct {
	Operation     ApprovalOperation         `json:"operation,omitempty"`
	Target        *ResourceRef              `json:"target,omitempty"`
	Parameters    *ProposedActionParameters `json:"parameters,omitempty"`
	Action        string                    `json:"action"`
	Risk          string                    `json:"risk"`
	Prerequisites []string                  `json:"prerequisites,omitempty"`
	Executed      bool                      `json:"executed"`
}

// Diagnosis is the locally validated terminal answer. The four legacy
// collections remain readable for storage compatibility; new answers use
// AnswerMarkdown, citation-backed ConfirmedFacts, and typed proposed actions.
type Diagnosis struct {
	ID                   DiagnosisID
	RunID                AgentRunID
	Scope                ScopeSnapshot
	ConfirmedFacts       []ConfirmedFact
	Hypotheses           []Hypothesis
	MissingInformation   []MissingInformation
	RecommendedActions   []RecommendedAction
	AnswerMarkdown       string
	ValidationWarnings   []string
	ObservedFrom         *time.Time
	ObservedTo           *time.Time
	CreatedAt            time.Time
	EvidenceDetailsState EvidenceDetailState
}

// Validate checks structure, bounded text, evidence references, and proposed
// action state.
func (diagnosis Diagnosis) Validate() error {
	if !diagnosis.ID.Valid() || !diagnosis.RunID.Valid() || diagnosis.Scope.Validate() != nil ||
		len(diagnosis.ConfirmedFacts) > maxDiagnosisItems || len(diagnosis.Hypotheses) > maxDiagnosisItems ||
		len(diagnosis.MissingInformation) > maxDiagnosisItems || len(diagnosis.RecommendedActions) > maxDiagnosisItems ||
		len(diagnosis.ValidationWarnings) > maxDiagnosisItems ||
		!validDiagnosisText(diagnosis.AnswerMarkdown, 1, maxDiagnosisBytes) ||
		!validPersistenceTime(diagnosis.CreatedAt) ||
		diagnosis.EvidenceDetailsState != "" && !diagnosis.EvidenceDetailsState.Valid() {
		return ErrInvalidDiagnosis
	}
	if (diagnosis.ObservedFrom == nil) != (diagnosis.ObservedTo == nil) {
		return ErrInvalidDiagnosis
	}
	if diagnosis.ObservedFrom != nil && (!validPersistenceTime(*diagnosis.ObservedFrom) || !validPersistenceTime(*diagnosis.ObservedTo) || diagnosis.ObservedTo.Before(*diagnosis.ObservedFrom)) {
		return ErrInvalidDiagnosis
	}
	if diagnosis.ObservedTo != nil && diagnosis.CreatedAt.Before(*diagnosis.ObservedTo) {
		return ErrInvalidDiagnosis
	}
	for _, fact := range diagnosis.ConfirmedFacts {
		if !validDiagnosisText(fact.Statement, 1, maxDiagnosisTextBytes) || !validEvidenceIDs(fact.EvidenceIDs, true) {
			return ErrInvalidDiagnosis
		}
	}
	for _, hypothesis := range diagnosis.Hypotheses {
		if !validDiagnosisText(hypothesis.Statement, 1, maxDiagnosisTextBytes) ||
			!validEvidenceIDs(hypothesis.SupportingEvidenceIDs, false) ||
			!hypothesis.Confidence.valid() ||
			!validDiagnosisText(hypothesis.Falsifier, 1, maxDiagnosisTextBytes) {
			return ErrInvalidDiagnosis
		}
	}
	for _, missing := range diagnosis.MissingInformation {
		if !missing.Kind.valid() || !validDiagnosisText(missing.Detail, 1, maxDiagnosisTextBytes) || !validDiagnosisText(missing.Impact, 1, maxDiagnosisTextBytes) {
			return ErrInvalidDiagnosis
		}
	}
	typedActionCount := 0
	for _, action := range diagnosis.RecommendedActions {
		if action.Executed || !validDiagnosisText(action.Action, 1, maxDiagnosisTextBytes) || !validDiagnosisText(action.Risk, 1, maxDiagnosisTextBytes) || len(action.Prerequisites) > maxDiagnosisItems {
			return ErrInvalidDiagnosis
		}
		if action.Operation == "" {
			if action.Target != nil || action.Parameters != nil {
				return ErrInvalidDiagnosis
			}
		} else {
			typedActionCount++
			if !action.Operation.Valid() || !ValidApprovalReasonSummary(action.Action) ||
				!validProposedAction(action, diagnosis.Scope) {
				return ErrInvalidDiagnosis
			}
		}
		for _, prerequisite := range action.Prerequisites {
			if !validDiagnosisText(prerequisite, 1, maxDiagnosisTextBytes) {
				return ErrInvalidDiagnosis
			}
		}
	}
	if typedActionCount > 1 {
		return ErrInvalidDiagnosis
	}
	for _, warning := range diagnosis.ValidationWarnings {
		if !validDiagnosisText(warning, 1, maxDiagnosisWarningBytes) {
			return ErrInvalidDiagnosis
		}
	}
	if len(diagnosis.ReferencedEvidenceIDs()) > maxEvidencePerInvocation {
		return ErrInvalidDiagnosis
	}
	size, err := diagnosisPayloadBytes(diagnosis)
	if err != nil || size > maxDiagnosisBytes {
		return ErrInvalidDiagnosis
	}
	return nil
}

func validProposedAction(action RecommendedAction, scope ScopeSnapshot) bool {
	if action.Target == nil || action.Target.Validate() != nil || action.Target.UID != "" || action.Target.ResourceVersion != "" {
		return false
	}
	target := action.Target
	switch action.Operation {
	case ActionOperationRestartDeployment:
		return action.Parameters == nil && target.APIVersion == RestartDeploymentTargetAPIVersion &&
			target.Kind == RestartDeploymentTargetKind && target.Namespace == scope.Namespace
	case ActionOperationScaleWorkload:
		return action.Parameters != nil && action.Parameters.validate(action.Operation) && target.APIVersion == "apps/v1" &&
			(target.Kind == "Deployment" || target.Kind == "StatefulSet") && target.Namespace == scope.Namespace
	case ActionOperationRollbackDeployment:
		return action.Parameters != nil && action.Parameters.validate(action.Operation) && target.APIVersion == "apps/v1" &&
			target.Kind == "Deployment" && target.Namespace == scope.Namespace
	case ActionOperationDeleteOwnedPod:
		return action.Parameters == nil && target.APIVersion == "v1" && target.Kind == "Pod" && target.Namespace == scope.Namespace
	case ActionOperationCordonNode, ActionOperationUncordonNode, ActionOperationDrainNode:
		return action.Parameters == nil && target.APIVersion == "v1" && target.Kind == "Node" && target.Namespace == ""
	case ActionOperationRestrictedLocalArgv, ActionOperationShell:
		return action.Parameters != nil && action.Parameters.validate(action.Operation) && target.APIVersion == "v1" &&
			target.Kind == "Namespace" && target.Namespace == "" && target.Name == scope.Namespace
	default:
		return false
	}
}

// ReferencedEvidenceIDs returns a sorted unique copy for repository validation.
func (diagnosis Diagnosis) ReferencedEvidenceIDs() []EvidenceID {
	set := make(map[EvidenceID]struct{})
	for _, fact := range diagnosis.ConfirmedFacts {
		for _, id := range fact.EvidenceIDs {
			set[id] = struct{}{}
		}
	}
	for _, hypothesis := range diagnosis.Hypotheses {
		for _, id := range hypothesis.SupportingEvidenceIDs {
			set[id] = struct{}{}
		}
	}
	result := make([]EvidenceID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func validEvidenceIDs(ids []EvidenceID, required bool) bool {
	if required && len(ids) == 0 || len(ids) > maxEvidencePerInvocation {
		return false
	}
	seen := make(map[EvidenceID]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid() {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func diagnosisPayloadBytes(diagnosis Diagnosis) (int, error) {
	collections := []any{
		diagnosis.ConfirmedFacts,
		diagnosis.Hypotheses,
		diagnosis.MissingInformation,
		diagnosis.RecommendedActions,
		diagnosis.ValidationWarnings,
	}
	total := len(diagnosis.AnswerMarkdown)
	for _, collection := range collections {
		encoded, err := json.Marshal(collection)
		if err != nil {
			return 0, err
		}
		total += len(encoded)
	}
	return total, nil
}

func validDiagnosisText(value string, minimumBytes, maximumBytes int) bool {
	if !validBoundedText(value, minimumBytes, maximumBytes) {
		return false
	}
	for _, current := range value {
		if current == '\n' || current == '\t' {
			continue
		}
		if unicode.IsControl(current) || isModelBidirectionalControl(current) {
			return false
		}
	}
	return true
}
