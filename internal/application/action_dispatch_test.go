package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestSharedDispatcherExecutesEveryTypedRemediationOnlyAfterDurableConsume(t *testing.T) {
	operations := []domain.ActionOperation{
		domain.ActionOperationScaleWorkload,
		domain.ActionOperationRollbackDeployment,
		domain.ActionOperationDeleteOwnedPod,
		domain.ActionOperationCordonNode,
		domain.ActionOperationUncordonNode,
		domain.ActionOperationDrainNode,
	}
	for _, operation := range operations {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			plan := dispatchedRemediationPlan(t, fixture, operation)
			executor := &fakeDispatchedRemediation{verification: domain.RemediationVerified}
			executor.beforeExecute = func() {
				if fixture.persistence.consumes != 1 || fixture.persistence.lastConsumeAudit.Type != domain.AuditEventWriteIntent {
					t.Fatal("executor was reached before durable consume and pre-operation audit")
				}
			}
			fixture.coordinator.remediation = executor
			request, err := fixture.coordinator.SubmitRemediationAction(
				context.Background(), 21, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}, plan,
			)
			if err != nil || request.State != domain.ApprovalStatePending || executor.totalCalls() != 0 {
				t.Fatalf("SubmitRemediationAction() = %#v/%v calls=%d", request, err, executor.totalCalls())
			}
			approved, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 21, 101))
			if err != nil || approved.State != domain.ApprovalStateApproved || executor.totalCalls() != 0 {
				t.Fatalf("Decide() = %#v/%v calls=%d", approved, err, executor.totalCalls())
			}
			result, err := fixture.coordinator.ConsumeApprovedAction(
				context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 21, 102),
			)
			if err != nil || result.Validate() != nil || result.State != domain.ApprovalStateConsumed ||
				result.ActionExecution == nil || result.ActionExecution.Remediation == nil ||
				result.ActionExecution.Remediation.Verification != domain.RemediationVerified ||
				executor.revalidates != 1 || executor.executes != 1 || executor.verifies != 1 {
				t.Fatalf("ConsumeApprovedAction() = %#v/%v executor=%#v", result, err, executor)
			}
			wantAudits := 2
			if operation == domain.ActionOperationDrainNode {
				wantAudits = 3
			}
			if len(fixture.persistence.writeResultAudits) != wantAudits {
				t.Fatalf("result audit count = %d, want %d", len(fixture.persistence.writeResultAudits), wantAudits)
			}
			if operation == domain.ActionOperationDrainNode {
				podAudit := fixture.persistence.writeResultAudits[1]
				if podAudit.Subject == nil || podAudit.Subject.Namespace != "team-b" ||
					podAudit.IntegrityHash != string(request.Digest) || podAudit.CorrelationID != string(request.ID) {
					t.Fatalf("cross-Namespace drain Pod audit = %#v", podAudit)
				}
			}
		})
	}
}

func TestRemediationPlansEnforceTargetSetNamespacePolicy(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	rollback := dispatchedRemediationPlan(t, fixture, domain.ActionOperationRollbackDeployment)
	members := rollback.TargetSet.Members()
	members[0].Resource.Namespace = "team-b"
	rollback.TargetSet = remediationSet(t, members)
	rollback.Target.TargetSetDigest = rollback.TargetSet.Digest()
	if rollback.Validate() == nil {
		t.Fatal("rollback admitted a ReplicaSet outside its frozen current-Namespace scope")
	}

	drain := dispatchedRemediationPlan(t, fixture, domain.ActionOperationDrainNode)
	drainIntent, err := drain.Intent(domain.PermissionProfileAsk)
	if err != nil {
		t.Fatal(err)
	}
	drainIntent.NamespaceAccess = domain.NamespaceAccessCurrent
	if domain.ValidateRemediationIntent(drainIntent) == nil {
		t.Fatal("drain intent admitted without explicit all-Namespace scope")
	}
	drain.Scope.NamespaceAccess = domain.NamespaceAccessCurrent
	if drain.Validate() == nil {
		t.Fatal("drain admitted a target set without explicit all-Namespace scope")
	}
}

func TestSharedDispatcherKeepsDirectArgvAndShellSeparate(t *testing.T) {
	for _, shell := range []bool{false, true} {
		name := "direct_argv"
		if shell {
			name = "shell"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.coordinator.localProcesses = &fakeDispatchedLocalProcess{}
			plan, shellPlan, observation := dispatchedLocalPlans(t, fixture)
			executor := &fakeDispatchedLocalProcess{observation: observation}
			executor.beforeExecute = func(envelope domain.ActionEnvelope) {
				if fixture.persistence.consumes != 1 {
					t.Fatal("local executor was reached before durable consume")
				}
				if envelope.Intent.Shell != shell || (!shell && envelope.Intent.Parameters.ShellCommand != "") ||
					(shell && envelope.Intent.Parameters.Arguments != (domain.ActionArguments{})) {
					t.Fatalf("local shell separation failed: %#v", envelope.Intent)
				}
			}
			fixture.coordinator.localProcesses = executor
			policy := PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}
			var request domain.ApprovalRequest
			var err error
			if shell {
				request, err = fixture.coordinator.SubmitLocalShellAction(context.Background(), 22, policy, shellPlan)
			} else {
				request, err = fixture.coordinator.SubmitLocalCommandAction(context.Background(), 22, policy, plan)
			}
			if err != nil || request.State != domain.ApprovalStatePending || executor.executeCalls != 0 {
				projectionError := error(nil)
				if fixture.persistence.lastCreated.ID.Valid() {
					projectionError = projectUIApprovalRequest(fixture.persistence.lastCreated, 22).Validate()
				}
				t.Fatalf("Submit local action = %#v/%v calls=%d creates=%d events=%d projection=%v intent=%v request=%v", request, err, executor.executeCalls, fixture.persistence.creates, len(fixture.ui.events), projectionError, fixture.persistence.lastCreated.Intent.ValidateShellCommand(), fixture.persistence.lastCreated.Validate())
			}
			projection := projectUIApprovalRequest(fixture.persistence.lastCreated, 22)
			if !strings.Contains(projection.ProposedSummary, "No OS filesystem or network sandbox is claimed") ||
				!strings.Contains(projection.ProposedSummary, "stdin=false tty=false") ||
				(!shell && !strings.Contains(projection.ProposedSummary, `credential-reference="none"`)) {
				t.Fatalf("local approval omitted its real boundary: %#v", projection)
			}
			if _, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 22, 201)); err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 22, 202))
			if err != nil || result.Validate() != nil || result.ActionExecution == nil || result.ActionExecution.LocalProcess == nil ||
				result.ActionExecution.SafeOutput != "bounded output" || executor.inspectCalls != 1 || executor.executeCalls != 1 {
				t.Fatalf("Consume local action = %#v/%v executor=%#v", result, err, executor)
			}
			if event := fixture.persistence.writeResultAudits[len(fixture.persistence.writeResultAudits)-1]; event.Type != domain.AuditEventWriteAttempted || event.Details.DetailCode == nil || *event.Details.DetailCode != string(domain.LocalProcessExited) {
				t.Fatalf("local result audit = %#v", event)
			}
		})
	}
}

func TestLocalOutcomeAuditTreatsBlockedSensitiveOutputAsFailure(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	plan, _, observation := dispatchedLocalPlans(t, fixture)
	fixture.coordinator.localProcesses = &fakeDispatchedLocalProcess{observation: observation}
	request, err := fixture.coordinator.SubmitLocalCommandAction(
		context.Background(), 23, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := domain.LocalCommandResult{
		State: domain.LocalProcessOutputBlocked, OutputDigest: domain.LocalSafeOutputDigest(""), Started: true,
		ErrorClass: domain.SafeErrorClassSensitiveOutputBlocked,
	}
	event, err := LocalOutcomeAudit(
		"00000000-0000-7000-8000-000000000923", request.ActionEnvelope(), result,
		request.RequestedAt.Add(time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != domain.AuditEventWriteVerificationFailed || event.Outcome != domain.AuditOutcomeFailure ||
		event.Details.DetailCode == nil || *event.Details.DetailCode != string(domain.LocalProcessOutputBlocked) ||
		event.Details.ErrorClass == nil || *event.Details.ErrorClass != domain.SafeErrorClassSensitiveOutputBlocked ||
		event.Details.Count == nil || *event.Details.Count != 0 {
		t.Fatalf("blocked-output audit = %#v", event)
	}
}

func TestSharedDispatcherDenialsAndStalenessMakeZeroExecutorCalls(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *approvalCoordinatorFixture, *fakeDispatchedRemediation, domain.RemediationActionPlan)
	}{
		{name: "user denial", run: func(t *testing.T, fixture *approvalCoordinatorFixture, executor *fakeDispatchedRemediation, plan domain.RemediationActionPlan) {
			request, err := fixture.coordinator.SubmitRemediationAction(context.Background(), 30, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}, plan)
			if err != nil {
				t.Fatal(err)
			}
			result, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 30, 301))
			if err != nil || result.State != domain.ApprovalStateRejected {
				t.Fatalf("reject result = %#v/%v", result, err)
			}
		}},
		{name: "pre-audit failure", run: func(t *testing.T, fixture *approvalCoordinatorFixture, executor *fakeDispatchedRemediation, plan domain.RemediationActionPlan) {
			request := submitAndApproveRemediation(t, fixture, plan, 31)
			fixture.persistence.consumeErr = errors.New("synthetic pre-operation audit failure")
			if _, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 31, 312)); !errors.Is(err, ErrApprovalPersistenceUnavailable) {
				t.Fatalf("pre-audit error = %v", err)
			}
		}},
		{name: "target changed", run: func(t *testing.T, fixture *approvalCoordinatorFixture, executor *fakeDispatchedRemediation, plan domain.RemediationActionPlan) {
			request := submitAndApproveRemediation(t, fixture, plan, 32)
			executor.revalidateErr = ErrRemediationActionInvalid
			result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 32, 322))
			if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated {
				t.Fatalf("changed target result = %#v/%v", result, err)
			}
		}},
		{name: "cancel before consume", run: func(t *testing.T, fixture *approvalCoordinatorFixture, executor *fakeDispatchedRemediation, plan domain.RemediationActionPlan) {
			request := submitAndApproveRemediation(t, fixture, plan, 33)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := fixture.coordinator.ConsumeApprovedAction(ctx, approvalDecisionCommand(UICommandApproveAction, request, 33, 332)); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}
		}},
		{name: "policy generation changed", run: func(t *testing.T, fixture *approvalCoordinatorFixture, executor *fakeDispatchedRemediation, plan domain.RemediationActionPlan) {
			request := submitAndApproveRemediation(t, fixture, plan, 34)
			if err := fixture.coordinator.permissions.Reconfigure(context.Background(), PermissionChangeActorLocalUser, PermissionPolicy{Profile: domain.PermissionProfileAsk}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 34, 342)); err == nil {
				t.Fatal("stale approval was consumed")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			executor := &fakeDispatchedRemediation{verification: domain.RemediationVerified}
			fixture.coordinator.remediation = executor
			plan := dispatchedRemediationPlan(t, fixture, domain.ActionOperationCordonNode)
			test.run(t, fixture, executor, plan)
			if executor.executes != 0 || executor.verifies != 0 {
				t.Fatalf("executor calls after denial = execute %d verify %d", executor.executes, executor.verifies)
			}
		})
	}
}

func TestLocalDispatcherApprovalLifecycleMakesAtMostOneProcessAttempt(t *testing.T) {
	t.Run("user rejection", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitLocalCommand(t, fixture, plan, 50)
		result, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 50, 501))
		if err != nil || result.State != domain.ApprovalStateRejected || executor.inspectCalls != 0 || executor.executeCalls != 0 {
			t.Fatalf("rejected local action = %#v/%v executor=%#v", result, err, executor)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitLocalCommand(t, fixture, plan, 51)
		fixture.clock.set(request.ExpiresAt)
		result, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 51, 511))
		if !errors.Is(err, ErrApprovalExpired) || result.State != domain.ApprovalStateExpired ||
			executor.inspectCalls != 0 || executor.executeCalls != 0 {
			t.Fatalf("expired local action = %#v/%v executor=%#v", result, err, executor)
		}
	})

	t.Run("path identity changed", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitAndApproveLocalCommand(t, fixture, plan, 52)
		executor.observation.ExecutableID = domain.ActionDigest(strings.Repeat("e", 64))
		result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 52, 522))
		if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated ||
			executor.inspectCalls != 1 || executor.executeCalls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("changed-path local action = %#v/%v executor=%#v consumes=%d", result, err, executor, fixture.persistence.consumes)
		}
	})

	t.Run("pre-operation audit failure", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitAndApproveLocalCommand(t, fixture, plan, 53)
		fixture.persistence.consumeErr = errors.New("synthetic pre-operation audit failure")
		result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 53, 532))
		if !errors.Is(err, ErrApprovalPersistenceUnavailable) || result.State != domain.ApprovalStateInvalidated ||
			executor.inspectCalls != 1 || executor.executeCalls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("pre-audit local action = %#v/%v executor=%#v consumes=%d", result, err, executor, fixture.persistence.consumes)
		}
	})

	for _, test := range []struct {
		name   string
		change func(*approvalCoordinatorFixture)
	}{
		{name: "scope changed", change: func(fixture *approvalCoordinatorFixture) {
			fixture.scope.scope.Generation++
		}},
		{name: "policy changed", change: func(fixture *approvalCoordinatorFixture) {
			if err := fixture.coordinator.permissions.Reconfigure(context.Background(), PermissionChangeActorLocalUser, PermissionPolicy{Profile: domain.PermissionProfileAsk}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, plan, executor := newLocalDispatchFixture(t)
			request := submitAndApproveLocalCommand(t, fixture, plan, 54)
			test.change(fixture)
			_, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 54, 542))
			if err == nil || executor.inspectCalls != 0 || executor.executeCalls != 0 || fixture.persistence.consumes != 0 {
				t.Fatalf("stale local action error/executor/consumes = %v/%#v/%d", err, executor, fixture.persistence.consumes)
			}
		})
	}

	t.Run("cancel before consume", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitAndApproveLocalCommand(t, fixture, plan, 55)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.coordinator.ConsumeApprovedAction(ctx, approvalDecisionCommand(UICommandApproveAction, request, 55, 552)); !errors.Is(err, context.Canceled) ||
			executor.inspectCalls != 0 || executor.executeCalls != 0 {
			t.Fatalf("cancelled local action error/executor = %v/%#v", err, executor)
		}
	})

	t.Run("consume replay", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		request := submitAndApproveLocalCommand(t, fixture, plan, 56)
		command := approvalDecisionCommand(UICommandApproveAction, request, 56, 562)
		result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), command)
		if err != nil || result.State != domain.ApprovalStateConsumed || executor.executeCalls != 1 || fixture.persistence.consumes != 1 {
			t.Fatalf("first local consume = %#v/%v executor=%#v", result, err, executor)
		}
		if _, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), command); !errors.Is(err, ErrApprovalUnavailable) ||
			executor.executeCalls != 1 || fixture.persistence.consumes != 1 {
			t.Fatalf("replayed local consume error/executor/consumes = %v/%#v/%d", err, executor, fixture.persistence.consumes)
		}
	})

	t.Run("ambiguous process outcome is not retried", func(t *testing.T) {
		fixture, plan, executor := newLocalDispatchFixture(t)
		executor.result = &domain.LocalCommandResult{
			State: domain.LocalProcessUnknown, OutputDigest: domain.LocalSafeOutputDigest(""), Started: true,
			ErrorClass: domain.SafeErrorClassTimeout,
		}
		executor.executeErr = context.DeadlineExceeded
		request := submitAndApproveLocalCommand(t, fixture, plan, 57)
		result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 57, 572))
		if !errors.Is(err, ErrApprovalActionOutcomeUnknown) || result.ActionExecution == nil ||
			result.ActionExecution.LocalProcess == nil || result.ActionExecution.LocalProcess.State != domain.LocalProcessUnknown ||
			executor.executeCalls != 1 {
			t.Fatalf("ambiguous local outcome = %#v/%v executor=%#v", result, err, executor)
		}
	})
}

func TestReadOnlyDeniesLocalExecutionAndMutationBeforePreparationIO(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	fixture.coordinator.remediation = &fakeDispatchedRemediation{verification: domain.RemediationVerified}
	fixture.coordinator.localProcesses = &fakeDispatchedLocalProcess{}
	if err := fixture.coordinator.permissions.Reconfigure(context.Background(), PermissionChangeActorLocalUser, PermissionPolicy{Profile: domain.PermissionProfileReadOnly}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		operation domain.ActionOperation
		effect    domain.CapabilityEffectClass
		risk      domain.RiskClass
	}{
		{domain.ActionOperationRestrictedLocalArgv, domain.CapabilityEffectLocalExecute, domain.RiskReview},
		{domain.ActionOperationShell, domain.CapabilityEffectLocalExecute, domain.RiskCritical},
		{domain.ActionOperationScaleWorkload, domain.CapabilityEffectClusterMutation, domain.RiskReview},
	} {
		if _, _, err := fixture.coordinator.PrepareActionPolicy(fixture.sessionID, fixture.scope.scope.Snapshot(), test.operation, test.effect, test.risk, true); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("PrepareActionPolicy(%s) error = %v", test.operation, err)
		}
	}
}

func TestCoordinatorBridgesEveryTypedProposalToFreshPreparationAndApproval(t *testing.T) {
	operations := []domain.ActionOperation{
		domain.ActionOperationScaleWorkload,
		domain.ActionOperationRollbackDeployment,
		domain.ActionOperationDeleteOwnedPod,
		domain.ActionOperationCordonNode,
		domain.ActionOperationUncordonNode,
		domain.ActionOperationDrainNode,
	}
	for _, operation := range operations {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.coordinator.remediation = &fakeDispatchedRemediation{verification: domain.RemediationVerified}
			outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome { return agent.RunOutcome{} }))
			plan := dispatchedRemediationPlan(t, fixture, operation)
			preparer := &recordingRemediationProposalPreparer{plan: plan}
			outer.approvals, outer.remediationProposals = fixture.coordinator, preparer
			proposal := remediationProposalFromPlan(plan)
			state := proposalActiveRun(fixture, proposal)
			if err := outer.prepareActionProposal(context.Background(), state); err != nil || preparer.calls != 1 || fixture.persistence.creates != 1 {
				t.Fatalf("prepareActionProposal() error/calls/creates = %v/%d/%d request=%#v", err, preparer.calls, fixture.persistence.creates, preparer.request)
			}
			if preparer.request.Target.UID != "" || preparer.request.Target.ResourceVersion != "" ||
				preparer.request.PolicyGeneration != plan.PolicyGeneration || state.bridge.sequence != 1 {
				t.Fatalf("trusted remediation bridge = %#v sequence=%d", preparer.request, state.bridge.sequence)
			}
		})
	}

	for _, shell := range []bool{false, true} {
		name := "direct argv"
		if shell {
			name = "shell"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.coordinator.localProcesses = &fakeDispatchedLocalProcess{}
			outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome { return agent.RunOutcome{} }))
			command, shellPlan, _ := dispatchedLocalPlans(t, fixture)
			preparer := newRecordingLocalProposalPreparer(t, command, shellPlan)
			outer.approvals, outer.localProposals = fixture.coordinator, preparer
			operation, policyID := domain.ActionOperationRestrictedLocalArgv, command.Policy.ID
			if shell {
				operation, policyID = domain.ActionOperationShell, shellPlan.Policy.ID
			}
			proposal := domain.RecommendedAction{
				Operation:  operation,
				Target:     &domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: fixture.scope.scope.Namespace},
				Parameters: &domain.ProposedActionParameters{Kind: domain.ProposedActionParameterPolicyID, Value: policyID},
				Action:     "Run one exact policy-owned local operation.", Risk: "Runtime policy supplies the authoritative risk.",
			}
			if shell {
				preparer.shell.Purpose = proposal.Action
			} else {
				preparer.command.Purpose = proposal.Action
			}
			state := proposalActiveRun(fixture, proposal)
			if err := outer.prepareActionProposal(context.Background(), state); err != nil || preparer.prepareCalls != 1 || fixture.persistence.creates != 1 {
				t.Fatalf("local prepareActionProposal() error/calls/creates = %v/%d/%d", err, preparer.prepareCalls, fixture.persistence.creates)
			}
			if fixture.persistence.lastCreated.Intent.Parameters.PolicyID != policyID ||
				fixture.persistence.lastCreated.Intent.Shell != shell || state.bridge.sequence != 1 {
				t.Fatalf("local proposal authority = %#v", fixture.persistence.lastCreated.Intent)
			}
			if err := outer.prepareActionProposal(context.Background(), state); err == nil ||
				preparer.prepareCalls != 1 || fixture.persistence.creates != 1 || state.localProcessCalls != 1 {
				t.Fatalf("one-over local reservation error/calls/creates/used = %v/%d/%d/%d", err, preparer.prepareCalls, fixture.persistence.creates, state.localProcessCalls)
			}
		})
	}
}

func TestCoordinatorScalePreflightUsesMinimumReviewRiskBeforeFreshRead(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	fixture.coordinator.remediation = &fakeDispatchedRemediation{verification: domain.RemediationVerified}
	if err := fixture.coordinator.permissions.Reconfigure(context.Background(), PermissionChangeActorLocalUser, PermissionPolicy{
		Profile: domain.PermissionProfileCustom,
		CustomRoutes: []CustomPermissionRoute{
			{Operation: domain.ActionOperationScaleWorkload, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionHuman},
			{Operation: domain.ActionOperationScaleWorkload, Risk: domain.RiskCritical, Disposition: domain.ReviewDispositionDeny},
		},
	}); err != nil {
		t.Fatal(err)
	}
	policy := fixture.coordinator.permissions.Status(fixture.clock.Now())
	plan := dispatchedRemediationPlan(t, fixture, domain.ActionOperationScaleWorkload)
	plan.PolicyGeneration = policy.PolicyGeneration

	outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome { return agent.RunOutcome{} }))
	preparer := &recordingRemediationProposalPreparer{plan: plan}
	outer.approvals, outer.remediationProposals = fixture.coordinator, preparer
	state := proposalActiveRun(fixture, remediationProposalFromPlan(plan))

	if err := outer.prepareActionProposal(context.Background(), state); err != nil || preparer.calls != 1 || fixture.persistence.creates != 1 {
		t.Fatalf("review-risk scale preflight error/prepares/creates = %v/%d/%d", err, preparer.calls, fixture.persistence.creates)
	}
	_, action := fixture.coordinator.Status()
	if request := fixture.persistence.lastCreated; request.Intent.Risk != domain.RiskReview || action == nil || action.Route != domain.ReviewDispositionHuman {
		t.Fatalf("prepared scale request/action = %#v/%#v", request, action)
	}
}

func TestCoordinatorReadOnlyProposalDenialsPerformNoPreparationOrExecution(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	fixture.coordinator.remediation = &fakeDispatchedRemediation{verification: domain.RemediationVerified}
	fixture.coordinator.localProcesses = &fakeDispatchedLocalProcess{}
	if err := fixture.coordinator.permissions.Reconfigure(context.Background(), PermissionChangeActorLocalUser, PermissionPolicy{Profile: domain.PermissionProfileReadOnly}); err != nil {
		t.Fatal(err)
	}
	outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome { return agent.RunOutcome{} }))
	remediationPlan := dispatchedRemediationPlan(t, fixture, domain.ActionOperationScaleWorkload)
	remediation := &recordingRemediationProposalPreparer{plan: remediationPlan}
	command, shellPlan, _ := dispatchedLocalPlans(t, fixture)
	local := newRecordingLocalProposalPreparer(t, command, shellPlan)
	outer.approvals, outer.remediationProposals, outer.localProposals = fixture.coordinator, remediation, local

	proposals := []domain.RecommendedAction{
		remediationProposalFromPlan(remediationPlan),
		{Operation: domain.ActionOperationRestrictedLocalArgv, Target: &domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: fixture.scope.scope.Namespace}, Parameters: &domain.ProposedActionParameters{Kind: domain.ProposedActionParameterPolicyID, Value: command.Policy.ID}, Action: command.Purpose, Risk: "Review."},
		{Operation: domain.ActionOperationShell, Target: &domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: fixture.scope.scope.Namespace}, Parameters: &domain.ProposedActionParameters{Kind: domain.ProposedActionParameterPolicyID, Value: shellPlan.Policy.ID}, Action: shellPlan.Purpose, Risk: "Critical."},
	}
	for _, proposal := range proposals {
		if err := outer.prepareActionProposal(context.Background(), proposalActiveRun(fixture, proposal)); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("read-only %s error = %v", proposal.Operation, err)
		}
	}
	if remediation.calls != 0 || local.prepareCalls != 0 || fixture.persistence.creates != 0 || fixture.persistence.consumes != 0 {
		t.Fatalf("read-only calls remediation/local/create/consume = %d/%d/%d/%d", remediation.calls, local.prepareCalls, fixture.persistence.creates, fixture.persistence.consumes)
	}
}

func TestSharedDispatcherPreservesUnknownAndVerificationFailureWithoutRetry(t *testing.T) {
	tests := []struct {
		name         string
		executeErr   error
		verification domain.RemediationVerificationState
		verifyErr    error
		wantErr      error
	}{
		{name: "ambiguous attempt", executeErr: context.DeadlineExceeded, verification: domain.RemediationVerified, wantErr: ErrApprovalActionOutcomeUnknown},
		{name: "verification conflict", verification: domain.RemediationVerificationFailed, verifyErr: ErrRemediationActionInvalid, wantErr: ErrApprovalActionVerificationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			executor := &fakeDispatchedRemediation{executeErr: test.executeErr, verification: test.verification, verifyErr: test.verifyErr}
			fixture.coordinator.remediation = executor
			plan := dispatchedRemediationPlan(t, fixture, domain.ActionOperationScaleWorkload)
			request := submitAndApproveRemediation(t, fixture, plan, 40)
			result, err := fixture.coordinator.ConsumeApprovedAction(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 40, 402))
			if !errors.Is(err, test.wantErr) || result.ActionExecution == nil || result.ActionExecution.Remediation == nil || executor.executes != 1 {
				t.Fatalf("ConsumeApprovedAction() = %#v/%v executor=%#v", result, err, executor)
			}
			if test.executeErr != nil {
				if executor.verifies != 0 || result.ActionExecution.Remediation.Attempt.State != domain.RemediationUnknown {
					t.Fatalf("ambiguous request was verified or retried: %#v", executor)
				}
			} else if executor.verifies != 1 || result.ActionExecution.Remediation.Verification != test.verification {
				t.Fatalf("verification failure = %#v/%#v", result.ActionExecution.Remediation, executor)
			}
		})
	}
}

type fakeDispatchedRemediation struct {
	revalidates, executes, verifies      int
	revalidateErr, executeErr, verifyErr error
	verification                         domain.RemediationVerificationState
	beforeExecute                        func()
}

type recordingRemediationProposalPreparer struct {
	calls   int
	request RemediationProposalRequest
	plan    domain.RemediationActionPlan
}

func (preparer *recordingRemediationProposalPreparer) PrepareRemediationAction(_ context.Context, request RemediationProposalRequest) (domain.RemediationActionPlan, error) {
	preparer.calls++
	preparer.request = request
	return preparer.plan, nil
}

type recordingLocalProposalPreparer struct {
	commands     domain.LocalCommandPolicyCatalog
	shells       domain.LocalShellPolicyCatalog
	command      domain.LocalCommandActionPlan
	shell        domain.LocalShellActionPlan
	prepareCalls int
}

func newRecordingLocalProposalPreparer(t *testing.T, command domain.LocalCommandActionPlan, shell domain.LocalShellActionPlan) *recordingLocalProposalPreparer {
	t.Helper()
	commands, err := domain.NewLocalCommandPolicyCatalog([]domain.LocalCommandPolicy{command.Policy})
	if err != nil {
		t.Fatal(err)
	}
	shells, err := domain.NewLocalShellPolicyCatalog([]domain.LocalShellPolicy{shell.Policy})
	if err != nil {
		t.Fatal(err)
	}
	return &recordingLocalProposalPreparer{commands: commands, shells: shells, command: command, shell: shell}
}

func (preparer *recordingLocalProposalPreparer) CommandCatalog() domain.LocalCommandPolicyCatalog {
	return preparer.commands
}
func (preparer *recordingLocalProposalPreparer) ShellCatalog() domain.LocalShellPolicyCatalog {
	return preparer.shells
}
func (preparer *recordingLocalProposalPreparer) CommandPolicy(id string) (domain.LocalCommandPolicy, bool) {
	return preparer.commands.Resolve(id)
}
func (preparer *recordingLocalProposalPreparer) ShellPolicy(id string) (domain.LocalShellPolicy, bool) {
	return preparer.shells.Resolve(id)
}
func (preparer *recordingLocalProposalPreparer) PrepareCommand(context.Context, domain.AgentRunID, domain.SessionID, domain.ClusterScope, domain.PolicyGeneration, string, string) (domain.LocalCommandActionPlan, error) {
	preparer.prepareCalls++
	return preparer.command, nil
}
func (preparer *recordingLocalProposalPreparer) PrepareShell(context.Context, domain.AgentRunID, domain.SessionID, domain.ClusterScope, domain.PolicyGeneration, string, string) (domain.LocalShellActionPlan, error) {
	preparer.prepareCalls++
	return preparer.shell, nil
}

func proposalActiveRun(fixture *approvalCoordinatorFixture, action domain.RecommendedAction) *activeRun {
	return &activeRun{
		run:       domain.AgentRun{ID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.scope.scope.Snapshot()},
		bridge:    &eventBridge{runID: fixture.runID, scopeGeneration: fixture.scope.scope.Generation},
		diagnosis: &domain.Diagnosis{RecommendedActions: []domain.RecommendedAction{action}},
	}
}

func remediationProposalFromPlan(plan domain.RemediationActionPlan) domain.RecommendedAction {
	target := plan.Target.Resource
	target.UID, target.ResourceVersion = "", ""
	proposal := domain.RecommendedAction{Operation: plan.Operation, Target: &target, Action: plan.ReasonSummary, Risk: "Runtime policy supplies the authoritative risk."}
	if plan.Operation == domain.ActionOperationScaleWorkload {
		proposal.Parameters = &domain.ProposedActionParameters{Kind: domain.ProposedActionParameterReplicas, Value: fmt.Sprintf("%d", plan.Parameters.ReplicaTarget)}
	}
	if plan.Operation == domain.ActionOperationRollbackDeployment {
		proposal.Parameters = &domain.ProposedActionParameters{Kind: domain.ProposedActionParameterRevision, Value: fmt.Sprintf("%d", plan.Parameters.Revision)}
	}
	return proposal
}

func (executor *fakeDispatchedRemediation) totalCalls() int {
	return executor.revalidates + executor.executes + executor.verifies
}

func (executor *fakeDispatchedRemediation) RevalidateRemediationAction(context.Context, domain.RemediationActionPlan) error {
	executor.revalidates++
	return executor.revalidateErr
}

func (executor *fakeDispatchedRemediation) ExecuteRemediationAction(_ context.Context, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	executor.executes++
	if executor.beforeExecute != nil {
		executor.beforeExecute()
	}
	if executor.executeErr != nil {
		return domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: domain.SafeErrorClassUnavailable}, executor.executeErr
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: plan.Limits.MaximumItems, AttemptedCount: plan.Limits.MaximumItems}, nil
}

func (executor *fakeDispatchedRemediation) VerifyRemediationAction(context.Context, domain.RemediationActionPlan) (domain.RemediationVerificationState, error) {
	executor.verifies++
	return executor.verification, executor.verifyErr
}

type fakeDispatchedLocalProcess struct {
	observation                domain.LocalExecutionObservation
	inspectCalls, executeCalls int
	inspectErr, executeErr     error
	result                     *domain.LocalCommandResult
	beforeExecute              func(domain.ActionEnvelope)
}

func (executor *fakeDispatchedLocalProcess) Inspect(context.Context, string, string) (domain.LocalExecutionObservation, error) {
	executor.inspectCalls++
	return executor.observation, executor.inspectErr
}

func (executor *fakeDispatchedLocalProcess) Execute(_ context.Context, envelope domain.ActionEnvelope) (domain.LocalCommandResult, error) {
	executor.executeCalls++
	if executor.beforeExecute != nil {
		executor.beforeExecute(envelope)
	}
	if executor.result != nil {
		return *executor.result, executor.executeErr
	}
	return domain.LocalCommandResult{
		State: domain.LocalProcessExited, SafeOutput: "bounded output",
		OutputDigest: domain.LocalSafeOutputDigest("bounded output"), ExitCode: 0,
		Started: true, LineCount: 1, ByteCount: len("bounded output"),
	}, executor.executeErr
}

func newLocalDispatchFixture(t *testing.T) (*approvalCoordinatorFixture, domain.LocalCommandActionPlan, *fakeDispatchedLocalProcess) {
	t.Helper()
	fixture := newApprovalCoordinatorFixture(t)
	plan, _, observation := dispatchedLocalPlans(t, fixture)
	executor := &fakeDispatchedLocalProcess{observation: observation}
	fixture.coordinator.localProcesses = executor
	return fixture, plan, executor
}

func submitLocalCommand(t *testing.T, fixture *approvalCoordinatorFixture, plan domain.LocalCommandActionPlan, sequence int64) domain.ApprovalRequest {
	t.Helper()
	request, err := fixture.coordinator.SubmitLocalCommandAction(
		context.Background(), sequence, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func submitAndApproveLocalCommand(t *testing.T, fixture *approvalCoordinatorFixture, plan domain.LocalCommandActionPlan, sequence int64) domain.ApprovalRequest {
	t.Helper()
	request := submitLocalCommand(t, fixture, plan, sequence)
	if _, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, sequence, uint64(sequence*10+1))); err != nil {
		t.Fatal(err)
	}
	return request
}

func dispatchedRemediationPlan(t *testing.T, fixture *approvalCoordinatorFixture, operation domain.ActionOperation) domain.RemediationActionPlan {
	t.Helper()
	scope := fixture.scope.scope
	target := domain.ActionTarget{Resource: domain.ResourceRef{UID: "target-uid", ResourceVersion: "10"}}
	parameters := domain.ActionParameters{}
	targetSet := domain.RemediationTargetSet{}
	maximumItems := 1
	switch operation {
	case domain.ActionOperationScaleWorkload:
		target.Resource.APIVersion, target.Resource.Kind, target.Resource.Namespace, target.Resource.Name = "apps/v1", "Deployment", scope.Namespace, "api"
		target.Subresource, target.Fingerprint, target.Generation = "scale", strings.Repeat("a", 64), 3
		parameters = domain.ActionParameters{Kind: domain.ActionParametersReplicaTarget, ReplicaCurrent: 2, ReplicaTarget: 3}
	case domain.ActionOperationRollbackDeployment:
		target.Resource.APIVersion, target.Resource.Kind, target.Resource.Namespace, target.Resource.Name = "apps/v1", "Deployment", scope.Namespace, "api"
		target.Fingerprint, target.Generation, target.Revision = strings.Repeat("a", 64), 3, 3
		targetSet = remediationSet(t, []domain.RemediationPlanMember{{
			Role:        domain.RemediationMemberRollbackSource,
			Resource:    domain.ResourceRef{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: scope.Namespace, Name: "api-old", UID: "source-uid", ResourceVersion: "8"},
			Fingerprint: domain.ActionDigest(strings.Repeat("b", 64)), Revision: 2,
		}})
		target.TargetSetDigest, target.TargetCount = targetSet.Digest(), 1
		parameters = domain.ActionParameters{Kind: domain.ActionParametersRevision, Revision: 2}
	case domain.ActionOperationDeleteOwnedPod:
		target.Resource.APIVersion, target.Resource.Kind, target.Resource.Namespace, target.Resource.Name = "v1", "Pod", scope.Namespace, "api-pod"
		target.Fingerprint = strings.Repeat("a", 64)
		targetSet = remediationSet(t, []domain.RemediationPlanMember{{
			Role:     domain.RemediationMemberPodController,
			Resource: domain.ResourceRef{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: scope.Namespace, Name: "api-rs", UID: "controller-uid", ResourceVersion: "7"},
		}})
		target.TargetSetDigest, target.TargetCount = targetSet.Digest(), 1
		parameters = domain.ActionParameters{Kind: domain.ActionParametersPodDelete, GracePeriodSeconds: domain.RemediationGracePeriodSeconds}
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode:
		target.Resource.APIVersion, target.Resource.Kind, target.Resource.Name = "v1", "Node", "worker-a"
		parameters = domain.ActionParameters{Kind: domain.ActionParametersNodeScheduling, Unschedulable: operation == domain.ActionOperationCordonNode}
	case domain.ActionOperationDrainNode:
		scope.NamespaceAccess = domain.NamespaceAccessAll
		fixture.scope.scope.NamespaceAccess = domain.NamespaceAccessAll
		target.Resource.APIVersion, target.Resource.Kind, target.Resource.Name = "v1", "Node", "worker-a"
		targetSet = remediationSet(t, []domain.RemediationPlanMember{
			{Role: domain.RemediationMemberDrainPod, Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "api-pod", UID: "pod-uid", ResourceVersion: "9"}, Fingerprint: domain.ActionDigest(strings.Repeat("b", 64)), Order: 1},
			{Role: domain.RemediationMemberDrainPDB, Resource: domain.ResourceRef{APIVersion: "policy/v1", Kind: "PodDisruptionBudget", Namespace: "team-b", Name: "api-pdb", UID: "pdb-uid", ResourceVersion: "6"}, DisruptionsAllowed: 1},
		})
		target.TargetSetDigest, target.TargetCount = targetSet.Digest(), 2
		parameters = domain.ActionParameters{Kind: domain.ActionParametersDrainPlan, PlanDigest: targetSet.Digest(), PlanTargetCount: 2, GracePeriodSeconds: domain.RemediationGracePeriodSeconds}
		maximumItems = 2
	default:
		t.Fatalf("unsupported remediation test operation %s", operation)
	}
	plan := domain.RemediationActionPlan{
		RunID: fixture.runID, SessionID: fixture.sessionID, Scope: scope, PolicyGeneration: 1,
		Operation: operation, Target: target, Parameters: parameters, TargetSet: targetSet,
		ReasonSummary: "Perform one exact supervised action.",
		Limits:        domain.ActionLimits{Timeout: domain.TypedRemediationActionTimeout, MaximumItems: maximumItems},
		PreparedAt:    fixture.clock.Now(),
	}
	if plan.Validate() != nil {
		t.Fatalf("test remediation plan is invalid: %#v", plan)
	}
	return plan
}

func remediationSet(t *testing.T, members []domain.RemediationPlanMember) domain.RemediationTargetSet {
	t.Helper()
	set, err := domain.NewRemediationTargetSet(members)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func dispatchedLocalPlans(t *testing.T, fixture *approvalCoordinatorFixture) (domain.LocalCommandActionPlan, domain.LocalShellActionPlan, domain.LocalExecutionObservation) {
	t.Helper()
	arguments, err := domain.NewActionArguments([]string{"status", "literal;not-shell"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewActionEnvironment([]string{"LC_ALL=C", "NO_COLOR=1"})
	if err != nil {
		t.Fatal(err)
	}
	observation := domain.LocalExecutionObservation{ExecutableID: domain.ActionDigest(strings.Repeat("c", 64)), WorkingDirectoryID: domain.ActionDigest(strings.Repeat("d", 64))}
	namespace := domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: fixture.scope.scope.Namespace, UID: "namespace-uid", ResourceVersion: "5"}
	commandPolicy := domain.LocalCommandPolicy{
		ID: "diagnostic-status", Kind: domain.LocalCommandDiagnostic, Operation: domain.LocalOperationDiagnostic,
		Executable: "/opt/kupilot/bin/diagnostic", Arguments: arguments, WorkingDirectory: "/opt/kupilot/work",
		Environment: environment, CredentialReference: domain.LocalCredentialNone,
		DiagnosticEffect: domain.LocalDiagnosticNoNetworkRead, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
	command := domain.LocalCommandActionPlan{
		RunID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.scope.scope, PolicyGeneration: 1,
		NamespaceTarget: namespace, Policy: commandPolicy, Arguments: arguments, Observation: observation,
		Limits:  domain.ActionLimits{Timeout: 5 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		Purpose: "Run one exact local diagnostic command.",
	}
	shellPolicy := domain.LocalShellPolicy{
		ID: "maintenance-shell", Executable: "/bin/sh", Command: "printf policy-owned",
		WorkingDirectory: "/opt/kupilot/work", Environment: environment, Network: domain.LocalShellNetworkNone,
		Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
	shell := domain.LocalShellActionPlan{
		RunID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.scope.scope, PolicyGeneration: 1,
		NamespaceTarget: namespace, Policy: shellPolicy, Observation: observation,
		Limits:  domain.ActionLimits{Timeout: 5 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		Purpose: "Run one exact policy-owned shell command.",
	}
	if command.Validate() != nil || shell.Validate() != nil {
		t.Fatalf("local test plans are invalid: %#v %#v", command, shell)
	}
	if _, err := command.Intent(domain.PermissionProfileAsk); err != nil {
		t.Fatalf("local command intent is invalid: %v", err)
	}
	if _, err := shell.Intent(domain.PermissionProfileAsk); err != nil {
		t.Fatalf("local shell intent is invalid: %v", err)
	}
	return command, shell, observation
}

func submitAndApproveRemediation(t *testing.T, fixture *approvalCoordinatorFixture, plan domain.RemediationActionPlan, sequence int64) domain.ApprovalRequest {
	t.Helper()
	request, err := fixture.coordinator.SubmitRemediationAction(context.Background(), sequence, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, sequence, uint64(sequence*10+1))); err != nil {
		t.Fatal(err)
	}
	return request
}
