package domain

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestActionEnvelopeCanonicalDigestBindsEveryField(t *testing.T) {
	base := testActionEnvelope(t)
	canonical, err := CanonicalAction(base)
	if err != nil || len(canonical) == 0 || base.Validate() != nil {
		t.Fatalf("canonical/validation = %d/%v/%v", len(canonical), err, base.Validate())
	}
	if strings.Contains(string(canonical), "{") || strings.Contains(string(canonical), "}") {
		t.Fatal("canonical action unexpectedly uses a JSON payload")
	}

	tests := []struct {
		name   string
		mutate func(*ActionEnvelope)
	}{
		{"schema", func(value *ActionEnvelope) { value.SchemaVersion = "other" }},
		{"request", func(value *ActionEnvelope) { value.RequestID = "00000000-0000-7000-8000-000000000011" }},
		{"session", func(value *ActionEnvelope) { value.SessionID = "00000000-0000-7000-8000-000000000012" }},
		{"run", func(value *ActionEnvelope) { value.RunID = "00000000-0000-7000-8000-000000000013" }},
		{"operation", func(value *ActionEnvelope) {
			value.Intent.Operation = ActionOperationScaleWorkload
			value.Intent.OperationSchemaVersion = value.Intent.Operation.SchemaVersion()
		}},
		{"operation schema", func(value *ActionEnvelope) { value.Intent.OperationSchemaVersion = "other/v1" }},
		{"policy version", func(value *ActionEnvelope) { value.Intent.PolicyVersion = "other-policy" }},
		{"profile", func(value *ActionEnvelope) { value.Intent.PermissionProfile = PermissionProfileAutoReview }},
		{"risk", func(value *ActionEnvelope) { value.Intent.Risk = RiskCritical }},
		{"effect", func(value *ActionEnvelope) { value.Intent.Effect = CapabilityEffectRemoteExecute }},
		{"policy generation", func(value *ActionEnvelope) { value.Intent.PolicyGeneration++ }},
		{"scope context", func(value *ActionEnvelope) { value.Intent.Scope.Context = "other-context" }},
		{"scope namespace", func(value *ActionEnvelope) { value.Intent.Scope.Namespace = "other-namespace" }},
		{"namespace access", func(value *ActionEnvelope) { value.Intent.NamespaceAccess = NamespaceAccessAll }},
		{"scope generation", func(value *ActionEnvelope) { value.Intent.Scope.Generation++ }},
		{"target api version", func(value *ActionEnvelope) { value.Intent.Target.Resource.APIVersion = "apps/v2" }},
		{"target kind", func(value *ActionEnvelope) { value.Intent.Target.Resource.Kind = "StatefulSet" }},
		{"target namespace", func(value *ActionEnvelope) { value.Intent.Target.Resource.Namespace = "other-namespace" }},
		{"target name", func(value *ActionEnvelope) { value.Intent.Target.Resource.Name = "other-name" }},
		{"target uid", func(value *ActionEnvelope) { value.Intent.Target.Resource.UID = "other-uid" }},
		{"resource version", func(value *ActionEnvelope) { value.Intent.Target.Resource.ResourceVersion = "other-version" }},
		{"subresource", func(value *ActionEnvelope) { value.Intent.Target.Subresource = "exec" }},
		{"fingerprint", func(value *ActionEnvelope) { value.Intent.Target.Fingerprint = strings.Repeat("b", 64) }},
		{"target generation", func(value *ActionEnvelope) { value.Intent.Target.Generation++ }},
		{"target revision", func(value *ActionEnvelope) { value.Intent.Target.Revision = 2 }},
		{"target set", func(value *ActionEnvelope) {
			value.Intent.Target.TargetSetDigest = ActionDigest(strings.Repeat("b", 64))
			value.Intent.Target.TargetCount = 1
		}},
		{"parameters", func(value *ActionEnvelope) {
			value.Intent.Parameters = ActionParameters{Kind: ActionParametersNodeScheduling, Unschedulable: true}
		}},
		{"stdin", func(value *ActionEnvelope) { value.Intent.Stdin = true }},
		{"tty", func(value *ActionEnvelope) { value.Intent.Stdin, value.Intent.TTY = true, true }},
		{"shell", func(value *ActionEnvelope) { value.Intent.Shell = true }},
		{"data", func(value *ActionEnvelope) { value.Intent.DataCategories |= ActionDataProjectedStatus }},
		{"sink", func(value *ActionEnvelope) { value.Intent.AllowedSinks |= ActionSinkTerminal }},
		{"network", func(value *ActionEnvelope) { value.Intent.NetworkEffects = ActionNetworkNone }},
		{"network destination", func(value *ActionEnvelope) {
			value.Intent.NetworkEffects = ActionNetworkDataSource
			value.Intent.NetworkDestinationHash = ActionDigest(strings.Repeat("c", 64))
		}},
		{"timeout", func(value *ActionEnvelope) { value.Intent.Limits.Timeout += time.Millisecond }},
		{"items", func(value *ActionEnvelope) { value.Intent.Limits.MaximumItems++ }},
		{"lines", func(value *ActionEnvelope) { value.Intent.Limits.MaximumLines++ }},
		{"bytes", func(value *ActionEnvelope) { value.Intent.Limits.MaximumBytes++ }},
		{"output", func(value *ActionEnvelope) { value.Intent.Limits.MaximumOutput++ }},
		{"verification", func(value *ActionEnvelope) { value.Intent.VerificationPlanID = "restart-rollout/v2" }},
		{"reason", func(value *ActionEnvelope) { value.Intent.ReasonSummary = "Different explicit intent." }},
		{"risk summary", func(value *ActionEnvelope) { value.Intent.RiskSummary = "Different fixed risk." }},
		{"requested", func(value *ActionEnvelope) {
			value.RequestedAt = value.RequestedAt.Add(time.Millisecond)
			value.ExpiresAt = value.ExpiresAt.Add(time.Millisecond)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("mutated envelope retained digest authority")
			}
			if changed.validateUnsigned() == nil {
				changedCanonical, err := CanonicalAction(changed)
				if err != nil || bytes.Equal(changedCanonical, canonical) {
					t.Fatalf("mutated canonical bytes = %q/%v", changedCanonical, err)
				}
			}
		})
	}
}

func TestActionEnvelopeCanonicalDigestFixedVector(t *testing.T) {
	t.Parallel()

	const want ActionDigest = "97c240d562d2aa92f7c070be441b4e3fcf9a7403c60a353ea8883ca274ac99f6"
	envelope := testActionEnvelope(t)
	canonical, err := CanonicalAction(envelope)
	if err != nil {
		t.Fatalf("CanonicalAction() error = %v", err)
	}
	if got := ActionDigest(SHA256Hex(string(canonical))); got != want || envelope.Digest != want {
		t.Fatalf("canonical digest vector = bytes %q/envelope %q, want %q", got, envelope.Digest, want)
	}
}

func TestActionIntentRequiresExactExternalNetworkDestination(t *testing.T) {
	base := testActionEnvelope(t).Intent
	base.Operation = ActionOperationPrometheusQuery
	base.OperationSchemaVersion = base.Operation.SchemaVersion()
	base.Risk = RiskReview
	base.Effect = CapabilityEffectNetworkEgress
	base.NetworkEffects = ActionNetworkDataSource
	base.NetworkDestinationHash = ""
	if base.Validate() == nil {
		t.Fatal("external data-source action accepted without an exact destination hash")
	}
	base.NetworkDestinationHash = ActionDigest(strings.Repeat("d", 64))
	if base.Validate() != nil {
		t.Fatalf("external data-source action rejected with destination hash: %v", base.Validate())
	}
	base.NetworkEffects = ActionNetworkKubernetesAPI
	if base.Validate() == nil {
		t.Fatal("Kubernetes-only action accepted an unrelated destination hash")
	}
}

func TestActionParametersAreClosedAndArgvIsCopied(t *testing.T) {
	argv := []string{"get", "pods"}
	arguments, err := NewActionArguments(argv)
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	argv[0] = "delete"
	values := arguments.Values()
	if !reflect.DeepEqual(values, []string{"get", "pods"}) {
		t.Fatalf("copied argv = %#v", values)
	}
	values[0] = "patch"
	if got := arguments.Values(); got[0] != "get" {
		t.Fatalf("returned argv aliases authority = %#v", got)
	}
	parameters := ActionParameters{
		Kind: ActionParametersLocalArgv, Executable: "kubectl", Arguments: arguments,
	}
	if parameters.Validate() != nil || !parameters.Digest().Valid() {
		t.Fatalf("parameters validation/digest = %v/%q", parameters.Validate(), parameters.Digest())
	}
	parameters.Container = "smuggled"
	if parameters.Validate() == nil {
		t.Fatal("local argv accepted a remote-container field")
	}
	if _, err := NewActionArguments([]string{"sh", "-c", "echo permitted only by later exact policy"}); err != nil {
		t.Fatalf("argv structure must not guess future executable policy: %v", err)
	}
}

func TestActionSubresourceIsAnExactCodeOwnedToken(t *testing.T) {
	for _, value := range []string{"", "exec", "ephemeralcontainers", "proxy-v1"} {
		if !ValidActionSubresource(value) {
			t.Fatalf("ValidActionSubresource(%q) = false", value)
		}
	}
	for _, value := range []string{"Exec", "pods/exec", "-exec", "exec-", "exec_query", "exec?token=value"} {
		if ValidActionSubresource(value) {
			t.Fatalf("ValidActionSubresource(%q) = true", value)
		}
	}
}

func testActionEnvelope(t *testing.T) ActionEnvelope {
	t.Helper()
	requestedAt := time.UnixMilli(1_700_000_000_000).UTC()
	intent := ActionIntent{
		Operation: ActionOperationRestartDeployment, OperationSchemaVersion: ActionOperationRestartDeployment.SchemaVersion(),
		PolicyVersion: ActionPolicyVersion, PermissionProfile: PermissionProfileAsk,
		Risk: RiskReview, Effect: CapabilityEffectClusterMutation, PolicyGeneration: 1,
		Scope:           ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		NamespaceAccess: NamespaceAccessCurrent,
		Target: ActionTarget{
			Resource:    ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "test-namespace", Name: "sample", UID: "deployment-uid", ResourceVersion: "42"},
			Fingerprint: strings.Repeat("a", 64), Generation: 3,
		},
		Parameters:     ActionParameters{Kind: ActionParametersNone},
		DataCategories: ActionDataResourceMetadata, AllowedSinks: ActionSinkKubernetesAPI,
		NetworkEffects:     ActionNetworkKubernetesAPI,
		Limits:             ActionLimits{Timeout: 30 * time.Second, MaximumItems: 1},
		VerificationPlanID: "restart-rollout/v1",
		ReasonSummary:      "Restart one exact Deployment.",
		RiskSummary:        "Restarting the Deployment replaces Pods and may temporarily reduce availability.",
	}
	envelope, err := NewActionEnvelope(
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003",
		intent,
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewActionEnvelope() error = %v", err)
	}
	return envelope
}
