package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestDiagnosisRepositoryRoundTripsTypedCollectionsAndEvidenceState(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "diagnosis-round-trip")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000004001", "00000000-0000-7000-8000-000000004002", "00000000-0000-7000-8000-000000004003", time.UnixMilli(300).UTC())
	tools := NewToolInvocationRepository(db)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000004004", run, 1, time.UnixMilli(301).UTC())
	evidence := []domain.Evidence{
		testEvidence("00000000-0000-7000-8000-000000004005", invocation, time.UnixMilli(302).UTC()),
		testEvidence("00000000-0000-7000-8000-000000004006", invocation, time.UnixMilli(303).UTC()),
	}
	evidence[1].Truncated = true
	evidence[1].Fingerprint = domain.SHA256Hex("truncated-diagnosis-evidence")
	invocation.EvidenceCount = len(evidence)
	if err := tools.Save(context.Background(), invocation, evidence); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}

	repository := NewDiagnosisRepository(db)
	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000004007", run, evidence)
	if err := repository.Save(context.Background(), diagnosis); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	want := diagnosis
	want.EvidenceDetailsState = domain.EvidenceDetailPartial
	got, err := repository.GetByRunID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByRunID() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetByRunID() = %#v, want %#v", got, want)
	}

	if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM evidence_items WHERE id = ?`, evidence[1].ID); err != nil {
		t.Fatalf("delete one Evidence error = %v", err)
	}
	partial, err := repository.GetByRunID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByRunID(partial) error = %v", err)
	}
	if partial.EvidenceDetailsState != domain.EvidenceDetailPartial {
		t.Fatalf("partial Evidence state = %q", partial.EvidenceDetailsState)
	}
	if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM evidence_items WHERE id = ?`, evidence[0].ID); err != nil {
		t.Fatalf("delete final Evidence error = %v", err)
	}
	expired, err := repository.GetByRunID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByRunID(expired) error = %v", err)
	}
	if expired.EvidenceDetailsState != domain.EvidenceDetailExpired {
		t.Fatalf("expired Evidence state = %q, want %q", expired.EvidenceDetailsState, domain.EvidenceDetailExpired)
	}
	if !reflect.DeepEqual(expired.ConfirmedFacts, diagnosis.ConfirmedFacts) {
		t.Fatal("historic fact text or references were fabricated or rewritten after expiry")
	}
}

func TestDiagnosisRepositoryRejectsCrossRunMinimalDuplicateAndOversizedData(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "diagnosis-denials")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000004101", "00000000-0000-7000-8000-000000004102", "00000000-0000-7000-8000-000000004103", time.UnixMilli(310).UTC())
	tools := NewToolInvocationRepository(db)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000004104", run, 1, time.UnixMilli(311).UTC())
	evidence := testEvidence("00000000-0000-7000-8000-000000004105", invocation, time.UnixMilli(312).UTC())
	invocation.EvidenceCount = 1
	if err := tools.Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}
	repository := NewDiagnosisRepository(db)

	missingReference := testDiagnosis("00000000-0000-7000-8000-000000004106", run, []domain.Evidence{evidence})
	missingReference.ConfirmedFacts[0].EvidenceIDs[0] = "00000000-0000-7000-8000-000000004199"
	if err := repository.Save(context.Background(), missingReference); !errors.Is(err, ErrDiagnosisEvidenceInvalid) {
		t.Fatalf("Save(missing Evidence) error = %v, want ErrDiagnosisEvidenceInvalid", err)
	}
	if _, err := repository.GetByRunID(context.Background(), run.ID); !errors.Is(err, ErrDiagnosisNotFound) {
		t.Fatalf("invalid reference left Diagnosis: %v", err)
	}

	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000004107", run, []domain.Evidence{evidence})
	if err := repository.Save(context.Background(), diagnosis); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}
	boundCanary := "diagnosis-bound-answer-canary"
	duplicate := diagnosis
	duplicate.ID = "00000000-0000-7000-8000-000000004108"
	duplicate.AnswerMarkdown = boundCanary
	err := repository.Save(context.Background(), duplicate)
	assertStorageError(t, err, ClassPersistenceUnavailable, "diagnosis_save_failed")
	if strings.Contains(err.Error(), boundCanary) {
		t.Fatal("duplicate Diagnosis error disclosed a bound answer")
	}

	oversized := diagnosis
	oversized.ID = "00000000-0000-7000-8000-000000004109"
	oversized.AnswerMarkdown = strings.Repeat("a", 131073)
	if err := repository.Save(context.Background(), oversized); !errors.Is(err, domain.ErrInvalidDiagnosis) {
		t.Fatalf("Save(oversized) error = %v, want ErrInvalidDiagnosis", err)
	}

	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE sessions
		SET title = '', privacy_mode = 'minimal', last_context = NULL,
			last_namespace = NULL, selected_resource_json = NULL, summary = NULL
		WHERE id = ?
	`, run.SessionID); err != nil {
		t.Fatalf("minimal Session setup error = %v", err)
	}
	minimal := diagnosis
	minimal.ID = "00000000-0000-7000-8000-000000004110"
	if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM diagnoses WHERE run_id = ?`, run.ID); err != nil {
		t.Fatalf("minimal setup delete error = %v", err)
	}
	if err := repository.Save(context.Background(), minimal); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("Save(minimal) error = %v, want ErrDurableContentDisabled", err)
	}
}

func TestDiagnosisRepositoryRejectsCorruptJSONAndHonorsCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "diagnosis-strict")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000004201", "00000000-0000-7000-8000-000000004202", "00000000-0000-7000-8000-000000004203", time.UnixMilli(320).UTC())
	tools := NewToolInvocationRepository(db)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000004204", run, 1, time.UnixMilli(321).UTC())
	evidence := testEvidence("00000000-0000-7000-8000-000000004205", invocation, time.UnixMilli(322).UTC())
	invocation.EvidenceCount = 1
	if err := tools.Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}
	repository := NewDiagnosisRepository(db)
	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000004206", run, []domain.Evidence{evidence})
	if err := repository.Save(context.Background(), diagnosis); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}
	rowCanary := "diagnosis-unknown-field-canary"
	corruptJSON := `[{"statement":"Historic statement","evidence_ids":["` + string(evidence.ID) + `"],"` + rowCanary + `":true}]`
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE diagnoses SET confirmed_json = ? WHERE id = ?`, corruptJSON, diagnosis.ID); err != nil {
		t.Fatalf("corrupt Diagnosis setup error = %v", err)
	}
	_, err := repository.GetByRunID(context.Background(), run.ID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "diagnosis_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("Diagnosis row error disclosed stored content")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.GetByRunID(cancelled, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetByRunID(cancelled) error = %v, want context.Canceled", err)
	}
}

func testDiagnosis(id domain.DiagnosisID, run domain.AgentRun, evidence []domain.Evidence) domain.Diagnosis {
	evidenceIDs := make([]domain.EvidenceID, 0, len(evidence))
	observedFrom := evidence[0].ObservedAt
	observedTo := evidence[0].ObservedAt
	for _, item := range evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
		if item.ObservedAt.Before(observedFrom) {
			observedFrom = item.ObservedAt
		}
		if item.ObservedAt.After(observedTo) {
			observedTo = item.ObservedAt
		}
	}
	return domain.Diagnosis{
		ID:    id,
		RunID: run.ID,
		Scope: run.Scope,
		ConfirmedFacts: []domain.ConfirmedFact{{
			Statement:   "The bounded observations show a readiness problem.",
			EvidenceIDs: evidenceIDs,
		}},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{evidenceIDs[0]},
			Confidence:            domain.DiagnosisConfidenceLow,
			Falsifier:             "A later bounded observation reports readiness.",
		}},
		MissingInformation: []domain.MissingInformation{{
			Kind:   domain.MissingInformationTruncated,
			Detail: "Some source detail may be unavailable.",
			Impact: "The Diagnosis does not claim a complete root cause.",
		}},
		RecommendedActions: []domain.RecommendedAction{{
			Action:   "Review the readiness configuration.",
			Risk:     "A manual change may restart Pods.",
			Executed: false,
		}},
		AnswerMarkdown:     "The bounded observations support a cautious Diagnosis.",
		ValidationWarnings: []string{"Supporting detail may be partial."},
		ObservedFrom:       &observedFrom,
		ObservedTo:         &observedTo,
		CreatedAt:          observedTo.Add(time.Millisecond),
	}
}
