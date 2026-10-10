package codegen

import (
	"strconv"
	"strings"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// Statement lowering (codegen design §5.2, §7, §8.10, plan D5, D8, D10, D11,
// D12, D16, D24, D25). stmt lowers one statement into the innermost block;
// stmts lowers a list and reports whether it ends in an unconditional jump
// (a return or a throw's goto), which drives the missing-return fallback.
// F3 owns the core statements and identifier assignment; for-of, member and
// index assignment, try/catch, throw and transactions are later features.

// stmt lowers a single statement into fc.blk.
func (fc *funcCtx) stmt(s ast.Statement) {
	fc.g.cur = s.Pos()
	switch s := s.(type) {
	case *ast.BlockStatement:
		fc.blockStmt(s)
	case *ast.LetStatement:
		fc.letStmt(s)
	case *ast.ReturnStatement:
		fc.returnStmt(s)
	case *ast.ExpressionStatement:
		fc.exprStmt(s)
	case *ast.IfStatement:
		fc.ifStmt(s)
	case *ast.WhileStatement:
		fc.whileStmt(s)
	case *ast.ForStatement:
		fc.forStmt(s)
	case *ast.ForOfStatement:
		fc.forOfStmt(s)
	case *ast.BreakStatement:
		fc.blk.line("break;")
	case *ast.ContinueStatement:
		fc.continueStmt(s)
	case *ast.ThrowStatement:
		fc.throwStmt(s)
	case *ast.TryCatchStatement:
		fc.tryCatchStmt(s)
	case *ast.TransactionStatement:
		fc.transactionStmt(s)
	case *ast.BadStatement:
		fc.g.fail(errBad(s.Pos(), badStatement))
	default:
		fc.g.fail(notImplemented(s.Pos(), "statement"))
	}
}

// stmts lowers a list of statements in order and reports whether the last
// one is an unconditional jump (a return, so no fallthrough), used to decide
// the missing-return fallback (plan D24).
func (fc *funcCtx) stmts(list []ast.Statement) (returned bool) {
	for _, s := range list {
		fc.stmt(s)
		returned = isJump(s)
	}
	return returned
}

// isJump reports whether s is an unconditional control-flow exit at the end
// of a block: a return statement, or a throw (which lowers to an
// unconditional goto to its handler) (plan D24).
func isJump(s ast.Statement) bool {
	switch s.(type) {
	case *ast.ReturnStatement, *ast.ThrowStatement:
		return true
	}
	return false
}

// blockStmt lowers a braced block as a nested C block with its own scope
// (codegen design §5.2 step 2, plan D5): the braces are written as lines of
// the enclosing block; the child block (opened by enter) holds the hoisted
// declarations and the deferred (void) fragments and is rendered one level
// deeper by the writer.
func (fc *funcCtx) blockStmt(s *ast.BlockStatement) {
	fc.blk.line("{")
	fc.enter()
	fc.stmts(s.Statements)
	fc.leave()
	fc.blk.line("}")
}

// letStmt lowers a let declaration (codegen design §5.2 step 2, §5.4, plan
// D5, D10): the variable's declaration is hoisted to the top of the block;
// the assignment of its initializer (or its zero value when uninitialized)
// happens where the let stands. A void binding is §12 item 3. A deferred
// (void) fragment is inserted so a variable written but never read is not a
// -Werror=unused-but-set-variable.
func (fc *funcCtx) letStmt(s *ast.LetStatement) {
	obj := fc.g.info.Defs[s.Name]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(s.Pos(), "let %s is not a variable", s.Name.Name))
	}
	t := fc.concrete(vr.Type)
	if types.IsVoid(t) {
		fc.g.fail(errVoidValue(s.Pos(), "local "+vr.Name))
	}
	name := fc.declare(vr, fc.g.ctype(t))
	if s.Value == nil {
		fc.blk.linef("%s = %s;", name, fc.g.zeroValue(t))
	} else {
		v := fc.storeValue(s.Value, vr.Kind == types.GlobalVar)
		fc.blk.linef("%s = %s;", name, v.code)
	}
	fc.markWritten(vr)
	fc.deferVoid(fc.blk, vr, name)
}

// deferVoid inserts a deferred fragment into blk that becomes "(void)<name>;"
// when the block closes iff vr was written but never read in the emitted C
// (plan D10). A read inside a catch body that D9 discarded never happens,
// because that code is not lowered.
func (fc *funcCtx) deferVoid(blk *block, vr *types.Var, name string) {
	blk.deferred(func() string {
		if fc.written[vr] && !fc.read[vr] {
			return "(void)" + name + ";"
		}
		return ""
	})
}

// returnStmt lowers a return (codegen design §5.2, §8.6): a value return
// evaluates its expression with the recorded ConvToOptional widening and
// returns it; a bare return is "return;". The function result type is the
// concrete type, so an instance returns the substituted type.
func (fc *funcCtx) returnStmt(s *ast.ReturnStatement) {
	if s.Value == nil {
		if fc.hasAfter {
			fc.blk.line("goto __after;")
			return
		}
		fc.blk.line("return;")
		return
	}
	v := fc.value(s.Value)
	if fc.hasAfter {
		// Store the value, then converge on __after so the hooks fire before
		// the single real return (this design, §5.3).
		fc.blk.linef("%s = %s;", fc.afterRetName, v.code)
		fc.blk.line("goto __after;")
		return
	}
	fc.blk.linef("return %s;", v.code)
}

// exprStmt lowers an expression used as a statement (codegen design §7, §8.10):
// an assignment, or a call for its effect (a void call is a bare statement, a
// discarded non-void call casts to void, plan D8).
func (fc *funcCtx) exprStmt(s *ast.ExpressionStatement) {
	switch e := s.Expression.(type) {
	case *ast.AssignmentExpression:
		fc.assign(e)
	case *ast.CallExpression:
		fc.callStmt(e)
	default:
		// Any other expression statement (a bare value) is lowered and
		// discarded with a (void) cast to keep -Wunused-value quiet.
		v := fc.value(s.Expression)
		fc.blk.linef("(void)(%s);", v.code)
	}
}

// callStmt lowers a call used as a statement (codegen design §7, §8.8, plan
// D8): a user call is discarded (bare or (void) cast) and checked; a builtin
// follows its own effect lowering.
func (fc *funcCtx) callStmt(e *ast.CallExpression) {
	c := fc.g.info.Calls[e]
	if c == nil {
		fc.g.fail(internalErr(e.Pos(), "call has no call info"))
	}
	switch c.Kind {
	case types.CallFunc, types.CallMethod:
		fc.userCall(e, c, true)
	case types.CallBuiltin:
		fc.callBuiltin(e, c, true)
	case types.CallConversion:
		// A conversion for its effect only: lower and discard.
		v := fc.conversion(e, c)
		fc.blk.linef("(void)(%s);", v.code)
	default:
		fc.g.fail(internalErr(e.Pos(), "unknown call kind in statement"))
	}
}

// assignNeedsStmts reports whether lowering the assignment a emits statements
// (plan D7): when its value needs statements, when a member or index target
// needs statements (F4), or when it is an integer /= or %= with a non-nonzero
// constant divisor, which spills a may-fail temporary (plan D8). A plain or
// pure-compound identifier assignment emits none, so it is a valid pure for
// post.
func (fc *funcCtx) assignNeedsStmts(a *ast.AssignmentExpression) bool {
	if _, ok := a.Target.(*ast.Identifier); !ok {
		return true // member/index targets are lowered with statements (F4)
	}
	if a.Value != nil && fc.needsStmts(a.Value) {
		return true
	}
	op := a.BinaryOp()
	if op == token.SLASH || op == token.MOD {
		id := a.Target.(*ast.Identifier)
		if vr, ok := fc.g.info.Uses[id].(*types.Var); ok {
			t := fc.concrete(vr.Type)
			if types.IsInteger(t) && !fc.nonzeroConst(a.Value) {
				return true
			}
		}
	}
	return false
}

// assign lowers an assignment statement (codegen design §8.10, plan D16,
// D25): an identifier target (F3) stores directly; a member or index target
// (F4) is sequenced in four steps (receiver, index, value, store). The
// lowered store is appended to the innermost block.
func (fc *funcCtx) assign(a *ast.AssignmentExpression) {
	switch target := a.Target.(type) {
	case *ast.Identifier:
		fc.blk.line(fc.assignIdent(target, a) + ";")
	case *ast.MemberExpression:
		fc.assignMember(target, a)
	case *ast.IndexExpression:
		fc.assignIndex(target, a)
	default:
		fc.g.fail(notImplemented(a.Pos(), "assignment target"))
	}
}

// assignMember lowers a store to a member target (codegen design §8.10, plan
// D16): a user field (<recv>-><f> = <v>) or an Error field (.message/.status
// as an lvalue). The receiver is spilled when it needs statements, or when it
// is unstable and the value needs statements; an Error member whose receiver
// is not a plain identifier is spilled once and reused for read and write
// (COVERAGE-NIT-6). A plain store into a spilled Error temporary is followed
// by (void)__tN; (P5), because the store is otherwise dead.
func (fc *funcCtx) assignMember(target *ast.MemberExpression, a *ast.AssignmentExpression) {
	sel := fc.g.info.Selections[target]
	if sel == nil {
		fc.g.fail(internalErr(target.Pos(), "member target %s has no selection", target.Property.Name))
	}
	switch sel.Kind {
	case types.SelField:
		fc.assignUserField(target, sel, a)
	case types.SelBuiltin:
		fc.assignErrorMember(target, sel, a)
	default:
		fc.g.fail(notImplemented(target.Pos(), "member assignment target"))
	}
}

// assignUserField lowers a store to a user interface field (codegen design
// §8.10, plan D16): the receiver is spilled per step 1, then one store
// <recv>-><f> = <v>, or a compound read-modify-write reusing the spilled
// receiver.
func (fc *funcCtx) assignUserField(target *ast.MemberExpression, sel *types.Selection, a *ast.AssignmentExpression) {
	recv := fc.storeRecv(target.Object, a)
	loc := recv.operand() + "->" + fieldName(sel.Field.Name)
	ft := fc.concrete(sel.Field.Type)
	fc.storeToLoc(loc, ft, a, "", fc.globalRoot(target.Object))
}

// assignErrorMember lowers a store to an Error .message or .status member
// (codegen design §8.10, plan D16, COVERAGE-NIT-6, P5). The receiver is
// spilled once when it is not a plain identifier and reused for read and
// write. A plain store into a spilled temporary is dead (the temporary is a
// copy of the Error value), so it is followed by (void)__tN; (P5).
func (fc *funcCtx) assignErrorMember(target *ast.MemberExpression, sel *types.Selection, a *ast.AssignmentExpression) {
	// The receiver must be an lvalue reused for read and write
	// (COVERAGE-NIT-6). value() already materializes a call result into a
	// stable temporary; only an unstable value (e.g. a global read) needs a
	// further spill. A spilled by-value Error temporary is a copy, so a plain
	// store into one of its fields is dead (P5).
	recv := fc.value(target.Object)
	if !recv.stable {
		recv = fc.spill(recv, fc.typ(target.Object))
	}
	// P5: when the receiver lowered to a fresh by-value Error temporary (a
	// call result or an index read, both copies), a plain store into one of
	// its fields is dead; mark the temporary used. A live lvalue path
	// (l_e, l_obj->f_err) is not a temporary and needs no (void).
	spilledTemp := ""
	if isTempName(recv.code) {
		spilledTemp = recv.code
	}
	var field string
	var ft types.Type
	switch sel.Builtin {
	case types.BuiltinErrorMessage:
		field, ft = "message", types.Typ[types.String]
	case types.BuiltinErrorStatus:
		field, ft = "status", types.Typ[types.Int32]
	case types.BuiltinErrorCategory:
		field, ft = "category", types.Typ[types.String]
	case types.BuiltinErrorCode:
		field, ft = "code", types.Typ[types.String]
	default:
		fc.g.fail(internalErr(target.Pos(), "unsupported Error member %s", sel.Builtin.String()))
	}
	loc := recv.operand() + "." + field
	// P5: a plain store into a field of a spilled by-value Error temporary is
	// a dead store (the temporary is a copy); mark it used. A compound store
	// reads the field, so it needs no (void).
	plain := a.BinaryOp() == "" && !a.IsIncDec()
	fc.storeToLoc(loc, ft, a, "", false)
	if spilledTemp != "" && plain {
		fc.blk.linef("(void)%s;", spilledTemp)
	}
}

// assignIndex lowers a store to an index target (codegen design §8.10, plan
// D16): the slice and index are spilled per steps 1-2 (with the index bounds
// check before the value when the value needs statements), then one store
// TLANG_SLICE_AT(...) = <v> with its own check, or a compound read-modify-
// write reusing the spilled temporaries.
func (fc *funcCtx) assignIndex(target *ast.IndexExpression, a *ast.AssignmentExpression) {
	elem := fc.typ(target)
	valNeeds := a.Value != nil && fc.needsStmts(a.Value)

	// Step 1: the TLANG_SLICE_AT macro reads its slice argument twice, so the
	// slice must be a stable side-effect-free path; spill it when it is not
	// already stable (a call result from value() is already a temporary).
	slice := fc.value(target.Left)
	if !slice.stable {
		slice = fc.spill(slice, fc.typ(target.Left))
	}

	// Step 2: the index is spilled when it is not already stable and the
	// value needs statements, so its value is read before the value's side
	// effects; the bounds check is emitted before the value then, because the
	// index access is may-fail and its check must fire at its source point.
	idx := fc.value(target.Index)
	preChecked := false
	if valNeeds && !idx.stable {
		idx = fc.spill(idx, fc.typ(target.Index))
	}
	if valNeeds {
		fc.blk.linef("if (!tlang_index_ok(__fib, %s, %s->len)) goto %s;", idx.operand(), slice.code, fc.failTarget())
		preChecked = true
	}

	loc := "TLANG_SLICE_AT(__fib, " + slice.code + ", " + macroArg(idx) + ", " + fc.g.ctype(elem) + ")"
	fc.storeIndex(loc, elem, a, preChecked, fc.globalRoot(target.Left))
}

// storeRecv lowers an assignment target's receiver and spills it to a
// temporary when it needs statements, or when it is unstable and the value
// needs statements (codegen design §8.10 step 1).
func (fc *funcCtx) storeRecv(obj ast.Expression, a *ast.AssignmentExpression) cval {
	v := fc.value(obj)
	if !v.stable {
		return fc.spill(v, fc.typ(obj))
	}
	return v
}

// storeToLoc emits the store of a plain or compound assignment to a non-
// may-fail C lvalue loc of concrete type t (codegen design §8.10, plan D16):
// a plain "=", a compound read-modify-write (which checks internally when it
// is a may-fail div/mod), or ++/--. unusedTemp, when non-empty, is a spilled
// temporary whose dead plain store is marked (void) (P5); callers that need
// that pass it, others pass "".
func (fc *funcCtx) storeToLoc(loc string, t types.Type, a *ast.AssignmentExpression, unusedTemp string, globalDest bool) {
	op := a.BinaryOp()
	switch {
	case op == "" && !a.IsIncDec():
		v := fc.storeValue(a.Value, globalDest)
		fc.blk.linef("%s = %s;", loc, v.code)
	case a.IsIncDec():
		fc.blk.linef("%s = %s;", loc, fc.incDec(loc, t, op))
	default:
		fc.blk.line(fc.compoundStore(loc, nil, t, op, a.Value) + ";")
	}
	if unusedTemp != "" {
		fc.blk.linef("(void)%s;", unusedTemp)
	}
}

// storeIndex emits the store of a plain or compound assignment to an index
// lvalue loc (codegen design §8.10, plan D16): a plain store followed by its
// may-fail check (unless the bounds were pre-checked in step 2), or a
// compound read-modify-write. The index store is itself the may-fail
// operation, so a plain store checks after it.
func (fc *funcCtx) storeIndex(loc string, t types.Type, a *ast.AssignmentExpression, preChecked, globalDest bool) {
	op := a.BinaryOp()
	switch {
	case op == "" && !a.IsIncDec():
		v := fc.storeValue(a.Value, globalDest)
		fc.blk.linef("%s = %s;", loc, v.code)
		if !preChecked {
			fc.check()
		}
	case a.IsIncDec():
		fc.blk.linef("%s = %s;", loc, fc.incDec(loc, t, op))
		fc.check()
	default:
		fc.blk.line(fc.compoundStore(loc, nil, t, op, a.Value) + ";")
		fc.check()
	}
}

// assignExprPure returns the C text of a pure identifier assignment without
// the trailing semicolon, for a for-post clause (plan D11). A member or index
// target is not a valid pure post in F3.
func (fc *funcCtx) assignExprPure(a *ast.AssignmentExpression) string {
	id, ok := a.Target.(*ast.Identifier)
	if !ok {
		fc.g.fail(notImplemented(a.Pos(), "assignment target"))
	}
	return fc.assignIdent(id, a)
}

// assignIdent lowers an assignment whose target is a local, parameter,
// receiver or global identifier and returns the store expression without the
// trailing semicolon (codegen design §8.10, plan D16, D25). The target is
// marked written (and read, for a compound assignment or ++/--, which read
// the old value). A global target's implicit read is unstable and is spilled
// when the value needs statements.
func (fc *funcCtx) assignIdent(id *ast.Identifier, a *ast.AssignmentExpression) string {
	loc, vr, t := fc.lvalue(id)
	op := a.BinaryOp()

	// Plain store.
	if op == "" {
		return fc.plainStore(id, loc, vr, t, a.Value)
	}

	// Compound assignment and ++/-- read the old value.
	if vr != nil {
		fc.markRead(vr)
	}
	fc.markWritten(vr)

	if a.IsIncDec() {
		return loc + " = " + fc.incDec(loc, t, op)
	}
	return fc.compoundStore(loc, vr, t, op, a.Value)
}

// lvalue returns the C location of an identifier target, its *Var (nil never
// happens for a well-typed target) and its concrete type (codegen design
// §8.1): a local/param/receiver is l_x, a global __fib->globals->g_x.
func (fc *funcCtx) lvalue(id *ast.Identifier) (loc string, vr *types.Var, t types.Type) {
	obj := fc.g.info.Uses[id]
	v, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(id.Pos(), "assignment target %s is not a variable", id.Name))
	}
	t = fc.concrete(v.Type)
	if v.Kind == types.GlobalVar {
		return "__fib->globals->" + globalName(v), v, t
	}
	name, ok := fc.locals.name(v)
	if !ok {
		fc.g.fail(internalErr(id.Pos(), "variable %s was not named", id.Name))
	}
	return name, v, t
}

// plainStore lowers "<loc> = <value>" (codegen design §8.10, §8.5, plan D16,
// D25). A self-assign x = x gets the §8.5 treatment (a scalar cast, or a
// (void) for a struct C type). A global store whose value needs statements
// has no unstable left read, so no spill is needed here (the target is the
// destination, not an operand).
func (fc *funcCtx) plainStore(id *ast.Identifier, loc string, vr *types.Var, t types.Type, value ast.Expression) string {
	if sid, ok := value.(*ast.Identifier); ok && fc.sameExpr(id, sid) {
		fc.markWritten(vr)
		fc.markRead(vr)
		return fc.selfAssign(loc, t)
	}
	fc.markWritten(vr)
	globalDest := vr != nil && vr.Kind == types.GlobalVar
	v := fc.storeValue(value, globalDest)
	return loc + " = " + v.code
}

// selfAssign lowers x = x (codegen design §8.5, plan D25): a scalar C type is
// re-cast to itself, "l_x = (<CType>)l_x", so the self-assign is intentional
// under the pragma; a struct C type (string, optional, error) cannot be
// re-cast, so it becomes "(void)l_x" (the assignment is a no-op). The caller
// adds the trailing semicolon.
func (fc *funcCtx) selfAssign(loc string, t types.Type) string {
	if isScalarCType(t) {
		return loc + " = (" + fc.g.ctype(t) + ")" + loc
	}
	return "(void)" + loc
}

// isScalarCType reports whether the C type of t is a scalar (integers,
// double, bool, and pointers: interfaces, arrays, Context, Transaction and
// their optionals), as opposed to a struct value (tlang_string, tlang_opt_*,
// tlang_error) (codegen design §8.5, plan D25).
func isScalarCType(t types.Type) bool {
	if b, ok := t.(*types.Basic); ok {
		switch b.Kind {
		case types.Int32, types.Int64, types.Float64, types.Bool,
			types.Context, types.Transaction:
			return true
		case types.String, types.Error:
			return false
		}
	}
	if types.IsPrimitiveOptional(t) {
		return false // tlang_opt_* is a struct
	}
	if types.IsString(types.NonOptional(t)) {
		return false // string|null reuses tlang_string, a struct
	}
	// Interface pointer, array pointer, and reference optionals: scalar.
	return true
}

// incDec returns the right-hand side of a ++/-- store (codegen design §8.10,
// plan D16): an integer uses the wrapping add helper with 1, a float uses the
// C "+ 1.0" / "- 1.0".
func (fc *funcCtx) incDec(loc string, t types.Type, op token.TokenType) string {
	if types.IsFloat(t) {
		if op == token.PLUS {
			return loc + " + 1.0"
		}
		return loc + " - 1.0"
	}
	kind := types.Int64
	if types.IsBasic(t, types.Int32) {
		kind = types.Int32
	}
	base := "tlang_add"
	if op == token.MINUS {
		base = "tlang_sub"
	}
	return intHelper(base, kind) + "(" + loc + ", 1)"
}

// compoundStore lowers "<loc> <op>= <value>" (codegen design §8.3, §8.10,
// plan D16): the §8.3 helper combines the old value and the new one. An
// integer /= or %= whose divisor is not a nonzero constant is a may-fail
// operation, computed into a temporary first (plan D8); a float %= uses
// tlang_mod_f64. The caller adds the semicolon.
func (fc *funcCtx) compoundStore(loc string, vr *types.Var, t types.Type, op token.TokenType, value ast.Expression) string {
	old := atom(loc)
	v := fc.value(value)

	switch op {
	case token.PLUS, token.MINUS, token.ASTERISK:
		if op == token.PLUS && types.IsString(t) {
			// String += is concatenation (codegen design §8.10, §8.3).
			return loc + " = tlang_str_concat(__fib, " + old.code + ", " + v.code + ")"
		}
		if types.IsFloat(t) {
			return loc + " = " + old.operand() + " " + binSym(op) + " " + v.operand()
		}
		kind := intKind(t)
		base := map[token.TokenType]string{token.PLUS: "tlang_add", token.MINUS: "tlang_sub", token.ASTERISK: "tlang_mul"}[op]
		return loc + " = " + intHelper(base, kind) + "(" + old.code + ", " + v.code + ")"
	case token.SLASH, token.MOD:
		if types.IsFloat(t) {
			if op == token.SLASH {
				return loc + " = " + old.operand() + " / " + v.operand()
			}
			return loc + " = tlang_mod_f64(" + old.code + ", " + v.code + ")"
		}
		kind := intKind(t)
		base := "tlang_div"
		if op == token.MOD {
			base = "tlang_mod"
		}
		code := intHelper(base, kind) + "(__fib, " + old.code + ", " + v.code + ")"
		if fc.nonzeroConst(value) {
			return loc + " = " + code
		}
		// May fail: compute into a temporary, check, then store (plan D8).
		res := fc.spill(opVal(code, false), t)
		fc.check()
		return loc + " = " + res.code
	}
	fc.g.fail(internalErr(fc.g.cur, "compound operator %s", op))
	return ""
}

// binSym returns the C symbol of an arithmetic operator token.
func binSym(op token.TokenType) string { return string(op) }

// isTempName reports whether code is a bare generated temporary name
// (__t<N>): a value spilled into a temporary, as opposed to an lvalue path
// into actual storage. Used to decide the P5 dead-store (void) for a by-value
// Error field store.
func isTempName(code string) bool {
	return strings.HasPrefix(code, "__t") && !strings.ContainsAny(code, ".->[](), ")
}

// intKind returns the integer width of t (Int32 or Int64) for a helper name.
func intKind(t types.Type) types.BasicKind {
	if types.IsBasic(t, types.Int32) {
		return types.Int32
	}
	return types.Int64
}

// ifStmt lowers an if/else-if/else (codegen design §7, plan D12). The
// condition's statements go before the if; an else whose statement is an if
// with a pure condition becomes "} else if (<c>) {"; a non-block branch body
// gets braces and its own scope.
func (fc *funcCtx) ifStmt(s *ast.IfStatement) {
	cond := fc.value(s.Condition)
	fc.blk.line("if (" + cond.code + ") {")
	fc.branch(s.Consequence)
	fc.lowerElse(s.Alternative)
}

// lowerElse lowers the else part of an if (plan D12): no else writes the
// closing brace; an else-if with a pure condition continues with
// "} else if (...) {"; any other else continues with "} else {". The brace
// that closes the preceding branch is on the same line as the else.
func (fc *funcCtx) lowerElse(alt ast.Statement) {
	if alt == nil {
		fc.blk.line("}")
		return
	}
	if elif, ok := alt.(*ast.IfStatement); ok && !fc.needsStmts(elif.Condition) {
		cond := fc.value(elif.Condition)
		fc.blk.line("} else if (" + cond.code + ") {")
		fc.branch(elif.Consequence)
		fc.lowerElse(elif.Alternative)
		return
	}
	fc.blk.line("} else {")
	fc.branch(alt)
	fc.blk.line("}")
}

// branch lowers the body of a controlled statement (an if arm) in a fresh
// scope rendered one level deeper (plan D5): a block statement's statements
// directly, any other statement as itself, so a let inside is hoisted into
// this branch, not the enclosing block.
func (fc *funcCtx) branch(s ast.Statement) {
	fc.enter()
	fc.lowerBodyInScope(s)
	fc.leave()
}

// whileStmt lowers a while loop (codegen design §7, plan D11). A pure
// condition is a plain "while (<c>)"; otherwise a "for (;;)" with the
// condition statements and an "if (!<c>) break;" at the top of the body.
func (fc *funcCtx) whileStmt(s *ast.WhileStatement) {
	fc.pushLoop("")
	if !fc.needsStmts(s.Condition) {
		cond := fc.value(s.Condition)
		fc.blk.line("while (" + cond.code + ") {")
		fc.enter()
		fc.lowerBodyInScope(s.Body)
		fc.leave()
		fc.blk.line("}")
		fc.popLoop()
		return
	}
	fc.blk.line("for (;;) {")
	fc.enter()
	cond := fc.value(s.Condition)
	fc.blk.linef("if (!%s) break;", cond.operand())
	fc.lowerBodyInScope(s.Body)
	fc.leave()
	fc.blk.line("}")
	fc.popLoop()
}

// lowerBodyInScope lowers a loop or branch body assuming the caller has
// already opened a scope: a block's statements directly, any other statement
// as itself.
func (fc *funcCtx) lowerBodyInScope(body ast.Statement) {
	if b, ok := body.(*ast.BlockStatement); ok {
		fc.stmts(b.Statements)
		return
	}
	fc.stmt(body)
}

// forStmt lowers a C-style for loop (codegen design §7, plan D11). An Init is
// lowered as statements in a header scope before the loop; the condition
// follows the while rule; the post is a pure assignment or value expression
// in the for header, or, when it needs statements, a continue label and post
// statements at the end of the body.
func (fc *funcCtx) forStmt(s *ast.ForStatement) {
	// Header scope for the init declarations (plan D5): a nested block whose
	// child holds the init lets, so the loop that reads them sits inside it.
	headerOpened := false
	if s.Init != nil {
		fc.blk.line("{")
		fc.enter()
		headerOpened = true
		fc.stmt(s.Init)
	}

	pureCond := s.Condition == nil || !fc.needsStmts(s.Condition)
	postNeeds := s.Post != nil && fc.needsStmts(s.Post)

	switch {
	case pureCond && !postNeeds:
		fc.forPlain(s)
	default:
		fc.forGeneral(s, pureCond)
	}

	if headerOpened {
		fc.leave()
		fc.blk.line("}")
	}
}

// forPlain lowers a for with a pure condition and a pure post as a C for
// header (plan D11): "for (; <c>; <post>)" with empty clauses written as
// "for (;;)".
func (fc *funcCtx) forPlain(s *ast.ForStatement) {
	cond := ""
	if s.Condition != nil {
		cond = fc.value(s.Condition).code
	}
	post := ""
	if s.Post != nil {
		post = fc.forPost(s.Post)
	}
	fc.pushLoop("")
	header := "for (;;) {"
	if cond != "" || post != "" {
		header = "for (; " + cond + "; " + post + ") {"
	}
	fc.blk.line(header)
	fc.enter()
	fc.lowerBodyInScope(s.Body)
	fc.leave()
	fc.blk.line("}")
	fc.popLoop()
}

// forGeneral lowers a for whose condition or post needs statements (plan
// D11): a "for (;;)" (or "for (; <c>; )" when the condition is pure), the
// may-fail condition test at the top of the body, the body, then the
// continue label (if any continue used it) and the post statements.
func (fc *funcCtx) forGeneral(s *ast.ForStatement, pureCond bool) {
	postNeeds := s.Post != nil && fc.needsStmts(s.Post)
	contLabel := ""
	if postNeeds {
		contLabel = "__cont_" + itoa(fc.nextLabel())
	}
	loop := fc.pushLoop(contLabel)

	header := "for (;;) {"
	if pureCond && s.Condition != nil {
		header = "for (; " + fc.value(s.Condition).code + "; ) {"
	}
	fc.blk.line(header)
	fc.enter()
	if !pureCond {
		cond := fc.value(s.Condition)
		fc.blk.linef("if (!%s) break;", cond.operand())
	}
	fc.lowerBodyInScope(s.Body)
	if contLabel != "" && loop.contUsed {
		fc.blk.label(contLabel)
	}
	if s.Post != nil {
		fc.postStmts(s.Post)
	}
	fc.leave()
	fc.blk.line("}")
	fc.popLoop()
}

// forPost returns the C text of a pure for-post clause (plan D11, §13 rule
// 14): a pure assignment is its store expression without the trailing
// semicolon; a pure value expression is "(void)(<e>)".
func (fc *funcCtx) forPost(e ast.Expression) string {
	if a, ok := e.(*ast.AssignmentExpression); ok {
		return fc.assignExprPure(a)
	}
	v := fc.value(e)
	return "(void)(" + v.code + ")"
}

// postStmts lowers a for-post that needs statements as ordinary statements at
// the end of the body (plan D11).
func (fc *funcCtx) postStmts(e ast.Expression) {
	if a, ok := e.(*ast.AssignmentExpression); ok {
		fc.assign(a)
		return
	}
	v := fc.value(e)
	fc.blk.linef("(void)(%s);", v.code)
}

// forOfStmt lowers a for-of loop (codegen design §7, plan D11, D10). The
// index runs over the iterable's elements: a counted C for whose len/items
// are re-read every iteration through the slice pointer (so a push in the
// body is seen). The slice is used directly when the iterable is a
// local/parameter identifier that is not reassigned in the body; anything
// else (a call, a field path, a global, a literal, a reassigned local) is
// spilled once into a temporary before the loop. The body is a C block that
// declares the loop variable, whose first statement reads the current
// element (a raw read, never bounds-checked).
func (fc *funcCtx) forOfStmt(s *ast.ForOfStatement) {
	obj := fc.g.info.Defs[s.Var]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(s.Pos(), "for-of variable %s is not a variable", s.Var.Name))
	}
	elem := fc.concrete(vr.Type)

	slice := fc.forOfSlice(s)
	i := "__i" + itoa(fc.nextTmp())
	fc.blk.linef("for (int64_t %s = 0; %s < %s->len; %s++) {", i, i, slice, i)
	fc.enter()
	name := fc.declare(vr, fc.g.ctype(elem))
	fc.blk.linef("%s = %s->items[%s];", name, slice, i)
	fc.markWritten(vr)
	fc.deferVoid(fc.blk, vr, name)
	fc.pushLoop("")
	fc.lowerBodyInScope(s.Body)
	fc.popLoop()
	fc.leave()
	fc.blk.line("}")
}

// forOfSlice returns the C text of the iterable's slice (codegen design §7,
// plan D11): used directly when the iterable is a local/parameter identifier
// not reassigned in the loop body, otherwise spilled once into a temporary
// before the loop.
func (fc *funcCtx) forOfSlice(s *ast.ForOfStatement) string {
	if id, ok := s.Iterable.(*ast.Identifier); ok {
		if vr, ok := fc.g.info.Uses[id].(*types.Var); ok &&
			vr.Kind != types.GlobalVar && !fc.reassignedIn(s.Body, vr) {
			return fc.value(id).code
		}
	}
	// Anything else (a call, field path, global, literal, reassigned local)
	// is spilled once into a temporary before the loop, so len/items are
	// re-read through that pointer each iteration (plan D11). A value already
	// lowered to a bare temporary (a call or literal result) is reused.
	v := fc.value(s.Iterable)
	if isTempName(v.code) {
		return v.code
	}
	return fc.spill(v, fc.typ(s.Iterable)).code
}

// reassignedIn reports whether the statement body assigns to the variable vr
// (an identifier target), so a for-of iterable that is that variable must be
// spilled before the loop (plan D11).
func (fc *funcCtx) reassignedIn(body ast.Statement, vr *types.Var) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignmentExpression)
		if !ok {
			return true
		}
		if id, ok := a.Target.(*ast.Identifier); ok && fc.g.info.Uses[id] == vr {
			found = true
		}
		return true
	})
	return found
}

// continueStmt lowers a continue (plan D11): a plain C continue for a loop
// whose post is in the header, or a goto to the loop's continue label when
// the post needs statements.
func (fc *funcCtx) continueStmt(s *ast.ContinueStatement) {
	if n := len(fc.loops); n > 0 {
		l := fc.loops[n-1]
		if l.contLabel != "" {
			l.contUsed = true
			fc.blk.linef("goto %s;", l.contLabel)
			return
		}
	}
	fc.blk.line("continue;")
}

// itoa is strconv.Itoa under a short name for label building.
func itoa(n int) string { return strconv.Itoa(n) }

// throwStmt lowers a throw (codegen design §7 Throw, plan D9): an Error value
// becomes tlang_throw_value(__fib, <v>); a string or new Error(m[, s]) (also
// new global Error) becomes tlang_throw_typed with internal/internal defaults. Then an
// unconditional goto to the nearest handler, which marks it used. A throw
// counts as an unconditional jump for the missing-return fallback (D24).
func (fc *funcCtx) throwStmt(s *ast.ThrowStatement) {
	fc.g.cur = s.Pos()
	t := fc.typ(s.Value)
	if types.IsBasic(t, types.Error) {
		if ne, ok := s.Value.(*ast.NewExpression); ok {
			// throw new Error(m[, s]) uses tlang_throw with the message and
			// status directly, rather than building an Error value first.
			msg, status, category, code := fc.throwNewErrorParts(ne)
			fc.blk.linef("tlang_throw_typed(__fib, %s, %s, %s, %s);", status, msg, category, code)
		} else {
			v := fc.value(s.Value)
			fc.blk.linef("tlang_throw_value(__fib, %s);", v.code)
		}
	} else {
		// throw "msg" (a string): status 500, the string as the message.
		v := fc.value(s.Value)
		fc.blk.linef("tlang_throw_typed(__fib, 500, %s, TLANG_STR(\"internal\"), TLANG_STR(\"internal\"));", v.code)
	}
	fc.blk.linef("goto %s;", fc.failTarget())
}

// throwNewErrorParts returns the message and status C expressions of a
// throw new Error(m[, s]) / throw new global Error(m[, s]) (codegen design §7,
// §8.9): the message defaults to the empty string, the status to 500. The
// Global flag has no effect (Error is a by-value tlang_error).
func (fc *funcCtx) throwNewErrorParts(e *ast.NewExpression) (msg, status, category, code string) {
	msg = `TLANG_STR("")`
	status = "500"
	category = `TLANG_STR("internal")`
	code = `TLANG_STR("internal")`
	if len(e.Args) >= 1 {
		msg = fc.value(e.Args[0]).code
	}
	if len(e.Args) >= 2 {
		status = fc.value(e.Args[1]).code
	}
	return msg, status, category, code
}

// tryCatchStmt lowers try { B } catch (e) { C } (codegen design §6.3, plan
// D9). The catch label is allocated and pushed as a handler, the try body B
// is lowered, and the handler is popped. When the label was used (some
// may-fail op, throw or transaction inside B targeted it, the
// handlerTargetsIn(B) predicate), the full scaffolding is emitted: a goto to
// the try-end label after B, the catch label, the binding, the catch body C
// (lowered with the next outer handler), and the try-end label. When the
// label was unused, B is emitted inline and C is discarded (dead code that
// would reference an undefined label).
func (fc *funcCtx) tryCatchStmt(s *ast.TryCatchStatement) {
	fc.g.cur = s.Pos()
	k := fc.nextLabel()
	catchLabel := "__tlang_catch_" + itoa(k)
	endLabel := "__tlang_try_end_" + itoa(k)

	// Lower B as a child block with the catch label on the handler stack.
	fc.blk.line("{")
	h := fc.pushHandler(catchLabel)
	fc.enter()
	fc.stmts(s.Body.Statements)
	if h.used {
		fc.blk.linef("goto %s;", endLabel)
	}
	fc.leave()
	fc.popHandler()
	fc.blk.line("}")

	if !h.used {
		// handlerTargetsIn(B) is false: B cannot reach the catch, so the
		// label would be unused and C is dead. Discard the catch body.
		return
	}

	// The catch label, then the binding and the catch body in their own
	// braced C block so the binding is scoped to the catch (plan D5: the
	// catch body is a C block). Sibling catches each get their own scope, so
	// two sibling catch (e) both name l_e; nested ones get l_e and l_e__2.
	// The catch body is lowered with the next outer handler (the inner one
	// was popped above). The try-end label follows (codegen design §6.3).
	fc.blk.label(catchLabel)
	fc.blk.line("{")
	fc.enter()
	fc.catchBinding(s)
	fc.stmts(s.CatchBody.Statements)
	fc.leave()
	fc.blk.line("}")
	fc.blk.label(endLabel)
}

// catchBinding lowers the catch binding (codegen design §6.3): catch (e)
// declares an Error local initialized from tlang_take_error, with a deferred
// (void) fragment when it is never read (an unread tlang_error local is
// -Werror=unused-variable); catch {} clears the error without a binding.
func (fc *funcCtx) catchBinding(s *ast.TryCatchStatement) {
	if s.CatchParam == nil {
		fc.blk.line("tlang_clear_error(__fib);")
		return
	}
	obj := fc.g.info.Defs[s.CatchParam]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(s.CatchParam.Pos(), "catch binding %s is not a variable", s.CatchParam.Name))
	}
	name := fc.declare(vr, fc.g.ctype(fc.concrete(vr.Type)))
	fc.blk.linef("%s = tlang_take_error(__fib);", name)
	fc.markWritten(vr)
	fc.deferVoid(fc.blk, vr, name)
}

// transactionStmt lowers db.transaction((tx) => { B }) (codegen design §7.1,
// plan D9). The fixed label scheme of §7.1: begin, the handle assignment, the
// body B with the rollback label on the handler stack, commit, and the
// rollback/fail/end labels. __tx_fail_N jumps to the nearest handler outside
// the transaction (resolved after the rollback label is popped).
func (fc *funcCtx) transactionStmt(s *ast.TransactionStatement) {
	fc.g.cur = s.Pos()
	n, ok := fc.g.info.TxIDs[s]
	if !ok {
		fc.g.fail(internalErr(s.Pos(), "transaction has no id"))
	}
	ns := itoa(n)
	txCtx := "__tx_" + ns
	rollback := "__tx_rollback_" + ns
	fail := "__tx_fail_" + ns
	end := "__tx_end_" + ns

	fc.blk.line("{")
	fc.enter()

	// The handle local from Defs[stmt.Param] (type Transaction, const).
	obj := fc.g.info.Defs[s.Param]
	vr, ok := obj.(*types.Var)
	if !ok {
		fc.g.fail(internalErr(s.Param.Pos(), "transaction handle %s is not a variable", s.Param.Name))
	}
	handle := fc.declare(vr, fc.g.ctype(fc.concrete(vr.Type)))

	fc.blk.linef("TxContext %s;", txCtx)
	fc.blk.linef("if (!tlang_tx_begin(__fib, &%s)) goto %s;", txCtx, fail)
	fc.blk.linef("%s = &%s;", handle, txCtx)
	fc.markWritten(vr)
	fc.deferVoid(fc.blk, vr, handle)

	// Lower B with __tx_rollback_N on the handler stack: tx.* data methods
	// and throws inside B target the rollback label.
	fc.pushHandler(rollback)
	fc.stmts(s.Body.Statements)
	fc.popHandler()

	fc.blk.linef("if (!tlang_tx_commit(__fib, &%s)) goto %s;", txCtx, rollback)
	fc.blk.linef("goto %s;", end)
	fc.blk.label(rollback)
	fc.blk.linef("tlang_tx_rollback(__fib, &%s);", txCtx)
	fc.blk.label(fail)
	// __tx_fail_N falls through to the nearest handler outside this
	// transaction (now that the rollback label has been popped): a catch
	// label or __fail. This marks that outer target used.
	fc.blk.linef("goto %s;", fc.failTarget())
	fc.blk.label(end)

	fc.leave()
	fc.blk.line("}")
}
