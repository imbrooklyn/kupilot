//go:build linux

package filesystem

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameExportNoReplace(source, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}

func isExportTargetExists(err error) bool {
	return errors.Is(err, os.ErrExist) || errors.Is(err, unix.EEXIST)
}

func syncExportDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
