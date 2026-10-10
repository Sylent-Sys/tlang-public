/* Focused tests for exact-name environment access. */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>

static int failures;
#define CHECK(x) do { if (!(x)) { fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #x); failures++; } } while (0)

int main(void) {
    static const tlang_string allowed[] = {TLANG_STR_INIT("TLANG_TEST_ALLOWED"), TLANG_STR_INIT("TLANG_TEST_UNSET")};
    const tlang_program prog = {.env_names = allowed, .env_name_count = 2, .has_manifest = false};
    tlang_config cfg;
    tlang_fiber fib;
    tlang_string got;
    MemoryArena arena;
    char err[8];
    memset(&fib, 0, sizeof fib);
    CHECK(tlang_arena_init(&arena) == 0);
    fib.arena = &arena;
    CHECK(setenv("TLANG_TEST_ALLOWED", "", 1) == 0);
    CHECK(unsetenv("TLANG_TEST_UNSET") == 0);
    CHECK(tlang_grants_load_validate(&prog, NULL, &cfg, err, sizeof err) == 0);
    got = tlang_env_get(&fib, TLANG_STR("TLANG_TEST_ALLOWED"));
    CHECK(!fib.err && got.data != NULL && got.len == 0);
    got = tlang_env_get(&fib, TLANG_STR("TLANG_TEST_UNSET"));
    CHECK(!fib.err && got.data == NULL);
    got = tlang_env_get(&fib, TLANG_STR("TLANG_TEST_DENIED"));
    CHECK(fib.err && fib.error.status == 403 && tlang_str_eq(fib.error.category, TLANG_STR(TLANG_ERROR_PERMISSION)) &&
          tlang_str_eq(fib.error.code, TLANG_STR("env.permission_denied")));
    unsetenv("TLANG_TEST_ALLOWED");
    tlang_arena_destroy(&arena);
    if (failures) return 1;
    puts("PASS test_env");
    return 0;
}
