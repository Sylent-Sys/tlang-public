/*
 * test_pg_query.c - db.query<T> / db.queryOne<T> and column mapping (src/pg.c).
 *
 * Requires TLANG_TEST_DATABASE_URL; prints SKIP and exits 0 when unset (no
 * database server in this sandbox, which is expected). When set it maps query
 * results onto a row struct and checks the type_desc mapping rules: exact and
 * case-insensitive column names, int/str/opt fields, SQL NULL into optionals.
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

/* A DB-mapped row: `id` (int64), `name` (string), `email` (string | null). */
typedef struct row_User {
    int64_t id;
    tlang_string name;
    tlang_string email;   /* data == NULL means SQL NULL */
} row_User;

TLANG_SLICE_DEFINE(User, row_User*)

static const tlang_field_desc fd_User[3] = {
    { TLANG_STR_INIT("id"), TLANG_KIND_I64, offsetof(row_User, id) },
    { TLANG_STR_INIT("name"), TLANG_KIND_STR, offsetof(row_User, name) },
    { TLANG_STR_INIT("email"), TLANG_KIND_OPT_STR, offsetof(row_User, email) }
};
static const tlang_type_desc td_User = {
    TLANG_STR_INIT("User"), sizeof(row_User), 3, fd_User, NULL
};

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

static bool str_is(tlang_string s, const char* e) {
    size_t n = strlen(e);
    return s.data != NULL && s.len == n && memcmp(s.data, e, n) == 0;
}

static void query_body(Fiber* self, void* arg) {
    tlang_fiber* f = &self->pub;
    (void)arg;

    tlang_db_execute(f, NULL,
                     TLANG_STR("CREATE TEMP TABLE tl_users (id bigint, name text, email text)"),
                     NULL, 0);
    CHECK(!f->err);
    tlang_db_execute(f, NULL,
                     TLANG_STR("INSERT INTO tl_users VALUES (1,'alice','a@x'),(2,'bob',NULL)"),
                     NULL, 0);
    CHECK(!f->err);

    /* queryOne: first row, exact column names. */
    {
        row_User* u = (row_User*)tlang_db_query_one(
            f, NULL, TLANG_STR("SELECT id, name, email FROM tl_users WHERE id = 1"),
            NULL, 0, &td_User);
        CHECK(!f->err);
        CHECK(u != NULL);
        if (u != NULL) {
            CHECK(u->id == 1);
            CHECK(str_is(u->name, "alice"));
            CHECK(str_is(u->email, "a@x"));
        }
    }

    /* queryOne: SQL NULL into the optional email field (email.data == NULL). */
    {
        row_User* u = (row_User*)tlang_db_query_one(
            f, NULL, TLANG_STR("SELECT id, name, email FROM tl_users WHERE id = 2"),
            NULL, 0, &td_User);
        CHECK(!f->err);
        CHECK(u != NULL);
        if (u != NULL) {
            CHECK(u->id == 2);
            CHECK(str_is(u->name, "bob"));
            CHECK(u->email.data == NULL);   /* null optional */
        }
    }

    /* queryOne on an empty result returns NULL without an error. */
    {
        row_User* u = (row_User*)tlang_db_query_one(
            f, NULL, TLANG_STR("SELECT id, name, email FROM tl_users WHERE id = 999"),
            NULL, 0, &td_User);
        CHECK(!f->err);
        CHECK(u == NULL);
    }

    /* query: all rows, case-insensitive aliases (PostgreSQL folds to lower). */
    {
        tlang_slice_User* rows = (tlang_slice_User*)tlang_db_query(
            f, NULL,
            TLANG_STR("SELECT id AS ID, name AS NAME, email AS EMAIL FROM tl_users ORDER BY id"),
            NULL, 0, &td_User);
        CHECK(!f->err);
        CHECK(rows != NULL);
        if (rows != NULL) {
            CHECK(rows->len == 2);
            if (rows->len == 2) {
                CHECK(rows->items[0]->id == 1);
                CHECK(str_is(rows->items[0]->name, "alice"));
                CHECK(rows->items[1]->id == 2);
                CHECK(rows->items[1]->email.data == NULL);
            }
        }
    }

    /* Extra columns are ignored; mapping still succeeds. */
    {
        row_User* u = (row_User*)tlang_db_query_one(
            f, NULL,
            TLANG_STR("SELECT id, name, email, 42 AS extra FROM tl_users WHERE id = 1"),
            NULL, 0, &td_User);
        CHECK(!f->err);
        CHECK(u != NULL && u->id == 1);
    }
}

int main(void) {
    if (getenv("TLANG_TEST_DATABASE_URL") == NULL) {
        printf("SKIP test_pg_query (TLANG_TEST_DATABASE_URL unset)\n");
        return 0;
    }
    run_fiber(query_body);
    if (failures != 0) {
        printf("FAIL test_pg_query (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_pg_query\n");
    return 0;
}
