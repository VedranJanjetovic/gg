# Troubleshooting gg

Installation and `PATH` problems are covered in
[the installation guide](install.md). This page covers failures during ordinary
use.

## `current folder is not configured`

Run `gg configure` from the repository folder. Every lifecycle command fails
closed without project configuration rather than guessing a folder.

## `gg run` opens the configuration wizard instead of starting

The folder holds a sparse legacy configuration file. gg never silently rewrites
one, so it asks for a complete replacement first. Save the wizard and the run
continues. See [the configuration guide](configuration.md).

## A phase fails to start, or the agent executable is not found

Install the selected `claude` or `codex` CLI and confirm it is on `PATH`. gg
delegates all phase execution to those processes and does not bundle them.

## A worktree or branch collision is reported

Inspect the existing `gg/<slug>` branch and the sibling
`.gg-worktrees/<slug>` directory. Choose a distinct project goal so the derived
slug differs, or resolve the collision deliberately; gg will not reuse a
worktree it does not recognize as its own.

## `gg prune` refuses a project

Prune is deliberately fail-closed and never deletes files it cannot account
for. Check for uncommitted changes in the worktree, a detached or mismatched
worktree, or persisted metadata that no longer matches the deterministic
branch and path. Resolve the cause rather than deleting the worktree by hand.
Prune also considers only `finished` and `terminated` projects, and it retains
the `gg/<slug>` branch after removing the worktree.

## A project is stuck in `running` after a crash

The owning process died. The next command that observes the project repairs the
stale `running` state to a resumable `stopped`, so run `gg list` or `gg status`
and then `gg resume <project>`.

## `gg update` refuses to proceed

Update is blocked while any project's status is exactly `running`. Run
`gg stop-all`, wait for the durable states to change, then retry. A failure to
read project state is fatal and is never treated as an empty project list.

## The pipeline waits on a grooming interview

The project view reports that it is waiting for answers and does not advance
past an unanswered interview. Attach and press `g` to continue. Submitting empty
answers for every current question opts out and lets the pipeline proceed.
