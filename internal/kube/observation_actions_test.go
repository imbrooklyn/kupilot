package kube

import (
	"context"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestObservationTargetResolutionAndRevalidationUseExactPodGets(t *testing.T) {
	pod := remoteHTTPPod()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", pod.DeepCopy())
	defer client.Close()
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	request := toolcontract.ObservationTargetRequest{
		Scope: liveScope("team-a"), PolicyGeneration: 3, Operation: domain.ActionOperationLogsCurrent,
		Namespace: "team-a", PodName: pod.Name, RequestedContainer: "app",
	}
	resolved, err := reader.ResolveObservationTarget(context.Background(), request)
	if err != nil || resolved.Reference.UID != string(pod.UID) || resolved.Reference.ResourceVersion != pod.ResourceVersion || resolved.Container != "app" {
		t.Fatalf("ResolveObservationTarget() = %#v/%v", resolved, err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")

	fakeClient.ClearActions()
	parameters := domain.ActionParameters{Kind: domain.ActionParametersObservation, Observation: domain.ActionObservationParameters{
		Kind: domain.ActionObservationPodLog, Container: "app", WindowSeconds: 300, TailLines: 20,
	}}
	plan := domain.ObservationActionPlan{
		RunID: "00000000-0000-7000-8000-000000088101", SessionID: "00000000-0000-7000-8000-000000088102",
		Scope: liveScope("team-a"), PolicyGeneration: 3, Operation: domain.ActionOperationLogsCurrent,
		Target:        domain.ActionTarget{Resource: resolved.Reference, Subresource: "log", Fingerprint: string(parameters.Digest())},
		Parameters:    parameters,
		Limits:        domain.ActionLimits{Timeout: domain.ObservationKubernetesTimeout, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 4096, MaximumOutput: 4096},
		ReasonSummary: "Read one exact bounded Pod log.",
	}
	if err := reader.RevalidateObservationAction(context.Background(), plan); err != nil {
		t.Fatalf("RevalidateObservationAction() error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")

	stored, err := fakeClient.CoreV1().Pods("team-a").Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored.ResourceVersion = "changed-after-approval"
	if _, err := fakeClient.CoreV1().Pods("team-a").Update(context.Background(), stored, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reader.RevalidateObservationAction(context.Background(), plan); err == nil {
		t.Fatal("changed Pod identity retained observation authority")
	} else {
		assertRemoteKubeClass(t, err, domain.SafeErrorClassConflict)
	}
}

func TestObservationTargetDenialsPerformZeroPodGets(t *testing.T) {
	pod := remoteHTTPPod()
	tests := []struct {
		name    string
		request toolcontract.ObservationTargetRequest
		guard   toolcontract.PolicyGenerationGuard
		class   domain.SafeErrorClass
	}{
		{
			name: "cross namespace", guard: alwaysCurrentResourcePolicy{}, class: domain.SafeErrorClassInvalidInput,
			request: toolcontract.ObservationTargetRequest{Scope: liveScope("team-a"), PolicyGeneration: 3, Operation: domain.ActionOperationLogsCurrent, Namespace: "team-b", PodName: pod.Name, RequestedContainer: "app"},
		},
		{
			name: "stale policy", guard: &sequenceResourcePolicy{results: []bool{false}}, class: domain.SafeErrorClassStaleScope,
			request: toolcontract.ObservationTargetRequest{Scope: liveScope("team-a"), PolicyGeneration: 3, Operation: domain.ActionOperationLogsCurrent, Namespace: "team-a", PodName: pod.Name, RequestedContainer: "app"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway, client, fakeClient := newFakeGateway(t, "team-a", pod.DeepCopy())
			defer client.Close()
			reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), test.guard)
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.ResolveObservationTarget(context.Background(), test.request)
			assertRemoteKubeClass(t, err, test.class)
			if actions := fakeClient.Actions(); len(actions) != 0 {
				t.Fatalf("denied target performed Kubernetes actions: %#v", actions)
			}
		})
	}
}
