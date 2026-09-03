# Kupilot Configuration

- Status: Accepted `v0.5` target with a documented `v0.4` implementation
  baseline
- Date: 2026-09-03

The current parser accepts only the version 1 schema documented below. The
`v0.5` model-role, permission, data-source, Session-memory, and execution
settings are accepted product semantics but do not yet have a usable YAML or
environment schema. That exact schema and its migration must be derived and
tested during implementation; unknown future-looking fields remain rejected.

Kupilot uses one process-frozen Home for its automatically managed local files.
`KUPILOT_HOME` selects an absolute, normalized directory; otherwise Kupilot
uses `$HOME/.kupilot`. The fixed layout is:

| Purpose | Path below Home |
| --- | --- |
| Local configuration | `config.yaml` |
| SQLite database | `state/kupilot.db` plus SQLite sidecars |
| Cache | `cache/` |
| Bounded operational log | `logs/kupilot.log` plus rotations |

There are no XDG, macOS Library, working-directory, or repository-relative
storage locations. The version 1 schema has no configurable path fields. An
explicit Session-summary export remains the only user-confirmed Kupilot write
outside Home.

## Current first start and interactive model setup

A bare `kupilot` starts the TUI without requiring a configuration file, model
endpoint, model identifier, or API key. When any model requirement is missing,
the sole composer opens a fixed setup flow:

1. Enter the OpenAI-compatible endpoint.
2. Enter the model identifier.
3. Choose `save` or `session`. Empty input selects `save`.
4. Enter the API key in masked mode.

Each step keeps a field label directly above the composer after typing replaces
the placeholder. `Ctrl+C` or `Esc` cancels an editable step. During in-flight
runtime construction, either key requests cancellation and keeps the current
runtime unless the replacement has already crossed its disclosed commit point.

`save` discloses that the key will be plaintext and not encrypted, then
atomically writes the effective typed settings and key to
`KUPILOT_HOME/config.yaml`. `session` keeps the key only in the current Kupilot
process. `/model` repeats setup. Reconfiguration cancels and joins an active
AgentRun, constructs one replacement runtime, invalidates consent if the
canonical model origin changed, and closes the prior runtime after the swap.

Adapter construction is local and network-free. The endpoint's complete stream
and structured Tool compatibility is checked by the first consented model
request; Kupilot does not probe, auto-detect, route, or fall back to another
provider.

For `v0.5`, the setup flow becomes role-aware: it must always produce one named
`agent` profile and may produce one `approval_reviewer` profile. Each profile
explicitly binds one `openai_compatible` model identifier, canonical origin,
opaque credential reference, finite evidence-based limits, and exactly one
consumer role. A Reviewer uses its own role-bound profile; that profile may
explicitly repeat the Agent profile's origin, model, or credential reference,
but it is not the `agent` profile and neither case is fallback or routing.
Summarization alone reuses `agent` with a separate budget and does not create a
`context_compactor` profile.

## Precedence and file selection

Non-sensitive settings use this precedence, highest first:

1. Explicit CLI options.
2. Admitted environment variables.
3. The selected YAML file.
4. Code-defined defaults.

The model credential uses `KUPILOT_MODEL_API_KEY` over an optional
`model.api_key` file value. There is no credential-valued CLI option.

`--config PATH` selects an explicit file. `KUPILOT_CONFIG_FILE` is used when
the CLI option is absent. Both must be absolute, normalized paths. Otherwise
Kupilot reads `KUPILOT_HOME/config.yaml`. A missing Home file is allowed; a
missing explicit file is an error.

Explicit files are read-only sources. Interactive setup always writes the fixed
Home file and never overwrites a `--config` or `KUPILOT_CONFIG_FILE` target.
`help`, `version`, and `cache clear` return before configuration or ordinary
business initialization.

Each load uses an independent parser. Unknown fields, duplicate or wrongly
typed values, unsupported enums, files larger than 64 KiB, additional YAML
documents, nulls, aliases, and merges are rejected. A selected file must be a
regular non-symlink file. Wider existing Unix permissions are accepted; Kupilot
does not chmod or chown the file and warns that it may contain a plaintext key.

## Accepted `v0.5` configuration semantics

The future versioned schema must represent these project-owned concepts without
generic maps or extension payloads:

- named model profiles and fixed `agent` and optional `approval_reviewer` role
  bindings;
- one canonical origin and opaque credential reference per profile, with
  role/origin/category consent and no auto-detection, fallback, router, load
  balancer, or cross-origin retry;
- permission profiles `read-only`, `ask`, `auto-review`, `full-access`, and
  `custom`, with `ask` as the default and explicit high-risk confirmation for
  full access or custom critical-auto rules;
- exact versioned capability policy for built-ins and admitted CRDs, optional
  Prometheus/Loki sources, sensitive reads, Pod diagnostics, diagnostic Pods,
  typed remediation, restricted local argv, and the separate shell class;
- exact Secret-metadata, ConfigMap-key, non-credential environment, and other
  sensitive-source policy, with independent data-category and sink controls;
- exact executable, argv/flag, fixed working directory, environment allowlist,
  image, path, destination, data/sink, timeout, output, and RBAC-related policy
  where a capability needs it;
- Session memory mode `standard` or `minimal`, summary/coverage retention, and
  independent Agent, Reviewer, summary, capability, process, time, byte, item,
  stream, and cost budgets; and
- current namespace access, kubeconfig exec-credential policy, logging, Home,
  and credential protections.

Default-off high-risk capabilities remain disabled even under `full-access`.
Permission profiles route only admitted and enabled operations. Configuration
cannot lower deterministic risk, override `deny`, grant Kubernetes RBAC,
restore a Session rule, or bypass consent, audit, target revalidation, finite
budgets, or scope and policy generations.

Exact YAML names, nesting, environment variables, migration behavior, model
token/context limits, stream limits, summary thresholds, and endpoint-specific
values are deliberately not invented here. They require the pinned Eino/OpenAI
component source and tests, selected endpoint evidence, strict parser and
credential-spike results, and deterministic compatibility fixtures. Until that
work lands, `config.example.yaml` remains a copyable example of the implemented
version 1 parser only.

## Implemented version 1 fields

The complete YAML schema is shown in
[the example configuration](../config.example.yaml). The tracked example omits
an actual key so it remains safe to copy and inspect.

<!-- markdownlint-disable MD013 -->

| Field | Default and validation |
| --- | --- |
| `version` | Required schema version `1`. |
| `context` | Empty; when set, at most 253 UTF-8 bytes with no control or bidirectional-control characters. When empty, startup uses the last successfully verified local Context, then kubeconfig `current-context`. |
| `namespace` | `default`; when set, one working-Namespace DNS label of at most 63 bytes. It is never an all-Namespace marker. |
| `no_color` | `false`; `--no-color` overrides it, while the presence of `NO_COLOR` supplies `true` at environment priority. |
| `runtime.budget_profile` | `balanced`; accepted values are `compact`, `balanced`, and `extended`. The profile is frozen into each run and cannot be expanded by model output. |
| `model.provider_kind` | Fixed to `openai_compatible`. |
| `model.endpoint` | Empty until configured; HTTPS is required except for explicit loopback HTTP. |
| `model.model` | Empty until configured; 1–128 ASCII letters, digits, `.`, `_`, `-`, `/`, or `:`. |
| `model.api_key` | Optional plaintext credential. It is extracted before Viper and is never part of the ordinary typed `Config` value. |
| `model.reasoning_effort` | Omitted by default; `none` is the only admitted explicit value. Use it when a reasoning model must disable reasoning to combine Chat Completions with function Tools. |
| `model.temperature` | `0.1`; accepted range `0` through `0.2`. |
| `model.max_output_tokens` | `8192`; accepted range `1` through the same code-defined `v0.4` ceiling. This is an implemented compatibility value, not a universal `v0.5` role or endpoint limit. |
| `model.request_timeout_seconds` | `300`; accepted range `1` through `300`. The active budget profile and remaining run time apply a smaller per-call deadline when required. |
| `model.streaming` | Fixed to `true`. |
| `model.tool_calling_required` | Fixed to `true`. |
| `kubernetes.exec_credentials` | `allow`; may be set to `deny`. It never selects or supplies a command. |
| `kubernetes.namespace_access` | `all`; may be tightened to `current`. `all` permits explicit cross-Namespace and all-Namespace reads in the same Context only when RBAC also permits them. |
| `logging.enabled` | `true`; may be disabled. |
| `logging.level` | `info`; `warn` and `error` are also accepted. Debug logging is unavailable. |
| `logging.sensitive_diagnostics` | `false`; when explicitly enabled, terminal model failures may add bounded endpoint, model, provider-error, and full Go stack details to the local log. |

<!-- markdownlint-enable MD013 -->

Each supplied endpoint or model identifier is validated independently. If the
effective endpoint, model identifier, or API key is absent, Kupilot opens the
interactive model setup flow to complete the profile.

The admitted environment variables are:

- Home and file selection: `KUPILOT_HOME`, `KUPILOT_CONFIG_FILE`.
- Scope, runtime, and rendering: `KUPILOT_CONTEXT`, `KUPILOT_NAMESPACE`,
  `KUPILOT_BUDGET_PROFILE`, `KUPILOT_NO_COLOR`, and `NO_COLOR`.
- Model: `KUPILOT_MODEL_ENDPOINT`, `KUPILOT_MODEL`,
  `KUPILOT_MODEL_API_KEY`, `KUPILOT_MODEL_REASONING_EFFORT`,
  `KUPILOT_MODEL_TEMPERATURE`, `KUPILOT_MODEL_MAX_OUTPUT_TOKENS`, and
  `KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS`.
- Kubernetes: `KUPILOT_EXEC_CREDENTIALS` and
  `KUPILOT_NAMESPACE_ACCESS`.
- Logging: `KUPILOT_LOG_ENABLED` and `KUPILOT_LOG_LEVEL`.

The CLI exposes only `--config`, `--context`, `--namespace`, and `--no-color`
as startup overrides. It has no API-key, token, kubeconfig-content, arbitrary
header, TLS-bypass, redirect, or command value option.

The remembered Context is not another configuration-precedence source and is
not written to a selected YAML file. It is a bounded global preference in the
Home SQLite database. An effective CLI, environment, or file `context` always
wins. Each startup resolves the chosen name against the current kubeconfig and
verifies the exact Namespace before it becomes an active scope.

## Home and permission behavior

`KUPILOT_HOME` is resolved before ordinary configuration loading and frozen for
the process. An existing Home symlink is canonicalized once; symbolic links
below that canonical Home are not followed for managed files.

On supported Unix platforms Kupilot applies these modes only when it creates a
path:

- New Home, `state`, `cache`, and `logs` directories: `0700`.
- New configuration, database, SQLite sidecar, cache, and log files: `0600`.

Existing user-managed directory and regular-file modes are respected, even
when they include group or other bits. Kupilot does not silently tighten them
and does not make an exact mode an availability requirement. The Home and
selected configuration checks may emit a bounded warning. Non-regular managed
targets, unsafe links below Home, path replacement, and actual I/O failures are
still rejected at the affected boundary. Local logging degrades visibly to a
disabled sink; required durable storage remains a fail-closed run gate.

These modes do not provide encryption, protection from another process running
as the same user, or forensic deletion. Windows remains experimental; Kupilot
does not claim equivalent Unix mode enforcement there.

Because no version has been released with the retired layout, Kupilot performs
no legacy path discovery, schema compatibility read, or data migration. The
configuration schema remains version 1.

## Model API key boundary

The API key may come from masked TUI input, `model.api_key` in the selected
file, or `KUPILOT_MODEL_API_KEY`. The environment value has highest credential
precedence. Kupilot reads it once into an opaque wrapper and removes the entry
from its own process environment; it never writes an environment-sourced value
back to disk automatically.

The file extractor removes `model.api_key` before Viper sees configuration
bytes. The ordinary typed configuration, Domain values, TUI transcript and
history, errors, logs, audit, SQLite, model content, and child-process
environment never contain the key. The guarded model transport uses it only for
the validated same-origin Authorization header. Missing, empty, control-bearing,
space-bearing, or values larger than 4096 bytes are rejected by the credential
boundary.

A locally saved key is deliberately plaintext. It may be exposed by wider
permissions, another same-user process, filesystem inspection, backups, or
snapshots. Kupilot does not claim an encrypted credential store. Removing an
environment entry from the Kupilot process also cannot remove a value exported
by its parent shell.

## Endpoint and transport policy

HTTPS endpoints may use public hosts, private hosts, or private IP addresses and
always use normal certificate and hostname verification. Plain HTTP is accepted
only for the exact `localhost` hostname or a literal loopback address such as
`127.0.0.1` or `::1`.

Endpoint user information, query parameters, fragments, escaped or traversing
paths, invalid ports, and non-HTTP schemes are rejected. There is no insecure
TLS setting. Redirects are allowed only when their canonical scheme, host, and
port match the configured origin; authentication never crosses an origin
boundary.

## Cache clearing

`kupilot cache clear` removes only entries below the canonical fixed `cache`
child. A missing cache is a successful no-op and is not created. The command
does not load YAML or initialize SQLite, logs, Kubernetes, the model, or the
TUI. It never removes Home, `config.yaml`, `state`, or `logs`, and on supported
Unix platforms it deletes through non-following directory handles. A partial
failure is reported honestly; cache clearing is not forensic erasure.

## Local structured log

TUI operation uses a local JSON `slog` file by default and never sends log
records to terminal stdout. The fixed ceilings are:

- At most 1 MiB per file.
- At most three files, including the current file.
- Rotation after seven days, with stale known files pruned when the sink opens.
- At most 12 validated safe attributes in one record; sensitive mode may add
  only its nine fixed bounded diagnostic attributes.

Configuration may disable the sink or raise its minimum level, but cannot
expand these ceilings. The handler admits only code-defined `startup`,
`agent_run`, and `model_request` events and validated scalar fields. Terminal
model failures use `error`, except cancellation at `warn`, and may include the
local request ID, stable error metadata, observed HTTP status, fixed cause
category, and a sink-generated call chain of at most 32 Kupilot function names
and 512 bytes. In the default mode, it does not write arbitrary messages,
caller-provided stacks, raw errors, file names or lines, headers, bodies,
arguments, configuration contents, credentials, or cluster payloads.

Set `logging.sensitive_diagnostics: true` only while diagnosing a model failure.
Terminal `model_request` failures may then add the configured endpoint and
model, a credential-redacted error chain capped at 16 KiB, the first 4 KiB of a
provider error response with a truncation flag, and a current-goroutine Go stack
capped at 64 KiB with file names, line numbers, and a truncation flag. Kupilot
normalizes these external fields and applies its fixed sensitive-value handling
before logging them. It prints a startup warning while this mode is active.
Kupilot does not deliberately attach headers, the model API key, request bodies,
successful responses, streams, prompts, Tool data, or Kubernetes content to the
record. The untrusted provider error and error chain may nevertheless echo user
or cluster content after fixed sensitive-value handling, so the resulting file
must still be treated as sensitive.

Sensitive records use the same local files and seven-day rotation. Disable the
setting after reproduction and remove `logs/kupilot.log` plus its numbered
rotations when the diagnostic copy is no longer needed. Kupilot does not encrypt
these files or remove them when the setting is turned off.

Set `logging.enabled: false` or `KUPILOT_LOG_ENABLED=false` to disable this
sink. This is independent from the container-output category in `/privacy`.
