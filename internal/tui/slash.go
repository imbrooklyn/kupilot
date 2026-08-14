package tui

import (
	"sort"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/application"
)

const MaxSlashCandidates = 8

type slashAction uint8

const (
	slashApplication slashAction = iota + 1
	slashHelp
	slashStatus
	slashQuit
)

// SlashCommand is one immutable code-defined local or Application command.
type SlashCommand struct {
	Name            string
	Aliases         []string
	Usage           string
	Summary         string
	AcceptsArgument bool
	action          slashAction
	commandKind     application.UICommandKind
}

var fixedSlashCommands = [...]SlashCommand{
	{Name: "help", Summary: "Show commands and key bindings", action: slashHelp},
	{Name: "context", Usage: "[filter]", Summary: "Select the Kubernetes Context", AcceptsArgument: true, action: slashApplication, commandKind: application.UICommandSelectContext},
	{Name: "namespace", Aliases: []string{"ns"}, Usage: "[filter]", Summary: "Select the Kubernetes Namespace", AcceptsArgument: true, action: slashApplication, commandKind: application.UICommandSelectNamespace},
	{Name: "resource", Aliases: []string{"res"}, Usage: "[filter]", Summary: "Select or clear the target resource", AcceptsArgument: true, action: slashApplication, commandKind: application.UICommandSelectResource},
	{Name: "status", Summary: "Show the current safe status", action: slashStatus},
	{Name: "new", Summary: "Start a new Session", action: slashApplication, commandKind: application.UICommandNewSession},
	{Name: "resume", Usage: "[filter]", Summary: "Resume a local Session", AcceptsArgument: true, action: slashApplication, commandKind: application.UICommandResumeSession},
	{Name: "rename", Usage: "[title]", Summary: "Rename the current Session", AcceptsArgument: true, action: slashApplication, commandKind: application.UICommandRenameSession},
	{Name: "privacy", Summary: "Show privacy, retention, and Session controls", action: slashApplication, commandKind: application.UICommandShowPrivacy},
	{Name: "cancel", Summary: "Cancel the active AgentRun", action: slashApplication, commandKind: application.UICommandCancelRun},
	{Name: "quit", Aliases: []string{"exit"}, Summary: "Exit KuPilot", action: slashQuit},
}

// SlashCommands returns a defensive copy of the compile-time registry.
func SlashCommands() []SlashCommand {
	commands := make([]SlashCommand, len(fixedSlashCommands))
	for index, command := range fixedSlashCommands {
		commands[index] = command
		commands[index].Aliases = append([]string(nil), command.Aliases...)
	}
	return commands
}

// DraftMode identifies chat, escaped chat, fixed Slash, or unknown Slash input.
type DraftMode uint8

const (
	DraftChat DraftMode = iota + 1
	DraftEscapedChat
	DraftSlash
	DraftUnknownSlash
)

// ParsedDraft is the pure result of applying the single-line first-character rule.
type ParsedDraft struct {
	Mode     DraftMode
	Command  *SlashCommand
	Name     string
	Argument string
}

// ParseSlashDraft applies fixed syntax without dispatching any action.
func ParseSlashDraft(draft string) ParsedDraft {
	if strings.ContainsRune(draft, '\n') || !strings.HasPrefix(draft, "/") {
		return ParsedDraft{Mode: DraftChat}
	}
	if strings.HasPrefix(draft, "//") {
		return ParsedDraft{Mode: DraftEscapedChat}
	}
	remainder := strings.TrimPrefix(draft, "/")
	name := remainder
	argument := ""
	if index := strings.IndexByte(remainder, ' '); index >= 0 {
		name = remainder[:index]
		argument = strings.TrimSpace(remainder[index+1:])
	}
	if name == "" {
		return ParsedDraft{Mode: DraftSlash}
	}
	if !validSlashName(name) {
		return ParsedDraft{Mode: DraftUnknownSlash, Name: name, Argument: argument}
	}
	command, ok := findSlashCommand(name)
	if !ok {
		return ParsedDraft{Mode: DraftUnknownSlash, Name: name, Argument: argument}
	}
	return ParsedDraft{Mode: DraftSlash, Command: &command, Name: name, Argument: argument}
}

func validSlashName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, current := range name {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' {
			continue
		}
		return false
	}
	return true
}

func findSlashCommand(name string) (SlashCommand, bool) {
	for _, command := range fixedSlashCommands {
		if command.Name == name {
			return command, true
		}
		for _, alias := range command.Aliases {
			if alias == name {
				return command, true
			}
		}
	}
	return SlashCommand{}, false
}

// FilterSlashCommands performs stable local matching and returns at most eight rows.
func FilterSlashCommands(query string) []SlashCommand {
	if query != strings.ToLower(query) || !validSlashFilter(query) {
		return nil
	}
	type match struct {
		command SlashCommand
		score   int
		index   int
	}
	matches := make([]match, 0, len(fixedSlashCommands))
	for index, command := range fixedSlashCommands {
		score, ok := slashMatchScore(command, query)
		if ok {
			matches = append(matches, match{command: command, score: score, index: index})
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].score == matches[right].score {
			return matches[left].index < matches[right].index
		}
		return matches[left].score < matches[right].score
	})
	limit := min(len(matches), MaxSlashCandidates)
	result := make([]SlashCommand, limit)
	for index := 0; index < limit; index++ {
		result[index] = matches[index].command
		result[index].Aliases = append([]string(nil), matches[index].command.Aliases...)
	}
	return result
}

func validSlashFilter(query string) bool {
	for _, current := range query {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' {
			continue
		}
		return false
	}
	return true
}

func slashMatchScore(command SlashCommand, query string) (int, bool) {
	if query == "" || strings.HasPrefix(command.Name, query) {
		return 0, true
	}
	for _, alias := range command.Aliases {
		if strings.HasPrefix(alias, query) {
			return 1, true
		}
	}
	if strings.Contains(command.Name, query) {
		return 2, true
	}
	for _, alias := range command.Aliases {
		if strings.Contains(alias, query) {
			return 3, true
		}
	}
	if strings.Contains(strings.ToLower(command.Summary), query) {
		return 4, true
	}
	return 0, false
}
