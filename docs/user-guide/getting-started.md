# Getting Started

## Requirements

Kupilot currently supports local interactive use on macOS and Linux on `amd64`
and `arm64`. You need:

- Go 1.25.0 or newer for a source build.
- A UTF-8-capable terminal.
- A local kubeconfig Context and an identity with the documented
  [least-privilege RBAC](../rbac/README.md).
- One model endpoint that satisfies the
  [Model Compatibility Contract](../model-compatibility.md).
- Permission under your organization's policy to send the displayed diagnostic
  data categories to that model destination.

Windows is experimental and is not part of the supported `v0.4` runtime gate.
Kupilot is not intended to run as a cluster controller, shared server, or
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

No configuration file is required to open Kupilot. A bare start uses one fixed
Home at `${KUPILOT_HOME:-$HOME/.kupilot}` and opens interactive model setup when
the endpoint, model identifier, or API key is absent.

If you prefer a file, Kupilot accepts one strict version 1 YAML document. For
example:

```yaml
version: 1
context: example-context
namespace: example-namespace

runtime:
  budget_profile: balanced

model:
  endpoint: https://model.example.invalid/v1
  model: example-model

kubernetes:
  namespace_access: all
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
file permissions are respected; new Kupilot-created Home directories use
`0700` and new files use `0600` on supported Unix platforms.

The key can instead come from `KUPILOT_MODEL_API_KEY`, which overrides a file
value, is read once, and is removed from the Kupilot process environment. It is
also accepted through the masked TUI setup. Choose `save` only after reviewing
the plaintext, not-encrypted disclosure; choose `session` to keep the key in
this process only. No credential-valued CLI option exists.

See [Configuration](../configuration.md) for the full schema, Home layout,
precedence, environment variables, endpoint rules, and credential boundary.

## Grant Kubernetes access

Choose `kubernetes.namespace_access: current` for working-Namespace-only reads,
or `all` for explicit cross-Namespace and all-Namespace reads in the same
Context. Apply only the matching namespaced and cluster-scoped read rules
through your normal cluster-administration process. If Deployment restart is
needed, add the separate exact resource-name Role for each admitted target. Do
not use `cluster-admin` or grant wildcard writes.

Kupilot also enforces its own Kind, Namespace, relationship, projection, and
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

The Context and working Namespace must exist in the selected kubeconfig and
Kubernetes API. A Context is resolved locally; the working Namespace is
verified through an exact read before it becomes an active ClusterScope. Empty
Namespace input never means all Namespaces. The configured namespace-access
policy is frozen into each run and is visible through `/status`.

## Complete the first-run flow

1. If model configuration is incomplete, complete the four-step endpoint,
   model, storage, and masked-key flow. Each field has a persistent label above
   the composer. `/model` can reconfigure it later, and `Ctrl+C` or `Esc`
   cancels any current setup step without silently replacing the active model.
2. Confirm the footer shows the intended verified Context, working Namespace,
   and `supervised` state. Use `/status` to check namespace policy and budget;
   use `/context` and `/namespace` when scope is unavailable.
3. Optionally use `/resource` to attach one allowlisted direct resource. Picker
   selection is only an input aid; it is not Evidence and does not prove that
   the object still exists.
4. Open `/privacy`. Review the canonical model destination, every enabled data
   category, and every never-eligible category. Container output is disabled by
   default. Accepting consent authorizes only the exact displayed tuple.
5. Enter an operational question. Kupilot durably begins the run before model
   or cluster I/O, freezes Context, working Namespace, namespace policy, and
   budget, and shows compact bounded activity steps.
6. Review the free-form Markdown answer and use `Ctrl+E` for bounded supporting
   observations. A proposed `restart_deployment` action remains unexecuted until
   a separate local approval is reviewed and accepted.

Changing Context, Namespace, or the container-output privacy category cancels
an active AgentRun and invalidates stale work before another transfer.

## TUI commands

The compile-time command registry is fixed:

| Command | Behavior |
| --- | --- |
| `/help` | Show commands and key bindings. |
| `/model` | Configure or replace the single model runtime. |
| `/context [filter]` | Select a kubeconfig Context. |
| `/namespace [filter]`, `/ns` | Select a Namespace in the current Context. |
| `/resource [filter]`, `/res` | Select or clear a direct target resource. |
| `/status` | Show current safe Session, scope, namespace policy, capability catalog, action availability, budget usage, privacy, and storage status. |
| `/new` | Create a new Session without querying history. |
| `/resume [filter]` | Open eligible local Session selection inside the TUI. |
| `/rename [title]` | Rename the current standard-persistence Session. |
| `/privacy` | Review model data sharing, persistence, retention, deletion, and export controls. |
| `/cancel` | Cancel the active diagnostic run. |
| `/quit`, `/exit` | Exit Kupilot. |

Unknown commands and `!` syntax perform no external action. Use a leading `//`
to submit an ordinary question that begins with `/`.

Key bindings include `Enter` to submit, `Shift+Enter` or `Alt+Enter` for a
newline, `Ctrl+J` for a newline when the terminal can distinguish it, `Tab` for
completion, arrow keys or `Ctrl+P`/`Ctrl+N` for choices, `Esc` to close or
cancel the current picker/dialog, `Page Up`/`Page Down` for the transcript,
`Ctrl+E` to inspect observation details, `Ctrl+X` to cancel a run, and `Ctrl+C`
to cancel the active local interaction first. Model setup, Pickers, observation
detail, privacy, export, deletion, resume-scope, and approval interactions own
that first `Ctrl+C`. With no child interaction, `Ctrl+C` clears a non-empty
ordinary draft first and quits when the composer is already empty. During an
active run, an empty-composer `Ctrl+C` cancels the run before the bounded exit.
Outside a Picker, `Up` recalls the newest submitted input from an empty
composer. `Up`/`Down` continue through history while the recalled text is
unchanged and the cursor is at the beginning or end of the complete input;
otherwise they move within the multiline editor. `Ctrl+P`/`Ctrl+N` remain
explicit history shortcuts. Model setup and other repurposed composer fields
cannot recall ordinary question history. Kupilot
leaves terminal mouse reporting disabled so visible text can be selected and
copied with the terminal's native controls. Mouse-wheel and trackpad gestures
are therefore terminal-owned and cannot recall composer history. The composer
uses an unframed `›` prompt and grows from one through eight content rows. Only
its first visual row shows `›`; continuation rows retain the same two-column
indent. Submitted user messages retain the same marker, continuous surface,
and vertical spacing as the composer. Multi-step command and Picker inputs keep
a short field label immediately above the same composer after typing begins.

## Stop safely

Use `/quit`, `/exit`, or `Ctrl+C` with an empty composer. If a draft is present,
the first `Ctrl+C` clears it without exiting. Kupilot cancels owned work,
restores the primary terminal, writes the completed safe transcript once to
terminal-owned scrollback, closes the model and Kubernetes adapters, waits for
bounded child work, and closes the SQLite database and local log. A run that
was durable and still marked running at process interruption is classified as
interrupted at the next validated startup; it is never replayed automatically.
