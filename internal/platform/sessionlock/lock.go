package sessionlock

import (
	"context"
	"errors"
)

// Mode distinguishes ordinary shared process ownership from an exclusive
// inactive-Session deletion window.
type Mode uint8

const (
	Shared Mode = iota + 1
	Exclusive
)

var (
	ErrInvalid = errors.New("the Kupilot process lock request is invalid")
	ErrActive  = errors.New("another Kupilot process may be active")
)

// Acquire takes one non-blocking OS lock. No PID, Session identifier, deletion
// plan, or authority is written into the lock file.
type platformLock interface {
	Close() error
	relock(Mode) error
}

// Lock owns one process-lifetime file descriptor.
type Lock struct {
	inner platformLock
	mode  Mode
}

func Acquire(ctx context.Context, path string, mode Mode) (*Lock, error) {
	if ctx == nil || ctx.Err() != nil || path == "" || mode != Shared && mode != Exclusive {
		return nil, ErrInvalid
	}
	inner, err := acquirePlatform(path, mode)
	if err != nil {
		return nil, err
	}
	return &Lock{inner: inner, mode: mode}, nil
}

// Upgrade attempts a non-blocking exclusive conversion.
func (lock *Lock) Upgrade(ctx context.Context) error {
	if lock == nil || lock.inner == nil || ctx == nil || ctx.Err() != nil {
		return ErrInvalid
	}
	if lock.mode == Exclusive {
		return nil
	}
	if err := lock.inner.relock(Exclusive); err != nil {
		_ = lock.inner.relock(Shared)
		return err
	}
	lock.mode = Exclusive
	return nil
}

// Downgrade restores ordinary shared ownership after a batch attempt.
func (lock *Lock) Downgrade() error {
	if lock == nil || lock.inner == nil {
		return nil
	}
	if lock.mode == Shared {
		return nil
	}
	if err := lock.inner.relock(Shared); err != nil {
		return err
	}
	lock.mode = Shared
	return nil
}

func (lock *Lock) Close() error {
	if lock == nil || lock.inner == nil {
		return nil
	}
	err := lock.inner.Close()
	lock.inner = nil
	return err
}
