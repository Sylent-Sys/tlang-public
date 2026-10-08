package checker

import (
	"go/constant"
	"math"

	"tlang/ast"
	"tlang/types"
)

// typecheck.go is the body typechecker (DESIGN.md pass 3): the statement and
// expression walker that records Info.Types/Uses/Defs(locals)/Selections/
// Calls/Conversions/Narrowed and seeds Info.Routes/DBTypes/UsesDB and the
// generic worklist. It drives the types-package predicates and folders
// rather than re-deriving any rule, recovers with Typ[Invalid] so one error
// does not cascade, and never panics on a parser-produced tree.

// checkBodies is pass 3. It checks every global initializer (in declaration
// order) and every function/method body once, in terms of the function's
// type parameters. It replaces the FEAT-001 no-op hook.
func (c *checker) checkBodies(prog *ast.Program) {
	for _, g := range c.globals {
		c.inScope(c.globalScope[g], func() { c.checkGlobalInit(g) })
	}
	for _, d := range c.funcs {
		c.inScope(d.sc, func() { c.checkFuncBody(d) })
	}
}

// checkGlobalInit checks a top-level let/const initializer and infers the
// variable's type when the annotation was omitted (DESIGN.md §2.10). An
// un-annotated global with no initializer whose type has no zero value is
// E-INIT.
func (c *checker) checkGlobalInit(g *types.Var) {
	let, ok := g.Decl.(*ast.LetStatement)
	if !ok {
		return
	}
	c.checkVarDecl(c.global, g, let)
}

// checkFuncBody checks one function or method body with a fresh scope that
// binds the receiver and parameters, the current result type, and the loop/
// try/transaction context reset. Generic bodies are checked once with their
// type parameters opaque. A function with a nil signature (a pass-2 failure)
// is skipped.
func (c *checker) checkFuncBody(d *funcDecl) {
	if d.fn.Sig == nil || d.stmt.Body == nil {
		return
	}
	sig := d.fn.Sig
	body := newScope(c.global)
	for _, tp := range sig.TypeParams {
		if tp.Obj != nil {
			body.objs[tp.Obj.Name] = tp.Obj
		}
	}
	if sig.Recv != nil {
		c.bindParam(body, sig.Recv)
	}
	for _, p := range sig.Params {
		c.bindParam(body, p)
	}

	saveResult, saveLoop, saveTry, saveTx := c.result, c.inLoop, c.inTry, c.inTx
	c.result = sig.Result
	c.inLoop, c.inTry, c.inTx = false, false, false
	c.stmtList(body, d.stmt.Body.Statements, factSet(nil))
	c.result, c.inLoop, c.inTry, c.inTx = saveResult, saveLoop, saveTry, saveTx
}

// bindParam declares a receiver or parameter *Var in the body scope by its
// name (the parameter identifier's Info.Defs was recorded in pass 2).
func (c *checker) bindParam(sc *scope, v *types.Var) {
	if v.Name != "" {
		sc.objs[v.Name] = v
	}
}

// ---------------------------------------------------------------------------
// Statements

// stmtList checks a sequence of statements in sc, threading the narrowing
// fact set. It returns the fact set that holds after the whole list (an "if
// (x == null) { return; }" at the top adds x to the facts of what follows).
func (c *checker) stmtList(sc *scope, stmts []ast.Statement, facts factSet) factSet {
	for _, s := range stmts {
		facts = c.stmt(sc, s, facts)
	}
	return facts
}

// stmt checks one statement in sc under the narrowing facts and returns the
// fact set that holds immediately after it.
func (c *checker) stmt(sc *scope, s ast.Statement, facts factSet) factSet {
	switch s := s.(type) {
	case *ast.LetStatement:
		return c.checkLocalLet(sc, s, facts)
	case *ast.ReturnStatement:
		c.checkReturn(sc, s, facts)
	case *ast.ExpressionStatement:
		c.checkExprStmt(sc, s, facts)
	case *ast.BlockStatement:
		child := newScope(sc)
		c.stmtList(child, s.Statements, facts)
	case *ast.IfStatement:
		return c.checkIf(sc, s, facts)
	case *ast.WhileStatement:
		c.checkWhile(sc, s, facts)
	case *ast.ForStatement:
		c.checkFor(sc, s, facts)
	case *ast.ForOfStatement:
		c.checkForOf(sc, s, facts)
	case *ast.BreakStatement:
		if !c.inLoop {
			c.errorf(s.BreakPos, "E-TYPE", "break outside a loop")
		}
	case *ast.ContinueStatement:
		if !c.inLoop {
			c.errorf(s.ContinuePos, "E-TYPE", "continue outside a loop")
		}
	case *ast.ThrowStatement:
		c.checkThrow(sc, s, facts)
	case *ast.TryCatchStatement:
		c.checkTryCatch(sc, s, facts)
	case *ast.TransactionStatement:
		c.checkTransaction(sc, s, facts)
	case *ast.BadStatement:
		// Parser already reported the syntax error; skip.
	}
	return facts
}

// checkLocalLet checks a block-scoped let/const, declaring the local *Var
// (recording Info.Defs) and threading facts. It returns facts unchanged
// (a declaration establishes no null-narrowing fact).
func (c *checker) checkLocalLet(sc *scope, s *ast.LetStatement, facts factSet) factSet {
	v := &types.Var{Name: s.Name.Name, Kind: types.LocalVar, IsConst: s.IsConst, Pos: s.Name.NamePos, Decl: s}
	c.info.Defs[s.Name] = v
	c.checkVarDeclFacts(sc, v, s, facts)
	c.declare(sc, s.Name.Name, types.DeclValue, v, s.Name.NamePos)
	return facts
}

// checkVarDecl checks a variable declaration with no ambient narrowing facts
// (used for globals).
func (c *checker) checkVarDecl(sc *scope, v *types.Var, s *ast.LetStatement) {
	c.checkVarDeclFacts(sc, v, s, factSet(nil))
}

// checkVarDeclFacts resolves a variable's declared type (if written) and
// checks its initializer against it, inferring the type when the annotation
// is omitted. An initializer-free declaration requires HasZeroValue (else
// E-INIT). It fills v.Type.
func (c *checker) checkVarDeclFacts(sc *scope, v *types.Var, s *ast.LetStatement, facts factSet) {
	var want types.Type
	if s.Type != nil {
		if v.Type != nil {
			want = v.Type // globals: resolved in pass 2
		} else {
			want = c.resolveTypeIn(sc, s.Type)
		}
	}
	if s.Value == nil {
		// No initializer: the type must be written and have a zero value.
		if want == nil {
			c.errorf(s.Name.NamePos, "E-TYPE", "cannot infer a type without an initializer")
			v.Type = types.Typ[types.Invalid]
			return
		}
		if !types.HasZeroValue(want) {
			c.errorf(s.Name.NamePos, "E-INIT", "%s has no zero value; add an initializer", want)
		}
		v.Type = want
		return
	}
	tv := c.expr(sc, s.Value, facts, want)
	if want != nil {
		c.assign(sc, s.Value, tv, want)
		v.Type = want
	} else {
		v.Type = types.Default(tv.Type)
		if types.IsBasic(v.Type, types.UntypedNull) || types.IsInvalid(v.Type) {
			// A context-free null (already reported E-TYPE by expr) or an
			// invalid initializer: recover with Invalid.
			if types.IsBasic(v.Type, types.UntypedNull) {
				v.Type = types.Typ[types.Invalid]
			}
		}
	}
}

// checkReturn checks a return statement's value against the current result
// type (so "return null" is handled by want, not the context-free-null
// rule). A bare "return" in a non-void function is E-TYPE.
func (c *checker) checkReturn(sc *scope, s *ast.ReturnStatement, facts factSet) {
	if s.Value == nil {
		if c.result != nil && !types.IsVoid(c.result) && !types.IsInvalid(c.result) {
			c.errorf(s.ReturnPos, "E-TYPE", "missing return value; %s expected", c.result)
		}
		return
	}
	want := c.result
	if want != nil && types.IsVoid(want) {
		c.expr(sc, s.Value, facts, nil)
		c.errorf(s.ReturnPos, "E-TYPE", "void function returns a value")
		return
	}
	tv := c.expr(sc, s.Value, facts, want)
	if want != nil {
		c.assign(sc, s.Value, tv, want)
	}
}

// checkExprStmt checks an expression statement. Only a call or an assignment
// is allowed for effect; any other bare expression is rejected (DESIGN.md
// §2.2 / the ExpressionStatement doc).
func (c *checker) checkExprStmt(sc *scope, s *ast.ExpressionStatement, facts factSet) {
	switch e := s.Expression.(type) {
	case *ast.CallExpression:
		c.expr(sc, e, facts, nil)
	case *ast.AssignmentExpression:
		c.checkAssignment(sc, e, facts)
	case *ast.BadExpression:
		// skip
	default:
		c.expr(sc, s.Expression, facts, nil)
		c.errorf(s.Expression.Pos(), "E-TYPE", "expression statement must be a call or an assignment")
	}
}

// checkIf checks an if statement: condition bool, consequence under whenTrue
// facts, alternative under whenFalse facts, each in a child scope. The facts
// after the if gain a binding whose null-check consequence always exits
// (DESIGN.md §2.5 always-exit guard). Facts are invalidated for bindings
// assigned anywhere in the branch bodies.
func (c *checker) checkIf(sc *scope, s *ast.IfStatement, facts factSet) factSet {
	c.condition(sc, s.Condition, facts)
	cf := c.condFactsOf(s.Condition, sc)

	consAssigned := c.assignedVars(s.Consequence, sc)
	consScope := newScope(sc)
	consFacts := facts.without(consAssigned).with(dropAssigned(cf.whenTrue, consAssigned)...)
	c.stmt(consScope, s.Consequence, consFacts)

	if s.Alternative != nil {
		altAssigned := c.assignedVars(s.Alternative, sc)
		altScope := newScope(sc)
		altFacts := facts.without(altAssigned).with(dropAssigned(cf.whenFalse, altAssigned)...)
		c.stmt(altScope, s.Alternative, altFacts)
	}

	// Always-exit guard: if the consequence always exits, the code after the
	// if runs under the whenFalse facts (e.g. "if (x == null) return;").
	if alwaysExits(s.Consequence) {
		return facts.with(cf.whenFalse...)
	}
	if s.Alternative != nil && alwaysExits(s.Alternative) {
		return facts.with(cf.whenTrue...)
	}
	return facts
}

// checkWhile checks a while loop: condition bool, body under the condition's
// whenTrue facts in a loop context. Facts reset at the top conservatively
// (an assignment in the body could invalidate), so only the condition's
// whenTrue applies inside (DESIGN.md §2.5).
func (c *checker) checkWhile(sc *scope, s *ast.WhileStatement, facts factSet) {
	c.condition(sc, s.Condition, facts)
	cf := c.condFactsOf(s.Condition, sc)
	assigned := c.assignedVars(s.Body, sc)
	body := newScope(sc)
	bodyFacts := facts.without(assigned).with(dropAssigned(cf.whenTrue, assigned)...)
	save := c.inLoop
	c.inLoop = true
	c.stmt(body, s.Body, bodyFacts)
	c.inLoop = save
}

// checkFor checks a C-style for loop: Init (a let or expression statement) in
// a loop-header scope, Condition bool, Post, and the body in a loop context.
func (c *checker) checkFor(sc *scope, s *ast.ForStatement, facts factSet) {
	header := newScope(sc)
	if s.Init != nil {
		facts = c.stmt(header, s.Init, facts)
	}
	if s.Condition != nil {
		c.condition(header, s.Condition, facts)
	}
	cf := c.condFactsOf(s.Condition, header)
	assigned := c.assignedVars(s.Body, header)
	body := newScope(header)
	bodyFacts := facts.without(assigned).with(dropAssigned(cf.whenTrue, assigned)...)
	save := c.inLoop
	c.inLoop = true
	c.stmt(body, s.Body, bodyFacts)
	if s.Post != nil {
		c.checkPost(header, s.Post, facts)
	}
	c.inLoop = save
}

// checkPost checks a for-loop post expression (usually an assignment or a
// call).
func (c *checker) checkPost(sc *scope, e ast.Expression, facts factSet) {
	switch e := e.(type) {
	case *ast.AssignmentExpression:
		c.checkAssignment(sc, e, facts)
	case *ast.CallExpression:
		c.expr(sc, e, facts, nil)
	default:
		c.expr(sc, e, facts, nil)
	}
}

// checkForOf checks "for (const x of xs)": xs must be an array, the loop
// variable takes the element type, and the body runs in a loop context.
func (c *checker) checkForOf(sc *scope, s *ast.ForOfStatement, facts factSet) {
	tv := c.expr(sc, s.Iterable, facts, nil)
	var elem types.Type = types.Typ[types.Invalid]
	if arr, ok := tv.Type.(*types.Array); ok {
		elem = arr.Elem
	} else if !types.IsInvalid(tv.Type) {
		c.errorf(s.Iterable.Pos(), "E-TYPE", "cannot iterate over %s; an array is required", tv.Type)
	}
	v := &types.Var{Name: s.Var.Name, Kind: types.LocalVar, IsConst: s.IsConst, Type: elem, Pos: s.Var.NamePos, Decl: s}
	c.info.Defs[s.Var] = v
	body := newScope(sc)
	c.declare(body, s.Var.Name, types.DeclValue, v, s.Var.NamePos)
	save := c.inLoop
	c.inLoop = true
	c.stmt(body, s.Body, facts)
	c.inLoop = save
}

// checkThrow checks a throw statement: the value must be an Error or a string
// (DESIGN.md §2.8). It never records a may-fail entry (statements are not in
// Info.MayFail; the enclosing function becomes may-fail in pass 5).
func (c *checker) checkThrow(sc *scope, s *ast.ThrowStatement, facts factSet) {
	tv := c.expr(sc, s.Value, facts, nil)
	if types.IsInvalid(tv.Type) {
		return
	}
	if !types.IsBasic(tv.Type, types.Error) && !types.IsString(tv.Type) {
		c.errorf(s.Value.Pos(), "E-TYPE", "throw value must be an Error or a string, got %s", tv.Type)
	}
}

// checkTryCatch checks a try/catch: the protected body and the handler each
// open a child scope; the catch binding (when present) has type Error.
func (c *checker) checkTryCatch(sc *scope, s *ast.TryCatchStatement, facts factSet) {
	if s.Body != nil {
		tryScope := newScope(sc)
		save := c.inTry
		c.inTry = true
		c.stmtList(tryScope, s.Body.Statements, facts)
		c.inTry = save
	}
	if s.CatchBody != nil {
		catchScope := newScope(sc)
		if s.CatchParam != nil {
			v := &types.Var{Name: s.CatchParam.Name, Kind: types.LocalVar, Type: types.Typ[types.Error], Pos: s.CatchParam.NamePos, Decl: s}
			c.info.Defs[s.CatchParam] = v
			c.declare(catchScope, s.CatchParam.Name, types.DeclValue, v, s.CatchParam.NamePos)
		}
		c.stmtList(catchScope, s.CatchBody.Statements, facts)
	}
}

// checkTransaction checks "db.transaction((tx) => { ... });": the receiver
// must be the db namespace, the handle tx is a const Transaction local in a
// child scope, and the body is checked in a transaction context. The may-fail
// and TxID bookkeeping is FEAT-003's; the handle and body are declared and
// checked here.
func (c *checker) checkTransaction(sc *scope, s *ast.TransactionStatement, facts factSet) {
	if id, ok := s.Receiver.(*ast.Identifier); ok {
		if b, ok := sc.lookup(id.Name).(*types.Builtin); !ok || b.ID != types.BuiltinDB {
			c.errorf(s.Receiver.Pos(), "E-TYPE", "transaction receiver must resolve to the db namespace")
		} else {
			c.info.Uses[id] = b
		}
	} else {
		c.errorf(s.Receiver.Pos(), "E-TYPE", "transaction receiver must resolve to the db namespace")
	}
	if s.ParamType != nil {
		if pt := c.resolveTypeIn(sc, s.ParamType); !types.IsBasic(pt, types.Transaction) && !types.IsInvalid(pt) {
			c.errorf(s.ParamType.Pos(), "E-TYPE", "transaction handle must be Transaction, got %s", pt)
		}
	}
	body := newScope(sc)
	if s.Param != nil {
		v := &types.Var{Name: s.Param.Name, Kind: types.LocalVar, IsConst: true, Type: types.Typ[types.Transaction], Pos: s.Param.NamePos, Decl: s}
		c.info.Defs[s.Param] = v
		c.declare(body, s.Param.Name, types.DeclValue, v, s.Param.NamePos)
	}
	c.info.UsesDB = true
	save := c.inTx
	c.inTx = true
	if s.Body != nil {
		c.stmtList(body, s.Body.Statements, facts)
	}
	c.inTx = save
}

// condition checks a condition expression and requires it to be bool.
func (c *checker) condition(sc *scope, e ast.Expression, facts factSet) {
	tv := c.expr(sc, e, facts, types.Typ[types.Bool])
	if !types.IsBool(tv.Type) && !types.IsInvalid(tv.Type) {
		c.errorf(e.Pos(), "E-TYPE", "condition must be bool, got %s", tv.Type)
	}
}

// ---------------------------------------------------------------------------
// Expressions

// expr type-checks e against the expected type want (nil for none), records
// Info.Types[e] for value expressions, and returns the TypeAndValue. It is
// the roleValue entry point; operator and narrowing code calls exprRole for
// positions that suppress narrowing.
func (c *checker) expr(sc *scope, e ast.Expression, facts factSet, want types.Type) types.TypeAndValue {
	return c.exprRole(sc, e, facts, want, roleValue)
}

// exprRole is expr with an explicit position role, so a narrowed identifier
// in an assignment target, a null comparison, a ?? left operand or a postfix
// ! operand keeps its optional type and is not marked Info.Narrowed.
func (c *checker) exprRole(sc *scope, e ast.Expression, facts factSet, want types.Type, role posRole) types.TypeAndValue {
	switch e := e.(type) {
	case *ast.Identifier:
		return c.exprIdent(sc, e, facts, role)
	case *ast.IntegerLiteral:
		return c.exprInt(e, want)
	case *ast.FloatLiteral:
		return c.exprFloat(e, want)
	case *ast.StringLiteral:
		return c.record(e, types.Typ[types.String], constant.MakeString(e.Value))
	case *ast.BooleanLiteral:
		return c.record(e, types.Typ[types.Bool], constant.MakeBool(e.Value))
	case *ast.NullLiteral:
		return c.exprNull(e, want)
	case *ast.PrefixExpression:
		return c.exprPrefix(sc, e, facts)
	case *ast.InfixExpression:
		return c.exprInfix(sc, e, facts, want)
	case *ast.TernaryExpression:
		return c.exprTernary(sc, e, facts, want)
	case *ast.NonNullExpression:
		return c.exprNonNull(sc, e, facts)
	case *ast.CallExpression:
		return c.exprCall(sc, e, facts, want)
	case *ast.MemberExpression:
		return c.exprMember(sc, e, facts)
	case *ast.IndexExpression:
		return c.exprIndex(sc, e, facts)
	case *ast.ArrayLiteral:
		return c.exprArray(sc, e, facts, want)
	case *ast.ObjectLiteral:
		return c.exprObject(sc, e, facts, want)
	case *ast.NewExpression:
		return c.exprNew(sc, e, facts)
	case *ast.AssignmentExpression:
		// An assignment is a statement; as an expression it is void.
		c.checkAssignment(sc, e, facts)
		return c.record(e, types.Typ[types.Void], nil)
	case *ast.BadExpression:
		return c.record(e, types.Typ[types.Invalid], nil)
	}
	return c.record(e, types.Typ[types.Invalid], nil)
}

// record stores Info.Types[e] = {t, v} and returns the TypeAndValue.
func (c *checker) record(e ast.Expression, t types.Type, v constant.Value) types.TypeAndValue {
	tv := types.TypeAndValue{Type: t, Value: v}
	c.info.Types[e] = tv
	return tv
}

// invalid records e as Invalid and returns it.
func (c *checker) invalid(e ast.Expression) types.TypeAndValue {
	return c.record(e, types.Typ[types.Invalid], nil)
}

// exprIdent resolves an identifier used as a value: a *Var (recording
// Info.Uses and, when narrowed, Info.Narrowed + the non-optional type), or an
// error for a function/type/namespace used as a value. Undefined is E-NAME.
func (c *checker) exprIdent(sc *scope, e *ast.Identifier, facts factSet, role posRole) types.TypeAndValue {
	obj := sc.lookup(e.Name)
	if obj == nil {
		c.errorf(e.NamePos, "E-NAME", "undefined: %s", e.Name)
		return c.invalid(e)
	}
	switch obj := obj.(type) {
	case *types.Var:
		c.info.Uses[e] = obj
		if v, ok := narrowable(obj); ok && facts.has(v) && role == roleValue {
			c.info.Narrowed[e] = true
			return c.record(e, types.NonOptional(obj.Type), nil)
		}
		return c.record(e, obj.Type, nil)
	case *types.Func:
		c.errorf(e.NamePos, "E-TYPE", "%s is a function, not a value", e.Name)
	case *types.TypeName:
		c.errorf(e.NamePos, "E-TYPE", "%s is a type, not a value", e.Name)
	case *types.Builtin:
		c.errorf(e.NamePos, "E-TYPE", "%s is a namespace, not a value", e.Name)
	case *types.ModuleNS:
		c.errorf(e.NamePos, "E-TYPE", "%s is a namespace, not a value", e.Name)
	}
	return c.invalid(e)
}

// exprInt records an integer literal: untyped int by default, or the target
// numeric type when a want is present and it fits (E-CONST otherwise). An
// overflowing literal is E-CONST.
func (c *checker) exprInt(e *ast.IntegerLiteral, want types.Type) types.TypeAndValue {
	if e.Overflow {
		c.errorf(e.ValuePos, "E-CONST", "integer literal %s overflows", e.Raw)
		return c.invalid(e)
	}
	v := constant.MakeUint64(e.Value)
	return c.recordConst(e, types.Typ[types.UntypedInt], v, want)
}

// exprFloat records a float literal: untyped float by default. A literal
// beyond the float64 range (+Inf) is E-CONST.
func (c *checker) exprFloat(e *ast.FloatLiteral, want types.Type) types.TypeAndValue {
	if math.IsInf(e.Value, 0) {
		c.errorf(e.ValuePos, "E-CONST", "float literal %s overflows", e.Raw)
		return c.invalid(e)
	}
	v := constant.MakeFloat64(e.Value)
	return c.recordConst(e, types.Typ[types.UntypedFloat], v, want)
}

// recordConst records an untyped constant, defaulting it when no want is
// present or converting it to the target numeric type when a want is present
// and the value is representable (E-CONST when it does not fit). It never
// records an optional type for the constant: a widening to an optional is
// recorded in Info.Conversions by the assign path instead.
func (c *checker) recordConst(e ast.Expression, untyped types.Type, v constant.Value, want types.Type) types.TypeAndValue {
	if want == nil || types.IsInvalid(want) {
		return c.record(e, types.Default(untyped), v)
	}
	target := types.NonOptional(want)
	if types.AssignableTo(untyped, target) {
		if cv, ok := types.Representable(v, target); ok {
			return c.record(e, target, cv)
		}
		c.errorf(e.Pos(), "E-CONST", "%s is not representable as %s", v, target)
		return c.record(e, target, nil)
	}
	// Not assignable to the target: default and let the caller's assign
	// report E-TYPE.
	return c.record(e, types.Default(untyped), v)
}

// exprNull records a null literal: when a want optional is present it is the
// null value of that optional (the ConvToOptional is recorded by the assign
// path); with no want it cannot be resolved — Default(null) stays untyped —
// so it is E-TYPE "cannot infer a type from null" (DESIGN.md §2.5 / OQ-null).
func (c *checker) exprNull(e *ast.NullLiteral, want types.Type) types.TypeAndValue {
	if want != nil && types.IsOptional(want) {
		return c.record(e, want, nil)
	}
	if want != nil && types.IsInvalid(want) {
		return c.record(e, types.Typ[types.Invalid], nil)
	}
	c.errorf(e.NullPos, "E-TYPE", "cannot infer a type from null; add a type annotation")
	return c.record(e, types.Typ[types.Invalid], nil)
}

// assign applies the assignability check for a value used in a typed context
// (initializer, assignment RHS, argument, return, field value, array
// element), recording a ConvToOptional in Info.Conversions when a T value (or
// an assignable untyped constant) widens to T | null. On failure it reports
// E-TYPE. The null literal, already recorded with the optional type, is a
// no-op widening and gets no conversion.
func (c *checker) assign(sc *scope, e ast.Expression, tv types.TypeAndValue, want types.Type) {
	if want == nil || types.IsInvalid(want) || types.IsInvalid(tv.Type) {
		return
	}
	if !types.AssignableTo(tv.Type, want) {
		c.errorf(e.Pos(), "E-TYPE", "cannot use %s as %s", tv.Type, want)
		return
	}
	// Record a widening conversion when a non-null value becomes an optional.
	if o, ok := want.(*types.Optional); ok {
		if types.IsBasic(tv.Type, types.UntypedNull) {
			return // null to T | null needs no value conversion
		}
		if types.Identical(tv.Type, o.Elem) || (types.IsUntyped(tv.Type) && types.AssignableTo(tv.Type, o.Elem)) {
			c.info.Conversions[e] = types.Conversion{Kind: types.ConvToOptional, From: tv.Type, To: want}
		}
	}
}
