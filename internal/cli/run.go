package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

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

const rootHelp = `Kupilot is a local Kubernetes diagnostic Agent.

Usage:
  kupilot
  kupilot resume [SESSION_ID | --last]
  kupilot sessions list [--limit N] [--cursor CURSOR] [--json]
  kupilot sessions delete SESSION_ID
  kupilot sessions delete --before CUTOFF [--limit N] [--dry-run | --confirm DIGEST]
  kupilot doctor [--json]
  kupilot cache clear
  kupilot version
  kupilot help [COMMAND]

Running kupilot without a subcommand starts a new Session.
Inside the TUI, /permissions reviews or changes supervision and /status shows
the content-free local permission, action, Reviewer, budget, and Session state.

Commands:
  resume   Resume by picker, exact Session ID, or --last.
  sessions List or transactionally delete bounded local Sessions.
  doctor   Show local redacted configuration, storage, and capability health.
  cache    Manage the local Kupilot cache.
  version  Print non-sensitive build information.
  help     Show help for a command.

Options:
  --config PATH     Use an explicit YAML configuration file.
  --context NAME    Select the initial Kubernetes Context.
  --namespace NAME  Select the initial Kubernetes Namespace.
  --no-color        Disable color output.
  --help            Show root help.
  --version         Print non-sensitive build information.
`

const resumeHelp = `Usage:
  kupilot resume [SESSION_ID | --last]

Resume an eligible Session by picker, exact Session ID, or --last.
Resume never starts a model request, Tool call, or Kubernetes read automatically.
`

const cacheHelp = `Usage:
  kupilot cache clear

Clear entries below the fixed KUPILOT_HOME cache directory.
A missing cache is a successful no-op.
`

const sessionsHelp = `Usage:
  kupilot sessions list [--limit N] [--cursor CURSOR] [--json]
  kupilot sessions delete SESSION_ID
  kupilot sessions delete --before 1d [--limit N] [--dry-run]
  kupilot sessions delete --before RFC3339 --confirm DIGEST

List safe bounded metadata or delete an exact frozen Session selection. Relative
d means 24 hours and w means seven 24-hour days. Batch deletion always excludes
the current Session and protected authority. Non-TTY automation requires a
dry-run followed by an absolute cutoff and the exact selection digest.
`

const doctorHelp = `Usage:
  kupilot doctor [--json]

Run local redacted checks without model, Kubernetes, Tool, Reviewer, approval,
process, or executor I/O.
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

// UnavailableError reports that the requested start path cannot be initialized.
type UnavailableError struct{}

func (UnavailableError) Error() string {
	return "start unavailable"
}

// Help returns one fixed help page.
func Help(topic HelpTopic) string {
	switch topic {
	case HelpResume:
		return resumeHelp
	case HelpCache:
		return cacheHelp
	case HelpSessions:
		return sessionsHelp
	case HelpDoctor:
		return doctorHelp
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
		writeSafe(stderr, "Kupilot could not start.\n")
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
		writeSafe(stderr, "Kupilot could not start.\n")
		return ExitFailure
	}
	if err := start(ctx, intent); err != nil {
		if errors.Is(err, context.Canceled) {
			writeSafe(stderr, "Interrupted.\n")
			return ExitInterrupted
		}

		var unavailable UnavailableError
		if errors.As(err, &unavailable) {
			switch intent.Kind {
			case IntentNew:
				writeSafe(stderr, "Starting a new Session is unavailable.\n")
			case IntentResumePicker, IntentResumeID, IntentResumeLast:
				writeSafe(stderr, "Session resume is unavailable.\n")
			case IntentSessionsList, IntentSessionsDelete:
				writeSafe(stderr, "Session management is unavailable.\n")
			case IntentDoctor:
				writeSafe(stderr, "Local diagnostics are unavailable.\n")
			default:
				writeSafe(stderr, "Kupilot could not start.\n")
			}
			return ExitUnavailable
		}

		var safe interface{ SafeMessage() string }
		if errors.As(err, &safe) {
			message := safe.SafeMessage()
			if validSafeMessage(message) {
				writeSafe(stderr, "Error: "+message+"\n")
				return ExitFailure
			}
		}

		writeSafe(stderr, "Kupilot could not start.\n")
		return ExitFailure
	}

	return ExitOK
}

func validSafeMessage(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current > 0x7e {
			return false
		}
	}
	return true
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
	writeSafe(stderr, "Kupilot could not write output.\n")
	return ExitFailure
}

func writeSafe(dst io.Writer, value string) {
	_, _ = fmt.Fprint(dst, value)
}
