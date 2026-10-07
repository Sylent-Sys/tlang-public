/*
 * test_pg_exec.c - db.execute against a live database (src/pg.c, PG=1).
 *
 * Requires a disposable PostgreSQL reachable through TLANG_TEST_DATABASE_URL.
 * When that variable is unset the test prints a SKIP note and exits 0: this
 * sandbox has no database server, which is the expected steady state (the
 * Makefile also skips test_pg_* when the variable is unset). When set, the
 * test runs CREATE TEMP TABLE / INSERT / UPDATE and checks the affected-row
 * counts, exercising the prepared-statement cache and parameter encoding.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

static tlang_config g_cfg;
static tlang_sched g_sched;
static tlang_program g_prog;

static void run_fiber(void (*fn)(Fiber*, void*)) {
    char err[256];
    tlang_config_defaults(&g_cfg);
    g_cfg.database_url = getenv("TLANG_TEST_DATABASE_URL");
    g_cfg.db_pool_size = 4;
    memset(&g_prog, 0, sizeof g_prog);
    g_prog.uses_db = true;

    if (tlang_sched_init(&g_sched, 0, &g_cfg, &g_prog, -1, err, sizeof err) != 0) {
        fprintf(stderr, "sched init failed: %s\n", err);
        exit(2);
    }
    tlang_fctx_init_thread(&g_sched.loop_ctx);
    if (tlang_pg_pool_create(&g_sched, err, sizeof err) != 0) {
        fprintf(stderr, "pool create failed: %s\n", err);
        exit(2);
    }
    if (tlang_spawn(&g_sched, fn, NULL) == NULL) { fprintf(stderr, "spawn failed\n"); exit(2); }
    tlang_sched_run(&g_sched);
    tlang_sched_destroy(&g_sched);
}

static void exec_body(Fiber* self, void* arg) {
    tlang_fiber* f = &self->pub;
    int64_t n;
    (void)arg;

    n = tlang_db_execute(f, NULL,
                         TLANG_STR("CREATE TEMP TABLE tl_exec (id int, name text)"),
                         NULL, 0);
    CHECK(!f->err);

    {
        tlang_pg_param p[2] = { TLANG_PG_I32(1), TLANG_PG_STR(TLANG_STR("alice")) };
        n = tlang_db_execute(f, NULL,
                             TLANG_STR("INSERT INTO tl_exec (id, name) VALUES ($1, $2)"),
                             p, 2);
        CHECK(!f->err);
        CHECK(n == 1);
    }
    {
        /* Same SQL again: reuses the prepared statement. */
        tlang_pg_param p[2] = { TLANG_PG_I32(2), TLANG_PG_STR(TLANG_STR("bob")) };
        n = tlang_db_execute(f, NULL,
                             TLANG_STR("INSERT INTO tl_exec (id, name) VALUES ($1, $2)"),
                             p, 2);
        CHECK(!f->err);
        CHECK(n == 1);
    }
    {
        n = tlang_db_execute(f, NULL, TLANG_STR("UPDATE tl_exec SET name = 'x'"), NULL, 0);
        CHECK(!f->err);
        CHECK(n == 2);
    }
}

int main(void) {
    if (getenv("TLANG_TEST_DATABASE_URL") == NULL) {
        printf("SKIP test_pg_exec (TLANG_TEST_DATABASE_URL unset)\n");
        return 0;
    }
    run_fiber(exec_body);
    if (failures != 0) {
        printf("FAIL test_pg_exec (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_pg_exec\n");
    return 0;
}
