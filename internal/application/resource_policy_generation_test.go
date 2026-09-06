package application

import (
	"context"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestResourceReadAuthorityDistinguishesCancellationFromPolicyChange(t *testing.T) {
	permissions, err := NewPermissionManager(PermissionPolicy{
		Profile: domain.PermissionProfileAsk, Generation: 1,
	})
	if err != nil {
		t.Fatalf("NewPermissionManager() error = %v", err)
	}
	authority, err := NewResourceReadAuthority(domain.DefaultResourcePolicyCatalog(), permissions)
	if err != nil {
		t.Fatalf("NewResourceReadAuthority() error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !authority.CurrentPolicyGeneration(cancelled, 1) {
		t.Fatal("a cancelled operation context changed an otherwise-current policy generation")
	}
	if authority.CurrentPolicyGeneration(cancelled, 2) {
		t.Fatal("a cancelled operation context made a stale policy generation current")
	}
}
