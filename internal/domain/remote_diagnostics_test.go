package domain

import (
	"testing"
	"time"
)

func TestRemoteDiagnosticsPolicyRejectsRiskLoweringShellAndUnpinnedTargets(t *testing.T) {
	predefinedArguments, err := NewActionArguments([]string{"/etc/resolv.conf"})
	if err != nil {
		t.Fatalf("NewActionArguments(predefined) error = %v", err)
	}
	predefined := PodExecPolicy{ID: "dns-config", Class: PodExecPolicyPredefined, Executable: "/bin/cat", Arguments: predefinedArguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096}
	if err := predefined.Validate(); err != nil {
		t.Fatalf("predefined policy validation = %v", err)
	}
	changed := predefined
	changed.Executable = "/bin/rm"
	if changed.Validate() == nil {
		t.Fatal("predefined class lowered the risk of an unrecognized command")
	}
	generalArguments, err := NewActionArguments([]string{"literal;touch", "$(ignored)", "*.log"})
	if err != nil {
		t.Fatalf("NewActionArguments(general) error = %v", err)
	}
	general := PodExecPolicy{ID: "literal-argv", Class: PodExecPolicyGeneral, Executable: "/usr/bin/printf", Arguments: generalArguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096}
	if err := general.Validate(); err != nil || len(general.Arguments.Values()) != 3 || general.Arguments.Values()[0] != "literal;touch" {
		t.Fatalf("general exact argv policy = %#v, validation = %v", general, err)
	}
	general.Executable = "/bin/sh"
	if general.Validate() == nil {
		t.Fatal("remote policy accepted a shell executable")
	}
	general.Executable = "/bin/ash"
	if general.Validate() == nil {
		t.Fatal("remote policy accepted an alternate shell executable")
	}
	wrapperArguments, err := NewActionArguments([]string{"sh", "-c", "touch /tmp/unexpected"})
	if err != nil {
		t.Fatalf("NewActionArguments(wrapper) error = %v", err)
	}
	general.Executable, general.Arguments = "/usr/bin/env", wrapperArguments
	if general.Validate() == nil {
		t.Fatal("remote policy accepted shell dispatch through env")
	}
	if ValidRemoteExecutable("/usr/bin/print\nf") {
		t.Fatal("remote policy accepted a control-bearing executable")
	}

	prefix, err := NewActionArguments([]string{"-z", "-v", "-w", "5"})
	if err != nil {
		t.Fatalf("NewActionArguments(diagnostic) error = %v", err)
	}
	diagnostic := DiagnosticPodPolicy{
		ID: "tcp-connect", Image: "registry.example/diag@sha256:" + repeatRemoteTest("a", 64),
		Namespace: "team-a", Executable: "/bin/nc", ArgumentPrefix: prefix, ServiceName: "api", Port: 8443,
		NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
	if err := diagnostic.Validate(); err != nil {
		t.Fatalf("diagnostic policy validation = %v", err)
	}
	host, err := diagnostic.TargetHost()
	if err != nil || host != "api.team-a.svc" || ValidRemoteDiagnosticTarget("169.254.169.254") || ValidRemoteDiagnosticTarget("localhost.team-a.svc") || ValidRemoteDiagnosticTarget("api.example.com") {
		t.Fatalf("diagnostic target host = %q/%v", host, err)
	}
	diagnostic.NetworkPolicyRequired = false
	if diagnostic.Validate() == nil {
		t.Fatal("diagnostic Pod policy accepted a missing NetworkPolicy prerequisite")
	}
	diagnostic.NetworkPolicyRequired = true
	diagnostic.Namespace = ""
	if diagnostic.Validate() == nil {
		t.Fatal("diagnostic Pod policy accepted an unfixed Namespace")
	}
}

func TestContainerFilePolicyNormalizesAndDeniesUnsafeRoots(t *testing.T) {
	roots, err := NewContainerFileRoots([]string{"/var/app/data"})
	if err != nil || !roots.Allows("/var/app/data/report.txt") || roots.Allows("/var/app/database.txt") {
		t.Fatalf("container roots = %#v/%v", roots.Values(), err)
	}
	for _, value := range []string{"var/app/data/report.txt", "/var/app/../secrets", "/var/app/data/"} {
		if _, err := NormalizeContainerFilePath(value); err == nil {
			t.Fatalf("NormalizeContainerFilePath(%q) accepted a non-normalized path", value)
		}
		if roots.Allows(value) {
			t.Fatalf("ContainerFileRoots.Allows(%q) = true", value)
		}
	}
	for _, value := range []string{"/proc/self/environ", "/var/run/secrets/kubernetes.io/serviceaccount/token", "/var/app/data/credentials/cloud", "/root/.ssh/id_ed25519"} {
		if roots.Allows(value) {
			t.Fatalf("ContainerFileRoots.Allows(%q) = true", value)
		}
	}
	components, err := ContainerFilePathComponents("/var/app/data/report.txt")
	if err != nil || len(components) != 4 || components[0] != "var" || components[3] != "var/app/data/report.txt" {
		t.Fatalf("ContainerFilePathComponents() = %#v/%v", components, err)
	}
	readerArguments, err := ContainerFileReaderArguments("/var/app/data/report.txt")
	if err != nil || len(readerArguments.Values()) != 11 || readerArguments.Values()[1] != "--blocking-factor=1" {
		t.Fatalf("ContainerFileReaderArguments() = %#v/%v", readerArguments.Values(), err)
	}
	if contentLimit, err := ContainerFileContentLimit("/var/app/data/report.txt", 4096); err != nil || contentLimit != 1024 {
		t.Fatalf("ContainerFileContentLimit() = %d/%v", contentLimit, err)
	}
	if _, err := NewContainerFileRoots([]string{"/etc/kubernetes"}); err == nil {
		t.Fatal("NewContainerFileRoots accepted a code-denied credential root")
	}
	if _, err := NewContainerFileRoots([]string{"/var/app/secrets"}); err == nil {
		t.Fatal("NewContainerFileRoots accepted a credential-shaped application root")
	}
	if _, err := NormalizeContainerFilePath("/var/app/data/report\n.txt"); err == nil {
		t.Fatal("NormalizeContainerFilePath accepted a control-bearing path")
	}
}

func TestContainerFileActionPlanBindsNormalizedPathAndNoShellFlags(t *testing.T) {
	arguments, err := ContainerFileReaderArguments("/var/app/data/report.txt")
	if err != nil {
		t.Fatalf("ContainerFileReaderArguments() error = %v", err)
	}
	plan := remoteTestPlan(ActionOperationContainerFileRead, ActionParameters{
		Kind: ActionParametersContainerFile, Container: "app", NormalizedPath: "/var/app/data/report.txt",
		Executable: "/bin/tar", Arguments: arguments,
	})
	plan.Risk = RiskReview
	plan.DataCategories = ActionDataFileOutput
	plan.VerificationPlan = ContainerFileVerificationPlanID
	plan.RiskSummary = ContainerFileRiskSummary
	plan.Target.Fingerprint = string(plan.Parameters.Digest())
	plan.Limits.MaximumOutput, err = ContainerFileContentLimit(plan.Parameters.NormalizedPath, plan.Limits.MaximumBytes)
	if err != nil {
		t.Fatalf("ContainerFileContentLimit() error = %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("container-file plan validation = %v", err)
	}
	intent := ActionIntent{
		Operation: plan.Operation, OperationSchemaVersion: plan.Operation.SchemaVersion(), PolicyVersion: ActionPolicyVersion,
		PermissionProfile: PermissionProfileFullAccess, Risk: plan.Risk, Effect: plan.Effect,
		PolicyGeneration: plan.PolicyGeneration, Scope: plan.Scope.Snapshot(), NamespaceAccess: plan.Scope.NamespaceAccess,
		Target: plan.Target, Parameters: plan.Parameters, DataCategories: plan.DataCategories, AllowedSinks: plan.AllowedSinks,
		NetworkEffects: plan.NetworkEffects, NetworkDestinationHash: plan.NetworkDestinationHash, Limits: plan.Limits, VerificationPlanID: plan.VerificationPlan,
		ReasonSummary: plan.ReasonSummary, RiskSummary: plan.RiskSummary,
	}
	if intent.Validate() != nil || !plan.MatchesIntent(intent) || intent.Stdin || intent.TTY || intent.Shell {
		t.Fatalf("container-file intent = %#v", intent)
	}
	intent.Parameters.NormalizedPath = "/var/app/data/other.txt"
	if plan.MatchesIntent(intent) {
		t.Fatal("plan matched a changed container path")
	}
	changed := plan
	changed.Parameters.Executable = "/bin/sh"
	if changed.Validate() == nil {
		t.Fatal("container-file plan accepted a shell in place of the fixed reader")
	}
	changed = plan
	changed.NetworkDestinationHash = ActionDigest(repeatRemoteTest("0", 64))
	if changed.Validate() == nil {
		t.Fatal("container-file plan accepted a changed remote Pod destination")
	}
}

func TestDiagnosticPodCatalogAllowsDistinctTargetsWithTheSameCodeOwnedProbe(t *testing.T) {
	arguments, err := NewActionArguments([]string{"-z", "-v", "-w", "5"})
	if err != nil {
		t.Fatal(err)
	}
	image := "registry.example/diag@sha256:" + repeatRemoteTest("a", 64)
	policies := []DiagnosticPodPolicy{
		{ID: "api-connect", Namespace: "team-a", Image: image, Executable: "/bin/nc", ArgumentPrefix: arguments, ServiceName: "api", Port: 8443, NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096},
		{ID: "database-connect", Namespace: "team-a", Image: image, Executable: "/bin/nc", ArgumentPrefix: arguments, ServiceName: "database", Port: 5432, NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096},
	}
	catalog, err := NewRemoteDiagnosticsPolicyCatalog(nil, nil, policies)
	if err != nil || len(catalog.DiagnosticPodPolicies()) != 2 {
		t.Fatalf("multi-target diagnostic catalog = %#v/%v", catalog, err)
	}
	resolved, found := catalog.ResolveDiagnosticPod("database-connect")
	if !found || resolved.ServiceName != "database" || resolved.Port != 5432 {
		t.Fatalf("resolved diagnostic target = %#v/%t", resolved, found)
	}
}

func TestRemoteDiagnosticsCatalogAdmitsOnlyExactDiagnosticPolicy(t *testing.T) {
	prefix, err := NewActionArguments([]string{"-z", "-v", "-w", "5"})
	if err != nil {
		t.Fatal(err)
	}
	policy := DiagnosticPodPolicy{
		ID: "api-connect", Namespace: "team-a", Image: "registry.example/diag@sha256:" + repeatRemoteTest("a", 64),
		Executable: "/bin/nc", ArgumentPrefix: prefix, ServiceName: "api", Port: 8443,
		NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
	catalog, err := NewRemoteDiagnosticsPolicyCatalog(nil, nil, []DiagnosticPodPolicy{policy})
	if err != nil {
		t.Fatal(err)
	}
	executable, arguments, err := policy.Command()
	if err != nil {
		t.Fatal(err)
	}
	parameters := ActionParameters{Kind: ActionParametersRemoteArgv, Container: "diagnostic", Executable: executable, Arguments: arguments}
	target := ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: "team-a", Name: "api", UID: "service-uid", ResourceVersion: "21"}
	plan := RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000000201", SessionID: "00000000-0000-7000-8000-000000000202",
		Scope:            ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		PolicyGeneration: 3, Operation: ActionOperationDiagnosticPod,
		Target: ActionTarget{Resource: target, Fingerprint: string(policy.ActionFingerprint(parameters))}, Parameters: parameters,
		Risk: RiskCritical, Effect: CapabilityEffectClusterMutation, DataCategories: ActionDataContainerOutput | ActionDataResourceMetadata,
		AllowedSinks:   ActionSinkTerminal | ActionSinkModel | ActionSinkKubernetesAPI,
		NetworkEffects: ActionNetworkKubernetesAPI | ActionNetworkRemotePod, NetworkDestinationHash: DiagnosticPodNetworkDestinationHash(target, "api.team-a.svc", 8443),
		Limits:           ActionLimits{Timeout: 10 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		VerificationPlan: DiagnosticPodVerificationPlanID, ReasonSummary: "Test one exact configured target.", RiskSummary: DiagnosticPodRiskSummary,
	}
	if plan.Validate() != nil || !catalog.AdmitsAction(plan) {
		t.Fatalf("exact diagnostic plan was not admitted: %#v", plan)
	}
	changed := plan
	changed.Target.Fingerprint = SHA256Hex("another digest-pinned image")
	if changed.Validate() != nil || catalog.AdmitsAction(changed) {
		t.Fatal("catalog admitted a structurally valid plan with another image-policy fingerprint")
	}
	changed = plan
	changed.Limits.Timeout = 11 * time.Second
	if changed.Validate() != nil || catalog.AdmitsAction(changed) {
		t.Fatal("catalog admitted a plan above its configured timeout")
	}
}

func TestValidPinnedContainerImageRejectsURLAndCredentialShapedReferences(t *testing.T) {
	digest := "@sha256:" + repeatRemoteTest("a", 64)
	for _, value := range []string{
		"registry.example/diag" + digest,
		"registry.example:5000/team/diag:v1" + digest,
		"localhost:5000/diag:Release_1" + digest,
	} {
		if !ValidPinnedContainerImage(value) {
			t.Fatalf("ValidPinnedContainerImage(%q) = false", value)
		}
	}
	for _, value := range []string{
		"https://registry.example/diag" + digest,
		"user:password@registry.example/diag" + digest,
		"registry.example/diag?token=value" + digest,
		"registry.example/team//diag" + digest,
		"registry.example:password/diag" + digest,
		"Registry.example/diag" + digest,
		"registry.example/diag@sha256:" + repeatRemoteTest("g", 64),
	} {
		if ValidPinnedContainerImage(value) {
			t.Fatalf("ValidPinnedContainerImage(%q) = true", value)
		}
	}
}

func TestRemoteDiagnosticTargetRejectsObviousMetadataAndAddressConfusion(t *testing.T) {
	for _, value := range []string{
		"metadata.team-a.svc",
		"metadata-proxy.team-a.svc",
		"link-local.team-a.svc",
		"localhost-proxy.team-a.svc",
		"127-0-0-1.team-a.svc",
		"169-254-169-254.team-a.svc",
		"0-0-0-0.team-a.svc",
		"169.254.169.254",
		"api.example.com",
	} {
		if ValidRemoteDiagnosticTarget(value) {
			t.Fatalf("ValidRemoteDiagnosticTarget(%q) = true", value)
		}
	}
	if !ValidRemoteDiagnosticTarget("api.team-a.svc") {
		t.Fatal("ValidRemoteDiagnosticTarget rejected an exact same-Namespace Service")
	}
}

func remoteTestPlan(operation ActionOperation, parameters ActionParameters) RemoteDiagnosticActionPlan {
	scope := ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	plan := RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000000101", SessionID: "00000000-0000-7000-8000-000000000102",
		Scope: scope, PolicyGeneration: 3, Operation: operation,
		Target:     ActionTarget{Resource: ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "app-0", UID: "pod-uid", ResourceVersion: "10"}, Subresource: "exec"},
		Parameters: parameters, Risk: RiskCritical, Effect: CapabilityEffectRemoteExecute,
		DataCategories: ActionDataContainerOutput, AllowedSinks: ActionSinkTerminal | ActionSinkModel | ActionSinkKubernetesAPI,
		NetworkEffects:   ActionNetworkKubernetesAPI | ActionNetworkRemotePod,
		Limits:           ActionLimits{Timeout: 5 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		VerificationPlan: PodExecVerificationPlanID, ReasonSummary: "Inspect one bounded target.", RiskSummary: PodExecRiskSummary,
	}
	plan.NetworkDestinationHash = RemotePodNetworkDestinationHash(plan.Target.Resource, parameters.Container)
	return plan
}

func repeatRemoteTest(value string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}
