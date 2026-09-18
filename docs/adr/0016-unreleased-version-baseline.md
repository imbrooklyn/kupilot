# ADR-0016: Keep One Unreleased Version-One Baseline

- Status: Accepted

## Context
Kupilot is not yet published. Development state does not establish a supported
compatibility contract.

## Decision
Keep product version v0.1.0 and project-owned schema, prompt, catalog, policy and
export formats at initial version 1 until the first publication. Correct the one
initial checksummed SQLite schema in place. Do not add development migrations,
compatibility readers, tags or alternate product versions.

Dependency/API versions and runtime generation/sequence/concurrency counters
retain their own semantics. ADRs are numbered consecutively from 0001 in the
current decision index; their identifiers are not product format versions.

Current documents describe the effective contract, implementation boundaries and
explicit limitations. ADRs record current decisions, rationale, consequences and
validation requirements; development refactoring narratives and superseded
decision chains are not retained in the working documentation tree. Git retains
source history. Test results must identify actual current evidence and never
convert an old run into a new success claim.

Unknown, corrupt or incompatible local storage fails closed without silent
deletion or recreation. An explicit user-controlled backup/reset is separate
from normal startup. After publication, migrations become forward-only,
versioned and checksummed, and released migrations must not be edited.

## Consequences and validation
There is one current baseline without development compatibility machinery.
Public decisions must be changed explicitly before dependent implementation.
Test initial creation, checksums, incompatible-state rejection, strict version-1
configuration, sensitive-data exclusion and build version identity. Audit all
documentation references and code/config/schema claims after a baseline change.
