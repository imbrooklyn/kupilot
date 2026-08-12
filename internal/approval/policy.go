package approval

import (
	"context"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func validateRequestCommand(command RequestCommand) error {
	if !command.ID.Valid() || !command.RunID.Valid() || !command.SessionID.Valid() {
		return domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	if command.Intent.Validate() != nil {
		return domain.NewApprovalError(domain.ApprovalErrorCodeInvalidIntent)
	}
	return nil
}

func validateRequestIdentity(requestID domain.ApprovalID) error {
	if !requestID.Valid() {
		return domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	return nil
}

func currentApprovalTime(clock Clock) (time.Time, error) {
	value := clock.Now()
	if value.IsZero() || value.UnixMilli() < 0 {
		return time.Time{}, domain.NewApprovalError(domain.ApprovalErrorCodeInternal)
	}
	return value.UTC().Truncate(time.Millisecond), nil
}

func contextApprovalError(ctx context.Context) error {
	if ctx == nil {
		return domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	if ctx.Err() != nil {
		return domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	return nil
}

func validCurrentScope(scope domain.ScopeSnapshot) bool {
	return scope.Validate() == nil && domain.ValidContextName(scope.Context) &&
		domain.ValidNamespaceName(scope.Namespace) && scope.Generation >= 1
}

func sameScope(left, right domain.ScopeSnapshot) bool {
	return left == right
}

func expiredAt(request domain.ApprovalRequest, now time.Time) bool {
	return !now.Before(request.ExpiresAt)
}
