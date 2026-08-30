package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"github.com/imbrooklyn/kupilot/internal/domain"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ErrorClass is the project-owned stable failure class used by this adapter.
type ErrorClass = domain.SafeErrorClass

const (
	ClassInvalidInput            ErrorClass = domain.SafeErrorClassInvalidInput
	ClassConfigurationInvalid    ErrorClass = domain.SafeErrorClassConfigurationInvalid
	ClassAuthenticationFailed    ErrorClass = domain.SafeErrorClassAuthenticationFailed
	ClassPermissionDenied        ErrorClass = domain.SafeErrorClassPermissionDenied
	ClassNotFound                ErrorClass = domain.SafeErrorClassNotFound
	ClassUnsupported             ErrorClass = domain.SafeErrorClassUnsupported
	ClassPolicyDenied            ErrorClass = domain.SafeErrorClassPolicyDenied
	ClassStaleScope              ErrorClass = domain.SafeErrorClassStaleScope
	ClassBudgetExhausted         ErrorClass = domain.SafeErrorClassBudgetExhausted
	ClassRateLimited             ErrorClass = domain.SafeErrorClassRateLimited
	ClassUnavailable             ErrorClass = domain.SafeErrorClassUnavailable
	ClassTimeout                 ErrorClass = domain.SafeErrorClassTimeout
	ClassCancelled               ErrorClass = domain.SafeErrorClassCancelled
	ClassSensitiveOutputBlocked  ErrorClass = domain.SafeErrorClassSensitiveOutputBlocked
	ClassInvalidExternalResponse ErrorClass = domain.SafeErrorClassInvalidExternalResponse
	ClassInternal                ErrorClass = domain.SafeErrorClassInternal
)

// SafeError contains only code-defined fields suitable for delivery and logs.
// Kubeconfig paths, credentials, transport details, and vendor errors are
// intentionally absent.
type SafeError struct {
	class     ErrorClass
	code      string
	operation string
	message   string
	retryable bool
}

func newKubeSafeError(class ErrorClass, code, operation, message string) *SafeError {
	return &SafeError{
		class:     class,
		code:      code,
		operation: operation,
		message:   message,
	}
}

func (err *SafeError) Error() string {
	if err == nil {
		return "Kupilot Kubernetes access failed."
	}
	return err.message + " (" + err.code + ")"
}

// Is preserves cancellation and deadline semantics without exposing a raw
// adapter cause.
func (err *SafeError) Is(target error) bool {
	if err == nil {
		return false
	}
	return err.class == ClassCancelled && target == context.Canceled ||
		err.class == ClassTimeout && target == context.DeadlineExceeded
}

// Class returns the stable error class.
func (err *SafeError) Class() ErrorClass {
	if err == nil {
		return ClassInternal
	}
	return err.class
}

// Code returns the stable error identifier.
func (err *SafeError) Code() string {
	if err == nil {
		return "kubernetes_internal"
	}
	return err.code
}

// ErrorCode makes SafeError consumable by delivery adapters without exposing
// implementation types.
func (err *SafeError) ErrorCode() string {
	return err.Code()
}

// Operation returns the code-defined operation that failed.
func (err *SafeError) Operation() string {
	if err == nil {
		return "kubernetes"
	}
	return err.operation
}

// Retryable reports the conservative retry classification. Retry scheduling is
// always owned by a bounded caller policy.
func (err *SafeError) Retryable() bool {
	return err != nil && err.retryable
}

// SafeMessage returns bounded text approved for terminal output.
func (err *SafeError) SafeMessage() string {
	return err.Error()
}

var errRedirectDenied = errors.New("Kubernetes redirects are denied")

func classifyKubernetesError(operation string, raw error) *SafeError {
	if raw == nil {
		return nil
	}
	var safe *SafeError
	if errors.As(raw, &safe) {
		return safe
	}
	if errors.Is(raw, context.Canceled) {
		return newKubeSafeError(ClassCancelled, "kubernetes_request_cancelled", operation, "The Kubernetes request was cancelled.")
	}
	if errors.Is(raw, context.DeadlineExceeded) {
		return newKubeSafeError(ClassTimeout, "kubernetes_request_timeout", operation, "The Kubernetes request reached its time limit.")
	}
	if errors.Is(raw, errRedirectDenied) {
		return newKubeSafeError(ClassPolicyDenied, "kubernetes_redirect_denied", operation, "The Kubernetes server attempted a redirect that Kupilot does not allow.")
	}
	if apierrors.IsUnauthorized(raw) {
		return newKubeSafeError(ClassAuthenticationFailed, "kubernetes_authentication_failed", operation, "Kubernetes authentication failed.")
	}
	if apierrors.IsForbidden(raw) {
		return newKubeSafeError(ClassPermissionDenied, "kubernetes_permission_denied", operation, "Kubernetes denied the requested read.")
	}
	if apierrors.IsNotFound(raw) {
		return newKubeSafeError(ClassNotFound, "kubernetes_not_found", operation, "The requested Kubernetes object was not found.")
	}
	if apierrors.IsTooManyRequests(raw) {
		return newKubeSafeError(ClassRateLimited, "kubernetes_rate_limited", operation, "Kubernetes temporarily limited the request rate.")
	}
	if apierrors.IsTimeout(raw) || apierrors.IsServerTimeout(raw) {
		return newKubeSafeError(ClassTimeout, "kubernetes_request_timeout", operation, "The Kubernetes request reached its time limit.")
	}
	if apierrors.IsServiceUnavailable(raw) {
		return newKubeSafeError(ClassUnavailable, "kubernetes_unavailable", operation, "Kubernetes is temporarily unavailable.")
	}
	if isTLSVerificationError(raw) {
		return newKubeSafeError(ClassAuthenticationFailed, "kubernetes_tls_verification_failed", operation, "Kubernetes server identity verification failed.")
	}
	var networkError net.Error
	if errors.As(raw, &networkError) {
		if networkError.Timeout() {
			return newKubeSafeError(ClassTimeout, "kubernetes_request_timeout", operation, "The Kubernetes request reached its time limit.")
		}
		return newKubeSafeError(ClassUnavailable, "kubernetes_unavailable", operation, "Kubernetes is temporarily unavailable.")
	}
	return newKubeSafeError(ClassInternal, "kubernetes_request_failed", operation, "The Kubernetes request failed safely.")
}

func isTLSVerificationError(raw error) bool {
	var verificationError *tls.CertificateVerificationError
	if errors.As(raw, &verificationError) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(raw, &unknownAuthority) {
		return true
	}
	var hostnameError x509.HostnameError
	if errors.As(raw, &hostnameError) {
		return true
	}
	var certificateError x509.CertificateInvalidError
	return errors.As(raw, &certificateError)
}
