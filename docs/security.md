# KuPilot Security Threat Model

- Status: Accepted security baseline for `v0.1` and the admitted `v0.2` write
  boundary
- Date: 2026-08-10

This document defines KuPilot's security objectives, trust boundaries, runtime
controls, and deterministic security tests. It is normative for implementation
and review. Prompt text may reinforce safe behavior, but it is never an
authorization, isolation, confidentiality, or approval control.

The [Architecture](architecture.md), [Scope](scope.md), and
[Data Retention Contract](data-retention.md) remain normative alongside this
document. Where a control cannot be enforced or its result cannot be recorded as
required, KuPilot fails closed at that boundary.

## 1. Scope and security objectives

KuPilot must protect all of the following:

- Kubernetes authentication material, including kubeconfig contents, bearer
  tokens, client certificates, private keys, and exec credential output.
- The model API key and any authentication header derived from it.
- Cluster data that may be sensitive even when it is not a credential, including
  Context, Namespace, and resource names, Events, status fields, and container
  output.
- ClusterScope integrity and the provenance of every accepted Evidence item.
- Tool, budget, endpoint, and model-contract integrity.
- Local history and audit data stored in SQLite.
- Terminal integrity and the user's ability to distinguish data, advice,
  approval, execution, and verification.
- The absence of a Kubernetes write path in `v0.1` and the integrity of the one
  admitted `v0.2` write path.

The security objectives are confidentiality of excluded data, least-privilege
cluster access, deterministic runtime authorization, cross-scope isolation,
bounded cost and output, honest auditability, and fail-closed writes.

## 2. Trust assumptions and non-goals

KuPilot trusts the local operating system and the interactive user to control
the user account under which the process runs. A local administrator, a process
with equivalent user privileges, a compromised kernel, terminal emulator, or
filesystem can read or alter KuPilot state and is outside the protection offered
by the application. SQLite is not an encrypted vault, and KuPilot does not claim
forensic erasure from filesystem snapshots, backups, or storage media.

The following inputs and systems are not trusted to set policy:

- Model output, including prose, structured Tool selections, stream events, and
  claimed approval or execution results.
- Kubernetes object fields, Events, container output, API errors, and discovery
  responses.
- User questions when they are copied to a model request or rendered back to the
  terminal.
- Kubeconfig fields and exec credential programs until the user has selected the
  file and the runtime has applied the controls in this document.
- Model endpoint responses, redirects, error bodies, and capability claims.
- Existing SQLite bytes until schema, migration, and integrity checks succeed.

Kubernetes RBAC remains an independent control. KuPilot must work with a
least-privilege identity, but it must still enforce its own scope, Kind, Tool,
relationship, and output rules when that identity has broader permissions.

## 3. Trust boundaries and permitted flows

<!-- markdownlint-disable MD013 -->

| Boundary | Data allowed to cross | Data that must not cross |
| --- | --- | --- |
| Local credential source to Kubernetes adapter | Credential material needed by the selected client transport, for the shortest necessary lifetime | Credential values in domain values, application events, model content, TUI content, ordinary logs, or SQLite |
| KuPilot to Kubernetes API | Fixed, namespaced, read-only requests for allowlisted projections in `v0.1`; one exact approved Deployment restart in `v0.2` | Generic discovery-driven access, Secret reads, ConfigMap data reads, cross-Namespace reads, model-generated requests, or arbitrary writes |
| KuPilot to model endpoint | Consented, projected, normalized, redacted, and bounded model content plus fixed Tool schemas | Kubernetes or model credentials, raw objects, raw protocol bodies, Secret data, raw container output, SQLite contents, or an unapproved endpoint |
| Application to SQLite | Explicitly eligible, sanitized domain and audit fields through fixed repository operations | Raw transport bodies, assembled prompts, stream deltas, full Tool results, framework objects, credentials, or raw logs |
| Application to terminal | Locally styled, normalized, bounded text and typed state | Unprocessed control sequences, model-selected styling, raw external errors, or a model-created approval decision |
| Approval coordinator to `v0.2` executor | One durable, current, digest-bound, single-use Deployment restart capability | Category approvals, expired or replayed approval, arbitrary parameters, another operation, or any `v0.1` call |

<!-- markdownlint-enable MD013 -->

The model has no direct connection to Kubernetes, SQLite, the terminal, or the
future write executor. The TUI has no direct connection to Kubernetes, the
model, SQLite, or the executor. `cmd/kupilot` is the only composition root that
can make an adapter reachable.

## 4. Mandatory runtime invariants

The implementation must preserve these invariants:

1. Credentials are transport inputs only. They do not implement `String`-like
   rendering, do not enter generic maps, and are never copied into safe errors.
2. Source allowlisting and field projection happen before normalization,
   sensitive-value detection, redaction, and output bounding. Redaction cannot
   make an otherwise forbidden source eligible.
3. KuPilot has no Secret reader and reads no ConfigMap data or container
   environment values. A forbidden source fails before a Kubernetes call when
   the source is known locally.
4. Every AgentRun owns one immutable ClusterScope. The model-visible Tool schema
   contains no Context, Namespace, endpoint, credential, arbitrary Kind,
   deadline, or hard-limit field.
5. Runtime code validates a fixed Tool name, strict schema, canonical arguments,
   call budget, repetition rule, deadline, and current scope before dispatch.
   Unknown fields and text that merely resembles a Tool call are rejected.
6. Scope generation is checked before an external Tool call, after every return,
   and when Application accepts an event. Cancellation alone is insufficient.
7. External free text is data. It is never evaluated as a command, Tool schema,
   URL, terminal style, SQL fragment, approval, or policy change.
8. The configured model origin is validated before use and is frozen in the
   consented policy snapshot for a run. Cross-origin redirects are denied, and
   authentication is never forwarded to another origin.
9. Durable fields are allowlisted. The absence of a prohibited field in a schema
   does not permit serializing it into a generic text, JSON, error, or metadata
   column.
10. `v0.1` constructs no Kubernetes mutation client, approval coordinator, or
    write executor. A recommendation remains text for the user to evaluate.
11. A `v0.2` approval is default-reject, short-lived, digest-bound, scope-bound,
    target-bound, single-use, revalidated, durably audited before the write, and
    consumed after at most one external request.

## 5. Model egress pipeline

Every value considered for model content must pass the following ordered
pipeline locally:

1. Confirm that the source and field are explicitly eligible for the current
   Tool and privacy policy.
2. Project the source into a project-owned, Tool-specific DTO. Discard the raw
   source before constructing model content.
3. Decode as valid text, normalize line endings, and remove or visibly replace
   terminal, bidirectional, and other unsafe control characters.
4. Apply high-risk sensitive-value detectors. A high-confidence credential-like
   match blocks the affected field or result; lower-risk eligible values may be
   replaced with typed redaction markers.
5. Enforce per-field, per-item, per-result, and cumulative byte and item limits.
6. Serialize through the fixed neutral model contract and mark external text as
   untrusted data, distinct from system instructions and Tool definitions.
7. Confirm that the consent snapshot still matches the canonical endpoint
   origin and every enabled data category immediately before transport.

Failure at any step returns a stable safe classification and records only
allowlisted audit metadata. The original value is not sent merely to preserve
diagnostic completeness. A blocked or truncated observation becomes missing
information in the Diagnosis.

## 6. Control catalog

### C01: Credential confinement

Kubeconfig bytes, certificate and key bytes, tokens, exec credential output, the
model API key, cookies, and authentication headers remain inside the adapter
that needs them. Configuration and domain values contain only a credential
source category. Errors expose a stable class and safe operation name, never a
credential-bearing vendor error or request dump.

The `v0.1` model API key comes from the ephemeral environment or optional safe
process-input source defined by ADR-0021. The composition root copies an
environment value into an opaque credential and immediately unsets the source
variable. The key is not accepted through a value-bearing CLI argument,
configuration file, prompt, database row, or Tool argument. KuPilot does not
generate a debug dump containing process environment or transport headers.

KuPilot warns when a selected kubeconfig source is group- or world-readable on
platforms with Unix permission bits. It never changes permissions on a user's
kubeconfig automatically and never includes its contents in the warning.

### C02: Kubeconfig exec credential containment

An exec credential program can run only as part of client-go constructing the
explicitly selected kubeconfig Context. A strict configuration mode rejects
every Context that requires exec authentication before process launch. The
model, Agent, Tool, Session, and TUI cannot weaken strict mode or create another
command path.

The program is launched directly, never through a shell. The model and Tool
arguments cannot choose the executable, arguments, environment, working
directory, API version, or interactive mode. Standard output is consumed only
by the credential protocol adapter; standard error is bounded and converted to
a safe classification. Neither stream is rendered, logged, persisted, or sent
to the model. The child has a deadline and is terminated with the owning
Context. KuPilot does not claim to control network activity performed by the
user-configured external program.

The selected client-go exec API and environment behavior must satisfy ADR-0020.

### C03: Endpoint and transport confinement

KuPilot accepts one canonical model origin for the process. `https` with normal
certificate and hostname verification is the default and may be explicitly
configured for public or private endpoints. Plain `http` is accepted only for
an explicit loopback endpoint. User information, query parameters, insecure TLS
overrides, and cross-origin redirects are rejected. No endpoint value can
originate in a question, model response, Kubernetes field, resumed Session, or
Tool argument.

The user sees the endpoint host and eligible data categories before the first
transfer. Changing the origin or any eligible category invalidates consent.
Capability checks contain no cluster data or conversation content. The model API
key is attached only by the transport for the validated origin, and sensitive
headers never cross an origin boundary.

### C04: Fixed Tool authorization

The code-defined `v0.1` catalog contains exactly six read-only Tools. Each uses
a strict, versioned schema with unknown fields rejected. Runtime canonicalizes
arguments, injects ClusterScope from RunInput, atomically reserves budgets, and
dispatches through a fixed name-to-handler table. There is no shell, kubectl,
generic HTTP, generic Kubernetes, dynamic plugin, text-to-command, or
prompt-parsed fallback path.

Model-provided free text in a structured Tool selection is normalized,
screened for sensitive values, and bounded before the call becomes a canonical
runtime value. Lower-risk matches use the code-defined redaction marker. A
high-confidence match or invalid control sequence is rejected before handler or
Kubernetes I/O, and only the processed canonical selection can enter a later
model request, Application event, or durable ToolInvocation.

### C05: Scope and stale-work isolation

The complete immutable ClusterScope and process-local generation are checked at
all three gates defined by ADR-0014. A Context or Namespace switch invalidates
the old generation, cancels the run, clears ResourceRef and picker state, and
discards every late result before model transfer, persistence, Evidence
acceptance, or successful rendering. Resume cannot restore a live scope.

### C06: Kubernetes source allowlists

Direct targets are limited to Pod, Deployment, ReplicaSet, Job, and Service in
the active Namespace. EndpointSlice contributes only address-free readiness
counts through a fixed Service relationship. StatefulSet may appear only as an
unfetched owner reference already projected from a Pod. Secret, ConfigMap data,
custom resources, generic discovery, Watch, informer, raw selector, and
all-Namespace access are absent.

Every Kubernetes port is task-specific and returns a project-owned projection.
The adapter applies server request limits where available and the runtime still
enforces local item and byte limits after return.

### C07: Injection and output safety

User text, resource names, Events, container output, model output, endpoint
errors, and resumed content remain untrusted data. They cannot add Tools, widen
scope, change budgets, choose an endpoint, create Evidence, approve a write, or
produce an execution record. Structured model output is decoded with strict
limits; invalid or extra content produces a classified failure or a safe gap.
Every model-provided Diagnosis text field passes the same local normalization,
sensitive-value, and bound policy before final validation. High-confidence
sensitive output is blocked before a Diagnosis event or durable write. Raw
structured response deltas are not published as display text; the UI receives
only code-defined progress text until the validated Diagnosis is ready.

Only local code assigns terminal style. Before rendering, all external text is
valid UTF-8, bounded, and stripped of escape, C0/C1, operating-system command,
device-control, and bidirectional override sequences except for locally
generated layout characters. Unsafe characters are removed or displayed with a
plain visible replacement.

### C08: SQLite confinement

The database lives in the resolved KuPilot state directory: the documented
platform default or an explicit absolute KuPilot state-path override from typed
local configuration. A model, Session, Tool, or Kubernetes value cannot select
the path. On supported platforms the directory is owner-only, database and
sidecar files are owner-readable and owner-writable, and symlinked path
components are rejected. SQL is fixed in the SQLite adapter; values are bound
parameters, and external text cannot select a table, column, pragma, migration,
or ordering expression.

Schema mappings contain only fields allowed by the retention contract. Opening,
migration, recovery, and retention enforcement are explicit startup gates.
Corruption or an unknown schema is not silently replaced. SQLite encryption or
tamper resistance is not claimed.

A durable Diagnosis verifies each cited Evidence ID independently against the
same AgentRun. Its exact observation window is derived from all accepted
Evidence stored for that run, including Evidence not cited by a confirmed fact
or hypothesis. Its initial detail state also includes accepted Tool-result
truncation when a fixed limit produced no Evidence. Historic reads preserve the
window and derive `partial` or `expired` when retention removes detail.

### C09: Bounded execution

The hard ceilings in ADR-0016 limit AgentRun duration, model and Kubernetes
request duration, loop steps, Tool calls, repeated calls, result bytes, result
items, log reads, relationship traversal, and no-progress steps. Kubernetes
client defaults are additionally capped at QPS 5 and Burst 10. Reservation is
atomic and precedes external I/O. Configuration may tighten but cannot expand a
ceiling. Model streams, decoder buffers, event queues, and terminal rendering
also have fixed bounds.

### C10: Safe error and audit projection

Vendor and raw transport errors terminate at their adapter. A project-owned
error contains a stable class, safe operation, retryability, user-safe message,
correlation identifier, and optional allowlisted scope or ResourceRef. Runtime
decisions use the class, never substring matching. Ordinary logs and AuditEvents
use explicit fields and never serialize arbitrary objects, request or response
bodies, environment, headers, SQL rows, or stack frames containing external
data.

### C11: `v0.1` write absence

The `v0.1` composition graph contains no mutation port, mutation adapter,
approval service, or generic Kubernetes client accessible from Application,
Agent, Tool, CLI, or TUI. Tool schemas and RBAC fixtures contain read verbs only.
Tests inspect imports, wiring, exposed methods, and fake-server requests. A
model statement that an action was performed is rejected by the Diagnosis
validator.

### C12: `v0.2` approval and execution

The only admitted operation is `restart_deployment` for one exact `apps/v1`
Deployment. A proposal uses a versioned canonical encoding. Its operation digest
covers the operation, immutable ClusterScope and generation, target Kind,
Namespace, name, UID, Pod-template fingerprint, Deployment generation, fixed
canonical parameters, policy version, request identity, and expiry. Approval
expires 60 seconds after proposal creation and cannot be extended in place.

The TUI defaults to rejection and returns the request identity, digest that was
actually displayed, decision, and one-time nonce. Approval then rechecks state,
digest, nonce, time, scope, policy, and target. It re-reads the exact Deployment
and requires the bound UID, template fingerprint, and canonical parameters. A
status-only resource-version change does not invalidate approval by itself; the
executor uses the fresh resource version as its optimistic concurrency
precondition. The consumed approval state and pre-operation audit record commit
atomically before execution. A final scope and state check precedes one external
request.

Expiry, cancellation, restart, scope change, target change, digest mismatch,
nonce mismatch, duplicate decision, audit failure, or uncertain prior attempt
prevents execution. After a process interruption KuPilot never retries the
write automatically. Request acceptance and bounded post-operation verification
are different outcomes and are recorded separately.

## 7. Threat-to-control-to-test mapping

All tests in this table are deterministic and use local fakes, fixtures, fake
clocks, or explicit concurrency barriers. A live cluster or live model is not a
security test oracle.

<!-- markdownlint-disable MD013 -->

| ID | Threat | Required runtime controls | Deterministic proof |
| --- | --- | --- | --- |
| T01 | Kubeconfig, token, certificate, key, or exec credential output reaches the model, terminal, logs, errors, or SQLite, or unsafe kubeconfig permissions go unnoticed | C01, C02, C08, C10; credentials are adapter-local and excluded by sink schemas; unsafe Unix source permissions produce a content-free warning without automatic changes | Inject a distinct synthetic canary at every credential source; exercise success and every adapter error; assert exact absence from captured model requests, events, rendered frames, logs, repository queries, and safe errors; table-test owner/group/world modes and assert warning text has no content or path and mode bits never change |
| T02 | The model API key is persisted, rendered, inherited by a child, disclosed by an error, or sent to a different endpoint | C01, C02, C03, C10; ephemeral source, immediate environment removal, same-origin transport | Use environment and safe-input fakes plus a redirect and exec child; assert the authentication value appears only in the outbound header to the configured origin, never in child or redirect traffic or any captured sink; scan formatted error paths with a synthetic canary |
| T03 | Secret data or another high-risk value is read from Kubernetes or smuggled through an Event, resource field, question, container output, Tool selection, or Diagnosis draft | C04 and C07 model-text processing plus C06 source denial and the ordered egress pipeline | `TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler`, `TestDiagnosisValidatorSanitizesEveryModelFreeTextField`, `TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence`, `TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction`, and the full sink integration test prove typed redaction or blocking before model, Tool/Kubernetes, TUI, Evidence, audit/log, and persistence sinks; denial paths assert zero forbidden calls |
| T04 | Prompt or Tool-result injection asks the Agent to ignore policy, reveal data, change endpoint, cross scope, write, or approve itself | C04, C05, C07, C11, C12; authority exists only in runtime state and fixed dispatch | Feed injection fixtures through user, Event, container-output, resource-name, historic-message, and model channels; assert unchanged catalog, endpoint, scope, budgets, approval state, and zero forbidden adapter calls |
| T05 | A structured Tool selection supplies unknown fields, scope, arbitrary Kind, raw selector, deadline, or larger limit | C04 strict decoding, internal scope binding, and C09 ceilings | Table-test missing, duplicate, unknown, wrong-type, overlong, and boundary fields; fuzz decoding; assert denial occurs before reservation or external I/O and canonical arguments contain only schema fields |
| T06 | A malicious endpoint, URL confusion, redirect, or error body exfiltrates the API key or cluster data | C03 canonical origin validation, normal TLS, loopback-only HTTP, cross-origin redirect denial, consent binding, and safe errors | Table-test public/private HTTPS, loopback HTTP, non-loopback HTTP, user information, query, and redirect forms; prove no credential forwarding; change origin after consent and assert zero model-content requests until renewed consent |
| T07 | A Context or Namespace switch allows a stale result into the model, database, TUI, Evidence, or approval state | C05 three-gate generation protocol and approval invalidation | Move generation with barriers before invocation and during I/O; assert respectively zero external calls and zero accepted sinks; deliver late and duplicate events and assert terminal state is unchanged |
| T08 | Broad RBAC permits reads outside KuPilot's Kind, relationship, Namespace, or field policy | C06 task-specific ports, code allowlists, projection, and local limits | Run every Tool against a request-recording fake API; compare exact verbs, resources, Namespace, limits, and projected fields to golden allowlists; assert adversarial owner graphs stop at fixed edges and hops |
| T09 | Model output bypasses structured Tool calling through prose, malformed stream fragments, duplicate calls, or an invented Tool name | C04 strict structured events and no text fallback; C09 repetition and loop limits | Replay chunk-boundary permutations, malformed events, duplicate identifiers, invented names, and prose that resembles a call; assert no unintended handler call and one classified terminal outcome |
| T10 | SQLite leaks excluded data, accepts SQL injection, opens an unsafe path, or silently loses integrity | C08 fixed path, permissions, schema allowlist, bound SQL, migrations, integrity gates, and the all-accepted-Evidence Diagnosis window invariant | Database and sidecar canary scans, path and mode tests, `TestRepositorySourcesKeepExplicitSQLBoundary`, corruption tests, `TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow`, truncation with and without Evidence, cross-run rejection, retention-state tests, and the full sink integration test verify excluded-data absence and exact durable Evidence semantics |
| T11 | Retained data outlives its contract, minimal-persistence becomes resumable, deletion is partial, or cleanup failure is hidden | C08 plus the Data Retention Contract | Use a fake clock at cutoff boundaries; assert complete transactional cascades, no content rows in minimal mode, no resume candidate, honest deletion failure, and a startup/run gate when mandatory pruning fails |
| T12 | Kubernetes, model, or user-controlled text executes terminal control sequences, spoofs approval, or hides scope | C07 local-only styling and typed approval state | Golden-test escape, control, invalid UTF-8, bidirectional, wide, combining, and oversized input from every external source; assert rendered bytes contain no forbidden sequence and approval fields cannot originate in text |
| T13 | Unbounded model streams, Kubernetes results, logs, recursion, retries, or event queues exhaust memory, time, or model budget | C09 atomic hard ceilings and owned cancellation | Test each exact boundary and one-over value with fake clocks and counters; fuzz stream chunks; assert bounded allocations/queues, no call after exhaustion, one terminal result, and an explicit gap |
| T14 | Raw vendor errors or logs disclose credentials, endpoint bodies, SQL, local paths, or cluster data | C01 and C10 safe classification and allowlisted logging | Wrap synthetic canaries at every adapter error boundary, including joined and formatted errors; enumerate TUI, model, audit, and ordinary-log outputs and assert only stable safe fields remain |
| T15 | A hidden Kubernetes write path exists in `v0.1` through a Tool, TUI shortcut, generic client, dependency, or model claim | C11 absent composition and read-only contracts | Static import and method checks plus a request-recording fake assert only admitted read verbs; exercise every command and Tool; validate that claimed execution prose is not accepted as fact |
| T16 | A future approval is replayed, broadened, approved after expiry or scope change, or executed against a changed Deployment | C12 versioned digest, 60-second TTL, nonce, one-time state, re-read, fingerprint, and concurrency precondition | With a fake clock and store, mutate each bound field individually, replay decisions, cross restart, cross generation, and race target changes; assert external write count zero for every mismatch |
| T17 | A database failure bypasses approval audit or causes an automatic duplicate write | C08 and C12 durable pre-operation gate, consumed state, no automatic retry | Fail each transaction boundary and interrupt before and after the fake external request; assert no request before durable consumed/audit state, at most one request, visible unknown outcome where needed, and no startup replay |
| T18 | A kubeconfig exec program is selected or influenced by the model, launched through a shell, leaks output, hangs, inherits the model key, or bypasses strict deny | C02 fixed selected config, direct launch, cleaned environment, bounded streams, deadline, and strict mode | Use a fake executable and launcher recorder; vary model and Tool content, arguments, environment, output, error, cancellation, timeout, and strict mode; assert exact argv ownership, no shell or key inheritance, no sink leakage, and zero launches under strict deny |
| T19 | A model recommendation or TUI event bypasses Application and invokes the future executor | C11/C12 composition isolation and typed commands | Contract and import tests prove TUI and Agent see no executor; send forged UI/model events and assert rejection before approval state or external I/O changes |
| T20 | Restart executes a broader patch, another write, multiple requests, or reports request acceptance as verified success | C12 one semantic operation, fixed parameters, one request, and separate verification | Compare the fake Kubernetes request to the fixed operation contract, reject every extra field or Kind, force accepted/timeout/verification variants, and assert the UI and audit keep them distinct |

<!-- markdownlint-enable MD013 -->

## 8. Test-fixture and logging rules

Security fixtures use obviously synthetic resources and a different generated
canary for each sensitive source. The canary value is created by the test and is
not copied into public documentation or examples. A sink inventory test must
cover model request capture, Kubernetes request capture, Application events,
TUI frames, safe errors, ordinary logs, AuditEvents, and every persistent table.

Tests must assert both the expected result and forbidden side-effect counts.
Timing-sensitive tests use fake clocks and channels or barriers, never sleeps.
Fuzz tests retain deterministic seeds in CI. Live model behavior, prompt
obedience, live RBAC, and a human visual check are supplementary evaluation only
and cannot replace these tests.

Public examples, fixtures committed for documentation, issue templates, and
screenshots must not contain kubeconfig material, credentials, Secret objects or
data, raw cluster output, raw container output, or model API keys. TUI mode writes
a bounded allowlisted local info log by default and lets the user disable it; it
never records bodies or Tool arguments. A future diagnostic bundle requires a
separate Accepted ADR and threat review.

## 9. Failure policy

- Validation, policy, scope, consent, endpoint, credential, and budget failures
  stop the affected external call.
- A failure to durably begin an AgentRun stops before the first model request.
- A later persistence failure in a read-only run may allow the in-memory
  Diagnosis to finish, but it sets `persistence_degraded`, is visible to the
  user, disables resume claims, and prevents another run until storage is
  healthy.
- A mandatory retention, migration, or startup recovery failure prevents new
  durable work and is never silently treated as successful cleanup.
- Any failure before the `v0.2` pre-operation audit commit prevents execution.
  A failure after an external write may produce an explicit unknown or
  unverified outcome; it never causes an automatic retry.
- Missing permissions, blocked sensitive data, unsupported capabilities, and
  hard-limit truncation become explicit gaps. They never trigger broader access.

## 10. Review and revisit triggers

This threat model must be reviewed before any of the following:

- Adding a Tool, Kind, relationship edge, model provider, endpoint mode, data
  category, durable field, listener, telemetry path, plugin, or write operation.
- Changing the scope model, supporting concurrent AgentRuns or scopes, or adding
  a process boundary.
- Selecting dependency versions or concrete APIs whose behavior weakens an
  invariant in this document.
- Discovering a credential, cross-scope, terminal, persistence, approval, or
  audit bypass in implementation or evaluation.

## 11. Operator controls and current limitations

Operators must preserve the following independent controls for the reachable
`v0.1` composition:

- Bind the rules in [Least-Privilege RBAC](rbac/README.md) to the selected
  kubeconfig identity. Do not grant `cluster-admin`, wildcard permissions, a
  namespaced read ClusterRole through a ClusterRoleBinding, Secret access,
  Watch, or a write verb for KuPilot.
- Keep configuration, state, database sidecars, and local logs in the resolved
  owner-only non-symlink paths. KuPilot does not encrypt these files or protect
  them from another process with the same local-user authority.
- Supply the model API key only through `KUPILOT_MODEL_API_KEY` in the KuPilot
  process environment. Do not place it in YAML, argv, history, user questions,
  issue reports, or diagnostic fixtures.
- Review the exact canonical model origin and enabled categories before
  accepting consent. Container output is disabled by default; enabling it
  invalidates prior consent and does not weaken local projection, sensitive-
  value, or byte limits.
- Set `kubernetes.exec_credentials: deny` when the selected kubeconfig must not
  launch an exec credential program. Allowed exec programs run with the local
  user's authority and are not sandboxed by KuPilot.
- Treat a Diagnosis as a bounded snapshot, not a guaranteed root cause or proof
  of current cluster state. Every `v0.1` recommendation is unexecuted.

The current public CLI/TUI always creates standard-persistence Sessions and
does not expose per-Session deletion, clear-history, delete-all, or a
minimal-persistence selector. The repository enforces the underlying retention,
minimal-mode, and transactional deletion contracts for compatible callers, but
operators using the current binary must follow the exact stopped-process file
cleanup boundary in
[Privacy and Local Data](user-guide/privacy-and-local-data.md). This limitation
must not be hidden behind a claim of data minimization or secure erasure.

KuPilot has no product telemetry, analytics, remote crash reporting, update
checker, account service, or KuPilot-operated control plane. A different
outbound destination, cross-Namespace read, Secret read, terminal-control
effect, or Kubernetes write is a security event and should be reported privately
under the [Security Policy](../SECURITY.md).

## References

- [Architecture](architecture.md)
- [Data Retention Contract](data-retention.md)
- [Privacy Overview](privacy-overview.md)
- [Least-Privilege RBAC](rbac/README.md)
- [Troubleshooting](troubleshooting.md)
- [Product Contract](product.md)
- [Scope](scope.md)
- [ADR-0012: Require Digest-Bound Approval for Writes](adr/0012-require-digest-bound-write-approval.md)
- [ADR-0014: Isolate Runs with ClusterScope Generation](adr/0014-cluster-scope-generation-isolation.md)
- [ADR-0016: Freeze Runtime Budgets](adr/0016-freeze-runtime-budgets.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](adr/0020-contain-kubeconfig-exec-credentials.md)
- [ADR-0021: Use Ephemeral Model API Key Sources](adr/0021-use-ephemeral-model-api-key-sources.md)
- [ADR-0025: Enforce Data Retention and User Deletion](adr/0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0027: Use Stable Safe Error Classes](adr/0027-use-stable-safe-error-classes.md)
- [ADR-0029: Limit `v0.2` to Deployment Restart](adr/0029-limit-v0.2-to-deployment-restart.md)
