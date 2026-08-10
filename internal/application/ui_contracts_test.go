package application

import (
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestStructuredUICommandsRejectMixedOrUnboundPickerData(t *testing.T) {
	t.Parallel()

	scope := domain.ScopeCandidate{Context: "development", Namespace: "payments"}
	resource := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api"}
	tests := []struct {
		name    string
		command UICommand
		wantErr bool
	}{
		{
			name:    "activate scope",
			command: UICommand{Kind: UICommandActivateScope, RequestID: 1, ExpectedScopeGeneration: 7, Scope: &scope},
		},
		{
			name:    "select Resource",
			command: UICommand{Kind: UICommandSelectResource, RequestID: 2, ExpectedScopeGeneration: 7, Resource: &resource},
		},
		{
			name:    "scope without request identity",
			command: UICommand{Kind: UICommandActivateScope, ExpectedScopeGeneration: 7, Scope: &scope},
			wantErr: true,
		},
		{
			name: "mixed scope and Resource",
			command: UICommand{
				Kind: UICommandActivateScope, RequestID: 3, ExpectedScopeGeneration: 7,
				Scope: &scope, Resource: &resource,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if gotErr := tt.command.Validate() != nil; gotErr != tt.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", tt.command.Validate(), tt.wantErr)
			}
		})
	}
}

func TestUIResumeAndSelectionResultsValidateExclusiveSafeShapes(t *testing.T) {
	t.Parallel()

	const sessionID domain.SessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a23"
	request := UIResumeRequest{RequestID: 1, Mode: UIResumeExact, SessionID: sessionID}
	if err := request.Validate(); err != nil {
		t.Fatalf("resume request validation error = %v", err)
	}
	result := UIResumeResult{
		RequestID: 1, Mode: UIResumeExact,
		Session: &UIResumedSession{Session: UISessionCandidate{
			ID: sessionID, UpdatedAtUnixMillis: 1, PrivacyMode: domain.PrivacyModeStandard,
		}},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("resume result validation error = %v", err)
	}
	result.Failure = UIQueryUnavailable
	if result.Validate() == nil {
		t.Fatal("resume result accepted both a Session and a failure")
	}

	scopeResult := UIScopeResult{
		RequestID: 2, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "development", Namespace: "payments", ReadOnly: true,
	}
	if err := scopeResult.Validate(); err != nil {
		t.Fatalf("scope result validation error = %v", err)
	}
	scopeResult.ScopeGeneration = scopeResult.ExpectedGeneration
	if scopeResult.Validate() == nil {
		t.Fatal("scope result accepted a non-advancing generation")
	}

	resource := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api"}
	resourceResult := UIResourceSelectionResult{RequestID: 3, ScopeGeneration: 8, Resource: &resource}
	if err := resourceResult.Validate(); err != nil {
		t.Fatalf("Resource result validation error = %v", err)
	}
	resourceResult.Cleared = true
	if resourceResult.Validate() == nil {
		t.Fatal("Resource result accepted selected and cleared state together")
	}
}
