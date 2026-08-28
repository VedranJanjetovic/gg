# AGENTS.md — `internal/pipeline`

Owns the canonical phase list, phase resolution, Development subphases, and
execution snapshots. Imports `config`, `execution`, and `state` — and must never
be imported by any of them.

Adding, renaming, or reordering a phase is a repo-wide lockstep change. Start
from the root [`AGENTS.md`](../../AGENTS.md), not here.

## `contract_text.go` — generated, but there is no generator

The header says `Code generated ... DO NOT EDIT`. There are **zero
`go:generate` directives in the repository**, no script, no Makefile target.
It is hand-maintained.

Each entry must be byte-identical to `skills/canonical/<name>/<name>.md`,
frontmatter included. Drift tests cover only 4 of 10 phases
(`contract_text_test.go:23` — development, qa, test-document, build-checker).
`acceptance_criteria`, `grooming`, `planning`, `rebase`, `pr`, and `ci` will
drift with CI green.

Editing QA text also risks six hardcoded substring assertions at
`contract_text_test.go:46`. A weaker structural guard covers all ten:
`resolve_test.go:295` requires every enabled phase's contract to be non-empty
and to contain `Stable phase ID:` and `Success Criteria`.

Rebase is a known exception: `PhaseContract` appends text at runtime
(`contract_text.go:19-27`), so it can never byte-match its source.

## Snapshot schema versions

Four constants at `snapshot.go:14-22`:

| Constant | Value | Meaning |
|---|---|---|
| `pipelineSnapshotWrapperVersion` | 1 | outer `state` wrapper |
| `legacyExecutionSnapshotSchemaVersion` | 1 | QA before Rebase |
| `executionSnapshotSchemaVersion` | 2 | current execution snapshot |
| `projectExecutionSnapshotSchemaVersion` | 3 | current project snapshot |

`executionPhaseOrder(version)` (`snapshot.go:553`) is a switch with one case per
version. v2 and v3 currently return the **same** order; only v1 differs (`qa`
before `rebase`). Adding or reordering a phase means either editing every case —
which breaks stored snapshots — or minting a new version with its own case.

**The decoder is strict**: `snapshot.go:539` sets `DisallowUnknownFields()` and
rejects trailing JSON. Adding a field to `executionSnapshot` (`snapshot.go:24`)
breaks compatibility in *both* directions — older binaries will refuse snapshots
written by newer ones.

`gg resume` must keep reading old snapshots. `snapshot_legacy_order_test.go`
holds a verbatim schema-1 document as an inline Go string literal and asserts it
restores, re-snapshots, and upgrades. There are no golden files and no `-update`
flag anywhere in this repo.

## The `legacyOrder` flag

`resolve.go:31` — a plan restored from a pre-swap snapshot carries
`legacyOrder: true` so re-snapshotting records that order as deliberate rather
than failing validation forever. `isLegacyOrderSnapshot` (`snapshot.go:517`)
treats either the flag or schema v1 as legacy.

This whole mechanism exists because Rebase moved ahead of QA once. That single
reorder produced a new schema version, the flag, `executionPhaseOrderForSnapshot`,
and a dedicated test file. Budget the same for any future reorder.

Order is validated per schema version — `validateExecutionSnapshot` errors with
`phases are not in schema-%d order` (`snapshot.go:486`), and `resolve_test.go:437`
pins both failure directions.

## `Optional` does not mean user-disableable

`PhaseMetadata.Optional` means "carries an `Enabled` flag." Grooming and Planning
are `Optional: true` yet required by `config.RequiredPhases()`. `Resolve` will
drop them if a config says disabled (`resolve.go:105`). The set users may
actually turn off is `config.OptionalPhases()` — qa, build_checker, pr, ci.

## Runtime self-validation

These run on every `Resolve`, so a mistake fails fast rather than silently:

- `validateDefaultPipeline` (`resolve.go:115`) re-derives `DefaultPipeline()` and
  errors on any count, order, or `Optional` difference.
- `validateResolvedPhases` (`resolve.go:147`) errors on a missing or unknown
  optional phase.
- `validatePRCICompatibility` (`resolve.go:180`) rejects CI enabled while PR is
  disabled.

`PhaseContracts()` returns a defensively copied map — keep it that way
(`resolve_test.go:317`).

## Grandfathering convention

`PlanningContractVersion = 1` (`snapshot.go:22`): a **missing** marker means the
project predates the strict Planning contract and is exempt. Follow this pattern
for any future tightening — add a marker and grandfather the absence. Do not
retro-enforce against stored state.
