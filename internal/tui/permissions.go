package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func (model *Model) showPermissionPicker(result application.UIPermissionsResult) {
	model.permission = result.Permission
	model.permissionReviewer = result.Reviewer
	model.permissionConfirmation = nil
	model.closePickers()
	model.composer.Reset()
	model.composer.SetPlaceholder("Filter the five fixed permission profiles")
	model.refreshPermissionPicker()
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func (model *Model) refreshPermissionPicker() {
	filter := strings.ToLower(strings.TrimSpace(model.composer.Value()))
	values := permissionCandidates(model.permission, model.permissionReviewer)
	filtered := make([]components.PermissionCandidate, 0, len(values))
	for _, candidate := range values {
		haystack := strings.ToLower(strings.Join([]string{
			string(candidate.Profile), candidate.Boundary, candidate.Reviewer, candidate.Risk,
		}, " "))
		if filter == "" || strings.Contains(haystack, filter) {
			filtered = append(filtered, candidate)
		}
	}
	model.permissionPicker.SetCandidates(filtered)
}

func permissionCandidates(
	permission application.UIPermissionStatus,
	reviewer application.UIModelRoleStatus,
) []components.PermissionCandidate {
	reviewerIdentity := "optional reviewer unavailable"
	if reviewer.Configured {
		reviewerIdentity = reviewer.Profile + " / " + reviewer.OriginHash
		if !reviewer.Available {
			reviewerIdentity += " (unavailable)"
		} else if !reviewer.Consented {
			reviewerIdentity += " (consent required)"
		}
	}
	customRisk := "missing routes deny; critical defaults human"
	for _, route := range permission.CustomRoutes {
		if route.Risk == domain.RiskCritical && route.Disposition == domain.ReviewDispositionAutomatic {
			customRisk = "high risk; exact enabled cluster or local actions may run without prompts"
			break
		}
	}
	return []components.PermissionCandidate{
		{
			Profile: domain.PermissionProfileReadOnly, Current: permission.Profile == domain.PermissionProfileReadOnly,
			Boundary: "safe reads automatic; sensitive reads human; mutation and execution denied",
			Reviewer: "never routes", Risk: "approval cannot elevate denied effects",
		},
		{
			Profile: domain.PermissionProfileAsk, Current: permission.Profile == domain.PermissionProfileAsk,
			Boundary: "safe automatic; review and critical require the local user",
			Reviewer: "not used", Risk: "default supervised profile",
		},
		{
			Profile: domain.PermissionProfileAutoReview, Current: permission.Profile == domain.PermissionProfileAutoReview,
			Boundary: "safe automatic; review delegated; critical remains human",
			Reviewer: reviewerIdentity, Risk: "review failure or timeout executes nothing",
		},
		{
			Profile: domain.PermissionProfileFullAccess, Current: permission.Profile == domain.PermissionProfileFullAccess,
			Boundary: "admitted and enabled review/critical may route automatically",
			Reviewer: "not used", Risk: "high risk; enabled cluster, Pod, or local process actions may run without prompts",
		},
		{
			Profile: domain.PermissionProfileCustom, Current: permission.Profile == domain.PermissionProfileCustom,
			Boundary: "exact routes only; missing safe/review routes deny; critical defaults human",
			Reviewer: "review routes only", Risk: customRisk,
		},
	}
}

func reviewerIdentity(status application.UIReviewerStatus) string {
	identity := "optional approval_reviewer"
	if status.Profile != "" {
		identity = status.Profile + " / " + status.OriginHash
	}
	return identity + " · automated " + reviewerStateLabel(status.State)
}

func reviewerStateLabel(state application.UIReviewerState) string {
	switch state {
	case application.UIReviewerReviewing:
		return "Reviewing"
	case application.UIReviewerApproved:
		return "Approved recommendation"
	case application.UIReviewerDenied:
		return "Denied recommendation"
	case application.UIReviewerEscalated:
		return "Escalated to user"
	case application.UIReviewerTimedOut:
		return "Timed out"
	default:
		return "Unavailable"
	}
}

func (model Model) selectPermissionCandidate() (tea.Model, tea.Cmd) {
	candidate, ok := model.permissionPicker.Selected()
	if !ok || model.pendingPermissionID != 0 {
		return model, nil
	}
	if candidate.Profile == model.permission.Profile {
		model.closePickers()
		model.transcript.AppendNotice("The permission profile is unchanged.")
		model.reflow()
		return model, nil
	}
	if candidate.Profile == domain.PermissionProfileFullAccess {
		profile := candidate.Profile
		model.permissionConfirmation = &profile
		model.permissionPicker.Close()
		model.composer.Reset()
		model.composer.ResetPlaceholder()
		model.showDialog(
			"Enable full access?",
			"This high-risk profile may route admitted and explicitly enabled review and critical operations automatically. Enabled cluster mutations, Pod diagnostics or Exec, diagnostic Pods, and policy-selected local processes or shell may run without another prompt. It does not grant RBAC, widen Context or Namespace scope, create consent, enable default-off capabilities, lower risk, or bypass audit, limits, revalidation, and hard denial.\n\nY enables it. Enter, Esc, or Ctrl+C keeps the current profile.",
		)
		model.reflow()
		return model, nil
	}
	return model.submitPermissionChange(candidate.Profile, false)
}

func (model Model) submitPermissionChange(profile domain.PermissionProfile, highRisk bool) (tea.Model, tea.Cmd) {
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: application.UICommandChangePermission, RequestID: requestID,
		ExpectedPolicyGeneration: model.permission.PolicyGeneration,
		PermissionProfile:        profile, HighRiskAcknowledged: highRisk,
	}
	if command.Validate() != nil {
		model.closePickers()
		model.showDialog("Permission unavailable", "The permission change could not be represented safely.")
		return model, nil
	}
	model.pendingPermissionID = requestID
	model.closePickers()
	model.reflow()
	return model, applicationCommand(command)
}

func (model Model) updatePermissionConfirmationKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.permissionConfirmation == nil {
		return model, nil
	}
	if strings.EqualFold(message.Text, "y") {
		profile := *model.permissionConfirmation
		model.permissionConfirmation = nil
		model.closeDialog()
		return model.submitPermissionChange(profile, true)
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit) ||
		key.Matches(message, model.keymap.Submit) {
		model.permissionConfirmation = nil
		model.closeDialog()
		model.transcript.AppendNotice("The permission profile was not changed.")
		model.reflow()
	}
	return model, nil
}

func permissionChangeNotice(result application.UIPermissionsResult) string {
	if result.RuleCreated {
		return fmt.Sprintf("A narrow current-Session rule was created under policy generation %d. The source action was invalidated and was not executed; a fresh request is required.", result.Permission.PolicyGeneration)
	}
	return fmt.Sprintf("Permission profile changed to %s at policy generation %d. Dependent reviews, approvals, rules, and old run work were invalidated before cancellation.", result.Permission.Profile, result.Permission.PolicyGeneration)
}
