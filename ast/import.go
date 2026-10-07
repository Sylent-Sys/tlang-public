package ast

import (
	"strconv"
	"strings"

	"tlang/token"
)

// ImportDecl is a top-level import declaration (DESIGN-modules.md §2.2):
//
//	import Default from "path";
//	import Default, { A, B as C } from "path";
//	import { A, B as C } from "path";
//	import * as m from "path";
//
// Exactly one of Default/Namespace/Named shapes is populated per statement
// (Default may combine with Named). The specifier string From is NOT
// path-validated by the parser; module resolution handles it.
type ImportDecl struct {
	// Keyword is the position of the "import" keyword.
	Keyword token.Position
	// Default is the default-import binding ("import X from ..."); nil when
	// absent.
	Default *Identifier
	// Namespace is the namespace binding ("import * as m from ..."); nil
	// when absent. It never combines with Default or Named.
	Namespace *Identifier
	// Named are the named-import specifiers ("{ A, B as C }"); nil when the
	// statement has no named list.
	Named []*ImportSpec
	// From is the decoded specifier string (without quotes).
	From string
	// FromPos is the position of the specifier string literal's opening
	// quote.
	FromPos token.Position
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// ImportSpec is one named-import or re-export specifier: "Name" or
// "Name as Alias".
type ImportSpec struct {
	// Name is the exported name as written in the source module.
	Name *Identifier
	// Alias is the local rename ("Name as Alias"); nil when absent.
	Alias *Identifier
	// NamePos is the position of Name (same as Name.Pos()).
	NamePos token.Position
	// AsPos is the position of the "as" keyword; zero when Alias is absent.
	AsPos token.Position
}

// ReExportDecl is a top-level re-export declaration (DESIGN-modules.md §2.2):
//
//	export { A, B as C } from "path";
//
// A bare "export { ... }" without a "from" clause is a syntax error.
type ReExportDecl struct {
	// Keyword is the position of the "export" keyword.
	Keyword token.Position
	// Specs are the re-exported specifiers in source order.
	Specs []*ImportSpec
	// From is the decoded specifier string (without quotes).
	From string
	// FromPos is the position of the specifier string literal's opening
	// quote.
	FromPos token.Position
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

func (*ImportDecl) statementNode()   {}
func (*ReExportDecl) statementNode() {}

// Pos implements Node.
func (s *ImportDecl) Pos() token.Position { return s.Keyword }

// End implements Node.
func (s *ImportDecl) End() token.Position {
	return semiEnd(s.Semicolon, s.FromPos.Advance(len(strconv.Quote(s.From))))
}

// String returns a deterministic rendering, e.g.
// `import D, { A, B as C } from "path";` or `import * as m from "path";`.
func (s *ImportDecl) String() string {
	var b strings.Builder
	b.WriteString("import ")
	if s.Namespace != nil {
		b.WriteString("* as " + identString(s.Namespace))
	} else {
		wrote := false
		if s.Default != nil {
			b.WriteString(identString(s.Default))
			wrote = true
		}
		if s.Named != nil {
			if wrote {
				b.WriteString(", ")
			}
			b.WriteString(importSpecsString(s.Named))
		}
	}
	b.WriteString(" from " + strconv.Quote(s.From) + ";")
	return b.String()
}

// Pos implements Node.
func (s *ReExportDecl) Pos() token.Position { return s.Keyword }

// End implements Node.
func (s *ReExportDecl) End() token.Position {
	return semiEnd(s.Semicolon, s.FromPos.Advance(len(strconv.Quote(s.From))))
}

// String returns `export { A, B as C } from "path";`.
func (s *ReExportDecl) String() string {
	return "export " + importSpecsString(s.Specs) + " from " + strconv.Quote(s.From) + ";"
}

// importSpecsString renders a "{ A, B as C }" specifier list, or "{}" when
// empty.
func importSpecsString(specs []*ImportSpec) string {
	if len(specs) == 0 {
		return "{}"
	}
	parts := make([]string, len(specs))
	for i, sp := range specs {
		s := identString(sp.Name)
		if sp.Alias != nil {
			s += " as " + identString(sp.Alias)
		}
		parts[i] = s
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
