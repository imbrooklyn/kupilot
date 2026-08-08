package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
)

// Stable process exit codes.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitUnavailable = 69
	ExitInterrupted = 130
)

const rootHelp = `KuPilot is a local Kubernetes diagnostic Agent.

Usage:
  kupilot
  kupilot resume [SESSION_ID | --last]
  kupilot version
  kupilot help [COMMAND]

Running kupilot without a subcommand starts a new Session.

Commands:
  resume   Resume by picker, exact Session ID, or --last.
  version  Print non-sensitive build information.
  help     Show help for a command.

Options:
  --help     Show root help.
  --version  Print non-sensitive build information.
`

const resumeHelp = `Usage:
  kupilot resume [SESSION_ID | --last]

Resume an eligible Session by picker, exact Session ID, or --last.
Session resume is unavailable in this development build.
`

const versionHelp = `Usage:
  kupilot version
  kupilot --version

Print non-sensitive build information.
`

const helpHelp = `Usage:
  kupilot help [COMMAND]
  kupilot --help

Show help for a command.
`

// StartFunc accepts a parsed Session start intent from the delivery adapter.
type StartFunc func(context.Context, StartIntent) error

// UnavailableError reports that the requested start path is not implemented in
// the current development build.
type UnavailableError struct{}

func (UnavailableError) Error() string {
	return "start unavailable"
}

// Help returns one fixed help page.
func Help(topic HelpTopic) string {
	switch topic {
	case HelpResume:
		return resumeHelp
	case HelpVersion:
		return versionHelp
	case HelpHelp:
		return helpHelp
	default:
		return rootHelp
	}
}

// Run parses and handles delivery-only commands, then passes Session starts to
// the injected composition callback.
func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	info buildinfo.Info,
	start StartFunc,
) int {
	if ctx == nil {
		writeSafe(stderr, "KuPilot could not start.\n")
		return ExitFailure
	}
	if err := ctx.Err(); err != nil {
		writeSafe(stderr, "Interrupted.\n")
		return ExitInterrupted
	}

	intent, err := Parse(args)
	if err != nil {
		writeSafe(stderr, "Error: "+err.Error()+"\n")
		return ExitUsage
	}

	switch intent.Kind {
	case IntentHelp:
		if err := writeContext(ctx, stdout, Help(intent.HelpTopic)); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	case IntentVersion:
		if err := writeContext(ctx, stdout, info.Line()+"\n"); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	}

	if start == nil {
		writeSafe(stderr, "KuPilot could not start.\n")
		return ExitFailure
	}
	if err := start(ctx, intent); err != nil {
		if errors.Is(err, context.Canceled) {
			writeSafe(stderr, "Interrupted.\n")
			return ExitInterrupted
		}

		var unavailable UnavailableError
		if errors.As(err, &unavailable) {
			if intent.Kind == IntentNew {
				writeSafe(stderr, "Starting a new Session is unavailable in this development build.\n")
			} else {
				writeSafe(stderr, "Session resume is unavailable in this development build.\n")
			}
			return ExitUnavailable
		}

		writeSafe(stderr, "KuPilot could not start.\n")
		return ExitFailure
	}

	return ExitOK
}

func writeContext(ctx context.Context, dst io.Writer, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(dst, value)
	return err
}

func writeFailure(stderr io.Writer, err error) int {
	if errors.Is(err, context.Canceled) {
		writeSafe(stderr, "Interrupted.\n")
		return ExitInterrupted
	}
	writeSafe(stderr, "KuPilot could not write output.\n")
	return ExitFailure
}

func writeSafe(dst io.Writer, value string) {
	_, _ = fmt.Fprint(dst, value)
}
