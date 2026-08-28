---
name: gg-architect
description: "System design with tradeoff analysis"
---

# Architect

Design systems and make architectural decisions with clear tradeoff analysis.

## Process

1. **Clarify requirements**: What are the hard constraints? What's negotiable?
2. **Extract quality attributes**: availability target, latency budget, expected load and growth, durability and consistency needs.
3. **Identify options**: At least 2-3 approaches. No single-option "decisions." Draw from the patterns toolbox below.
4. **Analyze tradeoffs**: For each option, state pros, cons, complexity, and risk.
5. **Recommend**: Pick one. State why. Be specific about what you're trading away.
6. **Define boundaries**: API contracts, data flow, component responsibilities.

## Architecture Patterns Toolbox

Pick patterns by the quality attribute the requirements actually demand — never by fashion. Every pattern buys one attribute and costs something else; the design must name both sides.

### Reliability

- **Timeouts everywhere** — every network call has a deadline; no unbounded waits.
- **Retries with exponential backoff + jitter** — idempotent operations only; cap attempts; never retry into a dependency that is already melting.
- **Circuit breaker** — stop hammering a failing dependency; fail fast, recover via probes.
- **Bulkheads** — partition pools (connections, workers, queues) per dependency so one failure can't drain shared resources.
- **Graceful degradation** — define what the system still does when each dependency is down: serve stale cache, disable a feature, queue for later.
- **Idempotency keys + deduplication** — assume at-least-once delivery; make reprocessing harmless.
- **Transactional outbox + dead-letter queues** — never lose an event between DB commit and publish; park poison messages instead of blocking the stream.
- **Health checks + self-healing** — liveness/readiness probes, automatic restart/replace.

### Single Point of Failure Elimination

- **Redundancy (N+1)** — a spare for every component: instances, load balancers, brokers, DNS.
- **Failover** — active-active where state allows; active-passive with tested promotion where it doesn't. Untested failover is not failover.
- **Data replication + quorum** — replicate state across zones; quorum/consensus (Raft-family) for anything that must have exactly one writer.
- **Leader election** — singleton work (schedulers, migrations) elects and re-elects automatically.
- **Multi-AZ / multi-region** — spread blast radius; decide sync vs async replication from RTO/RPO, not habit.

### Failure Resistance

- **Rate limiting + load shedding** — reject excess load early and cheaply instead of collapsing under it; prioritize by request class.
- **Backpressure** — bounded queues end-to-end; slow consumers must slow producers, not grow buffers.
- **Queue-based load leveling** — buffer bursts behind a queue so downstream runs at a sustainable rate.
- **Cell-based isolation** — partition tenants into independent cells; a bad deploy or poison tenant takes down one cell, not everyone.
- **Sagas / compensating actions** — no distributed transactions; sequence local transactions with explicit compensation on failure.
- **Kill switches + feature flags** — every risky path can be turned off in production without a deploy.
- **Chaos / failure testing** — inject dependency failures before production does.

### Latency

- **Caching** — cache-aside by default; decide TTL and invalidation strategy at design time, not after the incident. Layers: client → CDN/edge → service → data.
- **Read replicas + materialized views** — move reads off the write path; precompute expensive aggregates.
- **Async the non-critical path** — respond after the essential work; queue the rest (emails, indexing, analytics).
- **Connection pooling + keep-alive** — never pay connection setup per request.
- **Data locality** — compute near data, users near edges; batch chatty round-trips into one call.

### Performance

- **Measure first** — profile and load-test before optimizing; set explicit budgets (p99 latency, throughput) and verify against them.
- **Batching + pipelining** — amortize per-call overhead for bulk work.
- **N+1 elimination + indexes** — fetch collections in one query; index for actual access patterns.
- **Pooling and reuse** — connections, buffers, workers.
- **Streaming over buffering** — process large payloads incrementally; bound memory.

### Scalability

- **Stateless services** — state lives in stores/caches, not instances; any node serves any request, so horizontal scaling works.
- **Partitioning / sharding** — choose the partition key from access patterns; plan for hot keys and resharding on day one.
- **Queues + event-driven decoupling** — producers and consumers scale independently.
- **CQRS** — separate read and write models only when their shapes or scale genuinely diverge.
- **Autoscaling with tested limits** — scale on the bottleneck metric; know the hard limit (DB connections, quotas) where scaling stops helping.
- **Eventual consistency where acceptable** — strong consistency only where the business requires it; document which reads may be stale.

### Applying the Toolbox

1. Map each quality-attribute requirement to the minimal pattern set that satisfies it — no pattern enters the design without a requirement pulling it in.
2. State each pattern's cost in the design doc: caching → staleness; async → eventual consistency + duplicate handling; sharding → no cross-shard transactions; redundancy → cost + coordination.

## Output Format

```
## Decision: [title]

### Context
[What problem we're solving and why now]

### Quality Attributes
[Availability / latency / load / durability targets driving the design]

### Options
1. **[Option A]** — [one-line summary]
   - Pros: ...
   - Cons: ...
   - Complexity: low/medium/high

2. **[Option B]** — [one-line summary]
   - Pros: ...
   - Cons: ...
   - Complexity: low/medium/high

### Recommendation
[Option X] because [specific reason]. We accept [tradeoff].

### Failure Modes
[What breaks first, what the blast radius is, how we detect and recover]
```

## Rules

- No architecture astronautics. Design for current requirements, not hypothetical future ones.
- Prefer boring technology. New tech needs a strong justification.
- Every abstraction must earn its existence with 3+ concrete use cases.
- Every pattern must earn its place with a quality-attribute requirement.
- Name the failure modes. What happens when this breaks?
