# AGENTS.md — `skills/`

Scope note: this directory is **shipped payload**, not this repository's own
agent configuration. Nothing here configures the agent reading it. These files
are installed onto end-user machines by `install.sh` / `install.ps1`.

## Layout

```
skills/
├── canonical/<name>/<name>.md      20 skills — the source of truth
├── claude/commands/<name>.md       10 Claude-adapted overrides
├── codex/skills/<name>/SKILL.md    10 Codex-adapted overrides
└── core/coding-patterns.md         special-cased, no frontmatter in source
```

**The canonical file is named after its directory** — `canonical/qa/qa.md`, not
`SKILL.md`. Only the Codex variants use `SKILL.md`.

## Rules that bite

**A directory whose `.md` filename doesn't match the directory name is silently
skipped.** `install.sh:135` is `[[ -f "$skill_dir$name.md" ]] || continue` — no
warning, no error, the skill just never installs. Same in `install.ps1`.

**Adapted wins over canonical, per target, with silent fallback**
(`install.sh:136-139`). A missing `claude/commands/<name>.md` means the canonical
file is installed to the Claude destination unchanged. Each skill installs to
three places (`install.sh:140-142`):

- `~/.claude/commands/gg-<name>.md`
- `~/.claude/skills/gg-<name>/SKILL.md`
- `~/.codex/skills/gg-<name>/SKILL.md`

**The `gg-` prefix is a runtime contract, not cosmetic.** `install_skill_file`
rewrites the first frontmatter `name:` line to `name: gg-<name>`
(`install.sh:116-125`), and `internal/agent/prompt.go:146` emits `Load and follow
the agent skill named "gg-<name>"`. The rename only fires if **line 1 is exactly
`---`** — a file without frontmatter is copied verbatim and keeps its original
name, so only its path gets namespaced.

**`core/coding-patterns.md` is special-cased** (`install.sh:146-161`): the
installer synthesizes frontmatter at install time and additionally copies the
**raw** file to `~/.gg/gg-coding-patterns.md`, because prompts reference it by
absolute path rather than by skill name.

## Two classes of skill

**10 phase contracts** — `phase_id` + `phase_display_name` frontmatter, no
adapted variants:

```yaml
---
name: qa
description: "Independently verify declared acceptance criteria..."
phase_id: qa
phase_display_name: "QA"
---
```

Directory names hyphenate the phase ID: `test_document` → `test-document/`
(mapping at `internal/agent/prompt.go:320`).

**10 methodology skills** — the ones with Claude/Codex variants (architect,
debug, go-developer, multi-phase-workflow, plan, refactor, review, security,
single-phase-workflow, test). Frontmatter is **inconsistent here**: `architect`,
`debug`, `plan`, `refactor`, `review`, `security`, and `test` have none in their
canonical form. That is only tolerable because the adapted variants always win
for these names — don't "fix" it by adding frontmatter without checking both
variants exist.

Frontmatter shape differs per target: Claude commands carry `argument-hint` and
an `allowed-tools` list; Codex `SKILL.md` carries a nested
`metadata.short-description`.

## Editing a phase contract

Editing any of the 10 phase files under `canonical/` obligates a second, manual
edit — see the root [`AGENTS.md`](../AGENTS.md) invariant on
`internal/pipeline/contract_text.go`. The embedded Go copy must stay
**byte-identical**, and only `development`, `qa`, `test-document`, and
`build-checker` have a drift test. The other six will diverge with CI green.

Adding a new phase skill is part of a wider lockstep change — start from the
root file, not here.
