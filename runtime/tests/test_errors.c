/*
 * test_errors.c - unit tests for src/errors.c (throw / take / throw_fmt).
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

/* The real arena (arena.c) and allocation primitives (arena.c / fiber.c) are
 * linked from the library now; this test uses them directly. */
static MemoryArena g_arena;
static tlang_fiber g_fib;
static bool g_arena_live;

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
    memset(&g_fib, 0, sizeof g_fib);
    g_fib.arena = &g_arena;
}

static bool str_is(tlang_string s, const char* expect) {
    size_t n = strlen(expect);
    return s.data != NULL && s.len == n && memcmp(s.data, expect, n) == 0;
}

static void test_throw_take(void) {
    tlang_error e;
    arena_setup(4096);

    CHECK(!g_fib.err);
    tlang_throw(&g_fib, 404, TLANG_STR("not found"));
    CHECK(g_fib.err && g_fib.error.status == 404 && str_is(g_fib.error.message, "not found"));
    CHECK(str_is(g_fib.error.category, "internal") && str_is(g_fib.error.code, "internal"));

    tlang_throw_typed(&g_fib, 404, TLANG_STR("missing"), TLANG_STR("not_found"), TLANG_STR("resource_missing"));
    CHECK(g_fib.err && str_is(g_fib.error.category, "not_found") && str_is(g_fib.error.code, "resource_missing"));

    /* Throwing a caught value preserves category and code. */
    e = tlang_take_error(&g_fib);
    tlang_throw_value(&g_fib, e);
    CHECK(g_fib.err && g_fib.error.status == 404 && str_is(g_fib.error.category, "not_found"));
    CHECK(str_is(g_fib.error.code, "resource_missing"));

    /* A later throw replaces the pending error. */
    tlang_throw(&g_fib, 500, TLANG_STR("boom"));
    CHECK(g_fib.error.status == 500 && str_is(g_fib.error.message, "boom"));
    CHECK(str_is(g_fib.error.category, "internal") && str_is(g_fib.error.code, "internal"));

    /* take_error returns the pending error and clears it. */
    e = tlang_take_error(&g_fib);
    CHECK(e.status == 500 && str_is(e.message, "boom"));
    CHECK(str_is(e.category, "internal") && str_is(e.code, "internal"));
    CHECK(!g_fib.err);
    CHECK(str_is(g_fib.error.category, "") && str_is(g_fib.error.code, ""));

    /* With no error pending, take returns {0, ""}. */
    e = tlang_take_error(&g_fib);
    CHECK(e.status == 0 && str_is(e.message, ""));
    CHECK(str_is(e.category, "") && str_is(e.code, ""));
}

static void test_throw_null_message(void) {
    arena_setup(4096);
    g_fib.err = 0;
    tlang_throw(&g_fib, 500, TLANG_STR_NULL);  /* NULL message stored as "" */
    CHECK(g_fib.err && g_fib.error.message.data != NULL && g_fib.error.message.len == 0);
    CHECK(str_is(g_fib.error.category, "internal") && str_is(g_fib.error.code, "internal"));
}

static void test_throw_value(void) {
    tlang_error src = tlang_error_make(TLANG_STR("explicit"), 418);
    arena_setup(4096);
    g_fib.err = 0;
    tlang_throw_value(&g_fib, src);
    CHECK(g_fib.err && g_fib.error.status == 418 && str_is(g_fib.error.message, "explicit"));
    CHECK(str_is(g_fib.error.category, "internal") && str_is(g_fib.error.code, "internal"));
}

static void test_throw_fmt(void) {
    int i;
    arena_setup(1 << 16);

    g_fib.err = 0;
    tlang_throw_fmt_typed(&g_fib, 400, TLANG_STR("invalid_input"), TLANG_STR("invalid_integer"), "bad value %d for %s", 42, "key");
    CHECK(g_fib.err && g_fib.error.status == 400);
    CHECK(str_is(g_fib.error.category, "invalid_input") && str_is(g_fib.error.code, "invalid_integer"));
    CHECK(str_is(g_fib.error.message, "bad value 42 for key"));
    /* The message lives in the arena, independent of the format args. */
    CHECK(g_fib.error.message.data != NULL);

    /* A long message that exceeds the stack buffer takes the re-format path. */
    g_fib.err = 0;
    {
        char big[5000];
        for (i = 0; i < (int)sizeof(big) - 1; i++) big[i] = 'A' + (i % 26);
        big[sizeof(big) - 1] = '\0';
        tlang_throw_fmt(&g_fib, 500, "%s", big);
        CHECK(g_fib.err && g_fib.error.message.len == sizeof(big) - 1);
        CHECK(memcmp(g_fib.error.message.data, big, sizeof(big) - 1) == 0);
        CHECK(str_is(g_fib.error.category, "internal") && str_is(g_fib.error.code, "internal"));
    }
}

int main(void) {
    test_throw_take();
    tlang_clear_error(&g_fib);
    CHECK(str_is(g_fib.error.category, "") && str_is(g_fib.error.code, ""));
    test_throw_null_message();
    test_throw_value();
    test_throw_fmt();
    arena_free();  /* release the stand-in arena so ASan sees no leak */

    if (failures != 0) {
        printf("FAIL test_errors (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_errors\n");
    return 0;
}
