package checker

import (
	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// generics.go holds the thin instantiation-request wrappers the body pass
// (FEAT-002) uses to seed the generic worklist. They route every direct
// instantiation through Info.Instances so instances stay canonical and
// deduplicated, record the first requesting source position per canonical
// instance in instSite (first writer wins, for OQ7 error attribution), and
// append each newly created instance to the worklist consumed by FEAT-003's
// monomorphization closure. The closure itself and the InstanceCache.Errors()
// attribution live in FEAT-003.

// instantiateFunc instantiates the generic function origin with targs,
// recording the request position and seeding the worklist. On failure it
// reports E-GENERIC at pos and returns (nil, false).
func (c *checker) instantiateFunc(origin *types.Func, targs []types.Type, pos token.Position) (*types.Func, bool) {
	inst, err := c.info.Instances.InstantiateFunc(origin, targs)
	if err != nil {
		c.errorf(pos, "E-GENERIC", "%s", err.Error())
		return nil, false
	}
	c.seedFuncInstance(inst, pos)
	return inst, true
}

// instantiateNamed instantiates the generic interface origin with targs,
// recording the request position and seeding the worklist. On failure it
// reports E-GENERIC at pos and returns (nil, false).
func (c *checker) instantiateNamed(origin *types.Named, targs []types.Type, pos token.Position) (*types.Named, bool) {
	inst, err := c.info.Instances.InstantiateNamed(origin, targs)
	if err != nil {
		c.errorf(pos, "E-GENERIC", "%s", err.Error())
		return nil, false
	}
	c.seedNamedInstance(inst, pos)
	return inst, true
}

// seedFuncInstance records a function instance's first-request position and
// adds it to the generic worklist once. Non-instances are ignored.
func (c *checker) seedFuncInstance(inst *types.Func, pos token.Position) {
	if inst == nil || inst.Origin == nil {
		return
	}
	if _, seen := c.instSite[any(inst)]; !seen {
		c.recordInstSite(inst, pos)
		c.funcWork = append(c.funcWork, inst)
	}
}

// seedNamedInstance records an interface instance's first-request position
// and adds it to the generic worklist once. Non-instances are ignored.
func (c *checker) seedNamedInstance(inst *types.Named, pos token.Position) {
	if inst == nil || inst.Origin == nil {
		return
	}
	if _, seen := c.instSite[any(inst)]; !seen {
		c.recordInstSite(inst, pos)
		c.namedWork = append(c.namedWork, inst)
	}
}

// ---------------------------------------------------------------------------
// Monomorphization closure (FEAT-003, design pass 6)

// closeMonomorphization closes the instance set under monomorphization: for
// each concrete function instance already created, it substitutes that
// instance's type arguments into every generic callee and generic interface
// its body uses, creating new concrete instances, until none appear. It
// drives from Info.Instances (the authoritative set, in creation order) and
// re-scans whenever the set grew, so instances created transitively are also
// processed. Each inst.Fields() forcing a lazy field expansion is bracketed
// so OQ7 can attribute any Subst-time InstanceCache.Errors() to the right
// position.
func (c *checker) closeMonomorphization() {
	// Two separate maps, deliberately NOT collapsed into one (see the design's
	// "Why two sets"): forced is the recursion visited set threaded through the
	// forceFields/seedTypeInstances descent so each concrete *Named instance is
	// forced and seeded exactly once (this is what terminates the previously
	// unbounded forceFields<->seedTypeInstances recursion on self-referential
	// generics); seenNamed is the outer-loop progress detector, kept local so
	// progressed still flips only on genuine instance-set growth. Sharing one
	// map would let an instance marked during recursion suppress progressed on
	// the outer re-scan, exiting the loop before transitively-created func
	// instances were discovered.
	forced := map[*types.Named]bool{}
	processedFuncs := map[*types.Func]bool{}
	seenNamed := map[*types.Named]bool{}
	for {
		progressed := false

		for _, inst := range c.info.Instances.FuncInstances() {
			if processedFuncs[inst] {
				continue
			}
			processedFuncs[inst] = true
			progressed = true
			if inst.Origin != nil && types.IsConcrete(inst.Sig) {
				c.expandInstance(inst, forced)
			}
		}

		for _, n := range c.info.Instances.NamedInstances() {
			if seenNamed[n] {
				continue
			}
			seenNamed[n] = true
			progressed = true
			if n.Origin != nil && types.IsConcrete(n) {
				c.forceFields(n, forced)
			}
		}

		if !progressed {
			break
		}
	}
}

// expandInstance walks the body of a concrete function instance, mapping each
// generic callee (Call.Func / Selection.Func) and each instance type the body
// mentions (Info.Types) with the instance's type arguments via Concrete /
// ConcreteFunc, instantiating the resulting concrete callees and interfaces.
// The body is the origin's, so every position the walk reports or records
// lies in the origin's module, whichever module requested the instance.
func (c *checker) expandInstance(inst *types.Func, forced map[*types.Named]bool) {
	origin := inst.Origin
	if origin == nil || origin.Decl == nil || origin.Decl.Body == nil {
		return
	}
	c.inFile(c.objFile(origin), func() {
		// The frame is only callSitePos/exprPos's fallback for a nil node; it
		// must lie in the walked file, so it is the origin's declaration
		// rather than the instance site, which may be in another module.
		pos := origin.Pos
		ast.Inspect(origin.Decl.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpression:
				if call := c.info.Calls[n]; call != nil && call.Func != nil && call.Func.Origin != nil {
					c.instantiateConcreteCallee(call.Func, inst, c.callSitePos(n, pos))
				}
			}
			if e, ok := n.(ast.Expression); ok {
				if tv, ok := c.info.Types[e]; ok {
					c.seedTypeInstances(c.info.Concrete(tv.Type, inst), c.exprPos(e, pos), forced)
				}
			}
			return true
		})
	})
}

// instantiateConcreteCallee creates the concrete instance of a generic callee
// recorded inside inst's body: it substitutes inst's type arguments into the
// callee's own type arguments and instantiates the result through the cache
// (so a transitively-reached generic function such as inner<T> inside
// outer<int64> becomes inner<int64>). A callee whose arguments are already
// concrete is seeded as-is.
func (c *checker) instantiateConcreteCallee(callee, inst *types.Func, pos token.Position) {
	if callee.Origin == nil {
		return
	}
	args := make([]types.Type, len(callee.TypeArgs))
	for i, a := range callee.TypeArgs {
		args[i] = c.info.Concrete(a, inst)
	}
	concrete, err := c.info.Instances.InstantiateFunc(callee.Origin, args)
	if err != nil {
		c.errorf(pos, "E-GENERIC", "%s", err.Error())
		return
	}
	c.seedFuncInstance(concrete, pos)
}

// seedTypeInstances creates (via the cache) the named instance a concrete
// type mentions and records its site, so the closure and the output passes
// see it. Arrays and optionals are descended into.
func (c *checker) seedTypeInstances(t types.Type, pos token.Position, forced map[*types.Named]bool) {
	switch t := t.(type) {
	case *types.Named:
		if t.Origin != nil && types.IsConcrete(t) {
			c.seedNamedInstance(t, pos)
			c.forceFields(t, forced)
		}
	case *types.Array:
		c.seedTypeInstances(t.Elem, pos, forced)
	case *types.Optional:
		c.seedTypeInstances(t.Elem, pos, forced)
	}
}

// forceFields forces the lazy field expansion of a named instance, bracketing
// the call so any Subst-time instantiation errors are attributed to the
// instance's site (OQ7). It also seeds any nested instances the fields
// mention.
func (c *checker) forceFields(n *types.Named, forced map[*types.Named]bool) {
	// Visited-set guard: force (and seed the fields of) each canonical concrete
	// instance exactly once per closure run. Without it a self-referential or
	// mutually-recursive generic (e.g. interface Node<T> { next: Node<T> | null })
	// re-enters forceFields<->seedTypeInstances on the same *Named forever and
	// stack-overflows before codegen. The n == nil arm is defensive only (both
	// callers gate on Origin != nil && IsConcrete, and the outer loop ranges
	// NamedInstances() which never yields nil); it is cheap and future-proofs a
	// new caller.
	if n == nil || forced[n] {
		return
	}
	forced[n] = true
	// Named.Fields panics on an instance whose origin was never completed
	// (SetFields not called). That cannot happen for a well-formed interface
	// instance, but a recovered declaration (e.g. an E-UNSUPPORTED generic
	// object-type alias) may leave an incomplete origin; skip it so the
	// checker never panics on a parser-accepted tree.
	if !n.Complete() {
		return
	}
	// The site (or the origin declaration when none was recorded) may lie in
	// any module; the errors and the nested instances' sites are attributed
	// in its file.
	site := c.sitePos(n, filePos{c.objFile(n.Obj), n.Obj.Pos})
	before := len(c.info.Instances.Errors())
	fields := n.Fields()
	c.inFile(site.file, func() {
		c.attributeErrors(before, site.pos, n)
		for _, f := range fields {
			c.seedTypeInstances(f.Type, site.pos, forced)
		}
	})
}

// attributeErrors reports every new InstanceCache.Errors() element produced
// since the before snapshot as an E-GENERIC diagnostic at pos in the current
// file (OQ7), advances the drained cursor, and dedups by (code, file,
// position, message). target is the instance being forced, used only for the
// fallback origin note.
func (c *checker) attributeErrors(before int, pos token.Position, target any) {
	errs := c.info.Instances.Errors()
	for _, e := range errs[before:] {
		ie, ok := e.(*types.InstantiationError)
		if !ok {
			continue
		}
		if c.dedupGeneric(pos, ie.Error()) {
			continue
		}
		c.errorf(pos, "E-GENERIC", "%s", ie.Error())
	}
	if len(errs) > c.drained {
		c.drained = len(errs)
	}
}

// dedupGeneric reports whether an E-GENERIC at pos in the current file with
// msg was already reported, so repeated identical Subst-time failures
// collapse (OQ7 dedup by (code, position, message); two modules can share a
// line and column, so the position includes the file).
func (c *checker) dedupGeneric(pos token.Position, msg string) bool {
	for _, d := range c.diags.Items {
		if d.Code == "E-GENERIC" && d.File == c.file && d.Pos == pos && d.Message == msg {
			return true
		}
	}
	return false
}

// sitePos returns the recorded first-request position (with its file) of an
// instance, or the fallback (the origin declaration's position) when none
// was recorded.
func (c *checker) sitePos(key any, fallback filePos) filePos {
	if p, ok := c.instSite[key]; ok {
		return p
	}
	return fallback
}

// callSitePos returns the position to attribute a callee instantiated from a
// call site n, preferring the call's own position over the enclosing frame.
func (c *checker) callSitePos(n *ast.CallExpression, frame token.Position) token.Position {
	if n != nil {
		return n.Function.Pos()
	}
	return frame
}

// exprPos returns the position of an expression, or the enclosing frame when
// it has none.
func (c *checker) exprPos(e ast.Expression, frame token.Position) token.Position {
	if e != nil {
		return e.Pos()
	}
	return frame
}
