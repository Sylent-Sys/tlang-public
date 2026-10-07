package checker

import (
	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// mayfail.go is the may-fail analysis of DESIGN.md §2.8. It has two layers:
// per-expression Info.MayFail marking (the structural fallible operations and
// each call), and the Func.MayFail call-graph fixed point. It also numbers
// Info.TxIDs and runs the transaction-rule checks (E-TX). It reads
// BuiltinID.MayFail() (which returns only Fails, never Allocates), so
// allocating-but-not-failing builtins are correctly treated as cannot-fail.

// markMayFail is pass 5. It marks every structurally fallible expression,
// computes the Func.MayFail fixed point over the call graph, backfills each
// call's MayFail so Call.MayFail == Info.MayFail[call], numbers the
// transaction statements, and runs the transaction-rule checks. It replaces
// the FEAT-001 no-op hook.
func (c *checker) markMayFail() {
	c.markExprMayFail()
	c.computeFuncMayFail()
	c.backfillCallMayFail()
	c.numberTransactions()
	c.checkTransactionRules()
}

// markExprMayFail walks every function body and global initializer and marks
// the structurally fallible expressions in Info.MayFail: NonNullExpression
// and IndexExpression always; integer / and % (and /= %=) unless the divisor
// is a nonzero constant. Calls are marked later (they depend on the
// Func.MayFail fixed point).
func (c *checker) markExprMayFail() {
	for _, g := range c.globals {
		if let, ok := g.Decl.(*ast.LetStatement); ok && let.Value != nil {
			c.markExprsIn(let.Value)
		}
	}
	for _, d := range c.funcs {
		if d.stmt.Body != nil {
			c.markExprsIn(d.stmt.Body)
		}
	}
}

// markExprsIn marks the structural may-fail expressions reachable from root.
func (c *checker) markExprsIn(root ast.Node) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.NonNullExpression:
			c.info.MayFail[n] = true
		case *ast.IndexExpression:
			c.info.MayFail[n] = true
		case *ast.InfixExpression:
			if c.intDivMayFail(n.Operator, n.Left, n.Right) {
				c.info.MayFail[n] = true
			}
		case *ast.AssignmentExpression:
			if c.intDivMayFail(n.BinaryOp(), n.Target, n.Value) {
				c.info.MayFail[n] = true
			}
		}
		return true
	})
}

// intDivMayFail reports whether an integer / or % (operator SLASH or MOD) on
// the given operands may fail: the operands are integer and the divisor is
// not a nonzero constant. Float / and % never fail. A non-integer or invalid
// result (recorded by pass 3) is treated as not failing.
func (c *checker) intDivMayFail(op token.TokenType, left, right ast.Expression) bool {
	if op != token.SLASH && op != token.MOD {
		return false
	}
	if right == nil {
		return false
	}
	lt := c.operandType(left)
	rt := c.operandType(right)
	if !types.IsInteger(lt) || !types.IsInteger(rt) {
		return false
	}
	// A nonzero constant divisor cannot fail; folding would already have
	// reported E-CONST for a constant zero, so any non-nil divisor value
	// here is nonzero. A non-constant divisor may be zero at runtime.
	if rtv, ok := c.info.Types[right]; ok && rtv.IsConstant() {
		return false
	}
	return true
}

// operandType returns the recorded type of an operand expression: Info.Types
// for a value expression, or the resolved *Var's type for a plain identifier
// used as an assignment target (which pass 3 records in Info.Uses, not
// Info.Types).
func (c *checker) operandType(e ast.Expression) types.Type {
	if t := c.info.TypeOf(e); t != nil {
		return t
	}
	if id, ok := e.(*ast.Identifier); ok {
		if v, ok := c.info.Uses[id].(*types.Var); ok {
			return v.Type
		}
	}
	return nil
}

// funcByOrigin holds every non-instance function (origin or plain) for the
// fixed point; instances copy their origin's result.
func (c *checker) computeFuncMayFail() {
	origins := make([]*types.Func, 0, len(c.funcs))
	for _, d := range c.funcs {
		if d.fn.Sig != nil {
			origins = append(origins, d.fn)
		}
	}
	// Iterate to a fixed point: start every function at its current value
	// (false unless pass 2 copied an instance), recompute until stable.
	for {
		changed := false
		for _, fn := range origins {
			if fn.MayFail {
				continue
			}
			if c.funcBodyMayFail(fn) {
				fn.MayFail = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// Copy each origin's value to its instances (type args cannot change
	// what may fail).
	for _, fn := range c.info.Instances.FuncInstances() {
		if fn.Origin != nil {
			fn.MayFail = fn.Origin.MayFail
		}
	}
}

// funcBodyMayFail reports whether fn's body currently has a reason to fail: a
// guard that may fail, a throw, a may-fail-marked expression, a transaction
// statement, or a call to a may-fail function — each outside a try whose
// catch covers it (code in a catch block is outside its own try, since every
// catch catches everything).
func (c *checker) funcBodyMayFail(fn *types.Func) bool {
	for _, g := range fn.Guards {
		if g != nil && g.MayFail {
			return true
		}
	}
	if fn.Decl == nil || fn.Decl.Body == nil {
		return false
	}
	return c.blockMayFail(fn.Decl.Body)
}

// stmtMayFail reports whether a statement may fail at its own program point
// (its enclosing try is accounted for by the caller).
func (c *checker) stmtMayFail(s ast.Statement) bool {
	switch s := s.(type) {
	case *ast.BlockStatement:
		return c.blockMayFail(s)
	case *ast.ExpressionStatement:
		return c.exprMayFail(s.Expression)
	case *ast.LetStatement:
		return s.Value != nil && c.exprMayFail(s.Value)
	case *ast.ReturnStatement:
		return s.Value != nil && c.exprMayFail(s.Value)
	case *ast.ThrowStatement:
		return true
	case *ast.IfStatement:
		if c.exprMayFail(s.Condition) || c.stmtMayFail(s.Consequence) {
			return true
		}
		return s.Alternative != nil && c.stmtMayFail(s.Alternative)
	case *ast.WhileStatement:
		return c.exprMayFail(s.Condition) || c.stmtMayFail(s.Body)
	case *ast.ForStatement:
		if s.Init != nil && c.stmtMayFail(s.Init) {
			return true
		}
		if s.Condition != nil && c.exprMayFail(s.Condition) {
			return true
		}
		if s.Post != nil && c.exprMayFail(s.Post) {
			return true
		}
		return c.stmtMayFail(s.Body)
	case *ast.ForOfStatement:
		return c.exprMayFail(s.Iterable) || c.stmtMayFail(s.Body)
	case *ast.TryCatchStatement:
		// The protected Body cannot propagate failure out of the try (its
		// catch covers everything). The catch body is outside its own try,
		// so its failures do propagate.
		return s.CatchBody != nil && c.blockMayFail(s.CatchBody)
	case *ast.TransactionStatement:
		// A transaction statement may fail (BEGIN, COMMIT) and its body is
		// not inside a try.
		return true
	}
	return false
}

// blockMayFail reports whether any statement in a block may fail.
func (c *checker) blockMayFail(b *ast.BlockStatement) bool {
	if b == nil {
		return false
	}
	for _, s := range b.Statements {
		if c.stmtMayFail(s) {
			return true
		}
	}
	return false
}

// exprMayFail reports whether an expression (or any sub-expression) may fail:
// a structurally-marked expression, or a call to a may-fail function or
// builtin. It reads Func.MayFail (current fixed-point value) for user calls.
func (c *checker) exprMayFail(e ast.Expression) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		ex, ok := n.(ast.Expression)
		if ok && c.info.MayFail[ex] {
			found = true
			return false
		}
		if call, ok := n.(*ast.CallExpression); ok {
			if c.callMayFail(call) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// callMayFail reports whether a call expression may fail: a user callee's
// Func.MayFail (current value) or a builtin's BuiltinID.MayFail (Fails only).
func (c *checker) callMayFail(e *ast.CallExpression) bool {
	call := c.info.Calls[e]
	if call == nil {
		return false
	}
	switch call.Kind {
	case types.CallFunc, types.CallMethod:
		return call.Func != nil && call.Func.MayFail
	case types.CallBuiltin:
		return call.Builtin.MayFail()
	case types.CallConversion:
		return false
	}
	return false
}

// backfillCallMayFail sets Call.MayFail and Info.MayFail[call] for every call
// so the two agree (the Call doc requires Call.MayFail == Info.MayFail[call]).
func (c *checker) backfillCallMayFail() {
	for e, call := range c.info.Calls {
		fail := c.callMayFail(e)
		call.MayFail = fail
		if fail {
			c.info.MayFail[e] = true
		}
	}
}

// numberTransactions numbers every TransactionStatement from 1 in source
// order across the whole program (Info.TxIDs).
func (c *checker) numberTransactions() {
	n := 0
	for _, d := range c.funcs {
		if d.stmt.Body == nil {
			continue
		}
		ast.Inspect(d.stmt.Body, func(node ast.Node) bool {
			if tx, ok := node.(*ast.TransactionStatement); ok {
				n++
				c.info.TxIDs[tx] = n
			}
			return true
		})
	}
}

// checkTransactionRules runs the E-TX checks over every function body: a
// return that leaves a transaction block and a nested db.transaction (spec
// §11.4). A tx handle used outside its block is already an undefined name
// (the handle is scoped to the body in pass 3). FEAT-002's loop checks
// already reject a loop-less break/continue, so this pass does not duplicate
// them. Each body is walked in its own module (inScope) so a diagnostic
// names that module's file.
func (c *checker) checkTransactionRules() {
	for _, d := range c.funcs {
		if d.stmt.Body != nil {
			c.inScope(d.sc, func() { c.walkForTx(d.stmt.Body, false) })
		}
	}
}

// walkForTx walks statements tracking whether the point is inside a
// transaction block (inTx). The first transaction statement at a given nesting
// enters the block; a transaction found while already inTx is E-TX (nesting),
// and a return inside the block is E-TX.
func (c *checker) walkForTx(s ast.Statement, inTx bool) {
	switch s := s.(type) {
	case *ast.BlockStatement:
		for _, st := range s.Statements {
			c.walkForTx(st, inTx)
		}
	case *ast.TransactionStatement:
		if inTx {
			c.errorf(s.Receiver.Pos(), "E-TX", "db.transaction may not be nested")
		}
		if s.Body != nil {
			c.walkForTx(s.Body, true)
		}
	case *ast.ReturnStatement:
		if inTx {
			c.errorf(s.ReturnPos, "E-TX", "return may not leave a transaction block")
		}
	case *ast.IfStatement:
		c.walkForTx(s.Consequence, inTx)
		if s.Alternative != nil {
			c.walkForTx(s.Alternative, inTx)
		}
	case *ast.WhileStatement:
		c.walkForTx(s.Body, inTx)
	case *ast.ForStatement:
		if s.Init != nil {
			c.walkForTx(s.Init, inTx)
		}
		c.walkForTx(s.Body, inTx)
	case *ast.ForOfStatement:
		c.walkForTx(s.Body, inTx)
	case *ast.TryCatchStatement:
		if s.Body != nil {
			c.walkForTx(s.Body, inTx)
		}
		if s.CatchBody != nil {
			c.walkForTx(s.CatchBody, inTx)
		}
	}
}
