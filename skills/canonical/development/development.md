---
name: development
description: "Implement an approved plan in isolated generated subphases with verifiable artifacts."
phase_id: development
phase_display_name: "Development"
---

# Development

**Stable phase ID:** `development`
**Display name:** `Development`

## Isolated Context

Use only the assigned implementation brief, current worktree state, and explicitly declared artifacts (normally `plan.md` and its declared inputs). Do not use an earlier agent conversation as context. Transfer only changed worktree state and the artifacts below; every generated subphase receives a self-contained brief and cannot depend on another subphase conversation.

## Inputs

- Approved plan, acceptance criteria, declared repository/worktree scope, constraints, and artifact paths.
- Only explicitly named upstream artifacts.

## Outputs and Artifacts

- Planned source, configuration, test, and documentation changes in the assigned worktree.
- Executed verification results and unresolved findings.
- `development.md`: changed files, generated-subphase results, commands/results, and handoff risks at the assigned path.

## Generated Subphases

Generate subphases using the configured mode only:

- **Default:** exactly `implementation` / `Implementation`, `testing` / `Testing`, then `review` / `Review`, in that order; overrides are forbidden.
- **Override:** use only caller-provided definitions, in caller order. The list must be non-empty; each ID must be non-empty and unique; each display name must contain one to three words.
- **Disabled:** generate no subphases; overrides are forbidden.
- An unknown mode or invalid definition is a generation failure. Do not infer a sequence.

For each generated subphase, pass only its declared brief, inputs, allowed worktree/artifacts, and success criteria. Accept its result only after checking declared outputs; carry forward only explicit artifacts and worktree changes.

## Procedure

1. Confirm the plan and artifact paths match the assigned worktree.
2. Generate and execute the configured subphases with isolated briefs.
3. Keep changes cohesive and within scope; run planned verification.
4. Record actual changed files, commands, results, and risks in `development.md`.

## Success Criteria

- Subphase generation is valid and deterministic for the configured mode.
- All planned changes and required artifacts exist in the assigned worktree.
- Required verification passes, or failures are explicitly reported.
- `development.md` enables downstream phases without subphase conversations.

## Failure / Escalation

Stop on invalid subphase generation, failed required verification, missing plan input, merge conflict, or out-of-scope requirement. Preserve evidence, report the failing command/output and affected artifact, and escalate for a decision; do not skip a failed generated subphase or invent an alternate workflow.
