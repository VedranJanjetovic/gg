# Configure gg

`gg` keeps agent selection, phase settings, and GitOps choices in versioned YAML
configuration. A project run captures the effective values in its execution
snapshot, so changing ambient configuration later does not silently change an
already-created run.

## Configuration layers

Resolution proceeds from least specific to most specific:

1. Built-in phase defaults: all canonical phases are enabled, with GitOps
   defaults of parent branch `main`, base ref `HEAD`, PR enabled, and CI enabled.
2. Global defaults: the required default agent, model, and effort, plus optional
   GitOps defaults.
3. Project overrides: settings for the configured folder.
4. One-run overrides: flags supplied to that invocation only.

Global configuration is stored by the user configuration store, normally at
`$XDG_CONFIG_HOME/gg/config.yaml` on Unix. Project configuration is stored at
`<configured-root>/.gg/config.yaml`. Run-only values are not written to either
file, but they are included in the new project's persisted pipeline snapshot.

## YAML shape

This is a representative project configuration:

```yaml
version: 2
defaults:
  agent: claude
  model: claude-sonnet-5
  effort: medium
  provenance: catalog
phases:
  - phase: acceptance_criteria
    enabled: true
    required: true
    settings: {agent: claude, model: claude-sonnet-5, effort: high, provenance: catalog}
  # ...one complete entry per canonical phase, in execution order...
gitops:
  parent_branch: main
  base_ref: HEAD
  enable_pr: true
  enable_ci: true
```

A folder file is a complete, self-contained template for future projects.
`defaults` sets the default `agent`, `model`, and `effort`, and `phases` carries
one entry per canonical phase with its own complete settings tuple, so changing
a default never silently changes a phase. `provenance` records whether the model
came from the catalog or was typed manually; catalog models are validated
against their agent, manual ones are not. It is required in every complete
settings tuple, `defaults` included.

A complete file must list all ten canonical phases in the order above, and
unknown keys are rejected rather than ignored, so the example is illustrative
rather than loadable. `gg configure` writes the file itself; the block style it
emits and the flow style shown here are both accepted on read. The required
phases are
`acceptance_criteria`, `grooming`, `development`, `rebase`, and
`test_document`; `planning`, `qa`, `build_checker`, `pr`, and `ci` can be
toggled. With `planning` disabled there is no plan artifact, so Development
implements the whole accepted scope in a single pass and Acceptance criteria
declares the executable verification contract instead.
`linting` is still accepted as an alias for `build_checker` in legacy files.

Sparse legacy configuration is never silently rewritten: `gg run` opens
`gg configure` so a complete replacement is saved before project selection
continues.

## `gg configure`

Run `gg configure` (or `gg --configure`) from the project folder. On a terminal,
the full-screen wizard is prefilled with the current values:

- choose the default agent, model, and effort;
- review the complete pipeline in execution order;
- use `Space` to toggle the optional phases shown by the wizard and `Enter` on
  a phase to edit its agent, model, and effort; and
- select `Save configuration` to validate and persist the staged values.

The model picker includes a manual model-name option. Every phase stores its own
complete tuple, so saving writes the whole template rather than pinning only the
values that differ. Required phase rows are locked: their enabled state and
ordering cannot be changed, though their agent, model, and effort still can.
Folder reconfiguration may be repeated and affects only future projects.

When input or output is not a terminal, the wizard falls back to line-oriented
prompts. The fallback asks for the default values first, then offers an opt-in
sequence for per-phase overrides. Values are staged and validated before either
global or project configuration is saved.

## Run-only overrides

Run flags affect only that invocation; they are not persisted as ambient
configuration:

```bash
gg run dashboard \
  --parent-branch main --base-ref HEAD \
  --enable-pr --disable-ci --max-iterations 2
```

`--parent-branch` and `--base-ref` set GitOps references; `--enable-pr`,
`--disable-pr`, `--enable-ci`, and `--disable-ci` control release integration;
`--max-iterations` bounds the QA feedback loop; and
`--repair-existing-verification` opts Development into repairing verification
failures that predate the run. These operational flags are the complete run-only
set, and `--` passes every following token to the pipeline unchanged.
`gg resume` accepts `--repair-existing-verification` with the same meaning; the
GitOps and iteration flags apply to `gg run` only.

Agent, model, effort, and phase-structure selection moved to the attached
project picker, where `gg run` first offers
`Inherit folder configuration` or `Pick configuration for this project`; a
picked snapshot is isolated and never written back to the folder. A stopped or
failed project resumes from its persisted execution snapshot, so its original
run settings remain in force when using `gg resume`. To change them first,
attach to the project and press `e`, which reconfigures a stopped or failed
project before it is resumed.

## Grooming interview

Project creation can begin a persisted grooming interview before the pipeline:

- In the conversational flow, the selected agent asks questions in the project
  folder and appends each answer immediately to `.gg/interview-answers.md`.
  When the user says they are done, the session writes the
  `<!-- interview complete -->` marker; gg ingests the answers and removes the
  temporary file.
- If the conversational session cannot launch, gg falls back to a question-list
  flow. It asks one question at a time, folds non-empty answers into the
  acceptance criteria as `Clarification — Q: ... A: ...`, and asks the agent for
  another round when needed, up to three clarification rounds.
- Answered questions are stored in project state immediately. Leaving a session
  or pressing `Esc` pauses the interview with unanswered questions pending;
  reattach and press `g` to continue. The project view shows that it is waiting
  for grooming answers, and the pipeline does not start past an unanswered
  interview.
- A broken question check leaves the interview pending with a retry message.
  Deliberately submitting empty answers for every current question opts out and
  allows the pipeline to proceed.

Non-TTY runs skip the interactive grooming interview. The ordinary project
description prompt still has a line-oriented fallback: finish the description
with an empty line.
