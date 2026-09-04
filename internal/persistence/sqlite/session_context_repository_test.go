package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestSessionContextRepositoryPagesPersistsCoverageAndCascades(t *testing.T) {
	t.Parallel()

	database := openTestDB(t, context.Background(), testStateDir(t), "session-context-round-trip")
	session := testSession(
		"00000000-0000-7000-8000-000000060001", "Session model context", domain.PrivacyModeStandard,
		time.UnixMilli(60_000).UTC(),
	)
	if err := NewSessionRepository(database).Create(context.Background(), session); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}
	runs := NewAgentRunRepository(database)
	for index := 0; index < 51; index++ {
		startedAt := time.UnixMilli(60_001 + int64(index*3)).UTC()
		messageID := domain.MessageID(fmt.Sprintf("00000000-0000-7000-8001-%012d", index*2+1))
		runID := domain.AgentRunID(fmt.Sprintf("00000000-0000-7000-8002-%012d", index+1))
		request, running := testRunningPair(messageID, runID, session.ID, startedAt)
		request.Content = fmt.Sprintf("Safe prior question %03d", index)
		request.Hash = domain.MessageContentHash(request.Content)
		if err := runs.Begin(context.Background(), request, running); err != nil {
			t.Fatalf("Begin(%d) error = %v", index, err)
		}
		finishedAt := startedAt.Add(time.Millisecond)
		terminal := testTerminalRun(running, domain.AgentRunStatusCompleted, finishedAt)
		answer := fmt.Sprintf("Safe final answer %03d", index)
		assistant := testMessage(
			domain.MessageID(fmt.Sprintf("00000000-0000-7000-8001-%012d", index*2+2)),
			session.ID, &runID, answer, finishedAt,
		)
		assistant.Role = domain.MessageRoleAssistant
		assistant.Format = domain.MessageFormatMarkdown
		assistant.Scope = &running.Scope
		diagnosis := domain.Diagnosis{
			ID:    domain.DiagnosisID(fmt.Sprintf("00000000-0000-7000-8003-%012d", index+1)),
			RunID: runID, Scope: running.Scope, AnswerMarkdown: answer, CreatedAt: finishedAt,
		}
		audit := coordinatedRunAudit(
			domain.AuditEventID(fmt.Sprintf("00000000-0000-7000-8004-%012d", index+1)),
			terminal, domain.AuditEventRunCompleted, finishedAt,
		)
		if err := runs.CompleteWithAudit(context.Background(), diagnosis, assistant, terminal, audit); err != nil {
			t.Fatalf("CompleteWithAudit(%d) error = %v", index, err)
		}
	}
	for index, terminalStatus := range []domain.AgentRunStatus{
		domain.AgentRunStatusFailed,
		domain.AgentRunStatusCancelled,
	} {
		startedAt := time.UnixMilli(70_000 + int64(index*3)).UTC()
		request, running := testRunningPair(
			domain.MessageID(fmt.Sprintf("00000000-0000-7000-8006-%012d", index+1)),
			domain.AgentRunID(fmt.Sprintf("00000000-0000-7000-8007-%012d", index+1)),
			session.ID,
			startedAt,
		)
		request.Content = fmt.Sprintf("Excluded terminal request %03d", index)
		request.Hash = domain.MessageContentHash(request.Content)
		if err := runs.Begin(context.Background(), request, running); err != nil {
			t.Fatalf("Begin(excluded %d) error = %v", index, err)
		}
		finishedAt := startedAt.Add(time.Millisecond)
		terminal := testTerminalRun(running, terminalStatus, finishedAt)
		eventType := domain.AuditEventRunFailed
		if terminalStatus == domain.AgentRunStatusCancelled {
			eventType = domain.AuditEventRunCancelled
		}
		audit := coordinatedRunAudit(
			domain.AuditEventID(fmt.Sprintf("00000000-0000-7000-8008-%012d", index+1)),
			terminal,
			eventType,
			finishedAt,
		)
		if err := runs.FinishWithAudit(context.Background(), terminal, audit); err != nil {
			t.Fatalf("FinishWithAudit(excluded %d) error = %v", index, err)
		}
	}

	repository := NewMessageRepository(database)
	first, err := repository.ListEligibleModelContext(context.Background(), application.ModelContextPageRequest{
		SessionID: session.ID, Limit: 100,
	})
	if err != nil || len(first.Messages) != 100 || first.Next == nil ||
		first.Messages[0].Content != "Safe prior question 000" || first.Messages[99].Content != "Safe final answer 049" {
		t.Fatalf("first context page = %d/%#v/%v", len(first.Messages), first.Next, err)
	}
	second, err := repository.ListEligibleModelContext(context.Background(), application.ModelContextPageRequest{
		SessionID: session.ID, Limit: 100, After: first.Next,
	})
	if err != nil || len(second.Messages) != 2 || second.Next != nil ||
		second.Messages[0].Content != "Safe prior question 050" || second.Messages[1].Content != "Safe final answer 050" {
		t.Fatalf("second context page = %#v/%v", second, err)
	}
	all := append(append([]domain.Message(nil), first.Messages...), second.Messages...)
	digest, coveredBytes, err := domain.SessionContextCoverageDigest(all[:100])
	if err != nil {
		t.Fatalf("SessionContextCoverageDigest() error = %v", err)
	}
	summary := domain.SessionContextSummary{
		SessionID: session.ID, Text: "A bounded safe summary covers the first fifty completed turns.",
		SummaryHash:   domain.SHA256Hex("A bounded safe summary covers the first fifty completed turns."),
		SchemaVersion: domain.SessionContextSummarySchemaVersion, PolicyVersion: domain.SafeConversationContextPolicyVersion,
		CoveredFirstID: all[0].ID, CoveredThroughID: all[99].ID, CoveredCount: 100,
		CoveredBytes: coveredBytes, CoverageDigest: digest, GeneratedAt: time.UnixMilli(61_000).UTC(),
		AgentProfile: "agent", AgentOriginHash: domain.SHA256Hex("https://model.example"),
	}
	if err := repository.SaveSessionContextSummary(context.Background(), summary); err != nil {
		t.Fatalf("SaveSessionContextSummary() error = %v", err)
	}
	loaded, found, err := repository.LoadSessionContextSummary(context.Background(), session.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded, summary) {
		t.Fatalf("LoadSessionContextSummary() = %#v/%v/%v", loaded, found, err)
	}
	if err := repository.SaveSessionContextSummary(context.Background(), summary); err == nil {
		t.Fatal("equal summary coverage was accepted twice")
	}

	advanced := summary
	advanced.Text = "A bounded safe summary now covers every completed turn."
	advanced.SummaryHash = domain.SHA256Hex(advanced.Text)
	advanced.CoveredThroughID = all[len(all)-1].ID
	advanced.CoveredCount = len(all)
	advanced.CoverageDigest, advanced.CoveredBytes, err = domain.SessionContextCoverageDigest(all)
	advanced.GeneratedAt = advanced.GeneratedAt.Add(time.Millisecond)
	if err != nil {
		t.Fatalf("advanced SessionContextCoverageDigest() error = %v", err)
	}
	if err := repository.SaveSessionContextSummary(context.Background(), advanced); err != nil {
		t.Fatalf("SaveSessionContextSummary(advanced) error = %v", err)
	}
	if err := NewSessionRepository(database).Delete(context.Background(), session.ID); err != nil {
		t.Fatalf("Delete(Session) error = %v", err)
	}
	if _, found, err := repository.LoadSessionContextSummary(context.Background(), session.ID); err != nil || found {
		t.Fatalf("summary after Session delete = found %v/error %v", found, err)
	}
}

func TestSessionContextRepositoryRejectsCancellationAndInvalidRequestsWithoutQuery(t *testing.T) {
	t.Parallel()

	database := openTestDB(t, context.Background(), testStateDir(t), "session-context-denials")
	repository := NewMessageRepository(database)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ListEligibleModelContext(cancelled, application.ModelContextPageRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ListEligibleModelContext() error = %v", err)
	}
	if _, err := repository.ListEligibleModelContext(context.Background(), application.ModelContextPageRequest{}); !errors.Is(err, application.ErrModelContextUnavailable) {
		t.Fatalf("invalid ListEligibleModelContext() error = %v", err)
	}
	if _, _, err := repository.LoadSessionContextSummary(cancelled, "00000000-0000-7000-8000-000000060099"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled LoadSessionContextSummary() error = %v", err)
	}
}
