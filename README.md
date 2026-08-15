# KuPilot

KuPilot is a local, single-process Kubernetes TUI Agent for evidence-first
diagnosis. It turns one natural-language question into a bounded sequence of
read-only Kubernetes observations, then produces a cautious Diagnosis whose
confirmed facts cite Evidence from that run.

> [!IMPORTANT]
> The current KuPilot `v0.3` candidate composition is strictly read-only. It has
> no reachable Kubernetes write path, shell, kubectl execution, Pod Exec,
> approval dialog, or autonomous remediation. Recommendations are guidance for
> the user and are always marked as not executed.

KuPilot is Agent-first rather than resource-browser-first. Its TUI exists to
establish one verified Context and Namespace, accept diagnostic intent, and let
the user supervise each ToolInvocation. It is not k9s, a Kubernetes Dashboard,
an IDE, a monitoring system, or a general DevOps Agent.

## What `v0.3` can diagnose

The supported diagnostic categories are:

- CrashLoopBackOff
- OOMKilled
- ImagePullBackOff
- Pod Pending
- Readiness probe failure
- Deployment with no available replicas
- Failed Job
- Service with no ready Endpoint

Support means KuPilot can gather a bounded Evidence path and separate confirmed
facts, hypotheses, missing information, and recommended actions. It does not
guarantee that a model will identify the root cause. See
[Diagnostic Capabilities](docs/diagnostic-capabilities.md) for the exact
Evidence and limitation matrix.

The model-visible Tool catalog is fixed to `get_resource`, `list_resources`,
`get_events`, `get_pod_logs`, `get_previous_pod_logs`, and
`get_related_resources`. Direct targets are limited to Pod, Deployment,
ReplicaSet, Job, and Service in one active Namespace.

## Build from source

No published release artifact or package-manager installation is supported.
The verifiable installation path is a source build from a repository checkout.

Requirements:

- Go 1.25.0 or newer
- macOS or Linux on `amd64` or `arm64`
- A UTF-8-capable interactive terminal

Build the CGO-free binary and inspect its non-sensitive build information:

```sh
make build
./bin/kupilot --version
```

`make build` writes `./bin/kupilot`. The complete contributor and CI toolchain
uses an exact Go patch version; see [Contributing](CONTRIBUTING.md) before
submitting a change.

## Quick start

1. Create an owner-only YAML configuration. The full schema and platform
   locations are documented in [Configuration](docs/configuration.md). A
   minimal example is:

   ```yaml
   version: 1
   context: example-context
   namespace: example-namespace

   model:
     endpoint: https://model.example.invalid/v1
     model: example-model
   ```

   The `.invalid` endpoint and all `example-*` names are placeholders. Replace
   them with values approved for your environment. Do not put a model API key,
   kubeconfig content, token, or certificate in this file. An explicit
   configuration file must use an absolute path and mode `0600`.

2. Make `KUPILOT_MODEL_API_KEY` available to the KuPilot process through an
   appropriate local secret mechanism. Do not pass the value as a CLI argument
   or store it in the repository. KuPilot reads and removes its process-local
   environment entry once; if it was exported by a parent shell, remove the
   parent-shell value after KuPilot exits.

3. Grant the selected Kubernetes identity only the read permissions in
   [Least-Privilege RBAC](docs/rbac/README.md). Do not use `cluster-admin` for
   KuPilot.

4. Start a new Session:

   ```sh
   ./bin/kupilot --config /absolute/path/to/config.yaml
   ```

   A bare `kupilot` always creates a new Session and never queries history. If
   Context and Namespace are not supplied by configuration or CLI overrides,
   select and verify them in the TUI. Before the first model-content transfer,
   review the exact destination and enabled cloud data categories, then accept
   or reject the consent request.

5. Ask one diagnostic question. KuPilot shows bounded Tool steps and returns a
   structured Diagnosis. Press `Ctrl+E` to inspect the bounded safe provenance
   behind cited Evidence. Evaluate any recommendation independently; the
   current composed binary cannot execute it.

The [Getting Started Guide](docs/user-guide/getting-started.md) covers the TUI,
scope selection, privacy review, cancellation, and common first-run failures.

## CLI and Session behavior

The public command surface is fixed:

```text
kupilot
kupilot resume
kupilot resume SESSION_ID
kupilot resume --last
kupilot version
kupilot --version
kupilot help
kupilot help resume
```

- `kupilot resume` opens a bounded picker of eligible local Sessions.
- `kupilot resume SESSION_ID` requires an exact UUIDv7 Session identifier.
- `kupilot resume --last` selects the most recently active eligible Session.
- Resume restores safe history and unverified scope/resource candidates only.
  It does not resume an AgentRun, model stream, ToolInvocation, Kubernetes
  client, or live scope.
- An empty, cancelled, invalid, or non-resumable request never falls back to a
  new Session.
- Session selection is global local history. It never depends on the current
  working directory, repository path, Context, or Namespace.
- There is no `--all`, `--cd`, cwd-based Session lookup, automatic last-Session
  resume, or credential-valued option.
- `help` and `version` short-circuit before business storage, Kubernetes, model,
  or TUI initialization.

See [Sessions and Scope](docs/user-guide/sessions-and-scope.md) for resume
eligibility and saved-scope conflict behavior.

## Data, storage, and network boundaries

KuPilot runs locally, but eligible diagnostic content is sent directly to the
configured model endpoint after informed consent. The categories include the
processed user question, bounded safe conversation context, Context/Namespace
and resource references, projected Kubernetes status, projected Events, and
optionally redacted container-output facts. Container output is disabled by
default.

Kubeconfig contents, Kubernetes credentials, Secret objects and data,
ConfigMap data, raw objects, raw Events, raw or unbounded logs, the model API
key, raw prompts, and raw protocol bodies are not eligible model content.
Redaction reduces risk but cannot guarantee that every sensitive value in an
otherwise eligible field is recognized.

KuPilot stores sanitized Session history in a local SQLite database and writes
a small, allowlisted, rotating local operational log by default. Neither store
is encrypted by KuPilot. Local logging can be disabled independently from the
privacy control for container-output Tools.

The `/privacy` surface can start a new standard- or minimal-persistence Session,
tighten operational-detail retention, delete the current Session, and export a
versioned redacted summary of the current standard Session. A historical
standard Session can be deleted from the existing resume picker or explicitly
resumed before export. The same `/privacy` dialog can clear every Session graph
while preserving settings and valid consent, or close storage and delete the
validated database plus known SQLite sidecars. Operational logs and exported
summaries remain separate. The exact deletion boundary and forensic-erasure
limitation are documented in
[Privacy and Local Data](docs/user-guide/privacy-and-local-data.md).

KuPilot has no product telemetry, usage analytics, remote crash reporting,
KuPilot-operated account, or KuPilot control plane. The diagnosis network paths
are the selected Kubernetes API and the configured model endpoint. A trusted
kubeconfig exec credential program may have its own behavior outside KuPilot's
control.

## Known boundaries

- One local user, one process, one active AgentRun, one verified Context, and
  one Namespace per run.
- Kubernetes 1.34.x, 1.35.x, and 1.36.x are the supported API-server minors for
  the fixed stable APIs. See [Kubernetes Compatibility](docs/kubernetes-compatibility.md).
- Exactly one configured `openai_compatible` endpoint profile is supported.
  Compatibility is a strict streaming Chat Completions and structured
  Tool-calling contract, not a provider-label guarantee. See
  [Model Compatibility](docs/model-compatibility.md).
- No all-Namespace or cross-cluster diagnosis, Watch or informer, background
  scan, live monitoring, custom resources, Secret reads, arbitrary selectors,
  plugins, MCP, RAG, or Multi-Agent orchestration.
- Evidence is a bounded snapshot. RBAC denial, missing or stale data, disabled
  container output, truncation, and model incompatibility can leave an explicit
  gap instead of a root cause.

## Documentation

- [User Guide](docs/user-guide/README.md)
- [Configuration](docs/configuration.md)
- [Privacy Overview](docs/privacy-overview.md)
- [Security Threat Model](docs/security.md)
- [Least-Privilege RBAC](docs/rbac/README.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Product Contract](docs/product.md)
- [Version Scope](docs/scope.md)

Security issues should follow the private reporting instructions in
[Security Policy](SECURITY.md). Contributions follow
[Contributing](CONTRIBUTING.md). Notable source changes are recorded in the
[Changelog](CHANGELOG.md).

## License

KuPilot is licensed under the [Apache License 2.0](LICENSE).
