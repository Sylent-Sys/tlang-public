/*
 * config.c - process configuration from the environment (tlang_internal.h §2,
 * DESIGN.md §4.2). docs/RUNTIME.md §6, decision 15.
 *
 * tlang_config_defaults fills in the documented defaults (threads = online
 * CPUs). tlang_config_load applies every variable that is set and non-empty,
 * decimal only, range-checked per the tlang_config comment. An error writes a
 * one-line message naming the variable (never the DB URL value) and returns
 * -1. The database URL is never echoed.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static int tl_online_cpus(void) {
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    if (n < 1) n = 1;
    if (n > 1024) n = 1024;
    return (int)n;
}

/* A non-empty environment value, or NULL (empty counts as unset). */
static const char* tl_env(const char* name) {
    const char* v = getenv(name);
    if (v == NULL || v[0] == '\0') return NULL;
    return v;
}

/* Parses a decimal integer env var into *out within [lo, hi]. On a malformed
 * or out-of-range value writes a one-line message naming `name` and returns
 * -1. Returns 1 when applied, 0 when unset. */
static int tl_load_i64(const char* name, int64_t lo, int64_t hi, int64_t* out,
                       char* err, size_t errlen) {
    const char* v = tl_env(name);
    int64_t parsed;
    if (v == NULL) return 0;
    if (!tlang_parse_i64(v, strlen(v), &parsed) || parsed < lo || parsed > hi) {
        snprintf(err, errlen, "%s must be an integer in %lld..%lld",
                 name, (long long)lo, (long long)hi);
        return -1;
    }
    *out = parsed;
    return 1;
}

void tlang_config_defaults(tlang_config* cfg) {
    memset(cfg, 0, sizeof *cfg);
    memcpy(cfg->host, "0.0.0.0", sizeof "0.0.0.0");
    cfg->port = 8080;
    cfg->threads = tl_online_cpus();
    cfg->max_fibers = TLANG_MAX_FIBERS;
    cfg->stack_size = TLANG_STACK_SIZE;
    cfg->max_header_bytes = 8192;
    cfg->max_headers = 32;
    cfg->max_uri_bytes = 2048;
    cfg->max_body_bytes = 1048576;
    cfg->header_timeout_ms = 5000;
    cfg->idle_timeout_ms = 60000;
    cfg->json_max_depth = 32;
    cfg->database_url = NULL;
    cfg->db_pool_size = 8;
    cfg->db_pool_timeout_ms = 2000;
    cfg->db_statement_timeout_ms = 5000;
    cfg->shutdown_timeout_ms = 10000;
}

int tlang_config_load(tlang_config* cfg, char* err, size_t errlen) {
    const char* host;
    int64_t v;
    int r;
    size_t page;

    tlang_config_defaults(cfg);
    if (errlen > 0) err[0] = '\0';

    host = tl_env("TLANG_HOST");
    if (host != NULL) {
        if (strlen(host) >= sizeof cfg->host) {
            snprintf(err, errlen, "TLANG_HOST is too long (max %zu bytes)",
                     sizeof cfg->host - 1);
            return -1;
        }
        memcpy(cfg->host, host, strlen(host) + 1);
    }

#define LOAD(name, lo, hi, field)                                   \
    do {                                                            \
        r = tl_load_i64(name, (lo), (hi), &v, err, errlen);         \
        if (r < 0) return -1;                                       \
        if (r > 0) cfg->field = v;                                  \
    } while (0)

    LOAD("TLANG_PORT", 0, 65535, port);
    LOAD("TLANG_THREADS", 1, 1024, threads);
    LOAD("TLANG_MAX_FIBERS", 1, 1000000, max_fibers);
    LOAD("TLANG_STACK_SIZE", 65536, 67108864, stack_size);
    LOAD("TLANG_MAX_HEADER_BYTES", 1024, 1048576, max_header_bytes);
    LOAD("TLANG_MAX_HEADERS", 1, TLANG_CTX_MAX_HEADERS, max_headers);
    LOAD("TLANG_MAX_URI_BYTES", 16, 65536, max_uri_bytes);
    LOAD("TLANG_MAX_BODY_BYTES", 0, 1073741824, max_body_bytes);
    LOAD("TLANG_HEADER_TIMEOUT_MS", 1, 3600000, header_timeout_ms);
    LOAD("TLANG_IDLE_TIMEOUT_MS", 1, 86400000, idle_timeout_ms);
    LOAD("TLANG_JSON_MAX_DEPTH", 1, 256, json_max_depth);
    LOAD("TLANG_DB_POOL_SIZE", 1, 1024, db_pool_size);
    LOAD("TLANG_DB_POOL_TIMEOUT_MS", 1, 600000, db_pool_timeout_ms);
    LOAD("TLANG_DB_STATEMENT_TIMEOUT_MS", 0, 86400000, db_statement_timeout_ms);
    LOAD("TLANG_SHUTDOWN_TIMEOUT_MS", 0, 600000, shutdown_timeout_ms);

#undef LOAD

    /* Round stack_size up to the page size. */
    page = (size_t)sysconf(_SC_PAGESIZE);
    if (page == 0) page = 4096;
    if (cfg->stack_size % page != 0) {
        cfg->stack_size = (cfg->stack_size + page - 1) / page * page;
    }

    /* Legacy non-manifest programs may still use the environment. Manifest
     * programs replace this with the URL in the validated grant document. */
    cfg->database_url = tl_env("TLANG_DATABASE_URL");
    if (cfg->database_url == NULL) cfg->database_url = tl_env("DATABASE_URL");

    return 0;
}
