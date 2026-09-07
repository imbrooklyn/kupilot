package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	"github.com/imbrooklyn/kupilot/internal/platform/sessionlock"
)

const maxSessionConfirmationBytes = 256

type sessionCommandIO struct {
	input  io.Reader
	output io.Writer
	isTTY  bool
	now    func() time.Time
	getenv func(string) string
}

type processDeletionIsolation struct {
	lock             *sessionlock.Lock
	alreadyExclusive bool
}

type processDeletionLease struct {
	lock      *sessionlock.Lock
	downgrade bool
}

func (isolation *processDeletionIsolation) AcquireExclusive(ctx context.Context) (application.SessionDeletionLease, error) {
	if isolation == nil || isolation.lock == nil || ctx == nil || ctx.Err() != nil {
		return nil, application.ErrSessionProcessActive
	}
	if isolation.alreadyExclusive {
		return &processDeletionLease{}, nil
	}
	if err := isolation.lock.Upgrade(ctx); err != nil {
		return nil, application.ErrSessionProcessActive
	}
	return &processDeletionLease{lock: isolation.lock, downgrade: true}, nil
}

func (lease *processDeletionLease) Release() error {
	if lease == nil || !lease.downgrade || lease.lock == nil {
		return nil
	}
	lease.downgrade = false
	return lease.lock.Downgrade()
}

type fileDescriptorReader interface {
	Fd() uintptr
}

func sessionInputIsTerminal(input io.Reader) bool {
	value, ok := input.(fileDescriptorReader)
	return ok && term.IsTerminal(int(value.Fd()))
}

type cliTerminalCapabilities struct {
	InteractiveInput bool   `json:"interactive_input"`
	NativeClipboard  string `json:"native_clipboard"`
	OSC52            string `json:"osc_52"`
	Multiplexer      string `json:"multiplexer"`
	RemoteSession    string `json:"remote_session"`
	Title            string `json:"title"`
	Notification     string `json:"notification"`
	Color            string `json:"color"`
	AlternateScreen  string `json:"alternate_screen"`
	ReducedMotion    bool   `json:"reduced_motion"`
	Scrollback       string `json:"scrollback"`
}

type cliDoctorEnvelope struct {
	SchemaVersion string                     `json:"schema_version"`
	Doctor        application.UIDoctorResult `json:"doctor"`
	Terminal      cliTerminalCapabilities    `json:"terminal"`
}

func runLocalSessionCommand(
	ctx context.Context,
	intent cli.StartIntent,
	info buildinfo.Info,
	paths config.Paths,
	streams sessionCommandIO,
) error {
	if ctx == nil || streams.output == nil || streams.now == nil {
		return cli.UnavailableError{}
	}
	if err := config.EnsureHome(ctx, paths); err != nil {
		return err
	}
	lockMode := sessionlock.Shared
	if intent.Kind == cli.IntentSessionsDelete && intent.SessionDelete != nil && intent.SessionDelete.Before != "" {
		lockMode = sessionlock.Exclusive
	}
	processLock, err := sessionlock.Acquire(ctx, filepath.Join(paths.StateDir, ".kupilot-process.lock"), lockMode)
	if err != nil {
		if errors.Is(err, sessionlock.ErrActive) {
			return sessionCommandError("Another Kupilot process may be active. No data was deleted.")
		}
		return cli.UnavailableError{}
	}
	defer processLock.Close()
	version := info.Version
	if version == "" {
		version = "dev"
	}
	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: paths.StateDir, ApplicationVersion: version, CorrelationID: "session-management",
	})
	if err != nil {
		return err
	}
	defer database.Close()
	manager, err := application.NewIsolatedSessionManager(sqlite.NewSessionRepository(database), streams.now, &processDeletionIsolation{
		lock: processLock, alreadyExclusive: lockMode == sessionlock.Exclusive,
	})
	if err != nil {
		return cli.UnavailableError{}
	}
	switch intent.Kind {
	case cli.IntentSessionsList:
		return runSessionsList(ctx, manager, intent.SessionList, streams)
	case cli.IntentSessionsDelete:
		return runSessionsDelete(ctx, manager, intent.SessionDelete, streams)
	case cli.IntentDoctor:
		return runCLIDoctor(ctx, manager, intent.Doctor, info, paths, streams)
	default:
		return cli.UnavailableError{}
	}
}

func runSessionsList(ctx context.Context, manager *application.SessionManager, options *cli.SessionListOptions, streams sessionCommandIO) error {
	if options == nil {
		return cli.UnavailableError{}
	}
	page, err := manager.List(ctx, application.SessionListRequest{
		Limit: options.Limit, Cursor: options.Cursor, FrozenNow: streams.now(),
	})
	if err != nil {
		return err
	}
	for index := range page.Sessions {
		page.Sessions[index].Title = safeSessionCLIText(page.Sessions[index].Title, 512)
	}
	if options.JSON {
		encoder := json.NewEncoder(streams.output)
		encoder.SetEscapeHTML(true)
		return encoder.Encode(page)
	}
	if len(page.Sessions) == 0 {
		_, err = fmt.Fprintln(streams.output, "No Sessions.")
		return err
	}
	for _, session := range page.Sessions {
		title := session.Title
		if title == "" {
			title = "Untitled Session"
		}
		state := "not resumable"
		if session.Resumable {
			state = "resumable"
		}
		if session.Protected {
			state += "; protected=" + string(session.ProtectionReason)
		}
		if _, err = fmt.Fprintf(streams.output, "%s\t%s\tLast active: %s\t%s\t%s\n",
			session.ID, title, formatSessionLocalTime(session.LastActivityAt), session.PrivacyMode, state); err != nil {
			return err
		}
	}
	if page.NextCursor != "" {
		_, err = fmt.Fprintln(streams.output, "Next cursor:", page.NextCursor)
	}
	return err
}

func runSessionsDelete(ctx context.Context, manager *application.SessionManager, options *cli.SessionDeleteOptions, streams sessionCommandIO) error {
	if options == nil {
		return cli.UnavailableError{}
	}
	frozenNow := streams.now().UTC().Truncate(time.Millisecond)
	request := application.SessionDeletionSelectionRequest{
		FrozenNow: frozenNow, Limit: options.Limit,
	}
	if options.SessionID != "" {
		request.Kind = application.SessionDeletionExact
		request.SessionID = domain.SessionID(options.SessionID)
	} else {
		request.Kind = application.SessionDeletionBefore
		cutoff, err := application.ParseSessionDeletionCutoff(options.Before, frozenNow)
		if err != nil {
			return sessionCommandError("The deletion cutoff is invalid.")
		}
		request.Cutoff = cutoff
		if options.Confirm != "" && !strings.Contains(options.Before, "T") {
			return sessionCommandError("Automated confirmation requires the absolute RFC3339 cutoff returned by dry-run.")
		}
	}
	plan, err := manager.PreviewDeletion(ctx, request)
	if err != nil {
		return err
	}
	if err := writeDeletionPreview(streams.output, plan); err != nil {
		return err
	}
	if plan.Snapshot.OverLimit {
		return sessionCommandError("The eligible selection exceeds the bounded limit; use a narrower cutoff or an explicit limit no greater than 100.")
	}
	if options.DryRun || len(plan.Sessions) == 0 {
		_, err = fmt.Fprintln(streams.output, "No data was deleted.")
		return err
	}
	confirmation := options.Confirm
	if request.Kind == application.SessionDeletionExact {
		if !streams.isTTY {
			if confirmation == "" {
				return sessionCommandError("Non-interactive exact Session deletion requires --dry-run, then --confirm DIGEST for the same Session ID.")
			}
		} else {
			if confirmation != "" {
				return sessionCommandError("Interactive exact Session deletion requires the displayed confirmation phrase, not --confirm.")
			}
			phrase := "DELETE SESSION " + string(request.SessionID)
			if _, err := fmt.Fprintln(streams.output, "Type exactly:", phrase); err != nil {
				return err
			}
			confirmation, err = readExactConfirmation(streams.input)
			if err != nil || confirmation != phrase {
				return sessionCommandError("Deletion confirmation did not match. No data was deleted.")
			}
			confirmation = plan.Digest
		}
	} else if confirmation == "" {
		if !streams.isTTY {
			return sessionCommandError("Non-interactive batch deletion requires --dry-run, then an absolute cutoff with --confirm DIGEST.")
		}
		phrase := fmt.Sprintf("DELETE %d SESSIONS", len(plan.Sessions))
		if _, err := fmt.Fprintln(streams.output, "Type exactly:", phrase); err != nil {
			return err
		}
		confirmation, err = readExactConfirmation(streams.input)
		if err != nil || confirmation != phrase {
			return sessionCommandError("Deletion confirmation did not match. No data was deleted.")
		}
		confirmation = plan.Digest
	}
	committed, err := manager.CommitDeletion(ctx, plan, confirmation)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(streams.output, "Deleted: %d · protected: %d · remaining: %d\n", committed.Deleted, committed.Protected, committed.Remaining)
	return err
}

func writeDeletionPreview(output io.Writer, plan application.SessionDeletionPlan) error {
	request := plan.Snapshot.Request
	if request.Kind == application.SessionDeletionExact {
		if _, err := fmt.Fprintf(output, "Exact Session: %s\n", request.SessionID); err != nil {
			return err
		}
		if len(plan.Sessions) == 1 {
			title := safeSessionCLIText(plan.Sessions[0].Title, 512)
			if title == "" {
				title = "Untitled Session"
			}
			if _, err := fmt.Fprintf(output, "Session title: %s\nLast active: %s · UTC %s\n", title,
				formatSessionLocalTime(plan.Sessions[0].LastActivityAt),
				plan.Sessions[0].LastActivityAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
	} else if _, err := fmt.Fprintf(output, "Frozen cutoff local: %s\nFrozen cutoff UTC: %s\n",
		formatSessionLocalTime(request.Cutoff), request.Cutoff.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Matched: %d · eligible: %d · protected: %d · selected: %d · limit: %d\nSelection digest: %s\n",
		plan.Snapshot.Matched, plan.Snapshot.Eligible, plan.Snapshot.Protected, len(plan.Sessions), request.Limit, plan.Digest); err != nil {
		return err
	}
	if plan.Oldest != nil && plan.Newest != nil {
		if _, err := fmt.Fprintf(output, "Selected Last active UTC: %s through %s\n", plan.Oldest.UTC().Format(time.RFC3339Nano), plan.Newest.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "Deletion includes the complete selected Session graph. It excludes exported files, terminal scrollback, logs, backups, configuration, credentials, cache, and SQLite free pages. Logical deletion is not forensic erasure.")
	return err
}

func formatSessionLocalTime(value time.Time) string {
	return value.Local().Format(time.RFC3339Nano + " MST")
}

func runCLIDoctor(ctx context.Context, manager *application.SessionManager, options *cli.DoctorOptions, info buildinfo.Info, paths config.Paths, streams sessionCommandIO) error {
	if options == nil {
		return cli.UnavailableError{}
	}
	loaded, err := config.Load(ctx, config.LoadOptions{Paths: paths})
	if err != nil {
		return err
	}
	defer loaded.Credentials.Destroy()
	health, err := manager.StorageHealth(ctx, streams.now())
	if err != nil {
		return err
	}
	version := info.Version
	if version == "" {
		version = "dev"
	}
	digest := sha256.Sum256([]byte(loaded.Models.Agent.Origin))
	doctor, err := application.NewDoctorResult(version, fmt.Sprintf("v%d", config.CurrentVersion), hex.EncodeToString(digest[:]),
		loaded.Models.Agent.Model != "", false, health)
	if err != nil {
		return err
	}
	capabilities := projectTerminalCapabilities(streams.output, streams.getenv, loaded.NoColor, loaded.TerminalStatusTitles, loaded.ReducedMotion)
	terminal := cliTerminalCapabilities{
		InteractiveInput: streams.isTTY, NativeClipboard: string(capabilities.NativeClipboard), OSC52: string(capabilities.OSC52),
		Multiplexer: string(capabilities.Multiplexer), RemoteSession: string(capabilities.RemoteSession),
		Title: string(capabilities.Title), Notification: string(capabilities.Notification), Color: string(capabilities.Color),
		AlternateScreen: string(capabilities.AlternateScreen), ReducedMotion: capabilities.ReducedMotion, Scrollback: capabilities.Scrollback,
	}
	if options.JSON {
		encoder := json.NewEncoder(streams.output)
		encoder.SetEscapeHTML(true)
		return encoder.Encode(cliDoctorEnvelope{SchemaVersion: "kupilot.cli-doctor/v1", Doctor: doctor, Terminal: terminal})
	}
	_, err = fmt.Fprintf(streams.output,
		"Application: %s\nConfiguration schema: %s\nOrigin hash: %s\nModel boundary: %s %s / %s %s / %s\nLive conformance: %s\nStorage schema: %d\nSessions: %d\nProtected Last active: %d\nPending recovery: %d\nContinuation: Protocol continuation unavailable\nTerminal native clipboard/OSC 52/title/notification: %s / %s / %s / %s\nTerminal multiplexer/remote/color/alternate screen/reduced motion/scrollback: %s / %s / %s / %s / %t / %s\nNo external checks were run.\n",
		doctor.ApplicationVersion, doctor.ConfigurationSchema, doctor.AgentOriginHash,
		doctor.ModelCompatibility.Runtime, doctor.ModelCompatibility.RuntimeVersion,
		doctor.ModelCompatibility.Adapter, doctor.ModelCompatibility.AdapterVersion,
		doctor.ModelCompatibility.Protocol, doctor.ModelCompatibility.LiveConformance,
		doctor.Storage.SchemaRevision, doctor.Storage.SessionCount, doctor.Storage.ProtectedActivity, doctor.Storage.PendingRecoveryRuns,
		terminal.NativeClipboard, terminal.OSC52, terminal.Title, terminal.Notification,
		terminal.Multiplexer, terminal.RemoteSession, terminal.Color, terminal.AlternateScreen, terminal.ReducedMotion, terminal.Scrollback)
	return err
}

func readExactConfirmation(input io.Reader) (string, error) {
	if input == nil {
		return "", io.EOF
	}
	reader := bufio.NewReader(io.LimitReader(input, maxSessionConfirmationBytes+1))
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(value) > maxSessionConfirmationBytes {
		return "", sessionCommandError("Deletion confirmation is too long.")
	}
	return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r"), nil
}

type sessionCommandError string

func (err sessionCommandError) Error() string       { return string(err) }
func (err sessionCommandError) SafeMessage() string { return string(err) }

func safeSessionCLIText(value string, limit int) string {
	if !utf8.ValidString(value) {
		return "Invalid title"
	}
	var builder strings.Builder
	for _, current := range value {
		if builder.Len() >= limit {
			break
		}
		if unicode.IsControl(current) || isBidiControl(current) {
			builder.WriteRune('�')
			continue
		}
		if builder.Len()+utf8.RuneLen(current) <= limit {
			builder.WriteRune(current)
		}
	}
	return builder.String()
}

func isBidiControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}
