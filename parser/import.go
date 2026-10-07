package parser

import (
	"tlang/ast"
	"tlang/token"
)

// codeImport is the diagnostic code for import/export-list errors. It is an
// open-string code literal (no diag package change, NFR-6): E-IMPORT covers
// an empty import list and a duplicate local name within one statement.
const codeImport = "E-IMPORT"

// parseImport parses an import declaration (DESIGN-modules.md §2.2):
//
//	import ( default (',' '{' specs '}')? | '{' specs '}' | '*' 'as' IDENT )
//	       from STRING ';'
//
// The current token is "import". An empty "{}" is E-IMPORT; a duplicate local
// name within one statement is E-IMPORT at the second occurrence; mixing a
// namespace with a default or named list is E-PARSE. The specifier string is
// not path-validated here (deferred to module resolution).
func (p *parser) parseImport() ast.Statement {
	from := p.cur().Pos
	kw := p.advance() // "import"
	decl := &ast.ImportDecl{Keyword: kw.Pos}

	switch {
	case p.curIs(token.ASTERISK):
		// Namespace import: "* as IDENT". It never mixes with default/named.
		p.advance() // "*"
		if _, ok := p.expect(token.AS); !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		name, ok := p.expect(token.IDENT)
		if !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		decl.Namespace = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
		if p.curIs(token.COMMA) {
			p.errorf(p.cur().Pos, "a namespace import may not combine with named or default imports")
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
	case p.curIs(token.LBRACE):
		specs, ok := p.parseImportSpecs()
		if !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		decl.Named = specs
	case p.curIs(token.IDENT):
		name := p.advance()
		decl.Default = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
		if p.curIs(token.COMMA) {
			p.advance() // ","
			if p.curIs(token.ASTERISK) {
				p.errorf(p.cur().Pos, "a namespace import may not combine with named or default imports")
				to := p.synchronize()
				return &ast.BadStatement{From: from, To: to}
			}
			specs, ok := p.parseImportSpecs()
			if !ok {
				to := p.synchronize()
				return &ast.BadStatement{From: from, To: to}
			}
			decl.Named = specs
		}
	default:
		p.errorf(p.cur().Pos, "expected an import clause, got %s", describe(p.cur()))
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}

	p.checkImportDupes(decl)

	if _, ok := p.expect(token.FROM); !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	str, ok := p.expect(token.STRING)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	decl.From = str.Literal
	decl.FromPos = str.Pos
	semi, _ := p.expect(token.SEMICOLON)
	decl.Semicolon = semi.Pos
	return decl
}

// parseReExport parses a re-export declaration (DESIGN-modules.md §2.2):
//
//	export '{' specs '}' from STRING ';'
//
// The current token is "export" and the next is "{". A bare "export { ... }"
// with no "from" clause is E-PARSE reported right after "}" (no rewind).
func (p *parser) parseReExport() ast.Statement {
	from := p.cur().Pos
	kw := p.advance() // "export"
	decl := &ast.ReExportDecl{Keyword: kw.Pos}
	specs, ok := p.parseImportSpecs()
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	decl.Specs = specs
	if !p.curIs(token.FROM) {
		p.errorf(p.cur().Pos, "re-export requires `from`")
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	p.advance() // "from"
	str, ok := p.expect(token.STRING)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	decl.From = str.Literal
	decl.FromPos = str.Pos
	p.checkReExportDupes(decl)
	semi, _ := p.expect(token.SEMICOLON)
	decl.Semicolon = semi.Pos
	return decl
}

// parseImportSpecs parses "{ IDENT (as IDENT)? (, IDENT (as IDENT)?)* }". The
// current token is "{". An empty "{}" is E-IMPORT at the "{". It returns the
// specifiers and whether the list was well-formed.
func (p *parser) parseImportSpecs() ([]*ast.ImportSpec, bool) {
	lb := p.advance() // "{"
	if p.curIs(token.RBRACE) {
		p.diags.Errorf(lb.Pos, codeImport, "empty import list")
		p.advance() // "}"
		return nil, false
	}
	var specs []*ast.ImportSpec
	for !p.curIs(token.RBRACE) && !p.curIs(token.EOF) {
		name, ok := p.expect(token.IDENT)
		if !ok {
			return nil, false
		}
		spec := &ast.ImportSpec{
			Name:    &ast.Identifier{NamePos: name.Pos, Name: name.Literal},
			NamePos: name.Pos,
		}
		if p.curIs(token.AS) {
			as := p.advance()
			spec.AsPos = as.Pos
			alias, ok := p.expect(token.IDENT)
			if !ok {
				return nil, false
			}
			spec.Alias = &ast.Identifier{NamePos: alias.Pos, Name: alias.Literal}
		}
		specs = append(specs, spec)
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	if _, ok := p.expect(token.RBRACE); !ok {
		return nil, false
	}
	return specs, true
}

// checkImportDupes reports a duplicate local binding within one import
// statement (E-IMPORT at the second occurrence). The local name is the alias
// when present, else the imported name; the default and namespace bindings
// also count.
func (p *parser) checkImportDupes(decl *ast.ImportDecl) {
	seen := map[string]bool{}
	note := func(id *ast.Identifier) {
		if id == nil || id.Name == "" {
			return
		}
		if seen[id.Name] {
			p.diags.Errorf(id.NamePos, codeImport, "duplicate import name %q", id.Name)
			return
		}
		seen[id.Name] = true
	}
	note(decl.Default)
	note(decl.Namespace)
	for _, sp := range decl.Named {
		note(specLocal(sp))
	}
}

// checkReExportDupes reports a duplicate exported-as name within one
// re-export statement (E-IMPORT at the second occurrence).
func (p *parser) checkReExportDupes(decl *ast.ReExportDecl) {
	seen := map[string]bool{}
	for _, sp := range decl.Specs {
		id := specLocal(sp)
		if id == nil || id.Name == "" {
			continue
		}
		if seen[id.Name] {
			p.diags.Errorf(id.NamePos, codeImport, "duplicate import name %q", id.Name)
			continue
		}
		seen[id.Name] = true
	}
}

// specLocal returns the local binding of a specifier: the alias when present,
// otherwise the imported name.
func specLocal(sp *ast.ImportSpec) *ast.Identifier {
	if sp.Alias != nil {
		return sp.Alias
	}
	return sp.Name
}
