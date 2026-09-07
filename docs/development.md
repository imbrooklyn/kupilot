# Development and CI Gates

- Status: Accepted `v0.5` development contract
- Date: 2026-09-07

The commands below describe the currently implemented repository gates. Named
model profiles, the stable Eino ADK Session-context/summarization slice, the
deterministic permission matrix, and the common ActionEnvelope/approval
foundation now have deterministic tests. Restart, scale, rollback, one
controller-owned Pod delete, cordon, uncordon, drain, exact local direct argv,
and the separate shell operation are composed through one Application-owned
dispatcher. Pod Exec, container-file, and diagnostic-Pod handlers retain their
default-off gate and use the same Application supervision for human and
Reviewer routes. All execution evidence is synthetic or loopback-only and does
not by itself claim a live cluster, endpoint, host tool, or sandbox
integration. Opt-in, integration-tagged model, Reviewer-evaluation, Session,
and Kubernetes harnesses are available below; only an observed non-skipped run
is evidence for its exact configured target.

Kupilot's local and hosted gates use the repository `Makefile` as their single
command source. The hosted workflow invokes the same targets contributors run
locally; it does not duplicate test selection or security policy in workflow
steps.

## Required versions

The module's supported minimum remains Go 1.25.0. CI pins Go 1.25.13 as the
reviewed patch version on that minimum-version line. `GOTOOLCHAIN` is set to
`local` in CI so a job cannot silently replace the selected toolchain.
Aggregate gate targets verify that exact Go patch version before running. With
Go toolchain management enabled, the complete local equivalent can be selected
explicitly with `GOTOOLCHAIN=go1.25.13 make check-all`. The supporting version,
security, platform, and maintenance evidence is recorded in
[Dependency Compatibility](compatibility.md).

The following development tools are installed into the ignored `bin/tools`
tree by versioned Makefile rules. They are development dependencies and do not
enter `go.mod` or the production dependency graph.

| Tool | Version | Gate |
| --- | --- | --- |
| `goimports` | v0.48.0 | Import grouping and Go formatting |
| `golangci-lint` | v2.11.4 | Repository lint configuration |
| `govulncheck` | v1.6.0 | Reachable Go and module vulnerability scan |
| `actionlint` | v1.7.12 | GitHub Actions syntax and semantic checks |

## Local commands

Use these aggregate targets from the repository root:

| Command | Purpose |
| --- | --- |
| `make check` | Fast gate: formatting, imports, vet, lint, workflow lint, unit and integration tests, and a CGO-free build |
| `make check-slow` | Slow gate: race, security, dependency, migration, offline end-to-end, fuzz-seed, vulnerability, and supported cross-build checks |
| `make check-all` | Complete local equivalent of the required fast and slow Linux gates |
| `make platform-smoke` | Full deterministic test suite plus a CGO-free native build |

The component targets `workflow-lint`, `test-security`, `import-guard`,
`dependency-guard`, `test-migration`, `test-e2e`, `test-fuzz-seeds`,
`test-race`, `vuln`, and `cross-build` are available for focused diagnosis.
`make bootstrap-tools` installs all pinned development tools without running a
gate.

## Opt-in integration and Reviewer evaluation

Every live Go test uses `//go:build integration`. None is selected by ordinary
`go test ./...`, `make check`, `make check-slow`, or GitHub Actions. The tagged
Session contract and offline Reviewer evaluation are repeatable and do not
need a real endpoint, credential, kubeconfig, cluster, or public network:

```sh
GOTOOLCHAIN=go1.25.13 make test-integration-session
GOTOOLCHAIN=go1.25.13 make test-integration-contracts
GOTOOLCHAIN=go1.25.13 make test-reviewer-eval-offline
```

The contracts target covers named Agent/Reviewer profile topology, consent,
absence, strict parsing, timeout/error, budget exhaustion, and stale-policy
fail-closed behavior without making an external call.

Set `KUPILOT_INTEGRATION_PREFLIGHT_ONLY=1` on any live command to load and
validate only its non-secret profile, budgets, Context, and exact RBAC. The
summary explicitly says `NOT RUN`; a preflight-only success is never live
`PASS` evidence.

`test-integration-model` always runs the request-recording protocol contract.
Its live case reports `BLOCKED` unless the caller selects exactly one profile,
acknowledges external calls, and supplies a cost ceiling. `preferred` reads the
Agent endpoint, model, and opaque credential from the process-default Kupilot
configuration. It ignores API-key environment variables and requires the
credential source to be that configuration file. No endpoint or credential is
printed.

```sh
KUPILOT_INTEGRATION_LIVE=authorized \
KUPILOT_INTEGRATION_MAX_COST_USD=1.00 \
KUPILOT_INTEGRATION_PREFERRED_MODEL=gpt-4o-mini \
GOTOOLCHAIN=go1.25.13 \
make test-integration-model-preferred
```

`KUPILOT_INTEGRATION_PREFERRED_MODEL` is an optional exact test-only model
override on the configured preferred endpoint. It performs no discovery or
fallback; an unsupported model fails that exact run.

The Ollama target performs no discovery, installation, server start, or model
download. Supply its exact loopback endpoint and already-available model. The
test uses a non-secret, test-owned opaque bearer value because the local
OpenAI-compatible route still exercises the same transport contract.

```sh
KUPILOT_INTEGRATION_LIVE=authorized \
KUPILOT_INTEGRATION_MAX_COST_USD=0 \
KUPILOT_INTEGRATION_OLLAMA_ENDPOINT=http://127.0.0.1:11434/v1 \
KUPILOT_INTEGRATION_OLLAMA_MODEL=gpt-oss:20b \
GOTOOLCHAIN=go1.25.13 \
make test-integration-model-ollama
```

The model API harness permits at most three calls, 768 requested output tokens,
1 MiB of aggregate request payload, and three minutes. It distinguishes a
transport/protocol failure from a `MODEL_CAPABILITY_FAIL`; it never relaxes the
fragmented-Tool, finish-reason, optional-usage, cancellation, timeout,
authentication, safe-error, or body-close contract based on model quality.
Choose the least expensive endpoint-supported model suitable for these
protocol checks. Do not select a high-cost reasoning model when a small model
such as `gpt-4o-mini` is available on that exact endpoint.

The live Reviewer evaluation is a separate quality command. It sends eleven
bounded, Tool-free, non-streaming cases and records false approvals, false
denials, escalations, fail-closed results, latency, available usage, and the
operator-authorized cost ceiling. It creates no approval or execution
authority. A model must not be recommended for `auto-review` until an Accepted
gate exists and the recorded evidence satisfies it.

```sh
KUPILOT_INTEGRATION_LIVE=authorized \
KUPILOT_INTEGRATION_REVIEWER_EVAL=authorized \
KUPILOT_INTEGRATION_MODEL_TARGET=preferred \
KUPILOT_INTEGRATION_MAX_COST_USD=2.00 \
KUPILOT_INTEGRATION_PREFERRED_MODEL=gpt-4o-mini \
GOTOOLCHAIN=go1.25.13 \
make test-reviewer-eval-live
```

Kubernetes integration requires one explicitly selected current disposable
`k3d-` or `kind-` Context, mutation authorization, and a digest-pinned fixture
image already suitable for `/bin/sh`, `/bin/sleep`, `/bin/echo`, and `/bin/nc`
under a non-root diagnostic security context. Preflight checks the exact RBAC
set before creating the fixed `kupilot-integration-v05` Namespace. A Namespace
with that name but without the suite ownership label blocks the run. The suite
allows at most 224 operational HTTP calls, reserves 32 calls for bounded
cleanup, permits two Pod Exec attempts, runs for at most eight minutes, and
always attempts Namespace deletion.

```sh
KUPILOT_INTEGRATION_KUBE_CONTEXT=k3d-example \
KUPILOT_INTEGRATION_KUBE_MUTATION=authorized \
KUPILOT_INTEGRATION_KUBE_IMAGE=registry.example/fixture@sha256:REPLACE_WITH_64_HEX_DIGEST \
GOTOOLCHAIN=go1.25.13 \
make test-integration-kubernetes
```

`GOTOOLCHAIN=go1.25.13 make test-integration` runs every tagged surface and the
offline Reviewer fixtures. Missing live prerequisites remain visible as
`SKIP`/`BLOCKED`; they are never counted as release `PASS` evidence. No target
uploads configuration, databases, logs, responses, or output artifacts.

Security-denial behavior remains asserted in repository tests, including zero
external action or sink counts. The security target selects the documented
redactor, scope, Slash, endpoint, SQLite/WAL, terminal, model-text, and
Diagnosis-persistence controls. The dependency guard runs the static import and
single-supervised-write composition tests before verifying module checksums.

## Framework-first and dependency gates

Before implementing Agent memory, conversation iteration, or summarization,
development must inspect `go.mod`, `go env GOMODCACHE`, and tagged source and
tests for the exact Eino and Eino OpenAI versions. Stable Eino ADK
`ChatModelAgent`, `Runner`, message state, Tool pairing, events, and
summarization middleware must be reused directly inside
`internal/agent/einoadapter`.

The active-run input bridge is pinned to Eino v0.9.19 handler ordering:
summarization first, then a `BeforeModelRewriteState` steer claim whose returned
Messages are persisted, followed by the same handler's `WrapModel` commit
barrier before real model I/O. Compatibility tests must fail if that ordering
or state persistence changes. Eino `TurnLoop` is not a queue implementation or
commit primitive and must not appear in production.

Do not add another conversation/ReAct loop, `MemoryManager`, summary engine,
generic checkpoint/event store, raw framework transcript, or framework-neutral
Agent/memory facade. Runner-managed durable Session support may replace the
thin existing-SQLite-message bridge only after a non-prerelease tag passes all
adoption criteria in ADR-0047. A discussion, main branch, marketing page, or
prerelease API is not sufficient dependency evidence.

Every dependency or endpoint spike must record exact version, source/tests,
Go compatibility, license, request/stream behavior, cancellation, limits,
errors, and the commands actually run. A failed or unrun spike is never a
successful compatibility claim.

## `v0.5` evidence levels

- Deterministic CI is mandatory and network-independent. It uses scripted model
  and Reviewer behavior, request-recording Kubernetes/HTTP fixtures, direct
  process fixtures, fake clocks/barriers, and real temporary SQLite files.
- Tagged live integration is opt-in evidence for one exact dependency,
  endpoint, data source, local tool, or cluster version. It cannot replace CI
  or be generalized to another target.
- Model evaluation is separate evidence for Agent quality and Reviewer false
  approval/denial, escalation, latency, token use, and cost. It is not a
  protocol, authority, or deterministic security gate.

The `v0.5` deterministic matrix must cover every permission profile and risk
class, Reviewer failure, Session rules, both generations, safe history and
current-question-once, summarization coverage, each capability's exact request
and projection, ActionEnvelope fields, durable pre-operation audit, at-most-one
execution attempt, ambiguous outcomes, verification, process join, and
prohibited-data absence. It also covers steering lifecycle, FIFO one-at-a-time
drain, LIFO edit, exact queue limits and one-over, generation invalidation,
complete-run Session grammar, model-input exact-once, and every no-auto-send
terminal state. Every denial asserts the relevant external call count is zero.
It additionally covers authoritative Last active and transactional exact/batch
deletion; terminal capabilities, reverse search, semantic navigation,
undo/redo, doctor and accessibility; typed clarification and terminal reasons;
preflight and category budgets; Evidence freshness/conflict/negative coverage;
narrow safe-read reuse; strict completeness; the synthetic injection corpus;
and the unified recovery matrix.

## Hosted CI

The GitHub Actions workflow defines three gate job groups:

- `fast` runs `make check` on Linux amd64.
- `slow` runs `make check-slow` on Linux amd64.
- `platform-smoke` runs `make platform-smoke` on Linux arm64, macOS arm64, and
  macOS amd64.

Linux amd64 is the primary correctness and security gate. The additional Linux
and macOS jobs verify the supported architecture and platform contract.
Windows remains experimental and is not a release gate. Workflow actions are
pinned to immutable commit digests, permissions are read-only, and no workflow
uses `pull_request_target` or uploads runtime databases, WAL sidecars, logs, or
configuration files.

CI and deterministic tests must not use a real cluster, model endpoint,
kubeconfig, API key, or user state. Kubernetes and model transport tests use
narrow fakes or loopback-only fixture servers. The remote-command fixture
performs a real client-go SPDY upgrade against a request recorder and the
diagnostic-Pod fixture records its exact create/wait/log/delete/cleanup
lifecycle; neither contacts a cluster or public network. SQLite tests use
temporary database files that are not retained as artifacts.

Typed remediation fixtures record exact GET/LIST/PUT/PATCH/DELETE/eviction and
SelfSubjectAccessReview requests, optimistic preconditions, semantic bodies,
immutable drain sets, cancellation, ambiguity, and verification. Local-process
fixtures execute only the current test binary with exact argv, inspect path and
content identity, reject scripts and symlinks, prove minimal environment and
literal metacharacters, bound ordered output, and own process-group
cancellation/join. These tests prove adapter contracts, not operating-system
filesystem or network sandboxing.

## Network and vulnerability-database policy

A cold tool or module cache requires access to the configured official Go
module sources. `govulncheck` also requires the current official Go
vulnerability database. A dependency download, tool bootstrap, checksum, or
vulnerability-database availability failure fails the affected gate; it must
not be converted into a skip or ignored result. Retry only after connectivity
or the upstream service is restored.

Tool, action, runner, or Go patch-version updates must remain fixed and be
reviewed together with the Makefile, workflow, and this document. A scanner
finding must be fixed or handled through the project's documented security
decision process; the CI command must not suppress it merely to restore a green
status.

## Work-item and release-gate scope

Work-item acceptance, subsequent work sequencing, and release-candidate
admission are separate decisions. A repository-wide vulnerability gate may
remain open across independently scoped work only when review establishes all
of the following:

- The finding originates entirely in the pinned toolchain or dependency
  baseline rather than the work item's code or dependency changes.
- The work item neither introduces the affected dependency or call path nor
  makes the finding newly reachable.
- The finding does not invalidate the work item's boundary-specific security,
  privacy, migration, or sink tests.
- The scanner remains enabled, the finding remains visible, and remediation is
  owned by release-critical toolchain or dependency work.

Under those conditions, the finding does not reopen independently accepted
work, prevent that work from being committed, or prevent later separately
scoped work from starting. It still blocks a release candidate, tag,
publication, and any claim that the complete repository gate is green. A
finding introduced or made reachable by the current work remains a blocking
failure for that work item and cannot use this separation.

The platform policy is defined by
[ADR-0028](adr/0028-support-macos-and-linux-with-experimental-windows.md).
Security and privacy requirements remain normative in the
[Security Threat Model](security.md) and [Privacy Overview](privacy-overview.md).
The [v0.1 Security Review](security-review-v0.1.md) is historical evidence only;
a future `v0.5` release requires a fresh review of the actually reachable
composition; Accepted documentation alone is not release evidence.

The deterministic quality harness uses only synthetic response and
Evidence fixtures. It reports reference validity, unsupported current-state
claims, stale/cross-run rejection, uncertainty/limitation handling, and
response/citation bounds. It must not open a network connection, construct a
model client, or be reported as live model quality. Stream continuation
compatibility tests are source/loopback checks; with the pinned protocol they
assert unavailable, unknown/recovered, and zero retry rather than simulating a
nonexistent resume API.

The loopback endpoint-conformance fixtures exercise strict final output, Tool
call identity, stream ordering and usage, cancellation, timeout, malformed,
duplicate and out-of-order events, unknown outcome, continuation capability,
and no cross-origin retry. The injection corpus places synthetic hostile text
in every admitted untrusted source class and asserts that authority remains
unchanged. Neither suite runs at startup or constitutes live endpoint/model
quality evidence.

## References

- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Admit Controlled Execution and Remediation](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048: Own Run Steering and Queued Follow-Up Input](adr/0048-own-run-steering-and-queued-follow-up-input.md)
- [ADR-0049: Bound TUI Observability, Planning, Compaction, and Evidence Coverage](adr/0049-bound-tui-observability-planning-compaction-and-evidence-coverage.md)
- [ADR-0050: Use Authoritative Session Activity and Transactional Deletion](adr/0050-use-authoritative-session-activity-and-transactional-deletion.md)
- [ADR-0051: Use Bounded TUI Navigation, Capabilities, and Local Diagnostics](adr/0051-use-bounded-tui-navigation-capabilities-and-local-diagnostics.md)
- [ADR-0052: Use Typed Agent Outcomes, Evidence Integrity, and Preflight](adr/0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
