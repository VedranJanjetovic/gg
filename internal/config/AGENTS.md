# AGENTS.md — `internal/config`

Owns the YAML schema, validation, configuration stores, resolution, and the
agent/model catalog.

**This package must not import `internal/pipeline`.** The `config.Phase` enum
(`config.go:45-63`) deliberately duplicates the phase strings from
`pipeline.PhaseID` — that duplication is the price of the dependency direction,
not an oversight to clean up. `config` imports only `internal/robustio`.

## Two schema versions coexist

| Constant | Value | Applies to |
|---|---|---|
| `CurrentSchemaVersion` | 1 | global config, and legacy sparse project config |
| `CompleteSchemaVersion` | 2 | self-contained project folder config |

Both are the `version:` YAML field (`config.go:164`, `config.go:184`).
`validateVersion` (`validate.go:164`) rejects anything ≠ 1;
`validateCompleteProjectConfig` (`validate.go:60`) rejects anything ≠ 2.

## Decoding is strict — adding a field is a compatibility break

`store.go:513` sets `d.KnownFields(true)`. Unknown keys are rejected **by
design** (`docs/configuration.md:57`). Consequences of adding a field:

- Older gg binaries reject configs written by newer ones.
- Hand-written configs using the new key fail on older code.

Treat any schema addition as a versioned change, not an additive one.

## There are no migration functions — there is a classifier

Legacy files are **never silently rewritten**. `ClassifyProjectConfig`
(`store.go:41`) returns one of:

- `complete` — passes `ValidateCompleteProjectConfig`
- `migration_required` — readable for wizard prefill, but **must not** seed a new
  project until saved in complete form
- `malformed`

`gg run` on a `migration_required` config opens the wizard rather than upgrading
in place. The only legacy→complete path is `MaterializeCompleteProjectConfig`
(`config.go:265`), which runs at an explicit save boundary and stamps
`ModelProvenanceManual` on values carried over from legacy.

`complete_schema_test.go:201` asserts the on-disk bytes are **unchanged** after a
load-and-prefill cycle. Don't add write-on-read behavior.

## What the complete schema enforces

`complete_schema_test.go` pins all of this:

- every phase in `CompletePhaseOrder()` present, in exact order
- `Required` field equals `isRequiredPhase` for that phase
- required phases are enabled
- every settings tuple complete, including `provenance`
- `phase_overrides` absent (`validate.go:66`) — sparse overrides are illegal in a
  complete config

Adding a phase to `CompletePhaseOrder()` reclassifies **every existing on-disk
config** as `migration_required`. That is expected and tested
(`complete_schema_test.go:152` covers the missing-newer-phase prefill), but it
means a phase addition is a user-visible migration event.

## Three different notions of "optional"

Do not treat these as synonyms:

| Set | Members | Meaning |
|---|---|---|
| `pipeline` `Optional: true` | grooming, planning, qa, build_checker, pr, ci | has an `Enabled` flag |
| `RemovablePhases()` (`config.go:82`) | grooming, planning, qa, build_checker, pr, ci | schema permits absence |
| `OptionalPhases()` (`config.go:217`) | qa, build_checker, pr, ci | **actually user-disableable** |

`RequiredPhases()` (`config.go:213`) requires grooming and planning enabled even
though both appear in `RemovablePhases()`.

## Sentinel errors

`ErrGlobalConfigNotFound` and `ErrProjectNotConfigured` (`config.go:202-208`) —
always compare with `errors.Is`, never string-match.

## Adding or renaming a phase

Six touchpoints live in this package alone: the `Phase` const, `removablePhases`
/ `fixedPhases`, `RequiredPhases()`, `OptionalPhases()`, `CompletePhaseOrder()`,
and `isSupportedPhase` in `validate.go:260`. That is a fraction of the full
lockstep — start from the root [`AGENTS.md`](../../AGENTS.md).
