package main

import (
	"strings"
	"testing"
)

// hoverAt analyzes src and returns the hover value at the first occurrence of
// marker (positioned at its first byte).
func hoverAt(t *testing.T, src, marker string) *Hover {
	t.Helper()
	a := analyze("file:///app.tlang", src)
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found in source", marker)
	}
	return hover(a.info, a.prog, a.src, idx)
}

func TestHoverTypedVariable(t *testing.T) {
	src := "fn main(): void {\n\tlet count: int64 = 1;\n\tlet n = count;\n}\n"
	// Hover over the use of count on the "let n = count" line.
	idx := strings.LastIndex(src, "count")
	a := analyze("file:///app.tlang", src)
	h := hover(a.info, a.prog, a.src, idx)
	if h == nil {
		t.Fatal("hover over typed variable returned nil")
	}
	if !strings.Contains(h.Contents.Value, "int64") {
		t.Fatalf("hover value = %q, want to contain int64", h.Contents.Value)
	}
	// Bare type, not the "<kind> name type" form.
	if strings.Contains(h.Contents.Value, "local") {
		t.Fatalf("hover value = %q, should not leak the kind word", h.Contents.Value)
	}
}

func TestHoverPlainFunction(t *testing.T) {
	src := "fn greet(name: string): void {}\nfn main(): void {\n\tgreet(\"x\");\n}\n"
	h := hoverAt(t, src, "greet(\"x\")")
	if h == nil {
		t.Fatal("hover over function returned nil")
	}
	if !strings.Contains(h.Contents.Value, "fn(name: string): void") {
		t.Fatalf("hover value = %q, want the function signature", h.Contents.Value)
	}
}

func TestHoverReceiverMethod(t *testing.T) {
	src := "interface User { id: int64; }\n" +
		"fn (u: User) twice(): int64 { return u.id; }\n"
	h := hoverAt(t, src, "twice")
	if h == nil {
		t.Fatal("hover over receiver method returned nil")
	}
	if !strings.Contains(h.Contents.Value, "fn (u: User)") {
		t.Fatalf("hover value = %q, want a receiver-method signature", h.Contents.Value)
	}
}

func TestHoverTypeName(t *testing.T) {
	src := "interface User { id: int64; }\nfn f(u: User): void {}\n"
	// Hover over the "User" type name in the parameter.
	idx := strings.Index(src, "u: User") + len("u: ")
	a := analyze("file:///app.tlang", src)
	h := hover(a.info, a.prog, a.src, idx)
	if h == nil {
		t.Fatal("hover over type name returned nil")
	}
	if !strings.Contains(h.Contents.Value, "type User") {
		t.Fatalf("hover value = %q, want type User", h.Contents.Value)
	}
}

func TestHoverMemberField(t *testing.T) {
	src := "interface User { id: int64; }\n" +
		"fn f(u: User): int64 { return u.id; }\n"
	idx := strings.Index(src, "u.id") + len("u.")
	a := analyze("file:///app.tlang", src)
	h := hover(a.info, a.prog, a.src, idx)
	if h == nil {
		t.Fatal("hover over member field returned nil")
	}
	if !strings.Contains(h.Contents.Value, "int64") {
		t.Fatalf("hover value = %q, want the field type int64", h.Contents.Value)
	}
}

func TestHoverMemberMethod(t *testing.T) {
	src := "interface User { id: int64; }\n" +
		"fn (u: User) twice(): int64 { return u.id; }\n" +
		"fn f(u: User): int64 { return u.twice(); }\n"
	idx := strings.Index(src, "u.twice") + len("u.")
	a := analyze("file:///app.tlang", src)
	h := hover(a.info, a.prog, a.src, idx)
	if h == nil {
		t.Fatal("hover over member method returned nil")
	}
	if !strings.Contains(h.Contents.Value, "fn (u: User)") {
		t.Fatalf("hover value = %q, want the method signature", h.Contents.Value)
	}
}

func TestHoverWhitespaceNil(t *testing.T) {
	src := "fn main(): void {\n\n}\n"
	// Offset on the blank line between the braces.
	idx := strings.Index(src, "\n\n") + 1
	a := analyze("file:///app.tlang", src)
	if h := hover(a.info, a.prog, a.src, idx); h != nil {
		t.Fatalf("hover over whitespace = %+v, want nil", h)
	}
}
