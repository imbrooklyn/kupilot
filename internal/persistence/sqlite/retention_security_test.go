package sqlite

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDatabaseAndWALExcludeProhibitedContentCanaries(t *testing.T) {
	stateDir := testStateDir(t)
	db := openTestDB(t, context.Background(), stateDir, "persistence-file-canaries")
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000007001",
		"00000000-0000-7000-8000-000000007002",
		"00000000-0000-7000-8000-000000007003",
		time.UnixMilli(700).UTC(),
	)

	prohibited := []struct {
		name  string
		value string
	}{
		{name: "credential", value: strings.Join([]string{"synthetic", "api", "key", "canary", "7101"}, "-")},
		{name: "kubeconfig", value: strings.Join([]string{"synthetic", "kubeconfig", "canary", "7102"}, "-")},
		{name: "raw log", value: strings.Join([]string{"synthetic", "raw", "log", "canary", "7103"}, "-")},
		{name: "full prompt", value: strings.Join([]string{"synthetic", "full", "prompt", "canary", "7104"}, "-")},
		{name: "response body", value: strings.Join([]string{"synthetic", "response", "body", "canary", "7105"}, "-")},
		{name: "raw Tool output", value: strings.Join([]string{"synthetic", "raw", "tool", "output", "canary", "7106"}, "-")},
	}

	eligibleCanary := strings.Join([]string{"eligible", "projected", "metadata", "canary", "7199"}, "-")
	invocation := testToolInvocation("00000000-0000-7000-8000-000000007011", run, 1, time.UnixMilli(703).UTC())
	invocation.Purpose = &eligibleCanary
	evidence := testEvidence("00000000-0000-7000-8000-000000007012", invocation, time.UnixMilli(704).UTC())
	evidence.Fingerprint = domain.SHA256Hex(prohibited[2].value)
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(safe Tool metadata) error = %v", err)
	}
	request := testModelRequest("00000000-0000-7000-8000-000000007013", run.ID, 1, time.UnixMilli(705).UTC())
	request.PromptFingerprint = domain.SHA256Hex(prohibited[3].value)
	responseFingerprint := domain.SHA256Hex(prohibited[4].value)
	request.ResponseFingerprint = &responseFingerprint
	if err := NewModelRequestRepository(db).Save(context.Background(), request); err != nil {
		t.Fatalf("Save(safe model metadata) error = %v", err)
	}
	if err := NewSettingsRepository(db).Put(context.Background(), testRetentionSetting(30, time.UnixMilli(706).UTC())); err != nil {
		t.Fatalf("Put(safe setting) error = %v", err)
	}

	var storageBytes []byte
	for _, suffix := range []string{"", "-wal"} {
		path := filepath.Join(stateDir, databaseFilename+suffix)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", suffix, err)
		}
		storageBytes = append(storageBytes, content...)
	}
	if !bytes.Contains(storageBytes, []byte(eligibleCanary)) {
		t.Fatal("DB/WAL scan did not observe the eligible control value")
	}
	for _, canary := range prohibited {
		if bytes.Contains(storageBytes, []byte(canary.value)) {
			t.Errorf("DB/WAL contains prohibited %s content", canary.name)
		}
	}
}

func TestRepositorySourcesKeepFixedContextAwareSQL(t *testing.T) {
	directory := currentSQLiteDirectory(t)
	files, err := filepath.Glob(filepath.Join(directory, "*_repository.go"))
	if err != nil {
		t.Fatalf("Glob(repository sources) error = %v", err)
	}
	if len(files) == 0 {
		t.Fatal("repository source set is empty")
	}
	forbidden := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "SELECT star", pattern: regexp.MustCompile(`(?is)\bSELECT\s+\*`)},
		{name: "Unsafe", pattern: regexp.MustCompile(`\.Unsafe\s*\(`)},
		{name: "Must helper", pattern: regexp.MustCompile(`\bMust(?:Exec|Begin|Connect)\b`)},
		{name: "unbounded SelectContext", pattern: regexp.MustCompile(`\.SelectContext\s*\(`)},
		{name: "map boundary", pattern: regexp.MustCompile(`map\s*\[\s*string\s*\]\s*any`)},
		{name: "non-context database API", pattern: regexp.MustCompile(`\.(?:Exec|Query|Get)\s*\(`)},
		{name: "formatted SQL", pattern: regexp.MustCompile(`fmt\.Sprintf\s*\(`)},
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", filepath.Base(path), err)
		}
		for _, rule := range forbidden {
			if rule.pattern.Match(content) {
				t.Errorf("%s contains prohibited %s", filepath.Base(path), rule.name)
			}
		}
	}
}

func TestSQLXImportRemainsInsideSQLiteAdapter(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join(currentSQLiteDirectory(t), "..", "..", ".."))
	err := filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if value != "github.com/jmoiron/sqlx" {
				continue
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			adapterPrefix := filepath.Join("internal", "persistence", "sqlite") + string(filepath.Separator)
			if !strings.HasPrefix(relative, adapterPrefix) {
				t.Errorf("sqlx import escaped SQLite adapter: %s", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(repository) error = %v", err)
	}
}

func TestProductionHasOneClosedSupervisedRestartExecutor(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join(currentSQLiteDirectory(t), "..", "..", ".."))
	for _, relative := range []string{filepath.Join("internal", "executor")} {
		if _, err := os.Stat(filepath.Join(repositoryRoot, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("prohibited generic executor directory exists: %s", relative)
		}
	}
	approvalPrefix := filepath.Join(repositoryRoot, "internal", "approval") + string(filepath.Separator)
	domainApproval := filepath.Join(repositoryRoot, "internal", "domain", "approval.go")
	applicationPrefix := filepath.Join(repositoryRoot, "internal", "application") + string(filepath.Separator)
	agentPrompt := filepath.Join(repositoryRoot, "internal", "agent", "prompt.go")
	tuiStatus := filepath.Join(repositoryRoot, "internal", "tui", "update.go")
	restartAdapter := filepath.Join(repositoryRoot, "internal", "kube", "restart_deployment.go")
	rolloutAdapter := filepath.Join(repositoryRoot, "internal", "kube", "rollout.go")
	kubePrefix := filepath.Join(repositoryRoot, "internal", "kube") + string(filepath.Separator)
	kubeGateway := filepath.Join(repositoryRoot, "internal", "kube", "gateway.go")
	kubeRuntimeGateway := filepath.Join(repositoryRoot, "internal", "kube", "runtime_gateway.go")
	forbiddenEverywhere := []string{"WriteExecutor", "RestartDeployment("}
	approvalOnly := []string{
		"ApprovalService", "RestartDeploymentExecution", "RestartDeploymentExecutor",
		"RestartDeploymentRevalidator", "RestartDeploymentAcceptance",
		"ExecuteApprovedRestart(", "DeploymentRestarter",
	}
	executeCaller := filepath.Join(repositoryRoot, "internal", "approval", "service.go")
	executeContract := filepath.Join(repositoryRoot, "internal", "approval", "execution.go")
	patchCalls := 0
	executeOccurrences := 0
	err := filepath.WalkDir(filepath.Join(repositoryRoot, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, symbol := range forbiddenEverywhere {
			if bytes.Contains(content, []byte(symbol)) {
				t.Errorf("%s contains prohibited generic write symbol %q", path, symbol)
			}
		}
		if !strings.HasPrefix(path, approvalPrefix) && !strings.HasPrefix(path, applicationPrefix) && filepath.Clean(path) != restartAdapter {
			for _, symbol := range approvalOnly {
				if bytes.Contains(content, []byte(symbol)) {
					t.Errorf("%s exposes approval authority outside the isolated package: %q", path, symbol)
				}
			}
		}
		if bytes.Contains(content, []byte("ApprovalCoordinator")) && !strings.HasPrefix(path, applicationPrefix) {
			t.Errorf("ApprovalCoordinator escaped the Application package: %s", path)
		}
		if bytes.Contains(content, []byte("restart_deployment")) &&
			filepath.Clean(path) != domainApproval && filepath.Clean(path) != agentPrompt &&
			filepath.Clean(path) != tuiStatus &&
			filepath.Clean(path) != restartAdapter && filepath.Clean(path) != rolloutAdapter &&
			!strings.HasPrefix(path, approvalPrefix) && !strings.HasPrefix(path, applicationPrefix) {
			t.Errorf("restart_deployment escaped the isolated domain or approval packages: %s", path)
		}
		patchCount := bytes.Count(content, []byte(".Patch("))
		patchCalls += patchCount
		if patchCount != 0 && (filepath.Clean(path) != restartAdapter || patchCount != 1) {
			t.Errorf("Kubernetes Patch escaped the sole one-call restart adapter: %s", path)
		}
		if strings.HasPrefix(path, kubePrefix) {
			createCount := bytes.Count(content, []byte(".Create("))
			cleaned := filepath.Clean(path)
			if cleaned == kubeGateway || cleaned == kubeRuntimeGateway {
				if createCount != 1 {
					t.Errorf("opaque client creation count changed in %s: %d", path, createCount)
				}
			} else if createCount != 0 {
				t.Errorf("unexpected Create call entered Kubernetes production code: %s", path)
			}
			for _, mutation := range []string{
				".Update(", ".UpdateStatus(", ".Delete(", ".DeleteCollection(",
				".Apply(", ".ApplyStatus(", ".UpdateScale(", ".ApplyScale(", ".Evict(",
			} {
				if bytes.Contains(content, []byte(mutation)) {
					t.Errorf("additional Kubernetes mutation selector %q entered %s", mutation, path)
				}
			}
		}
		executeCount := bytes.Count(content, []byte("ExecuteApprovedRestart("))
		executeOccurrences += executeCount
		if executeCount != 0 {
			cleaned := filepath.Clean(path)
			if executeCount != 1 || (cleaned != executeCaller && cleaned != executeContract && cleaned != restartAdapter) {
				t.Errorf("restart executor occurrence escaped its one contract, service call, or adapter method: %s", path)
			}
		}
		for _, deliveryPrefix := range []string{
			filepath.Join(repositoryRoot, "internal", "agent") + string(filepath.Separator),
			filepath.Join(repositoryRoot, "internal", "tools") + string(filepath.Separator),
			filepath.Join(repositoryRoot, "internal", "tui") + string(filepath.Separator),
		} {
			if !strings.HasPrefix(path, deliveryPrefix) {
				continue
			}
			for _, authority := range approvalOnly {
				if bytes.Contains(content, []byte(authority)) {
					t.Errorf("restart authority %q leaked into Agent, Tool, or TUI code: %s", authority, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(internal) error = %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("production Kubernetes Patch call count = %d, want exactly 1", patchCalls)
	}
	if executeOccurrences != 3 {
		t.Fatalf("production restart executor occurrences = %d, want contract, caller, and adapter only", executeOccurrences)
	}
	commandRoot := filepath.Join(repositoryRoot, "cmd", "kupilot")
	constructorCounts := map[string]int{
		"approval.NewService(":                0,
		"application.NewApprovalCoordinator(": 0,
		"kube.NewDeploymentRestarter(":        0,
		"kube.NewDeploymentRolloutObserver(":  0,
	}
	err = filepath.WalkDir(commandRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for constructor := range constructorCounts {
			constructorCounts[constructor] += bytes.Count(content, []byte(constructor))
		}
		for _, symbol := range []string{
			"RestartDeploymentProposalBridge", "RestartDeploymentExecutor",
			"RestartDeploymentExecution", "ExecuteApprovedRestart(", ".Patch(",
		} {
			if bytes.Contains(content, []byte(symbol)) {
				t.Errorf("composition bypasses the closed approval service with %q in %s", symbol, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(cmd/kupilot) error = %v", err)
	}
	for constructor, count := range constructorCounts {
		if count != 1 {
			t.Errorf("composition constructor %q count = %d, want 1", constructor, count)
		}
	}
}

func TestRetentionAuditSQLMatchesCompleteTypedCatalog(t *testing.T) {
	readTypes := []domain.AuditEventType{
		domain.AuditEventSessionCreated,
		domain.AuditEventSessionDeleted,
		domain.AuditEventSessionExportRequested,
		domain.AuditEventRunStarted,
		domain.AuditEventRunCompleted,
		domain.AuditEventRunFailed,
		domain.AuditEventRunCancelled,
		domain.AuditEventRunTimedOut,
		domain.AuditEventRunStaleScope,
		domain.AuditEventRunInterrupted,
		domain.AuditEventScopeChanged,
		domain.AuditEventToolRequested,
		domain.AuditEventToolCompleted,
		domain.AuditEventToolDenied,
		domain.AuditEventModelRequested,
		domain.AuditEventModelCompleted,
		domain.AuditEventConsentGranted,
		domain.AuditEventConsentRevoked,
		domain.AuditEventPolicyDenied,
		domain.AuditEventPersistenceDegraded,
	}
	writeTypes := []domain.AuditEventType{
		domain.AuditEventApprovalRequested,
		domain.AuditEventApprovalApproved,
		domain.AuditEventApprovalRejected,
		domain.AuditEventApprovalExpired,
		domain.AuditEventApprovalCancelled,
		domain.AuditEventWriteIntent,
		domain.AuditEventWriteAttempted,
		domain.AuditEventWriteOutcomeUnknown,
		domain.AuditEventWriteVerified,
		domain.AuditEventWriteVerificationFailed,
	}
	assertRetentionAuditCatalog(t, deleteExpiredReadAuditEventsSQL, readTypes, domain.AuditRetentionRead)
	assertRetentionAuditCatalog(t, deleteExpiredWriteAuditEventsSQL, writeTypes, domain.AuditRetentionWrite)
}

func assertRetentionAuditCatalog(t *testing.T, query string, eventTypes []domain.AuditEventType, class domain.AuditRetentionClass) {
	t.Helper()
	literals := regexp.MustCompile(`'[a-z_]+'`).FindAllString(query, -1)
	if len(literals) != len(eventTypes) {
		t.Fatalf("retention query contains %d event literals, want %d", len(literals), len(eventTypes))
	}
	for _, eventType := range eventTypes {
		literal := "'" + string(eventType) + "'"
		if strings.Count(query, literal) != 1 {
			t.Errorf("retention query count for %s = %d, want 1", eventType, strings.Count(query, literal))
		}
		if eventType.RetentionClass() != class {
			t.Errorf("RetentionClass(%s) = %s, want %s", eventType, eventType.RetentionClass(), class)
		}
	}
}

func currentSQLiteDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Abs(current directory) error = %v", err)
	}
	return path
}
