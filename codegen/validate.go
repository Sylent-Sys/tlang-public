package codegen

import (
	"strings"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// validate is pass 1 (codegen design §3.1 step 1, plan D22). It rejects, in
// source order, the inputs of §12 that are decidable before any C is
// written, then registers every file-scope C name derived from a user
// declaration in the collision table:
//
//   - item 1: a Bad* node (reported before anything else, because it is the
//     cause of every Invalid type below it), or an expression or type the
//     checker recorded as Invalid;
//   - item 4: a break or continue whose nearest enclosing loop is outside the
//     nearest enclosing transaction body, judged on the source nodes only
//     (the C break of a may-fail loop condition has no source node and always
//     exits its own generated loop);
//   - item 5: db.transaction called with a non-arrow argument;
//   - item 6: a store to a builtin field-like member other than Error fields;
//   - item 7: two parameters (receiver included) with one name, or a
//     parameter or receiver name containing "__";
//   - item 8: two file-scope C names that collide, or one that generated code
//     reserves.
//
// Items 2, 3, 9, 10 and 11 depend on concrete types or on what is emitted and
// are detected where the construct is lowered.
func (g *generator) validate() {
	for _, s := range g.prog.Statements {
		ast.Inspect(s, g.rejectBad)
	}
	v := &validator{g: g}
	for _, s := range g.prog.Statements {
		ast.Inspect(s, v.visit)
	}
	g.registerNames()
}

// rejectBad fails on a Bad* node (§12 item 1).
func (g *generator) rejectBad(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.BadStatement:
		g.fail(errBad(n.Pos(), badStatement))
	case *ast.BadExpression:
		g.fail(errBad(n.Pos(), badExpression))
	case *ast.BadType:
		g.fail(errBad(n.Pos(), badType))
	}
	return true
}

// validator is the state of the pass-1 walk over one top-level statement.
type validator struct {
	g *generator
	// stack holds the nodes the walk is inside, outermost first.
	stack []ast.Node
	// fn is the function being walked (functions do not nest), and params
	// the receiver and parameter names of fn seen so far (lookup only).
	fn     *ast.FunctionStatement
	params map[string]bool
}

// visit is the ast.Inspect callback: it checks n, then walks its children
// with n on the stack.
func (v *validator) visit(n ast.Node) bool {
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return false
	}
	if p := n.Pos(); p.IsValid() {
		v.g.cur = p
	}
	switch n := n.(type) {
	case *ast.FunctionStatement:
		v.fn, v.params = n, map[string]bool{}
	case *ast.Parameter:
		v.checkParam(n)
	case *ast.BreakStatement:
		v.checkLoopExit(n, "break")
	case *ast.ContinueStatement:
		v.checkLoopExit(n, "continue")
	case *ast.CallExpression:
		v.checkCall(n)
	case *ast.AssignmentExpression:
		v.checkStore(n)
	}
	v.checkRecorded(n)
	v.stack = append(v.stack, n)
	return true
}

// checkParam checks the receiver or a parameter of the function being walked
// (§12 item 7); a Parameter node only occurs there. Parameters are visited
// in source order, receiver first.
func (v *validator) checkParam(p *ast.Parameter) {
	role := "parameter"
	if v.fn != nil && v.fn.Receiver == p {
		role = "receiver"
	}
	name := p.Name.Name
	if strings.Contains(name, "__") {
		v.g.fail(errDunderName(p.Name.NamePos, role, name))
	}
	if v.params[name] {
		v.g.fail(errDupParam(p.Name.NamePos, name))
	}
	v.params[name] = true
}

// checkLoopExit rejects a break or continue (keyword) whose nearest
// enclosing loop or transaction body is a transaction body (§12 item 4).
func (v *validator) checkLoopExit(n ast.Statement, keyword string) {
	for i := len(v.stack) - 1; i >= 0; i-- {
		switch v.stack[i].(type) {
		case *ast.WhileStatement, *ast.ForStatement, *ast.ForOfStatement:
			return
		case *ast.TransactionStatement:
			v.g.fail(errLeavesTx(n.Pos(), keyword))
		}
	}
}

// checkCall rejects db.transaction called as an ordinary builtin (§12 item
// 5): the arrow form is a TransactionStatement, not a CallExpression.
func (v *validator) checkCall(c *ast.CallExpression) {
	call := v.g.info.Calls[c]
	if call != nil && call.Kind == types.CallBuiltin && call.Builtin == types.BuiltinDBTransaction {
		v.g.fail(errTxCall(c.Pos()))
	}
}

// checkStore rejects an assignment, compound assignment or ++/-- whose
// target is a builtin field-like member other than the two by-value fields of
// tlang_error (§12 item 6).
func (v *validator) checkStore(a *ast.AssignmentExpression) {
	m, ok := a.Target.(*ast.MemberExpression)
	if !ok {
		return
	}
	sel := v.g.info.Selections[m]
	if sel == nil || sel.Kind != types.SelBuiltin {
		return
	}
	switch sel.Builtin {
	case types.BuiltinErrorMessage, types.BuiltinErrorStatus,
		types.BuiltinErrorCategory, types.BuiltinErrorCode:
		return
	}
	v.g.fail(errBuiltinStore(m.Pos(), sel.Builtin))
}

// checkRecorded rejects an expression or type expression the checker
// recorded as Invalid (§12 item 1).
func (v *validator) checkRecorded(n ast.Node) {
	info := v.g.info
	if e, ok := n.(ast.Expression); ok {
		if tv, ok := info.Types[e]; ok && types.IsInvalid(tv.Type) {
			v.g.fail(errBad(e.Pos(), invalidType))
		}
	}
	if t, ok := n.(ast.TypeExpr); ok {
		if tt, ok := info.TypeExprs[t]; ok && types.IsInvalid(tt) {
			v.g.fail(errBad(t.Pos(), invalidType))
		}
	}
}

// registerNames builds the C-name collision table (codegen design §4.4, §12
// item 8): the reserved names, then the StructCName of every entry of
// Info.Interfaces and the FuncCName of every entry of Info.Funcs and
// Info.FuncInstances, in that order. Interface instances that type
// collection discovers later are registered with addName as they are found.
func (g *generator) registerNames() {
	g.names = newNameTable()
	for _, n := range g.info.Interfaces {
		g.addName(g.structName(n), namedWhat(n), namedPos(n))
	}
	for _, f := range g.info.Funcs {
		g.addName(g.funcName(f), funcWhat(f), f.Pos)
	}
	for _, f := range g.info.FuncInstances {
		g.addName(g.funcName(f), funcWhat(f), f.Pos)
	}
	// Globals' C names are keyed too (DESIGN-modules.md §5.4), as defense in
	// depth behind the checker's authoritative pass: a cross-module g_ clash
	// is rejected here as well. For single-file programs every tag is "", so
	// g_<name> is already unique and this registers nothing that collides.
	for _, v := range g.info.Globals {
		g.addName(globalName(v), "global "+v.Name, v.Pos)
	}
}

// addName registers the C name of a user declaration, failing on a
// collision (§12 item 8).
func (g *generator) addName(cname, what string, pos token.Position) {
	if err := g.names.add(cname, what, pos); err != nil {
		g.fail(err)
	}
}
