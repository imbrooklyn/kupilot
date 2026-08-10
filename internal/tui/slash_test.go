package tui

import (
	"slices"
	"testing"
)

func TestSlashRegistryIsFixedAndReadOnly(t *testing.T) {
	t.Parallel()

	commands := SlashCommands()
	wantNames := []string{
		"help", "context", "namespace", "resource", "status", "new",
		"resume", "rename", "privacy", "cancel", "quit",
	}
	if len(commands) != len(wantNames) {
		t.Fatalf("command count = %d, want %d", len(commands), len(wantNames))
	}
	for index, want := range wantNames {
		if commands[index].Name != want {
			t.Fatalf("command %d = %q, want %q", index, commands[index].Name, want)
		}
	}

	wantAliases := map[string][]string{
		"namespace": {"ns"},
		"resource":  {"res"},
		"quit":      {"exit"},
	}
	for _, command := range commands {
		if !slices.Equal(command.Aliases, wantAliases[command.Name]) {
			t.Errorf("aliases for %q = %v, want %v", command.Name, command.Aliases, wantAliases[command.Name])
		}
		for _, forbidden := range []string{"shell", "kubectl", "exec", "delete", "patch", "apply", "restart", "approve"} {
			if command.Name == forbidden || slices.Contains(command.Aliases, forbidden) {
				t.Errorf("registry contains forbidden command %q", forbidden)
			}
		}
	}

	commands[0].Name = "changed"
	if SlashCommands()[0].Name != "help" {
		t.Fatal("SlashCommands exposed mutable registry state")
	}
}

func TestParseSlashDraftRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		draft       string
		mode        DraftMode
		commandName string
		argument    string
	}{
		{name: "chat", draft: "inspect /help", mode: DraftChat},
		{name: "empty slash", draft: "/", mode: DraftSlash},
		{name: "known command", draft: "/namespace pay", mode: DraftSlash, commandName: "namespace", argument: "pay"},
		{name: "known alias", draft: "/ns pay", mode: DraftSlash, commandName: "namespace", argument: "pay"},
		{name: "literal slash", draft: "//help", mode: DraftEscapedChat},
		{name: "multiline slash", draft: "/help\nnot a command", mode: DraftChat},
		{name: "unknown command", draft: "/unknown", mode: DraftUnknownSlash},
		{name: "uppercase command", draft: "/Help", mode: DraftUnknownSlash},
		{name: "invalid command token", draft: "/help?", mode: DraftUnknownSlash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseSlashDraft(tt.draft)
			if got.Mode != tt.mode || got.Argument != tt.argument {
				t.Fatalf("ParseSlashDraft(%q) = %#v", tt.draft, got)
			}
			if tt.commandName == "" {
				if got.Command != nil {
					t.Fatalf("command = %#v, want nil", got.Command)
				}
			} else if got.Command == nil || got.Command.Name != tt.commandName {
				t.Fatalf("command = %#v, want %q", got.Command, tt.commandName)
			}
		})
	}
}

func TestFilterSlashCommandsIsStableAndBounded(t *testing.T) {
	t.Parallel()

	all := FilterSlashCommands("")
	if len(all) != MaxSlashCandidates {
		t.Fatalf("empty filter count = %d, want %d", len(all), MaxSlashCandidates)
	}
	if got := FilterSlashCommands("res"); len(got) == 0 || got[0].Name != "resource" {
		t.Fatalf("alias filter = %#v", got)
	}
	if got := FilterSlashCommands("priv"); len(got) != 1 || got[0].Name != "privacy" {
		t.Fatalf("prefix filter = %#v", got)
	}
	if got := FilterSlashCommands("does-not-exist"); len(got) != 0 {
		t.Fatalf("unknown filter = %#v", got)
	}
}
