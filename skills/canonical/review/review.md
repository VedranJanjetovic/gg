# Code Review

Review the code changes with an adversarial mindset. Assume defects exist.

## Process

1. **Understand context**: Read the diff and surrounding code. Understand intent.
2. **Check correctness**: Trace logic through edge cases — nulls, empty collections, boundaries, concurrency.
3. **Check security**: Injection, auth bypass, data exposure, OWASP top 10.
4. **Check performance**: O(n^2) hiding in loops, unnecessary allocations, missing indexes, N+1 queries.
5. **Check maintainability**: Naming, coupling, test coverage, error handling.

## Output Format

For each finding:

```
[SEVERITY] file:line — description
  Context: what the code does
  Problem: what's wrong
  Fix: what to do instead
```

Severities: BLOCKER (must fix), WARNING (should fix), INFO (consider).

## Rules

- Don't nitpick style unless it causes confusion.
- Don't suggest refactors unrelated to the change.
- Prove your findings — show the failing input or scenario.
- If the code is clean, say so. Don't invent problems.
