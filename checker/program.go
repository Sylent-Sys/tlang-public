package checker

import (
	"tlang/ast"
	"tlang/diag"
	"tlang/module"
	"tlang/token"
	"tlang/types"
)

// program.go is the whole-program entry of the checker (DESIGN-modules.md
// §4): CheckProgram type-checks a module graph as one logical program with a
// per-module top-level scope. It keeps checker.Check verbatim as the
// single-file path (AC-28); a one-module import-free graph routed through
// CheckProgram produces the same Info a direct Check would (byte-equivalence
// test), because every tag is "" and the import/export machinery is a no-op.

// CheckProgram type-checks the modules of a built graph together and returns
// one merged *types.Info plus the diagnostics it produced. mods is the
// deterministic module order from module.Build (dependencies before
// importers); the first module is treated as the root for Info.
//
// It runs the same six passes as Check, but pass 0 (module resolution) and
// pass 1 (collect) are per-module, and passes 2-6 run whole-program over the
// union of each module's worklists in that order. The §4.4 cross-module
// global-init cycle check (E-INIT) runs after signatures resolve.
//
// Every diagnostic carries the file (module ID, mod.Prog.File) of the module
// its position lies in; the list's default File (the first module's) is never
// relied on.
func CheckProgram(mods []*module.Module) (*types.Info, *diag.List) {
	info := types.NewInfo()
	file := ""
	if len(mods) > 0 {
		file = mods[0].Prog.File
	}
	diags := diag.NewList(file)
	c := &checker{
		info:        info,
		diags:       diags,
		file:        file,
		global:      newScope(nil),
		declFiles:   map[types.Object]string{},
		methods:     map[string]*types.Func{},
		instSite:    map[any]filePos{},
		errDeclPos:  map[filePos]bool{},
		globalScope: map[*types.Var]*scope{},
		scopeTags:   map[*scope]string{},
		scopeFiles:  map[*scope]string{},
	}
	c.runProgram(mods)
	return info, diags
}

// runProgram drives the whole-program passes (DESIGN-modules.md §4.2). It
// mirrors run() but with per-module collect and import-table binding, and a
// cross-module global-init cycle check wired in after signatures resolve.
func (c *checker) runProgram(mods []*module.Module) {
	c.buildModuleContexts(mods) // pass 0: scopes, tags, export-intent
	c.collectModules()          // pass 1: collect, resolve exports, bind imports

	c.resolveSignatures()     // pass 2 (whole-program worklists)
	c.checkBodies(nil)        // pass 3
	c.checkGlobalInitCycles() // §4.4 cross-module global-init cycle (E-INIT)
	c.resolveGuards()         // @Use/@After guards
	c.analyzeEscape()         // pass 4
	c.markMayFail()           // pass 5
	c.selectEntryProgram()    // pass 6 (whole-program entry)
	c.checkCNameCollisions()  // cross-declaration C-name collisions
}

// buildModuleContexts creates one moduleCtx (and scope) per module, records
// each scope's tag for collect-time object stamping and its file for
// diagnostics, and fills each module's pass-0 export-intent map from its own
// Exported declarations. It reads only each module's own declarations (no
// cross-module read yet).
func (c *checker) buildModuleContexts(mods []*module.Module) {
	for _, m := range mods {
		sc := newScope(nil)
		c.scopeTags[sc] = m.Tag
		c.scopeFiles[sc] = m.Prog.File
		mc := &moduleCtx{
			mod:         m,
			scope:       sc,
			importTable: map[string]types.Object{},
			exportDecls: map[string]ast.Statement{},
		}
		c.inScope(sc, func() { c.recordExportIntent(mc) })
		c.modules = append(c.modules, mc)
	}
}

// recordExportIntent fills mc.exportDecls from the module's own `export`ed
// top-level declarations and detects a second `export default` and a name
// exported twice (E-IMPORT, with a note at the first). It records intent only;
// the export set is resolved to objects in pass 1 after collect.
func (c *checker) recordExportIntent(mc *moduleCtx) {
	var defaultStmt ast.Statement
	var defaultPos token.Position
	firstExport := map[string]exportSite{}
	for _, stmt := range mc.mod.Prog.Statements {
		if re, ok := stmt.(*ast.ReExportDecl); ok {
			for _, spec := range re.Specs {
				id := spec.Name
				if spec.Alias != nil {
					id = spec.Alias
				}
				c.checkDuplicateExport(firstExport, id.Name, exportSite{pos: id.NamePos, stmt: stmt})
			}
			continue
		}
		name, exported, isDefault, pos := exportInfo(stmt)
		if !exported {
			continue
		}
		if name != "" {
			c.checkDuplicateExport(firstExport, name, exportSite{pos: pos, stmt: stmt, direct: true})
			mc.exportDecls[name] = stmt
		}
		if isDefault {
			if defaultStmt != nil {
				c.errorf(pos, "E-IMPORT", "a module has at most one default export")
				c.notef(defaultPos, "E-IMPORT", "first default export declared here")
				continue
			}
			defaultStmt = stmt
			defaultPos = pos
		}
	}
	mc.defaultStmt = defaultStmt
}

// exportSite is where a module exports a name: the `export` keyword of a
// direct export, or the exported-as identifier of a re-export specifier.
type exportSite struct {
	pos    token.Position
	stmt   ast.Statement
	direct bool
}

// checkDuplicateExport records the first export of name in first and reports
// a later export of the same name (E-IMPORT at the later one, with a note at
// the first), so a module's export set has one binding per name. Two
// declarations of one name are a redeclaration that collect reports, and two
// specifiers of one re-export statement are already a parse error, so neither
// is reported again here.
func (c *checker) checkDuplicateExport(first map[string]exportSite, name string, site exportSite) {
	prev, seen := first[name]
	if !seen {
		first[name] = site
		return
	}
	if (prev.direct && site.direct) || prev.stmt == site.stmt {
		return
	}
	c.errorf(site.pos, "E-IMPORT", "duplicate export `%s`", name)
	c.notef(prev.pos, "E-IMPORT", "first export of `%s` here", name)
}

// exportInfo reports a top-level declaration's exported name, whether it is
// exported, whether it is the default, and the position of its `export`
// keyword. A non-decl statement returns exported=false.
func exportInfo(stmt ast.Statement) (name string, exported, isDefault bool, pos token.Position) {
	switch s := stmt.(type) {
	case *ast.InterfaceStatement:
		return s.Name.Name, s.Exported, s.IsDefault, s.ExportPos
	case *ast.TypeAliasStatement:
		return s.Name.Name, s.Exported, s.IsDefault, s.ExportPos
	case *ast.FunctionStatement:
		if s.Receiver != nil {
			return "", false, false, token.Position{}
		}
		return s.Name.Name, s.Exported, s.IsDefault, s.ExportPos
	case *ast.LetStatement:
		return s.Name.Name, s.Exported, s.IsDefault, s.ExportPos
	}
	return "", false, false, token.Position{}
}

// collectModules runs pass 1 for every module: collect its own declarations
// into its scope, then (once every module is collected) resolve export sets
// and declare each module's imported names into its scope.
func (c *checker) collectModules() {
	// Collect every module's own declarations first, so cross-module export
	// resolution and import binding see fully populated scopes.
	for _, mc := range c.modules {
		c.inScope(mc.scope, func() { c.collectNamesInto(mc.mod.Prog, mc.scope) })
	}
	// Bind each module's export set now that objects exist.
	c.bindExports()
	// Declare each module's imported/aliased names into its own scope,
	// reporting a local-vs-import collision as E-NAME (FR-24).
	for _, mc := range c.modules {
		c.inScope(mc.scope, func() { c.declareImports(mc) })
	}
}

// specTarget returns the moduleCtx a module imports via the given specifier,
// or nil when the specifier did not resolve (an E-IMPORT was already emitted
// by module.Build).
type importTarget struct {
	source   *moduleCtx
	standard *module.StandardModule
}

func (c *checker) specTarget(mc *moduleCtx, spec string) importTarget {
	for _, e := range mc.mod.Imports {
		if e.Spec != spec {
			continue
		}
		switch e.Target.Kind {
		case module.ImportTargetSource:
			return importTarget{source: c.ctxOf(e.Target.Source)}
		case module.ImportTargetStandard:
			return importTarget{standard: e.Target.Standard}
		}
	}
	return importTarget{}
}

// ctxOf returns the moduleCtx of a resolved module, or nil.
func (c *checker) ctxOf(m *module.Module) *moduleCtx {
	for _, mc := range c.modules {
		if mc.mod == m {
			return mc
		}
	}
	return nil
}

// bindExports fills every module's resolved export set (exports keyed by
// exported name, plus defaultExport). Direct exports bind to the object
// collect declared in the module's scope; re-export edges are followed with a
// per-name visited set so the result is order-independent and a re-export
// cycle terminates with an E-IMPORT.
func (c *checker) bindExports() {
	for _, mc := range c.modules {
		mc.exports = map[string]types.Object{}
	}
	// Direct exports first.
	for _, mc := range c.modules {
		for name, stmt := range mc.exportDecls {
			if obj := c.declaredObject(mc, name, stmt); obj != nil {
				mc.exports[name] = obj
			}
		}
		if mc.defaultStmt != nil {
			mc.defaultExport = c.defaultObject(mc)
		}
	}
	// Re-export edges, resolved by link-following with a visited set. Every
	// diagnostic is at a specifier of the re-exporting module, however far
	// the chain leads.
	for _, mc := range c.modules {
		c.inScope(mc.scope, func() { c.resolveReExports(mc) })
	}
}

// declaredObject returns the object a module's own exported declaration binds
// in its scope (the symbol the export names). It looks the name up in the
// module scope so a type/function/global all resolve to their object.
func (c *checker) declaredObject(mc *moduleCtx, name string, stmt ast.Statement) types.Object {
	if obj := mc.scope.objs[name]; obj != nil {
		return obj
	}
	return nil
}

// defaultObject returns the object a module's `export default` declaration
// names (looked up by the declaration's own name in the module scope).
func (c *checker) defaultObject(mc *moduleCtx) types.Object {
	name, _, _, _ := exportInfo(mc.defaultStmt)
	if name == "" {
		return nil
	}
	return mc.scope.objs[name]
}

// resolveReExports resolves a module's `export { X } from "./y"` edges into
// its export set, following chains of re-exports with a visited set so the
// resolution is order-independent and terminating. An unreachable name is
// E-IMPORT (not exported / no such name); a cycle is E-IMPORT "re-export
// cycle".
func (c *checker) resolveReExports(mc *moduleCtx) {
	for _, stmt := range mc.mod.Prog.Statements {
		re, ok := stmt.(*ast.ReExportDecl)
		if !ok {
			continue
		}
		target := c.specTarget(mc, re.From)
		for _, spec := range re.Specs {
			localName := spec.Name.Name
			exportAs := localName
			if spec.Alias != nil {
				exportAs = spec.Alias.Name
			}
			if target.source == nil && target.standard == nil {
				continue // unresolved specifier already reported by module.Build
			}
			visited := map[*moduleCtx]bool{}
			obj := c.followExport(target, localName, spec.Name.NamePos, visited)
			if obj != nil {
				mc.exports[exportAs] = obj
			}
		}
	}
}

// followExport resolves an exported name in target, following re-export
// chains. visited guards against a re-export cycle (E-IMPORT). A name that is
// not exported anywhere on the chain is E-IMPORT at pos.
func (c *checker) followExport(target importTarget, name string, pos token.Position, visited map[*moduleCtx]bool) types.Object {
	if target.standard != nil {
		if obj := standardExportObject(target.standard, name); obj != nil {
			return obj
		}
		c.errorf(pos, "E-IMPORT", "%s is not exported", name)
		return nil
	}
	if target.source == nil {
		return nil
	}
	source := target.source
	if visited[source] {
		c.errorf(pos, "E-IMPORT", "re-export cycle for %s", name)
		return nil
	}
	visited[source] = true

	// A direct export of the target binds immediately.
	if obj := c.directExportObject(source, name); obj != nil {
		return obj
	}
	// Otherwise it may be a re-export the target itself declares; follow it.
	for _, stmt := range source.mod.Prog.Statements {
		re, ok := stmt.(*ast.ReExportDecl)
		if !ok {
			continue
		}
		next := c.specTarget(source, re.From)
		for _, spec := range re.Specs {
			exportAs := spec.Name.Name
			if spec.Alias != nil {
				exportAs = spec.Alias.Name
			}
			if exportAs != name || (next.source == nil && next.standard == nil) {
				continue
			}
			return c.followExport(next, spec.Name.Name, pos, visited)
		}
	}
	c.errorf(pos, "E-IMPORT", "%s is not exported", name)
	return nil
}

// directExportObject returns the object a module directly exports under name
// (its own `export`ed declaration), or nil when it is not a direct export.
func (c *checker) directExportObject(mc *moduleCtx, name string) types.Object {
	if _, ok := mc.exportDecls[name]; !ok {
		return nil
	}
	return mc.scope.objs[name]
}

// declareImports binds each of a module's import declarations into its scope
// (DESIGN-modules.md §4.2): named/aliased imports bind the local name to the
// target's export object; a namespace import binds a *types.ModuleNS; a
// default import binds the target's default export. A local declaration that
// already occupies the name is a local-vs-import collision (E-NAME, FR-24).
func (c *checker) declareImports(mc *moduleCtx) {
	for _, stmt := range mc.mod.Prog.Statements {
		imp, ok := stmt.(*ast.ImportDecl)
		if !ok {
			continue
		}
		target := c.specTarget(mc, imp.From)
		if target.standard != nil && !target.standard.Available {
			c.errorf(imp.FromPos, "E-IMPORT", "standard module %q is not available", imp.From)
			continue
		}
		switch {
		case imp.Namespace != nil:
			c.bindNamespaceImport(mc, imp, target)
		default:
			c.bindDefaultImport(mc, imp, target)
			c.bindNamedImports(mc, imp, target)
		}
	}
}

// bindNamespaceImport binds "import * as m from ..." as a *types.ModuleNS
// carrying the target module's export set and default.
func (c *checker) bindNamespaceImport(mc *moduleCtx, imp *ast.ImportDecl, target importTarget) {
	exports := map[string]types.Object{}
	var def types.Object
	if target.source != nil {
		exports = target.source.exports
		def = target.source.defaultExport
	} else if target.standard != nil {
		exports = standardExports(target.standard)
	}
	ns := &types.ModuleNS{
		Name:    imp.Namespace.Name,
		Pos:     imp.Namespace.NamePos,
		Exports: exports,
		Default: def,
	}
	c.bindImportName(mc, imp.Namespace.Name, ns, imp.Namespace.NamePos)
}

// bindDefaultImport binds "import D from ..." to the target's default export
// (DESIGN-modules.md §6.4). A target with no default export is E-IMPORT.
func (c *checker) bindDefaultImport(mc *moduleCtx, imp *ast.ImportDecl, target importTarget) {
	if imp.Default == nil {
		return
	}
	if target.source == nil && target.standard == nil {
		return
	}
	if target.source == nil || target.source.defaultExport == nil {
		c.errorf(imp.Default.NamePos, "E-IMPORT", "module %q has no default export", imp.From)
		return
	}
	c.bindImportName(mc, imp.Default.Name, target.source.defaultExport, imp.Default.NamePos)
}

// bindNamedImports binds each "{ A, B as C }" specifier to the target's
// matching export, with the FR-16 (not exported) vs FR-17 (no such member)
// distinction.
func (c *checker) bindNamedImports(mc *moduleCtx, imp *ast.ImportDecl, target importTarget) {
	for _, spec := range imp.Named {
		local := spec.Name.Name
		if spec.Alias != nil {
			local = spec.Alias.Name
		}
		if target.source == nil && target.standard == nil {
			continue
		}
		var obj types.Object
		if target.source != nil {
			obj = target.source.exports[spec.Name.Name]
		} else {
			obj = standardExportObject(target.standard, spec.Name.Name)
		}
		if obj == nil {
			c.reportMissingImport(target, spec.Name.Name, spec.Name.NamePos, imp.From)
			continue
		}
		c.bindImportName(mc, local, obj, spec.Name.NamePos)
	}
}

// reportMissingImport reports the FR-16 vs FR-17 distinction for a named
// import that did not resolve: a name the target declares but does not export
// is "not exported by" (FR-16); a name it has no declaration for is "no
// exported member" (FR-17).
func (c *checker) reportMissingImport(target importTarget, name string, pos token.Position, spec string) {
	if target.source != nil && c.targetDeclares(target.source, name) {
		c.errorf(pos, "E-IMPORT", "%s is not exported by %s", name, spec)
	} else {
		c.errorf(pos, "E-IMPORT", "no exported member %s in %s", name, spec)
	}
}

func standardExports(m *module.StandardModule) map[string]types.Object {
	exports := make(map[string]types.Object, len(m.Exports))
	for _, export := range m.Exports {
		if obj := standardExportObject(m, export.Name); obj != nil {
			exports[export.Name] = obj
		}
	}
	return exports
}

func standardExportObject(m *module.StandardModule, name string) types.Object {
	if m == nil {
		return nil
	}
	for _, export := range m.Exports {
		if export.Name != name {
			continue
		}
		switch export.ID {
		case module.StandardExportDB:
			return types.StandardExportObject(types.StandardExportDatabase)
		case module.StandardExportConsole:
			return types.StandardExportObject(types.StandardExportSystemConsole)
		}
	}
	return nil
}

// targetDeclares reports whether target declares a top-level name at all
// (exported or not), so the FR-16/FR-17 distinction can be made.
func (c *checker) targetDeclares(target *moduleCtx, name string) bool {
	for _, stmt := range target.mod.Prog.Statements {
		if n, _, _, _ := exportInfo(stmt); n == name {
			return true
		}
	}
	return false
}

// bindImportName declares an imported local name into a module's scope and
// into its import table, reporting a local-vs-import collision as E-NAME
// (FR-24): a name already declared by a local declaration wins and the import
// is dropped. The name may instead be held by an earlier import, whose
// declaration lies in another module; the note names that module's file.
func (c *checker) bindImportName(mc *moduleCtx, local string, obj types.Object, pos token.Position) {
	if prev := mc.scope.objs[local]; prev != nil {
		c.errorf(pos, "E-NAME", "import `%s` collides with a local declaration", local)
		c.notefIn(c.objFile(prev), prev.ObjectPos(), "E-NAME", "`%s` declared here", local)
		return
	}
	mc.scope.objs[local] = obj
	mc.importTable[local] = obj
}

// selectEntryProgram is pass 6 for the whole program: it closes the
// monomorphization set, assembles the globals, selects the single
// whole-program entry, and assembles the output lists. It mirrors selectEntry
// but calls selectEntryPointProgram in place of selectEntryPoint.
func (c *checker) selectEntryProgram() {
	c.closeMonomorphization()
	c.assembleGlobals()
	c.selectEntryPointProgram()
	c.assembleOutputLists()
}
