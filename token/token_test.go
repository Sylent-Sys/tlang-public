package token

import "testing"

func TestLookupIdent(t *testing.T) {
	cases := map[string]TokenType{
		"let": LET, "const": CONST, "fn": FN, "interface": INTERFACE,
		"type": TYPE, "new": NEW, "return": RETURN, "if": IF, "else": ELSE,
		"for": FOR, "while": WHILE, "break": BREAK, "continue": CONTINUE,
		"try": TRY, "catch": CATCH, "throw": THROW,
		"true": TRUE, "false": FALSE, "null": NULL,
		"of": OF, "global": GLOBAL,
		// Not keywords.
		"x": IDENT, "int64": IDENT, "string": IDENT, "void": IDENT,
		"Context": IDENT, "Error": IDENT, "db": IDENT, "console": IDENT,
		"Let": IDENT, "function": IDENT, "undefined": IDENT, "transaction": IDENT,
	}
	for in, want := range cases {
		if got := LookupIdent(in); got != want {
			t.Errorf("LookupIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpellings(t *testing.T) {
	// The value of an operator token type is its lexeme.
	cases := map[TokenType]string{
		ASSIGN: "=", PLUS: "+", MINUS: "-", ASTERISK: "*", SLASH: "/", MOD: "%",
		BANG: "!", AMPERSAND: "&", AT: "@", PIPE: "|", QUESTION: "?",
		EQ: "==", NOT_EQ: "!=", LT: "<", GT: ">", LT_EQ: "<=", GT_EQ: ">=",
		AND: "&&", OR: "||", COMMA: ",", DOT: ".", COLON: ":", SEMICOLON: ";",
		LPAREN: "(", RPAREN: ")", LBRACE: "{", RBRACE: "}", LBRACKET: "[",
		RBRACKET: "]", ARROW: "=>",
		PLUS_ASSIGN: "+=", MINUS_ASSIGN: "-=", ASTERISK_ASSIGN: "*=",
		SLASH_ASSIGN: "/=", MOD_ASSIGN: "%=", INCREMENT: "++", DECREMENT: "--",
		NULLISH: "??",
		OF:      "OF", GLOBAL: "GLOBAL", LET: "LET", NULL: "NULL",
		ILLEGAL: "ILLEGAL", EOF: "EOF", IDENT: "IDENT", INT: "INT", FLOAT: "FLOAT",
		STRING: "STRING",
	}
	for tt, want := range cases {
		if string(tt) != want {
			t.Errorf("token %q, want %q", string(tt), want)
		}
	}
}

func TestIsKeyword(t *testing.T) {
	for _, k := range []TokenType{LET, OF, GLOBAL, NULL, TRUE} {
		if !k.IsKeyword() {
			t.Errorf("%q.IsKeyword() = false", k)
		}
	}
	for _, k := range []TokenType{IDENT, PLUS, EOF, ILLEGAL, ARROW} {
		if k.IsKeyword() {
			t.Errorf("%q.IsKeyword() = true", k)
		}
	}
}

func TestPosition(t *testing.T) {
	var zero Position
	if zero.IsValid() || zero.String() != "-" {
		t.Errorf("zero position: valid=%v string=%q", zero.IsValid(), zero.String())
	}
	if got := zero.Advance(3); got != zero {
		t.Errorf("invalid.Advance = %+v", got)
	}
	p := Position{Line: 3, Column: 5, Offset: 40}
	if p.String() != "3:5" {
		t.Errorf("String = %q", p.String())
	}
	if got := p.Advance(4); got != (Position{Line: 3, Column: 9, Offset: 44}) {
		t.Errorf("Advance = %+v", got)
	}
	q := Position{Line: 3, Column: 6, Offset: 41}
	r := Position{Line: 4, Column: 1, Offset: 50}
	if !p.Before(q) || q.Before(p) || !q.Before(r) || p.Before(p) {
		t.Error("Before ordering wrong")
	}
}
