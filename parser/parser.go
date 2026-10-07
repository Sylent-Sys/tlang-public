// Package parser turns the lexer's []token.Token into an *ast.Program (spec
// §5.3, grammar of DESIGN.md §2.2). It is a Pratt (precedence-climbing)
// parser with error recovery: it never panics on any input, and one parse
// reports as many independent syntax errors as it can.
//
// # Diagnostics
//
// Syntax errors are reported with code diag.CodeParse ("E-PARSE") at a useful
// position, in the house style (lower-case, no trailing period). An ILLEGAL
// token already carries an E-LEX diagnostic from the lexer, so the parser
// surfaces it through normal recovery WITHOUT re-reporting it (D-ILLEGAL). The
// list returned by Parse holds only the parser's own E-PARSE items; a driver
// combines it with the lexer's list (see ParseSource).
//
// # Error recovery
//
// On a syntax error the parser records one diagnostic, synchronizes to the
// next statement/declaration boundary, and substitutes the smallest enclosing
// *ast.BadStatement, *ast.BadExpression or *ast.BadType so the tree stays
// walkable and every required child is non-nil (ast.go invariants).
//
// # Resolved design decisions
//
// The parser follows the implementation plan's resolved decisions; the key
// ones are documented inline where they take effect:
//
//   - D-ASSIGN: assignment and ++/-- are statement-level only. The Pratt
//     expression parser has no ASSIGN level, so chained "a = b = c",
//     "f(x = 1)" and prefix "++x" are syntax errors naturally.
//   - D-GENCALL / D-SNAPSHOT: a "<" after an identifier or member expression
//     is parsed speculatively as a type-argument list; the parser snapshots
//     the cursor, trials the list, and rewinds (dropping any diagnostics) if
//     it is not immediately followed by "(". Only then does "<" mean a
//     generic call; otherwise it is the less-than operator.
//   - D-SPLIT: the lexer produces no ">>" and lexes "Page<User>=" as
//     IDENT LT IDENT GT_EQ, so a type-argument list that must close on ">"
//     but sees ">=" accepts the ">" and re-injects a synthetic "=" token.
//   - D-OQ1: a stray ";" at top level or in a block is an empty statement,
//     no node and no diagnostic.
//   - D-TXN: "db.transaction((tx) => { ... });" is a TransactionStatement;
//     "=>" anywhere else is E-PARSE and no ArrowFunction node exists.
package parser

import (
	"strconv"
	"strings"

	"tlang/ast"
	"tlang/diag"
	"tlang/lexer"
	"tlang/token"
)

// Operator precedence levels (lowest to highest), matching the DESIGN.md §2.2
// Pratt table. ASSIGN is NOT in this table: assignment is statement-level only
// (D-ASSIGN), so the expression parser never climbs into it.
const (
	lowest      = iota
	ternaryPrec // c ? a : b
	nullishPrec // ??
	orPrec      // ||
	andPrec     // &&
	equalsPrec  // == !=
	compPrec    // < > <= >=
	sumPrec     // + -
	productPrec // * /%
	prefixPrec  // !x -x
	// callPrec and memberPrec share one binding level: calls, indexing,
	// member access, and postfix "!" are all parsed in the same left-assoc
	// loop, so callPrec is kept only to hold its iota slot below prefixPrec.
	callPrec   // f(x) f<T>(x)
	memberPrec // a.b a[i] a!
)

var _ = callPrec // intentionally collapsed into memberPrec; see note above

// infixPrec maps an infix/postfix operator to its binding level, or lowest
// when the token does not continue an expression.
func infixPrec(t token.TokenType) int {
	switch t {
	case token.QUESTION:
		return ternaryPrec
	case token.NULLISH:
		return nullishPrec
	case token.OR:
		return orPrec
	case token.AND:
		return andPrec
	case token.EQ, token.NOT_EQ:
		return equalsPrec
	case token.LT, token.GT, token.LT_EQ, token.GT_EQ:
		return compPrec
	case token.PLUS, token.MINUS:
		return sumPrec
	case token.ASTERISK, token.SLASH, token.MOD:
		return productPrec
	case token.LPAREN, token.DOT, token.LBRACKET, token.BANG:
		return memberPrec
	case token.AMPERSAND:
		// "&" is reserved (D-AMP). It is given a precedence so the Pratt loop
		// reaches a handler that reports it once and consumes it, instead of
		// leaving it to a generic "expected ;" (which would double-report an
		// adjacent ILLEGAL token).
		return equalsPrec
	}
	return lowest
}

// parser holds the token cursor and the diagnostic list. The token slice
// always ends in a single EOF (lexer contract); the cursor never moves past
// it, so lookahead at end of input is safe.
type parser struct {
	file  string
	toks  []token.Token
	pos   int
	diags *diag.List
}

// Parse parses toks (which must end in an EOF token, as the lexer guarantees)
// and returns the program and the parser's diagnostics. The returned list
// contains only E-PARSE items; lexer E-LEX diagnostics are separate (see
// ParseSource).
func Parse(file string, toks []token.Token) (*ast.Program, *diag.List) {
	p := &parser{file: file, toks: toks, diags: diag.NewList(file)}
	return p.parseProgram(), p.diags
}

// ParseSource is a convenience wrapper: it lexes src and parses the result,
// returning a combined diagnostic list (lexer E-LEX items first, then the
// parser's E-PARSE items).
func ParseSource(file string, src []byte) (*ast.Program, *diag.List) {
	toks, lexDiags := lexer.Lex(file, src)
	prog, parseDiags := Parse(file, toks)
	combined := diag.NewList(file)
	combined.Items = append(combined.Items, lexDiags.Items...)
	combined.Items = append(combined.Items, parseDiags.Items...)
	return prog, combined
}

// ---------------------------------------------------------------------------
// Token cursor (D-STRUCT)

// cur returns the current token, clamped to the final EOF.
func (p *parser) cur() token.Token { return p.at(p.pos) }

// peek returns the token n positions ahead, clamped to the final EOF.
func (p *parser) peek(n int) token.Token { return p.at(p.pos + n) }

// at returns the token at index i, clamped to [0, last].
func (p *parser) at(i int) token.Token {
	if i < 0 {
		i = 0
	}
	if i >= len(p.toks) {
		// The slice always ends in EOF; return it for any over-read.
		if len(p.toks) == 0 {
			return token.Token{Type: token.EOF}
		}
		return p.toks[len(p.toks)-1]
	}
	return p.toks[i]
}

func (p *parser) curIs(t token.TokenType) bool         { return p.cur().Type == t }
func (p *parser) peekIs(n int, t token.TokenType) bool { return p.peek(n).Type == t }

// advance moves past the current token (never past EOF) and returns the token
// it moved over.
func (p *parser) advance() token.Token {
	tok := p.cur()
	if p.cur().Type != token.EOF {
		p.pos++
	}
	return tok
}

// expect consumes the current token when it has type t and returns it with
// true; otherwise it reports an E-PARSE "expected X, got Y" at the current
// position and returns the current token with false (the caller decides how
// to recover).
func (p *parser) expect(t token.TokenType) (token.Token, bool) {
	if p.curIs(t) {
		return p.advance(), true
	}
	// An ILLEGAL token already carries an E-LEX diagnostic from the lexer
	// (D-ILLEGAL): do NOT add an E-PARSE at the same spot. Consume it so the
	// parser keeps moving, and report failure without a duplicate message.
	if p.curIs(token.ILLEGAL) {
		bad := p.advance()
		return bad, false
	}
	p.errorf(p.cur().Pos, "expected %q, got %s", string(t), describe(p.cur()))
	return p.cur(), false
}

// errorf records one E-PARSE diagnostic.
func (p *parser) errorf(pos token.Position, format string, args ...any) {
	p.diags.Errorf(pos, diag.CodeParse, format, args...)
}

// describe renders a token for a "got X" message. ILLEGAL and EOF get a word;
// everything else shows its lexeme.
func describe(tok token.Token) string {
	switch tok.Type {
	case token.EOF:
		return "end of input"
	case token.ILLEGAL:
		return "invalid token"
	case token.IDENT, token.INT, token.FLOAT:
		return strconv.Quote(tok.Literal)
	case token.STRING:
		return "string literal"
	}
	return strconv.Quote(string(tok.Type))
}

// ---------------------------------------------------------------------------
// Snapshot / rewind for speculative parsing (D-SNAPSHOT)

// snapshot records the cursor and the current diagnostic count so a failed
// speculation can be rolled back with no side effects.
func (p *parser) snapshot() (int, int) { return p.pos, len(p.diags.Items) }

// rewind restores the cursor and truncates any diagnostics recorded since the
// snapshot, leaving the parser exactly as it was.
func (p *parser) rewind(pos, diagLen int) {
	p.pos = pos
	p.diags.Items = p.diags.Items[:diagLen]
}

// ---------------------------------------------------------------------------
// Recovery (D-RECOVER)

// isTopKeyword reports whether t starts a top-level declaration, a resync
// anchor for synchronize.
func isTopKeyword(t token.TokenType) bool {
	switch t {
	case token.FN, token.INTERFACE, token.TYPE, token.LET, token.CONST, token.AT:
		return true
	}
	return false
}

// synchronize skips tokens until it reaches a safe boundary and returns the
// position just past the last skipped byte. It consumes a terminating ";" but
// stops before a top-level keyword or a "}" (the brace owner closes those) and
// before EOF. It always makes progress (consumes at least one token unless
// already at a stop boundary at the start), so the parser can never spin.
func (p *parser) synchronize() token.Position {
	end := p.cur().Pos
	for !p.curIs(token.EOF) {
		if p.curIs(token.SEMICOLON) {
			end = p.advance().End
			return end
		}
		if isTopKeyword(p.cur().Type) || p.curIs(token.RBRACE) {
			return end
		}
		end = p.advance().End
	}
	return end
}

// ---------------------------------------------------------------------------
// Program and top-level declarations (D-TOP)

func (p *parser) parseProgram() *ast.Program {
	prog := &ast.Program{File: p.file}

	// Import phase (FR-19, A5): imports and re-exports must precede every
	// top-level declaration. An "export" counts as an import-phase statement
	// only when it is immediately followed by "{" (a re-export); every other
	// "export" is a decl prefix handled in the decl phase below.
	for p.isImportPhaseStart() {
		before := p.pos
		var decl ast.Statement
		if p.curIs(token.IMPORT) {
			decl = p.parseImport()
		} else {
			decl = p.parseReExport()
		}
		if decl != nil {
			prog.Statements = append(prog.Statements, decl)
		}
		if p.pos == before {
			p.advance()
		}
	}

	for !p.curIs(token.EOF) {
		// D-OQ1: a stray ";" at top level is an empty statement.
		if p.curIs(token.SEMICOLON) {
			p.advance()
			continue
		}
		// A stray "}" at top level (e.g. left over after recovering from a
		// non-declaration statement) is skipped so recovery makes progress
		// without re-reporting the same spot.
		if p.curIs(token.RBRACE) {
			p.advance()
			continue
		}
		// An import (or a re-export "export { ... } from") after a top-level
		// declaration has started is a syntax error: imports come first.
		if p.isImportPhaseStart() {
			p.errorf(p.cur().Pos, "imports must come first")
			before := p.pos
			if p.curIs(token.IMPORT) {
				p.parseImport()
			} else {
				p.parseReExport()
			}
			if p.pos == before {
				p.advance()
			}
			continue
		}
		before := p.pos
		if decl := p.parseTopDecl(); decl != nil {
			prog.Statements = append(prog.Statements, decl)
		}
		// Loop-progress guard: never spin on an unconsumed token.
		if p.pos == before {
			p.advance()
		}
	}
	prog.EOF = p.cur().Pos
	return prog
}

// isImportPhaseStart reports whether the cursor begins an import-phase
// statement: an "import" declaration, or an "export" immediately followed by
// "{" (a re-export). A bare "export" that prefixes a declaration is NOT an
// import-phase start.
func (p *parser) isImportPhaseStart() bool {
	if p.curIs(token.IMPORT) {
		return true
	}
	return p.curIs(token.EXPORT) && p.peekIs(1, token.LBRACE)
}

// parseTopDecl parses one top-level declaration. Only declarations and
// BadStatement reach Program.Statements (ast.go invariant). A leading
// "export" (optionally "export default") marks the declaration as exported
// (DESIGN-modules.md §2.2).
func (p *parser) parseTopDecl() ast.Statement {
	if p.curIs(token.EXPORT) {
		return p.parseExportedDecl()
	}
	switch p.cur().Type {
	case token.AT, token.FN:
		return p.parseFunction()
	case token.INTERFACE:
		return p.parseInterface()
	case token.TYPE:
		return p.parseTypeAlias()
	case token.LET, token.CONST:
		return p.parseLet()
	default:
		from := p.cur().Pos
		p.errorf(from, "expected a top-level declaration, got %s", describe(p.cur()))
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
}

// parseExportedDecl parses "export default? <named declaration>". The current
// token is "export". A re-export "export { ... } from" is handled in the
// import phase and never reaches here. An "export default" that is not a
// named declaration (an expression, or an anonymous form) is E-PARSE.
func (p *parser) parseExportedDecl() ast.Statement {
	from := p.cur().Pos
	exportTok := p.advance() // "export"
	isDefault := false
	if p.curIs(token.DEFAULT) {
		p.advance() // "default"
		isDefault = true
	}

	var decl ast.Statement
	switch p.cur().Type {
	case token.AT, token.FN:
		decl = p.parseFunction()
	case token.INTERFACE:
		decl = p.parseInterface()
	case token.TYPE:
		decl = p.parseTypeAlias()
	case token.LET, token.CONST:
		decl = p.parseLet()
	default:
		if isDefault {
			p.errorf(p.cur().Pos, "default export must be a named declaration")
		} else {
			p.errorf(p.cur().Pos, "expected a top-level declaration, got %s", describe(p.cur()))
		}
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}

	switch d := decl.(type) {
	case *ast.FunctionStatement:
		d.Exported = true
		d.ExportPos = exportTok.Pos
		d.IsDefault = isDefault
	case *ast.InterfaceStatement:
		d.Exported = true
		d.ExportPos = exportTok.Pos
		d.IsDefault = isDefault
	case *ast.TypeAliasStatement:
		d.Exported = true
		d.ExportPos = exportTok.Pos
		d.IsDefault = isDefault
	case *ast.LetStatement:
		d.Exported = true
		d.ExportPos = exportTok.Pos
		d.IsDefault = isDefault
	}
	return decl
}

// parseFunction parses "decorator* fn (recv)? name typeParams? (params) (: type)? block".
func (p *parser) parseFunction() ast.Statement {
	from := p.cur().Pos
	var decorators []*ast.Decorator
	for p.curIs(token.AT) {
		d := p.parseDecorator()
		if d == nil {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		decorators = append(decorators, d)
	}

	fnTok, ok := p.expect(token.FN)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}

	fn := &ast.FunctionStatement{Decorators: decorators, FnPos: fnTok.Pos}

	// Optional receiver "(name: type)" directly after fn.
	if p.curIs(token.LPAREN) {
		p.advance()
		recv := &ast.Parameter{}
		name, ok := p.expect(token.IDENT)
		if !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		recv.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
		if _, ok := p.expect(token.COLON); !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		recv.Type = p.parseType()
		if _, ok := p.expect(token.RPAREN); !ok {
			to := p.synchronize()
			return &ast.BadStatement{From: from, To: to}
		}
		fn.Receiver = recv
	}

	name, ok := p.expect(token.IDENT)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	fn.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}

	if p.curIs(token.LT) {
		fn.TypeParams = p.parseTypeParams()
	}

	if _, ok := p.expect(token.LPAREN); !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	fn.Parameters = p.parseParams()
	if _, ok := p.expect(token.RPAREN); !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}

	if p.curIs(token.COLON) {
		p.advance()
		fn.ReturnType = p.parseType()
	}

	if !p.curIs(token.LBRACE) {
		p.errorf(p.cur().Pos, "expected function body, got %s", describe(p.cur()))
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	fn.Body = p.parseBlock()
	return fn
}

// parseDecorator parses "@ Name ( IDENT (, IDENT)* )?". It returns nil on a
// malformed header so the caller can recover.
func (p *parser) parseDecorator() *ast.Decorator {
	at := p.advance() // "@"
	d := &ast.Decorator{AtPos: at.Pos}
	name, ok := p.expect(token.IDENT)
	if !ok {
		return nil
	}
	d.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	lp, ok := p.expect(token.LPAREN)
	if !ok {
		return nil
	}
	d.Lparen = lp.Pos
	for !p.curIs(token.RPAREN) && !p.curIs(token.EOF) {
		arg, ok := p.expect(token.IDENT)
		if !ok {
			return nil
		}
		d.Args = append(d.Args, &ast.Identifier{NamePos: arg.Pos, Name: arg.Literal})
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rp, ok := p.expect(token.RPAREN)
	if !ok {
		return nil
	}
	d.Rparen = rp.Pos
	return d
}

// parseTypeParams parses "< IDENT (, IDENT)* >". It assumes the current token
// is "<".
func (p *parser) parseTypeParams() []*ast.Identifier {
	p.advance() // "<"
	var params []*ast.Identifier
	for !p.curIs(token.GT) && !p.curIs(token.EOF) {
		name, ok := p.expect(token.IDENT)
		if !ok {
			break
		}
		params = append(params, &ast.Identifier{NamePos: name.Pos, Name: name.Literal})
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	p.expect(token.GT)
	return params
}

// parseParams parses "(IDENT : type (, IDENT : type)*)?" between the already
// consumed "(" and the ")" the caller expects.
func (p *parser) parseParams() []*ast.Parameter {
	var params []*ast.Parameter
	for !p.curIs(token.RPAREN) && !p.curIs(token.EOF) {
		name, ok := p.expect(token.IDENT)
		if !ok {
			break
		}
		param := &ast.Parameter{Name: &ast.Identifier{NamePos: name.Pos, Name: name.Literal}}
		if _, ok := p.expect(token.COLON); !ok {
			break
		}
		param.Type = p.parseType()
		params = append(params, param)
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	return params
}

// parseInterface parses "interface IDENT typeParams? { (field (;|,)?)* }".
func (p *parser) parseInterface() ast.Statement {
	from := p.cur().Pos
	kw := p.advance() // "interface"
	iface := &ast.InterfaceStatement{InterfacePos: kw.Pos}
	name, ok := p.expect(token.IDENT)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	iface.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	if p.curIs(token.LT) {
		iface.TypeParams = p.parseTypeParams()
	}
	lb, ok := p.expect(token.LBRACE)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	iface.Lbrace = lb.Pos
	iface.Fields = p.parseFields(token.RBRACE)
	rb, _ := p.expect(token.RBRACE)
	iface.Rbrace = rb.Pos
	return iface
}

// parseFields parses "(field (;|,)?)*" up to the end token. "field" is
// "IDENT ?? : type". Both ";" and "," are accepted as separators and a
// trailing one is optional.
func (p *parser) parseFields(end token.TokenType) []*ast.FieldDefinition {
	var fields []*ast.FieldDefinition
	for !p.curIs(end) && !p.curIs(token.EOF) {
		name, ok := p.expect(token.IDENT)
		if !ok {
			// Skip to the next separator or end to keep making progress.
			if !p.curIs(token.SEMICOLON) && !p.curIs(token.COMMA) && !p.curIs(end) {
				p.advance()
			}
			if p.curIs(token.SEMICOLON) || p.curIs(token.COMMA) {
				p.advance()
			}
			continue
		}
		field := &ast.FieldDefinition{Name: &ast.Identifier{NamePos: name.Pos, Name: name.Literal}}
		if p.curIs(token.QUESTION) {
			q := p.advance()
			field.Optional = true
			field.QuestionPos = q.Pos
		}
		if _, ok := p.expect(token.COLON); !ok {
			fields = append(fields, field)
			break
		}
		field.Type = p.parseType()
		fields = append(fields, field)
		if p.curIs(token.SEMICOLON) || p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	return fields
}

// parseTypeAlias parses "type IDENT typeParams? = (type | { field* }) ;".
func (p *parser) parseTypeAlias() ast.Statement {
	from := p.cur().Pos
	kw := p.advance() // "type"
	alias := &ast.TypeAliasStatement{TypePos: kw.Pos}
	name, ok := p.expect(token.IDENT)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	alias.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	if p.curIs(token.LT) {
		alias.TypeParams = p.parseTypeParams()
	}
	assign, ok := p.expect(token.ASSIGN)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	alias.AssignPos = assign.Pos
	// The ObjectType body is produced ONLY here (ast.go invariant).
	if p.curIs(token.LBRACE) {
		alias.Value = p.parseObjectType()
	} else {
		alias.Value = p.parseType()
	}
	semi, _ := p.expect(token.SEMICOLON)
	alias.Semicolon = semi.Pos
	return alias
}

// parseObjectType parses "{ (field (;|,)?)* }" as an ObjectType.
func (p *parser) parseObjectType() ast.TypeExpr {
	lb := p.advance() // "{"
	obj := &ast.ObjectType{Lbrace: lb.Pos}
	obj.Fields = p.parseFields(token.RBRACE)
	rb, _ := p.expect(token.RBRACE)
	obj.Rbrace = rb.Pos
	return obj
}

// parseLet parses "(let|const) IDENT (: type)? (= expr)? ;". The initializer
// uses the plain expression parser (not the assignment path), so there is no
// chained assignment here.
func (p *parser) parseLet() ast.Statement {
	return p.parseLetStmt(true)
}

// parseLetStmt parses a let/const declaration. When wantSemi is false (the
// for-loop init position) the trailing ";" is left for the for-header parser.
func (p *parser) parseLetStmt(wantSemi bool) ast.Statement {
	from := p.cur().Pos
	kw := p.advance() // "let" or "const"
	let := &ast.LetStatement{LetPos: kw.Pos, IsConst: kw.Type == token.CONST}
	name, ok := p.expect(token.IDENT)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	let.Name = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	if p.curIs(token.COLON) {
		p.advance()
		let.Type = p.parseType()
	}
	if p.curIs(token.ASSIGN) {
		p.advance()
		let.Value = p.parseExpression(lowest)
	}
	if wantSemi {
		semi, _ := p.expect(token.SEMICOLON)
		let.Semicolon = semi.Pos
	}
	return let
}

// ---------------------------------------------------------------------------
// Statements (D-STMT)

// parseBlock parses "{ statement* }". It assumes the current token is "{".
func (p *parser) parseBlock() *ast.BlockStatement {
	lb := p.advance() // "{"
	block := &ast.BlockStatement{Lbrace: lb.Pos}
	for !p.curIs(token.RBRACE) && !p.curIs(token.EOF) {
		if p.curIs(token.SEMICOLON) { // D-OQ1: empty statement.
			p.advance()
			continue
		}
		before := p.pos
		if st := p.parseStatement(); st != nil {
			block.Statements = append(block.Statements, st)
		}
		if p.pos == before {
			p.advance()
		}
	}
	rb, _ := p.expect(token.RBRACE)
	block.Rbrace = rb.Pos
	return block
}

// parseStatement dispatches on the current token to a statement production.
func (p *parser) parseStatement() ast.Statement {
	switch p.cur().Type {
	case token.LBRACE:
		return p.parseBlock()
	case token.LET, token.CONST:
		return p.parseLet()
	case token.IF:
		return p.parseIf()
	case token.WHILE:
		return p.parseWhile()
	case token.FOR:
		return p.parseFor()
	case token.RETURN:
		return p.parseReturn()
	case token.BREAK:
		return p.parseBreak()
	case token.CONTINUE:
		return p.parseContinue()
	case token.THROW:
		return p.parseThrow()
	case token.TRY:
		return p.parseTry()
	case token.FN, token.INTERFACE, token.TYPE:
		// Declarations are top-level only (ast.go invariant).
		from := p.cur().Pos
		p.errorf(from, "declaration not allowed here")
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	default:
		if p.isTransactionStart() {
			return p.parseTransaction()
		}
		return p.parseExpressionStatement()
	}
}

func (p *parser) parseIf() ast.Statement {
	kw := p.advance() // "if"
	stmt := &ast.IfStatement{IfPos: kw.Pos}
	if _, ok := p.expect(token.LPAREN); !ok {
		from := kw.Pos
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	stmt.Condition = p.parseExpression(lowest)
	p.expect(token.RPAREN)
	stmt.Consequence = p.parseStatement()
	if p.curIs(token.ELSE) {
		els := p.advance()
		stmt.ElsePos = els.Pos
		stmt.Alternative = p.parseStatement()
	}
	return stmt
}

func (p *parser) parseWhile() ast.Statement {
	kw := p.advance() // "while"
	stmt := &ast.WhileStatement{WhilePos: kw.Pos}
	if _, ok := p.expect(token.LPAREN); !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: kw.Pos, To: to}
	}
	stmt.Condition = p.parseExpression(lowest)
	p.expect(token.RPAREN)
	stmt.Body = p.parseStatement()
	return stmt
}

// parseFor parses both the C-style "for (init; cond; post)" and the
// "for (const x of expr)" forms (D-FOR).
func (p *parser) parseFor() ast.Statement {
	kw := p.advance() // "for"
	forPos := kw.Pos
	if _, ok := p.expect(token.LPAREN); !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: forPos, To: to}
	}

	// for-of: (let|const) IDENT of expr
	if (p.curIs(token.LET) || p.curIs(token.CONST)) && p.peekIs(1, token.IDENT) && p.peekIs(2, token.OF) {
		kw := p.advance() // let/const
		name := p.advance()
		p.advance() // "of"
		stmt := &ast.ForOfStatement{
			ForPos:  forPos,
			IsConst: kw.Type == token.CONST,
			Var:     &ast.Identifier{NamePos: name.Pos, Name: name.Literal},
		}
		stmt.Iterable = p.parseExpression(lowest)
		p.expect(token.RPAREN)
		stmt.Body = p.parseStatement()
		return stmt
	}

	// C-style for.
	stmt := &ast.ForStatement{ForPos: forPos}
	switch {
	case p.curIs(token.SEMICOLON):
		p.advance() // Init stays nil; consume the first ";".
	case p.curIs(token.LET) || p.curIs(token.CONST):
		init := p.parseLetStmt(false)
		p.expect(token.SEMICOLON)
		stmt.Init = init
	default:
		expr := p.parseSimpleExpr()
		p.expect(token.SEMICOLON)
		stmt.Init = &ast.ExpressionStatement{Expression: expr}
	}
	if !p.curIs(token.SEMICOLON) {
		stmt.Condition = p.parseExpression(lowest)
	}
	p.expect(token.SEMICOLON)
	if !p.curIs(token.RPAREN) {
		stmt.Post = p.parseSimpleExpr()
	}
	p.expect(token.RPAREN)
	stmt.Body = p.parseStatement()
	return stmt
}

func (p *parser) parseReturn() ast.Statement {
	kw := p.advance() // "return"
	stmt := &ast.ReturnStatement{ReturnPos: kw.Pos}
	if !p.curIs(token.SEMICOLON) {
		stmt.Value = p.parseExpression(lowest)
	}
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

func (p *parser) parseBreak() ast.Statement {
	kw := p.advance()
	stmt := &ast.BreakStatement{BreakPos: kw.Pos}
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

func (p *parser) parseContinue() ast.Statement {
	kw := p.advance()
	stmt := &ast.ContinueStatement{ContinuePos: kw.Pos}
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

func (p *parser) parseThrow() ast.Statement {
	kw := p.advance() // "throw"
	stmt := &ast.ThrowStatement{ThrowPos: kw.Pos}
	stmt.Value = p.parseExpression(lowest)
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

// parseTry parses "try block catch ('(' IDENT ')')? block".
func (p *parser) parseTry() ast.Statement {
	kw := p.advance() // "try"
	stmt := &ast.TryCatchStatement{TryPos: kw.Pos}
	if !p.curIs(token.LBRACE) {
		p.errorf(p.cur().Pos, "expected %q, got %s", "{", describe(p.cur()))
		to := p.synchronize()
		return &ast.BadStatement{From: kw.Pos, To: to}
	}
	stmt.Body = p.parseBlock()
	catch, ok := p.expect(token.CATCH)
	if !ok {
		to := p.synchronize()
		return &ast.BadStatement{From: kw.Pos, To: to}
	}
	stmt.CatchPos = catch.Pos
	if p.curIs(token.LPAREN) {
		p.advance()
		name, ok := p.expect(token.IDENT)
		if ok {
			stmt.CatchParam = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
		}
		p.expect(token.RPAREN)
	}
	if !p.curIs(token.LBRACE) {
		p.errorf(p.cur().Pos, "expected %q, got %s", "{", describe(p.cur()))
		to := p.synchronize()
		return &ast.BadStatement{From: kw.Pos, To: to}
	}
	stmt.CatchBody = p.parseBlock()
	return stmt
}

// isTransactionStart reports whether the statement starts with
// "IDENT . transaction (" followed by an arrow-function argument
// ("(" ... ")" "=>" or IDENT "=>"). Only then is it a TransactionStatement;
// otherwise "db.transaction(x)" is an ordinary call (D-TXN).
func (p *parser) isTransactionStart() bool {
	if !p.curIs(token.IDENT) || !p.peekIs(1, token.DOT) || !p.peekIs(2, token.IDENT) {
		return false
	}
	if p.peek(2).Literal != "transaction" || !p.peekIs(3, token.LPAREN) {
		return false
	}
	// Argument shape: "(" IDENT ... "=>" or IDENT "=>".
	if p.peekIs(4, token.LPAREN) {
		// (tx) => or (tx: T) =>
		if !p.peekIs(5, token.IDENT) {
			return false
		}
		if p.peekIs(6, token.RPAREN) {
			return p.peekIs(7, token.ARROW)
		}
		if p.peekIs(6, token.COLON) {
			// Scan for the matching ")" then "=>".
			return p.arrowAfterTypedParam()
		}
		return false
	}
	// bare IDENT =>
	return p.peekIs(4, token.IDENT) && p.peekIs(5, token.ARROW)
}

// arrowAfterTypedParam scans a "(tx: T)" parameter starting at peek(4)=="("
// and reports whether it is closed by ")" immediately followed by "=>". It
// does not move the cursor.
func (p *parser) arrowAfterTypedParam() bool {
	// peek offsets: 4="(" 5=IDENT 6=":" then a type, then ")" "=>".
	depth := 0
	for i := 4; ; i++ {
		tok := p.peek(i)
		switch tok.Type {
		case token.EOF:
			return false
		case token.LPAREN:
			depth++
		case token.RPAREN:
			depth--
			if depth == 0 {
				return p.peekIs(i+1, token.ARROW)
			}
		}
	}
}

// parseTransaction parses "Receiver . transaction ( param (: type)? => block ) ;".
// It is only called when isTransactionStart confirmed the shape.
func (p *parser) parseTransaction() ast.Statement {
	recvTok := p.advance() // receiver IDENT
	stmt := &ast.TransactionStatement{
		Receiver: &ast.Identifier{NamePos: recvTok.Pos, Name: recvTok.Literal},
	}
	p.advance()           // "."
	method := p.advance() // "transaction"
	stmt.Method = &ast.Identifier{NamePos: method.Pos, Name: method.Literal}
	lp := p.advance() // "("
	stmt.Lparen = lp.Pos

	// Parameter: either "(" IDENT (":" type)? ")" or bare IDENT.
	if p.curIs(token.LPAREN) {
		p.advance() // inner "("
		name := p.advance()
		stmt.Param = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
		if p.curIs(token.COLON) {
			p.advance()
			stmt.ParamType = p.parseType()
		}
		p.expect(token.RPAREN)
	} else {
		name := p.advance()
		stmt.Param = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	}
	p.expect(token.ARROW)
	if p.curIs(token.LBRACE) {
		stmt.Body = p.parseBlock()
	} else {
		p.errorf(p.cur().Pos, "expected transaction body, got %s", describe(p.cur()))
		stmt.Body = &ast.BlockStatement{Lbrace: p.cur().Pos, Rbrace: p.cur().Pos}
	}
	rp, _ := p.expect(token.RPAREN)
	stmt.Rparen = rp.Pos
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

// parseExpressionStatement parses an expression statement, including an
// assignment or a postfix ++/-- (D-EXPRSTMT).
func (p *parser) parseExpressionStatement() ast.Statement {
	from := p.cur().Pos
	expr := p.parseSimpleExpr()
	if _, isBad := expr.(*ast.BadExpression); isBad {
		to := p.synchronize()
		return &ast.BadStatement{From: from, To: to}
	}
	stmt := &ast.ExpressionStatement{Expression: expr}
	semi, _ := p.expect(token.SEMICOLON)
	stmt.Semicolon = semi.Pos
	return stmt
}

// parseSimpleExpr parses an expression that may be a top-level assignment or
// postfix ++/-- (the only places assignment is allowed, D-ASSIGN). The RHS of
// an assignment is a plain non-assignment expression, so "a = b = c" leaves a
// stray "=" that the caller's expect(";") reports.
func (p *parser) parseSimpleExpr() ast.Expression {
	left := p.parseExpression(lowest)
	switch p.cur().Type {
	case token.ASSIGN, token.PLUS_ASSIGN, token.MINUS_ASSIGN,
		token.ASTERISK_ASSIGN, token.SLASH_ASSIGN, token.MOD_ASSIGN:
		op := p.advance()
		value := p.parseExpression(lowest)
		return &ast.AssignmentExpression{
			Target: left, OpPos: op.Pos, Operator: op.Type, Value: value,
		}
	case token.INCREMENT, token.DECREMENT:
		op := p.advance()
		// Postfix form: Value stays nil (ast.go contract).
		return &ast.AssignmentExpression{Target: left, OpPos: op.Pos, Operator: op.Type}
	}
	return left
}

// ---------------------------------------------------------------------------
// Pratt expression parser

// parseExpression parses an expression binding tighter than prec. It never
// climbs into the ASSIGN level (D-ASSIGN).
func (p *parser) parseExpression(prec int) ast.Expression {
	left := p.parsePrefix()
	for prec < infixPrec(p.cur().Type) {
		left = p.parseInfix(left)
	}
	return left
}

// parsePrefix handles the start of an expression (a primary or a prefix
// operator).
func (p *parser) parsePrefix() ast.Expression {
	tok := p.cur()
	switch tok.Type {
	case token.INT:
		return p.parseIntegerLiteral()
	case token.FLOAT:
		return p.parseFloatLiteral()
	case token.STRING:
		p.advance()
		return &ast.StringLiteral{ValuePos: tok.Pos, Value: tok.Literal, ValueEnd: tok.End}
	case token.TRUE, token.FALSE:
		p.advance()
		return &ast.BooleanLiteral{ValuePos: tok.Pos, Value: tok.Type == token.TRUE}
	case token.NULL:
		p.advance()
		return &ast.NullLiteral{NullPos: tok.Pos}
	case token.IDENT:
		p.advance()
		return &ast.Identifier{NamePos: tok.Pos, Name: tok.Literal}
	case token.BANG, token.MINUS:
		p.advance()
		return &ast.PrefixExpression{OpPos: tok.Pos, Operator: tok.Type, Right: p.parseExpression(prefixPrec)}
	case token.LPAREN:
		// Grouped expression: no node, return the inner directly (ast.go).
		p.advance()
		inner := p.parseExpression(lowest)
		p.expect(token.RPAREN)
		return inner
	case token.LBRACKET:
		return p.parseArrayLiteral()
	case token.LBRACE:
		return p.parseObjectLiteral()
	case token.NEW:
		return p.parseNew()
	default:
		// No prefix handler: report, consume the offending token (so the
		// parser makes progress), and return a BadExpression spanning it.
		// An ILLEGAL token already has an E-LEX diagnostic, so do NOT
		// re-report it (D-ILLEGAL).
		from := tok.Pos
		to := tok.End
		switch tok.Type {
		case token.ILLEGAL:
			// Lexer already recorded E-LEX; surface via Bad* without a dup.
		case token.AMPERSAND:
			// "&" is reserved (D-AMP).
			p.errorf(from, "%q is reserved", "&")
		case token.ARROW:
			// "=>" is legal only in the transaction statement (D-TXN/D-AMP).
			p.errorf(from, "arrow functions are only allowed as the argument of db.transaction")
		default:
			p.errorf(from, "expected expression, got %s", describe(tok))
		}
		if tok.Type != token.EOF {
			p.advance()
		}
		return &ast.BadExpression{From: from, To: to}
	}
}

// parseInfix dispatches the infix/postfix handler for the current token.
func (p *parser) parseInfix(left ast.Expression) ast.Expression {
	tok := p.cur()
	switch tok.Type {
	case token.QUESTION:
		return p.parseTernary(left)
	case token.NULLISH, token.OR, token.AND, token.EQ, token.NOT_EQ,
		token.LT, token.GT, token.LT_EQ, token.GT_EQ,
		token.PLUS, token.MINUS, token.ASTERISK, token.SLASH, token.MOD:
		return p.parseBinary(left)
	case token.LPAREN:
		return p.parseCall(left, nil, token.Position{}, token.Position{})
	case token.DOT:
		return p.parseMember(left)
	case token.LBRACKET:
		return p.parseIndex(left)
	case token.BANG:
		bang := p.advance()
		return &ast.NonNullExpression{Left: left, BangPos: bang.Pos}
	case token.AMPERSAND:
		// "&" is reserved (D-AMP): report once, consume it, keep the left
		// operand so the surrounding expression stays walkable.
		p.errorf(tok.Pos, "%q is reserved", "&")
		p.advance()
		return left
	}
	return left
}

// parseBinary builds a left-associative InfixExpression. The "<" token is
// special: at the call/member stage it may begin a generic call, so parseInfix
// routes it here only after the generic-call lookahead failed (see parseCall /
// parseMember entry via parsePostfixChain). Here "<" is a plain less-than.
func (p *parser) parseBinary(left ast.Expression) ast.Expression {
	// Generic-call speculation: a "<" directly after an identifier or member
	// expression may be a type-argument list (D-GENCALL).
	if p.curIs(token.LT) && isGenericCallable(left) {
		if call, ok := p.tryGenericCall(left); ok {
			return call
		}
	}
	op := p.advance()
	prec := infixPrec(op.Type)
	right := p.parseExpression(prec)
	return &ast.InfixExpression{Left: left, OpPos: op.Pos, Operator: op.Type, Right: right}
}

// isGenericCallable reports whether a generic call "<...>(...)" may follow x.
func isGenericCallable(x ast.Expression) bool {
	switch x.(type) {
	case *ast.Identifier, *ast.MemberExpression:
		return true
	}
	return false
}

func (p *parser) parseTernary(left ast.Expression) ast.Expression {
	q := p.advance() // "?"
	consequence := p.parseExpression(lowest)
	colon, _ := p.expect(token.COLON)
	// Right associativity: the alternative binds at ternaryPrec - 1 so a
	// trailing "? :" nests on the right.
	alternative := p.parseExpression(ternaryPrec - 1)
	return &ast.TernaryExpression{
		Condition:   left,
		QuestionPos: q.Pos,
		Consequence: consequence,
		ColonPos:    colon.Pos,
		Alternative: alternative,
	}
}

// parseCall parses "(args)" after the callee, optionally carrying type
// arguments already parsed by the generic-call path.
func (p *parser) parseCall(fn ast.Expression, typeArgs []ast.TypeExpr, langle, rangle token.Position) ast.Expression {
	lp := p.advance() // "("
	call := &ast.CallExpression{
		Function: fn, TypeArgs: typeArgs, Langle: langle, Rangle: rangle, Lparen: lp.Pos,
	}
	for !p.curIs(token.RPAREN) && !p.curIs(token.EOF) {
		call.Arguments = append(call.Arguments, p.parseExpression(lowest))
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rp, _ := p.expect(token.RPAREN)
	call.Rparen = rp.Pos
	return call
}

func (p *parser) parseMember(left ast.Expression) ast.Expression {
	dot := p.advance() // "."
	name, _ := p.expect(token.IDENT)
	member := &ast.MemberExpression{Object: left, DotPos: dot.Pos}
	if name.Type == token.IDENT {
		member.Property = &ast.Identifier{NamePos: name.Pos, Name: name.Literal}
	} else {
		member.Property = &ast.Identifier{NamePos: dot.Pos.Advance(1), Name: ""}
	}
	return member
}

func (p *parser) parseIndex(left ast.Expression) ast.Expression {
	lb := p.advance() // "["
	index := p.parseExpression(lowest)
	rb, _ := p.expect(token.RBRACKET)
	return &ast.IndexExpression{Left: left, Lbracket: lb.Pos, Index: index, Rbracket: rb.Pos}
}

func (p *parser) parseIntegerLiteral() ast.Expression {
	tok := p.advance()
	lit := &ast.IntegerLiteral{ValuePos: tok.Pos, Raw: tok.Literal}
	digits := strings.ReplaceAll(tok.Literal, "_", "")
	base := 10
	if len(digits) > 2 && digits[0] == '0' && (digits[1] == 'x' || digits[1] == 'X') {
		base = 16
		digits = digits[2:]
	}
	v, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		// Range error: the checker reports E-CONST, the parser only flags
		// overflow (ast.go contract).
		lit.Overflow = true
	}
	lit.Value = v
	return lit
}

func (p *parser) parseFloatLiteral() ast.Expression {
	tok := p.advance()
	lit := &ast.FloatLiteral{ValuePos: tok.Pos, Raw: tok.Literal}
	v, _ := strconv.ParseFloat(strings.ReplaceAll(tok.Literal, "_", ""), 64)
	lit.Value = v
	return lit
}

// parseArrayLiteral parses "[a, b, ...]" with an optional trailing comma.
func (p *parser) parseArrayLiteral() ast.Expression {
	lb := p.advance() // "["
	arr := &ast.ArrayLiteral{Lbracket: lb.Pos}
	for !p.curIs(token.RBRACKET) && !p.curIs(token.EOF) {
		arr.Elements = append(arr.Elements, p.parseExpression(lowest))
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rb, _ := p.expect(token.RBRACKET)
	arr.Rbracket = rb.Pos
	return arr
}

// parseObjectLiteral parses "{ IDENT : expr, ... }" with identifier keys only
// and an optional trailing comma.
func (p *parser) parseObjectLiteral() ast.Expression {
	lb := p.advance() // "{"
	obj := &ast.ObjectLiteral{Lbrace: lb.Pos}
	for !p.curIs(token.RBRACE) && !p.curIs(token.EOF) {
		if !p.curIs(token.IDENT) {
			p.errorf(p.cur().Pos, "object key must be an identifier, got %s", describe(p.cur()))
			// Recover inside the object: skip to the next "," or "}".
			for !p.curIs(token.COMMA) && !p.curIs(token.RBRACE) && !p.curIs(token.EOF) {
				p.advance()
			}
			if p.curIs(token.COMMA) {
				p.advance()
			}
			continue
		}
		key := p.advance()
		entry := &ast.ObjectEntry{Key: &ast.Identifier{NamePos: key.Pos, Name: key.Literal}}
		colon, _ := p.expect(token.COLON)
		entry.ColonPos = colon.Pos
		entry.Value = p.parseExpression(lowest)
		obj.Entries = append(obj.Entries, entry)
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rb, _ := p.expect(token.RBRACE)
	obj.Rbrace = rb.Pos
	return obj
}

// parseNew parses "new global? primaryType ('[' ']')* (args?)" (D-NEW).
func (p *parser) parseNew() ast.Expression {
	kw := p.advance() // "new"
	expr := &ast.NewExpression{NewPos: kw.Pos}
	if p.curIs(token.GLOBAL) {
		g := p.advance()
		expr.Global = true
		expr.GlobalPos = g.Pos
	}
	base := p.parsePrimaryType()
	// Collect "[]" pairs. When at least one is present, the last pair sets
	// IsArray and the rest wrap the type (ast.go NewExpression contract).
	var brackets [][2]token.Position
	for p.curIs(token.LBRACKET) && p.peekIs(1, token.RBRACKET) {
		lb := p.advance()
		rb := p.advance()
		brackets = append(brackets, [2]token.Position{lb.Pos, rb.Pos})
	}
	if len(brackets) > 0 {
		expr.IsArray = true
		// All but the last pair become ArrayType wrappers around base.
		for i := 0; i < len(brackets)-1; i++ {
			base = &ast.ArrayType{Elem: base, Lbracket: brackets[i][0], Rbracket: brackets[i][1]}
		}
	}
	expr.Type = base
	lp, ok := p.expect(token.LPAREN)
	if !ok {
		return expr
	}
	expr.Lparen = lp.Pos
	for !p.curIs(token.RPAREN) && !p.curIs(token.EOF) {
		expr.Args = append(expr.Args, p.parseExpression(lowest))
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rp, _ := p.expect(token.RPAREN)
	expr.Rparen = rp.Pos
	return expr
}

// ---------------------------------------------------------------------------
// Generic-call speculation (D-GENCALL, D-SNAPSHOT, D-SPLIT)

// tryGenericCall speculatively parses a type-argument list after left, which
// must be followed immediately by "(". It snapshots the cursor; on failure it
// rewinds (dropping any diagnostics) and returns ok=false, leaving "<" to be
// parsed as the less-than operator. The current token is "<".
func (p *parser) tryGenericCall(left ast.Expression) (ast.Expression, bool) {
	mark, diagLen := p.snapshot()
	langle := p.cur().Pos
	p.advance() // "<"

	// A generic CALL always closes on ">" followed immediately by "(", never
	// on ">=" (D-SPLIT applies only to type contexts), so the trial does not
	// commit any token split (commit=false): mutating p.toks during a trial
	// that may rewind would corrupt the slice.
	typeArgs, rangle, ok := p.tryTypeArgList(false)
	if !ok || !p.curIs(token.LPAREN) {
		p.rewind(mark, diagLen)
		return nil, false
	}
	// Committed: the "<...>(" shape held. Build the generic call.
	return p.parseCall(left, typeArgs, langle, rangle), true
}

// tryTypeArgList parses "type (, type)* >" after the "<" has been consumed.
// It returns the parsed arguments, the position of the closing ">", and
// whether the list was well-formed (a non-empty list closed by ">" or a split
// ">="). An empty "<>" is not well-formed. commit reports whether this is a
// committed parse (so a ">=" closer may rewrite the token, D-SPLIT) rather
// than a speculative trial that might rewind (which must not mutate p.toks).
func (p *parser) tryTypeArgList(commit bool) ([]ast.TypeExpr, token.Position, bool) {
	if p.curIs(token.GT) || p.curIs(token.GT_EQ) {
		return nil, token.Position{}, false // empty "<>" is not a list
	}
	var args []ast.TypeExpr
	for {
		arg, ok := p.tryType(commit)
		if !ok {
			return nil, token.Position{}, false
		}
		args = append(args, arg)
		if p.curIs(token.COMMA) {
			p.advance()
			continue
		}
		break
	}
	rangle, ok := p.closeAngle(commit)
	if !ok {
		return nil, token.Position{}, false
	}
	return args, rangle, true
}

// closeAngle consumes the ">" closing a type-argument list and returns its
// position. A ">=" token (which the lexer produces for "Page<User>=", since
// there is no ">>" or split lexing) closes on its ">" and leaves an "=" for
// the enclosing context (D-SPLIT): in a committed parse the current token is
// rewritten in place to a synthetic "=" (ASSIGN) and NOT advanced, so the
// enclosing let/assignment reads it. During a speculative trial (commit ==
// false) the token must not be mutated — a trial may rewind — and a generic
// CALL never closes on ">=" anyway (its closer must be followed by "("), so a
// ">=" closer in a trial simply reports success without a split. "<=" never
// closes a type-arg list.
func (p *parser) closeAngle(commit bool) (token.Position, bool) {
	switch p.cur().Type {
	case token.GT:
		return p.advance().Pos, true
	case token.GT_EQ:
		gt := p.cur().Pos
		if commit {
			p.toks[p.pos] = token.Token{
				Type:    token.ASSIGN,
				Literal: "=",
				Pos:     gt.Advance(1),
				End:     p.cur().End,
			}
		}
		return gt, true
	}
	return token.Position{}, false
}

// ---------------------------------------------------------------------------
// Types (parseType) (D-TYPE part, D-SPLIT)

// parseType parses "primaryType ('[' ']')* ('|' 'null')?". On a failure it
// returns a BadType spanning the skipped tokens.
func (p *parser) parseType() ast.TypeExpr {
	// A direct parseType is always a committed context (annotations, params,
	// return types), so a ">=" closer may split (D-SPLIT).
	t, ok := p.tryType(true)
	if !ok {
		from := p.cur().Pos
		to := p.synchronize()
		return &ast.BadType{From: from, To: to}
	}
	return t
}

// tryType parses a type and reports whether it succeeded, WITHOUT recovering
// (so speculation can rewind cleanly). It is also the committing type parser
// for a confirmed generic call. ok=false means the current position does not
// start a type.
func (p *parser) tryType(commit bool) (ast.TypeExpr, bool) {
	base, ok := p.tryPrimaryType(commit)
	if !ok {
		return nil, false
	}
	// Zero or more "[]" pairs, left to right: T[][] is ArrayType(ArrayType(T)).
	for p.curIs(token.LBRACKET) && p.peekIs(1, token.RBRACKET) {
		lb := p.advance()
		rb := p.advance()
		base = &ast.ArrayType{Elem: base, Lbracket: lb.Pos, Rbracket: rb.Pos}
	}
	// Optional trailing "| null".
	if p.curIs(token.PIPE) {
		pipe := p.advance()
		null, ok := p.expect(token.NULL)
		if !ok {
			return nil, false
		}
		base = &ast.OptionalType{Elem: base, PipePos: pipe.Pos, NullPos: null.Pos}
	}
	return base, true
}

// tryPrimaryType parses "IDENT typeArgs? | '(' type ')'".
func (p *parser) tryPrimaryType(commit bool) (ast.TypeExpr, bool) {
	switch p.cur().Type {
	case token.IDENT:
		first := p.advance()
		named := &ast.NamedType{Name: &ast.Identifier{NamePos: first.Pos, Name: first.Literal}}
		// Dotted type "m.User" (DESIGN-modules.md §2.3): a "." followed by
		// an identifier makes first the module qualifier and the member the
		// type name. A "." NOT followed by an identifier is malformed; like
		// a bad "<...>" list it returns (nil, false) so a speculative caller
		// backtracks and the committed wrapper emits "expected a type".
		if p.curIs(token.DOT) {
			if !p.peekIs(1, token.IDENT) {
				return nil, false
			}
			p.advance() // "."
			second := p.advance()
			named.Qualifier = named.Name
			named.Name = &ast.Identifier{NamePos: second.Pos, Name: second.Literal}
		}
		if p.curIs(token.LT) {
			langle := p.advance()
			args, rangle, ok := p.tryTypeArgList(commit)
			if !ok {
				return nil, false
			}
			named.Langle = langle.Pos
			named.TypeArgs = args
			named.Rangle = rangle
		}
		return named, true
	case token.LPAREN:
		lp := p.advance()
		inner, ok := p.tryType(commit)
		if !ok {
			return nil, false
		}
		rp, ok := p.expect(token.RPAREN)
		if !ok {
			return nil, false
		}
		return &ast.ParenType{Lparen: lp.Pos, Inner: inner, Rparen: rp.Pos}, true
	}
	return nil, false
}

// parsePrimaryType is the recovering variant used by parseNew (which must not
// backtrack): on failure it returns a BadType.
func (p *parser) parsePrimaryType() ast.TypeExpr {
	// new-expression type head is a committed context.
	t, ok := p.tryPrimaryType(true)
	if !ok {
		from := p.cur().Pos
		p.errorf(from, "expected a type, got %s", describe(p.cur()))
		to := from
		if p.cur().Type != token.EOF {
			to = p.advance().End
		}
		return &ast.BadType{From: from, To: to}
	}
	return t
}
