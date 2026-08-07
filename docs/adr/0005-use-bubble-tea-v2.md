# ADR-0005: Use Bubble Tea v2 for the TUI Runtime

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot needs a terminal event loop for streaming Agent output, keyboard input,
window changes, cancellation, modal supervision, and orderly shutdown. The TUI
must remain a delivery adapter: it cannot become a second use-case layer or run
Kubernetes, model, or SQLite I/O from rendering code.

Bubble Tea has the required message-and-command model, but framework types and
lifecycle behavior are vendor contracts that must not leak into KuPilot's
Application or Domain packages. The v2 API and a concrete module version have
not yet been validated in this repository.

## Decision

KuPilot will use Bubble Tea v2 as the TUI runtime. Bubble Tea types are confined
to `internal/tui` and construction in `cmd/kupilot`.

The TUI will:

- Convert user input into typed Application commands and queries.
- Convert neutral Application events into Bubble Tea messages.
- Keep `Update` and `View` free of model, Kubernetes, and database I/O.
- Carry request identity, run identity, scope generation, and sequence where
  needed to reject stale or duplicate messages.
- Treat framework commands as delivery scheduling only; Application owns every
  long-running task and cancellation function.
- Render only normalized, bounded text using styles selected by local code.
- Leave and restore terminal state on normal completion, cancellation, panic
  recovery at the composition boundary, and supported termination signals.

No Bubble Tea model, message, command, callback, renderer, or key type crosses
an Application port. The TUI design itself is constrained further by ADR-0023.

This ADR selects the v2 major line, not an exact release, import path, or
unverified API signature.

## Consequences

Positive consequences:

- Streaming and input can share a well-defined single UI event loop.
- TUI state remains testable as a projection of neutral Application state.
- Vendor churn is isolated to one delivery adapter.
- Long I/O ownership and stale-message rejection remain outside framework
  callbacks.

Costs and constraints:

- Application events require explicit mapping to framework messages.
- Framework upgrades require lifecycle, rendering, and terminal-restoration
  regression tests.
- Accessibility, focus, paste behavior, narrow terminals, and control-character
  safety remain KuPilot responsibilities.

## Alternatives considered

- Bubble Tea v1 was rejected as the starting line because the project has not
  shipped a compatibility surface and the accepted direction is to validate v2
  before implementation.
- A custom terminal event loop was rejected because it would add substantial
  input, resize, rendering, and shutdown work without differentiating the
  product.
- Letting Bubble Tea types become Application events was rejected because it
  would make a delivery framework part of the use-case contract.
- A browser or desktop GUI was rejected because it changes the local terminal
  product boundary and introduces another process or web trust surface.

## Security and privacy impact

Bubble Tea does not authorize Tools, scope, or writes. The TUI cannot construct
Evidence, approve on behalf of the user, or call an executor. External text is
normalized and stripped of unsafe terminal control sequences before it becomes
a framework message or rendered view.

The TUI must not include raw adapter errors, credentials, model protocol bodies,
or raw Kubernetes data in debug output. Alternate-screen and terminal-state
cleanup are availability and terminal-integrity requirements, not cosmetic
behavior.

## Validation gate

S04 must use official module metadata and a compile probe to lock a Bubble Tea
v2 version, module path, compatible Bubbles/Lip Gloss set, and minimum Go
requirement. No later than S15 and before implementing TUI state, repository
fixtures must additionally verify:

1. The current Bubble Tea v2 module path, stable release, and minimum supported
   Go version.
2. Program startup, command delivery, streaming message delivery, resize,
   paste/input, cancellation, and terminal restoration APIs.
3. A deterministic test strategy that does not require a real interactive
   terminal for state transitions and rendered output.
4. Race-free shutdown when Application cancels a run while UI messages remain
   queued.
5. Supported-platform behavior for macOS and Linux terminals.

The spike result must record the selected version and APIs in the dependency
lock or a follow-up compatibility note. This ADR does not claim that any
specific v2 release or API has already passed.

## Revisit triggers

- The S04 or S15 gate shows that the v2 line cannot satisfy the supported Go or
  platform contract.
- Framework lifecycle behavior makes Application-owned cancellation or
  deterministic tests infeasible.
- The product stops being a terminal application.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
