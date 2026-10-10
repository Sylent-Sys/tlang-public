package codegen

import (
	"go/constant"
	"strings"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// Expression lowering (codegen design §6, §8, §9, plan D7, D8, D13, D14, D25,
// D28). value is the entry point: it lowers an expression to a cval, applying
// the narrowed-payload read and the ConvToOptional widening the checker
// recorded for it; expr lowers the bare value without the widening, for the
// few callers that handle the conversion themselves (a return, an argument,
// an initializer). needsStmts predicts, without side effects, whether
// lowering an expression emits statements into the current block before its
// value, which drives the strict left-to-right spilling of the operand rule.

// needsStmts predicts, without emitting anything, whether lowering e would
// write statements into the current block before producing its value (plan
// D7). It drives the operand rule: an earlier unstable operand is spilled
// when a later operand needs statements. A folded constant never needs
// statements. The prediction must match what expr actually does; a mismatch
// is a codegen bug, which the may-fail materialization asserts implicitly
// (an unpredicted spill would still be correct, but an unspilled unstable
// operand would read a changed value).
func (fc *funcCtx) needsStmts(e ast.Expression) bool {
	if foldable(fc.g.info, e) {
		return false
	}
	switch e := e.(type) {
	case *ast.Identifier, *ast.IntegerLiteral, *ast.FloatLiteral,
		*ast.StringLiteral, *ast.BooleanLiteral, *ast.NullLiteral:
		return false
	case *ast.PrefixExpression:
		return fc.needsStmts(e.Right)
	case *ast.InfixExpression:
		switch e.Operator {
		case token.AND, token.OR:
			// Short-circuit forms emit statements only when the right side
			// does (the if-form), but the left side can too.
			return fc.needsStmts(e.Left) || fc.needsStmts(e.Right)
		case token.SLASH, token.MOD:
			if types.IsInteger(fc.typ(e.Left)) && !fc.nonzeroConst(e.Right) {
				return true
			}
		case token.NULLISH:
			if !types.IsOptional(fc.typ(e.Left)) || types.IsOptional(fc.typ(e)) {
				// x never null, or the result stays optional: ?? is x, y dead.
				return fc.needsStmts(e.Left)
			}
			// The if-form only runs y on the null path, but it still emits
			// statements when y does; x into a temporary is a statement only
			// when x is unstable, decided at lowering.
			return fc.needsStmts(e.Left) || fc.needsStmts(e.Right)
		}
		return fc.needsStmts(e.Left) || fc.needsStmts(e.Right)
	case *ast.TernaryExpression:
		if fc.needsStmts(e.Consequence) || fc.needsStmts(e.Alternative) {
			return true
		}
		return fc.needsStmts(e.Condition)
	case *ast.NonNullExpression:
		if types.IsOptional(fc.typ(e)) {
			// Result stays optional: x! is a no-op (plan D28).
			return fc.needsStmts(e.Left)
		}
		if types.IsOptional(fc.typ(e.Left)) {
			return true // the unwrap is materialized and checked
		}
		return fc.needsStmts(e.Left)
	case *ast.CallExpression:
		return fc.callNeedsStmts(e)
	case *ast.MemberExpression:
		return fc.needsStmts(e.Object)
	case *ast.IndexExpression:
		return true // a checked read is always materialized
	case *ast.AssignmentExpression:
		return fc.assignNeedsStmts(e)
	case *ast.NewExpression:
		return fc.newNeedsStmts(e)
	case *ast.ArrayLiteral:
		// An array literal is always materialized into a temporary.
		return true
	case *ast.ObjectLiteral:
		// An object literal is new T() + field stores: always statements.
		return true
	}
	fc.g.fail(internalErr(e.Pos(), "needsStmts of unknown expression"))
	return true
}

// newNeedsStmts reports whether lowering a new expression emits statements
// (plan D7): new T[]() and new Error(...) are inline (pure); new T() emits
// statements only when it has a constructed field (plan D23); an argument of
// new Error that needs statements also makes it need statements.
func (fc *funcCtx) newNeedsStmts(e *ast.NewExpression) bool {
	if e.IsArray {
		return false
	}
	t := fc.typ(e)
	if types.IsBasic(t, types.Error) {
		for _, a := range e.Args {
			if fc.needsStmts(a) {
				return true
			}
		}
		return false
	}
	if named, ok := t.(*types.Named); ok {
		return fc.hasConstructedField(named)
	}
	return true
}

// callNeedsStmts reports whether lowering the call e emits statements: every
// user call always does (it is materialized and checked); a builtin does when
// it may fail or when any of its operands does; a conversion follows its
// argument.
func (fc *funcCtx) callNeedsStmts(e *ast.CallExpression) bool {
	c := fc.g.info.Calls[e]
	if c == nil {
		return true
	}
	switch c.Kind {
	case types.CallFunc, types.CallMethod:
		return true
	case types.CallConversion:
		if len(e.Arguments) == 1 {
			return fc.needsStmts(e.Arguments[0])
		}
		return true
	case types.CallBuiltin:
		return fc.builtinNeedsStmts(e, c)
	}
	return true
}

// value lowers e to its value with the narrowed-payload read of plan D28 and
// the ConvToOptional widening Info.Conversions recorded for e (codegen design
// §8.6): the conversion is applied after the value is computed.
func (fc *funcCtx) value(e ast.Expression) cval {
	v := fc.expr(e)
	v = fc.applyNarrow(e, v)
	return fc.applyConv(e, v)
}

// storeValue lowers e for a store whose destination is a global location when
// globalDest is set (plan D17): an array literal, or a new expression nested
// inside it, allocates in the global heap. Everything else lowers exactly as
// value does. The widening conversion still applies.
func (fc *funcCtx) storeValue(e ast.Expression, globalDest bool) cval {
	if !globalDest {
		return fc.value(e)
	}
	var v cval
	switch e := e.(type) {
	case *ast.ArrayLiteral:
		v = fc.arrayLiteral(e, true)
	case *ast.NewExpression:
		v = fc.newExpr(e, true)
	default:
		return fc.value(e)
	}
	v = fc.applyNarrow(e, v)
	return fc.applyConv(e, v)
}

// expr lowers e to its bare value: the narrowed-payload read is applied, but
// not the ConvToOptional widening (the caller applies it, or there is none).
func (fc *funcCtx) expr(e ast.Expression) cval {
	fc.g.cur = e.Pos()
	// A folded constant short-circuits every form (codegen design §9.4).
	if foldable(fc.g.info, e) {
		if v, ok := fc.foldConst(e); ok {
			return v
		}
	}
	switch e := e.(type) {
	case *ast.Identifier:
		return fc.ident(e)
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.BooleanLiteral:
		return fc.literal(e)
	case *ast.NullLiteral:
		// A bare null reaches here only through a context that lowers it
		// itself (an equality operand, a return, a widening); its stand-alone
		// value is the null form of its wanted optional type.
		return fc.nullValue(fc.typ(e))
	case *ast.PrefixExpression:
		return fc.prefix(e)
	case *ast.InfixExpression:
		return fc.infix(e)
	case *ast.CallExpression:
		return fc.call(e)
	case *ast.TernaryExpression:
		return fc.ternary(e)
	case *ast.NonNullExpression:
		return fc.nonNull(e)
	case *ast.MemberExpression:
		return fc.member(e)
	case *ast.IndexExpression:
		return fc.index(e)
	case *ast.NewExpression:
		return fc.newExpr(e, false)
	case *ast.ArrayLiteral:
		return fc.arrayLiteral(e, false)
	case *ast.ObjectLiteral:
		return fc.objectLiteral(e)
	}
	fc.g.fail(notImplemented(e.Pos(), "expression"))
	return cval{}
}

// typ returns the concrete type of e, rejecting a nil type (§12 item 2) and
// an Invalid type (§12 item 1); the checker should have caught both, so this
// is a safety net.
func (fc *funcCtx) typ(e ast.Expression) types.Type {
	tv, ok := fc.g.info.Types[e]
	if !ok || tv.Type == nil {
		fc.g.fail(errExprNoType(e.Pos()))
	}
	if types.IsInvalid(tv.Type) {
		fc.g.fail(errBad(e.Pos(), invalidType))
	}
	return fc.concrete(tv.Type)
}

// applyNarrow applies the payload-extraction guard of plan D28/§8.4: a
// narrowed read of a local/param/receiver identifier whose declared type is
// optional but whose narrowed concrete type is non-optional yields the
// payload of the optional value, not the optional itself. value and index
// callers use it; the already-extracted forms (x!, ??) do their own.
func (fc *funcCtx) applyNarrow(e ast.Expression, v cval) cval {
	id, ok := e.(*ast.Identifier)
	if !ok || !fc.g.info.Narrowed[id] {
		return v
	}
	// The identifier is narrowed: its *Var type is optional, the recorded
	// type here is the non-optional payload.
	obj := fc.g.info.Uses[id]
	vr, ok := obj.(*types.Var)
	if !ok {
		return v
	}
	return fc.payload(v, fc.concrete(vr.Type))
}

// payload extracts the payload of an optional value v of concrete optional
// type opt: ".v" for a primitive optional, the value unchanged for a string
// or reference optional (the null and present representations share the C
// type).
func (fc *funcCtx) payload(v cval, opt types.Type) cval {
	if types.IsPrimitiveOptional(opt) {
		return opVal(v.operand()+".v", v.stable).atomize()
	}
	return v
}

// atomize marks a cval as a primary expression (used after appending a member
// access like ".v", which binds tighter than any operator).
func (v cval) atomize() cval { v.prec = precAtom; return v }

// applyConv applies the ConvToOptional widening Info.Conversions recorded for
// e (codegen design §8.6, plan D28): wrap a non-optional value into its
// optional C representation. An identical From/To (after Concrete) is a
// no-op.
func (fc *funcCtx) applyConv(e ast.Expression, v cval) cval {
	conv, ok := fc.g.info.Conversions[e]
	if !ok || conv.Kind != types.ConvToOptional {
		return v
	}
	from := fc.concrete(conv.From)
	to := fc.concrete(conv.To)
	if types.Identical(from, to) {
		return v
	}
	return fc.widen(v, from)
}

// widen wraps a non-optional value v, of concrete type from, into the C
// representation of the optional type from|null (codegen design §8.6, §8.4):
// TLANG_SOME for a primitive, tlang_str_some for a string, the pointer
// unchanged for an interface or array.
func (fc *funcCtx) widen(v cval, from types.Type) cval {
	switch {
	case types.IsString(from):
		return atom("tlang_str_some(" + v.code + ")")
	case isPrimitive(from):
		return atom("TLANG_SOME(" + fc.g.mangle(from) + ", " + macroArg(v) + ")")
	}
	// Interface, array: the optional reuses the pointer C type.
	return v
}

// nullValue returns the null form of the optional type want (plan D28):
// TLANG_NONE for a primitive optional, TLANG_STR_NULL for a string optional,
// NULL for a reference optional.
func (fc *funcCtx) nullValue(want types.Type) cval {
	switch {
	case types.IsPrimitiveOptional(want):
		return atom("TLANG_NONE(" + fc.g.mangle(types.NonOptional(want)) + ")")
	case types.IsString(types.NonOptional(want)):
		return atom("TLANG_STR_NULL")
	}
	return atom("NULL")
}

// ident lowers an identifier value use (codegen design §8.1): a local,
// parameter or receiver reads its C name (l_x), a global reads
// __fib->globals->g_x. A global read is unstable (a later call can change
// it); a local read is stable.
func (fc *funcCtx) ident(id *ast.Identifier) cval {
	obj := fc.g.info.Uses[id]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(id.Pos(), "identifier %s is not a variable", id.Name))
	}
	if vr.Kind == types.GlobalVar {
		return opVal("__fib->globals->"+globalName(vr), false).atomize()
	}
	name, ok := fc.locals.name(vr)
	if !ok {
		fc.g.fail(internalErr(id.Pos(), "variable %s was not named", id.Name))
	}
	fc.markRead(vr)
	return atom(name)
}

// literal lowers an integer, float, string or bool literal from its recorded
// constant value (codegen design §9.1, §9.3). Non-folded literals reach here
// only when foldConst declined (a non-representable leaf), which constValue
// handles via wrapping.
func (fc *funcCtx) literal(e ast.Expression) cval {
	tv := fc.g.info.Types[e]
	if tv.Value == nil {
		fc.g.fail(internalErr(e.Pos(), "literal has no constant value"))
	}
	return atom(fc.g.constValue(tv.Value, fc.concrete(tv.Type)))
}

// foldConst lowers e as its recorded constant value when it is foldable
// (codegen design §9.2, §9.4, plan D13), returning false when e carries no
// constant value (an identifier bound to a const is never folded). A string
// constant may register a long string.
func (fc *funcCtx) foldConst(e ast.Expression) (cval, bool) {
	tv, ok := fc.g.info.Types[e]
	if !ok || !tv.IsConstant() || tv.Value == nil {
		return cval{}, false
	}
	t := fc.concrete(tv.Type)
	if !representable(tv.Value, t) {
		// The whole value is representable (foldable checked every
		// sub-value), so this cannot happen; guard anyway.
		return cval{}, false
	}
	return atom(fc.g.constValue(tv.Value, t)), true
}

// prefix lowers a unary ! or - (codegen design §8.2, §8.7). A folded form is
// handled by expr; this reaches a non-constant operand.
func (fc *funcCtx) prefix(e *ast.PrefixExpression) cval {
	switch e.Operator {
	case token.BANG:
		v := fc.value(e.Right)
		return opVal("!"+v.operand(), v.stable)
	case token.MINUS:
		return fc.negate(e)
	}
	fc.g.fail(internalErr(e.Pos(), "prefix operator %s", e.Operator))
	return cval{}
}

// negate lowers unary minus: float64 is the C "-" operator, integers use the
// wrapping run-time helper (codegen design §8.2).
func (fc *funcCtx) negate(e *ast.PrefixExpression) cval {
	t := fc.typ(e)
	v := fc.value(e.Right)
	if types.IsFloat(t) {
		return opVal("-"+v.operand(), v.stable)
	}
	fn := "tlang_neg_i64"
	if types.IsBasic(t, types.Int32) {
		fn = "tlang_neg_i32"
	}
	return opVal(fn+"("+v.code+")", v.stable).atomize()
}

// macroArg parenthesizes a cval that contains a top-level comma, so it stays
// one argument of a function-like macro (plan D26). The long-string form is
// already self-parenthesized.
func macroArg(v cval) string {
	if hasTopLevelComma(v.code) {
		return "(" + v.code + ")"
	}
	return v.code
}

// hasTopLevelComma reports whether s contains a comma outside any parentheses
// or braces, which would split a macro argument.
func hasTopLevelComma(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ',':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// isPrimitive reports whether t is one of the four primitive scalar types
// that have a tlang_opt_* optional (int32, int64, float64, bool).
func isPrimitive(t types.Type) bool {
	b, ok := t.(*types.Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case types.Int32, types.Int64, types.Float64, types.Bool:
		return true
	}
	return false
}

// spill evaluates v into a fresh temporary of concrete type t and returns a
// stable reference to it (codegen design §6.5, plan D7): "<CType> __tN = <v>;"
// A value already stable needs no spill, but the operand rule spills unstable
// earlier operands when a later one needs statements.
func (fc *funcCtx) spill(v cval, t types.Type) cval {
	name := fc.temp()
	fc.blk.linef("%s %s = %s;", fc.g.ctype(t), name, v.code)
	return atom(name)
}

// intHelper returns the wrapping run-time helper name for a binary integer
// operator of width kind (add/sub/mul; div and mod take the fiber).
func intHelper(base string, kind types.BasicKind) string {
	if kind == types.Int32 {
		return base + "_i32"
	}
	return base + "_i64"
}

// infix lowers a binary operator (codegen design §8.2-§8.6). It dispatches on
// the operator and the operand type: arithmetic uses wrapping helpers for
// integers and C operators for floats; comparisons and equality follow §8.4,
// §8.5; && and || short-circuit (if-form when the right side needs
// statements); string + concatenates.
func (fc *funcCtx) infix(e *ast.InfixExpression) cval {
	switch e.Operator {
	case token.AND, token.OR:
		return fc.logical(e)
	case token.EQ, token.NOT_EQ:
		return fc.equality(e)
	case token.NULLISH:
		return fc.nullish(e)
	}
	// Operand type: the left operand's concrete type (both sides agree for
	// arithmetic and ordering; equality is handled above).
	lt := fc.typ(e.Left)
	switch e.Operator {
	case token.PLUS:
		if types.IsString(lt) {
			return fc.strConcat(e)
		}
	}
	l, r := fc.binOperands(e.Left, e.Right)
	switch e.Operator {
	case token.PLUS, token.MINUS, token.ASTERISK:
		return fc.arith(e, lt, l, r)
	case token.SLASH, token.MOD:
		return fc.divMod(e, lt, l, r)
	case token.LT, token.GT, token.LT_EQ, token.GT_EQ:
		stable := l.stable && r.stable
		return opVal(l.operand()+" "+string(e.Operator)+" "+r.operand(), stable)
	}
	fc.g.fail(internalErr(e.Pos(), "infix operator %s", e.Operator))
	return cval{}
}

// binOperands lowers the two operands of a binary operator left-to-right,
// spilling the left operand when it is unstable and the right one needs
// statements (plan D7 operand rule).
func (fc *funcCtx) binOperands(left, right ast.Expression) (cval, cval) {
	l := fc.value(left)
	if fc.needsStmts(right) && !l.stable {
		l = fc.spill(l, fc.typ(left))
	}
	r := fc.value(right)
	return l, r
}

// arith lowers +, - and * (codegen design §8.2): float uses the C operator,
// integers the wrapping run-time helper.
func (fc *funcCtx) arith(e *ast.InfixExpression, t types.Type, l, r cval) cval {
	stable := l.stable && r.stable
	if types.IsFloat(t) {
		return opVal(l.operand()+" "+string(e.Operator)+" "+r.operand(), stable)
	}
	kind := types.Int64
	if types.IsBasic(t, types.Int32) {
		kind = types.Int32
	}
	var base string
	switch e.Operator {
	case token.PLUS:
		base = "tlang_add"
	case token.MINUS:
		base = "tlang_sub"
	case token.ASTERISK:
		base = "tlang_mul"
	}
	return opVal(intHelper(base, kind)+"("+l.code+", "+r.code+")", stable).atomize()
}

// divMod lowers / and % (codegen design §8.2, §8.3, plan D8): float / is the C
// operator, float % the run-time tlang_mod_f64 (needs -lm); integer / and %
// use the fiber-checked helper, which is pure (no check) only when the
// divisor is a nonzero constant, otherwise a may-fail temporary.
func (fc *funcCtx) divMod(e *ast.InfixExpression, t types.Type, l, r cval) cval {
	if types.IsFloat(t) {
		if e.Operator == token.SLASH {
			return opVal(l.operand()+" / "+r.operand(), l.stable && r.stable)
		}
		return atom("tlang_mod_f64(" + l.code + ", " + r.code + ")")
	}
	kind := types.Int64
	if types.IsBasic(t, types.Int32) {
		kind = types.Int32
	}
	base := "tlang_div"
	if e.Operator == token.MOD {
		base = "tlang_mod"
	}
	code := intHelper(base, kind) + "(__fib, " + l.code + ", " + r.code + ")"
	if fc.nonzeroConst(e.Right) {
		// Pure: the divisor cannot be zero, so the helper never fails.
		return opVal(code, l.stable && r.stable).atomize()
	}
	// May fail: materialize and check (plan D8).
	v := fc.spill(opVal(code, false), t)
	fc.check()
	return v
}

// nonzeroConst reports whether e is a constant whose value is a nonzero
// integer (plan D8): then integer / and % cannot fail and need no check.
func (fc *funcCtx) nonzeroConst(e ast.Expression) bool {
	tv, ok := fc.g.info.Types[e]
	if !ok || !tv.IsConstant() || tv.Value == nil {
		return false
	}
	iv := constant.ToInt(tv.Value)
	if iv.Kind() != constant.Int {
		return false
	}
	return constant.Sign(iv) != 0
}

// logical lowers && and || (codegen design §6.5, plan D7). When the right
// operand needs no statements, it is a plain C "a && b"; otherwise the
// if-form evaluates the right side only on the short-circuit path and stores
// the result in a bool temporary.
func (fc *funcCtx) logical(e *ast.InfixExpression) cval {
	l := fc.value(e.Left)
	if !fc.needsStmts(e.Right) {
		r := fc.value(e.Right)
		return opVal(l.operand()+" "+string(e.Operator)+" "+r.operand(), l.stable && r.stable)
	}
	name := fc.temp()
	if e.Operator == token.AND {
		fc.blk.linef("bool %s = false;", name)
		fc.blk.open("if (" + l.code + ")")
	} else {
		fc.blk.linef("bool %s = true;", name)
		fc.blk.open("if (!" + l.operand() + ")")
	}
	r := fc.value(e.Right)
	fc.blk.linef("%s = %s;", name, r.code)
	fc.blk.close("")
	return atom(name)
}

// equality lowers == and != (codegen design §8.4, §8.5, plan D28, D25):
// numbers and bools use the C operator (with the §8.5 self-compare cast),
// strings tlang_str_eq, references pointer compare, and a null operand uses
// the presence test of the live type.
func (fc *funcCtx) equality(e *ast.InfixExpression) cval {
	eq := e.Operator == token.EQ
	if other, t, ok := fc.nullCompare(e); ok {
		return fc.nullTest(other, t, eq)
	}
	// Self-compare (§8.5): the two operands are the same variable or the same
	// field path over the same variable; cast the left to its C type so the
	// tautology is intended, not a warning.
	lt := fc.typ(e.Left)
	l, r := fc.binOperands(e.Left, e.Right)
	if fc.sameExpr(e.Left, e.Right) {
		l = atom("(" + fc.g.ctype(lt) + ")" + l.operand())
	}
	stable := l.stable && r.stable
	op := "=="
	if !eq {
		op = "!="
	}
	if types.IsString(lt) {
		code := "tlang_str_eq(" + l.code + ", " + r.code + ")"
		if !eq {
			return opVal("!"+parenIfComposed(code), stable)
		}
		return opVal(code, stable).atomize()
	}
	return opVal(l.operand()+" "+op+" "+r.operand(), stable)
}

// nullCompare recognizes an == / != with a null literal operand. It returns
// the non-null operand's lowered value, its concrete type, and ok (codegen
// design §8.4). The null literal itself carries type UntypedNull, so the live
// type comes from the other operand.
func (fc *funcCtx) nullCompare(e *ast.InfixExpression) (other cval, t types.Type, ok bool) {
	switch {
	case isNullLiteral(e.Right):
		t = fc.typ(e.Left)
		return fc.value(e.Left), t, true
	case isNullLiteral(e.Left):
		t = fc.typ(e.Right)
		return fc.value(e.Right), t, true
	}
	return cval{}, nil, false
}

// nullTest lowers "<x> == null" / "!= null" by the C representation of the
// optional type t (codegen design §8.4): a primitive optional tests .has, a
// string optional tests .data, a reference optional tests the pointer.
func (fc *funcCtx) nullTest(v cval, t types.Type, eq bool) cval {
	switch {
	case types.IsPrimitiveOptional(t):
		field := v.operand() + ".has"
		if eq {
			return opVal("!"+field, v.stable)
		}
		return opVal(field, v.stable).atomize()
	case types.IsString(types.NonOptional(t)):
		op := "!="
		if eq {
			op = "=="
		}
		return opVal(v.operand()+".data "+op+" NULL", v.stable)
	}
	op := "!="
	if eq {
		op = "=="
	}
	return opVal(v.operand()+" "+op+" NULL", v.stable)
}

// parenIfComposed wraps code in parentheses when it is not already a single
// parenthesized or call form; used for the "!" of a string inequality.
func parenIfComposed(code string) string {
	return "(" + code + ")"
}

// strConcat lowers string + as nested tlang_str_concat calls (codegen design
// §8.3). A folded all-constant concatenation never reaches here.
func (fc *funcCtx) strConcat(e *ast.InfixExpression) cval {
	l, r := fc.binOperands(e.Left, e.Right)
	return atom("tlang_str_concat(__fib, " + l.code + ", " + r.code + ")")
}

// sameExpr reports whether a and b are the same variable identifier or the
// same field-selection chain over the same variable (codegen design §8.5):
// the §8.5 self-compare and self-assign forms.
func (fc *funcCtx) sameExpr(a, b ast.Expression) bool {
	switch a := a.(type) {
	case *ast.Identifier:
		bid, ok := b.(*ast.Identifier)
		return ok && fc.g.info.Uses[a] == fc.g.info.Uses[bid] && fc.g.info.Uses[a] != nil
	case *ast.MemberExpression:
		bm, ok := b.(*ast.MemberExpression)
		if !ok || a.Property.Name != bm.Property.Name {
			return false
		}
		sa, sb := fc.g.info.Selections[a], fc.g.info.Selections[bm]
		if sa == nil || sb == nil || sa.Kind != types.SelField || sb.Kind != types.SelField {
			return false
		}
		return fc.sameExpr(a.Object, bm.Object)
	}
	return false
}

// isNullLiteral reports whether e is the null literal.
func isNullLiteral(e ast.Expression) bool {
	_, ok := e.(*ast.NullLiteral)
	return ok
}

// nonNull lowers x! (codegen design §8.7, plan D28). When the concrete
// operand is still optional (a generic T|null whose T is itself optional),
// the operand is returned unchanged, no unwrap and no check. Otherwise the
// matching unwrap helper runs and the result is checked.
func (fc *funcCtx) nonNull(e *ast.NonNullExpression) cval {
	v := fc.value(e.Left)
	// When the result type is still optional, the operand stays optional
	// (the generic T := U|null case): x! is a no-op, no unwrap, no check
	// (plan D28, codegen design §8.7).
	if types.IsOptional(fc.typ(e)) {
		return v
	}
	ot := fc.typ(e.Left)
	if !types.IsOptional(ot) {
		// Concrete operand not optional: nothing to unwrap (plan D28).
		return v
	}
	elem := types.NonOptional(ot)
	var code string
	switch {
	case types.IsString(elem):
		code = "tlang_unwrap_str(__fib, " + v.code + ")"
	case isPrimitive(elem):
		code = "tlang_unwrap_" + fc.g.mangle(elem) + "(__fib, " + v.code + ")"
	default:
		// Interface or array pointer.
		code = "(" + fc.g.ctype(elem) + ")tlang_unwrap_ptr(__fib, " + v.code + ")"
	}
	res := fc.spill(opVal(code, false), elem)
	fc.check()
	return res
}

// nullish lowers x ?? y (codegen design §8.7, plan D7, D28). x is evaluated
// into a stable value; when its concrete type is still optional (the generic
// T := U|null case) the whole expression is x and y is dead. Otherwise the
// present test and payload come from the optional representation; when y
// needs no statements it is the C conditional operator, else the if-form
// stores x's payload or y into a temporary. y carries no conversion and is
// typed at the non-optional result type.
func (fc *funcCtx) nullish(e *ast.InfixExpression) cval {
	ot := fc.typ(e.Left)
	x := fc.value(e.Left)
	if !types.IsOptional(ot) {
		// Concrete left not optional: x is never null, y is dead.
		return x
	}
	if types.IsOptional(fc.typ(e)) {
		// The result stays optional (the generic T := U|null case): the
		// payload is x unchanged, so the whole ?? is a no-op yielding x and
		// y is dead (plan D28, codegen design §8.7).
		return x
	}
	if !x.stable {
		x = fc.spill(x, ot)
	}
	elem := types.NonOptional(ot)
	present := fc.presentTest(x, ot)
	pay := fc.payload(x, ot)
	if !fc.needsStmts(e.Right) {
		y := fc.value(e.Right)
		return opVal(present+" ? "+pay.operand()+" : "+y.operand(), false)
	}
	name := fc.temp()
	fc.blk.linef("%s %s;", fc.g.ctype(elem), name)
	fc.blk.open("if (" + present + ")")
	fc.blk.linef("%s = %s;", name, pay.code)
	fc.blk.reopen("} else {")
	y := fc.value(e.Right)
	fc.blk.linef("%s = %s;", name, y.code)
	fc.blk.close("")
	return atom(name)
}

// presentTest returns the C expression that is true when the optional value v
// of concrete optional type opt is present (codegen design §8.7): ".has" for
// a primitive optional, ".data != NULL" for a string optional, "!= NULL" for
// a reference optional.
func (fc *funcCtx) presentTest(v cval, opt types.Type) string {
	switch {
	case types.IsPrimitiveOptional(opt):
		return v.operand() + ".has"
	case types.IsString(types.NonOptional(opt)):
		return v.operand() + ".data != NULL"
	}
	return v.operand() + " != NULL"
}

// ternary lowers c ? x : y (codegen design §6.5, plan D7). A void result is
// §12 item 3. When neither branch needs statements it is the C conditional
// operator; otherwise an uninitialized temporary is assigned in each arm of
// an if/else, with each branch carrying its own ConvToOptional widening.
func (fc *funcCtx) ternary(e *ast.TernaryExpression) cval {
	t := fc.typ(e)
	if types.IsVoid(t) {
		fc.g.fail(errVoidValue(e.Pos(), "ternary"))
	}
	cond := fc.value(e.Condition)
	if !fc.needsStmts(e.Consequence) && !fc.needsStmts(e.Alternative) {
		x := fc.value(e.Consequence)
		y := fc.value(e.Alternative)
		stable := cond.stable && x.stable && y.stable
		return opVal(cond.operand()+" ? "+x.operand()+" : "+y.operand(), stable)
	}
	name := fc.temp()
	fc.blk.linef("%s %s;", fc.g.ctype(t), name)
	fc.blk.open("if (" + cond.code + ")")
	x := fc.value(e.Consequence)
	fc.blk.linef("%s = %s;", name, x.code)
	fc.blk.reopen("} else {")
	y := fc.value(e.Alternative)
	fc.blk.linef("%s = %s;", name, y.code)
	fc.blk.close("")
	return atom(name)
}

// call lowers a call expression (codegen design §8.8): a user function or
// method call (always materialized and checked, plan D8), a conversion
// (int32/int64/float64, §8.6), or a builtin (dispatched by builtins.go). A
// void result is handled by the statement path; a value call stores its
// result in a temporary so the check never follows a visible store.
func (fc *funcCtx) call(e *ast.CallExpression) cval {
	c := fc.g.info.Calls[e]
	if c == nil {
		fc.g.fail(internalErr(e.Pos(), "call has no call info"))
	}
	switch c.Kind {
	case types.CallFunc, types.CallMethod:
		return fc.userCall(e, c, false)
	case types.CallConversion:
		return fc.conversion(e, c)
	case types.CallBuiltin:
		return fc.callBuiltin(e, c, false)
	}
	fc.g.fail(internalErr(e.Pos(), "unknown call kind"))
	return cval{}
}

// userCall lowers a CallFunc or CallMethod (codegen design §8.8, plan D8).
// The receiver (a method) is the first argument and is lowered first, then
// the arguments left-to-right with the operand rule. The call is always
// materialized into a temporary and checked, unless discard is set, when a
// void result is a bare statement and a non-void result is cast to void (the
// statement path passes discard=true). It returns the temporary (or an empty
// cval when discarded).
func (fc *funcCtx) userCall(e *ast.CallExpression, c *types.Call, discard bool) cval {
	callee := fc.concreteFunc(c.Func, e.Pos())
	name := fc.g.funcName(callee)

	// Build the operand list: receiver first (a method), then the arguments.
	var operandExprs []ast.Expression
	if c.Kind == types.CallMethod && c.Recv != nil {
		operandExprs = append(operandExprs, c.Recv)
	}
	operandExprs = append(operandExprs, e.Arguments...)
	args := fc.lowerOperands(operandExprs)

	var b strings.Builder
	b.WriteString(name)
	b.WriteString("(__fib")
	for _, a := range args {
		b.WriteString(", ")
		b.WriteString(a.code)
	}
	b.WriteByte(')')
	code := b.String()

	result := fc.concrete(callee.Sig.Result)
	if discard {
		if types.IsVoid(result) {
			fc.blk.linef("%s;", code)
		} else {
			fc.blk.linef("(void)%s;", code)
		}
		fc.check()
		return cval{}
	}
	if types.IsVoid(result) {
		// A void call used as a value cannot occur in a well-typed program
		// (void let is §12 item 3, caught earlier); guard anyway.
		fc.g.fail(errVoidValue(e.Pos(), "expression"))
	}
	res := fc.spill(opVal(code, false), result)
	fc.check()
	return res
}

// lowerOperands lowers a left-to-right operand list with the operand rule of
// plan D7: when a later operand needs statements, every earlier unstable
// operand is spilled into a temporary right after it is lowered, so its value
// is read before the later operand's statements run. Operands after the last
// statement-needing one stay inline. Each operand carries its own
// ConvToOptional widening (through value).
func (fc *funcCtx) lowerOperands(exprs []ast.Expression) []cval {
	// Index of the last operand that needs statements.
	last := -1
	for i, e := range exprs {
		if fc.needsStmts(e) {
			last = i
		}
	}
	out := make([]cval, len(exprs))
	for i, e := range exprs {
		v := fc.value(e)
		if i < last && !v.stable {
			v = fc.spill(v, fc.typ(e))
		}
		out[i] = v
	}
	return out
}

// conversion lowers int32(x)/int64(x)/float64(x) (codegen design §8.6). A
// folded conversion of a representable constant never reaches here (expr
// handled it); this is the run-time cast or helper for a non-constant or
// non-representable operand.
func (fc *funcCtx) conversion(e *ast.CallExpression, c *types.Call) cval {
	if len(e.Arguments) != 1 {
		fc.g.fail(internalErr(e.Pos(), "conversion takes one argument"))
	}
	arg := e.Arguments[0]
	from := fc.typ(arg)
	to := fc.typ(e)
	v := fc.value(arg)
	if types.Identical(from, to) {
		return v
	}
	switch c.Builtin {
	case types.BuiltinConvInt32:
		switch {
		case types.IsBasic(from, types.Int64):
			return atom("tlang_i64_to_i32(" + v.code + ")")
		case types.IsFloat(from):
			return atom("tlang_f64_to_i32(" + v.code + ")")
		}
	case types.BuiltinConvInt64:
		switch {
		case types.IsBasic(from, types.Int32):
			return opVal("(int64_t)"+v.operand(), v.stable).atomize()
		case types.IsFloat(from):
			return atom("tlang_f64_to_i64(" + v.code + ")")
		}
	case types.BuiltinConvFloat64:
		if types.IsInteger(from) {
			return opVal("(double)"+v.operand(), v.stable).atomize()
		}
	}
	fc.g.fail(internalErr(e.Pos(), "unsupported conversion %s to %s", from, to))
	return cval{}
}

// member lowers a member read (codegen design §8.8): a struct field read
// "<obj>->f_x". Field-like builtin members (lengths, Error fields, ctx
// fields) and member assignment targets are lowered by later features; this
// is the F3 read path for a plain struct field.
func (fc *funcCtx) member(e *ast.MemberExpression) cval {
	sel := fc.g.info.Selections[e]
	if sel == nil {
		fc.g.fail(internalErr(e.Pos(), "member %s has no selection", e.Property.Name))
	}
	switch sel.Kind {
	case types.SelField:
		obj := fc.value(e.Object)
		return opVal(obj.operand()+"->"+fieldName(sel.Field.Name), obj.stable).atomize()
	case types.SelBuiltin:
		return fc.builtinMember(e, sel)
	case types.SelModuleValue:
		// Qualified access to an imported global ("m.counter"): lower to a
		// direct read of the target global, exactly as a bare reference to
		// the same symbol would. The namespace receiver (e.Object) is not
		// lowered; codegen sees only the target's tagged name.
		return opVal("__fib->globals->"+globalName(sel.Field), false).atomize()
	}
	fc.g.fail(notImplemented(e.Pos(), "member access"))
	return cval{}
}

// builtinMember lowers a field-like builtin member read (codegen design §8.8,
// §8.8a is for method calls): a string length ((int64_t)<s>.len), an array
// length (<xs>->len), an Error field (<recv>.message / <recv>.status) or a
// Context field (method/path/body, and rawQuery mapped to query). The
// receiver is lowered by the general receiver rule (§8.8): a stable value is
// used directly, otherwise it is spilled once.
func (fc *funcCtx) builtinMember(e *ast.MemberExpression, sel *types.Selection) cval {
	switch sel.Builtin {
	case types.BuiltinStringLen:
		recv := fc.memberRecv(e.Object)
		return opVal("(int64_t)"+recv.operand()+".len", recv.stable).atomize()
	case types.BuiltinArrayLen:
		recv := fc.memberRecv(e.Object)
		return opVal(recv.operand()+"->len", recv.stable).atomize()
	case types.BuiltinErrorMessage:
		recv := fc.memberRecv(e.Object)
		return opVal(recv.operand()+".message", recv.stable).atomize()
	case types.BuiltinErrorStatus:
		recv := fc.memberRecv(e.Object)
		return opVal(recv.operand()+".status", recv.stable).atomize()
	case types.BuiltinErrorCategory:
		recv := fc.memberRecv(e.Object)
		return opVal(recv.operand()+".category", recv.stable).atomize()
	case types.BuiltinErrorCode:
		recv := fc.memberRecv(e.Object)
		return opVal(recv.operand()+".code", recv.stable).atomize()
	case types.BuiltinCtxMethod:
		return fc.ctxField(e.Object, "method")
	case types.BuiltinCtxPath:
		return fc.ctxField(e.Object, "path")
	case types.BuiltinCtxBody:
		return fc.ctxField(e.Object, "body")
	case types.BuiltinCtxRawQuery:
		return fc.ctxField(e.Object, "query")
	case types.BuiltinJsonKind:
		recv := fc.memberRecv(e.Object)
		return atom("tlang_json_describe(" + recv.code + ")")
	case types.BuiltinJsonAsBool:
		recv := fc.memberRecv(e.Object)
		return atom("tlang_json_as_bool(" + recv.code + ")")
	case types.BuiltinJsonAsNumber:
		recv := fc.memberRecv(e.Object)
		return atom("tlang_json_as_number(" + recv.code + ")")
	case types.BuiltinJsonAsString:
		recv := fc.memberRecv(e.Object)
		return atom("tlang_json_as_string(" + recv.code + ")")
	}
	fc.g.fail(notImplemented(e.Pos(), "builtin member "+sel.Builtin.String()))
	return cval{}
}

// ctxField lowers a field-like Context member read (codegen design §8.8,
// §10.2): "<lower(ctx)>-><field>", where lower(ctx) is the Context receiver's
// local name (l_ctx in the common case).
func (fc *funcCtx) ctxField(recv ast.Expression, field string) cval {
	r := fc.memberRecv(recv)
	return opVal(r.operand()+"->"+field, r.stable).atomize()
}

// memberRecv lowers the receiver of a member read and spills it to a
// temporary when it is unstable, so the member access reads a stable value
// (codegen design §8.8). A stable receiver (a local, a field path, an Error
// temporary) is used directly.
func (fc *funcCtx) memberRecv(e ast.Expression) cval {
	v := fc.value(e)
	if v.stable {
		return v
	}
	return fc.spill(v, fc.typ(e))
}

// index lowers an array index read (codegen design §8.8, plan D8,
// SEMANTIC-NIT-2): TLANG_SLICE_AT materialized into a temporary and checked.
// The macro evaluates its slice argument twice, so an unstable slice is
// spilled; the operand rule spills the slice when the index needs statements
// (SEMANTIC-NIT-2).
func (fc *funcCtx) index(e *ast.IndexExpression) cval {
	slice, idx := fc.indexParts(e)
	elem := fc.typ(e)
	code := "TLANG_SLICE_AT(__fib, " + slice.code + ", " + macroArg(idx) + ", " + fc.g.ctype(elem) + ")"
	res := fc.spill(opVal(code, false), elem)
	fc.check()
	return res
}

// indexParts lowers the slice and index of an index expression left-to-right
// (plan D7, SEMANTIC-NIT-2): the slice is spilled when it is unstable (the
// TLANG_SLICE_AT macro reads it twice) or when the index needs statements;
// the index is spilled when it is unstable only if a later part needs
// statements, which never happens (the index is last), so it stays inline.
func (fc *funcCtx) indexParts(e *ast.IndexExpression) (slice, idx cval) {
	slice = fc.value(e.Left)
	if !slice.stable || fc.needsStmts(e.Index) {
		slice = fc.spill(slice, fc.typ(e.Left))
	}
	idx = fc.value(e.Index)
	return slice, idx
}

// newExpr lowers a new expression (codegen design §8.9, plan D23). global
// forces the global-heap allocators when the value is stored directly into a
// global location (plan D17): new T[]() / new global T[]() make an empty
// typed slice; new Error(...) / new global Error(...) build a tlang_error by
// value (the Global flag has no effect); new T() allocates a zeroed object
// and constructs its required nested fields inline, depth-first, when any
// exist (then the whole expression is a sequence of statements, returned as a
// stable temporary).
func (fc *funcCtx) newExpr(e *ast.NewExpression, global bool) cval {
	g := global || e.Global
	t := fc.typ(e)
	if e.IsArray {
		if arr, ok := t.(*types.Array); ok {
			return atom(fc.sliceNew(arr.Elem, g))
		}
		fc.g.fail(internalErr(e.Pos(), "new T[]() is not an array type"))
	}
	if types.IsBasic(t, types.Error) {
		return fc.newError(e)
	}
	named, ok := t.(*types.Named)
	if !ok {
		fc.g.fail(internalErr(e.Pos(), "new of non-interface type %v", t))
	}
	return fc.newObject(named, g)
}

// sliceNew returns a fresh empty slice of concrete element type elem,
// allocated in the request arena or, when global, the global heap (codegen
// design §8.9, plan D17).
func (fc *funcCtx) sliceNew(elem types.Type, global bool) string {
	macro := "TLANG_SLICE_NEW"
	if global {
		macro = "TLANG_SLICE_NEW_GLOBAL"
	}
	return macro + "(__fib, " + fc.g.mangle(elem) + ")"
}

// newError lowers new Error(m[, s]) / new global Error(...) (codegen design
// §8.9): tlang_error_make_typed(message, status, category, code), defaults to
// 500/internal/internal. The
// Global flag has no effect: Error is a by-value tlang_error.
func (fc *funcCtx) newError(e *ast.NewExpression) cval {
	msg := atom(`TLANG_STR("")`)
	status := "500"
	category := `TLANG_STR("internal")`
	code := `TLANG_STR("internal")`
	if len(e.Args) >= 1 {
		msg = fc.value(e.Args[0])
	}
	if len(e.Args) >= 2 {
		status = fc.value(e.Args[1]).code
	}
	return opVal("tlang_error_make_typed("+msg.code+", "+status+", "+category+", "+code+")", msg.stable).atomize()
}

// newObject lowers new T() / new global T() (codegen design §8.9, plan D23).
// A concrete type with no constructed field allocates inline (pure and
// stable). Otherwise the object is spilled to a temporary and its required
// nested interface and array fields are constructed inline, depth-first in
// Fields() order, each child completed before being stored into its parent.
func (fc *funcCtx) newObject(n *types.Named, global bool) cval {
	if !fc.hasConstructedField(n) {
		return atom(fc.allocZeroed(n, global)).atomize()
	}
	name := fc.temp()
	fc.blk.linef("%s %s = %s;", fc.g.ctype(n), name, fc.allocZeroed(n, global))
	fc.constructFields(name, n, global)
	return atom(name)
}

// allocZeroed returns the zeroed allocation of a concrete interface type n:
// request arena, or the global heap when global (codegen design §8.9).
func (fc *funcCtx) allocZeroed(n *types.Named, global bool) string {
	fn := "tlang_alloc_zeroed"
	if global {
		fn = "tlang_alloc_global_zeroed"
	}
	return "(" + fc.g.ctype(n) + ")" + fn + "(__fib, sizeof(" + fc.g.structName(n) + "))"
}

// constructFields emits the inline field stores of plan D23/§8.9 into the
// already-allocated object named obj of concrete type n, depth-first in
// Fields() order: a non-optional *Named field is a nested new (its own
// temporary when it has constructed fields of its own), a non-optional array
// field is an empty slice. Optional fields, strings, primitives, Error,
// Context and Transaction stay at their zero value.
func (fc *funcCtx) constructFields(obj string, n *types.Named, global bool) {
	for _, f := range n.Fields() {
		ft := fc.concrete(f.Type)
		if types.IsOptional(ft) {
			continue
		}
		switch inner := ft.(type) {
		case *types.Named:
			child := fc.newObject(inner, global)
			fc.blk.linef("%s->%s = %s;", obj, fieldName(f.Name), child.code)
		case *types.Array:
			fc.blk.linef("%s->%s = %s;", obj, fieldName(f.Name), fc.sliceNew(inner.Elem, global))
		}
	}
}

// hasConstructedField reports whether the concrete interface type n has any
// non-optional *Named or array field, so new T() must emit a statement
// sequence rather than an inline allocation (codegen design §8.9, plan D23).
func (fc *funcCtx) hasConstructedField(n *types.Named) bool {
	for _, f := range n.Fields() {
		ft := fc.concrete(f.Type)
		if types.IsOptional(ft) {
			continue
		}
		switch ft.(type) {
		case *types.Named, *types.Array:
			return true
		}
	}
	return false
}

// arrayLiteral lowers [a, b, ...] (codegen design §8.9, plan D17, D26). global
// selects the global-heap allocator when the literal is stored directly into
// a global location. An empty literal is just a fresh slice; otherwise the
// elements are lowered left-to-right (operand rule), the slice reserves N
// elements, and each is pushed. The slice is returned as a stable temporary.
func (fc *funcCtx) arrayLiteral(e *ast.ArrayLiteral, global bool) cval {
	t := fc.typ(e)
	arr, ok := t.(*types.Array)
	if !ok {
		fc.g.fail(internalErr(e.Pos(), "array literal is not an array type"))
	}
	name := fc.temp()
	fc.blk.linef("%s %s = %s;", fc.g.ctype(arr), name, fc.sliceNew(arr.Elem, global))
	if len(e.Elements) == 0 {
		return atom(name)
	}
	vals := fc.lowerOperands(e.Elements)
	fc.blk.linef("TLANG_SLICE_RESERVE(__fib, %s, %d);", name, len(e.Elements))
	for _, v := range vals {
		fc.blk.linef("TLANG_SLICE_PUSH(__fib, %s, %s);", name, macroArg(v))
	}
	return atom(name)
}

// objectLiteral lowers { k: v, ... } (codegen design §8.9, plan D23): new T()
// into a temporary, then one field store per entry in source order, each
// value with its own ConvToOptional widening. The object literal is always a
// request allocation (never global).
func (fc *funcCtx) objectLiteral(e *ast.ObjectLiteral) cval {
	t := fc.typ(e)
	named, ok := t.(*types.Named)
	if !ok {
		fc.g.fail(internalErr(e.Pos(), "object literal is not an interface type"))
	}
	name := fc.temp()
	fc.blk.linef("%s %s = %s;", fc.g.ctype(named), name, fc.allocZeroed(named, false))
	fc.constructFields(name, named, false)
	for _, entry := range e.Entries {
		field := fc.objectField(entry)
		v := fc.value(entry.Value)
		fc.blk.linef("%s->%s = %s;", name, fieldName(field.Name), v.code)
	}
	return atom(name)
}

// objectField returns the field *Var an object-literal entry sets, from
// Info.Uses[entry.Key] (codegen design §8.9).
func (fc *funcCtx) objectField(entry *ast.ObjectEntry) *types.Var {
	obj := fc.g.info.Uses[entry.Key]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(entry.Key.Pos(), "object key %s has no field", entry.Key.Name))
	}
	return vr
}

// globalRoot reports whether an assignment or push target expression's object
// chain ends at a global identifier (plan D17): a global identifier, a member
// of a global root, or an index of a global root. An array literal stored
// through such a target is allocated in the global heap.
func (fc *funcCtx) globalRoot(e ast.Expression) bool {
	switch e := e.(type) {
	case *ast.Identifier:
		vr, ok := fc.g.info.Uses[e].(*types.Var)
		return ok && vr.Kind == types.GlobalVar
	case *ast.MemberExpression:
		return fc.globalRoot(e.Object)
	case *ast.IndexExpression:
		return fc.globalRoot(e.Left)
	}
	return false
}
