# Getting Started

This guide distinguishes deterministic implementation evidence from live
integration and release readiness. Named model profiles, safe Session context/
summarization, expanded reads and diagnostics, permission routing, typed
remediation, and default-off exact local execution are now composed. No real
cluster or host-tool compatibility is implied.

## Requirements

Kupilot currently supports local interactive use on macOS and Linux on `amd64`
and `arm64`. You need:

- Go 1.25.0 or newer for a source build.
- A UTF-8-capable terminal.
- A local kubeconfig Context and an identity with the documented
  [least-privilege RBAC](../rbac/README.md).
- For the current binary, an Agent model endpoint that satisfies the
  [Model Compatibility Contract](../model-compatibility.md), plus an optional
  independently configured Reviewer endpoint when that role is enabled.
- Permission under your organization's policy to send the displayed diagnostic
  data categories to that model destination.

Windows is experimental and is not part of the supported runtime gate.
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

## Current configuration and model setup

No configuration file is required to open Kupilot. A bare start uses one fixed
Home at `${KUPILOT_HOME:-$HOME/.kupilot}` and opens interactive model setup when
the endpoint, model identifier, or API key is absent.

If you prefer a file, Kupilot writes strict schema version 2. It also reads a
legacy version 1 document through an in-memory migration without rewriting the
file. A minimal explicit version 2 Agent profile looks like:

```yaml
version: 2
context: example-context
namespace: example-namespace

runtime:
  budget_profile: balanced

models:
  agent:
    name: agent
    role: agent
    credential_ref: agent
    provider_kind: openai_compatible
    endpoint: https://model.example.invalid/v1
    model: example-model
    temperature: 0.1
    request_timeout_seconds: 300
    streaming: true
    tool_calling_required: true

kubernetes:
  namespace_access: all
```

The endpoint and names are deliberately non-working placeholders. Replace them
with approved values. `max_output_tokens` remains omitted until exact evidence
for that selected endpoint supports a positive configured value; independent
byte, call, time, stream, and cost-unit limits remain active. The default file
is `KUPILOT_HOME/config.yaml`. An
optional plaintext `models.agent.api_key` is admitted, but do not put a real
key in a repository, example, chat message, or public report. A separately
selected file must use an absolute normalized path:

```sh
./bin/kupilot --config /absolute/path/to/config.yaml
```

Unknown fields, duplicate keys, aliases, merges, nulls, wrong types, additional
documents, and files larger than 64 KiB are rejected. Existing user-managed
file permissions are respected; new Kupilot-created Home directories use
`0700` and new files use `0600` on supported Unix platforms.

The Agent key can instead come from exactly one of
`KUPILOT_AGENT_API_KEY` and the legacy `KUPILOT_MODEL_API_KEY` alias. A distinct
Reviewer key uses `KUPILOT_APPROVAL_REVIEWER_API_KEY`. Present role variables
are read once and removed from the Kupilot process environment. The Agent key
is also accepted through masked TUI setup. Choose `save` only after reviewing
the role-specific plaintext, not-encrypted disclosure; choose `session` to keep
the key in this process only. No credential-valued CLI option exists.

See [Configuration](../configuration.md) for the full schema, Home layout,
precedence, environment variables, endpoint rules, and credential boundary.

## Grant current Kubernetes access

Choose `kubernetes.namespace_access: current` for working-Namespace-only reads,
or `all` for explicit cross-Namespace and all-Namespace reads in the same
Context. Apply only the matching namespaced and cluster-scoped read rules
through your normal cluster-administration process. If Deployment restart is
needed, add its separate exact resource-name Role. Other typed remediation and
remote diagnostics likewise use their operation-specific opt-in fixtures. Do
not use `cluster-admin` or grant wildcard writes.

Kupilot also enforces its own Kind, Namespace, relationship, projection, and
budget allowlists. RBAC remains an independent defense if another defect or
local configuration grants a broader identity.

The current `v0.5` fixtures split exact built-in/CRD reads, metrics, logs, Pod
Exec, diagnostic Pods, scale, rollback, Pod delete, eviction, and Node patch.
Each remains disabled until explicitly configured and bound. Local execution
uses an exact host policy and existing external identity, not a Kubernetes RBAC
fixture.

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

## Current model and Session flow

Startup produces one required named `agent` model profile and may produce one
`approval_reviewer` profile. Each profile has an explicit origin, opaque
credential, role-bound consent, and independent budget. There is no fallback
or router. Summarization reuses `agent` rather than introducing a
`context_compactor` role. The Reviewer transport is connected only to the
deterministic `review` route and its recommendation never becomes permission or
execution authority by itself.

The permission interaction uses `ask` by default. `/permissions` exposes the
five fixed profiles with their boundary, Reviewer route, and risk, and changes
the profile through a typed Application command. No profile can enable a
default-off capability or bypass RBAC, consent, scope, audit, fresh
revalidation, or hard denial.

Every question after the first in a Session receives one ordered, bounded
representation of all retained eligible safe history when such history exists.
Standard Sessions supply it in process and after explicit resume; minimal
Sessions supply it only from the current process. Resume itself performs zero
model, Kubernetes, Tool, Reviewer, approval, process, or executor I/O. The next
question sends history only after current consent, scope, policy, coverage, and
budget gates pass; failure causes zero model calls and no current-question-only
fallback. Historic scope, Evidence, permissions, rules, ActionEnvelopes, and
execution never regain authority.

The implemented capability boundary is documented in
[Operational Capabilities](../diagnostic-capabilities.md). Every sensitive or
effectful request still requires deterministic risk and an immutable
ActionEnvelope. A visible permission route does not enable a default-off remote
diagnostic or optional source, and it does not bypass Pod-log consent; this
guide grants no capability that policy and composition have not enabled.

## Current first-run flow

1. If model configuration is incomplete, complete the four-step endpoint,
   model, storage, and masked-key flow. Each field has a persistent label above
   the composer. `/model` can reconfigure it later, and `Ctrl+C` or `Esc`
   cancels any current setup step without silently replacing the active model.
2. Confirm the footer shows the intended verified Context, working Namespace,
   permission profile, and supervision route, such as `ask · human`. Use
   `/status` to check detailed policy, Reviewer, action, model-context, and
   budget state; use `/context` and `/namespace` when scope is unavailable.
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
   observations. Any proposed action remains unexecuted until its exact current
   permission route, approval, revalidation, and durable pre-audit succeed.

Changing Context, Namespace, or the container-output privacy category cancels
an active AgentRun and invalidates stale work before another transfer.

## Current TUI commands

The compile-time command registry is fixed:

| Command | Behavior |
| --- | --- |
| `/help` | Show commands and key bindings. |
| `/model` | Configure or replace the required Agent runtime; an optional Reviewer remains file-configured. |
| `/context [filter]` | Select a kubeconfig Context. |
| `/namespace [filter]`, `/ns` | Select a Namespace in the current Context. |
| `/resource [filter]`, `/res` | Select or clear a direct target resource. |
| `/permissions` | Review the five fixed permission profiles and change the active profile through Application. |
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
completion, arrow keys or `Ctrl+P`/`Ctrl+N` for bounded choices, `Esc` to close or
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
otherwise they move within the multiline editor. Only `Up` and `Down` navigate
ordinary submitted-input history. Model setup and other repurposed composer
fields cannot recall ordinary question history. After an explicit Session
resume is accepted, `Up` and `Down` recall that Session's restored user
questions; restored answers and notices are display-only and never become
editable input history. A failed or cancelled resume keeps the current input
history unchanged. Kupilot does not enable mouse reporting. Native terminal
drag selection and copy therefore remain available, while wheel and trackpad
momentum use the terminal emulator's own scrollback behavior and never recall
composer history. `Page Up` and `Page Down` provide an explicit in-memory
transcript review fallback. While new output is live, Working-state layout
changes keep the complete newest user message and Agent output attached to the
bottom. Explicit transcript review stays at the selected position until it
returns to the bottom. Before completed output enters scrollback, Kupilot
removes it from the live projection and settles a compact frame; transient Tool
steps, Working text, and layout spacer rows are not copied into history.

Structured inventories with multiple resources and shared attributes use a
compact Markdown table per Kind by default. The user does not need to request
formatting. When the terminal is too narrow, the same data falls back to
readable key/value records.

The composer uses an unframed `›` prompt and grows from one through eight
content rows. Only its first visual row shows `›`; continuation rows retain the
same two-column indent. Submitted user messages retain the same marker,
continuous surface, and vertical spacing as the composer. Multi-step command
and Picker inputs keep a short field label immediately above the same composer
after typing begins.

## Stop safely

Use `/quit`, `/exit`, or `Ctrl+C` with an empty composer. If a draft is present,
the first `Ctrl+C` clears it without exiting. Kupilot cancels owned work,
clears only its remaining live frame, leaves already committed safe history in
terminal-owned scrollback, closes the model and Kubernetes adapters, waits for
bounded child work, and closes the SQLite database and local log. A run that
was durable and still marked running at process interruption is classified as
interrupted at the next validated startup; it is never replayed automatically.
