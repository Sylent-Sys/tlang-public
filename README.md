# TLang

A from-scratch compiler for **TLang**, a TypeScript-flavored language that
compiles to **C11**. The compiler is written in **Go 1.26 using only the
standard library** (no third-party modules); the generated C is built by
gcc/clang (release) or TCC (dev mode) and links against a hand-written C
runtime (fibers, an epoll scheduler, an HTTP/1.1 server, JSON, and async
libpq).

> Status: **v1 complete.** The full pipeline — lexer → parser → checker →
> codegen → driver → CLI — plus the C runtime are implemented, reviewed, and
> tested. A TLang program lexes, type-checks, generates a C11 translation unit,
> links against the runtime, and runs as a native binary that serves HTTP and
> talks to PostgreSQL. Version `0.1.0-dev`.

## What TLang looks like

A minimal REST handler (the example server lives in [`examples/app.ts`](examples/app.ts),
and is the spec's §13 program):

```typescript
interface CreateUserReq { id: int64; name: string; }
interface UserResponse  { id: int64; name: string; status: string; }

fn authGuard(ctx: Context): bool {
    let auth = ctx.header("Authorization");
    if (auth.len == 0) { ctx.text(401, "Unauthorized"); return false; }
    return true;
}

@Use(authGuard)
fn (ctx: Context) handleCreateUser(): void {
    let req = new CreateUserReq();
    if (!ctx.bindJson(req)) { ctx.text(400, "Invalid JSON Payload"); return; }

    try {
        db.transaction((tx) => {
            tx.execute("INSERT INTO users(id, name) VALUES ($1, $2)", req.id, req.name);
        });
        let res = new UserResponse();
        res.id = req.id; res.name = req.name; res.status = "SUCCESS";
        ctx.json(201, res);
    } catch (err) {
        ctx.text(500, "Database Transaction Failed");
    }
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.method.eq("POST") && ctx.path.eq("/api/users")) {
        ctx.handleCreateUser();
        return;
    }
    ctx.text(404, "Endpoint Not Found");
}
```

### Language highlights

- **Nominal, statically typed** with `int32`/`int64`/`float64`/`bool`/`string`,
  interfaces (reference semantics), arrays, and `T | null` optionals with flow
  narrowing (`if (x != null)`, `x ?? fallback`, `x!`).
- **No garbage collector.** Memory is a per-request arena that is reset after
  each request; long-lived data uses an explicit `new global` / `clone_global`.
  Lifetimes are checked at compile time (an escape analysis rejects storing a
  request value into global state).
- **Error model** via a builtin `Error` value and `throw` / `try` / `catch`,
  lowered to an explicit error flag threaded through the generated C (no
  exceptions, no `setjmp` in user code).
- **Generics** are monomorphized — one C struct/function per instantiation.
- **Server-first builtins:** a `Context` request API, a compile-time route
  matcher (`ctx.match("GET", "/users/:id")`), JSON bind/emit generated per
  interface, and a `db` API over async libpq with a per-scheduler connection
  pool and transactions.

The authoritative language/runtime definition is
[`TLang_Technical_Specification_1.2.1.md`](TLang_Technical_Specification_1.2.1.md);
the implementation decisions derived from it are in
[`docs/DESIGN.md`](docs/DESIGN.md) and [`docs/RUNTIME.md`](docs/RUNTIME.md).

## Architecture

```
source (.ts / .tlang)
  │
  ▼  lexer/      source → []token.Token (never panics; ILLEGAL + E-LEX on bad input)
  ▼  parser/     Pratt parser with error recovery → *ast.Program
  ▼  checker/    6-pass semantic analysis → *types.Info (E-* / W-* diagnostics)
  ▼  codegen/    checked AST + types → a single C11 translation unit
  ▼  driver/     toolchain discovery + build-plan assembly (gcc/clang/tcc, ± libpq)
  ▼  cmd/tlang/  the CLI: check · emit-c · run · build · version
  │
  ▼  runtime/    hand-written C11 runtime the generated code links against
```

| Package | Role |
| :--- | :--- |
| `token/ diag/ ast/ types/` | Shared contracts. `types` is rich: it holds the checker's whole output surface, type predicates, constant folding, generics instantiation, C-name mangling, regions, routes, and builtins. |
| `lexer/ parser/ checker/` | Front end: source to a type-checked program. |
| `codegen/` | Deterministic C11 emission (`Emit(prog, info) ([]byte, error)` — returns an error, never panics). |
| `driver/` | Toolchain discovery, runtime extraction/caching, and build-plan assembly. |
| `cmd/tlang/` | The user-facing CLI. |
| `runtime/` | The C runtime: arena, fibers + context switch, epoll scheduler, HTTP, router, JSON, async PostgreSQL. Embedded into the CLI with `go:embed`. |
| `tests/` | Golden, gcc/clang/tcc compile matrix, determinism, e2e, fuzz, and benchmark suites. |

## Requirements

- **Go 1.26** (standard library only).
- `tlang check` and `tlang emit-c` run on **any OS**.
- `tlang run` / `tlang build` and all C compilation and runtime tests need
  **Linux** with a C toolchain (gcc, clang, or TCC) and, for database programs,
  **libpq / PostgreSQL 17**. The repository ships a reproducible
  **`tlang-dev` container** ([`docker/`](docker/README.md)) bundling Go 1.26,
  gcc, clang, a link-capable TCC, and PostgreSQL 17 so the whole matrix runs on
  any host.

## Building and using the CLI

```sh
go build -o tlang ./cmd/tlang

./tlang version                 # tlang 0.1.0-dev
./tlang check   examples/app.ts # type-check only (any OS); exit 0 iff no errors
./tlang emit-c  examples/app.ts # print the generated C11 to stdout (any OS)
./tlang build   examples/app.ts -o app   # compile to a native binary (Linux + C toolchain)
./tlang run     examples/app.ts          # build and run (Linux + C toolchain)
```

A built server reads its configuration from the environment (host/port, thread
count, HTTP limits, pool sizing, `TLANG_DATABASE_URL`, …) — see
[`docs/DESIGN.md`](docs/DESIGN.md) §4.2 for the full table.

## Testing

```sh
# Pure-Go suites — run anywhere:
go build ./...
go vet ./...
go test ./...          # lexer, parser, checker, codegen goldens, determinism, fuzz
gofmt -l .             # empty output = formatted

# Fuzzing (Go-native): lexer, parser, checker, and the generated JSON path
go test ./tests -run '^$' -fuzz FuzzParser -fuzztime 30s
```

Everything that needs a C toolchain, TCC, or PostgreSQL runs inside the
`tlang-dev` container via the thin wrappers
([`scripts/dev.sh`](scripts/dev.sh) / [`scripts/dev.ps1`](scripts/dev.ps1)):

```sh
# Codegen golden + gcc/clang/tcc object-compile matrix
scripts/dev.sh "go test ./... -count=1 && go test -tags ccompile ./tests/..."

# C-runtime matrix across every compiler (all against libpq 17)
scripts/dev.sh "for c in gcc clang tcc; do make -C runtime clean && make -C runtime test CC=\$c; done"

# End-to-end: build a program, run it, and drive it over HTTP on three legs
# (clang release, clang ASan+UBSan, tcc). DB cases self-skip without a URL.
scripts/dev.sh "go test -tags e2e ./tests/... -count=1"
```

Database-backed e2e and runtime tests self-skip (and pass) unless
`TLANG_TEST_DATABASE_URL` points at a disposable PostgreSQL instance — the
container ships PostgreSQL 17 for that harness. See
[`docker/README.md`](docker/README.md) for the full matrix and the design
rationale behind the image.

## Editor support / Language Server

TLang ships a Language Server Protocol (LSP) server plus a thin VS Code client.
The server (`cmd/tlang-lsp/`, standard library only) reuses the compiler front
end to publish diagnostics and answer completion and hover requests over stdio;
the VS Code extension under [`editors/vscode/`](editors/vscode/) discovers the
binary (the `tlang.lsp.path` setting, then the binary bundled in the
platform-specific `.vsix` from a GitHub release, then `PATH`) and launches it. See [`docs/LSP.md`](docs/LSP.md) for the architecture, feature set,
position-mapping contract, and limitations.

## Benchmark

An opt-in load generator (`tests/benchmark_test.go`, enabled with
`TLANG_BENCH=1`) drives the server over persistent keep-alive connections with
an **open-loop, coordinated-omission-corrected** generator and an HDR-style
histogram. The spec's performance gate is **0 failed requests and p99 ≤ 2.5 ms**;
it is enforced only under disjoint CPU pinning and advisory otherwise (so it
cannot flake on shared hardware).

A representative run on a 2-core slice of an Intel Xeon Platinum 8488C, server
pinned to cores 0,1 and the generator to cores 2,3 (gate **enforced**):

```
Load:      50 keep-alive connections, 20000 req/s, 3s warm-up + 10s measured
Requests:  total=200000  failed=0
Latency:   p50=0.141ms  p90=1.021ms  p99=1.788ms  p99.9=18.448ms  Max=57.798ms
Gate:      p99 <= 2.5 ms, failures == 0  =>  PASS
```

**Read this with the configuration caveat.** This is a **single-runtime,
TLang-only** `TestBenchmark` run on a 2-core Xeon slice launched with
`TLANG_THREADS=4` — four worker threads over-subscribed onto two cores, which
inflates the tail: the p90=1.021 ms sitting at ~7× the p50=0.141 ms is the
bimodal signature of that over-subscription, not a steady-state runtime property
(see the over-subscription fix that derives the worker count from the core
count). It is **distinct** from the three-way TLang vs Node.js vs Bun comparison.
For the methodologically-sound, worker-matched three-way numbers on this machine
see [`tests/bench/RESULTS.md`](tests/bench/RESULTS.md).

Numbers vary with hardware; this is a measured run **with that configuration
caveat**. The gate records CPU, connection count, rate, and payload size with the
result so a run can be reproduced.

## Repository layout

```
cmd/tlang/      the CLI
token/ diag/    shared token and diagnostic contracts
ast/ types/     AST and the semantic type system
lexer/ parser/  front end: source → AST
checker/        name resolution, type checking, escape + may-fail analysis
codegen/        C11 emission
driver/         toolchain discovery and build orchestration
runtime/        the C runtime (include/, src/, tests/, embedded via embed.go)
tests/          golden, ccompile matrix, determinism, e2e, fuzz, benchmark
examples/       app.ts (the §13 server) and smaller programs
docs/           DESIGN.md (implementation decisions), RUNTIME.md (C runtime map)
docker/         the all-in-one tlang-dev image + README
scripts/        dev.sh / dev.ps1 (run a command inside the container)
```

## Documentation

- [`TLang_Technical_Specification_1.2.1.md`](TLang_Technical_Specification_1.2.1.md) — the authoritative language and runtime specification.
- [`docs/DESIGN.md`](docs/DESIGN.md) — implementation decisions derived from the spec (read this first).
- [`docs/RUNTIME.md`](docs/RUNTIME.md) — the C runtime architecture and module map.
- [`docker/README.md`](docker/README.md) — the `tlang-dev` container.
- [`docs/LSP.md`](docs/LSP.md) — the language server and the VS Code / Kiro extension.
- [`RELEASING.md`](RELEASING.md) — how releases are cut, what they contain, and how to install them.
- [`HANDOVER.md`](HANDOVER.md) — current project state and how work is done here.

## Licensing

TLang uses two licences:

- **Everything except `runtime/`** — the compiler, the language server, the
  VS Code extension, the tests, and the docs — is under the
  [Mozilla Public License 2.0](LICENSE) (`MPL-2.0`).
- **The C runtime in `runtime/`** is under the
  [Apache License 2.0 with LLVM Exceptions](runtime/LICENSE)
  (`Apache-2.0 WITH LLVM-exception`).

The runtime is permissive because it is compiled and linked into every program
TLang builds. The LLVM exception means a compiled program that embeds runtime
code can be distributed without the attribution and notice conditions of
Apache-2.0 §4(a), (b) and (d), so a TLang binary carries no licence obligations
from the runtime.

**Your code and the C it generates are yours.** The C source that `tlang
emit-c`, `tlang build` or `tlang run` generates from your TLang source, and the
programs built from it, belong to you. They are not covered by the compiler's
MPL-2.0 licence; the only TLang code that ends up in them is the runtime, under
the terms above.
