package types

import (
	"strings"
	"testing"

	"tlang/ast"
)

func names(list []*Named) string {
	parts := make([]string, len(list))
	for i, n := range list {
		parts[i] = n.String()
	}
	return strings.Join(parts, " ")
}

func TestSortByFieldDeps(t *testing.T) {
	a, b, c, d := iface("A"), iface("B"), iface("C"), iface("D")
	withFields(c)
	withFields(b, fieldVar("c", opt(c)))
	withFields(a, fieldVar("b", b), fieldVar("cs", arr(c)), fieldVar("ext", iface("NotInList")))
	withFields(d, fieldVar("n", i64))
	if got := names(SortByFieldDeps([]*Named{a, d, b, c})); got != "C B A D" {
		t.Errorf("order = %s", got)
	}
	// Cycle through an optional field: X { y?: Y }, Y { x: X }.
	x, y := iface("X"), iface("Y")
	withFields(x, fieldVar("y", opt(y)))
	withFields(y, fieldVar("x", x))
	if got := names(SortByFieldDeps([]*Named{x, y})); got != "Y X" {
		t.Errorf("cycle order = %s", got)
	}
	// Instances participate like plain types.
	cache := NewInstanceCache()
	page := iface("Page", "T")
	withFields(page, fieldVar("items", arr(page.TypeParams[0])))
	pu := mustNamed(cache, page, a)
	holder := withFields(iface("Holder"), fieldVar("p", pu))
	if got := names(SortByFieldDeps([]*Named{holder, pu, a, b, c})); got != "C B A Page<A> Holder" {
		t.Errorf("instance order = %s", got)
	}
}

func TestInfoLookupsAndConcrete(t *testing.T) {
	info := NewInfo()
	if info.Types == nil || info.TypeExprs == nil || info.Defs == nil || info.Uses == nil || info.Selections == nil ||
		info.Calls == nil || info.Conversions == nil || info.Narrowed == nil || info.MayFail == nil ||
		info.TxIDs == nil || info.Instances == nil {
		t.Fatal("NewInfo must allocate every map and the cache")
	}
	decl, use := &ast.Identifier{Name: "x"}, &ast.Identifier{Name: "x"}
	v := &Var{Name: "x", Kind: LocalVar, Type: i64}
	info.Defs[decl] = v
	info.Uses[use] = v
	info.Types[use] = TypeAndValue{Type: i64}
	if info.ObjectOf(decl) != v || info.ObjectOf(use) != v || info.ObjectOf(&ast.Identifier{}) != nil {
		t.Error("ObjectOf")
	}
	if info.TypeOf(use) != i64 || info.TypeOf(decl) != nil {
		t.Error("TypeOf")
	}

	// fn g<T>(xs: T[]): void { first<T>(xs); }   with instance g<bool>
	user := iface("User")
	first := firstFunc()
	T := NewTypeParam(&TypeName{Name: "T"}, 0)
	g := &Func{Name: "g", Sig: &Signature{TypeParams: []*TypeParam{T}, Params: []*Var{{Name: "xs", Kind: ParamVar, Type: arr(T)}}, Result: void}}
	firstT, _ := info.Instances.InstantiateFunc(first, []Type{T})
	firstUser, _ := info.Instances.InstantiateFunc(first, []Type{user})
	gb, _ := info.Instances.InstantiateFunc(g, []Type{bl})

	if got := info.Concrete(opt(arr(T)), gb); !Identical(got, opt(arr(bl))) {
		t.Errorf("Concrete = %v", got)
	}
	if info.Concrete(arr(T), nil) == nil || info.Concrete(i64, g) != i64 {
		t.Error("Concrete without an instance must return t")
	}
	if info.ConcreteFunc(firstT, gb) != nil {
		t.Error("ConcreteFunc must not create first<bool>")
	}
	firstBool, _ := info.Instances.InstantiateFunc(first, []Type{bl})
	if info.ConcreteFunc(firstT, gb) != firstBool {
		t.Error("ConcreteFunc(first<T>, g<bool>) must be first<bool>")
	}
	if info.ConcreteFunc(firstUser, gb) != firstUser || info.ConcreteFunc(g, gb) != g || info.ConcreteFunc(firstT, nil) != firstT {
		t.Error("ConcreteFunc must keep callees that need no substitution")
	}
}

func TestSliceTypes(t *testing.T) {
	info := NewInfo()
	user := iface("User")
	withFields(user, fieldVar("tags", arr(str)), fieldVar("friends", arr(user)))
	info.Interfaces = []*Named{user}

	// let g: int64[][] = [[1]];
	inner := &ast.ArrayLiteral{Elements: []ast.Expression{&ast.IntegerLiteral{Raw: "1", Value: 1}}}
	outer := &ast.ArrayLiteral{Elements: []ast.Expression{inner}}
	gName := &ast.Identifier{Name: "g"}
	gDecl := &ast.LetStatement{Name: gName, Value: outer}
	g := &Var{Name: "g", Kind: GlobalVar, Type: arr(arr(i64)), Decl: gDecl}
	info.Defs[gName] = g
	info.Types[outer] = TypeAndValue{Type: arr(arr(i64))}
	info.Types[inner] = TypeAndValue{Type: arr(i64)}
	info.Globals = []*Var{g}

	// fn f(xs: (int64 | null)[]) { let ys = new User[](); }
	ys := &ast.Identifier{Name: "ys"}
	newUsers := &ast.NewExpression{Type: &ast.NamedType{Name: &ast.Identifier{Name: "User"}}, IsArray: true}
	fDecl := &ast.FunctionStatement{Name: &ast.Identifier{Name: "f"}, Body: &ast.BlockStatement{Statements: []ast.Statement{
		&ast.LetStatement{Name: ys, Value: newUsers}}}}
	f := &Func{Name: "f", Decl: fDecl, Sig: &Signature{Params: []*Var{{Name: "xs", Kind: ParamVar, Type: arr(opt(i64))}}, Result: void}}
	info.Defs[ys] = &Var{Name: "ys", Kind: LocalVar, Type: arr(user)}
	info.Types[newUsers] = TypeAndValue{Type: arr(user)}
	info.Funcs = []*Func{f}

	// fn h<T>(xs: T[]) { let zs = new T[][](); }  instantiated as h<bool>
	T := NewTypeParam(&TypeName{Name: "T"}, 0)
	zs := &ast.Identifier{Name: "zs"}
	newNested := &ast.NewExpression{Type: &ast.ArrayType{Elem: &ast.NamedType{Name: &ast.Identifier{Name: "T"}}}, IsArray: true}
	hDecl := &ast.FunctionStatement{Name: &ast.Identifier{Name: "h"}, Body: &ast.BlockStatement{Statements: []ast.Statement{
		&ast.LetStatement{Name: zs, Value: newNested}}}}
	h := &Func{Name: "h", Decl: hDecl, Sig: &Signature{TypeParams: []*TypeParam{T}, Params: []*Var{{Name: "xs", Kind: ParamVar, Type: arr(T)}}, Result: void}}
	info.Defs[zs] = &Var{Name: "zs", Kind: LocalVar, Type: arr(arr(T))}
	info.Types[newNested] = TypeAndValue{Type: arr(arr(T))}
	hb, _ := info.Instances.InstantiateFunc(h, []Type{bl})
	info.FuncInstances = []*Func{hb}

	info.JSONTypes = []*JSONType{{Type: arr(arr(str)), Write: true}}

	var got []string
	for _, a := range info.SliceTypes() {
		got = append(got, Mangle(a))
	}
	want := "arr_str arr_User arr_i64 arr_arr_i64 arr_opt_i64 arr_bool arr_arr_bool arr_arr_str"
	if strings.Join(got, " ") != want {
		t.Errorf("SliceTypes = %s\nwant          %s", strings.Join(got, " "), want)
	}
}
