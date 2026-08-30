# Kupilot Security Threat Model

- Status: Accepted for Kupilot `v0.4`
- Last updated: 2026-08-30

## 1. Scope and security posture

Kupilot is a local, single-process, single-user Kubernetes operations Agent. It
uses the selected user's kubeconfig identity, one configured model origin, a
local SQLite database, and a conversational TUI. It can perform the typed reads
and supervised actions in [Scope](scope.md); it is not a sandbox for arbitrary
commands and does not claim that model output is trusted.

The `v0.4` posture deliberately allows more operational reads than the original
MVP. Security therefore rests on explicit authority, source projection, scope
generation, consent, bounded execution, and per-action approval rather than on
an artificially small permanent feature catalog.

## 2. Security objectives

Kupilot must:

1. keep kubeconfig contents, Kubernetes credentials, model credentials, and
   exec-credential output out of model, TUI, ordinary logs, audit, SQLite, and
   child environments;
2. ensure model and user text cannot choose a Context, widen namespace access,
   register a capability, expand budgets, create Evidence, approve an action,
   or call an executor;
3. prevent stale or cross-Context results from reaching model, persistence, or
   terminal sinks;
4. read only code-allowlisted sources and project only reviewed fields before
   redaction or serialization;
5. deny Secret objects and data, ConfigMap values, environment values, raw
   objects, arbitrary APIs, arbitrary network requests, and shell execution;
6. obtain origin- and category-bound informed consent before model-content
   transfer;
7. keep runtime work finite, cancellable, observable through `/status`, and
   bounded by a frozen profile and hard ceilings;
8. require exact local single-use approval and durable pre-operation audit for
   every Kubernetes mutation;
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
7. **Application approval to Kubernetes mutation.** This is a separate authority
   boundary requiring durable state and fresh target checks.

## 5. Threat actors and assumptions

Relevant actors include:

- a malicious or compromised model endpoint;
- prompt injection inside user text, Kubernetes fields, Events, logs, historic
  messages, or model output;
- a cluster user able to create crafted Kubernetes data;
- a Kubernetes API server returning malformed, oversized, or changing data;
- a local process able to alter configuration or files under the user's
  account; and
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
request, kubectl command, or shell command.

**Controls.** Catalog names and versions are code-defined. Schemas reject
unknown, duplicate, wrong-type, extra, and oversized fields. Context and access
policy are runtime-injected. Namespace arguments are canonicalized and checked
against the frozen `current` or `all` policy. Kubernetes uses typed task-specific
ports with no dynamic client, REST builder, discovery fallback, or shell.

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

**Threat.** Secret data, ConfigMap values, environment variables, Node addresses,
provider identifiers, raw object fields, or credentials reach a sink.

**Controls.** Source allowlisting precedes projection. Secret reads and
ConfigMap-value reads are denied before Kubernetes I/O when locally decidable.
Each Kind has a reviewed project-owned projection. Node addresses, images,
provider ID, system info, raw allocatable values, volume sources, arbitrary
annotations, Pod-template environment, and secret references are omitted.
Eligible text then passes normalization, sensitive-value block/redaction, and
hard size limits before serialization.

### T04: Prompt injection and false authority

**Threat.** Instruction-like data asks the Agent to alter policy, call an
unknown capability, fabricate Evidence, approve a restart, or claim execution.

**Controls.** External content is labeled untrusted in model envelopes. Runtime
state machines, not prompt compliance, control authority. Only local Tool
handling creates Evidence. Only typed Application action state can create a
proposal, approval, request attempt, or verification event. Markdown and Tool
text are inert data. Model commentary attached to a structured Tool response is
bounded and validated inside the model adapter, then discarded when the same
response finishes with `tool_calls`; it cannot become visible text, Tool
authority, Evidence, or action state. Indexed Tool assembly and every runtime
authorization check remain mandatory.

### T05: Model transfer without valid consent

**Threat.** Cluster or conversation content is sent to a changed or unexpected
model origin or with newly enabled categories.

**Controls.** Endpoint canonicalization rejects userinfo, query strings,
insecure TLS overrides, unsafe redirects, and non-loopback plain HTTP. Consent
binds policy version, canonical origin hash, and exact categories. Origin,
category, or meaning changes invalidate consent. Application checks consent and
origin immediately before the first and every subsequent eligible transfer.

### T06: Runaway Agent cost or API load

**Threat.** Repetition, large lists, logs, model latency, or an adversarial
stream consumes unbounded time, requests, memory, or spend.

**Controls.** Compact, balanced, and extended profiles freeze finite counters
and deadlines per run; all remain below code hard ceilings. Reservation is
atomic and precedes I/O. Child deadlines are capped by remaining run time.
Repeated-call, log-call, result-byte, item, traversal, no-progress, and terminal
rules are independent. `/status` exposes usage without external calls. Model
output cannot switch profile or grant an unlimited mode.

### T07: Terminal escape or misleading rendering

**Threat.** Kubernetes, user, history, model, or error text emits escape,
device-control, bidirectional, invalid UTF-8, or oversized terminal content.

**Controls.** External text is normalized, bounded, and strips or visibly
replaces unsafe control sequences before render state. `Update` and `View` have
no business I/O. Scope, policy, approval, and execution states include text and
do not rely on color. Unknown terminal backgrounds prefer default foreground
and dim styling rather than low-contrast hard-coded colors. Only that safe
render projection can cross the monotonic commit boundary into primary-screen
scrollback; streaming drafts, duplicate commits, model-selected styling,
clipboard controls, and device controls cannot. The composer exposes one real
cursor for operating-system input-method positioning; its placeholder is never
editable state. Working animation messages are local, bounded, correlated to
the active run and scope generation, and rejected after terminal or stale
state. They cannot affect budgets, Evidence, authority, or external calls.

### T08: Credential leakage

**Threat.** A key enters ordinary configuration, formatting, error, log, child
environment, model content, or persistence.

**Controls.** The model key is extracted before ordinary typed configuration
decoding and held in an opaque non-renderable wrapper. The one-shot environment
source is unset. It never enters CLI values, domain DTOs, prompts, TUI history,
audit, SQLite, or child environments. The optional saved plaintext copy is
limited to the fixed Home configuration after explicit disclosure. Kubernetes
credentials remain inside client-go and the kube adapter.

### T09: Unsafe kubeconfig exec credential launch

**Threat.** A kubeconfig credential plugin becomes a shell injection or receives
unrelated secrets.

**Controls.** Strict deny mode is available. Allow mode launches the exact
program and arguments from the selected kubeconfig without a shell, supplies a
minimal environment that omits the model key, bounds stdout/stderr, keeps output
inside the credential decoder, and terminates with the owning Context. No other
external-process capability exists.

### T10: Unapproved, stale, replayed, or ambiguous write

**Threat.** Model or TUI text authorizes a mutation; an approval is replayed,
expires, targets changed state, bypasses audit, retries a conflict, or repeats
after an ambiguous outcome.

**Controls.** Each action has a fixed semantic operation. Approval defaults to
rejection, expires after 60 seconds, is single-use, and binds a versioned digest
to run, Session, policy, complete scope, target identity, fingerprint,
parameters, and expiry. Application checks nonce, digest, time, state, and
scope; re-reads the target; durably consumes and pre-audits; checks scope again;
and permits at most one execution. Conflict, audit failure, restart, mismatch,
or ambiguity never triggers an automatic write retry. Request acceptance and
verification are separate.

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

## 7. Kubernetes access matrix

<!-- markdownlint-disable MD013 -->

| Source | Read behavior | Prohibited fields or behavior |
| --- | --- | --- |
| Namespace | Exact/bounded list; identity, phase, and one bounded reason | Contents, arbitrary annotations, finalizer details |
| Node | Exact/bounded list; Ready/NotReady state and one bounded health reason | Addresses, provider ID, images, system info, taint values, raw capacity/allocatable maps |
| Pod and controllers | Exact/bounded list; status, conditions, replica counts, fixed relationships | Environment values, volume sources, raw template, arbitrary annotations |
| Service and Ingress | Service type and bounded ports; Ingress identity only; address-free Service readiness relationship | Endpoint and Ingress addresses, Ingress rules/backends, TLS Secret contents, arbitrary annotations |
| PVC and PV | Identity and phase; PV may include one bounded status reason | Class, mode, capacity, claim details, credentials, CSI attributes, volume source, topology values |
| ConfigMap | Identity and creation time only | `data`, `binaryData`, values |
| HPA and PDB | Replica/health/disruption counts and one bounded failing-condition reason | HPA target and raw metrics, raw selector, object bodies |
| Event | Bounded normalized fields related to one target | Raw object, managed fields, arbitrary series payload |
| Pod log | Non-following bounded current or previous tail after consent | Unbounded/follow stream, raw persistence, automatic sensitive-field bypass |
| EndpointSlice | Indirect address-free readiness counts only | Addresses and direct selection |
| Secret | None | Object and data always denied |

<!-- markdownlint-enable MD013 -->

Kubernetes RBAC remains authoritative. Operators should grant only the resources
and verbs needed for their chosen namespace policy and action catalog. A broad
ClusterRole is not required when `current` mode and namespaced Roles suffice.

## 8. Mutation safety

The current action is `restart_deployment`, which changes only
`spec.template.metadata.annotations.kupilot.io/restartedAt` on one exact
Deployment with a fresh resource-version concurrency precondition. The model
does not supply the patch, annotation, timestamp, UID, generation, or resource
version.

Future mutations do not inherit approval merely because they are Kubernetes
writes. Each needs an Accepted schema and threat review covering target,
parameters, semantic diff, risk copy, digest fields, revalidation, audit,
one-attempt behavior, and verification.

## 9. Privacy and retention interactions

Broader resource access does not automatically enable broader model transfer.
Consent categories still govern conversation, resource references and status,
Events, and container output. Container output remains separately disabled by
default.

Standard persistence retains only bounded validated answers and safe metadata
for the durations in [Data Retention](data-retention.md). Minimal persistence
does not weaken approval audit. `/status` is in-memory and does not create a new
durable content record merely because it is viewed.

## 10. Required security tests

Tests must use synthetic canaries and deterministic fake clocks, clients,
barriers, and temporary databases. Required proof includes:

- exact Kubernetes requests and projections for each allowlisted source;
- zero Kubernetes calls for Secret, ConfigMap data, unknown API, malformed
  Namespace, `current`-policy cross-Namespace, and stale pre-call paths;
- zero model content calls before valid consent or after origin/category change;
- zero Tool/handler calls for malformed or invented structured calls;
- exact compact, balanced, extended, hard-ceiling, cancellation, timeout,
  byte, repetition, and no-progress accounting;
- zero executor calls for every approval, target, scope, time, state, audit, and
  replay denial;
- no credential or prohibited canary in model, TUI, error, log, audit, SQLite,
  child-process, or export sinks;
- terminal-safe rendering across dark, light, ANSI-16, and `NO_COLOR`, including
  real-cursor Unicode input and stale Working-frame rejection; and
- restart recovery that never restores a run, stream, live generation,
  approval authority, or write retry.

## 11. Residual risks

- A model can still produce an incorrect or misleading interpretation.
- Eligible operational text may contain a sensitive value that heuristics do
  not recognize.
- `all` namespace policy can expose more metadata when RBAC also permits it.
- Longer profiles can increase spend and API load within their finite limits.
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
