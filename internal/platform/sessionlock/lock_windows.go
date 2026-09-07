//go:build windows

package sessionlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type windowsLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquirePlatform(path string, mode Mode) (*windowsLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrInvalid
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if mode == Exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	lock := &windowsLock{file: file}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrActive
		}
		return nil, ErrInvalid
	}
	return lock, nil
}

func (lock *windowsLock) relock(mode Mode) error {
	if lock == nil || lock.file == nil {
		return ErrInvalid
	}
	if err := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped); err != nil {
		return ErrInvalid
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if mode == Exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(windows.Handle(lock.file.Fd()), flags, 0, 1, 0, &lock.overlapped); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrActive
		}
		return ErrInvalid
	}
	return nil
}

func (lock *windowsLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(err, closeErr)
}
