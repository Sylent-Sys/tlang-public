package types

import (
	"strings"
	"testing"
)

func TestInfer(t *testing.T) {
	c := NewInstanceCache()
	user, admin := iface("User"), iface("Admin")
	page := iface("Page", "T")
	T := NewTypeParam(&TypeName{Name: "T"}, 0)
	U := NewTypeParam(&TypeName{Name: "U"}, 1)
	tps := []*TypeParam{T, U}
	one := []*TypeParam{T}

	ok := []struct {
		name     string
		tparams  []*TypeParam
		explicit []Type
		params   []Type
		args     []Type
		want     string
	}{
		{"first(xs)", one, nil, []Type{arr(T)}, []Type{arr(user)}, "User"},
		{"id(5)", one, nil, []Type{T}, []Type{uint}, "int64"},
		{"id(2.5)", one, nil, []Type{T}, []Type{ufl}, "float64"},
		{"typed wins over untyped", one, nil, []Type{T, T}, []Type{uint, i32}, "int32"},
		{"optional param, plain arg", one, nil, []Type{opt(T)}, []Type{user}, "User"},
		{"optional param, optional arg", one, nil, []Type{opt(T)}, []Type{opt(str)}, "string"},
		{"plain param, optional arg", one, nil, []Type{T}, []Type{opt(i64)}, "int64 | null"},
		{"null plus typed", one, nil, []Type{opt(T), T}, []Type{null, user}, "User"},
		{"through instance", one, nil, []Type{mustNamed(c, page, T)}, []Type{mustNamed(c, page, arr(user))}, "User[]"},
		{"two params", tps, nil, []Type{T, arr(U)}, []Type{user, arr(opt(bl))}, "User, bool | null"},
		{"partial explicit", tps, []Type{admin}, []Type{U}, []Type{str}, "Admin, string"},
		{"full explicit ignores args", one, []Type{user}, []Type{arr(T)}, []Type{arr(admin)}, "User"},
		{"no-arg explicit", one, []Type{user}, nil, nil, "User"},
		{"invalid arg skipped", one, nil, []Type{T, T}, []Type{bad, i32}, "int32"},
	}
	for _, c := range ok {
		got, err := Infer(c.tparams, c.explicit, c.params, c.args)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := typeListString(got); s != c.want {
			t.Errorf("%s: inferred %s, want %s", c.name, s, c.want)
		}
	}

	bad := []struct {
		name     string
		tparams  []*TypeParam
		explicit []Type
		params   []Type
		args     []Type
		want     string
	}{
		{"conflict", one, nil, []Type{T, T}, []Type{i32, i64}, "inferred as both int32 and int64"},
		{"only null", one, nil, []Type{opt(T)}, []Type{null}, "cannot infer T"},
		{"no args", one, nil, nil, nil, "cannot infer T"},
		{"unused param", tps, nil, []Type{T}, []Type{user}, "cannot infer U"},
		{"too many explicit", one, []Type{user, admin}, nil, nil, "got 2 type arguments, want 1"},
		{"partial explicit conflict", tps, []Type{admin}, []Type{T, U}, []Type{user, str}, "inferred as both Admin and User"},
	}
	for _, c := range bad {
		_, err := Infer(c.tparams, c.explicit, c.params, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestIsDBScalar(t *testing.T) {
	for _, ty := range []Type{i32, i64, f64, bl, str, opt(i32), opt(str), opt(bl)} {
		if !IsDBScalar(ty) {
			t.Errorf("IsDBScalar(%v) = false", ty)
		}
	}
	for _, ty := range []Type{void, errT, ctx, uint, null, iface("User"), arr(i32), opt(arr(i32))} {
		if IsDBScalar(ty) {
			t.Errorf("IsDBScalar(%v) = true", ty)
		}
	}
}

func TestJSONEncodable(t *testing.T) {
	c := NewInstanceCache()
	addr := withFields(iface("Address"), fieldVar("city", str), fieldVar("zip", opt(i32)))
	user := withFields(iface("User"), fieldVar("id", i64), fieldVar("addr", addr), fieldVar("tags", arr(str)),
		fieldVar("score", opt(f64)), fieldVar("ok", bl))
	// Recursive through an optional field.
	node := iface("Node")
	withFields(node, fieldVar("v", i32), fieldVar("next", opt(node)))
	withErr := withFields(iface("Bad"), fieldVar("e", errT))
	page := iface("Page", "T")
	withFields(page, fieldVar("items", arr(page.TypeParams[0])), fieldVar("total", i64))
	incomplete := iface("Later")

	for _, ty := range []Type{i32, f64, str, opt(bl), user, arr(user), opt(user), node, mustNamed(c, page, user), arr(arr(opt(str)))} {
		if !JSONEncodable(ty) {
			t.Errorf("JSONEncodable(%v) = false", ty)
		}
	}
	for _, ty := range []Type{void, errT, ctx, tx, uint, null, withErr, arr(withErr), page, mustNamed(c, page, page.TypeParams[0]),
		mustNamed(c, page, withErr), incomplete} {
		if JSONEncodable(ty) {
			t.Errorf("JSONEncodable(%v) = true", ty)
		}
	}
}
