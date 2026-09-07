//go:build !windows

package sessionlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type unixLock struct {
	file *os.File
}

func acquirePlatform(path string, mode Mode) (*unixLock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, ErrInvalid
	}
	file := os.NewFile(uintptr(fd), path)
	operation := unix.LOCK_SH | unix.LOCK_NB
	if mode == Exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(fd, operation); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrActive
		}
		return nil, ErrInvalid
	}
	return &unixLock{file: file}, nil
}

func (lock *unixLock) relock(mode Mode) error {
	if lock == nil || lock.file == nil {
		return ErrInvalid
	}
	operation := unix.LOCK_SH | unix.LOCK_NB
	if mode == Exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(lock.file.Fd()), operation); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrActive
		}
		return ErrInvalid
	}
	return nil
}

func (lock *unixLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(err, closeErr)
}
