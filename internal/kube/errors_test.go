package kube

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClassifyKubernetesErrorUsesStableSafeClasses(t *testing.T) {
	t.Parallel()

	resource := schema.GroupResource{Group: "", Resource: "pods"}
	tests := []struct {
		name  string
		err   error
		class ErrorClass
		code  string
	}{
		{name: "cancelled", err: context.Canceled, class: ClassCancelled, code: "kubernetes_request_cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, class: ClassTimeout, code: "kubernetes_request_timeout"},
		{name: "unauthorized", err: apierrors.NewUnauthorized("synthetic detail"), class: ClassAuthenticationFailed, code: "kubernetes_authentication_failed"},
		{name: "forbidden", err: apierrors.NewForbidden(resource, "synthetic", errors.New("synthetic detail")), class: ClassPermissionDenied, code: "kubernetes_permission_denied"},
		{name: "not found", err: apierrors.NewNotFound(resource, "synthetic"), class: ClassNotFound, code: "kubernetes_not_found"},
		{name: "rate limited", err: apierrors.NewTooManyRequests("synthetic detail", 1), class: ClassRateLimited, code: "kubernetes_rate_limited"},
		{name: "server timeout", err: apierrors.NewServerTimeout(resource, "get", 1), class: ClassTimeout, code: "kubernetes_request_timeout"},
		{name: "unavailable", err: apierrors.NewServiceUnavailable("synthetic detail"), class: ClassUnavailable, code: "kubernetes_unavailable"},
		{name: "TLS identity", err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}}, class: ClassAuthenticationFailed, code: "kubernetes_tls_verification_failed"},
		{name: "network", err: &net.DNSError{Err: "synthetic detail", Name: "generated.invalid"}, class: ClassUnavailable, code: "kubernetes_unavailable"},
		{name: "redirect", err: errRedirectDenied, class: ClassPolicyDenied, code: "kubernetes_redirect_denied"},
		{name: "fallback", err: errors.New("synthetic detail"), class: ClassInternal, code: "kubernetes_request_failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := classifyKubernetesError("read_resource", test.err)
			assertKubeSafeError(t, err, test.class, test.code)
		})
	}
}

func TestClassifyKubernetesErrorNeverEchoesWrappedCanary(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("s", 47) + "-generated"
	raw := &url.Error{
		Op:  "Get",
		URL: "https://" + canary + ".invalid/path?token=" + canary,
		Err: fmt.Errorf("TLS response %s: %w", canary, x509.UnknownAuthorityError{Cert: &x509.Certificate{}}),
	}
	err := classifyKubernetesError("read_resource", raw)
	assertKubeSafeError(t, err, ClassAuthenticationFailed, "kubernetes_tls_verification_failed")
	if strings.Contains(err.Error(), canary) || strings.Contains(err.SafeMessage(), canary) {
		t.Fatal("safe Kubernetes error contains a transport canary")
	}
}

func TestClassifyKubernetesErrorPreservesExistingSafeError(t *testing.T) {
	t.Parallel()

	want := newKubeSafeError(
		ClassInvalidExternalResponse,
		"exec_credential_invalid",
		"exec_credential",
		"Kubernetes credential output was invalid.",
	)
	got := classifyKubernetesError("read_resource", fmt.Errorf("transport: %w", want))
	if got != want {
		t.Fatalf("classifyKubernetesError() = %p, want existing safe error %p", got, want)
	}
}

func assertKubeSafeError(t *testing.T, err error, class ErrorClass, code string) *SafeError {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q and code %q", class, code)
	}
	var safe *SafeError
	if !errors.As(err, &safe) {
		t.Fatalf("error type = %T, want *SafeError", err)
	}
	if safe.Class() != class {
		t.Errorf("error class = %q, want %q", safe.Class(), class)
	}
	if safe.Code() != code {
		t.Errorf("error code = %q, want %q", safe.Code(), code)
	}
	if safe.Retryable() {
		t.Errorf("Retryable() = true for %q", code)
	}
	return safe
}
