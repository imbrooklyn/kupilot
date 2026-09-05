//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package executor

import (
	"os"
	"os/exec"
)

func configureCommand(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
}

func openPathNoFollow(name string, _ bool) (*os.File, error) {
	return os.Open(name)
}

func platformFileIdentity(os.FileInfo) string { return "portable" }
