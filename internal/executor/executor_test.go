package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestAdapterExecutesExactArgvWithMinimalEnvironmentAndSanitizedOrderedOutput(t *testing.T) {
	executable := resolvedTestExecutable(t)
	workingDirectory := resolvedTempDirectory(t)
	marker := filepath.Join(workingDirectory, "shell-injection-marker")
	t.Setenv("OPENAI_API_KEY", "parent-environment-canary")
	t.Setenv("HTTPS_PROXY", "http://parent-proxy.invalid")
	arguments := []string{
		"-test.run=^TestExecutorHelperProcess$", "--", "args-env", marker,
		"literal;touch " + marker, "$(touch " + marker + ")",
	}
	envelope, adapter := localTestEnvelope(t, executable, workingDirectory, arguments, 4096, 20, 5*time.Second)
	result, err := adapter.Execute(context.Background(), envelope)
	if err != nil || result.Validate(envelope.Intent.Limits) != nil || result.State != domain.LocalProcessExited ||
		result.ExitCode != 0 || result.Truncated || result.RedactionCount != 0 {
		t.Fatalf("Execute() = %#v/%v", result, err)
	}
	for _, want := range []string{"[stdout]", "literal;touch", "$(touch", "LC_ALL=C", "NO_COLOR=1", "[stderr] helper-stderr"} {
		if !strings.Contains(result.SafeOutput, want) {
			t.Errorf("safe output omitted %q: %q", want, result.SafeOutput)
		}
	}
	for _, denied := range []string{"parent-environment-canary", "parent-proxy.invalid", "OPENAI_API_KEY", "HTTPS_PROXY"} {
		if strings.Contains(result.SafeOutput, denied) {
			t.Errorf("safe output exposed inherited environment %q", denied)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metacharacter argv was interpreted by a shell: %v", err)
	}
}

func TestAdapterRejectsExecutableReplacementAndSymlinkWorkingDirectoryBeforeStart(t *testing.T) {
	adapter, err := NewAdapter(security.NewRedactor(), testNow)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	workingDirectory := resolvedTempDirectory(t)
	original := resolvedTestExecutable(t)
	copyPath := filepath.Join(workingDirectory, "synthetic-command")
	copyExecutable(t, original, copyPath)
	observation, err := adapter.Inspect(context.Background(), copyPath, workingDirectory)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	envelope := localEnvelopeWithObservation(t, copyPath, workingDirectory,
		[]string{"-test.run=^TestExecutorHelperProcess$", "--", "success"}, observation, 1024, 10, 5*time.Second)
	file, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("OpenFile(replacement) error = %v", err)
	}
	if _, err := file.Write([]byte{0}); err != nil {
		t.Fatalf("Write(replacement) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(replacement) error = %v", err)
	}
	result, executeErr := adapter.Execute(context.Background(), envelope)
	if executeErr == nil || result.State != domain.LocalProcessNotAttempted || result.Started || classOf(executeErr) != domain.SafeErrorClassConflict {
		t.Fatalf("replacement Execute() = %#v/%v", result, executeErr)
	}

	realDirectory := filepath.Join(workingDirectory, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	symlinkDirectory := filepath.Join(workingDirectory, "linked")
	if err := os.Symlink(realDirectory, symlinkDirectory); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := adapter.Inspect(context.Background(), original, symlinkDirectory); err == nil || classOf(err) != domain.SafeErrorClassPolicyDenied {
		t.Fatalf("Inspect(symlink cwd) error = %v", err)
	}
}

func TestAdapterRejectsScriptExecutableBeforeStart(t *testing.T) {
	adapter, err := NewAdapter(security.NewRedactor(), testNow)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	workingDirectory := resolvedTempDirectory(t)
	script := filepath.Join(workingDirectory, "synthetic-script")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}
	if _, err := adapter.Inspect(context.Background(), script, workingDirectory); err == nil ||
		classOf(err) != domain.SafeErrorClassPolicyDenied {
		t.Fatalf("Inspect(script) error = %v", err)
	}
}

func TestAdapterBoundsCombinedStdoutStderrAndBlocksSensitiveOutput(t *testing.T) {
	executable := resolvedTestExecutable(t)
	workingDirectory := resolvedTempDirectory(t)

	overflowEnvelope, adapter := localTestEnvelope(t, executable, workingDirectory,
		[]string{"-test.run=^TestExecutorHelperProcess$", "--", "overflow"}, 96, 3, 5*time.Second)
	result, err := adapter.Execute(context.Background(), overflowEnvelope)
	if err != nil || result.State != domain.LocalProcessExited || !result.Truncated ||
		result.ByteCount > overflowEnvelope.Intent.Limits.MaximumOutput || result.LineCount > overflowEnvelope.Intent.Limits.MaximumLines {
		t.Fatalf("overflow Execute() = %#v/%v", result, err)
	}

	sensitiveEnvelope, adapter := localTestEnvelope(t, executable, workingDirectory,
		[]string{"-test.run=^TestExecutorHelperProcess$", "--", "sensitive"}, 4096, 20, 5*time.Second)
	result, err = adapter.Execute(context.Background(), sensitiveEnvelope)
	if err == nil || classOf(err) != domain.SafeErrorClassSensitiveOutputBlocked || result.SafeOutput != "" ||
		result.State != domain.LocalProcessOutputBlocked || result.ErrorClass != domain.SafeErrorClassSensitiveOutputBlocked ||
		result.OutputDigest != domain.LocalSafeOutputDigest("") {
		t.Fatalf("sensitive Execute() = %#v/%v", result, err)
	}
}

func TestAdapterCancellationKillsProcessGroupAndBoundsJoin(t *testing.T) {
	executable := resolvedTestExecutable(t)
	workingDirectory := resolvedTempDirectory(t)
	marker := filepath.Join(workingDirectory, "ready")
	envelope, adapter := localTestEnvelope(t, executable, workingDirectory,
		[]string{"-test.run=^TestExecutorHelperProcess$", "--", "spawn-grandchild", marker}, 4096, 20, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type executionResult struct {
		result domain.LocalCommandResult
		err    error
	}
	finished := make(chan executionResult, 1)
	go func() {
		result, err := adapter.Execute(ctx, envelope)
		finished <- executionResult{result: result, err: err}
	}()
	waitForMarker(t, marker)
	cancel()
	select {
	case execution := <-finished:
		if execution.err == nil || classOf(execution.err) != domain.SafeErrorClassCancelled ||
			execution.result.State != domain.LocalProcessUnknown || !execution.result.Started {
			t.Fatalf("cancelled Execute() = %#v/%v", execution.result, execution.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled process group did not drain and join within its bound")
	}
}

func TestAdapterTimeoutIsUnknownAndNeverRetries(t *testing.T) {
	executable := resolvedTestExecutable(t)
	workingDirectory := resolvedTempDirectory(t)
	marker := filepath.Join(workingDirectory, "ready")
	envelope, adapter := localTestEnvelope(t, executable, workingDirectory,
		[]string{"-test.run=^TestExecutorHelperProcess$", "--", "block", marker}, 4096, 20, 150*time.Millisecond)
	result, err := adapter.Execute(context.Background(), envelope)
	if err == nil || classOf(err) != domain.SafeErrorClassTimeout || result.State != domain.LocalProcessUnknown || !result.Started {
		t.Fatalf("timed out Execute() = %#v/%v", result, err)
	}
}

func TestOrderedCollectorSerializesStreamsAtCombinedLimits(t *testing.T) {
	t.Parallel()
	collector := newOrderedCollector(40, 3)
	stdout := streamWriter{collector: collector, stream: "stdout"}
	stderr := streamWriter{collector: collector, stream: "stderr"}
	_, _ = stdout.Write([]byte("one\n"))
	_, _ = stderr.Write([]byte("two\n"))
	_, _ = stdout.Write([]byte("three\nfour"))
	value, truncated := collector.result()
	if !truncated || !strings.HasPrefix(string(value), "[stdout] one\n[stderr] two") || len(value) > 40 {
		t.Fatalf("ordered collector = %q truncated=%t", value, truncated)
	}
}

// TestExecutorHelperProcess is invoked as the current test binary so the
// adapter tests never depend on a host-installed command.
func TestExecutorHelperProcess(t *testing.T) {
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	arguments := os.Args[separator+1:]
	switch arguments[0] {
	case "success":
		_, _ = fmt.Fprintln(os.Stdout, "helper-success")
	case "args-env":
		_, _ = fmt.Fprintf(os.Stdout, "args=%q\nenv=%q\n", arguments[2:], os.Environ())
		_, _ = fmt.Fprintln(os.Stderr, "helper-stderr")
	case "overflow":
		for index := 0; index < 100; index++ {
			_, _ = fmt.Fprintf(os.Stdout, "stdout-%03d-xxxxxxxx\n", index)
			_, _ = fmt.Fprintf(os.Stderr, "stderr-%03d-yyyyyyyy\n", index)
		}
	case "sensitive":
		_, _ = fmt.Fprintln(os.Stdout, "-----BEGIN PRIVATE KEY-----")
	case "block":
		writeReadyMarker(arguments[1])
		time.Sleep(time.Hour)
	case "spawn-grandchild":
		grandchild := exec.Command(os.Args[0], "-test.run=^TestExecutorHelperProcess$", "--", "block", arguments[1]+"-grandchild")
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		if err := grandchild.Start(); err != nil {
			t.Fatalf("grandchild Start() error = %v", err)
		}
		writeReadyMarker(arguments[1])
		_ = grandchild.Wait()
	default:
		t.Fatalf("unknown helper mode %q", arguments[0])
	}
}

func localTestEnvelope(t *testing.T, executable, directory string, arguments []string, maxBytes, maxLines int, timeout time.Duration) (domain.ActionEnvelope, *Adapter) {
	t.Helper()
	adapter, err := NewAdapter(security.NewRedactor(), testNow)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	observation, err := adapter.Inspect(context.Background(), executable, directory)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	return localEnvelopeWithObservation(t, executable, directory, arguments, observation, maxBytes, maxLines, timeout), adapter
}

func localEnvelopeWithObservation(t *testing.T, executable, directory string, values []string, observation domain.LocalExecutionObservation, maxBytes, maxLines int, timeout time.Duration) domain.ActionEnvelope {
	t.Helper()
	arguments, err := domain.NewActionArguments(values)
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	operation, ok := domain.ClassifyLocalCommand(domain.LocalCommandDiagnostic, arguments)
	if !ok {
		t.Fatalf("ClassifyLocalCommand() denied synthetic fixture")
	}
	environment, err := domain.NewActionEnvironment([]string{"LC_ALL=C", "NO_COLOR=1"})
	if err != nil {
		t.Fatalf("NewActionEnvironment() error = %v", err)
	}
	policy := domain.LocalCommandPolicy{
		ID: "synthetic-test-command", Kind: domain.LocalCommandDiagnostic, Operation: operation,
		Executable: executable, Arguments: arguments, WorkingDirectory: directory, Environment: environment,
		CredentialReference: domain.LocalCredentialNone, DiagnosticEffect: domain.LocalDiagnosticNoNetworkRead,
		Timeout: timeout, MaxLines: maxLines, MaxBytes: maxBytes,
	}
	scope := domain.ClusterScope{
		Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent,
		Generation: 7, ActivatedAt: testNow(),
	}
	plan := domain.LocalCommandActionPlan{
		RunID: "00000000-0000-7000-8000-000000000301", SessionID: "00000000-0000-7000-8000-000000000302",
		Scope: scope, PolicyGeneration: 4,
		NamespaceTarget: domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: scope.Namespace, UID: "namespace-uid", ResourceVersion: "42"},
		Policy:          policy, Arguments: arguments, Observation: observation,
		Limits:  domain.ActionLimits{Timeout: timeout, MaximumItems: 1, MaximumLines: maxLines, MaximumBytes: maxBytes, MaximumOutput: maxBytes},
		Purpose: "Run one synthetic local command fixture.",
	}
	intent, err := plan.Intent(domain.PermissionProfileAsk)
	if err != nil {
		t.Fatalf("Intent() error = %v", err)
	}
	envelope, err := domain.NewActionEnvelope(
		"00000000-0000-7000-8000-000000000303", plan.SessionID, plan.RunID, intent, testNow(),
	)
	if err != nil {
		t.Fatalf("NewActionEnvelope() error = %v", err)
	}
	return envelope
}

func resolvedTestExecutable(t *testing.T) string {
	t.Helper()
	name, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		t.Fatalf("EvalSymlinks(executable) error = %v", err)
	}
	return resolved
}

func resolvedTempDirectory(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(temp directory) error = %v", err)
	}
	return resolved
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatalf("Open(source) error = %v", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatalf("OpenFile(destination) error = %v", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatalf("Copy() error = %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("Close(destination) error = %v", err)
	}
}

func waitForMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not reach the cancellation barrier")
		}
		runtime.Gosched()
	}
}

func writeReadyMarker(name string) {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_ = file.Close()
	}
}

func testNow() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() }

func TestAdapterImplementsProjectOwnedShape(t *testing.T) {
	t.Parallel()
	adapter, err := NewAdapter(security.NewRedactor(), testNow)
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	type port interface {
		Inspect(context.Context, string, string) (domain.LocalExecutionObservation, error)
		Execute(context.Context, domain.ActionEnvelope) (domain.LocalCommandResult, error)
	}
	var implementation port = adapter
	if reflect.ValueOf(implementation).IsNil() {
		t.Fatal("adapter is nil")
	}
}
