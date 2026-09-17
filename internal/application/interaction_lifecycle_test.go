package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

// These ports supply a verified synthetic scope without a Kubernetes client.
type interactionScopePorts struct{ calls int }
type interactionScopeClient struct{}

func (interactionScopeClient) Context() application.ContextCandidate {
	return application.ContextCandidate{Name: "test-context", DefaultNamespace: "team-a", Current: true}
}
func (interactionScopeClient) Close() {}
func (ports *interactionScopePorts) Contexts(context.Context) ([]application.ContextCandidate, error) {
	ports.calls++
	return []application.ContextCandidate{interactionScopeClient{}.Context()}, nil
}
func (ports *interactionScopePorts) Create(context.Context, string) (application.ScopeClient, error) {
	ports.calls++
	return interactionScopeClient{}, nil
}
func (ports *interactionScopePorts) VerifyNamespace(context.Context, application.ScopeClient, string) error {
	ports.calls++
	return nil
}
func (ports *interactionScopePorts) ListNamespaces(context.Context, application.ScopeClient, int) (domain.NamespaceList, error) {
	ports.calls++
	return domain.NamespaceList{}, errors.New("unexpected synthetic scope list")
}
func (ports *interactionScopePorts) GetResource(context.Context, application.ScopeClient, domain.ClusterScope, domain.ResourceRef) (domain.ResourceSummary, error) {
	ports.calls++
	return domain.ResourceSummary{}, errors.New("unexpected synthetic scope read")
}
func (ports *interactionScopePorts) ListResources(context.Context, application.ScopeClient, domain.ClusterScope, domain.ResourceKind, int) (domain.ResourceList, error) {
	ports.calls++
	return domain.ResourceList{}, errors.New("unexpected synthetic scope list")
}
func (*interactionScopePorts) InvalidateScope(int64) error { return nil }

type interactionSessionAdapter struct{ service *sessioncontract.Service }

func (adapter interactionSessionAdapter) ListResumable(ctx context.Context, limit int) ([]application.ResumeSessionRecord, error) {
	page, err := adapter.service.ListResumable(ctx, sessioncontract.ResumePageRequest{Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]application.ResumeSessionRecord, len(page.Sessions))
	for i, item := range page.Sessions {
		result[i] = application.ResumeSessionRecord{ID: item.ID, Title: item.Title, LastActivityAt: item.LastActivityAt, PrivacyMode: item.PrivacyMode, LastScope: item.LastScope}
	}
	return result, nil
}
func (adapter interactionSessionAdapter) ResumeByID(ctx context.Context, id domain.SessionID) (application.ResumedSessionRecord, error) {
	history, err := adapter.service.ResumeByID(ctx, id)
	return application.ResumedSessionRecord{Session: history.Session, Messages: history.Messages}, err
}
func (adapter interactionSessionAdapter) ResumeLatest(ctx context.Context) (application.ResumedSessionRecord, error) {
	candidate, err := adapter.service.GetLatestResumable(ctx)
	if err != nil {
		return application.ResumedSessionRecord{}, err
	}
	return adapter.ResumeByID(ctx, candidate.ID)
}
func (adapter interactionSessionAdapter) ReadTitleState(ctx context.Context, id domain.SessionID) (application.SessionTitleState, error) {
	value, err := adapter.service.GetByID(ctx, id)
	return application.SessionTitleState{SessionID: value.ID, Version: value.Version}, err
}
func (adapter interactionSessionAdapter) Rename(ctx context.Context, record application.RenameSessionRecord) error {
	return adapter.service.Rename(ctx, sessioncontract.RenameSession{ID: record.SessionID, Title: record.Title, ExpectedVersion: record.ExpectedVersion, UpdatedAt: record.UpdatedAt})
}
func (adapter interactionSessionAdapter) RecoverInterrupted(ctx context.Context, at time.Time) error {
	_, err := adapter.service.RecoverInterruptedRuns(ctx, at)
	return err
}
func (interactionSessionAdapter) CleanupRetention(context.Context, time.Time) error { return nil }

func interactionAwait(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic barrier did not complete")
	}
}

func (harness *interactionHarness) assertRows(t *testing.T, count int) {
	t.Helper()
	page, err := harness.messages.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{SessionID: harness.session.ID, Limit: sessioncontract.MaxMessagePageSize})
	if err != nil || len(page.Messages) != count {
		t.Fatalf("committed rows = %d, %v; want %d", len(page.Messages), err, count)
	}
	seen := make(map[domain.MessageID]bool)
	for _, message := range page.Messages {
		if seen[message.ID] || strings.Contains(message.Content, "evidence_citations") {
			t.Fatal("duplicate or wire transcript row")
		}
		seen[message.ID] = true
	}
}

func (harness *interactionHarness) assertTerminal(t *testing.T, runID domain.AgentRunID, reason domain.RunTerminalReason, failure domain.InteractionFailure) {
	t.Helper()
	result, err := harness.coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.TerminalReason != reason || result.Diagnostic != failure {
		t.Fatalf("terminal = %#v, %v; want %s/%s", result, err, reason, failure)
	}
	count := 0
	for _, event := range harness.events.Events() {
		if event.RunID == runID && event.Terminal() {
			count++
			if event.Validate() != nil || event.TerminalOutcome == nil || event.TerminalOutcome.Reason != reason || event.TerminalOutcome.Diagnostic != failure {
				t.Fatalf("terminal UI = %#v", event)
			}
		}
	}
	if count != 1 {
		t.Fatalf("terminal UI rows = %d", count)
	}
}

func TestInteractionCompositionCancellationTimeoutAndStaleness(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", native), func(t *testing.T) { testInteractionCancellationTimeoutAndStaleness(t, native) })
	}
}

func testInteractionCancellationTimeoutAndStaleness(t *testing.T, native bool) {
	for _, mode := range []string{"cancel", "timeout", "scope", "policy", "transport"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			step := interactionStep{entered: entered, release: release, final: interactionGreeting}
			if mode == "transport" {
				step.provider = mode
				step.release = nil
			}
			harness := newInteractionHarness(t, interactionScenario{responses: native, steps: []interactionStep{step}})
			runID, err := harness.coordinator.StartRun(context.Background(), application.StartRunCommand{SessionID: harness.session.ID, Question: "A bounded question."})
			if err != nil {
				t.Fatal(err)
			}
			interactionAwait(t, entered)
			_, err = harness.coordinator.EnqueueFollowUp(context.Background(), application.EnqueueFollowUpCommand{RunID: runID, SessionID: harness.session.ID, ExpectedScopeGeneration: 7, ExpectedPolicyGeneration: 1, Text: "A queued follow-up."})
			if err != nil && mode != "transport" {
				t.Fatal(err)
			}
			reason, failure := domain.RunTerminalSourceUnavailable, domain.FailureProviderTransport
			switch mode {
			case "cancel":
				if err = harness.coordinator.CancelRun(context.Background(), application.CancelRunCommand{RunID: runID, ScopeGeneration: 7}); err != nil {
					t.Fatal(err)
				}
				reason, failure = domain.RunTerminalCancelled, domain.FailureCancelled
			case "timeout":
				harness.tool.clock.mu.Lock()
				harness.tool.clock.next = harness.tool.clock.next.Add(24 * time.Hour)
				harness.tool.clock.mu.Unlock()
				reason, failure = domain.RunTerminalTimedOut, domain.FailureTimeout
				close(release)
			case "scope":
				harness.scope.invalidate()
				reason, failure = domain.RunTerminalStaleGeneration, domain.FailureStaleGeneration
				close(release)
			case "policy":
				harness.policies.stale.Store(true)
				reason, failure = domain.RunTerminalStaleGeneration, domain.FailureStaleGeneration
				close(release)
			}
			harness.assertTerminal(t, runID, reason, failure)
			calls, evidence := harness.tool.snapshot()
			if len(harness.model.snapshot()) != 1 || len(calls) != 0 || len(evidence) != 0 {
				t.Fatal("failed run drained queue or invoked a Tool")
			}
			harness.assertRows(t, 1)
			if mode != "transport" {
				recovered := false
				for _, event := range harness.events.Events() {
					if event.ConversationInput != nil && event.ConversationInput.Status.Recovered == 1 {
						recovered = true
					}
				}
				if !recovered {
					t.Fatal("queued draft was not recovered")
				}
			}
		})
	}
}

func TestInteractionCompositionExplicitResumeThenTool(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", native), func(t *testing.T) { testInteractionExplicitResumeThenTool(t, native) })
	}
}

func testInteractionExplicitResumeThenTool(t *testing.T, native bool) {
	harness := newInteractionHarness(t, interactionScenario{responses: native, resume: true, steps: []interactionStep{{final: interactionGreeting}, {tool: domain.ToolNameListResources}, {final: interactionFinal(interactionClaim(0, "The resumed Namespace is Active."))}}})
	_, first := harness.run(t, "Hello.")
	if first.TerminalReason != domain.RunTerminalCompleted {
		t.Fatalf("first = %#v", first)
	}
	resumed := harness.coordinator
	before := len(harness.model.snapshot())
	request := application.UIResumeRequest{RequestID: 901, Mode: application.UIResumeExact, SessionID: harness.session.ID}
	review, err := resumed.ResumeUI(context.Background(), request)
	if err != nil || review.Failure != "" {
		t.Fatalf("resume review = %#v, %v", review, err)
	}
	scope, ok := harness.configuration.Scope.CurrentScope()
	if !ok {
		t.Fatal("synthetic scope missing")
	}
	accepted, err := resumed.ExecuteUICommand(context.Background(), application.UICommand{Kind: application.UICommandAcceptResume, RequestID: request.RequestID, ExpectedScopeGeneration: scope.Generation})
	if err != nil || accepted.Failure != "" {
		t.Fatalf("resume acceptance = %#v, %v", accepted, err)
	}
	calls, evidence := harness.tool.snapshot()
	if len(harness.model.snapshot()) != before || len(calls) != 0 || len(evidence) != 0 {
		t.Fatal("resume performed external I/O")
	}
	runID, result := harness.run(t, "List the synthetic Namespaces.")
	if result.TerminalReason != domain.RunTerminalCompleted || result.Diagnostic != "" {
		t.Fatalf("resumed run = %#v", result)
	}
	harness.assertTerminal(t, runID, domain.RunTerminalCompleted, "")
	requests := harness.model.snapshot()
	calls, evidence = harness.tool.snapshot()
	if len(requests) != 3 || len(calls) != 1 || len(evidence) != 1 || evidence[0].RunID != runID || evidence[0].Scope.Generation != scope.Generation || evidence[0].PolicyGeneration != 1 {
		t.Fatal("resumed run call or Evidence binding mismatch")
	}
	assertHistoricalResponseProtocol(t, requests[1], "List the synthetic Namespaces.")
	harness.assertRows(t, 4)
}

func TestInteractionCompositionSteersAcrossBoundariesAndQueueDrain(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", native), func(t *testing.T) { testInteractionSteersAcrossBoundariesAndQueueDrain(t, native) })
	}
}

func testInteractionSteersAcrossBoundariesAndQueueDrain(t *testing.T, native bool) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit and drain", true: "reject and recover"}[fail], func(t *testing.T) {
			firstEntered, firstRelease := make(chan struct{}), make(chan struct{})
			secondEntered, secondRelease := make(chan struct{}), make(chan struct{})
			nextEntered, nextRelease := make(chan struct{}), make(chan struct{})
			claim := interactionClaim(1, "The inspected Namespace is Active.")
			if fail {
				claim = interactionClaim(99, "An unbound observation.")
			}
			steps := []interactionStep{{tool: domain.ToolNameListResources, entered: firstEntered, release: firstRelease}, {tool: domain.ToolNameGetResource, entered: secondEntered, release: secondRelease}, {final: interactionFinal(claim)}}
			if !fail {
				steps = append(steps, interactionStep{final: interactionGreeting, entered: nextEntered, release: nextRelease})
			}
			harness := newInteractionHarness(t, interactionScenario{responses: native, steps: steps})
			runID, err := harness.coordinator.StartRun(context.Background(), application.StartRunCommand{SessionID: harness.session.ID, Question: "List and inspect the synthetic Namespace."})
			if err != nil {
				t.Fatal(err)
			}
			interactionAwait(t, firstEntered)
			command := application.SubmitSteerCommand{RunID: runID, SessionID: harness.session.ID, ExpectedScopeGeneration: 7, ExpectedPolicyGeneration: 1, Text: "Keep the first steering detail."}
			if _, err = harness.coordinator.SubmitSteer(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			command.Text = "A queued independent question."
			if _, err = harness.coordinator.EnqueueFollowUp(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			close(firstRelease)
			interactionAwait(t, secondEntered)
			command.Text = "Keep the second steering detail."
			if _, err = harness.coordinator.SubmitSteer(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			close(secondRelease)
			if fail {
				harness.assertTerminal(t, runID, domain.RunTerminalFailed, domain.FailureEvidenceUnknown)
				harness.assertRows(t, 3)
			} else {
				interactionAwait(t, nextEntered)
				harness.assertTerminal(t, runID, domain.RunTerminalCompleted, "")
				var successor domain.AgentRunID
				for _, event := range harness.events.Events() {
					if event.Kind == application.UIEventRunStarted && event.RunID != runID {
						successor = event.RunID
					}
				}
				if !successor.Valid() {
					t.Fatal("clean commit did not start exactly one successor")
				}
				close(nextRelease)
				harness.assertTerminal(t, successor, domain.RunTerminalCompleted, "")
				harness.assertRows(t, 6)
			}
			requests := harness.model.snapshot()
			calls, evidence := harness.tool.snapshot()
			if len(requests) != len(steps) || len(calls) != 2 || len(evidence) != 2 {
				t.Fatal("unexpected model/Tool/Kubernetes calls")
			}
			for index, want := range []int{0, 1, 2} {
				count := 0
				for _, message := range requests[index].Messages {
					if strings.Contains(message.Content, "steering detail.") {
						count++
					}
				}
				if count != want {
					t.Fatalf("boundary %d steer count = %d; want %d", index, count, want)
				}
			}
			for _, item := range evidence {
				if item.RunID != runID || item.Scope.Generation != 7 || item.PolicyGeneration != 1 {
					t.Fatal("steer changed Evidence authority")
				}
			}
		})
	}
}
