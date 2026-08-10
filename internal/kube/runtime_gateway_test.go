package kube

import (
	"context"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestToolScopeBindingBindsAndInvalidatesToolReaderByGeneration(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sample-pod", Namespace: "team-a", UID: types.UID("generated-pod-uid"),
			ResourceVersion: "17", CreationTimestamp: metav1.NewTime(time.UnixMilli(500).UTC()),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	base, initialClient, fakeClient := newFakeGateway(t, "team-a", pod)
	defer initialClient.Close()
	gateway, err := NewToolScopeBinding(base)
	if err != nil {
		t.Fatalf("NewToolScopeBinding() error = %v", err)
	}
	if err := gateway.InvalidateScope(1); err != nil {
		t.Fatalf("InvalidateScope(1) error = %v", err)
	}
	client, err := gateway.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer client.Close()
	scope := domain.ClusterScope{
		Context: "selected", Namespace: "team-a", Generation: 1,
		ActivatedAt: time.UnixMilli(1_000).UTC(),
	}
	request := toolcontract.ResourceReadRequest{
		Scope: scope,
		Reference: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
		},
		Detail: toolcontract.ResourceDetailSummary,
	}
	observation, err := gateway.ReadResource(context.Background(), request)
	if err != nil || observation.Validate() != nil {
		t.Fatalf("ReadResource() = %#v/%v", observation, err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
	fakeClient.ClearActions()

	if err := gateway.InvalidateScope(2); err != nil {
		t.Fatalf("InvalidateScope(2) error = %v", err)
	}
	if _, err := gateway.ReadResource(context.Background(), request); err == nil {
		t.Fatal("stale generation read succeeded")
	}
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("stale generation performed %d Kubernetes actions, want 0", len(actions))
	}
	request.Scope.Generation = 2
	if _, err := gateway.ReadResource(context.Background(), request); err != nil {
		t.Fatalf("ReadResource(generation 2) error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
}
