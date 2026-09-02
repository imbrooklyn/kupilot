package components

import (
	"strings"
	"testing"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestTerminalMarkdownRendersMultipleInventorySections(t *testing.T) {
	t.Parallel()

	markdown := strings.Join([]string{
		"# Cluster overview",
		"",
		"## Nodes",
		"",
		"| Node | State | Ready |",
		"| --- | --- | --- |",
		"| development-agent-0 | Ready | 1/1 |",
		"| development-server-0 | Ready | 1/1 |",
		"",
		"## Namespaces",
		"",
		"| Namespace | State |",
		"| --- | --- |",
		"| default | Active |",
		"| system | Active |",
		"",
		"## system Pods",
		"",
		"| Pod | State | Ready | Note |",
		"| --- | --- | --- | --- |",
		"| install-ingress-controller-7k2mv | Succeeded | 0/1 | Completed |",
		"| metrics-server-786d997795-jx9nl | Running | 1/1 | - |",
	}, "\n")
	rendered := renderTerminalMarkdown(markdown, 120, MarkdownStyles{})

	for _, required := range []string{
		"Cluster overview", "Nodes", "Namespaces", "system Pods",
		"development-agent-0", "install-ingress-controller-7k2mv", "Completed",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("rendered Markdown missing %q:\n%s", required, rendered)
		}
	}
	if strings.Contains(rendered, "| --- |") || strings.Count(rendered, "━━━") < 3 {
		t.Fatalf("multiple Markdown tables were not rendered as separate grids:\n%s", rendered)
	}
	if lipglossLineWidth(rendered) > 120 {
		t.Fatalf("rendered inventory exceeded 120 cells:\n%s", rendered)
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

func TestTerminalMarkdownUsesUnicodeLineBreaksWithoutSplittingTechnicalTerms(t *testing.T) {
	t.Parallel()

	technicalPrefix := strings.Repeat("\u8282", 7)
	technical := technicalPrefix + "imagePullPolicy " +
		"\u5f71\u54cd\u4f55\u65f6\u68c0\u67e5\u955c\u50cf\uff0c\u968f\u540e\u7531 kubelet \u7ee7\u7eed\u5904\u7406\u3002"
	technicalRendered := renderTerminalMarkdown(technical, 16, MarkdownStyles{})
	technicalPlain := ansi.Strip(technicalRendered)
	if !strings.Contains(technicalPlain, "\nimagePullPolicy") {
		t.Fatalf("a line break split or stranded the technical term:\n%s", technicalPlain)
	}
	for _, term := range []string{"imagePullPolicy", "kubelet"} {
		if !strings.Contains(technicalPlain, term) {
			t.Fatalf("rendered text lost technical term %q:\n%s", term, technicalPlain)
		}
	}

	paragraph := "\u8c03\u5ea6\u5668\u9009\u5b9a\u8282\u70b9\u540e\uff0c\u4f1a\u901a\u8fc7 API Server " +
		"\u5c06\u7ed1\u5b9a\u7ed3\u679c\u5199\u56de Pod\u3002\u7ed1\u5b9a\u672c\u8eab\u4e0d\u4ee3\u8868\u5bb9\u5668\u5df2\u7ecf\u542f\u52a8\uff0c" +
		"\u53ea\u8868\u793a\u8be5 Pod \u7684\u540e\u7eed\u751f\u547d\u5468\u671f\u7531\u76ee\u6807\u8282\u70b9\u4e0a\u7684 kubelet \u8d1f\u8d23\u3002"
	styled := "\x1b[1m" + paragraph + "\x1b[0m"
	rendered := wrapTerminalText(styled, 32)
	plain := ansi.Strip(rendered)
	stripWhitespace := func(value string) string {
		return strings.Map(func(current rune) rune {
			if unicode.IsSpace(current) {
				return -1
			}
			return current
		}, value)
	}
	if got, want := stripWhitespace(plain), stripWhitespace(paragraph); got != want {
		t.Fatalf("Unicode wrapping changed text:\ngot:  %q\nwant: %q", got, paragraph)
	}
	if strings.Contains(rendered, unicodeLineBreakMarker) {
		t.Fatalf("private line-break marker escaped the renderer: %q", rendered)
	}
	if got := wrapTerminalText("safe\u200btext", 32); got != "safetext" {
		t.Fatalf("source line-break marker was retained: %q", got)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if width := lipgloss.Width(line); width > 32 {
			t.Fatalf("Unicode-wrapped line width = %d, want <= 32: %q", width, line)
		}
	}
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "\uff0c") || strings.HasPrefix(trimmed, "\u3002") {
			t.Fatalf("Unicode wrapping stranded closing punctuation: %q", line)
		}
	}
}

func TestTranscriptRendersIncrementalMarkdownBeforeFinalReplacement(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(64, 20)
	transcript.StartAgent()
	fragments := []string{
		"Pods:\n\n| Pod | ",
		"State |\n| --- | --- |\n",
		"| api-0 | Running |",
	}
	var markdown strings.Builder
	for index, fragment := range fragments {
		markdown.WriteString(fragment)
		transcript.AppendAgent(fragment)
		if !strings.Contains(transcript.View(), "Pods") {
			t.Fatalf("incremental Markdown fragment %d was not visible: %q", index, transcript.View())
		}
	}
	if view := transcript.View(); strings.Contains(view, "| --- |") ||
		!strings.Contains(view, "api-0") || !strings.Contains(view, "━━━") {
		t.Fatalf("incremental table was not rendered readably:\n%s", view)
	}
	if transcript.TerminalTranscript() != "" {
		t.Fatalf("provisional Markdown entered terminal history: %q", transcript.TerminalTranscript())
	}
	transcript.FinishAgent(markdown.String())
	if terminal := transcript.TerminalTranscript(); strings.Contains(terminal, "| --- |") ||
		!strings.Contains(terminal, "api-0") || !strings.Contains(terminal, "━━━") {
		t.Fatalf("final Markdown replacement was not rendered readably:\n%s", terminal)
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
