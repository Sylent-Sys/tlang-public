package main

import (
	"tlang/ast"
	"tlang/module"
	"tlang/token"
	"tlang/types"
)

// importHitKind classifies what the cursor is on inside an import or
// re-export declaration.
type importHitKind int

const (
	hitNone      importHitKind = iota // the statement, but no name or specifier
	hitSpec                           // the specifier string: from "./y"
	hitNamespace                      // the m of import * as m
	hitDefault                        // the D of import D from
	hitName                           // X or Y of { X as Y } (import or re-export)
)

// importHit is the cursor's place in an import or re-export declaration.
type importHit struct {
	kind     importHitKind
	target   *module.Module // the module the specifier resolved to, or nil
	name     string         // hitNamespace: the binding; hitName: the exported name in target
	reExport bool           // the statement is `export { ... } from`
}

// site returns the declaration a default or named hit refers to, resolved as
// the checker binds it: an import takes the target's export set, a re-export
// follows the chain rule (see followExport).
func (mc *moduleContext) site(h importHit) (declSite, bool) {
	switch h.kind {
	case hitDefault:
		return mc.defaultExport(h.target)
	case hitName:
		if h.reExport {
			return mc.followExport(h.target, h.name)
		}
		return mc.exportOf(h.target, h.name)
	}
	return declSite{}, false
}

// importAt finds the import or re-export declaration of the document that
// covers offset. These statements need their own lookup because ast.Walk does
// not descend into them.
func (mc *moduleContext) importAt(src []byte, offset int) (importHit, bool) {
	for _, s := range mc.mod.Prog.Statements {
		var from string
		var fromPos token.Position
		var specs []*ast.ImportSpec
		var ns, def *ast.Identifier
		reExport := false
		switch d := s.(type) {
		case *ast.ImportDecl:
			from, fromPos, specs, ns, def = d.From, d.FromPos, d.Named, d.Namespace, d.Default
		case *ast.ReExportDecl:
			from, fromPos, specs, reExport = d.From, d.FromPos, d.Specs, true
		default:
			continue
		}
		if !covers(s, offset) {
			continue
		}
		h := importHit{target: mc.target(mc.mod, from), reExport: reExport}
		switch {
		case inStringLit(src, fromPos, offset):
			h.kind = hitSpec
		case onIdent(ns, offset):
			h.kind, h.name = hitNamespace, ns.Name
		case onIdent(def, offset):
			h.kind = hitDefault
		default:
			for _, sp := range specs {
				if onIdent(sp.Name, offset) || onIdent(sp.Alias, offset) {
					h.kind, h.name = hitName, sp.Name.Name
				}
			}
		}
		return h, true
	}
	return importHit{}, false
}

// describeImport is the hover text for the cursor inside an import or
// re-export declaration; inImport reports whether the cursor was in one.
func (mc *moduleContext) describeImport(src []byte, offset int) (desc string, inImport bool) {
	h, ok := mc.importAt(src, offset)
	if !ok {
		return "", false
	}
	switch h.kind {
	case hitSpec:
		if h.target != nil {
			return "module \"" + h.target.ID + "\"", true
		}
	case hitNamespace:
		return namespaceText(h.name, h.target), true
	case hitDefault, hitName:
		site, _ := mc.site(h)
		return describeObject(mc.objectOf(site)), true
	}
	return "", true
}

// namespaceText describes a namespace binding and the module it names.
func namespaceText(name string, target *module.Module) string {
	if target == nil {
		return "module " + name
	}
	return "module " + name + " (" + target.ID + ")"
}

// defTarget is a definition result: a declaring identifier in mod's file, or
// the start of that file when pos is the zero Position. mod is nil on the
// single-file path, meaning the document itself.
type defTarget struct {
	mod *module.Module
	pos token.Position
	n   int // length of the name at pos
}

func siteTarget(site declSite) defTarget {
	return defTarget{mod: site.mod, pos: site.id.NamePos, n: len(site.id.Name)}
}

// definitionIn resolves the declaration the cursor at offset refers to. mc is
// the document's module context, or nil on the single-file path.
//
// Inside an import or re-export declaration: the specifier string and a
// namespace binding go to the start of the target file, a default binding to
// the target's `export default` declaration, and a named specifier (either
// side of "as") to the original declaration, following re-export chains.
// Elsewhere the identifier's object (a use, a declaration, a decorator
// argument, a member's selection, a qualified type's name) goes to its
// declaring identifier, and a namespace name to the start of its file.
func definitionIn(mc *moduleContext, info *types.Info, prog *ast.Program, src []byte, offset int) (defTarget, bool) {
	if info == nil || prog == nil {
		return defTarget{}, false
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	if mc != nil {
		if h, ok := mc.importAt(src, offset); ok {
			return mc.importDefinition(h)
		}
	}

	c := locate(prog, offset)
	if c.id == nil {
		return defTarget{}, false
	}
	obj := info.ObjectOf(c.id)
	if obj == nil && c.meByProp != nil {
		if sel := info.Selections[c.meByProp]; sel != nil {
			switch {
			case sel.Func != nil:
				obj = sel.Func
			case sel.Field != nil:
				obj = sel.Field
			}
		}
	}

	if mc == nil {
		obj = originOf(obj)
		if obj == nil || !obj.ObjectPos().IsValid() {
			return defTarget{}, false
		}
		return defTarget{pos: obj.ObjectPos(), n: len(obj.ObjectName())}, true
	}

	if ns, ok := obj.(*types.ModuleNS); ok {
		if t := mc.namespaceTarget(ns); t != nil {
			return defTarget{mod: t}, true
		}
		return defTarget{}, false
	}
	if obj != nil {
		if site, ok := mc.siteOf(obj); ok {
			return siteTarget(site), true
		}
		return defTarget{}, false
	}

	// Unrecorded names (an error kept the checker from recording them):
	// resolve the m of "m.X" / "m.User", and the X / User, through the
	// document's namespace import.
	if (c.meCovering != nil && c.meCovering.Object == c.id) || (c.named != nil && c.named.Qualifier == c.id) {
		if t := mc.namespaceImport(c.id.Name); t != nil {
			return defTarget{mod: t}, true
		}
	}
	var qual *ast.Identifier
	switch {
	case c.meByProp != nil:
		qual, _ = c.meByProp.Object.(*ast.Identifier)
	case c.named != nil && c.named.Qualifier != nil && c.named.Name == c.id:
		qual = c.named.Qualifier
	}
	if qual != nil {
		if t := mc.namespaceOf(qual, qual.Name); t != nil {
			if site, ok := mc.exportOf(t, c.id.Name); ok {
				return siteTarget(site), true
			}
		}
	}
	return defTarget{}, false
}

// importDefinition is the definition target of a cursor inside an import or
// re-export declaration.
func (mc *moduleContext) importDefinition(h importHit) (defTarget, bool) {
	if h.target == nil {
		return defTarget{}, false
	}
	switch h.kind {
	case hitSpec, hitNamespace:
		return defTarget{mod: h.target}, true
	case hitDefault, hitName:
		if site, ok := mc.site(h); ok {
			return siteTarget(site), true
		}
	}
	return defTarget{}, false
}

// onIdent reports whether offset is on id.
func onIdent(id *ast.Identifier, offset int) bool {
	return id != nil && id.Pos().Offset <= offset && offset < id.End().Offset
}

// inStringLit reports whether offset is on the string literal that opens at
// pos, quotes included.
func inStringLit(src []byte, pos token.Position, offset int) bool {
	open := pos.Offset
	if !pos.IsValid() || open >= len(src) || (src[open] != '"' && src[open] != '\'') {
		return false
	}
	end := stringEnd(src, open)
	if end < 0 {
		end = len(src) - 1
	}
	return open <= offset && offset <= end
}

// targetRange is the LSP range of a definition target: the declaring name,
// or the empty range at the start of the file.
func (t defTarget) targetRange() Range {
	if !t.pos.IsValid() {
		return Range{}
	}
	start := Position{Line: t.pos.Line - 1, Character: t.pos.Column - 1}
	return Range{Start: start, End: Position{Line: start.Line, Character: start.Character + t.n}}
}
