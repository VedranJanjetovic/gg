# How the pipeline runs

`gg` records project lifecycle state and an executable pipeline snapshot so a
run can continue after the attaching process exits. A run uses the configured
project folder as its artifact root and, for a Git repository, a deterministic
sibling worktree for changes.

## Phase order

The canonical order is:

1. Acceptance criteria
2. Grooming
3. Planning
4. Development
5. Rebase
6. QA (optional)
7. Test/Document
8. Build checker (optional)
9. PR (optional)
10. CI (optional)

Only `qa`, `build_checker`, `pr`, and `ci` can be disabled, by effective
configuration or run-only flags. The other six phases always run and accept
per-phase agent, model, and effort settings but no enabled toggle.

Development uses the default subphase sequence **Implementation → Testing →
Review**. With a plan, gg runs that sequence once for each pending plan phase,
marking the plan phase complete only after its review passes. Without a plan,
Development runs one worktree-wide sequence.

## QA feedback

QA writes `.gg/qa-report.md` and `.gg/PROOF.md`. A passing QA result advances to
the next phase. Structured QA feedback sends the project back through Development
and then QA, preserving the bounded attempt count. `--max-iterations` sets the
maximum number of QA attempts for the run and defaults to `3`; exhaustion leaves
the project failed with the attempt count recorded.

For example:

```bash
gg run dashboard --max-iterations 2 --disable-pr --disable-ci
```

The effective agent, model, effort, enabled phases, GitOps settings, and QA
limit are captured in the project's pipeline snapshot. Resuming restores that
snapshot rather than re-resolving current configuration.

## Artifacts and project state

Phase artifacts live in the worktree's ignored `.gg/` directory:

| Phase | Artifact |
| --- | --- |
| Acceptance criteria | `.gg/acceptance-criteria.md` |
| Grooming | `.gg/grooming.md` |
| Planning | `.gg/plan.md` |
| Development | `.gg/development.md` |
| QA | `.gg/qa-report.md`, `.gg/PROOF.md` |
| Rebase | `.gg/rebase-report.md` |
| Test/Document | `.gg/test-document.md` |
| Build checker | `.gg/build-checker.md` |
| PR | `.gg/pr.md` |
| CI | `.gg/ci-report.md` |

The `.gg/.gitignore` keeps these artifacts out of commits and pull requests.
Durable project state is stored below
`<configured-root>/.gg/projects/<slug>/state.json`. For a Git repository, the
working branch and worktree are placed in the sibling directory
`<repository-parent>/.gg-worktrees/<slug>`.

For a configured folder that is not a Git repository, execution happens in the
folder itself: there is no branch or worktree, commit enforcement and the
uncommitted-proof check are skipped, and agents are instructed not to run Git
commands. Rebase, PR, and CI become deterministic no-ops with a
`<phase>-skipped.md` explanation. Pruning such a project removes only gg state,
never the configured folder or its contents.

## Attached and global views

The bare `gg` command shows the global project view, where configured folders
and their projects refresh once per second. Use `↑`/`↓` or `j`/`k` to move,
`Enter` to attach, and `q` or Ctrl-C to quit. Typing a listed project number
also attaches, for the numbers `1`–`9`; projects listed beyond `9` must be
attached with `Enter` or `gg <project>`. Selecting a
project leaves the global view while the project view owns the terminal; when
that session ends, the global view returns.

In an attached project view:

- `s` requests a stop for a running pipeline;
- `r` resumes a stopped pipeline;
- `b`, `q`, or Ctrl-C detaches or quits the view;
- `c` opens the worktree in Visual Studio Code;
- `t` opens a terminal in the worktree;
- `i` enters an interactive QA/feedback session when available;
- `g` answers or resumes a pending grooming interview;
- `e` reconfigures a stopped or failed project before it is resumed; and
- `d` toggles the per-phase token and reported-cost breakdown.

`s`, `r`, and `e` apply only in the states named above; pressing one otherwise
reports why it is unavailable instead of acting.

The project view polls durable state and shows phase status, progress, failure
reasons, and agent-reported usage. When stdin or stdout is not a terminal, gg
does not initialize an interactive UI: it runs the requested startup
synchronously and prints one deterministic status snapshot.

## Detached execution and recovery

Starting or resuming a project launches the pipeline in a detached background
gg process. Its output is appended to
`.gg/projects/<slug>/logs/daemon.log`, so it continues after detaching, quitting
gg, or closing the terminal. `gg stop` writes a durable stop request, while
`gg resume` or `r` starts a fresh background process from the persisted
snapshot.

If the owner process dies, the next command that observes the project repairs a
stale `running` state to resumable `stopped`. Active and stopped projects remain
available; terminal projects can be removed through `gg prune` after its
fail-closed worktree checks succeed.

## Updates

`gg update` reads the latest release from the GitHub Releases API, expects a
semantic `gg-vX.Y.Z` tag, and compares it with the running binary's version.
Malformed release tags and development metadata are not treated as valid
versions. If a newer release exists, update checks project state before
installing: any project whose status is exactly `running` blocks the update and
the command recommends `gg stop-all`.

When installation is allowed, gg fetches `install.sh` (or `install.ps1` on
Windows) over HTTPS **pinned to the release tag being installed** —
`.../gg-vX.Y.Z/install.sh` — writes it to a temporary file, and runs it once with
an explicit `--version` and an explicit `--prefix`. The prefix is the directory
holding the running `gg`, resolved through symlinks, so an update lands where gg
is actually installed rather than in the installer's default prefix. Every step
that cannot be resolved is reported: an unlocatable or non-regular executable and
an unwritable destination directory each fail before anything is downloaded.

The installer is fetched rather than read from a persisted copy so that the
installer and the binary it installs always come from the same commit. Pinning to
the tag is what makes fetching acceptable: the URL is an immutable, reviewable
artifact in the same repository already trusted for the binary, not a moving
branch tip. The fetched script is passed to the interpreter as an argv element —
gg never pipes it to the interpreter's stdin and never builds a shell command
string. A body that is empty, or that begins with `<` (a 404 HTML page), is
refused rather than executed.

For deterministic testing, `GG_RELEASE_SOURCE` may point to a trusted HTTP
release fixture and `GG_INSTALLER_SOURCE` to a trusted HTTP base URL for the
installer. The `/gg-vX.Y.Z/<script>` path is always appended, so an override
cannot downgrade the fetch to an unpinned script.
