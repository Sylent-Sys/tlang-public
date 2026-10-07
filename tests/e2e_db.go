//go:build e2e

// DB bring-up and the self-skip contract for the DB-backed e2e scenarios
// (tests-suites design §7). The suite does NOT provision a cluster: it treats
// TLANG_TEST_DATABASE_URL as an externally provided throwaway connection string
// (the same contract as runtime/tests/test_pg_*.c and the Makefile). When that
// var is unset every DB scenario self-skips-and-passes; when it is set the
// scenarios run and a missing psql is a loud failure, never a silent skip.
//
// No Go PostgreSQL driver is used (stdlib only). The one piece of SQL the
// harness runs itself — idempotent schema DDL — is executed by shelling out to
// the container's psql client. All assertions go through the TLang server's
// HTTP API, so the server stays the system under test.

package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// testDBURLEnv is the TEST trigger variable (distinct from the runtime's
// TLANG_DATABASE_URL): its presence selects run-vs-skip before anything is
// launched, matching the C test_pg_* convention (design §7).
const testDBURLEnv = "TLANG_TEST_DATABASE_URL"

// runtimeDBURLEnv is the runtime config variable the server child reads
// (runtime/src/config.c; fallback DATABASE_URL). The harness sets it from the
// test trigger var when launching a DB-backed fixture.
const runtimeDBURLEnv = "TLANG_DATABASE_URL"

// requireDB returns the DB URL for a DB-backed scenario, skipping the test (and
// passing) when TLANG_TEST_DATABASE_URL is unset, exactly like the C
// test_pg_* harness. A set-but-empty value is treated as unset.
func requireDB(t *testing.T) string {
	t.Helper()
	url := os.Getenv(testDBURLEnv)
	if url == "" {
		t.Skip(testDBURLEnv + " unset; skipping DB-backed e2e")
	}
	return url
}

// dbChildEnv returns the child-process environment entry that wires the runtime
// DB URL from the test URL, to be appended to startServer's env.
func dbChildEnv(url string) []string {
	return []string{runtimeDBURLEnv + "=" + url}
}

// schemaOnce guards the single, serialized schema setup per URL. The DB
// scenarios share one users table and run their legs as PARALLEL subtests, so
// running the DROP/CREATE DDL concurrently would race the PostgreSQL system
// catalog (two simultaneous CREATE TABLE users collide on
// pg_type_typname_nsp_index). The schema is shared and idempotent, so creating
// it exactly once up front is both correct and race-free.
var (
	schemaMu   sync.Mutex
	schemaDone = map[string]error{}
)

// setupSchema creates the users(id bigint primary key, name text) table
// idempotently by shelling out to psql (design §7.2), exactly once per URL for
// the whole test process. Both DB fixtures (examples/app.ts and
// tests/e2e/db_server.tl) share this schema. A set URL promises a usable
// cluster, so a missing psql binary fails loudly rather than skipping — the
// skip is reserved for the unset-URL case only. Serialized via schemaMu so
// parallel legs do not race the catalog.
func setupSchema(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if err, ok := schemaDone[url]; ok {
		if err != nil {
			t.Fatalf("psql schema setup previously failed: %v", err)
		}
		return
	}
	err := createSchema(ctx, url)
	schemaDone[url] = err
	if err != nil {
		t.Fatalf("%v", err)
	}
}

// createSchema runs the idempotent DROP/CREATE DDL once via psql.
func createSchema(ctx context.Context, url string) error {
	if _, err := exec.LookPath("psql"); err != nil {
		return fmt.Errorf("psql not found on PATH but %s is set (a set URL promises a usable cluster): %v", testDBURLEnv, err)
	}
	const ddl = "DROP TABLE IF EXISTS users; CREATE TABLE users(id bigint primary key, name text);"
	cmd := exec.CommandContext(ctx, "psql", url, "-v", "ON_ERROR_STOP=1", "-c", ddl)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql schema setup failed: %v\n%s", err, out)
	}
	return nil
}
