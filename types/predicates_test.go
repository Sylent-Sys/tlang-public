package types

import "testing"

func TestTypeStrings(t *testing.T) {
	c := NewInstanceCache()
	user := iface("User")
	page := iface("Page", "T")
	pu := mustNamed(c, page, user)
	sig := &Signature{TypeParams: page.TypeParams, Params: []*Var{{Name: "xs", Type: arr(page.TypeParams[0])}}, Result: opt(page.TypeParams[0])}
	cases := []struct {
		t    Type
		want string
	}{
		{i64, "int64"}, {uint, "untyped int"}, {null, "null"}, {bad, "invalid type"}, {ctx, "Context"},
		{user, "User"}, {page, "Page<T>"}, {pu, "Page<User>"},
		{arr(user), "User[]"}, {arr(opt(str)), "(string | null)[]"}, {opt(arr(pu)), "Page<User>[] | null"},
		{opt(opt(i32)), "int32 | null"},
		{sig, "fn<T>(xs: T[]): T | null"},
	}
	for _, tc := range cases {
		if got := tc.t.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
	if o := opt(i32); NewOptional(o) != o {
		t.Error("NewOptional must collapse T | null | null")
	}
}

func TestIdentical(t *testing.T) {
	user, admin := iface("User"), iface("Admin")
	withFields(user, fieldVar("id", i64))
	withFields(admin, fieldVar("id", i64))
	page := iface("Page", "T")
	c1, c2 := NewInstanceCache(), NewInstanceCache()
	yes := [][2]Type{
		{i64, i64}, {arr(user), arr(user)}, {opt(arr(i32)), opt(arr(i32))}, {user, user},
		{mustNamed(c1, page, arr(user)), mustNamed(c1, page, arr(user))},
		{mustNamed(c1, page, user), mustNamed(c2, page, user)}, // different caches, same origin and args
		{page.TypeParams[0], page.TypeParams[0]},
	}
	no := [][2]Type{
		{i32, i64}, {user, admin}, {arr(user), arr(admin)}, {opt(i32), i32}, {arr(opt(user)), arr(user)},
		{mustNamed(c1, page, user), mustNamed(c1, page, admin)}, {page.TypeParams[0], iface("Q", "T").TypeParams[0]},
		{user, nil},
	}
	for _, p := range yes {
		if !Identical(p[0], p[1]) {
			t.Errorf("Identical(%v, %v) = false", p[0], p[1])
		}
	}
	for _, p := range no {
		if Identical(p[0], p[1]) {
			t.Errorf("Identical(%v, %v) = true", p[0], p[1])
		}
	}
}

func TestAssignabilityMatrix(t *testing.T) {
	user, admin := iface("User"), iface("Admin")
	withFields(user, fieldVar("id", i64))
	withFields(admin, fieldVar("id", i64)) // same shape, different type (nominal)
	tp := iface("Box", "T").TypeParams[0]

	named := map[string]Type{
		"i32": i32, "i64": i64, "f64": f64, "bool": bl, "str": str, "void": void,
		"ctx": ctx, "err": errT, "tx": tx, "uint": uint, "ufloat": ufl, "null": null,
		"User": user, "Admin": admin, "User[]": arr(user), "(User|null)[]": arr(opt(user)),
		"User|null": opt(user), "Admin|null": opt(admin), "i32|null": opt(i32), "i64|null": opt(i64),
		"f64|null": opt(f64), "str|null": opt(str), "bool|null": opt(bl), "User[]|null": opt(arr(user)),
		"T": tp, "T|null": opt(tp), "invalid": bad,
	}
	// Every pair not listed here (and not identical) must be false.
	trueFor := map[[2]string]bool{}
	allow := func(from string, tos ...string) {
		for _, to := range tos {
			trueFor[[2]string{from, to}] = true
		}
	}
	allow("uint", "i32", "i64", "f64", "i32|null", "i64|null", "f64|null")
	allow("ufloat", "f64", "f64|null")
	allow("null", "User|null", "Admin|null", "i32|null", "i64|null", "f64|null", "str|null", "bool|null", "User[]|null", "T|null")
	allow("User", "User|null")
	allow("Admin", "Admin|null")
	allow("i32", "i32|null")
	allow("i64", "i64|null")
	allow("f64", "f64|null")
	allow("str", "str|null")
	allow("bool", "bool|null")
	allow("User[]", "User[]|null")
	allow("T", "T|null")

	for fromName, from := range named {
		for toName, to := range named {
			want := fromName == toName || trueFor[[2]string{fromName, toName}] || fromName == "invalid" || toName == "invalid"
			if got := AssignableTo(from, to); got != want {
				t.Errorf("AssignableTo(%s, %s) = %v, want %v", fromName, toName, got, want)
			}
		}
	}
	if AssignableTo(nil, i32) || AssignableTo(i32, nil) {
		t.Error("nil types are never assignable")
	}
}

func TestPredicates(t *testing.T) {
	user := iface("User")
	page := iface("Page", "T")
	tp := page.TypeParams[0]
	c := NewInstanceCache()
	pt := mustNamed(c, page, tp)
	pu := mustNamed(c, page, user)

	type row struct {
		t                                                        Type
		ref, region, canOpt, typeArg, zero, comparable, concrete bool
	}
	rows := []row{
		//            ref    region canOpt typeArg zero   cmp    concrete
		{i32, false, false, true, true, true, true, true},
		{f64, false, false, true, true, true, true, true},
		{bl, false, false, true, true, true, true, true},
		{str, false, true, true, true, true, true, true},
		{void, false, false, false, false, false, false, true},
		{ctx, true, true, false, false, false, true, true},
		{errT, false, true, false, false, false, false, true},
		{tx, true, true, false, false, false, true, true},
		{uint, false, false, false, false, false, true, false},
		{null, false, false, false, false, false, false, false},
		{user, true, true, true, true, false, true, true},
		{arr(i32), true, true, true, true, false, true, true},
		{opt(i32), false, false, false, true, true, false, true},
		{opt(str), false, true, false, true, true, false, true},
		{opt(user), true, true, false, true, true, false, true},
		{tp, false, true, true, true, false, false, false},
		{page, true, true, false, false, false, true, false},
		{pt, true, true, true, true, false, true, false},
		{pu, true, true, true, true, false, true, true},
		{arr(tp), true, true, true, true, false, true, false},
	}
	for _, r := range rows {
		check := func(name string, got, want bool) {
			if got != want {
				t.Errorf("%s(%v) = %v, want %v", name, r.t, got, want)
			}
		}
		check("IsReference", IsReference(r.t), r.ref)
		check("HasRegion", HasRegion(r.t), r.region)
		check("CanBeOptional", CanBeOptional(r.t), r.canOpt)
		check("ValidTypeArg", ValidTypeArg(r.t), r.typeArg)
		check("HasZeroValue", HasZeroValue(r.t), r.zero)
		check("Comparable", Comparable(r.t), r.comparable)
		check("IsConcrete", IsConcrete(r.t), r.concrete)
	}

	if !IsPrimitiveOptional(opt(i64)) || !IsPrimitiveOptional(opt(bl)) || IsPrimitiveOptional(opt(str)) ||
		IsPrimitiveOptional(opt(user)) || IsPrimitiveOptional(i64) {
		t.Error("IsPrimitiveOptional")
	}
	if Default(uint) != i64 || Default(ufl) != f64 || Default(null) != null || Default(i32) != i32 {
		t.Error("Default")
	}
	if !IsInteger(uint) || !IsInteger(i32) || IsInteger(f64) || !IsFloat(ufl) || !IsNumeric(f64) || IsNumeric(str) {
		t.Error("numeric predicates")
	}
	if !Ordered(i32) || !Ordered(uint) || Ordered(str) || Ordered(opt(i32)) {
		t.Error("Ordered")
	}
	if NonOptional(opt(user)) != user || NonOptional(user) != user {
		t.Error("NonOptional")
	}
	if !IsUntyped(null) || IsUntyped(i32) || !IsVoid(void) || !IsString(str) || !IsBool(bl) || !IsInvalid(bad) {
		t.Error("basic predicates")
	}
}

func TestCanCompareAndOrder(t *testing.T) {
	user, admin := iface("User"), iface("Admin")
	tp := iface("Box", "T").TypeParams[0]
	eq := []struct {
		x, y Type
		want bool
	}{
		{i32, i32, true}, {i64, i32, false}, {f64, f64, true}, {bl, bl, true}, {str, str, true},
		{uint, i32, true}, {i64, uint, true}, {uint, f64, true}, {ufl, f64, true}, {ufl, i32, false},
		{uint, ufl, true}, {uint, str, false}, {uint, opt(i64), false},
		{null, opt(i32), true}, {opt(str), null, true}, {null, opt(user), true}, {null, i32, false},
		{null, user, false}, {null, null, false}, {opt(tp), null, true},
		{user, user, true}, {user, admin, false}, {opt(user), user, true}, {user, opt(user), true},
		{opt(user), opt(user), true}, {opt(user), opt(admin), false}, {arr(i32), arr(i32), true},
		{opt(arr(i32)), arr(i32), true}, {ctx, ctx, true}, {tx, tx, true},
		{opt(str), str, false}, {opt(i32), i32, false}, {opt(i32), opt(i32), false}, {opt(bl), opt(bl), false},
		{errT, errT, false}, {tp, tp, false}, {void, void, false}, {bad, str, true},
	}
	for _, c := range eq {
		if got := CanCompare(c.x, c.y); got != c.want {
			t.Errorf("CanCompare(%v, %v) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	ord := []struct {
		x, y Type
		want bool
	}{
		{i32, i32, true}, {f64, f64, true}, {i32, i64, false}, {uint, i32, true}, {f64, uint, true},
		{ufl, i64, false}, {uint, ufl, true}, {str, str, false}, {bl, bl, false}, {opt(i32), i32, false},
		{uint, opt(i32), false}, {null, null, false}, {bad, str, true},
	}
	for _, c := range ord {
		if got := CanOrder(c.x, c.y); got != c.want {
			t.Errorf("CanOrder(%v, %v) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestRegion(t *testing.T) {
	if RegionStatic.Join(RegionRequest) != RegionRequest || RegionGlobal.Join(RegionStatic) != RegionGlobal ||
		RegionNone.Join(RegionNone) != RegionNone {
		t.Error("Join")
	}
	if RegionRequest.String() != "request" || RegionNone.String() != "none" {
		t.Error("String")
	}
}
