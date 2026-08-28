# AGENTS.md — `skills/`

Scope note: this directory is **shipped payload**, not this repository's own
agent configuration. Nothing here configures the agent reading it. These files
are installed onto end-user machines by `install.sh` / `install.ps1`.

## Layout

```
skills/
├── canonical/gg-<name>/gg-<name>.md   20 skills — the source of truth
├── claude/commands/gg-<name>.md       10 Claude-adapted overrides
├── codex/skills/gg-<name>/SKILL.md    10 Codex-adapted overrides
└── core/gg-coding-patterns.md         the coding-patterns reference
```

**The canonical file is named after its directory** — `canonical/gg-qa/gg-qa.md`,
not `SKILL.md`. Only the Codex variants use `SKILL.md`.

## Rules that bite

**Sources are source-native: the `gg-` prefix lives in the repository, not in
the installer.** Every directory name, file name, and frontmatter `name:` here
already carries exactly one `gg-` prefix, and `internal/agent/prompt.go:146`
emits `Load and follow the agent skill named "gg-<name>"` against that same
identity. `internal/skills/identity_test.go` pins this: a missing prefix, a
doubled `gg-gg-` prefix, or a frontmatter name that disagrees with its path
fails the build. Rename a file and you must rename its frontmatter with it.

**Installers copy bytes; they do not transform them.** `install_skill_file`
(`install.sh:113-130`) stages a plain `cp` and atomically moves it into place.
There is no frontmatter rewriting, no prefixing, and no wrapping, so an
installed file is byte-identical to the source it came from.

**A directory whose `.md` filename doesn't match the directory name is silently
skipped.** `install.sh:137-138` is `[[ -f "$skill_dir$identity.md" ]] || continue`
— no warning, no error, the skill just never installs. Same in `install.ps1`.

**Adapted wins over canonical, per target, with silent fallback**
(`install.sh:139-142`). A missing `claude/commands/gg-<name>.md` means the
canonical file is installed to the Claude destination. Each skill installs to
three places (`install.sh:143-145`):

- `~/.claude/commands/gg-<name>.md`
- `~/.claude/skills/gg-<name>/SKILL.md`
- `~/.codex/skills/gg-<name>/SKILL.md`

**`core/gg-coding-patterns.md` installs to four places** (`install.sh:148-155`):
the three destinations above plus `~/.gg/gg-coding-patterns.md`, because
code-touching phase prompts reference it by absolute path rather than by skill
name. It carries its own frontmatter in source like every other skill.

**Installation never touches anything outside the `gg-*` namespace.** Shared
user files (`CLAUDE.md`, `AGENTS.md`, `instructions.md`) and legacy unprefixed
skills from older installations are left alone — the installer has no delete
path. `internal/e2e/install_sh_unix_test.go` asserts both.

## Two classes of skill

**10 phase contracts** — `phase_id` + `phase_display_name` frontmatter, no
adapted variants:

```yaml
---
name: gg-qa
description: "Independently verify declared acceptance criteria..."
phase_id: qa
phase_display_name: "QA"
---
```

`phase_id` keeps the underscored Go form; the identity hyphenates it:
`test_document` → `gg-test-document` (mapping at
`internal/agent/prompt.go:323`).

**10 methodology skills** — the ones with Claude/Codex variants (gg-architect,
gg-debug, gg-go-developer, gg-multi-phase-workflow, gg-plan, gg-refactor,
gg-review, gg-security, gg-single-phase-workflow, gg-test). Every canonical
form carries frontmatter; `internal/skills/identity_test.go` requires it, so
a new skill without a `name:` fails rather than installing under a wrong name.

Frontmatter shape differs per target: Claude commands carry `argument-hint` and
an `allowed-tools` list; Codex `SKILL.md` carries a nested
`metadata.short-description`.

## Editing a phase contract

Editing any of the 10 phase files under `canonical/` obligates regenerating the
embedded Go copy — see the root [`AGENTS.md`](../AGENTS.md) invariant on
`internal/pipeline/contract_text.go`:

```bash
go generate ./internal/pipeline
```

All ten are drift-tested byte-for-byte
(`internal/pipeline/contract_text_test.go:10`), so forgetting to regenerate
fails CI rather than silently shipping stale contract text.

Adding a new phase skill is part of a wider lockstep change — start from the
root file, not here.
