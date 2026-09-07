package application

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestUIHistoryMessagesUseRoleSpecificContentLimits(t *testing.T) {
	t.Parallel()

	assistant := UIHistoryMessage{
		Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown,
		Content: strings.Repeat("a", MaxAnswerMarkdownBytes),
	}
	if !assistant.valid() {
		t.Fatal("assistant history at the answer limit was rejected")
	}
	assistant.Content += "a"
	if assistant.valid() {
		t.Fatal("assistant history over the answer limit was accepted")
	}

	user := UIHistoryMessage{
		Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain,
		Content: strings.Repeat("a", MaxQuestionBytes+1),
	}
	if user.valid() {
		t.Fatal("user history over the question limit was accepted")
	}
}

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
		{
			name:    "empty rename",
			command: UICommand{Kind: UICommandRenameSession},
		},
		{
			name:    "blank rename",
			command: UICommand{Kind: UICommandRenameSession, Text: " "},
			wantErr: true,
		},
		{
			name:    "status carrying generation",
			command: UICommand{Kind: UICommandShowStatus, ExpectedScopeGeneration: 7},
			wantErr: true,
		},
		{
			name:    "show permissions",
			command: UICommand{Kind: UICommandShowPermissions, RequestID: 4},
		},
		{
			name: "change permission",
			command: UICommand{
				Kind: UICommandChangePermission, RequestID: 5, ExpectedPolicyGeneration: 3,
				PermissionProfile: domain.PermissionProfileAutoReview,
			},
		},
		{
			name: "full access without acknowledgement",
			command: UICommand{
				Kind: UICommandChangePermission, RequestID: 6, ExpectedPolicyGeneration: 3,
				PermissionProfile: domain.PermissionProfileFullAccess,
			},
			wantErr: true,
		},
		{
			name: "stale-prone permission change without generation",
			command: UICommand{
				Kind: UICommandChangePermission, RequestID: 7,
				PermissionProfile: domain.PermissionProfileAsk,
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
		Session: &UIResumedSession{ResumeRequestID: 1, Session: UISessionCandidate{
			ID: sessionID, LastActivityAtUnixMillis: 1, PrivacyMode: domain.PrivacyModeStandard,
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
	scopeResult.ScopePreferenceDegraded = true
	if err := scopeResult.Validate(); err != nil {
		t.Fatalf("verified scope with preference warning validation error = %v", err)
	}
	failedScope := UIScopeResult{
		RequestID: 3, ExpectedGeneration: 7, ScopeGeneration: 8,
		Failure: UIQueryUnavailable, ScopePreferenceDegraded: true,
	}
	if failedScope.Validate() == nil {
		t.Fatal("failed scope result accepted an impossible preference-write warning")
	}
	scopeResult.ScopePreferenceDegraded = false
	scopeResult.ScopeGeneration = scopeResult.ExpectedGeneration
	if scopeResult.Validate() != nil {
		t.Fatal("scope result rejected a verified no-op generation")
	}
	scopeResult.ScopeGeneration--
	if scopeResult.Validate() == nil {
		t.Fatal("scope result accepted a regressing generation")
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
