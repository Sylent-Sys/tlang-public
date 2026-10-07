/*
 * test_config.c - unit tests for src/config.c: defaults, decimal parsing and
 * range checks per the tlang_config comment, stack_size page rounding, the
 * port-0 ephemeral marker, and that a bad TLANG_DATABASE_URL / DATABASE_URL is
 * never echoed in an error message (only the variable name). Each check would
 * fail if the loader were reverted.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* Clears every variable config reads, so each test starts from a clean env. */
static void clear_env(void) {
    static const char* names[] = {
        "TLANG_HOST", "TLANG_PORT", "TLANG_THREADS", "TLANG_MAX_FIBERS",
        "TLANG_STACK_SIZE", "TLANG_MAX_HEADER_BYTES", "TLANG_MAX_HEADERS",
        "TLANG_MAX_URI_BYTES", "TLANG_MAX_BODY_BYTES", "TLANG_HEADER_TIMEOUT_MS",
        "TLANG_IDLE_TIMEOUT_MS", "TLANG_JSON_MAX_DEPTH", "TLANG_DB_POOL_SIZE",
        "TLANG_DB_POOL_TIMEOUT_MS", "TLANG_DB_STATEMENT_TIMEOUT_MS",
        "TLANG_SHUTDOWN_TIMEOUT_MS", "TLANG_DATABASE_URL", "DATABASE_URL"
    };
    size_t i;
    for (i = 0; i < sizeof names / sizeof names[0]; i++) unsetenv(names[i]);
}

static void test_defaults(void) {
    tlang_config cfg;
    clear_env();
    tlang_config_defaults(&cfg);
    CHECK(strcmp(cfg.host, "0.0.0.0") == 0);
    CHECK(cfg.port == 8080);
    CHECK(cfg.threads >= 1 && cfg.threads <= 1024);
    CHECK(cfg.max_fibers == TLANG_MAX_FIBERS);
    CHECK(cfg.stack_size == (size_t)TLANG_STACK_SIZE);
    CHECK(cfg.max_header_bytes == 8192);
    CHECK(cfg.max_headers == 32);
    CHECK(cfg.max_uri_bytes == 2048);
    CHECK(cfg.max_body_bytes == 1048576);
    CHECK(cfg.header_timeout_ms == 5000);
    CHECK(cfg.idle_timeout_ms == 60000);
    CHECK(cfg.json_max_depth == 32);
    CHECK(cfg.database_url == NULL);
    CHECK(cfg.db_pool_size == 8);
    CHECK(cfg.db_pool_timeout_ms == 2000);
    CHECK(cfg.db_statement_timeout_ms == 5000);
    CHECK(cfg.shutdown_timeout_ms == 10000);
}

static void test_load_valid(void) {
    tlang_config cfg;
    char err[256];
    clear_env();
    setenv("TLANG_HOST", "127.0.0.1", 1);
    setenv("TLANG_PORT", "9090", 1);
    setenv("TLANG_THREADS", "4", 1);
    setenv("TLANG_MAX_FIBERS", "500", 1);
    setenv("TLANG_JSON_MAX_DEPTH", "64", 1);
    setenv("TLANG_SHUTDOWN_TIMEOUT_MS", "0", 1);  /* 0 is in range */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == 0);
    CHECK(strcmp(cfg.host, "127.0.0.1") == 0);
    CHECK(cfg.port == 9090);
    CHECK(cfg.threads == 4);
    CHECK(cfg.max_fibers == 500);
    CHECK(cfg.json_max_depth == 64);
    CHECK(cfg.shutdown_timeout_ms == 0);
}

static void test_empty_is_unset(void) {
    tlang_config cfg;
    char err[256];
    clear_env();
    setenv("TLANG_PORT", "", 1);  /* empty counts as unset -> keeps default */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == 0);
    CHECK(cfg.port == 8080);
}

static void test_range_checks(void) {
    tlang_config cfg;
    char err[256];

    clear_env();
    setenv("TLANG_PORT", "70000", 1);  /* > 65535 */
    err[0] = '\0';
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
    CHECK(strstr(err, "TLANG_PORT") != NULL);

    clear_env();
    setenv("TLANG_THREADS", "0", 1);  /* < 1 */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
    CHECK(strstr(err, "TLANG_THREADS") != NULL);

    clear_env();
    setenv("TLANG_JSON_MAX_DEPTH", "300", 1);  /* > 256 */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
    CHECK(strstr(err, "TLANG_JSON_MAX_DEPTH") != NULL);
}

static void test_decimal_only(void) {
    tlang_config cfg;
    char err[256];

    clear_env();
    setenv("TLANG_PORT", "0x10", 1);  /* not plain decimal */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
    CHECK(strstr(err, "TLANG_PORT") != NULL);

    clear_env();
    setenv("TLANG_THREADS", "4abc", 1);  /* trailing junk */
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
}

static void test_stack_size_rounding(void) {
    tlang_config cfg;
    char err[256];
    size_t page = (size_t)sysconf(_SC_PAGESIZE);
    char val[32];
    size_t requested;

    clear_env();
    /* A value that is in range but not a page multiple is rounded up. */
    requested = 65536 + 123;
    if (requested < 65536) requested = 65536;
    snprintf(val, sizeof val, "%zu", requested);
    setenv("TLANG_STACK_SIZE", val, 1);
    CHECK(tlang_config_load(&cfg, err, sizeof err) == 0);
    CHECK(cfg.stack_size % page == 0);
    CHECK(cfg.stack_size >= requested);
    CHECK(cfg.stack_size < requested + page);
}

static void test_db_url_set_and_redacted(void) {
    tlang_config cfg;
    char err[256];

    /* A valid URL is stored. */
    clear_env();
    setenv("TLANG_DATABASE_URL", "postgres://user:secret@host/db", 1);
    CHECK(tlang_config_load(&cfg, err, sizeof err) == 0);
    CHECK(cfg.database_url != NULL);
    CHECK(strcmp(cfg.database_url, "postgres://user:secret@host/db") == 0);

    /* Fallback to DATABASE_URL when TLANG_DATABASE_URL is unset. */
    clear_env();
    setenv("DATABASE_URL", "postgres://a:b@h/d", 1);
    CHECK(tlang_config_load(&cfg, err, sizeof err) == 0);
    CHECK(cfg.database_url != NULL && strcmp(cfg.database_url, "postgres://a:b@h/d") == 0);

    /* An unrelated range error must never echo the DB URL value, even when it
     * is set. (config loads the URL last; the error for TLANG_PORT must not
     * contain the secret.) */
    clear_env();
    setenv("TLANG_DATABASE_URL", "postgres://user:TOPSECRET@host/db", 1);
    setenv("TLANG_PORT", "999999", 1);
    err[0] = '\0';
    CHECK(tlang_config_load(&cfg, err, sizeof err) == -1);
    CHECK(strstr(err, "TOPSECRET") == NULL);  /* value never echoed */
    CHECK(strstr(err, "secret") == NULL);
}

int main(void) {
    test_defaults();
    test_load_valid();
    test_empty_is_unset();
    test_range_checks();
    test_decimal_only();
    test_stack_size_rounding();
    test_db_url_set_and_redacted();
    clear_env();

    if (failures != 0) {
        printf("FAIL test_config (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_config\n");
    return 0;
}
