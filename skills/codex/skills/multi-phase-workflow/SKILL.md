---
name: "multi-phase-workflow"
description: "Plan, execute, and track multi-phase projects. Splits complex work into parallelizable phases, executes each via single-phase-workflow in isolated worktrees, and tracks progress across sessions."
metadata:
  short-description: "Multi-phase project orchestrator with parallel execution"
---

# Multi-phase Workflow

Orchestrator for complex changes that span multiple deliverables. Splits work into phases, executes them in parallel where possible via `$single-phase-workflow`, and tracks state across sessions.

## Response Style

Concise by default. Only expand when a decision has non-obvious tradeoffs or the user asks for detail.

## Storage

All project state lives in `~/.developer-ai/projects/<project-id>/`.

The `<project-id>` is generated as `<short-slug>-<6-char-random-hex>` (e.g., `auth-refactor-a1b2c3`).

```
~/.developer-ai/projects/<project-id>/
  problem.md        # Problem statement and context
  research.md       # Research findings and technical analysis
  plan.md           # Phases, dependencies, status tracking
```

## Subcommands

### `$multi-phase-workflow <problem description>`

Plan a new multi-phase project.

### `$multi-phase-workflow execute`

Pick a non-finished project and execute the next batch of ready phases.

### `$multi-phase-workflow progress`

Show all projects and their phase statuses. User manually marks phases as done.

### `$multi-phase-workflow close [project-id]`

Clean up: remove project files, worktrees, and branches.

---

## Subcommand: Plan (`$multi-phase-workflow <problem>`)

### Step 1: Understand the Problem

1. Read the problem description.
2. Ask clarifying questions — focus on: target outcome, constraints, repo structure, tech stack, backwards-compatibility, operational concerns.
3. Do not proceed until the problem and success criteria are clear.

### Step 2: Research

1. Explore the codebase to understand current architecture, patterns, and relevant code.
2. Identify technical constraints, existing patterns to follow, and risks.
3. Note findings but do NOT write any files yet.

### Step 3: Split Into Phases

Design phases following these rules:

1. Each phase is a self-contained deliverable that can be implemented and reviewed independently.
2. Phases should be right-sized — not trivial (single file change) and not massive (multi-day effort). Aim for 2-5 phases for most projects.
3. Maximize parallelism: phases that don't depend on each other should be executable simultaneously.
4. Avoid unnecessary sequential dependencies. Only add a dependency when a phase truly cannot start without another's output.
5. If possible, include a final integration phase that connects parallel work and runs end-to-end validation.
6. Each phase must specify:
   - Clear scope and deliverables
   - Success criteria
   - Dependencies (which phases must complete first, if any)
   - Which repo/service it affects

### Step 4: Present for Approval

Present the plan to the user as structured output:

```
## Problem
[One paragraph summary]

## Phases

Phase 1: <name>
  Scope: ...
  Deliverables: ...
  Dependencies: none

Phase 2: <name>
  Scope: ...
  Deliverables: ...
  Dependencies: none

Phase 3: <name>
  Scope: ...
  Deliverables: ...
  Dependencies: [1, 2]

## Execution Order
  Batch 1 (parallel): Phase 1, Phase 2
  Batch 2 (after batch 1): Phase 3
```

Wait for explicit user approval. Allow the user to modify phases, reorder, add, or remove before accepting.

### Step 5: Write Project Files (only after approval)

Only after the user approves, create the project directory and files:

1. Generate project ID: `<short-slug>-<6-char-hex>`
2. Create `~/.developer-ai/projects/<project-id>/`
3. Write `problem.md` — problem statement, context, constraints
4. Write `research.md` — codebase findings, technical analysis, risks
5. Write `plan.md` — using the format below

**plan.md format:**

```markdown
---
id: <project-id>
repo: <absolute-path-to-repo>
status: ready
created: <ISO-8601>
---

# <Project Title>

## Phase 1: <name>
- status: pending
- depends_on: []
- worktree:
- branch:
- scope: <description>
- deliverables: <list>
- success_criteria: <list>

## Phase 2: <name>
- status: pending
- depends_on: []
- worktree:
- branch:
- scope: <description>
- deliverables: <list>
- success_criteria: <list>

## Phase 3: <name>
- status: pending
- depends_on: [1, 2]
- worktree:
- branch:
- scope: <description>
- deliverables: <list>
- success_criteria: <list>
```

Print the project ID and path so the user can reference it later.

---

## Subcommand: Execute (`$multi-phase-workflow execute`)

### Step 1: List Non-Finished Projects

1. Scan `~/.developer-ai/projects/*/plan.md`
2. Parse each plan's status and phase statuses
3. Filter to projects where `status` is `ready` or `executing`
4. If multiple projects exist, present a numbered list and ask the user to pick
5. If only one exists, confirm and proceed

### Step 2: Determine Ready Phases

A phase is ready when:
- Its status is `pending`
- All phases in its `depends_on` list have status `done`

If no phases are ready, inform the user and stop.

### Step 3: Execute Ready Phases

For each ready phase, in parallel:

1. Create a git worktree for the phase:
   ```
   git worktree add <worktree-path> -b <branch-name> main
   ```
   - Worktree path: `<repo>/.worktrees/<project-id>/phase-<N>-<slug>`
   - Branch name: `<user-prefix>/phase-<N>-<slug>`

2. Update `plan.md`: set phase status to `in-progress`, record worktree path and branch name.

3. Spawn a `$single-phase-workflow` subagent for the phase with:
   - The phase scope, deliverables, and success criteria from `plan.md`
   - The worktree path as the working directory
   - Instruction to work only within the phase scope
   - Instruction to commit on the phase branch (do not push)
   - The overall project context from `problem.md`

4. Phases that CAN run in parallel MUST be spawned simultaneously (parallel subagents).

5. After each subagent returns, report:
   - Phase name
   - Worktree path (so user can inspect)
   - Branch name
   - Summary of changes
   - Any issues or follow-up items

Do NOT update phase status to `done`. The user does this manually via `progress`.

### Step 4: Report

Print a summary table:

```
Project: <id>

Phase  Name              Status        Worktree
1      <name>            in-progress   <path>
2      <name>            in-progress   <path>
3      <name>            pending       (blocked by 1, 2)
```

---

## Subcommand: Progress (`$multi-phase-workflow progress`)

1. Scan `~/.developer-ai/projects/*/plan.md`
2. For each project, display:

```
Project: <id> (<status>)
Repo: <path>
Created: <date>

Phase  Name              Status        Worktree                          Branch
1      <name>            done          -                                 <branch>
2      <name>            in-progress   <path>                            <branch>
3      <name>            pending       -                                 -
```

3. Ask the user which phases to mark as `done`.
4. Update `plan.md` for the selected phases: set `status: done`.
5. If all phases are `done`, set project status to `done` in frontmatter.

The user decides when a phase is done — the agent does not auto-complete phases.

---

## Subcommand: Execute (when all phases done)

When `execute` is called and all phases have status `done`:

1. Inform the user that all phases are complete.
2. Ask if they want to run a final validation step. If yes, ask for validation instructions (e.g., "run integration tests", "verify API compatibility").
3. Run the validation if provided.
4. Ask if the user wants to close the project.
5. If yes, proceed to close.

---

## Subcommand: Close (`$multi-phase-workflow close [project-id]`)

1. If no project-id given, list projects and ask which to close.
2. Confirm with the user before proceeding.
3. Remove all worktrees created for this project:
   ```
   git worktree remove <path> --force
   ```
4. Delete phase branches (only the ones created by this workflow).
5. Remove the project directory: `~/.developer-ai/projects/<project-id>/`
6. Confirm cleanup complete.

---

## Hard Rules

1. Never write project files before the user approves the plan.
2. Never auto-mark a phase as done. The user decides.
3. Always use `$single-phase-workflow` (or equivalent) for phase execution — do not implement inline.
4. Maximize parallelism: spawn all independent phases simultaneously.
5. Each phase gets its own worktree and branch — no shared state between phases.
6. Never delete worktrees or branches without user confirmation.
7. Project state is the `plan.md` file — keep it updated and parseable.
8. If a phase subagent fails or reports blockers, surface them clearly and let the user decide next steps.
