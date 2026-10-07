package codegen

import (
	"fmt"
	"go/constant"
	"math"
	"math/big"
	"strconv"
	"strings"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// Constants (codegen design §9, plan D13-D15).

// maxStringLiteral is the longest string constant written as a C string
// literal. 4095 bytes is the ISO C minimum every compiler must support; a
// longer literal is -Woverlength-strings under -pedantic on gcc and clang
// (probes o4, o5), so it becomes a char array instead (longString).
const maxStringLiteral = 4095

// longStringPerLine is the number of values per line of a long string's
// char array initializer.
const longStringPerLine = 16

// longString is an overlength string constant: a file-scope
// "static const char __tl_s<N>[len+1]" array in region D, registered by the
// one use that needs it (codegen design §5.1, §9.3).
type longString struct {
	// name is __tl_s<N>.
	name string
	// data is the bytes, without the terminating NUL.
	data string
}

// intLit returns the C spelling of an integer constant of type int32 or
// int64 (codegen design §9.1, plan D14): a plain decimal without suffix or
// parentheses, except INT64_MIN and INT32_MIN, whose negative literals C
// cannot write (probes f1, f2). A value outside the type's range, an
// unrepresentable literal leaf the checker accepted such as
// 9223372036854775808, is first wrapped two's-complement to the type width,
// which is what the run-time arithmetic produces (plan D13).
func intLit(v constant.Value, kind types.BasicKind) string {
	x := wrapInt(v, kind)
	switch {
	case kind == types.Int64 && x == math.MinInt64:
		return "INT64_MIN"
	case kind == types.Int32 && x == math.MinInt32:
		return "INT32_MIN"
	}
	return strconv.FormatInt(x, 10)
}

// wrapInt returns the integer constant v reduced modulo 2^64 (int64) or 2^32
// (int32) into the signed range of the type.
func wrapInt(v constant.Value, kind types.BasicKind) int64 {
	var bits uint
	switch kind {
	case types.Int32:
		bits = 32
	case types.Int64:
		bits = 64
	default:
		panic(fmt.Sprintf("intLit: %v is not an integer type", types.Typ[kind]))
	}
	if v == nil {
		panic("intLit: no constant value")
	}
	iv := constant.ToInt(v)
	if iv.Kind() != constant.Int {
		panic(fmt.Sprintf("intLit: constant %s is not an integer", v.ExactString()))
	}
	n := new(big.Int)
	switch x := constant.Val(iv).(type) {
	case int64:
		n.SetInt64(x)
	case *big.Int:
		n.Set(x)
	}
	mod := new(big.Int).Lsh(big.NewInt(1), bits)
	n.Mod(n, mod) // Euclidean: 0 <= n < mod
	if n.Cmp(new(big.Int).Rsh(mod, 1)) >= 0 {
		n.Sub(n, mod)
	}
	return n.Int64()
}

// floatLit returns the C spelling of a float64 constant (codegen design
// §9.3, probe f3): the shortest decimal that round-trips, from
// strconv.FormatFloat(f, 'g', -1, 64), with ".0" appended when it has
// neither a '.' nor an exponent, so that it reads as a double (2 becomes
// 2.0). The checker never records a non-finite constant that may be folded.
func floatLit(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		panic(fmt.Sprintf("floatLit: %v is not finite", f))
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// cQuote returns s as a C string literal, quotes included (codegen design
// §9.3, probes o1-o3). Printable ASCII is copied, except that '"' and '\'
// are backslash-escaped and a '?' that follows a '?' in s is written in
// octal, so the output never contains "??" and cannot form a trigraph (gcc
// and clang translate trigraphs in ISO C mode, -Werror=trigraphs, while tcc
// does not: probe o2). Every byte below 0x20 or from 0x7F up is a
// three-digit octal escape; hexadecimal escapes are never used because they
// swallow following hex digits (probe o3). Since an octal escape has exactly
// three digits, a following digit is never absorbed.
func cQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || c >= 0x7F || (c == '?' && i > 0 && s[i-1] == '?'):
			b.WriteByte('\\')
			b.WriteByte('0' + c>>6)
			b.WriteByte('0' + c>>3&7)
			b.WriteByte('0' + c&7)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// strExpr returns a C expression of type tlang_string for the string
// constant s (codegen design §9.3, plan D15): TLANG_STR("...") up to
// maxStringLiteral bytes; beyond that a long string array is registered and
// used as ((tlang_string){ __tl_s<N>, len }). The long form is
// self-parenthesized so it stays one macro argument (plan P7). Call it only
// for a use that is emitted: the array exists only together with its use.
func (g *generator) strExpr(s string) string {
	if len(s) <= maxStringLiteral {
		return "TLANG_STR(" + cQuote(s) + ")"
	}
	return "((tlang_string){ " + g.longString(s) + ", " + strconv.Itoa(len(s)) + " })"
}

// strInit returns a tlang_string initializer for a static table entry:
// TLANG_STR_INIT("...") or, beyond maxStringLiteral bytes,
// { __tl_s<N>, len }.
func (g *generator) strInit(s string) string {
	if len(s) <= maxStringLiteral {
		return "TLANG_STR_INIT(" + cQuote(s) + ")"
	}
	return "{ " + g.longString(s) + ", " + strconv.Itoa(len(s)) + " }"
}

// sqlExpr returns the tlang_string expression of the constant SQL argument
// at pos. An embedded NUL byte is rejected first (§12 item 11), before the
// long-literal form is chosen, so a long SQL constant cannot carry a NUL
// past the check (codegen design §9.3).
func (g *generator) sqlExpr(pos token.Position, sql string) string {
	if strings.IndexByte(sql, 0) >= 0 {
		g.fail(errSQLNul(pos))
	}
	return g.strExpr(sql)
}

// longString registers s as the next long string and returns its array name.
func (g *generator) longString(s string) string {
	name := longStrName(len(g.longStrs) + 1)
	g.longStrs = append(g.longStrs, longString{name: name, data: s})
	return name
}

// writeLongStrings writes the array of every registered long string, in
// __tl_s<N> order, each as a separate entity (codegen design §9.3; plan D3
// puts them first in region D).
func (g *generator) writeLongStrings(w *cwriter) {
	for _, ls := range g.longStrs {
		w.gap()
		writeLongString(w, ls)
	}
}

// writeLongString writes "static const char __tl_s<N>[len+1] = { ... };".
// The bound is explicit (probe o6, TOOLCHAIN-NIT-2) and the values end with
// the terminating 0. Bytes below 0x80 are decimal; bytes from 0x80 up are
// octal character constants, because a decimal such as 200 overflows a
// signed char (-Werror=overflow on gcc, plan P6).
func writeLongString(w *cwriter, ls longString) {
	vals := make([]string, 0, len(ls.data)+1)
	for i := 0; i < len(ls.data); i++ {
		if c := ls.data[i]; c < 0x80 {
			vals = append(vals, strconv.Itoa(int(c)))
		} else {
			vals = append(vals, fmt.Sprintf(`'\%03o'`, c))
		}
	}
	vals = append(vals, "0")
	w.open(fmt.Sprintf("static const char %s[%d] =", ls.name, len(ls.data)+1))
	for i := 0; i < len(vals); i += longStringPerLine {
		j := min(i+longStringPerLine, len(vals))
		line := strings.Join(vals[i:j], ", ")
		if j < len(vals) {
			line += ","
		}
		w.line(line)
	}
	w.close(";")
}

// constValue returns the C spelling of the constant v of the concrete basic
// type t. The value is read by the type, not by v.Kind() (plan D13): a
// float64 constant written as an integer literal is a double literal (2.0).
// The caller has checked with foldable that v is representable.
func (g *generator) constValue(v constant.Value, t types.Type) string {
	b, _ := t.(*types.Basic)
	if b == nil || v == nil {
		panic(fmt.Sprintf("constValue: no constant of type %v", t))
	}
	switch b.Kind {
	case types.Int32, types.Int64:
		return intLit(v, b.Kind)
	case types.Float64:
		f, _ := constant.Float64Val(v)
		return floatLit(f)
	case types.Bool:
		if constant.BoolVal(v) {
			return "true"
		}
		return "false"
	case types.String:
		return g.strExpr(constant.StringVal(v))
	}
	panic(fmt.Sprintf("constValue: type %v has no constants", t))
}

// representable reports whether the constant v may be emitted as a value of
// type t: t is int32, int64, float64, bool or string and v is in its range
// (types.InInt32Range, types.InInt64Range; a finite float64; the matching
// kind). The checker can record values that are not (codegen design §9.2):
// 9223372036854775807 + 1 as int64, int32(5) * int32(1000000000) as int32,
// 1e308 * 10.0 as float64.
func representable(v constant.Value, t types.Type) bool {
	b, ok := t.(*types.Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case types.Int32, types.Int64, types.Float64, types.Bool, types.String:
		_, ok := types.Representable(v, t)
		return ok
	}
	return false
}

// foldable reports whether the constant expression e may be emitted as its
// recorded value (codegen design §9.2, §9.4, plan D13): e has a constant
// value, and that value and the value of every constant sub-expression are
// representable in their own types. Otherwise the operations are emitted, so
// the wrapping run-time helpers compute what the program means; folding an
// overflowed intermediate would disagree with them for / and % and
// comparisons ((9223372036854775807 + 1) / 2). Identifiers are never folded:
// they carry no value, not even const bindings.
//
// A minus applied directly to a number literal counts as one literal: only
// its own value must be representable. So -9223372036854775808 folds to
// INT64_MIN although the literal 9223372036854775808 alone does not fit.
func foldable(info *types.Info, e ast.Expression) bool {
	if tv, ok := info.Types[e]; !ok || !tv.IsConstant() {
		return false
	}
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		if !ok {
			return false
		}
		x, isExpr := n.(ast.Expression)
		if !isExpr {
			return true // a type argument, or the end of a node's children
		}
		if tv, has := info.Types[x]; has && tv.IsConstant() && !representable(tv.Value, tv.Type) {
			ok = false
			return false
		}
		if p, isPrefix := x.(*ast.PrefixExpression); isPrefix && p.Operator == token.MINUS && isNumberLiteral(p.Right) {
			return false
		}
		return true
	})
	return ok
}

// isNumberLiteral reports whether e is an integer or float literal.
func isNumberLiteral(e ast.Expression) bool {
	switch e.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral:
		return true
	}
	return false
}
