# KuPilot Repository Instructions

These instructions apply to the entire repository. They do not override any
higher-level instruction. This file MUST remain the only committed Agent or
Codex instruction file in the repository: MUST NOT add nested `AGENTS.md`
files, Codex-specific contribution guides, prompt collections, or local
workflow notes. General contributor guidance belongs in the Phase 15
`CONTRIBUTING.md`, not in an Agent-specific guide.

## Normative baseline

Before changing behavior, MUST read the relevant parts of the following public
baseline and every relevant ADR whose status is Accepted:

- [Product Contract](docs/product.md) and [Version Scope](docs/scope.md)
- [Architecture](docs/architecture.md) and
  [ADR-0013: Layered Boundaries](docs/adr/0013-layered-architecture-and-consumer-owned-ports.md)
- [Security Threat Model](docs/security.md),
  [Privacy Overview](docs/privacy-overview.md), and
  [Data Retention Contract](docs/data-retention.md)
- [ADR-0009: Fixed Structured Tools](docs/adr/0009-use-fixed-structured-tools.md)
  and [ADR-0011: Read-Only v0.1](docs/adr/0011-keep-v0.1-strictly-read-only.md)
- [ADR-0023: Agent-Supervision TUI](docs/adr/0023-use-a-single-screen-agent-supervision-tui.md)
  and [ADR-0031: Explicit Session Resume](docs/adr/0031-require-explicit-cli-session-resume.md)
- [ADR-0012: Digest-Bound Write Approval](docs/adr/0012-require-digest-bound-write-approval.md)
  and [ADR-0029: Deployment Restart Only](docs/adr/0029-limit-v0.2-to-deployment-restart.md)
- [ADR-0032: English Public Material](docs/adr/0032-use-english-for-public-project-material.md)

Accepted public decisions are normative. An implementation MUST NOT silently
contradict them. A decision change MUST be made explicitly through the public
documentation and ADR process before dependent implementation, and only when
that documentation work is within the requested scope. Tracked project files
MUST NOT depend on ignored or local-only documents.

## Product and version boundary

- KuPilot MUST remain a local, single-process, single-user, Agent-first
  Kubernetes TUI. `v0.1` MUST allow only one active AgentRun, and every run MUST
  use one immutable ClusterScope containing one verified Context and Namespace.
- `v0.1` MUST remain read-only and limited to the six Tools and eight diagnostic
  categories in the public contracts. It MUST provide supervised Evidence
  collection and a cautious Diagnosis, not autonomous remediation.
- `v0.2` MAY add only `restart_deployment` with the approval, revalidation,
  audit, and verification rules below. `v0.2` code or design MUST NOT make a
  write path reachable in a `v0.1` composition.
- KuPilot MUST NOT become k9s, a Dashboard, a kubectl wrapper, an IDE, a web or
  hosted service, a cluster controller, or a general DevOps Agent. MUST NOT add
  a resource tree, primary inventory table, full YAML view or editor, live
  monitoring, shell, kubectl, Pod Exec, port forwarding, Helm, arbitrary
  network requests, plugins, MCP, RAG, retrievers, Multi-Agent orchestration,
  dynamic commands, or simultaneous multi-provider support.
- MUST NOT add all-Namespace or cross-cluster diagnosis, background scans,
  Watch or informer loops, scheduled runs, or autonomous remediation.
- Every proposed feature MUST identify a named diagnostic need and show that it
  helps the Agent gather bounded Evidence or helps the user supervise that
  work. A feature that primarily replaces the Agent with cluster browsing or
  direct manipulation MUST be rejected.
- MUST NOT create speculative extension points, empty package trees, or future
  abstractions for unadmitted providers, Tools, resources, or writes.

## Public language

- All project-authored public material MUST be English: tracked documentation,
  ADRs, source comments and Go documentation, identifiers, CLI/TUI copy, model
  instructions, Tool descriptions, safe errors, schema and migration text,
  examples, fixtures, snapshots, issue and PR templates, commit and PR text,
  release notes, and suggested commit messages.
- Development discussion and ignored local notes MAY use Chinese or another
  language, but their public result MUST be English and MUST NOT become a
  project dependency.
- External data and user input MAY contain arbitrary valid Unicode. Tests for
  non-English external data MUST construct it with escapes or at runtime rather
  than commit non-English authored literals. The UI MUST normalize and render
  such data safely.
- The Agent MAY answer in the language of the current user question and MUST
  fall back to English when needed. MUST NOT add language settings, locale
  negotiation, translated safety copy, or an i18n framework in `v0.1`.

## Architecture and dependency direction

- `internal/domain` MUST contain only pure project-owned values, state
  transitions, and invariants. It MUST NOT perform I/O or import Bubble Tea,
  Eino, client-go, sqlx, a SQLite driver, delivery, or infrastructure packages.
- `internal/application` MUST be the only use-case layer. It MUST own Session,
  run, scope, cancellation, persistence intent, event acceptance, consent, and
  future approval orchestration. MUST NOT create a parallel `internal/app`
  package.
- `cmd/kupilot` MUST be the sole composition root. It MUST explicitly construct
  and inject concrete dependencies and own startup and shutdown order. It MUST
  NOT contain product policy or use-case logic, use a service locator or
  reflection-based dependency injector, or create hidden global mutable state.
- Interfaces MUST be owned by the consumer that needs the capability, normally
  contain one to three task-specific operations, and use project-owned concrete
  DTOs. An adapter MAY import the consumer contract it implements; a consumer
  MUST NOT import the concrete adapter. MUST NOT add `Repository[T]`, a generic
  Kubernetes gateway, a generic event bus, or a framework-wide unit of work.
- `internal/cli` and `internal/tui` MUST remain delivery adapters. For use cases
  they MUST depend on Application commands, queries, DTOs, and events; they
  MUST NOT directly import or call Kubernetes, model, Eino, Tool handler,
  persistence, or write-executor packages.
- `internal/agent/einoadapter` MUST be the Eino translation boundary. Eino
  message, Tool, callback, stream, and error types MUST NOT escape into Domain,
  Application, Tools, Kubernetes, persistence, CLI, or TUI.
- `internal/tools` MUST own each Tool handler and the narrow Kubernetes read
  port it consumes. Tool handlers MUST NOT import TUI, Application
  orchestration or repositories, persistence, Eino, or a generic Kubernetes
  client.
- `internal/kube` MAY implement Application- and Tool-owned ports. It MUST NOT
  import Agent, Eino, TUI, or persistence packages, and client-go types MUST NOT
  cross its adapter boundary.
- `internal/persistence/sqlite` MAY implement Application-owned storage ports.
  It MUST NOT import Agent, Tools, Kubernetes, or TUI, and SQL, rows, DB/Tx
  handles, sqlx types, and `db` tags MUST NOT cross its adapter boundary.
- Cross-layer values MUST use concrete project types, not vendor values or
  `map[string]any`. Every package, interface, and adapter MUST have an active
  consumer and a testable responsibility.

## Go and dependencies

- Production code MUST use Go and the repository-declared minimum Go version.
  Dependencies MUST be pinned and MUST satisfy the documented Go, license,
  platform, and compatibility gates before use. MUST NOT invent an SDK API or
  claim an unrun dependency spike succeeded.
- Go changes MUST be formatted with `gofmt`; imports MUST be normalized with the
  repository's configured import tool when present.
- Every I/O or blocking operation MUST accept and honor `context.Context`.
  Contexts MUST NOT be stored in long-lived structs. A child deadline MUST NOT
  exceed its owning operation's remaining deadline.
- Internal errors MUST preserve useful causes with `%w` where appropriate.
  Every adapter MUST translate vendor and transport failures to project-owned
  stable safe classes before they reach Application, TUI, model content, logs,
  audit, or persistence. Runtime policy MUST NOT branch on raw error text.
- DTOs and states MUST use concrete types and explicit enums and transitions.
  MUST NOT use `map[string]any` across a boundary or expose credentials through
  `String`, formatting, marshaling, or generic metadata.
- Every goroutine MUST have one owner, a cancellation path, and a bounded
  termination path. The creator or sole producer coordinator MUST own channel
  closure. Concurrency MUST NOT rely on unowned background work.
- MUST NOT use `init()` for business composition or panic for expected external
  failures. MUST NOT introduce an interface or abstraction with no current
  consumer and test seam.

## Tests and verification

- Every behavior change MUST include tests for its success, failure,
  cancellation, timeout, limit, stale-state, and sensitive-data paths as
  applicable. Tests MUST be deterministic and included in the same change.
- Tests required by CI MUST NOT require a real cluster, real model, public
  network, real credential, or user state. Kubernetes tests MUST use narrow
  fakes and request-recording HTTP fixtures; model tests MUST use scripted or
  local HTTP fixtures; SQLite tests MUST use real temporary database files.
- Concurrency tests MUST use fake clocks, channels, or barriers and MUST NOT
  depend on long sleeps. Security-denial tests MUST assert both the safe outcome
  and an external model, Kubernetes, Tool, repository, or executor call count of
  zero.
- Tool and Kubernetes tests MUST assert exact verbs, resources, Namespace,
  subresources, limits, projections, partial results, and prohibited fields.
  A client-go object fake alone MUST NOT be the security oracle.
- For Go changes, MUST run targeted tests first and then every applicable
  repository gate, including `go test ./...`, `go test -race ./...`,
  `go vet ./...`, configured lint, build, static import checks, and relevant
  cross-builds. A narrower command MAY replace an inapplicable gate only when
  the final report gives the reason.
- Live smoke tests and model evaluations MAY supplement deterministic tests but
  MUST NOT be reported as CI proof or replace contract tests.

## Kubernetes and scope safety

- Kubernetes access MUST use client-go only inside `internal/kube`, behind
  task-specific consumer-owned ports. `v0.1` ports MUST NOT expose a dynamic
  client, REST client, generic GroupVersionResource, raw request builder,
  Watch, informer, arbitrary selector, generic discovery, or write method.
- Direct `v0.1` targets MUST be limited to Pod, Deployment, ReplicaSet, Job, and
  Service in the active Namespace. EndpointSlice MAY contribute address-free
  readiness counts only through the fixed Service relationship. StatefulSet
  MAY appear only as an unfetched owner reference already projected from a Pod.
- Secret objects and data, ConfigMap data, container environment values,
  unlisted Kinds, custom resources, cluster-scoped targets, and cross-Namespace
  reads MUST be denied before a Kubernetes call whenever locally decidable.
  Source allowlisting and projection MUST happen before redaction.
- Kubeconfig contents, `rest.Config`, tokens, certificates, private keys, and
  exec credential output MUST remain in the Kubernetes adapter and MUST NOT
  enter model content, Application events, TUI, ordinary logs, audit, or SQLite.
- A Context or Namespace switch MUST invalidate the generation first, cancel
  the old run, clear ResourceRef, picker caches, and future approval state,
  dispose or rebuild the client as specified, and discard late results.
  Run-scoped work MUST check complete scope and generation before external I/O,
  after every return, and again when Application accepts the event.
- MUST NOT add an Agent- or user-controlled shell, kubectl, Pod Exec, or command
  runner. The only admitted external-process exception is client-go exec
  credential authentication required by the explicitly selected kubeconfig
  Context. It MUST support strict deny, launch directly without a shell, take
  its program and arguments only from that kubeconfig, omit the model API key,
  bound output and keep it confined to the credential decoder, and terminate
  with the owning Context.

## Model, privacy, and Evidence

- `v0.1` MUST support exactly one configured `openai_compatible` provider kind,
  one canonical origin, and the accepted Chat Completions-style streaming and
  structured Tool-call contract. MUST NOT add provider auto-detection,
  fallback, routing, or prompt-parsed Tool calls.
- The endpoint MUST come only from typed user configuration. HTTPS with normal
  verification MUST be the default; plain HTTP MUST be loopback-only. Userinfo,
  query parameters, insecure TLS overrides, and cross-origin redirects MUST be
  rejected, and Authorization MUST NOT cross an origin boundary.
- The model API key MUST come only from masked TUI input, optional plaintext
  `model.api_key`, or the one-shot `KUPILOT_MODEL_API_KEY` environment override.
  It MUST enter an opaque non-renderable wrapper; the environment source MUST be
  unset and the file field MUST be extracted before ordinary typed decoding.
  The sole admitted durable credential location is the fixed Home configuration
  after an explicit disclosed save choice. The key MUST NOT enter CLI value
  arguments, Domain or ordinary configuration values, child environments,
  prompts, rendered TUI or history, errors, logs, audit, SQLite, or model
  content.
- Before the first content transfer, Application MUST obtain informed consent
  bound to the policy version, canonical origin hash, and exact enabled data
  categories. A changed origin, category, meaning, or policy version MUST
  invalidate consent and produce zero content requests until renewed.
- Every model-bound field MUST pass this order locally: source allowlist,
  project-owned projection, text normalization and unsafe-control removal,
  sensitive-value block or typed redaction, hard item and byte limits, neutral
  serialization, then a final consent and origin check.
- User text, Kubernetes fields, Events, logs, model output, resumed content, and
  endpoint errors MUST remain untrusted data. Prompt text and model output MUST
  NOT change scope, policy, endpoint, budgets, Tool authority, consent,
  Evidence, approval, or execution state.
- Only deterministic runtime Tool handling MAY create Evidence. Every confirmed
  fact MUST cite accepted Evidence from the same AgentRun. A Diagnosis MUST keep
  confirmed facts, hypotheses, missing information, and recommended actions
  distinct; `v0.1` recommendations MUST be marked as not executed.

## SQLite and retention

- KuPilot MUST use one local SQLite database through `database/sql` and exactly
  one pure-Go driver selected by the required evidence gate. MUST NOT silently
  introduce CGO, a second production driver, another database, or claim an
  unverified driver passed.
- sqlx MAY be used only as a thin helper inside
  `internal/persistence/sqlite`. MUST NOT add an ORM, GORM, GORM Gen, sqlc,
  AutoMigrate, generic query builder, generic CRUD layer, or generated
  repository framework.
- SQL MUST use explicit column lists, fixed statements, bound values,
  context-aware calls, explicit mappings, enabled foreign keys, and short
  transactions. MUST NOT use `SELECT *`, `Unsafe()`, `Must*`, unbounded
  `Select`, `map[string]any` writes, value-bearing SQL debug output, or a
  transaction across model, Kubernetes, terminal, or user interaction.
- Migrations MUST be forward-only, versioned, and checksummed. A released
  migration MUST NOT be edited. Unknown, corrupt, or incompatible storage MUST
  NOT be silently deleted, overwritten, or recreated.
- Durable domain IDs MUST be application-generated UUIDv7 text. SQLite times
  MUST be UTC Unix milliseconds; retention and approval logic MUST use injected
  UTC clocks, while local-time conversion MUST remain presentation-only.
- Persistence MUST follow the Data Retention Contract. Except for the explicitly
  saved plaintext model key in the fixed Home configuration, credentials,
  kubeconfig, raw Kubernetes objects, raw Events or container output, full
  prompts, raw model traffic or streams, raw Tool results, vendor errors, and
  framework objects MUST never be persisted or placed in generic payload
  columns.
- Newly created Home, state, database, and sidecar paths MUST use owner-only
  permissions on supported platforms. Existing user-managed modes MUST be
  respected rather than rejected or changed solely for being wider. Unsafe
  managed symlinks and file types MUST still be rejected. KuPilot MUST NOT claim
  SQLite encryption, tamper resistance, or forensic deletion.
- A failed durable run start MUST prevent model and Tool I/O. A later read-only
  persistence failure MAY finish the in-memory Diagnosis only with visible
  degraded state and no false resume claim. A pre-write storage or audit failure
  MUST produce zero executor calls.

## CLI and TUI

- A bare `kupilot` MUST create a new Session without querying history. Only
  `kupilot resume`, `kupilot resume <session-id>`, and
  `kupilot resume --last` MAY query eligible history; they MUST NOT fall back to
  a new Session on empty, cancelled, invalid, or non-resumable results.
- Session selection MUST NOT depend on working directory, repository path,
  Context, Namespace, ResourceRef, environment, or crash state. MUST NOT add
  cwd/workspace semantics, `--all`, `--cd`, automatic last-Session resume, or a
  second Session-management CLI.
- Resume MUST load only eligible safe history and unverified scope/resource
  candidates. It MUST NOT resume an AgentRun, model stream, ToolInvocation,
  client, live generation, approval, or write. Resume alone MUST cause zero
  model, Kubernetes, Tool, approval, and executor calls; later scope activation
  and model transfer require their own explicit verification and consent gates.
- `help`, `version`, and `cache clear` MUST short-circuit without initializing
  the business database, Kubernetes, model, or TUI workflow. `cache clear` MUST
  remove only entries below the fixed Home cache child. CLI arguments MUST NOT
  accept credential values, raw kubeconfig, questions, arbitrary commands, or
  approval tokens.
- The TUI MUST remain a low-chrome single Agent-supervision screen: continuous
  transcript, inline Tool steps, exactly one three-to-eight-row multiline
  composer, optional untitled suggestions or Picker below it, and a scope footer
  that prioritizes Context, Namespace, and read-only or approval state.
- The TUI MUST NOT add a resource browser, primary table, full YAML, raw log
  viewer, action menu, dashboard, shell, kubectl, multi-page navigation, or a
  second editor. A Picker MUST remain a bounded input aid and MUST NOT create
  Evidence or prove a resource exists.
- Bubble Tea types MUST stay inside `internal/tui` and construction in
  `cmd/kupilot`. `Update` and `View` MUST perform no business I/O. Asynchronous
  messages MUST carry and validate the applicable request ID, run ID, scope
  generation, sequence, and terminal state before changing UI state.
- The Slash registry MUST be compile-time fixed to the commands accepted by
  ADR-0023. Unknown commands, dynamic commands, and `!` syntax MUST perform no
  external action and MUST NOT enter the model as Tool authority.
- External text MUST be valid, bounded, and stripped or visibly replaced for
  unsafe terminal, escape, device-control, and bidirectional sequences before
  render state. Scope, consent, approval, execution, and verification meaning
  MUST NOT rely on color alone.

## Tools and execution budgets

- The complete `v0.1` model-visible catalog MUST contain exactly
  `get_resource`, `list_resources`, `get_events`, `get_pod_logs`,
  `get_previous_pod_logs`, and `get_related_resources`.
- Every Tool name, purpose, version, input schema, result DTO, and Evidence
  mapping MUST be code-defined and strict. Unknown, duplicate, wrong-type,
  overlong, or extra fields MUST be rejected. The model MUST NOT supply Context,
  Namespace, endpoint, credential, arbitrary Kind or GVR, raw selector,
  deadline, or hard limit.
- Runtime MUST validate and canonicalize the call, atomically reserve budgets,
  inject immutable scope and ceilings, check generation at all three gates, and
  dispatch through a fixed table. Tool-like prose, malformed structured output,
  or an unknown Tool MUST cause zero handler calls.
- Tools MUST return only project-owned safe DTOs, explicit partial/truncation
  and safe error metadata, observation time, and deterministic Evidence. MUST
  deny, project, normalize, redact or block, and limit before any model or
  persistence sink; MUST NOT marshal a raw Kubernetes object as Tool output.
- Runtime MUST enforce ADR-0016 ceilings: 90-second runs; 10-second Kubernetes
  and 45-second model requests; 8 Agent steps, 10 Tool calls, and 3 model calls;
  64 KiB per ToolResult and 384 KiB per run; 50 resources, 50 Events, and 100
  Evidence items per result; 200 log lines, 15 minutes, 64 KiB, and 2 log calls;
  2 relationship hops, 25 nodes, and 40 edges; and stop after 2 no-progress
  steps. Configuration MAY tighten but MUST NOT expand these ceilings.
- A proposed seventh Tool is outside `v0.1` and MUST NOT be implemented until
  the version scope and relevant Accepted decisions admit it. Its proposal MUST
  name the diagnostic category, permissions, sources and model fields, privacy
  treatment, strict schema, budgets, errors and partial behavior, Evidence
  mapping, RBAC impact, fixtures, denial tests, and why it helps the Agent.

## Kubernetes writes and approval

- The `v0.1` composition MUST contain no mutation port, write-capable consumer
  interface, mutation adapter, approval coordinator, Approval Dialog, generic
  Kubernetes client, write Tool, or command that executes a recommendation.
  Read-only MUST be provable from imports, wiring, schemas, RBAC fixtures, and
  recorded requests.
- `v0.2` MUST expose only `restart_deployment` for one exact `apps/v1`
  Deployment. It MUST change only the KuPilot-owned Pod-template annotation and
  MUST NOT accept arbitrary patch, YAML, annotation, timestamp, resource
  version, delete, scale, rollback, exec, or additional write parameters.
- Agent, model, Tool, CLI, and TUI MUST NOT hold or call the executor. Only the
  Application-owned approval coordinator MAY call the fixed executor after all
  checks and durable pre-operation audit succeed.
- Approval MUST default to rejection, expire exactly 60 seconds after proposal
  creation, be single-use, and bind a versioned digest to operation, policy,
  immutable scope and generation, request identity, exact target and UID,
  template fingerprint, Deployment generation, canonical parameters, and
  expiry. TUI text or model output MUST NOT create approval authority.
- After approval, runtime MUST recheck one-time state, nonce, digest, time,
  scope, policy, and target; re-read and revalidate the Deployment; atomically
  persist consumed approval and pre-operation audit; perform a final scope
  check; and issue at most one write with the fresh concurrency precondition.
- Change, conflict, expiry, cancellation, restart, mismatch, audit failure, or
  ambiguous prior outcome MUST fail closed and MUST NOT trigger an automatic
  write retry. Request acceptance, rollout progress, unknown outcome, timeout,
  and verified completion MUST remain separate UI and audit states.

## Session conduct and scope control

- Before editing, MUST read every applicable `AGENTS.md`, inspect `git status`
  and the relevant existing implementation and tests, and give a short plan of
  three to seven steps. MUST identify pre-existing worktree changes and preserve
  them.
- MUST modify only files authorized by the user and only for the requested
  outcome. MUST NOT discard, overwrite, reformat, stage, or otherwise absorb
  unrelated user changes. MUST NOT add opportunistic refactors, generated
  artifacts, dependencies, or local workflow files.
- MUST use the smallest safe, testable change. MUST inspect existing contracts
  before adding a package, interface, dependency, schema field, Tool, Kind,
  network path, durable field, or write capability.
- MUST NOT commit, push, open or merge a PR, publish, release, mutate an
  external system, or perform another external side effect unless the user
  explicitly requests that exact action. Read-only research required by an
  authorized task does not authorize changing external state.
- MUST never place a real credential, kubeconfig, Secret, raw user cluster
  output, or credential-shaped example in source, fixtures, commands, logs, or
  the final report.
- MUST report commands and results truthfully. MUST NOT claim a test, lint,
  build, spike, review, or smoke test passed unless it was actually run and its
  result observed in the current session. Failed, skipped, and unavailable
  checks MUST be named with their reason.

## Definition of Done

A task is done only when all applicable items below are true:

- The requested behavior is complete without a placeholder, fake success,
  bypass, or deferred TODO that evades acceptance criteria.
- Layer ownership, scope isolation, cancellation, timeout, bounded-data,
  sensitive-data, error, degraded-storage, and write-absence or approval paths
  are handled as applicable.
- Focused tests and every applicable repository-wide gate were actually run and
  passed, or each unrun gate is explicitly reported with a valid reason.
- Public documentation, ADRs, schemas, migrations, fixtures, and user-facing
  copy are synchronized with changed behavior and remain English.
- `git status`, the complete `git diff`, and `git diff --check` were reviewed.
  The diff contains no unrelated edit, accidental generated file, credential,
  prohibited data, or lost user change.
- The final handoff lists changed files, actual checks and outcomes, security
  and language review, and remaining risks or follow-up. Compilation alone MUST
  NOT be represented as completion.

## Review rejection rules

Reviewers MUST reject a change when any applicable item is true:

1. It expands the product or version boundary without an admitted public
   decision, or makes the UI a cluster-management surface.
2. It violates import direction, consumer ownership, Eino/client-go/sqlx
   confinement, or the single composition root.
3. It has an unowned goroutine, missing cancellation or deadline, stale-event
   acceptance, mutable RunInput, or incomplete generation gate.
4. It adds a Kubernetes verb, Kind, relationship, Namespace scope, model field,
   network destination, durable field, or RBAC permission without explicit
   allowlist, privacy, budget, and deterministic test review.
5. A new sink to model, terminal, log, audit, SQLite, or error output is not
   necessary, projected, normalized, redacted or blocked, bounded, and tested
   with distinct synthetic canaries.
6. A Tool lacks strict schema, scope injection, exact budgets, partial/error
   behavior, Evidence provenance, request recording, or zero-call denial tests.
7. A migration edits a released file, lacks upgrade and rollback-failure tests,
   adds a generic payload escape hatch, or cannot upgrade an old fixture.
8. A `v0.2` write path has an approval bypass, mutable parameters, replay,
   stale-target path, missing durable pre-audit, automatic retry, or conflated
   execution and verification result.
9. A security test checks only an error string instead of also proving the
   forbidden model, Kubernetes, repository, child-process, or executor action
   count is zero.
10. The diff is broader than the request, hides behavior under bulk formatting
    or generation, includes non-English authored public content, or reports a
    command that was not run.
