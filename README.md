# Kupilot

Kupilot is a local, single-process Kubernetes operations Agent with a
conversational TUI. It turns natural-language requests into bounded typed
Kubernetes observations, keeps verified Evidence separate from model
interpretation, and returns the Markdown answer that best fits the question.

> [!IMPORTANT]
> The current `v0.4` candidate can read the admitted operational resource
> catalog and can propose one supervised action: restarting an exact
> Deployment. A restart is never autonomous. It requires a fresh local read,
> digest-bound local approval, revalidation, durable pre-write audit, one PATCH
> attempt, and separate rollout verification.

Kupilot is Agent-first. It is not k9s, a Kubernetes Dashboard, kubectl, a
shell, an IDE, a controller, or a generic API client. There is no resource
tree, YAML editor, command execution, Pod Exec, arbitrary REST request, dynamic
plugin, or background reconciliation loop.

## Operational capability

The code-owned Tool catalog currently contains:

- `get_resource`
- `list_resources`
- `get_events`
- `get_pod_logs`
- `get_previous_pod_logs`
- `get_related_resources`
- `get_cluster_overview`

Direct typed reads cover these stable built-in resources:

- Core `v1`: Namespace, Node, Pod, Service, PersistentVolumeClaim,
  PersistentVolume, and ConfigMap metadata only.
- `apps/v1`: Deployment, ReplicaSet, StatefulSet, and DaemonSet.
- `batch/v1`: Job and CronJob.
- `networking.k8s.io/v1`: Ingress.
- `autoscaling/v2`: HorizontalPodAutoscaler.
- `policy/v1`: PodDisruptionBudget.

The working Namespace is always visible. `kubernetes.namespace_access` freezes
each run to either `current` or `all`; the default is `all`. The latter permits
an explicit different Namespace or explicit bounded all-Namespace list in the
same Context only when Kubernetes RBAC also permits it. Cross-Context and
cross-cluster calls remain prohibited.

Secret objects and data, ConfigMap values, container environment values,
kubeconfig content, credentials, raw Kubernetes objects, arbitrary custom
resources, discovery-driven APIs, and unbounded logs are not model-readable.
See [Diagnostic Capabilities](docs/diagnostic-capabilities.md) and
[Product Contract](docs/product.md) for the exact projection and action
boundaries.

## Build from source

No published release artifact or package-manager installation is supported.
The verifiable installation path is a source build from a repository checkout.

Requirements:

- Go 1.25.0 or newer
- macOS or Linux on `amd64` or `arm64`
- A UTF-8-capable interactive terminal

```sh
make build
./bin/kupilot --version
```

`make build` writes `./bin/kupilot`. The contributor and CI toolchain uses an
exact Go patch version; see [Contributing](CONTRIBUTING.md).

## Quick start

1. Grant only the required Kubernetes permissions from
   [Least-Privilege RBAC](docs/rbac/README.md). Do not use `cluster-admin` for
   Kupilot.

2. Start a new Session:

   ```sh
   ./bin/kupilot
   ```

   A bare start never queries history. Automatically managed files stay below
   `${KUPILOT_HOME:-$HOME/.kupilot}`. If model configuration is incomplete,
   the TUI requests it with masked key input and discloses the difference
   between process-only and plaintext local credential storage.

3. Verify the Context and working Namespace. Before the first model-content
   transfer, review the destination and exact enabled data categories.

4. Ask an operational question. The transcript shows compact typed Tool steps
   and a free-form Markdown answer. Use `/status` for the active capability
   catalog, namespace policy, budget profile, usage, privacy, and storage
   state.

5. If the answer proposes `restart_deployment`, inspect the exact target,
   reason, risk, expiry, and digest-bound request. Rejecting, ignoring, changing
   scope, or letting the request expire performs no write.

The [Getting Started Guide](docs/user-guide/getting-started.md) covers the full
scope, consent, cancellation, and approval flow.

## Runtime budgets

Every AgentRun uses one immutable code-defined profile:

| Profile | Wall clock | Steps | Tool calls | Model calls | Tool-result total |
| --- | ---: | ---: | ---: | ---: | ---: |
| `compact` | 2 min | 12 | 16 | 6 | 1 MiB |
| `balanced` (default) | 10 min | 32 | 48 | 16 | 4 MiB |
| `extended` | 30 min | 64 | 128 | 32 | 12 MiB |

Independent per-request, item, log, graph, and output ceilings still apply.
Profiles cannot be changed by the model or expanded during a run. See
[Agent Runtime](docs/agent-runtime.md).

## CLI and Session behavior

```text
kupilot
kupilot resume
kupilot resume SESSION_ID
kupilot resume --last
kupilot cache clear
kupilot version
kupilot --version
kupilot help
kupilot help resume
kupilot help cache
```

- Resume restores bounded safe history and unverified scope/resource
  candidates only. It never resumes a run, model stream, live client,
  approval, or write.
- Empty, cancelled, invalid, and non-resumable requests never fall back to a
  new Session.
- Session selection is local global history; it does not depend on the working
  directory, repository, Context, Namespace, or crash state.
- `help`, `version`, and `cache clear` short-circuit before business storage,
  Kubernetes, model, and TUI initialization.
- CLI arguments never accept a model key, kubeconfig, question, arbitrary
  command, or approval token.

See [Sessions and Scope](docs/user-guide/sessions-and-scope.md).

## Data, storage, and network boundaries

Eligible processed content is sent directly to the configured model origin
only after informed consent. Kupilot has no product telemetry, usage analytics,
remote crash reporting, operated account, or control plane. The operational
network paths are the selected Kubernetes API and configured model endpoint; a
trusted kubeconfig exec credential program may have its own behavior outside
Kupilot's control.

SQLite stores bounded sanitized Session history and audit data. It does not
store kubeconfig data, credentials, raw objects, raw Events, raw logs, raw
model traffic, full prompts, or raw Tool results. Local SQLite, logs, exported
summaries, and an explicitly saved model key are plaintext and are not claimed
encrypted or forensically erasable.

The `/privacy` flow controls persistence mode, operational-detail retention,
Session/history/database deletion, and explicit
`kupilot.export-summary.v2` Markdown export. See
[Privacy and Local Data](docs/user-guide/privacy-and-local-data.md).

## Known boundaries

- One local user, one process, one active AgentRun, one verified Context, one
  working Namespace, and one immutable namespace policy per run.
- Kubernetes 1.34.x, 1.35.x, and 1.36.x are supported for the documented stable
  APIs. See [Kubernetes Compatibility](docs/kubernetes-compatibility.md).
- Exactly one configured `openai_compatible` endpoint profile is supported.
  Compatibility requires streaming Chat Completions-style responses and strict
  structured Tool calls. See [Model Compatibility](docs/model-compatibility.md).
- No Watch or informer, scheduled/background scan, live monitoring, arbitrary
  selector, custom-resource discovery, Helm, port forwarding, shell, kubectl,
  Pod Exec, plugin, MCP, RAG, or Multi-Agent orchestration.
- Evidence is a bounded snapshot. RBAC denial, missing or stale data, disabled
  container output, truncation, or model incompatibility can leave an explicit
  gap instead of a definitive cause.

## Documentation

- [User Guide](docs/user-guide/README.md)
- [Configuration](docs/configuration.md)
- [Privacy Overview](docs/privacy-overview.md)
- [Security Threat Model](docs/security.md)
- [Least-Privilege RBAC](docs/rbac/README.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Product Contract](docs/product.md)
- [Version Scope](docs/scope.md)

Security issues follow [Security Policy](SECURITY.md). Notable changes are in
the [Changelog](CHANGELOG.md).

## License

Kupilot is licensed under the [Apache License 2.0](LICENSE).
