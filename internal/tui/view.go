package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const (
	maxConversationPreviewTextBytes = 512
	maxConversationPreviewTextLines = 2
)

// View deterministically renders transcript, composer, suggestions, then footer.
func (model Model) View() tea.View {
	content, composerY, composerVisible := model.renderLayout()
	view := tea.NewView(content)
	if composerVisible {
		if cursor := model.composer.Cursor(); cursor != nil {
			cursor.Position.Y += composerY
			view.Cursor = cursor
		}
	}
	return model.configureView(view)
}

func (model Model) configureView(view tea.View) tea.View {
	// Completed conversation is inserted above this live primary-screen frame.
	// Keeping mouse reporting disabled leaves selection, copy, and high-density
	// wheel or trackpad scrolling under terminal ownership.
	view.AltScreen = false
	view.ReportFocus = true
	view.DisableBracketedPasteMode = false
	view.MouseMode = tea.MouseModeNone
	view.OnMouse = nil
	if model.terminalStatusTitles {
		view.WindowTitle = model.terminalTitle()
	}
	return view
}

func (model Model) terminalTitle() string {
	switch {
	case model.pendingApproval != nil:
		return "Kupilot — Approval needed"
	case model.run.Active && !model.run.Terminal:
		return "Kupilot — Working"
	case model.run.Terminal && model.run.Status == "completed":
		return "Kupilot — Complete"
	case model.run.Terminal:
		return "Kupilot — Failed"
	default:
		return "Kupilot"
	}
}

func (model Model) render() string {
	content, _, _ := model.renderLayout()
	return content
}

func (model Model) renderLayout() (content string, composerY int, composerVisible bool) {
	gap := model.layoutGap()
	contentWidth := model.contentWidth()
	if model.approvalDialog.Open() {
		return model.renderApprovalLayout(contentWidth, gap), 0, false
	}
	topSections := make([]string, 0, 2)
	if transcript := model.transcript.View(); transcript != "" {
		topSections = append(topSections, constrainLayoutWidth(transcript, contentWidth))
	}
	if working := model.workingView(); working != "" {
		topSections = append(topSections, constrainLayoutWidth(working, contentWidth))
	}
	top := joinLayoutSections(topSections, gap)

	bottomSections := make([]string, 0, 4)
	composerOffset := 0
	if label := model.inputLabelView(); label != "" {
		label = constrainLayoutWidth(label, contentWidth)
		bottomSections = append(bottomSections, label)
		composerOffset = lipgloss.Height(label)
	}
	bottomSections = append(bottomSections, constrainLayoutWidth(model.composer.View(), contentWidth))
	if model.pickerOpen() {
		bottomSections = append(bottomSections, constrainLayoutWidth(model.pickerView(), contentWidth))
	} else if model.slashMenu.Open() {
		bottomSections = append(bottomSections, constrainLayoutWidth(model.slashMenu.View(), contentWidth))
	}
	bottom := lipgloss.JoinVertical(lipgloss.Left, bottomSections...)
	if footer := model.footerView(); footer != "" {
		footer = constrainLayoutWidth(footer, contentWidth)
		bottom = joinLayoutSections([]string{bottom, footer}, gap)
	}

	liveHeight := lipgloss.Height(bottom)
	if top != "" {
		liveHeight += lipgloss.Height(top) + gap
	}
	visibleHistoryRows := min(max(0, model.terminalHistoryRows), max(0, model.height-liveHeight))
	targetHeight := max(liveHeight, model.height-visibleHistoryRows)
	spacer := max(0, targetHeight-liveHeight)
	main := bottom
	if top != "" {
		main = top + strings.Repeat("\n", spacer+gap+1) + bottom
		composerY = lipgloss.Height(top) + spacer + gap + composerOffset
	} else {
		main = strings.Repeat("\n", spacer) + bottom
		composerY = spacer + composerOffset
	}

	overlay := ""
	switch {
	case model.evidenceDialog.Open():
		overlay = model.evidenceDialog.View(model.width, model.height)
	case model.dialog.Open():
		overlay = model.dialog.View(model.width)
	}
	if overlay == "" {
		main, composerY = model.constrainLayoutHeight(main, composerY)
		return main, composerY, true
	}

	x := max(0, (model.width-lipgloss.Width(overlay))/2)
	y := max(0, (targetHeight-lipgloss.Height(overlay))/2)
	baseLayer := lipgloss.NewLayer(main).Z(0)
	overlayLayer := lipgloss.NewLayer(overlay).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlayLayer).Render(), composerY, false
}

// renderApprovalLayout gives the exact action review the whole working area.
// The transcript, Working row, and composer stay hidden so they cannot appear
// through the margins of a tall modal; the compact supervision footer remains
// visible beneath it.
func (model Model) renderApprovalLayout(contentWidth, gap int) string {
	footer := constrainLayoutWidth(model.footerView(), contentWidth)
	footerHeight := lipgloss.Height(footer)
	footerGap := 0
	if footer != "" && model.height-footerHeight > 8 {
		footerGap = gap
	}
	approvalWidth, approvalHeight := model.approvalReviewSize()
	approval := model.approvalDialog.View(approvalWidth, approvalHeight)
	content := lipgloss.Place(contentWidth, approvalHeight, lipgloss.Center, lipgloss.Center, approval)
	if footer != "" {
		content += strings.Repeat("\n", footerGap+1) + footer
	}
	return content
}

func (model Model) approvalReviewSize() (int, int) {
	contentWidth := model.contentWidth()
	footer := constrainLayoutWidth(model.footerView(), contentWidth)
	footerHeight := lipgloss.Height(footer)
	footerGap := 0
	if footer != "" && model.height-footerHeight > 8 {
		footerGap = model.layoutGap()
	}
	return contentWidth, max(1, model.height-footerHeight-footerGap)
}

func constrainLayoutWidth(content string, width int) string {
	if content == "" {
		return ""
	}
	return lipgloss.NewStyle().Width(max(1, width)).Render(content)
}

// constrainLayoutHeight performs the top-row clipping that Bubble Tea would
// otherwise do after it has forgotten the corresponding cursor adjustment.
// The focused composer row is the anchor, so even a transient tiny resize keeps
// the operating-system input method attached to the one real editor.
func (model Model) constrainLayoutHeight(content string, composerY int) (string, int) {
	rows := strings.Split(content, "\n")
	height := max(1, model.height)
	if len(rows) <= height {
		return content, composerY
	}
	start := len(rows) - height
	if cursor := model.composer.Cursor(); cursor != nil {
		cursorY := composerY + cursor.Position.Y
		if cursorY < start {
			start = cursorY
		} else if cursorY >= start+height {
			start = cursorY - height + 1
		}
	}
	start = max(0, min(start, len(rows)-height))
	return strings.Join(rows[start:start+height], "\n"), composerY - start
}

func (model Model) inputLabelView() string {
	label := ""
	hint := ""
	switch {
	case model.searchMode:
		label = "Find"
		current, total, _ := model.transcript.SearchState()
		hint = fmt.Sprintf("committed transcript · %d/%d · Enter next · Shift+Tab previous · Esc close", current, total)
	case model.historySearchMode:
		label = "Submitted input search"
		current := 0
		if len(model.historySearchMatches) > 0 {
			current = model.historySearchIndex + 1
		}
		hint = fmt.Sprintf("query %d/512 bytes · %d/%d · Ctrl+R next · Shift+Tab previous · Enter accept · Esc cancel",
			len(model.historySearchQuery), current, len(model.historySearchMatches))
	case model.modelSetup != nil:
		parts := strings.SplitN(model.modelSetupView(), " · ", 2)
		label = parts[0]
		if len(parts) == 2 {
			hint = parts[1]
		}
	case model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry:
		label = "Export target"
		hint = "absolute .md path"
	case model.pickerOpen():
		hint = "type to filter"
		if model.permissionPicker.Open() {
			label = "Permissions"
			hint = "boundary · reviewer · risk"
			break
		}
		switch model.activePicker {
		case application.UICompletionContext:
			label = "Context"
		case application.UICompletionNamespace:
			label = "Namespace"
		case application.UICompletionResource:
			label = "Resource"
		case application.UICompletionSession:
			label = "Session"
			hint = "Enter resume · D delete selected · Esc close"
		case application.UICompletionSessionManagement:
			label = "Sessions"
			hint = "Enter resume · D delete selected · B delete inactive · Esc close"
		}
	case model.slashMenu.Open():
		label = "Command"
		hint = "fixed Slash commands"
	}
	if label == "" {
		return ""
	}
	result := model.styles.footer.Value.Render(label)
	if hint != "" {
		result += model.styles.footer.Separator.Render(" · ") + model.styles.footer.Label.Render(hint)
	}
	return result
}

func joinLayoutSections(sections []string, gap int) string {
	visible := make([]string, 0, len(sections))
	for _, section := range sections {
		if section != "" {
			visible = append(visible, section)
		}
	}
	return strings.Join(visible, strings.Repeat("\n", max(0, gap)+1))
}

func (model Model) workingView() string {
	lines := make([]string, 0, 1+application.MaxConversationInputPreviewItems)
	if model.run.Active && !model.run.Terminal {
		bullet := "•"
		bulletStyle := model.styles.working.Normal
		if !model.reducedMotion && model.workingFrame/6%2 == 1 {
			bullet = "◦"
			bulletStyle = model.styles.working.Muted
		}
		working := model.styles.working.Normal.Render("Working")
		if !model.reducedMotion {
			working = model.shimmerText("Working")
		}
		line := bulletStyle.Render(bullet) + " " + working
		line += model.styles.working.Muted.Render(" (" + components.FormatElapsedCompact(model.workingElapsed()) + " • ")
		line += model.styles.working.Normal.Render("esc")
		line += model.styles.working.Muted.Render(" to interrupt)")
		lines = append(lines, line)
	}
	for index, item := range model.conversationPreview {
		if index >= application.MaxConversationInputPreviewItems {
			break
		}
		text := boundedConversationPreviewText(item.Text)
		if text == "" {
			continue
		}
		label := conversationInputLabel(item.State)
		line := model.styles.working.Muted.Render("↳ "+label+" · "+string(item.ItemID)+" · ") + model.styles.working.Normal.Render(text)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(model.contentWidth()).Render(strings.Join(lines, "\n"))
}

func conversationInputLabel(state application.ConversationInputState) string {
	switch state {
	case application.ConversationInputPending:
		return "Pending steer (accepted locally)"
	case application.ConversationInputCommitting:
		return "Committing steer"
	case application.ConversationInputQueued:
		return "Queued follow-up"
	case application.ConversationInputRejected:
		return "Rejected input"
	case application.ConversationInputRecovered:
		return "Recovered input"
	case application.ConversationInputUnknown:
		return "Unknown outcome"
	default:
		return "Conversation input"
	}
}

func boundedConversationPreviewText(value string) string {
	value = sanitizeExternalText(value, maxConversationPreviewTextBytes)
	lines := strings.Split(value, "\n")
	truncated := len(lines) > maxConversationPreviewTextLines
	if truncated {
		lines = lines[:maxConversationPreviewTextLines]
	}
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	if truncated {
		lines[len(lines)-1] = strings.TrimSpace(lines[len(lines)-1] + " …")
	}
	return strings.TrimSpace(strings.Join(lines, " / "))
}

func (model Model) workingElapsed() time.Duration {
	if model.run.StartedAt.IsZero() || model.workingAt.IsZero() || model.workingAt.Before(model.run.StartedAt) {
		return 0
	}
	return model.workingAt.Sub(model.run.StartedAt)
}

func (model Model) shimmerText(value string) string {
	const shimmerFrames = 20
	const padding = 10
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	period := len(runes) + padding*2
	position := int(model.workingFrame%shimmerFrames) * period / shimmerFrames
	var result strings.Builder
	for index, current := range runes {
		distance := index + padding - position
		if distance < 0 {
			distance = -distance
		}
		style := model.styles.working.Muted
		switch {
		case distance <= 1:
			style = model.styles.working.Highlight
		case distance <= 3:
			style = model.styles.working.Normal
		}
		result.WriteString(style.Render(string(current)))
	}
	return result.String()
}

func (model Model) footerView() string {
	approvalStatus := ""
	if model.pendingApproval != nil {
		approvalStatus = "approval pending"
		if model.approvalState == domain.ApprovalStateApproved {
			approvalStatus = "approved · not executed"
		} else if model.pendingApprovalID != 0 {
			approvalStatus = "approval in progress"
		}
	}
	if model.reviewerEvent != nil && approvalStatus == "" {
		approvalStatus = "Reviewer · " + reviewerStateLabel(model.reviewerEvent.Status.State)
	}
	permission, supervision := string(model.permission.Profile), permissionSupervision(model.permission.Profile)
	if !model.permission.Healthy {
		permission = "permission degraded"
		supervision = "no authority"
	}
	pressure := ""
	if model.contextPressure != "" {
		pressure = "context " + string(model.contextPressure)
	}
	plan := ""
	if model.planArmed {
		plan = "plan-only next"
	}
	egress := ""
	if model.run.ModelEgress != nil && model.run.Active {
		egress = fmt.Sprintf("egress %s · %d msg/%d B · %s",
			model.run.ModelEgress.CallKind, model.run.ModelEgress.MessageCount,
			model.run.ModelEgress.MessageBytes, model.run.ModelEgress.SummaryState)
	}
	return model.footer.View(model.contentWidth(), components.FooterStatus{
		Context: model.scope.Context, Namespace: model.scope.Namespace,
		ReadOnly: model.scope.ReadOnly, ScopeSwitching: model.scope.Switching,
		Permission: permission, Supervision: supervision,
		Approval:        approvalStatus,
		ContextPressure: pressure, Plan: plan, Egress: egress,
	})
}

func permissionSupervision(profile domain.PermissionProfile) string {
	switch profile {
	case domain.PermissionProfileReadOnly:
		return "safe reads only"
	case domain.PermissionProfileAsk:
		return "human"
	case domain.PermissionProfileAutoReview:
		return "Reviewer"
	case domain.PermissionProfileFullAccess:
		return "auto high-risk"
	case domain.PermissionProfileCustom:
		return "exact routes"
	default:
		return "unavailable"
	}
}
