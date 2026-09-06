package components

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestToolStepsSeparateCompactStatusFromDetails(t *testing.T) {
	t.Parallel()
	steps := NewToolSteps(ToolStepStyles{})
	steps.Upsert(ToolStep{
		InvocationID:  "invocation-1",
		Name:          "list_resources",
		Purpose:       "List Pods in the active Namespace.",
		Status:        "succeeded",
		Summary:       "Nine projected resources were collected.",
		EvidenceCount: 9,
	})
	want := strings.Join([]string{
		"• List resources · done",
		"    └ List Pods in the active Namespace. → Nine projected resources were collected.",
	}, "\n")
	if got := steps.View(); got != want {
		t.Fatalf("ToolSteps.View() = %q, want %q", got, want)
	}
}

func TestCommittedSteerStaysBeforeTheActiveAgentWithoutDetachingIt(t *testing.T) {
	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.AppendUser("Initial question.")
	transcript.StartAgent()
	transcript.AppendAgent("Provisional text.")
	transcript.InsertUserBeforeActiveAgent("Committed steer.")
	transcript.AppendAgent(" More text.")
	transcript.FinishAgent("Final answer.")

	entries := transcript.Entries()
	if len(entries) != 3 || entries[0].Kind != EntryUser || entries[0].Text != "Initial question." ||
		entries[1].Kind != EntryUser || entries[1].Text != "Committed steer." ||
		entries[2].Kind != EntryAgent || entries[2].Text != "Final answer." || entries[2].Streaming {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestTranscriptMatchesUserSurfaceAndFinalRunTimeline(t *testing.T) {
	t.Parallel()

	surface := lipgloss.NewStyle().Background(lipgloss.Color("#303030"))
	userText := surface
	transcript := NewTranscript(TranscriptStyles{
		UserSurface: surface.Padding(1, 0),
		UserPrompt:  surface.Foreground(lipgloss.Cyan).Bold(true),
		UserText:    userText,
	}, ToolStepStyles{})
	transcript.SetSize(48, 30)
	question := "How many nodes are in the cluster?"
	transcript.AppendUser(question)
	if content := transcript.renderContent(); !strings.Contains(content, "› ") ||
		lipgloss.Height(content) != 3 || !strings.Contains(content, userText.Render(question)) {
		t.Fatalf("historic user surface does not match the three-row composer language: %q", content)
	}

	transcript.StartAgent()
	transcript.UpsertToolStep(ToolStep{
		InvocationID: "invocation-1",
		Name:         "get_cluster_overview",
		Purpose:      "Count the current cluster nodes.",
		Status:       "succeeded",
	})
	transcript.FinishAgentWithDuration("The cluster has three Ready nodes.", 20*time.Second)
	content := transcript.renderContent()
	toolAt := strings.Index(content, "Inspect cluster overview · done")
	detailAt := strings.Index(content, "    └ Count the current cluster nodes.")
	separatorAt := strings.Index(content, strings.Repeat("─", 48))
	answerAt := strings.Index(content, "The cluster has three Ready nodes.")
	timingAt := strings.Index(content, "─ Worked for 20s ─")
	if !(toolAt >= 0 && toolAt < detailAt && detailAt < separatorAt && separatorAt < answerAt && answerAt < timingAt) {
		t.Fatalf("terminal run timeline order is invalid: %q", content)
	}
	timingLine := lineContainingText(content, "Worked for 20s")
	if lipgloss.Width(timingLine) != 48 {
		t.Fatalf("Worked separator width = %d, want 48: %q", lipgloss.Width(timingLine), timingLine)
	}
}

func TestTranscriptCollapsesBulkEvidenceWithoutLosingSelection(t *testing.T) {
	t.Parallel()
	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.StartAgent()
	transcript.FinishAgent("A bounded list was collected.")
	references := make([]EvidenceReference, 9)
	for index := range references {
		references[index] = EvidenceReference{
			Index: index,
			ID:    fmt.Sprintf("00000000-0000-7000-8000-%012d", 201+index),
			State: "available",
		}
	}
	references[len(references)-1].State = "partial"
	transcript.SetAgentEvidence(references)
	defaultView := transcript.renderContent()
	for _, reference := range references {
		if strings.Contains(defaultView, reference.ID) {
			t.Fatalf("default transcript exposed internal ID %q: %q", reference.ID, defaultView)
		}
	}
	if strings.Contains(defaultView, "Evidence") || strings.Contains(defaultView, "Observation 1/") {
		t.Fatalf("default transcript exposed provenance controls: %q", defaultView)
	}

	if !transcript.BeginEvidenceSelection() {
		t.Fatal("BeginEvidenceSelection() = false")
	}
	selected := transcript.renderContent()
	if !strings.Contains(selected, "Observation 1/9 · ready") || strings.Contains(selected, references[0].ID) {
		t.Fatalf("first bulk Evidence selection = %q", selected)
	}
	transcript.MoveEvidence(1)
	selected = transcript.renderContent()
	if !strings.Contains(selected, "Observation 2/9 · ready") || strings.Contains(selected, references[1].ID) {
		t.Fatalf("second bulk Evidence selection = %q", selected)
	}
}

func TestTranscriptRetainsManagedHistoryAndProjectsOnlyCompletedEntries(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(48, 30)
	transcript.AppendUser("How many Nodes are Ready?")
	if view := transcript.View(); !strings.Contains(view, "› How many Nodes are Ready?") {
		t.Fatalf("submitted user history left the managed transcript: %q", view)
	}

	transcript.StartAgent()
	if projected := transcript.TerminalTranscript(); !strings.Contains(projected, "› How many Nodes are Ready?") ||
		strings.Contains(projected, "A provisional answer.") {
		t.Fatalf("streaming projection = %q", projected)
	}

	transcript.UpsertToolStep(ToolStep{
		InvocationID: "invocation-1",
		Name:         "get_cluster_overview",
		Purpose:      "Count Ready Nodes.",
		Status:       "succeeded",
	})
	transcript.AppendAgent("A provisional answer.")
	if projected := transcript.TerminalTranscript(); strings.Contains(projected, "A provisional answer.") ||
		strings.Contains(projected, "Count Ready Nodes.") {
		t.Fatalf("streaming Agent state entered the terminal projection: %q", projected)
	}
	transcript.FinishAgentWithDuration("Three Nodes are Ready.", 20*time.Second)
	transcript.SetAgentEvidence([]EvidenceReference{{Index: 0, ID: "internal-evidence-id", State: "available"}})
	agentBlock := transcript.TerminalTranscript()
	for _, want := range []string{"Inspect cluster overview · done", "Count Ready Nodes.", "Three Nodes are Ready.", "Worked for 20s"} {
		if !strings.Contains(agentBlock, want) {
			t.Fatalf("terminal transcript missing %q: %q", want, agentBlock)
		}
	}
	if strings.Contains(agentBlock, "internal-evidence-id") || !strings.Contains(transcript.View(), "Three Nodes are Ready.") {
		t.Fatalf("terminal transcript exposed an internal ID or left managed history: block=%q view=%q", agentBlock, transcript.View())
	}
	if repeated := transcript.TerminalTranscript(); repeated != agentBlock {
		t.Fatalf("terminal projection changed between reads:\nfirst=%q\nsecond=%q", agentBlock, repeated)
	}
	transcript.AppendAgent(" late mutation")
	transcript.FinishAgent("replacement")
	if entries := transcript.Entries(); entries[1].Text != "Three Nodes are Ready." {
		t.Fatalf("completed Agent entry accepted a late mutation: %#v", entries[1])
	}

	transcript.PageUp()
	review := transcript.View()
	if !strings.Contains(review, "How many Nodes are Ready?") || !strings.Contains(review, "Three Nodes are Ready.") {
		t.Fatalf("retained transcript review = %q", review)
	}
	transcript.PageDown()
	if transcript.reviewing || !strings.Contains(transcript.View(), "Three Nodes are Ready.") {
		t.Fatalf("PageDown did not return to the live transcript bottom: reviewing=%v view=%q", transcript.reviewing, transcript.View())
	}

	transcript.PageUp()
	transcript.AppendUser("What changed?")
	transcript.StartAgent()
	if transcript.reviewing || !strings.Contains(transcript.View(), "What changed?") ||
		strings.Contains(transcript.View(), "Working") {
		t.Fatalf("new run did not return history review to the live projection: %q", transcript.View())
	}
}

func TestTranscriptClearsPreToolProvisionalTextAndAcceptsFinalStream(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(48, 20)
	transcript.StartAgent()
	transcript.AppendAgent("Discard this pre-Tool draft.")
	transcript.ClearAgent()
	transcript.UpsertToolStep(ToolStep{
		InvocationID: "invocation-1",
		Name:         "get_resource",
		Purpose:      "Inspect one resource.",
		Status:       "requested",
	})
	transcript.AppendAgent("Keep this final answer.")
	entries := transcript.Entries()
	if len(entries) != 1 || entries[0].Text != "Keep this final answer." ||
		strings.Contains(transcript.View(), "Discard this pre-Tool draft.") {
		t.Fatalf("cleared/final provisional transcript = %#v / %q", entries, transcript.View())
	}
	transcript.FinishAgent("Keep this final answer.")
	if strings.Contains(transcript.TerminalTranscript(), "Discard this pre-Tool draft.") ||
		!strings.Contains(transcript.TerminalTranscript(), "Keep this final answer.") {
		t.Fatalf("terminal transcript retained pre-Tool draft: %q", transcript.TerminalTranscript())
	}
}

func TestTranscriptCommitsOnlyImmutableHistoryExactlyOnce(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(48, 20)
	transcript.AppendUser("Inspect the current cluster.")
	transcript.StartAgent()
	transcript.AppendAgent("Provisional answer.")
	transcript.AppendNotice("Validation is still running.")

	first, firstRows := transcript.CommitReady()
	if firstRows == 0 || !strings.Contains(first, "Inspect the current cluster.") ||
		strings.Contains(first, "Provisional answer.") || strings.Contains(first, "Validation is still running.") ||
		!strings.HasSuffix(first, "\n") || strings.HasSuffix(first, "\n\n") {
		t.Fatalf("first immutable history commit = %q rows=%d", first, firstRows)
	}
	if contentRows := lipgloss.Height(strings.TrimSuffix(first, "\n")); firstRows != contentRows+terminalHistorySeparatorRows {
		t.Fatalf("first immutable history rows = %d, want content %d + separator %d",
			firstRows, contentRows, terminalHistorySeparatorRows)
	}
	if repeated, rows := transcript.CommitReady(); repeated != "" || rows != 0 {
		t.Fatalf("immutable history replayed: block=%q rows=%d", repeated, rows)
	}
	if view := transcript.View(); !strings.Contains(view, "Provisional answer.") ||
		strings.Contains(view, "Inspect the current cluster.") {
		t.Fatalf("live frame did not separate committed and provisional history: %q", view)
	}

	transcript.FinishAgentWithDuration("Final answer.", 2*time.Second)
	second, secondRows := transcript.CommitReady()
	if secondRows == 0 || strings.HasPrefix(second, "\n") || !strings.HasSuffix(second, "\n") ||
		strings.HasSuffix(second, "\n\n") ||
		!strings.Contains(second, "Final answer.") ||
		!strings.Contains(second, "Validation is still running.") ||
		strings.Contains(second, "Provisional answer.") {
		t.Fatalf("terminal Agent history commit = %q rows=%d", second, secondRows)
	}
	if transcript.PendingTerminalTranscript() != "" || transcript.View() != "" {
		t.Fatalf("completed history remained pending or live: pending=%q view=%q",
			transcript.PendingTerminalTranscript(), transcript.View())
	}
	transcript.PageUp()
	if review := transcript.View(); !strings.Contains(review, "Inspect the current cluster.") ||
		!strings.Contains(review, "Final answer.") {
		t.Fatalf("committed history was unavailable to keyboard review: %q", review)
	}
}

func TestTranscriptPreparedCommitRemainsRecoverableUntilAcknowledged(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(48, 12)
	transcript.AppendNotice("Completed notice.")
	block, rows, end := transcript.PrepareCommit()
	if block == "" || rows == 0 || end == 0 || transcript.View() != "" ||
		transcript.PendingTerminalTranscript() != block || !strings.HasSuffix(block, "\n") ||
		!strings.Contains(transcript.PendingTerminalTranscript(), "Completed notice.") {
		t.Fatalf("prepared commit block=%q rows=%d end=%d view=%q pending=%q",
			block, rows, end, transcript.View(), transcript.PendingTerminalTranscript())
	}
	if transcript.CompleteCommit(end+1) || transcript.PendingTerminalTranscript() == "" {
		t.Fatal("a stale terminal acknowledgement changed prepared history")
	}
	if !transcript.CompleteCommit(end) || transcript.PendingTerminalTranscript() != "" {
		t.Fatal("the exact terminal acknowledgement did not complete prepared history")
	}
}

func TestTranscriptAbortedCommitReturnsToLiveProjection(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(48, 12)
	transcript.AppendNotice("Keep this visible.")
	_, _, end := transcript.PrepareCommit()
	if !transcript.AbortCommit(end) || !strings.Contains(transcript.View(), "Keep this visible.") ||
		!strings.Contains(transcript.PendingTerminalTranscript(), "Keep this visible.") {
		t.Fatalf("aborted commit view=%q pending=%q", transcript.View(), transcript.PendingTerminalTranscript())
	}
}

func TestTerminalTranscriptIncludesTrailingUserAndKeepsContinuationAligned(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{
		UserSurface: lipgloss.NewStyle().Padding(1, 0),
	}, ToolStepStyles{})
	transcript.SetSize(24, 10)
	transcript.AppendUser("first line\nthird line\nfourth line")
	block := transcript.TerminalTranscript()
	if block == "" || strings.Count(block, "›") != 1 {
		t.Fatalf("terminal transcript did not retain one historic prompt: %q", block)
	}
	lines := strings.Split(block, "\n")
	firstColumn := columnContainingText(lines, "first line")
	thirdColumn := columnContainingText(lines, "third line")
	fourthColumn := columnContainingText(lines, "fourth line")
	if firstColumn < 0 || thirdColumn != firstColumn || fourthColumn != firstColumn {
		t.Fatalf("historic continuation columns = %d, %d, %d:\n%s", firstColumn, thirdColumn, fourthColumn, block)
	}
	if repeated := transcript.TerminalTranscript(); repeated != block {
		t.Fatalf("terminal transcript was not deterministic: %q", repeated)
	}
}

func TestTerminalTranscriptWrapsEastAsianUserTextWithoutSplittingTechnicalTerms(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.SetSize(18, 10)
	transcript.AppendUser(strings.Repeat("\u8282", 7) + "imagePullPolicy")
	block := transcript.TerminalTranscript()
	if strings.Count(block, "›") != 1 || !strings.Contains(block, "\n  imagePullPolicy") {
		t.Fatalf("historic user wrapping did not preserve one prompt and one technical term: %q", block)
	}
	for _, line := range strings.Split(block, "\n") {
		if width := lipgloss.Width(line); width > 18 {
			t.Fatalf("historic user line width = %d, want <= 18: %q", width, line)
		}
	}
}

func TestTranscriptReflowFollowsLiveBottomAndPreservesExplicitReview(t *testing.T) {
	t.Parallel()

	transcript := NewTranscript(TranscriptStyles{
		UserSurface: lipgloss.NewStyle().Padding(1, 0),
	}, ToolStepStyles{})
	transcript.SetSize(40, 4)
	for index := range 16 {
		transcript.AppendNotice(fmt.Sprintf("Historic row %02d", index))
	}
	transcript.AppendUser("Newest submitted question.")
	assertPaddedUserEntryVisible := func(stage string) {
		t.Helper()
		lines := strings.Split(transcript.View(), "\n")
		textRow := -1
		for index, line := range lines {
			if strings.Contains(line, "Newest submitted question.") {
				textRow = index
				break
			}
		}
		if textRow <= 0 || textRow >= len(lines)-1 ||
			strings.TrimSpace(lines[textRow-1]) != "" || strings.TrimSpace(lines[textRow+1]) != "" {
			t.Fatalf("%s did not keep the complete submitted-user surface at the live bottom: %q", stage, transcript.View())
		}
	}
	assertPaddedUserEntryVisible("initial layout")

	transcript.SetSize(40, 3)
	if !transcript.viewport.AtBottom() {
		t.Fatal("live transcript reflow detached from the bottom")
	}
	assertPaddedUserEntryVisible("smaller Working layout")

	transcript.StartAgent()
	transcript.AppendAgent("Initial provisional answer.")
	transcript.ScrollUp(2)
	if !transcript.reviewing || transcript.viewport.AtBottom() {
		t.Fatal("explicit transcript scroll did not enter review state")
	}
	reviewOffset := transcript.ScrollOffset()
	transcript.SetSize(40, 2)
	transcript.AppendAgent("\n\nA later provisional paragraph.")
	if !transcript.reviewing || transcript.viewport.AtBottom() || transcript.ScrollOffset() != reviewOffset {
		t.Fatalf("reflow or live update discarded explicit review: reviewing=%v offset=%d want=%d",
			transcript.reviewing, transcript.ScrollOffset(), reviewOffset)
	}
}

func columnContainingText(lines []string, value string) int {
	for _, line := range lines {
		if index := strings.Index(line, value); index >= 0 {
			return lipgloss.Width(line[:index])
		}
	}
	return -1
}

func TestWorkedDurationUsesCompactCodexFormatting(t *testing.T) {
	t.Parallel()

	tests := map[time.Duration]string{
		59 * time.Second:                      "59s",
		time.Minute:                           "1m 00s",
		4*time.Minute + 43*time.Second:        "4m 43s",
		time.Hour + time.Minute + time.Second: "1h 01m 01s",
	}
	for duration, want := range tests {
		if got := formatElapsedCompact(duration); got != want {
			t.Fatalf("formatElapsedCompact(%s) = %q, want %q", duration, got, want)
		}
	}
}

func lineContainingText(content, text string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

func TestToolStepNamesCoverTheFixedCatalogWithoutProtocolIdentifiers(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"get_resource":          "Inspect resource",
		"list_resources":        "List resources",
		"get_events":            "Read events",
		"get_pod_logs":          "Read current logs",
		"get_previous_pod_logs": "Read previous logs",
		"get_related_resources": "Inspect related resources",
		"get_cluster_overview":  "Inspect cluster overview",
	}
	for name, want := range tests {
		if got := toolStepDisplayName(name); got != want || strings.Contains(got, "_") {
			t.Fatalf("toolStepDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPickersShowHumanLabelsWithoutRepeatedNamespaceOrInternalPrefixes(t *testing.T) {
	t.Parallel()

	resources := NewResourcePicker(PickerStyles{})
	resources.SetCandidates([]ResourceCandidate{{
		APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api-7d9", Status: "Pending",
	}})
	resourceView := resources.View()
	if !strings.Contains(resourceView, "Pod/payment-api-7d9 · Pending") || strings.Contains(resourceView, "ns/") {
		t.Fatalf("resource picker = %q", resourceView)
	}

	sessions := NewSessionPicker(PickerStyles{})
	sessions.SetCandidates([]SessionCandidate{{
		ID: "session-1", Title: "Payment diagnosis", Context: "development", Namespace: "payments", Privacy: "standard",
	}})
	sessionView := sessions.View()
	if !strings.Contains(sessionView, "development / payments") || !strings.Contains(sessionView, "history saved") ||
		strings.Contains(sessionView, "ctx/") || strings.Contains(sessionView, "ns/") || strings.Contains(sessionView, "privacy/") {
		t.Fatalf("session picker = %q", sessionView)
	}
}
