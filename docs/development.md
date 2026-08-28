# Development reference

This document describes the current repository layout and the checks used to
maintain gg. It intentionally documents present responsibilities only.

## Package boundaries

- `cmd/gg` is the executable entrypoint and wires process arguments and exit
  codes into the CLI.
- `internal/cli` owns command parsing, dispatch, configuration, attachment,
  prompts, and lifecycle-facing workflows.
- `internal/config` owns the YAML schema, validation, configuration stores,
  resolution, and agent/model catalog.
- `internal/state` owns durable project lifecycle state, locks, liveness, and
  project observations.
- `internal/pipeline` owns canonical phases, phase resolution, Development
  subphases, artifacts, and execution snapshots.
- `internal/execution` owns the narrow process-execution contract shared by
  the pipeline coordinator and agent implementations.
- `internal/orchestrator` coordinates phase execution, background lifecycle,
  notifications, and resume/stop behavior.
- `internal/resume` holds the preparation shared by explicit and production
  resume paths; `internal/verification` runs named checks and separates
  pre-existing parent failures from new regressions.
- `internal/agent` adapts the supported agent processes, builds prompts, and
  records agent outcomes and usage.
- `internal/tui` presents global project selection, attached progress, prompts,
  and non-interactive status output.
- `internal/git` handles repository, branch, and worktree operations.
- `internal/proof` parses and stores validation evidence; `internal/eventlog`
  stores project event history.
- `internal/ci`, `internal/pr`, and `internal/gh` integrate CI monitoring,
  pull-request operations, and GitHub access.
- `internal/update` implements release lookup and guarded self-update;
  `internal/version` provides build metadata.
- `internal/robustio` retries the filesystem operations that only Windows
  reports as transient sharing violations.

Keep application behavior in these packages and keep composition in the
entrypoint and CLI wiring. Documentation-only changes do not require modifying
these boundaries.

## Agent-skill installation

The repository's `skills/` tree contains canonical phase contracts, methodology
skills, tool-adapted Claude/Codex variants, and `core/gg-coding-patterns.md`.
`install.sh` and `install.ps1` select an adapted source when one exists and
otherwise use the canonical file.

Sources are already named for the collision-free `gg-*` namespace — both the
directory or file name and the frontmatter `name:` — so the installers copy
each selected file byte-for-byte to its destination:

- Claude command files: `~/.claude/commands/gg-<name>.md`;
- Claude model-invoked skills: `~/.claude/skills/gg-<name>/SKILL.md`;
- Codex skills: `~/.codex/skills/gg-<name>/SKILL.md`; and
- the coding-patterns reference: `~/.gg/gg-coding-patterns.md`.

When an installer runs from a checkout, it reads that checkout's `skills/`
tree. A remote or pipe-based run downloads the repository snapshot matching
`main` for `latest` or the requested `gg-vX.Y.Z` tag, then installs from the
snapshot. The installers create the destination directories even when the
agent CLIs are not installed.

Re-running an installer overwrites only the `gg-*` namespace and the gg-owned
coding-patterns reference. It does not create, modify, or remove shared user
files such as `CLAUDE.md`, `AGENTS.md`, or `instructions.md`, nor any legacy
unprefixed skill left over from an earlier installation. Because installation
transforms nothing, an installed file always matches its source exactly, which
is what makes the operation deterministic and idempotent.

## Local checks

Run commands from the repository directory:

```bash
# Format all Go packages
go fmt ./...

# Unit tests
go test ./...

# Race detector
go test -race ./...

# Static analysis
go vet ./...

# Build a local binary
go build -o /tmp/gg ./cmd/gg
```

The focused real-CLI E2E suite uses deterministic fake Claude/Codex binaries
and does not contact an AI provider. It is supported on Linux and macOS:

```bash
go test ./internal/e2e -run 'TestRealCLI'
```

The E2E fixtures depend on POSIX commands, process groups, and platform-specific
network-denial support. Windows has no native real-CLI fixture set; its package
test reports that limitation as a skip. Portable Windows checks are:

```powershell
go test ./...
go vet ./...
go build ./...
```

To check Windows E2E compilation from another host without running the binary:

```bash
GOOS=windows GOARCH=amd64 go test -c -o /tmp/gg-e2e-windows.test ./internal/e2e
rm /tmp/gg-e2e-windows.test
```

## Release-maintainer commands

The ordinary release script builds one binary. Use artifact mode for the six
installer archives:

```bash
./build-release.sh VERSION /tmp/gg-release
./build-release.sh --artifacts VERSION ./bin
```

Artifact mode builds Linux, macOS, and Windows amd64/arm64 outputs named as
specified in [`release-contract.md`](../release-contract.md). Release metadata
contains the exact Git commit and its UTC commit timestamp. The `release`
Make target determines the next patch version, builds all assets, and prints
the tag, upload, and verification commands:

```bash
make release
```

Before submitting a change, inspect the diff and whitespace:

```bash
git diff --check
git status --short
```
