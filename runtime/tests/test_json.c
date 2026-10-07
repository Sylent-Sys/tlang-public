/*
 * test_json.c - unit tests for src/json.c (scanners, depth limit, writers).
 * Exercises round-trip float scanning, int overflow rejection, string escape
 * and surrogate decoding, zero-copy vs arena decode, and the depth limit.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* The arena and allocation primitives are provided by the library now
 * (arena.c / fiber.c); json.c uses arena_alloc directly and reports failure
 * (never aborts), so a plain fiber over a real arena is enough. */
static MemoryArena g_arena;
static bool g_arena_live;
static tlang_fiber g_fib;

static void arena_free(void) {
    if (g_arena_live) {
        tlang_arena_destroy(&g_arena);
        g_arena_live = false;
    }
}

static void arena_setup(size_t cap) {
    (void)cap;
    arena_free();  /* free any arena from a previous test (no leaks under ASan) */
    if (tlang_arena_init(&g_arena) != 0) { perror("tlang_arena_init"); exit(2); }
    g_arena_live = true;
    memset(&g_fib, 0, sizeof g_fib);
    g_fib.arena = &g_arena;
}

static bool str_is(tlang_string s, const char* expect) {
    size_t n = strlen(expect);
    return s.data != NULL && s.len == n && memcmp(s.data, expect, n) == 0;
}

/* Spans a NUL-terminated literal as a scan range.
 * tcc does not pool duplicate string literals, so evaluating `lit` twice would
 * place `p` and `end` in two distinct copies at different addresses (making
 * pointer differences/comparisons wrong). Evaluate the literal exactly once
 * into a single named array and derive both p and end from it. Each RANGE sits
 * in its own block, so range_lit__ never collides. sizeof(range_lit__) - 1 is
 * exactly the original literal length (array length minus the trailing NUL). */
#define RANGE(lit)                                        \
    static const char range_lit__[] = lit;                \
    const char* p = range_lit__;                          \
    const char* end = range_lit__ + sizeof(range_lit__) - 1

/* ---- scanners ---- */

static void test_scan_int(void) {
    {
        RANGE("123");
        int64_t v;
        CHECK(json_scan_int64(&p, end, &v) && v == 123 && p == end);
    }
    {
        RANGE("-9223372036854775808");
        int64_t v;
        CHECK(json_scan_int64(&p, end, &v) && v == INT64_MIN);
    }
    {   /* overflow rejected */
        RANGE("9223372036854775808");
        int64_t v;
        CHECK(!json_scan_int64(&p, end, &v));
    }
    {   /* a fraction/exponent is not an integer */
        RANGE("12.5");
        int64_t v;
        CHECK(!json_scan_int64(&p, end, &v));
    }
    {   /* leading zero: "0" scans as a valid token, the "1" is left behind */
        RANGE("01");
        int64_t v;
        CHECK(json_scan_int64(&p, end, &v) && v == 0 && p == end - 1 && *p == '1');
    }
    {   /* int32 range check */
        RANGE("2147483648");
        int32_t v;
        const char* save = p;
        CHECK(!json_scan_int32(&p, end, &v) && p == save);
    }
    {
        RANGE("-2147483648");
        int32_t v;
        CHECK(json_scan_int32(&p, end, &v) && v == INT32_MIN);
    }
}

static bool scan_f(const char* lit, size_t n, double* out) {
    const char* p = lit;
    const char* end = lit + n;
    return json_scan_float64(&p, end, out) && p == end;
}

static void test_scan_float(void) {
    double v;
    CHECK(scan_f("0.1", 3, &v) && v == 0.1);
    CHECK(scan_f("3.141592653589793", 17, &v) && v == 3.141592653589793);
    CHECK(scan_f("-2.5e3", 6, &v) && v == -2500.0);
    CHECK(scan_f("1E10", 4, &v) && v == 1e10);
    CHECK(scan_f("42", 2, &v) && v == 42.0);
    /* Overflow to infinity fails. */
    CHECK(!scan_f("1e400", 5, &v));
    /* Invalid syntax. */
    CHECK(!scan_f(".5", 2, &v));
    CHECK(!scan_f("1.", 2, &v));
    CHECK(!scan_f("1e", 2, &v));
    /* A very long token takes the temporary-malloc path. */
    {
        char big[200];
        size_t i;
        big[0] = '0'; big[1] = '.';
        for (i = 2; i < sizeof(big); i++) big[i] = '1';
        CHECK(scan_f(big, sizeof(big), &v) && v > 0.0 && v < 1.0);
    }
}

static void test_scan_literals(void) {
    {
        RANGE("true");
        bool b;
        CHECK(json_scan_bool(&p, end, &b) && b && p == end);
    }
    {
        RANGE("false");
        bool b;
        CHECK(json_scan_bool(&p, end, &b) && !b && p == end);
    }
    {
        RANGE("truX");
        bool b;
        CHECK(!json_scan_bool(&p, end, &b));
    }
    {
        RANGE("null");
        CHECK(json_scan_null(&p, end) && p == end);
    }
    {   /* non-null is left untouched */
        RANGE("nope");
        const char* save = p;
        CHECK(!json_scan_null(&p, end) && p == save);
    }
}

static void test_scan_string(void) {
    arena_setup(1 << 16);

    {   /* zero-copy: no escapes -> slice of the input */
        RANGE("\"hello\"rest");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "hello") && out.data == p - 6);  /* points into input */
        CHECK(*p == 'r');
    }
    {   /* escapes decode into the arena */
        RANGE("\"a\\\"b\\\\c\\n\\t\\/\"");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "a\"b\\c\n\t/"));
    }
    {   /* \uXXXX basic multilingual plane */
        RANGE("\"\\u00e9\\u0041\"");  /* é A */
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "\xC3\xA9" "A"));
    }
    {   /* surrogate pair -> U+1F600 (4-byte UTF-8) */
        RANGE("\"\\ud83d\\ude00\"");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "\xF0\x9F\x98\x80"));
    }
    {   /* lone high surrogate -> U+FFFD */
        RANGE("\"\\ud83d\"");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "\xEF\xBF\xBD"));
    }
    {   /* lone low surrogate -> U+FFFD */
        RANGE("\"\\udc00x\"");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "\xEF\xBF\xBD" "x"));
    }
    {   /* bytes >= 0x80 pass through unchanged */
        RANGE("\"\xC3\xA9\"");
        tlang_string out;
        CHECK(json_scan_string(&p, end, &g_arena, &out));
        CHECK(str_is(out, "\xC3\xA9"));
    }
    {   /* failures: missing quote, raw control byte, bad escape */
        tlang_string out;
        const char* p1 = "\"abc"; CHECK(!json_scan_string(&p1, p1 + 4, &g_arena, &out));
        const char* p2 = "\"a\x01\""; CHECK(!json_scan_string(&p2, p2 + 4, &g_arena, &out));
        const char* p3 = "\"\\x\""; CHECK(!json_scan_string(&p3, p3 + 4, &g_arena, &out));
        const char* p4 = "\"\\u00zz\""; CHECK(!json_scan_string(&p4, p4 + 8, &g_arena, &out));
    }
}

static void test_scan_key(void) {
    {   /* raw bytes between the quotes, undecoded */
        RANGE("\"name\":");
        tlang_string k;
        CHECK(json_scan_key(&p, end, &k) && str_is(k, "name") && *p == ':');
    }
    {   /* an escaped key stays raw (never matches a field name): 3 raw bytes */
        RANGE("\"a\\n\"");
        tlang_string k;
        CHECK(json_scan_key(&p, end, &k) && k.len == 3 && memcmp(k.data, "a\\n", 3) == 0);
    }
}

/* ---- skip_value and the depth limit ---- */

static bool skip_ok(const char* lit, size_t n, int depth) {
    const char* p = lit;
    const char* end = lit + n;
    return json_skip_value(&p, end, depth) && json_skip_ws(p, end) == end;
}

static void test_skip_value(void) {
    CHECK(skip_ok("123", 3, 0));
    CHECK(skip_ok("-4.5e2", 6, 0));
    CHECK(skip_ok("\"a b\"", 5, 0));
    CHECK(skip_ok("true", 4, 0));
    CHECK(skip_ok("null", 4, 0));
    CHECK(skip_ok("[1, 2, [3], {}]", 15, 0));
    CHECK(skip_ok("{ \"a\": 1, \"b\": [true, null] }", 29, 0));
    CHECK(skip_ok("  { }  ", 7, 0));
    /* malformed */
    CHECK(!skip_ok("[1,]", 4, 0));
    CHECK(!skip_ok("{\"a\"}", 5, 0));
    CHECK(!skip_ok("{,}", 3, 0));
    CHECK(!skip_ok("tru", 3, 0));
}

static void test_depth_limit(void) {
    /* Default limit is 32. Build nested arrays of a chosen depth. */
    char buf[600];
    int d;
    const char* p;
    const char* end;

    json_set_max_depth(32);

    /* 31 opening brackets => the innermost array sits at container-depth 31,
     * accepted (31 < 32). 32 would need container-depth 32 >= 32 and fail. */
    for (d = 0; d < 31; d++) buf[d] = '[';
    buf[31] = '0';
    for (d = 0; d < 31; d++) buf[32 + d] = ']';
    p = buf; end = buf + 63;
    CHECK(json_skip_value(&p, end, 0) && p == end);

    for (d = 0; d < 40; d++) buf[d] = '[';
    buf[40] = '0';
    for (d = 0; d < 40; d++) buf[41 + d] = ']';
    p = buf; end = buf + 81;
    CHECK(!json_skip_value(&p, end, 0));

    /* set_max_depth clamps to 1..256. */
    json_set_max_depth(0);
    CHECK(json_max_depth() == 1);
    json_set_max_depth(1000);
    CHECK(json_max_depth() == 256);
    json_set_max_depth(32);  /* restore default */
}

/* ---- writers ---- */

static void test_writers(void) {
    tlang_buf b;
    arena_setup(1 << 16);

    tlang_buf_init(&g_fib, &b, 16);
    json_write_str(&b, TLANG_STR("a\"b\\c\n\t"));
    CHECK(str_is(tlang_buf_string(&b), "\"a\\\"b\\\\c\\n\\t\""));

    tlang_buf_init(&g_fib, &b, 16);
    {   /* control bytes below 0x20 (other than the named ones) -> \u00XX */
        tlang_string s;
        s.data = "\x01\x1f"; s.len = 2;
        json_write_str(&b, s);
        CHECK(str_is(tlang_buf_string(&b), "\"\\u0001\\u001f\""));
    }

    tlang_buf_init(&g_fib, &b, 16);
    {   /* bytes >= 0x80 are copied unchanged */
        tlang_string s;
        s.data = "\xC3\xA9"; s.len = 2;
        json_write_str(&b, s);
        CHECK(str_is(tlang_buf_string(&b), "\"\xC3\xA9\""));
    }

    tlang_buf_init(&g_fib, &b, 16);
    json_write_i64(&b, INT64_MIN);
    CHECK(str_is(tlang_buf_string(&b), "-9223372036854775808"));

    tlang_buf_init(&g_fib, &b, 16);
    json_write_i32(&b, -5);
    CHECK(str_is(tlang_buf_string(&b), "-5"));

    tlang_buf_init(&g_fib, &b, 16);
    json_write_f64(&b, 0.1);
    CHECK(str_is(tlang_buf_string(&b), "0.1"));

    tlang_buf_init(&g_fib, &b, 16);
    json_write_f64(&b, strtod("inf", NULL));  /* non-finite -> null */
    CHECK(str_is(tlang_buf_string(&b), "null"));

    tlang_buf_init(&g_fib, &b, 16);
    json_write_f64(&b, -0.0);  /* -0 -> 0 */
    CHECK(str_is(tlang_buf_string(&b), "0"));

    tlang_buf_init(&g_fib, &b, 16);
    json_write_bool(&b, true);
    json_write_null(&b);
    CHECK(str_is(tlang_buf_string(&b), "truenull"));
}

int main(void) {
    test_scan_int();
    test_scan_float();
    test_scan_literals();
    test_scan_string();
    test_scan_key();
    test_skip_value();
    test_depth_limit();
    test_writers();
    arena_free();  /* release the stand-in arena so ASan sees no leak */

    if (failures != 0) {
        printf("FAIL test_json (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_json\n");
    return 0;
}
