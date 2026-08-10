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

func TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow(t *testing.T) {
	testCases := []struct {
		name               string
		evidenceCount      int
		referenced         []int
		truncated          []int
		wantState          domain.EvidenceDetailState
		wantReferenceCount int
	}{
		{name: "zero references", evidenceCount: 2, wantState: domain.EvidenceDetailAvailable},
		{name: "one accepted Evidence reference", evidenceCount: 1, referenced: []int{0}, wantState: domain.EvidenceDetailAvailable, wantReferenceCount: 1},
		{name: "subset of accepted Evidence references", evidenceCount: 3, referenced: []int{0, 1}, wantState: domain.EvidenceDetailAvailable, wantReferenceCount: 2},
		{name: "truncated accepted Evidence", evidenceCount: 2, referenced: []int{0}, truncated: []int{1}, wantState: domain.EvidenceDetailPartial, wantReferenceCount: 1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestDB(t, context.Background(), testStateDir(t), "diagnosis-all-evidence-window")
			run := seedStandardRun(t, db,
				"00000000-0000-7000-8000-000000004301",
				"00000000-0000-7000-8000-000000004302",
				"00000000-0000-7000-8000-000000004303",
				time.UnixMilli(330).UTC(),
			)
			invocation := testToolInvocation("00000000-0000-7000-8000-000000004304", run, 1, time.UnixMilli(331).UTC())
			evidenceIDs := []domain.EvidenceID{
				"00000000-0000-7000-8000-000000004305",
				"00000000-0000-7000-8000-000000004306",
				"00000000-0000-7000-8000-000000004307",
			}
			evidence := make([]domain.Evidence, testCase.evidenceCount)
			for index := range evidence {
				evidence[index] = testEvidence(
					evidenceIDs[index],
					invocation,
					time.UnixMilli(int64(332+index)).UTC(),
				)
			}
			for _, index := range testCase.truncated {
				evidence[index].Truncated = true
				evidence[index].Fingerprint = domain.SHA256Hex("truncated-all-evidence-window")
			}
			invocation.EvidenceCount = len(evidence)
			if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, evidence); err != nil {
				t.Fatalf("Save(ToolInvocation) error = %v", err)
			}

			diagnosis := testDiagnosis("00000000-0000-7000-8000-000000004309", run, evidence)
			diagnosis.ConfirmedFacts = nil
			diagnosis.Hypotheses = nil
			if len(testCase.referenced) > 0 {
				ids := make([]domain.EvidenceID, len(testCase.referenced))
				for index, evidenceIndex := range testCase.referenced {
					ids[index] = evidence[evidenceIndex].ID
				}
				diagnosis.ConfirmedFacts = []domain.ConfirmedFact{{
					Statement: "The accepted Evidence supports this bounded observation.", EvidenceIDs: ids,
				}}
			}
			diagnosis.EvidenceDetailsState = testCase.wantState
			repository := NewDiagnosisRepository(db)
			if err := repository.Save(context.Background(), diagnosis); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			got, err := repository.GetByRunID(context.Background(), run.ID)
			if err != nil {
				t.Fatalf("GetByRunID() error = %v", err)
			}
			if len(got.ReferencedEvidenceIDs()) != testCase.wantReferenceCount || got.EvidenceDetailsState != testCase.wantState ||
				got.ObservedFrom == nil || got.ObservedTo == nil ||
				!got.ObservedFrom.Equal(evidence[0].ObservedAt) || !got.ObservedTo.Equal(evidence[len(evidence)-1].ObservedAt) {
				t.Fatalf("GetByRunID() = %#v", got)
			}
			if testCase.wantReferenceCount == 0 {
				if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM evidence_items WHERE id = ?`, evidence[0].ID); err != nil {
					t.Fatalf("delete first unreferenced Evidence error = %v", err)
				}
				partial, err := repository.GetByRunID(context.Background(), run.ID)
				if err != nil || partial.EvidenceDetailsState != domain.EvidenceDetailPartial {
					t.Fatalf("GetByRunID(partial unreferenced Evidence) = %#v/%v", partial, err)
				}
				if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM evidence_items WHERE id = ?`, evidence[1].ID); err != nil {
					t.Fatalf("delete final unreferenced Evidence error = %v", err)
				}
				expired, err := repository.GetByRunID(context.Background(), run.ID)
				if err != nil || expired.EvidenceDetailsState != domain.EvidenceDetailExpired {
					t.Fatalf("GetByRunID(expired unreferenced Evidence) = %#v/%v", expired, err)
				}
			}
		})
	}

	t.Run("Tool result truncation without Evidence", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "diagnosis-empty-truncated-window")
		run := seedStandardRun(t, db,
			"00000000-0000-7000-8000-000000004321",
			"00000000-0000-7000-8000-000000004322",
			"00000000-0000-7000-8000-000000004323",
			time.UnixMilli(340).UTC(),
		)
		invocation := testToolInvocation("00000000-0000-7000-8000-000000004324", run, 1, time.UnixMilli(341).UTC())
		invocation.Truncated = true
		if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, nil); err != nil {
			t.Fatalf("Save(truncated ToolInvocation) error = %v", err)
		}
		diagnosis := domain.Diagnosis{
			ID:    "00000000-0000-7000-8000-000000004325",
			RunID: run.ID,
			Scope: run.Scope,
			MissingInformation: []domain.MissingInformation{{
				Kind:   domain.MissingInformationTruncated,
				Detail: "The Tool result reached a fixed limit before producing Evidence.",
				Impact: "The Diagnosis cannot account for content outside the admitted result.",
			}},
			AnswerMarkdown:       "No accepted Evidence was available from the truncated result.",
			CreatedAt:            time.UnixMilli(347).UTC(),
			EvidenceDetailsState: domain.EvidenceDetailPartial,
		}
		repository := NewDiagnosisRepository(db)
		if err := repository.Save(context.Background(), diagnosis); err != nil {
			t.Fatalf("Save(truncated Diagnosis without Evidence) error = %v", err)
		}
		got, err := repository.GetByRunID(context.Background(), run.ID)
		if err != nil || got.ObservedFrom != nil || got.ObservedTo != nil ||
			got.EvidenceDetailsState != domain.EvidenceDetailPartial {
			t.Fatalf("GetByRunID(truncated Diagnosis without Evidence) = %#v/%v", got, err)
		}
	})
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
	if err := NewAgentRunRepository(db).Finish(context.Background(), testTerminalRun(run, domain.AgentRunStatusCompleted, time.UnixMilli(313).UTC())); err != nil {
		t.Fatalf("Finish(AgentRun) error = %v", err)
	}
	repository := NewDiagnosisRepository(db)
	otherRun := seedStandardRun(t, db, "00000000-0000-7000-8000-000000004111", "00000000-0000-7000-8000-000000004112", "00000000-0000-7000-8000-000000004113", time.UnixMilli(314).UTC())
	otherInvocation := testToolInvocation("00000000-0000-7000-8000-000000004114", otherRun, 1, time.UnixMilli(315).UTC())
	otherEvidence := testEvidence("00000000-0000-7000-8000-000000004115", otherInvocation, time.UnixMilli(316).UTC())
	otherInvocation.EvidenceCount = 1
	if err := tools.Save(context.Background(), otherInvocation, []domain.Evidence{otherEvidence}); err != nil {
		t.Fatalf("Save(other ToolInvocation) error = %v", err)
	}
	crossRunReference := testDiagnosis("00000000-0000-7000-8000-000000004116", run, []domain.Evidence{evidence})
	crossRunReference.ConfirmedFacts[0].EvidenceIDs[0] = otherEvidence.ID
	if err := repository.Save(context.Background(), crossRunReference); !errors.Is(err, ErrDiagnosisEvidenceInvalid) {
		t.Fatalf("Save(cross-run Evidence) error = %v, want ErrDiagnosisEvidenceInvalid", err)
	}
	if _, err := repository.GetByRunID(context.Background(), run.ID); !errors.Is(err, ErrDiagnosisNotFound) {
		t.Fatalf("cross-run reference left Diagnosis: %v", err)
	}

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
