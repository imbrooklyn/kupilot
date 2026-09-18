# ADR-0003: Compose Native Eino Directly in Application

- Status: Accepted

## Context
Native Eino already provides message state, Tool pairing, ReAct iteration,
Runner events and summarization. A parallel framework would duplicate lifecycle
ownership and risk losing native reasoning state.

## Decision
Application is the sole use-case and execution authority and directly composes
one stable Eino ChatModelAgent and Runner. Domain contains pure project-owned
values and invariants. cmd/kupilot is the sole composition root. CLI and TUI are
delivery adapters; Tools own exact handlers and consumer ports; Kubernetes and
SQLite remain confined adapters. Cross-boundary values are concrete project DTOs.

Use native Eino Chat Completions or Responses according to explicit configuration.
Chat Completions Agent calls stream. Responses Agent calls use Generate because
agenticopenai v0.2.2 Stream loses encrypted reasoning in output-item completion;
do not emulate streaming or disable reasoning. Reviewer and summary calls are
non-streaming and Tool-free. Native reasoning stays bounded and ephemeral within
the current run, returning only to the same consented origin.

Disable SDK retries, server-side response storage, automatic response caching
and truncation. No hosted Tools, remote conversation store, checkpoint, repair,
fallback or second Agent is admitted. Project hooks retain preflight, consent,
generations, budgets, Tool policy, Evidence validation and persistence barriers.
The HTTP guard owns origin, credential, byte, timeout and body-closure checks.

Build ToolInfo with Eino's public NewParamsOneOfByJSONSchema constructor. Preserve
the exact closed schema and defensive snapshots. Retain narrow consumer-owned
interfaces only where an actual isolation or test seam requires them.

## Consequences and alternatives
Native messages retain protocol capabilities without a neutral provider facade.
Responses provisional text remains unavailable until a stable native component
passes the reasoning-stream fidelity fixture. A vendor fork or raw-event repair
adds an unsupported protocol implementation and is not an alternative.

## Validation
Inspect the exact pinned Eino source and tests. Run native request, Tool-pairing,
reasoning, history, cancellation, failure, no-retry and import-boundary tests.
Deterministic fixtures establish local correctness; separately authorized live
tests establish only the exact endpoint configuration tested.
See [Agent Runtime](../agent-runtime.md) and [Architecture](../architecture.md).
