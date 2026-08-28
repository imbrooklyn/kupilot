package logging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type rotatingWriter struct {
	mu           sync.Mutex
	directory    string
	maxFileBytes int64
	maxFiles     int
	maxAge       time.Duration
	now          func() time.Time
	file         *os.File
	size         int64
	openedAt     time.Time
	closed       bool
}

func openRotatingWriter(ctx context.Context, options Options) (*rotatingWriter, error) {
	if err := ensurePrivateDirectory(options.Directory); err != nil {
		return nil, err
	}
	writer := &rotatingWriter{
		directory:    options.Directory,
		maxFileBytes: options.MaxFileBytes,
		maxFiles:     options.MaxFiles,
		maxAge:       options.MaxAge,
		now:          options.Now,
	}
	if err := writer.prune(ctx); err != nil {
		return nil, err
	}
	if err := writer.openCurrent(); err != nil {
		return nil, err
	}
	return writer, nil
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return newSafeError(ClassInternal, "log_directory_unavailable", "KuPilot could not create its local log directory.")
		}
		created = true
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return newSafeError(ClassInternal, "log_directory_unavailable", "KuPilot could not inspect its local log directory.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return newSafeError(ClassConfigurationInvalid, "log_path_unsafe", "The local log directory must not be a symbolic link.")
	}
	if created {
		if err := os.Chmod(directory, 0o700); err != nil {
			return newSafeError(ClassInternal, "log_directory_unavailable", "KuPilot could not protect its new local log directory.")
		}
	}
	return nil
}

func (writer *rotatingWriter) prune(ctx context.Context) error {
	now := writer.now().UTC()
	for index := 0; index < DefaultMaxFiles; index++ {
		if err := ctx.Err(); err != nil {
			return newSafeError(ClassCancelled, "log_open_cancelled", "Local logging initialization was cancelled.")
		}
		name := writer.path(index)
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return newSafeError(ClassInternal, "log_file_unavailable", "KuPilot could not inspect its local log files.")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newSafeError(ClassConfigurationInvalid, "log_path_unsafe", "Local log files must be regular files and must not be symbolic links.")
		}
		if index >= writer.maxFiles || info.Size() > writer.maxFileBytes || now.Sub(info.ModTime()) >= writer.maxAge {
			if err := os.Remove(name); err != nil {
				return newSafeError(ClassInternal, "log_rotation_failed", "KuPilot could not enforce the local log retention ceiling.")
			}
		}
	}
	return nil
}

func (writer *rotatingWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed || writer.file == nil {
		return 0, errors.New("local log is closed")
	}
	if int64(len(value)) > writer.maxFileBytes {
		return 0, errors.New("local log record exceeds the file limit")
	}
	now := writer.now().UTC()
	if writer.size+int64(len(value)) > writer.maxFileBytes || now.Sub(writer.openedAt) >= writer.maxAge {
		if err := writer.rotate(); err != nil {
			return 0, errors.New("local log rotation failed")
		}
	}
	written, err := writer.file.Write(value)
	writer.size += int64(written)
	return written, err
}

func (writer *rotatingWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}

func (writer *rotatingWriter) rotate() error {
	if writer.file != nil {
		if err := writer.file.Close(); err != nil {
			return err
		}
		writer.file = nil
	}
	if writer.maxFiles == 1 {
		if err := removeIfExists(writer.path(0)); err != nil {
			return err
		}
	} else {
		for index := writer.maxFiles - 1; index >= 1; index-- {
			destination := writer.path(index)
			if err := removeIfExists(destination); err != nil {
				return err
			}
			source := writer.path(index - 1)
			if err := renameIfExists(source, destination); err != nil {
				return err
			}
		}
	}
	return writer.openCurrent()
}

func (writer *rotatingWriter) openCurrent() error {
	name := writer.path(0)
	info, err := os.Lstat(name)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newSafeError(ClassConfigurationInvalid, "log_path_unsafe", "The current local log must be a regular file and must not be a symbolic link.")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return newSafeError(ClassInternal, "log_file_unavailable", "KuPilot could not inspect its current local log.")
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return newSafeError(ClassInternal, "log_file_unavailable", "KuPilot could not open its local log file.")
	}
	openedInfo, err := file.Stat()
	pathInfo, pathErr := os.Lstat(name)
	if err != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return newSafeError(ClassConfigurationInvalid, "log_path_unsafe", "The current local log must remain one regular non-symlink file.")
	}
	writer.file = file
	writer.size = openedInfo.Size()
	writer.openedAt = writer.now().UTC()
	if !openedInfo.ModTime().IsZero() && openedInfo.Size() > 0 {
		writer.openedAt = openedInfo.ModTime().UTC()
	}
	return nil
}

func (writer *rotatingWriter) path(index int) string {
	base := filepath.Join(writer.directory, LogFileName)
	if index == 0 {
		return base
	}
	return base + "." + fmt.Sprintf("%d", index)
}

func removeIfExists(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("unsafe local log path")
	}
	return os.Remove(name)
}

func renameIfExists(source, destination string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("unsafe local log path")
	}
	return os.Rename(source, destination)
}
