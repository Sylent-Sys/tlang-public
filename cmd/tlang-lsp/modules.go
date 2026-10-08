package main

import (
	"path/filepath"
	"sort"
	"strings"

	"tlang/ast"
	"tlang/module"
	"tlang/types"
)

// moduleContext is what module-aware queries need beyond one document's AST
// and Info: the module graph the document was analyzed in, its own module,
// and the file access used to build it (for resolving a specifier that no
// edge records yet).
type moduleContext struct {
	graph   *module.Graph
	mod     *module.Module // the document's module (graph.Root)
	info    *types.Info
	fsys    module.FileSystem
	rootDir string

	// sites maps each declared object to its declaring identifier and module.
	// token.Position carries no file, so the module is the one whose AST owns
	// the identifier. Built on first use.
	sites map[types.Object]declSite
}

// declSite is where a name is declared: an identifier in a module's AST.
// stmt is the top-level declaration when the site came from an export
// lookup, so a kind is known even without type information.
type declSite struct {
	mod  *module.Module
	id   *ast.Identifier
	stmt ast.Statement
}

// topDecl returns the declared name of a top-level declaration that can be
// exported (a plain function, interface, type alias, or global) with its
// export flags, mirroring the checker's export rules. Methods and other
// statements return a nil identifier.
func topDecl(s ast.Statement) (id *ast.Identifier, exported, isDefault bool) {
	switch d := s.(type) {
	case *ast.FunctionStatement:
		if d.Receiver != nil {
			return nil, false, false
		}
		return d.Name, d.Exported, d.IsDefault
	case *ast.InterfaceStatement:
		return d.Name, d.Exported, d.IsDefault
	case *ast.TypeAliasStatement:
		return d.Name, d.Exported, d.IsDefault
	case *ast.LetStatement:
		return d.Name, d.Exported, d.IsDefault
	}
	return nil, false, false
}

// target returns the module that mod's specifier spec resolved to, or nil.
func (mc *moduleContext) target(mod *module.Module, spec string) *module.Module {
	if mod == nil {
		return nil
	}
	for _, e := range mod.Imports {
		if e.Spec == spec && e.Target.Kind == module.ImportTargetSource {
			return e.Target.Source
		}
	}
	return nil
}

// Export resolution mirrors the checker (checker/program.go), which applies
// two rules. A module's export set (what `import { X }` and `m.X` bind) is
// its direct exports overwritten by its re-exports in source order, so a
// resolvable `export { Y as X } from` beats a direct `export fn X`. Following
// a re-export chain through a module (followExport) checks its direct exports
// first, then its first re-export of the name. The two rules can only
// disagree on a name a module exports twice, which the checker rejects
// (E-IMPORT "duplicate export"); it still binds the rest of that program by
// both rules, so the LSP reproduces each where the checker uses it and its
// answers keep matching the checker's Info while the error is being fixed.

// directExport returns mod's own exported declaration of name.
func directExport(mod *module.Module, name string) (declSite, bool) {
	for _, s := range mod.Prog.Statements {
		if id, exported, _ := topDecl(s); id != nil && exported && id.Name == name {
			return declSite{mod: mod, id: id, stmt: s}, true
		}
	}
	return declSite{}, false
}

// followExport resolves name through mod the way the checker follows a
// re-export chain: a direct export of mod, else the first re-export of name
// whose specifier resolved, followed into its target. A re-export cycle or a
// missing name yields ok == false.
func (mc *moduleContext) followExport(mod *module.Module, name string) (declSite, bool) {
	visited := map[*module.Module]bool{}
	for mod != nil && !visited[mod] {
		visited[mod] = true
		if site, ok := directExport(mod, name); ok {
			return site, true
		}
		var next *module.Module
		nextName := ""
	specs:
		for _, s := range mod.Prog.Statements {
			re, ok := s.(*ast.ReExportDecl)
			if !ok {
				continue
			}
			for _, sp := range re.Specs {
				if t := mc.target(mod, re.From); t != nil && specLocalName(sp) == name {
					next, nextName = t, sp.Name.Name
					break specs
				}
			}
		}
		mod, name = next, nextName
	}
	return declSite{}, false
}

// exportSet is mod's export set as the checker binds it: direct exports, then
// each re-export (resolved with followExport) overwriting by exported name.
func (mc *moduleContext) exportSet(mod *module.Module) map[string]declSite {
	set := map[string]declSite{}
	if mod == nil {
		return set
	}
	for _, s := range mod.Prog.Statements {
		if id, exported, _ := topDecl(s); id != nil && exported {
			set[id.Name] = declSite{mod: mod, id: id, stmt: s}
		}
	}
	for _, s := range mod.Prog.Statements {
		re, ok := s.(*ast.ReExportDecl)
		if !ok {
			continue
		}
		for _, sp := range re.Specs {
			if site, ok := mc.followExport(mc.target(mod, re.From), sp.Name.Name); ok {
				set[specLocalName(sp)] = site
			}
		}
	}
	return set
}

// exportOf returns the declaration that importing name from mod binds.
func (mc *moduleContext) exportOf(mod *module.Module, name string) (declSite, bool) {
	site, ok := mc.exportSet(mod)[name]
	return site, ok
}

// defaultExport returns mod's `export default` declaration.
func (mc *moduleContext) defaultExport(mod *module.Module) (declSite, bool) {
	if mod == nil {
		return declSite{}, false
	}
	for _, s := range mod.Prog.Statements {
		if id, exported, isDefault := topDecl(s); id != nil && exported && isDefault {
			return declSite{mod: mod, id: id, stmt: s}, true
		}
	}
	return declSite{}, false
}

// exportEntry is one name a module exports, with its resolved declaration.
type exportEntry struct {
	name string
	site declSite
}

// exports lists mod's export set sorted by name.
func (mc *moduleContext) exports(mod *module.Module) []exportEntry {
	var out []exportEntry
	for name, site := range mc.exportSet(mod) {
		out = append(out, exportEntry{name: name, site: site})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// namespaceImport returns the target of the document's `import * as name`,
// or nil when name is not a namespace import.
func (mc *moduleContext) namespaceImport(name string) *module.Module {
	for _, s := range mc.mod.Prog.Statements {
		if d, ok := s.(*ast.ImportDecl); ok && d.Namespace != nil && d.Namespace.Name == name {
			return mc.target(mc.mod, d.From)
		}
	}
	return nil
}

// namespaceTarget returns the module a ModuleNS object names: the target of
// the document's namespace import that declared it.
func (mc *moduleContext) namespaceTarget(ns *types.ModuleNS) *module.Module {
	for _, s := range mc.mod.Prog.Statements {
		d, ok := s.(*ast.ImportDecl)
		if ok && d.Namespace != nil && d.Namespace.Name == ns.Name && d.Namespace.NamePos == ns.Pos {
			return mc.target(mc.mod, d.From)
		}
	}
	return mc.namespaceImport(ns.Name)
}

// namespaceOf resolves the receiver identifier of "m." or "m.X" to the module
// a namespace import names. A recorded use decides when present (so a local
// that shadows the import is not mistaken for it); otherwise, while the
// expression is still incomplete, the identifier text is looked up among the
// document's namespace imports.
func (mc *moduleContext) namespaceOf(id *ast.Identifier, text string) *module.Module {
	if id != nil {
		switch obj := mc.info.ObjectOf(id).(type) {
		case *types.ModuleNS:
			return mc.namespaceTarget(obj)
		case nil:
		default:
			return nil
		}
	}
	return mc.namespaceImport(text)
}

// siteOf returns the declaration of obj in the graph. A generic instance is
// mapped to its origin first.
func (mc *moduleContext) siteOf(obj types.Object) (declSite, bool) {
	obj = originOf(obj)
	if obj == nil {
		return declSite{}, false
	}
	if mc.sites == nil {
		mc.sites = map[types.Object]declSite{}
		for _, m := range mc.graph.Modules {
			ast.Inspect(m.Prog, func(n ast.Node) bool {
				if id, ok := n.(*ast.Identifier); ok {
					if def := mc.info.Defs[id]; def != nil {
						if _, dup := mc.sites[def]; !dup {
							mc.sites[def] = declSite{mod: m, id: id}
						}
					}
				}
				return true
			})
		}
	}
	site, ok := mc.sites[obj]
	return site, ok
}

// objectOf returns the checked object a declaration site declares, or nil.
func (mc *moduleContext) objectOf(site declSite) types.Object {
	if site.id == nil || mc.info == nil {
		return nil
	}
	return mc.info.Defs[site.id]
}

// moduleLabel names a module for hover and completion details: its ID, or
// the specifier when it did not resolve.
func moduleLabel(mod *module.Module, spec string) string {
	if mod == nil {
		return spec + " (unresolved)"
	}
	return mod.ID
}

// probeFile is the synthetic module resolveSpec builds next to the document.
const probeFile = "tlang-lsp-specifier-probe.ts"

// resolveSpec resolves a specifier written in the document. An edge of the
// analyzed graph answers when one exists; otherwise (the import statement does
// not parse yet, e.g. "import { } from ...") a one-statement probe module in
// the document's directory is built through the same overlay, so resolution
// follows module's rules exactly.
func (mc *moduleContext) resolveSpec(spec string) *module.Module {
	if t := mc.target(mc.mod, spec); t != nil {
		return t
	}
	if strings.ContainsAny(spec, "\"\\\n\r") {
		return nil
	}
	probe := filepath.Join(filepath.Dir(mc.mod.AbsPath), probeFile)
	fsys := &probeFS{FileSystem: mc.fsys, path: pathKey(probe), text: "import * as probe from \"" + spec + "\";\n"}
	g, _ := module.BuildWith(probe, module.BuildOptions{FS: fsys, RootDir: mc.rootDir})
	if g.Root == nil || len(g.Root.Imports) == 0 {
		return nil
	}
	if g.Root.Imports[0].Target.Kind != module.ImportTargetSource {
		return nil
	}
	return g.Root.Imports[0].Target.Source
}

// probeFS serves the probe module's text and delegates everything else.
type probeFS struct {
	module.FileSystem
	path string // pathKey of the probe file
	text string
}

func (p *probeFS) ReadFile(absPath string) ([]byte, error) {
	if pathKey(absPath) == p.path {
		return []byte(p.text), nil
	}
	return p.FileSystem.ReadFile(absPath)
}

func (p *probeFS) IsFile(absPath string) bool {
	return pathKey(absPath) == p.path || p.FileSystem.IsFile(absPath)
}

// specLocalName is the name a specifier binds: the alias when present.
func specLocalName(sp *ast.ImportSpec) string {
	if sp.Alias != nil {
		return sp.Alias.Name
	}
	return sp.Name.Name
}

// originOf maps a generic instance (function, or field/parameter of an
// instance) to the object declared in source.
func originOf(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Func:
		for o.Origin != nil {
			o = o.Origin
		}
		return o
	case *types.Var:
		for o.Origin != nil {
			o = o.Origin
		}
		return o
	}
	return obj
}
