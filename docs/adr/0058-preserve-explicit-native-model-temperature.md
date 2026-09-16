# ADR-0058: Preserve Explicit Native Model Temperature

- Status: Accepted
- Date: 2026-09-16
- Amends: ADR-0055

## Context

The pinned native Eino Ollama component v0.1.9 serializes the v0.1.0 client
Options value with `temperature,omitempty`. An explicit zero is consequently
absent from the HTTP request. The server then selects its own default instead
of receiving the frozen configured value. Nonzero temperature is transmitted.
This is a request fidelity defect, independent of model capability or malformed
provider Tool output. Existing local campaigns using a configured zero did not
establish behavior at an explicitly transmitted zero.

## Decision

Eino remains the sole native request serializer and response decoder. Inside
the existing guarded transport, one bounded correction restores only an absent
`options.temperature` when the frozen profile explicitly selects zero. A
present temperature must match the configured value at the native SDK's
float32 precision. Missing nonzero, null, duplicate, malformed, or mismatched
temperature and missing, null, or duplicate options fail before network I/O.
A nonzero setting that underflows to native zero is rejected, not treated as
an explicit zero.
No value is selected from model names, provider errors, or previous results.

The correction preserves every other request byte. Exact origin, path, method,
credential policy, cancellation, and request limits remain enforced. The final
serialized byte length, including the inserted scalar, is checked against the
same request ceiling. There is no second request, retry, fallback, new model
port, Agent, conversation loop, or persistence surface. The correction applies
to streaming and non-streaming calls through the same boundary.

Tool arguments, provider output, Evidence, scope, policy, generations, consent,
action authority, and commit barriers remain strict and are never repaired by
this correction. Unsupported Ollama or model behavior remains an explicit
failure. Raw requests and responses stay out of TUI, ordinary logs, SQLite,
and exports. User configuration is not rewritten.

## Evidence and consequences

Deterministic recording transports must assert field presence as well as value
at zero, the default, and the upper bound, including summary and full Agent
composition paths. They must cover exact byte limits, one-over rejection,
invalid structure, cancellation, and zero external calls on denial.

Historical local Ollama observations remain observations of the original wire
requests. Their configured zero must not be described as an effective zero or
evidence that lowering temperature improves reliability. New local conformance
must verify the transmitted setting before reporting a result. Neither a
synthetic fixture nor local conformance proves model semantic quality or
general support. This decision does not add compatibility handling for a
model that cannot produce valid Tool calls.

## References

- [Model Compatibility](../model-compatibility.md)
- [Configuration](../configuration.md)
- [ADR-0055](0055-use-explicit-openai-and-native-ollama-provider-kinds.md)
