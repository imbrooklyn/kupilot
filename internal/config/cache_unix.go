//go:build darwin || linux

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func clearCacheContents(ctx context.Context, path string) error {
	return clearCacheContentsWithHook(ctx, path, nil)
}

func clearCacheContentsWithHook(ctx context.Context, path string, beforeEntry func(string)) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		info, inspectErr := os.Lstat(path)
		if inspectErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return newSafeError(ClassConfigurationInvalid, "cache_path_unsafe", "clear_cache", "The KuPilot cache path must be a directory and must not be a symbolic link.")
		}
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "KuPilot could not open its local cache safely.")
	}
	directory := os.NewFile(uintptr(fd), filepath.Base(path))
	if directory == nil {
		_ = unix.Close(fd)
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "KuPilot could not open its local cache safely.")
	}
	defer directory.Close()
	return removeCacheDirectoryEntries(ctx, directory, beforeEntry)
}

func removeCacheDirectoryEntries(ctx context.Context, directory *os.File, beforeEntry func(string)) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache directory could not be read; the cache may be only partially cleared.")
	}
	parentFD := int(directory.Fd())
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return cacheClearCancelled()
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return newSafeError(ClassConfigurationInvalid, "cache_path_unsafe", "clear_cache", "A cache entry could not be resolved safely.")
		}
		if beforeEntry != nil {
			beforeEntry(name)
		}
		childFD, openErr := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr == nil {
			child := os.NewFile(uintptr(childFD), name)
			if child == nil {
				_ = unix.Close(childFD)
				return cacheClearFailed()
			}
			removeErr := removeCacheDirectoryEntries(ctx, child, beforeEntry)
			closeErr := child.Close()
			if removeErr != nil {
				return removeErr
			}
			if closeErr != nil {
				return cacheClearFailed()
			}
			if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
				return cacheClearFailed()
			}
			continue
		}
		if errors.Is(openErr, unix.ENOENT) {
			continue
		}
		if !errors.Is(openErr, unix.ENOTDIR) && !errors.Is(openErr, unix.ELOOP) {
			return cacheClearFailed()
		}
		// unlinkat removes a non-directory entry or the link itself. It never
		// follows a symbolic link. A concurrent type replacement fails safely.
		if err := unix.Unlinkat(parentFD, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return cacheClearFailed()
		}
	}
	return nil
}

func cacheClearCancelled() error {
	return newSafeError(ClassCancelled, "cache_clear_cancelled", "clear_cache", "Cache clearing was cancelled before every entry could be removed.")
}

func cacheClearFailed() error {
	return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache entry could not be removed; the cache may be only partially cleared.")
}
