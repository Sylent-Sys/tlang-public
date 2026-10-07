package main

import (
	"testing"

	"tlang/diag"
	"tlang/token"
)

func TestConvertSeverity(t *testing.T) {
	tests := []struct {
		in   diag.Severity
		want int
	}{
		{diag.Error, severityError},
		{diag.Warning, severityWarning},
		{diag.Note, severityInformation},
	}
	for _, tt := range tests {
		if got := convertSeverity(tt.in); got != tt.want {
			t.Errorf("convertSeverity(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestPosToLSPRangeBaseConversion(t *testing.T) {
	src := []byte("let foo = 1;")
	// "foo" starts at column 5 (1-based), offset 4.
	pos := token.Position{Line: 1, Column: 5, Offset: 4}
	r := posToLSPRange(src, pos)
	if r.Start.Line != 0 || r.Start.Character != 4 {
		t.Fatalf("start = %+v, want {0,4}", r.Start)
	}
	// Widened over the identifier "foo" (3 chars).
	if r.End.Line != 0 || r.End.Character != 7 {
		t.Fatalf("end = %+v, want {0,7}", r.End)
	}
}

func TestPosToLSPRangeInvalidPosition(t *testing.T) {
	src := []byte("whatever")
	r := posToLSPRange(src, token.Position{}) // Line == 0
	want := Range{}
	if r != want {
		t.Fatalf("invalid-position range = %+v, want (0,0)-(0,0)", r)
	}
}

func TestPosToLSPRangeZeroWidthOnPunctuation(t *testing.T) {
	src := []byte("a + b")
	// The "+" at column 3, offset 2 is not an identifier char: zero-width.
	pos := token.Position{Line: 1, Column: 3, Offset: 2}
	r := posToLSPRange(src, pos)
	if r.Start != r.End {
		t.Fatalf("expected zero-width range, got %+v", r)
	}
	if r.Start.Character != 2 {
		t.Fatalf("start character = %d, want 2", r.Start.Character)
	}
}

func TestLspToOffsetClamps(t *testing.T) {
	src := []byte("ab\ncd")
	// Line 1, char 1 -> 'd' at offset 4.
	if got := lspToOffset(src, 1, 1); got != 4 {
		t.Fatalf("lspToOffset(1,1) = %d, want 4", got)
	}
	// Negative inputs clamp to start.
	if got := lspToOffset(src, -1, -1); got != 0 {
		t.Fatalf("lspToOffset(-1,-1) = %d, want 0", got)
	}
	// Overlong character clamps to end of line, not past the newline.
	if got := lspToOffset(src, 0, 99); got != 2 {
		t.Fatalf("lspToOffset(0,99) = %d, want 2", got)
	}
}
