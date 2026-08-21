# Coding Patterns

Reference for how code should be designed in every project. Principles come first,
patterns second: a pattern is only justified when a principle demands it. When in
doubt, choose the simplest design that satisfies today's requirements.

## 1. Core Principles

### KISS — Keep It Simple

The simplest solution that solves the problem wins. Complexity must be deliberate
and justified (performance evidence, multiple disparate consumers). If an
implementation feels clever, rewrite it until it feels obvious.

### DRY — Don't Repeat Yourself (Rule of Three)

Extract shared logic only when the *knowledge* is duplicated, not just the text.
Two similar-looking blocks may encode different decisions — merging them couples
things that change for different reasons. Wait for the third occurrence before
abstracting; a little duplication is cheaper than the wrong abstraction.

### YAGNI — You Aren't Gonna Need It

No speculative features, hooks, config options, or extension points. Build for
current requirements. Delete code paths nothing calls.

### SOLID

- **S — Single Responsibility**: a module has exactly one reason to change. If you
  need "and" to describe what a type does, split it.
- **O — Open/Closed**: extend behavior by adding new implementations of an existing
  interface, not by editing a growing switch inside stable code. (Don't pre-build
  extension points — apply this when the second variant actually arrives.)
- **L — Liskov Substitution**: any implementation of an interface must honor the
  interface's full contract — same invariants, no surprise panics, no weakened
  guarantees. If an implementation must "not support" part of the contract, the
  interface is wrong.
- **I — Interface Segregation**: many small interfaces beat one fat one. Consumers
  declare the narrowest interface they need (in Go: define interfaces at the point
  of use, e.g. `io.Reader`, not a 20-method service interface).
- **D — Dependency Inversion**: high-level policy depends on abstractions, not on
  concrete low-level detail. The database, HTTP client, and clock are injected
  behind interfaces owned by the consumer.

### Composition over Inheritance

Build behavior by combining small components (embedding, interfaces, function
fields) instead of deep type hierarchies. Inheritance couples children to parent
internals; composition keeps parts replaceable and testable.

### High Cohesion, Low Coupling

Things that change together live together; things that change independently talk
through narrow, explicit contracts. A change to one package should rarely force a
change in another. Law of Demeter: talk to your direct collaborators, don't reach
through them (`a.B().C().D()` is a design smell).

## 2. Encapsulation & Information Hiding

Private logic stays private. The public API is a promise you maintain forever;
everything else is an implementation detail you're free to change.

- Export the minimum. Start unexported; promote only when an external caller needs it.
- Never expose internal state for callers to mutate (no returning internal maps or
  slices without copying; no public fields whose invariants the type must protect).
- Constructors establish invariants: if a type has rules ("port must be set",
  "queue is bounded"), force creation through `New*` and keep fields unexported.
- Deep modules, shallow interfaces: a small API hiding substantial functionality is
  the goal. Many tiny pass-through wrappers add surface without hiding anything.
- Prefer immutability: return copies, accept values, reserve pointers for identity
  and mutation that the contract actually requires.
- Don't leak transport/storage types across boundaries — map database rows and API
  DTOs to domain types at the edge.

## 3. Dependency Injection & Inversion of Control

Dependencies are explicit, visible, and provided from the outside.

- **Constructor injection is the default**: everything a component needs arrives as
  a parameter (`NewServer(store Store, clock Clock, log *slog.Logger)`). No
  component constructs its own database client, reads global config, or reaches
  into a singleton.
- **Inversion of control**: components don't look up their dependencies (service
  locator) and don't control the application lifecycle — `main` (the composition
  root) wires the object graph and owns startup/shutdown order.
- The composition root is the only place that knows concrete types. Everywhere
  else depends on interfaces owned by the consumer.
- No DI frameworks or reflection-based containers unless the wiring is genuinely
  unmanageable by hand — explicit wiring in `main` is documentation.
- Global mutable state is a hidden dependency. Package-level variables, init-time
  registration, and singletons make call order matter and tests interfere. If a
  convenience global exists, it must be a thin proxy over an injectable instance.
- Inject the ambient world too: time (`Clock`), randomness, filesystem, network.
  Anything you'd want to fake in a test is a dependency.

## 4. Design Patterns

Patterns are vocabulary, not targets. Reach for one when the problem shape matches;
never introduce one "for structure". In Go, most patterns collapse into a small
interface plus a function.

### Creational

- **Factory / Factory Method**: a function that returns an interface, choosing the
  concrete type from input (`NewStore(cfg) (Store, error)` returning Postgres or
  in-memory). Use when callers must not know concrete types.
- **Abstract Factory**: a factory that produces a *family* of related objects that
  must be consistent with each other (e.g. one `Driver` yielding matching `Conn`,
  `Tx`, `Stmt`). Rare — only when families genuinely vary together.
- **Builder**: staged construction for objects with many optional parts. In Go,
  prefer an options struct or functional options (`New(addr, WithTLS(cfg),
  WithTimeout(5*time.Second))`) over fluent builder chains.
- **Prototype**: clone a configured instance instead of rebuilding it. Useful for
  templates of expensive-to-construct objects; requires a correct deep copy.
- **Singleton**: almost always wrong — it's global state with a design-pattern
  badge. If exactly-one semantics are required (process-wide connection pool),
  create it once in `main` and inject it; don't gate access behind a global.

### Structural

- **Adapter**: make an existing type satisfy an interface it wasn't written for.
  The standard tool for integrating third-party code without letting its types
  spread through yours.
- **Facade**: one simple entry point over a messy subsystem. Use at package
  boundaries to keep the public API small (see Encapsulation).
- **Decorator**: wrap an implementation to add behavior behind the same interface
  — logging, metrics, retries, caching around a `Store` or `http.RoundTripper`.
  Composable and test-friendly; prefer it to editing the wrapped type.
- **Proxy**: same shape as decorator, but controls *access* — lazy initialization,
  rate limiting, authorization checks, remote stubs.
- **Composite**: tree of parts where groups and leaves share one interface
  (filesystem nodes, UI trees, expression ASTs).
- **Bridge**: split an abstraction from its implementation so both vary
  independently (message `Notifier` × transport `Sender`). Only when both axes
  really do vary.
- **Flyweight**: share immutable heavy data between many instances (interning).
  A performance pattern — apply only with profiling evidence.

### Behavioral

- **Strategy**: swap an algorithm behind an interface or function field
  (`type Backoff func(attempt int) time.Duration`). The default answer to "this
  behavior varies".
- **Observer / Pub-Sub**: decouple event producers from consumers. In-process:
  channels or callback registries; cross-service: message broker. Beware hidden
  control flow — keep handlers independent and idempotent.
- **Command**: reify an action as a value so it can be queued, logged, retried,
  or undone (job queues, undo stacks, migration steps).
- **Chain of Responsibility**: pass a request through handlers until one claims
  it — middleware stacks (`func(next Handler) Handler`).
- **Template Method**: fixed skeleton with pluggable steps. In Go, prefer passing
  step functions into one driver function over embedding-based overrides.
- **State**: behavior changes with internal state — model states as types behind
  one interface instead of switch-on-enum spread across methods.
- **Iterator**: sequential access without exposing the container (Go: channels,
  callback `func(yield func(T) bool)`, or `iter.Seq`).
- **Mediator**: centralize many-to-many coordination in one place instead of
  letting peers reference each other.
- **Memento**: capture/restore state snapshots (undo, crash recovery) without
  exposing internals.
- **Visitor**: many operations over a fixed type family (compilers, linters).
  Heavy; in Go a type switch is usually clearer until operations multiply.

**Anti-pattern check**: if removing the pattern makes the code shorter and no
harder to change, remove it.

## 5. Concurrency: Thread Pools, Workers, Async vs Sync

### Default: synchronous

Synchronous code is easier to read, test, and reason about. Introduce concurrency
only for a measured reason: I/O parallelism, throughput under load, latency hiding,
or genuinely independent work. Concurrency is a performance tool, not a style.

### Bounded workers, always

Unbounded concurrency is a bug (resource exhaustion, thundering herds). Every
concurrent design names its limit.

- **Thread pools** (Java/C#/Python/C++): fixed-size executor + bounded queue.
  Size from the workload: ~cores for CPU-bound, higher for I/O-bound. A full queue
  must apply backpressure or reject — never grow unbounded.
- **Go worker pools**: goroutines are cheap but not free — bound them.

  ```go
  // N workers draining a channel; errgroup propagates the first error
  // and cancels the rest via ctx.
  g, ctx := errgroup.WithContext(ctx)
  jobs := make(chan Job)            // unbuffered: producers feel backpressure
  for i := 0; i < workers; i++ {
      g.Go(func() error {
          for job := range jobs {
              if err := process(ctx, job); err != nil {
                  return err
              }
          }
          return nil
      })
  }
  // produce, close(jobs), then g.Wait()
  ```

  Or `errgroup.SetLimit(n)` / a semaphore channel for fan-out over a slice.

### Go rules

- Every goroutine has a known owner and a known exit: who starts it stops it.
  No fire-and-forget goroutines — they leak.
- Accept `context.Context` as the first parameter of anything that blocks; honor
  cancellation and deadlines all the way down.
- Share memory by communicating: channels for handoff and pipelines, mutexes for
  simple shared state. Don't mix both around the same data.
- Close channels from the sender side only. Specify channel direction in
  signatures (`<-chan`, `chan<-`).
- The race detector runs in CI (`go test -race`). A data race is a failed build.

### Async vs sync APIs

- Expose synchronous APIs; let the *caller* add concurrency. A blocking
  `Fetch(ctx)` composes into any pool; a callback/future-based API forces its
  model on everyone.
- Go async only at real async boundaries: queues between services, event handlers,
  long-running jobs with progress reporting.
- Async work needs the same rigor as sync: bounded queues, backpressure, timeouts,
  retry with exponential backoff + jitter, idempotent handlers (at-least-once
  delivery is the norm), and dead-letter handling for poison messages.

## 6. Reusability

- Reuse comes from small, single-purpose units with narrow contracts — not from
  "generic" frameworks built in advance.
- Package by domain capability, not by layer (`billing`, `inventory` — not
  `utils`, `helpers`, `managers`, `common`).
- A unit is reusable when it's context-free: no hidden global config, no
  environment assumptions, dependencies injected.
- Don't generalize on the first use (YAGNI) — but when the third caller appears,
  extract deliberately and give the shared piece its own tests and docs.
- Prefer the standard library, then existing internal libraries, then a
  well-maintained dependency, then writing your own — in that order.

## 7. Testability

Testability is a design property. Code that's hard to test is announcing a design
problem — fix the design, not the test.

- **Functional core, imperative shell**: keep business logic in pure functions
  (input → output, no I/O). Push side effects (DB, network, clock, filesystem) to
  a thin outer layer. The core needs no mocks at all.
- **Seams via DI**: every external effect sits behind a consumer-owned interface,
  so tests substitute a fake. If you can't fake it, you can't test it.
- Fake what you own: write test doubles for *your* interfaces; don't mock
  third-party clients directly — adapt them first (Adapter, §4).
- Prefer real implementations when cheap (in-memory store, `httptest.Server`,
  temp dirs) over hand-written mocks; prefer state assertions ("what happened")
  over interaction assertions ("what was called") — the latter welds tests to
  implementation.
- Deterministic by construction: injected clock and randomness, no sleeps, no
  ordering assumptions, no shared mutable fixtures between tests.
- Table-driven tests with subtests are the default shape; each case states input
  and expected output, edge cases included (empty, nil, boundary, error paths).
- Test through the public API. Needing to test unexported internals directly is a
  smell — either the logic deserves extraction into its own unit, or the public
  contract is missing a case.

## 8. Anti-Patterns — Reject on Sight

- God objects / packages that know everything (`manager`, `util`, `common`).
- Exposing internals: public mutable fields, returned internal slices/maps,
  leaked DB/transport types across boundaries.
- Hidden dependencies: globals, singletons, service locators, `init()` magic,
  environment reads deep inside logic.
- Premature abstraction: interfaces with one implementation and no test need,
  layers that only forward calls, "future-proof" config nobody asked for.
- Pattern-itis: factories building factories, strategy objects for behavior that
  never varies.
- Unbounded concurrency: goroutines/threads without limits, owners, or
  cancellation; queues without backpressure.
- Boolean parameters that change behavior (`process(data, true, false)`) — use
  options or separate functions.
- Stringly-typed programming: magic strings where a typed constant, enum, or
  sentinel error belongs.
- Swallowed errors and catch-all recovery that hides failure instead of
  handling it.
