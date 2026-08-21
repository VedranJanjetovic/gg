---
name: rebase
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

- Assigned branch/worktree and explicit target base revision.
- Declared conflict-resolution policy, acceptance criteria, and relevant artifacts.

## Outputs and Artifacts

- A rebased worktree or preserved conflict state with reproducible details.
- `rebase-report.md`: base, result, conflicts, resolutions, and required rerun checks at the assigned path.

## Procedure

1. Verify the assigned worktree and target base before changing history.
2. Rebase only the assigned branch onto the explicit base.
3. Resolve conflicts using declared artifacts and repository evidence; keep changes minimal.
4. Run required post-rebase checks and record results.

## Success Criteria

- The branch is based on the requested revision without unresolved conflicts.
- No unrelated files or speculative refactors were introduced.
- The report identifies the resulting state and downstream verification.

## Failure / Escalation

Stop on ambiguous conflicts, unavailable base, failed mandatory checks, or history that cannot be safely rebased. Preserve evidence and escalate; do not force, discard, or rewrite work outside the assigned branch.
