---
name: planning
description: "Produce a minimal, verifiable implementation plan from declared scope and artifacts."
phase_id: planning
phase_display_name: "Planning"
---

# Planning

**Stable phase ID:** `planning`
**Display name:** `Planning`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Approved scope and acceptance criteria.
- Declared grooming artifact, repository evidence, and technical constraints.

## Outputs and Artifacts

- Minimal cohesive changes, affected files/components, test strategy, verification commands, and risks.
- `plan.md`: execution plan with criterion-to-step traceability at the assigned path.

## Procedure

1. Inspect only repository areas needed to map scope to existing patterns.
2. Choose the smallest design that satisfies declared criteria (KISS/DRY/YAGNI).
3. Define implementation, test, documentation, and verification work.
4. Record prerequisites and blockers; do not silently expand scope.

## Success Criteria

- Every acceptance check maps to a planned change or verification step.
- The plan names expected file artifacts and executable verification.
- The artifact is self-contained for Development.

## Failure / Escalation

Stop and escalate missing architecture decisions, conflicting evidence, or a plan that cannot meet declared criteria. Do not implement a workaround or alter upstream artifacts.
