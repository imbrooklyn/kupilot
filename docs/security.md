# Kupilot Security Threat Model

- Status: Accepted target for Kupilot `v0.5`
- Last updated: 2026-09-05

The checked-in implementation now includes named model roles, role-scoped
consent, safe Session context/summarization, deterministic permission routing,
the common ActionEnvelope/approval foundation, broad policy-bound built-in and
exact CRD resource reads, and deterministic read-only observability adapters.
The typed Deployment restart and the default-off Pod Exec, container-file, and
diagnostic-Pod handlers are composed through the action foundation. Review-
class Pod logs, optional data-source reads, and remote-diagnostic operations
use the same human/Reviewer/automatic supervision and release no attempt
authority until durable consume and exact target revalidation; no universal
live cluster compatibility is claimed. Exact default-off
local direct argv and shell plus typed scale/rollback/controller-owned-Pod
delete/cordon/uncordon/drain now use the shared Application dispatcher. No live
host-tool execution or operating-system sandbox claim is made.

## 1. Scope and security posture

Kupilot is a local, single-process, single-user Kubernetes operations Agent. It
uses the selected user's kubeconfig identity, explicit named model profiles and
origins, a local SQLite database, and a conversational TUI. It may perform only
the typed reads, controlled diagnostics, and supervised actions in
[Scope](scope.md). It is not a sandbox for arbitrary commands and does not
claim that model or Reviewer output is trusted.

The `v0.5` posture deliberately admits daily operational capabilities rather
than relying on an artificially small feature catalog. Security rests on
explicit code-owned authority, source projection, scope and policy generations,
role-bound consent, finite budgets, permission routing, durable pre-operation
audit, one-attempt execution, and separate verification.

## 2. Security objectives

Kupilot must:

1. keep kubeconfig contents, Kubernetes credentials, model credentials, and
   exec-credential output out of model, TUI, ordinary logs, audit, SQLite, and
   child environments;
2. ensure model, Reviewer, Kubernetes, and user text cannot choose a Context,
   widen namespace access, register or enable a capability, lower risk, expand
   budgets, create Evidence or permission, or call an executor;
3. prevent stale or cross-Context results from reaching model, persistence, or
   terminal sinks;
4. read only code-allowlisted sources and project only reviewed fields before
   redaction or serialization;
5. deny credentials, Secret values, ServiceAccount tokens, raw objects,
   arbitrary APIs, arbitrary destinations, generic command or YAML payloads,
   and unbounded output; any separately admitted sensitive source must have an
   exact policy, category, projection, and sink contract;
6. obtain role-, origin-, policy-, and category-bound informed consent before
   every model-content transfer;
7. keep runtime work finite, cancellable, observable through `/status`, and
   bounded by a frozen profile and hard ceilings;
8. classify every sensitive or effectful operation as `safe`, `review`,
   `critical`, or `deny`, route it under the frozen permission profile, and
   require an exact immutable `ActionEnvelope` plus durable pre-operation audit
   before execution;
9. render all external text safely in a terminal and keep meaning independent
   of color; and
10. retain and delete only the data admitted by the public retention contract.

Kupilot does not claim prevention of all model error, complete sensitive-value
detection, encrypted local storage, tamper-resistant audit, forensic erasure,
or protection from a fully compromised local account.

## 3. Assets and sensitive data

### Critical credentials

- kubeconfig file contents, inline certificates and keys, bearer tokens, and
  client-go `rest.Config` values;
- output from kubeconfig exec credential programs;
- the configured model API key and Authorization header; and
- any credential-shaped value found in an otherwise eligible Event, log, field,
  or provider error.

### Operationally sensitive data

- Context and Namespace names;
- resource names, UIDs, owner relationships, conditions, replica counts,
  workload timing and state, and storage or network identities;
- Kubernetes Event text and bounded container output;
- user questions, conversation history, model answers, and proposed actions;
- model origin, model name, request metadata, and usage counters; and
- approval target, digest, outcome, and verification audit.

Names and statuses are not public merely because they are not credentials.

## 4. Trust boundaries

1. **Terminal user to Application.** Input is untrusted Unicode. Local approval
   is meaningful only through typed request-bound state, not arbitrary text.
2. **Kubeconfig and credential program to Kubernetes adapter.** Credentials are
   necessary transport inputs and must remain adapter-confined.
3. **Kubernetes API to projection pipeline.** Objects, Events, logs, names, and
   error text are untrusted external data.
4. **Application to model origin.** Eligible content leaves the workstation
   only after consent and final origin checks.
5. **Model to Agent runtime.** Stream text, structured calls, Markdown, Evidence
   IDs, and action proposals are untrusted suggestions.
6. **Application to SQLite and local files.** Only explicit bounded safe values
   cross. The local filesystem is not encrypted or tamper-proof.
7. **Application policy to Reviewer.** The optional Reviewer receives only a
   bounded review projection and can return a recommendation for `review` work;
   it cannot create authority or review `critical` work.
8. **Application approval to external execution.** Kubernetes mutations,
   remote Pod diagnostics, diagnostic Pods, optional data-source requests, and
   local argv are distinct boundaries requiring exact policy, durable state,
   fresh validation, and owned cancellation.

## 5. Threat actors and assumptions

Relevant actors include:

- a malicious or compromised model endpoint;
- prompt injection inside user text, Kubernetes fields, Events, logs, historic
  messages, or model output;
- a cluster user able to create crafted Kubernetes data;
- a Kubernetes API server returning malformed, oversized, or changing data;
- a local process able to alter configuration or files under the user's
  account;
- a compromised local executable, remote container, diagnostic image, optional
  data source, or Reviewer endpoint; and
- accidental operator selection of a broad Context, Namespace policy, RBAC
  identity, budget profile, or action.

The local user controls kubeconfig and configuration. The operating system,
terminal, Go runtime, pinned dependencies, selected Kubernetes client identity,
and TLS trust store are assumed not fully compromised. A malicious kubeconfig
exec program is outside Kupilot's containment guarantee, although Kupilot
restricts how it is launched and what environment and output it receives.

## 6. Principal threats and controls

### T01: Model-selected arbitrary cluster access

**Threat.** A model invents a Kind, GVR, selector, Namespace, Context, raw HTTP
request, executable, image, destination, kubectl argv, or shell command.

**Controls.** Catalog names and versions are code-defined. Schemas reject
unknown, duplicate, wrong-type, extra, and oversized fields. Context and access
policy are runtime-injected. Namespace arguments are canonicalized and checked
against the frozen `current` or `all` policy. Kubernetes and process access use
task-specific ports with no model-visible dynamic client, REST builder,
discovery fallback, executable handle, or shell fallback. Exact policy-admitted
CRDs and argv are locally selected before model input is bound.
For an exact CRD, discovery validates only the configured
group/version/resource/Kind/scope contract; it cannot register another API,
verb, subresource, field, or ceiling. Typed predicates produce only
policy-owned field or label selectors, and continuation tokens stay inside the
Kubernetes adapter.
One atomic batch of known, structurally safe selections that fails semantic
binding receives only fixed local policy feedback; rejected arguments are not
echoed and Tool handler and Kubernetes call counts remain zero. Unknown,
malformed, authority-bearing, and sensitive selections still fail terminally.

### T02: Cross-Namespace or cross-Context confusion

**Threat.** A broader namespace policy causes observations to be mislabeled as
coming from the working Namespace, or a late result from an old Context is
accepted.

**Controls.** Working Namespace and namespace access are distinct RunInput
fields. Every namespaced call records its exact Namespace in canonical
arguments and ResourceRef. All-Namespace lists use an explicit marker and retain
each item's actual Namespace. Cluster-scoped resources use no fake Namespace.
Generation checks occur before I/O, after return, and at Application event
acceptance. Context changes cancel and invalidate before a new client is
published.

### T03: Sensitive source disclosure

**Threat.** Secret data, ConfigMap values, environment variables, files, Node
addresses, provider identifiers, raw object fields, logs, metrics, process
output, or credentials reach an unintended sink.

**Controls.** Source allowlisting precedes projection. Credential and Secret-
value reads are denied before I/O when locally decidable. Each built-in, exact
CRD, metrics source, log mode, file policy, and process result has a reviewed
project-owned projection and explicit category. The current broad resource
path admits only scalar `metadata`, `status`, and `spec` paths, rejects a
credential-shaped path unless it is classified sensitive, never exposes a
sensitive field to the model, and reads Secret identity through metadata-only
transport. Eligible text then passes normalization,
sensitive-value block/redaction, hard item/byte limits, neutral serialization,
and a final role/origin/category consent check.

### T04: Prompt injection and false authority

**Threat.** Instruction-like data asks the Agent to alter policy, call an
unknown capability, fabricate Evidence, approve a restart, or claim execution.

**Controls.** External content is labeled untrusted in model envelopes. Runtime
state machines, not prompt compliance, control authority. Only local Tool
handling creates Evidence. Only typed Application action state can create a
proposal, approval, request attempt, or verification event. Markdown and Tool
text are inert data. Model commentary attached to a structured Tool response is
bounded inside the Eino boundary and discarded after the assembled response
proves `tool_calls`; it cannot become visible text, Tool authority, Evidence, or
action state. Eino's indexed argument assembly and every project-owned runtime
authorization check remain mandatory.

### T05: Model transfer without valid consent

**Threat.** Cluster or conversation content is sent to a changed or unexpected
model origin or with newly enabled categories.

**Controls.** Endpoint canonicalization rejects userinfo, query strings,
insecure TLS overrides, unsafe redirects, and non-loopback plain HTTP. Consent
binds policy version, model role, canonical origin hash, and exact categories.
Profile, role, origin, category, or meaning changes invalidate consent and
pending authority. Application checks the exact tuple immediately before every
eligible transfer. There is no fallback or router to another origin.

### T06: Runaway Agent cost or API load

**Threat.** Repetition, large lists, logs, model latency, or an adversarial
stream consumes unbounded time, requests, memory, or spend.

**Controls.** Profiles freeze independent finite Agent, Reviewer, summary,
capability, data-source, remote-exec, local-process, item, byte, stream, time,
and cost counters. Reservation is atomic and precedes I/O. Child deadlines are
capped by remaining ownership. Exact token and stream values require pinned
dependency and endpoint evidence; conservative byte, call, and time limits
remain mandatory. `/status` exposes usage without external calls. Model output
cannot switch profile or grant an unlimited mode. Broad resource lists send a
server-side `limit`, keep continuation private, and enforce independent
per-response bytes plus cumulative pages, scanned items, returned items, bytes,
and time. A reached post-page ceiling yields explicit partial Evidence instead
of silently presenting a complete result.

### T07: Terminal escape or misleading rendering

**Threat.** Kubernetes, user, history, model, or error text emits escape,
device-control, bidirectional, invalid UTF-8, or oversized terminal content.

**Controls.** External text is normalized, bounded, and strips or visibly
replaces unsafe control sequences before render state. `Update` and `View` have
no business I/O. Scope, policy, approval, and execution states include text and
do not rely on color. Unknown terminal backgrounds prefer default foreground
and dim styling rather than low-contrast hard-coded colors. Runtime content
uses a cleared primary-screen live frame. Newly immutable safe history is
removed from the live projection before a settled compact frame accepts
bounded row insertion. A completed block is acknowledged only afterward, and
an undersized terminal retains it safely instead of risking duplicate live
rows or transient layout gaps in scrollback. The only admitted spacing is one
code-owned inert trailing row per immutable block. Composer and streaming
drafts, model-selected styling, clipboard controls, and device controls cannot
enter scrollback. Mouse reporting remains disabled so native selection and
scrolling stay terminal-owned. The composer exposes one real cursor for
operating-system input-method positioning; its placeholder is never editable
state. Working animation messages are local, bounded, correlated to the active
run and scope generation, and rejected after terminal or stale state. They
cannot affect budgets, Evidence, authority, or external calls.

Provisional answer text uses a stateful pre-render processor so JSON escapes,
UTF-8 text, carriage returns, CSI, OSC, control strings, bidirectional controls,
and sensitive patterns remain safe when their syntax is split across provider
chunks. Raw envelope syntax and metadata are not rendered. The first safe
fragment is delivered promptly; later fragments are coalesced under independent
byte, time, and event limits. A terminal result replaces the draft, and only
that immutable result can enter terminal scrollback.

### T08: Credential leakage

**Threat.** A key enters ordinary configuration, formatting, error, log, child
environment, model content, or persistence.

**Controls.** The model key is extracted before ordinary typed configuration
decoding and held in an opaque non-renderable wrapper. The one-shot environment
source is unset. It never enters CLI values, domain DTOs, prompts, TUI history,
audit, SQLite, or child environments. The optional saved plaintext copy is
limited to the fixed Home configuration after explicit disclosure. Kubernetes
credentials remain inside client-go and the kube adapter.

Each streamed answer also uses an exact-credential guard that retains matching
prefixes across chunk boundaries before emitting text. After JSON decoding, the
complete Diagnosis draft is checked again so escaped credential bytes in answer
or metadata cannot enter Domain, persistence, UI terminal state, audit, or
logs. A match fails the run with code-authored safe text.

### T09: Unsafe external process launch

**Threat.** A kubeconfig credential plugin, restricted argv integration, or
shell request becomes command injection, receives unrelated secrets, escapes
its cancellation owner, or produces unbounded output.

**Controls.** Kubeconfig exec authentication remains a distinct adapter-only
exception whose executable and argv come solely from the selected kubeconfig.
Other local execution is default off and uses one policy-selected executable
and exact argv, `shell=false`, a fixed validated working directory, no inherited
stdin, and an allowlisted minimal environment without model or Kubernetes
credentials. It bounds stdout/stderr and owns the process group, timeout,
cancellation, and join. Tool-specific policy blocks kubectl identity/context
overrides, arbitrary Helm values/plugins, and unbound Argo CD origins. Shell is
a separately named `critical` capability with its own exact bounded command
field and cannot be smuggled through a restricted runner's `-c`, wrappers,
files, stdin, flags, or environment.

### T10: Unapproved, stale, replayed, or ambiguous operation

**Threat.** Model, Reviewer, or TUI text authorizes a sensitive read, remote or
local execution, or mutation; an approval is replayed, targets changed state,
bypasses audit, retries a conflict, or repeats after an ambiguous outcome.

**Controls.** Each operation has a fixed semantic schema and deterministic risk.
An immutable `ActionEnvelope` binds run, Session, policy/profile, both
generations, complete scope, exact target or executable, typed parameters,
data/sink/network effects, limits, expiry, and verification plan. Approve-once
defaults to rejection, expires after 60 seconds, and is single-use. Application
checks decision routing, nonce, digest, time, state, policy, scope, and target;
durably consumes and pre-audits; checks both generations again; and permits at
most one external attempt. Conflict, audit failure, restart, mismatch, or
ambiguity never triggers an automatic retry. Acceptance and verification are
separate.

### T11: Persistence over-collection or unsafe recovery

**Threat.** Raw content or authority is retained, deletion is partial, or
restart resumes live work or approval.

**Controls.** SQLite schemas contain explicit safe columns and no generic
payload escape hatch. Raw objects, Events, logs, model traffic, prompts, Tool
results, errors, credentials, and framework values are ineligible. Released
migrations are checksummed and forward-only. Startup marks running runs
interrupted and unexecuted approvals terminal. Resume restores safe history and
unverified candidates only. Retention and deletion use bounded transactional
operations with visible failures.

### T12: Error-text policy or disclosure

**Threat.** Raw provider, Kubernetes, SQLite, filesystem, or framework error
text leaks data or controls runtime branching.

**Controls.** Adapters map failures to stable project-owned classes and
code-authored safe messages. Internal causes remain wrapped for local handling
where appropriate but do not cross safe sinks. Runtime policy does not branch
on raw error strings. Sensitive diagnostics is a separate explicit local-log
choice with documented residual risk.

### T13: Reviewer becomes permission authority

**Threat.** A Reviewer approves critical work, changes deterministic risk,
creates a reusable rule, receives excessive operational data, or silently
replaces human review after a failure.

**Controls.** Reviewer delegation is limited to `review` under `auto-review` or
an exact custom rule. Input is a minimal envelope projection. Output is strict,
non-streaming, and no-Tool: `approve`, `deny`, or `escalate_to_user` with a
bounded rationale. Malformed output, timeout, cancellation, consent or budget
failure, and stale policy authorize nothing. `critical` is never
Reviewer-routed, there is no fallback, and only deterministic Application state
can create authority.

### T14: Remote diagnostics escape local assumptions

**Threat.** Pod Exec, container-file access, or a diagnostic Pod is treated as
equivalent to a local operating-system sandbox, follows unsafe paths, inherits
a ServiceAccount token, or contacts a model-selected destination.

**Controls.** File access binds exact Pod UID/container/path and uses one no-
shell USTAR request with framing reserved inside the transport ceiling. Its
ordered headers must prove every parent is a
directory and the final entry is a regular file. It rejects links, devices,
pseudo-filesystems, Secret/ConfigMap/projected/credential mounts, stderr, size
violations, and sink-policy failures. Sensitive mounts at `/`, sensitive volume
devices, duplicate volume identities, and unknown mount/device references fail
closed. This cooperative tar check is not an atomic kernel `openat2` guarantee
against a malicious filesystem race. Pod Exec binds one Pod,
container, UID, resource version, executable, argv, stdin/TTY/shell flags,
network/data effects, exact live Pod/container destination digest, timeout, and
output limit. General exact argv can still implement in-container side effects
or network access; this is conservatively approval-visible as a remote-Pod
effect and is not an in-container sandbox or an admission of credential data.
Diagnostic Pods use policy-selected pinned images, non-root/non-privileged
settings, a read-only root filesystem, no volumes or host namespaces, finite
resources, disabled token automount and Service links, non-preempting scheduling,
only a zero API-default priority with no PriorityClass, one policy-fixed
Namespace and exact same-Namespace Service destination digest, a required
non-empty Service selector, strict admission response revalidation, and
separately audited create, wait, log, delete, and ambiguous-cleanup states. ExternalName,
selectorless, obvious metadata/link-local/address-confusion, and policy-
external targets are denied. Definite create rejection cannot trigger deletion
of a pre-existing Pod. Bundle shutdown preserves the transport until bounded
exact cleanup joins. An image allowlist is not a network
sandbox; actual NetworkPolicy and CNI behavior remain operator-provided
deployment evidence that Kupilot does not verify.

## 7. Kubernetes access matrix

<!-- markdownlint-disable MD013 -->

| Source | Read behavior | Prohibited fields or behavior |
| --- | --- | --- |
| Namespace | Exact/bounded list; identity, phase, and one bounded reason | Contents, arbitrary annotations, finalizer details |
| Node | Exact/bounded list; Ready/NotReady state and one bounded health reason | Addresses, provider ID, images, system info, taint values, raw capacity/allocatable maps |
| Pod and controllers | Exact/bounded list; status, conditions, replica counts, fixed relationships; one exact non-credential environment value only through a separately consented `review` policy | Generic or bulk environment values, credential values, volume sources, raw template, arbitrary annotations |
| Service and Ingress | Service type and bounded ports; Ingress identity only; address-free Service readiness relationship | Endpoint and Ingress addresses, Ingress rules/backends, TLS Secret contents, arbitrary annotations |
| PVC and PV | Identity and phase; PV may include one bounded status reason | Class, mode, capacity, claim details, credentials, CSI attributes, volume source, topology values |
| ConfigMap | Identity and creation time; one exact key only through a separately consented `review` policy | Generic or bulk `data`/`binaryData`, credential values, object dump |
| HPA and PDB | Replica/health/disruption counts and one bounded failing-condition reason | HPA target and raw metrics, raw selector, object bodies |
| Event | Bounded normalized fields related to one target | Raw object, managed fields, arbitrary series payload |
| Pod log | Non-following bounded current or previous tail after consent | Unbounded/follow stream, raw persistence, automatic sensitive-field bypass |
| Pod and Node metrics | Bounded typed samples and reviewed fields | Unbounded time series, provider identifiers, or arbitrary metric query |
| Exact policy-admitted CRD | Fixed group/version/resource/Kind/scope/verbs and projection | Discovery expansion, arbitrary GVR, object dump, or unreviewed field |
| Prometheus or Loki | Explicit optional bounded query through its own source policy | Implicit fallback, arbitrary endpoint/query, credential forwarding, or unbounded response |
| Container file | Exact bounded path and safe projection | Credential paths, unsafe symlinks, devices, pseudo-filesystems, or raw persistence |
| Pod diagnostic | Exact Pod/container and policy-owned argv | Model-owned command string, unbounded output, hidden shell, or authority restoration |
| EndpointSlice | Indirect address-free readiness counts only | Addresses and direct selection |
| Secret | Exact safe metadata projection only | Data, token, credential value, generic object, or automatic model transfer always denied |

<!-- markdownlint-enable MD013 -->

Kubernetes RBAC remains authoritative. Operators should grant only the resources
and verbs needed for their chosen namespace policy and action catalog. A broad
ClusterRole is not required when `current` mode and namespaced Roles suffice.
The checked-in paths implement the built-in and exact configured CRD rows, safe
Secret metadata, bounded Events and logs, typed Pod/Node metrics, and explicit
Prometheus/Loki source adapters, plus the exact remote-diagnostic rows behind
default-off configuration. Review-class operations still require the separate
permission, consent, sink, and ActionEnvelope path described below. Exact local
process policies, typed remediation, and remote-diagnostic human or Reviewer
routes use that path. A remote Tool attempt remains blocked until Application
durably consumes the exact authority; every denial or stale result releases no
execution authority.

## 8. Permission and execution safety

The deterministic risk classes are `safe`, `review`, `critical`, and `deny`.
The default permission profile is `ask`. `auto-review` delegates only `review`;
`critical` remains human-reviewed. `full-access` and exact custom critical-auto
rules require explicit high-risk selection and cannot bypass capability
enablement, scope, RBAC, consent, audit, revalidation, or `deny`. `read-only`
cannot be approved into mutation or process access.

The typed P0 mutation catalog is restart, scale, rollback, one controller-owned
ordinary Pod delete, cordon, uncordon, and drain. Each uses its own exact
semantic diff and verification plan under the common `ActionEnvelope` flow.
Restart still changes only Kupilot's Pod-template annotation. Generic patch,
apply, edit, YAML, arbitrary delete, and model-generated command surfaces are
denied.

Predefined read-only Pod diagnostics (`review`), other Pod Exec (`critical`),
and diagnostic Pods (`critical`) now use separate exact schemas and the common
action lifecycle. Restricted local argv and shell use separate default-off
policy catalogs and risk classes. Direct argv is structurally classified,
scripts and known wrappers/interpreters are denied, and the shell command string
is accepted only by the shell-tagged `critical` operation. No decision for one
envelope authorizes a different target, parameter, command, attempt, or cleanup.

The deterministic evaluator covers every profile/effect/risk combination and
rejects disabled, unadmitted, incompatible, and hard-deny inputs before
Reviewer or executor access. Its process-local Session-rule APIs and durable
approval state do not create a capability. The composition root retains the
default `ask` profile and wires remote diagnostics, review-class Pod logs, and
optional data-source reads through the same inline human, Reviewer, Session-
rule, and automatic supervision. It performs operation-specific preparation,
target revalidation, durable consume, final generation/expiry checks, one
bounded attempt, and content-free outcome audit. `/permissions` changes this
routing only through a typed Application command and cannot enable a disabled
catalog entry or bypass consent, RBAC, risk, or `deny`.

## 9. Privacy and retention interactions

Broader resource access does not automatically enable broader model transfer.
Consent categories still govern conversation, resource references and status,
Events, logs, metrics, files, optional data sources, remote-diagnostic output,
and resumed context. Local-process output has no model-transfer category and
remains terminal-only. Sensitive categories and high-risk capabilities remain
separately disabled by default.

Standard persistence retains only bounded validated answers and safe metadata
for the durations in [Data Retention](data-retention.md). Minimal persistence
does not weaken approval audit. `/status` is in-memory and does not create a new
durable content record merely because it is viewed.

## 10. Required security tests

Tests must use synthetic canaries and deterministic fake clocks, clients,
barriers, and temporary databases. Required proof includes:

- exact Kubernetes, optional data-source, remote-exec, and local-process
  requests and projections for each allowlisted source;
- zero external calls for credentials, Secret values, unknown API/CRD,
  malformed Namespace, `current`-policy cross-Namespace, stale scope/policy,
  disallowed path/image/destination/argv, and shell-smuggling paths;
- zero model content calls before valid role/origin/category consent or after
  any bound value changes;
- zero Tool/handler calls for malformed or invented structured calls;
- exact role- and capability-aware hard-ceiling, cancellation, timeout, byte,
  item, process, repetition, no-progress, and cost accounting;
- the complete permission-profile, risk, Reviewer, Session-rule, and generation
  matrix, with zero executor calls for every denied or failed path;
- no credential or prohibited canary in model, TUI, error, log, audit, SQLite,
  child-process, or export sinks;
- terminal-safe rendering across dark, light, ANSI-16, and `NO_COLOR`, including
  real-cursor Unicode input and stale Working-frame rejection;
- provisional-answer safety across every synthetic chunk boundary, including
  Unicode and JSON escapes, exact credentials, sensitive patterns, terminal
  controls, cancellation, timeout, stale scope, Tool-turn reset, event limits,
  final replacement, and scrollback exclusion; and
- standard/minimal memory, summary coverage, and explicit resume that never
  restore a run, stream, live generation, Evidence, permission rule,
  ActionEnvelope, approval authority, or execution retry; and
- every typed action's revalidation, one-attempt, ambiguous-outcome, audit, and
  independent verification states.

## 11. Residual risks

- A model can still produce an incorrect or misleading interpretation.
- Eligible operational text may contain a sensitive value that heuristics do
  not recognize.
- `all` namespace policy can expose more metadata when RBAC also permits it.
- Longer profiles can increase spend and API load within their finite limits.
- A Reviewer can make an incorrect approval, denial, or escalation recommendation;
  deterministic risk and hard-deny policy still bound its effect.
- Pod Exec, diagnostic Pods, local processes, and shell can affect remote or
  local systems when explicitly enabled, even after correct review.
- An image or executable allowlist does not by itself provide network or
  operating-system isolation.
- Local SQLite, logs, configuration, and exports are not encrypted.
- Visible conversation may remain in terminal-emulator scrollback after
  Kupilot exits, changes Session, or deletes its own stored history.
- The local user or another process with the same file permissions can alter
  local state.
- A Kubernetes mutation can have workload impact even after correct approval.
- Kubeconfig exec programs and the configured model provider have behavior
  outside Kupilot's full control.

## References

- [Product Contract](product.md)
- [Scope](scope.md)
- [Architecture](architecture.md)
- [Privacy Overview](privacy-overview.md)
- [Data Retention Contract](data-retention.md)
- [ADR-0012: Require Digest-Bound Approval for Writes](adr/0012-require-digest-bound-write-approval.md)
- [ADR-0014: Isolate Runs with ClusterScope Generation](adr/0014-cluster-scope-generation-isolation.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](adr/0020-contain-kubeconfig-exec-credentials.md)
- [ADR-0026: Require Informed Consent Before Model Transfer](adr/0026-require-informed-consent-before-model-transfer.md)
- [ADR-0037: Adopt an Operational Capability Catalog](adr/0037-adopt-an-operational-capability-catalog.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](adr/0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](adr/0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0040: Use a Codex-Style Conversational TUI](adr/0040-use-a-codex-style-conversational-tui.md)
- [ADR-0043: Use One Eino Runtime Boundary](adr/0043-use-one-eino-runtime-boundary.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Admit Controlled Execution and Remediation](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
