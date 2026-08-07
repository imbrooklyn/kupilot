# ADR-0020: Contain Kubeconfig Exec Credentials

- Status: Accepted
- Date: 2026-08-08

## Context

Many local Kubernetes Contexts obtain short-lived credentials through the
kubeconfig exec credential mechanism. Rejecting all exec authentication would
exclude common managed-cluster workflows. Running an executable named by a
kubeconfig is nevertheless code execution with the local user's privileges and
may perform its own network requests, read environment, hang, or emit sensitive
credential material.

Exec authentication must remain a Kubernetes transport concern and must never
become a general command Tool or a model-selected capability.

## Decision

KuPilot will support the standard kubeconfig exec credential mechanism only when
it is required by the explicitly selected local Context and only through the
validated client-go path.

Typed configuration provides a strict-deny mode. When enabled, KuPilot rejects a
Context that requires exec authentication before launching the program. The
model, Agent, Tool, Session history, and TUI cannot disable or bypass this mode.
When strict deny is off, the explicitly selected kubeconfig is the source of the
exec configuration; KuPilot does not create a second approval or command system.

Runtime requirements are:

- Launch the resolved program directly, never through a shell or a command
  string.
- Take executable, arguments, protocol API version, and declared exec environment
  only from the selected kubeconfig after local validation. The model, user
  question, Session, Tool, and Kubernetes data cannot modify them.
- Remove KuPilot's model API-key environment variable and other KuPilot-only
  sensitive variables from the child environment. Do not add cluster or model
  content.
- Honor the owning Context, use a bounded deadline, terminate the child on
  cancellation or scope disposal, and allow no orphan process.
- Feed protocol output only to the credential decoder. Never render, log,
  persist, include in safe errors, or send standard output or standard error to
  the model.
- Bound standard error and translate failure into a stable safe class without
  copying vendor text.
- Never provide interactive terminal access unless the exact client-go mode and
  TUI behavior pass the S09 gate.

KuPilot cannot guarantee or audit network activity performed inside the
user-configured external program. Safe status and documentation must state that
it is part of Kubernetes authentication, not a KuPilot Tool.

## Consequences

Positive consequences:

- Common short-lived authentication workflows can work without storing
  credentials.
- Users can disable this local execution surface with strict-deny configuration.
- Model and Tool injection cannot choose or invoke a command.
- Credential protocol output remains confined to client transport setup.

Costs and constraints:

- Context activation may fail when strict deny is enabled, the program is
  unavailable, or the required interactive behavior is unsupported.
- KuPilot cannot sandbox an arbitrary external program portably under the
  accepted platform scope.
- Environment filtering and child-process ownership require platform tests.
- Allowing standard exec authentication does not make a malicious kubeconfig
  safe; the user still owns the kubeconfig trust decision.

## Alternatives considered

- Disabling exec authentication was rejected because it would exclude common
  local kubeconfig workflows.
- Requiring a separate per-launch command approval was rejected because exec is
  a user-configured authentication mechanism, while strict deny provides the
  explicit fail-closed choice without confusing it with write approval.
- Turning exec into a general Tool was rejected because it would create the
  shell capability explicitly excluded from the product.
- Persisting a broad approval for an executable was rejected because the
  underlying file, kubeconfig, arguments, or environment can change.
- Implementing a portable application sandbox was rejected as outside the MVP
  and not a substitute for user trust in kubeconfig.

## Security and privacy impact

Exec output can contain live Kubernetes credentials and is never eligible for
model, TUI, Application event, ordinary log, or persistence sinks. The model API
key must not be inherited by the child. Safe errors state the operation and
classification only.

An exec plugin is not authorized by prompt text, model output, or Tool syntax.
Allowing the selected kubeconfig mechanism does not authorize cluster writes or
widen the selected ClusterScope.

## Validation gate

S05 must implement typed strict-deny configuration and ephemeral API-key removal.
No later than S09 and before enabling exec authentication, an official client-go
API and process-control spike must verify:

1. Supported exec credential API versions, cache behavior, interactive-mode
   behavior, environment construction, and cancellation hooks.
2. Strict-deny behavior before launch and safe identification of an exec-related
   Context without exposing sensitive fields.
3. Direct launch without a shell, removal of the model key environment source,
   bounded output, timeout, cancellation, and child reaping on macOS and Linux.
4. Safe handling of success, malformed protocol output, nonzero exit, missing
   executable, permission denial, timeout, and scope switch.
5. Canary absence across model transport, TUI, Application events, logs, safe
   errors, and SQLite.

The exact client-go API and supported interactive behavior must be recorded from
the spike. This ADR does not claim they are already verified.

## Revisit triggers

- client-go changes exec protocol, caching, environment, or process ownership.
- Supported platforms gain a reviewed sandbox that materially improves
  containment.
- A required provider cannot operate under bounded non-interactive behavior.
- Evidence shows that strict deny and documentation are insufficient to make the
  trust boundary understandable.

## References

- [Security Threat Model](../security.md)
- [ADR-0007: Use client-go Behind Narrow Kubernetes Ports](0007-use-client-go-behind-narrow-kubernetes-ports.md)
- [ADR-0021: Use Ephemeral Model API Key Sources](0021-use-ephemeral-model-api-key-sources.md)
