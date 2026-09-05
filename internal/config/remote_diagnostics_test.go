package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestLoadRemoteDiagnosticsBuildsExactDefaultOffCatalog(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	writePrivateFile(t, paths.ConfigFile, []byte(version2Config("", "")+validRemoteDiagnosticsYAMLFixture()))
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer loaded.Credentials.Destroy()
	catalog, err := loaded.Config.RemoteDiagnosticsPolicyCatalog()
	if err != nil || catalog.Version() != domain.RemoteDiagnosticsPolicyVersion || len(catalog.PodExecPolicies()) != 2 || len(catalog.DiagnosticPodPolicies()) != 1 {
		t.Fatalf("RemoteDiagnosticsPolicyCatalog() = %#v/%v", catalog, err)
	}
	predefined, found := catalog.ResolvePodExec("dns-config")
	if !found || predefined.Class != domain.PodExecPolicyPredefined || predefined.Executable != "/bin/cat" || predefined.Arguments.Values()[0] != "/etc/resolv.conf" {
		t.Fatalf("predefined policy = %#v, found=%t", predefined, found)
	}
	general, found := catalog.ResolvePodExec("literal-argv")
	if !found || general.Class != domain.PodExecPolicyGeneral || general.Arguments.Values()[0] != "literal;not-a-shell" {
		t.Fatalf("general policy = %#v, found=%t", general, found)
	}
	file, found := catalog.ContainerFile()
	if !found || !file.AllowedRoots.Allows("/var/app/data/report.txt") {
		t.Fatalf("container file policy = %#v, found=%t", file, found)
	}
	diagnostic, found := catalog.ResolveDiagnosticPod("tcp-connect")
	if !found || diagnostic.Image != "registry.example/diag@sha256:"+strings.Repeat("a", 64) || diagnostic.Namespace != "team-a" || diagnostic.ServiceName != "api" {
		t.Fatalf("diagnostic policy = %#v, found=%t", diagnostic, found)
	}

	emptyRoot := t.TempDir()
	emptyPaths := testPaths(emptyRoot)
	writePrivateFile(t, emptyPaths.ConfigFile, []byte(version2Config("", "")))
	empty, err := Load(context.Background(), LoadOptions{Paths: emptyPaths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load(default off) error = %v", err)
	}
	defer empty.Credentials.Destroy()
	disabled, err := empty.Config.RemoteDiagnosticsPolicyCatalog()
	if err != nil || len(disabled.PodExecPolicies()) != 0 || len(disabled.DiagnosticPodPolicies()) != 0 {
		t.Fatalf("default-off catalog = %#v/%v", disabled, err)
	}
}

func TestLoadRemoteDiagnosticsRejectsSchemaAndSemanticEscalation(t *testing.T) {
	valid := version2Config("", "") + validRemoteDiagnosticsYAMLFixture()
	tests := []struct {
		name string
		text string
		code string
	}{
		{name: "unknown authority field", text: strings.Replace(valid, "      max_bytes: 4096", "      max_bytes: 4096\n      tty: true", 1), code: "config_schema_invalid"},
		{name: "predefined risk lowering", text: strings.Replace(valid, "      executable: /bin/cat", "      executable: /bin/rm", 1), code: "config_remote_diagnostics_invalid"},
		{name: "shell executable", text: strings.Replace(valid, "      executable: /usr/bin/printf", "      executable: /bin/sh", 1), code: "config_remote_diagnostics_invalid"},
		{name: "shell dispatch wrapper", text: strings.Replace(valid, "      executable: /usr/bin/printf\n        arguments: [literal;not-a-shell]", "      executable: /usr/bin/env\n        arguments: [sh, -c, id]", 1), code: "config_remote_diagnostics_invalid"},
		{name: "credential-shaped argv", text: strings.Replace(valid, "arguments: [literal;not-a-shell]", "arguments: [token=not-admitted]", 1), code: "config_remote_diagnostics_invalid"},
		{name: "credential file argv", text: strings.Replace(valid, "arguments: [literal;not-a-shell]", "arguments: [--client-secret-file=/tmp/value]", 1), code: "config_remote_diagnostics_invalid"},
		{name: "unsafe file root", text: strings.Replace(valid, "[/var/app/data]", "[/var/run/secrets]", 1), code: "config_remote_diagnostics_invalid"},
		{name: "unpinned image", text: strings.Replace(valid, "registry.example/diag@sha256:"+strings.Repeat("a", 64), "registry.example/diag:latest", 1), code: "config_remote_diagnostics_invalid"},
		{name: "image URL", text: strings.Replace(valid, "registry.example/diag@sha256:"+strings.Repeat("a", 64), "https://registry.example/diag@sha256:"+strings.Repeat("a", 64), 1), code: "config_remote_diagnostics_invalid"},
		{name: "image user information", text: strings.Replace(valid, "registry.example/diag@sha256:"+strings.Repeat("a", 64), "user:password@registry.example/diag@sha256:"+strings.Repeat("a", 64), 1), code: "config_remote_diagnostics_invalid"},
		{name: "missing network prerequisite", text: strings.Replace(valid, "network_policy_required: true", "network_policy_required: false", 1), code: "config_remote_diagnostics_invalid"},
		{name: "missing policy Namespace", text: strings.Replace(valid, "        namespace: team-a\n", "", 1), code: "config_schema_invalid"},
		{name: "model-selected target field", text: strings.Replace(valid, "      service_name: api", "      service_name: api\n      target: 169.254.169.254", 1), code: "config_schema_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			paths := testPaths(root)
			paths.ConfigFile = filepath.Join(root, "config.yaml")
			writePrivateFile(t, paths.ConfigFile, []byte(test.text))
			_, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
			assertSafeError(t, err, ClassConfigurationInvalid, test.code)
		})
	}
}

func validRemoteDiagnosticsYAMLFixture() string {
	return `kubernetes:
  remote_diagnostics:
    pod_exec:
      - id: dns-config
        class: predefined
        executable: /bin/cat
        arguments: [/etc/resolv.conf]
        timeout_seconds: 5
        max_lines: 20
        max_bytes: 4096
      - id: literal-argv
        class: general
        executable: /usr/bin/printf
        arguments: [literal;not-a-shell]
        timeout_seconds: 5
        max_lines: 20
        max_bytes: 4096
    container_file:
      reader_executable: /bin/tar
      allowed_roots: [/var/app/data]
      timeout_seconds: 5
      max_lines: 20
      max_bytes: 4096
    diagnostic_pods:
      - id: tcp-connect
        namespace: team-a
        image: registry.example/diag@sha256:` + strings.Repeat("a", 64) + `
        executable: /bin/nc
        argument_prefix: [-z, -v, -w, "5"]
        service_name: api
        port: 8443
        network_policy_required: true
        timeout_seconds: 10
        max_lines: 20
        max_bytes: 4096
`
}
