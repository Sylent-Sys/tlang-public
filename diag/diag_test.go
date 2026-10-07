package diag

import (
	"errors"
	"testing"

	"tlang/token"
)

func pos(line, col int) token.Position { return token.Position{Line: line, Column: col} }

func TestDiagnosticError(t *testing.T) {
	cases := []struct {
		d    Diagnostic
		want string
	}{
		{Diagnostic{File: "app.ts", Pos: pos(3, 5), Severity: Error, Code: CodeType, Message: "bad"}, "app.ts:3:5: error: bad"},
		{Diagnostic{File: "app.ts", Pos: pos(1, 1), Severity: Warning, Message: "w"}, "app.ts:1:1: warning: w"},
		{Diagnostic{Pos: pos(2, 7), Severity: Note, Message: "n"}, "2:7: note: n"},
		{Diagnostic{File: "app.ts", Severity: Error, Message: "no entry"}, "app.ts: error: no entry"},
		{Diagnostic{Severity: Error, Message: "bare"}, "error: bare"},
	}
	for _, c := range cases {
		if got := c.d.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

func TestListSortedAndError(t *testing.T) {
	l := NewList("b.ts")
	l.Errorf(pos(5, 2), CodeType, "third %d", 3)
	l.Errorf(pos(1, 9), CodeParse, "second")
	l.Warnf(pos(1, 1), "W-TEST", "first")
	l.Errorf(pos(5, 2), CodeType, "fourth (same position, inserted later)")
	l.Add(Diagnostic{File: "a.ts", Pos: pos(9, 9), Severity: Error, Code: CodeName, Message: "other file"})

	got := l.Sorted()
	wantMsgs := []string{"other file", "first", "second", "third 3", "fourth (same position, inserted later)"}
	for i, d := range got {
		if d.Message != wantMsgs[i] {
			t.Fatalf("Sorted()[%d] = %q, want %q", i, d.Message, wantMsgs[i])
		}
	}
	if l.Items[0].Message != "third 3" {
		t.Error("Sorted must not reorder Items")
	}
	if l.Items[0].File != "b.ts" || l.Items[0].Code != CodeType {
		t.Errorf("Errorf did not stamp file/code: %+v", l.Items[0])
	}
	want := "a.ts:9:9: error: other file\n" +
		"b.ts:1:1: warning: first\n" +
		"b.ts:1:9: error: second\n" +
		"b.ts:5:2: error: third 3\n" +
		"b.ts:5:2: error: fourth (same position, inserted later)"
	if l.Error() != want {
		t.Errorf("Error() =\n%s\nwant\n%s", l.Error(), want)
	}
	if l.Len() != 5 || l.ErrorCount() != 4 || !l.HasErrors() {
		t.Errorf("Len=%d ErrorCount=%d HasErrors=%v", l.Len(), l.ErrorCount(), l.HasErrors())
	}
	var err error = l
	var target *List
	if !errors.As(l.Err(), &target) || err.Error() != want {
		t.Error("Err() should return the list")
	}
}

func TestHasErrorsWarningsOnly(t *testing.T) {
	var l List
	if l.HasErrors() || l.Err() != nil || l.Error() != "no errors" {
		t.Error("empty list")
	}
	l.Warnf(pos(1, 1), "W-X", "w")
	l.Notef(pos(1, 1), "W-X", "n")
	if l.HasErrors() || l.Err() != nil {
		t.Error("warnings and notes are not errors")
	}
}

func TestRender(t *testing.T) {
	src := []byte("let a = 1;\r\n\tlet é = foo;\nlast\n")
	cases := []struct {
		d    Diagnostic
		want string
	}{
		{
			Diagnostic{File: "x.ts", Pos: pos(1, 5), Severity: Error, Message: "m"},
			"x.ts:1:5: error: m\nlet a = 1;\n    ^\n",
		},
		{
			// Column counts bytes: tab (1) + "let " (4) + "é" (2) + " = " (3) = 10 bytes before "foo".
			Diagnostic{File: "x.ts", Pos: pos(2, 11), Severity: Error, Message: "undefined: foo"},
			"x.ts:2:11: error: undefined: foo\n\tlet é = foo;\n\t        ^\n",
		},
		{
			// EOF position: the empty line after the final newline.
			Diagnostic{File: "x.ts", Pos: pos(4, 1), Severity: Error, Message: "eof"},
			"x.ts:4:1: error: eof\n\n^\n",
		},
		{
			Diagnostic{File: "x.ts", Pos: pos(9, 1), Severity: Error, Message: "out of range"},
			"x.ts:9:1: error: out of range\n",
		},
		{
			Diagnostic{File: "x.ts", Severity: Error, Message: "no pos"},
			"x.ts: error: no pos\n",
		},
	}
	for _, c := range cases {
		if got := c.d.Render(src); got != c.want {
			t.Errorf("Render =\n%q\nwant\n%q", got, c.want)
		}
	}
	if got := (Diagnostic{Pos: pos(1, 1), Message: "m"}).Render(nil); got != "1:1: error: m\n" {
		t.Errorf("Render(nil) = %q", got)
	}

	l := NewList("x.ts")
	l.Errorf(pos(3, 2), CodeParse, "second")
	l.Errorf(pos(1, 1), CodeParse, "first")
	l.Add(Diagnostic{File: "y.ts", Pos: pos(1, 1), Severity: Error, Message: "elsewhere"})
	want := "x.ts:1:1: error: first\nlet a = 1;\n^\n" +
		"x.ts:3:2: error: second\nlast\n ^\n" +
		"y.ts:1:1: error: elsewhere\n"
	if got := l.Render(src); got != want {
		t.Errorf("List.Render =\n%q\nwant\n%q", got, want)
	}
}
