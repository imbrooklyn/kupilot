# Diagnosis Quality Baseline

## Purpose and oracle

This baseline defines deterministic acceptance for the eight admitted
diagnostic categories. It measures Evidence provenance, visible uncertainty,
allowed and forbidden semantic assertions, and unexecuted recommendations. It
does not score prose style, compare complete natural-language answers, use a
live model, or delegate judgment to another model.

The Product Contract and ADR-0015 remain authoritative. A successful fixture
means the runtime honored the Evidence and Diagnosis contract for a bounded
synthetic conversation; it does not establish a universal root-cause accuracy
rate.

## Reproducible method

The primary quality command is:

```sh
GOTOOLCHAIN=go1.25.13 go test -count=1 ./internal/agent \
  -run '^(TestDiagnosisScenarioFixtures|TestDiagnosisRubric.*)$'
```

The offline integration command is:

```sh
GOTOOLCHAIN=go1.25.13 make test-e2e
```

Both commands use the scripted local model, fixed Tool binding, synthetic Tool
results, runtime Evidence registry, final Diagnosis validator, and human-
reviewed assertion IDs. They require no network, credential, cluster, user
state, or model endpoint. A failure is a quality regression or an invalid
fixture until its cause is explained; it is never converted to a pass by
weakening the rubric or changing prose alone.

## Global acceptance thresholds

Every run must satisfy all of these thresholds:

1. The Tool order exactly matches the category policy and the catalog remains
   the six fixed read-only Tools.
2. Every accepted Evidence item has the expected current AgentRun, immutable
   ClusterScope, ToolInvocation, category, safe projection, and exact
   observation time.
3. One hundred percent of confirmed facts cite accepted same-run Evidence, and
   every cited fixture item directly supports the reviewed assertion without
   truncation.
4. Zero forbidden confirmed assertions, unknown Evidence references, executed
   recommendations, or missing four-part sections are accepted.
5. Every permission denial, stale observation, conflict, sensitive block,
   absence, unsupported reference, partial result, or truncation required by a
   fixture appears as the corresponding `missing_information` kind.
6. A hypothesis stays within the fixture confidence ceiling and has a
   falsifier. If it declares supporting Evidence IDs, at least one accepted,
   non-truncated fixture Evidence item explicitly supports the reviewed
   hypothesis assertion.
7. Removing an unregistered or duplicate hypothesis citation forces that
   hypothesis to `low` confidence and adds an `unsupported` gap without
   changing confirmed facts.
8. Every recommendation is structurally `executed=false` and renders the plain
   `Not executed` marker.
9. The Diagnosis observation window equals the earliest and latest accepted
   Evidence times, including accepted Evidence not cited by final text.
10. Partial or truncated Evidence produces `partial` detail state, and hostile
    instruction-like fixture text changes no Tool, scope, policy, language,
    approval, or execution authority.

No aggregate score can compensate for a failed threshold. The acceptance level
is therefore eight of eight categories passing every applicable invariant.

## Eight-category failure and boundary matrix

<!-- markdownlint-disable MD013 -->

| Category | Sufficient path | Required negative or boundary path | Forbidden promotion |
| --- | --- | --- | --- |
| CrashLoopBackOff | Waiting state, restart/termination state, recent BackOff Event, bounded previous log availability, and owner relationship | Previous logs forbidden; unsupported hypothesis citation is removed and exposed while uncertainty stays low | A single log excerpt proves the root cause |
| OOMKilled | Kubernetes-reported previous `OOMKilled`, exit state, previous-log availability, and owner context | State conflicts with a memory phrase in logs; unsupported hypothesis citation remains a visible low-confidence gap | A log phrase confirms OOMKilled or an exact memory cause |
| ImagePullBackOff | Waiting state plus a recent pull-failure Event | Events forbidden; registry, network, image, and authorization explanations remain hypotheses; unsupported citation is visible | A registry credential is wrong or a Secret value is known |
| Pod Pending | Pending and unscheduled state plus current scheduling Event and owner context | Stale Event conflicts with current scheduling state; unsupported citation cannot preserve confidence | Missing Events prove capacity shortage or an unobserved node/volume cause |
| Readiness probe failure | Not-ready state, probe Event, and bounded current-log availability | Probe Event absent and logs truncated; unsupported citation is removed without promoting the log excerpt | Service outage proves probe failure or truncated logs prove the cause |
| Deployment with no available replicas | Deployment availability gap, bounded ReplicaSet/Pod graph, and relevant Event | Related graph partial, Event read forbidden, and Pod cause absent; unsupported citation is visible | A Deployment condition alone or an unobserved Pod proves root cause |
| Failed Job | Failed controller state, owned Pod termination, and relevant Event | Job counts conflict with owned Pod phase and required Pod detail is absent; unsupported citation is visible | Failed count alone proves an application error or unobserved exit cause |
| Service with no ready Endpoint | Selector summary, matched Pod readiness, and address-free EndpointSlice counts | Relationship read forbidden and backend state unknown; unsupported citation cannot create backend Evidence | Service existence or a missing count proves backend state; any endpoint address is known |

<!-- markdownlint-enable MD013 -->

## Fixture and rubric rules

Fixtures contain only synthetic `example-*` resources and generalized facts.
They must remain ASCII project-authored text and must not contain a real Context,
Namespace, resource name, identity, endpoint, IP address, token, certificate,
Secret object or data, raw production log, complete prompt, or raw Kubernetes
object. Instruction-like text is permitted only as a bounded synthetic canary
whose inability to change authority is asserted.

Each fixture separates data from review annotations:

- `supports` associates one safe Evidence definition with stable semantic
  assertion IDs; it is not free-form scoring.
- Diagnosis annotations bind collection indexes to those reviewed assertion
  IDs without comparing entire sentences.
- Required missing kinds and maximum hypothesis confidence are explicit.
- Sufficient and limited cases use the same production adapter and validator;
  no fixture-only authorization or Evidence path is allowed.

A fixture is deleted when its category or source contract is removed through an
accepted public decision. It is generalized or replaced when it duplicates an
existing boundary. It must not be retained if its value depends on sensitive or
unverifiable source data.

## Evidence detail supervision

The single-screen TUI exposes a non-editable detail for machine-checked
Diagnosis citations. Every detail request is bound to an Evidence ID, AgentRun
ID, complete historic scope, UI request ID, and ordered sequence. A changed
scope generation, cancellation, mismatched identity, duplicate terminal result,
or late result cannot replace current display state.

The ViewModel contains only the Evidence category, allowlisted projected source
path, Context and Namespace with generation, resource API version/Kind/name,
UTC observation time, partial/truncation state, sensitive-filter status, and a
revalidated concise projection capped at 512 UTF-8 bytes. UID, resource version,
annotations, addresses, raw Tool results, raw logs, Kubernetes objects, model
traffic, credentials, and adapter errors are excluded by the typed projection,
local filtering, and deterministic sink tests.

Current accepted Evidence remains inspectable from bounded Application memory
when later read-only persistence is degraded. Explicitly resumed history
restores references only from a retained same-run Diagnosis. Deleted or expired
Evidence produces `expired`; an invalid source, unreferenced identifier, or
run/scope mismatch produces `unavailable`; neither state carries observation
content. Partial source data or display projection truncation remains visibly
`partial` and cannot be interpreted as a complete root-cause proof.

## Interpretation limits

These tests prove structural provenance and reviewed semantics for bounded
inputs. They do not prove live model compliance, current cluster state,
causality, complete incident coverage, natural-language quality in every
language, or a percentage accuracy claim. Live evaluation may supplement this
baseline but cannot replace it or become CI proof.
