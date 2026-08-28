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
	IntentCacheClear
	IntentVersion
	IntentHelp
)

// HelpTopic identifies one fixed help page.
type HelpTopic uint8

const (
	HelpRoot HelpTopic = iota
	HelpResume
	HelpCache
	HelpVersion
	HelpHelp
)

// StartIntent is the complete typed result of command-line parsing.
type StartIntent struct {
	Kind      IntentKind
	SessionID string
	HelpTopic HelpTopic
	Options   StartOptions
}

// StartOptions contains only admitted non-sensitive startup overrides.
type StartOptions struct {
	ConfigFile    string
	ConfigFileSet bool
	Context       string
	ContextSet    bool
	Namespace     string
	NamespaceSet  bool
	NoColor       bool
	NoColorSet    bool
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
	if helpIndex != len(args)-1 {
		return StartIntent{}, true, errors.New("--help does not accept arguments")
	}
	topic, err := helpTopicBeforeOption(args[:helpIndex])
	if err != nil {
		return StartIntent{}, true, err
	}
	return StartIntent{Kind: IntentHelp, HelpTopic: topic}, true, nil
}

func helpTopicBeforeOption(args []string) (HelpTopic, error) {
	if len(args) == 2 && args[0] == "cache" && args[1] == "clear" {
		return HelpCache, nil
	}
	topic := HelpRoot
	commandSeen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--config" || argument == "--context" || argument == "--namespace":
			index++
			if index >= len(args) {
				return HelpRoot, errors.New("--help does not accept arguments")
			}
			continue
		case strings.HasPrefix(argument, "--config=") || strings.HasPrefix(argument, "--context=") || strings.HasPrefix(argument, "--namespace="):
			continue
		case argument == "--no-color" || strings.HasPrefix(argument, "--no-color="):
			continue
		case strings.HasPrefix(argument, "-"):
			return HelpRoot, errors.New("--help does not accept arguments")
		}

		if commandSeen {
			return HelpRoot, errors.New("--help does not accept arguments")
		}
		var ok bool
		topic, ok = helpTopicByName(argument)
		if !ok {
			return HelpRoot, errors.New("help is unavailable for that command")
		}
		commandSeen = true
	}
	return topic, nil
}

func newRootCommand(intent *StartIntent) *cobra.Command {
	startup := new(startupFlags)
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
		Run: func(command *cobra.Command, _ []string) {
			*intent = StartIntent{Kind: IntentNew, Options: startup.options(command)}
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&startup.configFile, "config", "", "Use an explicit YAML configuration file")
	root.PersistentFlags().StringVar(&startup.context, "context", "", "Select the initial Kubernetes Context")
	root.PersistentFlags().StringVar(&startup.namespace, "namespace", "", "Select the initial Kubernetes Namespace")
	root.PersistentFlags().BoolVar(&startup.noColor, "no-color", false, "Disable color output")
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "unknown option"}
	})
	root.SetHelpFunc(func(command *cobra.Command, _ []string) {
		*intent = StartIntent{Kind: IntentHelp, HelpTopic: helpTopicFor(command)}
	})

	helpCommand := newHelpCommand(intent)
	root.AddCommand(
		helpCommand,
		newResumeCommand(intent, startup),
		newCacheCommand(intent, startup),
		newVersionCommand(intent),
	)
	root.SetHelpCommand(helpCommand)

	return root
}

func (flags *startupFlags) changed(command *cobra.Command) bool {
	persistent := command.Root().PersistentFlags()
	return persistent.Changed("config") || persistent.Changed("context") ||
		persistent.Changed("namespace") || persistent.Changed("no-color")
}

type startupFlags struct {
	configFile string
	context    string
	namespace  string
	noColor    bool
}

func (flags *startupFlags) options(command *cobra.Command) StartOptions {
	persistent := command.Root().PersistentFlags()
	return StartOptions{
		ConfigFile:    flags.configFile,
		ConfigFileSet: persistent.Changed("config"),
		Context:       flags.context,
		ContextSet:    persistent.Changed("context"),
		Namespace:     flags.namespace,
		NamespaceSet:  persistent.Changed("namespace"),
		NoColor:       flags.noColor,
		NoColorSet:    persistent.Changed("no-color"),
	}
}

func newResumeCommand(intent *StartIntent, startup *startupFlags) *cobra.Command {
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
		Run: func(command *cobra.Command, args []string) {
			options := startup.options(command)
			switch {
			case last:
				*intent = StartIntent{Kind: IntentResumeLast, Options: options}
			case len(args) == 1:
				*intent = StartIntent{Kind: IntentResumeID, SessionID: sessionID, Options: options}
			default:
				*intent = StartIntent{Kind: IntentResumePicker, Options: options}
			}
		},
	}
	command.Flags().BoolVar(&last, "last", false, "Resume the most recently active eligible Session")
	command.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "unknown resume option"}
	})
	return command
}

func newCacheCommand(intent *StartIntent, startup *startupFlags) *cobra.Command {
	cache := &cobra.Command{
		Use:   "cache",
		Short: "Manage the local KuPilot cache",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &parseFailure{message: "unknown cache command"}
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			return &parseFailure{message: "cache requires the clear command"}
		},
	}
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Clear entries from the local KuPilot cache",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &parseFailure{message: "cache clear does not accept arguments"}
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if startup.changed(command) {
				return &parseFailure{message: "cache clear does not accept startup options"}
			}
			*intent = StartIntent{Kind: IntentCacheClear}
			return nil
		},
	}
	clear.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return &parseFailure{message: "cache clear does not accept options"}
	})
	cache.AddCommand(clear)
	return cache
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
	case "cache":
		return HelpCache, true
	case "version":
		return HelpVersion, true
	case "help":
		return HelpHelp, true
	default:
		return HelpRoot, false
	}
}
