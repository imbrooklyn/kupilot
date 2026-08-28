# KuPilot Scope

This document freezes the product boundary for the read-only `v0.1` MVP and
the single controlled change allowed in `v0.2`.

## `v0.1`: read-only Agent MVP

`v0.1` includes:

- A local, single-process, single-user TUI with one active AgentRun at a time.
- One fixed user-managed Home selected by `KUPILOT_HOME`, with configuration,
  SQLite state, cache, and bounded logs under fixed descendants.
- Bare unconfigured TUI startup, masked setup for the one model runtime, and an
  explicit plaintext-local or process-only credential choice.
- The fixed `kupilot cache clear` maintenance command, limited to entries below
  the Home cache child and short-circuited before ordinary startup.
- A new Session for every bare start and explicit Session resume by picker,
  exact Session identifier, or `--last`. Session history is not inferred from a
  working directory.
- Selection and verification of one kubeconfig Context and Namespace for the
  active ClusterScope.
- An optional ResourceRef limited to Pod, Deployment, ReplicaSet, Job, or
  Service in the current Namespace.
- Natural-language questions, streaming responses, visible ToolInvocations,
  cancellation, safe history, and recoverable error reporting.
- Exactly six constrained, structured, read-only Tools.
- The eight diagnostic categories in the
  [product contract](./product.md#eight-mvp-diagnostic-categories).
- A Diagnosis that separates confirmed facts, hypotheses, missing information,
  and recommended actions.
- Informed consent before eligible cluster data is sent to the configured cloud
  model, plus minimal and sanitized local persistence.
- No background scan, Watch, informer, or autonomous AgentRun.

`v0.1` performs no Kubernetes write. It contains no Approval Dialog, no hidden
write executor, and no alternative command that can perform a recommendation.
The user performs any chosen action independently.

## The six read-only Tools

These are the complete model-visible Tool catalog for `v0.1`. Each
ToolInvocation inherits the AgentRun ClusterScope. The model cannot select a
different Context or Namespace, expand hard limits, or supply a generic
Kubernetes resource type.

### `get_resource`

Reads the diagnostic projection of one ResourceRef. It returns selected status
and relationship fields, never full YAML or arbitrary object data.

### `list_resources`

Finds a bounded set of named candidates or abnormal summaries in the current
Namespace. It is not a resource browser and accepts no raw selectors, pagination
control, or all-Namespace listing.

### `get_events`

Reads recent, related Kubernetes Events for one ResourceRef. Events are bounded,
sanitized, and treated as untrusted data rather than instructions.

### `get_pod_logs`

Reads a bounded, sanitized tail from the current instance of one Pod container.
It provides no follow or streaming mode, and raw log bytes are not persisted.

### `get_previous_pod_logs`

Reads a bounded, sanitized tail from the previous instance of one restarted Pod
container. It is separate from current logs, is unavailable when no previous
instance exists, and never expands to every container.

### `get_related_resources`

Follows fixed Kubernetes relationships needed for a Diagnosis. Traversal is
directed and bounded to at most two hops, with no recursive discovery or generic
graph traversal.

The directly addressable target kinds are fixed to Pod, Deployment, ReplicaSet,
Job, and Service. EndpointSlice can contribute only safe endpoint counts inside
a related-resource ToolInvocation. A StatefulSet can appear only as an existing
owner reference already carried by a Pod; KuPilot does not fetch or list the
StatefulSet. Custom resources and arbitrary Kubernetes API types are outside the
MVP.

Tool output is bounded and locally projected. A partial, truncated, forbidden,
unsupported, or stale result becomes missing information in the Diagnosis; it is
not an invitation to bypass policy or broaden the ClusterScope.

## `v0.2`: one controlled write

`v0.2` adds exactly one Kubernetes write operation:
`restart_deployment` for one explicitly identified Deployment.

The product boundary for that operation is:

- The Agent may propose it but cannot approve or execute it by itself.
- A dedicated Approval Dialog appears only in `v0.2`, defaults to rejection, and
  authorizes one specific, short-lived proposal.
- KuPilot revalidates the ClusterScope, target identity, and operation before the
  write. A changed or expired proposal fails closed.
- The operation accepts no arbitrary patch, YAML, command, or additional write
  parameters.
- KuPilot records the write attempt and distinguishes request acceptance,
  observed rollout progress, timeout, and verified completion.

`v0.2` still excludes every other write, including arbitrary patch, apply, or
delete; Pod deletion; scale; rollback; exec; batch changes; automatic approval;
and background remediation. A second write operation requires a future product
decision and is not implied by `v0.2`.

## Explicit non-goals

KuPilot does not include:

- A Kubernetes Dashboard, k9s replacement, resource tree, primary resource
  table, full YAML browser or editor, or live monitoring view.
- Shell or kubectl execution, Pod Exec, port forwarding, Helm, arbitrary network
  requests, or a general command runner.
- All-Namespace or cross-cluster diagnosis, concurrent cluster diagnosis,
  background inspection, scheduled tasks, or autonomous remediation.
- Secret reads, ConfigMap data reads, arbitrary Kubernetes API discovery, custom
  resources, or unbounded relationship traversal.
- Plugins, dynamic commands, MCP, RAG, retrievers, Multi-Agent orchestration, or
  a general extension platform.
- A web application, server-side control plane, accounts, team collaboration,
  remote cluster-side Agent or controller, telemetry, or crash reporting.
- Multiple model providers at once or a claim of compatibility with every
  endpoint described as OpenAI-compatible.
- A repository workspace model, code execution workflow, or general DevOps
  Agent behavior.

## Feature admission gate

Every proposed capability starts with one question:

> Does this feature help the Agent gather bounded Evidence and produce a safer
> Diagnosis, or does it replace the Agent with another cluster interface?

A feature is not admitted when it mainly replaces the Agent. It must also pass
every applicable gate below:

1. Does it directly improve the success rate or safety of the
   Evidence-to-Diagnosis flow?
2. Without it, would the user need to leave KuPilot to provide context that the
   Agent actually needs?
3. Does natural-language diagnostic intent remain the primary interaction,
   instead of resource navigation or direct manipulation?
4. Can it use fixed structured inputs, allowlists, strict output budgets, and
   deterministic tests?
5. Does it avoid expanding exposure of kubeconfig material, Kubernetes Secrets,
   the model API key, Events, or logs?
6. Is it required by at least one named diagnostic category with a repeatable,
   sanitized fixture?
7. Can a small maintainer team support it without a new always-on service or a
   broad compatibility matrix?
8. If it writes, is it the already admitted `v0.2` operation? Any other write is
   outside the frozen version boundary even if it appears useful.

Passing the gate does not override the version contract. A proposal must state
the target diagnostic category, required permissions, data eligible for model
transfer, output limits, failure modes, non-goals, test fixture, and a clear
"helps the Agent" conclusion.

Examples:

- Following a fixed owner relationship to find bounded Pod Evidence helps the
  Agent.
- Showing the purpose and result of a ToolInvocation helps the user supervise
  the Agent.
- Adding a sortable Deployment inventory with a restart shortcut replaces the
  Agent and is rejected.
- Adding a full YAML editor creates another Kubernetes IDE and is rejected.

See the [Glossary](./glossary.md) for the canonical domain language and the
[Privacy Overview](./privacy-overview.md) for data boundaries.
