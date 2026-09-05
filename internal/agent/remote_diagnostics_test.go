package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestBindRemoteDiagnosticsKeepsExactArgvAndRejectsAuthorityExpansion(t *testing.T) {
	input := remoteDiagnosticsRunInput(t)
	selection := ToolSelection{
		ID: "call-remote-1", Name: domain.ToolNamePodExec,
		ArgumentsJSON: `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"test-namespace","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`,
	}
	call, err := BindToolCall(input, testInvocationID, selection)
	if err != nil || call.ArgumentsJSON() != selection.ArgumentsJSON || call.Purpose() != "Inspect exact argv output." {
		t.Fatalf("BindToolCall(pod_exec) = %#v/%v", call, err)
	}
	for _, changed := range []string{
		strings.Replace(selection.ArgumentsJSON, "literal;not-a-shell", "literal;touch /tmp/changed", 1),
		strings.Replace(selection.ArgumentsJSON, `"*.log"`, `"*.log","extra"`, 1),
		strings.Replace(selection.ArgumentsJSON, "/usr/bin/printf", "/bin/sh", 1),
		strings.Replace(selection.ArgumentsJSON, `"container":"app"`, `"container":"app","tty":true`, 1),
		strings.Replace(selection.ArgumentsJSON, "test-namespace", "other", 1),
	} {
		selection.ArgumentsJSON = changed
		if _, err := BindToolCall(input, testInvocationID, selection); err == nil {
			t.Fatalf("BindToolCall accepted expanded Pod Exec authority: %s", changed)
		}
	}
	disabled := testRunInput(t, "Inspect the selected Pod.")
	selection.ArgumentsJSON = `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"test-namespace","pod_name":"sample-pod","purpose":"Inspect exact argv output."}`
	if _, err := BindToolCall(disabled, testInvocationID, selection); err == nil {
		t.Fatal("BindToolCall accepted Pod Exec with a default-off policy catalog")
	}
}

func TestBindContainerFileAndDiagnosticPodUsesOnlyFrozenPolicy(t *testing.T) {
	input := remoteDiagnosticsRunInput(t)
	file := ToolSelection{ID: "call-file-1", Name: domain.ToolNameReadContainerFile, ArgumentsJSON: `{"container":"app","namespace":"test-namespace","path":"/var/app/data/report.txt","pod_name":"sample-pod","purpose":"Read one reviewed file."}`}
	call, err := BindToolCall(input, testInvocationID, file)
	if err != nil || call.ArgumentsJSON() != file.ArgumentsJSON {
		t.Fatalf("BindToolCall(read_container_file) = %#v/%v", call, err)
	}
	for _, path := range []string{"/var/app/data/../token", "/var/run/secrets/kubernetes.io/serviceaccount/token", "/var/app/database.txt"} {
		file.ArgumentsJSON = `{"container":"app","namespace":"test-namespace","path":"` + path + `","pod_name":"sample-pod","purpose":"Read one reviewed file."}`
		if _, err := BindToolCall(input, testInvocationID, file); err == nil {
			t.Fatalf("BindToolCall accepted denied path %q", path)
		}
	}
	diagnostic := ToolSelection{ID: "call-diag-1", Name: domain.ToolNameRunDiagnosticPod, ArgumentsJSON: `{"diagnostic_id":"tcp-connect","purpose":"Test one configured Service port."}`}
	if _, err := BindToolCall(input, testInvocationID, diagnostic); err != nil {
		t.Fatalf("BindToolCall(run_diagnostic_pod) error = %v", err)
	}
	for _, changed := range []string{
		strings.Replace(diagnostic.ArgumentsJSON, "tcp-connect", "other", 1),
		strings.Replace(diagnostic.ArgumentsJSON, `"purpose"`, `"namespace":"other","purpose"`, 1),
		strings.Replace(diagnostic.ArgumentsJSON, `"purpose"`, `"image":"registry.example/other@sha256:`+strings.Repeat("b", 64)+`","purpose"`, 1),
	} {
		diagnostic.ArgumentsJSON = changed
		if _, err := BindToolCall(input, testInvocationID, diagnostic); err == nil {
			t.Fatalf("BindToolCall accepted diagnostic authority expansion: %s", changed)
		}
	}
}

func TestRemoteDiagnosticPurposeMustFitActionEnvelope(t *testing.T) {
	input := remoteDiagnosticsRunInput(t)
	selection := ToolSelection{ID: "call-purpose-1", Name: domain.ToolNameRunDiagnosticPod, ArgumentsJSON: `{"diagnostic_id":"tcp-connect","purpose":"` + strings.Repeat("a", domain.MaxActionReasonSummaryBytes+1) + `"}`}
	if _, err := BindToolCall(input, testInvocationID, selection); err == nil {
		t.Fatal("BindToolCall accepted a purpose that cannot fit the ActionEnvelope")
	}
}

func TestRemoteToolSchemasExcludeRuntimeAndDiagnosticPodAuthority(t *testing.T) {
	for _, name := range []domain.ToolName{domain.ToolNamePodExec, domain.ToolNameReadContainerFile, domain.ToolNameRunDiagnosticPod} {
		var schema string
		for _, specification := range ToolSpecifications() {
			if specification.Name == name {
				schema = strings.ToLower(specification.InputSchemaJSON)
			}
		}
		if name == domain.ToolNameRunDiagnosticPod && strings.Contains(schema, `"namespace"`) {
			t.Fatal("diagnostic Pod schema exposed the policy-owned Namespace")
		}
		if schema == "" {
			t.Fatalf("missing schema for %q", name)
		}
		for _, field := range []string{`"uid"`, `"timeout"`, `"max_bytes"`, `"max_lines"`, `"stdin"`, `"tty"`, `"shell"`, `"image"`, `"target_host"`, `"service_account"`} {
			if strings.Contains(schema, field) {
				t.Fatalf("schema %q contains runtime authority %s", name, field)
			}
		}
	}
}

func remoteDiagnosticsRunInput(t *testing.T) RunInput {
	t.Helper()
	base := testRunInput(t, "Inspect one configured remote diagnostic.")
	generalArguments, err := domain.NewActionArguments([]string{"literal;not-a-shell", "$(ignored)", "*.log"})
	if err != nil {
		t.Fatalf("NewActionArguments(general) error = %v", err)
	}
	fileRoots, err := domain.NewContainerFileRoots([]string{"/var/app/data"})
	if err != nil {
		t.Fatalf("NewContainerFileRoots() error = %v", err)
	}
	diagnosticArguments, err := domain.NewActionArguments([]string{"-z", "-v", "-w", "5"})
	if err != nil {
		t.Fatalf("NewActionArguments(diagnostic) error = %v", err)
	}
	catalog, err := domain.NewRemoteDiagnosticsPolicyCatalog(
		[]domain.PodExecPolicy{{ID: "literal-argv", Class: domain.PodExecPolicyGeneral, Executable: "/usr/bin/printf", Arguments: generalArguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096}},
		&domain.ContainerFilePolicy{Enabled: true, ReaderExecutable: "/bin/tar", AllowedRoots: fileRoots, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096},
		[]domain.DiagnosticPodPolicy{{ID: "tcp-connect", Namespace: "test-namespace", Image: "registry.example/diag@sha256:" + strings.Repeat("a", 64), Executable: "/bin/nc", ArgumentPrefix: diagnosticArguments, ServiceName: "api", Port: 8443, NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096}},
	)
	if err != nil {
		t.Fatalf("NewRemoteDiagnosticsPolicyCatalog() error = %v", err)
	}
	input, err := NewRunInputWithCompletePolicyContext(
		base.RunID(), base.SessionID(), base.RequestMessageID(), base.Question(), base.Scope(), base.Resource(), base.BudgetLimits(), base.Conversation(),
		base.ResourcePolicies(), base.ObservabilityPolicies(), catalog, 3,
	)
	if err != nil {
		t.Fatalf("NewRunInputWithCompletePolicyContext() error = %v", err)
	}
	return input
}
