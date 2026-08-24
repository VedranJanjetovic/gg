# gg CLI

`gg` is the Go command-line entrypoint for coordinating developer-ai agent workflows. It lives in its own standalone repository at `VedranJanjetovic/gg`.

## Install the CLI

The release installers are user-local and do not require Go, administrator
privileges, or a system package manager. They download the matching release
archive, verify its expected single executable entry, and replace `gg`
atomically through a temporary file. Re-running an installer is safe for a
normal destination; symlink/reparse-point destination components are refused.

### Linux and macOS

Copy/paste this command to install the latest release. It downloads the
installer from the repository's raw URL and executes it with Bash:

```bash
curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh | bash
```

To select a version, append `--version VERSION` (without the `gg-v` tag
prefix), for example:

```bash
curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh | bash -s -- --version 1.2.3
```

For an inspected, non-pipe installation, download the script, review it, then
execute the local copy:

```bash
tmp="${TMPDIR:-/tmp}/gg-install.sh"
curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh -o "$tmp"
less "$tmp"
bash "$tmp" --version 1.2.3
rm -f -- "$tmp"
```

The default destination is `$XDG_BIN_HOME` when set, otherwise
`$HOME/.local/bin`. Override it with `--prefix /absolute/path` or
`GG_INSTALL_PREFIX`. The installer does not edit shell startup files. If the
destination is not already on `PATH`, add it for the current shell and then to
your shell profile, for example:

```bash
export PATH="${XDG_BIN_HOME:-$HOME/.local/bin}:$PATH"
gg --help
```

### Windows PowerShell

Download the PowerShell installer from the raw repository URL, inspect it, and
run it. `-Version` is optional and defaults to `latest`:

```powershell
$url = 'https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.ps1'
Invoke-WebRequest -Uri $url -OutFile .\install.ps1
Get-Content .\install.ps1
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\install.ps1 -Version 1.2.3
Remove-Item .\install.ps1
```

The process-scoped execution-policy change lasts only for the current PowerShell
session. If your organization enforces a policy that prevents scripts, follow
that policy or ask an administrator; do not weaken a machine-wide policy.
The default destination is `$HOME\.local\bin`. Override it with `-Prefix` or
`GG_INSTALL_PREFIX`. The installer warns when that directory is not on the
current user's `PATH`; add it in your user Environment Variables settings (or
for the current session with
`$env:Path = "$HOME\.local\bin;$env:Path"`), then start a new shell if needed.

### Platforms and release contract

The canonical contract is maintained in
[`release-contract.md`](release-contract.md). The summary below is kept here
because it is the installer-facing reference.

Supported targets are Linux amd64/arm64, macOS (Darwin) amd64/arm64, and
Windows amd64/arm64. Release tags use the canonical `gg-vX.Y.Z` form, such as
`gg-v1.2.3`. `latest` follows the repository's latest release; an explicit
version downloads that tag. The release builder produces these exact assets:

| Target | Asset | Archive root |
| --- | --- | --- |
| Linux amd64/arm64 | `gg-linux-amd64.tar.gz`, `gg-linux-arm64.tar.gz` | `gg` |
| macOS amd64/arm64 | `gg-darwin-amd64.tar.gz`, `gg-darwin-arm64.tar.gz` | `gg` |
| Windows amd64/arm64 | `gg-windows-amd64.zip`, `gg-windows-arm64.zip` | `gg.exe` |

The scripts select assets from GitHub Releases under
`VedranJanjetovic/gg`. Release assets must retain these names and
archive roots. The shell installer uses `uname` and the PowerShell installer
uses the Windows runtime architecture; unsupported OSes or architectures fail
before downloading. Release uploads must be built with
`build-release.sh --artifacts VERSION OUTPUT_DIR`; the builder's
ordinary single-binary output is not an installer asset. Before publication,
follow the exact-tag build and `gg version` provenance checklist in
[`release-contract.md`](release-contract.md#build-provenance); an artifact built
from a different `HEAD` must not be renamed or published for an older tag.

### Security and troubleshooting

Piping a remote script directly to a shell is convenient but gives the script
execution access before you inspect it. Prefer the inspected, non-pipe flow
when provenance or script review matters, and verify that the raw URL, release
tag, and downloaded archive are the expected ones.

- **`curl`/download failed:** check network access, TLS interception, and that
  the requested tag has the asset for your OS and architecture.
- **Unsupported OS or architecture:** use one of the targets listed above;
  the installers intentionally reject everything else.
- **`gg: command not found`:** add the reported user-local directory to `PATH`
  and start a new shell, or run the installed path directly.
- **Prefix or symlink/reparse-point refusal:** choose a real absolute directory
  you own; the safety check will not follow links or overwrite a linked target.
- **Windows script blocked:** use the process-scoped policy guidance above,
  subject to your organization's policy.
- **Archive validation failed:** do not use the archive; confirm the release
  asset name and that it contains only the documented root executable.

## Package boundaries

- `cmd/gg` — executable entrypoint; wires `os.Args` and process exit codes into the CLI package.
- `internal/cli` — command parsing and user-facing command dispatch only.
- `internal/config` — configuration workflow and future config persistence.
- `internal/state` — durable project lifecycle state used by `list`, `status`, `run`, `resume`, and `stop`.
- `internal/pipeline` — workflow lifecycle operations for `run`, `stop`, and `prune`.
- `internal/agent` — domain types for gg-managed agents.
- `internal/tui` — attachable Bubble Tea progress UI and non-interactive status rendering.
- `internal/git` — future repository integration.
- `internal/proof` — future verification evidence storage.
- `internal/update` — gg update operations.

### Non-git folders

A configured folder that is not a git repository works too: projects execute directly in that folder (no worktree, no branch), commit enforcement and the proof's uncommitted check are skipped, agents are told not to run git commands, and the Rebase, PR, and CI phases complete as deterministic no-ops with a `<phase>-skipped.md` artifact explaining why. `gg prune` on such a project removes only gg's state — never the folder or its contents.

Command parsing remains intentionally thin. The production executable resolves the configured folder, persists lifecycle metadata below `<configured-root>/.gg/projects/<slug>/state.json`, and uses a sibling worktree at `<repository-parent>/.gg-worktrees/<slug>`. Run, stop, resume, list, status, and prune all operate on that durable project store.

## Agent skills

`install.sh` also installs the gg agent skills — the ten canonical phase contracts, the methodology skills (`review`, `debug`, `architect`, `refactor`, `security`, `test`, `plan`, `go-developer`, …), and the coding-patterns reference — into the user-level agent configuration under a collision-free `gg-` prefix. The skill sources live at `skills/` (canonical plus Claude/Codex-adapted variants); a source checkout installs them directly, while a curl-piped install fetches the repository snapshot matching the requested version:

- Claude Code: `~/.claude/skills/gg-<name>/SKILL.md` (model-invoked) and `~/.claude/commands/gg-<name>.md` (invoke as `/gg-<name>`)
- Codex: `~/.codex/skills/gg-<name>/SKILL.md`
- Coding patterns reference: `~/.gg/gg-coding-patterns.md`

gg's agent prompts never paste skill content: each phase prompt tells the agent to **load the `gg-<phase>` skill by name** from its user-level skills directory (the ten phase contracts remain embedded in the binary only for validation/sync purposes), and code-touching phases (Development, QA, Test/Document, Build checker) add one sentence pointing at the absolute path of `~/.gg/gg-coding-patterns.md`. Directories are created even when the agent CLIs are not installed yet, so everything works the moment they arrive.

Each file's frontmatter `name` is rewritten to `gg-<name>` to match. Installation is idempotent (re-runs overwrite only the gg-owned `gg-*` namespace) and never creates or modifies shared user files such as `CLAUDE.md`, `AGENTS.md`, or `instructions.md`.

## Commands

```bash
gg --help
gg                         # create a project, start its pipeline, and attach
gg <project>               # attach to an existing project
gg configure
gg --configure
gg list
gg status
gg run <project>
gg resume <project>
gg stop <project>
gg stop-all
gg prune
gg remove <project>
gg update
gg usage
gg version
gg --version
```

### Update release contract

`gg update` reads the latest-release JSON from the GitHub Releases API endpoint
`https://api.github.com/repos/VedranJanjetovic/gg/releases/latest` (manual downloads use the corresponding GitHub Releases page).
The response must be HTTP 200 JSON containing a non-empty `tag_name` with a
semantic version using the canonical `gg-vX.Y.Z` convention, such as `gg-v1.2.3`. The
`gg-v` prefix is normalized before semantic-version comparison. Release lookup uses a bounded,
context-cancellable standard-library HTTP client. A newer release is installed only after the persisted project store confirms
that no project is exactly `running`; otherwise `gg update` refuses to invoke the
installer and prints `gg stop-all` as the next action. Project-state read errors
are fatal rather than being treated as an empty store. Development builds (for
example `dev`) and malformed release tags are never silently treated as versions.
When a newer release is clear, the production binary invokes the trusted
binary-only installer exactly once with explicit version arguments. Set
`GG_INSTALLER_PATH` to an absolute path to the inspected `install.sh`
(or `install.ps1` on Windows); the runner never discovers or executes a script
from the current directory and never uses shell interpolation. For deterministic
release testing, `GG_RELEASE_SOURCE` may point at a trusted HTTP release fixture.

Version output is deterministic and includes the release version, commit, and UTC build date. Local builds use `dev`, `unknown`, and `unknown` fallbacks. Release builds can override them with linker flags:

```bash
go build -ldflags "-X github.com/VedranJanjetovic/gg/internal/version.Version=v1.2.3 -X github.com/VedranJanjetovic/gg/internal/version.Commit=$(git rev-parse HEAD) -X github.com/VedranJanjetovic/gg/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/gg
```

The attached project view renders the persisted configured phases and Development subphases; the Development row is annotated with plan progress — `Development (phase 2/4 — Gameplay)` names the plan phase currently being worked. Development runs a **per-plan-phase loop**: every pending plan phase gets its own fresh Implementation → Testing → Review agent sequence (each subphase a new agent with a clean context, scoped to that one plan phase), and the phase is marked complete only after its review passes — so tests exist and pass for phase N before phase N+1 starts, and a resumed run continues at the first unfinished plan phase. Projects without a plan, and QA-feedback fix passes, run a single worktree-wide Implementation → Testing → Review pass. Completed phases have green checks, the active phase has a loader, and failed or stopped phases retain their status; a failed phase also shows the persisted failure reason (for example the agent's own error message) directly under its row. Press `s` to stop a running pipeline, `r` to continue a stopped pipeline, and `q` to detach at any time. When GitOps is not explicitly configured, the parent branch is detected from the repository (local `origin/HEAD`, then the remote's advertised HEAD, then the checked-out branch) instead of assuming `main`. Pipelines execute in a **detached background gg process** (its output is appended to `.gg/projects/<slug>/logs/daemon.log`), so runs survive detaching, quitting gg, and closing the terminal — start as many projects as you like and let them run in parallel. `gg stop` reaches a background run through the durable stop request in project state, and `r`/`gg resume` starts a fresh background process from the persisted snapshot. If a background process dies (crash, `kill -9`, reboot), the next gg command that sees the project detects the dead owner and repairs the state to a resumable `stopped`. Below the progress bar the view shows the total agent-reported token usage and, when reported, the USD cost (`Tokens: 1,234,567  ·  $12.34`); press `d` to toggle a per-phase breakdown with both. Counts and costs come from the agents themselves (codex's `tokens used` output; claude's JSON result usage and `total_cost_usd`) and are persisted per phase execution in project state — gg never estimates: agents that report no cost (codex) contribute tokens only. When stdin or stdout is not a terminal, `gg` runs the requested startup synchronously and prints one deterministic status snapshot instead of initializing the interactive UI.

The explicit `gg run`, `gg resume`, and `gg stop` commands remain available for scripting and direct lifecycle control.

## Developer workflow

From this directory:

```bash
# Format all Go packages
go fmt ./...

# Run unit tests
go test ./...

# Build the gg binary into the module root
go build -o gg ./cmd/gg

# Verify top-level command help
./gg --help
```

The module uses Charm's Bubble Tea, Bubbles, and Lip Gloss packages for terminal presentation; dependency checksums are committed in `go.sum`.

## Relationship to phase contracts and agent assets

The workflow contracts consumed by agents are canonically defined in the root
repository's `skills/<phase>/<phase>.md` files. `gg` orchestrates those phases;
it does not replace the contract source with a CLI-specific copy.

The root installers project the shared contracts and guidance into each tool's
native assets:

- Claude Code commands live under `agents/claude/commands/` and are installed
  by `install.sh`.
- Codex agents/skills live under `agents/codex/` and are installed by
  `install.sh` when the Codex CLI is available.
- Cursor rules live under `agents/cursor/rules/` and are installed by
  `install.sh`.
- Hermes uses the same root skills, rendered into a selected profile by
  `install_hermes.sh`, along with its project `SOUL.md` and `PROJECT.md`.

These are integration assets, not alternative ways to install the `gg`
executable. Re-running the installers is designed to be safe and idempotent:
unchanged content is skipped, unrelated files are preserved, and an existing
Hermes service is left installed while its profile content is refreshed.


## End-to-end CLI workflow

### First use and project creation

1. Install `gg` using the release installer above.
2. In the repository folder, run `gg configure` (or `gg --configure`). On first use it asks for the global default agent (`claude` or `codex`), model, and effort (`low`, `medium`, or `high`), then writes both global and project configuration.
3. Run `gg run`. It asks for a project goal and one or more acceptance criteria. The project slug is derived from the goal. `gg` validates the repository, creates or reuses the deterministic sibling worktree and `gg/<slug>` branch, persists state, and starts the pipeline.

`gg` is an orchestrator, not an AI harness: it spawns the configured Claude or Codex processes with the phase prompt and records their output and lifecycle state. It does not implement its own model, agent runtime, or prompt-execution engine.

### Lifecycle and attachment

```text
gg list              # active/non-terminal projects
gg list --all       # include finished and terminated projects
gg status            # table: name, status, phase, branch, worktree, updated
gg status <project>  # one project's details
gg run <project>    # run a new project or accept run options
gg resume <project> # resume stopped/failed durable state
gg stop <project>   # request a durable stop
gg stop-all         # request a stop for every exactly-running project
gg prune            # interactively remove terminal project state/worktrees
gg prune --yes      # non-interactive prune confirmation
gg <project>        # attach to an existing project
```

A bare `gg` creates a project and attaches to it. `gg <project>` attaches to an existing project. The attached view exposes the persisted phase/subphase state; `s` stops a running pipeline, `r` resumes a stopped pipeline, and `q` exits when no lifecycle action owns the foreground. In a non-TTY, startup runs synchronously and prints a deterministic status snapshot instead of opening the interactive UI. `run`, `resume`, and `stop` remain available for scripts.

`list` hides terminal projects unless `--all` is supplied. `prune` only considers `finished` and `terminated` projects, verifies the recorded worktree is the expected clean attached worktree, removes that worktree, and deletes state only after cleanup succeeds. It intentionally retains the `gg/<slug>` branch. Active and stopped projects are preserved.

### Pipeline phases and QA feedback

The executable pipeline uses the canonical contracts from the repository's root `skills/` directory rather than CLI-specific copies. The default order is acceptance criteria, grooming, planning, development (implementation/testing/review subphases), rebase, QA, test/document, build checker, PR, and CI; PR and CI can be disabled by effective configuration. The persisted phase history and attached view show the actual enabled phases and development subphases.

QA must produce a structured `PROOF.md` validation artifact. A pass advances to the next phase. QA feedback routes the workflow back through Development, Rebase, and then QA again, up to `--max-iterations` total QA attempts (default `3`). Exhaustion leaves the project failed with the attempt count persisted. For example:

```bash
gg run dashboard --max-iterations 2 --disable-pr --disable-ci
```

Run-only overrides are not persisted. Use `--agent`, `--model`, `--effort`, `--phase-agent phase=agent`, `--phase-model phase=model`, `--phase-effort phase=effort`, `--enable-phase phase`, `--disable-phase phase`, `--parent-branch branch`, and `--base-ref ref` as needed. `linting` is accepted as an alias for `build_checker`.

### Per-project configuration

Configuration is layered in this order: built-in defaults, global defaults, project overrides, then one-run overrides. Project configuration is stored at `<configured-root>/.gg/config.yaml`; global configuration is stored at `$XDG_CONFIG_HOME/gg/config.yaml` (or the platform's user config location used by the Go config store). The project file supports:

```yaml
version: 1
defaults:
  agent: claude
  model: claude-model
  effort: medium
phase_overrides:
  qa:
    enabled: true
    agent: codex
    model: gpt-5
    effort: high
gitops:
  parent_branch: main
  base_ref: HEAD
  enable_pr: true
  enable_ci: true
```

Phases `grooming`, `planning`, `qa`, `build_checker`, `pr`, and `ci` can be enabled/disabled (grooming per run only); `acceptance_criteria`, `development`, `rebase`, and `test_document` always run. Every phase — fixed ones included — accepts per-phase `agent`, `model`, and `effort` overrides. Grooming is enabled by default and its toggle is not offered by the interactive `gg configure` wizard (disable it per run with `--disable-phase grooming` or per project in `config.yaml`), but it still accepts per-phase agent/model/effort overrides.

To change models for a single project without touching any config file, pass run overrides when creating it: `gg run --phase-model qa=claude-opus-4-8 --phase-agent pr=codex --phase-effort development=high --enable-phase ci --disable-phase grooming "description"`. Run overrides apply to that invocation only and are baked into the created project's pipeline; `--agent`, `--model`, and `--effort` override the defaults for every phase of that run. Per-folder defaults for every future project live in the wizard (`gg configure`) or directly in `phase_overrides` in `<configured-root>/.gg/config.yaml`.

On a terminal, `gg configure` runs a single full-screen wizard: agent → model → effort → pipeline phases. The model screen lists the known models for the selected agent and ends with an `Enter model name manually…` option for any model not in the list (for example a newly released one). The phase screen shows the whole canonical pipeline in execution order with each phase's effective agent/model/effort. `Space` toggles the removable phases on/off; `Enter` on any phase row (fixed ones included) opens the same agent → model → effort pickers scoped to that phase and stages a per-phase override in the folder's `config.yaml`; `Enter` on the final `Save configuration` row writes everything. Only the fields that differ from the global defaults are pinned, so re-picking the default values makes the phase inherit again. Toggled phases are persisted as project `enabled` overrides, and untouched phases keep their existing configuration. Every screen is prefilled with the current values, so pressing Enter through the wizard keeps the configuration unchanged. Nothing is written until the wizard completes, and staged values are validated before saving. Without a terminal (piped input), configure falls back to equivalent line-oriented prompts, where per-phase agent/model/effort overrides can also be set.

Creating a project (`gg run` with no selector) opens a full-screen description editor in the same TUI style as the configure wizard: typed text is echoed live, `Enter` starts a new line, and `Enter` on an empty line (double Enter) opens a confirmation screen showing the inferred project name and the full description before anything is created (`Enter` creates the project, `Esc` returns to editing). Without a terminal (piped input), the flow falls back to the line prompt `Describe the project (finish with an empty line):`. The description becomes the project goal and seeds the initial acceptance criterion; the pipeline's Acceptance criteria phase derives the formal criteria from it.

All phase artifacts (`acceptance-criteria.md`, `plan.md`, `development.md`, `qa-report.md`, `PROOF.md`, `pr.md`, …) are written to the worktree's `.gg/` directory, which carries a self-ignoring `.gitignore` — they never appear in `git status`, never get committed, and never land in the pull request. Legacy artifacts found at the worktree root are migrated into `.gg/` automatically on the next phase run.

After the project is created, gg runs a **grooming interview** before the pipeline starts. By default this is a **live agent session**: the configured agent CLI (claude/codex) opens in the project folder, interviews you conversationally — requirement ambiguities, edge cases, and decision-level architecture choices with named options and trade-offs — and answers your counter-questions; the session appends every decision to `.gg/interview-answers.md` **as soon as you answer it**, and marks the file complete when you say you're done; on exit gg ingests the file (folding each Q/A into the interview state and acceptance criteria) and removes it. Leaving mid-conversation loses nothing: answered questions are already persisted, and pressing `g` re-opens the conversation with those answers carried as "already answered — do not re-ask". Only a session that reached the completion marker finishes the interview. When the agent CLI cannot launch, gg falls back to the question-list flow: the agent scans the description for open questions (shown as a full-screen spinner while it thinks), then each question is asked one at a time in a `gg grooming` screen (Enter on an empty line submits, an empty answer skips that question, Esc pauses the interview). Answered questions are folded into the project's acceptance criteria as `Clarification — Q: … A: …`, so every later phase prompt carries them, and the agent is re-run with the answers until it has no new questions (at most 3 rounds). The interview is persisted in project state: pausing keeps the answered questions and parks the rest — the pipeline does not start, the project screen shows "Waiting for grooming answers — press g", and pressing `g` (or re-attaching from `gg`) re-opens the remaining questions exactly where you left off. Deliberately skipping every question (empty submits, not Esc) opts out. If the question check itself fails (a broken agent install or model), the interview stays pending and the reason is shown — press `g` to retry after fixing the cause; the pipeline never starts past an unanswered interview. Piped/non-TTY runs skip the interview entirely.

### Troubleshooting

- **`current folder is not configured`:** run `gg configure` from the repository folder. Lifecycle commands intentionally fail closed without project configuration.
- **`gg: command not found`:** add `$XDG_BIN_HOME` or `$HOME/.local/bin` (the installer's reported prefix) to `PATH`; the installer does not edit shell startup files.
- **Agent executable not found or a phase fails to start:** install the selected `claude` or `codex` CLI and verify it is on `PATH`. `gg` delegates execution to those processes.
- **A worktree or branch collision is reported:** inspect the existing `gg/<slug>` branch and sibling `.gg-worktrees` entry; choose a distinct project goal/slug or resolve the collision deliberately.
- **Stop/update refuses to proceed:** `gg update` refuses while any project is exactly `running`; run `gg stop-all`, wait for durable states to change, then retry. Store read errors are fatal and are not treated as an empty project list.
- **Prune refuses a project:** do not delete files manually. Check for uncommitted changes, a detached/checked-out/mismatched worktree, or incorrect persisted metadata; prune is deliberately fail-closed.
- **Update/install fails:** use the inspected non-pipe installer flow, confirm the release asset exists for the current OS/architecture, and check that the destination is a real directory rather than a symlink/reparse path.

### Release and update behavior

`gg update` queries the configured latest-release JSON endpoint, accepts canonical semantic tags such as `gg-v1.2.3`, and compares them with the binary's embedded version. Development metadata (`dev`) and malformed tags do not silently count as versions. A newer release is installed only after the durable project store confirms that no project is exactly running. Set `GG_INSTALLER_PATH` to an absolute, inspected copy of `install.sh` or `install.ps1`; the production runner invokes that binary-only installer once with explicit version arguments and does not execute shell command strings. `GG_RELEASE_SOURCE` may point to a trusted HTTP fixture for deterministic testing.

The non-Homebrew release contract is GitHub Releases under `VedranJanjetovic/gg`, with `gg-vX.Y.Z` tags and the archive names documented in [Platforms and release contract](#platforms-and-release-contract). No Homebrew formula, tap, or package-manager installation is required or implied.

## Maintainer, CI, and build commands

Run Go commands from the repository root:

```bash
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/gg ./cmd/gg
```

The repository's real-CLI end-to-end (E2E) suite builds and invokes the real
executable with deterministic fake Claude/Codex binaries; it does not contact
an AI provider. The suite is currently supported on **Linux and macOS
(Darwin) only**. Its fixtures intentionally depend on POSIX shell commands,
process groups, and Linux-only network-denial support. Windows has no native
real-CLI fixtures yet: the shell/POSIX E2E files are excluded at build time,
and the Windows package test reports that limitation as a skip. This keeps
`go test ./...` honest on Windows without claiming unsupported E2E coverage.

On Linux or macOS, run the focused real-CLI suite with:

```bash
go test ./internal/e2e -run 'TestRealCLI'
```

Run the full supported-platform checks from the repository root with:

```bash
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/gg ./cmd/gg
```

On native Windows, run the portable package and production checks with:

```powershell
go test ./...
go vet ./...
go build ./...
```

The Windows `go test ./...` result includes the explicit unsupported-suite
skip above; it is not a native run of the Linux/macOS real-CLI E2Es. To check
Windows compilation from another host without running a Windows test binary,
use `go test -c` for the E2E package (and remove the external output after the
check):

```bash
GOOS=windows GOARCH=amd64 go test -c -o /tmp/gg-e2e-windows.test ./internal/e2e
rm /tmp/gg-e2e-windows.test
```

For a release build, supply version metadata with linker flags as shown above, package the platform assets with the repository release build workflow, and verify archive names/root entries against the installer contract. Before committing documentation or code, run `git diff --check`, inspect `git status --short`, and scan changed text for conflict markers. CI should run the full test, race, vet, format, and build gates; release checks additionally verify reproducible metadata and installer/archive compatibility.
