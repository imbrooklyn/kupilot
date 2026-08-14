//go:build windows

package filesystem

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func renameExportNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func isExportTargetExists(err error) bool {
	return errors.Is(err, os.ErrExist) || errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

func syncExportDirectory(string) error {
	return nil
}
