package codegen

import (
	"strings"
	"testing"
)

// TestDBCalls checks the db call shapes (codegen design §10.3): execute with
// autocommit NULL and a sized param array, query/queryOne with the row
// descriptor passed last and the result cast, and K == 0 giving NULL, 0.
func TestDBCalls(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
    name: string;
}

fn route_dispatcher(ctx: Context): void {
    let id: int64 = 1;
    let n = db.execute("UPDATE users SET name = $1 WHERE id = $2", "x", id);
    let n0 = db.execute("DELETE FROM users");
    let all = db.query<User>("SELECT * FROM users");
    let one = db.queryOne<User>("SELECT * FROM users WHERE id = $1", id);
}
`)
	if !strings.Contains(out, `tlang_db_execute(__fib, NULL, TLANG_STR("UPDATE users SET name = $1 WHERE id = $2"), (tlang_pg_param[2]){ TLANG_PG_STR(TLANG_STR("x")), TLANG_PG_I64(l_id) }, 2)`) {
		t.Error("db.execute shape wrong")
	}
	if !strings.Contains(out, `tlang_db_execute(__fib, NULL, TLANG_STR("DELETE FROM users"), NULL, 0)`) {
		t.Error("db.execute with no params should pass NULL, 0")
	}
	if !strings.Contains(out, `(tlang_slice_User*)tlang_db_query(__fib, NULL, TLANG_STR("SELECT * FROM users"), NULL, 0, &__tl_td_User)`) {
		t.Error("db.query shape wrong")
	}
	if !strings.Contains(out, `(tl_User*)tlang_db_query_one(__fib, NULL, TLANG_STR("SELECT * FROM users WHERE id = $1"), (tlang_pg_param[1]){ TLANG_PG_I64(l_id) }, 1, &__tl_td_User)`) {
		t.Error("db.queryOne shape wrong")
	}
}

// TestRowDescriptor checks the row descriptor emission (codegen design §10.3,
// plan D19, COVERAGE-NIT-5, P10): field kinds (base and optional), the column
// name is the TLang field name, the type name is n.Name(), and a zero-field
// row has no __tl_fd array and uses 0, NULL.
func TestRowDescriptor(t *testing.T) {
	out := mustEmit(t, `interface Row {
    id: int64;
    name: string;
    email: string | null;
    age: int32 | null;
    active: bool;
}

interface Empty {}

fn route_dispatcher(ctx: Context): void {
    let r = db.query<Row>("SELECT 1");
    let e = db.query<Empty>("SELECT 2");
}
`)
	if !strings.Contains(out, `{ TLANG_STR_INIT("id"), TLANG_KIND_I64, offsetof(tl_Row, f_id) }`) {
		t.Error("missing i64 field descriptor")
	}
	if !strings.Contains(out, `{ TLANG_STR_INIT("email"), TLANG_KIND_OPT_STR, offsetof(tl_Row, f_email) }`) {
		t.Error("missing optional string field descriptor")
	}
	if !strings.Contains(out, `{ TLANG_STR_INIT("age"), TLANG_KIND_OPT_I32, offsetof(tl_Row, f_age) }`) {
		t.Error("missing optional int32 field descriptor")
	}
	if !strings.Contains(out, `static const tlang_type_desc __tl_td_Row = { TLANG_STR_INIT("Row"), sizeof(tl_Row), 5, __tl_fd_Row, NULL };`) {
		t.Error("Row type descriptor wrong")
	}
	// A zero-field row: no __tl_fd_Empty array, descriptor uses 0, NULL, NULL.
	if strings.Contains(out, "__tl_fd_Empty") {
		t.Error("a zero-field row should have no field-descriptor array")
	}
	if !strings.Contains(out, `static const tlang_type_desc __tl_td_Empty = { TLANG_STR_INIT("Empty"), sizeof(tl_Empty), 0, NULL, NULL };`) {
		t.Error("Empty type descriptor wrong")
	}
}

// TestTxCalls checks the tx data methods inside a transaction body use the
// handle l_tx and the tx runtime wrappers (codegen design §10.3).
func TestTxCalls(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
}

fn route_dispatcher(ctx: Context): void {
    db.transaction((tx) => {
        let k = tx.execute("UPDATE users SET id = $1", 1);
        let us = tx.query<User>("SELECT * FROM users");
        let u1 = tx.queryOne<User>("SELECT * FROM users LIMIT 1");
    });
}
`)
	if !strings.Contains(out, `tlang_tx_exec(__fib, l_tx, TLANG_STR("UPDATE users SET id = $1"), (tlang_pg_param[1]){ TLANG_PG_I64(1) }, 1)`) {
		t.Error("tx.execute shape wrong")
	}
	if !strings.Contains(out, `(tlang_slice_User*)tlang_tx_query(__fib, l_tx, TLANG_STR("SELECT * FROM users"), NULL, 0, &__tl_td_User)`) {
		t.Error("tx.query shape wrong")
	}
	if !strings.Contains(out, `(tl_User*)tlang_tx_query_one(__fib, l_tx, TLANG_STR("SELECT * FROM users LIMIT 1"), NULL, 0, &__tl_td_User)`) {
		t.Error("tx.queryOne shape wrong")
	}
}

// TestDBUnusedRowNotEmitted checks that a row type referenced only from a
// never-instantiated generic body is not emitted (plan D18, P9).
func TestDBUnusedRowNotEmitted(t *testing.T) {
	out := mustEmit(t, `interface Used {
    id: int64;
}

interface Unused {
    id: int64;
}

fn neverCalled<T>(x: T): int64 {
    let rows = db.query<Unused>("SELECT 1");
    return rows.len;
}

fn route_dispatcher(ctx: Context): void {
    let r = db.query<Used>("SELECT 2");
}
`)
	if !strings.Contains(out, "__tl_td_Used") {
		t.Error("the referenced row descriptor should be emitted")
	}
	if strings.Contains(out, "__tl_td_Unused") {
		t.Error("a row type reached only from a never-instantiated generic should not be emitted")
	}
}

// TestConsoleKinds checks message serialization and level selection.
func TestConsoleKinds(t *testing.T) {
	out := mustEmit(t, `fn f(n: int64, i: int32, s: string, fl: float64, b: bool): void {
    console.info("ready", JsonValue.string("ready"));
    console.info("ready", JsonValue.string("ready"));
}
`)
	if !strings.Contains(out, `tlang_console_json(__fib, TLANG_LOG_INFO, TLANG_STR("ready"),`) {
		t.Error("structured console info call missing")
	}
}
