package checker

import (
	"strings"
	"testing"

	"tlang/diag"
	"tlang/parser"
	"tlang/types"
)

// check parses src and runs Check, failing the test on a parser error (so a
// semantic test never silently masks a syntax mistake).
func check(t *testing.T, src string) (*types.Info, *diag.List) {
	t.Helper()
	if !strings.Contains(src, "import") && (strings.Contains(src, "db.") || strings.Contains(src, "console.") || strings.Contains(src, "transaction(")) {
		src = injectImports(src)
	}
	prog, pdiags := parser.ParseSource("test.tl", []byte(src))
	if pdiags.HasErrors() {
		t.Fatalf("parse %q: unexpected parser errors: %s", src, pdiags.Error())
	}
	return Check(prog)
}

func injectImports(src string) string {
	imports := "import { db } from \"tlang/db\";\nimport { console } from \"tlang/system\";\n"
	if i := strings.Index(src, "\n"); i >= 0 && strings.HasPrefix(src, "\n") {
		return imports + src[1:]
	}
	return imports + src
}

// codesOf returns the diagnostic codes in sorted (deterministic) order.
func codesOf(d *diag.List) []string {
	sorted := d.Sorted()
	out := make([]string, len(sorted))
	for i, item := range sorted {
		out[i] = item.Code
	}
	return out
}

// wantCodes asserts that the sorted diagnostic codes equal want exactly.
func wantCodes(t *testing.T, d *diag.List, want ...string) {
	t.Helper()
	got := codesOf(d)
	if len(got) != len(want) {
		t.Fatalf("diagnostics: got %v, want %v (%s)", got, want, d.Error())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("diagnostics: got %v, want %v (%s)", got, want, d.Error())
		}
	}
}

// hasMessage reports whether some diagnostic's message contains sub.
func hasMessage(d *diag.List, sub string) bool {
	for _, item := range d.Items {
		if strings.Contains(item.Message, sub) {
			return true
		}
	}
	return false
}

func TestCheckEmptyProgram(t *testing.T) {
	info, diags := check(t, "")
	if diags.HasErrors() {
		t.Fatalf("empty program: unexpected errors: %s", diags.Error())
	}
	if info == nil || info.TypeExprs == nil {
		t.Fatalf("empty program: Info not allocated")
	}
}

func TestResolvePlainInterface(t *testing.T) {
	info, diags := check(t, "interface User { id: int64; name: string; }")
	wantCodes(t, diags)
	// The interface name is defined and resolves to a plain *Named with two
	// fields in declaration order.
	var named *types.Named
	for id, obj := range info.Defs {
		if id.Name == "User" {
			tn := obj.(*types.TypeName)
			named = tn.Type.(*types.Named)
		}
	}
	if named == nil {
		t.Fatal("User not in Info.Defs")
	}
	if named.IsGeneric() {
		t.Fatal("User should not be generic")
	}
	fields := named.Fields()
	if len(fields) != 2 || fields[0].Name != "id" || fields[1].Name != "name" {
		t.Fatalf("User fields = %v", fields)
	}
	if !types.IsBasic(fields[0].Type, types.Int64) || !types.IsBasic(fields[1].Type, types.String) {
		t.Fatalf("User field types = %s, %s", fields[0].Type, fields[1].Type)
	}
}

func TestResolveGenericInterface(t *testing.T) {
	src := `
interface Box<T> { value: T; }
interface User { id: int64; }
interface Holder { b: Box<User>; }
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	holder := namedByName(t, info, "Holder")
	f := holder.Field("b")
	if f == nil {
		t.Fatal("Holder.b missing")
	}
	inst, ok := f.Type.(*types.Named)
	if !ok || !inst.IsInstance() {
		t.Fatalf("Holder.b type = %s (want Box<User> instance)", f.Type)
	}
	if inst.String() != "Box<User>" {
		t.Fatalf("Holder.b = %s, want Box<User>", inst.String())
	}
}

func TestResolveTransparentAlias(t *testing.T) {
	src := `
type Ids = int64[];
interface Store { ids: Ids; }
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	store := namedByName(t, info, "Store")
	f := store.Field("ids")
	arr, ok := f.Type.(*types.Array)
	if !ok || !types.IsBasic(arr.Elem, types.Int64) {
		t.Fatalf("Store.ids = %s, want int64[]", f.Type)
	}
}

func TestResolveObjectTypeAlias(t *testing.T) {
	src := `type Point = { x: int64; y: int64; };`
	info, diags := check(t, src)
	wantCodes(t, diags)
	point := namedByName(t, info, "Point")
	if point.IsInstance() || point.IsGeneric() {
		t.Fatal("Point should be a plain nominal interface")
	}
	if len(point.Fields()) != 2 {
		t.Fatalf("Point fields = %d, want 2", len(point.Fields()))
	}
}

func TestResolveGenericTransparentAlias(t *testing.T) {
	src := `
type List<T> = T[];
interface Store { xs: List<int64>; }
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	store := namedByName(t, info, "Store")
	f := store.Field("xs")
	arr, ok := f.Type.(*types.Array)
	if !ok || !types.IsBasic(arr.Elem, types.Int64) {
		t.Fatalf("Store.xs = %s, want int64[]", f.Type)
	}
}

func TestGenericObjectTypeAliasUnsupported(t *testing.T) {
	src := `type Pair<T> = { a: T; b: T; };`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-UNSUPPORTED")
}

func TestRedeclaration(t *testing.T) {
	src := `
interface User { id: int64; }
interface User { name: string; }
`
	_, diags := check(t, src)
	// E-NAME error plus its note (both carry the E-NAME code).
	wantCodes(t, diags, "E-NAME", "E-NAME")
	if !hasMessage(diags, "redeclared") {
		t.Fatalf("want a redeclared message, got %s", diags.Error())
	}
	if !hasMessage(diags, "previous declaration") {
		t.Fatalf("want a previous-declaration note, got %s", diags.Error())
	}
}

func TestUniverseShadowValue(t *testing.T) {
	src := `let string = 5;`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-NAME")
	if !hasMessage(diags, "builtin type/name") {
		t.Fatalf("want a universe-shadow message, got %s", diags.Error())
	}
}

func TestUniverseShadowType(t *testing.T) {
	src := `type int32 = int64;`
	_, diags := check(t, src)
	// CheckDeclName rejects "int32" as a reserved type name first.
	wantCodes(t, diags, "E-NAME")
}

func TestUndefinedType(t *testing.T) {
	src := `interface Store { x: Missing; }`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-NAME")
	if !hasMessage(diags, "undefined type") {
		t.Fatalf("want an undefined-type message, got %s", diags.Error())
	}
}

func TestResolveArrayOptionalParen(t *testing.T) {
	src := `
interface User { id: int64; }
interface Store {
	a: int64[];
	b: string | null;
	c: (User | null)[];
}
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	store := namedByName(t, info, "Store")
	if _, ok := store.Field("a").Type.(*types.Array); !ok {
		t.Fatalf("Store.a = %s, want array", store.Field("a").Type)
	}
	if !types.IsOptional(store.Field("b").Type) {
		t.Fatalf("Store.b = %s, want optional", store.Field("b").Type)
	}
	arr, ok := store.Field("c").Type.(*types.Array)
	if !ok || !types.IsOptional(arr.Elem) {
		t.Fatalf("Store.c = %s, want (User | null)[]", store.Field("c").Type)
	}
}

func TestWOptionalFieldQuestion(t *testing.T) {
	// name?: T | null writes an already-optional element -> W-OPTIONAL.
	src := `interface Store { name?: string | null; }`
	_, diags := check(t, src)
	wantCodes(t, diags, "W-OPTIONAL")
	if diags.HasErrors() {
		t.Fatalf("W-OPTIONAL must not be an error: %s", diags.Error())
	}
	if !hasMessage(diags, "redundant optional") {
		t.Fatalf("want a redundant-optional message, got %s", diags.Error())
	}
}

func TestWOptionalAliasOfOptional(t *testing.T) {
	// let x: Opt | null, Opt an alias of U | null -> element already optional.
	src := `
type Opt = string | null;
let x: Opt | null = null;
`
	_, diags := check(t, src)
	wantCodes(t, diags, "W-OPTIONAL")
	if diags.HasErrors() {
		t.Fatalf("W-OPTIONAL must not be an error: %s", diags.Error())
	}
}

func TestNoWOptionalForPlainOptional(t *testing.T) {
	// A plain optional field must NOT warn.
	src := `interface Store { name?: string; a: int64 | null; }`
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestCyclicRequiredFields(t *testing.T) {
	src := `
interface A { b: B; }
interface B { a: A; }
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-INIT") {
		t.Fatalf("want E-INIT for a required-field cycle, got %s", diags.Error())
	}
	if !hasMessage(diags, "cycle of required interface fields") {
		t.Fatalf("want a cycle message, got %s", diags.Error())
	}
}

func TestOptionalBreaksRequiredCycle(t *testing.T) {
	// An optional field breaks the required-field cycle: no E-INIT.
	src := `
interface A { b: B | null; }
interface B { a: A; }
`
	_, diags := check(t, src)
	if hasCode(diags, "E-INIT") {
		t.Fatalf("optional field should break the cycle, got %s", diags.Error())
	}
}

func TestArrayBreaksRequiredCycle(t *testing.T) {
	src := `
interface A { b: B[]; }
interface B { a: A; }
`
	_, diags := check(t, src)
	if hasCode(diags, "E-INIT") {
		t.Fatalf("array field should break the cycle, got %s", diags.Error())
	}
}

func TestMethodFieldCollision(t *testing.T) {
	src := `
interface User { name: string; }
fn (u: User) name(): string { return u.name; }
`
	info, diags := check(t, src)
	if !hasCode(diags, "E-NAME") {
		t.Fatalf("want E-NAME for a method/field collision, got %s", diags.Error())
	}
	if !hasMessage(diags, "collides with a field") {
		t.Fatalf("want a collision message, got %s", diags.Error())
	}
	// Recovery: the field is kept, the colliding method is dropped.
	user := namedByName(t, info, "User")
	if user.Field("name") == nil {
		t.Fatal("field name should be kept after collision")
	}
	if user.Method("name") != nil {
		t.Fatal("colliding method name should be dropped")
	}
}

func TestReceiverMethodAttached(t *testing.T) {
	src := `
interface User { id: int64; }
fn (u: User) greet(): int64 { return u.id; }
`
	info, diags := check(t, src)
	wantCodes(t, diags)
	user := namedByName(t, info, "User")
	if m := user.Method("greet"); m == nil {
		t.Fatal("greet not attached to User")
	}
}

// namedByName returns the *Named declared with the given name, failing the
// test when it is absent.
func namedByName(t *testing.T, info *types.Info, name string) *types.Named {
	t.Helper()
	for id, obj := range info.Defs {
		if id.Name != name {
			continue
		}
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		if n, ok := tn.Type.(*types.Named); ok {
			return n
		}
	}
	t.Fatalf("%s not found as a *Named in Info.Defs", name)
	return nil
}

// hasCode reports whether any diagnostic carries code.
func hasCode(d *diag.List, code string) bool {
	for _, item := range d.Items {
		if item.Code == code {
			return true
		}
	}
	return false
}
