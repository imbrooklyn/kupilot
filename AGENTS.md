# Kupilot Repository Instructions

These instructions apply to the entire repository and do not override any
higher-level instruction. This file MUST remain the only committed Agent or
Codex instruction file in the repository. MUST NOT add nested `AGENTS.md`
files, Codex-specific contribution guides, prompt collections, or local
workflow notes. General contributor guidance belongs in `CONTRIBUTING.md`.

## Normative baseline

Before changing behavior, MUST read the relevant parts of the current public
baseline and every relevant Accepted ADR:

- [Product Contract](docs/product.md), [Version Scope](docs/scope.md), and
  [Architecture](docs/architecture.md)
- [Security Threat Model](docs/security.md),
  [Privacy Overview](docs/privacy-overview.md), and
  [Data Retention Contract](docs/data-retention.md)
- [Configuration](docs/configuration.md),
  [Model Compatibility](docs/model-compatibility.md),
  [Agent Runtime](docs/agent-runtime.md), and
  [Operational Capabilities](docs/diagnostic-capabilities.md)
- [ADR-0013: Layered Boundaries](docs/adr/0013-layered-architecture-and-consumer-owned-ports.md),
  [ADR-0014: Scope Generation](docs/adr/0014-cluster-scope-generation-isolation.md),
  [ADR-0038: Evidence-Backed Answers](docs/adr/0038-use-free-form-answers-with-verified-evidence-metadata.md),
  and [ADR-0043: One Eino Boundary](docs/adr/0043-use-one-eino-runtime-boundary.md)
- [ADR-0044: Daily Operations and Permission Profiles](docs/adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md),
  [ADR-0045: Controlled Execution and Remediation](docs/adr/0045-admit-controlled-execution-and-remediation.md),
  [ADR-0046: Named Model Roles and Auto-Review](docs/adr/0046-use-named-model-roles-and-optional-auto-review.md),
  and [ADR-0047: Eino ADK Session Context](docs/adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0031: Explicit Session Resume](docs/adr/0031-require-explicit-cli-session-resume.md),
  [ADR-0039: Runtime Budgets](docs/adr/0039-use-configurable-runtime-budget-profiles.md),
  [ADR-0040: Conversational TUI](docs/adr/0040-use-a-codex-style-conversational-tui.md),
  and [ADR-0041: Session Export](docs/adr/0041-export-free-form-session-summaries.md)

Accepted public decisions are normative. An implementation MUST NOT silently
contradict them. A decision change MUST be made explicitly through the public
documentation and ADR process before dependent implementation, and only when
that documentation work is in scope. Tracked files MUST NOT depend on ignored
or local-only documents.

The Accepted `v0.5` documents are an implementation target, not proof that a
capability is already reachable, tested, compatible, or release-ready. MUST NOT
claim implementation, test, migration, dependency, RBAC, live integration, or
model-evaluation success without running and observing the relevant evidence.

## Product and topology boundary

- Kupilot MUST remain local, single-process, single-user, Agent-first, and
  limited to one active AgentRun and one verified Kubernetes Context at a time.
- Every run MUST freeze one working Namespace, one namespace-access policy, one
  scope generation, one policy generation, one permission profile, exact
  model-role/origin consent, a versioned catalog, and finite budgets.
- The TUI MUST remain one low-chrome conversational Agent-supervision screen.
  MUST NOT add a primary resource tree/table, dashboard, action center, full
  YAML editor, raw log pane, shell console, second editor, or multi-page cluster
  management surface.
- Kupilot MUST NOT become a controller, operator, daemon, scheduled scanner,
  hosted service, cluster-resident service, multi-user control plane, telemetry
  collector, plugin host, MCP client, RAG/retriever system, or Multi-Agent
  orchestrator.
- MUST NOT add Watch, informers, background scans, scheduled work, autonomous
  remediation, cross-Context operations, cluster-admin assumptions, wildcard
  RBAC, provider routing, or dynamic model-selected capabilities.
- Every capability MUST name a current operational need and show how it helps
  the Agent gather bounded Evidence or lets the user supervise controlled work.
  MUST NOT create speculative extension points, empty package trees, generic
  clients, or future abstractions.

## Admitted P0 capability categories

The `v0.5` implementation MAY admit only code-owned versioned entries in these
categories, subject to all policy, privacy, budget, RBAC, and test gates:

- typed reviewed built-in and exact policy-admitted CRD `get`, `list`,
  conversational `describe`, and bounded query/count/table projections;
- Events; current, previous, and explicit all-container non-following logs;
  bounded local log search; and Pod/Node metrics;
- explicitly configured optional Prometheus and Loki data sources;
- a safe Secret-metadata projection and separately policy-admitted exact
  ConfigMap keys or non-credential container environment values;
- bounded container-file reads, predefined read-only Pod diagnostics,
  separately gated Pod Exec, and bounded diagnostic Pods;
- typed restart, scale, rollback, one controller-owned ordinary Pod delete,
  cordon, uncordon, and drain; and
- default-off restricted direct argv for exact `kubectl`, `helm`, `argocd`, or
  another policy-selected executable, with shell as a separate default-off
  risk class.

MUST NOT expose generic patch/apply/edit/delete, raw YAML, arbitrary selectors,
JSONPath/templates, model-selected GVRs, executables, images, destinations,
flags, stdin, runner commands, deadlines, limits, generic HTTP, port forwarding,
wildcard local execution, or a fallback from typed operations to a runner. The
only command-string exception is the exact bounded typed parameter of the
separate explicitly enabled `critical` shell operation.

`describe` MUST be built from typed reads and fixed relationships rather than
shelling out. Query/count/table MUST use code-defined fields and operators. An
exact CRD policy MUST name group, version, resource, Kind, scope, verbs,
projected fields, limits, and Evidence mapping. Discovery MAY validate an exact
entry but MUST NOT create model authority.

Lists and queries MUST use safe server-side filtering where supported and
runtime-owned continuation/pagination. They MUST enforce independent per-page
and aggregate page, item, byte, and time ceilings and expose explicit partial
or truncation state. The model MUST NOT supply a continuation token or raise a
ceiling.

## Permission and risk model

- Deterministic code MUST classify normalized operations and their data, sink,
  network, and side-effect characteristics as `safe`, `review`, `critical`, or
  `deny`. Model/user text, Reviewer rationale, and RBAC MUST NOT lower risk.
- `ask` MUST be the default permission profile. It runs `safe` automatically
  and routes `review` and `critical` to the human.
- `read-only` MAY run `safe` automatically and route an admitted sensitive read
  to a human. It MUST deny mutation, Pod Exec, diagnostic Pod, local process,
  and shell, and approval MUST NOT escalate it into those capabilities.
- `auto-review` MAY route only `review` to the optional Reviewer and MUST keep
  `critical` human-routed.
- `full-access` MAY automatically route admitted and enabled `review` and
  `critical` only after explicit high-risk user selection and local policy
  permission. It MUST NOT be a default or implicit fallback.
- `custom` MAY route `safe` to exact automatic, human, or deny outcomes and
  `review` to exact automatic, human, Reviewer, or deny outcomes. Each
  `critical` operation MAY be exact automatic, human, or deny and MUST default
  to human. Reviewer MUST NOT route `safe` or `critical`.
- No profile may grant RBAC, broaden scope or consent, enable a default-off
  capability, expose credentials, alter risk, bypass revalidation/audit/budget,
  or override `deny`.

The optional `approval_reviewer` is not permission authority. It MUST receive a
minimal bounded projection of explicit user intent, normalized ActionEnvelope,
and code-owned policy facts. Its call MUST be strict, non-streaming, and
Tool-free and return only `approve`, `deny`, or `escalate_to_user` plus a
bounded rationale. Malformed output, Tool calls, prose-only output, timeout,
cancellation, missing consent, stale policy, budget exhaustion, or transport
failure MUST authorize nothing and MUST NOT fall back to the Agent or another
endpoint.

A human Session rule MAY cover only an exact `review` operation, scope, target
pattern, typed parameter or argv template, data/sink/network effects, ceilings,
and expiry. It MUST be revocable, current-process, current-Session only, and
MUST NOT be persisted as resumable authority, cover `critical`/`deny`, or be
created by a model or Reviewer.

Permission profile, catalog/policy, Session rule, relevant data-source policy,
or origin-policy changes MUST advance `policy_generation`, invalidate dependent
reviews/approvals/rules/actions first, and then cancel old work. Context,
Namespace, or namespace-policy changes MUST advance scope generation first.

## Public language

- All project-authored tracked/public material MUST be English: docs, ADRs,
  code comments, identifiers, CLI/TUI copy, prompts, Tool descriptions, errors,
  schemas, migrations, examples, fixtures, snapshots, issue/PR templates,
  release notes, and suggested commit messages.
- Ignored local notes and development discussion MAY use another language but
  MUST NOT become a tracked dependency.
- External input MAY contain arbitrary valid Unicode. Tests for non-English
  external data MUST construct it with escapes or at runtime rather than commit
  non-English project-authored literals. Rendering MUST normalize it safely.
- MUST NOT add locale settings, translated safety copy, or an i18n framework in
  the current product line.

## Architecture and dependency direction

- `internal/domain` MUST contain only pure project-owned values, state
  transitions, and invariants. It MUST perform no I/O and import no Bubble Tea,
  Eino, client-go, sqlx, SQLite driver, delivery, or infrastructure package.
- `internal/application` MUST be the only use-case layer. It MUST own Session,
  run, scope and policy generations, cancellation, persistence intent, event
  acceptance, consent, permission routing, ActionEnvelope orchestration, and
  execution authority. MUST NOT create `internal/app`.
- `cmd/kupilot` MUST be the sole composition root. It MUST explicitly construct
  named model-role, Tool, Kubernetes, data-source, process, storage, and
  delivery dependencies and own startup/shutdown. MUST NOT contain product
  policy, use a service locator/reflection DI, or create hidden global state.
- Interfaces MUST be consumer-owned, task-specific, normally one to three
  operations, and use concrete project DTOs. MUST NOT add `Repository[T]`, a
  generic Kubernetes/data-source/process gateway, a generic event bus, or a
  framework-wide unit of work.
- `internal/cli` and `internal/tui` MUST remain delivery adapters and depend on
  Application commands, queries, DTOs, and events. They MUST NOT call model,
  Eino, Kubernetes, Tool, persistence, Reviewer, or executor implementations.
- `internal/agent/einoadapter` MUST be the only Eino, provider, message, Tool,
  callback, stream, and transport boundary. No Eino/provider type may escape.
- `internal/tools` MUST own strict capability handlers and the narrow ports
  they consume. Handlers MUST NOT import TUI, Application orchestration,
  repositories, persistence, Eino, or a generic Kubernetes/process client.
- `internal/kube` MAY implement Application- and Tool-owned ports. It MUST NOT
  import Agent, Eino, TUI, or persistence. Client-go objects, dynamic clients,
  REST/discovery clients, GVRs, request builders, and credentials MUST NOT cross
  its boundary. An internal dynamic CRD implementation, if evidence requires
  one, MUST remain behind exact policy-bound consumer ports.
- `internal/persistence/sqlite` MAY implement Application-owned storage ports.
  It MUST NOT import Agent, Tools, Kubernetes, processes, or TUI. SQL, rows,
  DB/Tx handles, sqlx types, and `db` tags MUST NOT cross its boundary.
- Cross-layer values MUST use concrete project types, explicit enums, and
  transitions, never vendor values or `map[string]any`.

## Framework-first Eino boundary

- MUST directly reuse stable Eino ADK `ChatModelAgent`, `Runner`, message state,
  Tool-message pairing, ReAct iteration/events, and summarization middleware
  inside `internal/agent/einoadapter`.
- MUST NOT implement another conversation/ReAct loop, `MemoryManager`, general
  Context manager, tokenizer, summarizer/summary engine, generic checkpoint or
  event store, second durable conversation log, raw Eino transcript, or a
  framework-neutral Agent/memory/provider facade for hypothetical replacement.
- For every AgentRun after the first question in an exact Session, when prior
  eligible Messages exist, Application MUST select one ordered, bounded
  representation of all retained eligible prior turns. The adapter MUST
  translate it once, and the current question MUST appear exactly once in the
  final Eino input. Omitting eligible history or falling back to a current-
  question-only model call MUST fail closed with zero model calls.
- The existing SQLite safe Messages MUST remain the durable source through a
  thin selection/ordering/coverage/DTO bridge until runner-managed Session
  support exists in a non-prerelease Eino tag and passes ADR-0047's full
  storage, authority, consent, retention, deletion, migration, cancellation,
  Tool-pairing, and test gate.
- A discussion, main branch, example, marketing page, or prerelease API MUST NOT
  be treated as a stable dependency contract. When a stable runner-managed
  Session passes the gate, it MUST replace the thin bridge rather than sit
  beneath a second memory abstraction.
- Summarization MUST reuse the `agent` profile with an independent reserved
  non-streaming, no-Tool, one-attempt budget. MUST NOT prebuild a
  `context_compactor` role or add fallback/retry.

## Go, dependencies, and evidence

- Production MUST use Go and the repository-declared minimum version.
  Dependencies MUST be pinned and pass Go, license, platform, API, lifecycle,
  and compatibility gates before use.
- Before changing Eino behavior, MUST inspect `go.mod`, locate the exact tagged
  module through `go env GOMODCACHE`, read relevant source and tests, and check
  current stable/prerelease status. MUST NOT invent an SDK API or claim an
  unrun dependency spike succeeded.
- Exact context windows, input/output tokens, request/stream limits,
  concurrency, summary thresholds, latency, and cost values MUST come from the
  exact pinned dependency and selected endpoint evidence. An old global value,
  model name, marketing claim, Eino example default, or characters-per-token
  estimate is insufficient.
- Go changes MUST use `gofmt` and the configured import tool. Every blocking or
  I/O operation MUST accept and honor `context.Context`; contexts MUST NOT be
  stored in long-lived structs.
- Errors MUST preserve useful internal causes with `%w` and be translated at
  adapter boundaries to project-owned safe classes. Runtime policy MUST NOT
  branch on raw error text.
- Every goroutine and child process MUST have one owner, cancellation path, and
  bounded termination/join path. The creator or sole producer coordinator MUST
  own channel closure.
- MUST NOT use `init()` for business composition, panic for expected external
  failure, or introduce an abstraction without an active consumer and test
  seam.

## Kubernetes, scope, and data safety

- Kubernetes access MUST use client-go only inside `internal/kube`, behind
  exact task-specific consumer-owned ports. The model MUST NOT receive a
  dynamic client, REST client, discovery client, GVR, raw request builder,
  Watch/informer, arbitrary selector, or generic write method.
- Namespaced operations MUST satisfy the frozen `current` or `all` policy.
  `all` MAY permit exact cross-Namespace or bounded all-Namespace operations in
  the same Context only. Cluster-scoped references MUST use no fake Namespace.
- Kubeconfig, `rest.Config`, tokens, certificates, keys, ServiceAccount tokens,
  and exec-credential output MUST remain adapter-confined and absent from model,
  Application events, TUI, history, logs, audit, SQLite, exports, and child
  environments.
- Every run-scoped external operation MUST check complete scope and applicable
  policy generation before I/O, after every return, at Application event
  acceptance, and again before an execution attempt.
- A Context/Namespace change MUST invalidate generation first, cancel and join
  the old run, clear ResourceRef, caches, Reviewer/approval/action state,
  dispose or rebuild the client, and discard late results.
- Source allowlisting MUST precede project-owned projection, normalization,
  sensitive-value block/redaction, hard item/byte limits, neutral
  serialization, and final role/origin/category consent.
- Secret metadata MAY use only its exact safe projection. Secret values remain
  denied. An exact ConfigMap key or non-credential container environment value
  MUST be at least `review` and require a versioned source policy, category
  consent, and sink policy; generic or bulk values MUST be denied.
- Credentials, Secret values, ServiceAccount tokens, raw Kubernetes objects,
  unreviewed fields/APIs, raw output, and unbounded data MUST remain prohibited.
  An exact sensitive source MAY be admitted only by a versioned category and
  policy; redaction MUST NOT authorize an unlisted source.

## Remote diagnostics and local processes

- Container-file reads MUST bind exact Pod UID/container/normalized path,
  recheck in-container symlink resolution, deny credential and ServiceAccount
  paths, devices, and unsafe pseudo-filesystems, and return only bounded
  projected output.
- Predefined read-only Pod diagnostics MUST use policy-owned argv with
  `stdin=false`, `tty=false`, and `shell=false` through client-go `pods/exec`.
  Other Pod Exec MUST be `critical`, default off, bind exact Pod/container/UID/
  executable/argv, and keep stdin/TTY/shell off unless a separately admitted
  operation says otherwise; data/sink/network effects, time, and output remain
  explicit.
- Diagnostic Pods MUST be `critical` and default off. Policy MUST select a
  pinned image, Namespace, non-root/non-privileged security context, read-only
  root filesystem, no host mounts or host network, finite resources and
  lifetime, disabled ServiceAccount-token automount, exact in-cluster target,
  and separately audited create/observe/delete/ambiguous-cleanup states. An
  image allowlist MUST NOT be described as a network sandbox or NetworkPolicy
  guarantee.
- A local runner MUST launch a policy-selected executable and exact argv
  directly with `shell=false`, a fixed validated working directory, no
  inherited stdin, and an allowlisted minimal environment without model/
  Kubernetes credentials. Output MUST be bounded and the process group,
  cancellation, timeout, and join MUST be owned.
- `kubectl`, `helm`, and `argocd` integrations MUST define exact verbs, flags,
  files, destinations, data projections, risk, and verification. Kubectl MUST
  deny Context, kubeconfig, token, credential, and impersonation overrides;
  Helm MUST deny arbitrary values/stdin/plugins; Argo CD MUST bind its explicit
  server origin, credential reference, application, revision, and consent.
  They MUST NOT replace a typed P0 operation or inherit plugin execution.
- Shell MUST be a separate `critical`, default-off capability. MUST NOT permit
  shell smuggling through the restricted runner's `-c`, command strings,
  wrappers, scripts, environment, stdin, values files, or flags. Its own schema
  MUST bind one policy-selected shell executable and exact bounded command
  string. An OS sandbox MUST NOT be treated as equivalent to remote Pod, RBAC,
  target, network, or data controls.
- Kubeconfig exec credentials remain a distinct exception: strict deny MUST be
  available; allow launches only the kubeconfig program and argv without a
  shell, with a minimal environment, bounded output, credential-decoder-only
  sink, and owning-Context termination.

## Models, credentials, consent, and Evidence

- MUST support exactly one provider kind, `openai_compatible`, with a required
  named `agent` profile and optional `approval_reviewer`. Profiles MAY bind
  several explicit canonical origins. MUST NOT add auto-detection, fallback,
  provider routing, load balancing, cross-origin retry, or prompt-parsed Tools.
- Each profile MUST explicitly bind consumer role, canonical origin, model,
  non-secret settings, finite limits, and opaque credential reference. Ollama
  MAY be an OpenAI-compatible integration target, not another provider kind.
- HTTPS with normal verification MUST be the default; plain HTTP MUST be
  loopback-only. Userinfo, query, insecure TLS, and cross-origin Authorization
  forwarding MUST be rejected.
- Model credentials MUST enter an opaque non-renderable wrapper and MUST NOT
  enter ordinary Config values, CLI arguments, Domain, child environments,
  prompts, TUI/history, errors, logs, audit, SQLite, exports, or model content.
  A disclosed plaintext Home save remains the only admitted durable credential
  location until another Accepted decision.
- Consent MUST bind policy version, exact model role, canonical origin hash, and
  exact data categories. Profile/role/origin/category/meaning changes MUST
  invalidate consent and dependent pending authority and cause zero content
  requests until renewed.
- User text, history, Kubernetes data, Events, logs, metrics, file/process
  output, model output, Reviewer output, and endpoint errors MUST remain
  untrusted. They MUST NOT change scope, policy, endpoint, budgets, authority,
  Evidence, approval, or execution.
- Only deterministic runtime handling MAY create Evidence. Current-cluster
  claims SHOULD cite accepted Evidence from the same run. Historic Evidence is
  display-only. Proposed actions remain unexecuted until Application creates
  valid local authority.

## Session context, retention, and SQLite

- A bare `kupilot` MUST create a new Session without querying history. Only
  `kupilot resume`, `kupilot resume <session-id>`, and `kupilot resume --last`
  MAY query eligible history. They MUST NOT fall back to a new Session.
- Standard mode MUST supply the eligible representation to every later
  AgentRun in process and, after explicit resume, across processes. Minimal
  mode MUST supply it only from current-process context and MUST persist no
  model memory.
- Resume itself MUST perform zero model, Kubernetes, Tool, Reviewer, approval,
  process, and executor I/O. The next explicit question MUST transmit safe
  history when it exists only after current consent, scope, policy, coverage,
  and budget checks; a failed gate MUST cause zero model calls.
- History MUST NOT restore AgentRun, stream, client, scope/policy generation,
  ResourceRef authority, ToolInvocation, Evidence authority, Session rule,
  Reviewer decision, ActionEnvelope, approval, execution, or retry authority.
- Standard summary persistence MAY contain only bounded safe summary text,
  schema/policy versions, covered first/last Message IDs, ordered count,
  coverage digest/time, Session ID, `agent` profile and origin hash, and
  truncation/degraded markers. The recent tail MUST remain eligible Message
  rows after coverage, not a generic copied payload.
- Required compaction failure, timeout, cancellation, sensitivity, staleness,
  corrupt coverage, or persistence failure MUST preserve the last committed
  state and send no oversized or silently truncated model request.
- `/status` MUST report memory mode, safe counts, coverage, recent tail,
  compaction, evidence basis for budgets, named role/origin hash, consent, and
  storage health without displaying content or credentials or doing external I/O.
- Kupilot MUST use one local SQLite database through `database/sql` and one
  evidence-gated pure-Go driver. sqlx MAY remain only a thin helper inside the
  SQLite adapter. MUST NOT add an ORM, code generator, generic CRUD, query
  builder, or payload escape hatch.
- SQL MUST use explicit columns/statements, bound values, context-aware calls,
  mappings, foreign keys, and short transactions. MUST NOT use `SELECT *`,
  `Unsafe()`, `Must*`, unbounded `Select`, generic maps, or value-bearing SQL
  debug output.
- Migrations MUST be forward-only, versioned, and checksummed. Released
  migrations MUST NOT be edited. Unknown/corrupt/incompatible storage MUST NOT
  be silently deleted, overwritten, or recreated.
- Credentials, kubeconfig, raw objects/Events/logs/metrics/files/process output,
  prompts/model traffic, raw Tool results, vendor errors, Reviewer bytes,
  Session rules, and framework objects MUST never be persisted.
- Retention, deletion, export, degraded storage, owner-only creation modes, safe
  path handling, and lack of encryption/tamper/forensic-erasure claims MUST
  follow the Data Retention Contract. Terminal scrollback remains an external
  retention surface.

## ActionEnvelope and typed remediation

- Every sensitive read, network access, remote/local execution, or mutation
  MUST first become an immutable project-owned `ActionEnvelope`.
- The envelope MUST bind schema and operation versions, policy/profile/risk and
  policy generation, Session/run/request, complete scope and generation, exact
  target identity/fingerprint/revision/target set, typed parameters or fixed
  executable+argv, stdin/TTY/shell flags, data/sinks/network destinations,
  finite limits, request/expiry times, and verification-plan ID.
- A restricted runner MUST bind only policy-owned executable and argv values.
  The separate shell operation MAY bind one exact bounded command string as its
  typed parameter; it MUST NOT enter through an argv fallback.
- Canonical encoding MUST be versioned, fixed-order, length-prefixed bytes with
  SHA-256. JSON ordering, maps, model prose, human summaries, raw YAML, or
  unnormalized command strings MUST NOT carry authority.
- Decision and audit records MUST bind the envelope digest, actor, decision,
  time, applicable Reviewer profile/origin hash, and bounded safe rationale.
  Raw Reviewer response bytes MUST NOT carry authority.
- Approve-once MUST default to rejection, expire exactly 60 seconds after
  proposal creation, and be single-use. TUI/model/Reviewer text MUST NOT mint
  approval.
- Application alone MUST execute this order: strict schema/canonicalization;
  hard policy; profile routing; fresh RBAC/target or executable validation;
  valid human/Reviewer/Session-rule decision; nonce/digest/time/policy/scope/
  generation/target recheck; atomic consumption plus durable pre-operation
  audit; final generation check; at most one external attempt; outcome; and
  separate bounded verification/post-audit.
- Any pre-operation storage/audit failure MUST produce zero executor calls.
  Conflict, timeout, cancellation, restart, or an ambiguous outcome MUST NOT
  trigger automatic execution retry. Acceptance, progress, unknown outcome,
  cleanup, timeout, failure, and verified completion MUST remain distinct.
- Restart MUST change only Kupilot's annotation on one exact Deployment. Scale
  MUST target one exact Deployment/StatefulSet; positive delta one is `review`,
  scale-to-zero or another delta is `critical`. Rollback MUST bind an exact
  Deployment and fresh prior ReplicaSet and be `critical`.
- Pod delete MUST target one exact ordinary controller-owned Pod and deny force,
  grace-zero, bulk, unmanaged, static, mirror, or ambiguous cases. Cordon/
  uncordon MUST change only one exact Node's `spec.unschedulable`. Drain MUST be
  `critical`, bind a bounded complete Node/Pod/PDB plan, and provide no force,
  delete-emptydir, or ignore-daemonset escape hatch. Its fixed ordered plan is
  one envelope execution; every pre-bound mutation has at most one attempt and
  durable outcome, and any retry requires a fresh envelope.
- Existing restart safety primitives MAY be generalized, but restart-only
  enums, schemas, digest fields, ports, SQLite constraints, UI copy, and
  verification DTOs MUST be replaced or split through a forward-only migration.

## TUI and CLI

- CLI/TUI MUST remain delivery-only. Bubble Tea `Update` and `View` MUST perform
  no business I/O. Async messages MUST validate request/run IDs, both relevant
  generations, sequence, and terminal state.
- `/status` and `/permissions` MUST be local bounded queries. Permission,
  Reviewer, approval, execution, ambiguity, and verification are P0 inline
  interactions on the one Agent-first screen.
- Auto-review MUST visibly distinguish `Reviewing`, `Approved`, `Denied`,
  `Escalated`, and `Timed out` from a human decision. Failure MUST perform no
  action. Human review MUST offer only approve once, an eligible narrow Session
  rule, deny, and cancel after displaying the exact envelope.
- The composer MUST remain exactly one borderless `›` editor growing from one
  to eight content rows. Suggestions and Pickers remain bounded input aids and
  MUST NOT create Evidence or prove a resource exists.
- The Slash registry MUST be compile-time fixed. Unknown commands and `!`
  syntax MUST perform no external action and MUST NOT enter model authority.
- `help`, `version`, and `cache clear` MUST short-circuit before business
  storage, Kubernetes, model, or TUI workflow. CLI values MUST NOT accept
  credentials, raw kubeconfig, questions, arbitrary commands, or approval
  tokens.
- External text MUST be valid, bounded, normalized, and stripped or visibly
  replaced for unsafe terminal/device/bidirectional controls. Safety meaning
  MUST NOT rely on color alone.

## Budgets and capability admission

- Budgets MUST remain immutable, atomically reserved before I/O, visible, and
  finite for Agent, Reviewer, Agent-summary, Tool, Kubernetes, optional data
  source, remote/local execution, items, pages, samples, lines, bytes, streams,
  wall/idle time, repetition, and estimated/known cost.
- Model/Tool output MUST NOT select, expand, or reopen a profile. Per-capability
  ceilings remain independent from run totals.
- A proposed capability outside the catalog MUST NOT be implemented until
  public scope and an Accepted decision admit its operational need, exact
  schema/operation, permissions, sources/fields/sinks/destinations, privacy,
  budgets, errors/partial behavior, Evidence/verification, RBAC, fixtures, and
  zero-call denials.

## Tests and evidence levels

- Every behavior change MUST include deterministic success, failure,
  cancellation, timeout, limit/one-over, stale-state, sensitive-data, and
  ambiguous-outcome tests as applicable.
- CI MUST NOT require a real cluster, model, public network, real credential,
  or user state. Kubernetes uses narrow fakes plus request-recording HTTP
  fixtures; models/data sources use scripted or loopback fixtures; local
  processes use direct synthetic fixtures; SQLite uses real temporary files.
- Denial tests MUST assert the safe outcome and zero relevant model, Reviewer,
  Kubernetes, Tool, repository, process, or executor calls. Exact request tests
  MUST assert verbs, GVR, Namespace, subresource, selector, body, precondition,
  projection, executable, argv, environment, stdin/TTY/shell, destination,
  output limit, cancellation, and join as applicable.
- Concurrency tests MUST use fake clocks, channels, or barriers and MUST NOT
  depend on long sleeps.
- Deterministic CI is the correctness and security authority. Opt-in tagged live
  integration is evidence only for the exact dependency/endpoint/cluster/data-
  source/tool version tested. Model eval is separate quality, Reviewer false-
  approval/denial, escalation, latency, token, and cost evidence. Neither
  replaces CI or generalizes to an untested target.
- For Go changes, MUST run focused tests first and every applicable repository
  gate, including `go test ./...`, `go test -race ./...`, `go vet ./...`, lint,
  build, static imports, migrations, security, and relevant cross-builds. A
  skipped gate MUST be named with its reason.

## Session conduct and Git discipline

- Before editing, MUST read every applicable `AGENTS.md`, inspect `git status`,
  relevant implementation/tests, and current complete diff, and give a short
  three-to-seven-step plan. MUST identify and preserve pre-existing worktree
  changes.
- MUST modify only authorized files for the requested outcome. MUST NOT discard,
  overwrite, reformat, stage, or absorb unrelated user work, or add opportunistic
  refactors, generated artifacts, dependencies, or local workflow files.
- MUST use the smallest safe testable change and inspect existing contracts
  before adding a package, interface, dependency, schema field, capability,
  source, sink, network path, durable field, RBAC verb, or execution path.
- MUST NOT commit, push, open/merge a PR, tag, publish, release, or mutate an
  external system unless the user explicitly requests that exact action.
- MUST never place a real credential, kubeconfig, Secret, raw user cluster
  output, or credential-shaped example in files, commands, logs, or reports.
- MUST report commands and results truthfully. MUST NOT claim a test, lint,
  build, spike, review, live integration, or eval passed unless it was actually
  run and observed in the current session.

## Definition of Done and review rejection

A task is done only when the requested behavior is complete, every applicable
authority/scope/cancellation/budget/sensitive-data/failure path is handled,
focused and repository gates have passed or are explicitly justified as unrun,
public contracts and migrations are synchronized, and `git status`, complete
diff, and `git diff --check` have been reviewed for unrelated changes, lost user
work, credentials, prohibited data, and non-English project copy.

Reviewers MUST reject a change that:

1. expands product authority without an Accepted public decision or creates a
   dashboard/controller/generic command surface;
2. violates dependency direction, consumer ownership, Eino/client-go/sqlx
   confinement, framework-first reuse, or the single composition root;
3. has unowned concurrency/processes, missing cancellation/deadlines, stale
   event acceptance, mutable RunInput, or incomplete scope/policy gates;
4. adds an API/verb/Kind/CRD/field/destination/sink/command/RBAC/durable field
   without explicit projection, privacy, budget, and deterministic tests;
5. lets model or Reviewer output create Evidence, permission, scope, approval,
   execution, or a fallback route;
6. lacks strict schemas, exact request recording, finite budgets, partial/error
   behavior, provenance, or zero-call denials;
7. edits a released migration, adds a generic payload, or cannot upgrade old
   fixtures and fail safely;
8. has an ActionEnvelope bypass, mutable parameters, replay, stale target,
   missing durable pre-audit, automatic retry, or conflated attempt and
   verification state;
9. tests only an error string instead of also proving the prohibited external
   call count is zero; or
10. exceeds requested scope, hides behavior under bulk formatting/generation,
    includes non-English project-authored public content, loses user work, or
    reports a command that was not run.
