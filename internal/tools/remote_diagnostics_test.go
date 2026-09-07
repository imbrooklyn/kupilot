package tools

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestPodExecUsesExactArgvActionAndSanitizesOrderedStreams(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	resolver := &recordingRemoteResolver{pod: remoteTestPod()}
	canary := strings.Repeat("remote-output-canary", 3)
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		if request.Pod != remoteTestPod().Reference || request.Container != "app" || request.Executable != "/usr/bin/printf" ||
			!equalStringSlices(request.Arguments.Values(), []string{"literal;not-a-shell", "$(ignored)", "*.log"}) || request.Timeout != 5*time.Second || request.MaxLines != 20 || request.MaxBytes != 16384 {
			t.Fatalf("ExecuteRemoteCommand() request = %#v", request)
		}
		stdout := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte("ready\n"))
		stderr := mustRemoteChunk(t, 2, RemoteOutputStderr, []byte("\x1b[31mtoken="+canary+"\x1b[0m\n"))
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{stdout, stderr}, Completed: true, LineCount: 2, ByteCount: len(stdout.content) + len(stderr.content)}, nil
	}}
	gate := &recordingRemoteActionGate{}
	policy := allowRemoteOutputPolicy()
	tool, err := NewPodExecTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, policy))
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 1 ||
		result.Evidence[0].Category != domain.EvidenceCategoryRemoteCommand || result.Evidence[0].PolicyVersion != domain.RemoteDiagnosticsPolicyVersion ||
		result.Evidence[0].SourcePath == nil || *result.Evidence[0].SourcePath != "api/v1/namespaces/team-a/pods/sample-pod/exec" ||
		strings.Contains(fmt.Sprintf("%#v", result), canary) || strings.ContainsRune(result.DataJSON, '\x1b') || !strings.Contains(result.DataJSON, "[REDACTED]") {
		t.Fatalf("Pod Exec result = %#v, validation=%v", result, result.Validate())
	}
	if resolver.podCount() != 1 || command.count() != 1 || gate.authorizeCount() != 1 || gate.outcomeCount() != 1 || policy.count() != 5 {
		t.Fatalf("call counts resolver=%d command=%d authorize=%d outcome=%d policy=%d", resolver.podCount(), command.count(), gate.authorizeCount(), gate.outcomeCount(), policy.count())
	}
	plan := gate.lastPlan()
	if plan.Operation != domain.ActionOperationPodExec || plan.Risk != domain.RiskCritical || plan.Target.Resource.UID != "pod-uid" ||
		plan.Parameters.Kind != domain.ActionParametersRemoteArgv || plan.Parameters.Executable != "/usr/bin/printf" || plan.Parameters.Container != "app" {
		t.Fatalf("Pod Exec action plan = %#v", plan)
	}
	if _, err := json.Marshal(mustRemoteChunk(t, 1, RemoteOutputStdout, []byte(canary))); !errors.Is(err, ErrRawRemoteOutputSerializationDenied) {
		t.Fatalf("json.Marshal(raw remote output) error = %v", err)
	}
}

func TestPodExecAcceptsSuccessfulEmptyOutput(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Completed: true}, nil
	}}
	gate := &recordingRemoteActionGate{}
	tool, err := NewPodExecTool(remoteToolDependencies(&recordingRemoteResolver{pod: remoteTestPod()}, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || command.count() != 1 || gate.outcomeCount() != 1 ||
		!strings.Contains(result.DataJSON, `"content":""`) || !strings.Contains(result.DataJSON, `"byte_count":0`) || len(result.Evidence) != 1 {
		t.Fatalf("empty Pod Exec result = %#v, validation=%v", result, result.Validate())
	}
	if outcome := gate.lastOutcome(); outcome.State != domain.RemoteDiagnosticOutcomeSucceeded || outcome.OutputBytes != 0 || outcome.OutputLines != 0 {
		t.Fatalf("empty Pod Exec outcome = %#v", outcome)
	}
}

func TestPodExecTrimsSanitizedProjectionToCompleteToolResultBudget(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	content := strings.Repeat("<", 16000)
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte(content))
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(content)}, nil
	}}
	gate := &recordingRemoteActionGate{}
	tool, err := NewPodExecTool(remoteToolDependencies(&recordingRemoteResolver{pod: remoteTestPod()}, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	var data safeRemoteData
	if result.Validate() != nil || result.Status != domain.ToolResultStatusPartial || !result.Truncation.Truncated ||
		json.Unmarshal([]byte(result.DataJSON), &data) != nil || !data.Truncated || data.ByteCount >= len(content) ||
		gate.outcomeCount() != 1 || !gate.lastOutcome().Truncated {
		t.Fatalf("fitted Pod Exec result/outcome = %#v/%#v validation=%v", result, gate.lastOutcome(), result.Validate())
	}
}

func TestRemoteDiagnosticsDenialsPerformZeroExecution(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	for _, test := range []struct {
		name          string
		policy        *sequenceRemoteOutputPolicy
		gateError     error
		wantResolvers int
		wantAudits    int
	}{
		{name: "missing consent", policy: &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyConsentRequired}}},
		{name: "human denial", policy: &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyAllowed}}, gateError: &fakeClassifiedError{class: domain.SafeErrorClassPolicyDenied}, wantResolvers: 1},
		{name: "reviewer denial", policy: &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyAllowed}}, gateError: &fakeClassifiedError{class: domain.SafeErrorClassPolicyDenied}, wantResolvers: 1},
		{name: "consent revoked before attempt", policy: &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyAllowed, LogPolicyConsentRequired}}, wantResolvers: 1, wantAudits: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &recordingRemoteResolver{pod: remoteTestPod()}
			command := &recordingRemoteCommand{}
			gate := &recordingRemoteActionGate{authorizeErr: test.gateError}
			tool, err := NewPodExecTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, test.policy))
			if err != nil {
				t.Fatalf("NewPodExecTool() error = %v", err)
			}
			result := tool.Execute(context.Background(), call)
			if !remoteToolFailed(result) || command.count() != 0 || resolver.podCount() != test.wantResolvers || gate.outcomeCount() != test.wantAudits || len(result.Evidence) != 0 {
				t.Fatalf("denied result=%#v resolver=%d command=%d audits=%d", result, resolver.podCount(), command.count(), gate.outcomeCount())
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := &recordingRemoteResolver{pod: remoteTestPod()}
	command := &recordingRemoteCommand{}
	gate := &recordingRemoteActionGate{}
	tool, _ := NewPodExecTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyAllowed}}))
	result := tool.Execute(cancelled, call)
	if !remoteToolFailed(result) || resolver.podCount() != 0 || command.count() != 0 || gate.authorizeCount() != 0 {
		t.Fatalf("cancelled result/calls = %#v/%d/%d/%d", result, resolver.podCount(), command.count(), gate.authorizeCount())
	}
}

func TestPodExecPostflightConsentChangeDiscardsOutput(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	resolver := &recordingRemoteResolver{pod: remoteTestPod()}
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte("must-not-reach-model"))
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(chunk.content)}, nil
	}}
	gate := &recordingRemoteActionGate{}
	policy := &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{LogPolicyAllowed, LogPolicyAllowed, LogPolicyConsentRequired}}
	tool, _ := NewPodExecTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, policy))
	result := tool.Execute(context.Background(), call)
	if !remoteToolFailed(result) || command.count() != 1 || len(result.Evidence) != 0 || strings.Contains(result.DataJSON, "must-not-reach-model") || gate.outcomeCount() != 1 {
		t.Fatalf("postflight consent result = %#v, command=%d outcomes=%d", result, command.count(), gate.outcomeCount())
	}
	if outcome := gate.lastOutcome(); outcome.State != domain.RemoteDiagnosticOutcomeFailed || outcome.ErrorClass != domain.SafeErrorClassConsentRequired {
		t.Fatalf("postflight outcome = %#v", outcome)
	}
}

func TestPodExecFinalAcceptanceConsentChangeDiscardsPreparedOutput(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte("final-consent-canary"))
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(chunk.content)}, nil
	}}
	gate := &recordingRemoteActionGate{}
	policy := &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{
		LogPolicyAllowed, LogPolicyAllowed, LogPolicyAllowed, LogPolicyConsentRequired,
	}}
	tool, _ := NewPodExecTool(remoteToolDependencies(&recordingRemoteResolver{pod: remoteTestPod()}, command, &recordingDiagnosticPod{}, gate, policy))
	result := tool.Execute(context.Background(), call)
	if !remoteToolFailed(result) || command.count() != 1 || len(result.Evidence) != 0 ||
		strings.Contains(result.DataJSON, "final-consent-canary") || gate.outcomeCount() != 1 || policy.count() != 4 {
		t.Fatalf("final acceptance result = %#v, command=%d outcomes=%d policy=%d", result, command.count(), gate.outcomeCount(), policy.count())
	}
	if outcome := gate.lastOutcome(); outcome.State != domain.RemoteDiagnosticOutcomeFailed || outcome.ErrorClass != domain.SafeErrorClassConsentRequired {
		t.Fatalf("final acceptance outcome = %#v", outcome)
	}
}

func TestRemoteDiagnosticsStaleAfterAuthorityPerformsZeroExecutionAndRecordsOutcome(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	resolver := &recordingRemoteResolver{pod: remoteTestPod()}
	command := &recordingRemoteCommand{}
	guard := &mutableScopeGuard{current: true}
	gate := &recordingRemoteActionGate{afterAuthorize: func() { guard.set(false) }}
	dependencies := remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy())
	dependencies.ScopeGuard = guard
	tool, err := NewPodExecTool(dependencies)
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if !remoteToolFailed(result) || command.count() != 0 || gate.authorizeCount() != 1 || gate.outcomeCount() != 1 {
		t.Fatalf("stale result/calls = %#v command=%d authorize=%d outcome=%d", result, command.count(), gate.authorizeCount(), gate.outcomeCount())
	}
	outcome := gate.lastOutcome()
	if outcome.State != domain.RemoteDiagnosticOutcomeFailed || outcome.ErrorClass != domain.SafeErrorClassStaleScope || outcome.OutputBytes != 0 {
		t.Fatalf("stale outcome = %#v", outcome)
	}
}

func TestRemoteDiagnosticsExpiredConsumedAuthorityPerformsZeroExecution(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	command := &recordingRemoteCommand{}
	gate := &recordingRemoteActionGate{}
	dependencies := remoteToolDependencies(&recordingRemoteResolver{pod: remoteTestPod()}, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy())
	dependencies.Now = func() time.Time { return testObservedAt.Add(time.Minute) }
	tool, err := NewPodExecTool(dependencies)
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if !remoteToolFailed(result) || result.Error.Class != domain.SafeErrorClassTimeout || command.count() != 0 || gate.authorizeCount() != 1 || gate.outcomeCount() != 1 {
		t.Fatalf("expired authority result/calls = %#v command=%d authorize=%d outcome=%d", result, command.count(), gate.authorizeCount(), gate.outcomeCount())
	}
	if outcome := gate.lastOutcome(); outcome.State != domain.RemoteDiagnosticOutcomeFailed || outcome.ErrorClass != domain.SafeErrorClassTimeout {
		t.Fatalf("expired authority outcome = %#v", outcome)
	}
}

func TestPodExecPostAttemptStaleScopeDiscardsRawOutput(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	guard := &mutableScopeGuard{current: true}
	command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
		chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte("stale-output-canary"))
		guard.set(false)
		return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(chunk.content)}, nil
	}}
	gate := &recordingRemoteActionGate{}
	dependencies := remoteToolDependencies(&recordingRemoteResolver{pod: remoteTestPod()}, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy())
	dependencies.ScopeGuard = guard
	tool, err := NewPodExecTool(dependencies)
	if err != nil {
		t.Fatalf("NewPodExecTool() error = %v", err)
	}
	result := tool.Execute(context.Background(), call)
	if !remoteToolFailed(result) || command.count() != 1 || len(result.Evidence) != 0 || strings.Contains(result.DataJSON, "stale-output-canary") || gate.outcomeCount() != 1 {
		t.Fatalf("post-attempt stale result/calls = %#v/%d/%d", result, command.count(), gate.outcomeCount())
	}
	outcome := gate.lastOutcome()
	if outcome.State != domain.RemoteDiagnosticOutcomeUnknown || outcome.ErrorClass != domain.SafeErrorClassStaleScope || outcome.OutputBytes != 0 || outcome.OutputLines != 0 {
		t.Fatalf("post-attempt stale outcome = %#v", outcome)
	}
}

func TestRemoteDiagnosticLimitsIntersectRunAndCapabilityCeilings(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNamePodExec, `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"team-a","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`)
	timeout, lines, bytes := boundedRemoteLimits(call, time.Minute, domain.MaxRemoteDiagnosticLines, domain.MaxRemoteDiagnosticBytes)
	ceilings := call.Ceilings()
	wantTimeout := min(ceilings.RequestTimeout, time.Minute)
	wantBytes := min(ceilings.MaxLogBytes, ceilings.MaxResultBytes)
	if timeout != wantTimeout || lines != ceilings.MaxLogLines || bytes != wantBytes {
		t.Fatalf("bounded limits = %s/%d/%d, want %s/%d/%d", timeout, lines, bytes, wantTimeout, ceilings.MaxLogLines, wantBytes)
	}
}

func TestContainerFileReadBindsPathRejectsSensitiveMountAndSymlink(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNameReadContainerFile, `{"container":"app","namespace":"team-a","path":"/var/app/data/report.txt","pod_name":"sample-pod","purpose":"Read one reviewed file."}`)

	t.Run("regular file", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := regularFileArchive(t, []byte("status=ready\n"))
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			want := []string{"--format=ustar", "--blocking-factor=1", "--no-recursion", "--create", "--file=-", "--directory=/", "--", "var", "var/app", "var/app/data", "var/app/data/report.txt"}
			if request.Executable != "/bin/tar" || !equalStringSlices(request.Arguments.Values(), want) {
				t.Fatalf("file reader request = %#v", request)
			}
			chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(archive)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || !strings.Contains(result.DataJSON, "status=ready") || len(result.Evidence) != 1 ||
			result.Evidence[0].SourcePath == nil || *result.Evidence[0].SourcePath != "/var/app/data/report.txt" || strings.Contains(result.Evidence[0].Fact, "status=ready") {
			t.Fatalf("regular file result = %#v, validation=%v", result, result.Validate())
		}
		plan := gate.lastPlan()
		if plan.Parameters.Kind != domain.ActionParametersContainerFile || plan.Parameters.NormalizedPath != "/var/app/data/report.txt" || plan.Parameters.Container != "app" || plan.Parameters.Executable != "/bin/tar" ||
			!equalStringSlices(plan.Parameters.Arguments.Values(), []string{"--format=ustar", "--blocking-factor=1", "--no-recursion", "--create", "--file=-", "--directory=/", "--", "var", "var/app", "var/app/data", "var/app/data/report.txt"}) ||
			plan.NetworkDestinationHash != domain.RemotePodNetworkDestinationHash(plan.Target.Resource, "app") {
			t.Fatalf("container file action plan = %#v", plan)
		}
		if plan.Limits.MaximumBytes != 16384 || plan.Limits.MaximumOutput != 13312 {
			t.Fatalf("container file transport/content limits = %#v", plan.Limits)
		}
	})

	t.Run("empty regular file", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := regularFileArchive(t, nil)
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(archive)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || command.count() != 1 || gate.outcomeCount() != 1 ||
			!strings.Contains(result.DataJSON, `"content":""`) || !strings.Contains(result.DataJSON, `"byte_count":0`) || len(result.Evidence) != 1 {
			t.Fatalf("empty regular file result = %#v, validation=%v", result, result.Validate())
		}
		if outcome := gate.lastOutcome(); outcome.State != domain.RemoteDiagnosticOutcomeSucceeded || outcome.OutputBytes != 0 || outcome.OutputLines != 0 {
			t.Fatalf("empty regular file outcome = %#v", outcome)
		}
	})

	t.Run("sensitive mount zero exec", func(t *testing.T) {
		pod := remoteTestPod()
		pod.DeniedMountRoots = []string{"/var/app/data"}
		resolver := &recordingRemoteResolver{pod: pod}
		command := &recordingRemoteCommand{}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if !remoteToolFailed(result) || command.count() != 0 || gate.authorizeCount() != 0 {
			t.Fatalf("sensitive mount result/calls = %#v/%d/%d", result, command.count(), gate.authorizeCount())
		}
	})

	t.Run("symlink archive is never Evidence", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := symlinkFileArchive(t)
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(archive)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if !remoteToolFailed(result) || len(result.Evidence) != 0 || gate.lastOutcome().ErrorClass != domain.SafeErrorClassSensitiveOutputBlocked {
			t.Fatalf("symlink result/outcome = %#v/%#v", result, gate.lastOutcome())
		}
	})

	t.Run("file content before parent proof is never Evidence", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := outOfOrderFileArchive(t, []byte("must-not-be-accepted"))
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(archive)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if !remoteToolFailed(result) || len(result.Evidence) != 0 || strings.Contains(result.DataJSON, "must-not-be-accepted") {
			t.Fatalf("out-of-order archive result = %#v", result)
		}
	})

	t.Run("trailing archive data is never Evidence", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := append(regularFileArchive(t, []byte("status=ready\n")), make([]byte, remoteArchiveBlockBytes)...)
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{chunk}, Completed: true, LineCount: 1, ByteCount: len(archive)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if !remoteToolFailed(result) || len(result.Evidence) != 0 || gate.lastOutcome().ErrorClass != domain.SafeErrorClassSensitiveOutputBlocked {
			t.Fatalf("trailing archive result/outcome = %#v/%#v", result, gate.lastOutcome())
		}
	})

	t.Run("reader stderr fails closed", func(t *testing.T) {
		resolver := &recordingRemoteResolver{pod: remoteTestPod()}
		archive := regularFileArchive(t, []byte("must-not-be-accepted"))
		command := &recordingRemoteCommand{run: func(request RemoteCommandRequest) (RemoteCommandObservation, error) {
			stdout := mustRemoteChunk(t, 1, RemoteOutputStdout, archive)
			stderr := mustRemoteChunk(t, 2, RemoteOutputStderr, []byte("reader warning"))
			return RemoteCommandObservation{Pod: request.Pod, Container: request.Container, Chunks: []RemoteOutputChunk{stdout, stderr}, Completed: true, LineCount: 1, ByteCount: len(archive) + len(stderr.content)}, nil
		}}
		gate := &recordingRemoteActionGate{}
		tool, _ := NewReadContainerFileTool(remoteToolDependencies(resolver, command, &recordingDiagnosticPod{}, gate, allowRemoteOutputPolicy()))
		result := tool.Execute(context.Background(), call)
		if !remoteToolFailed(result) || len(result.Evidence) != 0 || gate.lastOutcome().ErrorClass != domain.SafeErrorClassInvalidExternalResponse {
			t.Fatalf("stderr archive result/outcome = %#v/%#v", result, gate.lastOutcome())
		}
	})
}

func TestDiagnosticPodBindsPinnedImageServiceTargetAndCleanupOutcome(t *testing.T) {
	input := remoteToolRunInput(t)
	call := bindRemoteToolCall(t, input, domain.ToolNameRunDiagnosticPod, `{"diagnostic_id":"tcp-connect","purpose":"Test one configured Service port."}`)
	resolver := &recordingRemoteResolver{service: remoteTestService()}
	runner := &recordingDiagnosticPod{run: func(request DiagnosticPodRequest) (DiagnosticPodObservation, error) {
		if request.Service != remoteTestService().Reference || request.Image != "registry.example/diag@sha256:"+strings.Repeat("a", 64) || request.TargetHost != "api.team-a.svc" || request.TargetPort != 8443 ||
			request.Executable != "/bin/nc" || !equalStringSlices(request.Arguments.Values(), []string{"-z", "-v", "-w", "5", "api.team-a.svc", "8443"}) {
			t.Fatalf("RunDiagnosticPod() request = %#v", request)
		}
		chunk := mustRemoteChunk(t, 1, RemoteOutputStdout, []byte("connected\n"))
		return DiagnosticPodObservation{Pod: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: request.Name, UID: "diag-uid", ResourceVersion: "30"}, Chunks: []RemoteOutputChunk{chunk}, ExitCode: 0, Completed: true, Created: true, LineCount: 1, ByteCount: len(chunk.content), Lifecycle: completedDiagnosticPodLifecycle(), CleanupState: domain.DiagnosticPodCleanupVerified}, nil
	}}
	gate := &recordingRemoteActionGate{}
	tool, _ := NewRunDiagnosticPodTool(remoteToolDependencies(resolver, &recordingRemoteCommand{}, runner, gate, allowRemoteOutputPolicy()))
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || runner.count() != 1 || gate.lastPlan().Risk != domain.RiskCritical || gate.lastOutcome().CleanupState != domain.DiagnosticPodCleanupVerified ||
		len(result.Evidence) != 1 || result.Evidence[0].SourcePath == nil || *result.Evidence[0].SourcePath != "api/v1/namespaces/team-a/services/api#diagnostic_pod" ||
		strings.Contains(result.Evidence[0].Fact, "connected") || gate.lastPlan().NetworkDestinationHash != domain.DiagnosticPodNetworkDestinationHash(remoteTestService().Reference, "api.team-a.svc", 8443) {
		t.Fatalf("diagnostic Pod result/plan/outcome = %#v/%#v/%#v", result, gate.lastPlan(), gate.lastOutcome())
	}

	deniedRunner := &recordingDiagnosticPod{}
	deniedGate := &recordingRemoteActionGate{authorizeErr: &fakeClassifiedError{class: domain.SafeErrorClassPolicyDenied}}
	deniedTool, _ := NewRunDiagnosticPodTool(remoteToolDependencies(resolver, &recordingRemoteCommand{}, deniedRunner, deniedGate, allowRemoteOutputPolicy()))
	denied := deniedTool.Execute(context.Background(), call)
	if !remoteToolFailed(denied) || deniedRunner.count() != 0 {
		t.Fatalf("denied diagnostic Pod result/calls = %#v/%d", denied, deniedRunner.count())
	}

	t.Run("successful empty log", func(t *testing.T) {
		emptyRunner := &recordingDiagnosticPod{run: func(request DiagnosticPodRequest) (DiagnosticPodObservation, error) {
			return DiagnosticPodObservation{Pod: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: request.Name, UID: "diag-empty-uid", ResourceVersion: "31"}, ExitCode: 0, Completed: true, Created: true, Lifecycle: completedDiagnosticPodLifecycle(), CleanupState: domain.DiagnosticPodCleanupVerified}, nil
		}}
		emptyGate := &recordingRemoteActionGate{}
		emptyTool, _ := NewRunDiagnosticPodTool(remoteToolDependencies(resolver, &recordingRemoteCommand{}, emptyRunner, emptyGate, allowRemoteOutputPolicy()))
		result := emptyTool.Execute(context.Background(), call)
		if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || emptyRunner.count() != 1 || emptyGate.outcomeCount() != 1 ||
			!strings.Contains(result.DataJSON, `"content":""`) || !strings.Contains(result.DataJSON, `"byte_count":0`) || len(result.Evidence) != 1 {
			t.Fatalf("empty diagnostic Pod result = %#v, validation=%v", result, result.Validate())
		}
	})

	t.Run("malformed created result remains unknown and auditable", func(t *testing.T) {
		malformedRunner := &recordingDiagnosticPod{run: func(DiagnosticPodRequest) (DiagnosticPodObservation, error) {
			return DiagnosticPodObservation{Created: true}, errors.New("synthetic malformed adapter result")
		}}
		malformedGate := &recordingRemoteActionGate{}
		malformedTool, _ := NewRunDiagnosticPodTool(remoteToolDependencies(resolver, &recordingRemoteCommand{}, malformedRunner, malformedGate, allowRemoteOutputPolicy()))
		failed := malformedTool.Execute(context.Background(), call)
		outcome := malformedGate.lastOutcome()
		if !remoteToolFailed(failed) || malformedRunner.count() != 1 || malformedGate.outcomeCount() != 1 ||
			outcome.State != domain.RemoteDiagnosticOutcomeUnknown || outcome.Lifecycle.Validate() != nil ||
			outcome.Lifecycle.Create != domain.DiagnosticPodPhaseCompleted || outcome.Lifecycle.Wait != domain.DiagnosticPodPhaseUnknown ||
			outcome.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted || outcome.CleanupState != domain.DiagnosticPodCleanupUnknown {
			t.Fatalf("malformed diagnostic result/outcome = %#v/%#v", failed, outcome)
		}
	})
}

func completedDiagnosticPodLifecycle() domain.DiagnosticPodLifecycle {
	return domain.DiagnosticPodLifecycle{
		Create: domain.DiagnosticPodPhaseCompleted,
		Wait:   domain.DiagnosticPodPhaseCompleted,
		Log:    domain.DiagnosticPodPhaseCompleted,
		Delete: domain.DiagnosticPodPhaseCompleted,
	}
}

type recordingRemoteResolver struct {
	mu           sync.Mutex
	pod          ResolvedPod
	service      ResolvedService
	podCalls     int
	serviceCalls int
}

func (resolver *recordingRemoteResolver) ResolvePod(_ context.Context, _ PodResolveRequest) (ResolvedPod, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.podCalls++
	return resolver.pod, nil
}

func (resolver *recordingRemoteResolver) ResolveService(_ context.Context, _ ServiceResolveRequest) (ResolvedService, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.serviceCalls++
	return resolver.service, nil
}

func (resolver *recordingRemoteResolver) podCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.podCalls
}

type recordingRemoteCommand struct {
	mu       sync.Mutex
	requests []RemoteCommandRequest
	run      func(RemoteCommandRequest) (RemoteCommandObservation, error)
}

func (command *recordingRemoteCommand) ExecuteRemoteCommand(_ context.Context, request RemoteCommandRequest) (RemoteCommandObservation, error) {
	command.mu.Lock()
	command.requests = append(command.requests, request)
	run := command.run
	command.mu.Unlock()
	if run == nil {
		return RemoteCommandObservation{}, nil
	}
	return run(request)
}

func (command *recordingRemoteCommand) count() int {
	command.mu.Lock()
	defer command.mu.Unlock()
	return len(command.requests)
}

type recordingDiagnosticPod struct {
	mu       sync.Mutex
	requests []DiagnosticPodRequest
	run      func(DiagnosticPodRequest) (DiagnosticPodObservation, error)
}

func (runner *recordingDiagnosticPod) RunDiagnosticPod(_ context.Context, request DiagnosticPodRequest) (DiagnosticPodObservation, error) {
	runner.mu.Lock()
	runner.requests = append(runner.requests, request)
	run := runner.run
	runner.mu.Unlock()
	if run == nil {
		return DiagnosticPodObservation{}, nil
	}
	return run(request)
}

func (runner *recordingDiagnosticPod) count() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return len(runner.requests)
}

type recordingRemoteActionGate struct {
	mu             sync.Mutex
	plans          []domain.RemoteDiagnosticActionPlan
	outcomes       []domain.RemoteDiagnosticOutcome
	authorizeErr   error
	afterAuthorize func()
}

func (gate *recordingRemoteActionGate) AuthorizeAndConsume(_ context.Context, plan domain.RemoteDiagnosticActionPlan) (domain.ActionEnvelope, error) {
	gate.mu.Lock()
	gate.plans = append(gate.plans, plan)
	errorValue := gate.authorizeErr
	afterAuthorize := gate.afterAuthorize
	index := len(gate.plans)
	gate.mu.Unlock()
	if afterAuthorize != nil {
		afterAuthorize()
	}
	if errorValue != nil {
		return domain.ActionEnvelope{}, errorValue
	}
	policy := application.PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: plan.PolicyGeneration, FullAccessAllowed: true, HighRiskAcknowledged: true}
	return application.NewRemoteDiagnosticActionEnvelope(domain.ApprovalID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 70_000+index)), policy, plan, testObservedAt)
}

type mutableScopeGuard struct {
	mu      sync.Mutex
	current bool
}

func (guard *mutableScopeGuard) Current(context.Context, domain.ClusterScope) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.current
}

func (guard *mutableScopeGuard) set(current bool) {
	guard.mu.Lock()
	guard.current = current
	guard.mu.Unlock()
}

func (gate *recordingRemoteActionGate) RecordOutcome(_ context.Context, _ domain.ActionEnvelope, outcome domain.RemoteDiagnosticOutcome) error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.outcomes = append(gate.outcomes, outcome)
	return nil
}

func (gate *recordingRemoteActionGate) authorizeCount() int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return len(gate.plans)
}

func (gate *recordingRemoteActionGate) outcomeCount() int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return len(gate.outcomes)
}

func (gate *recordingRemoteActionGate) lastPlan() domain.RemoteDiagnosticActionPlan {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.plans[len(gate.plans)-1]
}

func (gate *recordingRemoteActionGate) lastOutcome() domain.RemoteDiagnosticOutcome {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.outcomes[len(gate.outcomes)-1]
}

type sequenceRemoteOutputPolicy struct {
	mu        sync.Mutex
	decisions []LogPolicyDecision
	calls     int
}

func (policy *sequenceRemoteOutputPolicy) AuthorizeRemoteOutput(_ context.Context, _ RemoteOutputPolicyRequest) LogPolicyDecision {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	policy.calls++
	if len(policy.decisions) == 0 {
		return LogPolicyDenied
	}
	result := policy.decisions[0]
	if len(policy.decisions) > 1 {
		policy.decisions = policy.decisions[1:]
	}
	return result
}

func (policy *sequenceRemoteOutputPolicy) count() int {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.calls
}

func allowRemoteOutputPolicy() *sequenceRemoteOutputPolicy {
	return &sequenceRemoteOutputPolicy{decisions: []LogPolicyDecision{
		LogPolicyAllowed, LogPolicyAllowed, LogPolicyAllowed, LogPolicyAllowed, LogPolicyAllowed,
	}}
}

func remoteToolDependencies(resolver RemoteTargetResolver, command RemoteCommandExecutor, runner DiagnosticPodRunner, gate RemoteDiagnosticActionGate, policy RemoteOutputDataPolicy) RemoteDiagnosticToolDependencies {
	return RemoteDiagnosticToolDependencies{
		Resolver: resolver, Commands: command, DiagnosticPods: runner, ScopeGuard: &sequenceScopeGuard{}, PolicyGuard: alwaysCurrentPolicyGuard{},
		Actions: gate, OutputPolicy: policy, EvidenceIDs: &sequenceEvidenceIDs{}, Text: security.NewRedactor(), Now: func() time.Time { return testObservedAt },
	}
}

func remoteToolRunInput(t *testing.T) agent.RunInput {
	t.Helper()
	base := testRunInput(t, 0)
	general, _ := domain.NewActionArguments([]string{"literal;not-a-shell", "$(ignored)", "*.log"})
	roots, _ := domain.NewContainerFileRoots([]string{"/var/app/data"})
	diagnostic, _ := domain.NewActionArguments([]string{"-z", "-v", "-w", "5"})
	catalog, err := domain.NewRemoteDiagnosticsPolicyCatalog(
		[]domain.PodExecPolicy{{ID: "literal-argv", Class: domain.PodExecPolicyGeneral, Executable: "/usr/bin/printf", Arguments: general, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 16384}},
		&domain.ContainerFilePolicy{Enabled: true, ReaderExecutable: "/bin/tar", AllowedRoots: roots, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 16384},
		[]domain.DiagnosticPodPolicy{{ID: "tcp-connect", Namespace: "team-a", Image: "registry.example/diag@sha256:" + strings.Repeat("a", 64), Executable: "/bin/nc", ArgumentPrefix: diagnostic, ServiceName: "api", Port: 8443, NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 16384}},
	)
	if err != nil {
		t.Fatalf("NewRemoteDiagnosticsPolicyCatalog() error = %v", err)
	}
	input, err := agent.NewRunInputWithCompletePolicyContext(base.RunID(), base.SessionID(), base.RequestMessageID(), base.Question(), base.Scope(), base.Resource(), base.BudgetLimits(), base.Conversation(), base.ResourcePolicies(), base.ObservabilityPolicies(), catalog, 3)
	if err != nil {
		t.Fatalf("NewRunInputWithCompletePolicyContext() error = %v", err)
	}
	return input
}

func bindRemoteToolCall(t *testing.T, input agent.RunInput, name domain.ToolName, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{ID: "call-remote", Name: name, ArgumentsJSON: arguments})
	if err != nil {
		t.Fatalf("BindToolCall(%s) error = %v", name, err)
	}
	return call
}

func remoteTestPod() ResolvedPod {
	return ResolvedPod{Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "20"}, Container: "app"}
}

func remoteTestService() ResolvedService {
	return ResolvedService{Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: "team-a", Name: "api", UID: "service-uid", ResourceVersion: "21"}, Port: 8443}
}

func mustRemoteChunk(t *testing.T, sequence int, stream RemoteOutputStream, content []byte) RemoteOutputChunk {
	t.Helper()
	chunk, err := NewRemoteOutputChunk(sequence, stream, content)
	if err != nil {
		t.Fatalf("NewRemoteOutputChunk() error = %v", err)
	}
	return chunk
}

func regularFileArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range []string{"var", "var/app", "var/app/data"} {
		if err := writer.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatalf("WriteHeader(%s) error = %v", name, err)
		}
	}
	if err := writer.WriteHeader(&tar.Header{Name: "var/app/data/report.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatalf("WriteHeader(file) error = %v", err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("Write(file) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(tar) error = %v", err)
	}
	return buffer.Bytes()
}

func symlinkFileArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "var/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "var/app", Typeflag: tar.TypeSymlink, Linkname: "/var/run/secrets"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func outOfOrderFileArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "var/app/data/report.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"var", "var/app", "var/app/data"} {
		if err := writer.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func remoteToolFailed(result ToolResult) bool {
	return (result.Status == domain.ToolResultStatusError || result.Status == domain.ToolResultStatusDenied) && result.Error != nil
}
