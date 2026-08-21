---
name: qa
description: "Independently verify declared acceptance criteria against an isolated worktree."
phase_id: qa
phase_display_name: "QA"
---

# QA

**Stable phase ID:** `qa`
**Display name:** `QA`

## Isolated Context

Use only the assigned brief, current repository/worktree state, and artifacts explicitly declared as inputs. Do not use prior-agent conversation history, undeclared phase results, or hidden assumptions. Export only the artifacts below; a later phase may use them only when its own brief explicitly declares them.

## Inputs

- Declared acceptance checks, test requirements, and known constraints.
- Assigned worktree state and explicitly named development artifacts.

## Outputs and Artifacts

- Independent pass/fail findings tied to acceptance checks with reproducible evidence.
- `qa-report.md`: executed checks, results, evidence, residual risk, and disposition at the assigned path.
- `.gg/PROOF.md`: one validation entry per exercised flow, written inside the ignored `.gg/` artifact directory of the assigned worktree (never committed). Each entry must name the status (`pass`, `fail`, or `feedback`), test location, test name, flow/scenario, what it verifies, proof it passed including the exact command run, and manual run instructions.

## Procedure

1. Derive a focused verification matrix from declared acceptance checks.
2. Trivial-unit-test-only evidence is explicitly prohibited. Validate a component-specific scenario/flow at the most exposed boundary: for API code, use the local API with real requests; for external components, use in-memory or mock equivalents where practical; for frontend code, use browser/Puppeteer-style tests with mocked calls where practical; for code-only changes, exercise the most exposed callable/CLI flow rather than only private helpers.
3. Run permitted automated and manual checks.
4. Record each validation in the uncommitted `.gg/PROOF.md` using the deterministic format below, including all required evidence fields, the exact command run, and reproducible manual instructions.
5. Record failures with input, expected result, actual result, and affected area.
6. Do not classify unavailable evidence as a pass; classify incomplete evidence as fail or feedback.
7. Classify every reproducible defect or unmet-but-fixable acceptance check as `feedback` with an actionable `## Feedback` section — feedback routes the findings into the bounded QA-fix loop where Development addresses them. Reserve `fail` for findings no further development work can remedy (unmeetable criteria, falsified or impossible evidence); a `fail` ends the run instead of requesting fixes.

## Deterministic PROOF.md Format

`PROOF.md` lives at `.gg/PROOF.md` and begins with YAML frontmatter containing `gg_run_id: "<assigned run id>"` — the same run ID as the canonical artifact, with no disposition field — followed by Markdown with one or more `## Validation: <label>` sections. Every validation uses exactly these dash-prefixed fields (each must be non-blank):

- `Status: pass|fail|feedback`
- `Test location: <file or exposed entry point>`
- `Test name: <test, command, or flow name>`
- `Flow/scenario: <scenario exercised>`
- `What it verifies: <observable behavior>`
- `Proof it passed: <the exact command prefixed with "$ " plus its observed result, e.g. $ node qa-e2e.js -> PASS S1; any tool is accepted as long as the command starts with "$ ">`
- `Manual run instructions: <reproducible steps>`

An optional `## Feedback` section contains actionable feedback. Parsing is deterministic: malformed Markdown, missing/blank fields, malformed status, missing command evidence, or missing manual instructions classify as `fail`; any valid `feedback` status or non-blank feedback section classifies as `feedback`; only all-valid `pass` entries classify as `pass`. These dispositions map to the runner protocol as `passed`, `failed`, and `feedback`.

## Success Criteria

- Every declared acceptance check has pass, fail, or blocked disposition.
- Findings contain enough evidence for Development to reproduce.
- The report states checks not run and residual risk.

## Failure / Escalation

Escalate missing environments, ambiguous criteria, or non-reproducible failures as blocked with evidence. Do not waive failed checks or make unapproved code fixes.
