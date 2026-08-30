# ADR-0003: Keep the Product Agent-First

- Status: Accepted
- Date: 2026-08-05

## Context

Kubernetes diagnosis often requires choosing a small sequence of observations
rather than browsing every object. A resource-first terminal interface would
pull Kupilot toward inventory, navigation, object viewing, and direct cluster
management. That would duplicate mature tools while weakening the distinction
between observed Evidence and model inference.

A chat-only product without structured reads would have the opposite problem:
it could produce fluent suggestions without gathering current, traceable
Evidence.

## Decision

Kupilot will be Agent-first.

The primary interaction is a natural-language operational question. For each
question, one AgentRun chooses a bounded Evidence path from the versioned
structured capability catalog and produces a validated answer. The TUI helps
the user:

- Establish and see the active ClusterScope.
- Optionally bind one ResourceRef through a bounded Picker.
- Submit diagnostic intent and cancel the run.
- Supervise Tool purpose, status, safe summaries, and Evidence-backed output.

The Picker is an input aid, not a general browser. Kupilot will not make a
resource tree, inventory table, full object view, direct manipulation surface,
or background monitor its primary interaction.

Feature admission uses this question:

> Does the capability help the Agent gather bounded Evidence and produce a safer
> Diagnosis, or does it replace the Agent with another cluster interface?

A capability in the second category is outside this architecture even when it
would be useful in another product.

## Consequences

Positive consequences:

- Product work stays focused on Evidence collection and Diagnosis quality.
- Every cluster observation has a bounded Tool contract and visible run context.
- The TUI remains small enough to supervise the Agent rather than becoming a
  second operational interface.
- Diagnostic category fixtures can test minimal Evidence paths and forbidden
  assertions.

Costs and constraints:

- Kupilot will not satisfy users seeking a direct general Kubernetes browser or
  imperative cluster-management interface.
- The Agent loop and Diagnosis validator require careful deterministic policy
  around budgets, Evidence, gaps, and uncertainty.
- The bounded Picker and Tool catalog may require users to use another tool for
  tasks outside the admitted diagnostic scope.

## Alternatives considered

- A resource-first TUI with an assistant panel was rejected because resource
  navigation would become the product center and expand cluster access.
- A broad cluster management TUI with embedded model features was rejected
  because it combines two products and creates ambiguous authority.
- A chat interface based only on pasted text was rejected because it cannot
  provide current, runtime-generated Evidence.

## Security and privacy impact

Agent-first does not give the model authority. Fixed runtime Tool schemas,
scope binding, allowlists, budgets, generation checks, local projection, and
redaction constrain every read. Prompt instructions are not a permission
boundary.

The bounded interaction reduces incidental data access compared with general
browsing, but resource names, Events, and log excerpts may still be sensitive.
Consent and data minimization remain required.

The TUI must represent a recommended action as unexecuted. Agent-first must not
be interpreted as autonomous remediation.

## Validation

The [Product Contract](../product.md) defines the current operational mission,
and the [Scope](../scope.md) fixes the admitted capability catalog and explicit
non-goals. The [Architecture](../architecture.md) routes all cluster reads
through bounded Tool or Picker contracts.

Acceptance tests must validate the Evidence paths and ensure that the TUI has no
alternate direct cluster path.

## Revisit triggers

- Repeated, documented diagnostic failures show that a bounded context feature
  is necessary and it passes the feature admission gate.
- The product mission is explicitly changed from diagnostic Agent to cluster
  management interface.
- Evidence shows that natural-language intent cannot serve the admitted
  diagnostic categories safely or effectively.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Glossary](../glossary.md)
