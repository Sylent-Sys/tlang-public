/*
 * test_header.c - checks include/tlang.h on its own.
 *
 * Compile-time: the header is self-contained (it is included first), layouts
 * the generic helpers rely on, and the code shapes generated code uses
 * (slice defines, static route and row tables, the program descriptor,
 * `__fib->globals->g_x`). Run time: the header-defined helpers that call no
 * runtime function. The test references no symbol of libtlangrt.a, so it
 * links even while src/ is empty. The Makefile builds it with -pedantic
 * -Werror (gcc, clang) or -Werror (tcc).
 */
#include "tlang.h"

#include <stdio.h>
#include <stdlib.h>

/* ---- shapes generated code emits (DESIGN.md §3.2, §3.3) ---- */

typedef struct tl_User tl_User;
TLANG_SLICE_DEFINE(User, tl_User*)
TLANG_SLICE_DEFINE(arr_User, tlang_slice_User*)
TLANG_SLICE_DEFINE(opt_i64, tlang_opt_i64)

struct tl_User {
    int64_t f_id;
    tlang_string f_name;
    tlang_string f_email;          /* email?: string */
    tlang_opt_i32 f_age;           /* age?: int32 */
    tlang_slice_User* f_friends;   /* friends: User[] */
};

struct tl_globals {
    int64_t g_counter;
    tlang_slice_str* g_names;
};

static void tl__init_globals(tlang_fiber* __fib, void* globals) {
    struct tl_globals* g = globals;
    g->g_counter = 1;
    __fib->globals->g_counter += 1;    /* DESIGN.md §3.2 access path */
}

static void tl_f_route_dispatcher(tlang_fiber* __fib, tlang_ctx* l_ctx) {
    (void)__fib;
    (void)l_ctx;
}

static const tlang_program __tl_program = {
    .init_globals = tl__init_globals,
    .globals_size = sizeof(struct tl_globals),
    .dispatcher = tl_f_route_dispatcher,
    .main = NULL,
    .uses_db = true,
};

static const tlang_route_seg __tl_r1_segs[2] = {
    { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
    { TLANG_SEG_PARAM, TLANG_STR_INIT("id") },
};
static const tlang_route __tl_r1 = { TLANG_STR_INIT("GET"), __tl_r1_segs, 2, 1 };
static const tlang_route __tl_r_root = { TLANG_STR_INIT("GET"), NULL, 0, 0 };

static const tlang_field_desc __tl_fd_User[4] = {
    { TLANG_STR_INIT("id"), TLANG_KIND_I64, offsetof(tl_User, f_id) },
    { TLANG_STR_INIT("name"), TLANG_KIND_STR, offsetof(tl_User, f_name) },
    { TLANG_STR_INIT("email"), TLANG_KIND_OPT_STR, offsetof(tl_User, f_email) },
    { TLANG_STR_INIT("age"), TLANG_KIND_OPT_I32, offsetof(tl_User, f_age) },
};
static const tlang_type_desc __tl_td_User = {
    TLANG_STR_INIT("User"), sizeof(tl_User), 4, __tl_fd_User, NULL
};

static const tlang_string k_static = TLANG_STR_INIT("static");

/* ---- layout assertions ---- */

#define SAME_SLICE_LAYOUT(a, b)                                      \
    (sizeof(a) == sizeof(b) && offsetof(a, items) == offsetof(b, items) && \
     offsetof(a, len) == offsetof(b, len) && offsetof(a, cap) == offsetof(b, cap) && \
     offsetof(a, global) == offsetof(b, global))

_Static_assert(SAME_SLICE_LAYOUT(tlang_slice_i32, tlang_slice_User), "slice layout User");
_Static_assert(SAME_SLICE_LAYOUT(tlang_slice_i64, tlang_slice_arr_User), "slice layout arr_User");
_Static_assert(SAME_SLICE_LAYOUT(tlang_slice_bool, tlang_slice_opt_i64), "slice layout opt_i64");
_Static_assert(SAME_SLICE_LAYOUT(tlang_slice_f64, tlang_slice_str), "slice layout f64/str");
_Static_assert(offsetof(tlang_slice_i32, items) == 0, "items first");

_Static_assert(sizeof(tlang_string) == 2 * sizeof(void*), "tlang_string is a fat pointer");
_Static_assert(offsetof(tlang_string, len) == sizeof(void*), "tlang_string.len");
_Static_assert(offsetof(struct tlang_fiber, err) == 0, "fiber err first");
_Static_assert(ARENA_ALIGNMENT == 8, "spec §6.2 alignment");
_Static_assert(ARENA_CHUNK_SIZE == 128 * 1024, "spec §6.2 chunk size");
_Static_assert(offsetof(ArenaChunk, data) % ARENA_ALIGNMENT == 0, "chunk data aligned");
_Static_assert(TLANG_MAX_PARAMS == 8, "spec §8.1 params");
_Static_assert(sizeof(((tlang_ctx*)0)->params) / sizeof(tlang_route_param) == TLANG_MAX_PARAMS,
               "ctx params capacity");
_Static_assert(sizeof(((tlang_ctx*)0)->headers) / sizeof(tlang_http_header) == TLANG_CTX_MAX_HEADERS,
               "ctx headers capacity");
_Static_assert(sizeof(TxContext) == sizeof(tlang_tx), "TxContext is tlang_tx");
_Static_assert(sizeof(tlang_pg_param) == sizeof(tlang_value), "pg param is a tagged value");
_Static_assert(sizeof(tlang_value) > 16, "tlang_value is passed in memory (TCC ABI note)");
_Static_assert((TLANG_KIND_OPT_I32 & ~TLANG_KIND_OPT) == TLANG_KIND_I32 &&
               (TLANG_KIND_OPT_STR & ~TLANG_KIND_OPT) == TLANG_KIND_STR, "optional kind flag");
_Static_assert(TLANG_SEG_STATIC != TLANG_SEG_PARAM, "segment kinds");
_Static_assert(TLANG_STR_END == INT64_MAX, "slice end sentinel");

/* ---- run-time checks ---- */

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

static bool str_is(tlang_string s, const char* expect) {
    size_t n = strlen(expect);
    return s.data != NULL && s.len == n && memcmp(s.data, expect, n) == 0;
}

static void test_strings(void) {
    tlang_string a = TLANG_STR("abc");
    tlang_string nul = TLANG_STR("a\0b");
    tlang_string empty = TLANG_STR("");
    tlang_string none = TLANG_STR_NULL;
    tlang_string hello = TLANG_STR("hello");
    tlang_string s;

    CHECK(a.len == 3 && memcmp(a.data, "abc", 3) == 0);
    CHECK(nul.len == 3 && nul.data[1] == '\0');
    CHECK(empty.len == 0 && empty.data != NULL);
    CHECK(none.data == NULL && none.len == 0);
    CHECK(str_is(k_static, "static"));

    CHECK(tlang_str_eq(a, TLANG_STR("abc")));
    CHECK(!tlang_str_eq(a, TLANG_STR("abd")));
    CHECK(!tlang_str_eq(a, TLANG_STR("ab")));
    CHECK(tlang_str_eq(none, empty));
    CHECK(tlang_str_eq(nul, TLANG_STR("a\0b")));
    CHECK(!tlang_str_eq(nul, TLANG_STR("a\0c")));

    CHECK(tlang_str_starts_with(hello, TLANG_STR("he")));
    CHECK(tlang_str_starts_with(hello, empty));
    CHECK(tlang_str_starts_with(none, none));
    CHECK(!tlang_str_starts_with(TLANG_STR("he"), hello));
    CHECK(tlang_str_ends_with(hello, TLANG_STR("llo")));
    CHECK(tlang_str_ends_with(hello, none));
    CHECK(!tlang_str_ends_with(hello, TLANG_STR("hel")));

    /* JavaScript String.prototype.slice semantics on bytes. */
    CHECK(str_is(tlang_str_slice(hello, 1, 3), "el"));
    CHECK(str_is(tlang_str_slice(hello, -3, TLANG_STR_END), "llo"));
    CHECK(str_is(tlang_str_slice(hello, 2, -1), "ll"));
    CHECK(str_is(tlang_str_slice(hello, 4, 2), ""));
    CHECK(str_is(tlang_str_slice(hello, -100, 100), "hello"));
    CHECK(str_is(tlang_str_slice(hello, 5, TLANG_STR_END), ""));
    CHECK(str_is(tlang_str_slice(hello, INT64_MIN, INT64_MAX), "hello"));
    CHECK(str_is(tlang_str_slice(none, 0, 3), ""));
    s = tlang_str_slice(hello, 0, 5);
    CHECK(s.data == hello.data && s.len == 5);

    s = tlang_str_some(none);
    CHECK(s.data != NULL && s.len == 0);
    s = tlang_str_some(a);
    CHECK(s.data == a.data && s.len == 3);

    CHECK(str_is(tlang_bool_to_string(NULL, true), "true"));
    CHECK(str_is(tlang_bool_to_string(NULL, false), "false"));
}

static void test_numbers(void) {
    double nan = strtod("nan", NULL);
    double inf = strtod("inf", NULL);

    CHECK(tlang_add_i32(INT32_MAX, 1) == INT32_MIN);
    CHECK(tlang_sub_i32(INT32_MIN, 1) == INT32_MAX);
    CHECK(tlang_mul_i32(INT32_MAX, 2) == -2);
    CHECK(tlang_mul_i32(65536, 65536) == 0);
    CHECK(tlang_neg_i32(INT32_MIN) == INT32_MIN);
    CHECK(tlang_neg_i32(5) == -5);
    CHECK(tlang_add_i64(INT64_MAX, 1) == INT64_MIN);
    CHECK(tlang_sub_i64(INT64_MIN, 1) == INT64_MAX);
    CHECK(tlang_mul_i64(INT64_MAX, 2) == -2);
    CHECK(tlang_mul_i64(INT64_C(4294967296), INT64_C(4294967296)) == 0);
    CHECK(tlang_neg_i64(INT64_MIN) == INT64_MIN);
    CHECK(tlang_add_i64(-7, 3) == -4);

    CHECK(tlang_i64_to_i32(INT64_C(4294967297)) == 1);
    CHECK(tlang_i64_to_i32(INT64_C(2147483648)) == INT32_MIN);
    CHECK(tlang_i64_to_i32(-1) == -1);

    CHECK(tlang_f64_to_i32(nan) == 0);
    CHECK(tlang_f64_to_i32(inf) == INT32_MAX);
    CHECK(tlang_f64_to_i32(-inf) == INT32_MIN);
    CHECK(tlang_f64_to_i32(3e9) == INT32_MAX);
    CHECK(tlang_f64_to_i32(-3e9) == INT32_MIN);
    CHECK(tlang_f64_to_i32(-2.9) == -2);
    CHECK(tlang_f64_to_i32(2147483646.9) == 2147483646);
    CHECK(tlang_f64_to_i64(nan) == 0);
    CHECK(tlang_f64_to_i64(1e19) == INT64_MAX);
    CHECK(tlang_f64_to_i64(-1e19) == INT64_MIN);
    CHECK(tlang_f64_to_i64(9223372036854775807.0) == INT64_MAX);
    CHECK(tlang_f64_to_i64(-9223372036854775808.0) == INT64_MIN);
    CHECK(tlang_f64_to_i64(-1.5) == -1);
    CHECK(tlang_f64_to_i64(4503599627370497.0) == INT64_C(4503599627370497));
}

static void test_optionals_and_values(void) {
    tlang_opt_i64 some = TLANG_SOME(i64, 42);
    tlang_opt_bool none_b = TLANG_NONE(bool);
    tlang_opt_f64 some_f = TLANG_SOME(f64, 1.5);
    tlang_opt_i32 none_i = TLANG_NONE(i32);
    tlang_value vals[7] = {
        TLANG_VAL_I32(-1), TLANG_VAL_I64(INT64_MIN), TLANG_VAL_F64(2.5),
        TLANG_VAL_BOOL(true), TLANG_VAL_STR(TLANG_STR("x")), TLANG_VAL_NULL,
        TLANG_PG_OPT_STR(TLANG_STR_NULL),
    };
    tlang_pg_param params[4] = {
        TLANG_PG_OPT_I64(some), TLANG_PG_OPT_BOOL(none_b),
        TLANG_PG_OPT_F64(some_f), TLANG_PG_OPT_I32(none_i),
    };

    CHECK(some.has && some.v == 42);
    CHECK(!none_b.has && !none_b.v);
    CHECK(some_f.has && some_f.v == 1.5);

    CHECK(vals[0].kind == TLANG_KIND_I32 && !vals[0].is_null && vals[0].v.i32 == -1);
    CHECK(vals[1].kind == TLANG_KIND_I64 && vals[1].v.i64 == INT64_MIN);
    CHECK(vals[2].kind == TLANG_KIND_F64 && vals[2].v.f64 == 2.5);
    CHECK(vals[3].kind == TLANG_KIND_BOOL && vals[3].v.b);
    CHECK(vals[4].kind == TLANG_KIND_STR && str_is(vals[4].v.str, "x"));
    CHECK(vals[5].kind == TLANG_KIND_NULL && vals[5].is_null);
    CHECK(vals[6].kind == TLANG_KIND_STR && vals[6].is_null);

    CHECK(params[0].kind == TLANG_KIND_I64 && !params[0].is_null && params[0].v.i64 == 42);
    CHECK(params[1].kind == TLANG_KIND_BOOL && params[1].is_null);
    CHECK(params[2].kind == TLANG_KIND_F64 && !params[2].is_null && params[2].v.f64 == 1.5);
    CHECK(params[3].kind == TLANG_KIND_I32 && params[3].is_null);
}

static void test_json_inline(void) {
    const char text[] = " \t\r\n {}";
    const char* end = text + sizeof(text) - 1;
    const char* p = json_skip_ws(text, end);
    tlang_buf b;

    CHECK(p == text + 5 && *p == '{');
    CHECK(json_skip_ws(end, end) == end);
    CHECK(json_skip_ws(NULL, NULL) == NULL);

    memset(&b, 0, sizeof b);
    CHECK(str_is(tlang_buf_string(&b), ""));
}

static void test_shapes(void) {
    struct tl_globals g;
    struct tlang_fiber fib;
    TxContext tx;
    tlang_tx* txp = &tx;
    tlang_ctx ctx;
    tlang_slice_User users;
    tl_User u;

    memset(&g, 0, sizeof g);
    memset(&fib, 0, sizeof fib);
    fib.globals = &g;
    __tl_program.init_globals(&fib, fib.globals);
    CHECK(g.g_counter == 2);
    CHECK(__tl_program.dispatcher == tl_f_route_dispatcher && __tl_program.main == NULL);
    CHECK(__tl_program.globals_size == sizeof(struct tl_globals) && __tl_program.uses_db);

    memset(&tx, 0, sizeof tx);
    CHECK(txp->state == TLANG_TX_NONE && txp->conn == NULL);

    memset(&ctx, 0, sizeof ctx);
    ctx.query = TLANG_STR("a=1");
    CHECK(str_is(ctx.query, "a=1") && !ctx.response_sent);

    CHECK(__tl_r1.nsegs == 2 && __tl_r1.nparams == 1 && __tl_r1.segs[1].kind == TLANG_SEG_PARAM);
    CHECK(str_is(__tl_r1.segs[0].text, "users") && str_is(__tl_r1.method, "GET"));
    CHECK(__tl_r_root.nsegs == 0 && __tl_r_root.segs == NULL);

    CHECK(__tl_td_User.nfields == 4 && __tl_td_User.size == sizeof(tl_User));
    CHECK(__tl_td_User.fields[3].offset == offsetof(tl_User, f_age));
    CHECK(__tl_td_User.fields[2].kind == (TLANG_KIND_OPT | TLANG_KIND_STR));

    /* Field-wise slice use, as generated code does after tlang_slice_new. */
    memset(&u, 0, sizeof u);
    memset(&users, 0, sizeof users);
    {
        tl_User* storage[2];
        users.items = storage;
        users.cap = 2;
        users.items[users.len++] = &u;
        CHECK(users.len == 1 && users.items[0] == &u && !users.global);
    }
}

int main(void) {
    test_strings();
    test_numbers();
    test_optionals_and_values();
    test_json_inline();
    test_shapes();
    if (failures != 0) {
        printf("FAIL test_header (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_header\n");
    return 0;
}
