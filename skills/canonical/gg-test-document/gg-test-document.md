---
name: gg-test-document
description: "Complete declared tests and user-facing or maintainer documentation for an isolated change."
phase_id: test_document
phase_display_name: "Test/Document"
---

# Test/Document

**Stable phase ID:** `test_document`
**Display name:** `Test/Document`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Declared behavior, changed worktree state, test strategy, and documentation audience.
- Explicit QA, rebase, or development artifacts when supplied.

## Pre-PR Verification Boundary

Test/Document owns final test and documentation gaps. Follow repository conventions, including adding established end-to-end coverage even when execution is deferred to CI. Perform ordinary local setup, including local dependencies, services, and containers, and run every applicable check that is locally runnable. Do not connect to AWS or any other remote environment, and do not use remote credentials or endpoints. A check may be deferred only when repository evidence shows that it requires a remote credential or external endpoint; an ordinary local setup or test failure is a failure and must not be reclassified as deferred. Record each valid deferral with its location, check name, flow and expected behavior, exact remote-only reason, repository evidence, and CI/manual run instructions without claiming that it passed. A valid deferral does not block the phase, even when PR or CI is disabled.

## Outputs and Artifacts

- Tests covering declared changed behavior and required documentation updates.
- `test-document.md`: test/document files, commands/results, coverage rationale, and gaps at the assigned path.

## Procedure

1. Map declared behavior to focused tests and required documentation.
2. Add only artifacts needed to explain and verify the assigned change.
3. Run applicable test and document validation commands.
4. Record skipped checks and why they could not run.

## Success Criteria

- Tests exercise declared behavior and relevant failure paths.
- Documentation matches implemented, verified behavior without future speculation.
- The artifact lists every produced file and actual validation result.

## Failure / Escalation

Escalate missing test environments, unclear documentation ownership, or unverifiable behavior. Do not claim coverage for unrun checks or modify unrelated implementation.
