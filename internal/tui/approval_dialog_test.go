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

func TestApprovalDialogDefaultsDenyAndEmitsOneBoundDecision(t *testing.T) {
	now := time.UnixMilli(1_700_000_600_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, cmd := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	if cmd == nil || !model.approvalDialog.Open() || model.approvalDialog.ApproveSelected() || model.FocusedEditorCount() != 0 {
		t.Fatalf("dialog command/open/default/focus = %v/%v/%v/%d", cmd != nil, model.approvalDialog.Open(), model.approvalDialog.ApproveSelected(), model.FocusedEditorCount())
	}
	model, view := collectApprovalReviewPages(t, model)
	for _, want := range []string{
		"Action approval", "Operation: Restart Deployment",
		"scope revision 7", "policy generation 1", "resource version sample-deployment-resource-version",
		"› Deny", "Current: Deployment generation 8", "Proposed: Update only", "Parameters: none",
		"Effects: effect=cluster_mutation", "Risk: review", "Timeout: 2m0s", "Expires:", string(request.Digest),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "restart_deployment") || strings.Contains(view, "/ generation 7") {
		t.Fatalf("approval dialog exposed protocol labels:\n%s", view)
	}
	model, decisionCmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	decision := commandFromCmd(t, decisionCmd)
	if decision.Kind != application.UICommandRejectAction || decision.RequestID == 0 ||
		decision.RunID != request.RunID || decision.ExpectedScopeGeneration != request.Scope.Generation ||
		decision.ExpectedPolicyGeneration != request.PolicyGeneration ||
		decision.ApprovalID != request.RequestID || decision.ApprovalDigest != request.Digest ||
		!decision.ApprovalNonce.Equal(request.Nonce) || decision.ApprovalSequence != request.Sequence {
		t.Fatalf("reject command = %#v", decision)
	}
	if !model.approvalDialog.Open() || !model.approvalDialog.Submitted() || model.pendingApproval == nil ||
		!strings.Contains(model.render(), "Submitting denial") {
		t.Fatal("submitted dialog did not remain visible while awaiting its bound result")
	}
	model, duplicate := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if duplicate != nil {
		t.Fatal("duplicate Enter emitted a second command")
	}
}

func TestCtrlCCancelsAnActiveApprovalInsteadOfQuitting(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_650_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	decision := commandFromCmd(t, command)
	if decision.Kind != application.UICommandCancelAction || !model.approvalDialog.Open() ||
		!model.approvalDialog.Submitted() || model.pendingApprovalID != decision.RequestID {
		t.Fatalf("Ctrl+C approval decision/state = %#v open=%v submitted=%v pending=%d",
			decision, model.approvalDialog.Open(), model.approvalDialog.Submitted(), model.pendingApprovalID)
	}
}

func TestEscapeCancelsAnActiveApprovalBeforeInterruptingTheRun(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_660_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	decision := commandFromCmd(t, command)
	if decision.Kind != application.UICommandCancelAction || decision.ApprovalID != request.RequestID ||
		!model.run.Active || model.quitAfterCancel || !model.approvalDialog.Submitted() {
		t.Fatalf("Escape approval precedence = %#v active=%v quit=%v submitted=%v",
			decision, model.run.Active, model.quitAfterCancel, model.approvalDialog.Submitted())
	}
}

func TestApprovalDialogEmitsOnlyTheSelectedAdmittedOption(t *testing.T) {
	now := time.UnixMilli(1_700_000_675_000).UTC()
	tests := []struct {
		name       string
		tabs       int
		want       application.UICommandKind
		permission bool
	}{
		{name: "cancel", tabs: 1, want: application.UICommandCancelAction},
		{name: "approve once", tabs: 2, want: application.UICommandApproveAction},
		{name: "narrow Session rule", tabs: 3, want: application.UICommandCreateSessionRule, permission: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel()
			model.now = func() time.Time { return now }
			model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
			request := testUIApprovalRequest(t, now, 2)
			model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
				Kind: application.UIEventApprovalRequested, RunID: testRunID,
				ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
			}})
			for range test.tabs {
				model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
			}
			model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			decision := applicationCommandFromCmd(t, command)
			if decision.Kind != test.want || decision.ExpectedPolicyGeneration != request.PolicyGeneration ||
				decision.ExpectedScopeGeneration != request.Scope.Generation ||
				decision.ApprovalID != request.RequestID || !decision.ApprovalDigest.Equal(request.Digest) {
				t.Fatalf("selected approval option = %#v", decision)
			}
			if test.permission != (model.pendingPermissionID == decision.RequestID) ||
				!test.permission && model.pendingApprovalID != decision.RequestID {
				t.Fatalf("pending approval/permission correlation = %d/%d", model.pendingApprovalID, model.pendingPermissionID)
			}
		})
	}
}

func TestApprovalDialogReverseNavigationMovesTowardTheLastSafeOption(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_680_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	decision := applicationCommandFromCmd(t, command)
	if decision.Kind != application.UICommandCreateSessionRule || model.pendingPermissionID != decision.RequestID {
		t.Fatalf("reverse approval selection = %#v pending=%d", decision, model.pendingPermissionID)
	}
}

func TestApprovalDialogResizeTooSmallForcesEnterToDeny(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_690_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	if !model.approvalDialog.ApproveSelected() {
		t.Fatal("explicit selection did not reach approve once before resize")
	}
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 20, Height: 8})
	approvalWidth, approvalHeight := model.approvalReviewSize()
	if model.approvalDialog.Reviewable(approvalWidth, approvalHeight) {
		t.Fatal("tiny approval layout remained reviewable")
	}
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	decision := applicationCommandFromCmd(t, command)
	if decision.Kind != application.UICommandRejectAction || model.approvalDialog.Choice() != 0 {
		t.Fatalf("tiny approval Enter = %#v choice=%d, want deny", decision, model.approvalDialog.Choice())
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
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	approve := commandFromCmd(t, command)

	accepted := testRestartExecution(request, 1, application.UIRestartPatchAccepted)
	accepted.TargetGeneration, accepted.TargetReplicas = 9, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(accepted)})
	if view := compactRenderedText(model.render()); !strings.Contains(view, "Restart request accepted") || !strings.Contains(view, "generation 9 with 3 target replicas") {
		t.Fatalf("approval dialog did not render restart-request acceptance:\n%s", view)
	}
	progress := testRestartExecution(request, 2, application.UIRestartRolloutProgress)
	progress.TargetGeneration, progress.ObservedGeneration = 9, 8
	progress.UpdatedReplicas, progress.AvailableReplicas, progress.TargetReplicas = 2, 1, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(progress)})
	if view := compactRenderedText(model.render()); !strings.Contains(view, "updated 2/3") || !strings.Contains(view, "available 1/3") {
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
	if view := compactRenderedText(model.render()); !model.approvalDialog.Terminal() || !strings.Contains(view, "timed out") ||
		!strings.Contains(view, "request failure") {
		t.Fatalf("approval dialog did not distinguish rollout timeout:\n%s", view)
	}
	result := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
		PolicyGeneration: 1, Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateConsumed, StateReason: domain.ApprovalReasonConsumed,
		Execution: &terminal,
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveAction, RequestID: approve.RequestID,
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

func TestApprovalDialogRendersExactObservationEnvelope(t *testing.T) {
	now := time.UnixMilli(1_700_000_760_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIObservationApprovalRequest(t, now, 2)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, view := collectApprovalReviewPages(t, model)
	for _, want := range []string{
		"Operation: Read Current Pod Logs", "Pod test-namespace/sample-pod", "subresource log",
		`kind=pod_log container="app"`, "sensitive_read", "container output", "Kubernetes API",
		"tail-lines=20", "timeout=30s", string(request.Digest), "› Deny",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("observation approval missing %q:\n%s", want, view)
		}
	}
	if model.approvalDialog.ApproveSelected() || strings.Contains(view, "raw-observation-content") {
		t.Fatalf("observation approval was unsafe or content-bearing:\n%s", view)
	}
}

func collectApprovalReviewPages(t *testing.T, model Model) (Model, string) {
	t.Helper()
	views := []string{model.render()}
	for range 4 {
		var command tea.Cmd
		model, command = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if command != nil {
			t.Fatal("approval detail paging emitted an Application command")
		}
		views = append(views, model.render())
	}
	return model, compactRenderedText(strings.Join(views, "\n"))
}

func compactRenderedText(value string) string {
	value = strings.Map(func(current rune) rune {
		if current >= '\u2500' && current <= '\u257f' {
			return -1
		}
		return current
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return strings.ReplaceAll(value, "- ", "-")
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
		ScopeGeneration: execution.ScopeGeneration, PolicyGeneration: 1, Sequence: execution.Sequence,
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
		ScopeGeneration: 8, PolicyGeneration: 1, Sequence: 2, Approval: &stale,
	}})
	if model.approvalDialog.Open() {
		t.Fatal("stale generation opened the dialog")
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	if !model.approvalDialog.ApproveSelected() {
		t.Fatal("explicit Tab did not select Approve")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	approve := commandFromCmd(t, cmd)
	if approve.Kind != application.UICommandApproveAction {
		t.Fatalf("decision kind = %s, want approve_restart", approve.Kind)
	}

	oldResult := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID, ScopeGeneration: 7,
		PolicyGeneration: 1, Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateApproved, StateReason: domain.ApprovalReasonUserApproved,
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveAction, RequestID: approve.RequestID + 1,
		Approval: &oldResult, RunID: request.RunID,
	}})
	if model.pendingApproval == nil {
		t.Fatal("stale command result cleared the pending decision")
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandApproveAction, RequestID: approve.RequestID,
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
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
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
	if expire.Kind != application.UICommandExpireAction || model.pendingApprovalID == 0 {
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
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
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
		if expire.Kind != application.UICommandExpireAction || model.approvalDialog.Open() {
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
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
		}})
		expire := commandFromCmd(t, cmd)
		if expire.Kind != application.UICommandExpireAction || model.approvalDialog.Open() {
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
			ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
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
		Operation:              domain.ActionOperationRestartDeployment,
		OperationSchemaVersion: domain.ActionOperationRestartDeployment.SchemaVersion(),
		PolicyVersion:          domain.ActionPolicyVersion,
		PermissionProfile:      domain.PermissionProfileAsk,
		Risk:                   domain.RiskReview,
		Effect:                 domain.CapabilityEffectClusterMutation,
		PolicyGeneration:       1,
		Scope:                  domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		NamespaceAccess:        domain.NamespaceAccessCurrent,
		Target: domain.ActionTarget{
			Resource: domain.ResourceRef{
				APIVersion:      domain.RestartDeploymentTargetAPIVersion,
				Kind:            domain.RestartDeploymentTargetKind,
				Namespace:       "test-namespace",
				Name:            "sample-deployment",
				UID:             "sample-deployment-uid",
				ResourceVersion: "sample-deployment-resource-version",
			},
			Fingerprint: strings.Repeat("a", 64),
			Generation:  8,
		},
		Parameters:         domain.ActionParameters{Kind: domain.ActionParametersNone},
		DataCategories:     domain.ActionDataResourceMetadata,
		AllowedSinks:       domain.ActionSinkTerminal | domain.ActionSinkKubernetesAPI,
		NetworkEffects:     domain.ActionNetworkKubernetesAPI,
		Limits:             domain.ActionLimits{Timeout: domain.RestartDeploymentActionTimeout, MaximumItems: 1},
		VerificationPlanID: domain.RestartDeploymentVerificationPlanID,
		ReasonSummary:      "Restart after the bounded diagnosis.",
		RiskSummary:        domain.RestartDeploymentRiskSummary,
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
		Sequence: sequence, Operation: intent.Operation, OperationSchema: intent.OperationSchemaVersion,
		PolicyVersion: intent.PolicyVersion, PermissionProfile: intent.PermissionProfile,
		PolicyGeneration: intent.PolicyGeneration, Risk: intent.Risk, Effect: intent.Effect,
		Scope: intent.Scope, NamespaceAccess: intent.NamespaceAccess, Target: intent.Target.Resource,
		TemplateFingerprint: intent.Target.Fingerprint, DeploymentGeneration: intent.Target.Generation,
		Parameters: intent.Parameters, DataCategories: intent.DataCategories,
		AllowedSinks: intent.AllowedSinks, NetworkEffects: intent.NetworkEffects,
		Limits: intent.Limits, VerificationPlanID: intent.VerificationPlanID,
		ReasonSummary: intent.ReasonSummary, RiskSummary: intent.RiskSummary,
		CurrentSummary:   "Deployment generation 8 with Pod template fingerprint " + intent.Target.Fingerprint + ".",
		ProposedSummary:  "Update only the Kupilot-owned restart annotation to create a new Pod template revision.",
		ParameterSummary: "none",
		EffectSummary:    "effect=cluster_mutation · data=resource metadata · sinks=terminal, Kubernetes API · network=Kubernetes API · destination=none · stdin=false tty=false shell=false · timeout=2m0s · items=1 lines=0 bytes=0 output=0",
		Digest:           domainRequest.Digest, Nonce: nonce, RequestedAt: requestedAt, ExpiresAt: domainRequest.ExpiresAt,
	}
}

func testUIObservationApprovalRequest(t *testing.T, requestedAt time.Time, sequence int64) application.UIApprovalRequest {
	t.Helper()
	nonce, err := domain.NewApprovalNonce(bytes.Repeat([]byte{0x52}, domain.ApprovalNonceBytes))
	if err != nil {
		t.Fatal(err)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersObservation, Observation: domain.ActionObservationParameters{
		Kind: domain.ActionObservationPodLog, Container: "app", WindowSeconds: 300, TailLines: 20,
	}}
	plan := domain.ObservationActionPlan{
		RunID: testRunID, SessionID: "00000000-0000-7000-8000-000000008402",
		Scope:            domain.ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: requestedAt.Add(-time.Second)},
		PolicyGeneration: 1, Operation: domain.ActionOperationLogsCurrent,
		Target: domain.ActionTarget{Resource: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod",
			UID: "sample-pod-uid", ResourceVersion: "sample-pod-resource-version",
		}, Subresource: "log", Fingerprint: string(parameters.Digest())},
		Parameters:    parameters,
		Limits:        domain.ActionLimits{Timeout: domain.ObservationKubernetesTimeout, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		ReasonSummary: "Read one exact bounded Pod log.",
	}
	intent, err := plan.Intent(domain.PermissionProfileAsk)
	if err != nil {
		t.Fatal(err)
	}
	domainRequest := domain.ApprovalRequest{
		ID: "00000000-0000-7000-8000-000000008403", RunID: plan.RunID, SessionID: plan.SessionID,
		Intent: intent, Nonce: nonce, State: domain.ApprovalStatePending, RequestedAt: requestedAt,
		ExpiresAt: requestedAt.Add(domain.ApprovalExecutionTTL), StateChangedAt: requestedAt,
	}
	domainRequest.Digest, err = approval.OperationDigest(domainRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := application.UIApprovalRequest{
		RequestID: domainRequest.ID, RunID: domainRequest.RunID, SessionID: domainRequest.SessionID,
		Sequence: sequence, Operation: intent.Operation, OperationSchema: intent.OperationSchemaVersion,
		PolicyVersion: intent.PolicyVersion, PermissionProfile: intent.PermissionProfile,
		PolicyGeneration: intent.PolicyGeneration, Risk: intent.Risk, Effect: intent.Effect,
		Scope: intent.Scope, NamespaceAccess: intent.NamespaceAccess, Target: intent.Target.Resource,
		TargetSubresource: intent.Target.Subresource, TemplateFingerprint: intent.Target.Fingerprint,
		Parameters: intent.Parameters, DataCategories: intent.DataCategories, AllowedSinks: intent.AllowedSinks,
		NetworkEffects: intent.NetworkEffects, Limits: intent.Limits, VerificationPlanID: intent.VerificationPlanID,
		ReasonSummary: intent.ReasonSummary, RiskSummary: intent.RiskSummary,
		CurrentSummary:   "Pod test-namespace/sample-pod with UID and resource version bound by digest.",
		ProposedSummary:  "Read only the displayed bounded, sanitized log selection from this exact Pod.",
		ParameterSummary: `kind=pod_log container="app" previous=false all-containers=false include-init=false include-ephemeral=false search="" query="" window=300s step=0s tail-lines=20 series-limit=0 line-limit=0`,
		EffectSummary:    "effect=sensitive_read · data=container output · sinks=terminal, model, Kubernetes API · network=Kubernetes API · destination=none · stdin=false tty=false shell=false · timeout=30s · items=1 lines=20 bytes=4096 output=4096",
		Digest:           domainRequest.Digest, Nonce: nonce, RequestedAt: requestedAt, ExpiresAt: domainRequest.ExpiresAt,
	}
	if request.Validate() != nil {
		t.Fatalf("observation UI request invalid: %#v", request)
	}
	return request
}
