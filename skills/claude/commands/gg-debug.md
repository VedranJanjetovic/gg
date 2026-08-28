---
name: gg-debug
description: Scientific method debugging — reproduce, hypothesize, verify
argument-hint: "[bug description or error message]"
allowed-tools:
  - Read
  - Glob
  - Grep
  - Bash
  - Edit
  - Write
  - Agent
---

Systematic debugging using the scientific method.

## Process

1. **Reproduce**: Write a minimal reproduction or test that triggers the bug.
2. **Hypothesize**: Form a specific, falsifiable hypothesis about the root cause.
3. **Test hypothesis**: Add logging, read code, or run experiments to confirm/refute.
4. **Iterate**: If refuted, form a new hypothesis. Don't guess-and-check randomly.
5. **Fix**: Apply the minimal fix. Don't refactor unrelated code.
6. **Verify**: Run the reproduction to confirm the fix. Check for regressions.

## Rules

- Always reproduce before fixing. A fix without a reproduction is a guess.
- Fix the root cause, not the symptom. If a null check "fixes" it, ask why it's null.
- One fix per bug. Don't bundle unrelated changes.
- If the bug is in a dependency, document the workaround clearly.

## Target

Debug: $ARGUMENTS
