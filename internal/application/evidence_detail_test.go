package application

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	evidenceDetailRunID        domain.AgentRunID       = "0198a46e-7d2a-7d34-9b6f-2df5f45a3101"
	evidenceDetailEvidenceID   domain.EvidenceID       = "0198a46e-7d2a-7d34-9b6f-2df5f45a3102"
	evidenceDetailInvocationID domain.ToolInvocationID = "0198a46e-7d2a-7d34-9b6f-2df5f45a3103"
	evidenceDetailDiagnosisID  domain.DiagnosisID      = "0198a46e-7d2a-7d34-9b6f-2df5f45a3104"
)

func TestQueryEvidenceDetailProjectsAllowlistedBoundedFields(t *testing.T) {
	t.Parallel()

	diagnosis, evidence, reference := evidenceDetailFixture()
	address := netip.AddrFrom4([4]byte{192, 0, 2, 17}).String()
	addressV6 := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}).String()
	evidence.Fact = "The Pod is not ready at " + address + " or " + addressV6 + "."
	evidence.Fingerprint = domain.SHA256Hex(evidence.Fact)
	reader := &recordingEvidenceDetailReader{
		diagnosis: diagnosis, diagnosisFound: true,
		evidence: evidence, evidenceFound: true,
		rawLog: "raw-log-canary-2201", rawTool: "raw-tool-canary-2202", rawModel: "raw-model-canary-2203",
	}
	coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
	result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{
		RequestID: 9, Reference: reference,
	})
	if err != nil || result.Validate() != nil || result.Detail == nil {
		t.Fatalf("QueryEvidenceDetail() = %#v/%v", result, err)
	}
	if result.Reference.State != UIEvidenceDetailAvailable || result.Detail.SensitiveFilter != UIEvidenceSensitiveFilterApplied ||
		result.Detail.Resource != (UIEvidenceResource{Type: domain.BuiltInResourceType(domain.ResourceKindPod), APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"}) ||
		result.Detail.SourcePath != "projected.status.conditions" || result.Detail.ObservedAt.Location() != time.UTC {
		t.Fatalf("safe detail projection = %#v", result.Detail)
	}
	encoded := fmt.Sprintf("%#v", result)
	for _, prohibited := range []string{address, addressV6, reader.rawLog, reader.rawTool, reader.rawModel, "sample-uid", "sample-version"} {
		if strings.Contains(encoded, prohibited) {
			t.Fatalf("prohibited field reached Evidence detail: %q in %s", prohibited, encoded)
		}
	}
	if strings.Count(result.Detail.Projection, "[FILTERED]") != 2 {
		t.Fatalf("address filter status/projection = %#v", result.Detail)
	}
	if reader.diagnosisCalls != 1 || reader.evidenceCalls != 1 {
		t.Fatalf("reader calls = diagnosis %d Evidence %d", reader.diagnosisCalls, reader.evidenceCalls)
	}
}

func TestQueryEvidenceDetailSupportsPartialClusterScopedCRDProvenance(t *testing.T) {
	t.Parallel()
	diagnosis, evidence, reference := evidenceDetailFixture()
	source := "status.state"
	evidence.ResourceType = domain.ResourceType{
		ID: "widgets", Group: "example.test", Version: "v1", Resource: "widgets", Kind: "Widget",
		Scope: domain.ResourceScopeCluster,
	}
	evidence.Resource = domain.ResourceRef{APIVersion: "example.test/v1", Kind: "Widget", Name: "sample-widget"}
	evidence.SourcePath = &source
	evidence.PolicyVersion = domain.ResourcePolicyVersion
	evidence.PolicyGeneration = 9
	evidence.Partial = true
	evidence.Fact = "The projected Widget state is Ready."
	evidence.Fingerprint = domain.SHA256Hex(evidence.Fact)
	reader := &recordingEvidenceDetailReader{
		diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
	}
	coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
	result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 17, Reference: reference})
	if err != nil || result.Validate() != nil || result.Reference.State != UIEvidenceDetailPartial || result.Detail == nil ||
		result.Detail.Resource.Type != evidence.ResourceType || result.Detail.Resource.Namespace != "" ||
		result.Detail.PolicyVersion != domain.ResourcePolicyVersion || result.Detail.PolicyGeneration != 9 || !result.Detail.Truncated {
		t.Fatalf("cluster CRD detail = %#v/%v", result, err)
	}
}

func TestQueryEvidenceDetailSupportsExactObservabilityProvenance(t *testing.T) {
	t.Parallel()
	diagnosis, evidence, reference := evidenceDetailFixture()
	source := "api/v1/query_range#pod_cpu_usage"
	from := evidence.ObservedAt.Add(-5 * time.Minute)
	through := evidence.ObservedAt
	evidence.Category = domain.EvidenceCategoryPrometheus
	evidence.PolicyVersion = domain.ObservabilityPolicyVersion
	evidence.PolicyGeneration = 11
	evidence.SourcePath = &source
	evidence.SourceOriginHash = domain.SHA256Hex("https://prometheus.example")
	evidence.Series = "container=app"
	evidence.ObservedFrom = &from
	evidence.ObservedThrough = &through
	evidence.Fact = "The admitted CPU series contains one bounded sample."
	evidence.Fingerprint = domain.SHA256Hex(evidence.Fact)
	reader := &recordingEvidenceDetailReader{
		diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
	}
	coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
	result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 19, Reference: reference})
	if err != nil || result.Validate() != nil || result.Reference.State != UIEvidenceDetailAvailable || result.Detail == nil ||
		result.Detail.Category != domain.EvidenceCategoryPrometheus || result.Detail.SourcePath != source ||
		result.Detail.PolicyVersion != domain.ObservabilityPolicyVersion || result.Detail.PolicyGeneration != 11 {
		t.Fatalf("observability detail = %#v/%v", result, err)
	}

	unsafeSource := "api/v1/query_range#model_supplied_query"
	evidence.SourcePath = &unsafeSource
	reader.evidence = evidence
	result, err = coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 20, Reference: reference})
	if err != nil || result.Reference.State != UIEvidenceDetailUnavailable || result.Detail != nil {
		t.Fatalf("unsafe observability source = %#v/%v", result, err)
	}
}

func TestQueryEvidenceDetailMarksProjectionAndSourceTruncationPartial(t *testing.T) {
	t.Parallel()

	for _, sourceTruncated := range []bool{false, true} {
		diagnosis, evidence, reference := evidenceDetailFixture()
		evidence.Truncated = sourceTruncated
		if !sourceTruncated {
			evidence.Fact = strings.Repeat("bounded observation ", 64)
			evidence.Fingerprint = domain.SHA256Hex(evidence.Fact)
		}
		reader := &recordingEvidenceDetailReader{
			diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
		}
		coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
		result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{
			RequestID: 10, Reference: reference,
		})
		if err != nil || result.Validate() != nil || result.Reference.State != UIEvidenceDetailPartial ||
			result.Detail == nil || !result.Detail.Truncated || len(result.Detail.Projection) > MaxUIEvidenceProjectionBytes {
			t.Fatalf("partial detail (source truncated %v) = %#v/%v", sourceTruncated, result, err)
		}
	}
}

func TestQueryEvidenceDetailFailsClosedBeforeEvidenceRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*domain.Diagnosis, *UIEvidenceReference)
		wantState UIEvidenceDetailState
		wantReads int
	}{
		{
			name: "expired retained detail",
			mutate: func(diagnosis *domain.Diagnosis, _ *UIEvidenceReference) {
				diagnosis.EvidenceDetailsState = domain.EvidenceDetailExpired
			},
			wantState: UIEvidenceDetailExpired,
		},
		{
			name: "scope mismatch",
			mutate: func(_ *domain.Diagnosis, reference *UIEvidenceReference) {
				reference.Scope.Generation++
			},
			wantState: UIEvidenceDetailUnavailable,
		},
		{
			name: "unreferenced identifier",
			mutate: func(_ *domain.Diagnosis, reference *UIEvidenceReference) {
				reference.EvidenceID = "0198a46e-7d2a-7d34-9b6f-2df5f45a3199"
			},
			wantState: UIEvidenceDetailUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diagnosis, evidence, reference := evidenceDetailFixture()
			test.mutate(&diagnosis, &reference)
			reader := &recordingEvidenceDetailReader{
				diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
			}
			coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
			result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{
				RequestID: 11, Reference: reference,
			})
			if err != nil || result.Validate() != nil || result.Reference.State != test.wantState ||
				result.Detail != nil || reader.diagnosisCalls != 1 || reader.evidenceCalls != test.wantReads {
				t.Fatalf("fail-closed result/calls = %#v/%v/%d/%d", result, err, reader.diagnosisCalls, reader.evidenceCalls)
			}
		})
	}
}

func TestQueryEvidenceDetailMissingAndUnsafeEvidenceAreNotProjected(t *testing.T) {
	t.Parallel()

	t.Run("deleted Evidence", func(t *testing.T) {
		diagnosis, _, reference := evidenceDetailFixture()
		reader := &recordingEvidenceDetailReader{diagnosis: diagnosis, diagnosisFound: true}
		coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
		result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 12, Reference: reference})
		if err != nil || result.Reference.State != UIEvidenceDetailExpired || result.Detail != nil || reader.evidenceCalls != 1 {
			t.Fatalf("deleted Evidence result = %#v/%v", result, err)
		}
	})

	t.Run("non-allowlisted source", func(t *testing.T) {
		diagnosis, evidence, reference := evidenceDetailFixture()
		source := "raw.object.status"
		evidence.SourcePath = &source
		reader := &recordingEvidenceDetailReader{
			diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
		}
		coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
		result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 13, Reference: reference})
		if err != nil || result.Reference.State != UIEvidenceDetailUnavailable || result.Detail != nil {
			t.Fatalf("unsafe source result = %#v/%v", result, err)
		}
	})

	t.Run("credential-shaped projected source", func(t *testing.T) {
		diagnosis, evidence, reference := evidenceDetailFixture()
		source := "status.credentialRef"
		evidence.SourcePath = &source
		reader := &recordingEvidenceDetailReader{
			diagnosis: diagnosis, diagnosisFound: true, evidence: evidence, evidenceFound: true,
		}
		coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
		result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{RequestID: 18, Reference: reference})
		if err != nil || result.Reference.State != UIEvidenceDetailUnavailable || result.Detail != nil {
			t.Fatalf("sensitive source result = %#v/%v", result, err)
		}
	})
}

func TestQueryEvidenceDetailCancellationPerformsZeroReads(t *testing.T) {
	t.Parallel()

	_, _, reference := evidenceDetailFixture()
	reader := new(recordingEvidenceDetailReader)
	coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := coordinator.QueryEvidenceDetail(ctx, UIEvidenceDetailQuery{RequestID: 14, Reference: reference})
	if !errors.Is(err, context.Canceled) || reader.diagnosisCalls != 0 || reader.evidenceCalls != 0 {
		t.Fatalf("cancelled query error/calls = %v/%d/%d", err, reader.diagnosisCalls, reader.evidenceCalls)
	}
}

func TestQueryEvidenceDetailPropagatesCancellationDuringRetainedRead(t *testing.T) {
	t.Parallel()

	_, _, reference := evidenceDetailFixture()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &recordingEvidenceDetailReader{diagnosisErr: context.Canceled, onDiagnosis: cancel}
	coordinator := &Coordinator{questions: security.NewRedactor(), evidenceDetails: reader}
	_, err := coordinator.QueryEvidenceDetail(ctx, UIEvidenceDetailQuery{RequestID: 16, Reference: reference})
	if !errors.Is(err, context.Canceled) || reader.diagnosisCalls != 1 || reader.evidenceCalls != 0 {
		t.Fatalf("mid-read cancellation error/calls = %v/%d/%d", err, reader.diagnosisCalls, reader.evidenceCalls)
	}
}

func TestAttachHistoryEvidenceRestoresReferencesWithoutReadingEvidence(t *testing.T) {
	t.Parallel()

	diagnosis, _, _ := evidenceDetailFixture()
	diagnosis.EvidenceDetailsState = domain.EvidenceDetailExpired
	reader := &recordingEvidenceDetailReader{diagnosis: diagnosis, diagnosisFound: true}
	coordinator := &Coordinator{evidenceDetails: reader}
	runID := diagnosis.RunID
	scope := diagnosis.Scope
	record := ResumedSessionRecord{Messages: []domain.Message{{
		Role: domain.MessageRoleAssistant, RunID: &runID, Scope: &scope,
	}}}
	projection := UIResumedSession{History: []UIHistoryMessage{{
		Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown,
		Content: "Historic conclusion.", RunID: runID,
	}}}
	if err := coordinator.attachHistoryEvidence(context.Background(), record, &projection); err != nil {
		t.Fatalf("attachHistoryEvidence() error = %v", err)
	}
	if len(projection.History[0].EvidenceReferences) != 1 ||
		projection.History[0].EvidenceReferences[0].State != UIEvidenceDetailExpired ||
		!projection.History[0].valid() || reader.diagnosisCalls != 1 || reader.evidenceCalls != 0 {
		t.Fatalf("historic Evidence projection/calls = %#v/%d/%d",
			projection.History[0].EvidenceReferences, reader.diagnosisCalls, reader.evidenceCalls)
	}
}

func TestQueryEvidenceDetailUsesCurrentAcceptedEvidenceWithoutRepositoryRead(t *testing.T) {
	t.Parallel()

	diagnosis, evidence, reference := evidenceDetailFixture()
	reader := &recordingEvidenceDetailReader{
		diagnosisErr: errors.New("generated repository failure"),
		evidenceErr:  errors.New("generated repository failure"),
	}
	coordinator := &Coordinator{
		questions: security.NewRedactor(), evidenceDetails: reader,
		lastDiagnosis: &diagnosis,
		lastEvidence:  map[domain.EvidenceID]domain.Evidence{evidence.ID: evidence},
	}
	result, err := coordinator.QueryEvidenceDetail(context.Background(), UIEvidenceDetailQuery{
		RequestID: 15, Reference: reference,
	})
	if err != nil || result.Validate() != nil || result.Detail == nil ||
		reader.diagnosisCalls != 0 || reader.evidenceCalls != 0 {
		t.Fatalf("current accepted Evidence result/calls = %#v/%v/%d/%d",
			result, err, reader.diagnosisCalls, reader.evidenceCalls)
	}
}

func TestCloneEvidenceOwnsObservabilityWindow(t *testing.T) {
	_, evidence, _ := evidenceDetailFixture()
	observedFrom := evidence.ObservedAt.Add(-time.Minute)
	observedThrough := evidence.ObservedAt
	evidence.ObservedFrom = &observedFrom
	evidence.ObservedThrough = &observedThrough

	cloned := cloneEvidence(evidence)
	wantFrom := observedFrom
	wantThrough := observedThrough
	*evidence.ObservedFrom = evidence.ObservedAt.Add(-time.Hour)
	*evidence.ObservedThrough = evidence.ObservedAt.Add(-time.Second)
	if cloned.ObservedFrom == nil || cloned.ObservedThrough == nil ||
		!cloned.ObservedFrom.Equal(wantFrom) || !cloned.ObservedThrough.Equal(wantThrough) {
		t.Fatalf("cloned observation window = %v through %v", cloned.ObservedFrom, cloned.ObservedThrough)
	}
}

func evidenceDetailFixture() (domain.Diagnosis, domain.Evidence, UIEvidenceReference) {
	scope := domain.ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 7}
	observed := time.Date(2026, time.August, 13, 4, 5, 6, 0, time.UTC)
	source := "projected.status.conditions"
	fact := "The projected Ready condition is false."
	evidence := domain.Evidence{
		ID: evidenceDetailEvidenceID, RunID: evidenceDetailRunID, InvocationID: evidenceDetailInvocationID,
		Category: domain.EvidenceCategoryCondition, Scope: scope,
		Resource: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
			UID: "sample-uid", ResourceVersion: "sample-version",
		},
		Fact: fact, SourcePath: &source, Fingerprint: domain.SHA256Hex(fact), ObservedAt: observed,
	}
	diagnosis := domain.Diagnosis{
		ID: evidenceDetailDiagnosisID, RunID: evidenceDetailRunID, Scope: scope,
		ConfirmedFacts: []domain.ConfirmedFact{{Statement: "The Pod is not ready.", EvidenceIDs: []domain.EvidenceID{evidence.ID}}},
		AnswerMarkdown: "The Pod is not ready.", ObservedFrom: &observed, ObservedTo: &observed,
		CreatedAt: observed.Add(time.Second), EvidenceDetailsState: domain.EvidenceDetailAvailable,
	}
	reference := UIEvidenceReference{
		EvidenceID: evidence.ID, RunID: evidence.RunID, Scope: scope, Sequence: 3, State: UIEvidenceDetailAvailable,
	}
	return diagnosis, evidence, reference
}

type recordingEvidenceDetailReader struct {
	diagnosis      domain.Diagnosis
	diagnosisFound bool
	diagnosisErr   error
	evidence       domain.Evidence
	evidenceFound  bool
	evidenceErr    error
	diagnosisCalls int
	evidenceCalls  int
	rawLog         string
	rawTool        string
	rawModel       string
	onDiagnosis    func()
}

func (reader *recordingEvidenceDetailReader) ReadDiagnosis(
	context.Context,
	domain.AgentRunID,
) (domain.Diagnosis, bool, error) {
	reader.diagnosisCalls++
	if reader.onDiagnosis != nil {
		reader.onDiagnosis()
	}
	return reader.diagnosis, reader.diagnosisFound, reader.diagnosisErr
}

func (reader *recordingEvidenceDetailReader) ReadEvidence(
	context.Context,
	domain.EvidenceID,
) (domain.Evidence, bool, error) {
	reader.evidenceCalls++
	return reader.evidence, reader.evidenceFound, reader.evidenceErr
}
