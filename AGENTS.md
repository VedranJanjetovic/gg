# AGENTS.md

Guidance for AI coding agents. Human-oriented docs live in [`docs/`](docs/) and
are linked below rather than repeated here.

This file holds **cross-cutting** rules only. Package-local depth lives in nested
files that load automatically when you work in those directories:

| Directory | Covers |
| --- | --- |
| [`skills/`](skills/AGENTS.md) | skill layout, install namespacing, editing phase contracts |
| [`internal/pipeline/`](internal/pipeline/AGENTS.md) | snapshot schemas, phase order, `contract_text.go` |
| [`internal/config/`](internal/config/AGENTS.md) | strict decoding, dual schema versions, the classifier |

## What this project is

`gg` is a Go CLI that runs a coding task through a deterministic, QA-gated
pipeline of ten phases (acceptance criteria → grooming → planning → development
→ rebase → QA → test/document → build checker → PR → CI). Each phase runs an
external agent CLI (`claude` / `codex`) in an isolated git worktree with its own
model and effort level, and writes a declared artifact.

~54k lines of Go across 231 files, roughly half of it tests. No web service, no
database — durable state is JSON and YAML on disk.

## Verify loop

CI is the contract. Run all of it before claiming a change works; this is what
the three-OS matrix in [`.github/workflows/ci.yml`](.github/workflows/ci.yml)
executes on ubuntu, macos, and windows with Go 1.22.12.

```bash
gofmt -l .                      # must print NOTHING (the only formatter gate)
go test ./...                   # internal/e2e (~25s) is included; it builds the real binary
go test -race ./...
go vet ./...
go build -o /tmp/gg ./cmd/gg    # never build into the worktree
git diff --check                # trailing whitespace / conflict markers fail CI
```

There is **no golangci-lint, staticcheck, or markdown linter** in this repo —
don't add lint config or chase "lint" findings no configured tool reports.
`make` has exactly one target (`release`); it is not a test entrypoint.

Build outputs must stay out of the worktree: `/gg`, `/bin/`, `/dist/`, and
`.gg/**` are gitignored, which is what makes the final `git diff --check`
meaningful.

## Non-negotiable invariants

**1. `internal/pipeline/contract_text.go` says "Code generated … DO NOT EDIT" but
no generator exists.** Zero `go:generate` directives in the repo. It embeds each
phase skill as a byte-exact Go string, so changing phase prompt text means
editing `skills/canonical/<name>/<name>.md` **and** the Go literal by hand. Only
4 of 10 phases are drift-tested; the rest diverge with CI green. Details in the
[`skills/`](skills/AGENTS.md) and [`internal/pipeline/`](internal/pipeline/AGENTS.md)
files.

**2. Both persistence decoders are strict, so adding a field breaks compatibility
in both directions.** `internal/config/store.go:513` (`KnownFields(true)`) and
`internal/pipeline/snapshot.go:539` (`DisallowUnknownFields()`). Snapshots must
stay readable because `gg resume` restores in-flight runs.

**3. `PhaseMetadata.Optional` does not mean user-disableable.** It means "carries
an enabled flag." Grooming and Planning are `Optional: true` yet required. The
set users can actually turn off is `config.OptionalPhases()` — qa, build_checker,
pr, ci. Three overlapping sets exist; see the `internal/config` file.

**4. Persistence on failure and cleanup paths needs `context.WithoutCancel(ctx)`.**
43 non-test uses, concentrated in `internal/orchestrator`. Cancellation must
never abort a durable write or a rollback — forgetting this corrupts the run
cursor when a user runs `gg stop`.

**5. Dependency direction is layered, enforced by design rather than by a
linter.** `internal/execution` is a deliberate anti-cycle seam with **zero**
internal imports: both `pipeline` and `agent` depend on it, and `agent` depends
on `pipeline`. Do not collapse it. Specifically:

- `internal/state` must not import `config` or `pipeline` — it stores the
  snapshot as opaque `json.RawMessage` precisely to hold this direction.
- `internal/config` must not import `pipeline`. The duplicated `config.Phase`
  enum is intentional.
- `internal/tui` must not import `orchestrator` or `agent`.
- Composition lives only in `cmd/gg` and `internal/cli`.

## Changing the phase list

A phase addition, rename, or reorder touches six directories. Reordering also
requires a new snapshot schema version and a legacy-order path — the historical
Rebase/QA swap is what produced schema v1 vs v2, the `legacyOrder` flag, and
`snapshot_legacy_order_test.go`. Budget accordingly.

- **`internal/pipeline`** — `model.go` (`PhaseID` const, `canonicalArtifactNames`,
  `DefaultPipeline()`), `resolve.go` (`isCanonicalPhase`), `contract_text.go`,
  `snapshot.go` (`executionPhaseOrder`, one case per schema version).
- **`internal/config`** — `Phase` const, `removablePhases`/`fixedPhases`,
  `RequiredPhases()`/`OptionalPhases()`, `CompletePhaseOrder()`,
  `isSupportedPhase`.
- **`skills/canonical/<name>/<name>.md`** — file named after its directory,
  hyphenated (`test_document` → `test-document`). Missing skill files degrade a
  run **silently**.
- **Elsewhere** — `internal/cli/configure.go` (wizard descriptions),
  `internal/agent/prompt.go` (phase sets, per-phase text),
  `internal/state/skip.go` + `internal/orchestrator/skip.go`,
  `testdata/fake-agent/main.go` (three phase→artifact maps),
  `docs/pipeline.md`, `docs/configuration.md`.
- **Tests hardcoding the full list** — `internal/pipeline/model_test.go`,
  `snapshot_legacy_order_test.go`, and
  `cmd/gg/production_pipeline_regression_test.go` (an exact 38-entry event
  sequence and exact process counts).

## Conventions

**Tests are stdlib only.** No testify, no go-cmp, no mocking framework; `go.sum`
is deliberately thin. Assertions are `if got != want { t.Fatalf(...) }`; deep
comparison is `reflect.DeepEqual`. Fail-fast dominates (~1800 `t.Fatalf` vs ~120
`t.Errorf`). Table-driven with `t.Run` subtests for pure logic; orchestrator and
CLI tests are long, descriptively-named scenario functions. Test names are full
sentences stating the invariant.

Isolation is always `t.TempDir()` (336 uses) plus `t.Setenv`. Fakes are
hand-rolled per package — there is no shared mocks package, and duplicating a
`fakeExecutor` across packages is the established pattern. Time is injected via
`Clock` interfaces; never call `time.Now()` in logic you want tested.

`*_contract_test.go` pins cross-package invariants; `*_regression_test.go` pins a
specific fixed bug. Both names are load-bearing — match them.

**There are no golden files and no `-update` flag.** "Snapshot" here means
persisted-state serialization, not golden output.

**Platform splits use build tags** in `unix`/`!unix` or
`windows`/`!unix && !windows` pairs. The `_other` variants are not stubs — they
carry equivalent tests plus no-op shims so the same bodies compile everywhere. No
test-only tags are ever passed; every tagged file must pass under a bare
`go test ./...`.

**Errors** are sentinel vars compared with `errors.Is`, wrapped with a lowercase
verb phrase and `%q` around offending values: `fmt.Errorf("resolve configured
root: %w", err)`. A zero exit code from an agent does not mean success —
`gg_disposition` frontmatter overrides it via `agent.SemanticFailureError`.

**Do not add `log` or `slog` calls.** There is exactly one `log.Printf` in the
non-test tree. User-facing output goes through the injected `Output{Stdout,
Stderr}` struct; durable observability is the JSONL event log. Publish an event
or return an error. No `panic` or `log.Fatal` in non-test code.

**Concurrency** is bounded and owned: worker pools with explicit limits, every
goroutine joined via `WaitGroup` or a done channel, `ctx` as first parameter.
Cross-process safety is an OS advisory file lock per project plus a
run-reservation token in state — not in-process mutexes.

## Two things that look like repo config but aren't

- **`.gg/`** is gitignored working output from `gg` running on itself
  (dogfooding). Not source, not spec. Don't treat `.gg/plan.md` or `.gg/PROOF.md`
  as requirements.
- **`skills/`** is shipped payload, not this repo's agent config. See
  [`skills/AGENTS.md`](skills/AGENTS.md).

`.gitattributes` pins `eol=lf` globally. This is load-bearing: without it, Git
for Windows checks out CRLF and `gofmt -l` flags every file, breaking Windows CI.

## Where to read more

| Topic | Doc |
| --- | --- |
| Package boundaries, local checks, release commands | [`docs/development.md`](docs/development.md) |
| Phase lifecycle, QA loop, artifacts, recovery | [`docs/pipeline.md`](docs/pipeline.md) |
| Config layers, YAML shape, precedence | [`docs/configuration.md`](docs/configuration.md) |
| Release asset naming and provenance | [`release-contract.md`](release-contract.md) |
| Install paths and platform support | [`docs/install.md`](docs/install.md) |
