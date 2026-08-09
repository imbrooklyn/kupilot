package domain

import (
	"encoding/json"
	"errors"
	"sort"
	"time"
)

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

// RecommendedAction is user-evaluated text and is never executed in v0.1.
type RecommendedAction struct {
	Action        string   `json:"action"`
	Risk          string   `json:"risk"`
	Prerequisites []string `json:"prerequisites,omitempty"`
	Executed      bool     `json:"executed"`
}

// Diagnosis is the locally validated four-part diagnostic result.
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

// Validate checks structure, bounded text, evidence references, and v0.1 action state.
func (diagnosis Diagnosis) Validate() error {
	if !diagnosis.ID.Valid() || !diagnosis.RunID.Valid() || diagnosis.Scope.Validate() != nil ||
		len(diagnosis.ConfirmedFacts) > maxDiagnosisItems || len(diagnosis.Hypotheses) > maxDiagnosisItems ||
		len(diagnosis.MissingInformation) > maxDiagnosisItems || len(diagnosis.RecommendedActions) > maxDiagnosisItems ||
		len(diagnosis.ValidationWarnings) > maxDiagnosisItems ||
		!validBoundedText(diagnosis.AnswerMarkdown, 1, maxDiagnosisBytes) ||
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
		if !validBoundedText(fact.Statement, 1, maxDiagnosisTextBytes) || !validEvidenceIDs(fact.EvidenceIDs, true) {
			return ErrInvalidDiagnosis
		}
	}
	for _, hypothesis := range diagnosis.Hypotheses {
		if !validBoundedText(hypothesis.Statement, 1, maxDiagnosisTextBytes) ||
			!validEvidenceIDs(hypothesis.SupportingEvidenceIDs, false) ||
			!hypothesis.Confidence.valid() ||
			!validBoundedText(hypothesis.Falsifier, 1, maxDiagnosisTextBytes) {
			return ErrInvalidDiagnosis
		}
	}
	for _, missing := range diagnosis.MissingInformation {
		if !missing.Kind.valid() || !validBoundedText(missing.Detail, 1, maxDiagnosisTextBytes) || !validBoundedText(missing.Impact, 1, maxDiagnosisTextBytes) {
			return ErrInvalidDiagnosis
		}
	}
	for _, action := range diagnosis.RecommendedActions {
		if action.Executed || !validBoundedText(action.Action, 1, maxDiagnosisTextBytes) || !validBoundedText(action.Risk, 1, maxDiagnosisTextBytes) || len(action.Prerequisites) > maxDiagnosisItems {
			return ErrInvalidDiagnosis
		}
		for _, prerequisite := range action.Prerequisites {
			if !validBoundedText(prerequisite, 1, maxDiagnosisTextBytes) {
				return ErrInvalidDiagnosis
			}
		}
	}
	for _, warning := range diagnosis.ValidationWarnings {
		if !validBoundedText(warning, 1, maxDiagnosisWarningBytes) {
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
