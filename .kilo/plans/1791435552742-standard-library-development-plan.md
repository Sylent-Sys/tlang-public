# TLang Standard Library Development Plan

## Objective

Deliver explicit compiler-provided modules (`tlang/db`, `tlang/http`, and `tlang/system`) as a production-grade, Linux-first standard library without regressing TLang's AOT C generation, deterministic output, request arenas, scheduler-pinned fibers, or existing PostgreSQL/HTTP fast paths.

This is an unreleased-v1 source break: implicit `db` and `console` access is removed without a legacy source mode. Existing source, examples, tests, and editor behavior migrate in the same release.

## Fixed Decisions

- Add project manifest `tlang.json`; it defines entry/project root, requested capabilities, runtime limits, target requirements, and named database resources.
- Compile manifest requests into program metadata. Deployed binaries receive grants from an explicit versioned JSON file (`--tlang-grants <path>`); secrets and grant contents are never echoed in diagnostics.
- Preserve current builtin IDs and C lowerings during initial import gating; never renumber existing `BuiltinID` values.
- Standard imports support the same implemented syntax as source modules, including aliases, default imports, namespace imports, named re-exports, and default declaration exports.
- Support namespace-valued exports fully (for example `import * as system from "tlang/system"; system.console.info(...)`) by extending checker/type selection—not by narrowing syntax.
- Use named functions and compiler-known registration for middleware, hooks, and ORM callbacks. General closures/lambdas are a separate language RFC.
- Native dependencies use a hybrid policy: vendor SQLite amalgamation and small UUID hash implementations; discover/version-check system TLS and ICU/tzdb dependencies with clear feature diagnostics.
- SQLite runs on a bounded blocking worker pool; it never blocks an epoll scheduler thread.
- Request fibers remain scheduler-pinned. Add worker-job distribution now; add accepted-socket handoff only if metrics justify it. True live-fiber stealing is evidence-gated behind a separate M:N runtime RFC.
- Each phase requires exact public declarations and ABI approval before its implementation PRs begin. The master roadmap fixes dependencies and behavior; subsystem specs freeze detailed signatures.

## Cross-Cutting Invariants

- `Graph.Modules` contains source modules only. Virtual modules never receive fake paths, parser ASTs, module tags, or emitted C declarations.
- Imports control visibility, not authority. Effective runtime authority cannot exceed requests compiled from `tlang.json` and must be covered by the runtime grant file.
- Runtime capability validation occurs before listener binding, worker/scheduler creation, database pool creation, global initialization, or user entry.
- All standard I/O uses the existing fiber suspension/cancellation model. Workers never execute generated TLang frames or access fiber arenas, request contexts, scheduler globals, PG pools, or stack locals.
- Typed JSON and typed DB rows keep generated direct paths. Generic/dynamic paths are opt-in and bounded.
- Large data uses streaming/backpressure. Whole-value convenience APIs have configurable defaults and hard ceilings.
- New link features replace the single `UsesDB` boolean and participate in runtime cache keys.
- Every phase adds target support metadata, deterministic diagnostics, security tests, and performance budgets.

## Phase 0 — Conformance Prerequisites

1. Correct implemented-module conformance gaps before building standard modules:
   - Add graph-wide collision detection for `module.Tag`/generated C names in `module/graph.go`, checker/codegen entry points, and tests.
   - Decide and implement secure source-root symlink containment or retain the documented non-sandbox behavior; do not conflate it with runtime filesystem capabilities.
   - Strengthen global-initializer analysis for function-call effects or add a deterministic rejection when safe global-read effects cannot be proven.
2. Add an implementation-conformance test table covering `module/resolve.go`, `module/graph.go`, `ast/import.go`, `parser/import.go`, `checker/program.go`, `checker/entry.go`, and codegen name qualification.
3. Establish benchmark baselines for compiler time, emitted C size, binary size, allocations, throughput, and p50/p99 for current JSON, HTTP, and PostgreSQL paths.

Acceptance:
- Existing module suites remain deterministic.
- Deliberately colliding tags cannot produce duplicate C identifiers.
- The documented initializer and source-root behavior matches tests.

## Phase 1 — Virtual Modules and Import Gating

### 1.1 Registry and Resolver

Files: `module/module.go`, `module/resolve.go`, `module/graph.go`, new `module/standard.go`.

- Add discriminated source/standard import targets; avoid nullable ambiguous fields.
- Recognize case-sensitive `tlang/` before local-file resolution. Reject `tlang`, `tlang/`, empty/repeated segments, `.`/`..`, and unknown modules as `E-IMPORT` without filesystem probes.
- Continue rejecting all other bare specifiers.
- Keep virtual targets on import edges for binding/LSP, but outside source ordering, mangling, merging, and code emission.
- Initial registry exports:
  - `tlang/db`: `db` canonical builtin namespace.
  - `tlang/system`: `console` canonical builtin namespace.
  - `tlang/http`: register only when Phase 4 declarations exist; before then report the module as unavailable, not empty.

### 1.2 Checker and Type Model

Files: `checker/program.go`, `checker/checker.go`, `checker/builtins.go`, `checker/scope.go`, `types/object.go`, `types/universe.go`, `types/builtin.go`, `types/info.go`.

- Remove `db` and `console` from universe fallback; keep primitive/special language types predeclared.
- Add canonical standard-export lookup keyed by stable registry descriptors.
- Generalize import/re-export binding to source or virtual export sets.
- Extend `types.Selection`/selection kinds for namespace-valued exports and recursive standard namespace selection.
- Make `checker.Check` bind known virtual imports for import-based unit tests; keep relative imports exclusive to `CheckProgram`.
- Ensure aliases and re-exports resolve to the same canonical builtin object.
- Preserve transaction parsing/lowering when `db` is aliased; revise diagnostics that hard-code receiver spelling.

### 1.3 Console Migration

Files: `types/builtin.go`, `checker/builtincall.go`, `codegen/builtins.go`, `runtime/include/tlang.h`, `runtime/src/console.c`.

- Append `BuiltinConsoleInfo`; do not repurpose or renumber `BuiltinConsoleLog`.
- Remove source availability of `console.log`; migrate to `console.info`.
- Initially lower `info` to the existing runtime output behavior. Add `debug`/`warn` only with Phase 2 structured logging ABI.

### 1.4 LSP and Corpus

Files: `cmd/tlang-lsp/modules.go`, `completion.go`, `frontend.go`, hover/definition handlers; `examples/app.ts`; all DB/console golden, reject, module, and e2e fixtures.

- Complete virtual module specifiers/exports and nested namespace members.
- Remove implicit `db`/`console` completion.
- Use a synthetic read-only declaration URI for hover/go-to-definition, generated from the virtual registry, so editor behavior is stable without fake project files.
- Update all source fixtures and golden C output.

Validation:
- Resolver tests prove zero filesystem probes for standards and unchanged source tags/order.
- Checker tests cover unimported `E-NAME`, per-file scope, aliases, namespace/default imports, collisions, re-exports, unknown module/export, and transaction aliases.
- Codegen output differs only by intended console symbol/migration changes.
- LSP completion/hover/definition tests pass.
- Full Go, golden, determinism, C-compile, fuzz, and e2e suites pass.

## Phase 2 — Program Features, Manifest, Capabilities, Errors, Logging

### 2.1 `tlang.json`

Add a standard-library-only Go package (for example `project`) and integrate it with `cmd/tlang` and LSP.

Versioned schema v1:
- `entry`: project-root-relative source path.
- `language`: current unreleased v1 identifier.
- `target`: platform/architecture requirements.
- `capabilities`: exact environment names; filesystem roots/modes; executable identities/constraints; connect/listen protocol/host/port/group scopes; lifecycle signal request.
- `databases`: named resources with engine (`postgres`/`sqlite`) and non-secret configuration identifiers.
- `limits`: overrides bounded by compiler/runtime hard ceilings.

Rules:
- Discover `tlang.json` upward from an explicitly supplied entry/project path; reject ambiguity.
- Project root comes from manifest location.
- Secrets/URLs belong in the runtime grant/config document, not `tlang.json` or generated C.
- Validate unknown fields and schema versions strictly.

### 2.2 Whole-Program Reachability

Files: checker call analysis near `checker/mayfail.go`, `checker/entry.go`, `types/info.go`.

- Add entry-rooted reachability after entry selection and generic instantiation closure.
- Roots: selected entry, globals, generated registration/startup code, reachable guards/after hooks/middleware, and compiler-known callbacks.
- Edges: direct/method calls, concrete generics, decorators/hooks, transaction callbacks, and future App registrations.
- Conservatively include type-compatible targets for unresolved indirect calls or emit a diagnostic.
- Produce `ProgramFeatures` with runtime APIs, per-engine DB use, capabilities, and native link requirements. Keep `UsesDB` temporarily as a projection only.

### 2.3 Stable Error Categories

Files: `types/types.go`, `types/builtin.go`, checker Error construction/members, `runtime/include/tlang.h`, `runtime/src/errors.c`, codegen throw/catch.

- Extend the language/runtime Error ABI with stable category/code while retaining status/message semantics.
- Categories: permission, invalid input, not found, limit, timeout, cancelled, unavailable, conflict, I/O, database, protocol, internal.
- Backend errno/provider data is optional diagnostic metadata and not portable control flow.
- Update ABI/layout tests before privileged APIs depend on it.

### 2.4 Runtime Grant Handoff

Files: `runtime/include/tlang.h`, `runtime/src/tlang_internal.h`, `runtime/src/config.c`, `runtime/src/main.c`, generated `tlang_program`, `cmd/tlang/build_run.go`.

- Compile manifest requirements into static program metadata.
- Parse `--tlang-grants <path>` before runtime startup; `tlang run` forwards an explicit grant file.
- Validate schema/version, requests covered by grants, target support, and hard ceilings before side effects.
- Recheck resource-scoped operations at use.
- Exit nonzero with redacted diagnostics for absent/malformed/insufficient grants.

### 2.5 Environment and Structured Logging

- Environment: read-only exact-name lookup; manifest request + runtime grant; permission error differs from unset; no enumeration/mutation.
- Logging: JSON Lines with UTC RFC 3339 timestamp, level, message, scalar/generic-JSON fields; serialize complete records across scheduler threads; best-effort sink failure via runtime diagnostics; no fiber suspension.

Validation:
- Manifest parser/fuzz tests and CLI/LSP project-root tests.
- Static requirement versus manifest diagnostics.
- Startup fails before any bind/thread/global/user code on bad grants.
- Security tests for grant escalation, redaction, exact env matching, malformed JSON, and limit bypass.
- Structured log schema/concurrency tests.

## Phase 3 — Common Data, Time, UUID, Streaming/Ownership

Create and approve exact declarations in dedicated `DESIGN-stdlib-system.md` before implementation.

### 3.1 Generic JSON

- Add an opaque/reference `JsonValue` standard type rather than a language-wide tagged-union feature.
- Kinds/accessors: null, bool, float64, string, array, object; checked int access.
- Preserve generated typed JSON as the no-reflection fast path with no intermediate tree.
- Add bounded parse/stringify plus stream reader/writer; request-scoped results require explicit global/owned clone beyond request lifetime.
- Extend `types`, checker builtins, codegen, `runtime/include/tlang.h`, and `runtime/src/json.c` or a new generic JSON module.

### 3.2 UUID

- Add by-value opaque 16-byte `UUID` type and runtime `uuid.c`.
- RFC 9562 v3/v4/v5/v7 generation, generic structural parse, version-specific validation, lowercase canonical output, byte conversion.
- Vendor compact MD5/SHA-1 solely for standards compatibility; use Linux `getrandom` for v4/v7 entropy, with typed failure.
- Process-wide linearizable v7 state protected with pthread synchronization; strict order at serialization points; bounded monotonic-clock wait and typed exhaustion error.
- Inject clocks/randomness in tests; include RFC vectors, rollback, same-ms bursts, concurrent order, entropy failure, and sanitizer coverage.

### 3.3 Time

Implement in subphases:
1. `Instant`, `Duration`, `MonotonicInstant`, deterministic ISO parsing/formatting.
2. `LocalDate`, `LocalDateTime`, `Period` with JS-Date-compatible normalization where specified.
3. host-IANA `Zone`/`ZonedDateTime`, explicit zone conversions, overlap/gap behavior.
4. localized formatting through discovered ICU/host locale service.

- Keep JavaScript `Date`/ECMAScript behavior as primary reference; document every extension/difference (nanoseconds, explicit zones, 0001–9999 range).
- Never use process-global `TZ` mutation for concurrent explicit zones.
- Driver discovers ICU when locale/timezone feature is reachable; deterministic ISO works independently where feasible.
- `MonotonicInstant` is nonserializable and unavailable to DB/JSON mapping.

Validation:
- Type representation and strict-header tests under GCC/Clang/TCC where supported.
- JSON fuzz/limit/stream/lifetime tests.
- UUID RFC/concurrency tests.
- Time boundary, DST, normalization, host-data-unavailable, locale, and deterministic ISO tests.
- Bench typed versus generic JSON and binary/compile-size impact.

## Phase 4 — HTTP Framework and Client

Create `DESIGN-http.md` with exact named-function declarations and generated descriptors first.

### 4.1 Server App Model

- Add virtual `tlang/http` types: `App`, `Router`, immutable `Request`/`Response`, Context convenience adapter, middleware registration descriptors.
- Builder registrations and decorators lower to the same deterministic static registration tables.
- Use named handler/middleware functions. `next` is a compiler/runtime-provided single-use continuation returning downstream Response; no general closures.
- Enforce exactly one response; double send/return-after-send is a typed error.
- Extend entry selection to App server versus script main. Legacy `route_dispatcher` either migrates mechanically or is retained only until App parity tests pass, then removed in the same unreleased-v1 break.
- Lower configured App or `main`+recognized `serve` form to `tlang_program.dispatcher`; never recursively start `tlang_main` from script mode.

### 4.2 Streaming and Runtime

- Extend runtime HTTP parser/writer with request-scoped streaming handles, bounded helpers, backpressure, deadlines, abort cleanup, and immutable snapshots.
- Preserve existing fast path and parser security constraints; route matching uses method/raw templates, one decode, malformed-escape and encoded-separator rejection.

### 4.3 Client

- Build scheduler-integrated DNS/connect, request writer, response parser, connection pooling, streaming, cancellation, and deadlines.
- Use system TLS discovery/version checks and certificate/hostname verification.
- Enforce manifest/runtime destination grants after DNS resolution; pin checked addresses; reauthorize each optional bounded redirect. No auto redirects by default.
- Provide one-shot helpers and reusable clients; avoid per-request pool construction.

Validation:
- App entry/lifecycle and deterministic route registration tests.
- Middleware order/single-next/double-response tests.
- Request smuggling/framing, malformed paths, streaming backpressure, disconnect, cancellation, limits, and graceful shutdown tests.
- DNS rebinding, redirect authorization, TLS verification, pooling, timeout, and leak tests.
- Preserve normal HTTP p99/failure benchmark gates.

## Phase 5 — Blocking Worker Pool and Privileged System APIs

### 5.1 Worker/Completion Substrate

Files: `runtime/src/sched.c`, `fiber.c`, `tlang_internal.h`, `main.c`, new `worker.c`; tests `test_worker.c`, `test_job_completion.c`.

- Keep fibers fixed to owner scheduler.
- Add bounded shared FIFO worker queue using pthread mutex/condition variables (TCC-compatible); do not start with lock-free/work-stealing deques.
- Add per-scheduler completion inbox and completion eventfd, job-wait state, operation generation/token, and scheduler-only fiber readying.
- Jobs/results are heap-owned copies. Workers never touch arenas, globals, request buffers, PG state, cleanup links, or stack locals.
- Cancellation detaches safely; late completion frees itself after generation validation. Shutdown stops submissions, applies bounded drain/cancel policy, joins workers, then tears down schedulers.
- Configuration: worker count, queue capacity, and deadlines in manifest/grants/runtime ceilings. Queue saturation returns a typed limit/backpressure error.

### 5.2 Filesystem

- Root capability handle + relative Path; Linux `openat2` `RESOLVE_IN_ROOT`/no-magic-link policy. Platforms lacking equivalent race-safe containment do not follow symlinks initially.
- Ordinary files/directories only; atomic-visible whole-file replace; append/stream explicit; sorted listings; bounded operations; process-scoped handles and cleanup.
- Adversarial symlink/rename/TOCTOU tests.

### 5.3 Process

- Exact executable allowlist; argv execution only (no shell); constrained cwd/env/args.
- Worker/native integration, pipes with bounded capture/streaming, await/terminate, timeout terminate-grace-force-kill-reap, shutdown cleanup.

### 5.4 Sockets and Lifecycle

- Portable TCP stream/listener and UDP unicast/multicast types; no raw fd exposure.
- Direction/protocol/address/port/group grants, resolved-address pinning, deadlines, cancellation, cleanup.
- Runtime-managed SIGINT/SIGTERM cancellation event; no arbitrary handlers/sending.
- Device APIs remain outside initial release.

Validation:
- Completion/cancel/reuse/shutdown race tests under sanitizers.
- Capability bypass and resource leak tests.
- Per-feature target diagnostics.
- Submission/completion overhead, saturation, and tail-latency benchmarks.

## Phase 6 — Database Expansion and ORM

Create `DESIGN-db.md` with exact syntax before implementation.

### 6.1 Resource and Link Model

- `tlang.json` declares named typed DB resources and engines.
- Imports expose constructors/handles whose engine is statically known; replace global ambiguous DB use over time.
- `ProgramFeatures.DBEngines` drives driver link discovery/macros/cache keys independently for PostgreSQL and SQLite.
- Vendor SQLite amalgamation; preserve system libpq discovery.

### 6.2 SQLite Executors

- Worker/lane-owned SQLite connections. Nontransactional jobs use bounded pool; transactions lease a lane/connection and all begin/query/commit/rollback jobs retain affinity.
- Cleanup posts rollback/release to owning lane; cancellation detaches request without freeing in-use job state.
- Define busy/lock timeout and result limits. Never call blocking SQLite on scheduler threads.

### 6.3 Shared DB Surface

- Preserve parameterized raw SQL; never interpolate values.
- Typed row mapping remains generated.
- Add named-column dynamic rows with tagged `DbValue`; per-engine built-ins plus explicit extension adapters.
- Callback and explicit transactions use named functions/handles; no new general closures.

### 6.4 ORM/Builder

- Decorated user interfaces define table/column metadata; extend decorator syntax only through an accepted syntax RFC if arguments exceed current identifiers.
- Compile-time lower CRUD, filters, ordering, pagination, explicit joins/includes, and one-to-one/one-to-many/many-to-many metadata to engine primitives.
- No migrations/schema ownership, runtime reflection, hidden lazy queries, or unbounded materialization.
- Raw SQL remains escape hatch.

Validation:
- PG regression suite unchanged for scheduler-local pools.
- SQLite lock/busy/cancel/transaction-affinity/shutdown/saturation tests.
- Reachable/unreachable and multi-engine linkage tests through globals, generics, re-exports, registrations, and named callbacks.
- SQL injection, parameter binding, mapping, adapter, and relation query-count tests.
- ORM generated C, compile time, binary size, throughput, and allocation budgets.

## Phase 7 — Load Balancing and Work Stealing Roadmap

### 7.1 Metrics First

Add per-scheduler metrics: live/runnable fibers, run queue, accepted/active connections, epoll sleep, handler time, DB waits, worker queue/completions. Benchmark few long-lived connections, uneven CPU costs, mixed worker jobs, and more schedulers than connections.

### 7.2 Pre-Fiber Socket Handoff

Only if metrics show `SO_REUSEPORT` imbalance:
- Transfer newly accepted, not-yet-registered sockets through bounded destination inboxes.
- Destination creates the fiber and owns fd/globals/pool from first execution.
- Define exactly-one close, enqueue failure, shutdown drain, and backpressure.
- Do not move live connection fibers.

### 7.3 Optional Worker Deque Stealing

Only if the central worker FIFO is benchmarked as a bottleneck:
- Introduce per-worker deques plus stealing for heap-owned jobs, not fibers.
- Retain lane affinity for SQLite transactions and other thread-confined resources.
- Provide pthread-based synchronization compatible with TCC; prove fairness and bounded shutdown.

### 7.4 Evidence-Gated M:N RFC

True live-fiber stealing is not a committed implementation phase. Open a separate RFC only if worker offload and socket handoff leave material imbalance. The RFC must resolve:
- per-scheduler globals semantics,
- mutable fiber ownership/trampoline/loop contexts,
- epoll/deadline/wait-queue transfer and stale events,
- PG pool/transaction pinning,
- cleanup/abort `setjmp` portability,
- ASan, assembly, AArch64, and TCC/ucontext cross-thread support,
- fiber pool accounting/cancellation/shutdown,
- language-visible concurrency/data-race guarantees.

No production implementation proceeds without accepted semantics and cross-platform/sanitizer prototypes.

## Driver and Runtime Feature Model

Replace `driver.AssembleFlags(bool usesDB)` and `tlang_program.uses_db` with additive feature metadata:

- compiler result: runtime APIs, DB engines, TLS, ICU/locale, SQLite, capability families;
- driver: dependency discovery, compile definitions, link libraries, target diagnostics;
- runtime program descriptor: requirements pointer, feature bits, server/script entry;
- artifact cache key: runtime hash + compiler/toolchain + target + complete feature set/flags.

Compile or stub runtime modules consistently; unused external libraries must not be linked or configured.

## Rollout and Repository Hygiene

- Land each numbered subphase as an independently reviewable change; do not combine parser/checker/runtime/ORM rewrites into one PR.
- Keep docs/spec, compiler, runtime ABI, LSP, examples, and tests synchronized in every source-breaking phase.
- Update `README.md`, technical specification, `docs/DESIGN.md`, `docs/RUNTIME.md`, release notes, examples, and editor synthetic declarations when behavior ships—not before.
- Add migration diagnostics for unimported `db`/`console` that name the required import; no compatibility mode or implicit fallback ships.
- Never commit generated secrets, grants, database URLs, or local runtime configuration.

## Final Verification Matrix

Run after every applicable phase and as a final release gate:

- Go: format, build, vet, unit, module, determinism, fuzz smoke, golden, C-compile, e2e, driver, CLI, and LSP suites.
- Runtime: GCC, Clang, TCC, no-PG/no-SQLite feature variants, and Clang ASan/UBSan.
- Databases: PostgreSQL 17 and pinned vendored SQLite; multi-engine and failure-injection suites.
- Native dependencies: missing/unsupported TLS and ICU diagnostics; supported-version integration tests.
- Security: malformed manifest/grants, privilege escalation, env denial, filesystem TOCTOU, process constraints, DNS rebinding/redirect, TLS hostname, SQL injection, parser limits.
- Lifecycle: startup failures before side effects, graceful/forced shutdown, queued/running worker jobs, active streams, transactions, child processes, and late completions.
- Performance: compare established baselines for compile time, generated C, binary size, allocation, throughput, p50/p99, streaming peak memory, worker overhead, DB contention, and per-scheduler balance. Any regression outside an approved budget blocks release.

## Completion Criteria

The roadmap is complete when:

- no standard capability is available implicitly;
- virtual imports, LSP, diagnostics, and re-exports are deterministic;
- capabilities fail closed at build/start/use boundaries;
- async/blocking work never stalls scheduler threads unexpectedly;
- generated typed fast paths remain direct and benchmarked;
- HTTP and DB lifecycle/cancellation/streaming contracts are enforced;
- PostgreSQL and SQLite link only when entry-reachable;
- all target/dependency/security/performance gates pass;
- optional load-balancing work follows measured evidence, and live-fiber stealing remains excluded unless its separate RFC is accepted.
