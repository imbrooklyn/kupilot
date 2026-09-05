package config

import (
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestLoadLocalExecutionBuildsExactDefaultOffCatalogs(t *testing.T) {
	paths := testPaths(t.TempDir())
	writePrivateFile(t, paths.ConfigFile, []byte(version2Config("", "")+validLocalExecutionYAMLFixture()))
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	commands, shells, err := loaded.Config.LocalExecutionPolicyCatalogs()
	if err != nil || commands.Validate() != nil || shells.Validate() != nil ||
		len(commands.Policies()) != 3 || len(shells.Policies()) != 1 {
		t.Fatalf("LocalExecutionPolicyCatalogs() = %#v/%#v/%v", commands, shells, err)
	}
	kubectl, found := commands.Resolve("kubectl-rollout-status")
	if !found || kubectl.Kind != domain.LocalCommandKubectl || kubectl.Operation != domain.LocalOperationKubectlRolloutStatus ||
		kubectl.Risk() != domain.RiskReview || kubectl.NetworkEffects() != domain.ActionNetworkKubernetesAPI ||
		kubectl.Environment.Values()[0] != "LC_ALL=C" {
		t.Fatalf("kubectl policy = %#v found=%t", kubectl, found)
	}
	argocd, found := commands.Resolve("argocd-sync")
	if !found || argocd.Risk() != domain.RiskCritical || argocd.ServerOrigin != "https://argocd.example.test" ||
		argocd.ServerOriginHash != domain.LocalCommandOriginHash(argocd.ServerOrigin) {
		t.Fatalf("Argo CD policy = %#v found=%t", argocd, found)
	}
	if shell, found := shells.Resolve("maintenance-shell"); !found || shell.Network != domain.LocalShellNetworkNone || shell.Command != "printf shell-policy" {
		t.Fatalf("shell policy = %#v found=%t", shell, found)
	}

	empty := Defaults()
	disabledCommands, disabledShells, err := empty.LocalExecutionPolicyCatalogs()
	if err != nil || len(disabledCommands.Policies()) != 0 || len(disabledShells.Policies()) != 0 {
		t.Fatalf("default local catalogs = %#v/%#v/%v", disabledCommands, disabledShells, err)
	}
}

func TestLoadLocalExecutionRejectsSchemaAndPrivilegeEscalation(t *testing.T) {
	valid := version2Config("", "") + validLocalExecutionYAMLFixture()
	tests := []struct {
		name string
		text string
		code string
	}{
		{name: "unknown field", text: strings.Replace(valid, "      max_bytes: 4096", "      max_bytes: 4096\n      plugin: true", 1), code: "config_schema_invalid"},
		{name: "alternate kubeconfig", text: strings.Replace(valid, "--watch=false]", "--kubeconfig=/tmp/other, --watch=false]", 1), code: "config_local_execution_invalid"},
		{name: "Context override", text: strings.Replace(valid, "--watch=false]", "--context=other, --watch=false]", 1), code: "config_local_execution_invalid"},
		{name: "impersonation", text: strings.Replace(valid, "--watch=false]", "--as=admin, --watch=false]", 1), code: "config_local_execution_invalid"},
		{name: "credential token", text: strings.Replace(valid, "--watch=false]", "--token=value, --watch=false]", 1), code: "config_local_execution_invalid"},
		{name: "shell smuggling", text: strings.Replace(valid, "kind: kubectl", "kind: diagnostic", 1), code: "config_local_execution_invalid"},
		{name: "inherited secret environment", text: strings.Replace(valid, "environment: [LC_ALL=C, NO_COLOR=1]", "environment: [OPENAI_API_KEY=value]", 1), code: "config_local_execution_invalid"},
		{name: "relative executable", text: strings.Replace(valid, "/usr/local/bin/kubectl", "kubectl", 1), code: "config_local_execution_invalid"},
		{name: "cwd traversal", text: strings.Replace(valid, "working_directory: /private/tmp", "working_directory: /private/tmp/../etc", 1), code: "config_local_execution_invalid"},
		{name: "shell in argv entry", text: strings.Replace(valid, "/usr/local/bin/kubectl", "/bin/sh", 1), code: "config_local_execution_invalid"},
		{name: "unbound Argo origin", text: strings.Replace(valid, "server_origin: https://argocd.example.test", "server_origin: http://argocd.example.test", 1), code: "config_local_execution_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			writePrivateFile(t, paths.ConfigFile, []byte(test.text))
			_, err := Load(context.Background(), LoadOptions{Paths: paths})
			assertSafeError(t, err, ClassConfigurationInvalid, test.code)
		})
	}
}

func validLocalExecutionYAMLFixture() string {
	return `local_execution:
  commands:
    - id: kubectl-rollout-status
      kind: kubectl
      executable: /usr/local/bin/kubectl
      arguments: [rollout, status, deployment/sample, --watch=false]
      working_directory: /private/tmp
      environment: [LC_ALL=C, NO_COLOR=1]
      credential_ref: none
      timeout_seconds: 30
      max_lines: 100
      max_bytes: 4096
    - id: helm-history
      kind: helm
      executable: /usr/local/bin/helm
      arguments: [history, sample, --max, "20"]
      working_directory: /private/tmp
      environment: [LC_ALL=C]
      credential_ref: none
      timeout_seconds: 30
      max_lines: 100
      max_bytes: 4096
    - id: argocd-sync
      kind: argocd
      executable: /usr/local/bin/argocd
      arguments: [app, sync, sample, --revision, abc-123]
      working_directory: /private/tmp
      environment: [LC_ALL=C]
      credential_ref: argocd_cli_profile
      server_origin: https://argocd.example.test
      timeout_seconds: 60
      max_lines: 200
      max_bytes: 8192
  shells:
    - id: maintenance-shell
      executable: /bin/sh
      command: printf shell-policy
      working_directory: /private/tmp
      environment: [LC_ALL=C]
      network: none
      timeout_seconds: 10
      max_lines: 20
      max_bytes: 1024
`
}
