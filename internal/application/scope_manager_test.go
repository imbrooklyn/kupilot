package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestScopeManagerBoundaryContainsNoClientGoTypes(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(NewScopeManager),
		reflect.TypeOf((*ScopeManager)(nil)),
		reflect.TypeOf((*ScopeClient)(nil)),
		reflect.TypeOf((*ScopeClientFactory)(nil)),
		reflect.TypeOf((*NamespaceReader)(nil)),
		reflect.TypeOf((*ResourceService)(nil)),
		reflect.TypeOf(ContextCandidate{}),
		reflect.TypeOf(ScopeView{}),
		reflect.TypeOf((*ScopeError)(nil)),
	}
	for _, current := range types {
		assertNoKubernetesVendorType(t, current, map[reflect.Type]bool{})
		for index := 0; index < current.NumMethod(); index++ {
			assertNoKubernetesVendorType(t, current.Method(index).Type, map[reflect.Type]bool{})
		}
	}
}

func TestScopeManagerSwitchContextInvalidatesBeforeCreatingTarget(t *testing.T) {
	clock := &sequenceClock{next: time.UnixMilli(1).UTC()}
	recorder := &scopeEventRecorder{}
	oldClient := &fakeScopeClient{
		context: ContextCandidate{Name: "old-context", DefaultNamespace: "team-a"},
		closeFn: func() { recorder.add("close:old-context") },
	}
	newClient := &fakeScopeClient{
		context: ContextCandidate{Name: "new-context", DefaultNamespace: "team-b"},
	}
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) {
			recorder.add("contexts")
			return []ContextCandidate{oldClient.context, newClient.context}, nil
		},
		createFn: func(_ context.Context, name string) (ScopeClient, error) {
			recorder.add("create:" + name)
			switch name {
			case oldClient.context.Name:
				return oldClient, nil
			case newClient.context.Name:
				return newClient, nil
			default:
				return nil, errors.New("unexpected Context")
			}
		},
	}
	namespaces := &fakeNamespaceReader{
		verifyFn: func(_ context.Context, _ ScopeClient, namespace string) error {
			recorder.add("verify:" + namespace)
			return nil
		},
		listFn: func(_ context.Context, _ ScopeClient, limit int) (domain.NamespaceList, error) {
			if limit != 50 {
				t.Fatalf("namespace limit = %d, want 50", limit)
			}
			return domain.NamespaceList{Items: []domain.NamespaceSummary{{Name: "team-a"}}}, nil
		},
	}
	resources := &fakeResourceService{
		listFn: func(_ context.Context, _ ScopeClient, scope domain.ClusterScope, kind domain.ResourceKind, limit int) (domain.ResourceList, error) {
			return domain.ResourceList{Items: []domain.ResourceSummary{resourceSummary(scope.Namespace, kind, "sample")}}, nil
		},
	}
	hook := &fakeScopeInvalidationHook{}
	manager, err := NewScopeManager(factory, namespaces, resources, hook, clock.Now)
	if err != nil {
		t.Fatalf("NewScopeManager() error = %v", err)
	}
	approvalHook := &fakeScopeInvalidationHook{invalidateFn: func(int64) error {
		recorder.add("approval-invalidation-hook")
		return nil
	}}
	if err := manager.BindApprovalInvalidationHook(approvalHook); err != nil {
		t.Fatalf("BindApprovalInvalidationHook() error = %v", err)
	}

	oldScope, err := manager.SwitchContext(context.Background(), oldClient.context.Name, 0)
	if err != nil {
		t.Fatalf("initial SwitchContext() error = %v", err)
	}
	if err := manager.SelectResource(oldScope, domain.ResourceRef{
		APIVersion: "v1",
		Kind:       "Pod",
		Namespace:  oldScope.Namespace,
		Name:       "selected-pod",
	}); err != nil {
		t.Fatalf("SelectResource() error = %v", err)
	}
	if _, err := manager.ListNamespaces(context.Background(), oldScope, 50); err != nil {
		t.Fatalf("ListNamespaces() error = %v", err)
	}
	if _, err := manager.ListResources(context.Background(), oldScope, domain.ResourceKindPod, 20); err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}

	recorder.reset()
	cancelled := make(chan struct{})
	if err := manager.BindRun(oldScope, domain.AgentRunID("00000000-0000-7000-8000-000000000001"), func() {
		view := manager.View()
		if view.State != ScopeStateActivating || view.Generation != oldScope.Generation+1 {
			t.Errorf("scope at cancellation = %#v, want activating generation %d", view, oldScope.Generation+1)
		}
		recorder.add("cancel-run")
		close(cancelled)
	}); err != nil {
		t.Fatalf("BindRun() error = %v", err)
	}
	hook.invalidateFn = func(generation int64) error {
		if generation != oldScope.Generation+1 {
			t.Errorf("hook generation = %d, want %d", generation, oldScope.Generation+1)
		}
		select {
		case <-cancelled:
		default:
			t.Error("invalidation hook ran before active-run cancellation")
		}
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		if manager.selectedResource != nil {
			t.Error("invalidation hook observed a selected ResourceRef")
		}
		if len(manager.namespaceCache) != 0 || len(manager.resourceCache) != 0 {
			t.Error("invalidation hook observed old-generation cache entries")
		}
		recorder.add("invalidation-hook")
		return nil
	}

	newScope, err := manager.SwitchContext(context.Background(), newClient.context.Name, oldScope.Generation)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	wantEvents := []string{
		"contexts",
		"cancel-run",
		"approval-invalidation-hook",
		"invalidation-hook",
		"close:old-context",
		"create:new-context",
		"verify:team-b",
	}
	if got := recorder.events(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("switch events = %#v, want %#v", got, wantEvents)
	}
	if newScope.Context != newClient.context.Name || newScope.Namespace != newClient.context.DefaultNamespace || newScope.Generation != oldScope.Generation+1 {
		t.Fatalf("new scope = %#v", newScope)
	}
	if view := manager.View(); view.State != ScopeStateActive || view.Scope == nil || *view.Scope != newScope {
		t.Fatalf("View() = %#v, want active scope %#v", view, newScope)
	}
	if selected, err := manager.SelectedResource(newScope); err != nil || selected != nil {
		t.Fatalf("SelectedResource() = %#v, %v; want nil, nil", selected, err)
	}
}

func TestScopeManagerContextActivationFailureLeavesNoActiveClient(t *testing.T) {
	tests := []struct {
		name             string
		createTarget     func(*fakeScopeClient) (ScopeClient, error)
		verifyTarget     func() error
		wantClass        domain.SafeErrorClass
		wantTargetClosed bool
	}{
		{
			name: "client creation failure",
			createTarget: func(*fakeScopeClient) (ScopeClient, error) {
				return nil, &testClassifiedError{class: domain.SafeErrorClassUnavailable}
			},
			wantClass: domain.SafeErrorClassUnavailable,
		},
		{
			name:         "Namespace forbidden",
			createTarget: func(client *fakeScopeClient) (ScopeClient, error) { return client, nil },
			verifyTarget: func() error {
				return &testClassifiedError{class: domain.SafeErrorClassPermissionDenied}
			},
			wantClass:        domain.SafeErrorClassPermissionDenied,
			wantTargetClosed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldClient := &fakeScopeClient{context: ContextCandidate{Name: "old-context", DefaultNamespace: "team-a"}}
			targetClient := &fakeScopeClient{context: ContextCandidate{Name: "target-context", DefaultNamespace: "team-b"}}
			factory := &fakeScopeFactory{
				contextsFn: func(context.Context) ([]ContextCandidate, error) {
					return []ContextCandidate{oldClient.context, targetClient.context}, nil
				},
				createFn: func(_ context.Context, name string) (ScopeClient, error) {
					if name == oldClient.context.Name {
						return oldClient, nil
					}
					return test.createTarget(targetClient)
				},
			}
			namespaces := &fakeNamespaceReader{verifyFn: func(_ context.Context, client ScopeClient, _ string) error {
				if client == targetClient && test.verifyTarget != nil {
					return test.verifyTarget()
				}
				return nil
			}}
			manager := newTestScopeManager(t, factory, namespaces, &fakeResourceService{}, &fakeScopeInvalidationHook{})
			oldScope, err := manager.SwitchContext(context.Background(), oldClient.context.Name, 0)
			if err != nil {
				t.Fatalf("initial SwitchContext() error = %v", err)
			}

			_, err = manager.SwitchContext(context.Background(), targetClient.context.Name, oldScope.Generation)
			assertClass(t, err, test.wantClass)
			view := manager.View()
			if view.State != ScopeStateUnavailable || view.Generation != oldScope.Generation+1 || view.Scope != nil {
				t.Fatalf("View() after failure = %#v", view)
			}
			if oldClient.closeCalls.Load() != 1 {
				t.Fatalf("old client Close() calls = %d, want 1", oldClient.closeCalls.Load())
			}
			wantTargetCloseCalls := int64(0)
			if test.wantTargetClosed {
				wantTargetCloseCalls = 1
			}
			if targetClient.closeCalls.Load() != wantTargetCloseCalls {
				t.Fatalf("target client Close() calls = %d, want %d", targetClient.closeCalls.Load(), wantTargetCloseCalls)
			}
		})
	}
}

func TestScopeManagerNamespaceSwitchVerifiesBeforeCommit(t *testing.T) {
	client := &fakeScopeClient{context: ContextCandidate{Name: "selected", DefaultNamespace: "team-a"}}
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) { return []ContextCandidate{client.context}, nil },
		createFn:   func(context.Context, string) (ScopeClient, error) { return client, nil },
	}
	verificationStarted := make(chan struct{}, 1)
	verificationResult := make(chan error, 1)
	namespaces := &fakeNamespaceReader{verifyFn: func(_ context.Context, _ ScopeClient, namespace string) error {
		if namespace == client.context.DefaultNamespace {
			return nil
		}
		verificationStarted <- struct{}{}
		return <-verificationResult
	}}
	hook := &fakeScopeInvalidationHook{}
	manager := newTestScopeManager(t, factory, namespaces, &fakeResourceService{}, hook)
	oldScope, err := manager.SwitchContext(context.Background(), client.context.Name, 0)
	if err != nil {
		t.Fatalf("initial SwitchContext() error = %v", err)
	}
	cancelCalls := atomic.Int64{}
	if err := manager.BindRun(oldScope, domain.AgentRunID("00000000-0000-7000-8000-000000000002"), func() {
		cancelCalls.Add(1)
	}); err != nil {
		t.Fatalf("BindRun() error = %v", err)
	}

	failed := make(chan error, 1)
	go func() {
		_, switchErr := manager.SwitchNamespace(context.Background(), "team-b", oldScope.Generation)
		failed <- switchErr
	}()
	receiveSignal(t, verificationStarted, "Namespace verification did not start")
	if view := manager.View(); view.State != ScopeStateActive || view.Generation != oldScope.Generation || view.Scope == nil || *view.Scope != oldScope {
		t.Fatalf("scope changed before Namespace verification: %#v", view)
	}
	if cancelCalls.Load() != 0 || hook.calls.Load() != 1 {
		t.Fatalf("pre-commit cancel/hook calls = %d/%d, want 0/1 initial hook", cancelCalls.Load(), hook.calls.Load())
	}
	verificationResult <- &testClassifiedError{class: domain.SafeErrorClassPermissionDenied}
	assertClass(t, receiveError(t, failed, "failed Namespace switch did not return"), domain.SafeErrorClassPermissionDenied)
	if view := manager.View(); view.State != ScopeStateActive || view.Generation != oldScope.Generation || view.Scope == nil || *view.Scope != oldScope {
		t.Fatalf("failed Namespace switch changed scope: %#v", view)
	}
	if cancelCalls.Load() != 0 || client.closeCalls.Load() != 0 {
		t.Fatalf("failed Namespace switch cancel/close calls = %d/%d, want 0/0", cancelCalls.Load(), client.closeCalls.Load())
	}

	succeeded := make(chan struct {
		scope domain.ClusterScope
		err   error
	}, 1)
	go func() {
		scope, switchErr := manager.SwitchNamespace(context.Background(), "team-b", oldScope.Generation)
		succeeded <- struct {
			scope domain.ClusterScope
			err   error
		}{scope: scope, err: switchErr}
	}()
	receiveSignal(t, verificationStarted, "successful Namespace verification did not start")
	verificationResult <- nil
	result := receiveNamespaceSwitch(t, succeeded)
	if result.err != nil {
		t.Fatalf("SwitchNamespace() error = %v", result.err)
	}
	if result.scope.Namespace != "team-b" || result.scope.Generation != oldScope.Generation+1 {
		t.Fatalf("new Namespace scope = %#v", result.scope)
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("successful Namespace switch cancel calls = %d, want 1", cancelCalls.Load())
	}
	if hook.calls.Load() != 2 {
		t.Fatalf("invalidation hook calls = %d, want 2 including initial activation", hook.calls.Load())
	}
	if client.closeCalls.Load() != 0 {
		t.Fatalf("Namespace switch closed Context client %d times", client.closeCalls.Load())
	}
}

func TestScopeManagerDropsBlockedOldGenerationResourceResult(t *testing.T) {
	oldClient := &fakeScopeClient{context: ContextCandidate{Name: "old-context", DefaultNamespace: "team-a"}}
	newClient := &fakeScopeClient{context: ContextCandidate{Name: "new-context", DefaultNamespace: "team-b"}}
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) {
			return []ContextCandidate{oldClient.context, newClient.context}, nil
		},
		createFn: func(_ context.Context, name string) (ScopeClient, error) {
			if name == oldClient.context.Name {
				return oldClient, nil
			}
			return newClient, nil
		},
	}
	namespaces := &fakeNamespaceReader{verifyFn: func(context.Context, ScopeClient, string) error { return nil }}
	callStarted := make(chan struct{}, 1)
	releaseResult := make(chan struct{})
	usedOldClient := make(chan bool, 1)
	resources := &fakeResourceService{listFn: func(_ context.Context, client ScopeClient, scope domain.ClusterScope, kind domain.ResourceKind, _ int) (domain.ResourceList, error) {
		usedOldClient <- client == oldClient
		callStarted <- struct{}{}
		<-releaseResult
		return domain.ResourceList{Items: []domain.ResourceSummary{resourceSummary(scope.Namespace, kind, "late-result")}}, nil
	}}
	manager := newTestScopeManager(t, factory, namespaces, resources, &fakeScopeInvalidationHook{})
	oldScope, err := manager.SwitchContext(context.Background(), oldClient.context.Name, 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}

	readResult := make(chan struct {
		list domain.ResourceList
		err  error
	}, 1)
	go func() {
		list, readErr := manager.ListResources(context.Background(), oldScope, domain.ResourceKindPod, 20)
		readResult <- struct {
			list domain.ResourceList
			err  error
		}{list: list, err: readErr}
	}()
	receiveSignal(t, callStarted, "resource read did not start")
	if !<-usedOldClient {
		t.Fatal("blocked resource read did not use the old Context client")
	}
	newScope, err := manager.SwitchContext(context.Background(), newClient.context.Name, oldScope.Generation)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	if oldClient.closeCalls.Load() != 1 {
		t.Fatalf("old Context client Close() calls = %d, want 1", oldClient.closeCalls.Load())
	}
	close(releaseResult)
	result := receiveResourceRead(t, readResult)
	assertClass(t, result.err, domain.SafeErrorClassStaleScope)
	if len(result.list.Items) != 0 {
		t.Fatalf("stale resource result returned %d items, want 0", len(result.list.Items))
	}
	if resources.listCalls.Load() != 1 {
		t.Fatalf("ResourceService ListResources() calls = %d, want 1", resources.listCalls.Load())
	}
	manager.mu.RLock()
	cacheEntries := len(manager.resourceCache)
	manager.mu.RUnlock()
	if cacheEntries != 0 {
		t.Fatalf("stale resource result populated %d cache entries", cacheEntries)
	}
	_, err = manager.GetResource(context.Background(), oldScope, domain.ResourceRef{
		APIVersion: "v1",
		Kind:       "Pod",
		Namespace:  oldScope.Namespace,
		Name:       "stale-pod",
	})
	assertClass(t, err, domain.SafeErrorClassStaleScope)
	if resources.getCalls.Load() != 0 {
		t.Fatalf("stale pre-call GetResource() calls = %d, want 0", resources.getCalls.Load())
	}
	if current, ok := manager.CurrentScope(); !ok || current != newScope {
		t.Fatalf("CurrentScope() = %#v, %v; want %#v, true", current, ok, newScope)
	}
}

func TestScopeManagerCancellationDuringContextCreationFailsUnavailable(t *testing.T) {
	client := &fakeScopeClient{context: ContextCandidate{Name: "selected", DefaultNamespace: "team-a"}}
	createStarted := make(chan struct{}, 1)
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) { return []ContextCandidate{client.context}, nil },
		createFn: func(ctx context.Context, _ string) (ScopeClient, error) {
			createStarted <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	manager := newTestScopeManager(t, factory, &fakeNamespaceReader{}, &fakeResourceService{}, &fakeScopeInvalidationHook{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := manager.SwitchContext(ctx, client.context.Name, 0)
		result <- err
	}()
	receiveSignal(t, createStarted, "Context creation did not start")
	cancel()
	assertClass(t, receiveError(t, result, "cancelled Context creation did not return"), domain.SafeErrorClassCancelled)
	if view := manager.View(); view.State != ScopeStateUnavailable || view.Generation != 1 || view.Scope != nil {
		t.Fatalf("View() after cancellation = %#v", view)
	}
}

func TestScopeManagerCachesListsOnlyWithinOneGeneration(t *testing.T) {
	client := &fakeScopeClient{context: ContextCandidate{Name: "selected", DefaultNamespace: "team-a"}}
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) { return []ContextCandidate{client.context}, nil },
		createFn:   func(context.Context, string) (ScopeClient, error) { return client, nil },
	}
	namespaces := &fakeNamespaceReader{
		verifyFn: func(context.Context, ScopeClient, string) error { return nil },
		listFn: func(context.Context, ScopeClient, int) (domain.NamespaceList, error) {
			return domain.NamespaceList{Items: []domain.NamespaceSummary{{Name: "team-a"}}}, nil
		},
	}
	resources := &fakeResourceService{listFn: func(_ context.Context, _ ScopeClient, scope domain.ClusterScope, kind domain.ResourceKind, _ int) (domain.ResourceList, error) {
		return domain.ResourceList{Items: []domain.ResourceSummary{resourceSummary(scope.Namespace, kind, "cached")}}, nil
	}}
	manager := newTestScopeManager(t, factory, namespaces, resources, &fakeScopeInvalidationHook{})
	oldScope, err := manager.SwitchContext(context.Background(), client.context.Name, 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	for range 2 {
		if _, err := manager.ListNamespaces(context.Background(), oldScope, 50); err != nil {
			t.Fatalf("ListNamespaces() error = %v", err)
		}
		if _, err := manager.ListResources(context.Background(), oldScope, domain.ResourceKindPod, 20); err != nil {
			t.Fatalf("ListResources() error = %v", err)
		}
	}
	if namespaces.listCalls.Load() != 1 || resources.listCalls.Load() != 1 {
		t.Fatalf("same-generation list calls = %d/%d, want 1/1", namespaces.listCalls.Load(), resources.listCalls.Load())
	}
	newScope, err := manager.SwitchNamespace(context.Background(), "team-b", oldScope.Generation)
	if err != nil {
		t.Fatalf("SwitchNamespace() error = %v", err)
	}
	if _, err := manager.ListNamespaces(context.Background(), newScope, 50); err != nil {
		t.Fatalf("new-generation ListNamespaces() error = %v", err)
	}
	if _, err := manager.ListResources(context.Background(), newScope, domain.ResourceKindPod, 20); err != nil {
		t.Fatalf("new-generation ListResources() error = %v", err)
	}
	if namespaces.listCalls.Load() != 2 || resources.listCalls.Load() != 2 {
		t.Fatalf("cross-generation list calls = %d/%d, want 2/2", namespaces.listCalls.Load(), resources.listCalls.Load())
	}
}

func TestScopeManagerRejectsStaleSwitchBeforeCandidateOrClientCalls(t *testing.T) {
	client := &fakeScopeClient{context: ContextCandidate{Name: "selected", DefaultNamespace: "team-a"}}
	var contextCalls atomic.Int64
	var createCalls atomic.Int64
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) {
			contextCalls.Add(1)
			return []ContextCandidate{client.context}, nil
		},
		createFn: func(context.Context, string) (ScopeClient, error) {
			createCalls.Add(1)
			return client, nil
		},
	}
	namespaces := &fakeNamespaceReader{verifyFn: func(context.Context, ScopeClient, string) error { return nil }}
	manager := newTestScopeManager(t, factory, namespaces, &fakeResourceService{}, &fakeScopeInvalidationHook{})
	scope, err := manager.SwitchContext(context.Background(), client.context.Name, 0)
	if err != nil {
		t.Fatalf("initial SwitchContext() error = %v", err)
	}
	_, err = manager.SwitchContext(context.Background(), client.context.Name, scope.Generation-1)
	assertClass(t, err, domain.SafeErrorClassStaleScope)
	if contextCalls.Load() != 1 || createCalls.Load() != 1 || namespaces.verifyCalls.Load() != 1 {
		t.Fatalf("stale switch calls Contexts/Create/Verify = %d/%d/%d, want 1/1/1 initial calls only", contextCalls.Load(), createCalls.Load(), namespaces.verifyCalls.Load())
	}
}

func TestScopeManagerInvalidationHookFailureFailsClosedBeforeTargetCreation(t *testing.T) {
	oldClient := &fakeScopeClient{context: ContextCandidate{Name: "old-context", DefaultNamespace: "team-a"}}
	targetClient := &fakeScopeClient{context: ContextCandidate{Name: "target-context", DefaultNamespace: "team-b"}}
	var targetCreateCalls atomic.Int64
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) {
			return []ContextCandidate{oldClient.context, targetClient.context}, nil
		},
		createFn: func(_ context.Context, name string) (ScopeClient, error) {
			if name == oldClient.context.Name {
				return oldClient, nil
			}
			targetCreateCalls.Add(1)
			return targetClient, nil
		},
	}
	namespaces := &fakeNamespaceReader{verifyFn: func(context.Context, ScopeClient, string) error { return nil }}
	hook := &fakeScopeInvalidationHook{}
	manager := newTestScopeManager(t, factory, namespaces, &fakeResourceService{}, hook)
	scope, err := manager.SwitchContext(context.Background(), oldClient.context.Name, 0)
	if err != nil {
		t.Fatalf("initial SwitchContext() error = %v", err)
	}
	hook.invalidateFn = func(int64) error { return errors.New("generated hook failure") }
	_, err = manager.SwitchContext(context.Background(), targetClient.context.Name, scope.Generation)
	assertClass(t, err, domain.SafeErrorClassInternal)
	if view := manager.View(); view.State != ScopeStateUnavailable || view.Generation != scope.Generation+1 || view.Scope != nil {
		t.Fatalf("View() after hook failure = %#v", view)
	}
	if oldClient.closeCalls.Load() != 1 {
		t.Fatalf("old client Close() calls = %d, want 1", oldClient.closeCalls.Load())
	}
	if targetCreateCalls.Load() != 0 {
		t.Fatalf("target Create() calls = %d, want 0", targetCreateCalls.Load())
	}
}

func TestScopeManagerCloseInvalidatesRunSelectionCachesAndClient(t *testing.T) {
	client := &fakeScopeClient{context: ContextCandidate{Name: "selected", DefaultNamespace: "team-a"}}
	factory := &fakeScopeFactory{
		contextsFn: func(context.Context) ([]ContextCandidate, error) { return []ContextCandidate{client.context}, nil },
		createFn:   func(context.Context, string) (ScopeClient, error) { return client, nil },
	}
	namespaces := &fakeNamespaceReader{
		verifyFn: func(context.Context, ScopeClient, string) error { return nil },
		listFn: func(context.Context, ScopeClient, int) (domain.NamespaceList, error) {
			return domain.NamespaceList{Items: []domain.NamespaceSummary{{Name: "team-a"}}}, nil
		},
	}
	resources := &fakeResourceService{listFn: func(_ context.Context, _ ScopeClient, scope domain.ClusterScope, kind domain.ResourceKind, _ int) (domain.ResourceList, error) {
		return domain.ResourceList{Items: []domain.ResourceSummary{resourceSummary(scope.Namespace, kind, "sample-pod")}}, nil
	}}
	hook := &fakeScopeInvalidationHook{}
	manager := newTestScopeManager(t, factory, namespaces, resources, hook)
	scope, err := manager.SwitchContext(context.Background(), client.context.Name, 0)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	if err := manager.SelectResource(scope, domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"}); err != nil {
		t.Fatalf("SelectResource() error = %v", err)
	}
	if _, err := manager.ListNamespaces(context.Background(), scope, 50); err != nil {
		t.Fatalf("ListNamespaces() error = %v", err)
	}
	if _, err := manager.ListResources(context.Background(), scope, domain.ResourceKindPod, 20); err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	var cancelCalls atomic.Int64
	if err := manager.BindRun(scope, domain.AgentRunID("00000000-0000-7000-8000-000000000003"), func() { cancelCalls.Add(1) }); err != nil {
		t.Fatalf("BindRun() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if view := manager.View(); view.State != ScopeStateClosed || view.Generation != scope.Generation+1 || view.Scope != nil {
		t.Fatalf("View() after Close = %#v", view)
	}
	if cancelCalls.Load() != 1 || client.closeCalls.Load() != 1 || hook.calls.Load() != 2 {
		t.Fatalf("Close cancel/client/hook calls = %d/%d/%d, want 1/1/2", cancelCalls.Load(), client.closeCalls.Load(), hook.calls.Load())
	}
	manager.mu.RLock()
	selected := manager.selectedResource
	namespaceCacheEntries := len(manager.namespaceCache)
	resourceCacheEntries := len(manager.resourceCache)
	manager.mu.RUnlock()
	if selected != nil || namespaceCacheEntries != 0 || resourceCacheEntries != 0 {
		t.Fatalf("Close retained selected/cache state: selected=%#v namespace=%d resource=%d", selected, namespaceCacheEntries, resourceCacheEntries)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if client.closeCalls.Load() != 1 {
		t.Fatalf("second Close() changed client close calls to %d", client.closeCalls.Load())
	}
}

type fakeScopeFactory struct {
	contextsFn func(context.Context) ([]ContextCandidate, error)
	createFn   func(context.Context, string) (ScopeClient, error)
}

func (factory *fakeScopeFactory) Contexts(ctx context.Context) ([]ContextCandidate, error) {
	if factory.contextsFn == nil {
		return nil, errors.New("unexpected Contexts call")
	}
	return factory.contextsFn(ctx)
}

func (factory *fakeScopeFactory) Create(ctx context.Context, name string) (ScopeClient, error) {
	if factory.createFn == nil {
		return nil, errors.New("unexpected Create call")
	}
	return factory.createFn(ctx, name)
}

type fakeScopeClient struct {
	context    ContextCandidate
	closeFn    func()
	closeCalls atomic.Int64
}

func (client *fakeScopeClient) Context() ContextCandidate {
	return client.context
}

func (client *fakeScopeClient) Close() {
	client.closeCalls.Add(1)
	if client.closeFn != nil {
		client.closeFn()
	}
}

type fakeNamespaceReader struct {
	verifyFn    func(context.Context, ScopeClient, string) error
	listFn      func(context.Context, ScopeClient, int) (domain.NamespaceList, error)
	verifyCalls atomic.Int64
	listCalls   atomic.Int64
}

func (reader *fakeNamespaceReader) VerifyNamespace(ctx context.Context, client ScopeClient, namespace string) error {
	reader.verifyCalls.Add(1)
	if reader.verifyFn == nil {
		return errors.New("unexpected VerifyNamespace call")
	}
	return reader.verifyFn(ctx, client, namespace)
}

func (reader *fakeNamespaceReader) ListNamespaces(ctx context.Context, client ScopeClient, limit int) (domain.NamespaceList, error) {
	reader.listCalls.Add(1)
	if reader.listFn == nil {
		return domain.NamespaceList{}, errors.New("unexpected ListNamespaces call")
	}
	return reader.listFn(ctx, client, limit)
}

type fakeResourceService struct {
	getFn     func(context.Context, ScopeClient, domain.ClusterScope, domain.ResourceRef) (domain.ResourceSummary, error)
	listFn    func(context.Context, ScopeClient, domain.ClusterScope, domain.ResourceKind, int) (domain.ResourceList, error)
	getCalls  atomic.Int64
	listCalls atomic.Int64
}

func (service *fakeResourceService) GetResource(ctx context.Context, client ScopeClient, scope domain.ClusterScope, reference domain.ResourceRef) (domain.ResourceSummary, error) {
	service.getCalls.Add(1)
	if service.getFn == nil {
		return domain.ResourceSummary{}, errors.New("unexpected GetResource call")
	}
	return service.getFn(ctx, client, scope, reference)
}

func (service *fakeResourceService) ListResources(ctx context.Context, client ScopeClient, scope domain.ClusterScope, kind domain.ResourceKind, limit int) (domain.ResourceList, error) {
	service.listCalls.Add(1)
	if service.listFn == nil {
		return domain.ResourceList{}, errors.New("unexpected ListResources call")
	}
	return service.listFn(ctx, client, scope, kind, limit)
}

type fakeScopeInvalidationHook struct {
	invalidateFn func(int64) error
	calls        atomic.Int64
}

func (hook *fakeScopeInvalidationHook) InvalidateScope(generation int64) error {
	hook.calls.Add(1)
	if hook.invalidateFn == nil {
		return nil
	}
	return hook.invalidateFn(generation)
}

type testClassifiedError struct {
	class domain.SafeErrorClass
}

func (err *testClassifiedError) Error() string                { return "safe test failure" }
func (err *testClassifiedError) SafeMessage() string          { return err.Error() }
func (err *testClassifiedError) Class() domain.SafeErrorClass { return err.class }
func (err *testClassifiedError) ErrorCode() string            { return "safe_test_failure" }
func (err *testClassifiedError) Operation() string            { return "test" }
func (err *testClassifiedError) Retryable() bool              { return false }

type scopeEventRecorder struct {
	mu     sync.Mutex
	values []string
}

func (recorder *scopeEventRecorder) add(value string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.values = append(recorder.values, value)
}

func (recorder *scopeEventRecorder) reset() {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.values = nil
}

func (recorder *scopeEventRecorder) events() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.values...)
}

type sequenceClock struct {
	mu   sync.Mutex
	next time.Time
}

func (clock *sequenceClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	result := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return result
}

func newTestScopeManager(t *testing.T, factory ScopeClientFactory, namespaces NamespaceReader, resources ResourceService, hook ScopeInvalidationHook) *ScopeManager {
	t.Helper()
	clock := &sequenceClock{next: time.UnixMilli(1).UTC()}
	manager, err := NewScopeManager(factory, namespaces, resources, hook, clock.Now)
	if err != nil {
		t.Fatalf("NewScopeManager() error = %v", err)
	}
	return manager
}

func resourceSummary(namespace string, kind domain.ResourceKind, name string) domain.ResourceSummary {
	return domain.ResourceSummary{
		Reference: domain.ResourceRef{
			APIVersion: kind.APIVersion(),
			Kind:       string(kind),
			Namespace:  namespace,
			Name:       name,
		},
		Status: domain.ResourceStatus{Phase: "Running"},
	}
}

func assertClass(t *testing.T, err error, want domain.SafeErrorClass) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q", want)
	}
	var classified interface {
		Class() domain.SafeErrorClass
	}
	if !errors.As(err, &classified) {
		t.Fatalf("error %T does not expose a safe class: %v", err, err)
	}
	if got := classified.Class(); got != want {
		t.Fatalf("error class = %q, want %q (error %v)", got, want, err)
	}
}

func receiveSignal(t *testing.T, channel <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatal(failure)
	}
}

func receiveError(t *testing.T, channel <-chan error, failure string) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal(failure)
		return nil
	}
}

func receiveNamespaceSwitch(t *testing.T, channel <-chan struct {
	scope domain.ClusterScope
	err   error
}) struct {
	scope domain.ClusterScope
	err   error
} {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("Namespace switch did not return")
		return struct {
			scope domain.ClusterScope
			err   error
		}{}
	}
}

func receiveResourceRead(t *testing.T, channel <-chan struct {
	list domain.ResourceList
	err  error
}) struct {
	list domain.ResourceList
	err  error
} {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("resource read did not return")
		return struct {
			list domain.ResourceList
			err  error
		}{}
	}
}

func assertNoKubernetesVendorType(t *testing.T, current reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if current == nil || seen[current] {
		return
	}
	seen[current] = true
	if strings.HasPrefix(current.PkgPath(), "k8s.io/") {
		t.Fatalf("Application boundary contains Kubernetes vendor type %s", current)
	}
	switch current.Kind() {
	case reflect.Array, reflect.Chan, reflect.Pointer, reflect.Slice:
		assertNoKubernetesVendorType(t, current.Elem(), seen)
	case reflect.Func:
		for index := 0; index < current.NumIn(); index++ {
			assertNoKubernetesVendorType(t, current.In(index), seen)
		}
		for index := 0; index < current.NumOut(); index++ {
			assertNoKubernetesVendorType(t, current.Out(index), seen)
		}
	case reflect.Interface:
		for index := 0; index < current.NumMethod(); index++ {
			assertNoKubernetesVendorType(t, current.Method(index).Type, seen)
		}
	case reflect.Map:
		assertNoKubernetesVendorType(t, current.Key(), seen)
		assertNoKubernetesVendorType(t, current.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			if field.IsExported() {
				assertNoKubernetesVendorType(t, field.Type, seen)
			}
		}
	}
}
