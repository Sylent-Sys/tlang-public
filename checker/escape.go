package checker

import (
	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// escape.go is the region/escape analysis of DESIGN.md §2.10 (spec §6.3
// rule 3). Every reference-typed value has a types.Region in the lattice
// RegionNone < RegionStatic < RegionGlobal < RegionRequest. A RegionRequest
// value may not be stored into a global variable, nor into a field or
// element of a RegionGlobal object (including a push onto a global array);
// doing so would let a request-lifetime pointer outlive its arena, so it is
// E-ESCAPE. The analysis is flow-insensitive and does not track aliases
// through parameters (a documented limit); it reuses types.Region/Join,
// HasRegion and the regionOf expression function below.

// analyzeEscape is pass 4. It computes every local's Var.Region (globals,
// parameters, receivers and catch bindings have fixed regions; locals take
// the join of everything assigned to them, pre-scanned per function), then
// rejects every request-into-global store: a global initializer that
// produces a request value, an assignment to a global or into a global
// object, and a push onto a global array. It replaces the FEAT-001 no-op
// hook.
func (c *checker) analyzeEscape() {
	for _, g := range c.globals {
		c.setGlobalRegion(g)
	}
	for _, g := range c.globals {
		c.inScope(c.globalScope[g], func() { c.checkGlobalInitEscape(g) })
	}
	for _, d := range c.funcs {
		c.inScope(d.sc, func() { c.analyzeFuncEscape(d) })
	}
}

// setGlobalRegion fixes a global variable's region to RegionGlobal when its
// type has a region, else RegionNone.
func (c *checker) setGlobalRegion(g *types.Var) {
	if types.HasRegion(g.Type) {
		g.Region = types.RegionGlobal
	} else {
		g.Region = types.RegionNone
	}
}

// checkGlobalInitEscape rejects a global initializer whose value is request
// region: a plain new, a concatenation, a clone, or any other request-region
// expression stored into a global is E-ESCAPE (§6.3 rule 2).
func (c *checker) checkGlobalInitEscape(g *types.Var) {
	let, ok := g.Decl.(*ast.LetStatement)
	if !ok || let.Value == nil {
		return
	}
	if g.Region != types.RegionGlobal {
		return
	}
	if c.regionOf(c.global, let.Value) == types.RegionRequest {
		c.errorf(let.Value.Pos(), "E-ESCAPE",
			"cannot store a request-region value into the global %s", g.Name)
	}
}

// analyzeFuncEscape computes the regions of a function's locals (pre-scan,
// flow-insensitive) and then rejects every request-into-global store in its
// body. A function with no body (a pass-2 failure) is skipped.
func (c *checker) analyzeFuncEscape(d *funcDecl) {
	if d.fn.Sig == nil || d.stmt.Body == nil {
		return
	}
	locals := c.collectLocalRegions(d)
	c.checkBodyEscape(d.stmt.Body, locals)
}

// collectLocalRegions pre-scans a function body and assigns each local *Var
// its region: the Join of every value assigned to it (initializer and every
// assignment target hit). Parameters, receivers and catch bindings are
// RegionRequest; the transaction handle is RegionRequest; a local whose type
// has no region is RegionNone. It returns a name->*Var view used by the body
// walk to resolve identifiers to their region (bindings share the single
// *Var the body pass created in Info.Defs).
func (c *checker) collectLocalRegions(d *funcDecl) map[*types.Var]bool {
	locals := map[*types.Var]bool{}
	sig := d.fn.Sig
	if sig.Recv != nil {
		sig.Recv.Region = requestOrNone(sig.Recv.Type)
	}
	for _, p := range sig.Params {
		p.Region = requestOrNone(p.Type)
	}
	// Walk the body, pre-seeding each declared local, then joining every
	// assigned value's region. Flow-insensitive: Join is max over the chain.
	ast.Inspect(d.stmt.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.LetStatement:
			if v, ok := c.info.Defs[n.Name].(*types.Var); ok && v.Kind == types.LocalVar {
				locals[v] = true
				v.Region = requestOrNone(v.Type)
				if n.Value != nil {
					v.Region = v.Region.Join(c.regionOf(nil, n.Value))
				}
			}
		case *ast.ForOfStatement:
			if v, ok := c.info.Defs[n.Var].(*types.Var); ok {
				locals[v] = true
				// A loop variable is an element read of the iterable; treat
				// it conservatively as request unless the type has no region.
				v.Region = requestOrNone(v.Type)
			}
		case *ast.TryCatchStatement:
			if n.CatchParam != nil {
				if v, ok := c.info.Defs[n.CatchParam].(*types.Var); ok {
					locals[v] = true
					v.Region = requestOrNone(v.Type)
				}
			}
		case *ast.TransactionStatement:
			if n.Param != nil {
				if v, ok := c.info.Defs[n.Param].(*types.Var); ok {
					locals[v] = true
					v.Region = requestOrNone(v.Type)
				}
			}
		case *ast.AssignmentExpression:
			if id, ok := n.Target.(*ast.Identifier); ok && n.Value != nil {
				if v, ok := c.info.Uses[id].(*types.Var); ok && v.Kind == types.LocalVar {
					v.Region = v.Region.Join(c.regionOf(nil, n.Value))
				}
			}
		}
		return true
	})
	return locals
}

// requestOrNone returns RegionRequest when t has a region, else RegionNone.
// It is the region of a parameter, receiver, catch binding or transaction
// handle, and the seed region of a local before its assignments are joined.
func requestOrNone(t types.Type) types.Region {
	if types.HasRegion(t) {
		return types.RegionRequest
	}
	return types.RegionNone
}

// regionOf returns the region of an expression (DESIGN.md §2.10). sc is used
// only to resolve a plain global identifier to RegionGlobal when the body
// scope is unavailable; it may be nil, in which case the identifier's region
// is read from its resolved *Var (Info.Uses). Composite reads take the Join
// of their parts; any call result and anything derived from ctx, DB, params
// or a plain new is RegionRequest.
func (c *checker) regionOf(sc *scope, e ast.Expression) types.Region {
	if e == nil {
		return types.RegionNone
	}
	if !types.HasRegion(c.info.TypeOf(e)) && c.info.TypeOf(e) != nil {
		return types.RegionNone
	}
	switch e := e.(type) {
	case *ast.StringLiteral:
		return types.RegionStatic
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.BooleanLiteral:
		return types.RegionNone
	case *ast.NullLiteral:
		return types.RegionStatic
	case *ast.ArrayLiteral:
		return c.arrayLiteralRegion(sc, e)
	case *ast.Identifier:
		return c.identRegion(sc, e)
	case *ast.MemberExpression:
		return c.memberRegion(sc, e)
	case *ast.IndexExpression:
		return c.regionOf(sc, e.Left)
	case *ast.NonNullExpression:
		return c.regionOf(sc, e.Left)
	case *ast.TernaryExpression:
		return c.regionOf(sc, e.Consequence).Join(c.regionOf(sc, e.Alternative))
	case *ast.InfixExpression:
		switch e.Operator {
		case "+":
			// String concatenation allocates in the request arena.
			if types.IsString(c.info.TypeOf(e)) {
				return types.RegionRequest
			}
			return types.RegionNone
		case "??":
			return c.regionOf(sc, e.Left).Join(c.regionOf(sc, e.Right))
		}
		return types.RegionNone
	case *ast.NewExpression:
		if e.Global {
			return types.RegionGlobal
		}
		return types.RegionRequest
	case *ast.CallExpression:
		return c.callRegion(sc, e)
	}
	return types.RegionRequest
}

// arrayLiteralRegion returns RegionStatic for an array literal whose
// elements are all static, else RegionRequest (the slice header allocates in
// the arena once a non-static element is present).
func (c *checker) arrayLiteralRegion(sc *scope, e *ast.ArrayLiteral) types.Region {
	r := types.RegionStatic
	for _, el := range e.Elements {
		if c.regionOf(sc, el) != types.RegionStatic {
			return types.RegionRequest
		}
	}
	return r
}

// identRegion returns the region of an identifier use: its resolved *Var's
// Region (globals RegionGlobal, locals/params their computed region), or
// RegionNone when it does not denote a value with a region.
func (c *checker) identRegion(sc *scope, e *ast.Identifier) types.Region {
	if v, ok := c.info.Uses[e].(*types.Var); ok {
		return v.Region
	}
	if sc != nil {
		if v, ok := sc.lookup(e.Name).(*types.Var); ok {
			return v.Region
		}
	}
	return types.RegionNone
}

// memberRegion returns the region of a field/member read: the clone_global
// builtin is RegionGlobal; otherwise a read through a value takes the
// receiver's region (reading through a global value yields a global-region
// value, DESIGN.md §2.10).
func (c *checker) memberRegion(sc *scope, e *ast.MemberExpression) types.Region {
	if sel := c.info.Selections[e]; sel != nil {
		if sel.Kind == types.SelBuiltin && sel.Builtin == types.BuiltinStringCloneGlobal {
			return types.RegionGlobal
		}
		if sel.Kind == types.SelBuiltin && c.isContextMember(sel.Builtin) {
			return types.RegionRequest
		}
	}
	return c.regionOf(sc, e.Object)
}

// isContextMember reports whether id is a field-like Context member read
// (ctx.path, ctx.body, etc.), which yields a request-region value.
func (c *checker) isContextMember(id types.BuiltinID) bool {
	return id.Info().Recv == types.RecvContext
}

// callRegion returns the region of a call result. clone_global is global;
// everything else (clone, toString, ctx.*, db results, user calls) is
// request region, conservatively.
func (c *checker) callRegion(sc *scope, e *ast.CallExpression) types.Region {
	if call := c.info.Calls[e]; call != nil {
		if call.Kind == types.CallBuiltin && call.Builtin == types.BuiltinStringCloneGlobal {
			return types.RegionGlobal
		}
		if call.Kind == types.CallConversion {
			return types.RegionNone
		}
	}
	return types.RegionRequest
}

// checkBodyEscape walks a function body and rejects every store of a
// request-region value into a global location: an assignment to a global
// variable or into a field/element of a global-region object, and a push
// onto a global array.
func (c *checker) checkBodyEscape(body *ast.BlockStatement, locals map[*types.Var]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignmentExpression:
			c.checkAssignEscape(n)
		case *ast.CallExpression:
			c.checkPushEscape(n)
		}
		return true
	})
}

// checkAssignEscape rejects "target = value" when target is a global-region
// location and value is request region (plain "=" only; a compound numeric
// assignment never moves a reference).
func (c *checker) checkAssignEscape(a *ast.AssignmentExpression) {
	if a.Value == nil {
		return
	}
	targetRegion, pos, ok := c.storeTargetRegion(a.Target)
	if !ok {
		return
	}
	if targetRegion == types.RegionGlobal && c.regionOf(nil, a.Value) == types.RegionRequest {
		c.errorf(pos, "E-ESCAPE", "cannot store a request-region value into a global-region location")
	}
}

// checkPushEscape rejects "xs.push(v)" when xs is a global-region array and v
// is request region.
func (c *checker) checkPushEscape(e *ast.CallExpression) {
	call := c.info.Calls[e]
	if call == nil || call.Kind != types.CallBuiltin || call.Builtin != types.BuiltinArrayPush {
		return
	}
	m, ok := e.Function.(*ast.MemberExpression)
	if !ok || len(e.Arguments) != 1 {
		return
	}
	if c.regionOf(nil, m.Object) == types.RegionGlobal && c.regionOf(nil, e.Arguments[0]) == types.RegionRequest {
		c.errorf(e.Arguments[0].Pos(), "E-ESCAPE", "cannot push a request-region value onto a global array")
	}
}

// storeTargetRegion returns the region of an assignment target that is a
// store location (a variable, a field store or an element store) and the
// position to report at. For a field or element store the relevant region is
// the object's region (storing into a global object escapes). The bool is
// false when the target has no region of interest.
func (c *checker) storeTargetRegion(target ast.Expression) (types.Region, token.Position, bool) {
	switch t := target.(type) {
	case *ast.Identifier:
		if v, ok := c.info.Uses[t].(*types.Var); ok {
			return v.Region, t.NamePos, true
		}
	case *ast.MemberExpression:
		return c.regionOf(nil, t.Object), t.Property.NamePos, true
	case *ast.IndexExpression:
		return c.regionOf(nil, t.Left), t.Left.Pos(), true
	}
	return types.RegionNone, token.Position{}, false
}
