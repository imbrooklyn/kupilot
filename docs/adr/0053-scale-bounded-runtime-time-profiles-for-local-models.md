# ADR-0053: Scale Bounded Runtime Time Profiles for Local Models

- Status: Accepted
- Date: 2026-09-07
- Amends: ADR-0007, ADR-0022, ADR-0039, ADR-0044

## Context

The existing two-, ten-, and thirty-minute AgentRun profiles and their
one-, two-, and five-minute model-request limits are too narrow for some
explicitly configured local OpenAI-compatible models. A normal read-only
investigation can require one model request to select a Tool, one bounded
Kubernetes read, and another model request to produce the strict final answer.
The final request can exhaust its child deadline even after the Tool completed
successfully.

Increasing time does not justify an unbounded loop, another attempt, a broader
Tool, or restored authority. Scope, policy, consent, call-count, byte, cost,
Evidence, cancellation, and no-blind-retry controls remain independent.

## Decision

The code-owned time dimensions of the three immutable run profiles are:

| Boundary | Compact | Balanced (default) | Extended | Hard ceiling |
| --- | ---: | ---: | ---: | ---: |
| AgentRun wall clock | 10 min | 30 min | 60 min | 60 min |
| Agent model request | 300 sec | 600 sec | 900 sec | 900 sec |
| Tool and Kubernetes request | 60 sec | 120 sec | 180 sec | 180 sec |

The remaining count, byte, item, data-source, summary, Reviewer, and cost-unit
dimensions retain their existing profile values. A child request deadline is
always the lesser of its profile limit, any explicit configuration tightening,
and the owning AgentRun's remaining wall time. Existing configuration with a
lower `models.agent.request_timeout_seconds` value remains an intentional
tightening and is not rewritten during upgrade.

The default configuration permits up to 900 seconds so that the selected
profile supplies the effective ceiling: 300 seconds for `compact`, 600 seconds
for the default `balanced`, and 900 seconds for `extended`. Values remain
finite and validation rejects one-over values before model, Tool, or
Kubernetes I/O.

Timeout does not create retry authority. Kupilot does not automatically repeat
a model request, Tool call, Kubernetes request, action, or user input after a
deadline. Cancellation and generation invalidation continue to stop admitted
work, and every request remains joined by its existing owner.

## Consequences

Slow local models have enough time to complete the post-Tool strict response
under the default profile. A stalled endpoint or Kubernetes API may now remain
active longer before deterministic cancellation, while the same finite
call-count, byte, and total-run ceilings bound exposure.

`balanced` remains the default and `extended` remains meaningful through its
larger call, step, data, and time envelopes. Operators who previously chose a
lower explicit request timeout keep that narrower behavior.

## Security and privacy impact

Longer deadlines do not admit new data, scope, origins, Tools, actions, or
permissions. They can extend the duration of an already admitted connection,
so scope and policy generations are checked at the same pre-I/O, post-return,
event-acceptance, and execution boundaries. No response, request, credential,
or raw error is newly retained.

## Validation

Deterministic tests must cover every exact profile value, each hard ceiling and
one-over rejection, configured tightening, remaining-run deadline capping,
cancellation, and Kubernetes client construction. Tests must use fake clocks,
scripted model transports, and loopback Kubernetes request recorders; no live
model or cluster is required.

## References

- [Agent Runtime](../agent-runtime.md)
- [Configuration](../configuration.md)
- [Model Compatibility](../model-compatibility.md)
- [Kubernetes Compatibility](../kubernetes-compatibility.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](0039-use-configurable-runtime-budget-profiles.md)
