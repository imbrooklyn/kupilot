package cli

import (
	"strings"
	"testing"
)

func TestCommandCatalogIsFixed(t *testing.T) {
	t.Parallel()

	command := newRootCommand(new(StartIntent))
	if !command.CompletionOptions.DisableDefaultCmd {
		t.Fatal("default completion command is enabled")
	}

	commands := command.Commands()
	want := []string{"help", "resume", "version"}
	if len(commands) != len(want) {
		t.Fatalf("command count = %d, want %d", len(commands), len(want))
	}
	for i, expected := range want {
		if commands[i].Name() != expected {
			t.Fatalf("command %d = %q, want %q", i, commands[i].Name(), expected)
		}
	}
}

func TestParseAcceptedIntents(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"

	tests := []struct {
		name string
		args []string
		want StartIntent
	}{
		{name: "new Session", want: StartIntent{Kind: IntentNew}},
		{name: "resume picker", args: []string{"resume"}, want: StartIntent{Kind: IntentResumePicker}},
		{name: "resume exact ID", args: []string{"resume", sessionID}, want: StartIntent{Kind: IntentResumeID, SessionID: sessionID}},
		{name: "resume canonicalizes ID", args: []string{"resume", strings.ToUpper(sessionID)}, want: StartIntent{Kind: IntentResumeID, SessionID: sessionID}},
		{name: "resume last", args: []string{"resume", "--last"}, want: StartIntent{Kind: IntentResumeLast}},
		{name: "version command", args: []string{"version"}, want: StartIntent{Kind: IntentVersion}},
		{name: "version option", args: []string{"--version"}, want: StartIntent{Kind: IntentVersion}},
		{name: "help command", args: []string{"help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "help option", args: []string{"--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "short help option", args: []string{"-h"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "resume help command", args: []string{"help", "resume"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "resume help option", args: []string{"resume", "--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "resume short help option", args: []string{"resume", "-h"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "version help command", args: []string{"help", "version"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpVersion}},
		{name: "version help option", args: []string{"version", "--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpVersion}},
		{name: "help help command", args: []string{"help", "help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpHelp}},
		{name: "help help option", args: []string{"help", "--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpHelp}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Parse() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "unknown command", args: []string{"status"}, wantErr: "unknown command"},
		{name: "disabled completion command", args: []string{"completion"}, wantErr: "unknown command"},
		{name: "unknown root option", args: []string{"--all"}, wantErr: "unknown option"},
		{name: "API key option", args: []string{"--api-key"}, wantErr: "unknown option"},
		{name: "token option", args: []string{"--token"}, wantErr: "unknown option"},
		{name: "kubeconfig content option", args: []string{"--kubeconfig-content"}, wantErr: "unknown option"},
		{name: "resume unknown option", args: []string{"resume", "--all"}, wantErr: "unknown resume option"},
		{name: "resume last true value", args: []string{"resume", "--last=true"}, wantErr: "unknown resume option"},
		{name: "resume last false value", args: []string{"resume", "--last=false"}, wantErr: "unknown resume option"},
		{name: "resume last then ID", args: []string{"resume", "--last", sessionID}, wantErr: "resume accepts either one session ID or --last"},
		{name: "resume ID then last", args: []string{"resume", sessionID, "--last"}, wantErr: "resume accepts either one session ID or --last"},
		{name: "resume two IDs", args: []string{"resume", sessionID, "another-id"}, wantErr: "resume accepts either one session ID or --last"},
		{name: "resume empty ID", args: []string{"resume", ""}, wantErr: "session ID must not be empty"},
		{name: "resume malformed ID", args: []string{"resume", "not-a-session-id"}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "resume non-v7 ID", args: []string{"resume", "0198a46e-7d2a-4d34-9b6f-2df5f45a2a10"}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "resume invalid variant", args: []string{"resume", "0198a46e-7d2a-7d34-7b6f-2df5f45a2a10"}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "resume overlong ID", args: []string{"resume", strings.Repeat("a", 64)}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "resume Unicode-confusable ID", args: []string{"resume", "0198a46e-7d2a-" + string(rune(0xff17)) + "d34-9b6f-2df5f45a2a10"}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "version argument", args: []string{"version", "extra"}, wantErr: "version does not accept arguments"},
		{name: "version option argument", args: []string{"--version", "extra"}, wantErr: "--version does not accept arguments"},
		{name: "help unknown command", args: []string{"help", "status"}, wantErr: "help is unavailable for that command"},
		{name: "help too many commands", args: []string{"help", "resume", "version"}, wantErr: "help accepts at most one command"},
		{name: "help option argument", args: []string{"--help", "resume"}, wantErr: "--help does not accept arguments"},
		{name: "help option value", args: []string{"--help=false"}, wantErr: "--help does not accept arguments"},
		{name: "unknown command help", args: []string{"status", "--help"}, wantErr: "help is unavailable for that command"},
		{name: "resume help option argument", args: []string{"resume", "--help", "extra"}, wantErr: "--help does not accept arguments"},
		{name: "version help option argument", args: []string{"version", "--help", "extra"}, wantErr: "--help does not accept arguments"},
		{name: "help help option argument", args: []string{"help", "--help", "extra"}, wantErr: "--help does not accept arguments"},
		{name: "resume option with help", args: []string{"resume", "--last", "--help"}, wantErr: "--help does not accept arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.args)
			if err == nil {
				t.Fatalf("Parse() = %#v, want error %q", got, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("Parse() error = %q, want %q", err, tt.wantErr)
			}
			if got != (StartIntent{}) {
				t.Fatalf("Parse() returned intent %#v with an error", got)
			}
		})
	}
}
