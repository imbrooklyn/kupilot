# ADR-0011: Route Deterministic Risk Through Permission Profiles

- Status: Accepted

## Context
RBAC and model judgment cannot determine whether an admitted operation should
run automatically or require explicit supervision.

## Decision
Code classifies normalized operation/data/sink/network/side effects as safe,
review, critical or deny. Text, Reviewer rationale and RBAC cannot lower risk.

| Profile | Routing |
| --- | --- |
| ask, default | safe automatic; review and critical human |
| read-only | safe automatic; admitted sensitive reads human; mutations and remote/local execution denied |
| auto-review | safe automatic; review may use the optional Reviewer; critical human |
| full-access | admitted enabled review/critical automatic only after explicit high-risk selection and local policy permission |
| custom | exact safe automatic/human/deny; review automatic/human/Reviewer/deny; critical automatic/human/deny, default human |

No profile grants RBAC, broadens scope/consent, enables default-off capabilities,
exposes credentials, changes risk, bypasses audit/revalidation/budgets or overrides
deny. Reviewer cannot route safe or critical operations.

A human Session rule covers only an exact review operation, scope, target pattern,
parameters or argv template, effects, ceilings and expiry. It is revocable,
current-process/current-Session only, never durable or model-created, and cannot
cover critical/deny. Profile, catalog, rule or relevant policy changes advance
policy generation and invalidate dependent authority before cancellation.

## Consequences and validation
One deterministic routing model serves every supervised operation. More
permissive profiles change routing only, not capability or execution authority.
Test the full routing matrix and zero-call denials, generation invalidation,
rule expiry/revocation and Reviewer failure. See [Approval Guide](../user-guide/approval.md)
and [Security](../security.md).
