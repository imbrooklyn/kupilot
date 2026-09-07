# Kupilot Configuration

- Status: Accepted `v0.5` contract with named-model, read, observability,
  remote-diagnostic, and local-execution policy configuration implemented
- Date: 2026-09-07

The current parser writes strict schema version 2 and reads schema version 1
through a deterministic in-memory compatibility migration. Loading never
rewrites a user file; the next explicit interactive save writes version 2 and
does not carry the historical v1 output-token default into the new schema.
Version 2 implements typed `agent` and optional `approval_reviewer` profiles,
exact `kubernetes.resource_policies` entries for approved CRDs, and the two
fixed optional `observability.prometheus` and `observability.loki` slots. It
also implements the default-off exact `kubernetes.remote_diagnostics` policy
for Pod Exec, container-file reads, and diagnostic Pods, and the separate
default-off top-level `local_execution` direct-argv and shell catalogs. Typed
remediation semantics are code-owned and have no generic patch, apply, or
per-operation policy payload. Unknown future-looking fields are rejected.

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
storage locations. The version 2 schema has no configurable path fields. An
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

`save` discloses that the Agent key and any existing file-sourced Reviewer,
Prometheus, and Loki keys will be plaintext and not encrypted, then atomically
writes those named keys and the effective typed settings to
`KUPILOT_HOME/config.yaml`. It never writes an environment-sourced Reviewer or
optional-source key implicitly. `session` keeps the new Agent key only in the
current Kupilot process. `/model` repeats Agent setup.
Reconfiguration cancels and joins an active AgentRun, constructs one
replacement runtime, invalidates Agent consent if the canonical model origin
changed, and closes the prior runtime after the swap.

Adapter construction is local and network-free. The endpoint's complete stream
and structured Tool compatibility is checked by the first consented model
request; Kupilot does not probe, auto-detect, route, or fall back to another
provider.

The loaded configuration always contains one named `agent` profile and may
contain one `approval_reviewer` profile. Each profile explicitly binds one
`openai_compatible` model identifier, canonical origin, opaque credential
reference, finite limits, and exactly one consumer role. A Reviewer may inherit
the Agent origin and selected settings, use the same origin with another model,
or provide another explicit origin. It remains a distinct role and consent
tuple in every case. Summarization reuses `agent` with a separate budget and
does not create a `context_compactor` profile.

## Precedence and file selection

Non-sensitive settings use this precedence, highest first:

1. Explicit CLI options.
2. Admitted environment variables.
3. The selected YAML file.
4. Code-defined defaults.

The Agent credential uses exactly one of `KUPILOT_AGENT_API_KEY` or the legacy
`KUPILOT_MODEL_API_KEY` alias over an optional `models.agent.api_key` file
value. Setting both aliases is an error. A Reviewer bound to its own credential
uses `KUPILOT_APPROVAL_REVIEWER_API_KEY` over
`models.approval_reviewer.api_key`; a Reviewer with `credential_ref: agent`
receives an independently owned opaque clone of the Agent credential. There is
no credential-valued CLI option.

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

## Implemented and remaining `v0.5` configuration semantics

Version 2 implements these project-owned concepts without generic maps or
extension payloads:

- fixed typed `agent` and optional `approval_reviewer` role bindings;
- one canonical origin and opaque credential reference per profile, with no
  auto-detection, fallback, router, load balancer, or cross-origin retry;
- current namespace access, kubeconfig exec-credential policy, logging, Home,
  and credential protections; and
- the existing `compact`, `balanced`, and `extended` run-budget selection,
  now consumed by independent Agent, Agent-summary, Reviewer, and broad
  Kubernetes page/item/byte ceilings;
- an optional finite list of exact CRD read policies. Built-in resource
  policies remain code-owned and cannot be replaced by configuration; and
- an optional exact remote-diagnostic catalog whose presence enables only its
  fixed no-shell argv, application-data roots, pinned image, policy Namespace,
  Service, port, and finite limits; and
- optional exact local direct-argv and separate shell catalogs binding fixed
  paths, structured command semantics, minimal environment, explicit network
  identity, and finite limits.

Public permission-profile configuration and generic remediation settings are
not accepted by the version 2 parser. Optional data-source, remote-diagnostic,
or local-execution configuration constructs only a bounded policy; it cannot
bypass consent, permission, ActionEnvelope, RBAC or path/target revalidation,
audit, or sink gates.

Default-off high-risk capabilities remain disabled even under `full-access`.
Permission profiles route only admitted and enabled operations. Configuration
cannot lower deterministic risk, override `deny`, grant Kubernetes RBAC,
restore a Session rule, or bypass consent, audit, target revalidation, finite
budgets, or scope and policy generations.

Exact endpoint context windows, token accounting, and cost values still require
evidence for the configured endpoint. The fixed byte, message, request, stream,
timeout, and output ceilings are safety bounds and are not claims about an
endpoint's token capacity.

Active-run steering and queued follow-up input add no configuration fields.
Their fixed product controls cannot be widened by YAML, environment, CLI,
model output, or permission profile: at most eight process-local items, 65,536
UTF-8 bytes per item, 262,144 aggregate bytes, one steer claim per model
boundary, and one FIFO auto-drain only after a clean durably completed turn.
`Enter`, `Tab`, and `Alt+Up` remain fixed TUI behavior; there is no runtime
keymap or automatic-retry setting.

Manual compaction, plan mode, queue cancel/clear, copy, and search use fixed
limits and add no configurable authority. `terminal_status_titles` is the sole
new local presentation setting. It defaults to `true`; `false` disables all
title changes. Its fixed title allowlist and teardown behavior cannot be
expanded by configuration. There is no notification command, clipboard
command, continuation, automatic-retry, plan Tool, or compaction-profile
setting.

Session listing/deletion, Last active, local doctor, terminal capability
projection, reverse search, semantic navigation, undo/redo, clarification,
preflight, safe-read reuse, completeness, and recovery add no configuration
fields. Their bounds and authority are compile-time fixed. Terminal
notifications remain disabled; there is no notification command, external
clipboard helper, deletion bypass, retry, continuation, cache TTL, dynamic
command, or per-run budget selector.

## Implemented version 2 fields

The complete YAML schema is shown in
[the example configuration](../config.example.yaml). The tracked example omits
an actual key so it remains safe to copy and inspect.

<!-- markdownlint-disable MD013 -->

| Field | Default and validation |
| --- | --- |
| `version` | Required write schema version `2`; version `1` remains a read-only compatibility input and is migrated in memory without rewriting the file. |
| `context` | Empty; when set, at most 253 UTF-8 bytes with no control or bidirectional-control characters. When empty, startup uses the last successfully verified local Context, then kubeconfig `current-context`. |
| `namespace` | `default`; when set, one working-Namespace DNS label of at most 63 bytes. It is never an all-Namespace marker. |
| `no_color` | `false`; `--no-color` overrides it, while the presence of `NO_COLOR` supplies `true` at environment priority. |
| `terminal_status_titles` | `true`; when enabled on a directly attached, conservatively recognized title-capable terminal, only the fixed `Kupilot`, `Working`, `Approval needed`, `Complete`, and `Failed` titles are emitted through terminal-native control and the prior title slot is restored on teardown. Multiplexers and unknown terminals receive no title sequence. |
| `runtime.budget_profile` | `balanced`; accepted values are `compact`, `balanced`, and `extended`. The profile is frozen into each run and cannot be expanded by model output. |
| `models.agent.name` | Required unique profile name; lowercase letters and digits with internal hyphens, at most 128 bytes. |
| `models.agent.role` | Required fixed value `agent`. |
| `models.agent.credential_ref` | Required fixed value `agent`. |
| `models.agent.provider_kind` | Required fixed value `openai_compatible`. |
| `models.agent.endpoint` | Required key and may be empty until interactive setup; HTTPS is required except for explicit loopback HTTP. |
| `models.agent.model` | Required key and may be empty until interactive setup; non-empty values are 1–128 admitted ASCII bytes. Endpoint and model must be either both empty or both non-empty. |
| `models.agent.api_key` | Optional plaintext credential extracted before ordinary typed configuration decode. |
| `models.agent.reasoning_effort` | Omitted by default; `none` is the only admitted explicit value. |
| `models.agent.temperature` | Required; accepted range `0` through `0.2`. |
| `models.agent.max_output_tokens` | Optional positive value. It is omitted by default and sent only when exact evidence exists for the selected endpoint; it is not inferred from the historical version 1 value. Independent output-byte, stream, call, time, and cost-unit limits always apply. |
| `models.agent.request_timeout_seconds` | Required; accepted range `1` through `300`. The run profile and remaining deadline may tighten it. |
| `models.agent.streaming` | Required fixed value `true`. |
| `models.agent.tool_calling_required` | Required fixed value `true`. |
| `models.approval_reviewer` | Optional typed profile. `name`, `role: approval_reviewer`, `inherit_agent`, and `credential_ref` are always explicit. A non-inheriting profile supplies every non-secret model field. The resolved Reviewer is fixed non-streaming and Tool-free. |
| `models.approval_reviewer.api_key` | Optional plaintext key only when `credential_ref: approval_reviewer`; it conflicts with `credential_ref: agent`. |
| `kubernetes.exec_credentials` | `allow`; may be set to `deny`. It never selects or supplies a command. |
| `kubernetes.namespace_access` | `all`; may be tightened to `current`. `all` permits explicit cross-Namespace and all-Namespace reads in the same Context only when RBAC also permits them. |
| `kubernetes.resource_policies` | Empty by default. Optional exact CRD entries are combined with, but cannot replace, the code-owned built-in catalog. At most 47 configured entries fit beneath the complete 64-entry hard cap. No environment or CLI override exists. |
| `kubernetes.resource_policies[].id` | Required unique model-visible local ID of 1–63 lowercase letters, digits, or internal hyphens. It cannot collide with a built-in ID or API identity. |
| `group`, `version`, `resource`, `kind`, `scope` in a resource policy | Required exact non-core CRD API identity. `scope` is `namespaced` or `cluster`; discovery only verifies this identity and never broadens it. Subresources are rejected. |
| `kubernetes.resource_policies[].verbs` | One or both of `get` and `list`; no write verb is accepted. |
| `kubernetes.resource_policies[].fields` | One to 32 exact scalar projections. Every field names a local ID, dot-separated `metadata`, `spec`, or `status` path, scalar type (`string`, `integer`, `decimal`, `boolean`, or `timestamp`), data class (`metadata`, `status`, `spec`, or `sensitive`), selector source, operators, and an explicit Evidence flag. Credential-, Secret-, token-, password-, API-key-, private-key-, access-key-, and environment-shaped paths must be classified `sensitive`; sensitive fields cannot create Evidence and are not reachable through the broad-read Tools. |
| `selector_source`, `selector_key`, and `operators` in a resource field | `none`, exact Kubernetes `field`, or exact `label`. Field selectors are limited to `metadata.name` or `metadata.namespace` with equality/inequality. Label selectors use one exact configured key and equality, inequality, or existence. Other declared operators are evaluated locally over the already admitted scalar projection. Raw selectors and expressions are absent. |
| `kubernetes.resource_policies[].limits` | Required positive `max_pages`, `page_items`, `page_bytes`, `max_items`, `max_bytes`, and `max_returned`. Hard caps are 8 pages, 100 items/page, 1 MiB/page, 500 scanned items, 4 MiB total, and 50 returned items. The frozen run profile may tighten every value. |
| `kubernetes.remote_diagnostics` | Omitted by default, which disables all three remote capabilities. It accepts only the fixed `pod_exec`, `container_file`, and `diagnostic_pods` fields. There is no environment or CLI override. |
| `kubernetes.remote_diagnostics.pod_exec[]` | At most 32 total Pod-exec and diagnostic-Pod entries. Each unique ID binds `class`, one normalized absolute non-shell `executable`, one exact non-empty non-credential argv vector of at most 16 entries and 4096 aggregate bytes, and finite limits. Direct shells and explicit shell dispatch through common executable multiplexers are rejected. `predefined` accepts only the code-owned `dns-config` or `process-status` contract; `general` is always `critical` because the configured executable may have in-container side effects or network behavior that Kupilot cannot infer or sandbox. The envelope binds the exact argv and conservative remote-Pod network effect. The model must repeat the selected executable and argv exactly and cannot add stdin, TTY, shell, deadline, or ceilings. |
| `kubernetes.remote_diagnostics.container_file` | Optional single reader policy. `reader_executable` is exactly `/bin/tar` or `/usr/bin/tar`; one through eight normalized absolute application-data roots are required. Root `/`, traversal, trailing slash, credential roots, ServiceAccount-token roots, `/proc`, `/sys`, `/dev`, and other code-denied system roots are rejected. Runtime uses fixed USTAR arguments with a one-block record size, rejects sensitive Pod mounts and non-regular archive entries, and derives a smaller file-content limit after reserving deterministic archive headers, padding, and terminators inside `max_bytes`. |
| `kubernetes.remote_diagnostics.diagnostic_pods[]` | Each unique local ID fixes a valid Namespace, same-Namespace Service name and port, one SHA-256 digest-pinned image, and the code-owned TCP-connect `/bin/nc -z -v -w 5` prefix. Image repository components are lowercase; an optional bounded tag is allowed, while URL schemes, user information, query/fragment text, percent escapes, empty path components, and invalid registry ports are rejected. The Service must retain a non-empty selector at both resolution and final pre-create revalidation; ExternalName, selectorless, obvious metadata/link-local/address-confusion, and policy-external targets are rejected. Namespace, image, argv, destination, Pod name/spec, ServiceAccount handling, and resources are absent from the Tool schema. `network_policy_required` must be `true`; this is an operator assertion, not runtime evidence that a compatible CNI enforces egress isolation. |
| Remote `timeout_seconds`, `max_lines`, and `max_bytes` | Required positive values capped at 60 seconds, 1000 lines, and 65536 bytes. The immutable run Tool deadline, log limits, and result-byte limit can only tighten them. For a container-file read, `max_bytes` bounds the complete USTAR transport and the envelope separately binds the derived content limit. A diagnostic-Pod timeout must be at least six seconds. |
| `local_execution` | Omitted by default, which disables both local process classes. It accepts only `commands` and `shells`, each with at most 32 exact entries and no environment or CLI override. An entry does not grant permission or claim an OS sandbox. |
| `local_execution.commands[]` | A unique bounded policy ID binds one `kind` (`kubectl`, `helm`, `argocd`, or `diagnostic`), normalized absolute executable and working directory, one structurally classified argv vector of at most 16 entries/4096 bytes, an ordered minimal environment, credential-reference identity, optional exact origin/effect, and finite limits. Kubectl/Helm/Argo entries accept only their closed operation shapes and deny Context, kubeconfig, token, impersonation, server, TLS, values-file, config, credential, plugin/post-renderer, and stdin override surfaces. Diagnostic entries declare `no_network_read`, `external_read`, or `external_side_effect`; known shells, interpreters, wrappers/multiplexers, and executable scripts are denied. |
| `local_execution.commands[].environment` | Zero to eight unique entries totaling at most 2048 bytes. Only `LANG` or `LC_ALL` set to `C`, `POSIX`, or `C.UTF-8`, and `NO_COLOR=1`, are admitted. The child inherits no `HOME`, `PATH`, proxy, model, kubeconfig, debug, or credential variable. |
| `local_execution.commands[].credential_ref` and `server_origin` | `argocd` requires the opaque identity `argocd_cli_profile` and one canonical explicit HTTPS origin (loopback HTTP only); other command kinds require `none`. No credential value, file path, environment variable, CLI argument, or Authorization material is injected by this policy. |
| `local_execution.shells[]` | A wholly separate `critical` entry binding one admitted absolute shell executable, one exact trimmed command string of at most 4096 bytes, exact cwd/environment, explicit `none`, `kubernetes_api`, or `external` network effect, optional canonical external origin, and finite limits. A direct-argv entry cannot select or fall back to it. |
| Local `timeout_seconds`, `max_lines`, and `max_bytes` | Required positive values capped at 600 seconds, 10000 lines, and 4 MiB. Runtime binds the same byte ceiling as combined process output and never accepts a model-selected limit. |
| `observability.prometheus` | Optional fixed Prometheus slot. Its `endpoint` must be one canonical HTTPS origin or an explicit loopback HTTP origin, with no path, user information, query, or fragment. |
| `observability.prometheus.credential_ref` | `none` or `prometheus`. The latter requires exactly one plaintext file key or `KUPILOT_PROMETHEUS_API_KEY`; the value is extracted into an opaque source-owned wrapper. |
| `observability.prometheus.queries` | One or more unique code-owned IDs: `pod_cpu_usage`, `pod_memory_working_set`, `pod_network_receive_rate`, or `pod_network_transmit_rate`. Raw PromQL is not a schema field. |
| `observability.loki` | Optional fixed Loki slot with the same canonical-origin rule and no implicit fallback from Kubernetes logs. |
| `observability.loki.credential_ref` | `none` or `loki`. The latter requires exactly one plaintext file key or `KUPILOT_LOKI_API_KEY`; credentials never enter ordinary Config values. |
| `observability.loki.queries` | One or more unique code-owned IDs; the implemented ID is `pod_logs`. Raw LogQL and regex are not schema fields. |
| `observability.*.request_timeout_seconds` | Required integer from 1 through 60. The frozen run deadline and source budget may tighten it. Redirects are always denied. |
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
- Agent model settings: role-named `KUPILOT_AGENT_ENDPOINT`,
  `KUPILOT_AGENT_MODEL`, `KUPILOT_AGENT_REASONING_EFFORT`,
  `KUPILOT_AGENT_TEMPERATURE`, `KUPILOT_AGENT_MAX_OUTPUT_TOKENS`, and
  `KUPILOT_AGENT_REQUEST_TIMEOUT_SECONDS`. Their respective legacy aliases are
  `KUPILOT_MODEL_ENDPOINT`, `KUPILOT_MODEL`,
  `KUPILOT_MODEL_REASONING_EFFORT`, `KUPILOT_MODEL_TEMPERATURE`,
  `KUPILOT_MODEL_MAX_OUTPUT_TOKENS`, and
  `KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS`; setting both names for one field is
  an error.
- Reviewer model settings, admitted only when the file declares the optional
  profile: `KUPILOT_APPROVAL_REVIEWER_ENDPOINT`,
  `KUPILOT_APPROVAL_REVIEWER_MODEL`,
  `KUPILOT_APPROVAL_REVIEWER_REASONING_EFFORT`,
  `KUPILOT_APPROVAL_REVIEWER_TEMPERATURE`,
  `KUPILOT_APPROVAL_REVIEWER_MAX_OUTPUT_TOKENS`, and
  `KUPILOT_APPROVAL_REVIEWER_REQUEST_TIMEOUT_SECONDS`.
- Model credentials: `KUPILOT_AGENT_API_KEY`, legacy Agent alias
  `KUPILOT_MODEL_API_KEY`, and `KUPILOT_APPROVAL_REVIEWER_API_KEY`.
- Kubernetes: `KUPILOT_EXEC_CREDENTIALS` and
  `KUPILOT_NAMESPACE_ACCESS`. Remote-diagnostic policy has no environment
  override. Local-execution policy likewise has no environment or CLI override;
  its configured child environment is not inherited from this list.
- Optional source credentials: `KUPILOT_PROMETHEUS_API_KEY` and
  `KUPILOT_LOKI_API_KEY`. Source origins and query IDs have no environment or
  CLI override.
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

Because no version has been released with the retired filesystem layout,
Kupilot performs no legacy path discovery. This is separate from the supported
schema version 1 to version 2 in-memory configuration migration.

## Credential boundary

An Agent API key may come from masked TUI input,
`models.agent.api_key` in the selected file, or exactly one of
`KUPILOT_AGENT_API_KEY` and its legacy `KUPILOT_MODEL_API_KEY` alias. An
independent Reviewer key may come from
`models.approval_reviewer.api_key` or
`KUPILOT_APPROVAL_REVIEWER_API_KEY`. An enabled optional source with its fixed
credential reference may use `observability.prometheus.api_key` or
`observability.loki.api_key`, overridden by `KUPILOT_PROMETHEUS_API_KEY` or
`KUPILOT_LOKI_API_KEY`. Environment values have highest credential precedence.
Kupilot reads every present key once into a separate opaque wrapper and removes
all admitted key entries from its process environment before validation
completes. It never writes an environment-sourced value back to disk
automatically.

The file extractor removes all four fixed `api_key` fields before the strict typed
configuration decoder sees the bytes. Ordinary typed configuration, Domain
values, TUI transcript and history, errors, logs, audit, SQLite, model content,
and child-process environments never contain any key. Each guarded model or
optional-source transport uses only its bound wrapper for the validated
same-origin Authorization header. Present empty, control-bearing, space-bearing,
ambiguous, or values larger than 4096 bytes are rejected by the credential
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
