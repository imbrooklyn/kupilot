package observability

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type SafeError struct {
	class domain.SafeErrorClass
	code  string
}

func (err *SafeError) Error() string {
	if err == nil {
		return "The observability request failed safely."
	}
	return "The observability request failed safely. (" + err.code + ")"
}
func (err *SafeError) Class() domain.SafeErrorClass {
	if err == nil {
		return domain.SafeErrorClassInternal
	}
	return err.class
}
func (err *SafeError) Is(target error) bool {
	return err != nil && (err.class == domain.SafeErrorClassCancelled && target == context.Canceled || err.class == domain.SafeErrorClassTimeout && target == context.DeadlineExceeded)
}

func safeError(class domain.SafeErrorClass, code string) *SafeError {
	return &SafeError{class: class, code: code}
}

var errRedirectDenied = errors.New("observability redirect denied")

func classifyRequestError(ctx context.Context, err error) error {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return safeError(domain.SafeErrorClassCancelled, "observability_request_cancelled")
	}
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return safeError(domain.SafeErrorClassTimeout, "observability_request_timeout")
	}
	if errors.Is(err, errRedirectDenied) {
		return safeError(domain.SafeErrorClassPolicyDenied, "observability_redirect_denied")
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return safeError(domain.SafeErrorClassTimeout, "observability_request_timeout")
		}
		return safeError(domain.SafeErrorClassUnavailable, "observability_unavailable")
	}
	return safeError(domain.SafeErrorClassUnavailable, "observability_unavailable")
}

func classifyStatus(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return safeError(domain.SafeErrorClassAuthenticationFailed, "observability_authentication_failed")
	case http.StatusForbidden:
		return safeError(domain.SafeErrorClassPermissionDenied, "observability_permission_denied")
	case http.StatusNotFound, http.StatusNotImplemented:
		return safeError(domain.SafeErrorClassUnsupported, "observability_api_unsupported")
	case http.StatusTooManyRequests:
		return safeError(domain.SafeErrorClassRateLimited, "observability_rate_limited")
	default:
		if status >= 500 {
			return safeError(domain.SafeErrorClassUnavailable, "observability_unavailable")
		}
		return safeError(domain.SafeErrorClassInvalidExternalResponse, "observability_response_status_invalid")
	}
}
