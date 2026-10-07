package main

import (
	"strings"
	"testing"
)

func TestAnalyzeParseError(t *testing.T) {
	a := analyze("file:///app.tlang", "let x = ;")
	if len(a.diags) == 0 {
		t.Fatal("parse-error source yielded no diagnostics")
	}
	if !hasDiagCode(a, "E-PARSE") {
		t.Fatalf("want an E-PARSE diagnostic, got %v", diagCodes(a))
	}
}

func TestAnalyzeTypeError(t *testing.T) {
	// A reference to an undefined name is a checker (E-NAME) diagnostic; the
	// source parses cleanly, so this proves the checker list surfaces.
	a := analyze("file:///app.tlang", "fn main(): void { let x = missing; }")
	if len(a.diags) == 0 {
		t.Fatal("type-error source yielded no diagnostics")
	}
	if !hasDiagCode(a, "E-NAME") {
		t.Fatalf("want an E-NAME diagnostic, got %v", diagCodes(a))
	}
}

func TestAnalyzeNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("analyze panicked: %v", r)
		}
	}()
	for _, src := range []string{"", "@@@", "fn (", "let let let", "interface {"} {
		analyze("file:///x.tlang", src)
	}
}

func TestFileNameWindowsURI(t *testing.T) {
	if got := fileName("file:///c%3A/dir/app.tlang"); got != "app.tlang" {
		t.Fatalf("fileName = %q, want app.tlang", got)
	}
}

func hasDiagCode(a analysis, code string) bool {
	for _, d := range a.diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func diagCodes(a analysis) string {
	var b strings.Builder
	for i, d := range a.diags {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Code)
	}
	return b.String()
}
