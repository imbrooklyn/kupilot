package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/config"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	clientauthentication "k8s.io/client-go/pkg/apis/clientauthentication"
	clientauthenticationinstall "k8s.io/client-go/pkg/apis/clientauthentication/install"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clienttransport "k8s.io/client-go/transport"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

const (
	// DefaultUserAgent is the fixed Kubernetes transport identity.
	DefaultUserAgent = "kupilot/0.5"

	// DefaultClientQPS and DefaultClientBurst are the non-expandable client
	// rate defaults.
	DefaultClientQPS   float32 = 5
	DefaultClientBurst         = 10

	// DefaultRequestTimeout is the hard ceiling for one Kubernetes request,
	// including an exec credential refresh required by that request.
	DefaultRequestTimeout = 10 * time.Second
	// MaxRequestTimeout is the largest profile-selected Kubernetes request
	// timeout. A caller may tighten it but cannot expand it beyond this bound.
	MaxRequestTimeout = 60 * time.Second

	maxExecOutputBytes = 64 * 1024
	maxExecErrorBytes  = 8 * 1024
	maxExecInputBytes  = 64 * 1024
	maxExecArguments   = 128
	maxExecEnvironment = 128
	execWaitDelay      = time.Second
	execInfoEnv        = "KUBERNETES_EXEC_INFO"
)

// ExecCredentialPolicy is fixed when a factory is constructed and cannot be
// changed by a Context, Agent, Tool, or request.
type ExecCredentialPolicy string

const (
	ExecCredentialsAllow ExecCredentialPolicy = "allow"
	ExecCredentialsDeny  ExecCredentialPolicy = "deny"
)

type commandContextFunc func(context.Context, string, ...string) *exec.Cmd
type typedClientFunc func(*rest.Config, *http.Client) (kubernetes.Interface, error)
type dynamicClientFunc func(*rest.Config, *http.Client) (dynamic.Interface, error)
type metadataClientFunc func(*rest.Config, *http.Client) (metadata.Interface, error)
type metricsClientFunc func(*rest.Config, *http.Client) (metricsv1beta1.MetricsV1beta1Interface, error)

// ClientFactory creates one independently owned, cancellable typed client
// bundle. It exposes no rest.Config or client-go client through its public API.
type ClientFactory struct {
	loader            *ConfigLoader
	execPolicy        ExecCredentialPolicy
	requestTimeout    time.Duration
	environment       func() []string
	commandContext    commandContextFunc
	newTypedClient    typedClientFunc
	newDynamicClient  dynamicClientFunc
	newMetadataClient metadataClientFunc
	newMetricsClient  metricsClientFunc
	now               func() time.Time
}

// NewClientFactory validates immutable Kubernetes client construction policy.
func NewClientFactory(loader *ConfigLoader, policy ExecCredentialPolicy, requestTimeout ...time.Duration) (*ClientFactory, error) {
	if loader == nil {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_loader_required",
			"create_kubernetes_client_factory",
			"A Kubernetes configuration loader is required.",
		)
	}
	if policy != ExecCredentialsAllow && policy != ExecCredentialsDeny {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_exec_policy_invalid",
			"create_kubernetes_client_factory",
			"Kubernetes exec credential policy must be allow or deny.",
		)
	}
	timeout := DefaultRequestTimeout
	if len(requestTimeout) > 1 || len(requestTimeout) == 1 &&
		(requestTimeout[0] <= 0 || requestTimeout[0] > MaxRequestTimeout) {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_request_timeout_invalid",
			"create_kubernetes_client_factory",
			"The Kubernetes request timeout is invalid.",
		)
	}
	if len(requestTimeout) == 1 {
		timeout = requestTimeout[0]
	}
	return &ClientFactory{
		loader:         loader,
		execPolicy:     policy,
		requestTimeout: timeout,
		environment:    os.Environ,
		commandContext: exec.CommandContext,
		newTypedClient: func(config *rest.Config, client *http.Client) (kubernetes.Interface, error) {
			return kubernetes.NewForConfigAndClient(config, client)
		},
		newDynamicClient: func(config *rest.Config, client *http.Client) (dynamic.Interface, error) {
			return dynamic.NewForConfigAndClient(config, client)
		},
		newMetadataClient: func(config *rest.Config, client *http.Client) (metadata.Interface, error) {
			return metadata.NewForConfigAndClient(config, client)
		},
		newMetricsClient: func(config *rest.Config, client *http.Client) (metricsv1beta1.MetricsV1beta1Interface, error) {
			return metricsv1beta1.NewForConfigAndClient(config, client)
		},
		now: time.Now,
	}, nil
}

// ClientBundle is an opaque, independently owned typed Kubernetes client
// bundle. Application lifecycle code may retain and close it, while Kubernetes
// operations remain private to this adapter.
type ClientBundle struct {
	info           ContextInfo
	typed          kubernetes.Interface
	dynamic        dynamic.Interface
	metadata       metadata.Interface
	metrics        metricsv1beta1.MetricsV1beta1Interface
	requestTimeout time.Duration
	transport      http.RoundTripper
	lifecycle      *lifecycleRoundTripper
	exec           *execCredentialManager
	closeOnce      sync.Once
	stopMu         sync.Mutex
	stopOwner      func() bool
}

// Context returns the safe Context projection bound to this bundle.
func (bundle *ClientBundle) Context() ContextInfo {
	if bundle == nil {
		return ContextInfo{}
	}
	return bundle.info
}

// Close cancels in-flight work, prevents new requests, releases cached exec
// credentials, and closes idle connections. It is safe to call repeatedly.
func (bundle *ClientBundle) Close() {
	if bundle == nil {
		return
	}
	bundle.closeResources()
	bundle.stopMu.Lock()
	stop := bundle.stopOwner
	bundle.stopOwner = nil
	bundle.stopMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (bundle *ClientBundle) closeResources() {
	bundle.closeOnce.Do(func() {
		if bundle.lifecycle != nil {
			bundle.lifecycle.closeAndWait()
		}
		if bundle.exec != nil {
			bundle.exec.close()
		}
		closeIdleConnections(bundle.transport)
	})
}

// Create resolves one exact Context and builds a fresh typed client bundle.
// It performs no Kubernetes request.
func (factory *ClientFactory) Create(ctx context.Context, contextName string) (*ClientBundle, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, cancelledClientError()
	}
	if factory == nil || factory.loader == nil || factory.newTypedClient == nil || factory.newDynamicClient == nil || factory.newMetadataClient == nil || factory.newMetricsClient == nil || factory.environment == nil || factory.commandContext == nil || factory.now == nil {
		return nil, newKubeSafeError(
			ClassInternal,
			"kubernetes_client_factory_unavailable",
			"create_kubernetes_client",
			"The Kubernetes client factory is unavailable.",
		)
	}
	raw, err := factory.loader.load(ctx)
	if err != nil {
		return nil, err
	}
	info, err := resolveContext(raw, contextName)
	if err != nil {
		return nil, err
	}

	clientConfig, rawErr := clientcmd.NewNonInteractiveClientConfig(
		*raw,
		info.Name,
		&clientcmd.ConfigOverrides{},
		nil,
	).ClientConfig()
	if ctx.Err() != nil {
		return nil, cancelledClientError()
	}
	if rawErr != nil {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubeconfig_context_invalid",
			"create_kubernetes_client",
			"The selected kubeconfig Context could not create a Kubernetes client.",
		)
	}
	if err := validateTransportPolicy(clientConfig); err != nil {
		return nil, err
	}
	clientConfig.UserAgent = DefaultUserAgent
	clientConfig.QPS = DefaultClientQPS
	clientConfig.Burst = DefaultClientBurst
	clientConfig.Timeout = factory.requestTimeout
	clientConfig.RateLimiter = nil

	manager, err := factory.prepareExecManager(ctx, clientConfig)
	if err != nil {
		return nil, err
	}
	clientConfig.ExecProvider = nil

	closed := make(chan struct{})
	roundTripper, rawErr := buildHTTPTransport(clientConfig, manager)
	if ctx.Err() != nil {
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(roundTripper)
		return nil, cancelledClientError()
	}
	if rawErr != nil {
		if manager != nil {
			manager.close()
		}
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_transport_invalid",
			"create_kubernetes_transport",
			"The selected Kubernetes transport configuration is invalid.",
		)
	}
	lifecycle := &lifecycleRoundTripper{base: roundTripper, closed: closed}
	httpClient := &http.Client{
		Transport: lifecycle,
		Timeout:   factory.requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectDenied
		},
	}
	typed, rawErr := factory.newTypedClient(clientConfig, httpClient)
	if ctx.Err() != nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, cancelledClientError()
	}
	if rawErr != nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_client_invalid",
			"create_kubernetes_client",
			"The selected Kubernetes client configuration is invalid.",
		)
	}
	dynamicClient, rawErr := factory.newDynamicClient(clientConfig, httpClient)
	if ctx.Err() != nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, cancelledClientError()
	}
	if rawErr != nil || dynamicClient == nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_dynamic_client_invalid",
			"create_kubernetes_client",
			"The selected Kubernetes client configuration could not create the bounded resource reader.",
		)
	}
	metadataClient, rawErr := factory.newMetadataClient(clientConfig, httpClient)
	if ctx.Err() != nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, cancelledClientError()
	}
	if rawErr != nil || metadataClient == nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_metadata_client_invalid",
			"create_kubernetes_client",
			"The selected Kubernetes client configuration could not create the metadata-only resource reader.",
		)
	}
	metricsClient, rawErr := factory.newMetricsClient(clientConfig, httpClient)
	if ctx.Err() != nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, cancelledClientError()
	}
	if rawErr != nil || metricsClient == nil {
		lifecycle.closeAndWait()
		if manager != nil {
			manager.close()
		}
		closeIdleConnections(lifecycle)
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_metrics_client_invalid",
			"create_kubernetes_client",
			"The selected Kubernetes client configuration could not create the bounded metrics reader.",
		)
	}
	bundle := &ClientBundle{
		info:           info,
		typed:          typed,
		dynamic:        dynamicClient,
		metadata:       metadataClient,
		metrics:        metricsClient,
		requestTimeout: factory.requestTimeout,
		transport:      lifecycle,
		lifecycle:      lifecycle,
		exec:           manager,
	}
	stopOwner := context.AfterFunc(ctx, bundle.closeResources)
	bundle.stopMu.Lock()
	bundle.stopOwner = stopOwner
	bundle.stopMu.Unlock()
	if ctx.Err() != nil {
		bundle.Close()
		return nil, cancelledClientError()
	}
	return bundle, nil
}

func (factory *ClientFactory) prepareExecManager(ctx context.Context, clientConfig *rest.Config) (*execCredentialManager, error) {
	if ctx.Err() != nil {
		return nil, cancelledClientError()
	}
	if clientConfig.ExecProvider == nil || hasStaticCredentials(clientConfig) {
		return nil, nil
	}
	if factory.execPolicy == ExecCredentialsDeny {
		return nil, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_exec_credentials_denied",
			"create_kubernetes_client",
			"The selected Context requires exec credentials, which are disabled by policy.",
		)
	}
	execConfig := copyExecConfig(clientConfig.ExecProvider)
	if err := validateExecConfig(execConfig); err != nil {
		return nil, err
	}
	var cluster *clientauthentication.Cluster
	if execConfig.ProvideClusterInfo {
		var rawErr error
		cluster, rawErr = rest.ConfigToExecCluster(clientConfig)
		if ctx.Err() != nil {
			return nil, cancelledClientError()
		}
		if rawErr != nil {
			return nil, newKubeSafeError(
				ClassConfigurationInvalid,
				"kubernetes_exec_cluster_invalid",
				"create_exec_credential_transport",
				"The selected Context contains invalid exec credential cluster information.",
			)
		}
	}
	scheme := runtime.NewScheme()
	clientauthenticationinstall.Install(scheme)
	group, _ := schema.ParseGroupVersion(execConfig.APIVersion)
	return &execCredentialManager{
		config:         execConfig,
		cluster:        cluster,
		group:          group,
		codecs:         serializer.NewCodecFactory(scheme),
		timeout:        factory.requestTimeout,
		environment:    factory.environment,
		commandContext: factory.commandContext,
		now:            factory.now,
	}, nil
}

func validateTransportPolicy(config *rest.Config) error {
	if config == nil {
		return newKubeSafeError(ClassInternal, "kubernetes_client_factory_unavailable", "validate_kubernetes_transport", "The Kubernetes client factory is unavailable.")
	}
	if config.Insecure {
		return newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_insecure_tls_denied",
			"validate_kubernetes_transport",
			"Kubernetes TLS certificate verification cannot be disabled.",
		)
	}
	parsed, err := url.Parse(config.Host)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_transport_policy_denied",
			"validate_kubernetes_transport",
			"The Kubernetes server URL does not satisfy the secure transport policy.",
		)
	}
	if config.AuthProvider != nil {
		return newKubeSafeError(
			ClassUnsupported,
			"kubernetes_auth_provider_unsupported",
			"validate_kubernetes_transport",
			"The selected Context uses an unsupported legacy authentication provider.",
		)
	}
	if config.Transport != nil || config.WrapTransport != nil {
		return newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_custom_transport_denied",
			"validate_kubernetes_transport",
			"Custom Kubernetes transports are not allowed.",
		)
	}
	return nil
}

func hasStaticCredentials(config *rest.Config) bool {
	return config.BearerToken != "" || config.BearerTokenFile != "" ||
		config.Username != "" || config.Password != "" ||
		config.CertFile != "" || config.KeyFile != "" ||
		len(config.CertData) != 0 || len(config.KeyData) != 0
}

func copyExecConfig(source *clientcmdapi.ExecConfig) clientcmdapi.ExecConfig {
	copy := *source
	copy.Args = append([]string(nil), source.Args...)
	copy.Env = append([]clientcmdapi.ExecEnvVar(nil), source.Env...)
	if source.Config != nil {
		copy.Config = source.Config.DeepCopyObject()
	}
	return copy
}

func validateExecConfig(config clientcmdapi.ExecConfig) error {
	if config.APIVersion != "client.authentication.k8s.io/v1" && config.APIVersion != "client.authentication.k8s.io/v1beta1" {
		return newKubeSafeError(
			ClassUnsupported,
			"kubernetes_exec_version_unsupported",
			"create_exec_credential_transport",
			"The selected Context uses an unsupported exec credential API version.",
		)
	}
	if config.InteractiveMode == clientcmdapi.AlwaysExecInteractiveMode {
		return newKubeSafeError(
			ClassUnsupported,
			"kubernetes_exec_interactive_unsupported",
			"create_exec_credential_transport",
			"The selected Context requires interactive exec credentials, which Kupilot does not support.",
		)
	}
	if config.InteractiveMode != clientcmdapi.NeverExecInteractiveMode && config.InteractiveMode != clientcmdapi.IfAvailableExecInteractiveMode {
		return newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_exec_configuration_invalid",
			"create_exec_credential_transport",
			"The selected Context contains invalid exec credential configuration.",
		)
	}
	if config.Command == "" || len(config.Command) > maxExecInputBytes || len(config.Args) > maxExecArguments || len(config.Env) > maxExecEnvironment {
		return newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_exec_configuration_invalid",
			"create_exec_credential_transport",
			"The selected Context contains invalid exec credential configuration.",
		)
	}
	total := len(config.Command) + len(config.APIVersion)
	for _, argument := range config.Args {
		total += len(argument)
	}
	for _, variable := range config.Env {
		if variable.Name == "" || strings.ContainsRune(variable.Name, '=') {
			return newKubeSafeError(ClassConfigurationInvalid, "kubernetes_exec_configuration_invalid", "create_exec_credential_transport", "The selected Context contains invalid exec credential configuration.")
		}
		total += len(variable.Name) + len(variable.Value)
	}
	if total > maxExecInputBytes {
		return newKubeSafeError(ClassConfigurationInvalid, "kubernetes_exec_configuration_invalid", "create_exec_credential_transport", "The selected Context contains invalid exec credential configuration.")
	}
	return nil
}

func buildHTTPTransport(config *rest.Config, manager *execCredentialManager) (http.RoundTripper, error) {
	transportConfig, err := config.TransportConfig()
	if err != nil {
		return nil, err
	}
	transportConfig.WrapTransport = nil
	transportConfig.Transport = nil
	if manager != nil {
		transportConfig.TLS.GetCertHolder = &clienttransport.GetCertHolder{GetCert: manager.cachedCertificate}
	}
	tlsConfig, err := clienttransport.TLSConfigFor(transportConfig)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsConfig
	base.DisableCompression = transportConfig.DisableCompression
	if transportConfig.Proxy != nil {
		base.Proxy = transportConfig.Proxy
	}
	if transportConfig.DialHolder != nil {
		base.DialContext = transportConfig.DialHolder.Dial
	}

	var roundTripper http.RoundTripper = base
	if manager != nil {
		roundTripper = &execCredentialRoundTripper{manager: manager, base: roundTripper}
	}
	switch {
	case transportConfig.HasBasicAuth() && transportConfig.HasTokenAuth():
		return nil, errors.New("multiple Kubernetes authentication methods")
	case transportConfig.HasTokenAuth():
		roundTripper, err = clienttransport.NewBearerAuthWithRefreshRoundTripper(
			transportConfig.BearerToken,
			transportConfig.BearerTokenFile,
			roundTripper,
		)
		if err != nil {
			return nil, err
		}
	case transportConfig.HasBasicAuth():
		roundTripper = clienttransport.NewBasicAuthRoundTripper(transportConfig.Username, transportConfig.Password, roundTripper)
	}
	if transportConfig.UserAgent != "" {
		roundTripper = clienttransport.NewUserAgentRoundTripper(transportConfig.UserAgent, roundTripper)
	}
	if transportConfig.Impersonate.UserName != "" || transportConfig.Impersonate.UID != "" ||
		len(transportConfig.Impersonate.Groups) != 0 || len(transportConfig.Impersonate.Extra) != 0 {
		roundTripper = clienttransport.NewImpersonatingRoundTripper(transportConfig.Impersonate, roundTripper)
	}
	return roundTripper, nil
}

type lifecycleRoundTripper struct {
	base     http.RoundTripper
	closed   chan struct{}
	mu       sync.Mutex
	closing  bool
	inflight sync.WaitGroup
}

func (roundTripper *lifecycleRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	roundTripper.mu.Lock()
	if roundTripper.closing {
		roundTripper.mu.Unlock()
		return nil, context.Canceled
	}
	roundTripper.inflight.Add(1)
	roundTripper.mu.Unlock()

	requestContext, cancel := context.WithCancel(request.Context())
	bodyReady := make(chan *lifecycleResponseBody, 1)
	completed := make(chan struct{})
	go func() {
		forceClose := false
		select {
		case <-roundTripper.closed:
			forceClose = true
		case <-request.Context().Done():
			forceClose = true
		case <-completed:
		}
		cancel()
		if forceClose {
			if body := <-bodyReady; body != nil {
				_ = body.Close()
			}
		}
	}()
	response, err := roundTripper.base.RoundTrip(request.Clone(requestContext))
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		bodyReady <- nil
		close(completed)
		roundTripper.inflight.Done()
		return response, err
	}

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			close(completed)
			roundTripper.inflight.Done()
		})
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	responseBody := response.Body
	if budgets := responseBudgetsFromContext(request.Context()); len(budgets) != 0 {
		responseBody = &budgetedResponseBody{base: responseBody, budgets: budgets}
	}
	body := &lifecycleResponseBody{base: responseBody, finish: finish}
	response.Body = body
	bodyReady <- body

	// Ownership passes to the response body. Its EOF or Close path now owns the
	// matching WaitGroup completion.
	return response, nil
}

func (roundTripper *lifecycleRoundTripper) WrappedRoundTripper() http.RoundTripper {
	return roundTripper.base
}

func (roundTripper *lifecycleRoundTripper) closeAndWait() {
	if roundTripper == nil {
		return
	}
	roundTripper.mu.Lock()
	if !roundTripper.closing {
		roundTripper.closing = true
		close(roundTripper.closed)
	}
	roundTripper.mu.Unlock()
	roundTripper.inflight.Wait()
}

type lifecycleResponseBody struct {
	base      io.ReadCloser
	finish    func()
	closeOnce sync.Once
	closeErr  error
}

func (body *lifecycleResponseBody) Read(destination []byte) (int, error) {
	read, err := body.base.Read(destination)
	if err != nil {
		body.finish()
	}
	return read, err
}

func (body *lifecycleResponseBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.base.Close()
	})
	body.finish()
	return body.closeErr
}

type execCredentialRoundTripper struct {
	manager *execCredentialManager
	base    http.RoundTripper
}

func (roundTripper *execCredentialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	credential, err := roundTripper.manager.credentials(request.Context())
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(request.Context())
	if len(credential.token) != 0 {
		cloned.Header.Set("Authorization", "Bearer "+string(credential.token))
	}
	response, err := roundTripper.base.RoundTrip(cloned)
	if response != nil && response.StatusCode == http.StatusUnauthorized {
		roundTripper.manager.invalidate(credential)
	}
	return response, err
}

func (roundTripper *execCredentialRoundTripper) WrappedRoundTripper() http.RoundTripper {
	return roundTripper.base
}

type execCredential struct {
	token       []byte
	certificate *tls.Certificate
	expiresAt   time.Time
}

type execCredentialManager struct {
	config         clientcmdapi.ExecConfig
	cluster        *clientauthentication.Cluster
	group          schema.GroupVersion
	codecs         serializer.CodecFactory
	timeout        time.Duration
	environment    func() []string
	commandContext commandContextFunc
	now            func() time.Time

	mu         sync.Mutex
	cached     *execCredential
	refreshing chan struct{}
	closed     bool
}

func (manager *execCredentialManager) credentials(ctx context.Context) (*execCredential, error) {
	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return nil, newKubeSafeError(ClassCancelled, "kubernetes_request_cancelled", "exec_credential", "The Kubernetes request was cancelled.")
		}
		if manager.cached != nil && (manager.cached.expiresAt.IsZero() || manager.now().Before(manager.cached.expiresAt)) {
			credential := manager.cached
			manager.mu.Unlock()
			return credential, nil
		}
		if manager.refreshing != nil {
			refreshing := manager.refreshing
			manager.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, classifyContextError(ctx.Err(), "exec_credential")
			case <-refreshing:
				continue
			}
		}
		refreshing := make(chan struct{})
		manager.refreshing = refreshing
		manager.mu.Unlock()

		credential, err := manager.run(ctx)
		manager.mu.Lock()
		if err == nil && !manager.closed {
			manager.cached = credential
		}
		closed := manager.closed
		manager.refreshing = nil
		close(refreshing)
		manager.mu.Unlock()
		if closed {
			return nil, newKubeSafeError(ClassCancelled, "kubernetes_request_cancelled", "exec_credential", "The Kubernetes request was cancelled.")
		}
		return credential, err
	}
}

func (manager *execCredentialManager) run(ctx context.Context) (*execCredential, error) {
	childContext, cancel := context.WithTimeout(ctx, manager.timeout)
	defer cancel()
	input := &clientauthentication.ExecCredential{
		Spec: clientauthentication.ExecCredentialSpec{Interactive: false},
	}
	if manager.config.ProvideClusterInfo {
		input.Spec.Cluster = manager.cluster
	}
	encoded, err := runtime.Encode(manager.codecs.LegacyCodec(manager.group), input)
	if err != nil || len(encoded) > maxExecInputBytes {
		return nil, newKubeSafeError(ClassConfigurationInvalid, "kubernetes_exec_input_invalid", "exec_credential", "Kubernetes exec credential input could not be constructed safely.")
	}

	stdout := &boundedBuffer{limit: maxExecOutputBytes}
	stderr := &boundedBuffer{limit: maxExecErrorBytes}
	command := manager.commandContext(childContext, manager.config.Command, manager.config.Args...)
	command.Env = buildExecEnvironment(manager.environment(), manager.config.Env, string(encoded))
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = execWaitDelay
	if err := command.Run(); err != nil {
		if childContext.Err() != nil {
			return nil, classifyContextError(childContext.Err(), "exec_credential")
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return nil, newKubeSafeError(ClassUnavailable, "kubernetes_exec_unavailable", "exec_credential", "The Kubernetes exec credential program is unavailable.")
		}
		return nil, newKubeSafeError(ClassAuthenticationFailed, "kubernetes_exec_failed", "exec_credential", "The Kubernetes exec credential program failed.")
	}
	if stdout.overflow {
		return nil, newKubeSafeError(ClassInvalidExternalResponse, "kubernetes_exec_output_invalid", "exec_credential", "Kubernetes exec credential output was invalid or exceeded its limit.")
	}

	decoded := &clientauthentication.ExecCredential{}
	_, groupVersionKind, err := manager.codecs.UniversalDecoder(manager.group).Decode(stdout.Bytes(), nil, decoded)
	if err != nil || groupVersionKind == nil || groupVersionKind.Group != manager.group.Group || groupVersionKind.Version != manager.group.Version || decoded.Status == nil {
		return nil, newKubeSafeError(ClassInvalidExternalResponse, "kubernetes_exec_output_invalid", "exec_credential", "Kubernetes exec credential output was invalid or exceeded its limit.")
	}
	status := decoded.Status
	if status.Token == "" && status.ClientCertificateData == "" && status.ClientKeyData == "" {
		return nil, newKubeSafeError(ClassInvalidExternalResponse, "kubernetes_exec_output_invalid", "exec_credential", "Kubernetes exec credential output was invalid or exceeded its limit.")
	}
	if (status.ClientCertificateData == "") != (status.ClientKeyData == "") {
		return nil, newKubeSafeError(ClassInvalidExternalResponse, "kubernetes_exec_output_invalid", "exec_credential", "Kubernetes exec credential output was invalid or exceeded its limit.")
	}
	credential := &execCredential{token: []byte(status.Token)}
	if status.ExpirationTimestamp != nil {
		credential.expiresAt = status.ExpirationTimestamp.Time
	}
	if status.ClientCertificateData != "" {
		certificate, err := tls.X509KeyPair([]byte(status.ClientCertificateData), []byte(status.ClientKeyData))
		if err != nil {
			return nil, newKubeSafeError(ClassInvalidExternalResponse, "kubernetes_exec_output_invalid", "exec_credential", "Kubernetes exec credential output was invalid or exceeded its limit.")
		}
		credential.certificate = &certificate
	}
	return credential, nil
}

func (manager *execCredentialManager) cachedCertificate() (*tls.Certificate, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.cached == nil {
		return nil, nil
	}
	return manager.cached.certificate, nil
}

func (manager *execCredentialManager) invalidate(credential *execCredential) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cached == credential {
		manager.cached = nil
	}
}

func (manager *execCredentialManager) close() {
	manager.mu.Lock()
	manager.closed = true
	manager.cached = nil
	manager.mu.Unlock()
}

func buildExecEnvironment(base []string, configured []clientcmdapi.ExecEnvVar, execInfo string) []string {
	environment := config.FilterChildEnvironment(base)
	filtered := make([]string, 0, len(environment)+len(configured)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, execInfoEnv+"=") {
			filtered = append(filtered, entry)
		}
	}
	for _, entry := range configured {
		if entry.Name == config.ModelAPIKeyEnvironmentVariable || entry.Name == execInfoEnv {
			continue
		}
		filtered = append(filtered, entry.Name+"="+entry.Value)
	}
	return append(filtered, execInfoEnv+"="+execInfo)
}

func classifyContextError(err error, operation string) *SafeError {
	if errors.Is(err, context.DeadlineExceeded) {
		return newKubeSafeError(ClassTimeout, "kubernetes_request_timeout", operation, "The Kubernetes request reached its time limit.")
	}
	return newKubeSafeError(ClassCancelled, "kubernetes_request_cancelled", operation, "The Kubernetes request was cancelled.")
}

func cancelledClientError() *SafeError {
	return newKubeSafeError(ClassCancelled, "kubernetes_client_cancelled", "create_kubernetes_client", "Kubernetes client construction was cancelled.")
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = buffer.buffer.Write(value[:remaining])
	}
	if written > remaining {
		buffer.overflow = true
	}
	return written, nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func closeIdleConnections(roundTripper http.RoundTripper) {
	if roundTripper == nil {
		return
	}
	if closer, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	if wrapped, ok := roundTripper.(interface{ WrappedRoundTripper() http.RoundTripper }); ok {
		closeIdleConnections(wrapped.WrappedRoundTripper())
	}
}
