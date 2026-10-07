/*
 * test_pg_tx.c - transactions and pool timeout (src/pg.c).
 *
 * Requires TLANG_TEST_DATABASE_URL; prints SKIP and exits 0 when unset (no
 * database server in this sandbox, which is expected). When set it checks:
 *   - begin / exec / commit makes changes durable;
 *   - begin / exec / rollback discards them (and rollback is idempotent);
 *   - a pool of size 1 held by one transaction makes a second taker time out
 *     with 503 "database pool timeout".
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

static void run_fiber(void (*fn)(Fiber*, void*), int pool_size, uint32_t pool_timeout_ms) {
    char err[256];
    tlang_config_defaults(&g_cfg);
    g_cfg.database_url = getenv("TLANG_TEST_DATABASE_URL");
    g_cfg.db_pool_size = pool_size;
    g_cfg.db_pool_timeout_ms = pool_timeout_ms;
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

static int64_t count_rows(tlang_fiber* f) {
    /* COUNT(*) via execute's affected count is not meaningful; use a one-row
     * query mapped to a tiny row. */
    typedef struct { int64_t n; } crow;
    static const tlang_field_desc fd[1] = { { TLANG_STR_INIT("n"), TLANG_KIND_I64, 0 } };
    static const tlang_type_desc td = { TLANG_STR_INIT("Count"), sizeof(crow), 1, fd, NULL };
    crow* r = (crow*)tlang_db_query_one(f, NULL,
                  TLANG_STR("SELECT count(*)::bigint AS n FROM tl_tx"), NULL, 0, &td);
    return (r != NULL && !f->err) ? r->n : -1;
}

static void tx_body(Fiber* self, void* arg) {
    tlang_fiber* f = &self->pub;
    (void)arg;

    tlang_db_execute(f, NULL, TLANG_STR("CREATE TABLE IF NOT EXISTS tl_tx (id int)"), NULL, 0);
    CHECK(!f->err);
    tlang_db_execute(f, NULL, TLANG_STR("DELETE FROM tl_tx"), NULL, 0);
    CHECK(!f->err);

    /* Commit path. */
    {
        TxContext tx;
        bool ok = tlang_tx_begin(f, &tx);
        CHECK(ok && !f->err);
        {
            tlang_pg_param p[1] = { TLANG_PG_I32(7) };
            tlang_tx_exec(f, &tx, TLANG_STR("INSERT INTO tl_tx VALUES ($1)"), p, 1);
            CHECK(!f->err);
        }
        CHECK(tlang_tx_commit(f, &tx));
        CHECK(!f->err);
        CHECK(count_rows(f) == 1);
    }

    /* Rollback path. */
    {
        TxContext tx;
        bool ok = tlang_tx_begin(f, &tx);
        CHECK(ok && !f->err);
        {
            tlang_pg_param p[1] = { TLANG_PG_I32(8) };
            tlang_tx_exec(f, &tx, TLANG_STR("INSERT INTO tl_tx VALUES ($1)"), p, 1);
            CHECK(!f->err);
        }
        tlang_tx_rollback(f, &tx);
        CHECK(!f->err);
        /* Idempotent second rollback. */
        tlang_tx_rollback(f, &tx);
        CHECK(!f->err);
        CHECK(count_rows(f) == 1);   /* the rolled-back insert is gone */
    }

    tlang_db_execute(f, NULL, TLANG_STR("DROP TABLE tl_tx"), NULL, 0);
    CHECK(!f->err);
}

/* Pool-timeout: a size-1 pool held by an open transaction while a second
 * fiber tries an autocommit query and must time out with 503. */
static int g_timeout_status;
static bool g_timeout_err;

static void holder_fiber(Fiber* self, void* arg) {
    tlang_fiber* f = &self->pub;
    TxContext* tx = (TxContext*)arg;
    bool ok = tlang_tx_begin(f, tx);
    CHECK(ok && !f->err);
    /* Hold the single connection until the sched empties: do a slow sleep. */
    tlang_sleep_until(self, tlang_now_ns() + 300ull * 1000000ull);
    tlang_tx_rollback(f, tx);
}

static void waiter_fiber(Fiber* self, void* arg) {
    tlang_fiber* f = &self->pub;
    (void)arg;
    /* The only connection is held: this autocommit query must time out. */
    tlang_db_execute(f, NULL, TLANG_STR("SELECT 1"), NULL, 0);
    g_timeout_err = f->err;
    g_timeout_status = f->error.status;
}

static TxContext g_hold_tx;

static void timeout_setup(Fiber* self, void* arg) {
    (void)arg;
    /* Spawn the holder (grabs the one connection) then the waiter. */
    tlang_spawn(self->sched, holder_fiber, &g_hold_tx);
    tlang_spawn(self->sched, waiter_fiber, NULL);
}

int main(void) {
    if (getenv("TLANG_TEST_DATABASE_URL") == NULL) {
        printf("SKIP test_pg_tx (TLANG_TEST_DATABASE_URL unset)\n");
        return 0;
    }
    run_fiber(tx_body, 4, 2000);

    /* Pool timeout: pool size 1, short timeout. */
    g_timeout_err = false;
    g_timeout_status = 0;
    run_fiber(timeout_setup, 1, 100);
    CHECK(g_timeout_err);
    CHECK(g_timeout_status == 503);

    if (failures != 0) {
        printf("FAIL test_pg_tx (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_pg_tx\n");
    return 0;
}
