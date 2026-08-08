package cli

import (
	"errors"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// IntentKind identifies one fixed command-line intent.
type IntentKind uint8

const (
	IntentNew IntentKind = iota + 1
	IntentResumePicker
	IntentResumeID
	IntentResumeLast
	IntentVersion
	IntentHelp
)

// HelpTopic identifies one fixed help page.
type HelpTopic uint8

const (
	HelpRoot HelpTopic = iota
	HelpResume
	HelpVersion
	HelpHelp
)

// StartIntent is the complete typed result of command-line parsing.
type StartIntent struct {
	Kind      IntentKind
	SessionID string
	HelpTopic HelpTopic
}

type parseFailure struct {
	message string
}

func (failure *parseFailure) Error() string {
	return failure.message
}

// Parse converts command-line arguments into a fixed typed intent.
func Parse(args []string) (StartIntent, error) {
	if intent, handled, err := parseShortcut(args); handled {
		return intent, err
	}

	var intent StartIntent
	command := newRootCommand(&intent)
	command.SetArgs(args)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)

	if _, err := command.ExecuteC(); err != nil {
		var failure *parseFailure
		if errors.As(err, &failure) {
			return StartIntent{}, errors.New(failure.message)
		}
		return StartIntent{}, errors.New("unknown command")
	}
	if intent.Kind == 0 {
		return StartIntent{}, errors.New("unknown command")
	}
	return intent, nil
}

func parseShortcut(args []string) (StartIntent, bool, error) {
	if len(args) == 0 {
		return StartIntent{}, false, nil
	}

	if args[0] == "--version" {
		if len(args) != 1 {
			return StartIntent{}, true, errors.New("--version does not accept arguments")
		}
		return StartIntent{Kind: IntentVersion}, true, nil
	}
	if args[0] == "resume" {
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "--last=") {
				return StartIntent{}, true, errors.New("unknown resume option")
			}
		}
	}

	helpIndex := -1
	for index, arg := range args {
		if strings.HasPrefix(arg, "--help=") || strings.HasPrefix(arg, "-h=") {
			return StartIntent{}, true, errors.New("--help does not accept arguments")
		}
		if arg == "--help" || arg == "-h" {
			helpIndex = index
			break
		}
	}
	if helpIndex == -1 {
		return StartIntent{}, false, nil
	}
	if len(args) == 1 && helpIndex == 0 {
		return StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}, true, nil
	}
	if len(args) == 2 && helpIndex == 1 {
		topic, ok := helpTopicByName(args[0])
		if ok {
			return StartIntent{Kind: IntentHelp, HelpTopic: topic}, true, nil
		}
		return StartIntent{}, true, errors.New("help is unavailable for that command")
	}

	return StartIntent{}, true, errors.New("--help does not accept arguments")
}

func newRootCommand(intent *StartIntent) *cobra.Command {
	root := &cobra.Command{
		Use:                "kupilot",
		Short:              "Diagnose Kubernetes issues through bounded Evidence",
		SilenceErrors:      true,
		SilenceUsage:       true,
		DisableSuggestions: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &parseFailure{message: "unknown command"}
			}
			return nil
		},
		Run: func(*cobra.Command, []string) {
			*intent = StartIntent{Kind: IntentNew}
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "unknown option"}
	})
	root.SetHelpFunc(func(command *cobra.Command, _ []string) {
		*intent = StartIntent{Kind: IntentHelp, HelpTopic: helpTopicFor(command)}
	})

	helpCommand := newHelpCommand(intent)
	root.AddCommand(
		helpCommand,
		newResumeCommand(intent),
		newVersionCommand(intent),
	)
	root.SetHelpCommand(helpCommand)

	return root
}

func newResumeCommand(intent *StartIntent) *cobra.Command {
	var last bool
	var sessionID string
	command := &cobra.Command{
		Use:   "resume [SESSION_ID]",
		Short: "Resume an eligible Session",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 || (last && len(args) == 1) {
				return &parseFailure{message: "resume accepts either one session ID or --last"}
			}
			if len(args) == 1 {
				if args[0] == "" {
					return &parseFailure{message: "session ID must not be empty"}
				}

				var ok bool
				sessionID, ok = canonicalSessionID(args[0])
				if !ok {
					return &parseFailure{message: "session ID must be a valid UUIDv7"}
				}
			}
			return nil
		},
		Run: func(_ *cobra.Command, args []string) {
			switch {
			case last:
				*intent = StartIntent{Kind: IntentResumeLast}
			case len(args) == 1:
				*intent = StartIntent{Kind: IntentResumeID, SessionID: sessionID}
			default:
				*intent = StartIntent{Kind: IntentResumePicker}
			}
		},
	}
	command.Flags().BoolVar(&last, "last", false, "Resume the most recently active eligible Session")
	command.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "unknown resume option"}
	})
	return command
}

func canonicalSessionID(value string) (string, bool) {
	if len(value) != 36 {
		return "", false
	}

	for index := range value {
		switch index {
		case 8, 13, 18, 23:
			if value[index] != '-' {
				return "", false
			}
		default:
			if !isASCIIHex(value[index]) {
				return "", false
			}
		}
	}
	if value[14] != '7' {
		return "", false
	}
	switch value[19] {
	case '8', '9', 'a', 'A', 'b', 'B':
	default:
		return "", false
	}

	return strings.ToLower(value), true
}

func isASCIIHex(value byte) bool {
	return value >= '0' && value <= '9' ||
		value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}

func newVersionCommand(intent *StartIntent) *cobra.Command {
	command := &cobra.Command{
		Use:   "version",
		Short: "Print non-sensitive build information",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &parseFailure{message: "version does not accept arguments"}
			}
			return nil
		},
		Run: func(*cobra.Command, []string) {
			*intent = StartIntent{Kind: IntentVersion}
		},
	}
	command.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "version does not accept arguments"}
	})
	return command
}

func newHelpCommand(intent *StartIntent) *cobra.Command {
	command := &cobra.Command{
		Use:   "help [COMMAND]",
		Short: "Show help for a command",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return &parseFailure{message: "help accepts at most one command"}
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				*intent = StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}
				return nil
			}

			topic, ok := helpTopicByName(args[0])
			if !ok {
				return &parseFailure{message: "help is unavailable for that command"}
			}
			*intent = StartIntent{Kind: IntentHelp, HelpTopic: topic}
			return nil
		},
	}
	command.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "help is unavailable for that command"}
	})
	return command
}

func helpTopicFor(command *cobra.Command) HelpTopic {
	topic, ok := helpTopicByName(command.Name())
	if !ok {
		return HelpRoot
	}
	return topic
}

func helpTopicByName(name string) (HelpTopic, bool) {
	switch name {
	case "resume":
		return HelpResume, true
	case "version":
		return HelpVersion, true
	case "help":
		return HelpHelp, true
	default:
		return HelpRoot, false
	}
}
