package lexer

import (
	"testing"

	"tlang/token"
)

// FuzzLexer asserts the two invariants that hold for every input: Lex never
// panics, and the returned slice is non-empty and ends with a single EOF
// token (so the parser always has a terminator to stop on).
func FuzzLexer(f *testing.F) {
	seeds := []string{
		"",
		"let x = 10;",
		"const y: int64 = 0xFF_FF;",
		"fn add(a: int64, b: int64): int64 { return a + b; }",
		`"hello\n\tworld\u0041\u{1F600}\uD83D\uDE00"`,
		"a ?? b ?? c",
		"i++; j--; x += 1; y %= 2;",
		"for (const v of xs) {}",
		"===",
		"!==",
		"->",
		"Page<Page<User>>=",
		"5.toString()",
		"/* unterminated",
		"\"unterminated",
		"1__0 0x 01 1e",
		"\x00\xff\xfe€💥",
		"// comment\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		toks, _ := Lex("fuzz.tl", src)
		if len(toks) == 0 {
			t.Fatalf("empty token slice for %q", src)
		}
		if last := toks[len(toks)-1]; last.Type != token.EOF {
			t.Fatalf("last token = %v, want EOF for %q", last.Type, src)
		}
	})
}
