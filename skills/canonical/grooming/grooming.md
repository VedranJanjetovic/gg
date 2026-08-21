---
name: grooming
description: "Refine a declared work item into an implementation-ready scope without changing it."
phase_id: grooming
phase_display_name: "Grooming"
---

# Grooming

**Stable phase ID:** `grooming`
**Display name:** `Grooming`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Assigned work item and declared acceptance artifact.
- Explicitly named repository evidence, constraints, and dependencies.

## Outputs and Artifacts

- Refined scope, dependency ordering, risks, ownership boundaries, and readiness decision.
- `grooming.md`: scoped work breakdown, dependencies, risks, and readiness at the assigned path.

## Procedure

1. Confirm each acceptance check is actionable and identify affected components.
2. Break work into cohesive, independently verifiable units; do not design speculative future work.
3. Identify dependencies, risks, and unresolved decisions.
4. Mark ready only when the scope and checks are sufficient for planning.

## Success Criteria

- Scope is bounded and non-goals remain preserved.
- Each proposed unit maps to declared acceptance criteria.
- The artifact states ready, needs decision, or blocked.

## Failure / Escalation

If criteria conflict, dependencies are unknown, or ownership is unavailable, mark blocked and escalate the exact question. Do not alter acceptance criteria or begin planning or implementation to hide the gap.
