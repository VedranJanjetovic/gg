---
name: pr
description: "Prepare a reviewable pull-request handoff from declared, verified change artifacts."
phase_id: pr
phase_display_name: "PR"
---

# PR

**Stable phase ID:** `pr`
**Display name:** `PR`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Assigned branch, target base, change summary, and explicit verification artifacts.
- Declared repository pull-request conventions and reviewer requirements.

## Outputs and Artifacts

- Reviewer-oriented PR title/description, verification evidence, risks, and URL when creation is authorized.
- `pr.md`: metadata, linked evidence, reviewer notes, and creation status at the assigned path.

## Procedure

1. Verify branch/base and enumerate only declared changes and evidence.
2. Compose a reviewer-oriented description with test results and known limitations.
3. Create the PR only when explicitly authorized; otherwise produce a draft.
4. Record the URL/identifier or exact reason it was not created.

## Success Criteria

- Reviewers can understand scope, verification, and risks without phase conversations.
- Every claimed check names explicit evidence.
- The artifact accurately states created, drafted, or blocked.

## Failure / Escalation

Escalate missing verification evidence, an unpushable branch, unavailable credentials, or unclear target base. Do not push, create a PR, or represent approval without authorization.
