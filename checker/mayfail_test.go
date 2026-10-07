package checker

import (
	"testing"

	"tlang/ast"
	"tlang/types"
)

// mayfail_test.go covers pass 5: per-expression Info.MayFail marking, the
// Func.MayFail call-graph fixed point (including mutual recursion and
// guards), Info.TxIDs numbering, the transaction-rule checks, and the
// Call.MayFail == Info.MayFail[call] invariant.

// firstExpr returns the first expression in prog whose String() equals want.
func firstExpr(prog *ast.Program, want string) ast.Expression {
	var out ast.Expression
	ast.Inspect(prog, func(n ast.Node) bool {
		if out != nil {
			return false
		}
		if e, ok := n.(ast.Expression); ok && e.String() == want {
			out = e
		}
		return true
	})
	return out
}

func TestMayFailNonNull(t *testing.T) {
	src := `fn f(x: int64 | null): int64 { return x!; }`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	e := firstExpr(prog, "x!")
	if e == nil || !info.MayFail[e] {
		t.Fatalf("x! should be may-fail")
	}
}

func TestMayFailIndex(t *testing.T) {
	src := `fn f(xs: int64[]): int64 { return xs[0]; }`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	e := firstExpr(prog, "xs[0]")
	if e == nil || !info.MayFail[e] {
		t.Fatalf("xs[0] should be may-fail")
	}
}

func TestMayFailIntegerDivNonConst(t *testing.T) {
	src := `fn f(a: int64, b: int64): int64 { return a / b; }`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	e := firstExpr(prog, "(a / b)")
	if e == nil || !info.MayFail[e] {
		t.Fatalf("a / b with a non-constant divisor should be may-fail")
	}
}

func TestMayFailIntegerDivConst(t *testing.T) {
	src := `fn f(a: int64): int64 { return a / 2; }`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	e := firstExpr(prog, "(a / 2)")
	if e == nil {
		t.Fatal("a / 2 not found")
	}
	if info.MayFail[e] {
		t.Fatalf("a / 2 with a nonzero constant divisor should not be may-fail")
	}
}

func TestMayFailFloatDivNever(t *testing.T) {
	src := `fn f(a: float64, b: float64): float64 { return a / b; }`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	e := firstExpr(prog, "(a / b)")
	if e == nil {
		t.Fatal("a / b not found")
	}
	if info.MayFail[e] {
		t.Fatalf("float / never fails")
	}
}

func TestMayFailThrowPropagates(t *testing.T) {
	// A function with a throw is may-fail; a caller of it is may-fail too.
	src := `
fn thrower(): void { throw "boom"; }
fn caller(): void { thrower(); }
`
	_, _, diags := checkProg(t, src)
	wantCodes(t, diags)
	var thrower, caller *types.Func
	_, info, _ := checkProg(t, src)
	for id, obj := range info.Defs {
		if f, ok := obj.(*types.Func); ok {
			switch id.Name {
			case "thrower":
				thrower = f
			case "caller":
				caller = f
			}
		}
	}
	if thrower == nil || !thrower.MayFail {
		t.Fatalf("thrower should be may-fail")
	}
	if caller == nil || !caller.MayFail {
		t.Fatalf("caller of a may-fail function should be may-fail")
	}
}

func TestMayFailTryCatchContains(t *testing.T) {
	// A throw inside a try (whose catch covers it) does not make the function
	// may-fail.
	src := `
fn safe(): void {
  try { throw "boom"; } catch (e) { }
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	for id, obj := range info.Defs {
		if f, ok := obj.(*types.Func); ok && id.Name == "safe" {
			if f.MayFail {
				t.Fatalf("a throw contained by a try/catch should not make safe may-fail")
			}
		}
	}
}

func TestMayFailMutualRecursion(t *testing.T) {
	// Two mutually recursive functions with no real failure reason stay
	// not-may-fail (the fixed point starts from false).
	src := `
fn ping(n: int64): int64 { if (n <= 0) { return 0; } return pong(n - 1); }
fn pong(n: int64): int64 { if (n <= 0) { return 0; } return ping(n - 1); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	for id, obj := range info.Defs {
		if f, ok := obj.(*types.Func); ok && (id.Name == "ping" || id.Name == "pong") {
			if f.MayFail {
				t.Fatalf("%s has no failure reason and should not be may-fail", id.Name)
			}
		}
	}
}

func TestMayFailGuard(t *testing.T) {
	// A function whose @Use guard may fail is itself may-fail.
	src := `
fn guard(ctx: Context): bool { return ctx.paramInt("n") > 0; }
@Use(guard)
fn handler(ctx: Context): void { }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	for id, obj := range info.Defs {
		if f, ok := obj.(*types.Func); ok && id.Name == "handler" {
			if !f.MayFail {
				t.Fatalf("a handler with a may-fail guard should be may-fail")
			}
		}
	}
}

func TestTxIDsNumbered(t *testing.T) {
	src := `
fn a(): void { db.transaction((tx) => { tx.execute("UPDATE t SET x = 1"); }); }
fn b(): void { db.transaction((tx) => { tx.execute("UPDATE t SET y = 2"); }); }
`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	ids := map[int]bool{}
	ast.Inspect(prog, func(n ast.Node) bool {
		if tx, ok := n.(*ast.TransactionStatement); ok {
			ids[info.TxIDs[tx]] = true
		}
		return true
	})
	if !ids[1] || !ids[2] {
		t.Fatalf("transactions should be numbered 1 and 2, got %v", ids)
	}
}

func TestCallMayFailAgreesWithInfo(t *testing.T) {
	src := `
fn thrower(): void { throw "boom"; }
fn caller(): void { thrower(); }
`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	ast.Inspect(prog, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpression); ok {
			if call := info.Calls[ce]; call != nil {
				if call.MayFail != info.MayFail[ce] {
					t.Fatalf("Call.MayFail (%v) != Info.MayFail[call] (%v) for %s", call.MayFail, info.MayFail[ce], ce.String())
				}
			}
		}
		return true
	})
}

func TestTxReturnRejected(t *testing.T) {
	src := `
fn a(): void { db.transaction((tx) => { return; }); }
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-TX") {
		t.Fatalf("want E-TX for a return leaving a transaction block, got %s", diags.Error())
	}
}

func TestTxNestedRejected(t *testing.T) {
	src := `
fn a(): void {
  db.transaction((tx) => {
    db.transaction((tx2) => { tx2.execute("UPDATE t SET x = 1"); });
  });
}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-TX") {
		t.Fatalf("want E-TX for a nested transaction, got %s", diags.Error())
	}
}
