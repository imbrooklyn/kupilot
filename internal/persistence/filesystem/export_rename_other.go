//go:build !darwin && !linux && !windows

package filesystem

import (
	"errors"
	"os"
)

func renameExportNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}

func isExportTargetExists(err error) bool {
	return errors.Is(err, os.ErrExist)
}

func syncExportDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
