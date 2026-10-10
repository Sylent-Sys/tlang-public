# TLang v1 Implementation Design

This document turns the TLang Technical Specification 1.2.1 (`TLang_Technical_Specification_1.2.1.md`, "the spec") into decisions an implementation can follow. Where the spec is silent or lists an open decision (Appendix B), this document picks one option and says why. Section references like §6.3 point into the spec.

Status of every decision here: implemented in v1 unless marked *(deferred)*,
except for the approved but partially implemented Phase 2 contracts in
`docs/DESIGN-modules.md` §12.6. Those contracts are not all implemented runtime
behavior.

---

## 0. Environment facts that constrain the design

Probed on the dev image (`docker/Dockerfile`, Debian trixie):

| Tool | Version | Relevant limits |
| :--- | :--- | :--- |
| Go | 1.26 | Compiler frontend. Standard library only, no third-party modules. |
| GCC | 14.2 | Release and sanitizer builds. |
| Clang | 19.1 | Release (`-O3 -flto`) and ASan/UBSan builds. |
| TCC | 0.9.27 | Dev mode. **No `stdatomic.h`, no `_Thread_local`.** Has `_Generic`, flexible array members, GNU statement expressions, x86_64 inline asm, `ucontext`, pthreads, epoll, libpq. |
| libpq | 17.11 | Has `LIBPQ_HAS_ASYNC_CANCEL` (`PQcancelStart`/`PQcancelPoll`). |

Consequences:

1. Neither generated code nor the runtime may use thread-local storage or C11 atomics when compiled by TCC. The runtime never needs a "current fiber" global: every generated function receives the fiber explicitly (§3.1 below).
2. Generated code must not use GNU statement expressions either, even though TCC accepts them, so that `-std=c11 -pedantic` builds stay clean. Use helper functions and temporaries.
3. The development host is Windows. `tlang check` and `tlang emit-c` work on any OS. `tlang run` and `tlang build` need Linux (§1.1 of the spec) and print a clear error elsewhere. All C compilation and all runtime tests run inside the `tlang-dev` container.

---

## 1. Repository layout

```
go.mod                         module tlang (Go 1.26, stdlib only)
cmd/tlang/main.go              CLI driver (run, build, emit-c, check, version)
token/token.go                 §5.1 tokens (+ additions listed in §2.1)
lexer/lexer.go                 source → []token.Token, never panics
ast/ast.go                     §5.2 AST
parser/parser.go               §5.3 Pratt parser with error recovery
diag/diag.go                   Diagnostic {Pos, Severity, Code, Message}, sorted printing
types/types.go                 semantic types (shared by checker and codegen)
checker/                       name resolution, type checking, escape analysis, may-fail analysis
codegen/                       C11 emission: lowering of receivers, @Use, try/catch, transactions, JSON, routes
driver/                        toolchain discovery, runtime extraction, tcc/clang/gcc invocation
runtime/                       C runtime (embedded into the CLI with go:embed)
  include/tlang.h              public API used by generated code (the only header generated code includes)
  src/*.c, src/*.h             implementation; private headers are never included by generated code
  tests/*.c                    C unit tests (one executable per file)
  embed.go                     package runtime: go:embed of include/ and src/
tests/                         Go test suites: golden, e2e, fuzz, benchmark (§12)
  golden/*.tl, golden/*.c.golden
examples/                      app.ts (spec §13) and smaller programs
scripts/                       dev.ps1 / dev.sh (run a command in the container), test-all.sh
docker/Dockerfile              dev image
docs/                          this file, LANGUAGE.md (language reference), RUNTIME.md
```

Source files may use the extension `.ts` or `.tlang` (§4). Tests and examples use `.tl` only where the file is not meant to look like TypeScript to editors; both are accepted everywhere.

---

## 2. Language surface (v1)

### 2.1 Lexical additions to §5.1

The §5.1 token list is kept. These tokens are added because the v1 grammar below needs them:

| Token | Lexeme | Why |
| :--- | :--- | :--- |
| `PLUS_ASSIGN`, `MINUS_ASSIGN`, `ASTERISK_ASSIGN`, `SLASH_ASSIGN`, `MOD_ASSIGN` | `+= -= *= /= %=` | compound assignment (desugared) |
| `INCREMENT`, `DECREMENT` | `++ --` | statement-level only (`i++;`), desugared to `i = i + 1` |
| `NULLISH` | `??` | `x ?? fallback` on optional types |
| `OF` | keyword `of` | `for (const x of xs)` |
| `GLOBAL` | keyword `global` | `new global T()` (§6.3, Appendix B item 3) |

Other lexical rules:

* Comments: `// ...` and `/* ... */` (unterminated block comment is an error).
* Identifiers: `[A-Za-z_][A-Za-z0-9_]*`. Identifiers starting with `__` are reserved for the compiler and rejected.
* Integer literals: decimal or `0x` hex, `_` separators allowed. Float literals: `1.5`, `1e9`, `2.5e-3`.
* String literals: `"..."` or `'...'`, escapes `\n \t \r \0 \\ \" \' \uXXXX` (and `\u{X..}`). Raw newlines inside a literal are an error. The lexer stores the decoded bytes (UTF-8) in `Token.Literal`.
* Positions are 1-based line and 1-based column counted in bytes.
* The lexer produces `ILLEGAL` tokens instead of panicking. The parser reports them.

### 2.2 Grammar summary

Semicolons are required after statements (no ASI). Interface members are separated by `;` or `,`; a trailing separator is optional.

```
program      := topDecl*
topDecl      := decorator* fnDecl | interfaceDecl | typeDecl | letDecl | constDecl
decorator    := '@' IDENT '(' (IDENT (',' IDENT)*)? ')'
fnDecl       := 'fn' ('(' param ')')? IDENT typeParams? '(' params? ')' (':' type)? block
interfaceDecl:= 'interface' IDENT typeParams? '{' (field (';'|',')?)* '}'
field        := IDENT '?'? ':' type
typeDecl     := 'type' IDENT typeParams? '=' (type | '{' field* '}') ';'
typeParams   := '<' IDENT (',' IDENT)* '>'
type         := primaryType ('[' ']')* ('|' 'null')?
primaryType  := IDENT typeArgs? | '(' type ')'
letDecl      := ('let'|'const') IDENT (':' type)? ('=' expr)? ';'
stmt         := letDecl | block | 'if' '(' expr ')' stmt ('else' stmt)?
              | 'while' '(' expr ')' stmt
              | 'for' '(' (letDecl | exprStmt | ';') expr? ';' simpleStmt? ')' stmt
              | 'for' '(' ('let'|'const') IDENT 'of' expr ')' stmt
              | 'return' expr? ';' | 'break' ';' | 'continue' ';'
              | 'try' block 'catch' ('(' IDENT ')')? block
              | 'throw' expr ';'
              | expr ';'               (includes db.transaction((tx) => { ... }); → TransactionStatement)
```

Expressions (Pratt, §5.3 table extended, lowest to highest):

| Level | Operators |
| :--- | :--- |
| ASSIGN | `=` `+=` `-=` `*=` `/=` `%=` (right assoc; only as an expression statement) |
| TERNARY | `c ? a : b` |
| NULLISH | `??` |
| OR | `\|\|` |
| AND | `&&` |
| EQUALS | `==` `!=` |
| LESSGREATER | `<` `>` `<=` `>=` |
| SUM | `+` `-` |
| PRODUCT | `*` `/` `%` |
| PREFIX | `!x` `-x` |
| CALL | `f(x)`, `f<T>(x)` |
| MEMBER | `a.b`, `a[i]`, postfix non-null assertion `a!` |

Primary expressions: literals (int, float, string, `true`, `false`, `null`), identifiers, `( expr )`, array literal `[a, b]`, object literal `{ k: v, ... }` (only where an expected interface type is known), `new T()`, `new T<A>()`, `new T[]()`, `new global T()`, `new global T[]()`, `new Error(msg)` / `new Error(msg, status)`, and the arrow function, which is accepted **only** as the single argument of `db.transaction(...)`.

Explicit type arguments in calls (`first<User>(xs)`, `db.query<User>(...)`) are parsed speculatively: after an identifier or member expression, if `<` begins a well-formed type-argument list that is immediately followed by `(`, it is a generic call; otherwise the parser backtracks and treats `<` as less-than. This is why the lexer produces the whole token slice up front.

### 2.3 Types (§3.5 completed)

| TLang | C11 | Notes |
| :--- | :--- | :--- |
| `int32` / `int64` | `int32_t` / `int64_t` | Wrapping arithmetic (§3.5). Codegen emits wrap-safe helpers (unsigned arithmetic cast back), and builds also pass `-fwrapv`. |
| `float64` | `double` | |
| `bool` | `bool` | |
| `string` | `tlang_string` | `{const char* data; size_t len;}` slice, never owns data. |
| `void` | `void` | Return type only. |
| `interface T` | `tl_T*` | Reference semantics (§3.3). |
| `T[]` | `tlang_slice_<m>*` | Reference to a slice header `{items, len, cap}` in the arena (Appendix B item 2, see §2.6). |
| `T \| null` | see §2.5 | |
| `Error` | `tlang_error` (by value) | Builtin, see §2.8. |
| `Context` | `tlang_ctx*` | Builtin request context (§8.1). |
| `Transaction` | `tlang_tx*` | Builtin, only exists inside `db.transaction`. |

Rules:

* **Nominal typing.** Two interfaces with identical fields are different types. `type A = B` is a transparent alias. `type P = { ... }` declares a new nominal interface named `P`.
* **No implicit numeric conversion.** `int32`, `int64` and `float64` never mix. Conversions are explicit builtin calls: `int32(x)`, `int64(x)`, `float64(x)`. Integer literals are untyped constants that take the expected type (default `int64`); a constant that does not fit the target type is a compile error. Float literals default to `float64` and never convert to an integer type implicitly.
* **Integer division and modulo** go through helpers that throw `Error{status 500, "division by zero"}` on a zero divisor and return the wrapped result for `MIN / -1` instead of trapping.
* **Comparison:** `== !=` on numbers, bools, strings (byte equality, lowered to length check plus `memcmp`), and references (identity). `< > <= >=` on numbers only. `==`/`!=` between an optional value and `null` tests presence.
* **`+`** on two strings concatenates into the fiber arena. No implicit string conversion: use `x.toString()` on numbers and bools.
* **Logical operators** require `bool` operands and short-circuit. Conditions (`if`, `while`, `for`, `?:`) must be `bool`.

### 2.4 Interfaces, `new`, object literals

* `interface User { id: int64; name: string; email?: string; address: Address; tags: string[]; }` maps to a C struct with fields in declaration order.
* `new User()` allocates a zeroed struct in the fiber arena. **Non-optional reference fields are never null:** `new` also allocates every non-optional interface field (recursively) and every non-optional array field (an empty slice). Non-optional `string` fields start as `""`. Optional fields start as `null`. A cycle of non-optional interface fields (`interface A { b: B } interface B { a: A }`) is a compile error that asks the user to make one field optional.
* An interface variable without an initializer is a compile error (§3.3). Every `let`/`const` needs an initializer or a type whose zero value is defined (numbers, bool, string, optional types). Interface and array types require an initializer.
* `const` forbids reassigning the binding. Fields of a `const` object stay mutable (TypeScript semantics).
* Object literal `{ id: 1, name: "a" }` is allowed only where the expected type is a known interface (annotated `let`, argument, return, field, array element). It lowers to `new T()` plus field stores. Missing required fields are a compile error. Unknown keys are a compile error.

### 2.5 Optional types and `null` (§3.1, §3.5)

* `null` is only assignable to `T | null`. Optional interface fields (`name?: T`) have type `T | null`.
* `T | null` is allowed for any `T` except `void`, `Error`, `Context`, `Transaction`, and another optional. Representation:
  * interface and array: the pointer, `NULL` is null.
  * `string`: `data == NULL` is null. Widening a `string` to `string | null` goes through `tlang_str_some()`, which replaces a `NULL` data pointer by a static `""`, so an empty string never turns into null.
  * `int32`, `int64`, `float64`, `bool`: `tlang_opt_i32`, `tlang_opt_i64`, `tlang_opt_f64`, `tlang_opt_bool` (`{bool has; T v;}`).
* Using an optional value where `T` is required is a compile error unless it is narrowed. Ways to get a `T`:
  * **Narrowing:** inside `if (x != null) { ... }` (and the right side of `x != null && ...`), and after `if (x == null) { <block that always exits> }`, a local variable or parameter `x` has type `T`. Narrowing is cancelled for a variable that is assigned anywhere inside the narrowed region. Field paths (`req.name`) are not narrowed: copy to a local first.
  * `x ?? fallback` (fallback must be `T`).
  * `x!`, which throws `Error{500, "null value"}` when `x` is null.

### 2.6 Arrays (Appendix B item 2: decided)

`T[]` is a reference to a slice header. Elements of interface type are stored as pointers (`tl_User**`), consistent with the reference semantics of §3.3: taking `users[0]` and then pushing into `users` must not leave a stale copy behind. Elements of primitive and string type are stored by value. Cache-friendly struct-of-values layout is left for a later version.

* `new User[]()` creates an empty array in the arena; `new global User[]()` in the global heap. `[a, b]` creates an arena array (element type from context or from the first element).
* `xs.len` (int64), `xs[i]` and `xs[i] = v` (bounds checked, out of range throws `Error{500, "index out of range"}`), `xs.push(v)` (amortized doubling; arena arrays grow inside the arena, global arrays with `realloc`), `for (const x of xs)`.

### 2.7 Strings (§3.4)

Members: `s.len` (int64), `s.eq(t)`, `s.slice(a, b)` (like JavaScript `String.prototype.slice`: negative indices count from the end, then both indices are clamped to `[0, len]`; the end index is exclusive and the result is empty when the resulting start is greater than the end), `s.startsWith(t)`, `s.endsWith(t)`, `s.indexOf(t)` (int64, -1 if absent), `s.clone()` (arena copy), `s.clone_global()` (global heap copy), `s.toInt()` (int64, throws `Error{400, "invalid integer"}`). Also `n.toString()` on `int32`, `int64`, `float64`, `bool` (arena).

### 2.8 Errors (§3.6)

* Builtin value type `Error` retains `message: string` and `status: int32` (suggested HTTP status) and appends mutable `category: string` and `code: string` fields. Stable categories are `permission`, `invalid_input`, `not_found`, `limit`, `timeout`, `cancelled`, `unavailable`, `conflict`, `io`, `database`, `protocol`, and `internal`; a new Error defaults to `internal` / `internal`. Codes are stable machine-readable identifiers, not errno or provider values. The language model, generated C ABI, and runtime throw/catch path implement these fields; this does not imply that every proposed capability API is implemented.
* `throw new Error("msg")` (status 500), `throw new Error("msg", 404)`, and `throw "msg"` (sugar for `new Error("msg")`). `throw e;` rethrows a caught `Error`.
* `catch (err) { ... }` binds `err: Error`. `catch { ... }` without binding is allowed.
* Runtime errors use the same mechanism: `BadRequest` (status 400) from `ctx.paramInt`, `s.toInt`; out-of-memory (503, §6.2); database errors (500); pool timeout (503); client disconnect (499, logged only); division by zero, index out of range, null assertion (500).
* **Lowering:** the fiber carries `int err` plus a `tlang_error` payload. After any call that *may fail* (computed by the checker as a fixed point over the call graph: a function may fail if it contains `throw`, a may-fail runtime operation, or a call to a may-fail function outside a `try` that catches it), codegen emits `if (__fib->err) goto <nearest catch label or the function's failure exit>`. A function's failure exit returns the zero value of its return type with the error still set.
* An error still set when the handler returns produces a response with `err.status` and a generic reason-phrase body (the message is written to stderr, not to the client, so database errors do not leak, per OWASP). The arena is reset as usual. A guard (`@Use`) that fails propagates the same way.

### 2.9 Functions, receivers, decorators (§3.2, §10)

* `fn name(a: T, b: U): R { ... }`, return type defaults to `void`. Functions are top-level only. Recursion is allowed.
* Receiver functions `fn (u: User) greet(): string` are called as `u.greet()` and lowered to a direct call (§3.2). Receivers may be interfaces or the builtin `Context` (spec §13 defines `fn (ctx: Context) handleCreateUser()`). A method name may not collide with a field or builtin member of that type.
* `@Use(g1, g2)` is allowed only on functions whose receiver or first parameter is `Context`. Each guard must be a function `(ctx: Context): bool`. Codegen inserts the calls at the top of the function exactly as in §10, left to right, with `goto __guard_denied` on `false` and a 500 if the guard wrote no response. Guards that may fail get the usual error check.
* Generics: `fn first<T>(xs: T[]): T | null` and `interface Page<T> { items: T[]; total: int64; }`. Type parameters are unconstrained: inside a generic body a `T` value may only be assigned, passed, returned, stored, compared with `null` when optional, and put in arrays. Type arguments are inferred from arguments by unification, or given explicitly. Codegen monomorphizes: one C function or struct per distinct instantiation.

### 2.10 Entry points and globals (Appendix B item 3: decided)

* A program is a **server** if it defines `fn route_dispatcher(ctx: Context): void` (spec §13). The runtime serves HTTP and calls it for every request.
* A program is a **script** if it defines `fn main(): void`. It runs `main` once in a fiber and exits with status 0, or 1 if an error escaped `main`. Defining both, or neither, is a compile error.
* **Globals are per scheduler.** §7.3 says schedulers share no state, so top-level `let`/`const` variables live in a generated `tl_globals` struct, one instance per scheduler thread, reached through the fiber (`__fib->globals`). Each scheduler runs the global initializers in declaration order on its own thread, inside a startup fiber whose arena is never reset, before accepting connections. A global counter therefore counts per scheduler. This is the share-nothing model, and it needs no locks or atomics (which TCC lacks anyway).
* Long-lived allocation uses `new global T()`, `new global T[]()` and `s.clone_global()` (global heap, `malloc`, never freed). Plain `new` in a global initializer is a compile error (§6.3 rule 2).
* **Escape check (§6.3 rule 3).** Every reference-typed expression has a region: `static` (literals and slices of literals), `global` (`new global`, `clone_global`, global variables and anything read through them), or `request` (plain `new`, `clone`, concatenation, `toString`, values from `ctx`, JSON binding, DB results). Parameters, call results, and `catch` bindings count as `request`. Locals take the join of everything assigned to them. Assigning a `request` value to a global variable, or to a field or element of a `global`-region object (including `push`), is a compile error. Numbers and bools have no region. The analysis is flow-insensitive and does not track aliases through parameters; LANGUAGE.md documents that limit.

### 2.11 Builtins

* `console.debug/info/warn/error(message, fields?)`: emits one structured JSON Lines record to stdout; field data remains nested under `fields`.
* `Context` (`ctx`):
  * fields: `ctx.method`, `ctx.path`, `ctx.rawQuery`, `ctx.body` (all `string`).
  * `ctx.header(name): string` (case-insensitive, `""` if absent), `ctx.query(name): string` (lazily percent-decoded into the arena only when the raw value contains `%` or `+`, §8.3; `""` if absent), `ctx.param(name): string`, `ctx.paramInt(name): int64` (§8.2, throws `BadRequest`).
  * `ctx.match(method, pattern): bool` with string-literal arguments, for example `ctx.match("GET", "/users/:id")`. Codegen compiles the pattern into a static segment table. On success the dynamic segments are recorded in `ctx->params` (at most 8, more is a compile error).
  * `ctx.bindJson(obj): bool` for an interface value (§9.1).
  * Responses: `ctx.text(status, s)`, `ctx.json(status, v)` where `v` is an interface value or an array of serializable elements (§9.2), `ctx.setHeader(name, value)`. A second response on the same request is ignored and logged.
* `db` (§11): `db.execute(sql, args...): int64` (rows affected), `db.query<T>(sql, args...): T[]`, `db.queryOne<T>(sql, args...): T | null`, `db.transaction((tx) => { ... })` with the same three methods on `tx`. `sql` must be a string literal (prepared statement cache key, §11.1, and no SQL built from strings). Arguments: `int32`, `int64`, `float64`, `bool`, `string`, or optional versions of them (null becomes SQL `NULL`). Result columns map to interface fields by name; text-format results are parsed into the field type; SQL `NULL` is allowed only for optional fields; a required field with no matching column is a runtime error (500).
* Transactions (§11.4): lowered exactly as the spec shows, with `__tx_rollback_N`, `__tx_fail_N`, `__tx_end_N` labels. `return`, `break` or `continue` that would leave the block is a compile error. Nested `db.transaction` is a compile error. `tx` is not usable outside the block.

### 2.12 JSON (§9)

* Codegen emits, per interface (per instantiation for generics) that is reachable from `ctx.bindJson`, `ctx.json`, or `db.query*`, a parser `tlj_parse_<T>` and a writer `tlj_write_<T>` following the §9.1 shape: bitmask of seen fields, unknown keys skipped with `json_skip_value`, commas checked, trailing data rejected, required fields enforced, last duplicate key wins, nesting depth limit 32 (configurable at runtime).
* Field types: numbers (int range checked), `bool`, `string` (zero-copy slice if it has no escapes, decoded into the arena otherwise), nested interfaces (nested parser, depth + 1), arrays of any supported type, optional versions (JSON `null` or absence gives null; JSON `null` for a required field fails).
* Writer: escapes `"`, `\`, and bytes below 0x20 (§9.2); `int64` via a digit loop; `float64` with the shortest of `%.15g`/`%.17g` that round-trips; NaN and infinities become `null`; null optional fields are written as `null`.

---

## 3. Code generation contract

### 3.1 Calling convention

Every generated function takes the current fiber as a hidden first parameter: `R tl_f_name(tlang_fiber* __fib, ...)`. Methods: `R tl_m_<Recv>__<name>(tlang_fiber* __fib, <Recv C type> recv, ...)`. There is no TLS anywhere (§0).

### 3.2 Name mangling

User identifiers never reach C unprefixed, so they cannot collide with C keywords, libc macros, or runtime names (the runtime only uses the `tlang_`, `TLANG_`, `json_` prefixes).

| Entity | C name |
| :--- | :--- |
| interface `User` | `tl_User` (struct tag and typedef) |
| generic instance `Page<User>` | `tl_Page__User`; argument mangles: `i32 i64 f64 bool str`, `arr_X` for `X[]`, `opt_X` for `X \| null`, interface name otherwise |
| field `name` | `f_name` |
| function `logger` | `tl_f_logger` (generic instance: `tl_f_first__User`) |
| method `handleCreateUser` on `Context` | `tl_m_Context__handleCreateUser` |
| local / parameter `req` | `l_req` (shadowing in nested blocks gets a numeric suffix) |
| global `cache` | `__fib->globals->g_cache` |
| JSON parser / writer | `tlj_parse_User` / `tlj_write_User` |
| slice type of element mangle `m` | `tlang_slice_<m>` (runtime predefines `i32 i64 f64 bool str`; codegen emits the rest) |
| compiler temporaries and labels | `__t1`, `__guard_denied`, `__tlang_catch_1`, `__tx_rollback_1`, `__fail` |

### 3.3 Program skeleton

A generated translation unit contains, in order: `#include "tlang.h"`, slice/struct/optional typedefs (topologically ordered), `struct tl_globals`, prototypes, JSON functions, user functions, `tl__init_globals`, and the entry:

```c
static const tlang_program __tl_program = {
    .init_globals = tl__init_globals, .globals_size = sizeof(struct tl_globals),
    .dispatcher = tl_f_route_dispatcher,   /* or NULL */
    .main = NULL,                          /* or tl_f_main */
    .uses_db = true,
};
int main(int argc, char** argv) { return tlang_main(argc, argv, &__tl_program); }
```

### 3.4 Golden tests

`tests/golden/<feature>.tl` → `<feature>.c.golden`. Codegen output must be deterministic (stable ordering of maps, no timestamps or absolute paths). `go test ./tests -run Golden -update` rewrites goldens.

---

## 4. Runtime architecture (§6, §7, §8, §11)

### 4.1 Modules

| File | Content |
| :--- | :--- |
| `include/tlang.h` | Everything generated code calls: strings, slices, optionals, errors, arena allocation, fiber error helpers, console, Context API, JSON scanners/writers, DB API, `tlang_program`, `tlang_main`. Includes only `<stdint.h> <stddef.h> <stdbool.h> <string.h>`. |
| `src/arena.c` | §6.2 tiered arena, `tlang_alloc_zeroed` (OOM → request fails with 503, never returns NULL to user code). |
| `src/fiber.c`, `src/ctxswitch.c` | Fiber pool with `mmap` stacks and a guard page, context switch: assembly for x86_64 and AArch64 in release builds, `ucontext` under TCC or `-DTLANG_USE_UCONTEXT`. ASan builds annotate switches with `__sanitizer_start_switch_fiber`/`__sanitizer_finish_switch_fiber`. |
| `src/sched.c` | Per-core scheduler: run queue, epoll netpoller with `EPOLLONESHOT` (ADD first, MOD to re-arm), deadline min-heap, `EPOLLERR/EPOLLHUP/EPOLLRDHUP` and timeouts wake the fiber with an error (§7.2). Fiber pool exhaustion pauses `accept` (listener disarmed until a fiber frees). |
| `src/http.c` | §8.1 parser and limits table, keep-alive loop, pipelining, `Expect: 100-continue`, response writer (status line, `Content-Length`, `Content-Type`, `Connection`, cached `Date`), header and idle timeouts. |
| `src/router.c`, `src/query.c` | §8.2 segment matcher, `paramInt`, §8.3 query scanner and lazy percent-decoding. |
| `src/json.c` | §9 scanners, `json_skip_value` with depth limit, string decoding with `\uXXXX` and surrogate pairs, writer and number formatting. |
| `src/pg.c` | §11 async libpq: `PQconnectStart/Poll`, `PQsendQueryParams/Prepared` + `PQflush`, `PQconsumeInput/PQisBusy`, per-scheduler bounded pool with a FIFO wait queue and timeout, prepared statement cache per connection keyed by SQL hash (collisions resolved by comparing the SQL text), `statement_timeout`, cancel via `PQcancelStart/PQcancelPoll` when the client disconnects mid-query. Compiled out with `-DTLANG_NO_PG` when the program does not use `db`, so dev mode does not need libpq. |
| `src/main.c` | `tlang_main`: config from environment, signal handling (SIGINT/SIGTERM → graceful stop through an eventfd per scheduler, SIGPIPE ignored), scheduler threads, script mode. |

### 4.2 Configuration (environment variables)

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `TLANG_HOST` / `TLANG_PORT` | `0.0.0.0` / `8080` | listen address |
| `TLANG_THREADS` | number of online CPUs | schedulers (§7.3) |
| `TLANG_MAX_FIBERS` | 10000 | per scheduler (§7.1) |
| `TLANG_STACK_SIZE` | 262144 | compile-time macro, overridable |
| `TLANG_MAX_HEADER_BYTES` / `TLANG_MAX_HEADERS` / `TLANG_MAX_URI_BYTES` / `TLANG_MAX_BODY_BYTES` | 8192 / 32 / 2048 / 1048576 | §8.1 limits |
| `TLANG_HEADER_TIMEOUT_MS` / `TLANG_IDLE_TIMEOUT_MS` | 5000 / 60000 | §8.1 limits |
| `TLANG_JSON_MAX_DEPTH` | 32 | §9.1 |
| `TLANG_DATABASE_URL` (fallback `DATABASE_URL`) | unset | libpq conninfo. Never hardcoded. |
| `TLANG_DB_POOL_SIZE` / `TLANG_DB_POOL_TIMEOUT_MS` / `TLANG_DB_STATEMENT_TIMEOUT_MS` | 8 / 2000 / 5000 | §11.3 |

### 4.3 Connection lifecycle (one fiber per connection, §6.1)

1. Accept (`accept4`, non-blocking, `TCP_NODELAY`), take a fiber from the pool (or pause accepting), run the connection loop.
2. Read until the header terminator is in the per-connection buffer (`TLANG_MAX_HEADER_BYTES`, plus room for the URI), enforcing the header timeout from the first byte of each request.
3. Parse (zero-copy slices into the buffer), enforce limits, read the body (into the buffer if it fits, otherwise into the arena), handle `Expect: 100-continue`, reject `Transfer-Encoding: chunked` with 501.
4. Call the dispatcher, then finish: if no response was written, send 404 for a dispatcher that returned normally and the error status otherwise.
5. Write the response, move pipelined leftover bytes to the front of the buffer, `arena_reset`, loop (idle timeout applies while waiting for the next request). Close on `Connection: close`, HTTP/1.0 without keep-alive, any protocol error, or timeout.

---

## 5. Testing and verification (§12)

* `go test ./...` on any OS: lexer, parser (including error recovery), checker, codegen goldens, fuzz seeds (`FuzzLexer`, `FuzzParser`).
* Inside the container (`scripts/dev.ps1 <cmd>` from Windows): `runtime/tests` C unit tests built with GCC, Clang, TCC, and Clang ASan+UBSan; e2e tests that compile `examples/*.ts`, build with Clang (release and ASan/UBSan) and TCC (`tlang run`), start the binary, and drive it over HTTP, against a throwaway PostgreSQL cluster created with `initdb` in a temp dir (run as the unprivileged `tlang` user). Scenarios include rollback, an empty pool, and a client disconnect.
* Benchmark (`tests/benchmark_test.go`, opt-in with `TLANG_BENCH=1`): open-loop constant-rate generator over persistent connections with coordinated-omission correction, an HDR-style histogram, warm-up, and the §12 gate (0 failed requests, p99 ≤ 2.5 ms). It records hardware, connection count, rate, and payload size with the result.
* Fuzzing: Go native fuzzing for lexer and parser; libFuzzer harnesses (`runtime/fuzz/`) for the HTTP parser and the JSON scanners, built with `clang -fsanitize=fuzzer,address`.

---

## 6. Deviations from the spec text

| Spec | v1 | Reason |
| :--- | :--- | :--- |
| §10, §11.4 examples call `logger(ctx)`, `Context_text(ctx, ...)`, use `fiber->err` | Calls carry `__fib` and use prefixed names (`tl_f_logger(__fib, l_ctx)`) | No TLS under TCC; prefixes avoid collisions. The control-flow shape (guards, `goto` labels) is unchanged. |
| §8.1 `ctx->query` field | TLang exposes the raw query as `ctx.rawQuery` | `ctx.query(name)` is a method; one name cannot be both. |
| §2 "symbols resolved by hash ID" | Go maps keyed by name | Same average O(1). |
| Appendix B 1: role of `&` | Still reserved: the lexer produces `AMPERSAND`, the parser rejects it with a clear message | Keeps the option open. |
| Appendix B 4: `@After` | *(deferred)* | Not needed for the spec's v1 scope. |
| Appendix B 5: chunked bodies, HTTP/2 | Out of scope: chunked request bodies get 501 | As §8.1 says. |
| Appendix B 6: work-stealing | *(deferred)*; arenas and pools stay per fiber and per scheduler | As §7.3 says. |
