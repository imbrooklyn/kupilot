# ADR-0054: Preserve Structured Response Compatibility Across Turns

- Status: Accepted
- Date: 2026-09-14
- Amends: ADR-0022, ADR-0038, ADR-0043, ADR-0047, and ADR-0052

## Context

Kupilot validates new Agent finals with strict response schema 2, but retained
assistant answers were reconstructed as the retired three-member response
shape. That representation restored no authority, but it contradicted the
current system instruction inside the same model conversation. Models that
imitate recent assistant output could therefore return the retired shape on a
later question and fail the current validator even when they followed the most
recent conversational example.

The opt-in live endpoint test had the same gap: its final-response assertion
required only `answer_markdown`, `evidence_citations`, and `proposed_actions`.
It could report protocol success without proving the current outcome, stop,
limitation, and clarification members. Prompt instructions alone are also less
reliable for some otherwise compatible local models than the optional
Chat Completions JSON-object response constraint.

This is a protocol-consistency and conformance problem. It is not a reason to
accept arbitrary prose, weaken Evidence validation, parse Tool authority from
text, retry a request, or introduce another Agent loop.

## Decision

### Current-schema retained assistant context

Every eligible retained assistant answer is represented to Eino as one complete
response schema 2 object. It contains the validated visible Markdown, empty
Evidence citations and proposed actions, schema version 2, outcome `answer`,
stop reason `completed`, and empty limitations and questions. The system prompt
continues to label the whole retained message as untrusted conversational
context and instructs the model to use only `answer_markdown`.

The empty authority-bearing arrays are deliberate. Historic Evidence, action,
approval, execution, scope, and generations never regain authority. The added
members keep the model-visible grammar internally consistent; they do not
claim that a historic answer remains current or semantically correct.

The system prompt includes one compact canonical no-Evidence schema 2 example
and advances its version. The example is syntax guidance only and cannot
change runtime policy or validation.

### Explicit JSON-object response constraint

Each fixed model profile has one code-owned `response_format` setting:

- `prompt` keeps the existing prompt-only structured response request; and
- `json_object` asks the pinned Eino OpenAI component to emit the standard
  Chat Completions `response_format: {"type":"json_object"}` member.

`prompt` remains the default so an already configured endpoint does not acquire
an unproved capability requirement. `json_object` is selected explicitly only
after the exact endpoint is known to support it. There is no endpoint probe,
model-name inference, automatic negotiation, error-text matching, fallback,
or retry.

For the `agent` role, the response constraint applies to streamed ordinary and
plan-only Agent requests, including requests that also carry the fixed Tool
catalog. Agent-summary requests remain plain text and therefore use the same
profile and HTTP client without the structured response constraint. For the
optional `approval_reviewer`, the constraint applies to its one strict JSON
decision request. This creates no new role, origin, credential, Agent,
ChatModelAgent, Runner, ReAct loop, provider route, or conversation state.

The adapter uses the pinned Eino configuration type for response-format
serialization. It does not reconstruct or modify Eino's serialized request
body. A JSON-object response is still untrusted: duplicate keys, missing or
unknown members, invalid enum values, malformed citations or actions, stale or
foreign Evidence, invalid clarification, limits, and sensitive content all
fail the same local validators. The response constraint cannot create Tool,
Evidence, action, approval, or execution authority.

### Conformance evidence

Deterministic request-recording tests must prove the exact response-format
field is present only for an explicitly constrained structured request and is
absent from Agent-summary and prompt-only requests. Tool calls must remain
structured and usable with the constraint. No unsupported response-format
error may cause a second request or a prompt-only retry.

The bounded live model suite must validate the complete current response
schema, then make a later request containing a reconstructed assistant answer
and validate the complete schema again. A three-member response is not current
schema conformance. Live evidence applies only to the exact Ollama version,
model identity, endpoint, configuration, and date observed; deterministic CI
remains the safety authority.

## Consequences

Multi-turn, same-process, resumed, and summarized-tail conversations no longer
show the model a retired assistant grammar. Compatible local models can use an
explicit protocol feature to reduce formatting drift without weakening local
validation.

Profiles whose endpoints do not implement JSON-object response formatting keep
`prompt`. Selecting `json_object` for such an endpoint fails safely on its one
request. Kupilot does not silently downgrade or resend content.

## Security and privacy impact

Retained assistant context remains bounded safe Message content and contains no
raw model response, Evidence payload, credential, provider object, or current
authority. The response-format selection is non-secret profile configuration.
It does not alter the consent tuple or add a data category because it changes
only the response syntax requested from the same role and origin.

Raw responses remain excluded from Application, TUI, ordinary logs, SQLite,
and export. Invalid output exposes only the existing fixed safe failure class.

## Validation

Tests cover:

1. exact schema 2 retained-answer encoding and current-validator acceptance;
2. second and later questions, same-process and resumed context, and summary
   recent tails without retired response shapes;
3. `prompt` and `json_object` configuration defaults, parsing, validation,
   writing, inheritance, and unknown-value rejection;
4. exact Eino request serialization for ordinary, plan-only, Reviewer, Tool,
   and Agent-summary calls;
5. complete response schema success plus missing, duplicate, unknown,
   malformed, action-bearing, Evidence-invalid, oversized, and sensitive
   output rejection;
6. one-request failure with no detection probe, fallback, retry, Tool call,
   persistence, approval, or execution; and
7. an explicitly authorized bounded loopback run against the exact configured
   Ollama model, reported separately from deterministic evidence.

## References

- [Model Compatibility](../model-compatibility.md)
- [Agent Runtime](../agent-runtime.md)
- [Configuration](../configuration.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0052: Use Typed Agent Outcomes, Evidence Integrity, and Preflight](0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
