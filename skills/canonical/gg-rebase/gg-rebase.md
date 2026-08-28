---
name: gg-rebase
description: "Rebase the assigned change safely and report the resulting worktree state."
phase_id: rebase
phase_display_name: "Rebase"
---

# Rebase

**Stable phase ID:** `rebase`
**Display name:** `Rebase`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Assigned branch/worktree and configured parent branch. The current `origin/<parent-branch>` is the only Rebase target; an explicit `base_ref` must not override it.
- Declared conflict-resolution policy, acceptance criteria, and relevant artifacts.
- Evidence from earlier Rebase attempts when this is a retry.

## Outputs and Artifacts

- A rebased worktree or preserved conflict state with reproducible details.
- `rebase-report.md`: base, result, conflicts, resolutions, and required rerun checks at the assigned path.

## Procedure

1. Capture one clean branch, index, and worktree checkpoint before changing history.
2. For each of at most three attempts, restore that checkpoint, fetch the configured parent from `origin`, and rebase onto the freshly updated `origin/<parent-branch>` ref.
3. Use a fresh Rebase agent for each attempt that needs conflict resolution. Supply the complete acceptance scope and all prior conflict or local-check evidence; preserve accepted feature and relevant upstream changes.
4. Verify that no rebase is active or unresolved path remains, and run focused locally available regression checks in this Rebase phase.
5. After every failed attempt, restore the original checkpoint. If the phase remains failed and is later skipped, abort any active rebase and verify that same checkpoint before continuation.

## Success Criteria

- The branch is based on the latest fetched `origin/<parent-branch>` without unresolved conflicts.
- No unrelated files or speculative refactors were introduced.
- The report identifies the resulting state, every attempt, and downstream verification.

## Failure / Escalation

Stop after three unsuccessful attempts for fetch, conflict, agent, unresolved-path, or locally runnable regression-check failures. Preserve evidence and escalate; do not force, discard, or rewrite work outside the assigned branch.
