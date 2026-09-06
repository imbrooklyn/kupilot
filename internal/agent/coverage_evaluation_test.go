package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// syntheticCoverageMetrics are deterministic validator measurements only.
// They are not evidence about the semantic quality of a live model.
type syntheticCoverageMetrics struct {
	EvidenceReferences            int
	ValidEvidenceReferences       int
	CurrentStateClaims            int
	UnsupportedCurrentStateClaims int
	StaleOrCrossRunRejected       int
	UncertaintyCases              int
	UncertaintyHandled            int
	BoundedComplianceCases        int
	BoundedComplianceClassified   int
}

func (metrics syntheticCoverageMetrics) validEvidenceReferenceRate() float64 {
	if metrics.EvidenceReferences == 0 {
		return 1
	}
	return float64(metrics.ValidEvidenceReferences) / float64(metrics.EvidenceReferences)
}

func (metrics syntheticCoverageMetrics) unsupportedCurrentStateClaimRate() float64 {
	if metrics.CurrentStateClaims == 0 {
		return 0
	}
	return float64(metrics.UnsupportedCurrentStateClaims) / float64(metrics.CurrentStateClaims)
}

func TestSyntheticClaimCoverageEvaluation(t *testing.T) {
	type evaluationCase struct {
		name            string
		makeDraft       func(domain.EvidenceID) DiagnosisDraft
		mutate          func(*EvidenceRegistry, *DiagnosisMetadata, domain.EvidenceID)
		wantValid       bool
		staleOrCrossRun bool
		uncertaintyCase bool
	}
	cases := []evaluationCase{
		{
			name: "valid Evidence observation",
			makeDraft: func(id domain.EvidenceID) DiagnosisDraft {
				claim := "The Pod is not Ready."
				return DiagnosisDraft{AnswerMarkdown: claim, ClaimCoverage: []ClaimCoverageDraft{
					validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified, id),
				}}
			},
			wantValid: true,
		},
		{
			name: "unsupported current observation",
			makeDraft: func(domain.EvidenceID) DiagnosisDraft {
				claim := "A current rollout revision was observed."
				return DiagnosisDraft{AnswerMarkdown: claim, ClaimCoverage: []ClaimCoverageDraft{
					validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified),
				}}
			},
		},
		{
			name: "stale Evidence generation",
			makeDraft: func(id domain.EvidenceID) DiagnosisDraft {
				claim := "The Pod is not Ready."
				return DiagnosisDraft{AnswerMarkdown: claim, ClaimCoverage: []ClaimCoverageDraft{
					validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified, id),
				}}
			},
			mutate: func(_ *EvidenceRegistry, metadata *DiagnosisMetadata, _ domain.EvidenceID) {
				metadata.PolicyGeneration++
			},
			staleOrCrossRun: true,
		},
		{
			name: "cross-run Evidence",
			makeDraft: func(id domain.EvidenceID) DiagnosisDraft {
				claim := "The Pod is not Ready."
				return DiagnosisDraft{AnswerMarkdown: claim, ClaimCoverage: []ClaimCoverageDraft{
					validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified, id),
				}}
			},
			mutate: func(registry *EvidenceRegistry, _ *DiagnosisMetadata, id domain.EvidenceID) {
				registry.mu.Lock()
				item := registry.items[id]
				item.RunID = "00000000-0000-7000-8000-000000009999"
				registry.items[id] = item
				registry.mu.Unlock()
			},
			staleOrCrossRun: true,
		},
		{
			name: "explicit uncertainty",
			makeDraft: func(domain.EvidenceID) DiagnosisDraft {
				claim := "The probe configuration was not collected."
				return DiagnosisDraft{AnswerMarkdown: claim, ClaimCoverage: []ClaimCoverageDraft{
					validCoverageDraft(1, domain.ClaimUncertainty, claim, domain.ClaimCoverageLimited),
				}}
			},
			wantValid: true, uncertaintyCase: true,
		},
		{
			name: "response byte bound",
			makeDraft: func(domain.EvidenceID) DiagnosisDraft {
				return DiagnosisDraft{AnswerMarkdown: strings.Repeat("x", MaxAnswerMarkdownBytes+1)}
			},
		},
	}

	metrics := syntheticCoverageMetrics{}
	for _, current := range cases {
		registry, input, evidenceID, _ := claimCoverageRegistry(t)
		draft := current.makeDraft(evidenceID)
		metadata := DiagnosisMetadata{
			ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(), PolicyGeneration: input.PolicyGeneration(),
		}
		if current.mutate != nil {
			current.mutate(registry, &metadata, evidenceID)
		}
		for _, claim := range draft.ClaimCoverage {
			metrics.EvidenceReferences += len(claim.EvidenceIDs)
			if claim.Kind == domain.ClaimCurrentObservation {
				metrics.CurrentStateClaims++
			}
		}
		_, err := ValidateDiagnosis(draft, metadata, registry)
		valid := err == nil
		metrics.BoundedComplianceCases++
		if valid == current.wantValid {
			metrics.BoundedComplianceClassified++
		}
		if valid {
			for _, claim := range draft.ClaimCoverage {
				metrics.ValidEvidenceReferences += len(claim.EvidenceIDs)
			}
		} else {
			for _, claim := range draft.ClaimCoverage {
				if claim.Kind == domain.ClaimCurrentObservation {
					metrics.UnsupportedCurrentStateClaims++
				}
			}
		}
		if current.staleOrCrossRun && !valid {
			metrics.StaleOrCrossRunRejected++
		}
		if current.uncertaintyCase {
			metrics.UncertaintyCases++
			if valid {
				metrics.UncertaintyHandled++
			}
		}
	}

	if metrics.EvidenceReferences != 3 || metrics.ValidEvidenceReferences != 1 ||
		metrics.validEvidenceReferenceRate() != 1.0/3.0 ||
		metrics.CurrentStateClaims != 4 || metrics.UnsupportedCurrentStateClaims != 3 ||
		metrics.unsupportedCurrentStateClaimRate() != 0.75 || metrics.StaleOrCrossRunRejected != 2 ||
		metrics.UncertaintyCases != 1 || metrics.UncertaintyHandled != 1 ||
		metrics.BoundedComplianceCases != len(cases) || metrics.BoundedComplianceClassified != len(cases) {
		t.Fatalf("synthetic claim coverage metrics = %#v", metrics)
	}
}
