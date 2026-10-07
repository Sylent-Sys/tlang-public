package checker

import (
	"testing"

	"tlang/ast"
	"tlang/diag"
	"tlang/parser"
	"tlang/types"
)

// wrap puts a snippet of statements inside a main function body so the body
// pass checks it.
func wrap(body string) string {
	return "fn main(): void {\n" + body + "\n}\n"
}

// findExpr returns the first expression in info for which pred is true,
// walking prog in source order.
func exprByString(prog *ast.Program, info *types.Info, want string) (ast.Expression, types.TypeAndValue, bool) {
	var found ast.Expression
	var tv types.TypeAndValue
	ok := false
	ast.Inspect(prog, func(n ast.Node) bool {
		if ok {
			return false
		}
		if e, isExpr := n.(ast.Expression); isExpr && e.String() == want {
			if t, has := info.Types[e]; has {
				found, tv, ok = e, t, true
				return false
			}
		}
		return true
	})
	return found, tv, ok
}

func TestLiteralDefaulting(t *testing.T) {
	src := wrap("let n = 1; let f = 1.5; let s = \"x\"; let b = true;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	for name, kind := range map[string]types.BasicKind{"n": types.Int64, "f": types.Float64, "s": types.String, "b": types.Bool} {
		v := localByName(t, info, name)
		if !types.IsBasic(v.Type, kind) {
			t.Fatalf("%s: got %s, want %s", name, v.Type, types.Typ[kind])
		}
	}
}

func TestLiteralWithWant(t *testing.T) {
	src := wrap("let x: int32 = 5;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsBasic(v.Type, types.Int32) {
		t.Fatalf("x = %s, want int32", v.Type)
	}
}

func TestContextFreeNull(t *testing.T) {
	src := wrap("let x = null;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
	if !hasMessage(diags, "cannot infer a type from null") {
		t.Fatalf("want a context-free null message, got %s", diags.Error())
	}
}

func TestNullWithWantConverts(t *testing.T) {
	src := wrap("let x: int64 | null = null;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsOptional(v.Type) {
		t.Fatalf("x = %s, want int64 | null", v.Type)
	}
}

func TestConvToOptionalRecorded(t *testing.T) {
	src := wrap("let x: int64 | null = 5;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	// The 5 is recorded int64, and a ConvToOptional is recorded for it.
	found := false
	for e, conv := range info.Conversions {
		if conv.Kind == types.ConvToOptional {
			if tv, ok := info.Types[e]; ok && types.IsBasic(tv.Type, types.Int64) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected a ConvToOptional on the int64 initializer")
	}
}

func TestIntOverflow(t *testing.T) {
	// 2^64 overflows uint64.
	src := wrap("let x = 18446744073709551616;")
	_, diags := check(t, src)
	if !hasCode(diags, "E-CONST") {
		t.Fatalf("want E-CONST for overflow, got %s", diags.Error())
	}
}

func TestArithStringConcat(t *testing.T) {
	src := wrap("let s = \"a\" + \"b\";")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "s")
	if !types.IsString(v.Type) {
		t.Fatalf("s = %s, want string", v.Type)
	}
}

func TestArithNumericMismatch(t *testing.T) {
	// int32 with int64 is E-TYPE (no implicit widening).
	src := wrap("let a: int32 = 1; let b: int64 = 2; let c = a + b;")
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for int32 + int64, got %s", diags.Error())
	}
}

func TestPlusOnBoolRejected(t *testing.T) {
	src := wrap("let x = true + false;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestIntegerDivByZero(t *testing.T) {
	src := wrap("let x = 1 / 0;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-CONST")
	if !hasMessage(diags, "division by zero") {
		t.Fatalf("want division-by-zero, got %s", diags.Error())
	}
}

func TestFloatDivByZeroAllowed(t *testing.T) {
	// Float / by a zero constant is not an error (runtime ±Inf).
	src := wrap("let x = 1.0 / 0.0;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsBasic(v.Type, types.Float64) {
		t.Fatalf("x = %s, want float64", v.Type)
	}
}

func TestIntegerModByZero(t *testing.T) {
	src := wrap("let x = 5 % 0;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-CONST")
}

func TestFloatModTypedNotFolded(t *testing.T) {
	src := wrap("let x = 5.0 % 2.0;")
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	_, tv, ok := exprByString(prog, info, "(5.0 % 2.0)")
	if !ok {
		t.Fatal("float % expression not recorded")
	}
	if !types.IsBasic(tv.Type, types.Float64) {
		t.Fatalf("float %% = %s, want float64", tv.Type)
	}
	if tv.Value != nil {
		t.Fatalf("float %% must not be folded, got value %v", tv.Value)
	}
}

func TestComparisonYieldsBool(t *testing.T) {
	src := wrap("let x = 1 < 2; let y = 1 == 1;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	for _, name := range []string{"x", "y"} {
		v := localByName(t, info, name)
		if !types.IsBool(v.Type) {
			t.Fatalf("%s = %s, want bool", name, v.Type)
		}
	}
}

func TestLogicalShortCircuit(t *testing.T) {
	src := wrap("let x = true && false; let y = true || false;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	for _, name := range []string{"x", "y"} {
		if !types.IsBool(localByName(t, info, name).Type) {
			t.Fatalf("%s not bool", name)
		}
	}
}

func TestNullishResultType(t *testing.T) {
	src := wrap("let o: int64 | null = 5; let x = o ?? 0;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("x = %s, want int64", v.Type)
	}
}

func TestNullishRequiresOptional(t *testing.T) {
	src := wrap("let x = 5 ?? 0;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestTernaryCommonType(t *testing.T) {
	src := wrap("let o: int64 | null = 5; let b = true; let x = b ? o : 7;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsOptional(v.Type) {
		t.Fatalf("x = %s, want int64 | null (common type)", v.Type)
	}
}

func TestNonNull(t *testing.T) {
	src := wrap("let o: int64 | null = 5; let x = o!;")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("x = %s, want int64", v.Type)
	}
}

func TestNonNullRequiresOptional(t *testing.T) {
	src := wrap("let n = 5; let x = n!;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestIndexExpression(t *testing.T) {
	src := wrap("let xs: int64[] = [1, 2, 3]; let x = xs[0];")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "x")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("x = %s, want int64", v.Type)
	}
}

func TestArrayLiteralInference(t *testing.T) {
	src := wrap("let xs = [1, 2, 3];")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "xs")
	arr, ok := v.Type.(*types.Array)
	if !ok || !types.IsBasic(arr.Elem, types.Int64) {
		t.Fatalf("xs = %s, want int64[]", v.Type)
	}
}

func TestEmptyArrayNoWant(t *testing.T) {
	src := wrap("let xs = [];")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
	if !hasMessage(diags, "empty array") {
		t.Fatalf("want an empty-array message, got %s", diags.Error())
	}
}

func TestObjectLiteral(t *testing.T) {
	src := `
interface User { id: int64; name: string; }
fn main(): void {
	let u: User = { id: 1, name: "a" };
}
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	// The keys are recorded in Info.Uses as the field Vars.
	count := 0
	for _, obj := range info.Uses {
		if v, ok := obj.(*types.Var); ok && v.Kind == types.FieldVar {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("expected object-literal keys recorded in Uses, got %d", count)
	}
}

func TestObjectLiteralMissingField(t *testing.T) {
	src := `
interface User { id: int64; name: string; }
fn main(): void {
	let u: User = { id: 1 };
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-INIT")
	if !hasMessage(diags, "missing field") {
		t.Fatalf("want a missing-field message, got %s", diags.Error())
	}
}

func TestObjectLiteralUnknownField(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let u: User = { id: 1, nope: 2 };
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-INIT")
	if !hasMessage(diags, "unknown field") {
		t.Fatalf("want an unknown-field message, got %s", diags.Error())
	}
}

func TestObjectLiteralNeedsInterface(t *testing.T) {
	src := wrap("let x = { a: 1 };")
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for object literal without an interface want, got %s", diags.Error())
	}
}

func TestNewInterface(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let u = new User();
}
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "u")
	if _, ok := v.Type.(*types.Named); !ok {
		t.Fatalf("u = %s, want User", v.Type)
	}
}

func TestNewInterfaceTakesNoArgs(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let u = new User(1);
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestNewArrayZeroArgs(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let xs = new User[]();
}
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "xs")
	if _, ok := v.Type.(*types.Array); !ok {
		t.Fatalf("xs = %s, want User[]", v.Type)
	}
}

func TestNewArrayWithArgsRejected(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let xs = new User[](1);
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestNewErrorArrayAllowed(t *testing.T) {
	src := wrap("let xs = new Error[]();")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "xs")
	arr, ok := v.Type.(*types.Array)
	if !ok || !types.IsBasic(arr.Elem, types.Error) {
		t.Fatalf("xs = %s, want Error[]", v.Type)
	}
}

func TestNewError(t *testing.T) {
	src := wrap("let e = new Error(\"boom\", 404);")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "e")
	if !types.IsBasic(v.Type, types.Error) {
		t.Fatalf("e = %s, want Error", v.Type)
	}
}

func TestNewErrorTooManyArgs(t *testing.T) {
	src := wrap("let e = new Error(\"boom\", 404, 1);")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestCompoundOnFloat(t *testing.T) {
	src := wrap("let x: float64 = 1.0; x++; x += 2.0;")
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestCompoundPlusOnString(t *testing.T) {
	src := wrap("let s = \"a\"; s += \"b\";")
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestCompoundMinusOnStringRejected(t *testing.T) {
	src := wrap("let s = \"a\"; s -= \"b\";")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestAssignToConstRejected(t *testing.T) {
	src := wrap("const x = 1; x = 2;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
	if !hasMessage(diags, "const") {
		t.Fatalf("want a const message, got %s", diags.Error())
	}
}

func TestBareExpressionStatementRejected(t *testing.T) {
	src := wrap("1 + 2;")
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for a bare expression statement, got %s", diags.Error())
	}
}

func TestLetNoZeroValue(t *testing.T) {
	src := `
interface User { id: int64; }
fn main(): void {
	let u: User;
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-INIT")
}

func TestReturnValueChecked(t *testing.T) {
	src := `fn f(): int64 { return "x"; }`
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for a wrong return type, got %s", diags.Error())
	}
}

func TestReturnNullOptional(t *testing.T) {
	src := `fn f(): int64 | null { return null; }`
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestForOfElementType(t *testing.T) {
	src := wrap("let xs: int64[] = [1, 2]; for (const x of xs) { let y = x + 1; }")
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestForOfNonArray(t *testing.T) {
	src := wrap("let n = 5; for (const x of n) { }")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestBreakOutsideLoop(t *testing.T) {
	src := wrap("break;")
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestTryCatchBinding(t *testing.T) {
	src := `fn main(): void { try { throw "x"; } catch (e) { let m = e.message; } }`
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestTransactionHandle(t *testing.T) {
	src := `fn main(): void { db.transaction((tx) => { let n = tx.execute("x"); }); }`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if !info.UsesDB {
		t.Fatal("a transaction must set UsesDB")
	}
}

func TestGenericInterfaceNew(t *testing.T) {
	src := `
interface Box<T> { v: T; }
fn main(): void {
	let b = new Box<int64>();
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if len(info.Instances.NamedInstances()) == 0 {
		t.Fatal("expected a Box<int64> instance seeded")
	}
}

func TestGenericArrayInference(t *testing.T) {
	src := `
fn first<T>(xs: T[]): T { return xs[0]; }
fn main(): void {
	let v = first([1, 2, 3]);
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "v")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("v = %s, want int64", v.Type)
	}
}

func TestConversion(t *testing.T) {
	src := wrap("let a: int32 = 1; let b = int64(a);")
	info, diags := check(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "b")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("b = %s, want int64", v.Type)
	}
}

// localByName returns the local *Var declared with the given name.
func localByName(t *testing.T, info *types.Info, name string) *types.Var {
	t.Helper()
	for id, obj := range info.Defs {
		if id.Name != name {
			continue
		}
		if v, ok := obj.(*types.Var); ok {
			return v
		}
	}
	t.Fatalf("local %s not found in Info.Defs", name)
	return nil
}

// checkProg parses src and runs Check, returning the program (for
// expression lookups), the Info and the diagnostics.
func checkProg(t *testing.T, src string) (*ast.Program, *types.Info, *diag.List) {
	t.Helper()
	prog, pdiags := parser.ParseSource("test.tl", []byte(src))
	if pdiags.HasErrors() {
		t.Fatalf("parse %q: unexpected parser errors: %s", src, pdiags.Error())
	}
	info, diags := Check(prog)
	return prog, info, diags
}
