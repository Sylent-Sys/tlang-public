# TLang — Production Readiness & Maturity Assessment

**Status: honest capability statement.** This document rates how ready TLang is
to build a production backend, what it is genuinely good for today, and what it
still lacks. It is deliberately frank — overselling a young language is how it
loses trust. It was written for the completed v1 compiler (all pipeline stages,
the C runtime, the full test suites, and all four flagged contract gaps merged to
`main`) and updated on 2026-10-06 after `@After` hooks (PR #14), chunked request
bodies (PR #15), the module system (PR #16), the language server, and the
TLang vs Node.js vs Bun benchmark.

Section references like §13 point into the spec
(`TLang_Technical_Specification_1.2.1.md`); DESIGN §x into `docs/DESIGN.md`;
RUNTIME §x into `docs/RUNTIME.md`.

---

## 1. Overall rating: 5 / 10 for production backend work

TLang is an impressive, genuinely-complete **compiler**, but a *language* is more
than a compiler and production readiness is more than "it runs fast." The core
(compiler + runtime) is roughly a 7–8; the *platform around it* (libraries,
ecosystem, tooling, real-world mileage) is still roughly a 2–3. Weighted for the
real question — "can a team actually ship and operate a backend with this?" — it
was about **4** for single-file v1. Modules (PR #16) removed the biggest blocker,
which §5 puts at 5–6; with the standard library, ecosystem, and production
mileage unchanged, it sits at the low end: about **5**.

The number alone is not useful; §2 and §3 explain it.

---

## 2. What drags the score down (the gaps that matter in production)

1. **Modularity — RESOLVED (PR #16); what remains is packaging.** v1 was
   single-file, which was the single biggest blocker. TLang now has ES-style
   `import`/`export` across files in one project (`docs/DESIGN-modules.md`).
   What is still missing: there are no packages — no bare specifiers,
   versioning, or third-party dependencies (see item 3).
2. **Thin standard library.** The built-ins are focused: HTTP server, PostgreSQL,
   JSON, strings, numbers, console. Production backends routinely need more:
   an outbound **HTTP client** (calling other APIs), caching/Redis, richer config
   than a handful of env vars, time/date handling, **crypto/hashing** (passwords!),
   UUIDs, a logging framework, regex, and general file I/O. If a service needs to
   call a third-party API or hash a password, that is not available today without
   dropping to C.
3. **No third-party ecosystem.** No package manager, no library registry, no
   community code. Anything not built in, you build yourself. This is the
   difference between "a language" and "a platform."
4. **Database story is PostgreSQL-only and query-level.** No migrations, no
   ORM/query builder, and no driver for anything but Postgres (no MySQL, SQLite,
   or others). SQL-as-string-literals works and is safe (parameterized, checked),
   but there is no schema management.
5. **Unproven in the wild.** No real deployments, no production battle scars. The
   test suite is excellent (golden + fuzz + e2e + benchmark, all green across
   gcc/clang/tcc), but tests are not the same as production mileage. Every young
   language has a tail of bugs that only real traffic finds.
6. **Bare operational tooling.** `@After` hooks (PR #14) give a place to hang
   per-request logging and metrics, but there is no logging/metrics/tracing
   library to call from them. There is a language server with diagnostics,
   completion, and hover (`docs/LSP.md`), but no debugger/profiler integration,
   no hot-reload, and limited observability. Production is substantially about
   "what do you do at 3am when it breaks," and that tooling is not there.

---

## 3. What pulls the score up (real strengths — it is not a toy)

- **The core compiler is genuinely production-grade in quality** — reviewed,
  deterministic, and tested with golden, fuzz, e2e, and benchmark suites; bug-
  clean against its spec. That level of rigor is rare for a young language.
- **A measured run (with a configuration caveat)** — p99 ≈ 1.8 ms with 0 failed
  requests at 20,000 req/s under an open-loop, coordinated-omission-corrected
  benchmark (spec §12 gate: 0 failures, p99 ≤ 2.5 ms). This is a single-runtime,
  TLang-only run on a 2-core slice with 4 workers (over-subscribed, which inflates
  the tail); for the fair, worker-matched three-way comparison on this machine see
  `tests/bench/RESULTS.md`. Many mature stacks do not hit that.
- **Small and cheap to run** — in the three-way comparison (`tests/bench/RESULTS.md`,
  a Docker Desktop VM, so directional), TLang peaked at ~2.75 MiB RSS against
  ~150 MiB for Bun and ~326 MiB for Node, and used about half the CPU per
  request. Median latency was a three-way tie; tail latency could not be
  compared on that VM.
- **Memory safety by construction** — a per-request arena (no GC pauses), escape
  analysis, and strict type/name checking rule out whole classes of bugs. OOM is
  a clean request-level abort, not undefined behavior.
- **Deploys as a single tiny binary** — fast cold starts, trivial containers, no
  runtime to install.
- **A sound concurrency architecture** — fibers + an epoll scheduler + async
  libpq is a legitimately good model for I/O-bound services (RUNTIME §4).

So the *foundation* is strong; the *platform* around it is early. That split is
exactly why the aggregate is a 5 and not a 7 or a 2.

---

## 4. What TLang is good for TODAY

A single, performance-sensitive, well-scoped service where:

- PostgreSQL + an HTTP/JSON API is all the service needs,
- everything it needs lives in its own project (modules split it across files;
  there are no third-party packages), and
- a small team owns it end-to-end.

Good fits: a hot-path API gateway, a high-throughput webhook ingester, a
specialized high-performance microservice, or a few such services sharing code
through modules (the deployment split HANDOVER.md recommends instead of
work-stealing).

**Not a fit today:** a general "build my company's backend" project, anything
needing third-party integrations (payments, auth providers, external APIs),
large multi-team codebases, or databases other than PostgreSQL.

---

## 5. Trajectory — where it can realistically go

| Milestone | Score | What it unlocks |
| :--- | :--- | :--- |
| v1 (single-file) | **4** | Hobby projects; one focused microservice if you live within the limits |
| + Modules (`import`/`export`) — **today** (PR #16) | **5–6** | Real multi-file apps; a small team can build a focused service; the separate-service deployment pattern becomes practical |
| + Richer stdlib (HTTP client, crypto, config, time, logging) | **6–7** | A genuinely usable backend for well-scoped services |
| + Package ecosystem + real production mileage | **8+** | "Boring, trust it" territory |

The roadmap (HANDOVER.md) went after the highest-leverage step first:
**modules moved the needle most** (4 → 5–6), and they have landed. The thin
standard library (§2 item 2) is the natural next lever — an outbound HTTP
client, crypto/hashing, time, config, and logging are the most
production-relevant additions. It is a candidate, not planned work.

---

## 6. The honest framing

TLang today is a beautifully engineered **engine with no car around it yet**. The
engine (compiler + runtime) is excellent — arguably better-built than many
popular languages were at the same age. But production backend work needs the car:
libraries, modularity, an ecosystem, operational tooling, and scar tissue from
real traffic. The engine earns trust; the car is still being built, and this
document will be updated as it is.
