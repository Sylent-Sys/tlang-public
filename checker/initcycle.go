package checker

import (
	"tlang/ast"
	"tlang/types"
)

// initcycle.go is the cross-module global-initializer cycle check
// (DESIGN-modules.md §4.4): top-level let/const initializers run once per
// scheduler at startup, in whole-program order (a module's globals after the
// globals of every module it imports, source order within a module). A cyclic
// *global-value* dependency — a global whose initializer reads a global that
// (transitively) reads it back — can never be initialized and is E-INIT, the
// same spirit as the non-optional required-field cycle. Pure type/function
// import cycles stay legal (§3.3); only a cyclic global VALUE dependency is
// rejected. A same-module self or backward cycle is also E-INIT (strictly
// stronger than the single-file order rule, never regressing a program that
// is already accepted).

// checkGlobalInitCycles builds the global-initializer dependency graph and
// reports E-INIT on any cycle. An edge v -> w exists when v's initializer
// directly reads global w (a lexical identifier or a module-qualified
// m.w access that lowers to w); references through a called function are NOT
// edges (the function runs later, not during initialization). It is run after
// signatures resolve so Info.Uses and Info.Selections are populated.
func (c *checker) checkGlobalInitCycles() {
	// out[v] lists the globals v's initializer reads directly, in first-seen
	// order (deterministic DFS).
	// c.globals is the whole-program globals worklist in init order;
	// Info.Globals is not assembled until pass 6, which runs after this.
	out := map[*types.Var][]*types.Var{}
	for _, v := range c.globals {
		out[v] = c.globalInitDeps(v)
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[*types.Var]int{}
	var stack []*types.Var
	var visit func(v *types.Var)
	visit = func(v *types.Var) {
		switch color[v] {
		case black:
			return
		case gray:
			// The cycle can span modules; each global is reported in the
			// file of the module that declares it.
			c.errorfIn(c.objFile(v), v.Pos, "E-INIT",
				"cycle in global initialization through %s", v.Name)
			for _, w := range stack {
				if w != v {
					c.notefIn(c.objFile(w), w.Pos, "E-INIT", "%s is on the initialization cycle", w.Name)
				}
			}
			return
		}
		color[v] = gray
		stack = append(stack, v)
		for _, w := range out[v] {
			if color[w] != black {
				visit(w)
			}
		}
		stack = stack[:len(stack)-1]
		color[v] = black
	}
	for _, v := range c.globals {
		visit(v)
	}
}

// globalInitDeps returns the globals a global's initializer reads directly
// (not through a function call). It walks the initializer AST and, for every
// identifier use and every module-qualified value selection, records the
// target when it is a GlobalVar. A CallExpression's callee and arguments are
// still walked (an argument may read a global directly), but the called
// function's body is not — that code runs after initialization.
func (c *checker) globalInitDeps(v *types.Var) []*types.Var {
	let, ok := v.Decl.(*ast.LetStatement)
	if !ok || let.Value == nil {
		return nil
	}
	var deps []*types.Var
	seen := map[*types.Var]bool{}
	add := func(w *types.Var) {
		if w == nil || w.Kind != types.GlobalVar || seen[w] {
			return
		}
		seen[w] = true
		deps = append(deps, w)
	}
	ast.Inspect(let.Value, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Identifier:
			if vr, ok := c.info.Uses[n].(*types.Var); ok {
				add(vr)
			}
		case *ast.MemberExpression:
			// A module-qualified global read (m.g) lowers to the same target
			// Var; it is a direct dependency too.
			if sel := c.info.Selections[n]; sel != nil && sel.Kind == types.SelModuleValue {
				add(sel.Field)
			}
		}
		return true
	})
	return deps
}
