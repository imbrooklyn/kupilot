package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDiagnosisValidationKeepsFourCollectionsAndUnexecutedActions(t *testing.T) {
	evidenceID := EvidenceID("00000000-0000-7000-8000-000000001301")
	observedFrom := time.UnixMilli(40).UTC()
	observedTo := time.UnixMilli(41).UTC()
	diagnosis := Diagnosis{
		ID:    "00000000-0000-7000-8000-000000001302",
		RunID: "00000000-0000-7000-8000-000000001303",
		Scope: ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 4},
		ConfirmedFacts: []ConfirmedFact{{
			Statement:   "The projected Pod condition is not Ready.",
			EvidenceIDs: []EvidenceID{evidenceID},
		}},
		Hypotheses: []Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []EvidenceID{evidenceID},
			Confidence:            DiagnosisConfidenceLow,
			Falsifier:             "A later bounded observation reports the container ready.",
		}},
		MissingInformation: []MissingInformation{{
			Kind:   MissingInformationTruncated,
			Detail: "A bounded source was truncated.",
			Impact: "The remaining content was not evaluated.",
		}},
		RecommendedActions: []RecommendedAction{{
			Action:   "Review the current readiness configuration.",
			Risk:     "Changing the configuration may restart Pods.",
			Executed: false,
		}},
		AnswerMarkdown:       "Confirmed facts and uncertainty are shown above.",
		ValidationWarnings:   []string{"A bounded source was truncated."},
		ObservedFrom:         &observedFrom,
		ObservedTo:           &observedTo,
		CreatedAt:            time.UnixMilli(42).UTC(),
		EvidenceDetailsState: EvidenceDetailPartial,
	}
	if err := diagnosis.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Diagnosis)
	}{
		{name: "confirmed fact without Evidence", mutate: func(value *Diagnosis) { value.ConfirmedFacts[0].EvidenceIDs = nil }},
		{name: "invalid hypothesis confidence", mutate: func(value *Diagnosis) { value.Hypotheses[0].Confidence = "certain" }},
		{name: "executed recommendation", mutate: func(value *Diagnosis) { value.RecommendedActions[0].Executed = true }},
		{name: "partial time window", mutate: func(value *Diagnosis) { value.ObservedTo = nil }},
		{name: "creation before observation", mutate: func(value *Diagnosis) { value.CreatedAt = observedFrom }},
		{name: "invalid Evidence state", mutate: func(value *Diagnosis) { value.EvidenceDetailsState = "current" }},
		{name: "oversized typed action reason", mutate: func(value *Diagnosis) {
			value.RecommendedActions[0].Operation = ApprovalOperationRestartDeployment
			value.RecommendedActions[0].Target = &ResourceRef{
				APIVersion: RestartDeploymentTargetAPIVersion,
				Kind:       RestartDeploymentTargetKind,
				Namespace:  value.Scope.Namespace,
				Name:       "sample-deployment",
			}
			value.RecommendedActions[0].Action = strings.Repeat("r", MaxApprovalReasonSummaryBytes+1)
		}},
		{name: "too many Evidence references", mutate: func(value *Diagnosis) {
			ids := make([]EvidenceID, maxEvidencePerInvocation)
			for index := range ids {
				ids[index] = EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", index+2000))
			}
			value.ConfirmedFacts = append(value.ConfirmedFacts, ConfirmedFact{Statement: "An invalid aggregate has too many references.", EvidenceIDs: ids})
		}},
		{name: "oversized record", mutate: func(value *Diagnosis) { value.AnswerMarkdown = strings.Repeat("a", maxDiagnosisBytes+1) }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := cloneDiagnosis(diagnosis)
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestDiagnosisValidatesEveryAdmittedTypedProposalWithoutExecutionAuthority(t *testing.T) {
	scope := ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 4}
	tests := []struct {
		operation  ActionOperation
		target     ResourceRef
		parameters *ProposedActionParameters
	}{
		{ActionOperationRestartDeployment, ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "api"}, nil},
		{ActionOperationScaleWorkload, ResourceRef{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "team-a", Name: "db"}, &ProposedActionParameters{Kind: ProposedActionParameterReplicas, Value: "3"}},
		{ActionOperationRollbackDeployment, ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "api"}, &ProposedActionParameters{Kind: ProposedActionParameterRevision, Value: "2"}},
		{ActionOperationDeleteOwnedPod, ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "api-123"}, nil},
		{ActionOperationCordonNode, ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a"}, nil},
		{ActionOperationUncordonNode, ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a"}, nil},
		{ActionOperationDrainNode, ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a"}, nil},
		{ActionOperationRestrictedLocalArgv, ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "team-a"}, &ProposedActionParameters{Kind: ProposedActionParameterPolicyID, Value: "helm-history"}},
		{ActionOperationShell, ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "team-a"}, &ProposedActionParameters{Kind: ProposedActionParameterPolicyID, Value: "maintenance-shell"}},
	}
	for index, test := range tests {
		diagnosis := Diagnosis{
			ID:    DiagnosisID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 3000+index)),
			RunID: AgentRunID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 3100+index)),
			Scope: scope, AnswerMarkdown: "The action is available for explicit supervision.",
			RecommendedActions: []RecommendedAction{{
				Operation: test.operation, Target: &test.target, Parameters: test.parameters,
				Action: "Propose one exact supervised action.", Risk: "Runtime policy determines the authoritative risk.",
			}},
			CreatedAt: time.UnixMilli(42).UTC(),
		}
		if err := diagnosis.Validate(); err != nil {
			t.Errorf("Validate(%s) error = %v", test.operation, err)
		}
	}

	invalid := []RecommendedAction{
		{Operation: ActionOperationScaleWorkload, Target: &ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "api"}, Parameters: &ProposedActionParameters{Kind: ProposedActionParameterReplicas, Value: "03"}, Action: "Invalid replicas.", Risk: "Invalid."},
		{Operation: ActionOperationRestrictedLocalArgv, Target: &ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "team-a"}, Parameters: &ProposedActionParameters{Kind: ProposedActionParameterPolicyID, Value: "kubectl --token=x"}, Action: "Invalid policy.", Risk: "Invalid."},
		{Operation: ActionOperationShell, Target: &ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "other"}, Parameters: &ProposedActionParameters{Kind: ProposedActionParameterPolicyID, Value: "shell"}, Action: "Invalid target.", Risk: "Invalid."},
	}
	for index, action := range invalid {
		diagnosis := Diagnosis{
			ID:    DiagnosisID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 3200+index)),
			RunID: AgentRunID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 3300+index)),
			Scope: scope, RecommendedActions: []RecommendedAction{action}, AnswerMarkdown: "Invalid proposal.",
			CreatedAt: time.UnixMilli(42).UTC(),
		}
		if diagnosis.Validate() == nil {
			t.Errorf("invalid proposal %d was accepted", index)
		}
	}
}

func cloneDiagnosis(value Diagnosis) Diagnosis {
	result := value
	result.ConfirmedFacts = append([]ConfirmedFact(nil), value.ConfirmedFacts...)
	result.ConfirmedFacts[0].EvidenceIDs = append([]EvidenceID(nil), value.ConfirmedFacts[0].EvidenceIDs...)
	result.Hypotheses = append([]Hypothesis(nil), value.Hypotheses...)
	result.Hypotheses[0].SupportingEvidenceIDs = append([]EvidenceID(nil), value.Hypotheses[0].SupportingEvidenceIDs...)
	result.MissingInformation = append([]MissingInformation(nil), value.MissingInformation...)
	result.RecommendedActions = append([]RecommendedAction(nil), value.RecommendedActions...)
	result.ValidationWarnings = append([]string(nil), value.ValidationWarnings...)
	return result
}
