package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalDialogDefaultsRejectAndEmitsOneBoundDecision(t *testing.T) {
	now := time.UnixMilli(1_700_000_600_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, cmd := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Approval: &request,
	}})
	if cmd == nil || !model.approvalDialog.Open() || model.approvalDialog.ApproveSelected() || model.FocusedEditorCount() != 0 {
		t.Fatalf("dialog command/open/default/focus = %v/%v/%v/%d", cmd != nil, model.approvalDialog.Open(), model.approvalDialog.ApproveSelected(), model.FocusedEditorCount())
	}
	view := model.render()
	for _, want := range []string{
		"Restart approval", "Operation: Restart Deployment", "scope revision 7", "› Reject", "Current: Deployment generation 8",
		"Proposed: Update only", string(request.Digest),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog view missing %q", want)
		}
	}
	if strings.Contains(view, "restart_deployment") || strings.Contains(view, "/ generation 7") {
		t.Fatalf("approval dialog exposed protocol labels:\n%s", view)
	}
	model, decisionCmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	decision := commandFromCmd(t, decisionCmd)
	if decision.Kind != application.UICommandRejectRestart || decision.RequestID == 0 ||
		decision.RunID != request.RunID || decision.ExpectedScopeGeneration != request.Scope.Generation ||
		decision.ApprovalID != request.RequestID || decision.ApprovalDigest != request.Digest ||
		!decision.ApprovalNonce.Equal(request.Nonce) || decision.ApprovalSequence != request.Sequence {
		t.Fatalf("reject command = %#v", decision)
	}
	if !model.approvalDialog.Open() || !model.approvalDialog.Submitted() || model.pendingApproval == nil ||
		!strings.Contains(model.render(), "Submitting rejection") {
		t.Fatal("submitted dialog did not remain visible while awaiting its bound result")
	}
	model, duplicate := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if duplicate != nil {
		t.Fatal("duplicate Enter emitted a second command")
	}
}

func TestCtrlCRejectsAnActiveApprovalInsteadOfQuitting(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_650_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Approval: &request,
	}})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	decision := commandFromCmd(t, command)
	if decision.Kind != application.UICommandRejectRestart || !model.approvalDialog.Open() ||
		!model.approvalDialog.Submitted() || model.pendingApprovalID != decision.RequestID {
		t.Fatalf("Ctrl+C approval decision/state = %#v open=%v submitted=%v pending=%d",
			decision, model.approvalDialog.Open(), model.approvalDialog.Submitted(), model.pendingApprovalID)
	}
}

func TestApprovalDialogRendersOrderedPatchAndRolloutResults(t *testing.T) {
	now := time.UnixMilli(1_700_000_750_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	approve := commandFromCmd(t, command)

	accepted := testRestartExecution(request, 1, application.UIRestartPatchAccepted)
	accepted.TargetGeneration, accepted.TargetReplicas = 9, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(accepted)})
	if view := model.render(); !strings.Contains(view, "Restart request accepted") || !strings.Contains(view, "generation 9 with 3 target replicas") {
		t.Fatalf("approval dialog did not render restart-request acceptance:\n%s", view)
	}
	progress := testRestartExecution(request, 2, application.UIRestartRolloutProgress)
	progress.TargetGeneration, progress.ObservedGeneration = 9, 8
	progress.UpdatedReplicas, progress.AvailableReplicas, progress.TargetReplicas = 2, 1, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(progress)})
	if view := model.render(); !strings.Contains(view, "updated 2/3") || !strings.Contains(view, "available 1/3") {
		t.Fatalf("approval dialog did not render rollout progress:\n%s", view)
	}
	stale := progress
	stale.EventIndex = 4
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(stale)})
	if model.approvalDialog.ExecutionIndex() != 2 {
		t.Fatal("out-of-order rollout event was accepted")
	}
	terminal := testRestartExecution(request, 3, application.UIRestartRolloutTimedOut)
	terminal.TargetGeneration, terminal.ObservedGeneration = 9, 8
	terminal.UpdatedReplicas, terminal.AvailableReplicas, terminal.TargetReplicas = 2, 1, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(terminal)})
	if view := model.render(); !model.approvalDialog.Terminal() || !strings.Contains(view, "timed out") ||
		!strings.Contains(view, "request failure") {
		t.Fatalf("approval dialog did not distinguish rollout timeout:\n%s", view)
	}
	result := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
		Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateConsumed, StateReason: domain.ApprovalReasonConsumed,
		Execution: &terminal,
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveRestart, RequestID: approve.RequestID,
		Approval: &result, RunID: request.RunID,
	}})
	if model.pendingApproval != nil || !model.approvalDialog.Open() || !model.approvalDialog.Terminal() {
		t.Fatal("terminal result retained authority or disappeared before acknowledgement")
	}
	model, closeCommand := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if closeCommand != nil || model.approvalDialog.Open() {
		t.Fatal("terminal result did not close locally without another external command")
	}
}

func testRestartExecution(
	request application.UIApprovalRequest,
	eventIndex int64,
	state application.UIRestartExecutionState,
) application.UIRestartExecution {
	return application.UIRestartExecution{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: request.Scope.Generation,
		Sequence: request.Sequence, EventIndex: eventIndex, Digest: request.Digest, State: state,
	}
}

func restartExecutionUIEvent(execution application.UIRestartExecution) application.UIEvent {
	return application.UIEvent{
		Kind: application.UIEventRestartExecution, RunID: execution.RunID,
		ScopeGeneration: execution.ScopeGeneration, Sequence: execution.Sequence,
		RestartExecution: &execution,
	}
}

func TestRestartExecutionStatusDistinguishesTerminalOutcomes(t *testing.T) {
	tests := []struct {
		state   application.UIRestartExecutionState
		failure application.RestartRolloutFailureCode
		want    string
	}{
		{state: application.UIRestartPatchFailed, want: "will not be retried"},
		{state: application.UIRestartPatchOutcomeUnknown, want: "outcome is unknown"},
		{state: application.UIRestartNotAttempted, want: "was not attempted"},
		{state: application.UIRestartRolloutSucceeded, want: "Rollout verified"},
		{
			state:   application.UIRestartRolloutFailed,
			failure: application.RestartRolloutFailureProgressDeadline,
			want:    "progress deadline was exceeded",
		},
		{state: application.UIRestartRolloutTimedOut, want: "timed out"},
		{state: application.UIRestartRolloutUnavailable, want: "became unavailable"},
		{state: application.UIRestartResultAuditFailed, want: "High-priority error"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			execution := application.UIRestartExecution{
				State: test.state, FailureCode: test.failure,
				TargetGeneration: 9, ObservedGeneration: 9,
				UpdatedReplicas: 3, AvailableReplicas: 3, TargetReplicas: 3,
			}
			if status := restartExecutionStatus(execution); !strings.Contains(status, test.want) {
				t.Fatalf("restartExecutionStatus(%q) = %q, want substring %q", test.state, status, test.want)
			}
		})
	}
}

func TestApprovalDialogApproveRequiresExplicitSelectionAndRejectsStaleMessages(t *testing.T) {
	now := time.UnixMilli(1_700_000_700_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)

	stale := request
	stale.Scope.Generation++
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 8, Sequence: 2, Approval: &stale,
	}})
	if model.approvalDialog.Open() {
		t.Fatal("stale generation opened the dialog")
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	if !model.approvalDialog.ApproveSelected() {
		t.Fatal("explicit Tab did not select Approve")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	approve := commandFromCmd(t, cmd)
	if approve.Kind != application.UICommandApproveRestart {
		t.Fatalf("decision kind = %s, want approve_restart", approve.Kind)
	}

	oldResult := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
		Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateApproved, StateReason: domain.ApprovalReasonUserApproved,
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveRestart, RequestID: approve.RequestID + 1,
		Approval: &oldResult, RunID: request.RunID,
	}})
	if model.pendingApproval == nil {
		t.Fatal("stale command result cleared the pending decision")
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveRestart, RequestID: approve.RequestID,
		Approval: &oldResult, RunID: request.RunID,
	}})
	if model.pendingApproval == nil || model.pendingApprovalID != 0 || model.approvalDialog.Open() {
		t.Fatal("matching approval result did not retain only the hidden expiry proof")
	}
	if !strings.Contains(model.render(), "approved · not executed") {
		t.Fatal("footer did not expose the approved-not-executed state")
	}
	model, late := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Approval: &request,
	}})
	if late != nil || model.approvalDialog.Open() {
		t.Fatal("duplicate old request reopened the dialog")
	}
	model.now = func() time.Time { return request.ExpiresAt }
	model, expiry := updateModel(t, model, ApprovalExpiryMsg{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
		Sequence: request.Sequence, Digest: request.Digest,
	})
	expire := commandFromCmd(t, expiry)
	if expire.Kind != application.UICommandExpireRestart || model.pendingApprovalID == 0 {
		t.Fatalf("approved-not-executed expiry command/state = %#v/%d", expire, model.pendingApprovalID)
	}
}

func TestApprovalDialogExpiryAndScopeChangeClearWithoutApproval(t *testing.T) {
	now := time.UnixMilli(1_700_000_800_000).UTC()
	t.Run("expiry", func(t *testing.T) {
		model := newTestModel()
		model.now = func() time.Time { return now }
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
		request := testUIApprovalRequest(t, now, 2)
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
			Kind: application.UIEventApprovalRequested, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Approval: &request,
		}})
		model, early := updateModel(t, model, ApprovalExpiryMsg{
			RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
			Sequence: request.Sequence, Digest: request.Digest,
		})
		if early != nil || !model.approvalDialog.Open() {
			t.Fatal("early expiry message changed the pending dialog")
		}
		model.now = func() time.Time { return request.ExpiresAt }
		model, cmd := updateModel(t, model, ApprovalExpiryMsg{
			RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
			Sequence: request.Sequence, Digest: request.Digest,
		})
		expire := commandFromCmd(t, cmd)
		if expire.Kind != application.UICommandExpireRestart || model.approvalDialog.Open() {
			t.Fatalf("expiry command/dialog = %#v/%v", expire, model.approvalDialog.Open())
		}
	})
	t.Run("already expired request", func(t *testing.T) {
		model := newTestModel()
		request := testUIApprovalRequest(t, now, 2)
		model.now = func() time.Time { return request.ExpiresAt }
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
		model, cmd := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
			Kind: application.UIEventApprovalRequested, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Approval: &request,
		}})
		expire := commandFromCmd(t, cmd)
		if expire.Kind != application.UICommandExpireRestart || model.approvalDialog.Open() {
			t.Fatalf("already-expired command/dialog = %#v/%v", expire, model.approvalDialog.Open())
		}
	})
	t.Run("scope change", func(t *testing.T) {
		model := newTestModel()
		model.now = func() time.Time { return now }
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
		request := testUIApprovalRequest(t, now, 2)
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
			Kind: application.UIEventApprovalRequested, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Approval: &request,
		}})
		model.pendingScopeID = 90
		model.applyScopeResult(application.UIScopeResult{
			RequestID: 90, ExpectedGeneration: 7, ScopeGeneration: 8,
			Context: "new-context", Namespace: "new-namespace", ReadOnly: true,
		})
		if model.approvalDialog.Open() || model.pendingApproval != nil {
			t.Fatal("scope change retained approval UI authority")
		}
	})
}

func testUIApprovalRequest(t *testing.T, requestedAt time.Time, sequence int64) application.UIApprovalRequest {
	t.Helper()
	nonce, err := domain.NewApprovalNonce(bytes.Repeat([]byte{0x51}, domain.ApprovalNonceBytes))
	if err != nil {
		t.Fatalf("NewApprovalNonce() error = %v", err)
	}
	intent := domain.OperationIntent{
		Operation:      domain.ApprovalOperationRestartDeployment,
		Scope:          domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		DeploymentName: "sample-deployment", DeploymentUID: "sample-deployment-uid",
		TemplateFingerprint: strings.Repeat("a", 64), DeploymentGeneration: 8,
		PolicyVersion: domain.RestartDeploymentApprovalPolicyVersion,
		ReasonSummary: "Restart after the bounded diagnosis.",
	}
	domainRequest := domain.ApprovalRequest{
		ID: "00000000-0000-7000-8000-000000008401", RunID: testRunID,
		SessionID: "00000000-0000-7000-8000-000000008402", Intent: intent,
		Nonce: nonce, State: domain.ApprovalStatePending, RequestedAt: requestedAt,
		ExpiresAt: requestedAt.Add(domain.ApprovalExecutionTTL), StateChangedAt: requestedAt,
	}
	domainRequest.Digest, err = approval.OperationDigest(domainRequest)
	if err != nil {
		t.Fatalf("OperationDigest() error = %v", err)
	}
	return application.UIApprovalRequest{
		RequestID: domainRequest.ID, RunID: domainRequest.RunID, SessionID: domainRequest.SessionID,
		Sequence: sequence, Operation: intent.Operation, Scope: intent.Scope,
		Target: domain.ResourceRef{
			APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
			Namespace: intent.Scope.Namespace, Name: intent.DeploymentName, UID: intent.DeploymentUID,
		},
		TemplateFingerprint: intent.TemplateFingerprint, DeploymentGeneration: intent.DeploymentGeneration,
		ReasonSummary: intent.ReasonSummary, RiskSummary: domain.RestartDeploymentRiskSummary,
		CurrentSummary:  "Deployment generation 8 with Pod template fingerprint " + intent.TemplateFingerprint + ".",
		ProposedSummary: "Update only the Kupilot-owned restart annotation to create a new Pod template revision.",
		Digest:          domainRequest.Digest, Nonce: nonce, RequestedAt: requestedAt, ExpiresAt: domainRequest.ExpiresAt,
	}
}
