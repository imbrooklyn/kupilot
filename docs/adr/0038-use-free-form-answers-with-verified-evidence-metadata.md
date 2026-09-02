# ADR-0038: Use Free-Form Answers with Verified Evidence Metadata

- Status: Accepted
- Date: 2026-08-30
- Amended: 2026-09-02
- Supersedes: ADR-0015

## Context

The four mandatory sections in the original Diagnosis contract make every
response look like a generated report, even for a one-line lookup, a resource
list, a comparison, or an operational follow-up. The fixed renderer also
discards the model's useful Markdown organization and makes streaming prose
visibly change shape when the terminal result arrives.

Evidence provenance is still valuable, but presentation structure does not
need to be the enforcement mechanism.

## Decision

The visible terminal result is bounded free-form Markdown authored for the
current question. Kupilot does not inject mandatory headings, empty `None.`
sections, scope boilerplate, or a fixed recommendation footer. A structured
inventory or comparison with multiple resources of the same Kind and shared
attributes defaults to one compact table per Kind, without requiring a format
request from the user. Short answers, lists, and longer explanations remain
appropriate when the content is not genuinely tabular.

The final model protocol remains structured internally. It contains:

- `answer_markdown`, the exact candidate visible answer;
- `evidence_citations`, bounded claim-to-Evidence references; and
- `proposed_actions`, bounded typed operation proposals, when an admitted
  action is relevant.

These metadata collections are not rendered as a fixed report. Runtime checks
that every cited Evidence ID belongs to the current run and scope, removes
invalid or duplicate references, records a visible validation warning when
support is lost, and keeps action proposals separate from execution authority.
Only deterministic Tool handling creates Evidence. Only Application approval
state can authorize an action.

Answers without cluster observations are valid for explanations, capability
questions, and explicit unsupported or permission-denied outcomes. When the
answer asserts a current cluster fact, the model is instructed to attach the
supporting Evidence metadata. A successful run means that the local protocol
and safety checks completed; it is not a guarantee of causal correctness.

The model is instructed to emit `answer_markdown` as the first top-level member
of the final object. After Eino validates each decoded content chunk, a passive
projector may incrementally decode only that JSON string and publish bounded
provisional Markdown. The projector must apply an exact model-credential guard,
cross-chunk sensitive-value handling, terminal-control normalization, scope
checks, and independent byte and run-wide event ceilings before publication.
It does not decide whether the assembled response is final text or a Tool call
and cannot create Tool, Evidence, citation, proposal, approval, or execution
authority.

Application may coalesce safe provisional fragments for bounded UI refreshes.
The TUI then replaces the draft atomically with the locally validated
`answer_markdown`. A requested Tool clears provisional prose from that earlier
model turn. Cancellation, timeout, stale scope, malformed or length-limited
output, and final validation failure replace the draft with the corresponding
safe terminal state. Provisional content is never committed as an assistant
Message or inserted into terminal-owned scrollback. The TUI must not replace a
valid free-form answer with a local fixed template.

The delivery layer parses the bounded answer as GitHub-Flavored Markdown and
renders project-owned, width-aware terminal blocks. A table uses a compact grid
when its columns remain readable and falls back to per-row key/value records on
a narrow terminal. A compact one-line table is repaired only when its header,
delimiter, column count, body rows, and trailing prose are unambiguous; the
stored `answer_markdown` remains the exact validated source. Invalid or
unsupported syntax degrades to inert text rather than an active terminal
feature. Prose wrapping uses Unicode Standard Annex #14 opportunities adjacent
to Han, Hiragana, Katakana, Hangul, and Bopomofo text. This keeps closing
punctuation with the preceding phrase, permits natural East Asian line breaks,
and leaves Latin technical identifiers and URLs to ordinary word wrapping.

The selected parser is `github.com/yuin/goldmark v1.7.8`. It is pure Go, has no
transitive module dependency, declares Go 1.19, and uses the MIT license. It is
confined to `internal/tui`; Domain, Application, Agent, persistence, model, and
Kubernetes packages do not import it. Terminal layout and styling continue to
use the already selected Lip Gloss v2 dependency.

Unicode boundary analysis uses `github.com/rivo/uniseg v0.4.7`, and
`github.com/charmbracelet/x/ansi v0.11.7` maps those visible-text boundaries
through code-owned ANSI styling before Lip Gloss wraps the result. Both modules
were already pinned in the Lip Gloss dependency closure, so using them directly
does not expand the module graph. Both are pure Go and MIT-licensed; their Go
1.18 and Go 1.24.2 declarations are compatible with the repository's Go 1.25
minimum.

## Consequences

Simple questions receive simple answers and complex investigations can use the
Markdown structure that best fits them. Evidence detail remains available on
demand without dominating every response.

Runtime cannot prove semantic equivalence between prose and a cited fact. It
therefore exposes lost-citation warnings and preserves accepted Evidence rather
than claiming that structured metadata makes model interpretation infallible.

## Security and privacy impact

Markdown is untrusted model output. It passes the existing text normalization,
sensitive-value, size, terminal-control, and persistence policies before any
sink. Links and code blocks are inert terminal text; they do not create network
or command execution. The renderer emits no OSC hyperlink, fetches no image,
executes no code, and treats raw HTML as visible inert text.

Streaming does not weaken that order. Recognition state retains the suffixes
needed to detect exact credentials, sensitive patterns, UTF-16 JSON escapes,
and terminal control strings split across provider chunks. A blocked candidate
is not partially emitted. Raw SSE, JSON envelopes, citation metadata, proposed
actions, reasoning content, and provider metadata never become provisional
Markdown.

An action phrase or proposal in Markdown never creates approval, execution, or
verification state. Those states come only from typed Application events.

## Validation

Tests must cover short prose, lists, wide and narrow tables, conservative inline
table repair, mixed East Asian and Latin prose, protected closing punctuation
and technical identifiers, code blocks, inert links, citations, no-Evidence
answers, invalid and duplicate Evidence IDs, sensitive output, oversized
Markdown, terminal controls, proposed-action validation, and persistence
round-trips. Rendering tests must prove that no mandatory four-section template
or active terminal link is added. Streaming tests must also cover fragmented
JSON strings and Unicode escapes, first-fragment visibility, bounded
coalescing, Tool-turn reset, scope change, cancellation, timeout, malformed and
length-limited responses, split credentials and terminal controls, event
ceilings, final replacement, and exclusion from persistence and terminal
scrollback.

## References

- [Product Contract](../product.md)
- [Architecture](../architecture.md)
- [Privacy Overview](../privacy-overview.md)
- [ADR-0014: Isolate Runs with ClusterScope Generation](0014-cluster-scope-generation-isolation.md)
- [Goldmark v1.7.8](https://github.com/yuin/goldmark/tree/v1.7.8)
- [Goldmark License](https://github.com/yuin/goldmark/blob/v1.7.8/LICENSE)
- [uniseg v0.4.7](https://github.com/rivo/uniseg/tree/v0.4.7)
- [uniseg License](https://github.com/rivo/uniseg/blob/v0.4.7/LICENSE.txt)
- [Charm ANSI v0.11.7](https://github.com/charmbracelet/x/tree/ansi/v0.11.7/ansi)
- [Charm ANSI License](https://github.com/charmbracelet/x/blob/ansi/v0.11.7/LICENSE)
