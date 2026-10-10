# TLang Runtime (C): API and Module Map

This document describes the C runtime of TLang v1: how its API is organized, which source file implements each declaration, the rules every module follows, and how generated code uses the API. The headers are authoritative for signatures and for the contract of each function (semantics, lifetime, failure); this document adds the module map and the cross-cutting rules. Approved but unimplemented standard API contracts are documented in `docs/DESIGN-modules.md` §12.6 and must not be read as shipped runtime APIs. "§x" points into `TLang_Technical_Specification_1.2.1.md`, "DESIGN §x" into `docs/DESIGN.md`.

---

## 1. Files

| Path | Role |
| :--- | :--- |
| `runtime/include/tlang.h` | Public API. The only header generated code includes. Includes only `<stdint.h> <stddef.h> <stdbool.h> <string.h>`. |
| `runtime/src/tlang_internal.h` | Private API shared by the runtime modules. Never included by generated code. |
| `runtime/src/<module>.c` | One file per module (section 4). Each one includes `tlang_internal.h` first. |
| `runtime/tests/test_<name>.c` | C unit tests, one executable per file. `test_pg_*.c` need a database. |
| `runtime/Makefile` | Builds `build/<variant>/libtlangrt.a` and the tests (section 7). |
| `runtime/embed.go` | Go package `runtime`: embeds `include/` and `src/` for the CLI (section 7.3). |

---

## 2. Conventions

These rules come from the comment at the top of `tlang.h` and apply to every declaration in both headers.

1. **Fiber parameter.** A function whose first parameter is `tlang_fiber* fib` may allocate in the fiber arena, may suspend the fiber on I/O, or may fail. A function without it never fails, never suspends and never aborts. Comments mark side-effect-free functions "Pure".
2. **May fail.** Failure sets `fib->err = 1` and `fib->error = {status, message, category, code}` and returns the zero value of the return type. `category` and `code` are appended after the original `status`/`message` fields in the Error ABI. Generated code checks `if (__fib->err) goto <label>;` right after the call. "Never fails" means the function never touches `fib->err`.
3. **Out of memory is an abort, not an Error.** Allocation never returns NULL and never sets `err`. On OOM (or a size computation that overflows) `tlang_alloc_failed` calls `tlang_request_abort(f, 503, ...)`, which longjmps to the connection loop. The loop runs the cleanup hooks (pooled DB connections are released), answers 503 if nothing was sent, resets the arena and closes the connection. Outside a request (global initialisation, script mode) the process logs and exits with status 1. Only the raw primitives `arena_grow`/`arena_alloc` and `json_scan_string` report allocation failure to their caller.
4. **Lifetimes.** *static*: process lifetime. *request*: until the dispatcher returns and the arena is reset (arena memory, request-buffer slices). *global*: heap memory that is never freed.
5. **Strings.** `{NULL, 0}` is accepted everywhere as `""`. No runtime function returns `data == NULL`; that value is the null of `string | null`.
6. **Threads.** No thread-local storage and no atomics anywhere (TCC 0.9.27 has neither). Every function receives the fiber or scheduler it works on. All state belongs to one scheduler thread; the only cross-thread call is `tlang_sched_request_stop`.
7. **Abort safety.** Inside a request any allocation may longjmp out. Runtime code keeps its data structures consistent at every allocation point and never holds a resource across an allocation unless a cleanup hook releases it.
8. **Reserved names.** Runtime symbols use the prefixes `tlang_`, `TLANG_`, `json_`, plus the spec names `ArenaChunk`, `MemoryArena`, `ARENA_*`, `arena_*`, `TxContext`, and inside the runtime `Fiber`, `FiberState`, `FiberContext`, `FIBER_*`. Generated code uses `tl_`, `tlj_`, `l_`, `f_`, `g_` and `__` names (DESIGN §3.2) and completes `struct tl_globals`.
9. **Secrets.** The database URL is never logged, not even in configuration errors. Request bodies are never logged.

The Error category/code fields are implemented in the language model, generated
C layout, and runtime throw/take/rethrow path. In contrast, the approved project
grants document is currently parsed and validated only by the Go `project`
package; the C runtime does not load it or enforce its capabilities. The
approved `env.get` and structured JSONL console APIs in `docs/DESIGN-modules.md`
§12.6 are not implemented runtime APIs. Existing runtime console functions emit
plain text, not those JSONL records.

---

## 3. API organization

### 3.1 Public API (`tlang.h`)

| § of tlang.h | Contents |
| :--- | :--- |
| 1 Forward declarations | `tlang_fiber`, `tlang_ctx`, `tlang_tx`, incomplete `struct tl_globals` |
| 2 Strings | `tlang_string`, `TLANG_STR`, `TLANG_STR_INIT`, `TLANG_STR_NULL`, `TLANG_STR_END` |
| 3 Errors (types) | `tlang_error` (`status`, `message`, appended `category`, `code`), `TLANG_STATUS_*`, `TLANG_MSG_*`, `TLANG_ERROR_*` |
| 4 Arena | `ArenaChunk`, `MemoryArena`, `ARENA_CHUNK_SIZE` (128 KiB), `ARENA_ALIGNMENT` (8), `arena_grow`, `arena_alloc` (inline), `arena_reset` |
| 5 Fiber | `struct tlang_fiber { int err; tlang_error error; struct tl_globals* globals; MemoryArena* arena; }` |
| 6 Throw / catch | `tlang_throw`, `tlang_throw_typed`, `tlang_throw_value`, `tlang_take_error`, `tlang_throw_fmt_typed`, `tlang_clear_error`, `tlang_error_make[_typed]`, `TLANG_THROW_LIT`, `tlang_throw_null/_index/_div_zero` |
| 7 Allocation | `tlang_alloc_failed`, `tlang_alloc_raw`, `tlang_alloc_zeroed`, `tlang_alloc_global_zeroed` |
| 8 String operations | `tlang_str_eq/_starts_with/_ends_with/_slice/_index_of/_concat/_clone/_clone_global/_to_int/_some`, `tlang_{i32,i64,f64,bool}_to_string` |
| 9 Numbers | `tlang_{add,sub,mul,neg}_{i32,i64}`, `tlang_{div,mod}_{i32,i64}`, `tlang_mod_f64`, `tlang_i64_to_i32`, `tlang_f64_to_i32`, `tlang_f64_to_i64` |
| 10 Optionals | `tlang_opt_{i32,i64,f64,bool}`, `TLANG_SOME`, `TLANG_NONE`, `tlang_unwrap_{i32,i64,f64,bool,str,ptr}` |
| 11 Arrays | `TLANG_SLICE_DEFINE`, `tlang_slice_{i32,i64,f64,bool,str}`, `tlang_slice_new/_grow/_oob/_at`, `tlang_index_ok`, `TLANG_SLICE_NEW`, `TLANG_SLICE_NEW_GLOBAL`, `TLANG_SLICE_RESERVE`, `TLANG_SLICE_PUSH`, `TLANG_SLICE_AT` |
| 12 Tagged values | `TLANG_KIND_*`, `tlang_value`, `TLANG_VAL_*`, `tlang_val_opt_*` |
| 13 Console | `tlang_console_log`, `tlang_console_info`, `tlang_console_error` (plain-text output; structured JSONL methods are not implemented) |
| 14 JSON | `json_skip_ws`, `json_scan_{key,int64,int32,float64,bool,null,string}`, `json_skip_value`, `json_max_depth`, `tlang_buf` + `tlang_buf_{init,grow,reserve,put,putc,put_str,string}`, `TLANG_BUF_PUT_LIT`, `json_write_{str,i64,i32,f64,bool,null}` |
| 15 Context and routing | `TLANG_MAX_PARAMS` (8), `TLANG_CTX_MAX_HEADERS` (32), `tlang_route_param`, `tlang_http_header`, `tlang_resp_header`, `struct tlang_ctx`, `tlang_ctx_{header,query,param,param_int,text,json,send,set_header,match}`, `TLANG_SEG_*`, `tlang_route_seg`, `tlang_route` |
| 16 Database | `tlang_pg_param` (= `tlang_value`), `TLANG_PG_*`, `tlang_field_desc`, `tlang_type_desc`, `struct tlang_tx`, `TxContext`, `TLANG_TX_*`, `tlang_db_{execute,query,query_one}`, `tlang_tx_{begin,exec,query,query_one,commit,rollback}` |
| 17 Program entry | `tlang_program`, `tlang_main` |

### 3.2 Internal API (`tlang_internal.h`)

| § of tlang_internal.h | Contents |
| :--- | :--- |
| 1 Build configuration | `TLANG_USE_UCONTEXT` (auto under TCC and non-x86_64/aarch64), `TLANG_ASAN`, `TLANG_PRINTF`, `TLANG_STACK_SIZE` (256 KiB), `TLANG_MAX_FIBERS` (10000) |
| 2 Configuration | `tlang_config` (every DESIGN §4.2 variable + `TLANG_SHUTDOWN_TIMEOUT_MS`), `tlang_config_defaults`, `tlang_config_load` |
| 3 Arena lifecycle | `tlang_arena_init`, `tlang_arena_destroy` |
| 4 Context switch | `FiberContext`, `tlang_fctx_init`, `tlang_fctx_init_thread`, `tlang_fctx_switch`, `tlang_fctx_exit` |
| 5 Fibers | `FiberState` (`FIBER_DEAD/READY/RUNNING/WAITING_IO`), `tlang_cleanup`, `tlang_waitq`, `struct Fiber` (embeds `tlang_fiber pub` first, static assert), `tlang_fiber_of`, `tlang_fiber_acquire/_release`, `tlang_spawn`, `tlang_fiber_pool_destroy`, `tlang_cleanup_push/_pop/_run_all`, `TLANG_ABORT_POINT`, `tlang_request_abort` |
| 6 Scheduler and waits | `TLANG_WAIT_*`, `TLANG_IO_ERR`, `tlang_fd_slot`, `struct tlang_sched`, `tlang_sched_{init,run,destroy,request_stop,ready,resume_accept,pause_accept}`, `tlang_now_ns`, `tlang_wait_fd`, `tlang_wait_fd2`, `tlang_fd_forget`, `tlang_sleep_until`, `tlang_yield`, `tlang_waitq_{init,wait,wake_one,cancel_all}` |
| 7 Sockets | `tlang_net_{listen,local_port,accept_ready,read,write_all,writev_all,close}` |
| 8 HTTP connections | `tlang_http_conn_main` |
| 9 PostgreSQL pool | `tlang_pg_pool_create`, `tlang_pg_pool_destroy` |
| 10 Errors and logging | `tlang_throw_fmt`, `tlang_throw_fmt_typed`, `tlang_log_error`, `tlang_log_warn`, `tlang_log_write` |
| 11 Shared helpers | `TLANG_FMT_I64_MAX`, `TLANG_FMT_F64_MAX`, `tlang_fmt_i64`, `tlang_fmt_f64`, `tlang_parse_i64`, `tlang_ascii_ieq`, `tlang_str_cstr`, `TLANG_SLICE_OFF_*`, `TLANG_SLICE_HDR_SIZE`, `tlang_slice_hdr_init` |
| 12 JSON configuration | `json_set_max_depth` |

### 3.3 The fiber "first member" pattern

The public `struct tlang_fiber` holds exactly the fields generated code reads (`err`, `error`, `globals`, `arena`). The private `struct Fiber` embeds it as its first member, `tlang_fiber pub;`, and `tlang_internal.h` asserts `offsetof(Fiber, pub) == 0`. C guarantees that a pointer to a struct, suitably converted, points to its first member and back (C11 6.7.2.1p15), so `tlang_fiber_of(fib)` is a cast. Every `tlang_fiber*` the runtime hands out is `&f->pub` of some `Fiber`. Generated code never creates a `tlang_fiber`.

The spec §7.1 field `int err` is `pub.err` (payload `pub.error`), so there is one source of truth. The spec field `MemoryArena arena` stays in `Fiber`; `pub.arena` points at it, except during global initialisation where it points at the scheduler's never-reset `globals_arena`. The entry function is `void (*fn)(Fiber* self, void* arg)` instead of `void (*fn)(void*)`: without TLS the entry needs its own fiber.

---

## 4. Module ownership map

Every extern function declared in either header is implemented in exactly one file, listed below (108 functions: 55 public, 53 internal). Types, macros and `static inline` functions are defined in the headers themselves; they are listed at the end of this section.

| File | Implements |
| :--- | :--- |
| `src/arena.c` | `arena_grow`, `arena_reset`, `tlang_alloc_failed`, `tlang_alloc_global_zeroed`, `tlang_arena_init`, `tlang_arena_destroy` |
| `src/errors.c` | `tlang_throw`, `tlang_throw_typed`, `tlang_throw_value`, `tlang_take_error`, `tlang_throw_fmt`, `tlang_throw_fmt_typed` |
| `src/strings.c` | `tlang_str_index_of`, `tlang_str_concat`, `tlang_str_clone`, `tlang_str_clone_global`, `tlang_str_to_int`, `tlang_i32_to_string`, `tlang_i64_to_string`, `tlang_f64_to_string`, `tlang_mod_f64`, `tlang_buf_init`, `tlang_buf_grow`, `tlang_fmt_i64`, `tlang_fmt_f64`, `tlang_parse_i64`, `tlang_ascii_ieq` |
| `src/slices.c` | `tlang_slice_new`, `tlang_slice_grow`, `tlang_slice_oob`, `tlang_slice_hdr_init` |
| `src/console.c` | `tlang_console_log`, `tlang_console_info`, `tlang_console_error`, `tlang_log_error`, `tlang_log_warn`, `tlang_log_write` |
| `src/json.c` | `json_scan_key`, `json_scan_int64`, `json_scan_int32`, `json_scan_float64`, `json_scan_bool`, `json_scan_null`, `json_scan_string`, `json_skip_value`, `json_max_depth`, `json_set_max_depth`, `json_write_str`, `json_write_i64`, `json_write_i32`, `json_write_f64` |
| `src/ctxswitch.c` | `tlang_fctx_init`, `tlang_fctx_init_thread`, `tlang_fctx_switch`, `tlang_fctx_exit` |
| `src/fiber.c` | `tlang_fiber_acquire`, `tlang_fiber_release`, `tlang_spawn`, `tlang_fiber_pool_destroy`, `tlang_cleanup_push`, `tlang_cleanup_pop`, `tlang_cleanup_run_all`, `tlang_request_abort` |
| `src/sched.c` | `tlang_sched_init`, `tlang_sched_run`, `tlang_sched_destroy`, `tlang_sched_request_stop`, `tlang_sched_ready`, `tlang_sched_resume_accept`, `tlang_sched_pause_accept`, `tlang_now_ns`, `tlang_wait_fd`, `tlang_wait_fd2`, `tlang_fd_forget`, `tlang_sleep_until`, `tlang_yield`, `tlang_waitq_init`, `tlang_waitq_wait`, `tlang_waitq_wake_one`, `tlang_waitq_cancel_all` |
| `src/net.c` | `tlang_net_listen`, `tlang_net_local_port`, `tlang_net_accept_ready`, `tlang_net_read`, `tlang_net_write_all`, `tlang_net_writev_all`, `tlang_net_close` |
| `src/http.c` | `tlang_ctx_header`, `tlang_ctx_text`, `tlang_ctx_json`, `tlang_ctx_send`, `tlang_ctx_set_header`, `tlang_http_conn_main` |
| `src/router.c` | `tlang_ctx_match`, `tlang_ctx_param`, `tlang_ctx_param_int` |
| `src/query.c` | `tlang_ctx_query` |
| `src/pg.c` | `tlang_db_execute`, `tlang_db_query`, `tlang_db_query_one`, `tlang_tx_begin`, `tlang_tx_commit`, `tlang_tx_rollback`, `tlang_pg_pool_create`, `tlang_pg_pool_destroy` |
| `src/config.c` | `tlang_config_defaults`, `tlang_config_load` |
| `src/main.c` | `tlang_main` |

**Header-defined (no .c owner).** `tlang.h`: `arena_alloc`, `tlang_clear_error`, `tlang_error_make`, `tlang_error_make_typed`, `tlang_throw_null`, `tlang_throw_index`, `tlang_throw_div_zero`, `tlang_alloc_raw`, `tlang_alloc_zeroed`, `tlang_str_eq`, `tlang_str_starts_with`, `tlang_str_ends_with`, `tlang_str_slice`, `tlang_str_some`, `tlang_bool_to_string`, `tlang_{add,sub,mul,neg}_{i32,i64}`, `tlang_{div,mod}_{i32,i64}`, `tlang_i64_to_i32`, `tlang_f64_to_i32`, `tlang_f64_to_i64`, `tlang_unwrap_{i32,i64,f64,bool,str,ptr}`, `tlang_index_ok`, `tlang_slice_at`, `tlang_val_opt_{i32,i64,f64,bool,str}`, `json_skip_ws`, `tlang_buf_reserve`, `tlang_buf_put`, `tlang_buf_putc`, `tlang_buf_put_str`, `tlang_buf_string`, `json_write_bool`, `json_write_null`, `tlang_tx_exec`, `tlang_tx_query`, `tlang_tx_query_one`, and every type and macro. `tlang_internal.h`: `tlang_fiber_of`, `tlang_str_cstr`, `TLANG_ABORT_POINT`, and every type and macro. Changes to either header go through the header owner, because every module and the code generator depend on them.

**Suggested split between implementers** (the modules of a group call each other most):

| Group | Files |
| :--- | :--- |
| Core runtime: arena, fibers, scheduler, process | `arena.c`, `ctxswitch.c`, `fiber.c`, `sched.c`, `config.c`, `main.c` |
| Text: strings, slices, errors, console, JSON | `strings.c`, `slices.c`, `errors.c`, `console.c`, `json.c` |
| HTTP | `net.c`, `http.c`, `router.c`, `query.c` |
| PostgreSQL | `pg.c` |

Dependencies across groups: everything uses `errors.c`, `console.c` (logging) and `arena.c` (allocation); HTTP and PostgreSQL need `sched.c`/`fiber.c` for waits; `json.c` and `console.c` use `tlang_fmt_*` from `strings.c`; `pg.c` uses `tlang_slice_new`/`tlang_slice_hdr_init` and `tlang_parse_i64`. Until a dependency exists, a module's unit test can define the few functions it needs in the test file itself (the library is static, so a definition in the test takes precedence only if the library does not provide one; remove the stand-in once the real module lands).

Unit test files are named after their module (`tests/test_arena.c`, `tests/test_json.c`, ...). Database tests are `tests/test_pg_*.c`.

---

## 5. Code generation guide

### 5.1 Program skeleton (DESIGN §3.3)

```c
#include "tlang.h"
typedef struct tl_User tl_User;                    /* all interface typedefs first */
TLANG_SLICE_DEFINE(User, tl_User*)                  /* no trailing ';' (the expansion has one) */
struct tl_User { int64_t f_id; tlang_string f_name; tlang_slice_User* f_friends; };
struct tl_globals { int64_t g_hits; };              /* omit entirely when there are no globals */
/* prototypes, route tables, row descriptors, JSON functions, user functions */
static void tl__init_globals(tlang_fiber* __fib, void* __globals) { __fib->globals->g_hits = 0; }
static const tlang_program __tl_program = {
    .init_globals = tl__init_globals, .globals_size = sizeof(struct tl_globals),
    .dispatcher = tl_f_route_dispatcher, .main = NULL, .uses_db = false,
};
int main(int argc, char** argv) { return tlang_main(argc, argv, &__tl_program); }
```

`__fib->globals` has type `struct tl_globals*`, so `__fib->globals->g_name` compiles as soon as the generated code completes the struct. `init_globals` takes `void*` (it equals `__fib->globals`). Without globals: `.init_globals = NULL, .globals_size = 0`.

### 5.2 Lowering table

| TLang | C |
| :--- | :--- |
| `"abc"` | `TLANG_STR("abc")`; in static tables `TLANG_STR_INIT("abc")` |
| `a + b`, `a - b`, `a * b`, `-a` (int32/int64) | `tlang_add_i64(a, b)`, `tlang_sub_*`, `tlang_mul_*`, `tlang_neg_*` |
| `a / b`, `a % b` (int32/int64) | `tlang_div_i64(__fib, a, b)`, `tlang_mod_i64(__fib, a, b)`, may fail (500) |
| `a % b` (float64) | `tlang_mod_f64(a, b)`; other float64 operators are plain C |
| `int32(x)` from int64 / float64 | `tlang_i64_to_i32(x)` / `tlang_f64_to_i32(x)` |
| `int64(x)` from float64 | `tlang_f64_to_i64(x)`; `int64(i32)`, `float64(i)` are C casts |
| `s == t`, `s.eq(t)` | `tlang_str_eq(s, t)` |
| `s.len` | `(int64_t)s.len` |
| `s.slice(a, b)` / `s.slice(a)` | `tlang_str_slice(s, a, b)` / `tlang_str_slice(s, a, TLANG_STR_END)` |
| `s.startsWith(t)`, `s.endsWith(t)`, `s.indexOf(t)` | `tlang_str_starts_with`, `tlang_str_ends_with`, `tlang_str_index_of` |
| `a + b` (strings) | `tlang_str_concat(__fib, a, b)` |
| `s.clone()`, `s.clone_global()` | `tlang_str_clone(__fib, s)`, `tlang_str_clone_global(__fib, s)` |
| `s.toInt()` | `tlang_str_to_int(__fib, s)`, may fail (400) |
| `n.toString()` | `tlang_i32_to_string`, `tlang_i64_to_string`, `tlang_f64_to_string`, `tlang_bool_to_string` (all take `__fib`) |
| widen `string` to `string \| null` | `tlang_str_some(s)` |
| widen `int64` to `int64 \| null` | `TLANG_SOME(i64, x)` (likewise `i32`, `f64`, `bool`) |
| typed `null` | `TLANG_NONE(i64)`, `TLANG_STR_NULL`, `NULL` for interfaces and arrays |
| `x == null` | `!x.has`, `x.data == NULL`, `x == NULL` |
| `x!` | `tlang_unwrap_i64(__fib, x)` etc., `(tl_User*)tlang_unwrap_ptr(__fib, x)`, may fail (500) |
| `x ?? y` | `(x.has ? x.v : y)` with x in a temporary; strings test `.data != NULL` |
| `new T()` | `(tl_T*)tlang_alloc_zeroed(__fib, sizeof(tl_T))`, then initialise non-optional nested fields, arrays and strings (DESIGN §2.4) |
| `new global T()` | `tlang_alloc_global_zeroed(__fib, sizeof(tl_T))` |
| `new T[]()`, `new global T[]()` | `TLANG_SLICE_NEW(__fib, m)`, `TLANG_SLICE_NEW_GLOBAL(__fib, m)` |
| `[a, b]` | `TLANG_SLICE_NEW`, then `TLANG_SLICE_RESERVE(__fib, s, 2)`, then two `TLANG_SLICE_PUSH` |
| `xs.push(v)` | `TLANG_SLICE_PUSH(__fib, l_xs, v)` (statement; v in a temporary if it has calls) |
| `xs[i]`, `xs[i] = v` | `TLANG_SLICE_AT(__fib, l_xs, i, T)` (lvalue), may fail (500); or `tlang_index_ok` then `l_xs->items[i]` |
| `xs.len` | `l_xs->len` |
| `for (const x of xs)` | `for (int64_t __i = 0; __i < l_xs->len; __i++) { T l_x = l_xs->items[__i]; ... }` (re-read `items` each time: the body may push) |
| `throw new Error(m, s)`, `throw "m"` | `tlang_throw_typed(__fib, s, m, TLANG_STR("internal"), TLANG_STR("internal"))`, then `goto` |
| `throw e` | `tlang_throw_value(__fib, l_e)` |
| `new Error(m)`, `new Error(m, s)` | `tlang_error_make_typed(m, 500, TLANG_STR("internal"), TLANG_STR("internal"))`, `tlang_error_make_typed(m, s, TLANG_STR("internal"), TLANG_STR("internal"))` |
| `catch (e) { }` / `catch { }` | `tlang_error l_e = tlang_take_error(__fib);` / `tlang_clear_error(__fib);` at the catch label |
| `e.message`, `e.status`, `e.category`, `e.code` | matching `l_e` fields (all mutable; `category` and `code` are appended to the prior Error ABI) |
| `console.info(message, fields)` | `tlang_console_json(__fib, TLANG_LOG_INFO, message, fields)`; all levels emit one serialized JSON Lines record |
| `ctx.method`, `ctx.path`, `ctx.rawQuery`, `ctx.body` | `l_ctx->method`, `l_ctx->path`, `l_ctx->query`, `l_ctx->body` |
| `ctx.header(n)`, `ctx.param(n)` | `tlang_ctx_header(l_ctx, n)`, `tlang_ctx_param(l_ctx, n)` |
| `ctx.query(n)` | `tlang_ctx_query(__fib, l_ctx, n)` |
| `ctx.paramInt(n)` | `tlang_ctx_param_int(__fib, l_ctx, n)`, may fail (400) |
| `ctx.match("GET", "/users/:id")` | `tlang_ctx_match(l_ctx, &__tl_rN)` with a static `tlang_route` (see `tlang.h` §15) |
| `ctx.text(s, b)`, `ctx.json(s, v)` | `tlang_ctx_text(__fib, l_ctx, s, b)`; JSON: `tlang_buf __b; tlang_buf_init(__fib, &__b, 256); tlj_write_T(__fib, &__b, v); tlang_ctx_json(__fib, l_ctx, s, tlang_buf_string(&__b));` |
| `ctx.setHeader(n, v)` | `tlang_ctx_set_header(__fib, l_ctx, n, v)`, may fail (500) |
| `ctx.bindJson(obj)` | `p = l_ctx->body.data; end = p + l_ctx->body.len; ok = tlj_parse_T(__fib, &p, end, obj, 0) && json_skip_ws(p, end) == end;` (`body.data` is never NULL) |
| `db.execute(sql, a, b)` | `tlang_db_execute(__fib, NULL, TLANG_STR(sql), (tlang_pg_param[2]){ TLANG_PG_I64(a), TLANG_PG_STR(b) }, 2)`; no args: `NULL, 0` |
| `db.query<T>(...)`, `db.queryOne<T>(...)` | `(tlang_slice_T*)tlang_db_query(..., &__tl_td_T)`, `(tl_T*)tlang_db_query_one(..., &__tl_td_T)` |
| `db.transaction((tx) => { ... })` | spec §11.4 shape with `TxContext __tx_N`, `tlang_tx_begin/commit/rollback(__fib, &__tx_N)`, `tlang_tx_exec/query/query_one(__fib, l_tx, ...)` |
| `@Use(g)` | spec §10 shape; the guard reads `l_ctx->response_sent` |

### 5.3 Calls that may fail

The checker's may-fail analysis (DESIGN §2.8) must treat exactly these runtime calls as failing: `tlang_div_*`, `tlang_mod_i32`, `tlang_mod_i64`, `tlang_unwrap_*`, `tlang_index_ok`, `tlang_slice_at` / `TLANG_SLICE_AT`, `tlang_str_to_int`, `tlang_ctx_param_int`, `tlang_ctx_set_header`, `tlang_db_*`, `tlang_tx_*` (begin and commit report it through their `bool` too), and `tlang_throw*`. Allocation never sets `err` (OOM aborts the request), so allocating builtins need no check of their own. A check after a call that cannot fail is harmless, only redundant.

### 5.4 Rules for emitted C

* **String literals.** Escape `"` and `\`. Write every byte below 0x20, every byte from 0x7F up, and every `?` that follows another `?` as a three-digit octal escape (`\303\251`, `\077`). Never use `\x`: it swallows the hex digits that follow. `-std=c11` turns `??!` and the other trigraphs into single characters in gcc and clang.
* **Arrays of non-constant structs need an explicit size** (`(tlang_value[2]){...}`, `tlang_pg_param p[3] = {...}`): TCC 0.9.27 rejects the unsized form with "index too large". Constant tables (route segments, field descriptors) may be unsized, but sizing them costs nothing.
* **No GNU statement expressions, no TLS, no atomics.** Use temporaries.
* **Evaluate once.** `TLANG_SLICE_PUSH`, `TLANG_SLICE_RESERVE` and `TLANG_SLICE_AT` evaluate the slice expression more than once; pass a local or a side-effect-free path. A pushed or assigned value that contains calls goes into a temporary first (the element address and the value are unsequenced in C).
* **Slice types.** Emit `TLANG_SLICE_DEFINE(m, T)` for every element type except the predefined `i32 i64 f64 bool str`, after the interface typedefs. To break a cycle, refer to `struct tlang_slice_<m>` by tag before its define.
* **Row descriptors.** One `static const tlang_type_desc` per DB-mapped interface (per generic instantiation), listing its number, bool and string fields (optional variants included) with `offsetof`. Fields of other types are not listed and keep what `new_row` (or zeroing) gave them, so a DB-mapped interface may only have other fields if they are optional, unless codegen supplies `new_row`.
* **SQL** must come from a string literal (`TLANG_STR`): the runtime relies on the terminating NUL and caches prepared statements by it.
* **Unused parameters.** Generated functions may ignore `__fib` or `l_ctx`; compile generated code without `-Werror=unused-parameter`, or emit `(void)x;`.

---

## 6. Implementation notes per module

These requirements complement the header comments.

**arena.c.** `arena_grow` appends after `a->current` (always the last chunk) and checks `n > SIZE_MAX - sizeof(ArenaChunk)`. `tlang_alloc_failed` finds the private fiber with `tlang_fiber_of`; it calls `tlang_request_abort(f, 503, "out of memory")` when `f->abort_armed`, otherwise logs and `exit(1)`. Never call `arena_reset` on an arena other code may still use.

**errors.c.** `tlang_throw` stores `""` for a NULL message. `tlang_throw_fmt` formats with `vsnprintf` into the arena (two passes or a stack buffer first).

**strings.c.** `tlang_str_index_of` may use `memmem` (`_GNU_SOURCE`). `tlang_fmt_f64` tries `%.15g`, `%.16g`, `%.17g` and keeps the first that `strtod` reads back exactly. `tlang_buf_grow` may extend in place when the buffer is the most recent allocation of `fib->arena->current` and the chunk has room. `tlang_parse_i64` must reject overflow without signed overflow (accumulate in `uint64_t`).

**slices.c.** Header and element writes go through scalar types (`tlang_slice_hdr_init` copies each field with `memcpy` at `TLANG_SLICE_OFF_*`), never through a "generic slice" struct type: generated code reads headers through its own `tlang_slice_<m>` types, and struct-path TBAA could reorder accesses made through a different struct type after LTO. Growth policy and overflow handling are in the `tlang_slice_grow` comment; overflow calls `tlang_alloc_failed`.

**console.c.** Format into a stack buffer (for example 1 KiB), fall back to one `malloc` for longer lines, free it after the write. `tlang_log_*` truncate at 4 KiB and never allocate.

**json.c.** `json_scan_float64` copies the number token to a NUL-terminated buffer before `strtod` (input is not NUL-terminated; use a stack buffer and a temporary `malloc` for very long tokens, `false` if that fails). `json_skip_value` recursion is bounded by the depth limit (default 32, at most 256). The writer formats `int64_t` with a digit loop that handles `INT64_MIN`.

**ctxswitch.c.** Assembly switch for x86_64 and aarch64 (callee-saved registers, FP control words, stack pointer). The ucontext variant passes the 64-bit argument to `makecontext` as two `int` halves. Under `TLANG_ASAN`, annotate every switch with `__sanitizer_start_switch_fiber` / `__sanitizer_finish_switch_fiber` (`<sanitizer/common_interface_defs.h>`); `tlang_fctx_exit` passes NULL as the fake-stack slot.

**fiber.c.** Stacks: `mmap(PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS|MAP_NORESERVE|MAP_STACK)` of `stack_size + page`, then `mprotect` the lowest page `PROT_NONE`. Fibers are created on demand up to `cfg->max_fibers` and pooled; `mmap` never happens on the request path once the pool is warm. `tlang_request_abort` longjmps within the fiber's own stack (the connection loop's frame), which ASan's longjmp interceptor handles.

**sched.c.** One epoll set per scheduler; fd registrations use `EPOLLONESHOT`, ADD the first time and MOD afterwards (the fd table remembers which). The listener and the wake eventfd are level-triggered. Scheduler threads own listener state and close it only after handling their wake event; each started server scheduler then writes exactly one listener-close acknowledgment to the main-owned eventfd. The main thread polls signals, thread completion, and close acknowledgments independently. Control events in an epoll batch precede listener events, and each accept dispatch is bounded and checks for a pending wake between accepts. Events are validated against the fd table before a fiber is woken (stale one-shot events are dropped). The deadline heap gives the `epoll_wait` timeout. A stop request: disarm and close the listener (`tlang_fd_forget` first), acknowledge closure, wake interruptible waits and wait queues with `TLANG_WAIT_CANCELLED`, and return once no fiber is live or `cfg->shutdown_timeout_ms` passed. Close acknowledgment does not mean request fibers have drained; `done_fd` continues to signal thread completion separately.

**net.c.** `accept4(SOCK_NONBLOCK|SOCK_CLOEXEC)`, `TCP_NODELAY`; accept at most 16 sockets per readiness dispatch, checking the scheduler wake eventfd between accepts, and only while `s->nlive < s->cfg->max_fibers`, otherwise `tlang_sched_pause_accept`. Level-triggered epoll reports remaining queued connections on the next iteration. Reads and writes retry `EINTR`, park on `EAGAIN`, and report `EPIPE`/`ECONNRESET` as `TLANG_IO_ERR` (SIGPIPE is ignored process-wide by `tlang_main`).

**http.c.** Implements DESIGN §4.3 and the §8.1 limits (431, 414, 413, 501 for chunked bodies, `Expect: 100-continue`, header timeout from the first byte of a request, idle timeout between requests with `f->interruptible = true`). All `tlang_ctx` strings get non-NULL data. Per request: set `f->req`, `f->watch_fd`; call the dispatcher under `TLANG_ABORT_POINT`; afterwards answer 404 if nothing was sent and no error is pending, or the error status (400..599, else 500; 499 writes nothing) with a generic reason-phrase body, logging the message to stderr; run `tlang_cleanup_run_all`; `arena_reset`; keep pipelined bytes. Response: status line, `Date` (cached per second in `s->http_date`), `Content-Type`, `Content-Length`, `Connection`, then `tlang_ctx_set_header` headers.

**router.c.** Matching rules are in the `tlang_ctx_match` comment. Write parameters to a local array and copy them to `ctx->params` only on success.

**query.c.** Decoding rules are in the `tlang_ctx_query` comment; decode into the arena only when the raw value contains `%` or `+`.

**pg.c.** With `-DTLANG_NO_PG` it compiles without libpq: every `tlang_db_*` / `tlang_tx_begin` / `tlang_tx_commit` throws 500 `TLANG_MSG_NO_DB`, `tlang_tx_rollback` is a no-op, and `tlang_pg_pool_create` succeeds. Otherwise: async libpq only (`PQconnectStart/Poll`, `PQsendQueryParams` or `PQsendPrepare/PQsendQueryPrepared`, `PQflush`, `PQconsumeInput/PQisBusy`, `PQgetResult` until NULL), waits through `tlang_wait_fd2(f, PQsocket(conn), ev, f->watch_fd, deadline)`. On `TLANG_WAIT_PEER_GONE`: cancel with `PQcancelStart/PQcancelPoll`, drain or close the connection, throw 499. Pool per scheduler with a FIFO `tlang_waitq` (direct hand-off) and `TLANG_DB_POOL_TIMEOUT_MS` (503). `SET statement_timeout` on new connections. Prepared-statement cache per connection keyed by the SQL text (hash, then compare bytes) and the parameter kinds. Every held connection has a `tlang_cleanup` node inside the pooled connection object, pushed when the connection is taken and popped when it is returned, so a request abort returns it (rolled back, or closed when its state is unknown). Copy result strings and server error messages into the arena before `PQclear`. Column mapping rules are in the `tlang_type_desc` comment.

**config.c.** Ranges and defaults are in the `tlang_config` comment. Error messages name the variable and the accepted range; for `TLANG_DATABASE_URL` / `DATABASE_URL` they never echo the value.

**main.c.** `tlang_main`: validate the program (exactly one of `dispatcher` and `main`, else exit 2); `tlang_config_load` (exit 2 on error); `json_set_max_depth`; ignore SIGPIPE. Script mode leaves SIGINT/SIGTERM unblocked so their default actions terminate the process; it runs one scheduler on the main thread without a listener, with one fiber running `init_globals` and then `main`. Server mode blocks SIGINT/SIGTERM before creating threads (every thread inherits the mask; the original mask is restored before `tlang_main` returns). Before binding, create a `signalfd` for SIGINT/SIGTERM and an `eventfd` that each scheduler thread increments as its last action (failure of either: exit 1); after startup the main thread `poll`s both, with no helper thread: the first signal calls `tlang_sched_request_stop` on every scheduler, any further signal during the drain `_exit(1)`s, and the wait ends once every scheduler thread has finished (so if all of them exit on their own, e.g. `init_globals` failed on each, `tlang_main` returns their exit code without a signal); then it joins the threads, destroys the schedulers and returns the last non-zero scheduler exit code, else 0. If startup fails after some threads started, those are stopped and waited for the same way (a signal meanwhile force-exits) and the exit status is 1. A scheduler that passed `tlang_sched_init` is destroyed on later startup failure, whether or not its thread started; listener descriptors use `-1` as the unset sentinel and every descriptor `>= 0` is closed. The signalfd stays open through teardown and is drained without blocking just before the mask is restored: a signal that arrived after a stop request `_exit(1)`s; with no stop requested it is discarded. Create `cfg->threads` listeners (scheduler 0 first; with port 0 the others bind the port it got), print one line `tlang: listening on http://HOST:PORT (N threads)` to stderr, start one thread per scheduler; each thread runs `tlang_sched_init`, `tlang_pg_pool_create` when `uses_db` (a missing URL fails startup, exit 1), allocates `globals_size` zeroed bytes, spawns a startup fiber whose `pub.arena` is the never-reset `globals_arena` and which runs `init_globals` and then `tlang_sched_resume_accept`, and calls `tlang_sched_run`. Script mode exits 1 if an uncaught fiber error escapes.

---

## 7. Building, testing, embedding

### 7.1 Makefile

```
make -C runtime [all|test|clean] [CC=gcc|clang|tcc] [SAN=1] [RELEASE=1] [PG=0]
```

* Library: every `src/*.c` (wildcard: adding a module needs no Makefile change) into `build/<variant>/libtlangrt.a`; `<variant>` is the compiler name plus `-san`, `-rel`, `-nopg`, so configurations never share objects. With no `.c` file yet the archive is empty.
* Tests: every `tests/test_*.c` becomes `build/<variant>/tests/test_*`, linked with the library and `-lpq -lpthread -lm`. `make test` runs them all and fails if any fails. `test_pg_*` are not built with `PG=0` and are skipped unless `TLANG_TEST_DATABASE_URL` is set (a libpq connection string for a disposable database).
* Flags: `-std=c11 -D_GNU_SOURCE -fwrapv -Wall`, plus `-Wextra` for gcc/clang, `-O2 -g` by default. `SAN=1` adds `-fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer -g -O1` (gcc/clang only). `RELEASE=1`: `-O3 -DNDEBUG`. `PG=0`: `-DTLANG_NO_PG`, no `-lpq`. With PG the include path comes from `pg_config --includedir` (fallback `/usr/include/postgresql`). `CFLAGS`, `CPPFLAGS`, `LDFLAGS`, `LDLIBS` on the command line are appended.
* Header checks: `tests/test_header.c` is compiled with `-pedantic -Werror` (`-Werror` for tcc), and `make all` compiles `tlang_internal.h` alone with the same strict flags.
* Every object depends on every header (coarse but correct for all three compilers).
* Verification used for the contracts: `./scripts/dev.ps1 "make -C runtime test CC=gcc && make -C runtime test CC=clang && make -C runtime test CC=tcc && make -C runtime test CC=clang SAN=1 && make -C runtime clean"`.

### 7.2 Compiling the runtime from the driver

The driver extracts the tree (section 7.3) and compiles every `src/*.c` with `-std=c11 -D_GNU_SOURCE -fwrapv -I<root>/include -I<root>/src`, plus `-I$(pg_config --includedir)` and `-lpq` when the program uses `db`, or `-DTLANG_NO_PG` when it does not, and always `-lpthread -lm`. Two compiler facts matter:

* **Build the runtime with the same compiler as the program.** TCC 0.9.27 passes small structs that mix integer and floating-point members differently from gcc/clang (checked: `{bool; double}` round-trips wrongly between a tcc caller and a gcc callee), and TCC objects linked by gcc need `libtcc1`. The public API avoids such structs in extern signatures, but mixing compilers is not supported. For `tlang run` the driver can pass the runtime sources and the program to one `tcc` invocation (`tcc ... runtime/src/*.c -lpq -lpthread -lm -run prog.c`) or cache a tcc-built archive keyed by `runtime.Hash()`.
* **LTO** (`clang -O3 -flto`): either compile the runtime sources in the same clang invocation as the program, or archive LTO objects with `llvm-ar`.

### 7.3 `runtime/embed.go`

Package `runtime` (import path `tlang/runtime`, standard library only):

| API | Meaning |
| :--- | :--- |
| `FS() fs.FS` | The embedded tree: `include/tlang.h`, `src/tlang_internal.h`, `src/*.c`. Tests and the Makefile are not embedded. |
| `Files() []string`, `CSources() []string` | All embedded paths / the `src/*.c` translation units, sorted. |
| `ReadFile(name) ([]byte, error)` | One file. |
| `Hash() string` | Hex SHA-256 over every path and content; name the cache directory after it. |
| `Extract(dir) error` | Writes the tree under `dir`; unchanged files keep their mtime; changed files are replaced atomically (temp file + rename). |
| `IncludeDir`, `SourceDir` | `"include"`, `"src"`. |

---

## 8. Decisions beyond DESIGN.md

1. **OOM aborts the request (503) through a longjmp and is not a catchable Error.** DESIGN §2.8 lists out-of-memory among runtime errors; v1 makes it an abort so that allocation sites need no error checks and `catch` blocks cannot run without memory. Allocation never sets `err`.
2. **`tlang_fiber.globals` is `struct tl_globals*`** (an incomplete type the generated code completes), so `__fib->globals->g_x` from DESIGN §3.2 compiles; `init_globals` takes `void*`.
3. **`s.slice(a, b)` follows JavaScript `String.prototype.slice`:** negative indices count from the end, then both indices are clamped to `[0, len]`; the end index is exclusive, and the result is empty when the resulting start is greater than the end.
4. **Number parsing and formatting:** `toInt`/`paramInt` accept `[+-]?[0-9]+` only; `float64.toString()` and JSON use the shortest of `%.15g/%.16g/%.17g` that round-trips, print `-0` as `0`; `toString` prints `NaN`/`Infinity`, JSON writes `null` for non-finite values.
5. **Conversions:** float64 to int32/int64 truncates, saturates, and maps NaN to 0; int64 to int32 wraps.
6. **One tagged value type** (`tlang_value`) serves console arguments and database parameters (`tlang_pg_param` is a typedef).
7. **Array helpers** take the element size; out-of-range access through `TLANG_SLICE_AT` throws 500 and redirects the access to a zeroed arena block, so expression contexts stay memory-safe. Growth: `max(min, 2*cap, 4)`.
8. **Headers:** `ctx.header` returns the first match; `ctx.query` returns the first match, compares raw keys, decodes values lazily, keeps invalid escapes; route parameters and `ctx.path` are raw (not percent-decoded).
9. **`ctx.setHeader` may fail (500)** for invalid names or values (header injection) and for `Content-Length`, `Transfer-Encoding` and `Connection`; `Content-Type` and `Date` override the defaults.
10. **Uncaught error status:** 400..599 is used as is, anything else becomes 500, 499 sends nothing.
11. **Response statuses** outside 200..599 passed to `ctx.text`/`ctx.json` become 500; 204/304 send no body; HEAD gets headers only.
12. **DB mapping:** column to field by exact name, then ASCII case-insensitive; extra columns ignored; `queryOne` takes the first row; strings copied into the arena; connect failure and pool timeout are 503.
13. **JSON depth** is counted from 0 at the top-level value; a container at depth `d` needs `d < limit`. Object keys are compared raw (escaped keys never match a field). Lone surrogates decode to U+FFFD; no UTF-8 validation.
14. **`TLANG_CTX_MAX_HEADERS` is 32** (the spec's array size); `TLANG_MAX_HEADERS` can only lower the limit.
15. **Configuration:** invalid or out-of-range values stop startup (exit 2); `TLANG_STACK_SIZE` and `TLANG_MAX_FIBERS` are also read from the environment; new `TLANG_SHUTDOWN_TIMEOUT_MS` (10000); `TLANG_PORT=0` picks an ephemeral port shared by all schedulers and the chosen port is printed at startup.
16. **Watching the client during DB waits** uses `EPOLLRDHUP`: a client that half-closes its side while a query runs is treated as gone (499), like Go's net/http.
17. **Spec §7.1 `fn`** receives its own fiber (`void (*fn)(Fiber*, void*)`).
18. **Spec §6.2 `arena_alloc`** tests `n > capacity - offset`, which cannot overflow (the spec's `offset + n > capacity` can).

## 9. Known limitations and documentation follow-ups

1. **String slicing documentation.** `s.slice(a, b)` follows JavaScript `String.prototype.slice`: negative indices count from the end, both indices are clamped to `[0, len]`, the end index is exclusive, and the result is empty when the resulting start is greater than the end (section 8.3). Keep language-facing documentation aligned with this behavior.
2. **Database row field types.** The checker restricts DB-mapped row fields to database scalar types. Interface and array fields are therefore rejected rather than initialized by row-decoding code. This is an intentional limitation unless DB row construction is extended to initialize such fields; preserve or explicitly revise the restriction and its tests if that contract changes.
