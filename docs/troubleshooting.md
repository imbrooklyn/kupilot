# Troubleshooting

The checked-in source now implements strict version 2 named model profiles,
safe Session context/summarization, the five permission profiles, optional
data sources, default-off Pod Exec/diagnostic Pod/local argv/shell policies,
and the typed remediation catalog. These capabilities remain bounded by their
configuration, consent, permission, ActionEnvelope, RBAC, and audit gates. No
release archive or universal endpoint/cluster compatibility is implied.

Kupilot fails closed when configuration, credential, scope, consent, storage,
model, or Kubernetes safety checks cannot be completed. A safe error may omit a
raw vendor message, filesystem path, endpoint body, or cluster payload on
purpose.

Start with non-sensitive command information:

```sh
./bin/kupilot --version
./bin/kupilot --help
./bin/kupilot help resume
./bin/kupilot doctor
```

Do not paste a model API key, kubeconfig, raw Kubernetes response, container
output, local database, or local log into a public support request.

## The binary does not build

- Confirm `go version` is 1.25.0 or newer.
- `make build` uses `CGO_ENABLED=0` and writes `./bin/kupilot`.
- A cold module cache needs access to the configured Go module sources.
- The complete contributor gates require the exact patch release documented in
  [Development and CI Gates](development.md), which is stricter than the
  production module's minimum.

No published archive or package-manager installation is supported. Do not
substitute an unreviewed third-party binary and assume it has the same build or
security properties.

## A configuration file is rejected

Check all of the following:

- An explicit `--config` or `KUPILOT_CONFIG_FILE` value is an absolute,
  normalized path.
- The file is regular, not a symlink, and no larger than 64 KiB. Wider existing
  permissions produce a warning rather than a rejection.
- The document contains `version: 2` with a complete required
  `models.agent` profile, or a supported legacy `version: 1` profile, and no
  unknown, duplicate, null, alias, merge, or second-document content.
- Values use the exact types and bounds in [Configuration](configuration.md).
- Each supplied `models.<role>.endpoint` and `models.<role>.model` value is
  valid. A missing Agent endpoint, model identifier, or API key starts the TUI
  in model-setup mode.

`config.example.yaml` deliberately contains no real credential. Copy it to the
fixed Home configuration or select an absolute external file. Do not add a real
key to the tracked example, repository, fixture, or support report. The schema
has no path fields; all managed local paths derive from `KUPILOT_HOME`.

`help`, `version`, and `cache clear` intentionally work without loading ordinary
configuration, storage, Kubernetes, or the model. Use them to distinguish CLI
parsing or cache maintenance from startup failure.

## Local diagnostics report unavailable state

Run `kupilot doctor` before opening the TUI, or `/doctor` inside an idle TUI.
Both use the same versioned, content-free health vocabulary. The CLI opens only
the fixed Home configuration and SQLite store; it does not construct a model,
Kubernetes client, Tool, Reviewer, approval, child process, or executor. Use
`kupilot doctor --json` for the bounded `kupilot.cli-doctor/v1` projection.

Doctor intentionally omits endpoint text, credentials, managed absolute paths,
Session titles, Messages, Evidence payloads, raw SQL/driver errors, and arbitrary
environment values. A reported unsupported terminal clipboard or title is not
an Agent failure. `Protocol continuation unavailable` means a disconnected
stream remains unknown/recovered and requires a new explicit input; it is not a
request to retry automatically.

## The model API key is missing or rejected

Use masked TUI setup, optional plaintext `models.agent.api_key`, or exactly one
of `KUPILOT_AGENT_API_KEY` and the legacy `KUPILOT_MODEL_API_KEY` alias. A
Reviewer with `credential_ref: approval_reviewer` uses
`models.approval_reviewer.api_key` or
`KUPILOT_APPROVAL_REVIEWER_API_KEY`. An environment value overrides its role's
file value. Each effective value must be non-empty, valid UTF-8 without spaces
or controls, and at most 4096 bytes. It cannot be supplied as a CLI value.

Kupilot reads and removes the entry from its own process environment once. That
does not remove an exported value from the parent shell. If a secret launcher
retries within the same process after the one-shot read, start a fresh Kupilot
process rather than attempting to reuse a consumed source.

If interactive construction fails, the TUI discards the submitted key and asks
for it again. `save` writes disclosed plaintext to
`KUPILOT_HOME/config.yaml`; `session` keeps it in the current process only.

## The model endpoint is rejected or incompatible

The configured endpoint is a base URL. Kupilot sends the model request to:

```text
{configured-endpoint}/chat/completions
```

Use verified HTTPS. Plain HTTP is accepted only for the exact `localhost` name
or a literal loopback address. User information, query strings, fragments,
ambiguous or traversing paths, invalid ports, insecure TLS, method-changing
redirects, and cross-origin redirects are rejected.

An endpoint described by a provider as "OpenAI-compatible" may still be
unsupported. Kupilot requires streamed Chat Completions, SSE, one response
choice, strict structured function Tool calls, indexed argument fragments, and
the supported finish states. It does not fall back to non-streaming responses,
prose-parsed Tool calls, the Responses API, another provider, or another origin.
See [Model Compatibility](model-compatibility.md).

For the official OpenAI API, a compatible configuration example is:

```yaml
models:
  agent:
    provider_kind: openai_compatible
    endpoint: https://api.openai.com/v1
    model: gpt-4o-mini
```

OpenAI documents `gpt-4o-mini` and later models as supporting Structured
Outputs. A third-party relay must preserve the same streaming function-call and
strict-schema behavior; using an OpenAI model name through a relay does not by
itself establish compatibility. See the official
[Structured Outputs guide](https://developers.openai.com/api/docs/guides/structured-outputs).

Some reasoning models default to a reasoning mode that a Chat Completions relay
does not combine with function Tools. When the provider explicitly requires
reasoning to be disabled, configure:

```yaml
models:
  agent:
    reasoning_effort: none
```

Kupilot then sends `reasoning_effort: "none"` on every model request. It omits
the field by default, accepts no other value, and never guesses from the model
name. Official OpenAI documentation lists `none` as supported for
[`gpt-5.6-luna`](https://developers.openai.com/api/docs/models/gpt-5.6-luna)
and documents the Chat Completions field in its
[model guidance](https://developers.openai.com/api/docs/guides/latest-model).

HTTP status text and endpoint response bodies are deliberately not echoed in
the TUI or default logs. Check the configured model identifier, provider-side
authorization and quota, TLS trust, and provider documentation without copying
sensitive responses into Kupilot configuration or public reports.

When local logging is enabled, inspect
`KUPILOT_HOME/logs/kupilot.log` (`~/.kupilot/logs/kupilot.log` by default) and
find the most recent terminal `model_request` record. Its local request ID
correlates the start and terminal records inside the log. The terminal record
reports the stable class and code, retryability, observed HTTP status when
available, fixed cause category, and a bounded Kupilot function-name call
chain. For example, HTTP 400 with `cause: http_status` and
`error_class: unsupported` means the endpoint rejected the admitted streaming
and Tool contract; it does not mean the network is unavailable. The log never
contains the provider body, Authorization header, key, URL, question, cluster
content, file paths, line numbers, or raw error in its default mode. No model
record is expected when project-owned request validation rejected the request
before the Eino boundary entered its transport. `cause: transport_validation`
without an HTTP status means an SDK or local transport constraint rejected the
call before network I/O;
`cause: transport_unavailable` without a status means the guarded transport was
entered but no valid HTTP response was observed.

HTTP 200 with `cause: stream_protocol`, `error_class:
invalid_external_response`, and `error_code: model_stream_invalid` means the
endpoint returned SSE but the decoded stream violated the bounded contract.
Kupilot accepts inert empty deltas and interleaving between distinct bounded
Tool indexes. It also accepts bounded commentary before or alongside a Tool
selection when that response terminates with `tool_calls`; the commentary is
discarded and cannot authorize a Tool. Kupilot still rejects missing or
non-contiguous indexes, incomplete calls, Tool calls completed with `stop` or
`length`, unsupported finish states, and data after terminal state.
Default logs intentionally do not retain the raw chunk or its content.

For a private, short-lived reproduction, add:

```yaml
logging:
  sensitive_diagnostics: true
```

Restart Kupilot and reproduce the failure. The terminal model record may then
include `sensitive_endpoint`, `sensitive_model`, `sensitive_error_chain`,
`sensitive_provider_error_body`, and `sensitive_call_stack`. Error, provider-
body, and stack fields have fixed size ceilings and explicit truncation flags.
External fields are normalized and pass fixed sensitive-value handling. The
model credential and Authorization remain excluded, but a provider error may
still echo user or cluster content. Do not post this log publicly. Disable the
setting after reproduction and remove `kupilot.log` plus numbered rotations
when the diagnostic copy is no longer needed.

For adapter-owned validation failures, `sensitive_error_chain` may contain a
code-defined stage such as `tool_index`, `finish_content_mismatch`, or
`tool_assembly`. These labels identify the rejected structure without logging
the SSE payload.

## The model returned an invalid Agent response

This error means the endpoint completed an accepted stream, but the final
assistant content did not satisfy the strict final-response envelope for
`answer_markdown`, Evidence citations, and typed proposed actions. Kupilot
rejects unexpected fences or commentary outside the envelope, missing or null
required fields, unknown or duplicate keys, invalid enum values, trailing
content, and malformed JSON. Partial or invalid model content is not persisted.

Use a current Kupilot build whose System Prompt includes the exact final JSON
shape and enum values. If the error persists through a relay, verify that the
relay serves the configured model without injecting prose or rewriting the
assistant content. Switching only the model name cannot make such rewriting
compatible; test the official endpoint when organizational policy permits.

## Context or Namespace cannot be activated

- Confirm the intended Context exists in the kubeconfig selected by client-go's
  normal `KUBECONFIG` or per-user precedence.
- Kupilot rejects kubeconfig server URLs using plain HTTP, user information,
  queries, fragments, insecure TLS, redirects, custom transports, or legacy auth
  providers.
- Context selection needs an exact Namespace `get`. `/namespace` completion also
  needs Namespace `list`; use the narrower configured-Namespace flow if listing
  Namespace names is not allowed.
- Empty Namespace input never means all Namespaces.
- A failed Context activation invalidates the attempted generation and does not
  silently restore the previous client. Explicitly choose a valid Context again.

If kubeconfig declares exec credentials, the default is `allow`. Set
`kubernetes.exec_credentials: deny` for strict no-launch behavior. Under `deny`,
a Context that requires exec authentication fails before process launch. Under
`allow`, Kupilot launches only the kubeconfig-declared executable directly,
without a shell, and cannot guarantee the program's own network or filesystem
behavior.

See [Kubernetes Compatibility](kubernetes-compatibility.md) for the supported
API-server minors and kubeconfig policy.

## A Kubernetes observation is forbidden or partial

Compare the identity's permissions with [Least-Privilege RBAC](rbac/README.md).
The common optional gaps are:

- Event `list` is absent, so Event-based Evidence is unavailable.
- `pods/log` `get` is absent, so current or previous container output is
  unavailable.
- EndpointSlice `list` is absent, so Service endpoint readiness counts are
  unavailable.
- One related resource's `get` or `list` is absent, so that relationship branch
  is partial.

Kupilot does not retry with a broader identity, resource, selector, Namespace,
or limit. Do not solve a narrow denial by granting `cluster-admin`. A partial or
forbidden observation belongs in Diagnosis missing information.

## A question asks for Nodes, Namespaces, or cluster-wide inventory

The current catalog supports bounded Namespace and Node projections through
`get_cluster_overview` and direct typed reads. If they are unavailable, inspect
`/status`, confirm that the intended kubeconfig identity has the
`kupilot-cluster-observer` permissions, and check the safe permission result.

All-Namespace lists of namespaced resources additionally require
`kubernetes.namespace_access: all` and matching cluster-wide namespaced RBAC.
The `current` policy denies them before Kubernetes I/O. Kupilot still does not
provide an unbounded inventory dashboard, API discovery, arbitrary Kind, or
cross-cluster view; narrow the question to one admitted Kind or use the bounded
cluster overview.

## Pod logs are not used

There are two independent log controls:

1. The `/privacy` container-output category controls whether the two fixed Pod
   log Tools may read and send processed container-output facts. It is disabled
   by default. Toggling it invalidates prior consent; accept the new exact tuple
   before another request.
2. `logging.enabled` controls the small local operational file. Disabling it has
   no effect on Pod log Tool authorization.

Even when container output is enabled and RBAC permits it, Kupilot can return a
gap when the Pod or container is missing, a multi-container request is
ambiguous, no previous instance exists, the container is not running, the
result is sensitive-blocked, or a time, line, byte, or run-call limit is reached.

## Consent is repeatedly required

Consent is valid only for the exact policy version, model role, canonical
endpoint-origin hash, and enabled category set. It is expected to become
pending after any profile, role, origin, category or meaning change, after a
sensitive-category toggle, after revocation, or after local state removal.

Rejecting or cancelling the dialog sends no pending question. If accepting the
unchanged tuple does not persist, inspect the safe storage failure and owner-only
creation or managed-path requirements; wider existing modes alone are not a
denial. Kupilot fails closed rather than treating an unsaved decision as
consent.

## Resume is unavailable

Only standard-persistence Sessions with at least one committed safe Message are
eligible. The three valid forms are picker, exact UUIDv7 ID, and `--last`.
Minimal, empty, archived, deleted, corrupt, and otherwise ineligible history is
not offered. Exact lookup of a known minimal Session reports
`session_not_resumable`; other unavailable identifiers use a non-disclosing
result.

Resume never falls back to a new Session. Picker cancellation exits a top-level
resume; `/resume` cancellation inside the TUI keeps the current Session. There
is no cwd, repository, Context, Namespace, or `--all` filter.

Use `kupilot sessions list` for bounded content-free discovery. `/sessions`
opens the same metadata in the single-screen picker. `Last active` is the
authoritative activity field; listing, viewing, resume alone, status, doctor,
export, and deletion preview do not update it. A corrupt or future activity
time is protected rather than silently selected for deletion.

Current-Session `/delete` is unavailable while a run, commit barrier, Reviewer,
approval, execution, or verification is active; deletion never cancels that
work. For automation, first run a non-interactive `sessions delete --dry-run`,
then supply the returned digest with the resolved absolute RFC3339 cutoff and
`--confirm`. Any changed snapshot is stale and deletes nothing. Logical deletion
does not remove exports, terminal scrollback, logs, backups, configuration,
credentials, cache, or SQLite free pages.

## A resumed Session shows a scope conflict

Historic Context and Namespace values are unverified candidates. When the saved
candidate differs from current independently verified authority, Kupilot opens
the ordinary Context or Namespace picker. Select one exact candidate to perform
normal Context construction and Namespace verification. `Esc` cancels resume;
there is no silent keep-current fallback. If current authority already matches
the historic candidate exactly, Kupilot can accept it without another
Kubernetes request.

A saved ResourceRef is cleared and revalidated only after the chosen scope is
active. Historic Evidence remains display-only. See
[Sessions and Scope](user-guide/sessions-and-scope.md).

## SQLite or local logging is unavailable

Confirm `KUPILOT_HOME` is an absolute, normalized, non-root directory and that
its fixed `state` and `logs` children have the expected file types. Kupilot
rejects symlinked managed descendants and non-regular database, sidecar, or log
targets. It assigns `0700`/`0600` only to newly created paths on supported Unix
platforms; wider existing user-managed modes are accepted and left unchanged.

A local log failure is reported and logging is disabled for that process. A
required SQLite failure still stops startup or blocks a new durable run.

An unknown, incompatible, checksum-mismatched, or corrupt database is not
deleted, overwritten, renamed, or recreated automatically. Preserve it if
recovery is required. If all local history may be discarded, quit every Kupilot
process and follow the exact-file cleanup boundary in
[Privacy and Local Data](user-guide/privacy-and-local-data.md).

A failed durable run start prevents model and Tool I/O. A later persistence
failure may allow the current in-memory answer to finish with a
visible degraded state, but Kupilot does not claim the missing turn is resumable
and does not start another run until storage is healthy.

## A `v0.5` permission or capability is unavailable

First confirm that the feature has been implemented; an Accepted ADR is not an
availability claim. Once implemented, `/status` and `/permissions` must show
the active capability version, technical enablement, permission profile,
deterministic risk, both generations, Reviewer availability, RBAC result,
consent, and remaining budget without external I/O.

`ask` is the default. `read-only` cannot be approved into a mutation, Pod Exec,
diagnostic Pod, local process, or shell. `auto-review` can route only `review`;
`critical` remains human-reviewed. `full-access` does not enable default-off
capabilities, grant Kubernetes RBAC, broaden scope or consent, or override a
hard denial. Do not solve a denial by selecting full access or granting
`cluster-admin` without identifying the exact missing layer.

A missing, unconsented, over-budget, timed-out, malformed, or stale Reviewer
must yield no action and no implicit fallback to the Agent or another endpoint.
The user may explicitly choose human review for the same still-fresh envelope;
changed or stale input requires a new envelope.

Pod Exec, diagnostic Pods, local argv, and shell have separate policies. Check
the exact Pod/container/path or image/destination, executable and argv, stdin/
TTY/shell flags, output/time bounds, and RBAC. An OS sandbox does not establish
remote Pod or Kubernetes safety, and an image allowlist does not establish
NetworkPolicy enforcement.

## A `v0.5` action outcome is unknown

An external timeout, cancellation, transport failure, or cleanup uncertainty
after the request may have left the operation applied. Kupilot records this as
ambiguous or unknown and consumes the one-attempt authority. It never retries
the execution automatically. Use an independently authorized bounded read to
inspect current state, then create a fresh ActionEnvelope if another operation
is still needed.

API acceptance, progress, verified completion, failure, timeout, cleanup, and
verification unavailable are separate states. A successful API response does
not prove rollout or remediation success, and a failed verification does not
rewrite an accepted request as unattempted.

## `v0.5` Session compaction is blocked or degraded

Each later AgentRun must receive direct eligible Messages that fit or one safe
summary plus the complete eligible recent tail. Minimal mode sources that
context only from the current process and does not persist model memory. If
summary coverage is missing, corrupt, stale, out of order, or cannot be
persisted, Kupilot must not guess, duplicate the current question, omit eligible
history, or send oversized or silently truncated context. When compaction is
required, the turn remains blocked or visibly degraded, no current-question-
only model call is made, and the last committed Session state is preserved.

Explicit resume itself performs zero model, Kubernetes, Tool, Reviewer,
approval, process, or executor I/O. The next question transmits the required
safe history only after current consent, scope, policy, coverage, and budget
gates pass; a failed gate causes zero model calls. Historic scope, Evidence,
permissions, Session rules, Reviewer decisions, ActionEnvelopes, approvals, and
execution never regain authority.

## Terminal rendering is unreadable or lacks color

Use `--no-color`, `NO_COLOR`, or `no_color: true`. Scope, consent, supervised
access, partial, denied, approval, and execution meaning does not rely on color.

Kupilot requires an interactive UTF-8-capable terminal. It strips or visibly
replaces unsafe escape, device-control, bidirectional-control, invalid UTF-8,
and oversized external text. Raw logs and raw object output are not available as
an alternate view.

## Network activity is unexpected

Kupilot has no telemetry, analytics, crash reporting, update checker, hosted
account, or Kupilot-operated control plane. Expected destinations, when their
exact capabilities are enabled, are:

- The Kubernetes API server for the explicitly selected kubeconfig Context.
- Each explicitly configured model-role origin after role-bound consent.
- Explicit optional Prometheus or Loki origins after source policy and consent.
- A kubeconfig-declared exec credential program, when present and allowed; that
  separate program may make its own connections.
- A policy-selected local argv process, Pod Exec target, or diagnostic Pod
  network target after its own permission and ActionEnvelope gates.

Stop Kupilot and report privately under [SECURITY.md](../SECURITY.md) if the
Kupilot process itself contacts another destination, forwards Authorization
across origin, reads across Namespace outside the displayed `all` policy, or
performs an external action without the exact permission, ActionEnvelope,
durable pre-operation audit, and one-attempt flow.

## Exit codes

The CLI uses these stable process categories:

| Code | Meaning |
| ---: | --- |
| `0` | Help, version, cache clearing, or the interactive flow completed successfully. |
| `1` | Safe startup or runtime failure. |
| `2` | Invalid command, option, or argument. |
| `69` | The selected new or resume start path is unavailable. |
| `130` | Interrupted or cancelled process start. |

Raw adapter errors are not encoded into exit status or printed to the terminal.
