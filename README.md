# KuPilot

KuPilot is a local, single-process Kubernetes TUI Agent that gathers Evidence
through a small set of constrained, structured, read-only Tools before producing
a cautious Diagnosis.

## Why KuPilot exists

Kubernetes diagnosis often depends on knowing which facts to collect, in what
order, and how to distinguish an observation from an inference. KuPilot is
intended to compress that work into a natural-language interaction while keeping
the active ClusterScope narrow, local credentials isolated, and every confirmed
fact traceable to Evidence.

The product is Agent-first. The Agent chooses a small, bounded Evidence path for
the user's question. The TUI exists to provide the Agent with user intent and to
let the user supervise each ToolInvocation; it is not a general cluster control
surface.

## The `v0.1` contract at a glance

- `v0.1` is read-only. It has no Kubernetes write path and no Approval Dialog.
- One local process serves one user, with one active AgentRun at a time.
- Every AgentRun is bound to one immutable Context and Namespace in its
  ClusterScope. There is no implicit or cross-Namespace query mode.
- The Agent can use exactly six read-only Tools: `get_resource`,
  `list_resources`, `get_events`, `get_pod_logs`,
  `get_previous_pod_logs`, and `get_related_resources`.
- The MVP covers eight diagnostic categories: CrashLoopBackOff, OOMKilled,
  ImagePullBackOff, Pod Pending, readiness probe failure, Deployment with no
  available replicas, failed Job, and Service with no ready Endpoint.
- A Diagnosis separates confirmed facts, hypotheses, missing information, and
  recommended actions. It does not guarantee a root cause.
- Recommended actions are not executed by `v0.1`.
- Cluster data is sent to a configured cloud model only after informed consent
  and only after local projection, bounding, and redaction.

## User journey

The `v0.1` user journey is:

1. Starting `kupilot` creates a new Session. History is queried only through an
   explicit `resume` command.
2. The user selects and verifies a kubeconfig Context and Namespace. Together
   they establish the active ClusterScope.
3. The user may attach one ResourceRef from the fixed target kinds: Pod,
   Deployment, ReplicaSet, Job, or Service.
4. The user asks a diagnostic question in natural language.
5. A single AgentRun performs bounded ToolInvocations and exposes their purpose,
   status, and safe summaries in the TUI.
6. KuPilot returns a Diagnosis with the observation time and supporting Evidence
   references, including any gaps caused by permissions, limits, or missing data.
7. The user decides whether and how to act on the recommendations outside
   KuPilot. No action is performed by `v0.1`.

## What KuPilot is not

KuPilot is not k9s, a Kubernetes Dashboard, a kubectl wrapper, an IDE, or a
general DevOps Agent. It does not aim to provide resource browsing, full YAML
viewing or editing, live monitoring, shell or kubectl execution, Pod Exec, port
forwarding, arbitrary Kubernetes operations, plugins, MCP, RAG, or Multi-Agent
orchestration.

The central feature question is: **does this help the Agent collect bounded
Evidence and form a safer Diagnosis, or does it replace the Agent with another
cluster management interface?** Features in the second category are outside the
product direction.

## Product documentation

- [Product contract and diagnostic coverage](docs/product.md)
- [Version scope, Tools, non-goals, and feature gate](docs/scope.md)
- [Privacy overview](docs/privacy-overview.md)
- [Glossary](docs/glossary.md)

## Development

KuPilot requires Go 1.25.0 or newer. Run the standard local gate with:

```sh
make check
```

`make check` verifies Go formatting without changing source files, runs
uncached tests and `go vet`, and builds the current platform binary. The full
local gate adds race-enabled tests and CGO-free builds for the supported macOS
and Linux architecture matrix:

```sh
make check-all
```

The individual targets are also available:

```sh
make fmt
make fmt-check
make test
make test-race
make vet
make build
make cross-build
```

`make fmt` updates Go source formatting. `make build` writes the development
binary to `./bin/kupilot`; `make cross-build` writes macOS and Linux `amd64` and
`arm64` binaries under `./bin/cross`.

## License

KuPilot is licensed under the [Apache License 2.0](LICENSE).
