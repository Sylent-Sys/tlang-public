// Package lexer turns TLang source text into a slice of token.Token.
//
// The whole file is tokenized up front (the parser backtracks over the slice
// for speculative type-argument parsing, DESIGN.md §2.2); the returned slice
// always ends with a single EOF token and the lexer never panics. White space
// and comments produce no tokens. Any lexical error produces one ILLEGAL token
// whose Literal is a human-readable message and whose Pos/End delimit the
// offending source text, together with one diag.List entry of code
// diag.CodeLex ("E-LEX"). The parser is what ultimately reports those.
//
// Resolved design decisions (see .agents/tasks/lexer-plan.md for the full
// reasoning):
//
//   - D2. There is no "->" token in the token contract (token/token.go has
//     only ARROW == "=>"). The lexer therefore never emits "->": a "-"
//     followed by ">" lexes as MINUS then GT by ordinary scanning.
//   - D3. "===" and "!==" are not TLang operators. Each is lexed as one
//     ILLEGAL token spanning all three bytes with a hint ("use ==" / "use !=")
//     plus an E-LEX diagnostic, so scanning resumes cleanly after it.
//   - D4. ">=" is emitted whole by maximal munch even in generic type syntax
//     such as "Page<User>=". Splitting a trailing ">=" is the parser's job; it
//     backtracks over the full token slice (DESIGN.md §2.2), matching the
//     token contract that "Page<Page<User>>" is two GT (no ">>" token).
//   - D5. Identifiers starting with "__" are returned as IDENT. The "__"
//     reservation is enforced by the checker (types.CheckDeclName), not here.
package lexer

import (
	"fmt"
	"unicode/utf8"

	"tlang/diag"
	"tlang/token"
)

// Lex tokenizes src and returns the token slice (always ending in EOF) and the
// diagnostics collected along the way. file names the source for diagnostics.
// Lex never panics; malformed input yields ILLEGAL tokens and E-LEX entries.
func Lex(file string, src []byte) ([]token.Token, *diag.List) {
	s := &scanner{
		src:   src,
		line:  1,
		diags: diag.NewList(file),
	}
	for {
		tok := s.next()
		s.toks = append(s.toks, tok)
		if tok.Type == token.EOF {
			break
		}
	}
	return s.toks, s.diags
}

// scanner holds the mutable lexing state. Positions are byte-oriented:
// column == off-lineStart+1, both 1-based, offset 0-based (token.Position).
type scanner struct {
	src       []byte
	off       int // byte offset of the next unread byte
	line      int // current 1-based line
	lineStart int // byte offset of the first byte of the current line
	diags     *diag.List
	toks      []token.Token
}

// pos returns the current position (at s.off).
func (s *scanner) pos() token.Position {
	return token.Position{Line: s.line, Column: s.off - s.lineStart + 1, Offset: s.off}
}

// atEOF reports whether all input has been consumed.
func (s *scanner) atEOF() bool { return s.off >= len(s.src) }

// peek returns the current byte, or 0 at EOF.
func (s *scanner) peek() byte {
	if s.off >= len(s.src) {
		return 0
	}
	return s.src[s.off]
}

// peekAt returns the byte n ahead of the cursor, or 0 past EOF.
func (s *scanner) peekAt(n int) byte {
	i := s.off + n
	if i >= len(s.src) {
		return 0
	}
	return s.src[i]
}

// advance consumes one byte, tracking line and column. A "\n" bumps the line
// and resets lineStart to the byte after it ("\r\n" advances the line on the
// "\n", so the "\r" is an ordinary byte on the previous line).
func (s *scanner) advance() {
	if s.off >= len(s.src) {
		return
	}
	if s.src[s.off] == '\n' {
		s.line++
		s.lineStart = s.off + 1
	}
	s.off++
}

// illegal records an E-LEX diagnostic at pos and returns an ILLEGAL token
// spanning [pos, end) whose Literal is the same human-readable message.
func (s *scanner) illegal(pos, end token.Position, format string, args ...any) token.Token {
	msg := fmt.Sprintf(format, args...)
	s.diags.Errorf(pos, diag.CodeLex, "%s", msg)
	return token.Token{Type: token.ILLEGAL, Literal: msg, Pos: pos, End: end}
}

// next scans and returns the next token, skipping white space and comments.
func (s *scanner) next() token.Token {
	for {
		s.skipSpace()
		if s.atEOF() {
			p := s.pos()
			return token.Token{Type: token.EOF, Pos: p, End: p}
		}
		c := s.peek()
		if c == '/' && (s.peekAt(1) == '/' || s.peekAt(1) == '*') {
			if tok, ok := s.skipComment(); !ok {
				// Unterminated block comment: ILLEGAL already built.
				return tok
			}
			continue
		}
		switch {
		case isIdentStart(c):
			return s.scanIdent()
		case isDigit(c):
			return s.scanNumber()
		case c == '"' || c == '\'':
			return s.scanString(c)
		default:
			return s.scanOperator()
		}
	}
}

// skipSpace consumes spaces, tabs, carriage returns and newlines.
func (s *scanner) skipSpace() {
	for !s.atEOF() {
		switch s.peek() {
		case ' ', '\t', '\r', '\n':
			s.advance()
		default:
			return
		}
	}
}

// skipComment consumes a "//" line comment or a "/* */" block comment. It
// returns ok == true when a comment was fully consumed. For an unterminated
// block comment it returns ok == false and an ILLEGAL token spanning from the
// opening "/*" to EOF (an E-LEX diagnostic was recorded).
func (s *scanner) skipComment() (token.Token, bool) {
	if s.peekAt(1) == '/' {
		s.advance() // first /
		s.advance() // second /
		for !s.atEOF() && s.peek() != '\n' {
			s.advance()
		}
		return token.Token{}, true
	}
	// Block comment.
	start := s.pos()
	s.advance() // /
	s.advance() // *
	for !s.atEOF() {
		if s.peek() == '*' && s.peekAt(1) == '/' {
			s.advance()
			s.advance()
			return token.Token{}, true
		}
		s.advance()
	}
	return s.illegal(start, s.pos(), "unterminated block comment"), false
}

// scanIdent scans an identifier run and classifies it via token.LookupIdent.
// Identifiers starting with "__" stay IDENT (D5).
func (s *scanner) scanIdent() token.Token {
	start := s.pos()
	for !s.atEOF() && isIdentPart(s.peek()) {
		s.advance()
	}
	lit := string(s.src[start.Offset:s.off])
	return token.Token{Type: token.LookupIdent(lit), Literal: lit, Pos: start, End: s.pos()}
}

// scanOperator scans one operator or delimiter with maximal munch, or emits an
// ILLEGAL for an unexpected byte. "===" and "!==" are the two ILLEGAL hints
// (D3); ">=" is never split (D4); there is no "->" (D2).
func (s *scanner) scanOperator() token.Token {
	start := s.pos()
	c := s.peek()
	c1 := s.peekAt(1)

	emit := func(n int, t token.TokenType) token.Token {
		for i := 0; i < n; i++ {
			s.advance()
		}
		return token.Token{Type: t, Literal: string(t), Pos: start, End: s.pos()}
	}

	switch c {
	case '=':
		if c1 == '=' && s.peekAt(2) == '=' { // === (D3)
			for i := 0; i < 3; i++ {
				s.advance()
			}
			return s.illegal(start, s.pos(), `invalid operator "===", use "=="`)
		}
		if c1 == '=' {
			return emit(2, token.EQ)
		}
		if c1 == '>' {
			return emit(2, token.ARROW)
		}
		return emit(1, token.ASSIGN)
	case '!':
		if c1 == '=' && s.peekAt(2) == '=' { // !== (D3)
			for i := 0; i < 3; i++ {
				s.advance()
			}
			return s.illegal(start, s.pos(), `invalid operator "!==", use "!="`)
		}
		if c1 == '=' {
			return emit(2, token.NOT_EQ)
		}
		return emit(1, token.BANG)
	case '+':
		if c1 == '+' {
			return emit(2, token.INCREMENT)
		}
		if c1 == '=' {
			return emit(2, token.PLUS_ASSIGN)
		}
		return emit(1, token.PLUS)
	case '-':
		if c1 == '-' {
			return emit(2, token.DECREMENT)
		}
		if c1 == '=' {
			return emit(2, token.MINUS_ASSIGN)
		}
		return emit(1, token.MINUS) // no "->" (D2)
	case '*':
		if c1 == '=' {
			return emit(2, token.ASTERISK_ASSIGN)
		}
		return emit(1, token.ASTERISK)
	case '/':
		if c1 == '=' {
			return emit(2, token.SLASH_ASSIGN)
		}
		return emit(1, token.SLASH)
	case '%':
		if c1 == '=' {
			return emit(2, token.MOD_ASSIGN)
		}
		return emit(1, token.MOD)
	case '<':
		if c1 == '=' {
			return emit(2, token.LT_EQ)
		}
		return emit(1, token.LT)
	case '>':
		if c1 == '=' { // GT_EQ whole, never split (D4)
			return emit(2, token.GT_EQ)
		}
		return emit(1, token.GT)
	case '&':
		if c1 == '&' {
			return emit(2, token.AND)
		}
		return emit(1, token.AMPERSAND)
	case '|':
		if c1 == '|' {
			return emit(2, token.OR)
		}
		return emit(1, token.PIPE)
	case '?':
		if c1 == '?' {
			return emit(2, token.NULLISH)
		}
		return emit(1, token.QUESTION)
	case '@':
		return emit(1, token.AT)
	case ',':
		return emit(1, token.COMMA)
	case '.':
		return emit(1, token.DOT)
	case ':':
		return emit(1, token.COLON)
	case ';':
		return emit(1, token.SEMICOLON)
	case '(':
		return emit(1, token.LPAREN)
	case ')':
		return emit(1, token.RPAREN)
	case '{':
		return emit(1, token.LBRACE)
	case '}':
		return emit(1, token.RBRACE)
	case '[':
		return emit(1, token.LBRACKET)
	case ']':
		return emit(1, token.RBRACKET)
	}

	// Unexpected byte: consume one rune so a multi-byte character is reported
	// and spanned as a unit, and scanning makes progress.
	r, size := utf8.DecodeRune(s.src[s.off:])
	for i := 0; i < size; i++ {
		s.advance()
	}
	if r == utf8.RuneError && size <= 1 {
		return s.illegal(start, s.pos(), "unexpected byte %#x", c)
	}
	return s.illegal(start, s.pos(), "unexpected character %q", r)
}

// scanNumber scans an INT or FLOAT. Literal keeps the raw source text. See the
// number rules in token/token.go and the plan: no leading zero for decimals,
// "0x"/"0X" hex, "_" only between two digits, a float needs digits on both
// sides of "." or an exponent (so "5." stays INT 5 then DOT).
func (s *scanner) scanNumber() token.Token {
	start := s.pos()

	if s.peek() == '0' && (s.peekAt(1) == 'x' || s.peekAt(1) == 'X') {
		return s.scanHex(start)
	}

	// Integer part.
	if bad := s.scanDigits(start, isDigit, "decimal"); bad != nil {
		return *bad
	}
	intText := s.src[start.Offset:s.off]
	// A single run of decimal digits with a leading zero (and length > 1) is an
	// octal-looking literal, which TLang does not allow.
	if len(intText) > 1 && intText[0] == '0' {
		s.consumeNumberTail()
		return s.illegal(start, s.pos(), "leading zeros are not allowed in a number literal")
	}

	isFloat := false

	// Fractional part: only a float when a digit follows the ".".
	if s.peek() == '.' && isDigit(s.peekAt(1)) {
		isFloat = true
		s.advance() // .
		if bad := s.scanDigits(start, isDigit, "decimal"); bad != nil {
			return *bad
		}
	}

	// Exponent.
	if s.peek() == 'e' || s.peek() == 'E' {
		isFloat = true
		s.advance() // e / E
		if s.peek() == '+' || s.peek() == '-' {
			s.advance()
		}
		if !isDigit(s.peek()) {
			s.consumeNumberTail()
			return s.illegal(start, s.pos(), "malformed number literal: exponent has no digits")
		}
		if bad := s.scanDigits(start, isDigit, "decimal"); bad != nil {
			return *bad
		}
	}

	// A number may not be immediately followed by an identifier character
	// (e.g. "123abc"); that is a malformed literal.
	if isIdentStart(s.peek()) {
		s.consumeNumberTail()
		return s.illegal(start, s.pos(), "malformed number literal")
	}

	typ := token.INT
	if isFloat {
		typ = token.FLOAT
	}
	lit := string(s.src[start.Offset:s.off])
	return token.Token{Type: typ, Literal: lit, Pos: start, End: s.pos()}
}

// scanHex scans "0x"/"0X" followed by hex digits with "_" only between digits.
func (s *scanner) scanHex(start token.Position) token.Token {
	s.advance() // 0
	s.advance() // x / X
	if !isHexDigit(s.peek()) {
		s.consumeNumberTail()
		return s.illegal(start, s.pos(), "malformed hexadecimal literal: no digits")
	}
	if bad := s.scanDigits(start, isHexDigit, "hexadecimal"); bad != nil {
		return *bad
	}
	// A hex literal may not be followed directly by "." or an identifier
	// character (e.g. "0xFF.5" or "0xGG"): that is a malformed literal.
	if isIdentStart(s.peek()) || s.peek() == '.' {
		s.consumeNumberTail()
		return s.illegal(start, s.pos(), "malformed hexadecimal literal")
	}
	lit := string(s.src[start.Offset:s.off])
	return token.Token{Type: token.INT, Literal: lit, Pos: start, End: s.pos()}
}

// scanDigits consumes a run of digits (per isDigit) with "_" allowed only
// between two digits. A leading, trailing or doubled "_" is an error. On error
// it consumes the rest of the number run and returns an ILLEGAL token.
func (s *scanner) scanDigits(start token.Position, isDigit func(byte) bool, kind string) *token.Token {
	if !isDigit(s.peek()) {
		return nil
	}
	s.advance() // first digit
	for {
		c := s.peek()
		switch {
		case isDigit(c):
			s.advance()
		case c == '_':
			if !isDigit(s.peekAt(1)) {
				s.advance() // consume the bad "_"
				s.consumeNumberTail()
				t := s.illegal(start, s.pos(), "'_' may appear only between digits in a %s literal", kind)
				return &t
			}
			s.advance() // the "_"
			s.advance() // the digit after it
		default:
			return nil
		}
	}
}

// consumeNumberTail swallows the remaining digit/dot/ident characters of a
// malformed number so the whole bad run is spanned by one ILLEGAL token and
// scanning resumes cleanly after it.
func (s *scanner) consumeNumberTail() {
	for !s.atEOF() {
		c := s.peek()
		if isIdentPart(c) || c == '.' || c == '_' {
			s.advance()
			continue
		}
		return
	}
}

// scanString scans a "..." or '...' literal. Literal holds the DECODED bytes
// (UTF-8), not the raw source. Pos is the opening quote and End is just past
// the closing quote. A raw newline, EOF before the closing quote, an invalid
// escape, or a bad/unpaired \u surrogate is an E-LEX error (ILLEGAL token).
func (s *scanner) scanString(quote byte) token.Token {
	start := s.pos()
	s.advance() // opening quote
	var buf []byte
	for {
		if s.atEOF() {
			return s.illegal(start, s.pos(), "unterminated string literal")
		}
		c := s.peek()
		switch c {
		case quote:
			s.advance() // closing quote
			return token.Token{Type: token.STRING, Literal: string(buf), Pos: start, End: s.pos()}
		case '\n':
			// Raw newline: span the opening quote up to (not including) the
			// newline and resume at the newline.
			return s.illegal(start, s.pos(), "newline in string literal")
		case '\\':
			b, bad := s.scanEscape(start)
			if bad != nil {
				return *bad
			}
			buf = append(buf, b...)
		default:
			buf = append(buf, c)
			s.advance()
		}
	}
}

// scanEscape decodes one escape sequence starting at the backslash and returns
// its decoded bytes. On an invalid escape it returns a non-nil ILLEGAL token
// (an E-LEX diagnostic was recorded) after consuming to the end of the string
// so scanning resumes cleanly.
func (s *scanner) scanEscape(start token.Position) ([]byte, *token.Token) {
	escPos := s.pos()
	s.advance() // backslash
	if s.atEOF() {
		t := s.stringError(start, start, "unterminated string literal")
		return nil, &t
	}
	c := s.peek()
	switch c {
	case 'n':
		s.advance()
		return []byte{'\n'}, nil
	case 't':
		s.advance()
		return []byte{'\t'}, nil
	case 'r':
		s.advance()
		return []byte{'\r'}, nil
	case '0':
		s.advance()
		return []byte{0}, nil
	case '\\':
		s.advance()
		return []byte{'\\'}, nil
	case '"':
		s.advance()
		return []byte{'"'}, nil
	case '\'':
		s.advance()
		return []byte{'\''}, nil
	case 'u':
		return s.scanUnicodeEscape(start, escPos)
	default:
		// Report the offending escape, then skip to the end of the string so
		// one bad escape does not cascade.
		s.advance() // the character after the backslash
		s.skipToStringEnd()
		t := s.stringError(start, escPos, "invalid escape sequence %q", "\\"+string(c))
		return nil, &t
	}
}

// stringError records an E-LEX diagnostic at diagPos (the precise location of
// the offending escape) and returns an ILLEGAL token spanning from start (the
// opening quote) to the current cursor, after recovery has run.
func (s *scanner) stringError(start, diagPos token.Position, format string, args ...any) token.Token {
	msg := fmt.Sprintf(format, args...)
	s.diags.Errorf(diagPos, diag.CodeLex, "%s", msg)
	return token.Token{Type: token.ILLEGAL, Literal: msg, Pos: start, End: s.pos()}
}

// scanUnicodeEscape decodes "\uXXXX" (exactly 4 hex) or "\u{X...}" (1+ hex),
// combining a high+low surrogate pair into one code point before UTF-8
// encoding. escPos points at the backslash. The cursor is at 'u' on entry.
func (s *scanner) scanUnicodeEscape(start, escPos token.Position) ([]byte, *token.Token) {
	cp, bad := s.readOneUnicode(start, escPos)
	if bad != nil {
		return nil, bad
	}

	// Surrogate handling.
	if cp >= 0xD800 && cp <= 0xDBFF {
		// High surrogate: must be followed by a low-surrogate escape.
		if s.peek() == '\\' && s.peekAt(1) == 'u' {
			save := *s
			s.advance() // backslash
			low, bad := s.readOneUnicode(start, s.pos())
			if bad == nil && low >= 0xDC00 && low <= 0xDFFF {
				r := 0x10000 + (cp-0xD800)<<10 + (low - 0xDC00)
				return utf8Encode(rune(r)), nil
			}
			// Not a valid low surrogate: restore and fall through to error.
			*s = save
		}
		s.skipToStringEnd()
		t := s.stringError(start, escPos, "unpaired high surrogate in unicode escape")
		return nil, &t
	}
	if cp >= 0xDC00 && cp <= 0xDFFF {
		s.skipToStringEnd()
		t := s.stringError(start, escPos, "unpaired low surrogate in unicode escape")
		return nil, &t
	}
	if cp > 0x10FFFF {
		s.skipToStringEnd()
		t := s.stringError(start, escPos, "unicode escape out of range")
		return nil, &t
	}
	return utf8Encode(rune(cp)), nil
}

// readOneUnicode reads a single "\uXXXX" or "\u{X...}" code point value. The
// cursor is at 'u' on entry; it is left just past the escape. On a malformed
// escape it returns a non-nil ILLEGAL token after skipping to the string end.
func (s *scanner) readOneUnicode(start, escPos token.Position) (int, *token.Token) {
	s.advance() // u
	if s.peek() == '{' {
		s.advance() // {
		val := 0
		n := 0
		for isHexDigit(s.peek()) {
			val = val*16 + hexVal(s.peek())
			n++
			if val > 0x10FFFF {
				val = 0x110000 // clamp so the range check later fires
			}
			s.advance()
		}
		if n == 0 || s.peek() != '}' {
			s.skipToStringEnd()
			t := s.stringError(start, escPos, "invalid \\u{...} escape")
			return 0, &t
		}
		s.advance() // }
		return val, nil
	}
	// Exactly four hex digits.
	val := 0
	for i := 0; i < 4; i++ {
		if !isHexDigit(s.peek()) {
			s.skipToStringEnd()
			t := s.stringError(start, escPos, "\\u escape needs four hex digits")
			return 0, &t
		}
		val = val*16 + hexVal(s.peek())
		s.advance()
	}
	return val, nil
}

// skipToStringEnd advances to just past the closing quote or to the newline or
// EOF, so an in-string error does not derail the token stream. It is a
// best-effort recovery helper, not quote-aware beyond the first terminator.
func (s *scanner) skipToStringEnd() {
	for !s.atEOF() {
		c := s.peek()
		if c == '\n' {
			return
		}
		if c == '"' || c == '\'' {
			s.advance()
			return
		}
		if c == '\\' {
			s.advance()
			if !s.atEOF() {
				s.advance()
			}
			continue
		}
		s.advance()
	}
}

// utf8Encode returns the UTF-8 encoding of r.
func utf8Encode(r rune) []byte {
	var b [utf8.UTFMax]byte
	n := utf8.EncodeRune(b[:], r)
	return b[:n]
}

func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }
