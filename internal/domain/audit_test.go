package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAuditEventValidationUsesClosedTypesAndTypedDetails(t *testing.T) {
	sessionID := SessionID("00000000-0000-7000-8000-000000001401")
	runID := AgentRunID("00000000-0000-7000-8000-000000001402")
	operation := "execute_tool"
	errorClass := SafeErrorClassPermissionDenied
	toolName := ToolNameGetEvents
	sequence := 2
	detailCode := "read_forbidden"
	event := AuditEvent{
		ID:        "00000000-0000-7000-8000-000000001403",
		SessionID: &sessionID,
		RunID:     &runID,
		Type:      AuditEventToolDenied,
		Actor:     AuditActorSystem,
		Outcome:   AuditOutcomeDenied,
		Scope:     &ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 5},
		Subject:   &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"},
		Details: AuditDetails{
			Operation:  &operation,
			ErrorClass: &errorClass,
			ToolName:   &toolName,
			Sequence:   &sequence,
			DetailCode: &detailCode,
		},
		CorrelationID: "command-1",
		OccurredAt:    time.UnixMilli(50).UTC(),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if event.Type.RetentionClass() != AuditRetentionRead {
		t.Fatalf("RetentionClass() = %q, want %q", event.Type.RetentionClass(), AuditRetentionRead)
	}
	if event.Type.AllowedInMinimalPersistence() {
		t.Fatal("Tool detail AuditEvent was admitted for minimal persistence")
	}
	if !AuditEventWriteIntent.RetentionClass().Valid() || AuditEventWriteIntent.RetentionClass() != AuditRetentionWrite {
		t.Fatal("future write AuditEvent was not classified as write audit")
	}
	if !AuditEventRunStarted.AllowedInMinimalPersistence() {
		t.Fatal("run lifecycle AuditEvent was denied for minimal persistence")
	}
	if AuditEventSessionExportRequested.RetentionClass() != AuditRetentionRead ||
		AuditEventSessionExportRequested.AllowedInMinimalPersistence() {
		t.Fatal("Session export audit was not classified as standard-persistence read audit")
	}
	clusterSubject := event
	clusterSubject.Subject = &ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a"}
	if err := clusterSubject.Validate(); err != nil {
		t.Fatalf("cluster-scoped audit subject Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AuditEvent)
	}{
		{name: "unknown event", mutate: func(value *AuditEvent) { value.Type = "free_form" }},
		{name: "unknown actor", mutate: func(value *AuditEvent) { value.Actor = "remote" }},
		{name: "known Kind API mismatch", mutate: func(value *AuditEvent) { value.Subject.APIVersion = "apps/v1" }},
		{name: "subject without scope", mutate: func(value *AuditEvent) { value.Scope = nil }},
		{name: "oversized correlation", mutate: func(value *AuditEvent) { value.CorrelationID = strings.Repeat("c", maxAuditCorrelationIDBytes+1) }},
		{name: "oversized detail", mutate: func(value *AuditEvent) {
			text := strings.Repeat("d", maxAuditDetailTextBytes+1)
			value.Details.DetailCode = &text
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := event
			if value.Subject != nil {
				subject := *value.Subject
				value.Subject = &subject
			}
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestAuditEventAllowsOnlyDigestBoundCrossNamespaceDrainPhases(t *testing.T) {
	newEvent := func() AuditEvent {
		sessionID := SessionID("00000000-0000-7000-8000-000000001411")
		runID := AgentRunID("00000000-0000-7000-8000-000000001412")
		operation := string(ActionOperationDrainNode)
		return AuditEvent{
			ID: "00000000-0000-7000-8000-000000001413", SessionID: &sessionID, RunID: &runID,
			Type: AuditEventWriteAttempted, Actor: AuditActorSystem, Outcome: AuditOutcomeSuccess,
			Scope:         &ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 5},
			Subject:       &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "9"},
			Details:       AuditDetails{Operation: &operation},
			CorrelationID: "00000000-0000-7000-8000-000000001414",
			IntegrityHash: strings.Repeat("a", 64), OccurredAt: time.UnixMilli(50).UTC(),
		}
	}
	if err := newEvent().Validate(); err != nil {
		t.Fatalf("cross-Namespace drain phase Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*AuditEvent)
	}{
		{name: "another operation", mutate: func(event *AuditEvent) {
			operation := string(ActionOperationScaleWorkload)
			event.Details.Operation = &operation
		}},
		{name: "final result type", mutate: func(event *AuditEvent) { event.Type = AuditEventWriteVerified }},
		{name: "missing digest", mutate: func(event *AuditEvent) { event.IntegrityHash = "" }},
		{name: "unbound correlation", mutate: func(event *AuditEvent) { event.CorrelationID = "not-an-approval" }},
		{name: "non-system actor", mutate: func(event *AuditEvent) { event.Actor = AuditActorUser }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := newEvent()
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("cross-Namespace audit exception was broadened")
			}
		})
	}
}

func TestSettingValidationHasExplicitAllowlistAndSensitiveDenylist(t *testing.T) {
	setting := Setting{
		Key:           SettingOperationalDetailRetentionDays,
		IntegerValue:  30,
		SchemaVersion: 1,
		UpdatedAt:     time.UnixMilli(60).UTC(),
	}
	if err := setting.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !DeniedSettingKey(SettingKey("model." + "api" + "_key")) {
		t.Fatal("credential-shaped setting key was not denied")
	}
	if !DeniedSettingKey(SettingKey("model.client_" + "cert")) {
		t.Fatal("certificate-shaped setting key was not denied")
	}

	tests := []struct {
		name   string
		mutate func(*Setting)
	}{
		{name: "unknown key", mutate: func(value *Setting) { value.Key = "ui.future" }},
		{name: "sensitive key", mutate: func(value *Setting) { value.Key = SettingKey("model." + "token") }},
		{name: "negative days", mutate: func(value *Setting) { value.IntegerValue = -1 }},
		{name: "unbounded days", mutate: func(value *Setting) { value.IntegerValue = maxOperationalDetailRetentionDays + 1 }},
		{name: "schema", mutate: func(value *Setting) { value.SchemaVersion = 0 }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := setting
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
