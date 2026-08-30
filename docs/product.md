# Kupilot Product Contract

## Product definition

Kupilot is a local, single-process Kubernetes operations Agent with a
conversational terminal interface. A user asks an operational question in
natural language, Kupilot chooses typed Kubernetes capabilities, shows the work
inline, and returns the Markdown answer that best fits the question.

Kupilot is designed for everyday cluster investigation and carefully supervised
actions. It is not a resource browser with an assistant attached. The primary
interaction remains a conversation, not a resource tree, dashboard, command
palette, YAML editor, or embedded shell.

The current product line is `v0.4`. It replaces the original diagnostic MVP's
permanent six-Tool, five-Kind, current-Namespace, four-section answer, and
90-second execution boundaries. The original security mechanisms are retained
where they protect authority or data rather than merely restricting usefulness.

## Product principles

1. **Agent-first interaction.** The user states intent and supervises visible
   capability and action steps. Browsing Kubernetes objects is not a second
   product center.
2. **Typed authority, natural answers.** Model-visible capabilities and actions
   use strict versioned schemas. The visible answer is free-form Markdown, not a
   fixed report template.
3. **Exact operational context.** Every run has one verified Context, one
   working Namespace, one namespace-access policy, and one generation. The
   footer keeps the Context and working Namespace visible; `/status` explains
   the complete authority and budget snapshot.
4. **Evidence remains runtime-owned.** Only deterministic local capability
   handling creates Evidence. Model and user text cannot invent observations,
   scope, approval, or execution state.
5. **Broader access is explicit.** The `all` namespace policy admits explicitly
   targeted cross-Namespace and bounded all-Namespace reads in the same
   Context. It never means cross-cluster access, hidden background scans, or a
   different Kubernetes identity.
6. **Sensitive sources stay closed.** Kubernetes credentials, Secret objects
   and data, ConfigMap values, environment values, raw objects, raw model
   traffic, and unbounded output are not model content.
7. **Writes are supervised transactions.** A model may propose only a
   code-defined operation. Every write requires an exact target, local
   digest-bound approval, fresh revalidation, durable pre-operation audit, one
   execution attempt, and separate verification.
8. **Budgets are visible operating controls.** Runs use an immutable compact,
   balanced, or extended profile. Limits remain finite and locally enforced,
   but the balanced default is sized for multi-resource investigations.
9. **Local ownership and deletion.** Kupilot has no hosted control plane or
   telemetry. Eligible local history follows the accepted retention and
   deletion contract.

## Intended user journey

1. The user starts `kupilot` for a new Session, or explicitly resumes safe local
   history. A new start resolves the configured, remembered, or kubeconfig
   current Context and verifies the `default` Namespace unless one was
   configured explicitly.
2. If needed, the single-screen TUI collects the model endpoint, model name,
   and masked API key and discloses plaintext local storage before saving it.
3. The user verifies a kubeconfig Context and working Namespace. The footer
   keeps both visible.
4. Before the first eligible model transfer, the user reviews the exact model
   origin and data categories and grants or rejects consent.
5. The user asks a question such as "which Nodes are under pressure?", "compare
   failing workloads across Namespaces", or "why is checkout unavailable?".
6. Kupilot creates one AgentRun with a frozen Context, working Namespace,
   namespace-access policy, capability catalog, consent, and budget profile.
7. Inline steps show bounded reads and any proposed action. While work is
   active, a live row shows compact elapsed time and the `Esc` interrupt hint.
   The user may cancel the run at any time and may inspect `/status` without
   causing external I/O.
8. Kupilot returns a validated free-form Markdown answer. Evidence detail and
   gaps remain inspectable without forcing every response into a fixed layout.
   The final answer ends with a full-width `Worked for` duration separator.
   Completed conversation blocks remain in the primary terminal scrollback
   after the managed composer exits.
9. If the Agent proposes an admitted mutation, Kupilot displays a default-reject
   approval bound to the exact target and operation. Request acceptance and
   post-operation verification remain distinct.

## Operational read contract

The versioned read catalog uses typed client-go operations and project-owned
projections. The first `v0.4` source allowlist is:

- Namespace, Node, Pod, Service, PersistentVolumeClaim, PersistentVolume, and
  ConfigMap metadata from core `v1`;
- Deployment, ReplicaSet, StatefulSet, and DaemonSet from `apps/v1`;
- Job and CronJob from `batch/v1`;
- Ingress from `networking.k8s.io/v1`;
- HorizontalPodAutoscaler from `autoscaling/v2`; and
- PodDisruptionBudget from `policy/v1`.

Capabilities cover exact resource reads, bounded lists, recent Events, current
and previous Pod log tails, code-defined relationships, and a bounded cluster
overview for Namespace and Node health. Namespaced calls default to the working
Namespace. Under the frozen `all` policy, the model may supply an explicit
Namespace or request a bounded all-Namespace list. The runtime validates and
canonicalizes that choice before Kubernetes I/O.

No capability accepts a kubeconfig, credential, endpoint, Context, arbitrary
GVR, raw selector, raw HTTP request, shell command, YAML document, pagination
token, or unlimited result size. Kubernetes RBAC is still enforced by the API
server and every denial remains visible.

## Answer and Evidence contract

The visible result is bounded Markdown. Kupilot does not prepend scope text or
append `Confirmed facts`, `Hypotheses`, `Missing information`, and
`Recommended actions` sections to every answer.

The internal final-response envelope separately carries:

- the candidate Markdown answer;
- claim-to-Evidence references; and
- typed proposed actions.

Runtime validates current-run Evidence IDs and action schemas before accepting
the result. Invalid citations are removed and produce a visible warning. A
permission denial, truncation, sensitive-output block, stale result, budget
stop, or unsupported source is stated honestly rather than hidden behind a
confident answer.

Evidence is a time-bounded projection, not a complete cluster truth. A
successful AgentRun means that Kupilot followed its local authority, data, and
protocol checks. It does not guarantee that a model identified the root cause.

Terminal scrollback is owned by the user's terminal emulator, not by Kupilot's
Session store. Starting a new Session, clearing history, deleting a Session, or
using minimal persistence does not erase text that the terminal has already
displayed.

## Supervised action contract

`restart_deployment` is the first admitted `v0.4` action. It changes only the
Kupilot-owned Pod-template restart annotation for one exact `apps/v1`
Deployment. It accepts no patch, YAML, annotation key, timestamp, resource
version, or arbitrary parameter from the model.

An action proposal is not approval. Approval is local, defaults to rejection,
expires after 60 seconds, is single-use, and binds the operation, policy,
Context, Namespace, generation, target identity, target fingerprint, reason,
and expiry. Kupilot re-reads the target, persists the consumed approval and
pre-operation audit, performs one write attempt, and reports API acceptance and
rollout verification separately.

Additional actions may be added only as independently reviewed typed
transactions. Kupilot does not expose generic apply, patch, delete, exec, or
command execution as an extension mechanism.

## Product identity and non-goals

Kupilot is intentionally not:

- k9s, a Kubernetes Dashboard, or a resource inventory application;
- a kubectl wrapper, shell, terminal multiplexer, IDE, or YAML editor;
- a controller, operator, daemon, scheduled scanner, or autonomous remediation
  service;
- a hosted service, cluster-resident component, multi-user control plane, or
  telemetry collector;
- a plugin host, MCP client, RAG system, arbitrary network agent, or Multi-Agent
  orchestrator.

These non-goals constrain interaction and authority, not the usefulness of
typed Kubernetes investigation. A new built-in resource or operation is
admitted through explicit product, permission, privacy, budget, and test review
rather than through a permanent low feature ceiling.

## References

- [Version Scope](scope.md)
- [Architecture](architecture.md)
- [Security Threat Model](security.md)
- [Privacy Overview](privacy-overview.md)
- [Data Retention Contract](data-retention.md)
- [ADR-0037: Adopt an Operational Capability Catalog](adr/0037-adopt-an-operational-capability-catalog.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](adr/0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](adr/0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0040: Use a Codex-Style Conversational TUI](adr/0040-use-a-codex-style-conversational-tui.md)
- [ADR-0041: Export Free-Form Session Summaries](adr/0041-export-free-form-session-summaries.md)
- [ADR-0042: Remember the Last Verified Kubernetes Context](adr/0042-remember-the-last-verified-kubernetes-context.md)
