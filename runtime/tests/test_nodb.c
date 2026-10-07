/*
 * test_nodb.c - assertions for the TLANG_NO_PG build of src/pg.c, with no
 * database server required.
 *
 * This file is NOT named test_pg_* on purpose: the Makefile excludes test_pg_*
 * from the PG=0 build, but the NO_PG behavior must be covered under PG=0. So
 * this test is built in both configurations. Under PG=0 (-DTLANG_NO_PG) it
 * asserts that every tlang_db_* / tlang_tx_begin / tlang_tx_commit throws
 * 500 "database support not compiled in", tlang_tx_rollback is a no-op, and
 * tlang_pg_pool_create succeeds. Under PG=1 there is nothing to assert without
 * a live database (test_pg_* cover that path), so the test is a clean no-op.
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

#ifdef TLANG_NO_PG

static MemoryArena g_arena;
static tlang_fiber g_fib;

static bool msg_is(tlang_string s, const char* expect) {
    size_t n = strlen(expect);
    return s.data != NULL && s.len == n && memcmp(s.data, expect, n) == 0;
}

static void reset(void) {
    g_fib.err = 0;
    g_fib.error.status = 0;
    g_fib.error.message = TLANG_STR("");
}

/* Every db call throws 500 TLANG_MSG_NO_DB and returns the zero value. */
static void test_db_calls_throw(void) {
    tlang_type_desc desc;
    memset(&desc, 0, sizeof desc);

    reset();
    CHECK(tlang_db_execute(&g_fib, NULL, TLANG_STR("SELECT 1"), NULL, 0) == 0);
    CHECK(g_fib.err && g_fib.error.status == 500);
    CHECK(msg_is(g_fib.error.message, "database support not compiled in"));

    reset();
    CHECK(tlang_db_query(&g_fib, NULL, TLANG_STR("SELECT 1"), NULL, 0, &desc) == NULL);
    CHECK(g_fib.err && g_fib.error.status == 500);
    CHECK(msg_is(g_fib.error.message, TLANG_MSG_NO_DB));

    reset();
    CHECK(tlang_db_query_one(&g_fib, NULL, TLANG_STR("SELECT 1"), NULL, 0, &desc) == NULL);
    CHECK(g_fib.err && g_fib.error.status == 500);
    CHECK(msg_is(g_fib.error.message, TLANG_MSG_NO_DB));
}

/* tlang_tx_begin throws 500 and leaves the handle NONE with no connection. */
static void test_tx_begin_throws(void) {
    TxContext tx;
    reset();
    CHECK(tlang_tx_begin(&g_fib, &tx) == false);
    CHECK(g_fib.err && g_fib.error.status == 500);
    CHECK(msg_is(g_fib.error.message, TLANG_MSG_NO_DB));
    CHECK(tx.state == TLANG_TX_NONE);
    CHECK(tx.conn == NULL);
}

/* tlang_tx_commit throws 500. */
static void test_tx_commit_throws(void) {
    TxContext tx;
    tx.conn = NULL;
    tx.state = TLANG_TX_ACTIVE;
    reset();
    CHECK(tlang_tx_commit(&g_fib, &tx) == false);
    CHECK(g_fib.err && g_fib.error.status == 500);
    CHECK(msg_is(g_fib.error.message, TLANG_MSG_NO_DB));
}

/* tlang_tx_rollback is a no-op: it never sets err and preserves a pending
 * error. */
static void test_tx_rollback_noop(void) {
    TxContext tx;
    tx.conn = NULL;
    tx.state = TLANG_TX_NONE;

    reset();
    tlang_tx_rollback(&g_fib, &tx);
    CHECK(!g_fib.err);

    /* It preserves an error that was already pending. */
    tlang_throw(&g_fib, 418, TLANG_STR("teapot"));
    tlang_tx_rollback(&g_fib, &tx);
    CHECK(g_fib.err && g_fib.error.status == 418);
    CHECK(msg_is(g_fib.error.message, "teapot"));
}

/* tlang_pg_pool_create succeeds and tlang_pg_pool_destroy is a safe no-op. */
static void test_pool_create_succeeds(void) {
    tlang_sched s;
    char err[128];
    memset(&s, 0, sizeof s);
    err[0] = 'x';
    CHECK(tlang_pg_pool_create(&s, err, sizeof err) == 0);
    CHECK(s.db_pool == NULL);
    tlang_pg_pool_destroy(&s);  /* must not crash */
    CHECK(s.db_pool == NULL);
}

int main(void) {
    if (tlang_arena_init(&g_arena) != 0) { perror("tlang_arena_init"); return 2; }
    g_fib.arena = &g_arena;

    test_db_calls_throw();
    test_tx_begin_throws();
    test_tx_commit_throws();
    test_tx_rollback_noop();
    test_pool_create_succeeds();

    tlang_arena_destroy(&g_arena);

    if (failures != 0) {
        printf("FAIL test_nodb (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_nodb\n");
    return 0;
}

#else /* !TLANG_NO_PG */

int main(void) {
    /* PG=1 build: the live-database behavior is covered by test_pg_* when
     * TLANG_TEST_DATABASE_URL is set. Nothing to assert here without a server. */
    (void)failures;
    printf("PASS test_nodb (PG=1 no-op)\n");
    return 0;
}

#endif /* TLANG_NO_PG */
