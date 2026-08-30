package components

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTerminalMarkdownRendersReadableTablesAtWideAndNarrowWidths(t *testing.T) {
	t.Parallel()

	markdown := strings.Join([]string{
		"Pods:",
		"",
		"| Pod | State | Ready |",
		"| --- | --- | --- |",
		"| api-0 | Running | 1/1 |",
		"| install-0 | Succeeded | 0/1 |",
	}, "\n")
	wide := renderTerminalMarkdown(markdown, 72, MarkdownStyles{})
	if strings.Contains(wide, "| --- |") || !strings.Contains(wide, "api-0") ||
		!strings.Contains(wide, "install-0") || strings.Count(wide, "\n") < 4 {
		t.Fatalf("wide Markdown table is not readable:\n%s", wide)
	}
	if !strings.Contains(wide, "━━━") || !strings.Contains(wide, "───") ||
		strings.ContainsAny(wide, "│┌┐└┘") {
		t.Fatalf("wide Markdown table does not use the borderless Codex-style grid:\n%s", wide)
	}
	if lipglossLineWidth(wide) > 72 {
		t.Fatalf("wide Markdown table exceeded 72 cells:\n%s", wide)
	}

	narrow := renderTerminalMarkdown(markdown, 28, MarkdownStyles{})
	if strings.Contains(narrow, "| --- |") || !strings.Contains(narrow, "Pod") ||
		!strings.Contains(narrow, "State") || !strings.Contains(narrow, "Ready") ||
		!strings.Contains(narrow, strings.Repeat("─", 28)) {
		t.Fatalf("narrow Markdown table did not fall back to readable records:\n%s", narrow)
	}
	if lipglossLineWidth(narrow) > 28 {
		t.Fatalf("narrow Markdown records exceeded 28 cells:\n%s", narrow)
	}
}

func TestTerminalMarkdownRepairsOnlyUnambiguousInlineTableRows(t *testing.T) {
	t.Parallel()

	compact := "Pods: | Pod | State | Ready | |---|---|---| | api-0 | Running | 1/1 | | install-0 | Succeeded | 0/1 | Two Pods were returned."
	normalized := normalizeInlineMarkdownTables(compact)
	for _, required := range []string{
		"Pods:\n\n| Pod | State | Ready |",
		"|---|---|---|\n| api-0 | Running | 1/1 |",
		"| install-0 | Succeeded | 0/1 |\n\nTwo Pods were returned.",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("normalized compact table missing %q:\n%s", required, normalized)
		}
	}
	rendered := renderTerminalMarkdown(compact, 72, MarkdownStyles{})
	if strings.Contains(rendered, "|---|") || !strings.Contains(rendered, "api-0") ||
		!strings.Contains(rendered, "Two Pods were returned.") {
		t.Fatalf("compact table was not rendered safely:\n%s", rendered)
	}

	ordinary := "Keep this literal sequence | | because it has no table delimiter."
	if got := normalizeInlineMarkdownTables(ordinary); got != ordinary {
		t.Fatalf("ordinary prose changed from %q to %q", ordinary, got)
	}
}

func TestTerminalMarkdownRendersListsAndLinksAsInertText(t *testing.T) {
	t.Parallel()

	markdown := "- **Ready**: [documentation](https://example.test/docs)\n- Run `inspect` next."
	rendered := renderTerminalMarkdown(markdown, 48, MarkdownStyles{})
	for _, required := range []string{"• Ready", "documentation", "https://example.test/docs", "inspect"} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("rendered Markdown missing %q:\n%s", required, rendered)
		}
	}
	for _, forbidden := range []string{"**", "](", "\x1b]8;"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("rendered Markdown contains active or raw syntax %q:\n%s", forbidden, rendered)
		}
	}
}

func TestTranscriptCachesRenderedMarkdownAndInvalidatesOnLayoutChanges(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(64, 20)
	transcript.StartAgent()
	transcript.FinishAgent("**Ready**")
	if len(transcript.entries) != 1 || !transcript.entries[0].markdownCacheValid ||
		transcript.entries[0].markdownCacheWidth != 64 || transcript.entries[0].markdownCache != "Ready" {
		t.Fatalf("initial Markdown cache = %#v", transcript.entries)
	}
	first := transcript.entries[0].markdownCache
	_ = transcript.renderContent()
	if transcript.entries[0].markdownCache != first {
		t.Fatal("unchanged Markdown was rendered into a different cache value")
	}
	transcript.SetSize(28, 20)
	if !transcript.entries[0].markdownCacheValid || transcript.entries[0].markdownCacheWidth != 28 {
		t.Fatalf("resized Markdown cache = %#v", transcript.entries[0])
	}
	transcript.SetStyles(TranscriptStyles{}, ToolStepStyles{})
	if !transcript.entries[0].markdownCacheValid || transcript.entries[0].markdownCacheWidth != 28 {
		t.Fatalf("restyled Markdown cache = %#v", transcript.entries[0])
	}
	publicEntries := transcript.Entries()
	if publicEntries[0].markdownCacheValid || publicEntries[0].markdownCache != "" {
		t.Fatalf("Entries exposed internal render cache: %#v", publicEntries[0])
	}
}

func lipglossLineWidth(value string) int {
	maximum := 0
	for _, line := range strings.Split(value, "\n") {
		maximum = max(maximum, lipgloss.Width(line))
	}
	return maximum
}
