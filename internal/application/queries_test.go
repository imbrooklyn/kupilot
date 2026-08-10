package application

import (
	"fmt"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestUIStartIntentValidation(t *testing.T) {
	t.Parallel()

	const sessionID domain.SessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a20"
	tests := []struct {
		name    string
		intent  UIStartIntent
		wantErr bool
	}{
		{name: "new", intent: UIStartIntent{Kind: UIStartNew}},
		{name: "resume picker", intent: UIStartIntent{Kind: UIStartResumePicker}},
		{name: "resume ID", intent: UIStartIntent{Kind: UIStartResumeID, SessionID: sessionID}},
		{name: "resume last", intent: UIStartIntent{Kind: UIStartResumeLast}},
		{name: "new with ID", intent: UIStartIntent{Kind: UIStartNew, SessionID: sessionID}, wantErr: true},
		{name: "resume ID without ID", intent: UIStartIntent{Kind: UIStartResumeID}, wantErr: true},
		{name: "unknown", intent: UIStartIntent{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if gotErr := tt.intent.Validate() != nil; gotErr != tt.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", tt.intent.Validate(), tt.wantErr)
			}
		})
	}
}

func TestUICompletionQueryAndResultAreTypedAndBounded(t *testing.T) {
	t.Parallel()

	query := UICompletionQuery{
		RequestID: 7, Kind: UICompletionResource, Filter: "pay",
		ScopeGeneration: 11, Limit: MaxUIQueryCandidates,
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("query validation error = %v", err)
	}

	resources := make([]UIResourceCandidate, MaxUIQueryCandidates)
	for index := range resources {
		resources[index] = UIResourceCandidate{
			APIVersion: "v1", Kind: domain.ResourceKindPod,
			Namespace: "payments", Name: fmt.Sprintf("payment-api-%02d", index), Status: "Ready",
		}
	}
	result := UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind,
		ScopeGeneration: query.ScopeGeneration, Resources: resources,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result validation error = %v", err)
	}

	result.Resources = append(result.Resources, resources[0])
	if result.Validate() == nil {
		t.Fatal("result accepted more than the fixed source limit")
	}
	result.Resources = nil
	result.Contexts = []UIContextCandidate{{Name: "unexpected"}}
	if result.Validate() == nil {
		t.Fatal("result accepted a payload for the wrong completion kind")
	}
}
