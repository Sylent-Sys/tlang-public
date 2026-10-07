package types

import (
	"errors"
	"go/constant"
	gotoken "go/token"
	"math"
	"testing"

	"tlang/token"
)

func TestRanges(t *testing.T) {
	cases := []struct {
		v          constant.Value
		in32, in64 bool
	}{
		{constant.MakeInt64(0), true, true},
		{constant.MakeInt64(math.MaxInt32), true, true},
		{constant.MakeInt64(math.MaxInt32 + 1), false, true},
		{constant.MakeInt64(math.MinInt32), true, true},
		{constant.MakeInt64(math.MinInt32 - 1), false, true},
		{constant.MakeInt64(math.MaxInt64), false, true},
		{constant.MakeUint64(1 << 63), false, false},
		{FoldUnary(token.MINUS, constant.MakeUint64(1<<63)), false, true}, // -9223372036854775808
		{constant.MakeFloat64(1), false, false},                           // floats never become ints
		{constant.MakeBool(true), false, false},
		{nil, false, false},
	}
	for _, c := range cases {
		if InInt32Range(c.v) != c.in32 || InInt64Range(c.v) != c.in64 {
			t.Errorf("%v: InInt32Range=%v InInt64Range=%v, want %v %v", c.v, InInt32Range(c.v), InInt64Range(c.v), c.in32, c.in64)
		}
	}
}

func TestRepresentable(t *testing.T) {
	huge := constant.MakeFromLiteral("1e400", gotoken.FLOAT, 0)
	cases := []struct {
		v    constant.Value
		t    Type
		ok   bool
		want string // ExactString of the converted value when ok
	}{
		{constant.MakeInt64(5), i32, true, "5"},
		{constant.MakeInt64(5), opt(i32), true, "5"},
		{constant.MakeInt64(1 << 40), i32, false, ""},
		{constant.MakeInt64(1 << 40), i64, true, "1099511627776"},
		{constant.MakeInt64(3), f64, true, "3"},
		{constant.MakeFloat64(2.5), f64, true, "5/2"},
		{constant.MakeFloat64(2.5), i64, false, ""},
		{huge, f64, false, ""},
		{huge, ufl, true, huge.ExactString()},
		{constant.MakeInt64(7), uint, true, "7"},
		{constant.MakeFloat64(7), uint, false, ""},
		{constant.MakeBool(true), bl, true, "true"},
		{constant.MakeString("x"), str, true, `"x"`},
		{constant.MakeString("x"), i32, false, ""},
		{constant.MakeInt64(1), str, false, ""},
		{constant.MakeInt64(1), iface("User"), false, ""},
		{constant.MakeInt64(1), bad, true, "1"},
	}
	for _, c := range cases {
		got, ok := Representable(c.v, c.t)
		if ok != c.ok {
			t.Errorf("Representable(%v, %v) ok = %v, want %v", c.v, c.t, ok, c.ok)
			continue
		}
		if ok && got.ExactString() != c.want {
			t.Errorf("Representable(%v, %v) = %s, want %s", c.v, c.t, got.ExactString(), c.want)
		}
	}
}

func TestFold(t *testing.T) {
	i := constant.MakeInt64
	f := constant.MakeFloat64
	cases := []struct {
		op   token.TokenType
		x, y constant.Value
		want string
		err  error
	}{
		{token.PLUS, i(2), i(3), "5", nil},
		{token.MINUS, i(2), i(3), "-1", nil},
		{token.ASTERISK, i(4), f(0.5), "2", nil},
		{token.SLASH, i(7), i(2), "3", nil},
		{token.SLASH, i(-7), i(2), "-3", nil},
		{token.MOD, i(-7), i(2), "-1", nil},
		{token.MOD, i(7), i(-2), "1", nil},
		{token.SLASH, f(7), i(2), "7/2", nil},
		{token.SLASH, i(1), i(0), "", ErrDivisionByZero},
		{token.MOD, i(1), i(0), "", ErrDivisionByZero},
		{token.SLASH, f(1), f(0), "", ErrDivisionByZero},
		{token.MOD, f(1.5), i(1), "", nil},
		{token.LT, i(1), f(2.5), "true", nil},
		{token.EQ, i(2), f(2), "true", nil},
		{token.GT_EQ, i(1), i(2), "false", nil},
		{token.AND, constant.MakeBool(true), constant.MakeBool(false), "false", nil},
		{token.OR, constant.MakeBool(true), constant.MakeBool(false), "true", nil},
		{token.NOT_EQ, constant.MakeBool(true), constant.MakeBool(false), "true", nil},
		{token.PLUS, constant.MakeString("a"), constant.MakeString("b"), "", nil},
		{token.AND, i(1), i(2), "", nil},
		{token.NULLISH, i(1), i(2), "", nil},
	}
	for _, c := range cases {
		got, err := FoldBinary(c.op, c.x, c.y)
		if !errors.Is(err, c.err) {
			t.Errorf("FoldBinary(%s, %v, %v) err = %v, want %v", c.op, c.x, c.y, err, c.err)
			continue
		}
		gotStr := ""
		if got != nil {
			gotStr = got.ExactString()
		}
		if gotStr != c.want {
			t.Errorf("FoldBinary(%s, %v, %v) = %q, want %q", c.op, c.x, c.y, gotStr, c.want)
		}
	}
	if v, _ := FoldBinary(token.SLASH, i(7), i(2)); v.Kind() != constant.Int {
		t.Error("integer division must stay an integer constant")
	}
	if FoldUnary(token.MINUS, i(5)).ExactString() != "-5" || FoldUnary(token.BANG, constant.MakeBool(true)).ExactString() != "false" ||
		FoldUnary(token.BANG, i(1)) != nil || FoldUnary(token.MINUS, nil) != nil {
		t.Error("FoldUnary")
	}
}
