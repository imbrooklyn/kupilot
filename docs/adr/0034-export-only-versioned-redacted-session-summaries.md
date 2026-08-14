# ADR-0034: Export Only Versioned Redacted Session Summaries

- Status: Accepted
- Date: 2026-08-14

## Context

Users need a portable account of their own local KuPilot Session without
turning the product into a Session browser, backup system, or generic data
exporter. A useful summary may include retained conversation text and
Diagnosis provenance, but an unrestricted serialization could also expose raw
Tool data, container output, model traffic, credentials, approval authority,
or repository implementation details.

ADR-0017 excludes raw prompts and outputs from every durable sink and requires
a separate decision before adding a user-controlled export. ADR-0023 requires
the TUI to keep one composer and no second Session-management surface. This ADR
defines the narrow export category that satisfies those constraints.

## Decision

KuPilot may export one explicitly selected, resumable standard-persistence
Session as a deterministic Markdown document. Export is available only through
the existing `/privacy` flow, uses the existing composer for target entry, and
requires a target-bound confirmation. It adds no CLI command, file browser,
Session page, automatic export, remote destination, or model call.

The exported document uses the fixed `kupilot.export-summary.v1` schema and an
explicit projection. Its only eligible fields are:

- Schema version, export time, and explicit truncation state.
- Session ID, sanitized title, standard privacy mode, creation and update times,
  and the historic display-only Context and Namespace candidate.
- Committed user and final assistant Messages: role, creation time, and bounded
  sanitized content.
- The four Diagnosis collections: confirmed facts, hypotheses, missing
  information, and recommended actions. References, confidence, falsifier,
  impact, risk, and prerequisites remain bounded project-owned fields, and
  recommended actions remain explicitly not executed.
- Referenced accepted Evidence summaries: Evidence ID, category, safe
  ResourceRef display fields, safe source path, observation time, availability,
  partial, truncated, and redaction state, plus the bounded concise fact while
  operational detail remains eligible.

Expired Evidence is represented as unavailable and its removed detail is not
reconstructed. The export never includes raw Tool input or output, raw or
complete container logs, raw Events, Kubernetes objects, complete prompts,
model requests, responses or streams, framework payloads, Secrets, ConfigMap
data, environment values, kubeconfig data or paths, credentials, approval
nonces or digests, execution authority, internal fingerprints, arbitrary
errors, target paths, or a generic serialization of repository entities.

Every eligible free-text field passes the local SecretRedactor and its own byte
ceiling before Markdown rendering. External text is escaped as Markdown data,
not interpreted as document structure. The complete rendered document passes
the export guard and aggregate byte ceiling again. Redaction never makes a
prohibited source eligible.

Application owns selection, policy, confirmation, serialization intent,
operation serialization, and the content-free audit event. A narrow SQLite
projection supplies one consistent allowlisted snapshot. The filesystem
adapter receives only the final bounded bytes and an explicit absolute target.
It rejects symlink path components, non-regular or existing targets, a final
parent that is not owner-only, and non-sticky ancestor directories writable by
group or others on supported Unix platforms. It writes a `0600` temporary file
in the target directory, synchronizes it, publishes it atomically without
replacing an existing target, and removes the temporary file on every failure.

The audit event records only its fixed event type, Session ID, UTC time,
outcome, and export policy version. It contains neither exported content nor
the target path. Failure to persist this pre-export audit denies the filesystem
write. Restart never restores or replays an export confirmation.

The existing Application operation boundary serializes export with Session
deletion and an active run denies export. Every execution attempt consumes its
confirmation, including an active or busy denial. Cancellation before
confirmation causes zero export-snapshot, export-audit, and filesystem calls.
If deletion commits first, export reports the Session unavailable and writes no
file; if export owns the operation first, deletion waits or is rejected until that
bounded operation finishes.

Resume discovery may perform a bounded local match over only sanitized title,
the canonical UTC display timestamp, and display-only Context and Namespace.
Results are limited before crossing the repository boundary and use a fixed
stable ordering. No Message preview or content index is introduced.

## Consequences

Users can create a reviewable local summary while the export surface remains
smaller than the retained database model. The output can still contain
operationally sensitive names and conversation text, is not encrypted, and is
protected only by local path controls and the operating system. Users remain
responsible for the exported file after creation.

The projection, Markdown renderer, filesystem publication, and audit path need
independent deterministic tests. A generic export, backup, exact replay, or
forensic-erasure claim remains prohibited.

## Security and privacy impact

Export creates a new local durable copy of explicitly eligible data. Source
exclusion, allowlist projection, two-pass sensitive processing, strict limits,
owner-only permissions, no-overwrite publication, and content-free audit reduce
that exposure. They do not provide encryption, tamper resistance, anonymous
data, or forensic deletion from storage media, backups, snapshots, swap, or
filesystem journals.

Synthetic canary tests must prove byte-for-byte absence of every prohibited raw
and credential category from the Markdown bytes, audit rows, safe errors, and
temporary and final files. Denial tests must also assert zero filesystem,
model, Kubernetes, Tool, approval, and executor calls as applicable.

## Validation

Deterministic tests use temporary SQLite databases, temporary directories,
fake clocks, and fake TUI consumers to cover:

1. The exact schema allowlist, per-field and aggregate limits, Markdown
   escaping, two-pass redaction, expired Evidence, and prohibited canaries.
2. Explicit absolute targets, owner-only directory and file permissions,
   symlink rejection, directory rejection, no overwrite, atomic publication,
   cancellation, injected write and synchronization failures, and cleanup of
   every temporary file.
3. Content-free and path-free audit metadata, audit failure before filesystem
   action, concurrent deletion, active-run denial, stale confirmation, and
   restart without replay.
4. Bounded stable Session matching across title, timestamp, Context, and
   Namespace while bare startup still performs no history query.
5. The single-composer TUI flow, category preview, target-bound confirmation,
   cancellation, success, and safe failure rendering.

## Revisit triggers

- Adding another format, overwrite mode, automatic destination, multiple
  Sessions, search over conversation content, export CLI, backup, sync, or
  remote transfer.
- Exporting a new field or a source currently prohibited by ADR-0017.
- Supporting a platform that cannot provide the required no-replace atomic
  publication and owner-only path behavior.

## References

- [ADR-0013: Layered Architecture and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0017: Do Not Persist Full Prompts or Raw Outputs](0017-do-not-persist-full-prompts-or-raw-outputs.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
- [Data Retention Contract](../data-retention.md)
- [Privacy Overview](../privacy-overview.md)
- [Security Threat Model](../security.md)
