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

	tests := []struct {
		name   string
		mutate func(*AuditEvent)
	}{
		{name: "unknown event", mutate: func(value *AuditEvent) { value.Type = "free_form" }},
		{name: "unknown actor", mutate: func(value *AuditEvent) { value.Actor = "remote" }},
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
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
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
