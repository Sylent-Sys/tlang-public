package types

import "testing"

func TestMangleAndCType(t *testing.T) {
	c := NewInstanceCache()
	user := iface("User")
	page := iface("Page", "T")
	mp := iface("Map", "K", "V")
	pu := mustNamed(c, page, user)
	ppu := mustNamed(c, page, pu)
	mapT := mustNamed(c, mp, str, arr(user))

	cases := []struct {
		t            Type
		mangle, ctyp string
	}{
		{i32, "i32", "int32_t"},
		{i64, "i64", "int64_t"},
		{f64, "f64", "double"},
		{bl, "bool", "bool"},
		{str, "str", "tlang_string"},
		{void, "void", "void"},
		{ctx, "Context", "tlang_ctx*"},
		{errT, "Error", "tlang_error"},
		{tx, "Transaction", "tlang_tx*"},
		{user, "User", "tl_User*"},
		{pu, "Page__User", "tl_Page__User*"},
		{ppu, "Page__Page__User", "tl_Page__Page__User*"},
		{mapT, "Map__str__arr_User", "tl_Map__str__arr_User*"},
		{arr(i64), "arr_i64", "tlang_slice_i64*"},
		{arr(user), "arr_User", "tlang_slice_User*"},
		{arr(arr(user)), "arr_arr_User", "tlang_slice_arr_User*"},
		{arr(opt(str)), "arr_opt_str", "tlang_slice_opt_str*"},
		{arr(opt(i64)), "arr_opt_i64", "tlang_slice_opt_i64*"},
		{opt(Typ[JsonValue]), "opt_JsonValue", "tlang_opt_json_value"},
		{arr(pu), "arr_Page__User", "tlang_slice_Page__User*"},
		{opt(i32), "opt_i32", "tlang_opt_i32"},
		{opt(i64), "opt_i64", "tlang_opt_i64"},
		{opt(f64), "opt_f64", "tlang_opt_f64"},
		{opt(bl), "opt_bool", "tlang_opt_bool"},
		{opt(str), "opt_str", "tlang_string"},
		{opt(user), "opt_User", "tl_User*"},
		{opt(arr(i64)), "opt_arr_i64", "tlang_slice_i64*"},
		{opt(pu), "opt_Page__User", "tl_Page__User*"},
	}
	for _, tc := range cases {
		if got := Mangle(tc.t); got != tc.mangle {
			t.Errorf("Mangle(%v) = %q, want %q", tc.t, got, tc.mangle)
		}
		if got := CType(tc.t); got != tc.ctyp {
			t.Errorf("CType(%v) = %q, want %q", tc.t, got, tc.ctyp)
		}
	}

	if StructCName(pu) != "tl_Page__User" || SliceCName(user) != "tlang_slice_User" || SliceCName(opt(str)) != "tlang_slice_opt_str" {
		t.Error("struct/slice names")
	}
	if FieldCName("name") != "f_name" || GlobalCName("", "cache") != "g_cache" || LocalCName("req") != "l_req" {
		t.Error("field/global/local names")
	}
	if JSONParseCName(user) != "tlj_parse_User" || JSONWriteCName(arr(pu)) != "tlj_write_arr_Page__User" {
		t.Error("JSON names")
	}
	for _, e := range []Type{i32, i64, f64, bl, str, opt(Typ[JsonValue])} {
		if !PredefinedSlice(e) {
			t.Errorf("PredefinedSlice(%v) = false", e)
		}
	}
	for _, e := range []Type{user, opt(i32), arr(i32), errT} {
		if PredefinedSlice(e) {
			t.Errorf("PredefinedSlice(%v) = true", e)
		}
	}

	tp := page.TypeParams[0]
	for _, bad := range []Type{uint, ufl, null, Typ[Invalid], tp, page, arr(tp), opt(errT), opt(ctx), &Signature{Result: void}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("CType/Mangle(%v) should panic", bad)
				}
			}()
			_ = CType(bad) + Mangle(bad)
		}()
	}
}

func TestFuncCName(t *testing.T) {
	c := NewInstanceCache()
	user := iface("User")
	logger := &Func{Name: "logger", Sig: &Signature{Params: []*Var{{Name: "ctx", Type: ctx}}, Result: bl}}
	handle := &Func{Name: "handleCreateUser", Sig: &Signature{Recv: &Var{Name: "ctx", Kind: RecvVar, Type: ctx}, Result: void}}
	greet := &Func{Name: "greet", Sig: &Signature{Recv: &Var{Name: "u", Kind: RecvVar, Type: user}, Result: str}}
	first := firstFunc()
	fu, _ := c.InstantiateFunc(first, []Type{user})
	t1 := NewTypeParam(&TypeName{Name: "A"}, 0)
	t2 := NewTypeParam(&TypeName{Name: "B"}, 1)
	pair := &Func{Name: "pair", Sig: &Signature{TypeParams: []*TypeParam{t1, t2}, Result: void}}
	pr, _ := c.InstantiateFunc(pair, []Type{user, arr(i64)})

	cases := map[*Func]string{
		logger: "tl_f_logger",
		handle: "tl_m_Context__handleCreateUser",
		greet:  "tl_m_User__greet",
		fu:     "tl_f_first__User",
		pr:     "tl_f_pair__User__arr_i64",
	}
	for f, want := range cases {
		if got := FuncCName(f); got != want {
			t.Errorf("FuncCName(%s) = %q, want %q", f.FullName(), got, want)
		}
	}
	if handle.FullName() != "Context.handleCreateUser" || !handle.IsMethod() || logger.IsMethod() {
		t.Error("FullName/IsMethod")
	}
}

// TestFuncCNameInstanceTag: an instance carries its origin's module tag, so
// two modules' same-named generic functions instantiated at the same type
// arguments get distinct C names; the untagged (single-file) origin keeps
// today's name.
func TestFuncCNameInstanceTag(t *testing.T) {
	c := NewInstanceCache()
	mkID := func(tag string) *Func {
		tp := NewTypeParam(&TypeName{Name: "T"}, 0)
		return &Func{Name: "id", Tag: tag, Sig: &Signature{
			TypeParams: []*TypeParam{tp},
			Params:     []*Var{{Name: "x", Type: tp}},
			Result:     tp,
		}}
	}
	untagged, a, main := mkID(""), mkID("a_ts_1abc"), mkID("main_ts_2xyz")
	cases := []struct {
		origin *Func
		want   string
	}{
		{untagged, "tl_f_id__i64"},
		{a, "tl_f_a_ts_1abc__id__i64"},
		{main, "tl_f_main_ts_2xyz__id__i64"},
	}
	for _, tc := range cases {
		inst, err := c.InstantiateFunc(tc.origin, []Type{i64})
		if err != nil {
			t.Fatal(err)
		}
		if inst.Tag != tc.origin.Tag {
			t.Errorf("instance Tag = %q, want origin's %q", inst.Tag, tc.origin.Tag)
		}
		if got := FuncCName(inst); got != tc.want {
			t.Errorf("FuncCName(%s, tag %q) = %q, want %q", inst.FullName(), tc.origin.Tag, got, tc.want)
		}
	}
}

func TestCheckDeclName(t *testing.T) {
	ok := []struct {
		name string
		kind DeclKind
	}{
		{"x", DeclValue}, {"snake_case", DeclValue}, {"route_dispatcher", DeclValue}, {"User", DeclType},
		{"T", DeclType}, {"array_like", DeclType}, {"a__b", DeclField}, {"i32", DeclValue},
		// Reserved generated C shapes apply only to DeclType: these names stay
		// legal as values (they mangle to g_.../tl_f_..., not the reserved
		// struct names).
		{"globals", DeclValue}, {"_init_globals", DeclValue}, {"tl_x", DeclValue},
		// Boundary: not the reserved spelling / the prefix is "tl_" not "tl".
		{"globalsX", DeclType}, {"tlx", DeclType},
	}
	for _, c := range ok {
		if err := CheckDeclName(c.name, c.kind); err != nil {
			t.Errorf("CheckDeclName(%q, %d) = %v", c.name, c.kind, err)
		}
	}
	bad := []struct {
		name string
		kind DeclKind
	}{
		{"__x", DeclValue}, {"__x", DeclField}, {"a__b", DeclValue}, {"Page__User", DeclType},
		{"i32", DeclType}, {"str", DeclType}, {"bool", DeclType}, {"Error", DeclType}, {"string", DeclType},
		{"arr_User", DeclType}, {"opt_x", DeclType},
		// Reserved generated C shapes (DESIGN.md §3.2).
		{"globals", DeclType}, {"_init_globals", DeclType}, {"tl_x", DeclType}, {"tl_f_logger", DeclType},
	}
	for _, c := range bad {
		if err := CheckDeclName(c.name, c.kind); err == nil {
			t.Errorf("CheckDeclName(%q, %d) accepted", c.name, c.kind)
		}
	}
}
