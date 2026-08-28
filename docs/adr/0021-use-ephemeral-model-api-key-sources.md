# ADR-0021: Use Ephemeral Model API Key Sources

- Status: Superseded by ADR-0035
- Date: 2026-08-08

## Context

This decision is retained as design history. ADR-0035 supersedes its source
restriction before the first public release. The one-shot environment handling,
opaque runtime wrapper, origin binding, and sink exclusions remain applicable;
the prohibition on a local configuration-file source and interactive TUI input
does not.

KuPilot needs one transport credential for the configured model endpoint.
Accepting the value in a CLI flag exposes it to shell history and process
arguments. Storing it in a project configuration file or SQLite turns those
stores into credential stores, conflicts with the retention contract, and makes
examples and support artifacts hazardous.

The MVP does not include cross-platform keychain integration or an encrypted
configuration store.

## Decision

`v0.1` uses `KUPILOT_MODEL_API_KEY` as its primary source and may also accept a
one-shot safe standard-input source selected by a non-secret startup mode.
ModelConfiguration stores only the source category, never the value. A chat
message, ordinary TUI composer, and value-bearing CLI flag are not safe input
sources.

The composition root reads the selected source once during validated
model-adapter construction. After copying an environment value into a
non-renderable credential wrapper, it immediately unsets the environment
variable; child-process filtering remains a second defense. Safe standard input
is bounded, is not echoed, and is closed after the one value. An absent or empty
value produces a stable safe configuration error before any model request. The
wrapper is attached only by the model transport to the validated origin and is
released with the adapter. Application, Domain, Agent messages, Tools, TUI,
errors, audit, and repositories never receive it.

KuPilot will not accept the key through:

- A value-bearing CLI argument, prompt, interactive chat message, or Tool
  argument.
- A KuPilot configuration file, Session, SQLite row, ordinary log, or crash
  bundle.
- A Kubernetes object, kubeconfig field, model response, or endpoint response.

The source variable is unset in the KuPilot process and explicitly absent from
kubeconfig exec credential children and any other external child process.
Sensitive headers are never forwarded to another origin.

Public documentation may name the variable and explain how to supply it, but it
must not include an API key value or credential-shaped example.

## Consequences

Positive consequences:

- The project does not create a durable model credential store.
- The key is absent from command arguments, Session history, and database
  migrations.
- Transport tests can prove a single permitted sink.
- Key rotation is handled by starting a process with a new ephemeral value.

Costs and constraints:

- Environment values can be observed by sufficiently privileged local processes
  and may be captured by external shell or process-management tooling.
- Users must arrange secure environment injection appropriate to their system.
- A key change requires adapter reconstruction or process restart; it is not a
  chat command.
- Native credential managers are not available in `v0.1`.

## Alternatives considered

- A CLI flag was rejected because process listings and shell history commonly
  retain arguments.
- A plaintext configuration file was rejected because KuPilot would own
  permission, backup, parsing, example, and accidental-commit risks for a
  credential store.
- SQLite storage was rejected because the database is explicitly not an
  encrypted vault and retention/deletion would become key lifecycle controls.
- Reusing the TUI composer or chat prompt for a key was rejected because it would
  enter message and rendering paths. A one-shot bounded standard-input source is
  the only admitted alternative to the environment.
- OS keychains were not selected because macOS and Linux integrations and
  headless behavior require a separate cross-platform design.

## Security and privacy impact

An ephemeral source is not proof of endpoint trust. Canonical origin validation,
normal TLS for HTTPS, loopback-only HTTP, informed consent, cross-origin redirect
denial, and safe error mapping remain mandatory.

The key must not implement general formatting or serialization. Error wrapping,
HTTP tracing, Eino callbacks, test snapshots, and debug logging must be reviewed
as possible accidental sinks.

## Validation

Configuration, transport, and child-process tests must prove:

- Missing, empty, oversized, and repeated values fail before network I/O without
  echoing either source.
- A generated synthetic canary appears only in the authentication header sent to
  the configured origin.
- Redirect, error, retry, tracing, and stream-closure paths expose the canary to
  no other transport or sink.
- Exec credential and other child environments omit the variable.
- Formatting, joining, wrapping, configuration serialization, TUI state,
  Application events, audit, and SQLite mappings cannot contain the wrapper or
  value.

## Revisit triggers

- A supported platform keychain design can replace or supplement the environment
  without adding an unsafe fallback.
- Multiple provider credentials are admitted through a new provider ADR.
- The selected model transport cannot prevent credential forwarding or logging.

## References

- [Security Threat Model](../security.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](0020-contain-kubeconfig-exec-credentials.md)
