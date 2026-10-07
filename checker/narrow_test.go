package checker

import (
	"testing"

	"tlang/ast"
	"tlang/types"
)

// narrowedCount returns how many identifier uses are marked Info.Narrowed.
func narrowedCount(info *types.Info) int {
	n := 0
	for _, b := range info.Narrowed {
		if b {
			n++
		}
	}
	return n
}

// narrowedTypeOf returns the recorded type of the first narrowed use of the
// named identifier, or nil.
func narrowedTypeOf(prog *ast.Program, info *types.Info, name string) types.Type {
	var out types.Type
	ast.Inspect(prog, func(n ast.Node) bool {
		id, ok := n.(*ast.Identifier)
		if !ok || id.Name != name || !info.Narrowed[id] {
			return true
		}
		if tv, ok := info.Types[id]; ok && out == nil {
			out = tv.Type
		}
		return true
	})
	return out
}

func TestNarrowIfNotNull(t *testing.T) {
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	let x = o + 1;
}
`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if narrowedCount(info) == 0 {
		t.Fatal("expected o to be narrowed inside if (o != null)")
	}
	if nt := narrowedTypeOf(prog, info, "o"); !types.IsBasic(nt, types.Int64) {
		t.Fatalf("narrowed o = %s, want int64", nt)
	}
}

func TestNoNarrowOutsideGuard(t *testing.T) {
	// Without the guard, o keeps its optional type and + 1 is an error.
	src := wrap(`
let o: int64 | null = 5;
let x = o + 1;
`)
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE using an un-narrowed optional, got %s", diags.Error())
	}
}

func TestNarrowAndChain(t *testing.T) {
	// b is checked under a's whenTrue facts, so a is non-null in a.len... but
	// here just confirm a && use type-checks.
	src := wrap(`
let o: int64 | null = 5;
let ok = o != null && o + 1 > 0;
`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestNarrowOrChain(t *testing.T) {
	src := wrap(`
let o: int64 | null = 5;
let ok = o == null || o + 1 > 0;
`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestNarrowBang(t *testing.T) {
	// if (!(o == null)) narrows o in the consequence.
	src := wrap(`
let o: int64 | null = 5;
if (!(o == null)) {
	let x = o + 1;
}
`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestNarrowAlwaysExitGuard(t *testing.T) {
	// if (o == null) return; narrows o for the code after the if.
	src := `
fn f(o: int64 | null): int64 {
	if (o == null) {
		return 0;
	}
	return o + 1;
}
`
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestNarrowInvalidatedByAssignment(t *testing.T) {
	// An assignment to o anywhere in the branch cancels narrowing, so o + 1
	// is an error.
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	o = null;
	let x = o + 1;
}
`)
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE after narrowing invalidated by assignment, got %s", diags.Error())
	}
}

func TestNarrowNotMarkedAtNullCompare(t *testing.T) {
	// The operand of == null keeps the optional type and is not marked.
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	let same = o == null;
}
`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	// The "o" inside "o == null" must not be marked narrowed.
	ast.Inspect(prog, func(n ast.Node) bool {
		infix, ok := n.(*ast.InfixExpression)
		if !ok || infix.Operator != "==" {
			return true
		}
		if id, ok := infix.Left.(*ast.Identifier); ok && id.Name == "o" {
			if info.Narrowed[id] {
				t.Fatal("operand of == null must not be marked narrowed")
			}
		}
		return true
	})
}

func TestNarrowNotMarkedAtNullishLeft(t *testing.T) {
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	let x = o ?? 0;
}
`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	ast.Inspect(prog, func(n ast.Node) bool {
		infix, ok := n.(*ast.InfixExpression)
		if !ok || infix.Operator != "??" {
			return true
		}
		if id, ok := infix.Left.(*ast.Identifier); ok && id.Name == "o" {
			if info.Narrowed[id] {
				t.Fatal("left operand of ?? must not be marked narrowed")
			}
		}
		return true
	})
}

func TestNarrowNotMarkedAtPostfixBang(t *testing.T) {
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	let x = o!;
}
`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	ast.Inspect(prog, func(n ast.Node) bool {
		nn, ok := n.(*ast.NonNullExpression)
		if !ok {
			return true
		}
		if id, ok := nn.Left.(*ast.Identifier); ok && id.Name == "o" {
			if info.Narrowed[id] {
				t.Fatal("operand of postfix ! must not be marked narrowed")
			}
		}
		return true
	})
}

func TestNarrowNotMarkedAtAssignTarget(t *testing.T) {
	src := wrap(`
let o: int64 | null = 5;
if (o != null) {
	o = 7;
}
`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	ast.Inspect(prog, func(n ast.Node) bool {
		asg, ok := n.(*ast.AssignmentExpression)
		if !ok {
			return true
		}
		if id, ok := asg.Target.(*ast.Identifier); ok && id.Name == "o" {
			if info.Narrowed[id] {
				t.Fatal("assignment target must not be marked narrowed")
			}
		}
		return true
	})
}

func TestFieldPathNeverNarrowed(t *testing.T) {
	// Field access (req.o) is never narrowed; only plain locals.
	src := `
interface Req { o: int64 | null; }
fn f(req: Req): int64 {
	if (req.o != null) {
		return req.o + 1;
	}
	return 0;
}
`
	_, diags := check(t, src)
	// req.o + 1 uses the optional field type, so + 1 is an error.
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE: field paths are never narrowed, got %s", diags.Error())
	}
}
