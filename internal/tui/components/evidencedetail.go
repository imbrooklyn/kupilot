package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// EvidenceDetailStyles distinguish neutral, partial, and unavailable states
// without relying on color alone.
type EvidenceDetailStyles struct {
	Frame   lipgloss.Style
	Title   lipgloss.Style
	Body    lipgloss.Style
	Muted   lipgloss.Style
	Warning lipgloss.Style
}

// EvidenceDetailContent contains only Application-projected display strings.
type EvidenceDetailContent struct {
	Category          string
	Scope             string
	Resource          string
	ObservedAt        string
	Status            string
	SensitiveFiltered bool
	Projection        string
	Claims            string
}

type evidenceDialogState uint8

const (
	evidenceDialogClosed evidenceDialogState = iota
	evidenceDialogLoading
	evidenceDialogAvailable
	evidenceDialogPartial
	evidenceDialogExpired
	evidenceDialogUnavailable
)

// EvidenceDetailDialog is one non-editable bounded overlay. It owns no query,
// repository, raw payload, or second input control.
type EvidenceDetailDialog struct {
	state      evidenceDialogState
	evidenceID string
	content    EvidenceDetailContent
	styles     EvidenceDetailStyles
}

// NewEvidenceDetailDialog creates a closed display-only overlay.
func NewEvidenceDetailDialog(styles EvidenceDetailStyles) EvidenceDetailDialog {
	return EvidenceDetailDialog{styles: styles}
}

// SetStyles updates presentation without changing evidence state.
func (dialog *EvidenceDetailDialog) SetStyles(styles EvidenceDetailStyles) { dialog.styles = styles }

// ShowLoading opens a request-bound placeholder without observation content.
func (dialog *EvidenceDetailDialog) ShowLoading(evidenceID string) {
	dialog.state = evidenceDialogLoading
	dialog.evidenceID = evidenceID
	dialog.content = EvidenceDetailContent{}
}

// ShowDetail opens one terminal safe detail result.
func (dialog *EvidenceDetailDialog) ShowDetail(content EvidenceDetailContent, partial bool) {
	dialog.state = evidenceDialogAvailable
	if partial {
		dialog.state = evidenceDialogPartial
	}
	dialog.content = content
}

// ShowExpired reports deleted or expired supporting detail without revealing
// which retention or lookup condition occurred.
func (dialog *EvidenceDetailDialog) ShowExpired(evidenceID string) {
	dialog.state = evidenceDialogExpired
	dialog.evidenceID = evidenceID
	dialog.content = EvidenceDetailContent{}
}

// ShowUnavailable reports a fail-closed lookup or identity failure.
func (dialog *EvidenceDetailDialog) ShowUnavailable(evidenceID string) {
	dialog.state = evidenceDialogUnavailable
	dialog.evidenceID = evidenceID
	dialog.content = EvidenceDetailContent{}
}

// Close clears every projected detail string.
func (dialog *EvidenceDetailDialog) Close() {
	dialog.state = evidenceDialogClosed
	dialog.evidenceID = ""
	dialog.content = EvidenceDetailContent{}
}

// Open reports whether this overlay owns keyboard focus.
func (dialog EvidenceDetailDialog) Open() bool { return dialog.state != evidenceDialogClosed }

// View renders within the current terminal bounds and clips excess safe
// projection text instead of adding scrolling or another navigation surface.
func (dialog EvidenceDetailDialog) View(width, height int) string {
	if !dialog.Open() {
		return ""
	}
	lines := []string{
		dialog.styles.Title.Render("Observation detail"),
		dialog.styles.Muted.Render("Esc, Ctrl+C, or Enter to close"),
	}
	switch dialog.state {
	case evidenceDialogLoading:
		lines = append(lines,
			dialog.styles.Body.Render("Loading the saved observation…"),
		)
	case evidenceDialogExpired:
		lines = append(lines,
			dialog.styles.Warning.Render("State: expired"),
			dialog.styles.Body.Render("Supporting detail was deleted, expired, or is no longer retained."),
		)
	case evidenceDialogUnavailable:
		lines = append(lines,
			dialog.styles.Warning.Render("State: unavailable"),
			dialog.styles.Body.Render("The detail could not be matched to the recorded run and scope safely."),
		)
	case evidenceDialogAvailable, evidenceDialogPartial:
		content := dialog.content
		lines = append(lines,
			dialog.styles.Body.Render("Resource: "+content.Resource),
			dialog.styles.Body.Render("Scope: "+content.Scope),
			dialog.styles.Body.Render("Observed: "+content.ObservedAt),
			dialog.styles.Body.Render("Type: "+observationCategoryLabel(content.Category)),
			dialog.styles.Body.Render("Status: "+content.Status),
		)
		if content.Claims != "" {
			lines = append(lines, dialog.styles.Body.Render("Referenced by claims: "+content.Claims))
		}
		if content.SensitiveFiltered {
			lines = append(lines, dialog.styles.Warning.Render("Sensitive values were filtered."))
		}
		lines = append(lines,
			dialog.styles.Muted.Render("Details:"),
			dialog.styles.Body.Render(content.Projection),
		)
	}
	contentWidth := max(1, min(width-6, 82))
	contentHeight := max(1, height-4)
	return dialog.styles.Frame.Width(contentWidth).MaxHeight(contentHeight).Render(strings.Join(lines, "\n"))
}

func observationCategoryLabel(category string) string {
	switch category {
	case "resource_status":
		return "Resource status"
	case "condition":
		return "Condition"
	case "container_state":
		return "Container state"
	case "event":
		return "Kubernetes event"
	case "log_excerpt":
		return "Log excerpt"
	case "owner":
		return "Owner relationship"
	case "rollout":
		return "Rollout status"
	case "service_endpoint":
		return "Service readiness"
	default:
		return "Observation"
	}
}
