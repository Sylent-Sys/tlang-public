// Package checker is the semantic-analysis stage of the TLang compiler
// (DESIGN.md §2.3-§2.12): it takes the parser's *ast.Program and produces a
// populated *types.Info, the structure the C code generator reads. It reports
// semantic errors and warnings into a diag.List and never panics on a tree the
// parser produced.
package checker

import (
	"fmt"

	"tlang/ast"
	"tlang/diag"
	"tlang/module"
	"tlang/token"
	"tlang/types"
)

// codeOptional is the checker's only warning code, the W-OPTIONAL redundant
// optional warning (OQ1). It is single-sourced here so the literal cannot
// drift between the resolveType call sites that emit it; diag stores the code
// as an open string, so no edit to diag is needed.
const codeOptional = "W-OPTIONAL"

// Check type-checks prog and returns its semantic model together with the
// diagnostics it produced. The returned *diag.List carries prog.File on every
// item (diag.NewList(prog.File)) and is a fresh list, not the parser's. Info
// is complete only when diags.HasErrors() is false; codegen must run only on
// an error-free program.
func Check(prog *ast.Program) (*types.Info, *diag.List) {
	info := types.NewInfo()
	diags := diag.NewList(prog.File)
	c := &checker{
		info:        info,
		diags:       diags,
		file:        prog.File,
		global:      newScope(nil),
		declFiles:   map[types.Object]string{},
		methods:     map[string]*types.Func{},
		instSite:    map[any]filePos{},
		errDeclPos:  map[filePos]bool{},
		globalScope: map[*types.Var]*scope{},
	}
	c.run(prog)
	return info, diags
}

// checker holds the shared state threaded through every pass.
type checker struct {
	// info is the output being built.
	info *types.Info
	// diags collects diagnostics.
	diags *diag.List
	// file is the file of the module whose source is being processed, stamped
	// on every diagnostic errorf/warnf/notef report (a token.Position names no
	// file). For the single-file Check it is prog.File throughout. For
	// CheckProgram it is re-pointed together with global (inScope), so a
	// per-module pass reports in its own module's file; a whole-program pass
	// whose positions can lie in any module names the file explicitly
	// (errorfIn/notefIn with objFile).
	file string
	// global is the top-level scope of the module currently being processed
	// (its parent is nil). For the single-file Check it is the one and only
	// module scope. For CheckProgram it is re-pointed to each module's scope
	// as the passes visit that module's declarations, so every per-module
	// function resolves names in its own scope.
	global *scope

	// scopeTags maps each module scope to its mangling tag; nil for the
	// single-file Check (every object keeps the empty tag). CheckProgram
	// fills it so collect-time stamping (TypeName/Func/Var.Tag) is a scope
	// lookup.
	scopeTags map[*scope]string
	// scopeFiles maps each module scope to its module's file (Prog.File, the
	// module ID); nil for the single-file Check, whose file never changes.
	// inScope reads it to re-point file along with global.
	scopeFiles map[*scope]string
	// declFiles maps every top-level declaration object (interface, alias,
	// function, method, global) to the file of the module that declares it,
	// recorded at collect, so objFile can name the file of a declaration that
	// another module's pass reports at.
	declFiles map[types.Object]string

	// modules holds the per-module context of a whole-program CheckProgram,
	// in module order; nil for the single-file Check. Each worklist entry
	// (ifaceDecl/aliasDecl/funcDecl) remembers its owning scope so the
	// whole-program passes resolve each declaration in its own module.
	modules []*moduleCtx

	// result is the declared result type of the function whose body is
	// being checked (pass 3); nil outside a body.
	result types.Type
	// inLoop, inTry and inTx track the enclosing-statement context of the
	// body pass (DESIGN.md §2.8); unused until FEAT-002/003.
	inLoop bool
	inTry  bool
	inTx   bool

	// interfaces, aliases, funcs and globals are the pass-1 worklists, in
	// source order, so pass 2 and later passes revisit declarations without
	// re-walking the tree.
	interfaces []*ifaceDecl
	aliases    []*aliasDecl
	funcs      []*funcDecl
	globals    []*types.Var

	// globalScope maps each global *Var to its owning module's scope, so
	// the global-init / escape / may-fail passes resolve the initializer in
	// the right module. A global absent from the map resolves in c.global
	// (the single-file Check never consults it).
	globalScope map[*types.Var]*scope

	// methods is the Context-method table: receiver methods whose receiver
	// is Context, keyed by method name (they are not attached to any
	// *Named, since Context is a Basic singleton).
	methods map[string]*types.Func

	// funcWork and namedWork are the generic worklists seeded by the body
	// pass (FEAT-002) from concrete call sites and concrete type
	// expressions, in request order; FEAT-003's monomorphization closure
	// drains them.
	funcWork  []*types.Func
	namedWork []*types.Named

	// instSite maps a canonical *types.Named or *types.Func to the source
	// position (and file) of its first instantiation, for OQ7 error
	// attribution; the drained cursor records how many InstanceCache.Errors()
	// have already been reported.
	instSite map[any]filePos
	// drained is the count of InstanceCache.Errors() already reported, used
	// to attribute new Subst-time failures by error-count delta (OQ7).
	drained int

	// errDeclPos records the defining positions at which a declaration-name
	// E-NAME has already been reported (the CheckDeclName call sites in
	// declare and resolveFields). The pass-6 C-name collision pass consults
	// it to avoid re-reporting a declaration that 1a already rejected (so a
	// reserved-name type such as `interface globals` yields one E-NAME, not
	// two). Keyed with the file, since two modules can declare at the same
	// line and column.
	errDeclPos map[filePos]bool
}

// filePos is a source position together with the file it lies in, for a
// position recorded while one module is processed and read back later,
// possibly while another module is.
type filePos struct {
	file string
	pos  token.Position
}

// moduleCtx is the per-module context of a whole-program CheckProgram
// (DESIGN-modules.md §4). It pairs a module with its own top-level scope, its
// pass-0 export-intent map, its import table, and the resolved export set
// pass 1 fills.
type moduleCtx struct {
	// mod is the module this context belongs to.
	mod *module.Module
	// scope is the module's top-level scope (parent nil); every
	// declaration of the module is declared here and resolves names here.
	scope *scope
	// importTable binds each imported/aliased local name to the target
	// object it resolves to (pass 0). It is declared into scope in pass 1.
	importTable map[string]types.Object
	// exportDecls is the pass-0 intent map: each name this module exports
	// directly (its own Exported declarations) to the declaring statement.
	// It is filled before any cross-module read; re-export edges are then
	// resolved against it.
	exportDecls map[string]ast.Statement
	// exports is the module's resolved named export set, keyed by exported
	// name; nil until pass 1 fills it (the object each export name denotes).
	exports map[string]types.Object
	// defaultStmt is the module's `export default` declaration, or nil
	// (recorded in pass 0; its object is bound in pass 1).
	defaultStmt ast.Statement
	// defaultExport is the module's default export object, or nil.
	defaultExport types.Object
}

// ifaceDecl pairs an interface declaration with the *Named/*TypeName pass 1
// created for it, so pass 2 can resolve its fields. sc is the owning module's
// scope (c.global for the single-file Check).
type ifaceDecl struct {
	stmt  *ast.InterfaceStatement
	named *types.Named
	obj   *types.TypeName
	sc    *scope
}

// aliasDecl pairs a type-alias declaration with the object pass 1 created:
// either a transparent alias *TypeName or a nominal *Named (object-type
// alias).
type aliasDecl struct {
	stmt  *ast.TypeAliasStatement
	alias *types.TypeName // non-nil for a transparent alias
	named *types.Named    // non-nil for an object-type (nominal) alias
	obj   *types.TypeName
	sc    *scope
}

// funcDecl pairs a function declaration with the *Func pass 1 created, so
// pass 2 can resolve its signature and receiver.
type funcDecl struct {
	stmt *ast.FunctionStatement
	fn   *types.Func
	sc   *scope
}

// run drives the passes. Passes 1-2 (resolve.go) establish the global name
// environment and resolve every declared type and signature. Pass 3
// (typecheck.go) checks every body. Passes 4-6 (escape.go, mayfail.go,
// entry.go) are the cross-cutting analyses, with @Use guard resolution run
// between pass 3 and pass 4 so the may-fail fixed point sees the guards.
func (c *checker) run(prog *ast.Program) {
	c.collectNames(prog)  // pass 1
	c.resolveSignatures() // pass 2
	c.checkBodies(prog)   // pass 3 (FEAT-002)
	// Passes 4-6 are cross-cutting. They read the model pass 3 built and each
	// recovers with Invalid/nil guards (a global whose type stayed Invalid, a
	// call with no recorded Call, a non-concrete instance), so they run even
	// after earlier errors and never panic on a parser-accepted tree; Check
	// always returns a (possibly partial) Info.
	c.resolveGuards()   // @Use guards (pass 6 work the may-fail fixed point needs)
	c.analyzeEscape()   // pass 4 (FEAT-003)
	c.markMayFail()     // pass 5 (FEAT-003)
	c.selectEntry(prog) // pass 6 (FEAT-003)
	// After selectEntry has assembled the concrete output lists, reject any
	// two file-scope C names that collide (DESIGN.md §3.2, codegen §12 item
	// 8): the authoritative check now lives here, with codegen's nameTable as
	// a defensive backstop.
	c.checkCNameCollisions()
}

// scopeTag returns the mangling tag of a module scope: "" for the
// single-file Check (scopeTags nil) and for the empty-tag root module, else
// the tag CheckProgram recorded.
func (c *checker) scopeTag(sc *scope) string {
	if c.scopeTags == nil {
		return ""
	}
	return c.scopeTags[sc]
}

// errorf reports an error with code at pos in the current file (c.file).
func (c *checker) errorf(pos token.Position, code, format string, args ...any) {
	c.report(diag.Error, c.file, pos, code, format, args)
}

// warnf reports a warning with code at pos in the current file (c.file).
func (c *checker) warnf(pos token.Position, code, format string, args ...any) {
	c.report(diag.Warning, c.file, pos, code, format, args)
}

// notef reports a note at pos in the current file (c.file), explaining the
// preceding diagnostic.
func (c *checker) notef(pos token.Position, code, format string, args ...any) {
	c.report(diag.Note, c.file, pos, code, format, args)
}

// errorfIn is errorf at a position in file, for a whole-program pass whose
// position may lie in a module other than the current one.
func (c *checker) errorfIn(file string, pos token.Position, code, format string, args ...any) {
	c.report(diag.Error, file, pos, code, format, args)
}

// notefIn is notef at a position in file: a note at a declaration names the
// file of that declaration, which may differ from the preceding error's.
func (c *checker) notefIn(file string, pos token.Position, code, format string, args ...any) {
	c.report(diag.Note, file, pos, code, format, args)
}

// report appends one diagnostic stamped with file. For the single-file Check
// file is prog.File, the list's own default, so the items are exactly what
// diag.List.Errorf/Warnf/Notef would add.
func (c *checker) report(sev diag.Severity, file string, pos token.Position, code, format string, args []any) {
	c.diags.Add(diag.Diagnostic{File: file, Pos: pos, Severity: sev, Code: code, Message: fmt.Sprintf(format, args...)})
}

// objFile returns the file of the module that declares obj: the file
// recorded at collect for a top-level interface, alias, function, method or
// global (a generic function instance resolves through its origin; an
// interface instance shares its origin's *TypeName). Any other object (a
// local, parameter, field or type parameter) is declared in the module being
// processed and resolves to c.file; a field of another module's interface is
// reported through the interface's object instead.
func (c *checker) objFile(obj types.Object) string {
	if f, ok := obj.(*types.Func); ok && f != nil && f.Origin != nil {
		obj = f.Origin
	}
	if file, ok := c.declFiles[obj]; ok {
		return file
	}
	return c.file
}

// inFile runs fn with c.file temporarily set to file, for a whole-program
// pass that reports in one module outside inScope (the monomorphization
// closure, at a generic origin's body or an instance's recorded site).
func (c *checker) inFile(file string, fn func()) {
	prev := c.file
	c.file = file
	fn()
	c.file = prev
}

// recordType records the resolved type of a type expression in
// Info.TypeExprs and returns it.
func (c *checker) recordType(e ast.TypeExpr, t types.Type) types.Type {
	c.info.TypeExprs[e] = t
	return t
}

// setInvalid records e as Typ[Invalid] and returns it, so one resolution
// error does not cascade.
func (c *checker) setInvalid(e ast.TypeExpr) types.Type {
	return c.recordType(e, types.Typ[types.Invalid])
}
