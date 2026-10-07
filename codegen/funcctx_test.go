package codegen

import (
	"strings"
	"testing"

	"tlang/ast"
	"tlang/types"
)

func TestFuncCtxParameters(t *testing.T) {
	_, info := checkSrc(t, `interface User {
    id: int64;
}

fn (u: User) add(n: int64, s: string): int64 {
    return u.id + n;
}

fn id<T>(x: T, y: int64): T {
    return x;
}

fn main(): void {
    let a = id<string>("s", 1);
}
`)
	g := testGenerator()

	// A method: the receiver first, then the parameters, all in the function
	// scope.
	var add *types.Func
	for _, f := range info.Funcs {
		if f.Name == "add" {
			add = f
		}
	}
	fc := g.newFuncCtx(add)
	if fc.inst != nil {
		t.Fatalf("a plain method is not an instance")
	}
	for i, v := range []*types.Var{add.Sig.Recv, add.Sig.Params[0], add.Sig.Params[1]} {
		want := []string{"l_u", "l_n", "l_s"}[i]
		if got, ok := fc.locals.name(v); !ok || got != want {
			t.Errorf("name(%s) = %q, %v; want %q", v.Name, got, ok, want)
		}
	}

	// A generic instance: the origin's parameters, which the shared body
	// refers to, have the instance parameters' names.
	if len(info.FuncInstances) != 1 {
		t.Fatalf("want one instance, got %d", len(info.FuncInstances))
	}
	inst := info.FuncInstances[0]
	fc = g.newFuncCtx(inst)
	if fc.inst != inst {
		t.Fatalf("an instance drives Concrete: inst must be set")
	}
	for i, p := range inst.Sig.Params {
		want := []string{"l_x", "l_y"}[i]
		if got, ok := fc.locals.name(p); !ok || got != want {
			t.Errorf("instance param %s = %q, %v; want %q", p.Name, got, ok, want)
		}
		if got, ok := fc.locals.name(inst.Origin.Sig.Params[i]); !ok || got != want {
			t.Errorf("origin param %s = %q, %v; want %q", p.Name, got, ok, want)
		}
	}

	// tl__init_globals has no function.
	fc = g.newFuncCtx(nil)
	if fc.fn != nil || fc.inst != nil || fc.blk != fc.body {
		t.Fatalf("tl__init_globals context: %+v", fc)
	}
	for _, name := range []string{"__fib", "__fail", "__guard_denied"} {
		if fc.locals.inUse[name] != 1 {
			t.Fatalf("%s must be reserved", name)
		}
	}

	if err := catch(g, func() { g.newFuncCtx(&types.Func{Name: "nosig"}) }); err == nil ||
		!strings.Contains(err.Error(), "internal error: function nosig has no signature") {
		t.Fatalf("missing signature: got %v", err)
	}
}

// TestFuncCtxConcrete checks the generic-instance mapping of types and
// callees, and §12 item 9: a callee whose instance the checker did not
// create (simulated with an empty instance cache) is an internal error.
func TestFuncCtxConcrete(t *testing.T) {
	src := `fn id<T>(x: T): T {
    return x;
}
fn twice<T>(x: T): T {
    let y = id<T>(x);
    return id(y);
}
fn main(): void {
    let a = twice<int64>(1);
}
`
	_, info := checkSrc(t, src)
	var twice *types.Func
	for _, f := range info.FuncInstances {
		if f.Name == "twice" {
			twice = f
		}
	}
	var call *ast.CallExpression
	ast.Inspect(twice.Decl.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpression); ok && call == nil {
			call = c
		}
		return true
	})
	g := testGenerator()
	g.info = info
	fc := g.newFuncCtx(twice)
	if got := fc.concrete(twice.Origin.Sig.Params[0].Type); !types.Identical(got, types.Typ[types.Int64]) {
		t.Fatalf("concrete(T) in twice<int64> = %v", got)
	}
	callee := info.Calls[call].Func
	got := fc.concreteFunc(callee, call.Pos())
	if got.FullName() != "id<int64>" || !got.IsInstance() {
		t.Fatalf("concreteFunc(%s) = %s", callee.FullName(), got.FullName())
	}
	plain := g.newFuncCtx(info.Funcs[0])
	if f := plain.concreteFunc(info.Funcs[0], call.Pos()); f != info.Funcs[0] {
		t.Fatalf("outside generic code the callee is returned unchanged")
	}
	if err := catch(g, func() { plain.concreteFunc(nil, call.Pos()) }); err == nil ||
		err.Error() != "test.tl:5:13: internal error: call without a callee" {
		t.Fatalf("nil callee: got %v", err)
	}

	info.Instances = types.NewInstanceCache()
	err := catch(g, func() { fc.concreteFunc(callee, call.Pos()) })
	want := "test.tl:5:13: internal error: missing instance of id<T> in twice<int64>"
	if err == nil || err.Error() != want {
		t.Fatalf("missing instance: got %v, want %q", err, want)
	}
}

func TestFuncCtxBlocks(t *testing.T) {
	g := testGenerator()
	fc := g.newFuncCtx(nil)
	a, inner, b := testVar("a"), testVar("a"), testVar("b")

	if got := fc.declare(a, "int64_t"); got != "l_a" {
		t.Fatalf("declare = %q", got)
	}
	fc.blk.line("l_a = 1;")
	fc.blk.line("{")
	fc.enter()
	if got := fc.declare(inner, "tlang_string"); got != "l_a__2" {
		t.Fatalf("shadowing declare = %q", got)
	}
	fc.blk.line("l_a__2 = TLANG_STR(\"x\");")
	fc.blk.deferred(func() string { return "(void)l_a__2;" })
	fc.leave()
	fc.blk.line("}")
	// The inner name was released: a later sibling reuses it.
	fc.blk.line("{")
	fc.enter()
	if got := fc.declare(testVar("a"), "bool"); got != "l_a__2" {
		t.Fatalf("sibling declare = %q", got)
	}
	fc.leave()
	fc.blk.line("}")
	if got := fc.declare(b, "tl_User*"); got != "l_b" {
		t.Fatalf("declare = %q", got)
	}
	if fc.blk != fc.body {
		t.Fatalf("leave must return to the body")
	}
	fc.body.finish()

	var w cwriter
	w.open("static void tl__init_globals(tlang_fiber* __fib, void* __globals)")
	w.writeBlock(fc.body)
	w.close("")
	want := strings.Join([]string{
		"static void tl__init_globals(tlang_fiber* __fib, void* __globals) {",
		"    int64_t l_a;",
		"    tl_User* l_b;",
		"    l_a = 1;",
		"    {",
		"        tlang_string l_a__2;",
		"        l_a__2 = TLANG_STR(\"x\");",
		"        (void)l_a__2;",
		"    }",
		"    {",
		"        bool l_a__2;",
		"    }",
		"}",
		"",
	}, "\n")
	if got := string(w.bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	if err := catch(g, func() { fc.leave() }); err == nil || !strings.Contains(err.Error(), "leave without enter") {
		t.Fatalf("leave of the body: got %v", err)
	}
}

func TestFuncCtxCounters(t *testing.T) {
	fc := testGenerator().newFuncCtx(nil)
	if got := fc.temp(); got != "__t1" {
		t.Fatalf("temp = %q", got)
	}
	if got := fc.temp(); got != "__t2" {
		t.Fatalf("temp = %q", got)
	}
	if got := fc.nextTmp(); got != 3 {
		t.Fatalf("nextTmp = %d (temporaries and other generated locals share the counter)", got)
	}
	if a, b := fc.nextLabel(), fc.nextLabel(); a != 1 || b != 2 {
		t.Fatalf("nextLabel = %d, %d", a, b)
	}
}
