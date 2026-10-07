/*
 * test_router.c - unit tests for src/router.c (tlang_ctx_match,
 * tlang_ctx_param, tlang_ctx_param_int). Covers the matching table rules
 * (static vs param segments, segment counts, trailing slash, method
 * sensitivity), the copy-on-success-only contract for ctx->params /
 * ctx->param_count, the >TLANG_MAX_PARAMS never-matches rule, and
 * paramInt success / 400 failure.
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

static void set_req(tlang_ctx* ctx, const char* method, const char* path) {
    memset(ctx, 0, sizeof *ctx);
    ctx->method = S(method);
    ctx->path = S(path);
    ctx->query = S("");
    ctx->body = S("");
}

/* ---- static-only routes ---- */
static void test_static_match(void) {
    static const tlang_route_seg segs[2] = {
        { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
        { TLANG_SEG_STATIC, TLANG_STR_INIT("new") },
    };
    static const tlang_route route = { TLANG_STR_INIT("GET"), segs, 2, 0 };

    tlang_ctx ctx;
    set_req(&ctx, "GET", "/users/new");
    CHECK(tlang_ctx_match(&ctx, &route));
    CHECK(ctx.param_count == 0);

    set_req(&ctx, "GET", "/users/old");
    CHECK(!tlang_ctx_match(&ctx, &route));

    /* Method mismatch. */
    set_req(&ctx, "POST", "/users/new");
    CHECK(!tlang_ctx_match(&ctx, &route));

    /* Case-sensitive method. */
    set_req(&ctx, "get", "/users/new");
    CHECK(!tlang_ctx_match(&ctx, &route));
}

/* ---- the "/" route has zero segments ---- */
static void test_root_route(void) {
    static const tlang_route route = { TLANG_STR_INIT("GET"), NULL, 0, 0 };
    tlang_ctx ctx;

    set_req(&ctx, "GET", "/");
    CHECK(tlang_ctx_match(&ctx, &route));
    CHECK(ctx.param_count == 0);

    set_req(&ctx, "GET", "/x");
    CHECK(!tlang_ctx_match(&ctx, &route));
}

/* ---- parameter segments ---- */
static void test_param_match(void) {
    static const tlang_route_seg segs[2] = {
        { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
        { TLANG_SEG_PARAM, TLANG_STR_INIT("id") },
    };
    static const tlang_route route = { TLANG_STR_INIT("GET"), segs, 2, 1 };

    tlang_ctx ctx;
    set_req(&ctx, "GET", "/users/42");
    CHECK(tlang_ctx_match(&ctx, &route));
    CHECK(ctx.param_count == 1);
    tlang_string id = tlang_ctx_param(&ctx, S("id"));
    CHECK(id.len == 2 && memcmp(id.data, "42", 2) == 0);
    /* An unknown parameter name returns "". */
    tlang_string none = tlang_ctx_param(&ctx, S("nope"));
    CHECK(none.len == 0);

    /* Param segments require a non-empty segment: "/users/" does not match a
     * two-segment param route (3 segments, last empty). */
    set_req(&ctx, "GET", "/users/");
    CHECK(!tlang_ctx_match(&ctx, &route));

    /* Too few segments. */
    set_req(&ctx, "GET", "/users");
    CHECK(!tlang_ctx_match(&ctx, &route));

    /* Too many segments. */
    set_req(&ctx, "GET", "/users/42/extra");
    CHECK(!tlang_ctx_match(&ctx, &route));
}

/* ---- copy-on-success-only: a failed match leaves params unchanged ---- */
static void test_params_preserved_on_failure(void) {
    static const tlang_route_seg segs_ok[2] = {
        { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
        { TLANG_SEG_PARAM, TLANG_STR_INIT("id") },
    };
    static const tlang_route route_ok = { TLANG_STR_INIT("GET"), segs_ok, 2, 1 };

    /* This route captures a param in the first segment, then fails on the
     * second static segment: a naive implementation that writes params during
     * capture would corrupt ctx->params before failing. */
    static const tlang_route_seg segs_fail[2] = {
        { TLANG_SEG_PARAM, TLANG_STR_INIT("pid") },
        { TLANG_SEG_STATIC, TLANG_STR_INIT("edit") },
    };
    static const tlang_route route_fail = { TLANG_STR_INIT("GET"), segs_fail, 2, 1 };

    tlang_ctx ctx;
    set_req(&ctx, "GET", "/users/7");
    CHECK(tlang_ctx_match(&ctx, &route_ok));
    CHECK(ctx.param_count == 1);

    /* A failing match must not overwrite the previous params. */
    set_req(&ctx, "GET", "/users/7");  /* reset method/path, keep params? */
    /* set_req zeroed params; redo the successful match then a failing one. */
    CHECK(tlang_ctx_match(&ctx, &route_ok));
    CHECK(ctx.param_count == 1);
    tlang_string before = tlang_ctx_param(&ctx, S("id"));
    CHECK(before.len == 1 && before.data[0] == '7');

    /* Now attempt a route that captures "pid" then fails on "edit" vs
     * "delete"; ctx->params / param_count must be unchanged from the previous
     * successful match. */
    ctx.path = S("/99/delete");
    CHECK(!tlang_ctx_match(&ctx, &route_fail));
    CHECK(ctx.param_count == 1);
    tlang_string after = tlang_ctx_param(&ctx, S("id"));
    CHECK(after.len == 1 && after.data[0] == '7');  /* unchanged */
    /* The failed route's "pid" must not be visible. */
    tlang_string pid = tlang_ctx_param(&ctx, S("pid"));
    CHECK(pid.len == 0);
}

/* ---- more than TLANG_MAX_PARAMS never matches ---- */
static void test_too_many_params(void) {
    /* A route with TLANG_MAX_PARAMS + 1 param segments. */
    static tlang_route_seg segs[TLANG_MAX_PARAMS + 1];
    static tlang_route route;
    for (int i = 0; i < TLANG_MAX_PARAMS + 1; i++) {
        segs[i].kind = TLANG_SEG_PARAM;
        segs[i].text = TLANG_STR("p");
    }
    route.method = TLANG_STR("GET");
    route.segs = segs;
    route.nsegs = TLANG_MAX_PARAMS + 1;
    route.nparams = TLANG_MAX_PARAMS + 1;

    /* Build a path with TLANG_MAX_PARAMS + 1 segments. */
    char path[128];
    size_t off = 0;
    for (int i = 0; i < TLANG_MAX_PARAMS + 1; i++)
        off += (size_t)snprintf(path + off, sizeof path - off, "/a");
    tlang_ctx ctx;
    set_req(&ctx, "GET", path);
    CHECK(!tlang_ctx_match(&ctx, &route));
}

/* ---- paramInt ---- */
static void test_param_int(void) {
    MemoryArena arena;
    CHECK(tlang_arena_init(&arena) == 0);
    tlang_fiber fib;
    memset(&fib, 0, sizeof fib);
    fib.arena = &arena;

    static const tlang_route_seg segs[2] = {
        { TLANG_SEG_STATIC, TLANG_STR_INIT("users") },
        { TLANG_SEG_PARAM, TLANG_STR_INIT("id") },
    };
    static const tlang_route route = { TLANG_STR_INIT("GET"), segs, 2, 1 };

    tlang_ctx ctx;
    set_req(&ctx, "GET", "/users/42");
    CHECK(tlang_ctx_match(&ctx, &route));

    fib.err = 0;
    int64_t v = tlang_ctx_param_int(&fib, &ctx, S("id"));
    CHECK(fib.err == 0);
    CHECK(v == 42);

    /* Non-integer value -> 400. */
    set_req(&ctx, "GET", "/users/abc");
    CHECK(tlang_ctx_match(&ctx, &route));
    fib.err = 0;
    tlang_ctx_param_int(&fib, &ctx, S("id"));
    CHECK(fib.err != 0);
    CHECK(fib.error.status == 400);

    /* Absent parameter -> 400. */
    fib.err = 0;
    tlang_ctx_param_int(&fib, &ctx, S("missing"));
    CHECK(fib.err != 0);
    CHECK(fib.error.status == 400);

    tlang_arena_destroy(&arena);
}

int main(void) {
    test_static_match();
    test_root_route();
    test_param_match();
    test_params_preserved_on_failure();
    test_too_many_params();
    test_param_int();

    if (failures != 0) {
        printf("FAIL test_router (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_router\n");
    return 0;
}
