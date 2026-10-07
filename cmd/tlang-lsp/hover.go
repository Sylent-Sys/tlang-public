package main

import (
	"tlang/ast"
	"tlang/types"
)

// hover returns hover content for the identifier at a byte offset into src on
// the single-file path (no module context).
func hover(info *types.Info, prog *ast.Program, src []byte, offset int) *Hover {
	return hoverIn(nil, info, prog, src, offset)
}

// hoverIn returns hover content for the identifier at a byte offset into src,
// or nil when nothing resolves (whitespace, punctuation, a non-identifier).
// The content is a markdown tlang code fence. mc is the document's module
// context, or nil on the single-file path; with it, names inside import and
// re-export declarations, the specifier string, and namespace names resolve
// too.
func hoverIn(mc *moduleContext, info *types.Info, prog *ast.Program, src []byte, offset int) *Hover {
	if info == nil || prog == nil {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}

	if mc != nil {
		if desc, inImport := mc.describeImport(src, offset); inImport {
			return codeHover(desc)
		}
	}
	c := locate(prog, offset)
	if c.id == nil {
		return nil
	}
	if mc != nil {
		if ns, ok := info.ObjectOf(c.id).(*types.ModuleNS); ok {
			return codeHover(namespaceText(ns.Name, mc.namespaceTarget(ns)))
		}
	}
	return codeHover(describe(info, c.id, c.meByProp, c.meCovering))
}

// codeHover wraps desc in a tlang code fence; "" yields nil.
func codeHover(desc string) *Hover {
	if desc == "" {
		return nil
	}
	return &Hover{Contents: MarkupContent{
		Kind:  "markdown",
		Value: "```tlang\n" + desc + "\n```",
	}}
}

// cursor is what locate finds at an offset: the innermost covering
// identifier, the member expression whose Property it is (pointer match), the
// innermost member expression covering the offset, and the named type whose
// Name or Qualifier it is.
type cursor struct {
	id         *ast.Identifier
	meByProp   *ast.MemberExpression
	meCovering *ast.MemberExpression
	named      *ast.NamedType
}

// locate walks prog for the nodes at offset (see cursor). Identifiers inside
// import and re-export declarations are not visited (ast.Walk has no children
// for them).
func locate(prog *ast.Program, offset int) cursor {
	var c cursor
	ast.Inspect(prog, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Identifier:
			p, e := node.Pos().Offset, node.End().Offset
			if p <= offset && offset < e {
				if c.id == nil || (p >= c.id.Pos().Offset && e <= c.id.End().Offset) {
					c.id = node
				}
			}
		case *ast.MemberExpression:
			if covers(node, offset) {
				if c.meCovering == nil ||
					(node.Pos().Offset >= c.meCovering.Pos().Offset && node.End().Offset <= c.meCovering.End().Offset) {
					c.meCovering = node
				}
			}
		}
		return true
	})
	if c.id == nil {
		return c
	}
	// Resolve the Property->MemberExpression and NamedType links now that id
	// is known.
	ast.Inspect(prog, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.MemberExpression:
			if node.Property == c.id {
				c.meByProp = node
			}
		case *ast.NamedType:
			if node.Name == c.id || node.Qualifier == c.id {
				c.named = node
			}
		}
		return true
	})
	return c
}

// describe resolves the hover text for id, preferring its Object, then a member
// selection, then the expression's type.
func describe(info *types.Info, id *ast.Identifier, meByProp, meCovering *ast.MemberExpression) string {
	if s := describeObject(info.ObjectOf(id)); s != "" {
		return s
	}

	// Member property: Info.Uses/Defs do not record a MemberExpression's
	// Property, so derive from the enclosing selection.
	me := meByProp
	if me == nil {
		me = meCovering
	}
	if me != nil {
		if sel := info.Selections[me]; sel != nil {
			switch sel.Kind {
			case types.SelField, types.SelModuleValue:
				if sel.Field != nil && sel.Field.Type != nil {
					return sel.Field.Type.String()
				}
			case types.SelMethod:
				if sel.Func != nil && sel.Func.Sig != nil {
					return sel.Func.Sig.String()
				}
			case types.SelBuiltin:
				return sel.Builtin.String()
			}
		}
	}

	// Last resort: the type of the identifier used as an expression.
	if t := info.TypeOf(id); t != nil {
		return t.String()
	}
	return ""
}

// describeObject is the hover text of an object: a variable's type, a
// function's signature, or the object's own description.
func describeObject(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Var:
		if o.Type != nil {
			return o.Type.String()
		}
	case *types.Func:
		if o.Sig != nil {
			return o.Sig.String()
		}
	case *types.TypeName:
		return o.String()
	case *types.Builtin:
		return o.String()
	case *types.ModuleNS:
		return o.String()
	}
	return ""
}
