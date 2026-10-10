/*
 * test_strings.c - unit tests for src/strings.c (string ops, number
 * formatting/parsing, tlang_buf). Each check exercises real behavior and would
 * fail if the implementation were reverted.
 */
#include "tlang_internal.h"

#include <math.h>
#include <setjmp.h>
#include <stdio.h>
#include <stdlib.h>

/* ---- test harness ---- */

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* The arena and allocation/abort primitives are provided by the library now
 * (arena.c / fiber.c). The test fiber is a real Fiber so the OOM-abort path
 * (tlang_alloc_failed -> tlang_request_abort -> longjmp f->abort_jmp) works;
 * g_fib names its public view. */
static MemoryArena g_arena;
static bool g_arena_live;
static Fiber g_f;
#define g_fib (g_f.pub)

static void arena_free(void) {
    if (g_arena_live) {
        tlang_arena_destroy(&g_arena);
        g_arena_live = false;
    }
}

static void arena_setup(size_t cap) {
    (void)cap;  /* the real arena sizes its first chunk from ARENA_CHUNK_SIZE */
    arena_free();  /* free any arena from a previous test (no leaks under ASan) */
    if (tlang_arena_init(&g_arena) != 0) { perror("tlang_arena_init"); exit(2); }
    g_arena_live = true;
    memset(&g_f, 0, sizeof g_f);
    g_f.pub.arena = &g_arena;
}

static void arena_reset_local(void) {
    arena_reset(&g_arena);
}

static bool str_is(tlang_string s, const char* expect) {
    size_t n = strlen(expect);
    return s.data != NULL && s.len == n && memcmp(s.data, expect, n) == 0;
}

/* ---- tests ---- */

static void test_fmt_i64(void) {
    char buf[TLANG_FMT_I64_MAX];
    CHECK(tlang_fmt_i64(buf, 0) == 1 && str_is((tlang_string){ buf, 1 }, "0"));
    CHECK(tlang_fmt_i64(buf, 42) == 2 && memcmp(buf, "42", 2) == 0);
    CHECK(tlang_fmt_i64(buf, -7) == 2 && memcmp(buf, "-7", 2) == 0);
    tlang_fmt_i64(buf, INT64_MAX);
    CHECK(strcmp(buf, "9223372036854775807") == 0);
    tlang_fmt_i64(buf, INT64_MIN);  /* the hard case: negate without UB */
    CHECK(strcmp(buf, "-9223372036854775808") == 0);
}

static void test_parse_i64(void) {
    int64_t v;
    CHECK(tlang_parse_i64("0", 1, &v) && v == 0);
    CHECK(tlang_parse_i64("+123", 4, &v) && v == 123);
    CHECK(tlang_parse_i64("-123", 4, &v) && v == -123);
    CHECK(tlang_parse_i64("9223372036854775807", 19, &v) && v == INT64_MAX);
    CHECK(tlang_parse_i64("-9223372036854775808", 20, &v) && v == INT64_MIN);
    /* Overflow must be rejected without signed overflow. */
    CHECK(!tlang_parse_i64("9223372036854775808", 19, &v));
    CHECK(!tlang_parse_i64("-9223372036854775809", 20, &v));
    CHECK(!tlang_parse_i64("99999999999999999999", 20, &v));
    CHECK(!tlang_parse_i64("", 0, &v));
    CHECK(!tlang_parse_i64("+", 1, &v));
    CHECK(!tlang_parse_i64("-", 1, &v));
    CHECK(!tlang_parse_i64("12a", 3, &v));
    CHECK(!tlang_parse_i64(" 12", 3, &v));
    CHECK(!tlang_parse_i64("0x10", 4, &v));
}

static bool roundtrips(double v) {
    char buf[TLANG_FMT_F64_MAX];
    tlang_fmt_f64(buf, v, false);
    return strtod(buf, NULL) == v;
}

static void test_fmt_f64(void) {
    char buf[TLANG_FMT_F64_MAX];

    tlang_fmt_f64(buf, 0.1, false);
    CHECK(strcmp(buf, "0.1") == 0);
    tlang_fmt_f64(buf, 0.1 + 0.2, false);
    CHECK(strcmp(buf, "0.30000000000000004") == 0);
    tlang_fmt_f64(buf, 1e21, false);
    CHECK(strcmp(buf, "1e+21") == 0);
    tlang_fmt_f64(buf, -0.0, false);  /* -0 prints as 0 */
    CHECK(strcmp(buf, "0") == 0);
    tlang_fmt_f64(buf, 0.0, false);
    CHECK(strcmp(buf, "0") == 0);
    tlang_fmt_f64(buf, 3.0, false);
    CHECK(strcmp(buf, "3") == 0);

    /* Non-finite: text form vs JSON "null". */
    tlang_fmt_f64(buf, strtod("nan", NULL), false);
    CHECK(strcmp(buf, "NaN") == 0);
    tlang_fmt_f64(buf, strtod("inf", NULL), false);
    CHECK(strcmp(buf, "Infinity") == 0);
    tlang_fmt_f64(buf, -strtod("inf", NULL), false);
    CHECK(strcmp(buf, "-Infinity") == 0);
    tlang_fmt_f64(buf, strtod("nan", NULL), true);
    CHECK(strcmp(buf, "null") == 0);
    tlang_fmt_f64(buf, strtod("inf", NULL), true);
    CHECK(strcmp(buf, "null") == 0);

    /* Round-trip a spread of values. */
    CHECK(roundtrips(0.1));
    CHECK(roundtrips(0.1 + 0.2));
    CHECK(roundtrips(1.0 / 3.0));
    CHECK(roundtrips(2.2250738585072014e-308));
    CHECK(roundtrips(1.7976931348623157e308));
    CHECK(roundtrips(123456789.123456789));
    CHECK(roundtrips(-9.87654321e-13));
}

static void test_ascii_ieq(void) {
    CHECK(tlang_ascii_ieq(TLANG_STR("Content-Type"), TLANG_STR("content-type")));
    CHECK(tlang_ascii_ieq(TLANG_STR(""), TLANG_STR("")));
    CHECK(!tlang_ascii_ieq(TLANG_STR("abc"), TLANG_STR("abcd")));
    CHECK(!tlang_ascii_ieq(TLANG_STR("ab1"), TLANG_STR("ab2")));
    /* Only ASCII letters fold: byte 0x80+ is compared verbatim. */
    CHECK(!tlang_ascii_ieq(TLANG_STR("\xC3\xA9"), TLANG_STR("\xC3\x89")));
}

static void test_index_of(void) {
    tlang_string s = TLANG_STR("hello world");
    CHECK(tlang_str_index_of(s, TLANG_STR("world")) == 6);
    CHECK(tlang_str_index_of(s, TLANG_STR("o")) == 4);
    CHECK(tlang_str_index_of(s, TLANG_STR("")) == 0);
    CHECK(tlang_str_index_of(s, TLANG_STR("xyz")) == -1);
    CHECK(tlang_str_index_of(s, TLANG_STR("hello world!")) == -1);
    CHECK(tlang_str_index_of(TLANG_STR("aaa"), TLANG_STR("aa")) == 0);
}

static void test_concat_clone(void) {
    tlang_string a = TLANG_STR("foo");
    tlang_string b = TLANG_STR("bar");
    tlang_string r;

    arena_setup(4096);
    r = tlang_str_concat(&g_fib, a, b);
    CHECK(str_is(r, "foobar"));
    /* An empty operand returns the other, never NULL data. */
    r = tlang_str_concat(&g_fib, TLANG_STR_NULL, b);
    CHECK(str_is(r, "bar"));
    r = tlang_str_concat(&g_fib, a, TLANG_STR_NULL);
    CHECK(str_is(r, "foo"));
    r = tlang_str_concat(&g_fib, TLANG_STR_NULL, TLANG_STR_NULL);
    CHECK(r.data != NULL && r.len == 0);

    r = tlang_str_clone(&g_fib, a);
    CHECK(str_is(r, "foo") && r.data != a.data);  /* independent copy */
    r = tlang_str_clone(&g_fib, TLANG_STR_NULL);
    CHECK(r.data != NULL && r.len == 0);

    r = tlang_str_clone_global(&g_fib, a);
    CHECK(str_is(r, "foo"));
    free((void*)r.data);  /* global clone is heap memory (stand-in frees it) */
}

static void test_to_string_and_int(void) {
    arena_setup(4096);
    CHECK(str_is(tlang_i32_to_string(&g_fib, -5), "-5"));
    CHECK(str_is(tlang_i64_to_string(&g_fib, INT64_MIN), "-9223372036854775808"));
    CHECK(str_is(tlang_f64_to_string(&g_fib, 0.1), "0.1"));

    CHECK(tlang_str_to_int(&g_fib, TLANG_STR("123")) == 123 && !g_fib.err);
    CHECK(tlang_str_to_int(&g_fib, TLANG_STR("-9")) == -9 && !g_fib.err);
    /* Failure sets a 400 error and returns 0 (tlang.h §8). */
    g_fib.err = 0;
    CHECK(tlang_str_to_int(&g_fib, TLANG_STR("x")) == 0);
    CHECK(g_fib.err && g_fib.error.status == TLANG_STATUS_BAD_REQUEST);
    CHECK(tlang_str_eq(g_fib.error.category, TLANG_STR(TLANG_ERROR_INVALID_INPUT)));
    CHECK(tlang_str_eq(g_fib.error.code, TLANG_STR(TLANG_ERROR_CODE_INVALID_INTEGER)));
    g_fib.err = 0;
    CHECK(tlang_str_to_int(&g_fib, TLANG_STR("99999999999999999999")) == 0);
    CHECK(g_fib.err && g_fib.error.status == 400);
    CHECK(tlang_str_eq(g_fib.error.category, TLANG_STR(TLANG_ERROR_INVALID_INPUT)));
    CHECK(tlang_str_eq(g_fib.error.code, TLANG_STR(TLANG_ERROR_CODE_INVALID_INTEGER)));
}

static void test_mod_f64(void) {
    CHECK(tlang_mod_f64(5.5, 2.0) == 1.5);
    CHECK(tlang_mod_f64(-5.5, 2.0) == -1.5);
    CHECK(isnan(tlang_mod_f64(1.0, 0.0)));  /* JS semantics */
}

static void test_buf(void) {
    tlang_buf b;
    tlang_string s;
    int i;

    arena_setup(4096);
    tlang_buf_init(&g_fib, &b, 0);
    CHECK(b.data == NULL && b.cap == 0 && b.len == 0);
    CHECK(str_is(tlang_buf_string(&b), ""));

    TLANG_BUF_PUT_LIT(&b, "hello");
    tlang_buf_putc(&b, ' ');
    tlang_buf_put_str(&b, TLANG_STR("world"));
    s = tlang_buf_string(&b);
    CHECK(str_is(s, "hello world"));

    /* Force several growths and verify the contents survive. */
    tlang_buf_init(&g_fib, &b, 8);
    for (i = 0; i < 1000; i++) tlang_buf_putc(&b, 'x');
    CHECK(b.len == 1000 && b.cap >= 1000);
    for (i = 0; i < 1000; i++) CHECK(b.data[i] == 'x');

    /* In-place growth path: the buffer is the arena's last allocation. */
    arena_reset_local();
    tlang_buf_init(&g_fib, &b, 16);
    {
        char* first = b.data;
        tlang_buf_reserve(&b, 32);  /* chunk still has room -> extend in place */
        CHECK(b.data == first && b.cap >= 48);
    }
}

static void test_buf_overflow_aborts(void) {
    tlang_buf b;
    arena_setup(4096);
    tlang_buf_init(&g_fib, &b, 16);
    b.len = SIZE_MAX - 4;  /* len + extra overflows -> abort */
    g_f.abort_armed = true;
    if (TLANG_ABORT_POINT(&g_f) == 0) {
        tlang_buf_grow(&b, 100);
        CHECK(0 && "expected abort on size overflow");
    } else {
        CHECK(g_f.abort_status == TLANG_STATUS_UNAVAILABLE);  /* abort fired (503 OOM) */
    }
    g_f.abort_armed = false;
}

int main(void) {
    test_fmt_i64();
    test_parse_i64();
    test_fmt_f64();
    test_ascii_ieq();
    test_index_of();
    test_concat_clone();
    test_to_string_and_int();
    test_mod_f64();
    test_buf();
    test_buf_overflow_aborts();
    arena_free();  /* release the stand-in arena so ASan sees no leak */

    if (failures != 0) {
        printf("FAIL test_strings (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_strings\n");
    return 0;
}
