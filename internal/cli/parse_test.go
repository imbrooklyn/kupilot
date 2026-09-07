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
	want := []string{"cache", "doctor", "help", "resume", "sessions", "version"}
	if len(commands) != len(want) {
		t.Fatalf("command count = %d, want %d", len(commands), len(want))
	}
	for i, expected := range want {
		if commands[i].Name() != expected {
			t.Fatalf("command %d = %q, want %q", i, commands[i].Name(), expected)
		}
	}
}

func TestParseSessionManagementAndDoctorIntents(t *testing.T) {
	t.Parallel()
	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"
	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, intent StartIntent)
	}{
		{name: "list defaults", args: []string{"sessions", "list"}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsList || intent.SessionList == nil || intent.SessionList.Limit != 20 || intent.SessionList.JSON {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "list page", args: []string{"sessions", "list", "--limit", "7", "--cursor", "opaque", "--json"}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsList || intent.SessionList == nil || intent.SessionList.Limit != 7 || intent.SessionList.Cursor != "opaque" || !intent.SessionList.JSON {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "exact delete", args: []string{"sessions", "delete", sessionID}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsDelete || intent.SessionDelete == nil || intent.SessionDelete.SessionID != sessionID {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "exact delete dry run", args: []string{"sessions", "delete", sessionID, "--dry-run"}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsDelete || intent.SessionDelete == nil || intent.SessionDelete.SessionID != sessionID || !intent.SessionDelete.DryRun {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "exact delete digest", args: []string{"sessions", "delete", sessionID, "--confirm", strings.Repeat("a", 64)}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsDelete || intent.SessionDelete == nil || intent.SessionDelete.SessionID != sessionID || len(intent.SessionDelete.Confirm) != 64 {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "batch preview", args: []string{"sessions", "delete", "--before", "30d", "--limit", "100", "--dry-run"}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsDelete || intent.SessionDelete == nil || intent.SessionDelete.Before != "30d" || intent.SessionDelete.Limit != 100 || !intent.SessionDelete.DryRun {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "batch confirm", args: []string{"sessions", "delete", "--before", "2026-09-01T00:00:00Z", "--confirm", strings.Repeat("a", 64)}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentSessionsDelete || intent.SessionDelete == nil || len(intent.SessionDelete.Confirm) != 64 {
				t.Fatalf("intent = %#v", intent)
			}
		}},
		{name: "doctor json", args: []string{"doctor", "--json"}, check: func(t *testing.T, intent StartIntent) {
			if intent.Kind != IntentDoctor || intent.Doctor == nil || !intent.Doctor.JSON {
				t.Fatalf("intent = %#v", intent)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent, err := Parse(test.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			test.check(t, intent)
		})
	}
	if _, err := Parse([]string{"sessions", "delete", strings.ToUpper(sessionID)}); err == nil ||
		!strings.Contains(err.Error(), "exact canonical UUIDv7") {
		t.Fatalf("uppercase exact deletion ID error = %v", err)
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
		{name: "cache clear", args: []string{"cache", "clear"}, want: StartIntent{Kind: IntentCacheClear}},
		{name: "version command", args: []string{"version"}, want: StartIntent{Kind: IntentVersion}},
		{name: "version option", args: []string{"--version"}, want: StartIntent{Kind: IntentVersion}},
		{name: "help command", args: []string{"help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "help option", args: []string{"--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "short help option", args: []string{"-h"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpRoot}},
		{name: "resume help command", args: []string{"help", "resume"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "resume help option", args: []string{"resume", "--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "resume short help option", args: []string{"resume", "-h"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpResume}},
		{name: "cache help command", args: []string{"help", "cache"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpCache}},
		{name: "cache clear help option", args: []string{"cache", "clear", "--help"}, want: StartIntent{Kind: IntentHelp, HelpTopic: HelpCache}},
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

func TestParseNonSensitiveStartupOptions(t *testing.T) {
	t.Parallel()

	const sessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"
	configPath := "/tmp/kupilot/config.yaml"
	wantOptions := StartOptions{
		ConfigFile:    configPath,
		ConfigFileSet: true,
		Context:       "development",
		ContextSet:    true,
		Namespace:     "team-a",
		NamespaceSet:  true,
		NoColor:       true,
		NoColorSet:    true,
	}

	tests := []struct {
		name string
		args []string
		kind IntentKind
	}{
		{
			name: "new Session",
			args: []string{"--config", configPath, "--context", "development", "--namespace", "team-a", "--no-color"},
			kind: IntentNew,
		},
		{
			name: "resume picker",
			args: []string{"resume", "--config", configPath, "--context", "development", "--namespace", "team-a", "--no-color"},
			kind: IntentResumePicker,
		},
		{
			name: "resume ID with flags before command",
			args: []string{"--config=" + configPath, "--context=development", "--namespace=team-a", "--no-color", "resume", sessionID},
			kind: IntentResumeID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.kind)
			}
			if got.Options != wantOptions {
				t.Errorf("Options = %#v, want %#v", got.Options, wantOptions)
			}
			if tt.kind == IntentResumeID && got.SessionID != sessionID {
				t.Errorf("SessionID = %q, want %q", got.SessionID, sessionID)
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
		{name: "cache without clear", args: []string{"cache"}, wantErr: "cache requires the clear command"},
		{name: "unknown cache command", args: []string{"cache", "prune"}, wantErr: "unknown cache command"},
		{name: "cache clear argument", args: []string{"cache", "clear", "extra"}, wantErr: "cache clear does not accept arguments"},
		{name: "cache clear startup option", args: []string{"cache", "clear", "--no-color"}, wantErr: "cache clear does not accept startup options"},
		{name: "sessions missing child", args: []string{"sessions"}, wantErr: "sessions requires list or delete"},
		{name: "sessions unbounded", args: []string{"sessions", "list", "--all"}, wantErr: "unknown sessions list option"},
		{name: "sessions list zero", args: []string{"sessions", "list", "--limit", "0"}, wantErr: "sessions list options are invalid"},
		{name: "delete missing selector", args: []string{"sessions", "delete"}, wantErr: "sessions delete requires one Session ID or --before"},
		{name: "delete title", args: []string{"sessions", "delete", "a title"}, wantErr: "session ID must be an exact canonical UUIDv7"},
		{name: "delete force", args: []string{"sessions", "delete", sessionID, "--force"}, wantErr: "unknown sessions delete option"},
		{name: "delete yes", args: []string{"sessions", "delete", sessionID, "--yes"}, wantErr: "unknown sessions delete option"},
		{name: "delete mixed", args: []string{"sessions", "delete", sessionID, "--before", "1d"}, wantErr: "exact Session deletion does not accept batch options"},
		{name: "delete exact dry confirm", args: []string{"sessions", "delete", sessionID, "--dry-run", "--confirm", strings.Repeat("a", 64)}, wantErr: "--dry-run and --confirm are mutually exclusive"},
		{name: "delete short digest", args: []string{"sessions", "delete", "--before", "1d", "--confirm", "abc"}, wantErr: "sessions delete options are invalid"},
		{name: "delete uppercase digest", args: []string{"sessions", "delete", "--before", "1d", "--confirm", strings.Repeat("A", 64)}, wantErr: "sessions delete options are invalid"},
		{name: "delete dry confirm", args: []string{"sessions", "delete", "--before", "1d", "--dry-run", "--confirm", strings.Repeat("a", 64)}, wantErr: "--dry-run and --confirm are mutually exclusive"},
		{name: "resume Unicode-confusable ID", args: []string{"resume", "0198a46e-7d2a-" + string(rune(0xff17)) + "d34-9b6f-2df5f45a2a10"}, wantErr: "session ID must be a valid UUIDv7"},
		{name: "version argument", args: []string{"version", "extra"}, wantErr: "version does not accept arguments"},
		{name: "version option argument", args: []string{"--version", "extra"}, wantErr: "--version does not accept arguments"},
		{name: "help unknown command", args: []string{"help", "status"}, wantErr: "help is unavailable for that command"},
		{name: "help cache child without parent", args: []string{"help", "clear"}, wantErr: "help is unavailable for that command"},
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
