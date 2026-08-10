package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestViewStructureKeepsTranscriptComposerSuggestionsAndFooterOrder(t *testing.T) {
	t.Parallel()

	model := populatedViewModel(t)
	content := sanitizeExternalText(model.View().Content, 0)
	if containsUnsafeTerminalText(content) {
		t.Fatalf("view contains unsafe external terminal text: %q", content)
	}

	questionAt := strings.Index(content, "Why is the Pod restarting?")
	agentAt := strings.Index(content, "Final diagnosis.")
	toolAt := strings.Index(content, "get_resource · succeeded")
	composerAt := strings.Index(content, "/r")
	candidateAt := strings.Index(content, "/resource")
	footerAt := strings.Index(content, "ctx/test-context")
	if !(questionAt >= 0 && questionAt < agentAt && agentAt < toolAt && toolAt < composerAt && composerAt < candidateAt && candidateAt < footerAt) {
		t.Fatalf("unexpected vertical order: question=%d agent=%d tool=%d composer=%d candidate=%d footer=%d\n%s",
			questionAt, agentAt, toolAt, composerAt, candidateAt, footerAt, content)
	}

	lines := strings.Split(content, "\n")
	questionLine := lineContaining(lines, "Why is the Pod restarting?")
	composerLine := lineContaining(lines, "/r")
	agentLine := lineContaining(lines, "Final diagnosis.")
	if !strings.Contains(questionLine, "│") || !strings.Contains(composerLine, "│") {
		t.Fatalf("user/composer surface mismatch: %q / %q", questionLine, composerLine)
	}
	if strings.Contains(agentLine, "│") || strings.Contains(agentLine, "You:") || strings.Contains(agentLine, "KuPilot:") {
		t.Fatalf("Agent prose is framed or role-labeled: %q", agentLine)
	}
	if strings.Contains(content, "Commands:") || strings.Contains(content, "Command mode") || strings.Contains(content, "Ask KuPilot") {
		t.Fatalf("view contains a forbidden mode title: %q", content)
	}
	if countCandidateLines(lines) > MaxSlashCandidates {
		t.Fatal("view rendered more than eight Slash candidates")
	}
}

func TestViewNoColorSemanticGolden(t *testing.T) {
	t.Parallel()

	model := populatedViewModel(t)
	got := semanticViewSnapshot(sanitizeExternalText(model.View().Content, 0))
	want := strings.TrimSpace(`<surface-border>
<user-surface> Why is the Pod restarting?
<surface-border>
<agent> Final diagnosis.
<tool> get_resource · succeeded
<surface-border>
<composer> /r
<surface-border>
<candidate> /resource
<footer> ctx/test-context · ns/test-namespace · read-only`)
	if got != want {
		t.Fatalf("semantic golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestViewHelpAndErrorCopyIsEnglishAndModalIsNonEditable(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/unknown"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatal("error modal changed the editor invariant or dispatched an action")
	}
	content := model.View().Content
	if !strings.Contains(content, "Unknown command") || !strings.Contains(content, "Type /help") {
		t.Fatalf("safe error copy missing: %q", content)
	}
	model, _ = updateModel(t, model, keyText("x"))
	if model.composer.Value() != "/unknown" {
		t.Fatal("modal leaked printable input to the background composer")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.dialog.Open() || model.FocusedEditorCount() != 1 || model.composer.Value() != "/unknown" {
		t.Fatal("closing the modal did not restore the sole composer and draft")
	}
}

func TestSemanticPaletteModesKeepMeaningIndependentOfColor(t *testing.T) {
	t.Parallel()

	for _, mode := range []ThemeMode{ThemeDark, ThemeLight, ThemeANSI16} {
		if !SemanticPaletteFor(mode, true).ColorEnabled {
			t.Fatalf("theme %v unexpectedly disabled color", mode)
		}
	}
	if SemanticPaletteFor(ThemeNoColor, true).ColorEnabled {
		t.Fatal("no-color palette reports color enabled")
	}
	if !strings.Contains(newTestModel().View().Content, "read-only") {
		t.Fatal("read-only state depends on color")
	}
}

func populatedViewModel(t *testing.T) Model {
	t.Helper()
	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Why is the Pod restarting?"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, Sequence: 2, Text: "Inspecting.",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
			Purpose: "Inspect the selected Pod.", Status: application.ToolStepSucceeded, EvidenceCount: 2,
		},
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID, ScopeGeneration: 7, Sequence: 4, Text: "Final diagnosis.",
	}})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/r"})
	return model
}

func lineContaining(lines []string, value string) string {
	for _, line := range lines {
		if strings.Contains(line, value) {
			return line
		}
	}
	return ""
}

func countCandidateLines(lines []string) int {
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "› /") || strings.HasPrefix(trimmed, "/resource") ||
			strings.HasPrefix(trimmed, "/resume") || strings.HasPrefix(trimmed, "/rename") {
			count++
		}
	}
	return count
}

func semanticViewSnapshot(content string) string {
	var snapshot []string
	borderCount := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "╰"):
			if borderCount < 4 {
				snapshot = append(snapshot, "<surface-border>")
				borderCount++
			}
		case strings.Contains(trimmed, "Why is the Pod restarting?"):
			snapshot = append(snapshot, "<user-surface> Why is the Pod restarting?")
		case strings.Contains(trimmed, "Final diagnosis."):
			snapshot = append(snapshot, "<agent> Final diagnosis.")
		case strings.Contains(trimmed, "get_resource · succeeded"):
			snapshot = append(snapshot, "<tool> get_resource · succeeded")
		case strings.Contains(trimmed, "│ /r"):
			snapshot = append(snapshot, "<composer> /r")
		case strings.HasPrefix(trimmed, "› /resource"):
			snapshot = append(snapshot, "<candidate> /resource")
		case strings.Contains(trimmed, "ctx/test-context"):
			snapshot = append(snapshot, "<footer> ctx/test-context · ns/test-namespace · read-only")
		}
	}
	return strings.Join(snapshot, "\n")
}
