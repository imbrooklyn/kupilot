package domain

import (
	"strings"
	"testing"
	"time"
)

func TestSessionValidationAndMinimalPersistenceBoundary(t *testing.T) {
	now := time.UnixMilli(1).UTC()
	summary := "Safe summary"
	standard := Session{
		ID:          "00000000-0000-7000-8000-000000000001",
		Title:       "Safe title",
		Status:      SessionStatusActive,
		PrivacyMode: PrivacyModeStandard,
		LastScope:   &ScopeCandidate{Context: "test-context", Namespace: "test-namespace"},
		SelectedResource: &ResourceRef{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "test-namespace",
			Name:       "sample-pod",
		},
		Summary:        &summary,
		Version:        1,
		CreatedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
	}
	if err := standard.Validate(); err != nil {
		t.Fatalf("standard Validate() error = %v", err)
	}
	if !standard.HasResumableMetadata() {
		t.Fatal("standard active Session is missing resumable metadata")
	}

	minimal := Session{
		ID:             "00000000-0000-7000-8000-000000000002",
		Status:         SessionStatusActive,
		PrivacyMode:    PrivacyModeMinimal,
		Version:        1,
		CreatedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
	}
	if err := minimal.Validate(); err != nil {
		t.Fatalf("minimal Validate() error = %v", err)
	}
	if minimal.HasResumableMetadata() {
		t.Fatal("minimal Session reported resumable metadata")
	}
	minimal.Title = "Must remain in memory"
	if err := minimal.Validate(); err == nil {
		t.Fatal("minimal Session with durable title Validate() error = nil")
	}
}

func TestSessionValidationRejectsIdentifierTextAndSizeViolations(t *testing.T) {
	now := time.UnixMilli(1).UTC()
	valid := Session{
		ID:             "00000000-0000-7000-8000-000000000011",
		Title:          strings.Repeat("a", maxSessionTitleBytes),
		Status:         SessionStatusActive,
		PrivacyMode:    PrivacyModeStandard,
		Version:        1,
		CreatedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("boundary Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Session)
	}{
		{name: "non-v7 ID", mutate: func(value *Session) { value.ID = "00000000-0000-6000-8000-000000000011" }},
		{name: "uppercase ID", mutate: func(value *Session) { value.ID = "00000000-0000-7000-8000-000000000A11" }},
		{name: "oversized title", mutate: func(value *Session) { value.Title += "a" }},
		{name: "invalid UTF-8 title", mutate: func(value *Session) { value.Title = string([]byte{0xff}) }},
		{name: "time regression", mutate: func(value *Session) { value.UpdatedAt = time.UnixMilli(0).UTC() }},
		{name: "activity before creation", mutate: func(value *Session) { value.LastActivityAt = time.UnixMilli(0).UTC() }},
		{name: "activity after update", mutate: func(value *Session) { value.LastActivityAt = value.UpdatedAt.Add(time.Millisecond) }},
		{name: "partial scope", mutate: func(value *Session) { value.LastScope = &ScopeCandidate{Context: "test-context"} }},
		{name: "resource without scope", mutate: func(value *Session) {
			value.SelectedResource = &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"}
		}},
		{name: "resource namespace mismatch", mutate: func(value *Session) {
			value.LastScope = &ScopeCandidate{Context: "test-context", Namespace: "test-namespace"}
			value.SelectedResource = &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "other-namespace", Name: "sample-pod"}
		}},
		{name: "resource outside allowlist", mutate: func(value *Session) {
			value.LastScope = &ScopeCandidate{Context: "test-context", Namespace: "test-namespace"}
			value.SelectedResource = &ResourceRef{APIVersion: "v1", Kind: "Secret", Namespace: "test-namespace", Name: "sample-secret"}
		}},
		{name: "empty present summary", mutate: func(value *Session) { empty := ""; value.Summary = &empty }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := valid
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestResourceRefValidationUsesOperationalTargetAllowlist(t *testing.T) {
	allowed := []ResourceRef{
		{APIVersion: "v1", Kind: "Namespace", Name: "sample-namespace"},
		{APIVersion: "v1", Kind: "Node", Name: "sample-node"},
		{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"},
		{APIVersion: "v1", Kind: "Service", Namespace: "test-namespace", Name: "sample-service"},
		{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "test-namespace", Name: "sample-pvc"},
		{APIVersion: "v1", Kind: "PersistentVolume", Name: "sample-pv"},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "test-namespace", Name: "sample-config"},
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "test-namespace", Name: "sample-deployment"},
		{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "test-namespace", Name: "sample-replicaset"},
		{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "test-namespace", Name: "sample-statefulset"},
		{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: "test-namespace", Name: "sample-daemonset"},
		{APIVersion: "batch/v1", Kind: "Job", Namespace: "test-namespace", Name: "sample-job"},
		{APIVersion: "batch/v1", Kind: "CronJob", Namespace: "test-namespace", Name: "sample-cronjob"},
		{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "test-namespace", Name: "sample-ingress"},
		{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Namespace: "test-namespace", Name: "sample-hpa"},
		{APIVersion: "policy/v1", Kind: "PodDisruptionBudget", Namespace: "test-namespace", Name: "sample-pdb"},
	}
	for _, value := range allowed {
		if err := value.Validate(); err != nil {
			t.Fatalf("Validate(%s %s) error = %v", value.APIVersion, value.Kind, err)
		}
	}

	denied := []ResourceRef{
		{APIVersion: "v1", Kind: "Secret", Namespace: "test-namespace", Name: "sample-secret"},
		{APIVersion: "v1", Kind: "Deployment", Namespace: "test-namespace", Name: "wrong-api-version"},
		{APIVersion: "v1", Kind: "Node", Namespace: "test-namespace", Name: "namespaced-node"},
		{APIVersion: "v1", Kind: "Pod", Name: "namespace-less-pod"},
	}
	for _, value := range denied {
		if err := value.Validate(); err == nil {
			t.Fatalf("Validate(%s %s) error = nil", value.APIVersion, value.Kind)
		}
	}
}
