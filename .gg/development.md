---
gg_run_id: "gg-tool-generally-great-but-1787575707310868000/development/testing/iteration-0"
gg_disposition: passed
---

# Development testing

## Phase 9: Isolated new-project configuration flow

The test subphase added coverage for the Pick path, complete tuple/provenance
snapshot fidelity, folder-store isolation, interaction ordering, and
cancellation before project/worktree creation. An injected project chooser is
now honored independently of TTY detection, preserving the production TUI
gate while making alternate frontends and deterministic tests usable.

## Verification

- `gofmt -w internal/cli/project_config.go internal/cli/project_config_test.go` — passed.
- `git diff --check` — passed.
- `go test ./internal/config ./internal/pipeline ./internal/state ./internal/cli ./internal/tui ./cmd/gg` — passed.
- `go test -race ./internal/cli ./internal/pipeline ./internal/tui` — passed.
- `go test ./...` — one unchanged documented macOS baseline failure remains in `internal/e2e.TestRealCLIConfigureCreatesProjectAndDisposableWorktree`: `/private/var/...` versus `/var/...` temporary-path spelling. All other packages passed; no Phase 9 failure or regression was observed.

## Handoff risks

- Live terminal feel for the Inherit/Pick picker remains suitable for optional human smoke verification; deterministic chooser/picker coverage passes.
- The full-suite path spelling failure is pre-existing environment-specific evidence recorded in `.gg/plan.md`, not actionable Phase 9 work.
