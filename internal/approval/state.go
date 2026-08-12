package approval

import "github.com/imbrooklyn/kupilot/internal/domain"

type transitionAction string

const (
	actionApprove    transitionAction = "approve"
	actionReject     transitionAction = "reject"
	actionExpire     transitionAction = "expire"
	actionCancel     transitionAction = "cancel"
	actionInvalidate transitionAction = "invalidate"
	actionConsume    transitionAction = "consume"
)

func nextState(current domain.ApprovalState, action transitionAction) (domain.ApprovalState, bool) {
	if current.Terminated() {
		return current, false
	}
	switch current {
	case domain.ApprovalStatePending:
		switch action {
		case actionApprove:
			return domain.ApprovalStateApproved, true
		case actionReject:
			return domain.ApprovalStateRejected, true
		case actionExpire:
			return domain.ApprovalStateExpired, true
		case actionCancel:
			return domain.ApprovalStateCancelled, true
		case actionInvalidate:
			return domain.ApprovalStateInvalidated, true
		}
	case domain.ApprovalStateApproved:
		switch action {
		case actionExpire:
			return domain.ApprovalStateExpired, true
		case actionCancel:
			return domain.ApprovalStateCancelled, true
		case actionInvalidate:
			return domain.ApprovalStateInvalidated, true
		case actionConsume:
			return domain.ApprovalStateConsumed, true
		}
	}
	return current, false
}
