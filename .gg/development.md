---
gg_run_id: "gg-tool-generally-great-but-1787575707310868000/development/implementation/iteration-0"
gg_disposition: passed
---

# Development

## Phase 9: Isolated new-project configuration flow

Implemented the new-project configuration boundary for interactive `gg run`:

- New projects choose `Inherit folder configuration` or `Pick configuration for this project` before the project description prompt.
- The Pick flow receives complete folder defaults, lets the user configure full project and per-phase tuples, and does not write folder configuration.
- Sparse legacy folder configuration is routed through `gg configure` and materialized as a complete self-contained configuration before project creation.
- New projects persist a version 3 immutable execution snapshot containing project defaults, complete phase structure, enabled/required state, per-phase tuples, GitOps settings, development subphases, and QA retry limit.
- Removed per-run configuration flags now fail as unknown flags; retained operational run controls remain available.
- Required pipeline phases are locked on while their complete agent/model/effort tuples remain editable.

## Verification

- `git diff --check` — passed.
- `go test ./internal/config ./internal/pipeline ./internal/state ./internal/cli ./internal/tui ./cmd/gg` — passed.
- `go test ./...` — all packages passed except the existing macOS-only E2E path spelling assertion, where `/private/var/...` and `/var/...` refer to the same temporary path. This is the known local baseline recorded by the plan and is unrelated to Phase 9 behavior.

Interactive terminal selection was covered through injected chooser/picker tests; manual terminal interaction remains a reviewer-level verification item.
