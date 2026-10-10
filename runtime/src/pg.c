/*
 * pg.c - PostgreSQL access (tlang.h §16, tlang_internal.h §9, DESIGN.md §2.11,
 * spec §11). docs/RUNTIME.md §4 assigns these functions here:
 *   tlang_db_execute, tlang_db_query, tlang_db_query_one,
 *   tlang_tx_begin, tlang_tx_commit, tlang_tx_rollback,
 *   tlang_pg_pool_create, tlang_pg_pool_destroy.
 * (tlang_tx_exec / tlang_tx_query / tlang_tx_query_one are header-inline.)
 *
 * Two builds:
 *   -DTLANG_NO_PG   compiles without libpq. Every tlang_db_* / tlang_tx_begin /
 *                   tlang_tx_commit throws 500 "database support not compiled
 *                   in", tlang_tx_rollback is a no-op, tlang_pg_pool_create
 *                   succeeds and tlang_pg_pool_destroy is a safe no-op.
 *   otherwise       async libpq only (PQconnectStart/Poll, PQsendQueryParams or
 *                   PQsendPrepare/PQsendQueryPrepared, PQflush, PQconsumeInput/
 *                   PQisBusy, PQgetResult until NULL); every wait goes through
 *                   tlang_wait_fd2(f, PQsocket(conn), ev, f->watch_fd, deadline).
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* ========================================================================
 * TLANG_NO_PG: database support not compiled in.
 * ======================================================================== */
#ifdef TLANG_NO_PG

int64_t tlang_db_execute(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams) {
    (void)tx; (void)sql; (void)params; (void)nparams;
    tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB),
                      TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
    return 0;
}

void* tlang_db_query(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                     const tlang_pg_param* params, int nparams,
                     const tlang_type_desc* desc) {
    (void)tx; (void)sql; (void)params; (void)nparams; (void)desc;
    tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB),
                      TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
    return NULL;
}

void* tlang_db_query_one(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams,
                         const tlang_type_desc* desc) {
    (void)tx; (void)sql; (void)params; (void)nparams; (void)desc;
    tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB),
                      TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
    return NULL;
}

bool tlang_tx_begin(tlang_fiber* fib, tlang_tx* tx) {
    if (tx != NULL) {
        tx->conn = NULL;
        tx->state = TLANG_TX_NONE;
    }
    tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB),
                      TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
    return false;
}

bool tlang_tx_commit(tlang_fiber* fib, tlang_tx* tx) {
    (void)tx;
    tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB),
                      TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
    return false;
}

void tlang_tx_rollback(tlang_fiber* fib, tlang_tx* tx) {
    /* No-op: no connection was ever taken. Preserves any pending error. */
    (void)fib; (void)tx;
}

int tlang_pg_pool_create(tlang_sched* s, char* err, size_t errlen) {
    /* Succeeds: a NO_PG build has no pool, every db call throws later. */
    (void)err; (void)errlen;
    if (s != NULL) s->db_pool = NULL;
    return 0;
}

void tlang_pg_pool_destroy(tlang_sched* s) {
    if (s != NULL) s->db_pool = NULL;
}

#else /* !TLANG_NO_PG */

/* ========================================================================
 * Async libpq implementation.
 * ======================================================================== */
#include <libpq-fe.h>

/* Binary wire OIDs for scalar parameters (tlang.h §16 wire-encoding note). */
#define TLANG_OID_INT4  23
#define TLANG_OID_INT8  20
#define TLANG_OID_FLOAT8 701
#define TLANG_OID_BOOL  16

/* One cached prepared statement on a connection. The key is the SQL text (its
 * bytes come from a string literal in generated code and never change) plus
 * the parameter kinds, so a statement is reused only for an identical call
 * shape. The statement name is a short decimal string unique to the
 * connection. */
typedef struct tlang_pg_stmt {
    struct tlang_pg_stmt* next;
    uint32_t hash;              /* hash of the SQL bytes */
    char* sql;                  /* owned NUL-terminated copy of the SQL text */
    size_t sql_len;
    int nparams;
    int* kinds;                 /* nparams parameter kinds, owned */
    char name[24];              /* prepared-statement name */
} tlang_pg_stmt;

/* A pooled connection. The cleanup node lives inside this object so a request
 * abort returns the connection (tlang.h §16, RUNTIME.md §6). */
typedef struct tlang_pg_conn {
    struct tlang_pg_conn* next; /* free-list / all-connections link */
    PGconn* pg;                 /* NULL until connected; closed and reopened on error */
    tlang_sched* sched;         /* owner scheduler */
    tlang_pg_stmt* stmts;       /* prepared-statement cache */
    unsigned stmt_seq;          /* next prepared-statement number */
    bool broken;                /* connection is in an unknown/bad state: close on release */
    tlang_cleanup cleanup;      /* release hook, pushed while held by a fiber */
    Fiber* holder;              /* fiber that currently holds it (for the cleanup) */
} tlang_pg_conn;

/* Per-scheduler pool (tlang_internal.h §9). Connections are opened on demand. */
struct tlang_pg_pool {
    tlang_sched* sched;
    const char* url;            /* libpq conninfo; SECRET, never logged */
    int max_conns;              /* cfg->db_pool_size */
    uint32_t pool_timeout_ms;   /* cfg->db_pool_timeout_ms */
    uint32_t stmt_timeout_ms;   /* cfg->db_statement_timeout_ms (0 = off) */
    int created;                /* connections created so far */
    int available;             /* idle connections on the free list */
    tlang_pg_conn* idle;        /* free list of idle connections */
    tlang_pg_conn* all;         /* every connection ever created (for destroy) */
    tlang_waitq waiters;        /* FIFO of fibers waiting for a connection */
};

/* ---- forward declarations --------------------------------------------- */

static PGresult* pg_run(Fiber* f, tlang_pg_conn* c, uint64_t deadline,
                        bool* peer_gone, bool* io_err);
static bool pg_set_statement_timeout(Fiber* f, tlang_pg_conn* c, uint32_t ms);
static void pg_conn_reset_or_close(Fiber* f, struct tlang_pg_pool* pool,
                                   tlang_pg_conn** pc, uint64_t deadline);
static void pg_conn_cleanup(Fiber* f, void* arg);
static void pg_pool_return(struct tlang_pg_pool* pool, tlang_pg_conn* c);

/* ---- small helpers ---------------------------------------------------- */

/* FNV-1a over n bytes. */
static uint32_t pg_hash(const char* p, size_t n) {
    uint32_t h = 2166136261u;
    size_t i;
    for (i = 0; i < n; i++) {
        h ^= (unsigned char)p[i];
        h *= 16777619u;
    }
    return h;
}

/* Monotonic deadline `ms` from now, 0 meaning "none" is avoided by using a
 * never-zero value when ms is 0 (callers always pass a positive timeout). */
static uint64_t pg_deadline(uint32_t ms) {
    return tlang_now_ns() + (uint64_t)ms * 1000000ull;
}

/* Copies [p, p+n) into the arena as a NUL-terminated string. */
static char* pg_arena_cstr(tlang_fiber* fib, const char* p, size_t n) {
    char* out = (char*)tlang_alloc_raw(fib, n + 1);
    if (n != 0 && p != NULL) memcpy(out, p, n);
    out[n] = '\0';
    return out;
}

/* ---- pool creation / destruction -------------------------------------- */

int tlang_pg_pool_create(tlang_sched* s, char* err, size_t errlen) {
    struct tlang_pg_pool* pool;
    const tlang_config* cfg = s->cfg;

    if (cfg->database_url == NULL || cfg->database_url[0] == '\0') {
        /* Never echo the URL (it is empty here anyway) nor its name's value. */
        snprintf(err, errlen, "database URL not configured");
        s->db_pool = NULL;
        return -1;
    }

    pool = (struct tlang_pg_pool*)calloc(1, sizeof *pool);
    if (pool == NULL) {
        snprintf(err, errlen, "out of memory creating database pool");
        s->db_pool = NULL;
        return -1;
    }
    pool->sched = s;
    pool->url = cfg->database_url;
    pool->max_conns = cfg->db_pool_size;
    pool->pool_timeout_ms = cfg->db_pool_timeout_ms;
    pool->stmt_timeout_ms = cfg->db_statement_timeout_ms;
    pool->created = 0;
    pool->available = 0;
    pool->idle = NULL;
    pool->all = NULL;
    tlang_waitq_init(&pool->waiters);

    s->db_pool = pool;
    return 0;
}

static void pg_stmt_free_list(tlang_pg_stmt* st) {
    while (st != NULL) {
        tlang_pg_stmt* next = st->next;
        free(st->sql);
        free(st->kinds);
        free(st);
        st = next;
    }
}

static void pg_conn_close(tlang_pg_conn* c) {
    if (c->pg != NULL) {
        PQfinish(c->pg);
        c->pg = NULL;
    }
    pg_stmt_free_list(c->stmts);
    c->stmts = NULL;
    c->broken = true;
}

void tlang_pg_pool_destroy(tlang_sched* s) {
    struct tlang_pg_pool* pool;
    tlang_pg_conn* c;

    if (s == NULL) return;
    pool = s->db_pool;
    if (pool == NULL) return;

    c = pool->all;
    while (c != NULL) {
        tlang_pg_conn* next = c->next;
        pg_conn_close(c);
        free(c);
        c = next;
    }
    free(pool);
    s->db_pool = NULL;
}

/* ---- connection open (async) ------------------------------------------ */

/* Drives PQconnectPoll to completion, parking the fiber on the connection's
 * socket. Returns true when CONNECTION_OK, false otherwise (never throws). */
static bool pg_connect_poll(Fiber* f, PGconn* pg, uint64_t deadline) {
    for (;;) {
        PostgresPollingStatusType st = PQconnectPoll(pg);
        uint32_t ev;
        int fd;
        int r;

        if (st == PGRES_POLLING_OK) return true;
        if (st == PGRES_POLLING_FAILED) return false;

        fd = PQsocket(pg);
        if (fd < 0) return false;

        if (st == PGRES_POLLING_READING) ev = EPOLLIN;
        else if (st == PGRES_POLLING_WRITING) ev = EPOLLOUT;
        else continue; /* PGRES_POLLING_ACTIVE: loop again */

        /* No watch_fd during connect: a disconnect of the client is handled
         * once the connection is in use. */
        r = tlang_wait_fd(f, fd, ev, deadline);
        if (r == TLANG_WAIT_TIMEOUT || r == TLANG_WAIT_CANCELLED ||
            r == TLANG_WAIT_ERR)
            return false;
        /* Any readiness (including HUP) -> let PQconnectPoll decide. */
    }
}

/* Connects an already-reserved pool node `c` (its slot is counted in
 * pool->created and it is already linked into pool->all by the caller) and
 * runs SET statement_timeout when configured. Returns true on success with
 * c->pg set; on failure returns false leaving c->pg == NULL (the caller
 * releases the reservation). Never throws.
 *
 * The node is reserved and linked into pool->all BEFORE the suspending connect
 * (pg_connect_poll parks the fiber on the connect socket). This is what makes
 * the pool-size limit hold across the yield: a concurrent fiber resuming while
 * this open is in flight sees pool->created == pool->max_conns and takes the
 * waitq/503 path instead of opening its own connection. It also guarantees the
 * node is reachable from pool->all for tlang_pg_pool_destroy, so an in-flight
 * or failed open never leaks. */
static bool pg_conn_open(Fiber* f, struct tlang_pg_pool* pool,
                         tlang_pg_conn* c, uint64_t deadline) {
    PGconn* pg;

    pg = PQconnectStart(pool->url);
    if (pg == NULL) return false;
    if (PQstatus(pg) == CONNECTION_BAD || PQsetnonblocking(pg, 1) != 0) {
        PQfinish(pg);
        return false;
    }
    if (!pg_connect_poll(f, pg, deadline)) {
        PQfinish(pg);
        return false;
    }
    c->pg = pg;

    /* SET statement_timeout on the new connection (cfg->db_statement_timeout_ms). */
    if (!pg_set_statement_timeout(f, c, pool->stmt_timeout_ms)) {
        PQfinish(c->pg);
        c->pg = NULL;
        return false;
    }
    return true;
}

/* ---- query execution (async) ------------------------------------------ */

/* Runs the async send/flush/consume loop for a request on `c`, collecting the
 * last non-NULL PGresult. On client disconnect it cancels the query. Returns
 * the final PGresult (caller PQclears it) or NULL. *peer_gone is set when the
 * HTTP client disconnected; *io_err when a socket/libpq error occurred. */
static PGresult* pg_run(Fiber* f, tlang_pg_conn* c, uint64_t deadline,
                        bool* peer_gone, bool* io_err) {
    PGconn* pg = c->pg;
    int fd = PQsocket(pg);
    PGresult* last = NULL;
    int flush;

    *peer_gone = false;
    *io_err = false;

    /* Flush the request, parking on EPOLLOUT until the buffer drains. */
    while ((flush = PQflush(pg)) == 1) {
        int r = tlang_wait_fd2(f, fd, EPOLLOUT, f->watch_fd, deadline);
        if (r == TLANG_WAIT_PEER_GONE) { *peer_gone = true; goto cancel; }
        if (r == TLANG_WAIT_TIMEOUT || r == TLANG_WAIT_CANCELLED ||
            r == TLANG_WAIT_ERR) { *io_err = true; goto fail; }
        /* Readable before fully flushed is fine: drain input too. */
        if (PQconsumeInput(pg) == 0) { *io_err = true; goto fail; }
    }
    if (flush < 0) { *io_err = true; goto fail; }

    /* Read results until PQgetResult returns NULL. */
    for (;;) {
        while (PQisBusy(pg)) {
            int r = tlang_wait_fd2(f, fd, EPOLLIN, f->watch_fd, deadline);
            if (r == TLANG_WAIT_PEER_GONE) { *peer_gone = true; goto cancel; }
            if (r == TLANG_WAIT_TIMEOUT || r == TLANG_WAIT_CANCELLED ||
                r == TLANG_WAIT_ERR) { *io_err = true; goto fail; }
            if (PQconsumeInput(pg) == 0) { *io_err = true; goto fail; }
        }
        {
            PGresult* res = PQgetResult(pg);
            if (res == NULL) break;
            if (last != NULL) PQclear(last);
            last = res;
        }
    }
    return last;

cancel:
    /* The HTTP client went away: cancel the running query (async cancel). */
    {
        PGcancelConn* cc = PQcancelCreate(pg);
        if (cc != NULL) {
            uint64_t cdl = pg_deadline(1000);
            if (PQcancelStart(cc) != 0) {
                for (;;) {
                    PostgresPollingStatusType st = PQcancelPoll(cc);
                    uint32_t ev;
                    int cfd;
                    int r;
                    if (st == PGRES_POLLING_OK || st == PGRES_POLLING_FAILED) break;
                    cfd = PQcancelSocket(cc);
                    if (cfd < 0) break;
                    if (st == PGRES_POLLING_READING) ev = EPOLLIN;
                    else if (st == PGRES_POLLING_WRITING) ev = EPOLLOUT;
                    else continue;
                    r = tlang_wait_fd(f, cfd, ev, cdl);
                    if (r == TLANG_WAIT_TIMEOUT || r == TLANG_WAIT_CANCELLED ||
                        r == TLANG_WAIT_ERR)
                        break;
                }
            }
            PQcancelFinish(cc);
        }
    }
fail:
    if (last != NULL) { PQclear(last); last = NULL; }
    c->broken = true; /* connection state is now unknown */
    return NULL;
}

/* ---- prepared-statement cache ----------------------------------------- */

/* Looks up a cached statement matching sql + kinds, or NULL. */
static tlang_pg_stmt* pg_stmt_find(tlang_pg_conn* c, uint32_t hash,
                                   tlang_string sql, const int* kinds,
                                   int nparams) {
    tlang_pg_stmt* st;
    for (st = c->stmts; st != NULL; st = st->next) {
        if (st->hash != hash) continue;
        if (st->sql_len != sql.len) continue;
        if (st->nparams != nparams) continue;
        if (sql.len != 0 && memcmp(st->sql, sql.data, sql.len) != 0) continue;
        if (nparams != 0 && memcmp(st->kinds, kinds, (size_t)nparams * sizeof(int)) != 0)
            continue;
        return st;
    }
    return NULL;
}

/* Allocates a cache entry (heap, owned by the connection). NULL on OOM. */
static tlang_pg_stmt* pg_stmt_add(tlang_pg_conn* c, uint32_t hash,
                                  tlang_string sql, const int* kinds,
                                  int nparams) {
    tlang_pg_stmt* st = (tlang_pg_stmt*)calloc(1, sizeof *st);
    if (st == NULL) return NULL;
    st->sql = (char*)malloc(sql.len + 1);
    if (st->sql == NULL) { free(st); return NULL; }
    if (sql.len != 0) memcpy(st->sql, sql.data, sql.len);
    st->sql[sql.len] = '\0';
    st->sql_len = sql.len;
    st->hash = hash;
    st->nparams = nparams;
    if (nparams != 0) {
        st->kinds = (int*)malloc((size_t)nparams * sizeof(int));
        if (st->kinds == NULL) { free(st->sql); free(st); return NULL; }
        memcpy(st->kinds, kinds, (size_t)nparams * sizeof(int));
    }
    snprintf(st->name, sizeof st->name, "tl_s%u", c->stmt_seq++);
    st->next = c->stmts;
    c->stmts = st;
    return st;
}

/* ---- parameter encoding ----------------------------------------------- */

/* Encodes params into text-format arrays libpq can send. All values use text
 * format (OID 0, server-inferred) so a prepared statement can be reused for
 * identical SQL regardless of inferred types. Returns false on OOM-free
 * formatting failure (never: it aborts on OOM). The buffers live in the arena
 * (lifetime request). */
static void pg_encode_params(tlang_fiber* fib, const tlang_pg_param* params,
                             int nparams, const char*** out_values,
                             int** out_kinds) {
    const char** values;
    int* kinds;
    int i;

    if (nparams == 0) {
        *out_values = NULL;
        *out_kinds = NULL;
        return;
    }
    values = (const char**)tlang_alloc_raw(fib, (size_t)nparams * sizeof(char*));
    kinds = (int*)tlang_alloc_raw(fib, (size_t)nparams * sizeof(int));

    for (i = 0; i < nparams; i++) {
        const tlang_pg_param* p = &params[i];
        kinds[i] = p->kind;
        if (p->is_null) {
            values[i] = NULL;   /* SQL NULL */
            continue;
        }
        switch (p->kind) {
        case TLANG_KIND_I32: {
            char buf[TLANG_FMT_I64_MAX];
            size_t n = tlang_fmt_i64(buf, (int64_t)p->v.i32);
            values[i] = pg_arena_cstr(fib, buf, n);
            break;
        }
        case TLANG_KIND_I64: {
            char buf[TLANG_FMT_I64_MAX];
            size_t n = tlang_fmt_i64(buf, p->v.i64);
            values[i] = pg_arena_cstr(fib, buf, n);
            break;
        }
        case TLANG_KIND_F64: {
            char buf[TLANG_FMT_F64_MAX];
            size_t n = tlang_fmt_f64(buf, p->v.f64, false);
            values[i] = pg_arena_cstr(fib, buf, n);
            break;
        }
        case TLANG_KIND_BOOL:
            values[i] = p->v.b ? "t" : "f";
            break;
        case TLANG_KIND_STR:
            values[i] = pg_arena_cstr(fib, p->v.str.data, p->v.str.len);
            break;
        default:
            /* NULL kind without is_null: treat as SQL NULL. */
            values[i] = NULL;
            break;
        }
    }
    *out_values = values;
    *out_kinds = kinds;
}

/* ---- taking and returning connections --------------------------------- */

/* Takes a connection from the pool, waiting up to pool_timeout_ms. On success
 * returns a connection, pushes its cleanup hook, and records the holder. On
 * failure throws (503 pool timeout / 503 unavailable / 499 client gone) and
 * returns NULL. */
static tlang_pg_conn* pg_pool_take(Fiber* f, struct tlang_pg_pool* pool) {
    tlang_pg_conn* c = NULL;
    uint64_t deadline = pg_deadline(pool->pool_timeout_ms);

    if (pool->idle != NULL) {
        c = pool->idle;
        pool->idle = c->next;
        pool->available--;
    } else if (pool->created < pool->max_conns) {
        /* Reserve the slot BEFORE the suspending open: allocate the node, link
         * it into pool->all, and bump pool->created now, so a concurrent fiber
         * that resumes while pg_connect_poll has this fiber parked sees the
         * pool as full and takes the waitq/503 path. Linking into pool->all up
         * front also makes the node reachable from tlang_pg_pool_destroy on
         * every path, so an in-flight or failed open never leaks. */
        c = (tlang_pg_conn*)calloc(1, sizeof *c);
        if (c == NULL) {
            tlang_throw_typed(&f->pub, TLANG_STATUS_UNAVAILABLE, TLANG_STR(TLANG_MSG_DB_UNAVAILABLE),
                             TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
            return NULL;
        }
        c->sched = pool->sched;
        c->next = pool->all;
        pool->all = c;
        pool->created++;

        if (!pg_conn_open(f, pool, c, deadline)) {
            /* Release the reservation exactly once: unlink, drop the count,
             * close (frees any cached statements) and free the node. */
            tlang_pg_conn** link = &pool->all;
            while (*link != NULL && *link != c) link = &(*link)->next;
            if (*link == c) *link = c->next;
            pool->created--;
            pg_conn_close(c);
            free(c);
            tlang_throw_typed(&f->pub, TLANG_STATUS_UNAVAILABLE, TLANG_STR(TLANG_MSG_DB_UNAVAILABLE),
                             TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
            return NULL;
        }
    } else {
        /* Pool is full: wait for a connection to be handed off directly. */
        void* v = NULL;
        int r = tlang_waitq_wait(&pool->waiters, f, deadline, &v);
        if (r == TLANG_WAIT_OK) {
            c = (tlang_pg_conn*)v;
        } else if (r == TLANG_WAIT_TIMEOUT) {
            tlang_throw_typed(&f->pub, TLANG_STATUS_UNAVAILABLE, TLANG_STR(TLANG_MSG_POOL_TIMEOUT),
                             TLANG_STR(TLANG_ERROR_TIMEOUT), TLANG_STR(TLANG_ERROR_CODE_DATABASE_POOL_TIMEOUT));
            return NULL;
        } else {
            /* Cancelled by shutdown. */
            tlang_throw_typed(&f->pub, TLANG_STATUS_UNAVAILABLE, TLANG_STR(TLANG_MSG_POOL_TIMEOUT),
                             TLANG_STR(TLANG_ERROR_CANCELLED), TLANG_STR(TLANG_ERROR_CODE_DATABASE_POOL_CANCELLED));
            return NULL;
        }
    }

    /* If a handed-off / idle connection is broken, open a fresh one. */
    if (c->pg == NULL || c->broken || PQstatus(c->pg) != CONNECTION_OK) {
        pg_conn_reset_or_close(f, pool, &c, deadline);
        if (c == NULL) {
            tlang_throw_typed(&f->pub, TLANG_STATUS_UNAVAILABLE, TLANG_STR(TLANG_MSG_DB_UNAVAILABLE),
                             TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
            return NULL;
        }
    }

    c->broken = false;
    c->holder = f;
    c->cleanup.fn = pg_conn_cleanup;
    c->cleanup.arg = c;
    tlang_cleanup_push(f, &c->cleanup);
    return c;
}

/* Returns a connection to the pool: hands it to the oldest waiter directly, or
 * puts it on the idle list. A broken connection is closed first (its slot is
 * still reusable: a later take reopens it). */
static void pg_pool_return(struct tlang_pg_pool* pool, tlang_pg_conn* c) {
    c->holder = NULL;
    if (c->broken && c->pg != NULL) {
        PQfinish(c->pg);
        c->pg = NULL;
        pg_stmt_free_list(c->stmts);
        c->stmts = NULL;
    }
    /* Direct hand-off to the oldest waiter (FIFO). */
    if (tlang_waitq_wake_one(&pool->waiters, c)) return;
    c->next = pool->idle;
    pool->idle = c;
    pool->available++;
}

/* Pops the cleanup hook and returns the connection. */
static void pg_release(Fiber* f, struct tlang_pg_pool* pool, tlang_pg_conn* c) {
    tlang_cleanup_pop(f, &c->cleanup);
    pg_pool_return(pool, c);
}

static void pg_conn_cleanup(Fiber* f, void* arg) {
    tlang_pg_conn* c = (tlang_pg_conn*)arg;
    struct tlang_pg_pool* pool = f->sched->db_pool;
    /* The request is unwinding: the connection's state is unknown. Close it so
     * no half-finished transaction leaks, then return the slot to the pool. */
    c->broken = true;
    if (pool != NULL) pg_pool_return(pool, c);
}

/* ---- statement_timeout on a fresh connection -------------------------- */

/* Runs `SET statement_timeout = <ms>` on a newly opened connection. Returns
 * true on success. */
static bool pg_set_statement_timeout(Fiber* f, tlang_pg_conn* c, uint32_t ms) {
    char sql[64];
    uint64_t deadline = pg_deadline(5000);
    bool peer_gone = false, io_err = false;
    PGresult* res;

    if (ms == 0) return true;
    snprintf(sql, sizeof sql, "SET statement_timeout = %u", (unsigned)ms);
    if (PQsendQuery(c->pg, sql) == 0) { c->broken = true; return false; }
    res = pg_run(f, c, deadline, &peer_gone, &io_err);
    if (res == NULL) return false;
    {
        bool ok = PQresultStatus(res) == PGRES_COMMAND_OK;
        PQclear(res);
        if (!ok) c->broken = true;
        return ok;
    }
}

/* Reopens a broken/handed-off connection or closes it. On success *pc holds a
 * usable connection with statement_timeout set; on failure *pc is NULL (the
 * slot is removed from pool->all and freed). */
static void pg_conn_reset_or_close(Fiber* f, struct tlang_pg_pool* pool,
                                   tlang_pg_conn** pc, uint64_t deadline) {
    tlang_pg_conn* c = *pc;
    PGconn* pg;

    /* Close the old handle and its cached statements. */
    if (c->pg != NULL) { PQfinish(c->pg); c->pg = NULL; }
    pg_stmt_free_list(c->stmts);
    c->stmts = NULL;
    c->stmt_seq = 0;

    pg = PQconnectStart(pool->url);
    if (pg == NULL || PQstatus(pg) == CONNECTION_BAD ||
        PQsetnonblocking(pg, 1) != 0 || !pg_connect_poll(f, pg, deadline)) {
        if (pg != NULL) PQfinish(pg);
        goto drop;
    }
    c->pg = pg;
    c->broken = false;
    if (!pg_set_statement_timeout(f, c, pool->stmt_timeout_ms)) {
        PQfinish(c->pg);
        c->pg = NULL;
        goto drop;
    }
    return;

drop:
    /* Remove the slot from pool->all and free it. */
    {
        tlang_pg_conn** link = &pool->all;
        while (*link != NULL && *link != c) link = &(*link)->next;
        if (*link == c) *link = c->next;
        pool->created--;
        free(c);
    }
    *pc = NULL;
}

/* ---- shared statement runner ------------------------------------------ */

/* Sends a parameterised statement over `c` using the prepared-statement cache,
 * runs it to completion and returns the final PGresult (caller PQclears). On
 * failure throws the right status and returns NULL. */
static PGresult* pg_exec_on(Fiber* f, tlang_pg_conn* c, tlang_string sql,
                            const tlang_pg_param* params, int nparams) {
    tlang_fiber* fib = &f->pub;
    const char** values = NULL;
    int* kinds = NULL;
    uint32_t hash;
    tlang_pg_stmt* st;
    uint64_t deadline;
    bool peer_gone = false, io_err = false;
    PGresult* res;

    pg_encode_params(fib, params, nparams, &values, &kinds);
    hash = pg_hash(sql.data, sql.len);
    deadline = c->sched->db_pool->stmt_timeout_ms != 0
        ? pg_deadline(c->sched->db_pool->stmt_timeout_ms + 2000)
        : pg_deadline(60000);

    st = pg_stmt_find(c, hash, sql, kinds, nparams);
    if (st == NULL) {
        /* Prepare the statement first (text SQL comes from a literal). */
        st = pg_stmt_add(c, hash, sql, kinds, nparams);
        if (st == NULL) {
            /* OOM on the cache entry: fall back to an unnamed exec using the
             * SQL text directly (it is NUL-terminated: sql.data[sql.len]=0). */
            if (PQsendQueryParams(c->pg, sql.data, nparams, NULL, values,
                                  NULL, NULL, 0) == 0) {
                c->broken = true;
                tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", PQerrorMessage(c->pg));
                return NULL;
            }
            res = pg_run(f, c, deadline, &peer_gone, &io_err);
            goto finish;
        }
        if (PQsendPrepare(c->pg, st->name, st->sql, nparams, NULL) == 0) {
            c->broken = true;
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", PQerrorMessage(c->pg));
            return NULL;
        }
        res = pg_run(f, c, deadline, &peer_gone, &io_err);
        if (res == NULL) goto run_failed;
        if (PQresultStatus(res) != PGRES_COMMAND_OK) {
            char* msg = pg_arena_cstr(fib, PQresultErrorMessage(res),
                                      strlen(PQresultErrorMessage(res)));
            PQclear(res);
            c->broken = true;
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, tlang_str_cstr(msg), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR));
            return NULL;
        }
        PQclear(res);
    }

    if (PQsendQueryPrepared(c->pg, st->name, nparams, values, NULL, NULL, 0) == 0) {
        c->broken = true;
        tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", PQerrorMessage(c->pg));
        return NULL;
    }
    res = pg_run(f, c, deadline, &peer_gone, &io_err);

finish:
    if (res == NULL) goto run_failed;
    {
        ExecStatusType es = PQresultStatus(res);
        if (es != PGRES_COMMAND_OK && es != PGRES_TUPLES_OK) {
            /* Copy the server message into the arena before PQclear. */
            const char* m = PQresultErrorMessage(res);
            char* msg = pg_arena_cstr(fib, m, strlen(m));
            PQclear(res);
            c->broken = true;
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, tlang_str_cstr(msg), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR));
            return NULL;
        }
    }
    return res;

run_failed:
    if (peer_gone)
        tlang_throw_typed(fib, TLANG_STATUS_CLIENT_CLOSED, TLANG_STR(TLANG_MSG_CLIENT_GONE), TLANG_STR(TLANG_ERROR_CANCELLED), TLANG_STR(TLANG_ERROR_CODE_CLIENT_DISCONNECTED));
    else
        tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", c->pg != NULL ? PQerrorMessage(c->pg) : "database error");
    return NULL;
}

/* ---- column mapping ---------------------------------------------------- */

/* Finds the field index for column `col`, exact match then ASCII
 * case-insensitive. Returns -1 when no field matches (column ignored). */
static int pg_field_for_column(const tlang_type_desc* desc, const char* col) {
    tlang_string cs = tlang_str_cstr(col);
    size_t i;
    for (i = 0; i < desc->nfields; i++) {
        if (desc->fields[i].name.len == cs.len && cs.len != 0 &&
            memcmp(desc->fields[i].name.data, cs.data, cs.len) == 0)
            return (int)i;
    }
    for (i = 0; i < desc->nfields; i++) {
        if (tlang_ascii_ieq(desc->fields[i].name, cs)) return (int)i;
    }
    return -1;
}

/* Parses one text-format cell into the field at `fd->offset` in `row`. Returns
 * true on success; on failure throws 500 and returns false. */
static bool pg_map_cell(tlang_fiber* fib, void* row, const tlang_field_desc* fd,
                        const char* text, int len, bool is_null) {
    char* dst = (char*)row + fd->offset;
    int base = fd->kind & ~TLANG_KIND_OPT;
    bool optional = (fd->kind & TLANG_KIND_OPT) != 0;

    if (is_null) {
        if (!optional) {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "null value for non-optional field");
            return false;
        }
        /* Store the null variant. */
        switch (base) {
        case TLANG_KIND_I32: { tlang_opt_i32 o = TLANG_NONE(i32); memcpy(dst, &o, sizeof o); break; }
        case TLANG_KIND_I64: { tlang_opt_i64 o = TLANG_NONE(i64); memcpy(dst, &o, sizeof o); break; }
        case TLANG_KIND_F64: { tlang_opt_f64 o = TLANG_NONE(f64); memcpy(dst, &o, sizeof o); break; }
        case TLANG_KIND_BOOL: { tlang_opt_bool o = TLANG_NONE(bool); memcpy(dst, &o, sizeof o); break; }
        case TLANG_KIND_STR: { tlang_string s = TLANG_STR_NULL; memcpy(dst, &s, sizeof s); break; }
        default: break;
        }
        return true;
    }

    switch (base) {
    case TLANG_KIND_I32: {
        int64_t v;
        if (!tlang_parse_i64(text, (size_t)len, &v) || v < INT32_MIN || v > INT32_MAX) {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "invalid int32 value in result");
            return false;
        }
        if (optional) { tlang_opt_i32 o = TLANG_SOME(i32, (int32_t)v); memcpy(dst, &o, sizeof o); }
        else { int32_t x = (int32_t)v; memcpy(dst, &x, sizeof x); }
        break;
    }
    case TLANG_KIND_I64: {
        int64_t v;
        if (!tlang_parse_i64(text, (size_t)len, &v)) {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "invalid int64 value in result");
            return false;
        }
        if (optional) { tlang_opt_i64 o = TLANG_SOME(i64, v); memcpy(dst, &o, sizeof o); }
        else memcpy(dst, &v, sizeof v);
        break;
    }
    case TLANG_KIND_F64: {
        char* buf = pg_arena_cstr(fib, text, (size_t)len);
        char* endp = NULL;
        double v = strtod(buf, &endp);
        if (endp == buf || *endp != '\0') {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "invalid float64 value in result");
            return false;
        }
        if (optional) { tlang_opt_f64 o = TLANG_SOME(f64, v); memcpy(dst, &o, sizeof o); }
        else memcpy(dst, &v, sizeof v);
        break;
    }
    case TLANG_KIND_BOOL: {
        bool v;
        if (len == 1 && (text[0] == 't' || text[0] == 'f')) v = (text[0] == 't');
        else {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "invalid bool value in result");
            return false;
        }
        if (optional) { tlang_opt_bool o = TLANG_SOME(bool, v); memcpy(dst, &o, sizeof o); }
        else memcpy(dst, &v, sizeof v);
        break;
    }
    case TLANG_KIND_STR: {
        char* copy = pg_arena_cstr(fib, text, (size_t)len);
        tlang_string s; s.data = copy; s.len = (size_t)len;
        memcpy(dst, &s, sizeof s);
        break;
    }
    default:
        break;
    }
    return true;
}

/* Allocates one row (new_row or zeroed) and maps every column onto it. Returns
 * the row on success, NULL on failure (an error is set). */
static void* pg_map_row(tlang_fiber* fib, PGresult* res, int row,
                        const tlang_type_desc* desc, int ncols,
                        const int* col_field) {
    void* obj;
    bool* seen;
    int c;
    size_t i;

    if (desc->new_row != NULL) obj = desc->new_row(fib);
    else obj = tlang_alloc_zeroed(fib, desc->size);

    seen = (bool*)tlang_alloc_zeroed(fib, desc->nfields == 0 ? 1 : desc->nfields * sizeof(bool));

    for (c = 0; c < ncols; c++) {
        int fi = col_field[c];
        if (fi < 0) continue;               /* unmapped column: ignored */
        {
            const tlang_field_desc* fd = &desc->fields[fi];
            bool is_null = PQgetisnull(res, row, c) != 0;
            const char* text = PQgetvalue(res, row, c);
            int len = PQgetlength(res, row, c);
            if (!pg_map_cell(fib, obj, fd, text, len, is_null)) return NULL;
            seen[fi] = true;
        }
    }

    /* Non-optional fields with no matching column fail; optional ones become
     * null (if new_row did not already give them a value, zeroing left them
     * as a null optional / NULL-data string, which is correct). */
    for (i = 0; i < desc->nfields; i++) {
        if (seen[i]) continue;
        if ((desc->fields[i].kind & TLANG_KIND_OPT) == 0 && desc->new_row == NULL) {
            tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_VALUE), "no column for non-optional field");
            return NULL;
        }
    }
    return obj;
}

/* Precomputes column -> field index for every column of res. */
static int* pg_column_map(tlang_fiber* fib, PGresult* res, int ncols,
                          const tlang_type_desc* desc) {
    int* m = (int*)tlang_alloc_raw(fib, (ncols == 0 ? 1 : (size_t)ncols) * sizeof(int));
    int c;
    for (c = 0; c < ncols; c++)
        m[c] = pg_field_for_column(desc, PQfname(res, c));
    return m;
}

/* ---- the three public db calls ---------------------------------------- */

/* Resolves the connection to run on: an active transaction's connection, or a
 * freshly taken pooled connection (then *taken is the pool to return it to).
 * On failure throws and returns NULL. */
static tlang_pg_conn* pg_resolve_conn(Fiber* f, tlang_tx* tx, bool* autocommit) {
    struct tlang_pg_pool* pool = f->sched->db_pool;
    *autocommit = false;

    if (pool == NULL) {
        /* uses_db was false but a db call was made: unavailable. */
        tlang_throw_typed(&f->pub, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
        return NULL;
    }
    if (tx != NULL) {
        if (tx->state != TLANG_TX_ACTIVE || tx->conn == NULL) {
            tlang_throw_typed(&f->pub, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_TX_INACTIVE), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_TX_INACTIVE));
            return NULL;
        }
        return tx->conn;
    }
    *autocommit = true;
    return pg_pool_take(f, pool);
}

int64_t tlang_db_execute(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams) {
    Fiber* f = tlang_fiber_of(fib);
    bool autocommit = false;
    tlang_pg_conn* c = pg_resolve_conn(f, tx, &autocommit);
    PGresult* res;
    int64_t affected = 0;

    if (c == NULL) return 0;

    res = pg_exec_on(f, c, sql, params, nparams);
    if (res != NULL) {
        const char* t = PQcmdTuples(res);
        if (t != NULL && t[0] != '\0') {
            int64_t v;
            if (tlang_parse_i64(t, strlen(t), &v)) affected = v;
        }
        PQclear(res);
    }
    if (autocommit) pg_release(f, f->sched->db_pool, c);
    return fib->err ? 0 : affected;
}

void* tlang_db_query(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                     const tlang_pg_param* params, int nparams,
                     const tlang_type_desc* desc) {
    Fiber* f = tlang_fiber_of(fib);
    bool autocommit = false;
    tlang_pg_conn* c = pg_resolve_conn(f, tx, &autocommit);
    PGresult* res;
    void* slice = NULL;

    if (c == NULL) return NULL;

    res = pg_exec_on(f, c, sql, params, nparams);
    if (res != NULL) {
        int nrows = PQntuples(res);
        int ncols = PQnfields(res);
        int* col_field = pg_column_map(fib, res, ncols, desc);
        void** items = NULL;
        int r;

        if (nrows > 0)
            items = (void**)tlang_alloc_raw(fib, (size_t)nrows * sizeof(void*));
        for (r = 0; r < nrows; r++) {
            void* row = pg_map_row(fib, res, r, desc, ncols, col_field);
            if (row == NULL) { PQclear(res); goto done; } /* error set */
            items[r] = row;
        }
        PQclear(res);
        res = NULL;

        slice = tlang_slice_new(fib, TLANG_SLICE_HDR_SIZE, false);
        tlang_slice_hdr_init(slice, items, nrows, nrows, false);
    }

done:
    if (autocommit) pg_release(f, f->sched->db_pool, c);
    return fib->err ? NULL : slice;
}

void* tlang_db_query_one(tlang_fiber* fib, tlang_tx* tx, tlang_string sql,
                         const tlang_pg_param* params, int nparams,
                         const tlang_type_desc* desc) {
    Fiber* f = tlang_fiber_of(fib);
    bool autocommit = false;
    tlang_pg_conn* c = pg_resolve_conn(f, tx, &autocommit);
    PGresult* res;
    void* row = NULL;

    if (c == NULL) return NULL;

    res = pg_exec_on(f, c, sql, params, nparams);
    if (res != NULL) {
        int nrows = PQntuples(res);
        int ncols = PQnfields(res);
        if (nrows > 0) {
            int* col_field = pg_column_map(fib, res, ncols, desc);
            row = pg_map_row(fib, res, 0, desc, ncols, col_field);
        }
        PQclear(res);
    }
    if (autocommit) pg_release(f, f->sched->db_pool, c);
    return fib->err ? NULL : row;
}

/* ---- transactions ------------------------------------------------------ */

bool tlang_tx_begin(tlang_fiber* fib, tlang_tx* tx) {
    Fiber* f = tlang_fiber_of(fib);
    struct tlang_pg_pool* pool = f->sched->db_pool;
    tlang_pg_conn* c;
    PGresult* res;
    bool peer_gone = false, io_err = false;

    tx->conn = NULL;
    tx->state = TLANG_TX_NONE;

    if (pool == NULL) {
        tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_NO_DB), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
        return false;
    }

    c = pg_pool_take(f, pool);
    if (c == NULL) return false;     /* pg_pool_take threw already */

    if (PQsendQuery(c->pg, "BEGIN") == 0) {
        c->broken = true;
        tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", PQerrorMessage(c->pg));
        pg_release(f, pool, c);
        return false;
    }
    res = pg_run(f, c, pg_deadline(60000), &peer_gone, &io_err);
    if (res == NULL || PQresultStatus(res) != PGRES_COMMAND_OK) {
        if (res != NULL) {
            const char* m = PQresultErrorMessage(res);
            char* msg = pg_arena_cstr(fib, m, strlen(m));
            PQclear(res);
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, tlang_str_cstr(msg), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR));
        } else if (peer_gone) {
            tlang_throw_typed(fib, TLANG_STATUS_CLIENT_CLOSED, TLANG_STR(TLANG_MSG_CLIENT_GONE), TLANG_STR(TLANG_ERROR_CANCELLED), TLANG_STR(TLANG_ERROR_CODE_CLIENT_DISCONNECTED));
        } else {
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_DB_UNAVAILABLE), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
        }
        c->broken = true;
        pg_release(f, pool, c);
        return false;
    }
    PQclear(res);

    tx->conn = c;
    tx->state = TLANG_TX_ACTIVE;
    return true;
}

bool tlang_tx_commit(tlang_fiber* fib, tlang_tx* tx) {
    Fiber* f = tlang_fiber_of(fib);
    struct tlang_pg_pool* pool = f->sched->db_pool;
    tlang_pg_conn* c;
    PGresult* res;
    bool peer_gone = false, io_err = false;

    if (tx == NULL || tx->state != TLANG_TX_ACTIVE || tx->conn == NULL) {
        tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_TX_INACTIVE), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_TX_INACTIVE));
        return false;
    }
    c = tx->conn;

    if (PQsendQuery(c->pg, "COMMIT") == 0) {
        c->broken = true;
        tlang_throw_fmt_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR), "%s", PQerrorMessage(c->pg));
        return false;   /* connection still held: codegen jumps to rollback */
    }
    res = pg_run(f, c, pg_deadline(60000), &peer_gone, &io_err);
    if (res == NULL || PQresultStatus(res) != PGRES_COMMAND_OK) {
        if (res != NULL) {
            const char* m = PQresultErrorMessage(res);
            char* msg = pg_arena_cstr(fib, m, strlen(m));
            PQclear(res);
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, tlang_str_cstr(msg), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_ERROR));
        } else if (peer_gone) {
            tlang_throw_typed(fib, TLANG_STATUS_CLIENT_CLOSED, TLANG_STR(TLANG_MSG_CLIENT_GONE), TLANG_STR(TLANG_ERROR_CANCELLED), TLANG_STR(TLANG_ERROR_CODE_CLIENT_DISCONNECTED));
        } else {
            tlang_throw_typed(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_DB_UNAVAILABLE), TLANG_STR(TLANG_ERROR_DATABASE), TLANG_STR(TLANG_ERROR_CODE_DATABASE_UNAVAILABLE));
        }
        c->broken = true;
        return false;   /* connection still held */
    }
    PQclear(res);

    pg_release(f, pool, c);
    tx->conn = NULL;
    tx->state = TLANG_TX_DONE;
    return true;
}

void tlang_tx_rollback(tlang_fiber* fib, tlang_tx* tx) {
    Fiber* f = tlang_fiber_of(fib);
    struct tlang_pg_pool* pool;
    tlang_pg_conn* c;

    if (tx == NULL || tx->state != TLANG_TX_ACTIVE || tx->conn == NULL) {
        /* Idempotent: nothing to do for NONE / DONE. */
        if (tx != NULL) { tx->conn = NULL; tx->state = TLANG_TX_DONE; }
        return;
    }
    pool = f->sched->db_pool;
    c = tx->conn;

    /* Try a clean ROLLBACK when the connection state is known; otherwise close
     * it. Never fails: it preserves fib->err / fib->error. */
    if (!c->broken && c->pg != NULL && PQstatus(c->pg) == CONNECTION_OK) {
        bool peer_gone = false, io_err = false;
        if (PQsendQuery(c->pg, "ROLLBACK") != 0) {
            PGresult* res = pg_run(f, c, pg_deadline(60000), &peer_gone, &io_err);
            if (res == NULL || PQresultStatus(res) != PGRES_COMMAND_OK) {
                c->broken = true;
                tlang_log_warn("database rollback failed");
            }
            if (res != NULL) PQclear(res);
        } else {
            c->broken = true;
        }
    } else {
        c->broken = true;
    }

    if (pool != NULL) pg_release(f, pool, c);
    tx->conn = NULL;
    tx->state = TLANG_TX_DONE;
}

#endif /* TLANG_NO_PG */
