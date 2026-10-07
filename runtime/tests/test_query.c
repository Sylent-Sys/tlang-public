/*
 * test_query.c - unit tests for src/query.c (tlang_ctx_query). Covers the
 * linear scan over `k=v&k2=v2`, raw (undecoded) key comparison, a key without
 * '=' yielding "", zero-copy slices for values without '%' or '+', arena
 * decoding for '+' (space) and %XX, invalid escapes kept literally, the first
 * match winning, and "" when absent.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <string.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

static tlang_string S(const char* s) {
    tlang_string r;
    r.data = s;
    r.len = strlen(s);
    return r;
}

static bool eq(tlang_string a, const char* b) {
    size_t n = strlen(b);
    return a.len == n && (n == 0 || memcmp(a.data, b, n) == 0);
}

static MemoryArena g_arena;
static tlang_fiber g_fib;
static tlang_ctx g_ctx;

static void setup(const char* query) {
    memset(&g_ctx, 0, sizeof g_ctx);
    g_ctx.query = S(query);
    g_ctx.method = S("GET");
    g_ctx.path = S("/");
    g_ctx.body = S("");
}

static void test_basic(void) {
    setup("a=1&b=2&c=3");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("a")), "1"));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("b")), "2"));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("c")), "3"));
    /* Absent key -> "". */
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("z")), ""));
}

static void test_zero_copy(void) {
    setup("name=plain");
    tlang_string v = tlang_ctx_query(&g_fib, &g_ctx, S("name"));
    CHECK(eq(v, "plain"));
    /* A value without '%' or '+' is a zero-copy slice of the query buffer. */
    CHECK(v.data >= g_ctx.query.data && v.data < g_ctx.query.data + g_ctx.query.len);
}

static void test_key_without_value(void) {
    setup("flag&x=1");
    /* A key without '=' has value "". */
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("flag")), ""));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("x")), "1"));
}

static void test_empty_value(void) {
    setup("k=&y=9");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("k")), ""));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("y")), "9"));
}

static void test_plus_and_percent(void) {
    setup("q=hello+world&p=%41%42");
    tlang_string q = tlang_ctx_query(&g_fib, &g_ctx, S("q"));
    CHECK(eq(q, "hello world"));
    /* Decoded values live in the arena, not in the request buffer. */
    CHECK(q.data < g_ctx.query.data || q.data >= g_ctx.query.data + g_ctx.query.len);

    tlang_string p = tlang_ctx_query(&g_fib, &g_ctx, S("p"));
    CHECK(eq(p, "AB"));
}

static void test_invalid_escape(void) {
    /* An invalid escape is kept literally. */
    setup("a=%zz&b=%4&c=%");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("a")), "%zz"));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("b")), "%4"));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("c")), "%"));
}

static void test_mixed_decode(void) {
    setup("x=a%20b+c");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("x")), "a b c"));
}

static void test_raw_key_compare(void) {
    /* Keys are compared raw: an encoded key does not match a decoded name. */
    setup("na%6de=v");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("name")), ""));
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("na%6de")), "v"));
}

static void test_first_match_wins(void) {
    setup("d=first&d=second");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("d")), "first"));
}

static void test_empty_query(void) {
    setup("");
    CHECK(eq(tlang_ctx_query(&g_fib, &g_ctx, S("anything")), ""));
}

int main(void) {
    if (tlang_arena_init(&g_arena) != 0) {
        fprintf(stderr, "arena init failed\n");
        return 2;
    }
    memset(&g_fib, 0, sizeof g_fib);
    g_fib.arena = &g_arena;

    test_basic();
    test_zero_copy();
    test_key_without_value();
    test_empty_value();
    test_plus_and_percent();
    test_invalid_escape();
    test_mixed_decode();
    test_raw_key_compare();
    test_first_match_wins();
    test_empty_query();

    tlang_arena_destroy(&g_arena);

    if (failures != 0) {
        printf("FAIL test_query (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_query\n");
    return 0;
}
