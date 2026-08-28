---
name: gg-test
description: "Write meaningful tests that catch real bugs"
metadata:
  short-description: "Write meaningful tests that catch real bugs"
---

Write meaningful tests that catch real bugs. Not coverage theater.

## Process

1. **Identify what to test**: Focus on behavior, not implementation. What should the code do?
2. **Write the happy path first**: Confirm the basic contract works.
3. **Add edge cases**: Nulls, empty inputs, boundaries, large inputs, concurrent access.
4. **Add failure cases**: Invalid inputs, network errors, timeouts, permission denied.
5. **Verify assertions are meaningful**: Each assertion should fail if the code is wrong.

## Rules

- Test behavior, not implementation. Tests should survive refactors.
- One concept per test. Name tests after what they verify: `test_rejects_expired_token`.
- No test interdependence. Each test sets up its own state.
- Mock at boundaries (HTTP, DB, filesystem), not internal functions.
- If a test can't fail, it's not testing anything. Delete it.
- Run tests after writing them. A test you haven't run is not a test.
