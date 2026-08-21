# Plan

Break down a task into verifiable steps before executing.

## Process

1. **Restate the goal**: In one sentence, what does "done" look like?
2. **Identify unknowns**: What do you need to learn before coding?
3. **Break into steps**: Each step should be independently verifiable.
4. **Define verification**: For each step, how do you know it worked?
5. **Identify risks**: What could go wrong? What's the fallback?

## Output Format

```
## Goal
[One sentence: what "done" looks like]

## Steps
1. [Step] -> verify: [how to confirm]
2. [Step] -> verify: [how to confirm]
3. [Step] -> verify: [how to confirm]

## Risks
- [Risk]: [mitigation]
```

## Rules

- Plans should be short. If it's more than 7 steps, you're either over-planning or the task needs splitting.
- Each step should take roughly the same effort. If one step is 80% of the work, break it down further.
- Verification must be concrete: a test passes, a command outputs X, a file contains Y.
- Don't plan what you can just do. If it's 5 minutes of work, skip the plan and execute.
