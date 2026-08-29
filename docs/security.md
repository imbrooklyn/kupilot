# KuPilot Security Threat Model

- Status: Accepted security baseline for `v0.1`, the admitted `v0.2` write
  boundary, and the `v0.3` Session lifecycle, export, and assurance boundary
- Date: 2026-08-15

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
- Explicitly exported local Session summaries and their filesystem targets.
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
| Application to summary filesystem adapter | One explicit target and the final bounded `kupilot.export-summary.v1` Markdown bytes | Repository entities, raw Tool or log data, model traffic, credentials, approval authority, or target-path details in audit and errors |
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
12. A Session summary export uses only its fixed versioned allowlist, two-pass
    redaction and bounds, content-free audit, owner-only same-directory temporary
    file, and atomic no-replace publication. It never calls the model, cluster,
    Tool, approval path, or executor.

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

Kubeconfig bytes, certificate and key bytes, tokens, exec credential output,
the model API key, cookies, and authentication headers remain inside the narrow
extractor, opaque wrapper, writer, or transport that needs them. Ordinary typed
configuration and Domain values contain only a fixed runtime source marker.
Errors expose a stable class and safe operation name, never a credential-bearing
vendor error or request dump.

The model API key may come from masked TUI input, optional plaintext
`model.api_key`, or the one-shot `KUPILOT_MODEL_API_KEY` override defined by
ADR-0035. The environment entry is copied into an opaque credential and
immediately unset; the file field is removed before Viper or ordinary typed
decoding. The key is not accepted through a value-bearing CLI argument, prompt,
database row, Tool argument, Application event, rendered TUI, history, log, or
audit. The sole admitted durable key location is the fixed Home configuration
after the user's disclosed `save` choice. KuPilot does not generate a debug dump
containing configuration bytes, process environment, or transport headers.

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

The optional `reasoning_effort: none` request field comes only from typed user
configuration. It is allowlisted exactly, is not inferred from the model name
or endpoint error text, and cannot change origin, consent, Tool authority,
budgets, or retry policy.

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

The database has the fixed `state/kupilot.db` path below the process-frozen
KuPilot Home. Only `KUPILOT_HOME` can select that root; a model, YAML field,
Session, Tool, Kubernetes value, working directory, or repository cannot select
the database path. On supported platforms newly created state, database, and
sidecar paths use owner-only modes. Wider existing user-managed modes are
preserved, while symlinked managed descendants and non-regular database or
sidecar files are rejected. SQL is fixed in the SQLite adapter; values are
bound parameters, and external text cannot select a table, column, pragma,
migration, or ordering expression.

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
decisions use the class, never substring matching. AuditEvents and default
operational logs use explicit fields and never serialize arbitrary objects,
request or response bodies, environment, headers, SQL rows, or stack frames
containing external data. ADR-0036 additionally admits a bounded safe local
model-failure projection: observed HTTP status, fixed cause category, and a
sink-generated chain of KuPilot function names without files, lines, arguments,
values, dependency frames, or raw causes.

When the user explicitly sets `logging.sensitive_diagnostics: true`, the same
failure event may also contain the configured endpoint and model, a 16 KiB
credential-redacted error chain, the first 4 KiB of a failed provider response,
and a 64 KiB current-goroutine Go stack with paths and lines. This copy never
enters safe errors, runtime decisions, AuditEvents, SQLite, TUI, model content,
or exports. KuPilot does not deliberately attach Authorization, the model
credential, headers, requests, successful responses, streams, prompts, Tool
data, Kubernetes content, environment, or SQL. The untrusted provider error or
error chain may still echo user or cluster content after fixed sensitive-value
handling. Startup warns while the mode is enabled.

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

After an accepted PATCH, Application coordinates an exact Deployment observer
with a 90-second default window, a two-second minimum polling interval, and a
45-observation ceiling. Configuration can only shorten the window or slow the
poll. Success requires the target `observedGeneration` and both updated and
available replicas to reach the post-PATCH target. Replaced or subsequently
changed targets and fixed controller failure conditions are failures; deadline,
cancellation, stale scope, and read unavailability remain distinct outcomes.
None of them grants or attempts another write.

Every changed progress transition and terminal verification result produces a
bounded structured audit record. Post-attempt audit makes at most three attempts
for one immutable event ID. Exhaustion stops observation and raises a
high-priority UI result; it never retries the PATCH. Raw Deployment conditions,
API response bodies, resource versions, patches, and vendor errors are excluded
from UI and audit sinks.

### C13: Versioned local Session-summary export

Export is available only for the current resumable standard-persistence Session
through `/privacy`. The sole composer accepts one explicit target, and a
non-editable category preview defaults to cancellation unless the user presses
`Y`. Application serializes export against deletion and denies it while a run is
starting or active. A restart restores no target or confirmation.

SQLite returns one consistent, bounded projection rather than an entity
serialization. The only eligible fields are safe Session display metadata,
committed user and final assistant text, the four structured Diagnosis
collections, and referenced Evidence summaries or expired markers. Source
allowlisting precedes per-field redaction and bounds; Markdown structure is
code-defined and escaped; the complete bytes pass the redactor and aggregate cap
again. Raw Tool inputs and results, raw logs and Events, Kubernetes objects,
prompts, model traffic, credentials, Secrets, kubeconfig values and paths,
approval authority, internal fingerprints, and arbitrary errors are excluded.

A fixed `session_export_requested` AuditEvent is durably appended before file
I/O. It records Session identity, event time, outcome, operation, and schema
version, but no content or target path. The filesystem adapter requires a clean
absolute `.md` target under an existing owner-only directory. It rejects
symlink path components, non-regular or existing targets, and non-sticky
ancestor directories writable by group or others on supported Unix platforms.
It uses a `0600` same-directory temporary file plus atomic no-replace
publication. Every failure removes the temporary file and never publishes
partial bytes. The output remains unencrypted user-controlled data and is
outside later Session deletion and forensic-erasure guarantees.

### C14: Unified Home, interactive model setup, and cache maintenance

`KUPILOT_HOME`, or `$HOME/.kupilot` when absent, is resolved and frozen before
ordinary startup. Automatic writes are limited to fixed configuration, state,
cache, and log descendants. Explicit configuration overrides remain read-only;
the separately confirmed summary export is the only admitted write outside
Home. New Unix paths receive `0700` directory or `0600` file modes, while
existing user-managed modes are neither rejected solely for being wider nor
silently changed. Managed symlinks below canonical Home, wrong file types, and
replacement races fail the affected operation safely.

The TUI masks credential input and excludes it from history and transcript.
Application serializes model replacement, cancels and joins active run work,
builds before swapping, retains the old runtime on construction or publication
failure, and invalidates consent on an origin change. The secret wrapper is
destroyed on every result. A local save uses a same-Home temporary file,
synchronization, target revalidation, and atomic rename; it creates a new file
as `0600` and preserves an existing user-managed mode.

`kupilot cache clear` resolves only Home, short-circuits ordinary composition,
and removes entries through non-following directory handles on supported Unix
platforms. A missing cache is not created. Home, configuration, state, logs,
explicit exports, and link targets remain outside the deletion set; cancellation
or partial failure is reported without a forensic-erasure claim.

## 7. Threat-to-control-to-test mapping

All tests in this table are deterministic and use local fakes, fixtures, fake
clocks, or explicit concurrency barriers. A live cluster or live model is not a
security test oracle.

<!-- markdownlint-disable MD013 -->

| ID | Threat | Required runtime controls | Deterministic proof |
| --- | --- | --- | --- |
| T01 | Kubeconfig, token, certificate, key, or exec credential output reaches the model, terminal, logs, errors, or SQLite, or unsafe kubeconfig permissions go unnoticed | C01, C02, C08, C10; credentials are adapter-local and excluded by sink schemas; unsafe Unix source permissions produce a content-free warning without automatic changes | The credential-source matrix injects distinct kubeconfig, token, certificate, key, exec-output, and exec-error canaries and asserts exact permitted transport, child, and server actions plus project-boundary exclusion; the downstream sink matrix covers model, terminal, log, error, and SQLite projections; permission tests assert content-free, path-free warnings and unchanged owner/group/world mode bits |
| T02 | The model API key is persisted without the explicit local-save choice, rendered, retained after a failed setup, inherited by a child, disclosed by an error, or sent to a different endpoint | C01, C02, C03, C10, and C14; sensitive extraction, immediate environment removal, masked input, opaque one-shot wrappers, fixed-Home publication, and same-origin transport | Use distinct file, environment, and TUI canaries plus a redirect and exec child; assert the value appears only in the explicitly selected Home file and same-origin Authorization header, never in ordinary Config, Viper state, UI render/history, errors, logs, audit, SQLite, child or redirect traffic; assert failed and process-only setup publish no credential file |
| T03 | Secret data or another high-risk value is read from Kubernetes or smuggled through an Event, resource field, question, container output, Tool selection, or Diagnosis draft | C04 and C07 model-text processing plus C06 source denial and the ordered egress pipeline | `TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler`, `TestDiagnosisValidatorSanitizesEveryModelFreeTextField`, `TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence`, `TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction`, and the full sink integration test prove typed redaction or blocking before model, Tool/Kubernetes, TUI, Evidence, audit/log, and persistence sinks; denial paths assert zero forbidden calls |
| T04 | Prompt or Tool-result injection asks the Agent to ignore policy, reveal data, change endpoint, cross scope, write, or approve itself | C04, C05, C07, C11, C12; authority exists only in runtime state and fixed dispatch | Feed injection fixtures through user, Event, container-output, resource-name, historic-message, and model channels; assert unchanged catalog, endpoint, scope, budgets, approval state, and zero forbidden adapter calls |
| T05 | A structured Tool selection supplies unknown fields, scope, arbitrary Kind, raw selector, deadline, or larger limit | C04 strict decoding, internal scope binding, and C09 ceilings | Table-test missing, duplicate, unknown, wrong-type, overlong, and boundary fields; fuzz decoding; assert denial occurs before reservation or external I/O and canonical arguments contain only schema fields |
| T06 | A malicious endpoint, URL confusion, redirect, or error body exfiltrates the API key or cluster data, or endpoint text changes request policy | C03 canonical origin validation, normal TLS, loopback-only HTTP, cross-origin redirect denial, typed reasoning configuration, consent binding, and safe errors | Table-test public/private HTTPS, loopback HTTP, non-loopback HTTP, user information, query, redirect, and optional reasoning-field forms; prove no credential forwarding or error-driven field change; change origin after consent and assert zero model-content requests until renewed consent |
| T07 | A Context or Namespace switch allows a stale result into the model, database, TUI, Evidence, or approval state | C05 three-gate generation protocol and approval invalidation | Move generation with barriers before invocation and during I/O; assert respectively zero external calls and zero accepted sinks; deliver late and duplicate events and assert terminal state is unchanged |
| T08 | Broad RBAC permits reads outside KuPilot's Kind, relationship, Namespace, or field policy | C06 task-specific ports, code allowlists, projection, and local limits | Run every Tool against a request-recording fake API; compare exact verbs, resources, Namespace, limits, and projected fields to golden allowlists; assert adversarial owner graphs stop at fixed edges and hops |
| T09 | Model output bypasses structured Tool calling through prose, malformed stream fragments, duplicate calls, or an invented Tool name | C04 strict structured events and no text fallback; C09 repetition and loop limits | Replay chunk-boundary permutations, malformed events, duplicate identifiers, invented names, and prose that resembles a call; assert no unintended handler call and one classified terminal outcome |
| T10 | SQLite leaks excluded data, accepts SQL injection, opens an unsafe path, or silently loses integrity | C08 fixed path, permissions, schema allowlist, bound SQL, migrations, integrity gates, and the all-accepted-Evidence Diagnosis window invariant | Database and sidecar canary scans, path and mode tests, `TestRepositorySourcesKeepExplicitSQLBoundary`, corruption tests, `TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow`, truncation with and without Evidence, cross-run rejection, retention-state tests, and the full sink integration test verify excluded-data absence and exact durable Evidence semantics |
| T11 | Retained data outlives its contract, minimal-persistence becomes resumable, deletion is partial, an approval survives deletion, or cleanup failure is hidden | C08, C12, and the Data Retention Contract | Use a fake clock at cutoff boundaries; assert one-way retention updates, complete transactional cascades, no content rows in minimal mode, no resume candidate, target-bound confirmation, active-run cancellation, pending/approved invalidation, zero executor calls on denial, honest deletion failure, and a startup/run gate when mandatory pruning fails |
| T12 | Kubernetes, model, or user-controlled text executes terminal control sequences, spoofs approval, or hides scope | C07 local-only styling and typed approval state | Golden-test escape, control, invalid UTF-8, bidirectional, wide, combining, and oversized input from every external source; assert rendered bytes contain no forbidden sequence and approval fields cannot originate in text |
| T13 | Unbounded model streams, Kubernetes results, logs, recursion, retries, or event queues exhaust memory, time, or model budget | C09 atomic hard ceilings and owned cancellation | Test each exact boundary and one-over value with fake clocks and counters; fuzz stream chunks; assert bounded allocations/queues, no call after exhaustion, one terminal result, and an explicit gap |
| T14 | Raw vendor errors or logs disclose credentials, endpoint bodies, SQL, local paths, or cluster data outside the explicit sensitive model-failure mode | C01 and C10 safe classification, default allowlisted logging, and bounded opt-in diagnostics | `TestSecurityAssuranceSafeErrorSourceSinkMatrix`, `TestSecurityAssuranceSafeErrorCancellationIdentity`, `TestSecurityAssuranceShutdownAggregationKeepsOnlySafeErrors`, `TestModelRequestErrorMappingUsesObservedHTTPStatusWithoutRetainingRawCause`, `TestFileLoggerWritesBoundedSafeModelFailureDiagnostics`, `TestFileLoggerWritesOnlyExplicitOptInSensitiveModelDiagnostics`, and model credential-redaction tests cover direct, nested, wrapped, joined, and formatted adapter failures; default sinks exclude every canary, while sensitive mode admits only documented bounded fields and removes the exact credential |
| T15 | A hidden Kubernetes write path exists in `v0.1` through a Tool, TUI shortcut, generic client, dependency, or model claim | C11 absent composition and read-only contracts | Static import and method checks plus a request-recording fake assert only admitted read verbs; exercise every command and Tool; validate that claimed execution prose is not accepted as fact |
| T16 | A future approval is replayed, broadened, approved after expiry or scope change, or executed against a changed Deployment | C12 versioned digest, 60-second TTL, nonce, one-time state, re-read, fingerprint, and concurrency precondition | With a fake clock and store, mutate each bound field individually, replay decisions, cross restart, cross generation, and race target changes; assert external write count zero for every mismatch |
| T17 | A database failure bypasses approval audit or causes an automatic duplicate write | C08 and C12 durable pre-operation gate, consumed state, no automatic retry | Fail each transaction boundary and interrupt before and after the fake external request; assert no request before durable consumed/audit state, at most one request, visible unknown outcome where needed, and no startup replay |
| T18 | A kubeconfig exec program is selected or influenced by the model, launched through a shell, leaks output, hangs, inherits the model key, or bypasses strict deny | C02 fixed selected config, direct launch, cleaned environment, bounded streams, deadline, and strict mode | Use a fake executable and launcher recorder; vary model and Tool content, arguments, environment, output, error, cancellation, timeout, and strict mode; assert exact argv ownership, no shell or key inheritance, no sink leakage, and zero launches under strict deny |
| T19 | A model recommendation or TUI event bypasses Application and invokes the future executor | C11/C12 composition isolation and typed commands | Contract and import tests prove TUI and Agent see no executor; send forged UI/model events and assert rejection before approval state or external I/O changes |
| T20 | Restart executes a broader patch, another write, multiple requests, or reports request acceptance as verified success | C12 one semantic operation, fixed parameters, one request, and separate verification | Compare the fake Kubernetes request to the fixed operation contract; lock the 90-second/two-second/45-observation policy; force reject, expiry, change, forbidden, conflict, accepted, progress, success, failure, timeout, cancellation, stale scope, restart recovery, and result-audit failure; assert exact write counts and distinct bounded UI/audit states |
| T21 | Summary export leaks a prohibited source or target path, overwrites a file, follows a symlink, publishes partial bytes, races deletion, or replays after restart | C08, C10, and C13 versioned projection, two-pass guard, content-free audit, serialized operation, and atomic no-replace publication | Use a temporary database and directory, fake clock, barriers, and distinct raw, credential, path, and approval canaries; assert prohibited byte occurrence is zero, owner-only mode, symlink and unsafe-permission rejection, no overwrite, temporary cleanup, zero filesystem writes on pre-audit denial, stable concurrent deletion, and no restart replay |
| T22 | Automatic startup or interactive save writes outside the selected Home, overwrites an explicit configuration source, changes an existing user-managed mode, follows a managed symlink, or leaves a partial credential file | C14 frozen fixed descendants, read-only external configuration, create-only mode policy, revalidation, and atomic publication | Test default and overridden Home, canonical root links, every fixed descendant, external config selection, new and wider existing modes, cancellation, wrong types, managed links, target replacement, write failure, temporary cleanup, and distinct path/credential canaries |
| T23 | Cache clearing initializes ordinary services, deletes Home or non-cache data, follows a link, escapes through a replacement race, creates a missing cache, or reports partial work as complete | C14 short-circuit routing and descriptor-relative non-following deletion | Populate nested cache entries plus distinct configuration, database, log, export, and external-link canaries; test absent cache, cancellation after partial work, root and entry links, non-directory failure, type replacement, and exact initialization counters; assert only cache entries are removed |

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
data, raw cluster output, raw container output, or model API keys. TUI mode
writes a bounded allowlisted local info log by default and lets the user disable
it. The fixed `model_request` event records only the ADR-0036 safe failure
projection by default, including a bounded project-symbol call chain without
files, lines, arguments, values, external frames, or raw causes. The explicit
ADR-0036 sensitive mode adds only its bounded failure fields and never Tool
arguments or request content as direct sources. Its untrusted failure fields may
echo operational content after sensitive-value handling, but must never retain
the exact model credential. A future diagnostic bundle requires a separate
Accepted ADR and threat review.

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
- A summary snapshot, projection, pre-export audit, target validation, or file
  publication failure reports no export success. Pre-audit failure performs zero
  filesystem writes, and no failure exposes partial output bytes.
- Any failure before the `v0.2` pre-operation audit commit prevents execution.
  A failure after an external write may produce an explicit unknown or
  unverified outcome; it never causes an automatic retry. Post-attempt audit
  uses at most three idempotent attempts for the same record. If all fail,
  verification stops and the TUI displays a high-priority audit error.
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

Operators must preserve the following independent controls for the current
read-only composition:

- Bind the rules in [Least-Privilege RBAC](rbac/README.md) to the selected
  kubeconfig identity. Do not grant `cluster-admin`, wildcard permissions, a
  namespaced read ClusterRole through a ClusterRoleBinding, Secret access,
  Watch, or a write verb for KuPilot.
- Select one absolute normalized `KUPILOT_HOME` when the default
  `$HOME/.kupilot` is unsuitable. Review any wider existing Home or
  configuration permissions. KuPilot does not encrypt these files or protect
  them from another process with the same local-user authority.
- Supply the model API key through masked setup, the one-shot environment
  override, or the optional local plaintext configuration field. Use `save`
  only after accepting the not-encrypted disclosure. Never place the key in
  argv, Session history, user questions, issue reports, repository examples, or
  diagnostic fixtures.
- Review the exact canonical model origin and enabled categories before
  accepting consent. Container output is disabled by default; enabling it
  invalidates prior consent and does not weaken local projection, sensitive-
  value, or byte limits.
- Set `kubernetes.exec_credentials: deny` when the selected kubeconfig must not
  launch an exec credential program. Allowed exec programs run with the local
  user's authority and are not sandboxed by KuPilot.
- Treat a Diagnosis as a bounded snapshot, not a guaranteed root cause or proof
  of current cluster state. Every recommendation in the current composed binary
  is unexecuted.

For a `v0.2` composition, add only the exact namespaced Deployment `get` and
`patch` rule in [Least-Privilege RBAC](rbac/README.md) for approved targets.
Do not replace it with wildcard write permissions, `update`, `delete`, Watch,
or a ClusterRoleBinding. A rollout timeout or unavailable verification requires
operator review; it is not a reason to repeat the PATCH automatically.

The existing `/privacy` dialog displays persistence mode, effective retention,
and minimal mode's non-resumable effect. It can only tighten operational-detail
retention and starts a new Session when persistence mode changes. It deletes the
current Session only after a second explicit confirmation; the resume picker can
confirm deletion of its exact selected historical Session. TUI commands cross
only the Application boundary. Approval invalidation and run cancellation occur
before the run is quiescent and before the SQLite adapter's single Session-graph
transaction. A consuming approval, cancellation, or persistence failure denies
deletion without an executor call or partial-success claim.

The same dialog requires a separate `Y` confirmation for `H` clear-history and
`X` delete-all-local-state. Both operations cancel and await current run work
and durably close every pending or approved-not-executed approval before
deletion; any consuming approval denies the request with zero executor calls.
Clear-history uses a bounded transaction and states that settings and valid
consent remain. Delete-all validates the exact state directory, database, and
known sidecars before closing the database; it follows no symlink and removes
no directory or unrelated file. Preflight denial leaves storage open. A failure
after close is reported as incomplete, and KuPilot exits after acknowledgement
without creating replacement state.

For a current standard-persistence Session, the same `/privacy` flow may use the
sole composer to collect an explicit Markdown target and then preview and
confirm the fixed redacted summary categories. Minimal Sessions do not offer
export. The target is never logged or placed in audit, existing files are not
overwritten, and the resulting local copy is not encrypted or automatically
removed with its source Session.

There is no separate Session-management page or second composer. Operational
logs and exported summaries remain outside database-state deletion; their exact
manual cleanup boundary is documented in [Privacy and Local
Data](user-guide/privacy-and-local-data.md). Logical deletion,
ordinary `DELETE`, file removal, checkpointing, and `VACUUM` must not be described
as forensic erasure. SQLite free pages, WAL, filesystem journals, backups,
snapshots, swap, and storage media remain outside that guarantee.

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
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](adr/0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0036: Record Bounded Model Failure Diagnostics](adr/0036-record-bounded-safe-model-failure-diagnostics.md)
- [ADR-0025: Enforce Data Retention and User Deletion](adr/0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0027: Use Stable Safe Error Classes](adr/0027-use-stable-safe-error-classes.md)
- [ADR-0029: Limit `v0.2` to Deployment Restart](adr/0029-limit-v0.2-to-deployment-restart.md)
- [ADR-0034: Export Only Versioned Redacted Session Summaries](adr/0034-export-only-versioned-redacted-session-summaries.md)
