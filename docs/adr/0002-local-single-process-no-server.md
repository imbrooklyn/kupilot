# ADR-0002: Use a Local Single Process with No KuPilot Server

- Status: Accepted
- Date: 2026-08-05

## Context

KuPilot serves one terminal user, uses that user's kubeconfig, stores limited
history locally, and calls a user-configured cloud model endpoint. The product
does not require accounts, team coordination, remote scheduling, a cluster-side
controller, or a KuPilot-operated control plane.

Adding a server would create a second credential and data boundary, an
operational service, identity and authorization requirements, telemetry and
retention questions, and a larger maintenance surface before any of them
provide MVP diagnostic value.

## Decision

KuPilot will be a local, single-process application with one local user and one
active AgentRun at a time in `v0.1`.

The process will:

- Read the selected local kubeconfig and connect directly to the corresponding
  Kubernetes API server.
- Contact only the user-configured model endpoint for model requests.
- Store the permitted local history and audit data in a local SQLite database.
- Render the TUI and own all run, stream, cancellation, and scope state.

KuPilot will not operate a server, account system, remote control plane,
cluster-side Agent, analytics endpoint, or crash-reporting backend. The local
`v0.2` approval workflow does not change this topology.

## Consequences

Positive consequences:

- Kubernetes and model credentials do not need to pass through a KuPilot
  service.
- There is no server deployment, account lifecycle, remote database, or
  multi-tenant authorization system to operate.
- Run ownership, scope generation, cancellation, and local persistence can be
  coordinated within one process.
- The network paths required for a Diagnosis remain limited and explainable.

Costs and constraints:

- Users install and configure the binary and retain responsibility for local
  endpoint and cluster policy.
- Conversation history is local to a workstation unless the user moves it by a
  separately designed mechanism.
- The local process is not a high-availability service; interruption terminates
  active work and the next startup marks it interrupted.
- Local SQLite data depends on filesystem permissions, retention controls, and
  operating-system disk protection rather than server-side controls.

## Alternatives considered

- A hosted KuPilot service was rejected because it would centralize cluster data
  and credentials and require accounts, tenancy, operations, and a new trust
  boundary.
- A cluster-resident controller was rejected because continuous cluster access
  and background operation conflict with the user-driven snapshot Diagnosis.
- A local UI plus local background daemon was rejected because it adds IPC,
  lifecycle, and credential-sharing complexity without an MVP need.

## Security and privacy impact

"Local" does not mean "offline." Consented, bounded diagnostic content is sent
to the configured cloud model. The TUI must disclose that boundary before the
first eligible transfer.

Kubernetes credentials and model transport credentials remain local adapter
inputs and must not enter model content, SQLite, application events, or ordinary
logs. The local database is not claimed to be encrypted; minimization,
permissions, retention, and optional minimal persistence are required controls.

The absence of a server removes one remote trust boundary but does not replace
Kubernetes RBAC, endpoint validation, data projection, redaction, or scope
generation checks.

## Validation

The system-context and runtime diagrams in
[Architecture](../architecture.md) document every allowed network and durable
storage boundary. The public [Privacy Overview](../privacy-overview.md) states
which data may cross the model boundary and which data is excluded.

Automated startup and network tests must assert that KuPilot creates no
undisclosed listener or product telemetry path.

## Revisit triggers

- An accepted product requirement needs multi-user coordination or shared
  durable state.
- A future remote component has a documented threat model, operating owner,
  privacy contract, and migration plan.
- Platform constraints make the local topology infeasible and evidence supports
  a replacement architecture.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Privacy Overview](../privacy-overview.md)
- [Scope](../scope.md)
