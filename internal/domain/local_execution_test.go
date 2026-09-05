package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLocalCommandClassificationRejectsOverridesAndKeepsMetacharactersLiteral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind      LocalCommandKind
		arguments []string
		want      LocalCommandOperation
	}{
		{LocalCommandKubectl, []string{"version", "--client=true"}, LocalOperationKubectlVersion},
		{LocalCommandKubectl, []string{"rollout", "status", "deployment/api", "--watch=false"}, LocalOperationKubectlRolloutStatus},
		{LocalCommandKubectl, []string{"auth", "can-i", "get", "pods"}, LocalOperationKubectlAuthCanI},
		{LocalCommandHelm, []string{"list", "--max", "50"}, LocalOperationHelmList},
		{LocalCommandHelm, []string{"status", "api"}, LocalOperationHelmStatus},
		{LocalCommandHelm, []string{"history", "api", "--max", "20"}, LocalOperationHelmHistory},
		{LocalCommandHelm, []string{"rollback", "api", "3"}, LocalOperationHelmRollback},
		{LocalCommandArgoCD, []string{"app", "get", "api"}, LocalOperationArgoCDAppGet},
		{LocalCommandArgoCD, []string{"app", "diff", "api", "--revision", "main"}, LocalOperationArgoCDAppDiff},
		{LocalCommandArgoCD, []string{"app", "sync", "api", "--revision", "sha-123"}, LocalOperationArgoCDAppSync},
		{LocalCommandArgoCD, []string{"app", "rollback", "api", "2"}, LocalOperationArgoCDAppRollback},
		{LocalCommandDiagnostic, []string{"literal;still-one-argument", "$(not-expanded)"}, LocalOperationDiagnostic},
	}
	for _, test := range tests {
		arguments, err := NewActionArguments(test.arguments)
		if err != nil {
			t.Fatalf("NewActionArguments(%q) error = %v", test.arguments, err)
		}
		operation, ok := ClassifyLocalCommand(test.kind, arguments)
		if !ok || operation != test.want {
			t.Errorf("ClassifyLocalCommand(%s, %q) = %s/%t, want %s/true", test.kind, test.arguments, operation, ok, test.want)
		}
	}

	denied := []struct {
		kind      LocalCommandKind
		arguments []string
	}{
		{LocalCommandKubectl, []string{"get", "pods"}},
		{LocalCommandKubectl, []string{"version", "--client=true", "--kubeconfig=/tmp/other"}},
		{LocalCommandKubectl, []string{"rollout", "status", "deployment/api", "--watch=false", "--context", "other"}},
		{LocalCommandHelm, []string{"rollback", "api", "3", "--values", "/tmp/values"}},
		{LocalCommandHelm, []string{"plugin", "install", "value"}},
		{LocalCommandArgoCD, []string{"app", "sync", "api", "--revision", "main", "--auth-token=value"}},
		{LocalCommandDiagnostic, []string{"-c", "do work"}},
		{LocalCommandDiagnostic, []string{"--command=do work"}},
		{LocalCommandDiagnostic, []string{"--config", "/tmp/diagnostic.conf"}},
		{LocalCommandDiagnostic, []string{"@/tmp/response-file"}},
		{LocalCommandDiagnostic, []string{"/tmp/diagnostic.py"}},
	}
	for _, test := range denied {
		arguments, err := NewActionArguments(test.arguments)
		if err != nil {
			continue
		}
		if operation, ok := ClassifyLocalCommand(test.kind, arguments); ok {
			t.Errorf("ClassifyLocalCommand(%s, %q) unexpectedly admitted %s", test.kind, test.arguments, operation)
		}
	}
}

func TestLocalCommandPolicyBuildsRuntimeOwnedScopeArguments(t *testing.T) {
	t.Parallel()

	policy := testLocalCommandPolicy(t, LocalCommandKubectl, []string{"rollout", "status", "deployment/api", "--watch=false"})
	scope := testLocalExecutionScope()
	arguments, err := policy.RuntimeArguments(scope)
	if err != nil {
		t.Fatalf("RuntimeArguments() error = %v", err)
	}
	want := []string{"rollout", "status", "deployment/api", "--watch=false", "--context", "test-context", "--namespace", "test-namespace"}
	if !reflect.DeepEqual(arguments.Values(), want) {
		t.Fatalf("RuntimeArguments() = %#v, want %#v", arguments.Values(), want)
	}

	override, _ := NewActionArguments([]string{"rollout", "status", "deployment/api", "--watch=false", "--namespace=other"})
	policy.Arguments = override
	policy.Operation = LocalOperationKubectlRolloutStatus
	if policy.Validate() == nil {
		t.Fatal("policy admitted a caller-selected Namespace override")
	}
}

func TestLocalCommandPolicyRejectsKnownCommandAndScriptMultiplexers(t *testing.T) {
	t.Parallel()
	for _, executable := range []string{
		"/usr/bin/busybox", "/usr/bin/toybox", "/usr/bin/awk", "/usr/bin/sed", "/usr/bin/find",
		"/usr/bin/git", "/usr/bin/ssh", "/usr/bin/timeout", "/usr/bin/nice", "/usr/bin/chroot",
		"/usr/bin/open", "/usr/bin/xdg-open",
	} {
		policy := testLocalCommandPolicy(t, LocalCommandDiagnostic, []string{"status"})
		policy.Executable = executable
		if policy.Validate() == nil {
			t.Errorf("policy admitted command multiplexer %q", executable)
		}
	}
}

func TestLocalCommandPlanBindsFilesystemEnvironmentAndNetworkIdentity(t *testing.T) {
	t.Parallel()

	policy := testLocalCommandPolicy(t, LocalCommandArgoCD, []string{"app", "sync", "api", "--revision", "main"})
	policy.ServerOrigin = "https://argocd.example.test:443"
	policy.ServerOriginHash = LocalCommandOriginHash(policy.ServerOrigin)
	policy.CredentialReference = LocalCredentialArgoCDCLIProfile
	if policy.Validate() != nil || policy.Risk() != RiskCritical || policy.NetworkEffects() != ActionNetworkExternalCommand {
		t.Fatalf("Argo CD policy = %#v", policy)
	}
	plan := testLocalCommandPlan(t, policy)
	intent, err := plan.Intent(PermissionProfileAsk)
	if err != nil || intent.ValidateLocalCommand() != nil || !plan.MatchesIntent(intent) {
		t.Fatalf("Intent() = %#v/%v", intent, err)
	}
	if intent.Parameters.Executable != policy.Executable || intent.Parameters.ExecutableID != plan.Observation.ExecutableID ||
		intent.Parameters.WorkingDirectory != policy.WorkingDirectory ||
		intent.Parameters.WorkingDirectoryID != plan.Observation.WorkingDirectoryID ||
		intent.Parameters.Environment != policy.Environment || intent.Parameters.CredentialReference != LocalCredentialArgoCDCLIProfile ||
		intent.NetworkDestinationHash != policy.ServerOriginHash ||
		intent.Stdin || intent.TTY || intent.Shell {
		t.Fatalf("local command intent omitted a bound field: %#v", intent)
	}

	changed := intent
	changed.Parameters.ExecutableID = ActionDigest(strings.Repeat("c", 64))
	if plan.MatchesIntent(changed) {
		t.Fatal("plan matched a replaced executable identity")
	}
	changed = intent
	changed.Parameters.WorkingDirectory = "/tmp"
	if plan.MatchesIntent(changed) {
		t.Fatal("plan matched a replaced working directory")
	}
	changed = intent
	changed.Parameters.CredentialReference = LocalCredentialNone
	if plan.MatchesIntent(changed) || changed.Parameters.Digest().Equal(intent.Parameters.Digest()) {
		t.Fatal("plan or digest ignored a replaced credential-reference identity")
	}
}

func TestShellCommandIsSeparateCriticalTaggedParameter(t *testing.T) {
	t.Parallel()

	environment, err := NewActionEnvironment([]string{"LC_ALL=C", "NO_COLOR=1"})
	if err != nil {
		t.Fatalf("NewActionEnvironment() error = %v", err)
	}
	policy := LocalShellPolicy{
		ID: "exact-maintenance", Executable: "/bin/sh", Command: "printf '%s' exact",
		WorkingDirectory: "/var/empty", Environment: environment, Network: LocalShellNetworkNone,
		Timeout: 30 * time.Second, MaxLines: 10, MaxBytes: 1024,
	}
	plan := LocalShellActionPlan{
		RunID: "00000000-0000-7000-8000-000000000101", SessionID: "00000000-0000-7000-8000-000000000102",
		Scope: testLocalExecutionScope(), PolicyGeneration: 4, NamespaceTarget: testLocalNamespaceTarget(), Policy: policy,
		Observation: LocalExecutionObservation{ExecutableID: ActionDigest(strings.Repeat("a", 64)), WorkingDirectoryID: ActionDigest(strings.Repeat("b", 64))},
		Limits:      ActionLimits{Timeout: policy.Timeout, MaximumItems: 1, MaximumLines: policy.MaxLines, MaximumBytes: policy.MaxBytes, MaximumOutput: policy.MaxBytes},
		Purpose:     "Run one exact configured maintenance command.",
	}
	intent, err := plan.Intent(PermissionProfileAsk)
	if err != nil || intent.ValidateShellCommand() != nil || intent.Risk != RiskCritical || !intent.Shell ||
		intent.Parameters.Kind != ActionParametersShellCommand || intent.Parameters.Arguments != (ActionArguments{}) ||
		intent.Parameters.ShellCommand != policy.Command {
		t.Fatalf("shell intent = %#v/%v", intent, err)
	}
	changed := intent
	changed.Shell = false
	if changed.Validate() == nil {
		t.Fatal("shell command entered a direct argv envelope")
	}
}

func TestLocalEnvironmentIsOrderedCopiedAndStrict(t *testing.T) {
	t.Parallel()

	source := []string{"NO_COLOR=1", "LC_ALL=C"}
	environment, err := NewActionEnvironment(source)
	if err != nil {
		t.Fatalf("NewActionEnvironment() error = %v", err)
	}
	source[0] = "API_KEY=canary"
	if got := environment.Values(); !reflect.DeepEqual(got, []string{"LC_ALL=C", "NO_COLOR=1"}) {
		t.Fatalf("environment = %#v", got)
	}
	for _, denied := range [][]string{
		{"PATH=/tmp"}, {"HOME=/tmp"}, {"KUBECONFIG=/tmp/value"}, {"HTTPS_PROXY=http://proxy"},
		{"OPENAI_API_KEY=value"}, {"LC_ALL=C", "LC_ALL=POSIX"},
	} {
		if _, err := NewActionEnvironment(denied); err == nil {
			t.Errorf("NewActionEnvironment(%q) unexpectedly succeeded", denied)
		}
	}
}

func testLocalCommandPolicy(t *testing.T, kind LocalCommandKind, values []string) LocalCommandPolicy {
	t.Helper()
	arguments, err := NewActionArguments(values)
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	operation, ok := ClassifyLocalCommand(kind, arguments)
	if !ok {
		t.Fatalf("ClassifyLocalCommand(%s, %q) denied test fixture", kind, values)
	}
	executable := "/opt/bin/diagnostic"
	switch kind {
	case LocalCommandKubectl:
		executable = "/usr/local/bin/kubectl"
	case LocalCommandHelm:
		executable = "/usr/local/bin/helm"
	case LocalCommandArgoCD:
		executable = "/usr/local/bin/argocd"
	}
	return LocalCommandPolicy{
		ID: "test-command", Kind: kind, Operation: operation, Executable: executable,
		Arguments: arguments, WorkingDirectory: "/var/empty", CredentialReference: LocalCredentialNone,
		DiagnosticEffect: func() LocalDiagnosticEffect {
			if kind == LocalCommandDiagnostic {
				return LocalDiagnosticNoNetworkRead
			}
			return ""
		}(),
		Timeout: 30 * time.Second, MaxLines: 20, MaxBytes: 2048,
	}
}

func testLocalExecutionScope() ClusterScope {
	return ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.UnixMilli(1700000000000).UTC()}
}

func testLocalNamespaceTarget() ResourceRef {
	return ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "test-namespace", UID: "namespace-uid", ResourceVersion: "42"}
}

func testLocalCommandPlan(t *testing.T, policy LocalCommandPolicy) LocalCommandActionPlan {
	t.Helper()
	scope := testLocalExecutionScope()
	arguments, err := policy.RuntimeArguments(scope)
	if err != nil {
		t.Fatalf("RuntimeArguments() error = %v", err)
	}
	return LocalCommandActionPlan{
		RunID: "00000000-0000-7000-8000-000000000101", SessionID: "00000000-0000-7000-8000-000000000102",
		Scope: scope, PolicyGeneration: 4, NamespaceTarget: testLocalNamespaceTarget(), Policy: policy, Arguments: arguments,
		Observation: LocalExecutionObservation{ExecutableID: ActionDigest(strings.Repeat("a", 64)), WorkingDirectoryID: ActionDigest(strings.Repeat("b", 64))},
		Limits:      ActionLimits{Timeout: policy.Timeout, MaximumItems: 1, MaximumLines: policy.MaxLines, MaximumBytes: policy.MaxBytes, MaximumOutput: policy.MaxBytes},
		Purpose:     "Run one exact configured local command.",
	}
}
