# Getting Started

## Requirements

KuPilot currently supports local interactive use on macOS and Linux on `amd64`
and `arm64`. You need:

- Go 1.25.0 or newer for a source build.
- A UTF-8-capable terminal.
- A local kubeconfig Context and an identity with the documented
  [least-privilege RBAC](../rbac/README.md).
- One model endpoint that satisfies the
  [Model Compatibility Contract](../model-compatibility.md).
- Permission under your organization's policy to send the displayed diagnostic
  data categories to that model destination.

Windows is experimental and is not part of the supported `v0.3` runtime gate.
KuPilot is not intended to run as a cluster controller, shared server, or
container-only service.

## Build the binary

From a repository checkout:

```sh
make build
./bin/kupilot version
```

The build is CGO-free and writes `./bin/kupilot`. No published archive or
package-manager installation is supported.

## Create an owner-only configuration

KuPilot accepts one strict, non-sensitive YAML document. Unknown fields,
duplicate keys, aliases, merge keys, nulls, wrong types, additional YAML
documents, and files larger than 64 KiB are rejected.

A minimal configuration is:

```yaml
version: 1
context: example-context
namespace: example-namespace

model:
  endpoint: https://model.example.invalid/v1
  model: example-model
```

The endpoint and names are deliberately non-working placeholders. Replace them
with approved values. Do not add an API key, token, certificate, kubeconfig
content, header, or TLS-bypass value; none belongs to the schema.

The default file is:

| Platform | Path |
| --- | --- |
| Linux | `${XDG_CONFIG_HOME:-$HOME/.config}/kupilot/config.yaml` |
| macOS | `~/Library/Application Support/KuPilot/config.yaml` |

The file must be a regular non-symlink file with mode `0600`. Alternatively,
select an owner-only file by absolute normalized path:

```sh
./bin/kupilot --config /absolute/path/to/config.yaml
```

See [Configuration](../configuration.md) for the full schema, defaults,
precedence, environment variables, platform paths, and endpoint rules.

## Supply the model credential

`KUPILOT_MODEL_API_KEY` is the only admitted model credential source. Supply it
to the KuPilot process through an appropriate local secret mechanism. KuPilot
reads the value once into a non-renderable runtime wrapper and removes the entry
from its own process environment. It does not write the value to YAML, SQLite,
the local application log, the TUI, model content, or a child-process
environment.

Do not put the value in a command argument, repository file, chat message, or
shell-history assignment. Removing the child process's environment entry cannot
remove a value exported in its parent shell; clear that parent-shell value when
it is no longer needed.

## Grant read-only Kubernetes access

Apply the namespaced read rules and Namespace-verification rule through your
normal cluster-administration process. Bind namespaced access only in each
Namespace KuPilot is permitted to diagnose. Do not bind a reusable namespaced
ClusterRole with a ClusterRoleBinding, and do not use `cluster-admin`.

KuPilot also enforces its own Kind, Namespace, relationship, projection, and
budget allowlists. RBAC remains an independent defense if another defect or
local configuration grants a broader identity.

## Start a new Session

Run the binary with no subcommand:

```sh
./bin/kupilot
```

This always creates a new Session and performs no history query. Non-sensitive
startup overrides may be supplied explicitly:

```sh
./bin/kupilot --context example-context --namespace example-namespace
```

The Context and Namespace must exist in the selected kubeconfig and Kubernetes
API. A Context is resolved locally; the Namespace is verified through an exact
read before it becomes an active ClusterScope. Empty Namespace input never
means all Namespaces.

## Complete the first-run flow

1. Confirm the footer shows the intended verified Context, Namespace, and
   `read-only` state. If no scope is active, use `/context` and `/namespace`.
2. Optionally use `/resource` to attach one Pod, Deployment, ReplicaSet, Job, or
   Service. Picker selection is only an input aid; it is not Evidence and does
   not prove that the object still exists.
3. Open `/privacy`. Review the canonical model destination, every enabled data
   category, and every never-eligible category. Container output is disabled by
   default. Accepting consent authorizes only the exact displayed tuple.
4. Enter one diagnostic question. KuPilot durably begins the run before any
   model or Tool I/O, binds it to the immutable ClusterScope, and shows each
   bounded Tool step.
5. Review the final confirmed facts, hypotheses, missing information, and
   recommendations. Every recommendation is marked `Not executed`. Press
   `Ctrl+E` to inspect bounded safe details for cited Evidence.

Changing Context, Namespace, or the container-output privacy category cancels
an active AgentRun and invalidates stale work before another transfer.

## TUI commands

The compile-time command registry is fixed:

| Command | Behavior |
| --- | --- |
| `/help` | Show commands and key bindings. |
| `/context [filter]` | Select a kubeconfig Context. |
| `/namespace [filter]`, `/ns` | Select a Namespace in the current Context. |
| `/resource [filter]`, `/res` | Select or clear a direct target resource. |
| `/status` | Show current safe Session, scope, access, and run status. |
| `/new` | Create a new Session without querying history. |
| `/resume [filter]` | Open eligible local Session selection inside the TUI. |
| `/rename [title]` | Rename the current standard-persistence Session. |
| `/privacy` | Review model data sharing, persistence, retention, deletion, and export controls. |
| `/cancel` | Cancel the active AgentRun. |
| `/quit`, `/exit` | Exit KuPilot. |

Unknown commands and `!` syntax perform no external action. Use a leading `//`
to submit an ordinary question that begins with `/`.

Key bindings include `Enter` to submit, `Ctrl+J` for a newline, `Tab` for
completion, arrow keys or `Ctrl+P`/`Ctrl+N` for choices, `Esc` to close or
cancel the current picker/dialog, `Page Up`/`Page Down` for the transcript,
`Ctrl+E` to inspect cited Evidence, `Ctrl+X` to cancel a run, and `Ctrl+C` to
quit.

## Stop safely

Use `/quit`, `/exit`, or `Ctrl+C`. KuPilot cancels owned work, shuts down the
TUI, closes the model and Kubernetes adapters, waits for bounded child work,
and closes the SQLite database and local log. A run that was durable and still
marked running at process interruption is classified as interrupted at the
next validated startup; it is never replayed automatically.
