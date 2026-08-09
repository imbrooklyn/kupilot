package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestToolInvocationRepositoryAtomicallyStoresInvocationAndEvidence(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "tool-evidence-round-trip")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000003001", "00000000-0000-7000-8000-000000003002", "00000000-0000-7000-8000-000000003003", time.UnixMilli(200).UTC())
	tools := NewToolInvocationRepository(db)
	evidenceRepository := NewEvidenceRepository(db)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000003004", run, 1, time.UnixMilli(201).UTC())
	evidence := []domain.Evidence{
		testEvidence("00000000-0000-7000-8000-000000003005", invocation, time.UnixMilli(202).UTC()),
		testEvidence("00000000-0000-7000-8000-000000003006", invocation, time.UnixMilli(203).UTC()),
	}
	evidence[1].Category = domain.EvidenceCategoryEvent
	evidence[1].Truncated = true
	evidence[1].Fingerprint = domain.SHA256Hex("second-normalized-evidence")
	invocation.EvidenceCount = len(evidence)
	if err := tools.Save(context.Background(), invocation, evidence); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	gotInvocation, err := tools.GetByID(context.Background(), invocation.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(gotInvocation, invocation) {
		t.Fatalf("GetByID() = %#v, want %#v", gotInvocation, invocation)
	}
	listedInvocations, err := tools.ListByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListByRun() error = %v", err)
	}
	if !reflect.DeepEqual(listedInvocations, []domain.ToolInvocation{invocation}) {
		t.Fatalf("ListByRun() = %#v", listedInvocations)
	}

	gotEvidence, err := evidenceRepository.GetByID(context.Background(), evidence[0].ID)
	if err != nil {
		t.Fatalf("Evidence GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(gotEvidence, evidence[0]) {
		t.Fatalf("Evidence GetByID() = %#v, want %#v", gotEvidence, evidence[0])
	}
	listedEvidence, err := evidenceRepository.ListByInvocation(context.Background(), invocation.ID)
	if err != nil {
		t.Fatalf("ListByInvocation() error = %v", err)
	}
	if !reflect.DeepEqual(listedEvidence, evidence) {
		t.Fatalf("ListByInvocation() = %#v", listedEvidence)
	}
	if listedEvidence[1].DetailState() != domain.EvidenceDetailPartial {
		t.Fatal("truncated Evidence did not remain explicitly partial")
	}

	if _, err := tools.GetByID(context.Background(), "00000000-0000-7000-8000-000000003099"); !errors.Is(err, ErrToolInvocationNotFound) {
		t.Fatalf("Tool GetByID(missing) error = %v", err)
	}
	if _, err := evidenceRepository.GetByID(context.Background(), "00000000-0000-7000-8000-000000003098"); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("Evidence GetByID(missing) error = %v", err)
	}
}

func TestToolInvocationRepositoryRejectsMismatchedMinimalAndOversizedData(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "tool-evidence-denials")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000003101", "00000000-0000-7000-8000-000000003102", "00000000-0000-7000-8000-000000003103", time.UnixMilli(210).UTC())
	tools := NewToolInvocationRepository(db)
	evidenceRepository := NewEvidenceRepository(db)

	invocation := testToolInvocation("00000000-0000-7000-8000-000000003104", run, 1, time.UnixMilli(211).UTC())
	evidence := testEvidence("00000000-0000-7000-8000-000000003105", invocation, time.UnixMilli(212).UTC())
	invocation.EvidenceCount = 1
	mismatched := evidence
	mismatched.InvocationID = "00000000-0000-7000-8000-000000003199"
	if err := tools.Save(context.Background(), invocation, []domain.Evidence{mismatched}); !errors.Is(err, domain.ErrInvalidEvidence) {
		t.Fatalf("Save(mismatched Evidence) error = %v, want ErrInvalidEvidence", err)
	}
	if _, err := tools.GetByID(context.Background(), invocation.ID); !errors.Is(err, ErrToolInvocationNotFound) {
		t.Fatalf("mismatched transaction left ToolInvocation: %v", err)
	}
	if _, err := evidenceRepository.GetByID(context.Background(), mismatched.ID); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("mismatched transaction left Evidence: %v", err)
	}

	duplicateInvocation := invocation
	duplicateInvocation.ID = "00000000-0000-7000-8000-000000003106"
	duplicateInvocation.Sequence = 2
	duplicateEvidence := testEvidence("00000000-0000-7000-8000-000000003107", duplicateInvocation, time.UnixMilli(212).UTC())
	duplicateInvocation.EvidenceCount = 2
	err := tools.Save(context.Background(), duplicateInvocation, []domain.Evidence{duplicateEvidence, duplicateEvidence})
	assertStorageError(t, err, ClassPersistenceUnavailable, "tool_invocation_save_failed")
	if _, err := tools.GetByID(context.Background(), duplicateInvocation.ID); !errors.Is(err, ErrToolInvocationNotFound) {
		t.Fatalf("failed Evidence insert left ToolInvocation: %v", err)
	}

	oversized := invocation
	oversized.ID = "00000000-0000-7000-8000-000000003108"
	text := strings.Repeat("s", 4097)
	oversized.ResultSummary = &text
	if err := tools.Save(context.Background(), oversized, nil); !errors.Is(err, domain.ErrInvalidToolInvocation) {
		t.Fatalf("Save(oversized) error = %v, want ErrInvalidToolInvocation", err)
	}

	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE sessions
		SET title = '', privacy_mode = 'minimal', last_context = NULL,
			last_namespace = NULL, selected_resource_json = NULL, summary = NULL
		WHERE id = ?
	`, run.SessionID); err != nil {
		t.Fatalf("minimal Session setup error = %v", err)
	}
	minimal := invocation
	minimal.ID = "00000000-0000-7000-8000-000000003109"
	minimal.Sequence = 3
	minimal.EvidenceCount = 0
	if err := tools.Save(context.Background(), minimal, nil); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("Save(minimal) error = %v, want ErrDurableContentDisabled", err)
	}
}

func TestToolAndEvidenceRepositoriesUseStrictRowsSafeErrorsAndContexts(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "tool-evidence-strict")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000003201", "00000000-0000-7000-8000-000000003202", "00000000-0000-7000-8000-000000003203", time.UnixMilli(220).UTC())
	tools := NewToolInvocationRepository(db)
	evidenceRepository := NewEvidenceRepository(db)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000003204", run, 1, time.UnixMilli(221).UTC())
	evidence := testEvidence("00000000-0000-7000-8000-000000003205", invocation, time.UnixMilli(222).UTC())
	invocation.EvidenceCount = 1
	if err := tools.Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}

	rowCanary := "unsupported-evidence-category-canary"
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE evidence_items SET category = ? WHERE id = ?`, rowCanary, evidence.ID); err != nil {
		t.Fatalf("corrupt Evidence setup error = %v", err)
	}
	_, err := evidenceRepository.GetByID(context.Background(), evidence.ID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "evidence_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("Evidence row error disclosed stored content")
	}

	boundCanary := "bound-tool-purpose-canary"
	duplicate := invocation
	duplicate.ID = "00000000-0000-7000-8000-000000003206"
	purpose := boundCanary
	duplicate.Purpose = &purpose
	duplicate.EvidenceCount = 0
	err = tools.Save(context.Background(), duplicate, nil)
	assertStorageError(t, err, ClassPersistenceUnavailable, "tool_invocation_save_failed")
	if strings.Contains(err.Error(), boundCanary) {
		t.Fatal("Tool constraint error disclosed a bound value")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := invocation
	other.ID = "00000000-0000-7000-8000-000000003207"
	other.Sequence = 2
	other.EvidenceCount = 0
	err = tools.Save(cancelled, other, nil)
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := tools.ListByRun(cancelled, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByRun(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := evidenceRepository.ListByInvocation(cancelled, invocation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByInvocation(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestEvidenceRepositoryRejectsMoreThanOneResultBudget(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "evidence-over-limit")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000003301", "00000000-0000-7000-8000-000000003302", "00000000-0000-7000-8000-000000003303", time.UnixMilli(230).UTC())
	invocation := testToolInvocation("00000000-0000-7000-8000-000000003304", run, 1, time.UnixMilli(231).UTC())
	evidence := testEvidence("00000000-0000-7000-8000-000000003305", invocation, time.UnixMilli(232).UTC())
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+3306)
		fingerprint := domain.SHA256Hex(id)
		if _, err := db.handle.ExecContext(context.Background(), `
			INSERT INTO evidence_items (
				id, run_id, invocation_id, category, resource_ref_json,
				fact, fingerprint, observed_at_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, id, run.ID, invocation.ID, "condition", `{"api_version":"v1","kind":"Pod","namespace":"test-namespace","name":"sample-pod"}`, "Synthetic safe fact", fingerprint, 232); err != nil {
			t.Fatalf("insert over-limit Evidence %d error = %v", index, err)
		}
	}
	_, err := NewEvidenceRepository(db).ListByInvocation(context.Background(), invocation.ID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "evidence_row_invalid")
}

func testToolInvocation(id domain.ToolInvocationID, run domain.AgentRun, sequence int, startedAt time.Time) domain.ToolInvocation {
	finishedAt := startedAt.Add(5 * time.Millisecond)
	purpose := "Inspect one bounded projection."
	summary := "A bounded projection was collected."
	arguments := `{"kind":"Pod","name":"sample-pod"}`
	return domain.ToolInvocation{
		ID:              id,
		RunID:           run.ID,
		Sequence:        sequence,
		Name:            domain.ToolNameGetResource,
		Version:         "tool-v1",
		Purpose:         &purpose,
		Scope:           run.Scope,
		ArgumentsJSON:   arguments,
		ArgumentsDigest: domain.SHA256Hex(arguments),
		Status:          domain.ToolInvocationStatusSucceeded,
		ResultSummary:   &summary,
		ReturnedBytes:   256,
		StartedAt:       &startedAt,
		FinishedAt:      &finishedAt,
	}
}

func testEvidence(id domain.EvidenceID, invocation domain.ToolInvocation, observedAt time.Time) domain.Evidence {
	sourcePath := "status.conditions[0].status"
	severity := domain.EvidenceSeverityWarning
	return domain.Evidence{
		ID:           id,
		RunID:        invocation.RunID,
		InvocationID: invocation.ID,
		Category:     domain.EvidenceCategoryCondition,
		Scope:        invocation.Scope,
		Resource: domain.ResourceRef{
			APIVersion:      "v1",
			Kind:            "Pod",
			Namespace:       invocation.Scope.Namespace,
			Name:            "sample-pod",
			UID:             "pod-uid",
			ResourceVersion: "20",
		},
		Fact:           "The projected Pod condition is not ready.",
		SourcePath:     &sourcePath,
		Severity:       &severity,
		RedactionCount: 1,
		Fingerprint:    domain.SHA256Hex("normalized-evidence"),
		ObservedAt:     observedAt,
	}
}
