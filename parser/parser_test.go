package parser

import (
	"strings"
	"testing"

	"tlang/ast"
	"tlang/diag"
	"tlang/lexer"
)

// parseSrc lexes and parses src, returning the program and ONLY the parser's
// E-PARSE diagnostics (the lexer's E-LEX list is separate, as in production).
func parseSrc(t *testing.T, src string) (*ast.Program, *diag.List) {
	t.Helper()
	toks, _ := lexer.Lex("test.tl", []byte(src))
	return Parse("test.tl", toks)
}

// mustExpr parses src as the value of a global "let v = <src>;" and returns the
// initializer's String(). It fails on any diagnostic so precedence tests stay
// honest.
func mustExpr(t *testing.T, src string) string {
	t.Helper()
	prog, dg := parseSrc(t, "let v = "+src+";")
	if dg.Len() != 0 {
		t.Fatalf("parse %q: unexpected diagnostics: %s", src, dg.Error())
	}
	if len(prog.Statements) != 1 {
		t.Fatalf("parse %q: want 1 statement, got %d", src, len(prog.Statements))
	}
	let, ok := prog.Statements[0].(*ast.LetStatement)
	if !ok {
		t.Fatalf("parse %q: want *ast.LetStatement, got %T", src, prog.Statements[0])
	}
	if let.Value == nil {
		t.Fatalf("parse %q: nil initializer", src)
	}
	return let.Value.String()
}

func TestLiterals(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"42", "42"},
		{"0xFF", "0xFF"},
		{"1_000", "1_000"},
		{"1.5", "1.5"},
		{"2.5e-3", "2.5e-3"},
		{`"hi"`, `"hi"`},
		{"true", "true"},
		{"false", "false"},
		{"null", "null"},
		{"foo", "foo"},
	}
	for _, c := range cases {
		if got := mustExpr(t, c.src); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestIntegerValue(t *testing.T) {
	prog, dg := parseSrc(t, "let v = 0xFF_FF;")
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	lit := prog.Statements[0].(*ast.LetStatement).Value.(*ast.IntegerLiteral)
	if lit.Value != 0xFFFF {
		t.Errorf("value = %d, want %d", lit.Value, 0xFFFF)
	}
	if lit.Overflow {
		t.Errorf("unexpected overflow")
	}
}

func TestIntegerOverflow(t *testing.T) {
	prog, _ := parseSrc(t, "let v = 99999999999999999999999999;")
	lit := prog.Statements[0].(*ast.LetStatement).Value.(*ast.IntegerLiteral)
	if !lit.Overflow {
		t.Errorf("want overflow flag set")
	}
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"-a * b", "((-a) * b)"},
		{"a + b * c", "(a + (b * c))"},
		{"a + b + c", "((a + b) + c)"},
		{"a < b == c", "((a < b) == c)"},
		{"a ?? b || c", "(a ?? (b || c))"},
		{"a && b || c", "((a && b) || c)"},
		{"a == b && c != d", "((a == b) && (c != d))"},
		{"a % b * c", "((a % b) * c)"},
		{"!a == b", "((!a) == b)"},
		{"a - b - c", "((a - b) - c)"},
	}
	for _, c := range cases {
		if got := mustExpr(t, c.src); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestTernaryRightAssoc(t *testing.T) {
	if got := mustExpr(t, "a ? b : c ? d : e"); got != "(a ? b : (c ? d : e))" {
		t.Errorf("got %q", got)
	}
	if got := mustExpr(t, "a ? b : c"); got != "(a ? b : c)" {
		t.Errorf("got %q", got)
	}
}

func TestMemberCallIndexChains(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"a.b.c", "a.b.c"},
		{"f(x, y)", "f(x, y)"},
		{"xs[0]", "xs[0]"},
		{"x!", "x!"},
		{"a.b().c[0]!", "a.b().c[0]!"},
		{"f()", "f()"},
	}
	for _, c := range cases {
		if got := mustExpr(t, c.src); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestGroupingNoNode(t *testing.T) {
	if got := mustExpr(t, "(a + b) * c"); got != "((a + b) * c)" {
		t.Errorf("got %q", got)
	}
	// A grouped expression must not introduce a node: ((x)) is just x.
	prog, _ := parseSrc(t, "let v = ((x));")
	if _, ok := prog.Statements[0].(*ast.LetStatement).Value.(*ast.Identifier); !ok {
		t.Errorf("grouping produced a wrapper node")
	}
}

func TestGenericCallCommitted(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"first<User>(xs)", "first<User>(xs)"},
		{"db.query<User>(sql)", "db.query<User>(sql)"},
		{"f<A, B>(x)", "f<A, B>(x)"},
	}
	for _, c := range cases {
		got := mustExpr(t, c.src)
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
		prog, _ := parseSrc(t, "let v = "+c.src+";")
		call, ok := prog.Statements[0].(*ast.LetStatement).Value.(*ast.CallExpression)
		if !ok {
			t.Errorf("%q: want *ast.CallExpression, got %T", c.src, prog.Statements[0].(*ast.LetStatement).Value)
			continue
		}
		if len(call.TypeArgs) == 0 {
			t.Errorf("%q: expected type args", c.src)
		}
	}
}

func TestGenericCallBacktrack(t *testing.T) {
	// "a < b > c" is NOT a generic call (no "(" after ">"): comparisons.
	got := mustExpr(t, "a < b > c")
	if got != "((a < b) > c)" {
		t.Errorf("a < b > c: got %q, want %q", got, "((a < b) > c)")
	}
	// "x < y" is plain less-than.
	if got := mustExpr(t, "x < y"); got != "(x < y)" {
		t.Errorf("x < y: got %q", got)
	}
	// "a < b" with a following addition still backtracks (no generic call).
	if got := mustExpr(t, "a < b + c"); got != "(a < (b + c))" {
		t.Errorf("a < b + c: got %q", got)
	}
	// The backtrack must leave no spurious diagnostic.
	_, dg := parseSrc(t, "let v = a < b > c;")
	if dg.Len() != 0 {
		t.Errorf("backtrack left diagnostics: %s", dg.Error())
	}
}

func TestGreaterEqSplitInType(t *testing.T) {
	// "Page<User>= x" lexes as IDENT LT IDENT GT_EQ IDENT. In a type
	// annotation the type-arg list must close on the ">" of ">=" and the
	// leftover "=" starts the initializer (D-SPLIT).
	prog, dg := parseSrc(t, "let p: Page<User>= x;")
	if dg.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", dg.Error())
	}
	let := prog.Statements[0].(*ast.LetStatement)
	named, ok := let.Type.(*ast.NamedType)
	if !ok {
		t.Fatalf("type = %T (%s), want *ast.NamedType", let.Type, let.Type.String())
	}
	if named.String() != "Page<User>" {
		t.Errorf("type = %q, want %q", named.String(), "Page<User>")
	}
	if let.Value == nil || let.Value.String() != "x" {
		t.Errorf("initializer = %v, want x", let.Value)
	}
}

func TestGreaterEqSplitInGenericCall(t *testing.T) {
	// A generic call whose closing ">" is glued to "=" by the lexer:
	// "f<User>= x" would require the ">(" shape to be a generic call, which
	// it is not here, so it stays a comparison. The split only fires when a
	// type-arg list is genuinely closing (confirmed by the type-annotation
	// test above and by a nested generic type below).
	prog, dg := parseSrc(t, "let m: Map<string>= y;")
	if dg.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", dg.Error())
	}
	let := prog.Statements[0].(*ast.LetStatement)
	if let.Type == nil || let.Type.String() != "Map<string>" {
		t.Errorf("type = %v, want Map<string>", let.Type)
	}
}

func TestGenericSpeculationNoTokenCorruption(t *testing.T) {
	// "a<b>=c" starts a speculative generic-call trial that sees ">=" and
	// fails (no "(" follows), then rewinds. The ">=" token must NOT have been
	// mutated by the trial, so the whole thing parses as "(a < b) >= c".
	got := mustExpr(t, "a<b>=c")
	if got != "((a < b) >= c)" {
		t.Errorf("a<b>=c: got %q, want %q", got, "((a < b) >= c)")
	}
}

func TestNestedGenerics(t *testing.T) {
	// Two GT tokens close f<Page<User>>(x) correctly.
	if got := mustExpr(t, "f<Page<User>>(x)"); got != "f<Page<User>>(x)" {
		t.Errorf("got %q", got)
	}
}

func TestTypes(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{"int64", "int64"},
		{"string[]", "string[]"},
		{"T | null", "T | null"},
		{"string[] | null", "string[] | null"},
		{"(T | null)[]", "(T | null)[]"},
		{"Page<User>", "Page<User>"},
		{"int64[][]", "int64[][]"},
	}
	for _, c := range cases {
		src := "let v: " + c.typ + " = x;"
		prog, dg := parseSrc(t, src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.typ, dg.Error())
			continue
		}
		typ := prog.Statements[0].(*ast.LetStatement).Type
		if typ == nil {
			t.Errorf("%q: nil type", c.typ)
			continue
		}
		if got := typ.String(); got != c.want {
			t.Errorf("%q: got %q, want %q", c.typ, got, c.want)
		}
	}
}

func TestDeclarations(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"fn main(): void { return; }", "fn main(): void { return; }"},
		{"fn (u: User) greet(): string { return u.name; }",
			"fn (u: User) greet(): string { return u.name; }"},
		{"fn first<T>(xs: T[]): T | null { return null; }",
			"fn first<T>(xs: T[]): T | null { return null; }"},
		{"interface Page<T> { items: T[]; total: int64 }",
			"interface Page<T> { items: T[]; total: int64 }"},
		{"interface P { a: int64, b?: string, }",
			"interface P { a: int64; b?: string }"},
		{"type P = { id: int64; name: string };",
			"type P = { id: int64; name: string };"},
		{"type Id = int64;", "type Id = int64;"},
		{"let g: int64 = 1;", "let g: int64 = 1;"},
		{"const k = 2;", "const k = 2;"},
	}
	for _, c := range cases {
		prog, dg := parseSrc(t, c.src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		got := strings.TrimRight(prog.String(), "\n")
		if got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.src, got, c.want)
		}
	}
}

func TestDecoratorPos(t *testing.T) {
	src := "@Use(g1, g2) fn h(): void {}"
	prog, dg := parseSrc(t, src)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	fn := prog.Statements[0].(*ast.FunctionStatement)
	if len(fn.Decorators) != 1 {
		t.Fatalf("want 1 decorator, got %d", len(fn.Decorators))
	}
	if len(fn.Decorators[0].Args) != 2 {
		t.Fatalf("want 2 decorator args, got %d", len(fn.Decorators[0].Args))
	}
	// Pos() must start at the first decorator "@" (column 1).
	if fn.Pos() != fn.Decorators[0].Pos() {
		t.Errorf("Pos() = %v, want decorator pos %v", fn.Pos(), fn.Decorators[0].Pos())
	}
	if fn.Pos().Column != 1 {
		t.Errorf("Pos().Column = %d, want 1", fn.Pos().Column)
	}
}

func TestAfterDecoratorPos(t *testing.T) {
	// @After needs no new grammar: it parses into the generic decorator list
	// exactly like @Use, distinguished only by the name.
	src := "@After(a, b) fn h(ctx: Context): void {}"
	prog, dg := parseSrc(t, src)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	fn := prog.Statements[0].(*ast.FunctionStatement)
	if len(fn.Decorators) != 1 {
		t.Fatalf("want 1 decorator, got %d", len(fn.Decorators))
	}
	if fn.Decorators[0].Name.Name != "After" {
		t.Fatalf("decorator name = %q, want After", fn.Decorators[0].Name.Name)
	}
	if len(fn.Decorators[0].Args) != 2 {
		t.Fatalf("want 2 decorator args, got %d", len(fn.Decorators[0].Args))
	}
	if fn.Pos() != fn.Decorators[0].Pos() || fn.Pos().Column != 1 {
		t.Errorf("Pos() = %v, want first decorator at column 1", fn.Pos())
	}
}

func TestUseAndAfterDecorators(t *testing.T) {
	// @Use and @After, split and grouped, parse into one source-order
	// decorator list.
	src := "@Use(x) @After(a) @After(b) fn h(ctx: Context): void {}"
	prog, dg := parseSrc(t, src)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	fn := prog.Statements[0].(*ast.FunctionStatement)
	if len(fn.Decorators) != 3 {
		t.Fatalf("want 3 decorators, got %d", len(fn.Decorators))
	}
	wantNames := []string{"Use", "After", "After"}
	for i, want := range wantNames {
		if got := fn.Decorators[i].Name.Name; got != want {
			t.Errorf("decorator %d name = %q, want %q", i, got, want)
		}
	}
}

func TestStatements(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"fn m(): void { { return; } }", "fn m(): void { { return; } }"},
		{"fn m(): void { if (a) { return; } else { return; } }",
			"fn m(): void { if (a) { return; } else { return; } }"},
		{"fn m(): void { if (a) {} else if (b) {} }",
			"fn m(): void { if (a) {} else if (b) {} }"},
		{"fn m(): void { while (a) {} }", "fn m(): void { while (a) {} }"},
		{"fn m(): void { for (let i = 0; i < n; i++) {} }",
			"fn m(): void { for (let i = 0; (i < n); i++) {} }"},
		{"fn m(): void { for (;;) {} }", "fn m(): void { for (;;) {} }"},
		{"fn m(): void { for (const x of xs) {} }",
			"fn m(): void { for (const x of xs) {} }"},
		{"fn m(): void { return 1; }", "fn m(): void { return 1; }"},
		{"fn m(): void { return; }", "fn m(): void { return; }"},
		{"fn m(): void { break; }", "fn m(): void { break; }"},
		{"fn m(): void { continue; }", "fn m(): void { continue; }"},
		{"fn m(): void { throw e; }", "fn m(): void { throw e; }"},
		{"fn m(): void { try { a(); } catch (e) { b(); } }",
			"fn m(): void { try { a(); } catch (e) { b(); } }"},
		{"fn m(): void { try {} catch {} }", "fn m(): void { try {} catch {} }"},
	}
	for _, c := range cases {
		prog, dg := parseSrc(t, c.src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		got := strings.TrimRight(prog.String(), "\n")
		if got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.src, got, c.want)
		}
	}
}

func TestAssignmentForms(t *testing.T) {
	good := []struct {
		src  string
		want string
	}{
		{"x = 1;", "x = 1;"},
		{"x += 1;", "x += 1;"},
		{"x -= 1;", "x -= 1;"},
		{"x++;", "x++;"},
		{"x--;", "x--;"},
		{"a.b = 1;", "a.b = 1;"},
		{"xs[0] = 1;", "xs[0] = 1;"},
	}
	for _, c := range good {
		src := "fn m(): void { " + c.src + " }"
		prog, dg := parseSrc(t, src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		fn := prog.Statements[0].(*ast.FunctionStatement)
		got := fn.Body.Statements[0].String()
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}

	// Postfix ++ keeps a nil Value.
	prog, _ := parseSrc(t, "fn m(): void { x++; }")
	fn := prog.Statements[0].(*ast.FunctionStatement)
	es := fn.Body.Statements[0].(*ast.ExpressionStatement)
	asn := es.Expression.(*ast.AssignmentExpression)
	if asn.Value != nil {
		t.Errorf("postfix ++ Value = %v, want nil", asn.Value)
	}
	if !asn.IsIncDec() {
		t.Errorf("IsIncDec() = false")
	}
}

func TestAssignmentErrors(t *testing.T) {
	bad := []string{
		"fn m(): void { a = b = c; }", // chained assignment
		"fn m(): void { ++x; }",       // prefix increment
		"fn m(): void { f(x = 1); }",  // assignment inside a call
	}
	for _, src := range bad {
		_, dg := parseSrc(t, src)
		if !dg.HasErrors() {
			t.Errorf("%q: expected an E-PARSE diagnostic", src)
		}
	}
}

func TestTransaction(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"fn m(): void { db.transaction((tx) => { a(); }); }",
			"fn m(): void { db.transaction((tx) => { a(); }); }"},
		{"fn m(): void { db.transaction((tx: Transaction) => { a(); }); }",
			"fn m(): void { db.transaction((tx: Transaction) => { a(); }); }"},
	}
	for _, c := range cases {
		prog, dg := parseSrc(t, c.src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		fn := prog.Statements[0].(*ast.FunctionStatement)
		if _, ok := fn.Body.Statements[0].(*ast.TransactionStatement); !ok {
			t.Errorf("%q: want TransactionStatement, got %T", c.src, fn.Body.Statements[0])
		}
		got := strings.TrimRight(prog.String(), "\n")
		if got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.src, got, c.want)
		}
	}

	// db.transaction(x) with a non-arrow argument is an ordinary call.
	prog, dg := parseSrc(t, "fn m(): void { db.transaction(x); }")
	if dg.Len() != 0 {
		t.Fatalf("ordinary call: diagnostics: %s", dg.Error())
	}
	fn := prog.Statements[0].(*ast.FunctionStatement)
	es := fn.Body.Statements[0].(*ast.ExpressionStatement)
	if _, ok := es.Expression.(*ast.CallExpression); !ok {
		t.Errorf("db.transaction(x): want CallExpression, got %T", es.Expression)
	}

	// "=>" elsewhere is an error.
	_, dg2 := parseSrc(t, "fn m(): void { let f = (x) => x; }")
	if !dg2.HasErrors() {
		t.Errorf("arrow outside transaction: expected E-PARSE")
	}
}

func TestNewForms(t *testing.T) {
	cases := []struct {
		src     string
		want    string
		isArray bool
	}{
		{"new T()", "new T()", false},
		{"new T<A>()", "new T<A>()", false},
		{"new T[]()", "new T[]()", true},
		{"new T[][]()", "new T[][]()", true},
		{"new global T()", "new global T()", false},
		{"new global T[]()", "new global T[]()", true},
		{"new Error(m)", "new Error(m)", false},
		{"new Error(m, 404)", "new Error(m, 404)", false},
	}
	for _, c := range cases {
		got := mustExpr(t, c.src)
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
		prog, _ := parseSrc(t, "let v = "+c.src+";")
		ne := prog.Statements[0].(*ast.LetStatement).Value.(*ast.NewExpression)
		if ne.IsArray != c.isArray {
			t.Errorf("%q: IsArray = %v, want %v", c.src, ne.IsArray, c.isArray)
		}
	}

	// new T[][]() has Type == ArrayType and IsArray set.
	prog, _ := parseSrc(t, "let v = new T[][]();")
	ne := prog.Statements[0].(*ast.LetStatement).Value.(*ast.NewExpression)
	if _, ok := ne.Type.(*ast.ArrayType); !ok {
		t.Errorf("new T[][](): Type = %T, want *ast.ArrayType", ne.Type)
	}
}

func TestObjectLiteral(t *testing.T) {
	if got := mustExpr(t, "{ a: 1, b: x }"); got != "{ a: 1, b: x }" {
		t.Errorf("got %q", got)
	}
	if got := mustExpr(t, "{ a: 1, }"); got != "{ a: 1 }" {
		t.Errorf("trailing comma: got %q", got)
	}
	// Quoted key is an error.
	_, dg := parseSrc(t, `let v = { "k": 1 };`)
	if !dg.HasErrors() {
		t.Errorf("quoted key: expected E-PARSE")
	}
	// Shorthand entry is an error (no colon).
	_, dg2 := parseSrc(t, "let v = { id };")
	if !dg2.HasErrors() {
		t.Errorf("shorthand: expected E-PARSE")
	}
}

func TestArrayLiteral(t *testing.T) {
	if got := mustExpr(t, "[1, 2, 3]"); got != "[1, 2, 3]" {
		t.Errorf("got %q", got)
	}
	if got := mustExpr(t, "[]"); got != "[]" {
		t.Errorf("empty: got %q", got)
	}
	if got := mustExpr(t, "[1, 2,]"); got != "[1, 2]" {
		t.Errorf("trailing comma: got %q", got)
	}
}

func TestTopLevelDiscipline(t *testing.T) {
	// A non-declaration at top level becomes a BadStatement + E-PARSE.
	prog, dg := parseSrc(t, "if (x) {}")
	if !dg.HasErrors() {
		t.Errorf("top-level if: expected E-PARSE")
	}
	if len(prog.Statements) != 1 {
		t.Fatalf("want 1 statement, got %d", len(prog.Statements))
	}
	if _, ok := prog.Statements[0].(*ast.BadStatement); !ok {
		t.Errorf("want BadStatement, got %T", prog.Statements[0])
	}

	// A stray top-level ";" is accepted silently.
	prog2, dg2 := parseSrc(t, "type Id = int64;; type J = int64;")
	if dg2.Len() != 0 {
		t.Errorf("stray ';': unexpected diagnostics: %s", dg2.Error())
	}
	if len(prog2.Statements) != 2 {
		t.Errorf("stray ';': want 2 statements, got %d", len(prog2.Statements))
	}
}

func TestAmpersandReserved(t *testing.T) {
	_, dg := parseSrc(t, "let v = a & b;")
	if !dg.HasErrors() {
		t.Fatalf("expected E-PARSE for '&'")
	}
	found := false
	for _, d := range dg.Items {
		if d.Code == diag.CodeParse && strings.Contains(d.Message, "reserved") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'reserved' message, got: %s", dg.Error())
	}
}

func TestMultiErrorRecovery(t *testing.T) {
	// Several independent broken declarations plus good ones around them.
	src := "type A = int64; fn () {} type B = ; let g = 1;"
	prog, dg := parseSrc(t, src)
	if dg.ErrorCount() < 2 {
		t.Errorf("want multiple errors, got %d: %s", dg.ErrorCount(), dg.Error())
	}
	// Errors should be at distinct positions.
	positions := map[string]bool{}
	for _, d := range dg.Items {
		positions[d.Pos.String()] = true
	}
	if len(positions) < 2 {
		t.Errorf("want errors at distinct positions, got %v", positions)
	}
	// The good declarations survive.
	if len(prog.Statements) == 0 {
		t.Errorf("program lost all statements")
	}
}

func TestIllegalNoDoubleReport(t *testing.T) {
	// "===" and "!==" each lex to a single ILLEGAL with an E-LEX diagnostic.
	// The parser must not add an E-PARSE for them.
	src := "fn m(): void { let a = 1 === 2; }"
	lexToks, lexDiags := lexer.Lex("test.tl", []byte(src))
	_, parseDiags := Parse("test.tl", lexToks)
	// Every lexer diagnostic is E-LEX.
	for _, d := range lexDiags.Items {
		if d.Code != diag.CodeLex {
			t.Errorf("lexer produced non-E-LEX: %s", d.Error())
		}
	}
	// The parser must NOT have emitted an E-PARSE at the ILLEGAL token's
	// position (no duplicate of the lexer's report).
	for _, pd := range parseDiags.Items {
		for _, ld := range lexDiags.Items {
			if pd.Pos == ld.Pos {
				t.Errorf("parser double-reported at %s: %s", pd.Pos, pd.Message)
			}
		}
	}
}

func TestProgramGolden(t *testing.T) {
	src := `interface User { id: int64; name: string }
fn (u: User) greet(): string { return u.name; }
@Use(auth) fn handler<T>(x: T): void { let n = x; }
type Id = int64;
const limit = 100;`
	prog, dg := parseSrc(t, src)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	want := strings.Join([]string{
		"interface User { id: int64; name: string }",
		"fn (u: User) greet(): string { return u.name; }",
		"@Use(auth) fn handler<T>(x: T): void { let n = x; }",
		"type Id = int64;",
		"const limit = 100;",
		"",
	}, "\n")
	if got := prog.String(); got != want {
		t.Errorf("golden mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestNeverPanic runs a batch of pathological inputs through Parse and asserts
// each returns a non-nil Program ending with a valid EOF and never panics.
func TestNeverPanic(t *testing.T) {
	inputs := []string{
		"",
		"   \n\t  ",
		"// just a comment\n",
		"/* unterminated",
		"fn foo(",
		"fn foo() {",
		"{{{{{{",
		"}}}}}}",
		"(((((",
		")))))",
		"[[[[[",
		"let let let",
		"=> => =>",
		"type type type",
		"fn () () ()",
		"a === b !== c",
		"new new new",
		"interface { }",
		"@@@@@",
		"1 2 3 4 5",
		"? : ? : ?",
		"a < b < c < (",
		"db.transaction(",
		"db.transaction((x) =>",
		"for (for (for (",
		"&&&&&",
		"........",
		"\"unterminated",
		"0xGG 1.2.3 __reserved",
		"fn m(): T<<<<< {}",
		"return return return;",
	}
	for _, src := range inputs {
		src := src
		t.Run(src, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", src, r)
				}
			}()
			toks, _ := lexer.Lex("test.tl", []byte(src))
			prog, _ := Parse("test.tl", toks)
			if prog == nil {
				t.Fatalf("nil program for %q", src)
			}
			// Walking String() must not panic either.
			_ = prog.String()
		})
	}
}

// TestNeverPanicIllegalSlice feeds a hand-built slice made entirely of ILLEGAL
// tokens (plus the required trailing EOF) and asserts no panic and no E-PARSE
// duplication of the (absent here) lexer diagnostics.
func TestNeverPanicIllegalSlice(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on ILLEGAL slice: %v", r)
		}
	}()
	toks, _ := lexer.Lex("test.tl", []byte("`~#\\$"))
	prog, _ := Parse("test.tl", toks)
	if prog == nil {
		t.Fatal("nil program")
	}
}
