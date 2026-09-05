package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRemoteDiagnosticGateDurablyConsumesAutomaticActionBeforeReturningAuthority(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
	if err != nil || envelope.Validate() != nil || !plan.MatchesIntent(envelope.Intent) {
		t.Fatalf("AuthorizeAndConsume() = %#v/%v", envelope, err)
	}
	if fixture.persistence.creates != 1 || fixture.persistence.resolves != 1 || fixture.persistence.consumes != 1 ||
		fixture.revalidator.calls != 1 || fixture.persistence.lastConsumed.State != domain.ApprovalStateConsumed || fixture.persistence.lastConsumeAudit.Type != domain.AuditEventWriteIntent {
		t.Fatalf("durable lifecycle creates/resolves/consumes=%d/%d/%d consumed=%#v audit=%#v", fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.consumes, fixture.persistence.lastConsumed, fixture.persistence.lastConsumeAudit)
	}
	if envelope.Intent.Stdin || envelope.Intent.TTY || envelope.Intent.Shell || envelope.Intent.Parameters.Executable != "/usr/bin/printf" ||
		!equalRemoteGateArguments(envelope.Intent.Parameters.Arguments.Values(), []string{"literal;not-a-shell", "$(ignored)"}) {
		t.Fatalf("normalized envelope = %#v", envelope.Intent)
	}
	outcome := domain.RemoteDiagnosticOutcome{State: domain.RemoteDiagnosticOutcomeSucceeded, OutputBytes: 12, OutputLines: 1, CleanupState: domain.DiagnosticPodCleanupNotNeeded}
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err != nil {
		t.Fatalf("RecordOutcome() error = %v", err)
	}
	if len(fixture.persistence.writeResultAudits) != 1 {
		t.Fatalf("write-result audits = %#v", fixture.persistence.writeResultAudits)
	}
	audit := fixture.persistence.writeResultAudits[0]
	if audit.IntegrityHash != string(envelope.Digest) || audit.CorrelationID != string(envelope.RequestID) || audit.Subject == nil || *audit.Subject != plan.Target.Resource ||
		audit.Details.Operation == nil || *audit.Details.Operation != string(plan.Operation) || strings.Contains(fmt.Sprintf("%#v", audit), "literal output") {
		t.Fatalf("outcome audit = %#v", audit)
	}
	tooLarge := outcome
	tooLarge.OutputBytes = envelope.Intent.Limits.MaximumOutput + 1
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, tooLarge); err == nil || len(fixture.persistence.writeResultAudits) != 1 {
		t.Fatalf("RecordOutcome(over limit) error/audits = %v/%d", err, len(fixture.persistence.writeResultAudits))
	}
}

func TestRemoteDiagnosticGatePersistsDiagnosticPodPhasesAsOneAuditSet(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	plan := remoteDiagnosticPodGatePlan(t)
	envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
	if err != nil {
		t.Fatalf("AuthorizeAndConsume() error = %v", err)
	}
	outcome := domain.RemoteDiagnosticOutcome{
		State:       domain.RemoteDiagnosticOutcomeSucceeded,
		OutputBytes: 12,
		OutputLines: 1,
		Lifecycle: domain.DiagnosticPodLifecycle{
			Create: domain.DiagnosticPodPhaseCompleted,
			Wait:   domain.DiagnosticPodPhaseCompleted,
			Log:    domain.DiagnosticPodPhaseCompleted,
			Delete: domain.DiagnosticPodPhaseCompleted,
		},
		CleanupState: domain.DiagnosticPodCleanupVerified,
	}
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err != nil {
		t.Fatalf("RecordOutcome() error = %v", err)
	}
	if fixture.persistence.writeResultCalls != 1 || len(fixture.persistence.writeResultAudits) != 5 {
		t.Fatalf("atomic audit calls/events = %d/%d", fixture.persistence.writeResultCalls, len(fixture.persistence.writeResultAudits))
	}
	wantDetails := []string{
		"diagnostic_create_completed",
		"diagnostic_wait_completed",
		"diagnostic_log_completed",
		"diagnostic_delete_completed",
		"diagnostic_cleanup_completed",
	}
	for index, event := range fixture.persistence.writeResultAudits {
		if event.Details.Sequence == nil || *event.Details.Sequence != index+1 || event.Details.DetailCode == nil || *event.Details.DetailCode != wantDetails[index] ||
			event.CorrelationID != string(envelope.RequestID) || event.IntegrityHash != string(envelope.Digest) {
			t.Fatalf("phase audit[%d] = %#v", index, event)
		}
		if index == 2 {
			if event.Details.Count == nil || *event.Details.Count != 12 {
				t.Fatalf("log phase count = %#v", event.Details.Count)
			}
		} else if event.Details.Count != nil {
			t.Fatalf("non-log phase count = %#v", event.Details.Count)
		}
	}
}

func TestRemoteDiagnosticGateAuditsBoundedContainerFileArchiveBytes(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	plan := remoteDiagnosticContainerFileGatePlan(t)
	envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
	if err != nil {
		t.Fatalf("AuthorizeAndConsume() error = %v", err)
	}
	if envelope.Intent.Limits.MaximumOutput >= envelope.Intent.Limits.MaximumBytes {
		t.Fatalf("container file output/archive limits = %d/%d", envelope.Intent.Limits.MaximumOutput, envelope.Intent.Limits.MaximumBytes)
	}
	outcome := domain.RemoteDiagnosticOutcome{
		State:        domain.RemoteDiagnosticOutcomeFailed,
		ErrorClass:   domain.SafeErrorClassInvalidInput,
		OutputBytes:  envelope.Intent.Limits.MaximumOutput + 1,
		OutputLines:  1,
		CleanupState: domain.DiagnosticPodCleanupNotNeeded,
	}
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err != nil {
		t.Fatalf("RecordOutcome(raw archive bytes) error = %v", err)
	}
	if fixture.persistence.writeResultCalls != 1 || len(fixture.persistence.writeResultAudits) != 1 {
		t.Fatalf("raw archive outcome audits = calls %d events %#v", fixture.persistence.writeResultCalls, fixture.persistence.writeResultAudits)
	}
	outcome.State = domain.RemoteDiagnosticOutcomeSucceeded
	outcome.ErrorClass = ""
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err == nil || fixture.persistence.writeResultCalls != 1 {
		t.Fatalf("RecordOutcome(over content limit success) error/calls = %v/%d", err, fixture.persistence.writeResultCalls)
	}
	outcome.State = domain.RemoteDiagnosticOutcomeFailed
	outcome.ErrorClass = domain.SafeErrorClassInvalidInput
	outcome.OutputBytes = envelope.Intent.Limits.MaximumBytes + 1
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err == nil || fixture.persistence.writeResultCalls != 1 {
		t.Fatalf("RecordOutcome(over archive limit) error/calls = %v/%d", err, fixture.persistence.writeResultCalls)
	}
}

func TestRemoteDiagnosticGateRecordsOutcomeAfterOperationCancellation(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
	if err != nil {
		t.Fatalf("AuthorizeAndConsume() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := domain.RemoteDiagnosticOutcome{State: domain.RemoteDiagnosticOutcomeFailed, ErrorClass: domain.SafeErrorClassCancelled, CleanupState: domain.DiagnosticPodCleanupNotNeeded}
	if err := fixture.gate.RecordOutcome(cancelled, envelope, outcome); err != nil {
		t.Fatalf("RecordOutcome(cancelled) error = %v", err)
	}
	if fixture.persistence.writeResultCalls != 1 || len(fixture.persistence.writeResultAudits) != 1 || fixture.persistence.writeResultAudits[0].Outcome != domain.AuditOutcomeFailure {
		t.Fatalf("cancelled outcome audits = calls %d events %#v", fixture.persistence.writeResultCalls, fixture.persistence.writeResultAudits)
	}
}

func TestRemoteDiagnosticGatePermissionRoutesFailClosedWithoutDurableAuthority(t *testing.T) {
	predefined := remoteDiagnosticGatePlan(t, domain.ActionOperationPodDiagnostic, domain.RiskReview)
	predefined.RiskSummary = domain.PodDiagnosticRiskSummary
	for _, test := range []struct {
		name   string
		policy PermissionPolicy
		plan   domain.RemoteDiagnosticActionPlan
	}{
		{name: "read-only denies remote execution", policy: PermissionPolicy{Profile: domain.PermissionProfileReadOnly, Generation: 3}, plan: predefined},
		{name: "ask routes general exec to human", policy: PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, plan: remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)},
		{name: "auto-review delegates predefined review", policy: PermissionPolicy{Profile: domain.PermissionProfileAutoReview, Generation: 3}, plan: predefined},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRemoteDiagnosticGateFixture(t, test.policy)
			if _, err := fixture.gate.AuthorizeAndConsume(context.Background(), test.plan); err == nil {
				t.Fatal("AuthorizeAndConsume() unexpectedly returned execution authority")
			}
			if fixture.persistence.creates != 0 || fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 {
				t.Fatalf("denied durable writes = %d/%d/%d", fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.consumes)
			}
		})
	}
}

func TestRemoteDiagnosticGatePersistenceAndStaleGenerationFailuresReturnNoEnvelope(t *testing.T) {
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	fixture.persistence.createErr = errors.New("synthetic persistence failure")
	if envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan); err == nil || envelope != (domain.ActionEnvelope{}) || fixture.persistence.consumes != 0 {
		t.Fatalf("persistence failure envelope/error/consumes = %#v/%v/%d", envelope, err, fixture.persistence.consumes)
	}

	stale := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 4, FullAccessAllowed: true, HighRiskAcknowledged: true})
	if envelope, err := stale.gate.AuthorizeAndConsume(context.Background(), plan); err == nil || envelope != (domain.ActionEnvelope{}) || stale.persistence.creates != 0 {
		t.Fatalf("stale generation envelope/error/creates = %#v/%v/%d", envelope, err, stale.persistence.creates)
	}
}

func TestRemoteDiagnosticGateRejectsValidButUnconfiguredActionBeforeApproval(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	arguments, err := domain.NewActionArguments([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	plan.Parameters.Executable = "/usr/bin/id"
	plan.Parameters.Arguments = arguments
	plan.Target.Fingerprint = string(plan.Parameters.Digest())
	if plan.Validate() != nil {
		t.Fatal("unconfigured plan must remain structurally valid")
	}
	if envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan); err == nil || envelope != (domain.ActionEnvelope{}) {
		t.Fatalf("unconfigured action envelope/error = %#v/%v", envelope, err)
	}
	if fixture.persistence.creates != 0 || fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 || fixture.revalidator.calls != 0 {
		t.Fatalf("unconfigured action writes/revalidation = %d/%d/%d/%d", fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.consumes, fixture.revalidator.calls)
	}
}

func TestRemoteDiagnosticGateRevalidatesTargetBeforeDurableConsumption(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	fixture.revalidator.err = remoteGateError{class: domain.SafeErrorClassConflict}
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	if envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan); err == nil || envelope != (domain.ActionEnvelope{}) {
		t.Fatalf("target conflict envelope/error = %#v/%v", envelope, err)
	}
	if fixture.revalidator.calls != 1 || fixture.persistence.creates != 1 || fixture.persistence.resolves != 1 ||
		fixture.persistence.closes != 1 || fixture.persistence.consumes != 0 || fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonTargetChanged {
		t.Fatalf("revalidation lifecycle calls=%d creates/resolves/closes/consumes=%d/%d/%d/%d closed=%#v", fixture.revalidator.calls, fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes, fixture.persistence.consumes, fixture.persistence.lastClosed)
	}
}

func TestRemoteDiagnosticGateDurablyClosesApprovedRequestWhenScopeTurnsStale(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	fixture.persistence.afterResolve = func() {
		fixture.scope.scope.Generation++
	}
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	if envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan); err == nil || envelope != (domain.ActionEnvelope{}) {
		t.Fatalf("stale scope envelope/error = %#v/%v", envelope, err)
	}
	if fixture.persistence.creates != 1 || fixture.persistence.resolves != 1 || fixture.persistence.closes != 1 || fixture.persistence.consumes != 0 ||
		fixture.persistence.lastCloseExpected != domain.ApprovalStateApproved || fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonScopeChanged {
		t.Fatalf("stale durable lifecycle creates/resolves/closes/consumes=%d/%d/%d/%d expected=%s closed=%#v", fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes, fixture.persistence.consumes, fixture.persistence.lastCloseExpected, fixture.persistence.lastClosed)
	}
}

func TestRemoteDiagnosticGateReturnsConsumedEnvelopeForAuditedFinalGenerationFailure(t *testing.T) {
	fixture := newRemoteDiagnosticGateFixture(t, PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true})
	fixture.persistence.afterConsume = func() {
		fixture.permissions.mu.Lock()
		fixture.permissions.policy.Generation++
		fixture.permissions.mu.Unlock()
	}
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
	if err == nil || envelope.Validate() != nil || !plan.MatchesIntent(envelope.Intent) || fixture.persistence.consumes != 1 {
		t.Fatalf("final generation envelope/error/consumes = %#v/%v/%d", envelope, err, fixture.persistence.consumes)
	}
	var classified interface{ Class() domain.SafeErrorClass }
	if !errors.As(err, &classified) || classified.Class() != domain.SafeErrorClassStaleScope {
		t.Fatalf("final generation error = %v", err)
	}
	outcome := domain.RemoteDiagnosticOutcome{State: domain.RemoteDiagnosticOutcomeFailed, ErrorClass: domain.SafeErrorClassStaleScope, CleanupState: domain.DiagnosticPodCleanupNotNeeded}
	if err := fixture.gate.RecordOutcome(context.Background(), envelope, outcome); err != nil || fixture.persistence.writeResultCalls != 1 {
		t.Fatalf("RecordOutcome(final generation) error/calls = %v/%d", err, fixture.persistence.writeResultCalls)
	}
}

type remoteDiagnosticGateFixture struct {
	gate        *RemoteDiagnosticActionGate
	persistence *fakeApprovalPersistence
	scope       *fakeApprovalCurrentScope
	permissions *PermissionManager
	revalidator *fakeRemoteDiagnosticRevalidator
}

func newRemoteDiagnosticGateFixture(t *testing.T, policy PermissionPolicy) remoteDiagnosticGateFixture {
	t.Helper()
	clock := &approvalCoordinatorClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	persistence := &fakeApprovalPersistence{}
	identifiers := &approvalCoordinatorIDs{}
	service, err := approval.NewService(approval.ServiceConfig{Clock: clock, Nonces: approvalNonceSource{value: 0x72}, Store: persistence, AuditIDs: identifiers})
	if err != nil {
		t.Fatalf("approval.NewService() error = %v", err)
	}
	permissions, err := NewPermissionManager(policy)
	if err != nil {
		t.Fatalf("NewPermissionManager() error = %v", err)
	}
	if err := permissions.BindSession("00000000-0000-7000-8000-000000078002"); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	scope := &fakeApprovalCurrentScope{scope: remoteDiagnosticGateScope()}
	revalidator := &fakeRemoteDiagnosticRevalidator{}
	gate, err := NewRemoteDiagnosticActionGate(RemoteDiagnosticActionGateConfig{
		Service: service, Persistence: persistence, ResultAudits: persistence, Scope: scope,
		Revalidator: revalidator, Identifiers: identifiers, Permissions: permissions,
		PolicyCatalog: remoteDiagnosticGatePolicyCatalog(t), Now: clock.Now, PersistenceTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRemoteDiagnosticActionGate() error = %v", err)
	}
	return remoteDiagnosticGateFixture{gate: gate, persistence: persistence, scope: scope, permissions: permissions, revalidator: revalidator}
}

type fakeRemoteDiagnosticRevalidator struct {
	calls int
	err   error
}

func (validator *fakeRemoteDiagnosticRevalidator) RevalidateRemoteDiagnosticAction(context.Context, domain.RemoteDiagnosticActionPlan) error {
	validator.calls++
	return validator.err
}

func remoteDiagnosticGatePlan(t *testing.T, operation domain.ActionOperation, risk domain.RiskClass) domain.RemoteDiagnosticActionPlan {
	t.Helper()
	executable := "/usr/bin/printf"
	argumentValues := []string{"literal;not-a-shell", "$(ignored)"}
	if operation == domain.ActionOperationPodDiagnostic {
		executable = "/bin/cat"
		argumentValues = []string{"/etc/resolv.conf"}
	}
	arguments, err := domain.NewActionArguments(argumentValues)
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersRemoteArgv, Container: "app", Executable: executable, Arguments: arguments}
	plan := domain.RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000078001", SessionID: "00000000-0000-7000-8000-000000078002",
		Scope: remoteDiagnosticGateScope(), PolicyGeneration: 3, Operation: operation,
		Target:     domain.ActionTarget{Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "20"}, Subresource: "exec", Fingerprint: string(parameters.Digest())},
		Parameters: parameters, Risk: risk, Effect: domain.CapabilityEffectRemoteExecute, DataCategories: domain.ActionDataContainerOutput,
		AllowedSinks:     domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects:   domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		Limits:           domain.ActionLimits{Timeout: 5 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		VerificationPlan: domain.PodExecVerificationPlanID, ReasonSummary: "Inspect exact argv output.", RiskSummary: domain.PodExecRiskSummary,
	}
	plan.NetworkDestinationHash = domain.RemotePodNetworkDestinationHash(plan.Target.Resource, parameters.Container)
	return plan
}

func remoteDiagnosticPodGatePlan(t *testing.T) domain.RemoteDiagnosticActionPlan {
	t.Helper()
	arguments, err := domain.NewActionArguments([]string{"-z", "-v", "-w", "5", "api.team-a.svc", "8443"})
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersRemoteArgv, Container: "diagnostic", Executable: "/bin/nc", Arguments: arguments}
	plan := domain.RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000078001", SessionID: "00000000-0000-7000-8000-000000078002",
		Scope: remoteDiagnosticGateScope(), PolicyGeneration: 3, Operation: domain.ActionOperationDiagnosticPod,
		Target:     domain.ActionTarget{Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: "team-a", Name: "api", UID: "service-uid", ResourceVersion: "21"}},
		Parameters: parameters, Risk: domain.RiskCritical, Effect: domain.CapabilityEffectClusterMutation,
		DataCategories:   domain.ActionDataContainerOutput | domain.ActionDataResourceMetadata,
		AllowedSinks:     domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects:   domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		Limits:           domain.ActionLimits{Timeout: 10 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		VerificationPlan: domain.DiagnosticPodVerificationPlanID, ReasonSummary: "Test one configured Service port.", RiskSummary: domain.DiagnosticPodRiskSummary,
	}
	plan.NetworkDestinationHash = domain.DiagnosticPodNetworkDestinationHash(plan.Target.Resource, "api.team-a.svc", 8443)
	policy, found := remoteDiagnosticGatePolicyCatalog(t).ResolveDiagnosticPod("tcp-connect")
	if !found {
		t.Fatal("diagnostic test policy is absent")
	}
	plan.Target.Fingerprint = string(policy.ActionFingerprint(parameters))
	return plan
}

func remoteDiagnosticGatePolicyCatalog(t *testing.T) domain.RemoteDiagnosticsPolicyCatalog {
	t.Helper()
	literalArguments, err := domain.NewActionArguments([]string{"literal;not-a-shell", "$(ignored)"})
	if err != nil {
		t.Fatal(err)
	}
	diagnosticArguments, err := domain.NewActionArguments([]string{"/etc/resolv.conf"})
	if err != nil {
		t.Fatal(err)
	}
	probeArguments, err := domain.NewActionArguments([]string{"-z", "-v", "-w", "5"})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := domain.NewContainerFileRoots([]string{"/var/log"})
	if err != nil {
		t.Fatal(err)
	}
	filePolicy := domain.ContainerFilePolicy{Enabled: true, ReaderExecutable: "/bin/tar", AllowedRoots: roots, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096}
	catalog, err := domain.NewRemoteDiagnosticsPolicyCatalog(
		[]domain.PodExecPolicy{
			{ID: "literal", Class: domain.PodExecPolicyGeneral, Executable: "/usr/bin/printf", Arguments: literalArguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096},
			{ID: "dns-config", Class: domain.PodExecPolicyPredefined, Executable: "/bin/cat", Arguments: diagnosticArguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096},
		},
		&filePolicy,
		[]domain.DiagnosticPodPolicy{{ID: "tcp-connect", Namespace: "team-a", Image: "registry.example/diag@sha256:" + strings.Repeat("a", 64), Executable: "/bin/nc", ArgumentPrefix: probeArguments, ServiceName: "api", Port: 8443, NetworkPolicyRequired: true, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096}},
	)
	if err != nil {
		t.Fatalf("NewRemoteDiagnosticsPolicyCatalog() error = %v", err)
	}
	return catalog
}

func remoteDiagnosticContainerFileGatePlan(t *testing.T) domain.RemoteDiagnosticActionPlan {
	t.Helper()
	const path = "/var/log/app.log"
	arguments, err := domain.ContainerFileReaderArguments(path)
	if err != nil {
		t.Fatalf("ContainerFileReaderArguments() error = %v", err)
	}
	parameters := domain.ActionParameters{
		Kind: domain.ActionParametersContainerFile, Container: "app", NormalizedPath: path,
		Executable: "/bin/tar", Arguments: arguments,
	}
	const archiveMaximum = 4096
	contentMaximum, err := domain.ContainerFileContentLimit(path, archiveMaximum)
	if err != nil {
		t.Fatalf("ContainerFileContentLimit() error = %v", err)
	}
	plan := domain.RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000078001", SessionID: "00000000-0000-7000-8000-000000078002",
		Scope: remoteDiagnosticGateScope(), PolicyGeneration: 3, Operation: domain.ActionOperationContainerFileRead,
		Target:     domain.ActionTarget{Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "20"}, Subresource: "exec", Fingerprint: string(parameters.Digest())},
		Parameters: parameters, Risk: domain.RiskReview, Effect: domain.CapabilityEffectRemoteExecute, DataCategories: domain.ActionDataFileOutput,
		AllowedSinks:     domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects:   domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		Limits:           domain.ActionLimits{Timeout: 5 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: archiveMaximum, MaximumOutput: contentMaximum},
		VerificationPlan: domain.ContainerFileVerificationPlanID, ReasonSummary: "Read one exact bounded container file.", RiskSummary: domain.ContainerFileRiskSummary,
	}
	plan.NetworkDestinationHash = domain.RemotePodNetworkDestinationHash(plan.Target.Resource, parameters.Container)
	return plan
}

func remoteDiagnosticGateScope() domain.ClusterScope {
	return domain.ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.Date(2026, 1, 2, 3, 4, 4, 0, time.UTC)}
}

func equalRemoteGateArguments(left, right []string) bool {
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
