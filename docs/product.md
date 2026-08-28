# KuPilot Product Contract

## Product definition

KuPilot is a local, single-process Kubernetes TUI Agent. It reads a narrowly
bound Kubernetes ClusterScope through constrained, structured Tools, turns the
safe observations into Evidence, and uses a configured cloud model to produce a
cautious Diagnosis.

KuPilot is Agent-first: natural-language diagnostic intent is the primary
interaction, and the Agent decides which bounded Evidence to gather. The TUI
helps the user provide context and supervise ToolInvocations. It does not become
a parallel resource management interface.

The core value of `v0.1` is to make the common diagnostic sequence - decide what
to inspect, collect the minimum useful facts, and explain uncertainty - available
in one interaction without giving the model general cluster access.

## Product principles

1. **Evidence before Diagnosis.** Confirmed facts must be traceable to Evidence
   from the current AgentRun. Model text cannot create Evidence.
2. **Narrow, immutable scope.** Each AgentRun is bound to one ClusterScope. A
   scope change invalidates the active AgentRun instead of mixing observations.
3. **Read-only means read-only.** `v0.1` has no Kubernetes write path and no
   Approval Dialog. Recommendations are clearly described as not executed.
4. **Minimum necessary access.** Tools use fixed resource kinds, relationships,
   and hard budgets. They do not expose Kubernetes as a generic API.
5. **Visible uncertainty.** Missing, denied, stale, conflicting, or truncated
   information remains a visible gap. KuPilot may conclude that there is not
   enough Evidence.
6. **Snapshot diagnosis, not monitoring.** Evidence describes an observation at
   a recorded time. KuPilot does not claim that the cluster remains unchanged.
7. **Local credential isolation.** Kubernetes and model credentials are not
   model content. A model key may be kept process-only or explicitly saved as
   disclosed plaintext under the user-managed KuPilot Home, but it never enters
   Session history, logs, audit, SQLite, or ordinary model-facing values.
   Cluster data is projected and redacted locally before any permitted cloud
   transfer.

## Intended `v0.1` user journey

1. The user starts a new Session. A bare `kupilot` invocation always creates a
   new Session; only an explicit `resume` action queries local Session history.
2. If the model profile or key is missing, the user completes masked setup in
   the single-screen TUI and chooses disclosed local plaintext storage or
   process-only use. No configuration file is required merely to open KuPilot.
3. The user selects and verifies a kubeconfig Context and Namespace. KuPilot
   forms the active ClusterScope without copying or persisting kubeconfig
   contents.
4. The user can optionally attach one ResourceRef from the fixed target kinds:
   Pod, Deployment, ReplicaSet, Job, or Service.
5. The user submits a diagnostic question in natural language. That submission
   creates one AgentRun with an immutable ClusterScope.
6. The Agent selects from the six read-only Tools. The TUI shows each
   ToolInvocation and its safe status or summary, and the user can cancel the
   AgentRun.
7. Tool results are projected, bounded, and redacted before they become Evidence
   or eligible model context.
8. KuPilot returns a Diagnosis with confirmed facts, hypotheses, missing
   information, recommended actions, the observed ClusterScope, and observation
   times.
9. The user evaluates and performs any desired action independently. `v0.1`
   never presents a recommendation as an action KuPilot executed.

## Diagnosis contract

Every completed Diagnosis is organized into four distinct parts:

- **Confirmed facts:** observations supported by Evidence from the current
  AgentRun. Each fact refers to its Evidence and observation time.
- **Hypotheses:** possible explanations, with uncertainty and a way to confirm or
  disprove them. A hypothesis is not promoted to fact by fluent wording.
- **Missing information:** facts KuPilot could not obtain because they were
  absent, forbidden, unsupported, stale, conflicting, or outside a hard limit.
- **Recommended actions:** steps the user may consider. They include relevant
  risks or prerequisites and are explicitly marked as not executed in `v0.1`.

A successful AgentRun means that KuPilot followed the Evidence and safety
contract. It does not mean that KuPilot necessarily found the root cause.

## Eight MVP diagnostic categories

These categories define the `v0.1` coverage target. They are product acceptance
boundaries, not promises that every real-world incident has a single discoverable
cause.

### 1. CrashLoopBackOff

- **User intent:** understand why a Pod container repeatedly starts and exits.
- **Permitted Evidence:** projected Pod and container state, restart and last
  termination details, recent related Events, bounded current or previous
  container log excerpts, and bounded owner relationships.
- **Successful Diagnosis:** identifies the observed restart behavior, correlates
  relevant state, Events, and log excerpts, and separates supported facts from
  possible causes.
- **Boundary:** a single log line is not sufficient to claim a root cause.

### 2. OOMKilled

- **User intent:** investigate a container whose previous instance was reported
  as OOMKilled.
- **Permitted Evidence:** projected termination reason and exit details, restart
  count, bounded previous container log excerpts, and owning workload status.
- **Successful Diagnosis:** confirms whether Kubernetes reported OOMKilled,
  describes the affected container and observed restart state, and identifies
  what additional resource context is missing.
- **Boundary:** a memory-related phrase in a log excerpt alone does not confirm
  OOMKilled or its underlying cause.

### 3. ImagePullBackOff

- **User intent:** understand why a Pod cannot obtain a container image.
- **Permitted Evidence:** projected container waiting reasons and sanitized,
  recent Events such as image pull failures or backoff reports.
- **Successful Diagnosis:** reports the observed pull state and Event details,
  then distinguishes supported findings from possible registry, image, network,
  or authorization explanations.
- **Boundary:** KuPilot does not read Kubernetes Secrets and must not guess that
  a registry credential is wrong without Evidence.

### 4. Pod Pending

- **User intent:** understand why a Pod remains Pending.
- **Permitted Evidence:** projected Pod conditions, safe scheduling constraint
  summaries, recent scheduling Events, and bounded owner relationships.
- **Successful Diagnosis:** states whether the Pod is unscheduled or blocked at
  another stage, cites the available scheduling Evidence, and exposes missing
  Event or permission data.
- **Boundary:** absent scheduling Events are not proof of resource shortage.

### 5. Readiness probe failure

- **User intent:** understand why a container or Pod is not Ready.
- **Permitted Evidence:** projected readiness conditions and container state,
  sanitized health-related Events, and bounded current container log excerpts.
- **Successful Diagnosis:** correlates the readiness state with probe Events or
  application observations and distinguishes the symptom from a possible cause.
- **Boundary:** service unavailability by itself does not prove a readiness probe
  failure.

### 6. Deployment with no available replicas

- **User intent:** understand why a Deployment has no available replicas.
- **Permitted Evidence:** projected Deployment replica counts and conditions,
  bounded related ReplicaSet and Pod status, and relevant sanitized Events or
  bounded log excerpts.
- **Successful Diagnosis:** traces the availability gap from the Deployment to
  affected ReplicaSets or Pods and identifies the strongest supported failure
  layer.
- **Boundary:** a Deployment condition alone is not a complete root-cause
  explanation.

### 7. Failed Job

- **User intent:** understand why a Job has failed or cannot complete.
- **Permitted Evidence:** projected Job counts and conditions, bounded related
  Pod termination state, recent sanitized Events, and bounded log excerpts.
- **Successful Diagnosis:** correlates Job status with the relevant Pod outcome
  and separates controller state, container failure, and application-level
  hypotheses.
- **Boundary:** a nonzero failed count alone does not prove an application error.

### 8. Service with no ready Endpoint

- **User intent:** understand why a Service has no ready backend Endpoint.
- **Permitted Evidence:** projected Service selector information, bounded related
  Pod matches and readiness, and EndpointSlice ready and not-ready counts without
  addresses.
- **Successful Diagnosis:** distinguishes no selector match, matched but unready
  Pods, and incomplete or forbidden relationship Evidence.
- **Boundary:** the existence of a Service object does not prove that a working
  backend exists.

## Product identity and adjacent tools

KuPilot is not:

- k9s or a Kubernetes Dashboard. It does not make resource navigation or direct
  manipulation the primary interaction.
- a kubectl wrapper. The model cannot generate or execute shell or kubectl
  commands through KuPilot.
- a generic chat bot. KuPilot gathers bounded, time-stamped Evidence instead of
  relying only on text pasted by the user.
- an IDE, monitoring system, cluster controller, or general DevOps Agent.

The detailed version boundary and feature admission gate are defined in
[Scope](./scope.md). Data handling is summarized in
[Privacy Overview](./privacy-overview.md). Canonical terms are defined in the
[Glossary](./glossary.md).
