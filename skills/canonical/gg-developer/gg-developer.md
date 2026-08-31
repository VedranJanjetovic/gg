---
name: gg-developer
description: Implement code changes in any language following repo conventions, clean-architecture boundaries, service-local contracts, tests, and observability.
---

Before writing code, detect the target project's toolchain from the repository itself — never assume one:
- Language and build system: read the build manifest present (`go.mod`, `pom.xml`/`build.gradle`, `package.json`, `pyproject.toml`/`setup.cfg`, `Cargo.toml`, `*.csproj`, `Gemfile`, `composer.json`).
- Declared commands: check `Makefile`/`Justfile`/`Taskfile`, `scripts/`, `package.json` scripts, and the CI workflow (`.github/workflows/`, `.gitlab-ci.yml`) — CI is the authoritative list of build, test, format, and lint commands.
- Existing conventions: match the layout, naming, error style, and test shape already in the tree over anything you would do by default.

When asked to implement code changes:
- Identify the owning service / code / app first and follow both the root `AGENTS.md` and internal AGENTS.md.
- Preserve existing API surface and route grouping, unless explicitly requested.
- Keep handler logic thin: validation + orchestration. Put multi-step logic in the service-owned use-case/service layer and keep adapters infrastructure-focused.
- Propagate cancellation and deadlines through every I/O boundary (HTTP clients, queues, databases, cloud SDKs) using the language's idiom (Go `context.Context`, Java/JS `AbortSignal`/`CancellationToken`, Python `asyncio` cancellation + timeouts).
- Error handling: wrap/annotate errors with context using the language's mechanism; avoid losing root cause; return consistent HTTP status codes.
- Observability: use structured logs with relevant business identifiers (for example `orgID`, `subnetID`, `relayerID`, `workflowID`) and preserve metrics/tracing behavior.
- Keep shared code in common place (e.g. `libs/shared` or `common`, depending what exists today, double check) focused on reusable platform logic; do not move service-owned business rules into shared modules.
- When touching models or handler request/response shapes, update API schema/docs targets if the service exposes them.

Testing and verification:
- Run the project's own declared build, test, format, and lint commands as discovered above — prefer the repo's task runner target over invoking a toolchain directly, and mirror exactly what CI runs. Do not introduce a tool the project has not adopted.

Before finalizing:
- Summarize what changed and why.
- Suggest verification commands.
