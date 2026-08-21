---
name: ci
description: "Evaluate declared CI results and return an evidence-based release disposition."
phase_id: ci
phase_display_name: "CI"
---

# CI

**Stable phase ID:** `ci`
**Display name:** `CI`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Explicit CI run/build identifiers, required checks, and target branch/commit.
- Declared PR and verification artifacts plus permitted retry policy.

## Outputs and Artifacts

- Pass/fail/blocked disposition for each required CI check.
- `ci-report.md`: checks, run evidence, disposition, retries, and escalation state at the assigned path.

## Procedure

1. Confirm the CI run targets the assigned commit and required checks.
2. Collect each result and distinguish infrastructure from product failures.
3. Retry only when the brief permits it; preserve every retry reference.
4. Produce an evidence-based disposition without changing code or CI configuration.

## Success Criteria

- Every required CI check has a pass, fail, or blocked disposition.
- The report identifies the tested commit and run evidence.
- The artifact supports a later decision without conversation history.

## Failure / Escalation

Escalate failed required checks, stale or mismatched runs, unavailable evidence, or exhausted retries. Do not rerun indefinitely, waive checks, alter pipeline configuration, or claim readiness without evidence.
