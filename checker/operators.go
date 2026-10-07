package checker

import (
	"go/constant"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// operators.go holds the operator, composite-literal, new and assignment
// typing of pass 3 (DESIGN.md §2.3-§2.7). It is part of the body
// typechecker; it is split from typecheck.go only to keep one concern per
// screenful. All of it drives the types predicates (IsNumeric, CanCompare,
// CanOrder, AssignableTo) and folders (FoldUnary, FoldBinary) rather than
// re-deriving any rule.

// exprPrefix types a prefix expression: -x requires a numeric operand, !x a
// bool operand (E-TYPE otherwise). A constant operand is folded with
// FoldUnary.
func (c *checker) exprPrefix(sc *scope, e *ast.PrefixExpression, facts factSet) types.TypeAndValue {
	tv := c.expr(sc, e.Right, facts, nil)
	if types.IsInvalid(tv.Type) {
		return c.invalid(e)
	}
	switch e.Operator {
	case token.MINUS:
		if !types.IsNumeric(tv.Type) {
			c.errorf(e.OpPos, "E-TYPE", "operator - requires a number, got %s", tv.Type)
			return c.invalid(e)
		}
		return c.record(e, tv.Type, types.FoldUnary(e.Operator, tv.Value))
	case token.BANG:
		if !types.IsBool(tv.Type) {
			c.errorf(e.OpPos, "E-TYPE", "operator ! requires bool, got %s", tv.Type)
			return c.invalid(e)
		}
		return c.record(e, types.Typ[types.Bool], types.FoldUnary(e.Operator, tv.Value))
	}
	return c.invalid(e)
}

// exprInfix dispatches a binary operator by class and records the result.
func (c *checker) exprInfix(sc *scope, e *ast.InfixExpression, facts factSet, want types.Type) types.TypeAndValue {
	switch e.Operator {
	case token.NULLISH:
		return c.exprNullish(sc, e, facts, want)
	case token.AND, token.OR:
		return c.exprLogical(sc, e, facts)
	case token.EQ, token.NOT_EQ:
		return c.exprEquality(sc, e, facts)
	case token.LT, token.GT, token.LT_EQ, token.GT_EQ:
		return c.exprOrdering(sc, e, facts)
	case token.PLUS, token.MINUS, token.ASTERISK, token.SLASH, token.MOD:
		return c.exprArith(sc, e, facts, want)
	}
	return c.invalid(e)
}

// exprArith types + - * / %. + is numeric or two strings; - * are numeric;
// / and % follow the integer-only div-by-zero rule (OQ6). It pairs untyped
// operands via the untyped machinery and never widens implicitly.
func (c *checker) exprArith(sc *scope, e *ast.InfixExpression, facts factSet, want types.Type) types.TypeAndValue {
	lt := c.expr(sc, e.Left, facts, nil)
	rt := c.expr(sc, e.Right, facts, nil)
	if types.IsInvalid(lt.Type) || types.IsInvalid(rt.Type) {
		return c.invalid(e)
	}

	// String concatenation: + on two strings.
	if e.Operator == token.PLUS && types.IsString(lt.Type) && types.IsString(rt.Type) {
		v := foldConcat(lt.Value, rt.Value)
		return c.record(e, types.Typ[types.String], v)
	}

	xt, yt, ok := c.pairNumeric(e, lt, rt)
	if !ok {
		c.errorf(e.OpPos, "E-TYPE", "operator %s is not defined on %s and %s", e.Operator, lt.Type, rt.Type)
		return c.invalid(e)
	}
	result := xt
	if types.IsUntyped(xt) && types.IsUntyped(yt) {
		// Two untyped constants: default the result (or take the want below).
		result = types.Default(xt)
		if types.IsFloat(xt) || types.IsFloat(yt) {
			result = types.Typ[types.Float64]
		}
	} else if types.IsUntyped(xt) {
		result = yt
	}

	// Fold constants, honouring the integer-only div-by-zero rule.
	v := c.foldArith(e, result, xt, yt, lt.Value, rt.Value)
	return c.record(e, result, v)
}

// pairNumeric resolves the operand types of a numeric binary operator: both
// must be numeric and compatible (two typed must be identical; an untyped
// pairs with a typed it is assignable to). It returns the resolved left and
// right types and whether the pairing is valid.
func (c *checker) pairNumeric(e *ast.InfixExpression, lt, rt types.TypeAndValue) (types.Type, types.Type, bool) {
	x, y := lt.Type, rt.Type
	if !types.IsNumeric(x) || !types.IsNumeric(y) {
		return x, y, false
	}
	switch {
	case types.IsUntyped(x) && types.IsUntyped(y):
		return x, y, true
	case types.IsUntyped(x):
		if types.AssignableTo(x, y) {
			return y, y, true
		}
		return x, y, false
	case types.IsUntyped(y):
		if types.AssignableTo(y, x) {
			return x, x, true
		}
		return x, y, false
	default:
		if types.Identical(x, y) {
			return x, y, true
		}
		return x, y, false
	}
}

// foldArith folds a constant arithmetic expression. result is the resolved
// result type, xt/yt the operand types. For / with any float operand and a
// zero divisor it skips folding (runtime ±Inf/NaN per OQ6); for integer / and
// % by a constant zero it reports E-CONST.
func (c *checker) foldArith(e *ast.InfixExpression, result, xt, yt types.Type, xv, yv constant.Value) constant.Value {
	if xv == nil || yv == nil {
		return nil
	}
	// Float / with a zero divisor: do not fold, no E-CONST (OQ6).
	if e.Operator == token.SLASH && (types.IsFloat(xt) || types.IsFloat(yt)) && constant.Sign(yv) == 0 {
		return nil
	}
	v, err := types.FoldBinary(e.Operator, xv, yv)
	if err == types.ErrDivisionByZero {
		// Only integer /,% reach here as an error (float / was handled above;
		// float % returns (nil,nil) from FoldBinary).
		c.errorf(e.OpPos, "E-CONST", "division by zero")
		return nil
	}
	if v == nil {
		return nil
	}
	// Re-check representability in the result type (e.g. int32 overflow).
	if cv, ok := types.Representable(v, result); ok {
		return cv
	}
	return v
}

// exprEquality types == and != via CanCompare, giving an untyped constant
// the other operand's type, folding constant pairs, and yielding bool. A
// null operand is typed from the other operand (which must be optional), per
// CanCompare, not by the context-free-null rule.
func (c *checker) exprEquality(sc *scope, e *ast.InfixExpression, facts factSet) types.TypeAndValue {
	lt := c.compareOperand(sc, e.Left, e.Right, facts)
	rt := c.compareOperand(sc, e.Right, e.Left, facts)
	if types.IsInvalid(lt.Type) || types.IsInvalid(rt.Type) {
		return c.record(e, types.Typ[types.Bool], nil)
	}
	if !types.CanCompare(lt.Type, rt.Type) {
		c.errorf(e.OpPos, "E-TYPE", "cannot compare %s and %s", lt.Type, rt.Type)
		return c.record(e, types.Typ[types.Bool], nil)
	}
	v, _ := types.FoldBinary(e.Operator, lt.Value, rt.Value)
	return c.record(e, types.Typ[types.Bool], v)
}

// compareOperand types one operand of an equality comparison. A null literal
// is recorded with no want so it stays UntypedNull (CanCompare pairs it with
// the other operand's optional type); the context-free-null error is
// suppressed here because the comparison, not an annotation, gives it meaning.
// A narrowed identifier compared with null keeps its optional type (it is in
// the roleNullCompare position when the other operand is null).
func (c *checker) compareOperand(sc *scope, self, other ast.Expression, facts factSet) types.TypeAndValue {
	if _, ok := self.(*ast.NullLiteral); ok {
		return c.record(self, types.Typ[types.UntypedNull], nil)
	}
	role := roleValue
	if _, ok := other.(*ast.NullLiteral); ok {
		role = roleNullCompare
	}
	return c.exprRole(sc, self, facts, nil, role)
}

// exprOrdering types < > <= >= via CanOrder, folding constant pairs, yielding
// bool.
func (c *checker) exprOrdering(sc *scope, e *ast.InfixExpression, facts factSet) types.TypeAndValue {
	lt := c.expr(sc, e.Left, facts, nil)
	rt := c.expr(sc, e.Right, facts, nil)
	if types.IsInvalid(lt.Type) || types.IsInvalid(rt.Type) {
		return c.record(e, types.Typ[types.Bool], nil)
	}
	if !types.CanOrder(lt.Type, rt.Type) {
		c.errorf(e.OpPos, "E-TYPE", "cannot order %s and %s", lt.Type, rt.Type)
		return c.record(e, types.Typ[types.Bool], nil)
	}
	v, _ := types.FoldBinary(e.Operator, lt.Value, rt.Value)
	return c.record(e, types.Typ[types.Bool], v)
}

// exprLogical types && and ||: both operands bool, result bool, short-circuit.
// The right operand of && is checked under the left's whenTrue facts; the
// right operand of || under the left's whenFalse facts (DESIGN.md §2.5).
func (c *checker) exprLogical(sc *scope, e *ast.InfixExpression, facts factSet) types.TypeAndValue {
	lt := c.expr(sc, e.Left, facts, types.Typ[types.Bool])
	if !types.IsBool(lt.Type) && !types.IsInvalid(lt.Type) {
		c.errorf(e.Left.Pos(), "E-TYPE", "operator %s requires bool, got %s", e.Operator, lt.Type)
	}
	cf := c.condFactsOf(e.Left, sc)
	rightFacts := facts
	if e.Operator == token.AND {
		rightFacts = facts.with(cf.whenTrue...)
	} else {
		rightFacts = facts.with(cf.whenFalse...)
	}
	rt := c.expr(sc, e.Right, rightFacts, types.Typ[types.Bool])
	if !types.IsBool(rt.Type) && !types.IsInvalid(rt.Type) {
		c.errorf(e.Right.Pos(), "E-TYPE", "operator %s requires bool, got %s", e.Operator, rt.Type)
	}
	v, _ := types.FoldBinary(e.Operator, lt.Value, rt.Value)
	return c.record(e, types.Typ[types.Bool], v)
}

// exprNullish types "left ?? right": left must be optional T | null, right
// assignable to T with no conversion recorded; result T (DESIGN.md §2.5). The
// left operand keeps its optional type (roleNullishLeft).
func (c *checker) exprNullish(sc *scope, e *ast.InfixExpression, facts factSet, want types.Type) types.TypeAndValue {
	lt := c.exprRole(sc, e.Left, facts, nil, roleNullishLeft)
	if types.IsInvalid(lt.Type) {
		c.expr(sc, e.Right, facts, nil)
		return c.invalid(e)
	}
	if !types.IsOptional(lt.Type) {
		c.errorf(e.Left.Pos(), "E-TYPE", "operator ?? requires an optional left operand, got %s", lt.Type)
		c.expr(sc, e.Right, facts, nil)
		return c.invalid(e)
	}
	elem := types.NonOptional(lt.Type)
	rt := c.expr(sc, e.Right, facts, elem)
	if !types.IsInvalid(rt.Type) && !types.AssignableTo(rt.Type, elem) {
		c.errorf(e.Right.Pos(), "E-TYPE", "?? fallback %s is not assignable to %s", rt.Type, elem)
	}
	return c.record(e, elem, nil)
}

// exprTernary types "cond ? a : b": cond bool, branches against want; with no
// want the result is the common type of the branches (identical, or the
// optional when one is T and the other T | null or null, recording
// ConvToOptional on the non-optional branch).
func (c *checker) exprTernary(sc *scope, e *ast.TernaryExpression, facts factSet, want types.Type) types.TypeAndValue {
	c.condition(sc, e.Condition, facts)
	cf := c.condFactsOf(e.Condition, sc)
	at := c.expr(sc, e.Consequence, facts.with(cf.whenTrue...), want)
	bt := c.expr(sc, e.Alternative, facts.with(cf.whenFalse...), want)
	if want != nil {
		c.assign(sc, e.Consequence, at, want)
		c.assign(sc, e.Alternative, bt, want)
		return c.record(e, want, nil)
	}
	if types.IsInvalid(at.Type) || types.IsInvalid(bt.Type) {
		return c.invalid(e)
	}
	common, ok := c.commonType(e, at, bt)
	if !ok {
		c.errorf(e.QuestionPos, "E-TYPE", "branches of ?: have incompatible types %s and %s", at.Type, bt.Type)
		return c.invalid(e)
	}
	return c.record(e, common, nil)
}

// commonType finds the result type of a want-less ternary and records a
// ConvToOptional on the branch that widens to the optional. It handles
// identical branches, T vs T | null, and T vs null.
func (c *checker) commonType(e *ast.TernaryExpression, at, bt types.TypeAndValue) (types.Type, bool) {
	x, y := at.Type, bt.Type
	if types.Identical(types.Default(x), types.Default(y)) {
		return types.Default(x), true
	}
	// One branch null: result is the other's optional.
	if types.IsBasic(x, types.UntypedNull) && types.CanBeOptional(types.NonOptional(y)) {
		opt := types.NewOptional(y)
		c.info.Types[e.Consequence] = types.TypeAndValue{Type: opt}
		return opt, true
	}
	if types.IsBasic(y, types.UntypedNull) && types.CanBeOptional(types.NonOptional(x)) {
		opt := types.NewOptional(x)
		c.info.Types[e.Alternative] = types.TypeAndValue{Type: opt}
		return opt, true
	}
	// T vs T | null.
	if types.IsOptional(x) && types.Identical(types.NonOptional(x), y) {
		c.info.Conversions[e.Alternative] = types.Conversion{Kind: types.ConvToOptional, From: y, To: x}
		return x, true
	}
	if types.IsOptional(y) && types.Identical(types.NonOptional(y), x) {
		c.info.Conversions[e.Consequence] = types.Conversion{Kind: types.ConvToOptional, From: x, To: y}
		return y, true
	}
	return nil, false
}

// exprNonNull types "x!": the operand must be optional; the result is its
// non-null type. The operand keeps its optional type (rolePostfixBang) and
// the expression is always may-fail (marked in FEAT-003's pass).
func (c *checker) exprNonNull(sc *scope, e *ast.NonNullExpression, facts factSet) types.TypeAndValue {
	tv := c.exprRole(sc, e.Left, facts, nil, rolePostfixBang)
	if types.IsInvalid(tv.Type) {
		return c.invalid(e)
	}
	if !types.IsOptional(tv.Type) {
		c.errorf(e.BangPos, "E-TYPE", "operator ! requires an optional operand, got %s", tv.Type)
		return c.invalid(e)
	}
	return c.record(e, types.NonOptional(tv.Type), nil)
}

// exprIndex types "xs[i]": xs an array, i an integer; result the element
// type. Always may-fail (bounds check), marked in FEAT-003's pass.
func (c *checker) exprIndex(sc *scope, e *ast.IndexExpression, facts factSet) types.TypeAndValue {
	lt := c.expr(sc, e.Left, facts, nil)
	it := c.expr(sc, e.Index, facts, types.Typ[types.Int64])
	if !types.IsInvalid(it.Type) && !types.IsInteger(it.Type) {
		c.errorf(e.Index.Pos(), "E-TYPE", "array index must be an integer, got %s", it.Type)
	}
	arr, ok := lt.Type.(*types.Array)
	if !ok {
		if !types.IsInvalid(lt.Type) {
			c.errorf(e.Left.Pos(), "E-TYPE", "cannot index %s; an array is required", lt.Type)
		}
		return c.invalid(e)
	}
	return c.record(e, arr.Elem, nil)
}

// exprArray types "[a, b, ...]": the element type is NonOptional(want) when a
// want array is present, else the defaulted type of the first element; every
// element is checked against it with widening. An empty [] with no want is
// E-TYPE (cannot infer).
func (c *checker) exprArray(sc *scope, e *ast.ArrayLiteral, facts factSet, want types.Type) types.TypeAndValue {
	var elem types.Type
	if arr, ok := want.(*types.Array); ok {
		elem = arr.Elem
	}
	if elem == nil {
		if len(e.Elements) == 0 {
			c.errorf(e.Lbracket, "E-TYPE", "cannot infer the element type of an empty array; add a type annotation")
			return c.invalid(e)
		}
		first := c.expr(sc, e.Elements[0], facts, nil)
		elem = types.Default(first.Type)
		if types.IsBasic(elem, types.UntypedNull) || types.IsInvalid(elem) {
			return c.invalid(e)
		}
		for _, el := range e.Elements[1:] {
			tv := c.expr(sc, el, facts, elem)
			c.assign(sc, el, tv, elem)
		}
		return c.record(e, types.NewArray(elem), nil)
	}
	for _, el := range e.Elements {
		tv := c.expr(sc, el, facts, elem)
		c.assign(sc, el, tv, elem)
	}
	return c.record(e, types.NewArray(elem), nil)
}

// exprObject types "{ k: v, ... }": allowed only where want is a known
// interface *Named. Each key must be a field (recorded in Info.Uses with the
// field *Var); unknown or duplicate keys are E-INIT; values are checked with
// widening; every non-optional field must be present (E-INIT).
func (c *checker) exprObject(sc *scope, e *ast.ObjectLiteral, facts factSet, want types.Type) types.TypeAndValue {
	named, ok := want.(*types.Named)
	if !ok || named.IsGeneric() {
		c.errorf(e.Lbrace, "E-TYPE", "object literal needs an interface type")
		for _, entry := range e.Entries {
			c.expr(sc, entry.Value, facts, nil)
		}
		return c.invalid(e)
	}
	seen := map[string]bool{}
	for _, entry := range e.Entries {
		field := named.Field(entry.Key.Name)
		if field == nil {
			c.errorf(entry.Key.NamePos, "E-INIT", "unknown field %s in %s", entry.Key.Name, named)
			c.expr(sc, entry.Value, facts, nil)
			continue
		}
		if seen[entry.Key.Name] {
			c.errorf(entry.Key.NamePos, "E-INIT", "duplicate field %s", entry.Key.Name)
		}
		seen[entry.Key.Name] = true
		c.info.Uses[entry.Key] = field
		tv := c.expr(sc, entry.Value, facts, field.Type)
		c.assign(sc, entry.Value, tv, field.Type)
	}
	for _, f := range named.Fields() {
		if !f.Optional && !seen[f.Name] {
			c.errorf(e.Lbrace, "E-INIT", "missing field %s in %s", f.Name, named)
		}
	}
	return c.record(e, named, nil)
}

// exprNew types a new expression (DESIGN.md §2.4, OQ4): interface new T() and
// array new T[]() take no arguments; new Error takes 0/1/2 args. A
// non-interface, non-Error, non-array type is E-TYPE; a generic origin
// without args is E-GENERIC.
func (c *checker) exprNew(sc *scope, e *ast.NewExpression, facts factSet) types.TypeAndValue {
	base := c.resolveTypeIn(sc, e.Type)
	if e.IsArray {
		if len(e.Args) != 0 {
			c.errorf(e.NewPos, "E-TYPE", "new T[]() takes no arguments")
		}
		for _, a := range e.Args {
			c.expr(sc, a, facts, nil)
		}
		if types.IsInvalid(base) {
			return c.invalid(e)
		}
		return c.record(e, types.NewArray(base), nil)
	}
	if types.IsBasic(base, types.Error) {
		return c.exprNewError(sc, e, facts)
	}
	named, ok := base.(*types.Named)
	if !ok {
		if !types.IsInvalid(base) {
			c.errorf(e.NewPos, "E-TYPE", "cannot allocate %s with new; an interface, array or Error is required", base)
		}
		for _, a := range e.Args {
			c.expr(sc, a, facts, nil)
		}
		return c.invalid(e)
	}
	if named.IsGeneric() {
		c.errorf(e.NewPos, "E-GENERIC", "generic type %s needs type arguments", named.Name())
		return c.invalid(e)
	}
	if len(e.Args) != 0 {
		c.errorf(e.NewPos, "E-TYPE", "new %s() takes no arguments", named.Name())
		for _, a := range e.Args {
			c.expr(sc, a, facts, nil)
		}
	}
	return c.record(e, named, nil)
}

// exprNewError types new Error(...): new Error(), new Error(msg: string) or
// new Error(msg: string, status: int32); any other arity is E-TYPE.
func (c *checker) exprNewError(sc *scope, e *ast.NewExpression, facts factSet) types.TypeAndValue {
	if len(e.Args) > 2 {
		c.errorf(e.NewPos, "E-TYPE", "new Error takes at most 2 arguments")
	}
	if len(e.Args) >= 1 {
		tv := c.expr(sc, e.Args[0], facts, types.Typ[types.String])
		if !types.IsInvalid(tv.Type) && !types.IsString(tv.Type) {
			c.errorf(e.Args[0].Pos(), "E-TYPE", "Error message must be a string, got %s", tv.Type)
		}
	}
	if len(e.Args) >= 2 {
		tv := c.expr(sc, e.Args[1], facts, types.Typ[types.Int32])
		c.assign(sc, e.Args[1], tv, types.Typ[types.Int32])
	}
	for _, a := range e.Args[min2(len(e.Args), 2):] {
		c.expr(sc, a, facts, nil)
	}
	return c.record(e, types.Typ[types.Error], nil)
}

// ---------------------------------------------------------------------------
// Assignments

// checkAssignment checks an assignment statement expression: a valid l-value
// that is not const, and either a plain "=" (RHS assignable to the target
// type) or a compound/inc-dec form typed via the underlying binary operator
// (OQ5: += on strings; ++/-- on any numeric; other compound numeric-only).
func (c *checker) checkAssignment(sc *scope, e *ast.AssignmentExpression, facts factSet) {
	target, ok := c.lvalue(sc, e, facts)
	if !ok {
		if e.Value != nil {
			c.expr(sc, e.Value, facts, nil)
		}
		c.record(e, types.Typ[types.Void], nil)
		return
	}
	if types.IsInvalid(target) {
		if e.Value != nil {
			c.expr(sc, e.Value, facts, nil)
		}
		c.record(e, types.Typ[types.Void], nil)
		return
	}

	if e.Operator == token.ASSIGN {
		tv := c.expr(sc, e.Value, facts, target)
		c.assign(sc, e.Value, tv, target)
		c.record(e, types.Typ[types.Void], nil)
		return
	}

	// Compound or inc/dec: type target BinaryOp value.
	op := e.BinaryOp()
	var vt types.TypeAndValue
	if e.IsIncDec() {
		vt = types.TypeAndValue{Type: types.Typ[types.UntypedInt], Value: constant.MakeInt64(1)}
	} else {
		vt = c.expr(sc, e.Value, facts, target)
	}
	c.checkCompound(e, op, target, vt)
	c.record(e, types.Typ[types.Void], nil)
}

// checkCompound checks the arithmetic of a compound assignment: += on strings
// (concatenation) is allowed; ++/-- and compound numeric ops require a
// numeric target; other compound operators on non-numeric targets are
// E-TYPE. The implied value must be assignable back to the target type.
func (c *checker) checkCompound(e *ast.AssignmentExpression, op token.TokenType, target types.Type, vt types.TypeAndValue) {
	if types.IsInvalid(vt.Type) {
		return
	}
	// += on strings: concatenation.
	if op == token.PLUS && types.IsString(target) {
		if !types.IsString(vt.Type) {
			c.errorf(e.OpPos, "E-TYPE", "cannot concatenate %s to a string", vt.Type)
		}
		return
	}
	if !types.IsNumeric(target) {
		c.errorf(e.OpPos, "E-TYPE", "operator %s is not defined on %s", e.Operator, target)
		return
	}
	if !types.IsNumeric(vt.Type) {
		c.errorf(e.OpPos, "E-TYPE", "operator %s requires a number, got %s", e.Operator, vt.Type)
		return
	}
	// The implied value must be assignable to the target (an untyped constant
	// such as 1 always is for a numeric target; a typed value must match).
	if !types.IsUntyped(vt.Type) && !types.Identical(vt.Type, target) {
		c.errorf(e.OpPos, "E-TYPE", "cannot use %s in a compound assignment to %s", vt.Type, target)
	}
}

// lvalue resolves an assignment target to its type and reports whether it is
// assignable: an identifier (a non-const *Var), a field selection, or an
// array index. A const binding, an unknown target shape, or a non-value
// target is rejected. The target is checked in the roleAssignTarget position
// so a narrowed identifier keeps its optional type.
func (c *checker) lvalue(sc *scope, e *ast.AssignmentExpression, facts factSet) (types.Type, bool) {
	switch t := e.Target.(type) {
	case *ast.Identifier:
		obj := sc.lookup(t.Name)
		if obj == nil {
			c.errorf(t.NamePos, "E-NAME", "undefined: %s", t.Name)
			return nil, false
		}
		v, ok := obj.(*types.Var)
		if !ok {
			c.errorf(t.NamePos, "E-TYPE", "cannot assign to %s", t.Name)
			return nil, false
		}
		c.info.Uses[t] = v
		if v.IsConst {
			c.errorf(t.NamePos, "E-TYPE", "cannot assign to const %s", t.Name)
			return nil, false
		}
		return v.Type, true
	case *ast.MemberExpression:
		tv := c.exprMember(sc, t, facts)
		return tv.Type, !types.IsInvalid(tv.Type)
	case *ast.IndexExpression:
		tv := c.exprIndex(sc, t, facts)
		return tv.Type, !types.IsInvalid(tv.Type)
	}
	c.errorf(e.Target.Pos(), "E-TYPE", "invalid assignment target")
	return nil, false
}

// foldConcat folds a constant string concatenation, or returns nil when
// either operand is not a constant.
func foldConcat(x, y constant.Value) constant.Value {
	if x == nil || y == nil || x.Kind() != constant.String || y.Kind() != constant.String {
		return nil
	}
	return constant.MakeString(constant.StringVal(x) + constant.StringVal(y))
}

// min2 returns the smaller of a and b.
func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
