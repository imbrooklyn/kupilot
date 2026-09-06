# Kupilot

Kupilot is a local, single-process Kubernetes operations Agent with a
conversational TUI. It turns natural-language intent into bounded typed
observations and controlled actions, keeps deterministic Evidence separate from
model interpretation, and makes permission and verification state visible.

> [!IMPORTANT]
> The checked-in source implements the deterministic `v0.5` contract described
> by ADR-0044 through ADR-0047 and the canonical docs. It is unreleased. Passing
> deterministic gates is not a release-readiness claim, and opt-in live results
> apply only to the exact endpoint, model, cluster, and versions tested.

Kupilot is Agent-first. It is not k9s, a Kubernetes Dashboard, a generic
kubectl wrapper, a shell console, an IDE, a controller, or a generic API client.
There is no primary resource tree, YAML editor, action dashboard, dynamic
plugin, background reconciliation loop, or autonomous remediation.

## Implemented `v0.5` boundary

The P0 capability catalog covers:

- typed built-in and exact policy-admitted CRD `get`, `list`, conversational
  `describe`, and bounded query/count/table projections;
- Events, current/previous/all-container non-following logs, bounded local log
  search, and Pod/Node metrics;
- explicitly configured optional Prometheus and Loki data sources;
- bounded container-file reads, predefined Pod diagnostics, separately gated
  Pod Exec, and diagnostic Pods;
- typed restart, scale, rollback, one ordinary controller-owned Pod delete,
  cordon, uncordon, and drain; and
- default-off restricted direct argv for exact `kubectl`, `helm`, `argocd`, or
  another policy-selected executable, with shell as a separate default-off
  critical class.

Every capability is code-owned, versioned, strictly decoded, scope-bound,
projected, finite, consent-aware, and tested against its exact RBAC and external
request. Model text cannot select arbitrary APIs, executables, images,
destinations, YAML, commands, or limits. Generic patch/apply/edit/delete,
wildcard commands or RBAC, Watch/informers, cross-Context calls, plugins, MCP,
RAG, retrievers, and Multi-Agent orchestration remain outside P0.

See [Operational and Diagnostic Capabilities](docs/diagnostic-capabilities.md)
and [Version Scope](docs/scope.md) for the exact target.

## Permission model

The deterministic risk classes are `safe`, `review`, `critical`, and `deny`.
The permission profiles are:

| Profile | `safe` | `review` | `critical` | `deny` |
| --- | --- | --- | --- | --- |
| `read-only` | Automatic | Human only for admitted sensitive reads | Denied | Denied |
| `ask` (default) | Automatic | Human | Human | Denied |
| `auto-review` | Automatic | Optional Reviewer or human escalation | Human | Denied |
| `full-access` | Automatic | Automatic | Automatic | Denied |
| `custom` | Exact route | Exact automatic/human/Reviewer/deny route | Exact automatic/human/deny route; human by default | Denied |

A profile routes only an already admitted and enabled operation. It never
grants Kubernetes RBAC, broadens scope or consent, exposes credentials, lowers
risk, enables a default-off capability, bypasses durable audit or revalidation,
or overrides `deny`. `full-access` is explicit and never the default.

The optional Reviewer is a bounded decision input for `review`, not permission
authority. It cannot review `critical`, create a Session rule, or call an
executor. Timeout, malformed output, missing consent, stale policy, and other
failures authorize nothing; there is no implicit fallback.

Every sensitive or effectful request is bound to an immutable versioned
`ActionEnvelope`. Application performs fresh validation, decision matching,
durable pre-operation audit, a final generation check, at most one external
attempt, and separate verification. An ambiguous outcome is never retried
automatically.

## Models and Session context

Kupilot retains one provider protocol kind, `openai_compatible`, with an
explicit required `agent` profile and optional `approval_reviewer` profile.
Profiles may use different explicit origins, credentials, consent tuples, and
budgets. There is no provider auto-detection, router, fallback, load balancing,
or cross-origin retry. Summarization reuses `agent`; no `context_compactor` role
is prebuilt.

The `v0.5` Agent directly reuses stable Eino ADK `ChatModelAgent`, `Runner`,
message state, and summarization middleware inside the one Eino adapter. Kupilot
does not build another conversation loop, memory manager, summary engine,
checkpoint store, or framework facade. Until a stable Eino runner-managed
Session passes the documented adoption gate, the existing safe SQLite Messages
remain the durable source through a thin ordered bridge.

Every question after the first in a Session receives one ordered, bounded
representation of all retained eligible prior turns. Standard mode supplies it
in process and after explicit resume; minimal mode supplies it only from the
current process. Resume itself causes zero model, Kubernetes, Tool, Reviewer,
approval, process, or executor I/O. The next question transmits history only
after current consent, scope, policy, coverage, and budget checks; a failed gate
causes zero model calls, never a current-question-only fallback. History never
restores scope, ResourceRef, Evidence, permission rules, Reviewer decisions,
ActionEnvelopes, approvals, execution, or generations as authority.

## Current source build and runtime

No current release archive or package-manager installation is supported. Build
the checked-in implementation from a repository checkout:

```sh
make build
./bin/kupilot --version
```

Requirements are Go 1.25.0 or newer and macOS or Linux on `amd64` or `arm64`.
The contributor and CI toolchain uses the exact patch version documented in
[Development and CI Gates](docs/development.md).

The current source implements the fixed fourteen-Tool operational catalog,
named Agent and optional Reviewer profiles, Session context and summarization,
permission supervision, remote diagnostics, local execution policy, and the
typed remediation catalog. `config.example.yaml` uses the current strict
version 2 schema and intentionally contains no credential.

Start a new Session with:

```sh
./bin/kupilot
```

A bare start never queries history. Use only the explicit current resume forms:

```text
kupilot resume
kupilot resume SESSION_ID
kupilot resume --last
```

Automatically managed files stay below
`${KUPILOT_HOME:-$HOME/.kupilot}`. If current model configuration is incomplete,
the TUI requests one endpoint, model, and masked key and discloses plaintext
local credential storage before saving it into the named Agent profile.

## Scope, data, and storage

Every run uses one verified Context, one visible working Namespace, and one
immutable namespace policy. `current` pins namespaced operations to that
Namespace; `all` permits an explicit different Namespace or bounded all-
Namespace read in the same Context only when the catalog and Kubernetes RBAC
also permit it. Cross-Context and cross-cluster calls remain prohibited.

Every model-bound field passes source allowlisting, project-owned projection,
normalization, sensitive-value blocking or typed redaction, item/byte limits,
neutral serialization, and final role/origin/category consent. Credentials,
kubeconfig, Secret values, ServiceAccount tokens, raw objects, raw model
traffic, raw Tool results, raw operational output, and framework values never
become generic model, history, log, audit, SQLite, or child-environment data.

Kupilot has no product telemetry, analytics, remote crash reporting, operated
account, update checker, or control plane. Local SQLite, logs, exports, and an
explicitly saved key are plaintext and are not claimed encrypted,
tamper-resistant, or forensically erasable. Terminal scrollback is owned by the
terminal emulator and can outlive Kupilot deletion.

## Verification levels

Deterministic CI is the required correctness and security proof. It uses
scripted models, request-recording Kubernetes and HTTP fixtures, direct process
fixtures, fake clocks/barriers, and real temporary SQLite files without a real
cluster, credential, or public network.

Opt-in tagged live integration proves compatibility only for one exact
dependency, endpoint, cluster, data source, or local tool. Model evaluation is
separate evidence for Agent quality and Reviewer approval/denial/escalation,
latency, and cost. Neither replaces deterministic CI or generalizes to an
untested target.

## Documentation

- [Product Contract](docs/product.md)
- [Version Scope](docs/scope.md)
- [Architecture](docs/architecture.md)
- [Security Threat Model](docs/security.md)
- [Privacy Overview](docs/privacy-overview.md)
- [Data Retention Contract](docs/data-retention.md)
- [Configuration](docs/configuration.md)
- [Model Compatibility](docs/model-compatibility.md)
- [User Guide](docs/user-guide/README.md)
- [Least-Privilege RBAC](docs/rbac/README.md)
- [Troubleshooting](docs/troubleshooting.md)

Security issues follow [Security Policy](SECURITY.md). Notable changes are in
the [Changelog](CHANGELOG.md).

## License

Kupilot is licensed under the [Apache License 2.0](LICENSE).
