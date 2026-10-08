/*
 * tlang.h - public C API of the TLang v1 runtime.
 *
 * This is the only header that generated code includes (DESIGN.md §4.1). It
 * includes nothing but <stdint.h>, <stddef.h>, <stdbool.h> and <string.h>, and
 * compiles warning-free with `gcc|clang -std=c11 -Wall -Wextra -pedantic` and
 * with `tcc -std=c11 -Wall`. It uses no thread-local storage, no C11 atomics
 * and no GNU statement expressions. docs/RUNTIME.md maps every declaration to
 * the source file that implements it.
 *
 * Conventions (they apply to every declaration below unless its comment says
 * otherwise):
 *
 * Fiber parameter. A function whose first parameter is `tlang_fiber* fib` may
 *   allocate in the fiber arena, may suspend the fiber on I/O, or may fail.
 *   Functions without that parameter never fail, never suspend and never
 *   abort; the few that allocate do so from an arena they receive explicitly.
 *   Comments mark side-effect-free functions "Pure". `fib` must not be NULL
 *   unless the comment allows it.
 *
 * May fail. A function documented "May fail" reports failure by setting
 *   fib->err = 1 and fib->error = {status, message} and returning the zero
 *   value of its return type (0, false, NULL, or an empty string). Generated
 *   code tests `if (__fib->err) goto <catch label or failure exit>;` right
 *   after the call. A function documented "Never fails" never touches fib->err.
 *
 * Out of memory. Apart from the raw arena primitives (arena_grow, arena_alloc)
 *   and json_scan_string, which report it to their caller, no function returns
 *   because memory ran out. Running out of memory, or asking for a size whose
 *   computation overflows, aborts the current HTTP request with status 503
 *   through the request-abort path: a longjmp to the connection loop, which
 *   runs the per-fiber cleanup hooks (releasing pooled DB connections),
 *   answers 503 if nothing was sent yet, resets the arena and closes the
 *   connection. OOM is not an Error and TLang code cannot catch it. Outside a
 *   request (global initialisation, script mode) the runtime logs the failure
 *   and exits with status 1. Comments say "Aborts on OOM" for functions that
 *   allocate.
 *
 * Lifetimes of returned data:
 *   static  - valid for the whole process (string literals, constant tables).
 *   request - valid until the current request ends, that is until the
 *             dispatcher returns and the arena is reset. Covers arena memory
 *             and slices of the request buffer (spec §6.3 rule 1). In script
 *             mode and during global initialisation the arena is never reset.
 *   global  - heap memory that is never freed (`new global`, clone_global).
 *
 * Strings. Every function accepts {NULL, 0} as the empty string. Runtime
 *   functions never return a string whose data is NULL: that value is the null
 *   of `string | null` (DESIGN.md §2.5).
 *
 * Threads. All runtime state belongs to one scheduler thread. A fiber, its
 *   arena, its context and the globals it reaches are only ever touched by the
 *   thread of the scheduler that created the fiber.
 *
 * Reserved names. The runtime uses the prefixes tlang_, TLANG_ and json_, the
 *   spec names ArenaChunk, MemoryArena, ARENA_*, arena_* and TxContext, and the
 *   struct tag tl_globals (completed by generated code). Generated code must
 *   not define other identifiers with these names or prefixes.
 *
 * TCC 0.9.27 rules that generated code must follow:
 *   - An array whose initializer holds non-constant struct values needs an
 *     explicit size: `(tlang_value[2]){ TLANG_VAL_I64(x), TLANG_VAL_STR(s) }`.
 *     The unsized form `(tlang_value[]){ ... }` fails with "index too large".
 *   - TCC does not follow the x86-64 ABI for small structs that mix integer
 *     and floating-point members (tlang_opt_f64, for example). No extern
 *     function declared here passes or returns such a struct by value, but
 *     the runtime and the program must still be compiled by the same compiler.
 */
#ifndef TLANG_H
#define TLANG_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include <string.h>

/* ========================================================================
 * 1. Forward declarations
 * ======================================================================== */

/* Public view of a fiber (section 5). Generated code receives it as the hidden
 * first parameter `__fib` of every generated function (DESIGN.md §3.1). */
typedef struct tlang_fiber tlang_fiber;

/* HTTP request context, the TLang builtin `Context` (section 15). */
typedef struct tlang_ctx tlang_ctx;

/* Database transaction, the TLang builtin `Transaction` (section 16). */
typedef struct tlang_tx tlang_tx;

/* Per-scheduler globals of the program. Generated code completes the type with
 * `struct tl_globals { ... };` (DESIGN.md §3.3) and reads a global as
 * `__fib->globals->g_name`. The runtime never sees the layout: it allocates
 * tlang_program.globals_size zeroed bytes once per scheduler thread. */
struct tl_globals;

/* ========================================================================
 * 2. Strings (spec §3.4, DESIGN.md §2.7)
 * ======================================================================== */

/* A byte slice that never owns its memory. `data` points at `len` bytes, with
 * no NUL terminator implied (never hand `data` to an API expecting a C
 * string). data == NULL is allowed only with len == 0. As a non-optional
 * `string` it means "", as `string | null` it means null (DESIGN.md §2.5).
 * The bytes are usually UTF-8; every operation works on bytes. */
typedef struct tlang_string {
    const char* data;
    size_t len;
} tlang_string;

/* String literal as an expression: TLANG_STR("abc") has static lifetime and
 * len 3. The argument must be a string literal (the "" concatenation rejects
 * anything else at compile time). Embedded NULs count: TLANG_STR("a\0b") has
 * len 3. Not a constant expression: use TLANG_STR_INIT in static tables. */
#define TLANG_STR(lit) ((tlang_string){ "" lit, sizeof("" lit) - 1 })

/* Brace initializer for tlang_string objects with static storage, e.g.
 * `static const tlang_string k = TLANG_STR_INIT("id");` or a member of a route
 * table. Same rules as TLANG_STR. */
#define TLANG_STR_INIT(lit) { "" lit, sizeof("" lit) - 1 }

/* The null value of `string | null`. */
#define TLANG_STR_NULL ((tlang_string){ NULL, 0 })

/* End argument for tlang_str_slice when TLang code omits it (`s.slice(a)`). */
#define TLANG_STR_END INT64_MAX

/* ========================================================================
 * 3. Errors (spec §3.6, DESIGN.md §2.8): types and constants
 * ======================================================================== */

/* The TLang builtin `Error`, passed by value. `status` is the suggested HTTP
 * status. `message` must stay valid until the request ends: it is a literal,
 * arena memory, or a slice of the request. Runtime modules copy transient
 * text (libpq messages, for example) into the arena before throwing it.
 * When an error escapes the dispatcher, the HTTP layer answers with `status`
 * if it is in 400..599 and with 500 otherwise, using a generic reason-phrase
 * body; the message goes to stderr, never to the client. Status 499 (client
 * disconnected) is only logged: no response is written. */
typedef struct tlang_error {
    int32_t status;
    tlang_string message;
} tlang_error;

/* Statuses the runtime itself throws. */
#define TLANG_STATUS_BAD_REQUEST    400 /* ctx.paramInt, s.toInt */
#define TLANG_STATUS_CLIENT_CLOSED  499 /* client went away during a DB wait */
#define TLANG_STATUS_INTERNAL       500 /* default for throw, runtime errors */
#define TLANG_STATUS_UNAVAILABLE    503 /* OOM abort, DB pool timeout, DB down */

/* Messages of the errors the runtime throws (string literals). */
#define TLANG_MSG_DIV_ZERO       "division by zero"
#define TLANG_MSG_INDEX_RANGE    "index out of range"
#define TLANG_MSG_NULL_VALUE     "null value"
#define TLANG_MSG_INVALID_INT    "invalid integer"
#define TLANG_MSG_BAD_HEADER     "invalid response header"
#define TLANG_MSG_OUT_OF_MEMORY  "out of memory"
#define TLANG_MSG_CLIENT_GONE    "client disconnected"
#define TLANG_MSG_POOL_TIMEOUT   "database pool timeout"
#define TLANG_MSG_DB_UNAVAILABLE "database unavailable"
#define TLANG_MSG_TX_INACTIVE    "transaction is not active"
#define TLANG_MSG_NO_DB          "database support not compiled in"

/* ========================================================================
 * 4. Arena (spec §6.2)
 * ======================================================================== */

/* Size of the first chunk of every fiber arena. That chunk is kept across
 * requests; extra chunks are freed by arena_reset. Only arena.c reads it, so
 * overriding it with -D changes no struct layout. */
#ifndef ARENA_CHUNK_SIZE
#define ARENA_CHUNK_SIZE (128 * 1024)
#endif

/* Alignment of every arena allocation. Enough for every TLang value type
 * (int64_t, double, pointers, tlang_string). Not overridable: arena_alloc is
 * inlined into generated code. */
#define ARENA_ALIGNMENT 8

/* One block of arena memory. `data` is ARENA_ALIGNMENT-aligned (checked by
 * static assertion below and in tests/test_header.c). */
typedef struct ArenaChunk {
    struct ArenaChunk* next;   /* next chunk in the chain, NULL at the end */
    size_t capacity;           /* usable bytes in data[] */
    size_t offset;             /* bytes of data[] already handed out */
    uint8_t data[];            /* flexible array member (C11) */
} ArenaChunk;

_Static_assert(offsetof(ArenaChunk, data) % ARENA_ALIGNMENT == 0,
               "ArenaChunk.data must be ARENA_ALIGNMENT-aligned");

/* A bump allocator made of a chain of chunks. `first` is owned by the fiber
 * and only freed when the fiber pool is destroyed. `current` is the last chunk
 * of the chain, the one allocations come from. An arena must be initialised
 * (tlang_arena_init, internal API) before use. */
typedef struct MemoryArena {
    ArenaChunk* first;
    ArenaChunk* current;
} MemoryArena;

/* Allocates a chunk with capacity max(ARENA_CHUNK_SIZE, n) and offset 0,
 * appends it to the chain and makes it a->current. Returns NULL (arena
 * unchanged) when malloc fails or sizeof(ArenaChunk) + capacity overflows.
 * Called by arena_alloc only. */
ArenaChunk* arena_grow(MemoryArena* a, size_t n);

/* Returns `size` bytes (rounded up to ARENA_ALIGNMENT), uninitialised and
 * ARENA_ALIGNMENT-aligned, valid until the next arena_reset. Returns NULL when
 * the rounded size overflows or arena_grow fails; the arena is unchanged then.
 * size 0 returns a valid pointer that must not be dereferenced.
 * (Same as spec §6.2, with the capacity test written so it cannot overflow.) */
static inline void* arena_alloc(MemoryArena* a, size_t size) {
    size_t n;
    ArenaChunk* c;
    void* p;
    if (size > SIZE_MAX - (ARENA_ALIGNMENT - 1)) return NULL;
    n = (size + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
    c = a->current;
    if (n > c->capacity - c->offset) {
        c = arena_grow(a, n);
        if (c == NULL) return NULL;
    }
    p = c->data + c->offset;
    c->offset += n;
    return p;
}

/* Frees every chunk after the first, sets first->offset = 0 and makes `first`
 * current again. O(1) when the request fit in the first chunk, O(k) for k
 * extra chunks. Every pointer obtained from the arena becomes invalid. Called
 * by the runtime after each request; generated code never calls it. */
void arena_reset(MemoryArena* a);

/* ========================================================================
 * 5. Fiber, public view
 * ======================================================================== */

/* The fields of a fiber that generated code reads directly. The runtime's
 * private fiber (struct Fiber in src/tlang_internal.h) embeds this struct as
 * its first member, so a tlang_fiber* is also a pointer to the private
 * fiber. Generated code never creates, copies or frees a tlang_fiber. */
struct tlang_fiber {
    /* Nonzero while an error is pending (spec §3.6 `fiber->err`). Set by
     * tlang_throw*, cleared by tlang_take_error / tlang_clear_error. */
    int err;
    /* The pending error; meaningful only while err != 0. */
    tlang_error error;
    /* Globals instance of this fiber's scheduler (see struct tl_globals). */
    struct tl_globals* globals;
    /* Arena that allocations of this fiber come from: the per-request arena,
     * or a never-reset arena during global initialisation. Pass it to
     * json_scan_string; use tlang_alloc_* for everything else. */
    MemoryArena* arena;
};

/* ========================================================================
 * 6. Throwing and catching (implemented in errors.c)
 * ======================================================================== */

/* `throw new Error(message, status)` and runtime errors: sets fib->err = 1 and
 * fib->error = {status, message}, replacing an error that is already pending
 * (codegen never throws over a pending error). A NULL message.data is stored
 * as "". `message` must stay valid until the request ends (see tlang_error).
 * Has no other effect. */
void tlang_throw(tlang_fiber* fib, int32_t status, tlang_string message);

/* `throw e;` for a caught Error value: same as tlang_throw(fib, err.status,
 * err.message). */
void tlang_throw_value(tlang_fiber* fib, tlang_error err);

/* Entry of a `catch` block: returns the pending error and clears it
 * (fib->err = 0). With no error pending it returns {0, ""}. */
tlang_error tlang_take_error(tlang_fiber* fib);

/* `catch { ... }` without a binding: drops the pending error, if any. */
static inline void tlang_clear_error(tlang_fiber* fib) {
    fib->err = 0;
    fib->error.status = 0;
    fib->error.message = TLANG_STR("");
}

/* `new Error(message)` (status 500) and `new Error(message, status)`: builds
 * the value without throwing it. Pure. */
static inline tlang_error tlang_error_make(tlang_string message, int32_t status) {
    tlang_error e;
    e.status = status;
    e.message = message;
    return e;
}

/* Throws an error whose message is a string literal. */
#define TLANG_THROW_LIT(fib, status, lit) tlang_throw((fib), (status), TLANG_STR(lit))

/* Throws Error{500, "null value"}: the `x!` assertion on a null value. */
static inline void tlang_throw_null(tlang_fiber* fib) {
    tlang_throw(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NULL_VALUE));
}

/* Throws Error{500, "index out of range"}. */
static inline void tlang_throw_index(tlang_fiber* fib) {
    tlang_throw(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_INDEX_RANGE));
}

/* Throws Error{500, "division by zero"}. */
static inline void tlang_throw_div_zero(tlang_fiber* fib) {
    tlang_throw(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_DIV_ZERO));
}

/* ========================================================================
 * 7. Allocation (spec §6.2, §6.3)
 * ======================================================================== */

/* Out-of-memory handler (arena.c). Never returns: inside a request it aborts
 * the request with 503 through the request-abort path; elsewhere it logs
 * "out of memory" with `size` and exits the process with status 1. fib may be
 * NULL (then it always exits). */
_Noreturn void tlang_alloc_failed(tlang_fiber* fib, size_t size);

/* Uninitialised arena memory for `size` bytes, ARENA_ALIGNMENT-aligned.
 * Lifetime: request. Never returns NULL. Aborts on OOM. */
static inline void* tlang_alloc_raw(tlang_fiber* fib, size_t size) {
    void* p = arena_alloc(fib->arena, size);
    if (p == NULL) tlang_alloc_failed(fib, size);
    return p;
}

/* `new T()`: zeroed arena memory, e.g.
 * `tl_User* u = (tl_User*)tlang_alloc_zeroed(__fib, sizeof(tl_User));`.
 * Lifetime: request. Never returns NULL. Aborts on OOM. */
static inline void* tlang_alloc_zeroed(tlang_fiber* fib, size_t size) {
    void* p = tlang_alloc_raw(fib, size);
    memset(p, 0, size);
    return p;
}

/* `new global T()`: zeroed heap memory (calloc), at least 16-byte aligned.
 * Lifetime: global (never freed). Never returns NULL. Aborts on OOM (exits
 * outside a request). fib may be NULL. */
void* tlang_alloc_global_zeroed(tlang_fiber* fib, size_t size);

/* ========================================================================
 * 8. String operations (DESIGN.md §2.7)
 * ======================================================================== */

/* `a == b`, `a.eq(b)`: byte equality (length check plus memcmp). {NULL, 0}
 * equals "". Pure. */
static inline bool tlang_str_eq(tlang_string a, tlang_string b) {
    if (a.len != b.len) return false;
    if (a.len == 0 || a.data == b.data) return true;
    return memcmp(a.data, b.data, a.len) == 0;
}

/* `s.startsWith(prefix)`. An empty prefix matches. Pure. */
static inline bool tlang_str_starts_with(tlang_string s, tlang_string prefix) {
    if (prefix.len > s.len) return false;
    return prefix.len == 0 || memcmp(s.data, prefix.data, prefix.len) == 0;
}

/* `s.endsWith(suffix)`. An empty suffix matches. Pure. */
static inline bool tlang_str_ends_with(tlang_string s, tlang_string suffix) {
    if (suffix.len > s.len) return false;
    return suffix.len == 0 ||
           memcmp(s.data + (s.len - suffix.len), suffix.data, suffix.len) == 0;
}

/* `s.slice(start, end)` with JavaScript String.prototype.slice semantics on
 * bytes: a negative index counts from the end (len + index), then both are
 * clamped to [0, len]; end <= start gives "". Pass TLANG_STR_END for an
 * omitted end. The result shares s's bytes (same lifetime as s), except that
 * an empty result is the static "". Pure. */
static inline tlang_string tlang_str_slice(tlang_string s, int64_t start, int64_t end) {
    int64_t n = s.len > (size_t)INT64_MAX ? INT64_MAX : (int64_t)s.len;
    tlang_string r;
    if (start < 0) {
        start += n;
        if (start < 0) start = 0;
    } else if (start > n) {
        start = n;
    }
    if (end < 0) {
        end += n;
        if (end < 0) end = 0;
    } else if (end > n) {
        end = n;
    }
    if (end <= start) return TLANG_STR("");
    r.data = s.data + start;
    r.len = (size_t)(end - start);
    return r;
}

/* `s.indexOf(needle)`: byte offset of the first occurrence, or -1. An empty
 * needle returns 0. Pure. */
int64_t tlang_str_index_of(tlang_string s, tlang_string needle);

/* `a + b`: a string holding a's bytes then b's bytes, in the arena. Lifetime:
 * request. When one operand is empty the result may be the other operand
 * itself (never a NULL data pointer: two empty operands give the static "").
 * Never fails. Aborts on OOM. */
tlang_string tlang_str_concat(tlang_fiber* fib, tlang_string a, tlang_string b);

/* `s.clone()`: copy of s in the arena, independent of the request buffer.
 * Lifetime: request ("" is returned as the static ""). Never fails. Aborts on
 * OOM. */
tlang_string tlang_str_clone(tlang_fiber* fib, tlang_string s);

/* `s.clone_global()`: copy of s on the heap. Lifetime: global (never freed;
 * "" is returned as the static ""). Never fails. Aborts on OOM. fib may be
 * NULL. */
tlang_string tlang_str_clone_global(tlang_fiber* fib, tlang_string s);

/* `s.toInt()`: parses `[+-]?[0-9]+` (decimal, no whitespace, no other
 * characters) into an int64. May fail: 400 "invalid integer" when s does not
 * match or the value does not fit in int64; returns 0 then. */
int64_t tlang_str_to_int(tlang_fiber* fib, tlang_string s);

/* Widening `string` -> `string | null`: replaces a NULL data pointer by the
 * static "", so an empty string never becomes null (DESIGN.md §2.5). Pure. */
static inline tlang_string tlang_str_some(tlang_string s) {
    if (s.data == NULL) s.data = "";
    return s;
}

/* `n.toString()` for int32 / int64: decimal digits with a leading '-' for
 * negative values. Lifetime: request. Never fails. Aborts on OOM. */
tlang_string tlang_i32_to_string(tlang_fiber* fib, int32_t v);
tlang_string tlang_i64_to_string(tlang_fiber* fib, int64_t v);

/* `x.toString()` for float64: the shortest of %.15g, %.16g and %.17g that
 * reads back as the same double ("0.1", "1e+21", "0.30000000000000004"),
 * "-0" printed as "0", NaN as "NaN", infinities as "Infinity" / "-Infinity".
 * The runtime never calls setlocale, so the decimal point is '.'.
 * Lifetime: request. Never fails. Aborts on OOM. */
tlang_string tlang_f64_to_string(tlang_fiber* fib, double v);

/* `b.toString()`: the static "true" or "false". Never fails. */
static inline tlang_string tlang_bool_to_string(tlang_fiber* fib, bool v) {
    (void)fib;
    return v ? TLANG_STR("true") : TLANG_STR("false");
}

/* ========================================================================
 * 9. Numbers: wrapping arithmetic and conversions (spec §3.5, DESIGN.md §2.3)
 * ======================================================================== */

/* int32/int64 `+ - *` and unary `-` wrap modulo 2^32 / 2^64. They compute in
 * unsigned arithmetic and convert back, so they are correct even without
 * -fwrapv (gcc, clang and tcc convert out-of-range unsigned values to signed
 * by wrapping). Pure. Comparisons and float64 arithmetic need no helper. */
static inline int32_t tlang_add_i32(int32_t a, int32_t b) { return (int32_t)((uint32_t)a + (uint32_t)b); }
static inline int32_t tlang_sub_i32(int32_t a, int32_t b) { return (int32_t)((uint32_t)a - (uint32_t)b); }
static inline int32_t tlang_mul_i32(int32_t a, int32_t b) { return (int32_t)((uint32_t)a * (uint32_t)b); }
static inline int32_t tlang_neg_i32(int32_t a) { return (int32_t)(0u - (uint32_t)a); }
static inline int64_t tlang_add_i64(int64_t a, int64_t b) { return (int64_t)((uint64_t)a + (uint64_t)b); }
static inline int64_t tlang_sub_i64(int64_t a, int64_t b) { return (int64_t)((uint64_t)a - (uint64_t)b); }
static inline int64_t tlang_mul_i64(int64_t a, int64_t b) { return (int64_t)((uint64_t)a * (uint64_t)b); }
static inline int64_t tlang_neg_i64(int64_t a) { return (int64_t)(UINT64_C(0) - (uint64_t)a); }

/* Integer `/` and `%`: C semantics (quotient truncated toward zero, remainder
 * takes the sign of a). MIN / -1 wraps to MIN and MIN % -1 is 0 instead of
 * trapping. May fail: 500 "division by zero" when b == 0; returns 0 then. */
static inline int32_t tlang_div_i32(tlang_fiber* fib, int32_t a, int32_t b) {
    if (b == 0) { tlang_throw_div_zero(fib); return 0; }
    if (b == -1) return tlang_neg_i32(a);
    return a / b;
}
static inline int32_t tlang_mod_i32(tlang_fiber* fib, int32_t a, int32_t b) {
    if (b == 0) { tlang_throw_div_zero(fib); return 0; }
    if (b == -1) return 0;
    return a % b;
}
static inline int64_t tlang_div_i64(tlang_fiber* fib, int64_t a, int64_t b) {
    if (b == 0) { tlang_throw_div_zero(fib); return 0; }
    if (b == -1) return tlang_neg_i64(a);
    return a / b;
}
static inline int64_t tlang_mod_i64(tlang_fiber* fib, int64_t a, int64_t b) {
    if (b == 0) { tlang_throw_div_zero(fib); return 0; }
    if (b == -1) return 0;
    return a % b;
}

/* float64 `%`: fmod(a, b) (JavaScript semantics; b == 0 gives NaN). Lives in
 * strings.c so that this header does not need <math.h>; link with -lm.
 * Never fails. */
double tlang_mod_f64(double a, double b);

/* `int32(x)` for an int64 x: keeps the low 32 bits (two's complement wrap).
 * Pure. */
static inline int32_t tlang_i64_to_i32(int64_t x) {
    return (int32_t)(uint32_t)(uint64_t)x;
}

/* `int32(x)` / `int64(x)` for a float64 x: truncates toward zero, saturates
 * at the target's MIN/MAX, NaN gives 0. Never undefined behaviour. Pure.
 * (`float64(i)` and `int64(i32)` are plain C casts.) */
static inline int32_t tlang_f64_to_i32(double x) {
    if (x != x) return 0;
    if (x >= 2147483647.0) return INT32_MAX;
    if (x <= -2147483648.0) return INT32_MIN;
    return (int32_t)x;
}
static inline int64_t tlang_f64_to_i64(double x) {
    if (x != x) return 0;
    if (x >= 9223372036854775807.0) return INT64_MAX;   /* the literal is 2^63 */
    if (x <= -9223372036854775808.0) return INT64_MIN;
    return (int64_t)x;
}

/* ========================================================================
 * 10. Optional values (DESIGN.md §2.5)
 * ======================================================================== */

/* `T | null` for the number and bool types. has == false means null; v is
 * then 0/false and must be ignored. Interfaces and arrays use a NULL pointer
 * for null, strings use data == NULL. */
typedef struct tlang_opt_i32 { bool has; int32_t v; } tlang_opt_i32;
typedef struct tlang_opt_i64 { bool has; int64_t v; } tlang_opt_i64;
typedef struct tlang_opt_f64 { bool has; double v; } tlang_opt_f64;
typedef struct tlang_opt_bool { bool has; bool v; } tlang_opt_bool;

/* Optional constructors: m is one of i32, i64, f64, bool.
 * TLANG_SOME(i64, x) is a present value, TLANG_NONE(i64) is null. */
#define TLANG_SOME(m, x) ((tlang_opt_##m){ true, (x) })
#define TLANG_NONE(m)    ((tlang_opt_##m){ false, 0 })

/* `x!` on an optional: returns the value. May fail: 500 "null value" when x
 * is null; returns 0/false/NULL/"" then. */
static inline int32_t tlang_unwrap_i32(tlang_fiber* fib, tlang_opt_i32 o) {
    if (!o.has) { tlang_throw_null(fib); return 0; }
    return o.v;
}
static inline int64_t tlang_unwrap_i64(tlang_fiber* fib, tlang_opt_i64 o) {
    if (!o.has) { tlang_throw_null(fib); return 0; }
    return o.v;
}
static inline double tlang_unwrap_f64(tlang_fiber* fib, tlang_opt_f64 o) {
    if (!o.has) { tlang_throw_null(fib); return 0.0; }
    return o.v;
}
static inline bool tlang_unwrap_bool(tlang_fiber* fib, tlang_opt_bool o) {
    if (!o.has) { tlang_throw_null(fib); return false; }
    return o.v;
}
static inline tlang_string tlang_unwrap_str(tlang_fiber* fib, tlang_string s) {
    if (s.data == NULL) { tlang_throw_null(fib); return TLANG_STR(""); }
    return s;
}
/* For interface and array references: `(tl_User*)tlang_unwrap_ptr(__fib, u)`. */
static inline void* tlang_unwrap_ptr(tlang_fiber* fib, void* p) {
    if (p == NULL) tlang_throw_null(fib);
    return p;
}

/* ========================================================================
 * 11. Arrays (DESIGN.md §2.6)
 * ======================================================================== */

/* Declares the slice header type for element type T with mangle m
 * (DESIGN.md §3.2): `TLANG_SLICE_DEFINE(User, tl_User*)` declares
 * `tlang_slice_User`. A TLang `T[]` value is a `tlang_slice_<m>*`.
 *   items  - element storage (arena or heap), NULL while cap == 0. Elements
 *            at index >= len are unspecified.
 *   len    - number of elements, 0 <= len <= cap.
 *   cap    - allocated elements.
 *   global - true for `new global T[]()`: header and items live on the heap
 *            and items grow with realloc. false: everything lives in the arena.
 * Every instantiation has the same size and field offsets (static assertions
 * below), which the generic helpers rely on. The expansion ends with ';':
 * write the invocation at file scope without a trailing ';' (an extra ';'
 * only draws a -pedantic warning). To break a cycle between an interface and
 * its slice type, name the tag `struct tlang_slice_<m>` before the define. */
#define TLANG_SLICE_DEFINE(m, T)                                               \
    typedef struct tlang_slice_##m { T* items; int64_t len; int64_t cap; bool global; } tlang_slice_##m;

/* Predefined slices: int32[], int64[], float64[], bool[], string[]. */
TLANG_SLICE_DEFINE(i32, int32_t)
TLANG_SLICE_DEFINE(i64, int64_t)
TLANG_SLICE_DEFINE(f64, double)
TLANG_SLICE_DEFINE(bool, bool)
TLANG_SLICE_DEFINE(str, tlang_string)

_Static_assert(sizeof(tlang_slice_i32) == sizeof(tlang_slice_str) &&
               sizeof(tlang_slice_bool) == sizeof(tlang_slice_f64),
               "all slice headers must have the same size");
_Static_assert(offsetof(tlang_slice_i32, len) == offsetof(tlang_slice_str, len) &&
               offsetof(tlang_slice_i32, cap) == offsetof(tlang_slice_str, cap) &&
               offsetof(tlang_slice_i32, global) == offsetof(tlang_slice_str, global),
               "all slice headers must have the same layout");

/* Allocates a zeroed slice header of header_size bytes (pass
 * sizeof(tlang_slice_<m>)): items NULL, len 0, cap 0. global == false puts it
 * in the arena (lifetime request); global == true puts it on the heap
 * (lifetime global) and sets its `global` field. Prefer the TLANG_SLICE_NEW*
 * macros. Never fails. Aborts on OOM. */
void* tlang_slice_new(tlang_fiber* fib, size_t header_size, bool global);

/* Grows element storage so that it holds at least min_cap elements of
 * elem_size bytes. New capacity: max(min_cap, 2 * *cap, 4). Copies the first
 * len elements. Arena storage (global == false) moves to a new arena block
 * (the old block stays until the arena is reset; growing in place is allowed
 * when the block is the arena's last allocation); heap storage (global ==
 * true) uses realloc. Stores the new capacity in *cap and returns the new
 * items pointer. Returns items unchanged when *cap >= min_cap. Never returns
 * NULL. Never fails: an element count or byte size that overflows int64_t /
 * size_t is treated as OOM. Aborts on OOM. */
void* tlang_slice_grow(tlang_fiber* fib, void* items, int64_t len, int64_t* cap,
                       int64_t min_cap, bool global, size_t elem_size);

/* Out-of-range path of tlang_slice_at: throws 500 "index out of range" and
 * returns a zeroed arena block of elem_size bytes, so that the caller's read
 * or write stays in bounds (the value read is the zero value; a write is
 * discarded). Aborts on OOM. */
void* tlang_slice_oob(tlang_fiber* fib, int64_t index, int64_t len, size_t elem_size);

/* Bounds check for codegen that tests before indexing: returns true when
 * 0 <= index < len. May fail: 500 "index out of range"; returns false then. */
static inline bool tlang_index_ok(tlang_fiber* fib, int64_t index, int64_t len) {
    if ((uint64_t)index < (uint64_t)len) return true;
    tlang_throw_index(fib);
    return false;
}

/* Address of element `index` of an items array of length len. Out of range:
 * see tlang_slice_oob (may fail with 500, returns a scratch block). */
static inline void* tlang_slice_at(tlang_fiber* fib, void* items, int64_t len,
                                   int64_t index, size_t elem_size) {
    if ((uint64_t)index < (uint64_t)len) return (char*)items + (size_t)index * elem_size;
    return tlang_slice_oob(fib, index, len, elem_size);
}

/* `new T[]()` and `new global T[]()`: a new empty slice, typed. */
#define TLANG_SLICE_NEW(fib, m) \
    ((tlang_slice_##m*)tlang_slice_new((fib), sizeof(tlang_slice_##m), false))
#define TLANG_SLICE_NEW_GLOBAL(fib, m) \
    ((tlang_slice_##m*)tlang_slice_new((fib), sizeof(tlang_slice_##m), true))

/* Statement: makes room for at least n elements (array literals reserve their
 * element count before pushing). s and n are evaluated several times: pass
 * locals, constants or side-effect-free paths. Never fails. Aborts on OOM. */
#define TLANG_SLICE_RESERVE(fib, s, n)                                          \
    do {                                                                        \
        if ((s)->cap < (int64_t)(n))                                            \
            (s)->items = tlang_slice_grow((fib), (s)->items, (s)->len, &(s)->cap, \
                                          (int64_t)(n), (s)->global,            \
                                          sizeof(*(s)->items));                 \
    } while (0)

/* Statement: `s.push(v)`, amortised doubling. s is evaluated several times;
 * v once, after a possible reallocation and unsequenced with the element
 * address: pass a local or side-effect-free path as s, and evaluate a v that
 * contains calls into a temporary first. Never fails. Aborts on OOM. */
#define TLANG_SLICE_PUSH(fib, s, v)                                             \
    do {                                                                        \
        if ((s)->len >= (s)->cap)                                               \
            (s)->items = tlang_slice_grow((fib), (s)->items, (s)->len, &(s)->cap, \
                                          (s)->len + 1, (s)->global,            \
                                          sizeof(*(s)->items));                 \
        (s)->items[(s)->len] = (v);                                             \
        (s)->len += 1;                                                          \
    } while (0)

/* Lvalue of element i of slice s with element type T, bounds checked:
 * `x = TLANG_SLICE_AT(__fib, s, i, tl_User*);` and
 * `TLANG_SLICE_AT(__fib, s, i, int64_t) = v;`. s is evaluated twice. In an
 * assignment evaluate a v that contains calls into a temporary first: the
 * element address is computed before v in no guaranteed order. May fail: 500
 * "index out of range" (the read yields the zero value, the write is lost). */
#define TLANG_SLICE_AT(fib, s, i, T) \
    (*(T*)tlang_slice_at((fib), (s)->items, (s)->len, (i), sizeof(T)))

/* ========================================================================
 * 12. Tagged values: console arguments and database parameters
 * ======================================================================== */

/* Value kinds. tlang_value uses the base kinds (NULL..STR). Row descriptors
 * (tlang_field_desc) also use the OPT_* kinds, where the field is stored as
 * tlang_opt_i32 / tlang_opt_i64 / tlang_opt_f64 / tlang_opt_bool, or as a
 * tlang_string with data == NULL for null (OPT_STR). */
enum {
    TLANG_KIND_NULL = 0,         /* untyped null (the `null` literal) */
    TLANG_KIND_I32 = 1,          /* int32_t */
    TLANG_KIND_I64 = 2,          /* int64_t */
    TLANG_KIND_F64 = 3,          /* double */
    TLANG_KIND_BOOL = 4,         /* bool */
    TLANG_KIND_STR = 5,          /* tlang_string */
    TLANG_KIND_OPT = 0x10,       /* flag: optional variant of a base kind */
    TLANG_KIND_OPT_I32 = 0x11,
    TLANG_KIND_OPT_I64 = 0x12,
    TLANG_KIND_OPT_F64 = 0x13,
    TLANG_KIND_OPT_BOOL = 0x14,
    TLANG_KIND_OPT_STR = 0x15
};

/* A scalar value with its type: an argument of console.log / console.error or
 * a parameter of a database call. `kind` is a base TLANG_KIND_*. is_null ==
 * true means SQL NULL / the text "null"; the union is then ignored. The value
 * borrows string bytes; it only has to stay valid for the duration of the
 * call it is passed to. Build values with the TLANG_VAL_* constructors and
 * pass arrays with an explicit size (TCC rule in the header comment):
 *   tlang_console_log(__fib, (tlang_value[2]){ TLANG_VAL_STR(s), TLANG_VAL_I64(n) }, 2); */
typedef struct tlang_value {
    int kind;
    bool is_null;
    union {
        int32_t i32;
        int64_t i64;
        double f64;
        bool b;
        tlang_string str;
    } v;
} tlang_value;

/* Constructors. The plain forms evaluate x once and give non-null values.
 * The OPT forms take a tlang_opt_* (or a `string | null` for STR) and give
 * is_null = true for null. TLANG_VAL_NULL is the untyped `null` literal. */
#define TLANG_VAL_I32(x)  ((tlang_value){ TLANG_KIND_I32, false, { .i32 = (x) } })
#define TLANG_VAL_I64(x)  ((tlang_value){ TLANG_KIND_I64, false, { .i64 = (x) } })
#define TLANG_VAL_F64(x)  ((tlang_value){ TLANG_KIND_F64, false, { .f64 = (x) } })
#define TLANG_VAL_BOOL(x) ((tlang_value){ TLANG_KIND_BOOL, false, { .b = (x) } })
#define TLANG_VAL_STR(x)  ((tlang_value){ TLANG_KIND_STR, false, { .str = (x) } })
#define TLANG_VAL_NULL    ((tlang_value){ TLANG_KIND_NULL, true, { .i64 = 0 } })

static inline tlang_value tlang_val_opt_i32(tlang_opt_i32 o) {
    tlang_value r;
    r.kind = TLANG_KIND_I32; r.is_null = !o.has; r.v.i64 = 0; r.v.i32 = o.v;
    return r;
}
static inline tlang_value tlang_val_opt_i64(tlang_opt_i64 o) {
    tlang_value r;
    r.kind = TLANG_KIND_I64; r.is_null = !o.has; r.v.i64 = o.v;
    return r;
}
static inline tlang_value tlang_val_opt_f64(tlang_opt_f64 o) {
    tlang_value r;
    r.kind = TLANG_KIND_F64; r.is_null = !o.has; r.v.f64 = o.v;
    return r;
}
static inline tlang_value tlang_val_opt_bool(tlang_opt_bool o) {
    tlang_value r;
    r.kind = TLANG_KIND_BOOL; r.is_null = !o.has; r.v.i64 = 0; r.v.b = o.v;
    return r;
}
static inline tlang_value tlang_val_opt_str(tlang_string s) {
    tlang_value r;
    r.kind = TLANG_KIND_STR; r.is_null = s.data == NULL; r.v.str = s;
    return r;
}
#define TLANG_VAL_OPT_I32(o)  tlang_val_opt_i32(o)
#define TLANG_VAL_OPT_I64(o)  tlang_val_opt_i64(o)
#define TLANG_VAL_OPT_F64(o)  tlang_val_opt_f64(o)
#define TLANG_VAL_OPT_BOOL(o) tlang_val_opt_bool(o)
#define TLANG_VAL_OPT_STR(s)  tlang_val_opt_str(s)

/* ========================================================================
 * 13. Console (DESIGN.md §2.11)
 * ======================================================================== */

/* console.log / console.error: formats the nargs values into one line (values
 * separated by one space, terminated by '\n') and writes it to stdout /
 * stderr with a single write(2), retried on EINTR and short writes (lines up
 * to PIPE_BUF bytes are atomic on pipes). Formatting: STR raw bytes, I32/I64
 * decimal, F64 as tlang_f64_to_string, BOOL true/false, is_null "null".
 * args may be NULL when nargs == 0 (prints an empty line). Uses a stack buffer
 * and a temporary malloc for long lines, never the arena. Blocks the scheduler
 * thread for the duration of the write. Never fails: write errors are ignored.
 * fib may be NULL. */
void tlang_console_log(tlang_fiber* fib, const tlang_value* args, int nargs);
void tlang_console_info(tlang_fiber* fib, const tlang_value* args, int nargs);
void tlang_console_error(tlang_fiber* fib, const tlang_value* args, int nargs);

/* ========================================================================
 * 14. JSON (spec §9, DESIGN.md §2.12)
 * ======================================================================== */

/* Scanner conventions. The scanners read the bytes [*p, end) of a JSON text
 * (normally ctx->body) and never read outside that range. *p must point at
 * the first byte of the token: scanners skip no whitespace, before or after.
 * On success they return true and leave *p just past the token. On failure
 * they return false and *p is unspecified: the generated parser gives up.
 * They never touch fib->err and never abort.
 *
 * Depth convention (limit json_max_depth(), default 32, TLANG_JSON_MAX_DEPTH):
 * the top-level value has depth 0 and every value inside a container at depth
 * d has depth d + 1. A container (object or array) at depth d is accepted only
 * while d < json_max_depth(). A generated parser for an object at depth d
 * fails when d >= json_max_depth(), passes d to json_skip_value for members it
 * skips, and d + 1 to the parsers of nested objects and arrays. */

/* Skips JSON whitespace (space, \t, \n, \r) and returns the first other
 * position, or end. p == end == NULL is allowed. Pure. */
static inline const char* json_skip_ws(const char* p, const char* end) {
    while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r')) p++;
    return p;
}

/* Scans an object key, a JSON string at *p. *key receives the raw bytes
 * between the quotes, zero-copy (lifetime of the input), with escape
 * sequences left undecoded: a key spelled with escapes never equals a field
 * name, so the generated parser skips it as unknown. Fails on a missing
 * quote, an unescaped byte below 0x20, or a malformed escape. */
bool json_scan_key(const char** p, const char* end, tlang_string* key);

/* Scans an integer number: -?(0|[1-9][0-9]*). Fails when the number has a
 * fraction or exponent, is not valid JSON, or does not fit the target type. */
bool json_scan_int64(const char** p, const char* end, int64_t* out);
bool json_scan_int32(const char** p, const char* end, int32_t* out);

/* Scans any JSON number into a double, correctly rounded (strtod on a
 * NUL-terminated copy of the token). Fails on invalid syntax and on overflow
 * to infinity; underflow gives 0 or a subnormal. */
bool json_scan_float64(const char** p, const char* end, double* out);

/* Scans `true` or `false`. */
bool json_scan_bool(const char** p, const char* end, bool* out);

/* Peek for null: when *p starts with the literal `null`, consumes it and
 * returns true; otherwise returns false and leaves *p unchanged (then scan
 * the non-null value). */
bool json_scan_null(const char** p, const char* end);

/* Scans a JSON string at *p into *out. Without escapes *out is a zero-copy
 * slice of the input (lifetime of the input). With escapes (\" \\ \/ \b \f
 * \n \r \t \uXXXX with surrogate pairs) the decoded UTF-8 is written to
 * `arena` (lifetime request); a lone surrogate decodes to U+FFFD. Bytes >= 0x80
 * are passed through without UTF-8 validation. Fails on a missing closing
 * quote, an unescaped byte below 0x20, a malformed escape, or when the arena
 * cannot allocate (returns false, does not abort). */
bool json_scan_string(const char** p, const char* end, MemoryArena* arena, tlang_string* out);

/* Skips one complete JSON value of any type at *p, validating it, including
 * whitespace inside containers. `depth` is the depth of the container the
 * value belongs to (see the depth convention above: a generated parser passes
 * its own depth). Fails on invalid JSON or when a container would reach
 * depth >= json_max_depth(). Recursion is bounded by that limit. */
bool json_skip_value(const char** p, const char* end, int depth);

/* The configured depth limit (TLANG_JSON_MAX_DEPTH, default 32). Set once at
 * startup before scheduler threads start; constant afterwards. Pure. */
int json_max_depth(void);

/* Growable output buffer in the fiber arena, used by the generated JSON
 * writers (and by other runtime code that builds text).
 *   data - arena memory (NULL while cap == 0), not NUL-terminated.
 *   len  - bytes written. cap - bytes allocated.
 *   fib  - owner; growth allocates from fib->arena.
 * Lifetime of the contents: request. Never fails. Aborts on OOM. */
typedef struct tlang_buf {
    char* data;
    size_t len;
    size_t cap;
    tlang_fiber* fib;
} tlang_buf;

/* Initialises *b for fib with room for initial_cap bytes (0 allocates
 * nothing yet). Aborts on OOM. */
void tlang_buf_init(tlang_fiber* fib, tlang_buf* b, size_t initial_cap);

/* Ensures cap - len >= extra: new capacity max(2 * cap, len + extra, 64),
 * contents copied (in place when the block is the arena's last allocation).
 * Aborts on OOM, including when len + extra overflows. */
void tlang_buf_grow(tlang_buf* b, size_t extra);

/* Ensures room for `extra` more bytes, calling tlang_buf_grow only when the
 * buffer is too small. Aborts on OOM. */
static inline void tlang_buf_reserve(tlang_buf* b, size_t extra) {
    if (b->cap - b->len < extra) tlang_buf_grow(b, extra);
}

/* Appends n bytes from src (src may be NULL when n == 0). Aborts on OOM. */
static inline void tlang_buf_put(tlang_buf* b, const void* src, size_t n) {
    if (n == 0) return;
    tlang_buf_reserve(b, n);
    memcpy(b->data + b->len, src, n);
    b->len += n;
}

/* Appends one byte. Aborts on OOM. */
static inline void tlang_buf_putc(tlang_buf* b, char c) {
    tlang_buf_reserve(b, 1);
    b->data[b->len++] = c;
}

/* Appends the raw bytes of s (no escaping). Aborts on OOM. */
static inline void tlang_buf_put_str(tlang_buf* b, tlang_string s) {
    tlang_buf_put(b, s.data, s.len);
}

/* Appends a string literal, e.g. TLANG_BUF_PUT_LIT(b, "{\"id\":"). */
#define TLANG_BUF_PUT_LIT(b, lit) tlang_buf_put((b), "" lit, sizeof("" lit) - 1)

/* The contents as a string sharing the buffer's memory (lifetime request);
 * the static "" when empty. Further writes may move the data, so take the
 * string after the last write. */
static inline tlang_string tlang_buf_string(const tlang_buf* b) {
    tlang_string s;
    if (b->len == 0) return TLANG_STR("");
    s.data = b->data;
    s.len = b->len;
    return s;
}

/* JSON writers: append one JSON value. Never fail. Abort on OOM.
 * json_write_str: quoted string; escapes '"' and '\' as \" and \\, \b \f \n
 * \r \t, every other byte below 0x20 as \u00XX; bytes >= 0x80 are copied
 * unchanged (no UTF-8 validation). */
void json_write_str(tlang_buf* b, tlang_string s);
/* Decimal integer, digit loop, no allocation besides the buffer. */
void json_write_i64(tlang_buf* b, int64_t v);
void json_write_i32(tlang_buf* b, int32_t v);
/* Shortest of %.15g/%.16g/%.17g that round-trips; -0 as 0; NaN and
 * infinities as null (JSON has no representation for them). */
void json_write_f64(tlang_buf* b, double v);

/* `true` / `false`. Aborts on OOM. */
static inline void json_write_bool(tlang_buf* b, bool v) {
    if (v) TLANG_BUF_PUT_LIT(b, "true");
    else TLANG_BUF_PUT_LIT(b, "false");
}

/* `null`, for null optional fields. Aborts on OOM. */
static inline void json_write_null(tlang_buf* b) {
    TLANG_BUF_PUT_LIT(b, "null");
}

/* ========================================================================
 * 15. HTTP request context and routing (spec §8, DESIGN.md §2.11)
 * ======================================================================== */

/* Maximum dynamic segments per route and per request (spec §8.1; more in a
 * pattern is a compile error). */
#define TLANG_MAX_PARAMS 8

/* Capacity of tlang_ctx.headers. TLANG_MAX_HEADERS can lower the request
 * header limit (431 above it) but not raise it past this value. */
#define TLANG_CTX_MAX_HEADERS 32

/* A dynamic route segment recorded by tlang_ctx_match: name is the pattern's
 * parameter name (static), value the raw, not percent-decoded segment bytes
 * (request buffer). */
typedef struct tlang_route_param {
    tlang_string name;
    tlang_string value;
} tlang_route_param;

/* A request header as received: name as sent (case preserved), value with
 * leading and trailing spaces/tabs removed. Both slice the request buffer. */
typedef struct tlang_http_header {
    tlang_string name;
    tlang_string value;
} tlang_http_header;

/* A response header added by tlang_ctx_set_header (arena list, in call
 * order). Runtime-owned. */
typedef struct tlang_resp_header {
    tlang_string name;
    tlang_string value;
    struct tlang_resp_header* next;
} tlang_resp_header;

/* The request context (spec §8.1). The runtime owns it: it lives in the
 * connection fiber and is valid from the dispatcher call until the dispatcher
 * returns. All tlang_string fields have non-NULL data and slice the request
 * buffer (the body may live in the arena): lifetime request.
 * Generated code may read method, path, query (TLang `ctx.rawQuery`, the raw
 * query string after '?', without the '?'), body and response_sent. Every
 * other field is runtime state: read it only through the functions below. */
struct tlang_ctx {
    int client_fd;                 /* client socket */
    tlang_fiber* fiber;            /* fiber serving the request (== __fib) */
    MemoryArena* arena;            /* that fiber's arena (== __fib->arena) */
    tlang_string method;           /* request method, e.g. "GET" (case kept) */
    tlang_string path;             /* request path, raw (not percent-decoded) */
    tlang_string query;            /* raw query string, "" when absent */
    tlang_string body;             /* request body, "" when absent */
    tlang_route_param params[TLANG_MAX_PARAMS]; /* set by tlang_ctx_match */
    size_t param_count;
    tlang_http_header headers[TLANG_CTX_MAX_HEADERS]; /* in arrival order */
    size_t num_headers;
    bool response_sent;            /* a response was written or queued */
    /* Runtime state below this line (http.c). */
    bool keep_alive;               /* keep the connection after this request */
    uint8_t http_minor;            /* 0 for HTTP/1.0, 1 for HTTP/1.1 */
    int32_t status;                /* status of the response sent, 0 before */
    tlang_resp_header* resp_headers;      /* tlang_ctx_set_header list head */
    tlang_resp_header* resp_headers_tail; /* list tail for O(1) append */
    void* conn;                    /* private connection object of http.c */
};

/* `ctx.header(name)`: value of the first request header whose name equals
 * `name` ASCII case-insensitively, or "" (static) when there is none.
 * Lifetime request. Pure. */
tlang_string tlang_ctx_header(const tlang_ctx* ctx, tlang_string name);

/* `ctx.query(name)` (spec §8.3): scans ctx->query (`k=v&k2=v2`) linearly and
 * returns the value of the first parameter whose raw key equals `name`
 * byte for byte (keys are not decoded). A key without '=' has the value "".
 * The value is a zero-copy slice of the request buffer unless it contains
 * '%' or '+'; then it is decoded into the arena ('+' becomes a space, %XX a
 * byte; an invalid escape is kept literally). "" (static) when absent.
 * Lifetime request. Never fails. Aborts on OOM. */
tlang_string tlang_ctx_query(tlang_fiber* fib, tlang_ctx* ctx, tlang_string name);

/* `ctx.param(name)`: raw value of the dynamic segment `name` recorded by the
 * last successful tlang_ctx_match, or "" (static) when absent. Lifetime
 * request. Pure. */
tlang_string tlang_ctx_param(const tlang_ctx* ctx, tlang_string name);

/* `ctx.paramInt(name)` (spec §8.2): tlang_str_to_int of tlang_ctx_param,
 * without a temporary string. May fail: 400 "invalid integer" when the
 * parameter is absent, not an integer, or out of int64 range; returns 0. */
int64_t tlang_ctx_param_int(tlang_fiber* fib, const tlang_ctx* ctx, tlang_string name);

/* `ctx.text(status, body)`: sends a response with Content-Type
 * "text/plain; charset=utf-8" (unless tlang_ctx_set_header supplied one),
 * Content-Length, Date, Connection, and the headers added by
 * tlang_ctx_set_header. Statuses outside 200..599 are replaced by 500 and
 * logged; 204 and 304 send no body; a HEAD request gets the headers only.
 * The response reaches the socket before the next request is read (the
 * runtime may write it at once, suspending the fiber, or when the dispatcher
 * returns). A second response on the same request is ignored and logged.
 * Sets ctx->response_sent. `body` must stay valid until the request ends.
 * Never fails: a write error or timeout closes the connection after the
 * dispatcher returns. Aborts on OOM. */
void tlang_ctx_text(tlang_fiber* fib, tlang_ctx* ctx, int32_t status, tlang_string body);

/* `ctx.json(status, v)` (spec §9.2): like tlang_ctx_text with Content-Type
 * "application/json". `json` is the serialized body produced by a generated
 * writer (tlang_buf_string). */
void tlang_ctx_json(tlang_fiber* fib, tlang_ctx* ctx, int32_t status, tlang_string json);

/* Like tlang_ctx_text with an explicit Content-Type (used by the two above;
 * a Content-Type set with tlang_ctx_set_header still wins). */
void tlang_ctx_send(tlang_fiber* fib, tlang_ctx* ctx, int32_t status,
                    tlang_string content_type, tlang_string body);

/* `ctx.setHeader(name, value)`: adds a header to the response this request
 * will send; repeated names are all sent, in call order. A Content-Type or
 * Date set here replaces the runtime default. Ignored (and logged) after the
 * response was sent. name and value must stay valid until the request ends.
 * May fail: 500 "invalid response header" when name is not an RFC 9110 token,
 * value contains CR, LF, NUL or another control byte except HTAB, or name is
 * Content-Length, Transfer-Encoding or Connection (managed by the runtime).
 * Aborts on OOM. */
void tlang_ctx_set_header(tlang_fiber* fib, tlang_ctx* ctx, tlang_string name, tlang_string value);

/* Route segment kinds. */
enum {
    TLANG_SEG_STATIC = 0,   /* matches text byte for byte */
    TLANG_SEG_PARAM = 1     /* matches one non-empty segment, recorded as text */
};

/* One '/'-separated segment of a route pattern. For TLANG_SEG_STATIC, text is
 * the literal bytes; for TLANG_SEG_PARAM, text is the parameter name without
 * the ':'. */
typedef struct tlang_route_seg {
    int kind;
    tlang_string text;
} tlang_route_seg;

/* A compiled `ctx.match(method, pattern)` (spec §8.2). Codegen emits one static
 * const table per call site:
 *   static const tlang_route_seg __tl_r1_segs[2] = {
 *       { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
 *       { TLANG_SEG_PARAM, TLANG_STR_INIT("id") } };
 *   static const tlang_route __tl_r1 = { TLANG_STR_INIT("GET"), __tl_r1_segs, 2, 1 };
 * The pattern "/" has nsegs == 0 and segs == NULL. nparams counts the
 * TLANG_SEG_PARAM segments (at most TLANG_MAX_PARAMS). Lifetime static. */
typedef struct tlang_route {
    tlang_string method;            /* compared byte for byte (case-sensitive) */
    const tlang_route_seg* segs;
    uint32_t nsegs;
    uint32_t nparams;
} tlang_route;

/* `ctx.match(method, pattern)`: true when ctx->method equals route->method and
 * ctx->path splits into exactly route->nsegs segments that match. The path
 * after its leading '/' is split on '/': "/" has 0 segments, "/users/42" has
 * 2, "/users/42/" has 3 (the last one empty). Static segments compare bytes,
 * parameter segments match any non-empty segment. On success
 * ctx->params[0..nparams-1] and ctx->param_count are overwritten; on failure
 * they are left unchanged. A route with more than TLANG_MAX_PARAMS parameters
 * never matches. Never fails. */
bool tlang_ctx_match(tlang_ctx* ctx, const tlang_route* route);

/* ========================================================================
 * 16. Database (spec §11, DESIGN.md §2.11)
 * ======================================================================== */

/* Database parameters are tagged values (section 12). The TLANG_PG_*
 * constructors are the TLANG_VAL_* constructors under the names the spec
 * uses. Suggested wire encoding (pg.c decides): int32/int64/float64/bool in
 * binary format with OIDs 23/20/701/16, strings in text format with OID 0
 * (type inferred by the server) and a NUL-terminated arena copy (spec §11.2),
 * is_null as a NULL parameter. */
typedef tlang_value tlang_pg_param;
#define TLANG_PG_I32(x)      TLANG_VAL_I32(x)
#define TLANG_PG_I64(x)      TLANG_VAL_I64(x)
#define TLANG_PG_F64(x)      TLANG_VAL_F64(x)
#define TLANG_PG_BOOL(x)     TLANG_VAL_BOOL(x)
#define TLANG_PG_STR(x)      TLANG_VAL_STR(x)
#define TLANG_PG_OPT_I32(o)  TLANG_VAL_OPT_I32(o)
#define TLANG_PG_OPT_I64(o)  TLANG_VAL_OPT_I64(o)
#define TLANG_PG_OPT_F64(o)  TLANG_VAL_OPT_F64(o)
#define TLANG_PG_OPT_BOOL(o) TLANG_VAL_OPT_BOOL(o)
#define TLANG_PG_OPT_STR(s)  TLANG_VAL_OPT_STR(s)
#define TLANG_PG_NULL        TLANG_VAL_NULL

/* One field of a DB-mapped interface: name (static) is the TLang field name,
 * kind a TLANG_KIND_* (base or OPT_*) giving the C storage type, offset the
 * offsetof() of the field in the struct. */
typedef struct tlang_field_desc {
    tlang_string name;
    int kind;
    size_t offset;
} tlang_field_desc;

/* Row descriptor emitted by codegen per DB-mapped interface (per generic
 * instantiation), lifetime static:
 *   static const tlang_field_desc __tl_fd_User[2] = {
 *       { TLANG_STR_INIT("id"), TLANG_KIND_I64, offsetof(tl_User, f_id) },
 *       { TLANG_STR_INIT("email"), TLANG_KIND_OPT_STR, offsetof(tl_User, f_email) } };
 *   static const tlang_type_desc __tl_td_User =
 *       { TLANG_STR_INIT("User"), sizeof(tl_User), 2, __tl_fd_User, NULL };
 * Mapping rules (pg.c): a column maps to the field whose name equals the
 * column name exactly, else ASCII case-insensitively (PostgreSQL folds
 * unquoted aliases to lower case). Columns without a field are ignored.
 * Text-format values are parsed into the field type (int range checked;
 * bool "t"/"f"; float via strtod). SQL NULL is allowed only for OPT_* kinds.
 * A non-optional field with no matching column, a NULL in a non-optional
 * field, or an unparsable value fails the call with 500. Optional fields
 * without a column are null. Strings are copied into the arena. Fields that
 * are not listed (nested interfaces, arrays) keep what new_row gave them. */
typedef struct tlang_type_desc {
    tlang_string name;                  /* interface name, for messages */
    size_t size;                        /* sizeof the generated struct */
    size_t nfields;
    const tlang_field_desc* fields;     /* nfields entries */
    /* Optional: allocates one row exactly like `new T()` (arena). NULL makes
     * the runtime use tlang_alloc_zeroed(fib, size). */
    void* (*new_row)(tlang_fiber* fib);
} tlang_type_desc;

/* A transaction handle (spec §11.4). Generated code declares it on the stack
 * (`TxContext __tx_1;`), passes its address and never reads its fields.
 * tlang_tx_begin initialises every field, so it may start uninitialised. The
 * pooled connection it holds is also registered as a fiber cleanup hook, so
 * a request abort releases it even though the stack frame is gone. */
struct tlang_tx {
    struct tlang_pg_conn* conn;   /* held pooled connection, NULL when none */
    int state;                    /* TLANG_TX_* */
};

/* Spec name of the transaction handle. */
typedef tlang_tx TxContext;

/* Transaction states. */
enum {
    TLANG_TX_NONE = 0,     /* begin failed, or not begun */
    TLANG_TX_ACTIVE = 1,   /* BEGIN succeeded, connection held */
    TLANG_TX_DONE = 2      /* committed or rolled back, connection released */
};

/* Common contract of the three calls below:
 * - tx == NULL runs the statement on a connection taken from the scheduler's
 *   pool and returned before the call returns (autocommit). A non-NULL tx
 *   must be TLANG_TX_ACTIVE; otherwise the call fails with 500 "transaction
 *   is not active".
 * - sql must come from a string literal (TLANG_STR): the runtime relies on
 *   sql.data[sql.len] == '\0' and on the bytes never changing; it is also the
 *   prepared-statement cache key (spec §11.1).
 * - params points at nparams values (NULL when nparams == 0), $1..$n in order.
 * - The fiber is suspended while the database works; other fibers run.
 * May fail: 503 "database pool timeout" (no connection within
 * TLANG_DB_POOL_TIMEOUT_MS), 503 "database unavailable" (connect failed),
 * 500 with the server's message for SQL errors and mapping errors, 499
 * "client disconnected" when the HTTP client goes away while the query runs
 * (the query is cancelled), 500 "database support not compiled in" in
 * TLANG_NO_PG builds. Aborts on OOM. */

/* `db.execute(sql, args...)` / `tx.execute(...)`: runs a statement and
 * returns the number of rows it affected (0 for statements that report
 * none). Returns 0 on failure. */
int64_t tlang_db_execute(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams);

/* `db.query<T>(sql, args...)`: runs a query and returns a new arena slice of
 * row pointers, laid out as tlang_slice_<m> for the element type T* (cast it:
 * `(tlang_slice_User*)tlang_db_query(...)`), one row per result row, each
 * row allocated per desc (see tlang_type_desc). Lifetime request. Returns
 * NULL on failure. */
void* tlang_db_query(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                     const tlang_pg_param* params, int nparams,
                     const tlang_type_desc* desc);

/* `db.queryOne<T>(sql, args...)`: the first result row mapped per desc
 * (lifetime request), or NULL when the result is empty. Further rows are
 * ignored. Returns NULL on failure too: test fib->err to tell them apart. */
void* tlang_db_query_one(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams,
                         const tlang_type_desc* desc);

/* Starts `db.transaction((tx) => { ... })`: initialises *tx, takes a pooled
 * connection (waiting up to TLANG_DB_POOL_TIMEOUT_MS) and runs BEGIN.
 * Returns true with tx ACTIVE. May fail (same statuses as above): returns
 * false with tx NONE and no connection held; generated code then jumps
 * straight to __tx_fail_N without calling tlang_tx_rollback. */
bool tlang_tx_begin(tlang_fiber* fib, tlang_tx* tx);

/* `tx.execute(...)`, `tx.query<T>(...)`, `tx.queryOne<T>(...)`: the db calls
 * on the transaction's connection. */
static inline int64_t tlang_tx_exec(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                                    const tlang_pg_param* params, int nparams) {
    return tlang_db_execute(fib, tx, sql, params, nparams);
}
static inline void* tlang_tx_query(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                                   const tlang_pg_param* params, int nparams,
                                   const tlang_type_desc* desc) {
    return tlang_db_query(fib, tx, sql, params, nparams, desc);
}
static inline void* tlang_tx_query_one(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                                       const tlang_pg_param* params, int nparams,
                                       const tlang_type_desc* desc) {
    return tlang_db_query_one(fib, tx, sql, params, nparams, desc);
}

/* Runs COMMIT. Returns true when it succeeded: the connection is back in the
 * pool and tx is DONE. May fail: returns false with the connection still
 * held, and generated code then jumps to __tx_rollback_N. */
bool tlang_tx_commit(tlang_fiber* fib, tlang_tx* tx);

/* Rolls back if tx still holds a connection (ROLLBACK, or closing the
 * connection when its state is unknown, e.g. after a cancelled query) and
 * always returns the connection to the pool; tx becomes DONE. Idempotent:
 * no-op for NONE and DONE. Never fails: it preserves the pending error
 * (fib->err / fib->error stay as they were) and only logs its own problems. */
void tlang_tx_rollback(tlang_fiber* fib, tlang_tx* tx);

/* ========================================================================
 * 17. Program entry (DESIGN.md §2.10, §3.3)
 * ======================================================================== */

/* Description of the generated program, a static const object:
 *   static const tlang_program __tl_program = {
 *       .init_globals = tl__init_globals, .globals_size = sizeof(struct tl_globals),
 *       .dispatcher = tl_f_route_dispatcher, .main = NULL, .uses_db = true };
 * Exactly one of dispatcher and main must be non-NULL. */
typedef struct tlang_program {
    /* Runs the global initialisers in declaration order. Called once per
     * scheduler thread, in a startup fiber whose arena is never reset, with
     * fib->globals == globals pointing at globals_size zeroed bytes, before
     * the scheduler serves requests. An error left in fib->err stops the
     * process (exit status 1). NULL when the program has no globals. */
    void (*init_globals)(tlang_fiber* fib, void* globals);
    /* sizeof(struct tl_globals), 0 without globals (then generated code does
     * not define the struct: ISO C has no empty structs). */
    size_t globals_size;
    /* Server mode: `void tl_f_route_dispatcher(tlang_fiber* __fib,
     * tlang_ctx* l_ctx)`, called once per request on the connection fiber. */
    void (*dispatcher)(tlang_fiber* fib, tlang_ctx* ctx);
    /* Script mode: `void tl_f_main(tlang_fiber* __fib)`, run once in a fiber. */
    void (*main)(tlang_fiber* fib);
    /* True when the program references `db`: the runtime then creates the
     * per-scheduler pools and requires TLANG_DATABASE_URL (or DATABASE_URL). */
    bool uses_db;
} tlang_program;

/* The C main of every generated program: `return tlang_main(argc, argv,
 * &__tl_program);`. Reads the configuration from the environment
 * (DESIGN.md §4.2), ignores SIGPIPE, turns SIGINT/SIGTERM into a graceful
 * stop, then runs the server (TLANG_THREADS schedulers, each with its own
 * SO_REUSEPORT listener, globals and DB pool) or the script. argv is not
 * interpreted in v1. Returns the process exit status: 0 on success (server
 * stopped by a signal, or script finished), 1 when an error escaped main or
 * startup failed (bind error, global initialiser error, database URL missing),
 * 2 for an invalid configuration or program description. */
int tlang_main(int argc, char** argv, const tlang_program* prog);

#endif /* TLANG_H */
