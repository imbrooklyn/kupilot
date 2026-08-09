package kube

import (
	"context"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	defaultNamespace   = "default"
	maxContextNameSize = 253
	maxNamespaceSize   = 63
)

// ContextInfo is the complete safe kubeconfig projection available outside the
// Kubernetes adapter. It contains no source path, server, user, or credential.
type ContextInfo struct {
	Name            string
	Namespace       string
	Current         bool
	ExecCredentials bool
}

// KubeconfigWarning is a fixed, content-free warning about a selected source.
type KubeconfigWarning string

const (
	// KubeconfigWarningUnsafePermissions reports that at least one selected
	// source is group- or world-accessible on a platform with Unix mode bits.
	KubeconfigWarningUnsafePermissions KubeconfigWarning = "unsafe_source_permissions"
)

// ConfigLoader applies client-go's standard KUBECONFIG precedence and merge
// behavior. Raw kubeconfig values remain private to this package.
type ConfigLoader struct {
	rules clientcmd.ClientConfigLoadingRules
}

// NewConfigLoader creates an independent loader using KUBECONFIG when present
// and the standard per-user kubeconfig location otherwise. Legacy migration is
// disabled because KuPilot never writes a user's kubeconfig.
func NewConfigLoader() *ConfigLoader {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.MigrationRules = nil
	rules.Warner = func(error) {}
	return &ConfigLoader{rules: *rules}
}

// Contexts returns a deterministic list of safe Context projections.
func (loader *ConfigLoader) Contexts(ctx context.Context) ([]ContextInfo, error) {
	raw, err := loader.load(ctx)
	if err != nil {
		return nil, err
	}
	contexts, err := projectContexts(raw)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, cancelledKubeconfigError()
	}
	return contexts, nil
}

// CurrentContext returns the safe projection selected by current-context.
func (loader *ConfigLoader) CurrentContext(ctx context.Context) (ContextInfo, error) {
	raw, err := loader.load(ctx)
	if err != nil {
		return ContextInfo{}, err
	}
	contexts, err := projectContexts(raw)
	if err != nil {
		return ContextInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ContextInfo{}, cancelledKubeconfigError()
	}
	if raw.CurrentContext == "" {
		return ContextInfo{}, newKubeSafeError(
			ClassNotFound,
			"kubeconfig_current_context_missing",
			"load_current_context",
			"The merged kubeconfig does not select a current Context.",
		)
	}
	for _, current := range contexts {
		if current.Current {
			return current, nil
		}
	}
	return ContextInfo{}, newKubeSafeError(
		ClassInvalidExternalResponse,
		"kubeconfig_current_context_invalid",
		"load_current_context",
		"The merged kubeconfig current Context is invalid.",
	)
}

// Warnings returns fixed source warnings without exposing source paths or
// changing user-owned file permissions.
func (loader *ConfigLoader) Warnings(ctx context.Context) ([]KubeconfigWarning, error) {
	if _, err := loader.load(ctx); err != nil {
		return nil, err
	}
	for _, path := range loader.rules.GetLoadingPrecedence() {
		if err := ctx.Err(); err != nil {
			return nil, cancelledKubeconfigError()
		}
		info, err := os.Stat(path)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, cancelledKubeconfigError()
		}
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			return []KubeconfigWarning{KubeconfigWarningUnsafePermissions}, nil
		}
	}
	return nil, nil
}

func (loader *ConfigLoader) load(ctx context.Context) (*clientcmdapi.Config, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, cancelledKubeconfigError()
	}
	if loader == nil {
		return nil, newKubeSafeError(
			ClassInternal,
			"kubeconfig_loader_unavailable",
			"load_kubeconfig",
			"The Kubernetes configuration loader is unavailable.",
		)
	}
	rules := loader.rules
	rules.Precedence = append([]string(nil), loader.rules.Precedence...)
	rules.MigrationRules = nil
	rules.Warner = func(error) {}
	raw, err := rules.Load()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, cancelledKubeconfigError()
	}
	if err != nil {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubeconfig_invalid",
			"load_kubeconfig",
			"The selected kubeconfig sources could not be loaded safely.",
		)
	}
	if raw == nil || len(raw.Contexts) == 0 {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubeconfig_missing",
			"load_kubeconfig",
			"No usable kubeconfig Context was found.",
		)
	}
	return raw, nil
}

func projectContexts(raw *clientcmdapi.Config) ([]ContextInfo, error) {
	if raw == nil {
		return nil, newKubeSafeError(
			ClassInternal,
			"kubeconfig_loader_unavailable",
			"project_kubeconfig_contexts",
			"The Kubernetes configuration loader is unavailable.",
		)
	}
	if raw.CurrentContext != "" && !validContextName(raw.CurrentContext) {
		return nil, invalidContextProjectionError()
	}
	result := make([]ContextInfo, 0, len(raw.Contexts))
	for name, selected := range raw.Contexts {
		if !validContextName(name) || selected == nil {
			return nil, invalidContextProjectionError()
		}
		namespace := selected.Namespace
		if namespace == "" {
			namespace = defaultNamespace
		}
		if !validNamespace(namespace) {
			return nil, invalidContextProjectionError()
		}
		if selected.Cluster == "" || raw.Clusters[selected.Cluster] == nil {
			return nil, invalidContextProjectionError()
		}
		execCredentials := false
		if selected.AuthInfo != "" {
			authInfo := raw.AuthInfos[selected.AuthInfo]
			if authInfo == nil {
				return nil, invalidContextProjectionError()
			}
			execCredentials = authInfo.Exec != nil
		}
		result = append(result, ContextInfo{
			Name:            name,
			Namespace:       namespace,
			Current:         raw.CurrentContext == name,
			ExecCredentials: execCredentials,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result, nil
}

func resolveContext(raw *clientcmdapi.Config, name string) (ContextInfo, error) {
	if name == "" && raw != nil {
		name = raw.CurrentContext
	}
	if !validContextName(name) {
		return ContextInfo{}, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubeconfig_context_required",
			"select_kubeconfig_context",
			"A valid kubeconfig Context must be selected.",
		)
	}
	contexts, err := projectContexts(raw)
	if err != nil {
		return ContextInfo{}, err
	}
	for _, current := range contexts {
		if current.Name == name {
			return current, nil
		}
	}
	return ContextInfo{}, newKubeSafeError(
		ClassNotFound,
		"kubeconfig_context_not_found",
		"select_kubeconfig_context",
		"The selected kubeconfig Context was not found.",
	)
}

func cancelledKubeconfigError() *SafeError {
	return newKubeSafeError(
		ClassCancelled,
		"kubeconfig_load_cancelled",
		"load_kubeconfig",
		"Kubernetes configuration loading was cancelled.",
	)
}

func invalidContextProjectionError() *SafeError {
	return newKubeSafeError(
		ClassInvalidExternalResponse,
		"kubeconfig_context_invalid",
		"project_kubeconfig_contexts",
		"The merged kubeconfig contains an invalid Context reference.",
	)
}

func validContextName(value string) bool {
	if value == "" || len(value) > maxContextNameSize || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || isBidirectionalControl(current) {
			return false
		}
	}
	return true
}

func validNamespace(value string) bool {
	if value == "" || len(value) > maxNamespaceSize || !lowerAlphaNumeric(value[0]) || !lowerAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' {
			continue
		}
		return false
	}
	return true
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func isBidirectionalControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}
