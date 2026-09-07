package sessionlock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSharedOwnershipBlocksExclusiveDeletionAndReleasesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "process.lock")
	first, err := Acquire(context.Background(), path, Shared)
	if err != nil {
		t.Fatalf("Acquire(shared) error = %v", err)
	}
	defer first.Close()
	second, err := Acquire(context.Background(), path, Shared)
	if err != nil {
		t.Fatalf("Acquire(second shared) error = %v", err)
	}
	if _, err := Acquire(context.Background(), path, Exclusive); !errors.Is(err, ErrActive) {
		t.Fatalf("Acquire(exclusive with owners) error = %v, want ErrActive", err)
	}
	if err := first.Upgrade(context.Background()); !errors.Is(err, ErrActive) {
		t.Fatalf("Upgrade(with another owner) error = %v, want ErrActive", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
	if err := first.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade(single owner) error = %v", err)
	}
	if err := first.Downgrade(); err != nil {
		t.Fatalf("Downgrade() error = %v", err)
	}
}

func TestInvalidOrCancelledLockRequestsCreateNoAuthority(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(cancelled, filepath.Join(t.TempDir(), "cancelled.lock"), Exclusive); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Acquire(cancelled) error = %v", err)
	}
	if _, err := Acquire(context.Background(), "", Shared); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Acquire(empty path) error = %v", err)
	}
}
