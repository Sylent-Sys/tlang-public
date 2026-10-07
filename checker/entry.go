package checker

import (
	"tlang/ast"
	"tlang/types"
)

// entry.go is pass 6 (DESIGN.md "Decorators, guards, receivers, entry
// points"): it resolves @Use guards, selects the program entry point, and
// assembles the final ordered output lists of Info. It runs after the body
// pass has seeded the generic worklist, so it first closes the generic
// instance set under monomorphization (generics.go) and only then builds the
// concrete Interfaces/Funcs/FuncInstances/JSONTypes/DBTypes lists. The §2.9
// collision check and method attachment live in pass 2 (resolve.go); this
// pass only consumes the already-attached methods.

// selectEntry is pass 6. It closes the monomorphization set, selects the
// entry point, and assembles the output lists. @Use guard resolution runs
// earlier in the driver (the may-fail fixed point of pass 5 reads
// Func.Guards). It replaces the FEAT-001 no-op hook.
func (c *checker) selectEntry(prog *ast.Program) {
	c.closeMonomorphization()
	c.assembleGlobals()
	c.selectEntryPoint(prog)
	c.assembleOutputLists()
}

// ---------------------------------------------------------------------------
// @Use guards and decorators

// resolveGuards resolves the decorators of every function: Use is the only
// known decorator, allowed only on a function whose receiver or first
// parameter is Context, and each argument must resolve to a guard function
// (ctx: Context): bool. It records Func.Guards in source order and
// Info.Uses[arg] = guardFunc. It runs before the pass-5 may-fail fixed point,
// which reads Func.Guards to decide whether a decorated function may fail.
func (c *checker) resolveGuards() {
	for _, d := range c.funcs {
		c.inScope(d.sc, func() { c.resolveFuncGuards(d) })
	}
}

// resolveFuncGuards resolves one function's decorators: @Use guards (spec
// §10) and @After hooks (this design), the two known decorators.
func (c *checker) resolveFuncGuards(d *funcDecl) {
	for _, dec := range d.stmt.Decorators {
		switch dec.Name.Name {
		case "Use":
			c.resolveUseDecorator(d, dec)
		case "After":
			c.resolveAfterDecorator(d, dec)
		default:
			c.errorf(dec.Name.NamePos, "E-DECORATOR", "unknown decorator @%s", dec.Name.Name)
		}
	}
}

// resolveUseDecorator resolves one @Use decorator: it requires a Context
// receiver or first parameter, and each argument must resolve to a guard
// function (ctx: Context): bool, appended to Func.Guards in source order and
// recorded in Info.Uses.
func (c *checker) resolveUseDecorator(d *funcDecl, dec *ast.Decorator) {
	if !c.takesContext(d.fn) {
		c.errorf(dec.AtPos, "E-DECORATOR", "@Use requires a Context receiver or first parameter")
		return
	}
	for _, arg := range dec.Args {
		if g := c.resolveGuardArg(arg); g != nil {
			d.fn.Guards = append(d.fn.Guards, g)
			c.info.Uses[arg] = g
		}
	}
}

// resolveAfterDecorator resolves one @After decorator, mirroring @Use: it
// requires a Context receiver or first parameter, and each argument must
// resolve to a hook function (ctx: Context): void, appended to
// Func.AfterHooks in source order and recorded in Info.Uses (so the hook
// identifier is treated as a resolved function reference like a guard).
func (c *checker) resolveAfterDecorator(d *funcDecl, dec *ast.Decorator) {
	if !c.takesContext(d.fn) {
		c.errorf(dec.AtPos, "E-DECORATOR", "@After requires a Context receiver or first parameter")
		return
	}
	for _, arg := range dec.Args {
		if h := c.resolveHookArg(arg); h != nil {
			d.fn.AfterHooks = append(d.fn.AfterHooks, h)
			c.info.Uses[arg] = h
		}
	}
}

// takesContext reports whether fn's receiver or first parameter is Context.
func (c *checker) takesContext(fn *types.Func) bool {
	if fn.Sig == nil {
		return false
	}
	if fn.Sig.Recv != nil && types.IsBasic(fn.Sig.Recv.Type, types.Context) {
		return true
	}
	return len(fn.Sig.Params) > 0 && types.IsBasic(fn.Sig.Params[0].Type, types.Context)
}

// resolveGuardArg resolves one @Use argument to a guard function, requiring
// it to be a function (ctx: Context): bool (else E-DECORATOR "invalid
// guard"). It returns the resolved *Func, or nil on error.
func (c *checker) resolveGuardArg(arg *ast.Identifier) *types.Func {
	obj := c.global.lookup(arg.Name)
	fn, ok := obj.(*types.Func)
	if !ok {
		c.errorf(arg.NamePos, "E-DECORATOR", "invalid guard %s: not a function", arg.Name)
		return nil
	}
	if !c.isGuardSignature(fn.Sig) {
		c.errorf(arg.NamePos, "E-DECORATOR", "invalid guard %s: want (ctx: Context): bool", arg.Name)
		return nil
	}
	return fn
}

// isGuardSignature reports whether sig is (ctx: Context): bool with no
// receiver and no type parameters.
func (c *checker) isGuardSignature(sig *types.Signature) bool {
	return sig != nil && sig.Recv == nil && len(sig.TypeParams) == 0 &&
		len(sig.Params) == 1 && types.IsBasic(sig.Params[0].Type, types.Context) &&
		types.IsBool(sig.Result)
}

// resolveHookArg resolves one @After argument to a hook function, requiring
// it to be a function (ctx: Context): void (else E-DECORATOR "invalid after
// hook"). It mirrors resolveGuardArg, differing only in the required void
// result. It returns the resolved *Func, or nil on error.
func (c *checker) resolveHookArg(arg *ast.Identifier) *types.Func {
	obj := c.global.lookup(arg.Name)
	fn, ok := obj.(*types.Func)
	if !ok {
		c.errorf(arg.NamePos, "E-DECORATOR", "invalid after hook %s: not a function", arg.Name)
		return nil
	}
	if !c.isHookSignature(fn.Sig) {
		c.errorf(arg.NamePos, "E-DECORATOR", "invalid after hook %s: want (ctx: Context): void", arg.Name)
		return nil
	}
	return fn
}

// isHookSignature reports whether sig is (ctx: Context): void with no
// receiver and no type parameters. It is isGuardSignature with a void result
// instead of a bool one.
func (c *checker) isHookSignature(sig *types.Signature) bool {
	return sig != nil && sig.Recv == nil && len(sig.TypeParams) == 0 &&
		len(sig.Params) == 1 && types.IsBasic(sig.Params[0].Type, types.Context) &&
		types.IsVoid(sig.Result)
}

// ---------------------------------------------------------------------------
// Entry point

// selectEntryPoint scans the top-level functions for route_dispatcher(ctx:
// Context): void (server) and main(): void (script). Exactly one must exist;
// both or neither is E-ENTRY, and a wrong signature is E-ENTRY. It sets
// Info.Kind and Info.Entry (ProgramUnknown only when an error was reported).
func (c *checker) selectEntryPoint(prog *ast.Program) {
	var dispatcher, mainFn *types.Func
	for _, d := range c.funcs {
		if d.stmt.Receiver != nil {
			continue
		}
		switch d.fn.Name {
		case "route_dispatcher":
			dispatcher = d.fn
		case "main":
			mainFn = d.fn
		}
	}

	switch {
	case dispatcher != nil && mainFn != nil:
		c.errorf(mainFn.Pos, "E-ENTRY", "a program has either main or route_dispatcher, not both")
		c.notef(dispatcher.Pos, "E-ENTRY", "route_dispatcher declared here")
		c.info.Kind = types.ProgramUnknown
	case dispatcher != nil:
		if c.isDispatcherSignature(dispatcher.Sig) {
			c.info.Kind = types.ProgramServer
			c.info.Entry = dispatcher
		} else {
			c.errorf(dispatcher.Pos, "E-ENTRY", "route_dispatcher must be (ctx: Context): void")
			c.info.Kind = types.ProgramUnknown
		}
	case mainFn != nil:
		if c.isMainSignature(mainFn.Sig) {
			c.info.Kind = types.ProgramScript
			c.info.Entry = mainFn
		} else {
			c.errorf(mainFn.Pos, "E-ENTRY", "main must be (): void")
			c.info.Kind = types.ProgramUnknown
		}
	default:
		// Neither entry function is declared. A translation unit without an
		// entry is a library fragment, not a runnable program; it has no
		// entry (ProgramUnknown) but is not itself an error. The driver that
		// compiles a whole program for execution checks for a missing entry
		// (and E-ENTRY is reported for the both/wrong-signature cases above).
		c.info.Kind = types.ProgramUnknown
	}
}

// selectEntryPointProgram is the whole-program entry selection
// (DESIGN-modules.md §4.4 H1), used by CheckProgram in place of
// selectEntryPoint (which stays intact for the single-file Check, AC-28). It
// scans c.funcs (whole-program) for non-receiver main / route_dispatcher
// functions. Zero across the program leaves ProgramUnknown (the driver then
// reports ErrNoEntry); more than one is E-ENTRY naming each competing entry's
// position (so two mains in two files both appear); exactly one is validated
// with the existing isMainSignature / isDispatcherSignature and sets
// ProgramScript / ProgramServer. It needs no prog argument. Each diagnostic
// names the file of the module that declares the entry it is positioned at.
func (c *checker) selectEntryPointProgram() {
	var entries []*types.Func
	for _, d := range c.funcs {
		if d.stmt.Receiver != nil {
			continue
		}
		if d.fn.Name == "main" || d.fn.Name == "route_dispatcher" {
			entries = append(entries, d.fn)
		}
	}

	switch {
	case len(entries) == 0:
		// No entry anywhere: a library-only program. Not itself an error; the
		// driver reports a missing entry when it tries to run the program.
		c.info.Kind = types.ProgramUnknown
	case len(entries) > 1:
		first := entries[0]
		c.errorfIn(c.objFile(first), first.Pos, "E-ENTRY",
			"a program has exactly one entry point, found %d (main or route_dispatcher)", len(entries))
		for _, e := range entries[1:] {
			c.notefIn(c.objFile(e), e.Pos, "E-ENTRY", "%s also declared here", e.Name)
		}
		c.info.Kind = types.ProgramUnknown
	default:
		e := entries[0]
		switch e.Name {
		case "route_dispatcher":
			if c.isDispatcherSignature(e.Sig) {
				c.info.Kind = types.ProgramServer
				c.info.Entry = e
			} else {
				c.errorfIn(c.objFile(e), e.Pos, "E-ENTRY", "route_dispatcher must be (ctx: Context): void")
				c.info.Kind = types.ProgramUnknown
			}
		case "main":
			if c.isMainSignature(e.Sig) {
				c.info.Kind = types.ProgramScript
				c.info.Entry = e
			} else {
				c.errorfIn(c.objFile(e), e.Pos, "E-ENTRY", "main must be (): void")
				c.info.Kind = types.ProgramUnknown
			}
		}
	}
}

// isDispatcherSignature reports whether sig is (ctx: Context): void with no
// receiver and no type parameters.
func (c *checker) isDispatcherSignature(sig *types.Signature) bool {
	return sig != nil && sig.Recv == nil && len(sig.TypeParams) == 0 &&
		len(sig.Params) == 1 && types.IsBasic(sig.Params[0].Type, types.Context) &&
		types.IsVoid(sig.Result)
}

// isMainSignature reports whether sig is (): void with no receiver, no params
// and no type parameters.
func (c *checker) isMainSignature(sig *types.Signature) bool {
	return sig != nil && sig.Recv == nil && len(sig.TypeParams) == 0 &&
		len(sig.Params) == 0 && types.IsVoid(sig.Result)
}

// ---------------------------------------------------------------------------
// Output assembly

// assembleGlobals fills Info.Globals in whole-program order (the order
// collectNames pushed each module's globals: module-topological, then source
// order within a module) and reassigns Var.Index as a whole-program running
// counter so the globals struct layout and initialization order are
// well-defined across modules (DESIGN-modules.md §4.4 N3). For the
// single-file Check this reproduces the pass-1 indices exactly.
func (c *checker) assembleGlobals() {
	for i, v := range c.globals {
		v.Index = i
	}
	c.info.Globals = append(c.info.Globals, c.globals...)
}

// assembleOutputLists builds the final ordered concrete output lists:
// Interfaces (source-order plain interfaces then concrete NamedInstances in
// creation order, all through SortByFieldDeps), Funcs (non-generic functions
// and methods in source order), FuncInstances (concrete instances in creation
// order), and JSONTypes (closed over field/element types, deduped by Mangle,
// dependencies first). DBTypes and Routes are populated during pass 3 in
// their natural order.
func (c *checker) assembleOutputLists() {
	c.assembleInterfaces()
	c.assembleFuncs()
	c.assembleJSONTypes()
}

// assembleInterfaces lists the plain interfaces and object-type declarations
// in source order, then the concrete named instances in creation order, and
// passes the whole list through SortByFieldDeps (dependencies first).
func (c *checker) assembleInterfaces() {
	var list []*types.Named
	for _, d := range c.interfaces {
		if !d.named.IsGeneric() {
			list = append(list, d.named)
		}
	}
	for _, d := range c.aliases {
		if d.named != nil && !d.named.IsGeneric() {
			list = append(list, d.named)
		}
	}
	for _, n := range c.info.Instances.NamedInstances() {
		if n.Origin != nil && types.IsConcrete(n) {
			list = append(list, n)
		}
	}
	c.info.Interfaces = types.SortByFieldDeps(list)
}

// assembleFuncs lists every non-generic function and method in source order
// (Funcs) and every concrete function instance in creation order
// (FuncInstances).
func (c *checker) assembleFuncs() {
	for _, d := range c.funcs {
		if d.fn.Sig != nil && !d.fn.IsGeneric() {
			c.info.Funcs = append(c.info.Funcs, d.fn)
		}
	}
	for _, inst := range c.info.Instances.FuncInstances() {
		if inst.Origin != nil && types.IsConcrete(inst.Sig) {
			c.info.FuncInstances = append(c.info.FuncInstances, inst)
		}
	}
}

// assembleJSONTypes closes the JSON demand set over field and element types:
// each demanded type contributes its field types (and array elements), with
// dependencies emitted before the types that need them, deduplicated by
// Mangle. Optional fields and elements contribute their non-null type.
func (c *checker) assembleJSONTypes() {
	seeds := c.info.JSONTypes
	c.info.JSONTypes = nil
	seen := map[string]*types.JSONType{}
	var order []*types.JSONType
	var visit func(t types.Type, parse, write bool)
	visit = func(t types.Type, parse, write bool) {
		switch t := t.(type) {
		case *types.Named:
			if t.IsGeneric() || !types.IsConcrete(t) {
				return
			}
			key := types.Mangle(t)
			if existing := seen[key]; existing != nil {
				existing.Parse = existing.Parse || parse
				existing.Write = existing.Write || write
				return
			}
			j := &types.JSONType{Type: t, Parse: parse, Write: write}
			seen[key] = j
			// Dependencies first: visit field types before appending.
			for _, f := range t.Fields() {
				visit(types.NonOptional(f.Type), parse, write)
			}
			order = append(order, j)
		case *types.Array:
			// An array JSON type depends on its element type.
			ek := types.Mangle(t)
			if existing := seen[ek]; existing != nil {
				existing.Parse = existing.Parse || parse
				existing.Write = existing.Write || write
				return
			}
			visit(types.NonOptional(t.Elem), parse, write)
			if types.IsConcrete(t) {
				j := &types.JSONType{Type: t, Parse: parse, Write: write}
				seen[ek] = j
				order = append(order, j)
			}
		case *types.Optional:
			visit(t.Elem, parse, write)
		}
	}
	for _, seed := range seeds {
		visit(seed.Type, seed.Parse, seed.Write)
	}
	c.info.JSONTypes = order
}
