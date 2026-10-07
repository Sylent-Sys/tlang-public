package codegen

import (
	"strings"
	"testing"

	"tlang/token"
	"tlang/types"
)

func TestNameWrappers(t *testing.T) {
	g := testGenerator()
	i64 := types.Typ[types.Int64]
	user := testNamed("User", pos(1, 11), &types.Var{Name: "id", Type: i64})
	users := types.NewArray(user)
	logger := &types.Func{Name: "logger", Sig: &types.Signature{Result: types.Typ[types.Void]}}
	greet := &types.Func{Name: "greet", Sig: &types.Signature{
		Recv:   &types.Var{Name: "u", Kind: types.RecvVar, Type: user},
		Result: types.Typ[types.Void],
	}}
	cases := []struct{ got, want string }{
		{g.mangle(i64), "i64"},
		{g.mangle(users), "arr_User"},
		{g.ctype(types.NewOptional(types.Typ[types.Int32])), "tlang_opt_i32"},
		{g.ctype(user), "tl_User*"},
		{g.ctype(types.Typ[types.Void]), "void"},
		{g.structName(user), "tl_User"},
		{g.sliceName(user), "tlang_slice_User"},
		{g.sliceName(types.Typ[types.String]), "tlang_slice_str"},
		{g.funcName(logger), "tl_f_logger"},
		{g.funcName(greet), "tl_m_User__greet"},
		{g.jsonParseName(users), "tlj_parse_arr_User"},
		{g.jsonWriteName(user), "tlj_write_User"},
		{fieldName("name"), "f_name"},
		{globalName(&types.Var{Name: "cache"}), "g_cache"},
		{localBase("req"), "l_req"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}

	// Non-concrete types are internal errors, not panics of package types.
	tp := types.NewTypeParam(&types.TypeName{Name: "T"}, 0)
	generic := types.NewNamed(&types.TypeName{Name: "Box"}, nil, []*types.TypeParam{tp})
	origin := &types.Func{Name: "first", Sig: &types.Signature{TypeParams: []*types.TypeParam{tp}, Result: tp}}
	bad := []func(){
		func() { g.mangle(tp) },
		func() { g.ctype(nil) },
		func() { g.ctype(types.Typ[types.Invalid]) },
		func() { g.ctype(types.Typ[types.UntypedNull]) },
		func() { g.structName(generic) },
		func() { g.structName(nil) },
		func() { g.sliceName(types.NewArray(tp)) },
		func() { g.funcName(origin) },
		func() { g.funcName(&types.Func{Name: "nosig"}) },
		func() { g.jsonParseName(types.Typ[types.UntypedInt]) },
		func() { g.jsonWriteName(generic) },
	}
	for i, f := range bad {
		err := catch(g, f)
		if err == nil || !strings.Contains(err.Error(), "test.tl: internal error: ") {
			t.Errorf("case %d: got %v, want an internal error", i, err)
		}
	}
}

func TestGeneratedNames(t *testing.T) {
	cases := []struct{ got, want string }{
		{routeName(3), "__tl_r3"},
		{routeSegsName(3), "__tl_r3_segs"},
		{typeDescName("User"), "__tl_td_User"},
		{fieldDescName("Page__User"), "__tl_fd_Page__User"},
		{longStrName(12), "__tl_s12"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestLocalNames(t *testing.T) {
	l := newLocalNames()
	l.reserve("__fib")
	expect := func(v *types.Var, want string) {
		t.Helper()
		if got := l.declare(v); got != want {
			t.Fatalf("declare(%s) = %q, want %q", v.Name, got, want)
		}
		if got, ok := l.name(v); !ok || got != want {
			t.Fatalf("name(%s) = %q, %v; want %q", v.Name, got, ok, want)
		}
	}

	// The function scope holds the receiver and the parameters.
	recv, x := testVar("u"), testVar("x")
	expect(recv, "l_u")
	expect(x, "l_x")

	// Nested shadowing takes the first free suffix.
	l.push()
	x2 := testVar("x")
	expect(x2, "l_x__2")
	l.push()
	expect(testVar("x"), "l_x__3")
	l.pop()

	// A sibling scope reuses the released name.
	l.push()
	expect(testVar("x"), "l_x__3")
	l.pop()
	l.pop()
	l.push()
	expect(testVar("x"), "l_x__2")
	expect(testVar("y"), "l_y")
	l.pop()

	// Popping released l_y; the names of closed scopes stay resolvable.
	expect(testVar("y"), "l_y")
	if got, ok := l.name(x2); !ok || got != "l_x__2" {
		t.Fatalf("name of a variable of a closed scope = %q, %v", got, ok)
	}

	// A reserved name is skipped like a declared one.
	l.reserve("l_z__2")
	l.push()
	expect(testVar("z"), "l_z")
	l.push()
	expect(testVar("z"), "l_z__3")
	l.pop()
	l.pop()

	// alias maps the origin's parameter to the instance parameter's name.
	instParam := testVar("p")
	originParam := testVar("p")
	expect(instParam, "l_p")
	l.alias(originParam, instParam)
	if got, ok := l.name(originParam); !ok || got != "l_p" {
		t.Fatalf("alias: name = %q, %v; want l_p", got, ok)
	}
	if _, ok := l.name(testVar("unknown")); ok {
		t.Fatalf("an undeclared variable must have no name")
	}

	mustPanic(t, "declared twice", func() { l.declare(x) })
	mustPanic(t, "declare of a nil variable", func() { l.declare(nil) })
	mustPanic(t, "alias of unnamed variable", func() { l.alias(testVar("a"), testVar("b")) })
	mustPanic(t, "pop of the function scope", func() { l.pop() })
}

func TestNameTable(t *testing.T) {
	// Reserved names of generated code.
	for _, cname := range reservedCNames {
		tab := newNameTable()
		err := tab.add(cname, "interface X", pos(1, 11))
		want := "1:11: unsupported input: C name collision: " + cname +
			" is reserved for generated code (interface X at 1:11)"
		if err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", cname, err, want)
		}
	}

	tab := newNameTable()
	add := func(cname, what string, p token.Position) *cgError {
		t.Helper()
		return tab.add(cname, what, p)
	}
	// Distinct names never collide.
	for i, cname := range []string{"tl_User", "tl_f_logger_x", "tl_m_User__greet", "tl_f_first__User", "tl_Page__User"} {
		if err := add(cname, "decl", pos(i+1, 1)); err != nil {
			t.Fatalf("%s: unexpected %v", cname, err)
		}
	}
	// interface f_logger beside fn logger: both positions, in source order,
	// reported at the later one.
	if err := add("tl_f_logger", "interface f_logger", pos(1, 11)); err != nil {
		t.Fatal(err)
	}
	err := add("tl_f_logger", "function logger", pos(5, 4))
	want := "5:4: unsupported input: C name collision: tl_f_logger (interface f_logger at 1:11 and function logger at 5:4)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	// Registered later but declared earlier: still listed in source order.
	if err := add("tl_m_User__a", "interface m_User<a>", pos(9, 11)); err != nil {
		t.Fatal(err)
	}
	err = add("tl_m_User__a", "method User.a", pos(3, 15))
	want = "9:11: unsupported input: C name collision: tl_m_User__a (method User.a at 3:15 and interface m_User<a> at 9:11)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	// Reserving a name a user declaration already has is the same error.
	if err := add("tl_X", "interface X", pos(2, 11)); err != nil {
		t.Fatal(err)
	}
	err = tab.reserve("tl_X")
	want = "2:11: unsupported input: C name collision: tl_X is reserved for generated code (interface X at 2:11)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if err := tab.reserve("tl_globals"); err != nil {
		t.Fatalf("reserving a reserved name again: %v", err)
	}
}

func TestDescriptions(t *testing.T) {
	_, info := checkSrc(t, `
interface User { id: int64; }
type Point = { x: float64; };
interface Box<T> { v: T; }
fn (u: User) greet(): void {}
fn (c: Context) handle(): void {}
fn first<T>(x: T): T { return x; }
fn main(): void {
    let b = new Box<User>();
    let f = first<User>(new User());
}
`)
	var got []string
	for _, n := range info.Interfaces {
		got = append(got, namedWhat(n)+" @"+namedPos(n).String())
	}
	for _, f := range info.Funcs {
		got = append(got, funcWhat(f)+" @"+f.Pos.String())
	}
	for _, f := range info.FuncInstances {
		got = append(got, funcWhat(f)+" @"+f.Pos.String())
	}
	want := []string{
		"interface User @2:11",
		"type Point @3:6",
		"interface Box<User> @4:11",
		"method User.greet @5:14",
		"method Context.handle @6:17",
		"function main @8:4",
		"function first<User> @7:4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("descriptions:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if namedPos(types.NewNamed(nil, nil, nil)).IsValid() {
		t.Fatalf("an interface without a name object has no position")
	}
}
