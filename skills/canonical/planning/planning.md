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
- `plan.md`: execution plan with criterion-to-step traceability at the assigned path. Its frontmatter must include the machine-readable complexity, evidence, ordered phase names, and phase-boundary justifications described below.

## Procedure

1. Inspect only repository areas needed to map scope to existing patterns.
2. Classify the complete work before splitting it using the highest applicable signal:
   - **Trivial**: one cohesive localized outcome with no migration, public-contract change, or meaningful dependency ordering.
   - **Simple**: one localized component with routine backward-compatible behavior and tests.
   - **Moderate**: multiple components, meaningful ordering, new public behavior, or a contained data/config migration.
   - **Complex**: cross-service work, breaking contracts, substantial migration or rollback concerns, security-critical changes, or several independently deliverable outcomes.
3. Use advisory phase bands: Trivial exactly 1; Simple usually 1–2; Moderate usually 2–4; Complex usually 5–10. Only Trivial exactly one and the hard maximum of 10 phases are enforced. Do not create artificial splits to satisfy an advisory band.
4. Choose the smallest cohesive design that satisfies the complete criteria (KISS/DRY/YAGNI). Never truncate, merge, rename, or drop scope to fit the hard maximum; consolidate cohesive work before writing the artifact.
5. Record prerequisites and blockers; do not silently expand scope.

## Planning Artifact Contract

Before the first attempt, the ten-phase maximum is a hard cap. The four frontmatter fields below must be single-line, JSON-compatible YAML and must match the fixed body structure exactly:

```yaml
gg_plan_complexity: "Trivial|Simple|Moderate|Complex"
gg_plan_complexity_evidence: ["observable evidence", "observable evidence"]
gg_plan_phases: ["Phase 1: name", "Phase 2: name"]
gg_plan_phase_boundaries: [{"phase":"Phase 1: name","justification":"why this boundary exists"}, {"phase":"Phase 2: name","justification":"why this boundary exists"}]
```

The body must contain `## Complexity assessment`, `- Complexity category: **<category>**`, `- Selected phase count: **<count>**`, a `Supporting evidence:` numbered list, one heading exactly matching each phase name (for example `## Phase 1: <name>`), and a `Boundary justification: <justification>` line under each heading. Category, evidence, phase names/order/count, and justifications must match frontmatter. Every phase receives one boundary explanation, including a one-phase Trivial plan.

Benchmark the classification consistently: a README-only wording update is Trivial with exactly one phase; a localized backward-compatible bug fix is Simple and normally one to two phases; an ordered multi-component feature is Moderate and normally two to four phases; a cross-service or breaking migration is Complex and normally five to ten phases. These are examples, not an independent gg classifier.

If an existing plan has completed phases, update only pending work and preserve completed phase names and scope exactly. Do not independently reclassify the work in a way that discards accepted completed work.

## Success Criteria

- Every acceptance check maps to a planned change or verification step.
- The plan names expected file artifacts and executable verification.
- The artifact is self-contained for Development.
- The complexity category and observable evidence are recorded in both machine-readable frontmatter and the fixed body section.
- Trivial plans have exactly one phase and no plan has more than ten phases.

## Failure / Escalation

Stop and escalate missing architecture decisions, conflicting evidence, or a plan that cannot meet declared criteria. Do not implement a workaround or alter upstream artifacts.
