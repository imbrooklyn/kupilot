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

## Optional configuration and model setup

No configuration file is required to open KuPilot. A bare start uses one fixed
Home at `${KUPILOT_HOME:-$HOME/.kupilot}` and opens interactive model setup when
the endpoint, model identifier, or API key is absent.

If you prefer a file, KuPilot accepts one strict version 1 YAML document. For
example:

```yaml
version: 1
context: example-context
namespace: example-namespace

model:
  endpoint: https://model.example.invalid/v1
  model: example-model
```

The endpoint and names are deliberately non-working placeholders. Replace them
with approved values. The default file is `KUPILOT_HOME/config.yaml`. An
optional plaintext `model.api_key` is admitted, but do not put a real key in a
repository, example, chat message, or public report. A separately selected file
must use an absolute normalized path:

```sh
./bin/kupilot --config /absolute/path/to/config.yaml
```

Unknown fields, duplicate keys, aliases, merges, nulls, wrong types, additional
documents, and files larger than 64 KiB are rejected. Existing user-managed
file permissions are respected; new KuPilot-created Home directories use
`0700` and new files use `0600` on supported Unix platforms.

The key can instead come from `KUPILOT_MODEL_API_KEY`, which overrides a file
value, is read once, and is removed from the KuPilot process environment. It is
also accepted through the masked TUI setup. Choose `save` only after reviewing
the plaintext, not-encrypted disclosure; choose `session` to keep the key in
this process only. No credential-valued CLI option exists.

See [Configuration](../configuration.md) for the full schema, Home layout,
precedence, environment variables, endpoint rules, and credential boundary.

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

1. If the footer shows `model not configured`, complete the four-step endpoint,
   model, storage, and masked-key flow. `/model` can reconfigure it later.
2. Confirm the footer shows the intended verified Context, Namespace, and
   `read-only` state. If no scope is active, use `/context` and `/namespace`.
3. Optionally use `/resource` to attach one Pod, Deployment, ReplicaSet, Job, or
   Service. Picker selection is only an input aid; it is not an observation and
   does not prove that the object still exists.
4. Open `/privacy`. Review the canonical model destination, every enabled data
   category, and every never-eligible category. Container output is disabled by
   default. Accepting consent authorizes only the exact displayed tuple.
5. Enter one diagnostic question. KuPilot durably begins the run before any
   model or cluster read, binds it to the verified Context and Namespace, and
   shows each bounded activity step with a readable label.
6. Review the final confirmed facts, hypotheses, missing information, and
   recommendations. Every recommendation is marked `Not executed`. Press
   `Ctrl+E` to inspect bounded safe observation details when needed. Repeated
   homogeneous resource statuses appear as a compact, non-interactive table
   with columns appropriate to Pod, Deployment, ReplicaSet, Job, or Service.

Changing Context, Namespace, or the container-output privacy category cancels
an active diagnostic run and invalidates stale work before another transfer.

## TUI commands

The compile-time command registry is fixed:

| Command | Behavior |
| --- | --- |
| `/help` | Show commands and key bindings. |
| `/model` | Configure or replace the single model runtime. |
| `/context [filter]` | Select a kubeconfig Context. |
| `/namespace [filter]`, `/ns` | Select a Namespace in the current Context. |
| `/resource [filter]`, `/res` | Select or clear a direct target resource. |
| `/status` | Show current safe Session, scope, access, and run status. |
| `/new` | Create a new Session without querying history. |
| `/resume [filter]` | Open eligible local Session selection inside the TUI. |
| `/rename [title]` | Rename the current standard-persistence Session. |
| `/privacy` | Review model data sharing, persistence, retention, deletion, and export controls. |
| `/cancel` | Cancel the active diagnostic run. |
| `/quit`, `/exit` | Exit KuPilot. |

Unknown commands and `!` syntax perform no external action. Use a leading `//`
to submit an ordinary question that begins with `/`.

Key bindings include `Enter` to submit, `Ctrl+J` for a newline, `Tab` for
completion, arrow keys or `Ctrl+P`/`Ctrl+N` for choices, `Esc` to close or
cancel the current picker/dialog, `Page Up`/`Page Down` for the transcript,
`Ctrl+E` to inspect observation details, `Ctrl+X` to cancel a run, and `Ctrl+C` to
quit.

## Stop safely

Use `/quit`, `/exit`, or `Ctrl+C`. KuPilot cancels owned work, shuts down the
TUI, closes the model and Kubernetes adapters, waits for bounded child work,
and closes the SQLite database and local log. A run that was durable and still
marked running at process interruption is classified as interrupted at the
next validated startup; it is never replayed automatically.
