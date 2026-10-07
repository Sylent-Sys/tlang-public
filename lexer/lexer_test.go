package lexer

import (
	"strings"
	"testing"

	"tlang/token"
)

// lexTypes runs Lex and returns the token types (minus the trailing EOF) for
// compact assertions on token sequences.
func lexTypes(t *testing.T, src string) []token.TokenType {
	t.Helper()
	toks, _ := Lex("test.tl", []byte(src))
	if len(toks) == 0 || toks[len(toks)-1].Type != token.EOF {
		t.Fatalf("Lex(%q): last token is not EOF: %v", src, toks)
	}
	out := make([]token.TokenType, 0, len(toks)-1)
	for _, tk := range toks[:len(toks)-1] {
		out = append(out, tk.Type)
	}
	return out
}

func typesEqual(a, b []token.TokenType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOperatorsAndDelimiters(t *testing.T) {
	cases := []struct {
		src  string
		want []token.TokenType
	}{
		// single-char
		{"= + - * / % ! & @ | ? < > , . : ; ( ) { } [ ]", []token.TokenType{
			token.ASSIGN, token.PLUS, token.MINUS, token.ASTERISK, token.SLASH,
			token.MOD, token.BANG, token.AMPERSAND, token.AT, token.PIPE,
			token.QUESTION, token.LT, token.GT, token.COMMA, token.DOT,
			token.COLON, token.SEMICOLON, token.LPAREN, token.RPAREN,
			token.LBRACE, token.RBRACE, token.LBRACKET, token.RBRACKET,
		}},
		// multi-char maximal munch
		{"== != <= >= && || => += -= *= /= %= ++ -- ??", []token.TokenType{
			token.EQ, token.NOT_EQ, token.LT_EQ, token.GT_EQ, token.AND,
			token.OR, token.ARROW, token.PLUS_ASSIGN, token.MINUS_ASSIGN,
			token.ASTERISK_ASSIGN, token.SLASH_ASSIGN, token.MOD_ASSIGN,
			token.INCREMENT, token.DECREMENT, token.NULLISH,
		}},
		// no "->" token (D2): "-" ">" are two tokens
		{"->", []token.TokenType{token.MINUS, token.GT}},
		// ">=" is not split for generics (D4)
		{"Page<User>=", []token.TokenType{token.IDENT, token.LT, token.IDENT, token.GT_EQ}},
		// nested generics close as two GT (token contract, no ">>")
		{"Page<Page<User>>", []token.TokenType{
			token.IDENT, token.LT, token.IDENT, token.LT, token.IDENT, token.GT, token.GT,
		}},
	}
	for _, tc := range cases {
		if got := lexTypes(t, tc.src); !typesEqual(got, tc.want) {
			t.Errorf("Lex(%q) types = %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestKeywordsVsIdentifiers(t *testing.T) {
	cases := []struct {
		src  string
		want token.TokenType
	}{
		{"let", token.LET}, {"const", token.CONST}, {"fn", token.FN},
		{"interface", token.INTERFACE}, {"type", token.TYPE}, {"new", token.NEW},
		{"return", token.RETURN}, {"if", token.IF}, {"else", token.ELSE},
		{"for", token.FOR}, {"while", token.WHILE}, {"break", token.BREAK},
		{"continue", token.CONTINUE}, {"try", token.TRY}, {"catch", token.CATCH},
		{"throw", token.THROW}, {"true", token.TRUE}, {"false", token.FALSE},
		{"null", token.NULL}, {"of", token.OF}, {"global", token.GLOBAL},
		// identifiers, including type/builtin names that are NOT keywords
		{"foo", token.IDENT}, {"int64", token.IDENT}, {"string", token.IDENT},
		{"Context", token.IDENT}, {"db", token.IDENT}, {"console", token.IDENT},
		// "__" names stay IDENT in the lexer (D5)
		{"__secret", token.IDENT}, {"__", token.IDENT},
		{"letx", token.IDENT}, {"_x", token.IDENT}, {"x1", token.IDENT},
	}
	for _, tc := range cases {
		toks, diags := Lex("test.tl", []byte(tc.src))
		if diags.HasErrors() {
			t.Errorf("Lex(%q): unexpected errors: %v", tc.src, diags.Error())
		}
		if toks[0].Type != tc.want {
			t.Errorf("Lex(%q) = %v, want %v", tc.src, toks[0].Type, tc.want)
		}
		if toks[0].Literal != tc.src {
			t.Errorf("Lex(%q) literal = %q, want %q", tc.src, toks[0].Literal, tc.src)
		}
	}
}

func TestIntegerLiterals(t *testing.T) {
	ok := []string{"0", "1", "42", "1_000", "1_000_000", "0xFF", "0Xff", "0xFF_FF", "0x0"}
	for _, src := range ok {
		toks, diags := Lex("test.tl", []byte(src))
		if diags.HasErrors() {
			t.Errorf("Lex(%q): unexpected errors: %v", src, diags.Error())
			continue
		}
		if toks[0].Type != token.INT || toks[0].Literal != src {
			t.Errorf("Lex(%q) = {%v %q}, want INT with raw literal", src, toks[0].Type, toks[0].Literal)
		}
	}

	// Note: "_1" is a valid identifier (leading underscore), not a number.
	bad := []string{"01", "0x", "1__0", "1_", "0xFF_", "0x_FF", "123abc", "0xGG"}
	for _, src := range bad {
		toks, diags := Lex("test.tl", []byte(src))
		if !diags.HasErrors() {
			t.Errorf("Lex(%q): expected an E-LEX error, got none", src)
		}
		if toks[0].Type != token.ILLEGAL {
			t.Errorf("Lex(%q) = %v, want ILLEGAL", src, toks[0].Type)
		}
	}
}

func TestFloatLiterals(t *testing.T) {
	ok := []string{"1.5", "0.5", "1e9", "1E9", "2.5e-3", "2.5e+3", "10.25", "1_0.2_5"}
	for _, src := range ok {
		toks, diags := Lex("test.tl", []byte(src))
		if diags.HasErrors() {
			t.Errorf("Lex(%q): unexpected errors: %v", src, diags.Error())
			continue
		}
		if toks[0].Type != token.FLOAT || toks[0].Literal != src {
			t.Errorf("Lex(%q) = {%v %q}, want FLOAT with raw literal", src, toks[0].Type, toks[0].Literal)
		}
	}

	// "5.toString()" must NOT greedily consume "5." as a float.
	got := lexTypes(t, "5.toString()")
	want := []token.TokenType{token.INT, token.DOT, token.IDENT, token.LPAREN, token.RPAREN}
	if !typesEqual(got, want) {
		t.Errorf("Lex(5.toString()) = %v, want %v", got, want)
	}
	toks, _ := Lex("test.tl", []byte("5.toString()"))
	if toks[0].Literal != "5" {
		t.Errorf("Lex(5.toString()) first literal = %q, want \"5\"", toks[0].Literal)
	}

	// "1e" has no exponent digits -> malformed.
	toks, diags := Lex("test.tl", []byte("1e"))
	if !diags.HasErrors() || toks[0].Type != token.ILLEGAL {
		t.Errorf("Lex(1e): want ILLEGAL + error, got %v errs=%v", toks[0].Type, diags.HasErrors())
	}
}

func TestStringLiterals(t *testing.T) {
	cases := []struct {
		src  string
		want string // decoded bytes
	}{
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`""`, ""},
		{`"a\nb"`, "a\nb"},
		{`"a\tb"`, "a\tb"},
		{`"a\rb"`, "a\rb"},
		{`"a\0b"`, "a\x00b"},
		{`"a\\b"`, `a\b`},
		{`"she said \"hi\""`, `she said "hi"`},
		{`'it\'s'`, "it's"},
		{`"\u0041"`, "A"},
		{`"\u00e9"`, "\u00e9"},        // é, two UTF-8 bytes
		{`"\u{1F600}"`, "\U0001F600"}, // emoji via \u{...}
		{`"\u{41}"`, "A"},
		{`"\uD83D\uDE00"`, "\U0001F600"}, // surrogate pair -> one code point
		{`"a'b"`, "a'b"},                 // other quote is literal inside
		{`'a"b'`, `a"b`},
	}
	for _, tc := range cases {
		toks, diags := Lex("test.tl", []byte(tc.src))
		if diags.HasErrors() {
			t.Errorf("Lex(%q): unexpected errors: %v", tc.src, diags.Error())
			continue
		}
		if toks[0].Type != token.STRING {
			t.Errorf("Lex(%q) = %v, want STRING", tc.src, toks[0].Type)
			continue
		}
		if toks[0].Literal != tc.want {
			t.Errorf("Lex(%q) literal = %q, want %q", tc.src, toks[0].Literal, tc.want)
		}
	}
}

func TestStringErrors(t *testing.T) {
	bad := []string{
		`"unterminated`,
		`'unterminated`,
		"\"line\nbreak\"", // raw newline in string
		`"\x41"`,          // invalid escape
		`"\q"`,            // invalid escape
		`"\u"`,            // not enough hex
		`"\u12"`,          // not four hex
		`"\u{}"`,          // empty braces
		`"\u{XYZ}"`,       // non-hex
		`"\u{110000}"`,    // out of range
		`"\uD83D"`,        // unpaired high surrogate
		`"\uDE00"`,        // unpaired low surrogate
		`"\uD83Dx"`,       // high surrogate not followed by low
	}
	for _, src := range bad {
		toks, diags := Lex("test.tl", []byte(src))
		if !diags.HasErrors() {
			t.Errorf("Lex(%q): expected an E-LEX error, got none", src)
		}
		if toks[0].Type != token.ILLEGAL {
			t.Errorf("Lex(%q) = %v, want ILLEGAL", src, toks[0].Type)
		}
	}
}

func TestComments(t *testing.T) {
	// Comments produce no tokens.
	got := lexTypes(t, "a // comment\nb /* block */ c")
	want := []token.TokenType{token.IDENT, token.IDENT, token.IDENT}
	if !typesEqual(got, want) {
		t.Errorf("comment handling: got %v, want %v", got, want)
	}

	// A line comment at EOF with no trailing newline.
	if got := lexTypes(t, "x // tail"); !typesEqual(got, []token.TokenType{token.IDENT}) {
		t.Errorf("line comment at EOF: got %v", got)
	}

	// Multi-line block comment keeps line tracking correct.
	toks, _ := Lex("test.tl", []byte("a /* one\ntwo\n */ b"))
	// a is on line 1, b after the block comment is on line 3.
	if toks[0].Pos.Line != 1 {
		t.Errorf("a line = %d, want 1", toks[0].Pos.Line)
	}
	if toks[1].Pos.Line != 3 {
		t.Errorf("b line = %d, want 3", toks[1].Pos.Line)
	}

	// Unterminated block comment is an E-LEX error.
	toks, diags := Lex("test.tl", []byte("/* never ends"))
	if !diags.HasErrors() || toks[0].Type != token.ILLEGAL {
		t.Errorf("unterminated block comment: want ILLEGAL + error, got %v errs=%v", toks[0].Type, diags.HasErrors())
	}
	if diags.Items[0].Code != "E-LEX" {
		t.Errorf("diagnostic code = %q, want E-LEX", diags.Items[0].Code)
	}
}

func TestStrictEqualityHint(t *testing.T) {
	for _, src := range []string{"===", "!=="} {
		toks, diags := Lex("test.tl", []byte(src))
		if toks[0].Type != token.ILLEGAL {
			t.Errorf("Lex(%q) = %v, want ILLEGAL", src, toks[0].Type)
		}
		if !diags.HasErrors() {
			t.Errorf("Lex(%q): expected an E-LEX error", src)
		}
		// Span covers all three bytes, then EOF follows cleanly.
		if toks[0].End.Offset != 3 {
			t.Errorf("Lex(%q) end offset = %d, want 3", src, toks[0].End.Offset)
		}
		if len(toks) != 2 || toks[1].Type != token.EOF {
			t.Errorf("Lex(%q): expected ILLEGAL then EOF, got %v", src, toks)
		}
	}

	// "==" alone is a valid EQ.
	if got := lexTypes(t, "=="); !typesEqual(got, []token.TokenType{token.EQ}) {
		t.Errorf("Lex(==) = %v, want [==]", got)
	}
}

func TestPositionTracking(t *testing.T) {
	src := "let x =\n  42;"
	toks, _ := Lex("test.tl", []byte(src))

	// let @ 1:1 offset 0..3
	checkPos(t, toks[0], "let", 1, 1, 0, 1, 4, 3)
	// x @ 1:5
	checkPos(t, toks[1], "x", 1, 5, 4, 1, 6, 5)
	// = @ 1:7
	if toks[2].Pos.Line != 1 || toks[2].Pos.Column != 7 {
		t.Errorf("= pos = %v, want 1:7", toks[2].Pos)
	}
	// 42 @ 2:3 (column counted from the line start in bytes)
	if toks[3].Pos.Line != 2 || toks[3].Pos.Column != 3 {
		t.Errorf("42 pos = %v, want 2:3", toks[3].Pos)
	}
	if toks[3].Pos.Offset != 10 {
		t.Errorf("42 offset = %d, want 10", toks[3].Pos.Offset)
	}

	// EOF position is just past the last byte.
	eof := toks[len(toks)-1]
	if eof.Type != token.EOF {
		t.Fatalf("last token = %v, want EOF", eof.Type)
	}
	if eof.Pos != eof.End {
		t.Errorf("EOF Pos %v != End %v", eof.Pos, eof.End)
	}
	if eof.Pos.Offset != len(src) {
		t.Errorf("EOF offset = %d, want %d", eof.Pos.Offset, len(src))
	}
}

func checkPos(t *testing.T, tk token.Token, lit string, pl, pc, po, el, ec, eo int) {
	t.Helper()
	if tk.Literal != lit {
		t.Errorf("literal = %q, want %q", tk.Literal, lit)
	}
	if tk.Pos.Line != pl || tk.Pos.Column != pc || tk.Pos.Offset != po {
		t.Errorf("%q Pos = %v (off %d), want %d:%d (off %d)", lit, tk.Pos, tk.Pos.Offset, pl, pc, po)
	}
	if tk.End.Line != el || tk.End.Column != ec || tk.End.Offset != eo {
		t.Errorf("%q End = %v (off %d), want %d:%d (off %d)", lit, tk.End, tk.End.Offset, el, ec, eo)
	}
}

func TestMultiByteColumnsAreBytes(t *testing.T) {
	// A two-byte rune in a string makes the next token's column advance by two.
	src := `"é" x`
	toks, diags := Lex("test.tl", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	// "é" is bytes 0..4 (quote, 2 bytes, quote), so x is at column 6 (1-based).
	if toks[1].Literal != "x" {
		t.Fatalf("second token = %q, want x", toks[1].Literal)
	}
	if toks[1].Pos.Column != 6 {
		t.Errorf("x column = %d, want 6 (bytes)", toks[1].Pos.Column)
	}
}

func TestEOFOnEmptyInput(t *testing.T) {
	toks, diags := Lex("test.tl", []byte(""))
	if len(toks) != 1 || toks[0].Type != token.EOF {
		t.Errorf("empty input tokens = %v, want single EOF", toks)
	}
	if diags.HasErrors() {
		t.Errorf("empty input: unexpected errors: %v", diags.Error())
	}
}

// TestNeverPanicsAndEndsWithEOF feeds a batch of malformed and odd inputs and
// asserts the lexer never panics and always terminates with an EOF token.
func TestNeverPanicsAndEndsWithEOF(t *testing.T) {
	inputs := []string{
		"", " ", "\n\n\n", "\x00", "\xff\xfe", "€", "💥",
		"===", "!==", "1__0", "0x", "01", "1e", "\"", "'", "\"\\",
		"\"\\u", "\"\\u{", "\"\\uD83D", "/*", "// no newline",
		"let x = 10;", "0xDEAD_BEEF", "a\rb", "~`#$^", "\"\n\"",
		strings.Repeat("(", 1000), strings.Repeat("\"\\u{1F600}\"", 50),
	}
	for _, src := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Lex(%q) panicked: %v", src, r)
				}
			}()
			toks, _ := Lex("test.tl", []byte(src))
			if len(toks) == 0 {
				t.Errorf("Lex(%q): empty token slice", src)
				return
			}
			if toks[len(toks)-1].Type != token.EOF {
				t.Errorf("Lex(%q): last token = %v, want EOF", src, toks[len(toks)-1].Type)
			}
		}()
	}
}

func TestIllegalCarriesMessageAndSpan(t *testing.T) {
	toks, diags := Lex("test.tl", []byte("$"))
	if toks[0].Type != token.ILLEGAL {
		t.Fatalf("Lex($) = %v, want ILLEGAL", toks[0].Type)
	}
	if toks[0].Literal == "" {
		t.Error("ILLEGAL literal is empty, want a human-readable message")
	}
	if toks[0].Pos.Offset != 0 || toks[0].End.Offset != 1 {
		t.Errorf("ILLEGAL span = %d..%d, want 0..1", toks[0].Pos.Offset, toks[0].End.Offset)
	}
	if !diags.HasErrors() || diags.Items[0].Code != "E-LEX" {
		t.Errorf("expected one E-LEX diagnostic, got %v", diags.Error())
	}
}
