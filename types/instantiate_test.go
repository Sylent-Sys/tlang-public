package types

import (
	"strings"
	"testing"
)

// interface Page<T> { items: T[]; total: int64; next?: Page<T> }
func pageType(c *InstanceCache) *Named {
	page := iface("Page", "T")
	T := page.TypeParams[0]
	self := mustNamed(c, page, T) // Page<T> inside its own declaration
	return withFields(page,
		fieldVar("items", arr(T)),
		fieldVar("total", i64),
		fieldVar("next", opt(self)),
	)
}

func TestInstanceDedup(t *testing.T) {
	c := NewInstanceCache()
	user := withFields(iface("User"), fieldVar("id", i64))
	page := pageType(c)

	a := mustNamed(c, page, user)
	b := mustNamed(c, page, user)
	if a != b {
		t.Fatal("Page<User> instantiated twice gave two pointers")
	}
	// Structurally identical but distinct argument values still dedup.
	x := mustNamed(c, page, NewArray(user))
	y := mustNamed(c, page, NewArray(user))
	if x != y {
		t.Error("Page<User[]> not deduplicated")
	}
	// Nested instances dedup through the key of their own arguments.
	pp1 := mustNamed(c, page, mustNamed(c, page, user))
	pp2 := mustNamed(c, page, a)
	if pp1 != pp2 {
		t.Error("Page<Page<User>> not deduplicated")
	}
	if c.LookupNamed(page, []Type{user}) != a || c.LookupNamed(page, []Type{i32}) != nil {
		t.Error("LookupNamed")
	}
	// Creation order: Page<T> (from pageType), Page<User>, Page<User[]>, Page<Page<User>>.
	var names []string
	for _, n := range c.NamedInstances() {
		names = append(names, n.String())
	}
	if got := strings.Join(names, " "); got != "Page<T> Page<User> Page<User[]> Page<Page<User>>" {
		t.Errorf("instance order = %s", got)
	}
	if len(page.Instances) != 4 || page.Instances[1] != a {
		t.Errorf("origin.Instances = %v", page.Instances)
	}
	if !a.IsInstance() || a.IsGeneric() || !page.IsGeneric() || a.Origin != page || a.Obj != page.Obj {
		t.Error("instance metadata")
	}
}

func TestInstanceFields(t *testing.T) {
	c := NewInstanceCache()
	user := withFields(iface("User"), fieldVar("id", i64))
	page := pageType(c)
	pu := mustNamed(c, page, user)

	fs := pu.Fields()
	if len(fs) != 3 {
		t.Fatalf("fields = %d", len(fs))
	}
	if !Identical(fs[0].Type, arr(user)) || fs[0].Optional || fs[0].Index != 0 || fs[0].Origin != page.Fields()[0] {
		t.Errorf("items field: %v optional=%v index=%d", fs[0].Type, fs[0].Optional, fs[0].Index)
	}
	if fs[1].Type != i64 || fs[1].Kind != FieldVar {
		t.Errorf("total field: %v", fs[1])
	}
	next, ok := fs[2].Type.(*Optional)
	if !ok || next.Elem != pu || !fs[2].Optional || fs[2].Index != 2 {
		t.Errorf("next field must be Page<User> | null with the canonical pointer: %v", fs[2].Type)
	}
	if pu.Field("total") != fs[1] || pu.Field("nope") != nil {
		t.Error("Field lookup")
	}
	if &pu.Fields()[0] != &fs[0] {
		t.Error("Fields must be computed once")
	}

	// interface Box<T> { v?: T } with T := int64 | null collapses the double optional.
	box := iface("Box", "T")
	withFields(box, fieldVar("v", opt(box.TypeParams[0])))
	bo := mustNamed(c, box, opt(i64))
	if v := bo.Fields()[0]; !Identical(v.Type, opt(i64)) || !v.Optional {
		t.Errorf("Box<int64 | null>.v = %v", v.Type)
	}
	// A non-optional field becomes optional when T is optional.
	w := iface("W", "T")
	withFields(w, fieldVar("v", w.TypeParams[0]))
	if w.Fields()[0].Optional {
		t.Error("W<T>.v is not optional in the origin")
	}
	if wo := mustNamed(c, w, opt(str)); !wo.Fields()[0].Optional {
		t.Error("W<string | null>.v must be optional")
	}
}

func TestLazyFieldsBeforeOriginComplete(t *testing.T) {
	c := NewInstanceCache()
	later := iface("Later", "T")
	inst := mustNamed(c, later, i32) // allowed before Later's fields are known
	if inst.Complete() {
		t.Error("instance of an incomplete origin reported complete")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Fields() on an instance of an incomplete origin must panic")
			}
		}()
		inst.Fields()
	}()
	withFields(later, fieldVar("x", later.TypeParams[0]))
	if !inst.Complete() || inst.Fields()[0].Type != i32 {
		t.Error("instance fields after completing the origin")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("SetFields on an instance must panic")
			}
		}()
		inst.SetFields(nil)
	}()
}

func TestExpandingRecursionStops(t *testing.T) {
	// interface Tree<T> { kids: Tree<T[]>[] }
	c := NewInstanceCache()
	tree := iface("Tree", "T")
	T := tree.TypeParams[0]
	withFields(tree, fieldVar("kids", arr(mustNamed(c, tree, arr(T)))))

	n := mustNamed(c, tree, i32)
	steps := 0
	for {
		kids := n.Fields()[0].Type
		a, ok := kids.(*Array)
		if !ok {
			t.Fatalf("unexpected kids type %v", kids)
		}
		next, ok := a.Elem.(*Named)
		if !ok {
			if !IsInvalid(a.Elem) {
				t.Fatalf("expected Invalid after the depth limit, got %v", a.Elem)
			}
			break
		}
		n = next
		steps++
		if steps > 2*MaxInstanceDepth {
			t.Fatal("expansion did not stop")
		}
	}
	errs := c.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "cannot instantiate Tree<") {
		t.Errorf("Errors() = %v", errs)
	}
}

func TestInstantiateErrors(t *testing.T) {
	c := NewInstanceCache()
	page := iface("Page", "T")
	user := iface("User")
	if _, err := c.InstantiateNamed(page, nil); err == nil || !strings.Contains(err.Error(), "want 1, got 0") {
		t.Errorf("arity error: %v", err)
	}
	if _, err := c.InstantiateNamed(user, []Type{i32}); err == nil || !strings.Contains(err.Error(), "not a generic interface") {
		t.Errorf("non-generic error: %v", err)
	}
	if _, err := c.InstantiateNamed(page, []Type{nil}); err == nil {
		t.Error("nil type argument accepted")
	}
	pu := mustNamed(c, page, user)
	if _, err := c.InstantiateNamed(pu, []Type{user}); err == nil {
		t.Error("instantiating an instance accepted")
	}
	plain := &Func{Name: "f", Sig: &Signature{Result: void}}
	if _, err := c.InstantiateFunc(plain, nil); err == nil || !strings.Contains(err.Error(), "not a generic function") {
		t.Errorf("non-generic func error: %v", err)
	}
	var deep Type = i32
	for range MaxInstanceDepth {
		deep = arr(deep)
	}
	if _, err := c.InstantiateNamed(page, []Type{deep}); err == nil || !strings.Contains(err.Error(), "nested deeper") {
		t.Errorf("depth error: %v", err)
	}
}

// fn first<T>(xs: T[]): T | null
func firstFunc() *Func {
	tp := NewTypeParam(&TypeName{Name: "T"}, 0)
	return &Func{Name: "first", Sig: &Signature{
		TypeParams: []*TypeParam{tp},
		Params:     []*Var{{Name: "xs", Kind: ParamVar, Type: arr(tp)}},
		Result:     opt(tp),
	}, MayFail: true}
}

func TestInstantiateFunc(t *testing.T) {
	c := NewInstanceCache()
	user := iface("User")
	first := firstFunc()
	fu, err := c.InstantiateFunc(first, []Type{user})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.InstantiateFunc(first, []Type{user}); again != fu {
		t.Error("function instance not deduplicated")
	}
	if fu.Origin != first || !fu.IsInstance() || fu.IsGeneric() || !first.IsGeneric() || !fu.MayFail {
		t.Error("instance metadata")
	}
	if fu.Sig.TypeParams != nil || !Identical(fu.Sig.Params[0].Type, arr(user)) || !Identical(fu.Sig.Result, opt(user)) {
		t.Errorf("instance signature = %v", fu.Sig)
	}
	if p := fu.Sig.Params[0]; p == first.Sig.Params[0] || p.Origin != first.Sig.Params[0] || p.Name != "xs" || p.Kind != ParamVar {
		t.Error("instance parameters must be fresh Vars pointing at the origin's")
	}
	if first.Sig.Params[0].Type.String() != "T[]" {
		t.Error("origin signature modified")
	}
	if fu.FullName() != "first<User>" || len(first.Instances) != 1 || c.LookupFunc(first, []Type{user}) != fu ||
		c.LookupFunc(first, []Type{i32}) != nil || len(c.FuncInstances()) != 1 {
		t.Error("bookkeeping")
	}
}

func TestSubst(t *testing.T) {
	c := NewInstanceCache()
	user := iface("User")
	page := pageType(c)
	T := page.TypeParams[0]
	U := NewTypeParam(&TypeName{Name: "U"}, 0)
	tps, args := []*TypeParam{T}, []Type{user}

	if got := c.Subst(opt(arr(T)), tps, args); !Identical(got, opt(arr(user))) {
		t.Errorf("Subst(T[] | null) = %v", got)
	}
	if got := c.Subst(opt(T), tps, []Type{opt(str)}); !Identical(got, opt(str)) {
		t.Errorf("Subst(T | null, T := string | null) = %v", got)
	}
	pt := mustNamed(c, page, T)
	if got := c.Subst(pt, tps, args); got != mustNamed(c, page, user) {
		t.Errorf("Subst(Page<T>) = %v, want the canonical Page<User>", got)
	}
	// Types without the parameters come back unchanged (same pointer).
	for _, ty := range []Type{user, i64, arr(U), opt(U), mustNamed(c, page, U), page} {
		if got := c.Subst(ty, tps, args); got != ty {
			t.Errorf("Subst(%v) changed an unrelated type to %v", ty, got)
		}
	}
	if c.Subst(T, nil, nil) != T || c.Subst(nil, tps, args) != nil {
		t.Error("degenerate Subst")
	}
	sig := &Signature{TypeParams: []*TypeParam{T, U}, Params: []*Var{{Name: "a", Type: T}, {Name: "b", Type: U}}, Result: arr(T)}
	got := c.Subst(sig, tps, args).(*Signature)
	if len(got.TypeParams) != 1 || got.TypeParams[0] != U || got.Params[0].Type != user || got.Params[1].Type != U ||
		!Identical(got.Result, arr(user)) || got.Params[0].Origin != sig.Params[0] {
		t.Errorf("Subst(signature) = %v", got)
	}
	if TypeDepth(i32) != 1 || TypeDepth(arr(opt(i32))) != 3 || TypeDepth(mustNamed(c, page, arr(user))) != 3 {
		t.Error("TypeDepth")
	}
}
