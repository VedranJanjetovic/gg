---
name: build-checker
description: "Run declared build and static-quality checks and report reproducible outcomes."
phase_id: build_checker
phase_display_name: "Build checker"
---

# Build checker

**Stable phase ID:** `build_checker`
**Display name:** `Build checker`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Assigned worktree and explicit build, lint, format, static-analysis, or packaging commands.
- Declared platform/toolchain constraints and relevant artifacts.

## Outputs and Artifacts

- A pass/fail/blocked result for every declared quality gate.
- `build-checker.md`: commands, environment assumptions, results, evidence, and follow-up at the assigned path.

## Procedure

1. Confirm the required toolchain and declared commands are available.
2. Run each declared gate against the assigned worktree without changing scope.
3. Separate source failures from environment/tooling failures.
4. Record command, exit status, concise evidence, and disposition.

## Success Criteria

- Every declared gate has a pass, fail, or blocked result.
- Passing claims are backed by an executed command.
- The artifact enables downstream reproduction of failures.

## Failure / Escalation

Stop and escalate unavailable toolchains, nondeterministic failures, or failed required gates with command evidence. Do not downgrade a failure, omit a gate, or introduce unrelated changes.
