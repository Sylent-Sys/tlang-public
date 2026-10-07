// Package token defines the lexical tokens of TLang (spec §5.1 plus the
// additions of DESIGN.md §2.1) and source positions.
//
// Lexical contract between the lexer and the parser:
//
//   - The lexer turns the whole file into a []Token up front (the parser
//     backtracks over it for speculative type-argument parsing, DESIGN.md
//     §2.2). The last token is always EOF. The lexer never panics.
//   - Comments (// ... and /* ... */) and white space produce no tokens.
//   - Any lexical error produces one ILLEGAL token whose Literal is a
//     human-readable message (for example "unterminated string literal") and
//     whose Pos/End delimit the offending source text. The parser reports it
//     with code diag.CodeLex. Examples: an unterminated block comment, a raw
//     newline in a string literal, an invalid escape, a malformed number, an
//     unexpected character, and an identifier that starts with "__"
//     (reserved for the compiler, DESIGN.md §2.1).
//   - "&" is lexed as AMPERSAND (reserved); the parser rejects it.
//   - There is no ">>" or ">>=" token, so Page<Page<User>> lexes as two GT.
//   - Numbers carry no sign ("-1" is MINUS INT). INT is "0", a decimal
//     literal without a leading zero, or "0x"/"0X" followed by hex digits;
//     "_" may appear only between two digits ("1_000", "0xFF_FF"). FLOAT has
//     digits on both sides of "." and/or an exponent: "1.5", "1e9",
//     "2.5e-3". "1." and ".5" are not floats, so "5.toString()" lexes as
//     INT DOT IDENT LPAREN RPAREN.
//   - String literals use "..." or '...'.
package token

import "strconv"

// TokenType identifies the kind of a token. For operators and delimiters the
// value is the lexeme itself (PLUS == "+", NULLISH == "??"), so
// string(t) can be printed in diagnostics; for keywords and literal classes
// it is an upper-case name (LET == "LET", INT == "INT").
type TokenType string

// Position is a location in a source file.
//
// Line and Column are 1-based. Column counts bytes from the start of the
// line, not runes or display cells (DESIGN.md §2.1). Offset is the 0-based
// byte offset from the start of the file, so src[p.Offset] is the byte at p.
// The zero Position (Line == 0) means "no position".
//
// Compare positions with Before or by Line/Column; Offset is redundant with
// Line/Column for a given source and exists so that tools can slice the
// source text of a node: src[n.Pos().Offset:n.End().Offset].
type Position struct {
	Line   int
	Column int
	Offset int
}

// IsValid reports whether p denotes a real location (Line > 0).
func (p Position) IsValid() bool { return p.Line > 0 }

// String formats p as "line:col", or "-" when p is not valid.
func (p Position) String() string {
	if !p.IsValid() {
		return "-"
	}
	return strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column)
}

// Advance returns the position n bytes after p on the same line (Column and
// Offset both grow by n). It is meant for single-line lexemes, for example
// the end of an identifier: id.NamePos.Advance(len(id.Name)). An invalid p is
// returned unchanged.
func (p Position) Advance(n int) Position {
	if !p.IsValid() {
		return p
	}
	return Position{Line: p.Line, Column: p.Column + n, Offset: p.Offset + n}
}

// Before reports whether p comes strictly before q (by Line, then Column).
func (p Position) Before(q Position) bool {
	if p.Line != q.Line {
		return p.Line < q.Line
	}
	return p.Column < q.Column
}

// Token is one lexeme.
//
// Literal depends on Type:
//   - IDENT and keywords: the source text.
//   - INT and FLOAT: the exact source text, including a 0x/0X prefix and "_"
//     separators (the parser computes the value).
//   - STRING: the decoded value as UTF-8 bytes, without quotes (escapes
//     \n \t \r \0 \\ \" \' \uXXXX \u{X...} already applied; a \uD8xx\uDCxx
//     surrogate pair decodes to one code point).
//   - operators and delimiters: the lexeme (same as string(Type)).
//   - EOF: "".
//   - ILLEGAL: a human-readable error message (see the package comment).
//
// Pos is the position of the first byte of the lexeme and End the position
// immediately after its last byte (exclusive); for STRING, Pos is the opening
// quote and End is just past the closing quote. For EOF, Pos == End == the
// position just past the last byte of the file.
type Token struct {
	Type    TokenType
	Literal string
	Pos     Position
	End     Position
}

// Token types of spec §5.1, unchanged.
const (
	ILLEGAL TokenType = "ILLEGAL"
	EOF     TokenType = "EOF"

	IDENT  TokenType = "IDENT"
	INT    TokenType = "INT"
	FLOAT  TokenType = "FLOAT"
	STRING TokenType = "STRING"

	ASSIGN    TokenType = "="
	PLUS      TokenType = "+"
	MINUS     TokenType = "-"
	ASTERISK  TokenType = "*"
	SLASH     TokenType = "/"
	MOD       TokenType = "%"
	BANG      TokenType = "!"
	AMPERSAND TokenType = "&" // reserved, see §3.3
	AT        TokenType = "@"
	PIPE      TokenType = "|" // optional type: T | null
	QUESTION  TokenType = "?" // optional field: name?: string

	EQ     TokenType = "=="
	NOT_EQ TokenType = "!="
	LT     TokenType = "<"
	GT     TokenType = ">"
	LT_EQ  TokenType = "<="
	GT_EQ  TokenType = ">="
	AND    TokenType = "&&"
	OR     TokenType = "||"

	COMMA     TokenType = ","
	DOT       TokenType = "."
	COLON     TokenType = ":"
	SEMICOLON TokenType = ";"
	LPAREN    TokenType = "("
	RPAREN    TokenType = ")"
	LBRACE    TokenType = "{"
	RBRACE    TokenType = "}"
	LBRACKET  TokenType = "["
	RBRACKET  TokenType = "]"
	ARROW     TokenType = "=>"

	LET       TokenType = "LET"
	CONST     TokenType = "CONST"
	FN        TokenType = "FN"
	INTERFACE TokenType = "INTERFACE"
	TYPE      TokenType = "TYPE"
	NEW       TokenType = "NEW"
	RETURN    TokenType = "RETURN"
	IF        TokenType = "IF"
	ELSE      TokenType = "ELSE"
	FOR       TokenType = "FOR"
	WHILE     TokenType = "WHILE"
	BREAK     TokenType = "BREAK"
	CONTINUE  TokenType = "CONTINUE"
	TRY       TokenType = "TRY"
	CATCH     TokenType = "CATCH"
	THROW     TokenType = "THROW"
	TRUE      TokenType = "TRUE"
	FALSE     TokenType = "FALSE"
	NULL      TokenType = "NULL"
)

// Token types added by DESIGN.md §2.1.
const (
	PLUS_ASSIGN     TokenType = "+=" // compound assignment, statement level only
	MINUS_ASSIGN    TokenType = "-=" // compound assignment, statement level only
	ASTERISK_ASSIGN TokenType = "*=" // compound assignment, statement level only
	SLASH_ASSIGN    TokenType = "/=" // compound assignment, statement level only
	MOD_ASSIGN      TokenType = "%=" // compound assignment, statement level only
	INCREMENT       TokenType = "++" // postfix x++, statement level only
	DECREMENT       TokenType = "--" // postfix x--, statement level only
	NULLISH         TokenType = "??" // x ?? fallback

	OF     TokenType = "OF"     // keyword "of": for (const x of xs)
	GLOBAL TokenType = "GLOBAL" // keyword "global": new global T()
)

// Token types added by DESIGN-modules.md §2.1 for the module system. The
// *, {, }, ,, STRING and . that the import/export grammar also uses already
// exist above (ASTERISK, LBRACE, RBRACE, COMMA, STRING, DOT).
const (
	IMPORT  TokenType = "IMPORT"  // keyword "import"
	EXPORT  TokenType = "EXPORT"  // keyword "export"
	FROM    TokenType = "FROM"    // keyword "from": import ... from "path"
	AS      TokenType = "AS"      // keyword "as": import * as m, { X as Y }
	DEFAULT TokenType = "DEFAULT" // keyword "default": export default decl
)

var keywords = map[string]TokenType{
	"let": LET, "const": CONST, "fn": FN, "interface": INTERFACE,
	"type": TYPE, "new": NEW, "return": RETURN, "if": IF, "else": ELSE,
	"for": FOR, "while": WHILE, "break": BREAK, "continue": CONTINUE,
	"try": TRY, "catch": CATCH, "throw": THROW,
	"true": TRUE, "false": FALSE, "null": NULL,
	"of": OF, "global": GLOBAL,
	"import": IMPORT, "export": EXPORT, "from": FROM, "as": AS, "default": DEFAULT,
}

// LookupIdent returns the keyword token type for ident, or IDENT if ident is
// not a keyword. Type names such as int64, string, void, Context and Error,
// and the builtin namespaces db and console, are identifiers, not keywords.
func LookupIdent(ident string) TokenType {
	if tok, ok := keywords[ident]; ok {
		return tok
	}
	return IDENT
}

// IsKeyword reports whether t is the token type of a keyword.
func (t TokenType) IsKeyword() bool {
	for _, k := range keywords {
		if k == t {
			return true
		}
	}
	return false
}
