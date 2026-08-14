package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestCoordinatorExportsConfirmedCurrentSessionAfterContentFreeAudit(t *testing.T) {
	clock := newCoordinatorClock()
	var agentCalls atomic.Int64
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		agentCalls.Add(1)
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	reader := &recordingExportReader{snapshot: exportCoordinatorSnapshot(session)}
	writer := new(recordingExportWriter)
	coordinator.exports = reader
	coordinator.exportFiles = writer
	coordinator.exportText = security.NewRedactor()

	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 81})
	if err != nil || show.Privacy == nil {
		t.Fatalf("ShowPrivacy() = %#v, %v", show, err)
	}
	command := UICommand{
		Kind: UICommandExportSession, RequestID: 81, PrivacyRevision: show.Privacy.Revision,
		Export: &ExportSummaryIntent{
			SessionID: session.ID, TargetPath: "/private/export/session-summary.md",
			ExpectedCurrent: true, Confirmed: true, SchemaVersion: ExportSummarySchemaVersion,
		},
	}
	result, err := coordinator.ExecuteUICommand(context.Background(), command)
	if err != nil || result.Validate() != nil || result.Export == nil || result.Export.SessionID != session.ID ||
		result.Export.SchemaVersion != ExportSummarySchemaVersion || reader.callCount() != 1 || writer.callCount() != 1 || agentCalls.Load() != 0 {
		t.Fatalf("export outcome/error/reader/writer/Agent = %#v/%v/%d/%d/%d", result, err, reader.callCount(), writer.callCount(), agentCalls.Load())
	}
	written := writer.lastFile()
	if written.TargetPath != command.Export.TargetPath || !strings.Contains(string(written.Content), ExportSummarySchemaVersion) {
		t.Fatalf("written export = %#v", written)
	}

	persistence.mu.Lock()
	audits := append([]domain.AuditEvent(nil), persistence.audits...)
	persistence.mu.Unlock()
	if len(audits) != 2 {
		t.Fatalf("audit count = %d, want create plus export request", len(audits))
	}
	audit := audits[1]
	if audit.Type != domain.AuditEventSessionExportRequested || audit.SessionID == nil || *audit.SessionID != session.ID ||
		audit.Actor != domain.AuditActorUser || audit.Outcome != domain.AuditOutcomeSuccess ||
		audit.Details.Operation == nil || *audit.Details.Operation != "export_summary" ||
		audit.Details.PolicyVersion == nil || *audit.Details.PolicyVersion != ExportSummarySchemaVersion {
		t.Fatalf("export audit = %#v", audit)
	}
	encoded, marshalErr := json.Marshal(audit)
	if marshalErr != nil {
		t.Fatalf("Marshal(audit) error = %v", marshalErr)
	}
	for _, forbidden := range []string{command.Export.TargetPath, "A safe exported question."} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("audit disclosed export content or target: %s", encoded)
		}
	}
}

func TestCoordinatorExportDenialsPerformZeroFilesystemWrites(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Coordinator, *memoryCoordinatorPersistence, *recordingExportReader, *recordingExportWriter)
		mutate    func(*UICommand)
		cancel    bool
		wantError error
		wantReads int
		wantAudit int
	}{
		{
			name: "unconfirmed", mutate: func(command *UICommand) { command.Export.Confirmed = false },
			wantError: ErrInvalidUICommand,
		},
		{name: "cancelled", cancel: true, wantError: context.Canceled},
		{
			name: "stale target", mutate: func(command *UICommand) { command.Export.SessionID = domain.SessionID(coordinatorUUID(999)) },
		},
		{
			name: "reader failure", configure: func(_ *Coordinator, _ *memoryCoordinatorPersistence, reader *recordingExportReader, _ *recordingExportWriter) {
				reader.err = errors.New("synthetic snapshot failure")
			}, wantReads: 1,
		},
		{
			name: "audit failure", configure: func(_ *Coordinator, persistence *memoryCoordinatorPersistence, _ *recordingExportReader, _ *recordingExportWriter) {
				persistence.setAuditFailure(domain.AuditEventSessionExportRequested)
			}, wantReads: 1,
		},
		{
			name: "active run", configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *recordingExportReader, _ *recordingExportWriter) {
				coordinator.mu.Lock()
				coordinator.starting = true
				coordinator.mu.Unlock()
			}, wantError: ErrRunAlreadyActive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			session := createCoordinatorSession(t, coordinator)
			reader := &recordingExportReader{snapshot: exportCoordinatorSnapshot(session)}
			writer := new(recordingExportWriter)
			coordinator.exports = reader
			coordinator.exportFiles = writer
			coordinator.exportText = security.NewRedactor()
			show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 82})
			if err != nil {
				t.Fatalf("ShowPrivacy() error = %v", err)
			}
			command := UICommand{
				Kind: UICommandExportSession, RequestID: 82, PrivacyRevision: show.Privacy.Revision,
				Export: &ExportSummaryIntent{
					SessionID: session.ID, TargetPath: "/private/export/denied.md",
					ExpectedCurrent: true, Confirmed: true, SchemaVersion: ExportSummarySchemaVersion,
				},
			}
			if test.configure != nil {
				test.configure(coordinator, persistence, reader, writer)
			}
			if test.mutate != nil {
				test.mutate(&command)
			}
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			beforeAudits := exportAuditCount(persistence)
			result, executeErr := coordinator.ExecuteUICommand(ctx, command)
			if test.wantError != nil {
				if !errors.Is(executeErr, test.wantError) {
					t.Fatalf("ExecuteUICommand() error = %v, want %v", executeErr, test.wantError)
				}
			} else if executeErr != nil || result.Failure != UIQueryUnavailable {
				t.Fatalf("denied export = %#v, %v", result, executeErr)
			}
			if reader.callCount() != test.wantReads || writer.callCount() != 0 || exportAuditCount(persistence)-beforeAudits != test.wantAudit {
				t.Fatalf("denial calls = reader %d writer %d audit %d", reader.callCount(), writer.callCount(), exportAuditCount(persistence)-beforeAudits)
			}
		})
	}
}

func TestCoordinatorExportFilesystemFailureLeavesRequestedAuditAndNoSuccess(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	reader := &recordingExportReader{snapshot: exportCoordinatorSnapshot(session)}
	writer := &recordingExportWriter{err: errors.New("synthetic disk failure")}
	coordinator.exports, coordinator.exportFiles, coordinator.exportText = reader, writer, security.NewRedactor()
	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 83})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}
	result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandExportSession, RequestID: 83, PrivacyRevision: show.Privacy.Revision,
		Export: &ExportSummaryIntent{SessionID: session.ID, TargetPath: "/private/export/fail.md", ExpectedCurrent: true, Confirmed: true, SchemaVersion: ExportSummarySchemaVersion},
	})
	if err != nil || result.Failure != UIQueryUnavailable || reader.callCount() != 1 || writer.callCount() != 1 || exportAuditCount(persistence) != 1 {
		t.Fatalf("filesystem failure = %#v/%v reader=%d writer=%d export-audit=%d", result, err, reader.callCount(), writer.callCount(), exportAuditCount(persistence))
	}
}

func TestCoordinatorExportActiveRunDenialConsumesConfirmation(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	reader := &recordingExportReader{snapshot: exportCoordinatorSnapshot(session)}
	writer := new(recordingExportWriter)
	coordinator.exports, coordinator.exportFiles, coordinator.exportText = reader, writer, security.NewRedactor()
	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 86})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}
	command := UICommand{
		Kind: UICommandExportSession, RequestID: 86, PrivacyRevision: show.Privacy.Revision,
		Export: &ExportSummaryIntent{
			SessionID: session.ID, TargetPath: "/private/export/active.md",
			ExpectedCurrent: true, Confirmed: true, SchemaVersion: ExportSummarySchemaVersion,
		},
	}
	coordinator.mu.Lock()
	coordinator.starting = true
	coordinator.mu.Unlock()
	if _, err := coordinator.ExecuteUICommand(context.Background(), command); !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("active export error = %v, want ErrRunAlreadyActive", err)
	}
	coordinator.mu.Lock()
	coordinator.starting = false
	coordinator.mu.Unlock()
	if _, err := coordinator.ExecuteUICommand(context.Background(), command); !errors.Is(err, ErrPrivacyReviewStale) {
		t.Fatalf("replayed export error = %v, want ErrPrivacyReviewStale", err)
	}
	if reader.callCount() != 0 || writer.callCount() != 0 || exportAuditCount(persistence) != 0 {
		t.Fatalf("active/replayed calls = reader %d writer %d audit %d", reader.callCount(), writer.callCount(), exportAuditCount(persistence))
	}
}

func TestCoordinatorSerializesExportAgainstDeletionAndDoesNotReplayAfterRestart(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	reader := &recordingExportReader{
		snapshot: exportCoordinatorSnapshot(session), entered: make(chan struct{}), release: make(chan struct{}),
	}
	writer := new(recordingExportWriter)
	coordinator.exports, coordinator.exportFiles, coordinator.exportText = reader, writer, security.NewRedactor()
	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 84})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}
	command := UICommand{
		Kind: UICommandExportSession, RequestID: 84, PrivacyRevision: show.Privacy.Revision,
		Export: &ExportSummaryIntent{SessionID: session.ID, TargetPath: "/private/export/race.md", ExpectedCurrent: true, Confirmed: true, SchemaVersion: ExportSummarySchemaVersion},
	}
	type exportResult struct {
		outcome UICommandOutcome
		err     error
	}
	done := make(chan exportResult, 1)
	go func() {
		outcome, executeErr := coordinator.ExecuteUICommand(context.Background(), command)
		done <- exportResult{outcome: outcome, err: executeErr}
	}()
	<-reader.entered
	_, deleteErr := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandDeleteSession, RequestID: 85,
		Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
	})
	if !errors.Is(deleteErr, ErrCoordinatorBusy) || persistence.deleteWrites() != 0 {
		t.Fatalf("concurrent delete error/writes = %v/%d", deleteErr, persistence.deleteWrites())
	}
	close(reader.release)
	completed := <-done
	if completed.err != nil || completed.outcome.Export == nil || writer.callCount() != 1 {
		t.Fatalf("export completion = %#v, %v, writes=%d", completed.outcome, completed.err, writer.callCount())
	}

	restarted, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	restartReader := &recordingExportReader{snapshot: exportCoordinatorSnapshot(session)}
	restartWriter := new(recordingExportWriter)
	restarted.exports, restarted.exportFiles, restarted.exportText = restartReader, restartWriter, security.NewRedactor()
	if _, err := restarted.ExecuteUICommand(context.Background(), command); !errors.Is(err, ErrPrivacyReviewStale) ||
		restartReader.callCount() != 0 || restartWriter.callCount() != 0 {
		t.Fatalf("restart replay error/reader/writer = %v/%d/%d", err, restartReader.callCount(), restartWriter.callCount())
	}
}

type recordingExportReader struct {
	mu       sync.Mutex
	calls    int
	snapshot SessionExportSnapshot
	err      error
	entered  chan struct{}
	release  chan struct{}
}

func (reader *recordingExportReader) ReadExportSnapshot(ctx context.Context, _ domain.SessionID) (SessionExportSnapshot, error) {
	reader.mu.Lock()
	reader.calls++
	snapshot, err, entered, release := reader.snapshot, reader.err, reader.entered, reader.release
	reader.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-ctx.Done():
			return SessionExportSnapshot{}, ctx.Err()
		case <-release:
		}
	}
	return snapshot, err
}

func (reader *recordingExportReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type recordingExportWriter struct {
	mu    sync.Mutex
	calls int
	file  ExportFile
	err   error
}

func (writer *recordingExportWriter) WriteSummary(_ context.Context, file ExportFile) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.calls++
	writer.file = ExportFile{TargetPath: file.TargetPath, Content: append([]byte(nil), file.Content...)}
	return writer.err
}

func (writer *recordingExportWriter) callCount() int {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.calls
}

func (writer *recordingExportWriter) lastFile() ExportFile {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return ExportFile{TargetPath: writer.file.TargetPath, Content: append([]byte(nil), writer.file.Content...)}
}

func exportCoordinatorSnapshot(session domain.Session) SessionExportSnapshot {
	return SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: session.ID, Title: "Safe Session", PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
		},
		Messages: []ExportMessageRecord{{Role: domain.MessageRoleUser, Content: "A safe exported question.", CreatedAt: session.CreatedAt}},
	}
}

func exportAuditCount(persistence *memoryCoordinatorPersistence) int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	count := 0
	for _, event := range persistence.audits {
		if event.Type == domain.AuditEventSessionExportRequested {
			count++
		}
	}
	return count
}
