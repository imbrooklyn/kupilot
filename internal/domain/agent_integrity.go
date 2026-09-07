package domain

import (
	"errors"
	"time"
)

const (
	AnswerCompletenessSchemaVersion = "kupilot.answer-completeness/v2"
	MaxClarificationQuestions       = 3
	MaxClarificationChoices         = 3
	MaxAnswerSources                = 100
	MaxAnswerManifestItems          = maxDiagnosisItems
	maxClarificationPromptBytes     = 2048
	maxClarificationChoiceBytes     = 512
)

var ErrInvalidAgentIntegrity = errors.New("Agent integrity data is invalid")

// RunTerminalReason is the Application-verified terminal explanation. A model
// may suggest a reason in its strict response, but cannot make it authoritative.
type RunTerminalReason string

const (
	RunTerminalCompleted            RunTerminalReason = "completed"
	RunTerminalCancelled            RunTerminalReason = "cancelled"
	RunTerminalTimedOut             RunTerminalReason = "timed_out"
	RunTerminalFailed               RunTerminalReason = "failed"
	RunTerminalUnknown              RunTerminalReason = "unknown"
	RunTerminalRecovered            RunTerminalReason = "recovered"
	RunTerminalPersistenceDegraded  RunTerminalReason = "persistence_degraded"
	RunTerminalStaleGeneration      RunTerminalReason = "stale_generation"
	RunTerminalInsufficientEvidence RunTerminalReason = "insufficient_evidence"
	RunTerminalSourceUnavailable    RunTerminalReason = "source_unavailable"
	RunTerminalPolicyDenied         RunTerminalReason = "policy_denied"
	RunTerminalBudgetExhausted      RunTerminalReason = "budget_exhausted"
	RunTerminalConflictingEvidence  RunTerminalReason = "conflicting_evidence"
	RunTerminalPartialResult        RunTerminalReason = "partial_result"
	RunTerminalNeedsUserInput       RunTerminalReason = "needs_user_input"
)

// Valid reports whether the reason has fixed product semantics.
func (reason RunTerminalReason) Valid() bool {
	switch reason {
	case RunTerminalCompleted, RunTerminalCancelled, RunTerminalTimedOut, RunTerminalFailed,
		RunTerminalUnknown, RunTerminalRecovered, RunTerminalPersistenceDegraded,
		RunTerminalStaleGeneration, RunTerminalInsufficientEvidence, RunTerminalSourceUnavailable,
		RunTerminalPolicyDenied, RunTerminalBudgetExhausted, RunTerminalConflictingEvidence,
		RunTerminalPartialResult, RunTerminalNeedsUserInput:
		return true
	default:
		return false
	}
}

// RunTerminalReasonBasis records which deterministic rule selected a
// successful Diagnosis terminal reason. Model output never supplies it.
type RunTerminalReasonBasis string

const (
	RunTerminalReasonFromCoverage      RunTerminalReasonBasis = "coverage"
	RunTerminalReasonFromClarification RunTerminalReasonBasis = "clarification"
	RunTerminalReasonFromRuntimeBudget RunTerminalReasonBasis = "runtime_budget"
)

// ClarificationQuestionKind distinguishes a bounded choice from one bounded
// free-form answer. Neither form carries execution or approval authority.
type ClarificationQuestionKind string

const (
	ClarificationChoice   ClarificationQuestionKind = "choice"
	ClarificationFreeForm ClarificationQuestionKind = "free_form"
)

// ClarificationChoiceValue is display text only.
type ClarificationChoiceValue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ClarificationQuestion is one ordered question in a terminal outcome.
type ClarificationQuestion struct {
	Sequence int                        `json:"sequence"`
	Kind     ClarificationQuestionKind  `json:"kind"`
	Prompt   string                     `json:"prompt"`
	Choices  []ClarificationChoiceValue `json:"choices"`
}

func (question ClarificationQuestion) validate() bool {
	if question.Sequence < 1 || question.Sequence > MaxClarificationQuestions ||
		!validBoundedText(question.Prompt, 1, maxClarificationPromptBytes) {
		return false
	}
	switch question.Kind {
	case ClarificationChoice:
		if len(question.Choices) < 2 || len(question.Choices) > MaxClarificationChoices {
			return false
		}
	case ClarificationFreeForm:
		return len(question.Choices) == 0
	default:
		return false
	}
	seen := make(map[string]struct{}, len(question.Choices))
	for _, choice := range question.Choices {
		if !ValidModelToken(choice.ID, 64) || !validDiagnosisText(choice.Label, 1, maxClarificationChoiceBytes) {
			return false
		}
		if _, duplicate := seen[choice.ID]; duplicate {
			return false
		}
		seen[choice.ID] = struct{}{}
	}
	return true
}

// ClarificationRequest is a typed terminal request for a new explicit input.
type ClarificationRequest struct {
	SchemaVersion string                  `json:"schema_version"`
	Questions     []ClarificationQuestion `json:"questions"`
}

// Validate checks question order and fixed bounds.
func (request ClarificationRequest) Validate() error {
	if request.SchemaVersion != AnswerCompletenessSchemaVersion || len(request.Questions) < 1 ||
		len(request.Questions) > MaxClarificationQuestions {
		return ErrInvalidAgentIntegrity
	}
	for index, question := range request.Questions {
		if question.Sequence != index+1 || !question.validate() {
			return ErrInvalidAgentIntegrity
		}
	}
	return nil
}

// SourceCoverageState distinguishes negative Evidence from sources that were
// never checked or did not complete.
type SourceCoverageState string

const (
	SourceCheckedPresent SourceCoverageState = "checked_present"
	SourceCheckedAbsent  SourceCoverageState = "checked_absent"
	SourceNotChecked     SourceCoverageState = "not_checked"
	SourceUnavailable    SourceCoverageState = "unavailable"
	SourceDenied         SourceCoverageState = "denied"
	SourcePartial        SourceCoverageState = "partial"
	SourceTruncated      SourceCoverageState = "truncated"
	SourceTimedOut       SourceCoverageState = "timed_out"
	SourceStale          SourceCoverageState = "stale"
	SourceConflicting    SourceCoverageState = "conflicting"
)

func (state SourceCoverageState) valid() bool {
	switch state {
	case SourceCheckedPresent, SourceCheckedAbsent, SourceNotChecked, SourceUnavailable,
		SourceDenied, SourcePartial, SourceTruncated, SourceTimedOut, SourceStale, SourceConflicting:
		return true
	default:
		return false
	}
}

// EvidenceFreshnessState never calls an observation fresh without a
// code-owned exact ceiling.
type EvidenceFreshnessState string

const (
	EvidenceFreshnessUnknown EvidenceFreshnessState = "freshness_unknown"
	EvidenceFresh            EvidenceFreshnessState = "fresh"
	EvidenceStale            EvidenceFreshnessState = "stale"
)

func (state EvidenceFreshnessState) valid() bool {
	return state == EvidenceFreshnessUnknown || state == EvidenceFresh || state == EvidenceStale
}

// EvidenceConflictState is derived only from typed source/subject identity and
// fingerprints, never from arbitrary prose.
type EvidenceConflictState string

const (
	EvidenceConflictNone       EvidenceConflictState = "none"
	EvidenceConflictDetected   EvidenceConflictState = "conflicting"
	EvidenceConflictSuperseded EvidenceConflictState = "superseded"
)

func (state EvidenceConflictState) valid() bool {
	return state == EvidenceConflictNone || state == EvidenceConflictDetected || state == EvidenceConflictSuperseded
}

// AnswerSourceCoverage contains content-free source identity plus accepted
// Evidence IDs. It never embeds an Evidence payload.
type AnswerSourceCoverage struct {
	Sequence                int                    `json:"sequence"`
	SourceHash              string                 `json:"source_hash"`
	SubjectHash             string                 `json:"subject_hash"`
	State                   SourceCoverageState    `json:"state"`
	EvidenceIDs             []EvidenceID           `json:"evidence_ids"`
	Freshness               EvidenceFreshnessState `json:"freshness"`
	Conflict                EvidenceConflictState  `json:"conflict"`
	SupersededByEvidenceIDs []EvidenceID           `json:"superseded_by_evidence_ids"`
	ObservedFrom            *time.Time             `json:"observed_from,omitempty"`
	ObservedThrough         *time.Time             `json:"observed_through,omitempty"`
}

func (source AnswerSourceCoverage) validate() bool {
	if source.Sequence < 1 || source.Sequence > MaxAnswerSources || !validSHA256Hex(source.SourceHash) ||
		!validSHA256Hex(source.SubjectHash) || !source.State.valid() || !source.Freshness.valid() ||
		!source.Conflict.valid() || !validEvidenceIDs(source.EvidenceIDs, source.State == SourceCheckedPresent) ||
		(source.ObservedFrom == nil) != (source.ObservedThrough == nil) {
		return false
	}
	if source.ObservedFrom != nil && (!validPersistenceTime(*source.ObservedFrom) || !validPersistenceTime(*source.ObservedThrough) ||
		source.ObservedThrough.Before(*source.ObservedFrom)) {
		return false
	}
	if source.State == SourceCheckedAbsent || source.State == SourceNotChecked || source.State == SourceUnavailable ||
		source.State == SourceDenied || source.State == SourceTimedOut || source.State == SourceStale {
		if len(source.EvidenceIDs) != 0 {
			return false
		}
	}
	if source.Conflict == EvidenceConflictSuperseded {
		if !validEvidenceIDs(source.SupersededByEvidenceIDs, true) {
			return false
		}
	} else if len(source.SupersededByEvidenceIDs) != 0 {
		return false
	}
	return true
}

// AnswerCompletenessManifest proves only structure, provenance ownership, and
// declared coverage. It cannot prove semantic correctness or exhaustive facts.
type AnswerCompletenessManifest struct {
	SchemaVersion         string                  `json:"schema_version"`
	ResponseSchemaVersion int                     `json:"response_schema_version"`
	Claims                []ClaimEvidenceCoverage `json:"claims"`
	Limitations           []MissingInformation    `json:"limitations"`
	Sources               []AnswerSourceCoverage  `json:"sources"`
	StopReason            RunTerminalReason       `json:"stop_reason"`
	StopReasonBasis       RunTerminalReasonBasis  `json:"stop_reason_basis"`
}

// Validate checks exact ordering, bounds, and internal Evidence references.
func (manifest AnswerCompletenessManifest) Validate() error {
	if manifest.SchemaVersion != AnswerCompletenessSchemaVersion ||
		(manifest.ResponseSchemaVersion != 1 && manifest.ResponseSchemaVersion != 2) || !manifest.StopReason.Valid() ||
		len(manifest.Claims) > maxDiagnosisItems || len(manifest.Limitations) > maxDiagnosisItems ||
		len(manifest.Sources) > MaxAnswerSources {
		return ErrInvalidAgentIntegrity
	}
	claimEvidence := make(map[EvidenceID]struct{})
	for index, claim := range manifest.Claims {
		if !claim.valid() || claim.Sequence != index+1 {
			return ErrInvalidAgentIntegrity
		}
		for _, id := range claim.EvidenceIDs {
			claimEvidence[id] = struct{}{}
		}
	}
	for _, limitation := range manifest.Limitations {
		if !limitation.Kind.valid() || !validDiagnosisText(limitation.Detail, 1, maxDiagnosisTextBytes) ||
			!validDiagnosisText(limitation.Impact, 1, maxDiagnosisTextBytes) {
			return ErrInvalidAgentIntegrity
		}
	}
	sourceEvidence := make(map[EvidenceID]struct{})
	supersedingEvidence := make(map[EvidenceID]struct{})
	for index, source := range manifest.Sources {
		if !source.validate() || source.Sequence != index+1 {
			return ErrInvalidAgentIntegrity
		}
		for _, id := range source.EvidenceIDs {
			if _, duplicate := sourceEvidence[id]; duplicate {
				return ErrInvalidAgentIntegrity
			}
			sourceEvidence[id] = struct{}{}
		}
		for _, id := range source.SupersededByEvidenceIDs {
			if _, duplicate := supersedingEvidence[id]; duplicate {
				return ErrInvalidAgentIntegrity
			}
			supersedingEvidence[id] = struct{}{}
		}
	}
	for id := range claimEvidence {
		if _, present := sourceEvidence[id]; !present {
			return ErrInvalidAgentIntegrity
		}
	}
	for id := range supersedingEvidence {
		if _, present := sourceEvidence[id]; !present {
			return ErrInvalidAgentIntegrity
		}
	}
	switch manifest.StopReasonBasis {
	case RunTerminalReasonFromCoverage:
		derived, err := DeriveAnswerTerminalReason(manifest.Claims, manifest.Limitations, manifest.Sources, false)
		if err != nil || derived != manifest.StopReason {
			return ErrInvalidAgentIntegrity
		}
	case RunTerminalReasonFromClarification:
		derived, err := DeriveAnswerTerminalReason(manifest.Claims, manifest.Limitations, manifest.Sources, true)
		if err != nil || derived != manifest.StopReason {
			return ErrInvalidAgentIntegrity
		}
	case RunTerminalReasonFromRuntimeBudget:
		if manifest.StopReason != RunTerminalBudgetExhausted || !hasRuntimeBudgetLimitation(manifest.Limitations) {
			return ErrInvalidAgentIntegrity
		}
	default:
		return ErrInvalidAgentIntegrity
	}
	return nil
}

func hasRuntimeBudgetLimitation(limitations []MissingInformation) bool {
	for _, limitation := range limitations {
		if limitation.Kind == MissingInformationUnsupported || limitation.Kind == MissingInformationTruncated {
			return true
		}
	}
	return false
}

// DeriveAnswerTerminalReason projects the authoritative completed-result
// reason from typed coverage. A model-suggested label is deliberately absent
// from this input and therefore cannot lower or fabricate runtime state.
func DeriveAnswerTerminalReason(
	claims []ClaimEvidenceCoverage,
	limitations []MissingInformation,
	sources []AnswerSourceCoverage,
	clarification bool,
) (RunTerminalReason, error) {
	if len(claims) > maxDiagnosisItems || len(limitations) > maxDiagnosisItems || len(sources) > MaxAnswerSources {
		return "", ErrInvalidAgentIntegrity
	}
	for index, claim := range claims {
		if !claim.valid() || claim.Sequence != index+1 {
			return "", ErrInvalidAgentIntegrity
		}
	}
	for _, limitation := range limitations {
		if !limitation.Kind.valid() || !validDiagnosisText(limitation.Detail, 1, maxDiagnosisTextBytes) ||
			!validDiagnosisText(limitation.Impact, 1, maxDiagnosisTextBytes) {
			return "", ErrInvalidAgentIntegrity
		}
	}
	for index, source := range sources {
		if !source.validate() || source.Sequence != index+1 {
			return "", ErrInvalidAgentIntegrity
		}
	}
	if clarification {
		if len(claims) != 0 || len(limitations) != 0 {
			return "", ErrInvalidAgentIntegrity
		}
		return RunTerminalNeedsUserInput, nil
	}

	var conflict, stale, denied, unavailable, partial, insufficient bool
	superseded := make(map[EvidenceID]struct{})
	for _, source := range sources {
		conflict = conflict || source.Conflict == EvidenceConflictDetected || source.State == SourceConflicting
		stale = stale || source.State == SourceStale
		denied = denied || source.State == SourceDenied
		unavailable = unavailable || source.State == SourceUnavailable || source.State == SourceTimedOut
		partial = partial || source.State == SourcePartial || source.State == SourceTruncated
		if source.Conflict == EvidenceConflictSuperseded {
			for _, id := range source.EvidenceIDs {
				superseded[id] = struct{}{}
			}
		}
	}
	for _, claim := range claims {
		if claim.Kind == ClaimCurrentObservation {
			for _, id := range claim.EvidenceIDs {
				if _, found := superseded[id]; found {
					conflict = true
				}
			}
		}
		if claim.Kind == ClaimUnsupportedObservation || claim.State == ClaimCoverageUnsupported {
			insufficient = true
		}
	}
	for _, limitation := range limitations {
		switch limitation.Kind {
		case MissingInformationConflicting:
			conflict = true
		case MissingInformationStale:
			stale = true
		case MissingInformationForbidden, MissingInformationSensitiveOutputBlocked:
			denied = true
		case MissingInformationUnsupported:
			insufficient = true
		case MissingInformationTruncated:
			partial = true
		case MissingInformationAbsent:
			insufficient = true
		}
	}
	switch {
	case conflict:
		return RunTerminalConflictingEvidence, nil
	case stale:
		return RunTerminalStaleGeneration, nil
	case denied:
		return RunTerminalPolicyDenied, nil
	case unavailable:
		return RunTerminalSourceUnavailable, nil
	case partial:
		return RunTerminalPartialResult, nil
	case insufficient:
		return RunTerminalInsufficientEvidence, nil
	default:
		return RunTerminalCompleted, nil
	}
}

// Empty reports whether the manifest predates this schema. It is used only to
// read retained v1 rows and never for newly validated model output.
func (manifest AnswerCompletenessManifest) Empty() bool {
	return manifest.SchemaVersion == "" && manifest.ResponseSchemaVersion == 0 && len(manifest.Claims) == 0 && len(manifest.Limitations) == 0 &&
		len(manifest.Sources) == 0 && manifest.StopReason == "" && manifest.StopReasonBasis == ""
}
