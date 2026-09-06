package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestReviewerLifecycleIsExplicitOrderedAndNeverRenderedAsHumanApproval(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_001_000_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)

	reviewing := reviewerUIEvent(request, 1, application.UIReviewerReviewing, "")
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: reviewing})
	if model.reviewerEvent == nil || !strings.Contains(model.render(), "Reviewer · Reviewing") ||
		!strings.Contains(transcriptEntryText(model), "No action has started") {
		t.Fatalf("Reviewing state is not explicit:\n%s", model.render())
	}

	late := reviewerUIEvent(request, 3, application.UIReviewerDenied, "late")
	before := transcriptEntryText(model)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: late})
	if transcriptEntryText(model) != before || model.reviewerEvent.EventIndex != 1 {
		t.Fatal("out-of-order Reviewer event changed the transcript")
	}

	approved := reviewerUIEvent(request, 2, application.UIReviewerApproved, "The exact review request is within policy.")
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: approved})
	transcript := transcriptEntryText(model)
	if !strings.Contains(transcript, "Approved recommendation") ||
		!strings.Contains(transcript, "automated recommendation is not human approval") ||
		model.approvalDialog.Open() || model.pendingApproval != nil {
		t.Fatalf("Reviewer approval was confused with human approval:\n%s", transcript)
	}

	execution := testRestartExecution(request, 1, application.UIRestartPatchAccepted)
	execution.TargetGeneration, execution.TargetReplicas = 9, 3
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(execution)})
	if !strings.Contains(transcriptEntryText(model), "Restart request accepted") {
		t.Fatal("ordered automatic execution state was not displayed")
	}
	duplicate := execution
	duplicate.TargetGeneration = 10
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: restartExecutionUIEvent(duplicate)})
	if strings.Contains(transcriptEntryText(model), "generation 10") {
		t.Fatal("duplicate automatic action event was accepted")
	}
}

func TestReviewerTimeoutEscalatesToExactHumanDialogWithoutExecution(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_001_100_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)

	for _, event := range []application.UIEvent{
		reviewerUIEvent(request, 1, application.UIReviewerReviewing, ""),
		reviewerUIEvent(request, 2, application.UIReviewerTimedOut, ""),
		reviewerUIEvent(request, 3, application.UIReviewerEscalated, "A local user must decide."),
	} {
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: event})
	}
	escalated := model.reviewerEvent.Status
	request.Reviewer = &escalated
	model, expiry := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, Approval: &request,
	}})
	model, view := collectApprovalReviewPages(t, model)
	transcript := transcriptEntryText(model)
	if expiry == nil || !model.approvalDialog.Open() || model.approvalDialog.ApproveSelected() ||
		!strings.Contains(transcript, "Timed out") || !strings.Contains(view, "Escalated to user") ||
		!strings.Contains(view, "Reviewer rationale: A local user must decide.") ||
		!strings.Contains(view, "Deny is selected by default") {
		t.Fatalf("Reviewer escalation dialog is incomplete:\n%s", view)
	}
	if strings.Contains(view, "human approved") {
		t.Fatal("Reviewer escalation was rendered as human approval")
	}
}

func TestReviewerDenialAndStalePolicyEventsExecuteNothingAndCloseOnce(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_001_200_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	request := testUIApprovalRequest(t, now, 2)

	stale := reviewerUIEvent(request, 1, application.UIReviewerReviewing, "")
	stale.PolicyGeneration = 2
	stale.Reviewer.PolicyGeneration = 2
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: stale})
	if model.reviewerEvent != nil {
		t.Fatal("stale-policy Reviewer event was accepted")
	}

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: reviewerUIEvent(request, 1, application.UIReviewerReviewing, "")})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: reviewerUIEvent(
		request, 2, application.UIReviewerDenied, "The request should remain denied.",
	)})
	closed := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateRejected, StateReason: domain.ApprovalReasonReviewerRejected,
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalClosed, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, ApprovalResult: &closed,
	}})
	transcript := transcriptEntryText(model)
	if !strings.Contains(transcript, "Denied recommendation") ||
		!strings.Contains(transcript, "No action was started") ||
		!strings.Contains(transcript, "denied before execution") || model.approvalDialog.Open() {
		t.Fatalf("Reviewer denial state is incomplete:\n%s", transcript)
	}
	before := transcript
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalClosed, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, ApprovalResult: &closed,
	}})
	if transcriptEntryText(model) != before {
		t.Fatal("duplicate closed action event was accepted")
	}
}

func TestInRunApprovalSequenceDoesNotCreateAnAgentEventGap(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_001_300_000).UTC()
	model := newTestModel()
	model.now = func() time.Time { return now }
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID, Name: domain.ToolNamePodExec,
			Purpose: "Run one exact diagnostic.", Status: application.ToolStepRunning,
		},
	}})
	request := testUIApprovalRequest(t, now, 1)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalRequested, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 1, Approval: &request,
	}})
	if !model.approvalDialog.Open() || model.run.LastSequence != 2 || model.run.LastActionSequence != 1 {
		t.Fatalf("in-run approval ordering = run %#v dialog=%v", model.run, model.approvalDialog.Open())
	}
	closed := application.UIApprovalResult{
		RequestID: request.RequestID, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, Digest: request.Digest,
		State: domain.ApprovalStateCancelled, StateReason: domain.ApprovalReasonUserCancelled,
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventApprovalClosed, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 1, ApprovalResult: &closed,
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID, Name: domain.ToolNamePodExec,
			Status: application.ToolStepCancelled,
		},
	}})
	steps := model.transcript.ToolSteps()
	if model.run.LastSequence != 3 || len(steps) != 1 || steps[0].Status != string(application.ToolStepCancelled) {
		t.Fatalf("post-approval Tool event was rejected: run=%#v steps=%#v", model.run, steps)
	}
}

func TestRemoteAuthorizationCopyDoesNotClaimCompletion(t *testing.T) {
	t.Parallel()

	status := approvalExecutionStatus(application.UIApprovalResult{ActionExecution: &application.UIActionExecution{
		Authorization: &application.UIToolActionAuthorization{State: "authorized"},
	}})
	if !strings.Contains(status, "approval authority was consumed") ||
		!strings.Contains(status, "final scope, policy, expiry, and cancellation checks") ||
		!strings.Contains(status, "completion is reported by the Tool step") || strings.Contains(status, "succeeded") {
		t.Fatalf("remote authorization status = %q", status)
	}
}

func transcriptEntryText(model Model) string {
	values := make([]string, 0, len(model.transcript.Entries()))
	for _, entry := range model.transcript.Entries() {
		values = append(values, entry.Text)
	}
	return strings.Join(values, "\n")
}

func reviewerUIEvent(
	request application.UIApprovalRequest,
	index int64,
	state application.UIReviewerState,
	rationale string,
) application.UIEvent {
	reviewer := application.UIReviewerEvent{
		RequestID: request.RequestID, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, EventIndex: index, Digest: request.Digest,
		Status: application.UIReviewerStatus{
			State: state, Profile: "approval_reviewer", OriginHash: strings.Repeat("e", 64),
			RationaleSummary: rationale,
		},
	}
	return application.UIEvent{
		Kind: application.UIEventReviewerState, RunID: request.RunID,
		ScopeGeneration: request.Scope.Generation, PolicyGeneration: request.PolicyGeneration,
		Sequence: request.Sequence, Reviewer: &reviewer,
	}
}
