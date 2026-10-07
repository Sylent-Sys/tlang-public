package codegen

import (
	"strings"
	"testing"

	"tlang/types"
)

// jsonClosureOf runs type collection and the recomputed JSON demand closure
// on a checked program, returning the ordered closure. The program must be
// front-end clean.
func jsonClosureOf(t *testing.T, src string) (*generator, []jsonType) {
	t.Helper()
	prog, info := checkSrc(t, src)
	g := &generator{
		prog:      prog,
		info:      info,
		file:      prog.File,
		routeUsed: map[int]bool{},
		dbUsed:    map[string]bool{},
	}
	g.names = newNameTable()
	g.collectTypes()
	return g, g.jsonClosure()
}

// find returns the closure entry for the type whose Mangle is m, and whether
// it is present.
func find(g *generator, order []jsonType, m string) (jsonType, bool) {
	for _, jt := range order {
		if g.mangle(jt.t) == m {
			return jt, true
		}
	}
	return jsonType{}, false
}

// TestJSONClosurePropagation checks that the recomputed closure propagates
// Parse and Write to field dependencies to a fixed point (plan D20): the
// json_flags shape where ctx.json(a) demands A write (so C gains write) and
// ctx.bindJson(b)/(n) demand B/N parse (so A, C gain parse and the self-array
// arr_N gains both directions).
func TestJSONClosurePropagation(t *testing.T) {
	g, order := jsonClosureOf(t, `interface C {
    v: int64;
}

interface A {
    c: C;
}

interface B {
    a: A;
}

interface N {
    name: string;
    kids: N[];
}

fn route_dispatcher(ctx: Context): void {
    let a = new A();
    ctx.json(200, a);
    let b = new B();
    if (ctx.bindJson(b)) {
        ctx.text(200, "b");
    }
    let n = new N();
    if (ctx.bindJson(n)) {
        ctx.json(200, n);
    }
}
`)
	cases := []struct {
		m            string
		parse, write bool
	}{
		{"A", true, true},  // bindJson(b) -> B -> A parse; ctx.json(a) -> A write
		{"B", true, false}, // only bindJson(b)
		{"C", true, true},  // through A, both directions
		{"N", true, true},  // bindJson(n) parse, ctx.json(n) write
		{"arr_N", true, true},
	}
	for _, c := range cases {
		jt, ok := find(g, order, c.m)
		if !ok {
			t.Fatalf("%s not in the closure", c.m)
		}
		if jt.parse != c.parse || jt.write != c.write {
			t.Errorf("%s: parse=%v write=%v, want parse=%v write=%v", c.m, jt.parse, jt.write, c.parse, c.write)
		}
	}
}

// TestJSONClosureInlinedCategories checks that primitive/bool/string fields
// and optional-primitive fields contribute no generated function, while a
// *Named field, an array field and an array-of-array element do (plan D20).
func TestJSONClosureInlinedCategories(t *testing.T) {
	g, order := jsonClosureOf(t, `interface Inner {
    v: int64;
}

interface Shape {
    n: int64;
    s: string;
    b: bool;
    opt: int32 | null;
    maybeStr: string | null;
    inner: Inner;
    innerOpt: Inner | null;
    nums: int64[];
    grid: int64[][];
}

fn route_dispatcher(ctx: Context): void {
    let x = new Shape();
    ctx.json(200, x);
}
`)
	present := map[string]bool{}
	for _, jt := range order {
		present[g.mangle(jt.t)] = true
	}
	wantPresent := []string{"Shape", "Inner", "arr_i64", "arr_arr_i64"}
	for _, m := range wantPresent {
		if !present[m] {
			t.Errorf("%s should be in the closure", m)
		}
	}
	// A primitive/bool/string/optional-primitive field never adds a scalar
	// JSON function: there is no tlj_* for int64, string, bool or int32.
	for _, m := range []string{"i64", "str", "bool", "i32", "opt_i32", "opt_str"} {
		if present[m] {
			t.Errorf("%s must not be in the closure (inlined as a runtime primitive)", m)
		}
	}
}

// TestJSONMaskConstants checks the required-mask constant for 1, 32, 33, 40
// and 64 required fields (plan D20): the mask is UINT64_C(0x...) with the
// low n bits set.
func TestJSONMaskConstants(t *testing.T) {
	g := testGenerator()
	cases := []struct {
		n    int
		want string
	}{
		{1, "UINT64_C(0x1)"},
		{32, "UINT64_C(0xffffffff)"},
		{33, "UINT64_C(0x1ffffffff)"},
		{40, "UINT64_C(0xffffffffff)"},
		{64, "UINT64_C(0xffffffffffffffff)"},
	}
	for _, c := range cases {
		fields := make([]*types.Var, c.n)
		for i := range fields {
			fields[i] = &types.Var{Name: "f", Kind: types.FieldVar, Type: types.Typ[types.Int64]}
		}
		if got := g.requiredMask(fields); got != c.want {
			t.Errorf("%d fields: mask %s, want %s", c.n, got, c.want)
		}
	}
	// All-optional fields require nothing: the parser returns true.
	optFields := []*types.Var{{Name: "o", Kind: types.FieldVar, Type: types.NewOptional(types.Typ[types.Int64])}}
	if got := g.requiredMask(optFields); got != "" {
		t.Errorf("all-optional mask %q, want empty", got)
	}
}

// TestJSONTooWide checks that a JSON-demanded interface with 65 fields is
// §12 item 10, reported at the interface declaration (plan D20).
func TestJSONTooWide(t *testing.T) {
	var b strings.Builder
	b.WriteString("interface Huge {\n")
	for i := 0; i < maxJSONFields+1; i++ {
		b.WriteString("    f")
		b.WriteString(itoaTest(i))
		b.WriteString(": int64;\n")
	}
	b.WriteString("}\n\nfn route_dispatcher(ctx: Context): void {\n    let h = new Huge();\n    if (ctx.bindJson(h)) {\n        ctx.text(200, \"ok\");\n    }\n}\n")

	prog, info := checkSrc(t, b.String())
	got := emitErr(t, prog, info)
	want := prefixUnsupported + "JSON type Huge has 65 fields; at most 64 are supported"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to contain %q", got, want)
	}
}

// itoaTest formats a small non-negative int for the generated field names.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
