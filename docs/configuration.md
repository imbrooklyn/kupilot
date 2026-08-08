# KuPilot Configuration

KuPilot loads one typed, non-sensitive YAML configuration. Configuration is
resolved in this order, from highest to lowest priority:

1. Explicit CLI options.
2. Admitted environment variables.
3. The selected YAML file.
4. Code-defined defaults.

Each load uses an independent parser instance. Unknown fields, duplicate or
wrongly typed values, unsupported enum values, and files larger than 64 KiB are
rejected. A file must contain at most one YAML document; null, alias, and merge
values are not accepted. KuPilot does not write, watch, or reload configuration
files.

## Configuration file selection

`--config PATH` selects an explicit file. `KUPILOT_CONFIG_FILE` is used when the
CLI option is absent. Both overrides must be absolute, normalized paths. When
neither is set, KuPilot looks for `config.yaml` in the platform configuration
directory. A missing platform-default file is allowed and leaves defaults in
effect; a missing explicit file is an error.

Configuration files must be regular, owner-only files with mode `0600` and must
not be symbolic links. KuPilot rejects unsafe permissions instead of changing a
user-owned file automatically.

## Typed fields

The complete YAML schema is shown in [the example configuration](../config.example.yaml).
The following values are code-defined defaults or bounds:

| Field | Default and validation |
| --- | --- |
| `version` | Required schema version `1`. |
| `context` | Empty; when set, at most 253 UTF-8 bytes with no control or bidirectional-control characters. |
| `namespace` | Empty; when set, one DNS label of at most 63 bytes. All-Namespace values are rejected. |
| `no_color` | `false`; `--no-color` sets the CLI override, while the presence of `NO_COLOR` supplies `true` at environment priority. |
| `paths.state_dir` | Platform default; an override must be absolute, normalized, and non-root. |
| `paths.cache_dir` | Platform default; an override must be absolute, normalized, and non-root. |
| `paths.log_dir` | Platform default; an override must be absolute, normalized, and non-root. |
| `model.provider_kind` | Fixed to `openai_compatible`. |
| `model.endpoint` | Empty until configured; HTTPS is required except for explicit loopback HTTP. |
| `model.model` | Empty until configured; 1–128 ASCII letters, digits, `.`, `_`, `-`, `/`, or `:`. No provider model is selected by default. |
| `model.api_key_source` | Fixed to `environment`; this is only a source category, never a credential value. |
| `model.temperature` | `0.1`; accepted range `0` through `0.2`. |
| `model.max_output_tokens` | `2048`; accepted range `1` through the code-defined ceiling `8192`. |
| `model.request_timeout_seconds` | `45`; accepted range `1` through the hard model-request ceiling `45`. |
| `model.streaming` | Fixed to `true`. |
| `model.tool_calling_required` | Fixed to `true`. |
| `kubernetes.exec_credentials` | `allow`; may be set to `deny`. It never selects or supplies a command. |
| `logging.enabled` | `true`; may be disabled. |
| `logging.level` | `info`; `warn` and `error` are also accepted. Debug logging is not available. |

The admitted non-sensitive environment variables are:

- `KUPILOT_CONFIG_FILE`
- `KUPILOT_CONTEXT`
- `KUPILOT_NAMESPACE`
- `KUPILOT_NO_COLOR` and `NO_COLOR`
- `KUPILOT_STATE_DIR`, `KUPILOT_CACHE_DIR`, and `KUPILOT_LOG_DIR`
- `KUPILOT_MODEL_ENDPOINT`, `KUPILOT_MODEL`,
  `KUPILOT_MODEL_TEMPERATURE`, `KUPILOT_MODEL_MAX_OUTPUT_TOKENS`, and
  `KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS`
- `KUPILOT_EXEC_CREDENTIALS`
- `KUPILOT_LOG_ENABLED` and `KUPILOT_LOG_LEVEL`

The CLI exposes only `--config`, `--context`, `--namespace`, and `--no-color` as
configuration overrides. It has no API-key, token, kubeconfig-content, arbitrary
header, TLS-bypass, redirect, or command value option.

## Platform paths

Linux follows the XDG Base Directory specification:

| Purpose | Default |
| --- | --- |
| Configuration | `${XDG_CONFIG_HOME:-$HOME/.config}/kupilot/config.yaml` |
| Persistent state | `${XDG_STATE_HOME:-$HOME/.local/state}/kupilot` |
| Cache | `${XDG_CACHE_HOME:-$HOME/.cache}/kupilot` |
| Local log | `${XDG_STATE_HOME:-$HOME/.local/state}/kupilot/logs/kupilot.log` |

macOS uses standard per-user locations when no XDG override is set:

| Purpose | Default |
| --- | --- |
| Configuration | `~/Library/Application Support/KuPilot/config.yaml` |
| Persistent state | `~/Library/Application Support/KuPilot` |
| Cache | `~/Library/Caches/KuPilot` |
| Local log | `~/Library/Logs/KuPilot/kupilot.log` |

On Linux, an explicitly set `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, or
`XDG_CACHE_HOME` must be absolute. With `XDG_STATE_HOME`, logs use its
`kupilot/logs` subdirectory. macOS path discovery does not use XDG environment
variables. Typed `paths.*` values or their admitted KuPilot environment
variables override the resolved state, cache, and log directories after the
file is selected. KuPilot application directories use mode `0700`; local log
files use mode `0600`. Unsafe existing log permissions and symbolic-link targets
are rejected.

## Endpoint and transport policy

HTTPS endpoints may use public hosts, private hosts, or private IP addresses and
always use normal certificate and hostname verification. Plain HTTP is accepted
only for the exact `localhost` hostname or a literal loopback address such as
`127.0.0.1` or `::1`.

Endpoint user information, query parameters, fragments, escaped or traversing
paths, invalid ports, and non-HTTP schemes are rejected. There is no insecure
TLS setting. Redirects are allowed only when their canonical scheme, host, and
port match the configured origin; cross-origin redirects are rejected and
authentication must never be forwarded to another origin.

## Model API key

The API key is not a `Config` field. It cannot be supplied through YAML, a CLI
value, SQLite, a chat message, or a log field. `KUPILOT_MODEL_API_KEY` is the one
admitted source. Users should inject it through an appropriate local secret
mechanism rather than storing it in a project file.

KuPilot reads the environment entry once during validated model-adapter
construction, copies it into a runtime-only wrapper, and immediately removes
the source entry. Missing, empty, control-bearing, or values larger than 4096
bytes fail before model I/O. Child-process environments remove the variable
again as a second defense. The wrapper redacts every formatting operation,
rejects JSON, text, and YAML marshaling, and is never part of serializable
configuration.

## Local structured log

TUI operation uses a local JSON `slog` file by default and never sends log
records to terminal stdout. The fixed ceilings are:

- At most 1 MiB per file.
- At most three files, including the current file.
- Rotation after seven days, with stale known files pruned when the sink opens.
- At most 12 validated attributes in one record; additional input is dropped.

Configuration may disable the sink or raise its minimum level, but cannot
expand these ceilings. The handler admits only code-defined events and validated
scalar fields: `component`, `operation`, `outcome`, `error_class`,
`provider_kind`, `count`, `duration_ms`, `sequence`, `truncated`, and
`degraded`. Unknown fields, invalid values, arbitrary messages, raw errors,
headers, bodies, arguments, configuration contents, credentials, and cluster
payloads are not written.

Local logs are bounded operational diagnostics, not Session history, telemetry,
an audit ledger, an encrypted store, or a credential store.
