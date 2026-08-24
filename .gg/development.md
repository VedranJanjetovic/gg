---
gg_run_id: "gg-tool-generally-great-but-1787575707310868000/development/review/iteration-0"
gg_disposition: passed
---

# Development review

## Phase 9: Isolated new-project configuration flow

Reviewed and corrected Phase 9 integration defects:

- Sparse folder configurations now always pass through an explicit configure/save migration gate, including stores using the compatibility `ConfigureStore` interface.
- Older complete prefix-shaped folder structures can be inherited without adding newer phases; Pick still exposes the current canonical phase list.
- Project snapshots preserve the inherited structure, reject missing required phases, and restore absent optional phases as disabled for current resolution.
- Updated fixtures distinguish sparse configuration tests from complete new-project creation tests.
- Added regression coverage for sparse migration and older-prefix snapshot fidelity.

Changed files:

- `internal/cli/project_config.go`
- `internal/pipeline/snapshot.go`
- `internal/cli/project_config_test.go`
- `internal/pipeline/snapshot_phase9_test.go`
- `internal/cli/configure_test.go`
- `internal/cli/app_test.go`
- `internal/cli/attach_test.go`

## Verification

- `gofmt` on all changed Go files — passed.
- `git diff --check` — passed.
- `go test ./internal/config ./internal/pipeline ./internal/state ./internal/cli ./internal/tui ./cmd/gg` — passed.
- `go test -race ./internal/cli ./internal/pipeline ./internal/tui` — passed.
- `go test ./...` — all packages passed except the documented macOS baseline failure in `internal/e2e.TestRealCLIConfigureCreatesProjectAndDisposableWorktree`, where `/private/var/...` and `/var/...` are equivalent temporary-path spellings.

## Handoff risks

- Manual terminal smoke testing of the Inherit/Pick picker remains optional; deterministic chooser/picker coverage passes.
- The full-suite path-spelling failure is environment-specific baseline evidence recorded in `.gg/plan.md`, not actionable Phase 9 work.
