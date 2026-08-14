//go:build darwin

package filesystem

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameExportNoReplace(source, target string) error {
	return unix.RenamexNp(source, target, unix.RENAME_EXCL)
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
