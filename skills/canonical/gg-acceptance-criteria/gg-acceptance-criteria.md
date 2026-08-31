---
name: gg-acceptance-criteria
description: "Define an executable acceptance contract before a pipeline change proceeds."
phase_id: acceptance_criteria
phase_display_name: "Acceptance criteria"
---

# Acceptance criteria

**Stable phase ID:** `acceptance_criteria`
**Display name:** `Acceptance criteria`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Assigned problem statement, constraints, and explicitly supplied stakeholder decisions.
- Named repository evidence only when the brief identifies it as relevant.

## Outputs and Artifacts

- Observable scope, non-goals, behavior, edge cases, and pass/fail acceptance checks.
- `acceptance-criteria.md`: approved criteria, assumptions, dependencies, and open questions at the orchestrator-assigned path.
- The executable verification contract in that artifact's frontmatter, but only when the brief explicitly requires it; otherwise Planning declares it.

## Procedure

1. Normalize the requested outcome into observable behaviors and constraints.
2. Separate required behavior from non-goals and assumptions.
3. Add pass/fail checks, including relevant error and boundary behavior.
4. Record unresolved ambiguity as a blocker rather than selecting a product decision.

## Success Criteria

- Every requested outcome has an observable acceptance check.
- Scope, non-goals, dependencies, and assumptions are explicit.
- The artifact is self-contained for a later phase.

## Failure / Escalation

Stop and report the missing decision, conflicting requirement, or unavailable evidence. Do not begin design or implementation, and do not manufacture criteria. Escalate the exact question and affected acceptance check.
