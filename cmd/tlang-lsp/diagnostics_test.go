package main

import (
	"path/filepath"
	"strings"
	"testing"

	"tlang/diag"
	"tlang/module"
)

// brokenDeps is a project whose open document imports three closed modules
// that do not build: one with a parse error, one with an unresolved import,
// and one that is fine itself but imports a module with a parse error.
var brokenDeps = map[string]string{
	"parse.ts":  "export fn p(): void { let = ; }\n",
	"unres.ts":  "import { gone } from \"./missing\";\nexport fn u(): void {}\n",
	"via.ts":    "import { d } from \"./deep\";\nexport fn v(): void {}\n",
	"deep.ts":   "export fn d(): void { let = ; }\n",
	"clean.ts":  "export fn ok(): void {}\n",
	"header.ts": "import { p } from \"./parse\";\nimport { u } from \"./unres\";\nimport { v } from \"./via\";\nimport { ok } from \"./clean\";\n",
}

// messagesOf returns "code message" for each diagnostic, for assertions.
func messagesOf(ds []diag.Diagnostic) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Code + " " + d.Message
	}
	return out
}

func containsMessage(ds []diag.Diagnostic, code, text string) bool {
	for _, d := range ds {
		if d.Code == code && strings.Contains(d.Message, text) {
			return true
		}
	}
	return false
}

// TestBrokenDependencyDiagnostics: closed dependencies that do not build
// neither hide the open document's own diagnostics nor go unreported. Each
// specifier leading to a broken module (directly or through its imports)
// gets one E-IMPORT on the document, naming module IDs only; a clean import
// gets none; and no diagnostic of another file leaks into the document.
func TestBrokenDependencyDiagnostics(t *testing.T) {
	header := brokenDeps["header.ts"]
	root := writeProject(t, brokenDeps)

	check := func(t *testing.T, a analysis, src string) {
		t.Helper()
		for _, d := range a.diags {
			if d.File != "main.ts" {
				t.Errorf("main.ts got a diagnostic of %q: %s", d.File, d.Message)
			}
			if strings.Contains(d.Message, root) {
				t.Errorf("diagnostic leaks a path: %q", d.Message)
			}
		}
		for _, w := range []struct{ spec, msg string }{
			{"\"./parse\"", `imported module "parse.ts" has errors`},
			{"\"./unres\"", `imported module "unres.ts" has errors`},
			{"\"./via\"", `imported module "via.ts" depends on "deep.ts", which has errors`},
		} {
			found := false
			for _, d := range a.diags {
				if d.Code == "E-IMPORT" && d.Message == w.msg {
					found = true
					if d.Pos.Offset != strings.Index(src, w.spec) {
						t.Errorf("%s reported at offset %d, want the specifier at %d", w.msg, d.Pos.Offset, strings.Index(src, w.spec))
					}
				}
			}
			if !found {
				t.Errorf("missing %q in %v", w.msg, messagesOf(a.diags))
			}
		}
		if containsMessage(a.diags, "E-IMPORT", `"clean.ts"`) {
			t.Errorf("a clean import was reported: %v", messagesOf(a.diags))
		}
	}

	t.Run("document builds", func(t *testing.T) {
		// The type error is the document's own checker diagnostic. It is not
		// asserted here because which file the checker names depends on its
		// attribution; TestDocumentDiagnosticsGate covers the gate instead.
		src := header + "fn main(): void { p(); u(); v(); ok(); let x: int64 = \"s\"; }\n"
		a := analyzeIn(t, root, map[string]string{"main.ts": src}, "main.ts")
		check(t, a, src)
	})

	t.Run("document has its own build errors", func(t *testing.T) {
		src := header + "import { n } from \"./nope\";\nfn main(): void { let y = ; }\n"
		a := analyzeIn(t, root, map[string]string{"main.ts": src}, "main.ts")
		check(t, a, src)
		if !containsMessage(a.diags, "E-IMPORT", `cannot resolve import "./nope"`) {
			t.Errorf("missing the document's own unresolved import: %v", messagesOf(a.diags))
		}
		if !containsMessage(a.diags, "E-PARSE", "") {
			t.Errorf("missing the document's own parse error: %v", messagesOf(a.diags))
		}
	})
}

// TestDocumentDiagnosticsGate exercises the checker-diagnostic gate with
// checker diagnostics stamped by hand, so it does not depend on how the
// checker attributes files: a broken dependency does not withhold the
// document's checker diagnostics, the document's own build errors do, and
// another file's checker diagnostics never reach the document.
func TestDocumentDiagnosticsGate(t *testing.T) {
	check := []diag.Diagnostic{
		{File: "main.ts", Severity: diag.Error, Code: "E-NAME", Message: "undefined: mine"},
		{File: "a.ts", Severity: diag.Error, Code: "E-NAME", Message: "undefined: theirs"},
	}
	for _, tc := range []struct {
		name        string
		main        string
		wantChecker bool
	}{
		{"broken dependency only", "import { a } from \"./a\";\nfn main(): void { a(); }\n", true},
		{"own parse error", "import { a } from \"./a\";\nfn main(): void { let = ; }\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeProject(t, map[string]string{
				"main.ts": tc.main,
				"a.ts":    "export fn a(): void { let = ; }\n",
			})
			g, bd := module.BuildWith(filepath.Join(root, "main.ts"), module.BuildOptions{RootDir: root})
			got := documentDiagnostics(g, bd.Items, check)
			if containsMessage(got, "E-NAME", "undefined: mine") != tc.wantChecker {
				t.Errorf("document's checker diagnostic published = %v, want %v: %v",
					!tc.wantChecker, tc.wantChecker, messagesOf(got))
			}
			if containsMessage(got, "E-NAME", "undefined: theirs") {
				t.Errorf("another file's checker diagnostic leaked: %v", messagesOf(got))
			}
			if !containsMessage(got, "E-IMPORT", `imported module "a.ts" has errors`) {
				t.Errorf("missing the broken-import diagnostic: %v", messagesOf(got))
			}
		})
	}
}

// TestAnalyzeModuleOwnBuildErrorsWithholdChecker: in a document without
// imports (a one-module graph, where checker attribution is exact), a parse
// error withholds the checker's diagnostics, as `tlang check` does.
func TestAnalyzeModuleOwnBuildErrorsWithholdChecker(t *testing.T) {
	root := writeProject(t, map[string]string{"main.ts": "fn main(): void {}\n"})
	clean := "fn main(): void { let x = missing; }\n"
	a := analyzeIn(t, root, map[string]string{"main.ts": clean}, "main.ts")
	if !containsMessage(a.diags, "E-NAME", "undefined: missing") {
		t.Fatalf("checker diagnostic not published for a document that builds: %v", messagesOf(a.diags))
	}
	broken := "fn main(): void { let x = missing; let = ; }\n"
	a = analyzeIn(t, root, map[string]string{"main.ts": broken}, "main.ts")
	if !containsMessage(a.diags, "E-PARSE", "") {
		t.Fatalf("missing the parse error: %v", messagesOf(a.diags))
	}
	if containsMessage(a.diags, "E-NAME", "") {
		t.Fatalf("checker diagnostics published despite the document's parse error: %v", messagesOf(a.diags))
	}
}
