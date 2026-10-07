package codegen

import (
	"go/constant"
	gotoken "go/token"
	"math"
	"strings"
	"testing"

	"tlang/ast"
	"tlang/types"
)

func TestIntLit(t *testing.T) {
	big := func(s string) constant.Value { return constant.MakeFromLiteral(s, gotoken.INT, 0) }
	cases := []struct {
		v    constant.Value
		kind types.BasicKind
		want string
	}{
		{constant.MakeInt64(0), types.Int64, "0"},
		{constant.MakeInt64(1), types.Int64, "1"},
		{constant.MakeInt64(-1), types.Int64, "-1"},
		{constant.MakeInt64(-9), types.Int64, "-9"},
		{constant.MakeInt64(math.MaxInt64), types.Int64, "9223372036854775807"},
		{constant.MakeInt64(math.MinInt64), types.Int64, "INT64_MIN"},
		{constant.MakeInt64(math.MaxInt32), types.Int32, "2147483647"},
		{constant.MakeInt64(math.MinInt32), types.Int32, "INT32_MIN"},
		{constant.MakeInt64(-5), types.Int32, "-5"},
		{constant.MakeInt64(math.MinInt32), types.Int64, "-2147483648"},
		// Out-of-range leaves wrap two's-complement to the type width.
		{constant.MakeUint64(1 << 63), types.Int64, "INT64_MIN"},
		{constant.MakeUint64(math.MaxUint64), types.Int64, "-1"},
		{big("18446744073709551616"), types.Int64, "0"},
		{big("18446744073709551621"), types.Int64, "5"},
		{big("-9223372036854775809"), types.Int64, "9223372036854775807"},
		{constant.MakeInt64(5000000000), types.Int32, "705032704"},
		{constant.MakeInt64(math.MaxInt32 + 1), types.Int32, "INT32_MIN"},
		{constant.MakeInt64(math.MinInt32 - 1), types.Int32, "2147483647"},
		{big("18446744073709551623"), types.Int32, "7"},
		// An integral value of float kind is still an integer.
		{constant.MakeFloat64(3), types.Int64, "3"},
	}
	for _, c := range cases {
		if got := intLit(c.v, c.kind); got != c.want {
			t.Errorf("intLit(%s, %v) = %q, want %q", c.v.ExactString(), types.Typ[c.kind], got, c.want)
		}
	}
	mustPanic(t, "is not an integer type", func() { intLit(constant.MakeInt64(1), types.Float64) })
	mustPanic(t, "is not an integer", func() { intLit(constant.MakeFloat64(2.5), types.Int64) })
	mustPanic(t, "no constant value", func() { intLit(nil, types.Int64) })
}

func TestFloatLit(t *testing.T) {
	cases := []struct {
		f    float64
		want string
	}{
		{2, "2.0"},
		{0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{0.5, "0.5"},
		{-2.5, "-2.5"},
		{100000, "100000.0"},
		{1e6, "1e+06"},
		{123456789, "1.23456789e+08"},
		{1e21, "1e+21"},
		{5e-324, "5e-324"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
		{0.30000000000000004, "0.30000000000000004"},
		{0.1, "0.1"},
	}
	for _, c := range cases {
		if got := floatLit(c.f); got != c.want {
			t.Errorf("floatLit(%v) = %q, want %q", c.f, got, c.want)
		}
	}
	mustPanic(t, "is not finite", func() { floatLit(math.Inf(1)) })
	mustPanic(t, "is not finite", func() { floatLit(math.NaN()) })
}

func TestCQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"Hello, world!", `"Hello, world!"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"tab\there", `"tab\011here"`},
		{"line\n", `"line\012"`},
		{"cr\r", `"cr\015"`},
		{"\x00", `"\000"`},
		{"\x001", `"\0001"`}, // an octal escape never absorbs a following digit
		{"\x1f \x7e", "\"\\037 ~\""},
		{"\x7f", `"\177"`},
		{"\x80\xff", `"\200\377"`},
		{"é", `"\303\251"`},
		{"??=", `"?\077="`},
		{"???", `"?\077\077"`},
		{"what??!", `"what?\077!"`},
		{"a?b?", `"a?b?"`},
		{"?", `"?"`},
		{"100%d", `"100%d"`},
		{"it's", `"it's"`},
	}
	for _, c := range cases {
		if got := cQuote(c.in); got != c.want {
			t.Errorf("cQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
	// Exhaustive property: every byte value survives, the output never holds
	// "??", a raw control or non-ASCII byte, or a hexadecimal escape.
	var all strings.Builder
	for i := range 256 {
		all.WriteByte(byte(i))
		all.WriteString("??")
	}
	q := cQuote(all.String())
	inner := q[1 : len(q)-1]
	if strings.Contains(inner, "??") || strings.Contains(inner, `\x`) {
		t.Fatalf("cQuote output contains a trigraph start or a hex escape: %s", q)
	}
	for i := 0; i < len(q); i++ {
		if q[i] < 0x20 || q[i] >= 0x7F {
			t.Fatalf("cQuote output contains raw byte %#x", q[i])
		}
	}
}

func TestStrExprBoundary(t *testing.T) {
	g := testGenerator()
	short := strings.Repeat("a", maxStringLiteral)
	if got := g.strExpr(short); got != `TLANG_STR("`+short+`")` {
		t.Fatalf("4095 bytes: got %.40q...", got)
	}
	if got := g.strInit(short); got != `TLANG_STR_INIT("`+short+`")` {
		t.Fatalf("4095 bytes init: got %.40q...", got)
	}
	if len(g.longStrs) != 0 {
		t.Fatalf("a 4095-byte string must stay a literal")
	}

	data := []byte(strings.Repeat("a", maxStringLiteral+1))
	data[0], data[1], data[2] = 0xC8, 0x00, '"'
	long := string(data)
	if got := g.strExpr(long); got != "((tlang_string){ __tl_s1, 4096 })" {
		t.Fatalf("4096 bytes: got %q", got)
	}
	if got := g.strInit(long + "b"); got != "{ __tl_s2, 4097 }" {
		t.Fatalf("4097 bytes init: got %q", got)
	}
	if len(g.longStrs) != 2 || g.longStrs[0].name != "__tl_s1" || g.longStrs[0].data != long ||
		g.longStrs[1].name != "__tl_s2" || g.longStrs[1].data != long+"b" {
		t.Fatalf("long strings not registered in order: %d", len(g.longStrs))
	}

	var w cwriter
	g.writeLongStrings(&w)
	lines := strings.Split(strings.TrimSuffix(string(w.bytes()), "\n"), "\n")
	// __tl_s1: 4097 values (4096 bytes and the terminating 0), 16 per line,
	// so 256 full lines and a last line holding the 0.
	if lines[0] != "static const char __tl_s1[4097] = {" {
		t.Fatalf("first line %q", lines[0])
	}
	if want := `    '\310', 0, 34, 97, 97, 97, 97, 97, 97, 97, 97, 97, 97, 97, 97, 97,`; lines[1] != want {
		t.Fatalf("first values %q, want %q", lines[1], want)
	}
	if lines[257] != "    0" || lines[258] != "};" || lines[259] != "" {
		t.Fatalf("end of __tl_s1: %q %q %q", lines[257], lines[258], lines[259])
	}
	// __tl_s2: 4098 values, the last line holds "98, 0".
	if lines[260] != "static const char __tl_s2[4098] = {" || lines[len(lines)-2] != "    98, 0" || lines[len(lines)-1] != "};" {
		t.Fatalf("__tl_s2 framing: %q ... %q %q", lines[260], lines[len(lines)-2], lines[len(lines)-1])
	}
}

func TestWriteLongString(t *testing.T) {
	var w cwriter
	writeLongString(&w, longString{name: "__tl_s7", data: "hi\xc3\xa9\x00\xff!"})
	want := "static const char __tl_s7[8] = {\n    104, 105, '\\303', '\\251', 0, '\\377', 33, 0\n};\n"
	if got := string(w.bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	w = cwriter{}
	writeLongString(&w, longString{name: "__tl_s1", data: strings.Repeat("A", 16)})
	want = "static const char __tl_s1[17] = {\n" +
		"    65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65,\n" +
		"    0\n" +
		"};\n"
	if got := string(w.bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSQLExpr(t *testing.T) {
	g := testGenerator()
	if got := g.sqlExpr(pos(1, 1), "SELECT 1"); got != `TLANG_STR("SELECT 1")` {
		t.Fatalf("got %q", got)
	}
	// A NUL byte is rejected before the form is chosen, short or long, and
	// no long string array is registered for a rejected constant.
	for _, sql := range []string{"UPDATE t SET a = 1\x00 WHERE b = 2", strings.Repeat("x", 5000) + "\x00"} {
		err := catch(g, func() { g.sqlExpr(pos(2, 24), sql) })
		want := "test.tl:2:24: unsupported input: SQL string contains a NUL byte"
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	}
	if len(g.longStrs) != 0 {
		t.Fatalf("a rejected SQL constant registered a long string")
	}
	if got := g.sqlExpr(pos(1, 1), strings.Repeat("y", 5000)); got != "((tlang_string){ __tl_s1, 5000 })" {
		t.Fatalf("long SQL: got %q", got)
	}
}

func TestConstValue(t *testing.T) {
	g := testGenerator()
	cases := []struct {
		v    constant.Value
		t    types.Type
		want string
	}{
		{constant.MakeInt64(5), types.Typ[types.Int64], "5"},
		{constant.MakeInt64(-7), types.Typ[types.Int32], "-7"},
		{constant.MakeUint64(1 << 63), types.Typ[types.Int64], "INT64_MIN"},
		{constant.MakeFloat64(2), types.Typ[types.Float64], "2.0"},
		{constant.MakeInt64(2), types.Typ[types.Float64], "2.0"}, // read by type
		{constant.MakeFloat64(0.25), types.Typ[types.Float64], "0.25"},
		{constant.MakeBool(true), types.Typ[types.Bool], "true"},
		{constant.MakeBool(false), types.Typ[types.Bool], "false"},
		{constant.MakeString("a\"b"), types.Typ[types.String], `TLANG_STR("a\"b")`},
	}
	for _, c := range cases {
		if got := g.constValue(c.v, c.t); got != c.want {
			t.Errorf("constValue(%s, %v) = %q, want %q", c.v.ExactString(), c.t, got, c.want)
		}
	}
	for _, f := range []func(){
		func() { g.constValue(constant.MakeInt64(1), types.NewArray(types.Typ[types.Int64])) },
		func() { g.constValue(nil, types.Typ[types.Int64]) },
		func() { g.constValue(constant.MakeInt64(1), types.Typ[types.Void]) },
		func() { g.constValue(constant.MakeString("x"), types.Typ[types.Int64]) },
	} {
		if err := catch(g, f); err == nil || !strings.HasPrefix(err.msg, prefixInternal) {
			t.Errorf("got %v, want an internal error", err)
		}
	}
}

func TestRepresentable(t *testing.T) {
	huge := constant.BinaryOp(constant.MakeFloat64(1e308), gotoken.MUL, constant.MakeFloat64(10))
	cases := []struct {
		v    constant.Value
		t    types.Type
		want bool
	}{
		{constant.MakeInt64(1 << 40), types.Typ[types.Int32], false},
		{constant.MakeInt64(1 << 40), types.Typ[types.Int64], true},
		{constant.MakeInt64(math.MinInt32), types.Typ[types.Int32], true},
		{constant.MakeUint64(1 << 63), types.Typ[types.Int64], false},
		{constant.MakeInt64(math.MinInt64), types.Typ[types.Int64], true},
		{constant.MakeFloat64(1.5), types.Typ[types.Float64], true},
		{huge, types.Typ[types.Float64], false},
		{constant.MakeBool(true), types.Typ[types.Bool], true},
		{constant.MakeString("x"), types.Typ[types.String], true},
		{constant.MakeInt64(1), types.Typ[types.String], false},
		{constant.MakeInt64(1), types.Typ[types.UntypedInt], false},
		{constant.MakeInt64(1), types.Typ[types.Invalid], false},
		{constant.MakeInt64(1), types.NewArray(types.Typ[types.Int64]), false},
		{nil, types.Typ[types.Int64], false},
	}
	for _, c := range cases {
		if got := representable(c.v, c.t); got != c.want {
			t.Errorf("representable(%v, %v) = %v, want %v", c.v, c.t, got, c.want)
		}
	}
}

// TestFoldable runs the fold decision of plan D13 on checked initializers.
func TestFoldable(t *testing.T) {
	prog, info := checkSrc(t, `
fn main(): void {
    let a = 1 + 2;
    let b = 9223372036854775807 + 1;
    let c = -9223372036854775808;
    let d = (9223372036854775807 + 1) / 2;
    let e = int32(5) * int32(1000000000);
    let f = int32(5);
    let g = 1e308 * 10.0;
    let h = "a" + "b";
    let i = 9223372036854775807 + 1 > 0;
    let j = a;
    let k = int32(-2147483648);
    let l = -2.5;
    let m = float64(3);
    let n = int64(2.5);
    let o = 0xFFFFFFFFFFFFFFFF;
    let p = !true;
    let q = 2.0 * 3.0;
    let r = int32(3000000000);
    let s = 7 / 2 + 7 % 2;
    let t = -(1 + 2);
    let u = -(9223372036854775807 + 1);
}
`)
	want := map[string]struct {
		fold  bool
		value string // constValue of the initializer when it folds
	}{
		"a": {true, "3"},
		"b": {false, ""},
		"c": {true, "INT64_MIN"},
		"d": {false, ""}, // in-range result over an overflowed intermediate
		"e": {false, ""},
		"f": {true, "5"},
		"g": {false, ""},
		"h": {true, `TLANG_STR("ab")`},
		"i": {false, ""},
		"j": {false, ""}, // identifiers never fold
		"k": {true, "INT32_MIN"},
		"l": {true, "-2.5"},
		"m": {true, "3.0"},
		"n": {false, ""}, // not folded by the checker: no value
		"o": {false, ""}, // an out-of-range leaf is wrapped by intLit, not folded
		"p": {true, "false"},
		"q": {true, "6.0"},
		"r": {false, ""},
		"s": {true, "4"},
		"t": {true, "-3"}, // a minus on a non-literal checks its operand
		"u": {false, ""},  // in-range value, out-of-range operand
	}
	g := testGenerator()
	seen := 0
	ast.Inspect(prog, func(n ast.Node) bool {
		let, ok := n.(*ast.LetStatement)
		if !ok {
			return true
		}
		w, ok := want[let.Name.Name]
		if !ok {
			t.Fatalf("unexpected let %s", let.Name.Name)
		}
		seen++
		if got := foldable(info, let.Value); got != w.fold {
			t.Errorf("foldable(%s) = %v, want %v", let.Value, got, w.fold)
			return false
		}
		if w.fold {
			tv := info.Types[let.Value]
			if got := g.constValue(tv.Value, tv.Type); got != w.value {
				t.Errorf("constValue(%s) = %q, want %q", let.Value, got, w.value)
			}
		}
		return false
	})
	if seen != len(want) {
		t.Fatalf("checked %d initializers, want %d", seen, len(want))
	}
}
