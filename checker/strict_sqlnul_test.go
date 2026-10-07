package checker

import "testing"

// GAP 2: a SQL string literal with an embedded NUL byte is rejected in the
// checker with an E-DB diagnostic (checkSQLArgs in builtincall.go), covering
// db.execute/db.query/db.queryOne and the tx.* forms. These are the SAME
// programs codegen's consts.go NUL check rejects, just rejected earlier.

const sqlNULMsg = "SQL string literal contains a NUL byte"

func TestSQLNULExecute(t *testing.T) {
	src := `fn main(): void { let n = db.execute("UPDATE t SET a = 1\0 WHERE b = 2"); }`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULAtEnd(t *testing.T) {
	src := `fn main(): void { let n = db.execute("delete from users\0"); }`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULQuery(t *testing.T) {
	src := `
interface Row { id: int64; }
fn main(): void {
	let rows = db.query<Row>("select id from users\0");
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULQueryOne(t *testing.T) {
	src := `
interface Row { id: int64; }
fn main(): void {
	let row = db.queryOne<Row>("select id\0 from users limit 1");
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULTxExecute(t *testing.T) {
	src := `fn main(): void { db.transaction((tx) => { tx.execute("UPDATE t SET a = 1\0"); }); }`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULTxQuery(t *testing.T) {
	src := `
interface Row { id: int64; }
fn main(): void {
	db.transaction((tx) => { let rows = tx.query<Row>("select id\0 from users"); });
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

func TestSQLNULTxQueryOne(t *testing.T) {
	src := `
interface Row { id: int64; }
fn main(): void {
	db.transaction((tx) => { let row = tx.queryOne<Row>("select id\0 limit 1"); });
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
	if !hasMessage(diags, sqlNULMsg) {
		t.Fatalf("want %q, got %s", sqlNULMsg, diags.Error())
	}
}

// TestSQLNULCleanLiteralOK confirms a legitimate SQL literal (no NUL) still
// passes: the strict check must not over-reject.
func TestSQLNULCleanLiteralOK(t *testing.T) {
	src := `fn main(): void { let n = db.execute("UPDATE t SET a = 1 WHERE b = 2"); }`
	_, diags := check(t, src)
	wantCodes(t, diags)
}
