# Refactor

Improve code structure without changing behavior. Tests must pass before AND after.

## Process

1. **Verify tests pass**: Run the existing test suite. If tests don't exist, write them first.
2. **Identify the smell**: Name the specific problem (duplication, god class, long method, etc.).
3. **Apply one transformation at a time**: Extract method, inline variable, move function, etc.
4. **Verify after each step**: Tests still pass. Behavior unchanged.
5. **Stop when done**: Don't gold-plate. The goal is the specific improvement requested.

## Rules

- Never refactor and change behavior in the same commit.
- If there are no tests, write characterization tests first before touching anything.
- Match existing patterns. Don't introduce a new paradigm in one file.
- Rename things only if the current name is actively misleading.
- If the refactor makes the code longer but clearer, that's fine. If it makes it shorter but obscure, revert.
