---
name: gg-single-phase-workflow
description: "Drive new changes through a consistent (single phase) workflow: understand the problem deeply, ask clarifying questions, create a user-prefixed branch, plan the change, spawn implementation, testing, and review subagents, iterate remediation until the diff is clean, and only then prepare a signed commit, push, and optional PR."
metadata:
  short-description: "Disciplined single-change delivery workflow"
---

# Single-phase Change Workflow

Use this skill when the user wants agent to implement a new change and expects a disciplined end-to-end delivery flow instead of ad-hoc edits.

## Response Style

Default to concise replies. Keep status updates, questions, reviews, and final summaries as short as possible while still preserving the information needed for safe execution. Only become more verbose when:

- the user explicitly asks for more detail
- a decision has non-obvious tradeoffs or risk
- a failure, blocker, or review finding needs explanation

Prefer direct answers, short checklists, and compact summaries over long narrative explanations.

## Objective

Produce a safe, reviewable, and release-ready change by following the same workflow every time:

1. Understand the problem deeply before editing code.
2. Start from a clean local `main` that is synchronized with the latest `origin/main`.
3. Create a dedicated git branch with the triggering user's prefix.
4. Plan the change and spawn an implementation subagent with the framed problem.
5. Inspect the implementation result and refresh generated and supporting artifacts for the affected services, including mocks and Swagger outputs.
6. Run a testing phase to ensure all relevant tests are written and critical flows are covered.
7. Run an implementation/gg-review remediation loop until no confirmed fixable issues remain. The reviewer must verify that tests exist and are valid.
8. Require valid GPG-signed commits for every commit and push/PR step, verified only with git signature commands. (if GPG signing is enabled)

## Required Workflow

### 1. Problem Framing First

Before creating a branch or editing files:

1. Understand the requested problem in depth.
2. Identify:
- the target outcome
- impacted services / shared modules and apps
- constraints and backwards-compatibility requirements
- operational or rollout concerns
- relevant tradeoffs
3. Ask targeted clarifying questions when key information is missing.
4. Ask the user about test cases and scenarios for integration and e2e tests. Present this as optional — the user may provide specific test cases, or say "skip it and decide on your own" to let the agent determine appropriate test coverage autonomously. If the user provides test cases, record them as testing requirements for the testing phase.
5. Do not start implementation until the problem and success criteria are clear enough to avoid wasteful rework.

Questions should focus on the problem, not on busywork. Prefer a short list of concrete questions that help uncover the real objective, edge cases, and tradeoffs.

### 2. Start From a Clean, Up-to-Date Main Worktree

Before creating a working branch:

1. Ensure the current worktree is clean.
2. Switch to the local `main` branch unless the user explicitly requests a different clean base branch.
3. Fetch the latest remote refs from `origin`.
4. Fast-forward or otherwise align local `main` to the latest `origin/main`.
5. Confirm the starting point is a clean `main` worktree with no local modifications and the newest remote commits.

Do not create a feature branch from a stale or dirty base. If the current worktree is dirty, stop and resolve that explicitly before proceeding. The default expectation is: start from a clean `main`, synced with the latest `origin/main`, and branch from there.

### 3. Create the Working Branch

Once the problem is understood well enough:

1. Determine the git user prefix from `git config user.name`.
2. Normalize it into a lowercase slug suitable for branch names.
3. Create a new branch using:
`<git-user-prefix>/<short-change-description>`
4. Keep the suffix brief, descriptive, and kebab-cased.

Example:
`vedran/add-new-developer-skill`

If the user explicitly requests a different branch naming convention, follow the user's request.

### 4. Plan and Spawn the Implementation Subagent

Before editing code, create a compact implementation brief in the main thread. The brief must include:
- framed problem and target outcome
- success criteria and explicit non-goals
- impacted services or shared modules
- compatibility, operational, rollout, and observability constraints
- current branch/base branch and any relevant changed-file context
- planned implementation steps
- expected generated artifacts, mocks, Swagger/docs updates, and verification commands

Then ALWAYS spawn exactly one implementation subagent using `$gg-developer`, which detects the project's language and toolchain from the repository itself.

The implementation subagent prompt must include the implementation brief. The subagent should:
- edit the current working branch/worktree as needed
- identify the owning service first
- follow the root `AGENTS.md` and the service-specific `AGENTS.md`
- keep handlers thin and preserve clean architecture boundaries
- preserve compatibility unless the user explicitly requests a breaking change
- keep observability and contextual error handling intact
- update Swagger/docs when API contracts change
- refresh generated artifacts when the affected service workflow requires them
- run appropriate verification commands when feasible
- summarize files changed, verification run, skipped verification, risks, and follow-up questions

The implementation subagent must not create branches, commit, push, open PRs, or ask the user for acceptance. Those remain main-thread responsibilities.

If implementation subagent tooling is unavailable in the current agent surface, stop and explain that independent implementation cannot be performed; do not silently replace it with same-thread implementation.

After the implementation subagent returns, the main thread must inspect `git status` and the diff, validate the result against the implementation brief, and address obvious gaps before moving to artifact synchronization and review.

### 5. Keep Affected Service Artifacts in Sync

After the implementation subagent returns and the main thread has inspected the result, verify whether any affected service also requires updates to generated or supporting artifacts.

Always check for, and update when needed:
- documentation
- Swagger/OpenAPI outputs
- mocks
- generated code or service-local artifacts

Do not assume code-only changes are complete until this synchronization check has been performed for every affected service.

Before considering the development work finished, explicitly run the relevant mock-generation and Swagger-generation steps for the affected services when those artifacts exist in the service workflow. Treat these as part of the standard finishing pass, not as optional cleanup.

### 6. Testing Phase

After implementation and artifact synchronization are complete, run a dedicated testing phase before review. The goal is to ensure all relevant tests are written and all critical flows are tested.

Spawn exactly one testing subagent using: $gg-test

The testing subagent prompt must include:
- the framed problem and target outcome from step 1
- the list of changed files and affected services
- user-provided test cases and scenarios (if any were provided in step 1)
- instructions on which test types to focus on (unit, integration, e2e)

The testing subagent must:
- review the implementation to identify all critical flows that need test coverage
- write missing unit tests for new or changed logic
- write integration tests for cross-component interactions
- write e2e tests for user-facing scenarios when applicable
- if the user provided specific test cases in step 1, ensure every one of those test cases is implemented
- run all written tests and ensure they pass (except regression e2e tests that require staging — those should be written but marked as skip/pending with a clear comment explaining they require the staging environment)
- not create branches, commit, push, open PRs, or ask the user for acceptance

If the user skipped providing test cases in step 1, the testing subagent decides appropriate coverage autonomously based on the change scope and critical paths.

After the testing subagent returns, the main thread must:
1. Inspect `git status` and verify the new/modified test files.
2. Confirm that user-provided test cases (if any) are all covered.
3. Confirm that tests pass (check the subagent's reported test results).
4. If there are gaps in test coverage or failing tests, spawn a new testing subagent with a remediation brief and repeat until tests are adequate and passing.

### 7. Run the Implementation/Review Remediation Loop Before Asking to Commit

Before proposing a commit or push, run a review loop. Each review pass must spawn exactly one independent reviewer subagent using: $gg-review

Ask the reviewer subagent to compare the current branch diff versus `origin/main` when available, otherwise `main`.

Keep the reviewer subagent prompt compact: include the user objective, base/head branches, changed files when useful, and the instruction to inspect the diff itself. Do not preload the main agent's implementation rationale unless it is necessary to understand the change.

The reviewer subagent must:
- inspect only; do not edit files, commit, push, create branches, or open PRs
- act as a senior reviewer with fresh context
- verify that tests exist for the changed code and that they are valid, meaningful, and cover critical flows — flag missing or inadequate test coverage as a confirmed fixable issue
- if the user provided specific test cases in step 1, verify that each one has a corresponding test implementation
- return findings, reviewer checklist items, author questions, and suggested verification commands
- call out when no notable concerns are found for a required checklist area

If subagent tooling is unavailable in the current agent surface, stop and explain that an independent review cannot be performed; do not silently replace it with a same-thread self-review.

After each reviewer subagent returns, reconcile its findings in the main thread:
- surface confirmed risks first, regardless of severity
- identify any findings that are questions, tradeoffs, duplicates, or false positives
- add any main-agent concerns the subagent missed
- keep the reconciled review compact and actionable

If the reconciled review contains confirmed fixable issues, missing tests, skipped verification that should be run, generated-artifact gaps, compatibility concerns, or operational/observability gaps, do not ask for commit acceptance yet. Instead:
1. Create a compact remediation brief with the original framed problem, current diff status, confirmed reviewer findings, intended fixes, constraints, and verification/artifact expectations.
2. Spawn exactly one implementation subagent with the remediation brief (use the same implementation skill as step 4).
3. Allow the implementation subagent to edit files and run verification, but not create branches, commit, push, open PRs, or ask for user acceptance.
4. After it returns, inspect `git status` and the diff in the main thread.
5. Re-run the affected artifact synchronization checks from Step 5.
6. If the remediation involved test-related findings, re-run the testing phase from Step 6.
7. Spawn a fresh reviewer subagent and repeat this loop.

Continue the loop until the reconciled review has no confirmed fixable issues and all required verification/artifact gaps are resolved. If a finding is a legitimate tradeoff rather than a fix, ask the user to accept or reject that tradeoff before commit. If the same confirmed issue persists after two remediation attempts, or a subagent reports a blocker that cannot be resolved with the available context, stop and ask the user for a decision instead of looping indefinitely.

Present the final clean or explicitly accepted review state clearly and wait for user acceptance before committing.

### 8. Commit and Push Only After User Acceptance

If the user accepts the change:

1. Draft a concise commit message summarizing the change (why, not what).
2. Create the commit as a GPG-signed commit. (if signing is enabled)
3. Verify the signature with (if signing is enabled):
`git log -1 --show-signature`
4. Never inspect or depend on the GPG trust DB; use git signature output only.
5. If the signature is missing or invalid, treat the commit as incomplete and fix it before continuing.
6. Do not push changes at all.

## Hard Rules

1. Write the skill behavior and generated workflow guidance in English.
2. Default to minimal, high-signal replies unless the user asks for more verbosity.
3. Do not skip the problem-understanding phase.
4. Do not start coding before clarifying critical ambiguity.
5. Do not skip branch creation for a new implementation task unless the user explicitly says to stay on the current branch.
6. Always start from a clean local `main` that is updated to the latest `origin/main`, unless the user explicitly requests another clean base branch.
7. Always use the git user prefix for the branch unless the user overrides it.
8. Always create a compact implementation brief before code edits.
9. Always spawn exactly one implementation subagent with the framed problem and implementation plan.
10. The implementation subagent may edit files and run verification, but must not create branches, commit, push, open PRs, or ask for user acceptance.
11. Always inspect the implementation subagent's result in the main thread before moving to artifact synchronization or review.
12. Always verify that affected docs, Swagger, mocks, and generated artifacts are updated.
13. Always include mock generation and Swagger generation in the final development pass when applicable to the affected services.
14. Always ask about test cases and scenarios during problem framing. Accept "skip" as a valid answer.
15. Always run the testing phase after implementation and artifact sync, before review.
16. Each testing pass must spawn exactly one independent testing subagent.
17. The testing subagent must write and run tests but must not create branches, commit, push, open PRs, or ask for user acceptance.
18. If user-provided test cases exist, every one must be implemented and verified.
19. Always run the implementation/gg-review remediation loop before proposing commit/push.
20. Each review pass must spawn exactly one independent reviewer subagent.
21. The reviewer subagent must inspect only and must not edit files, create branches, commit, push, or open PRs.
22. The reviewer must verify that tests exist, are valid, and cover critical flows.
23. For every confirmed fixable reviewer issue, spawn a new implementation subagent with a remediation brief, inspect its result, re-check artifacts, re-run testing if needed, and run review again.
24. Do not ask for commit acceptance while confirmed fixable issues or required verification/artifact gaps remain.
25. Always reconcile each reviewer subagent's findings in the main thread before deciding whether to remediate, ask for a tradeoff decision, or request acceptance.
26. Only commit after the user accepts the implemented change.
27. All commits must be GPG-signed and signature-verified with git commands only.
28. Never push unsigned or invalidly signed commits.
29. If opening a PR, use the PR description skill and share the link.

## Completion Checklist

Before declaring the task complete, confirm all of the following:

1. The problem statement, constraints, and tradeoffs were understood.
2. Clarifying questions were asked when needed.
3. Test cases and scenarios were discussed with the user (or user chose to skip).
4. The work started from a clean local `main` synced with the latest `origin/main`, or an explicit exception was granted by the user.
5. A correctly named branch was created or an explicit exception was granted by the user.
6. A compact implementation brief was created from the framed problem and plan.
7. An implementation subagent completed the planned work.
8. The main thread inspected the implementation subagent's result.
9. All affected generated/supporting artifacts were updated.
10. Relevant mocks and Swagger outputs were regenerated for affected services when applicable.
11. The testing phase ran: all relevant tests were written, critical flows covered, and tests pass.
12. User-provided test cases (if any) were all implemented.
13. Regression e2e tests requiring staging were written but appropriately marked as skip/pending.
14. Verification was run, or any skipped verification was explained.
15. The implementation/gg-review remediation loop ran until no confirmed fixable issues remained.
16. The reviewer verified that tests exist and are valid.
17. Any required remediation subagents completed, and their results were inspected in the main thread.
18. The final reviewer subagent reviewed the branch diff.
19. The final reviewer findings were reconciled in the main thread.
20. Any remaining tradeoffs were explicitly accepted by the user.
21. User acceptance was obtained before commit.
22. The commit message came from the commit-message skill.
23. The commit was GPG-signed and the signature was verified with git commands only.
24. The branch was pushed if requested or required.
25. If a PR was requested, the PR description skill was used and the PR link was shared.
