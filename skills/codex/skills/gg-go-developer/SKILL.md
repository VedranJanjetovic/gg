---
name: gg-go-developer
description: Implement Go changes following repo conventions, clean-architecture boundaries, service-local contracts, tests, and observability.
---

When asked to implement code changes:
- Identify the owning service / code / app first and follow both the root `AGENTS.md` and internal AGENTS.md.
- Preserve existing API surface and route grouping, unless explicitly requested.
- Keep handler logic thin: validation + orchestration. Put multi-step logic in the service-owned use-case/service layer and keep adapters infrastructure-focused.
- Thread `context.Context` through all I/O boundaries (AWS, HTTP clients, queues, databases).
- Error handling: wrap/annotate errors with context; avoid losing root cause; return consistent HTTP status codes.
- Observability: use structured logs with relevant business identifiers (for example `orgID`, `subnetID`, `relayerID`, `workflowID`) and preserve metrics/tracing behavior.
- Keep shared code in common place (e.g. `libs/shared` or `common`, depending what exists today, double check) focused on reusable platform logic; do not move service-owned business rules into shared modules.
- When touching models or handler request/response shapes, update Swagger/docs targets if the service exposes them.

Testing and verification:
- Run: go build / make build / go test / make test / make fmt / make lint / go fmt  (check local MAKEFILE and / or project to understand how to validate formatting, linter, tests and build)

Before finalizing:
- Summarize what changed and why.
- Suggest verification commands.
