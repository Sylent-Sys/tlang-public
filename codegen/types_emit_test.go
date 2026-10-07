package codegen

import (
	"strings"
	"testing"

	"tlang/types"
)

// TestZeroValue checks the §5.4 canonical zero value for every row of the
// table plus void (the empty string).
func TestZeroValue(t *testing.T) {
	g := testGenerator()
	cases := []struct {
		t    types.Type
		want string
	}{
		{types.Typ[types.Int32], "0"},
		{types.Typ[types.Int64], "0"},
		{types.Typ[types.Float64], "0.0"},
		{types.Typ[types.Bool], "false"},
		{types.Typ[types.String], "(tlang_string){0}"},
		{types.Typ[types.Error], "(tlang_error){0}"},
		{types.Typ[types.Context], "NULL"},
		{types.Typ[types.Transaction], "NULL"},
		{types.Typ[types.Void], ""},
		{&types.Optional{Elem: types.Typ[types.Int32]}, "(tlang_opt_i32){0}"},
		{&types.Optional{Elem: types.Typ[types.Int64]}, "(tlang_opt_i64){0}"},
		{&types.Optional{Elem: types.Typ[types.Float64]}, "(tlang_opt_f64){0}"},
		{&types.Optional{Elem: types.Typ[types.Bool]}, "(tlang_opt_bool){0}"},
		{&types.Optional{Elem: types.Typ[types.String]}, "(tlang_string){0}"},
		{&types.Array{Elem: types.Typ[types.Int64]}, "NULL"},
		{&types.Optional{Elem: &types.Array{Elem: types.Typ[types.Int64]}}, "NULL"},
	}
	for _, c := range cases {
		if got := g.zeroValue(c.t); got != c.want {
			t.Errorf("zeroValue(%v) = %q, want %q", c.t, got, c.want)
		}
	}

	// An interface pointer and its optional both zero to NULL.
	user := testNamed("User", pos(1, 1), testVar("id"))
	if got := g.zeroValue(user); got != "NULL" {
		t.Errorf("zeroValue(interface) = %q, want NULL", got)
	}
	if got := g.zeroValue(&types.Optional{Elem: user}); got != "NULL" {
		t.Errorf("zeroValue(interface|null) = %q, want NULL", got)
	}
}

// TestCollectAndEmitTypes checks that the fixed point discovers an instance
// reached only through a field (plan D4/P3), orders element arrays before
// arrays of them in region A, and emits the empty-interface placeholder.
func TestCollectAndEmitTypes(t *testing.T) {
	out := mustEmit(t, `interface Empty {}

interface Item {
    id: int64;
}

interface Box<T> {
    value: T;
    items: T[];
}

interface Holder {
    b: Box<Item>;
    grid: int64[][];
}

fn main(): void {
}
`)
	// The instance Box<Item>, reached only through Holder.b, is collected and
	// gets a struct body and a forward typedef.
	for _, want := range []string{
		"typedef struct tl_Box__Item tl_Box__Item;",
		"struct tl_Box__Item {",
		"tl_Item* f_value;",
		"tlang_slice_Item* f_items;",
		"struct tl_Empty {",
		"char __empty;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("region A missing %q in:\n%s", want, out)
		}
	}
	// Element arrays come before arrays of them: tlang_slice_i64 before the
	// slice of it. int64[] is predefined (no define); int64[][] is not.
	if !strings.Contains(out, "TLANG_SLICE_DEFINE(arr_i64, tlang_slice_i64*)") {
		t.Errorf("missing the non-predefined slice define in:\n%s", out)
	}
	if strings.Contains(out, "TLANG_SLICE_DEFINE(i64,") {
		t.Errorf("a predefined slice must not get a define:\n%s", out)
	}
	// The define has no trailing semicolon.
	if strings.Contains(out, "tlang_slice_i64*);") {
		t.Errorf("slice define must not end with a semicolon:\n%s", out)
	}
}

// TestVoidFieldRejected checks §12 item 3 for a void field, before any C.
func TestVoidFieldRejected(t *testing.T) {
	g := testGenerator()
	n := testNamed("X", pos(2, 11), &types.Var{Name: "v", Kind: types.FieldVar, Type: types.Typ[types.Void], Pos: pos(2, 5)})
	err := catch(g, func() { g.checkFieldType(n, n.Fields()[0]) })
	if err == nil || !strings.Contains(err.Error(), "void value in field X.v") {
		t.Fatalf("void field: got %v", err)
	}
}
