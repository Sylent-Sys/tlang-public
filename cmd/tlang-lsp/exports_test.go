package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tlang/checker"
	"tlang/module"
)

// withinTime fails the test if f does not return promptly (a resolution loop
// that does not terminate would otherwise hang until the test timeout).
func withinTime(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not terminate", what)
	}
}

// TestReExportCycleTerminates: two modules re-exporting a name from each
// other (a re-export cycle, which the checker rejects) leave the name
// unresolved for definition, hover and completion, which all return.
func TestReExportCycleTerminates(t *testing.T) {
	root := writeProject(t, map[string]string{
		"a.ts": "export { x } from \"./b\";\n",
		"b.ts": "export { x } from \"./a\";\n",
	})
	src := "import { x } from \"./a\";\nimport * as m from \"./b\";\nfn f(): void { m.x(); }\n"
	a := analyzeIn(t, root, map[string]string{"main.ts": src}, "main.ts")
	re := analyzeIn(t, root, nil, "a.ts")

	withinTime(t, "resolution through a re-export cycle", func() {
		for off := 0; off <= len(src); off++ {
			// Only a file-start target (specifier, namespace) may land in the
			// cyclic modules; a name there has no declaration.
			if d, ok := definitionIn(a.mc, a.info, a.prog, a.src, off); ok && d.pos.IsValid() && d.mod.ID != "main.ts" {
				t.Errorf("offset %d: definition in %s through a re-export cycle", off, d.mod.ID)
			}
			hoverIn(a.mc, a.info, a.prog, a.src, off)
			completeIn(a.mc, a.info, a.prog, a.src, off)
		}
		if _, ok := definitionIn(re.mc, re.info, re.prog, re.src, strings.Index(fixtureText(t, root, "a.ts"), "x")); ok {
			t.Error("definition of a re-export in a cycle should be null")
		}
		if items := a.mc.exportItems(a.mc.target(a.mc.mod, "./a")); len(items) != 0 {
			t.Errorf("exports of a cyclic re-export = %v, want none", items)
		}
	})
}

// fixtureText reads a project file.
func fixtureText(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := (&overlayFS{}).ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// directAndReExport is a module a.ts that exports X twice: a re-export of
// b.ts's Y as X, and its own fn X. The checker rejects the second export
// (E-IMPORT "duplicate export"), but still binds the rest of the program, and
// the two rules bind X differently: importing X from a.ts (named or through a
// namespace) takes the re-export, while a chain through a.ts (c.ts's
// `export { X as Z } from "./a"`) takes the direct declaration.
var directAndReExport = map[string]string{
	"a.ts": "export { Y as X } from \"./b\";\nexport fn X(): int64 {\n    return 1;\n}\n",
	"b.ts": "export fn Y(): string {\n    return \"y\";\n}\n",
	"c.ts": "export { X as Z } from \"./a\";\n",
	"main.ts": "import { X } from \"./a\";\nimport { Z } from \"./c\";\nimport * as m from \"./a\";\n" +
		"fn main(): void {\n    let v = X();\n    let w = Z();\n    let u = m.X();\n}\n",
}

// TestExportPrecedenceMatchesChecker: a name exported both directly and by a
// re-export is a checker error, which the LSP publishes on the exporting
// module; in that rejected program, definition and hover still resolve the
// name exactly as the checker binds it. The use sites resolve through the
// checker's own Info, so each import-side answer is compared with the
// checker's binding.
func TestExportPrecedenceMatchesChecker(t *testing.T) {
	root := writeProject(t, directAndReExport)
	g, bd := module.BuildWith(filepath.Join(root, "main.ts"), module.BuildOptions{RootDir: root})
	if bd.Len() > 0 {
		t.Fatalf("fixture does not build clean: %s", bd.Error())
	}
	_, cd := checker.CheckProgram(g.Modules)
	var gotCheck []string
	for _, d := range cd.Sorted() {
		gotCheck = append(gotCheck, fmt.Sprintf("%s:%s: %s %s: %s", d.File, d.Pos, d.Severity, d.Code, d.Message))
	}
	wantCheck := []string{
		"a.ts:1:15: note E-IMPORT: first export of `X` here",
		"a.ts:2:1: error E-IMPORT: duplicate export `X`",
	}
	if !reflect.DeepEqual(gotCheck, wantCheck) {
		t.Fatalf("checker diagnostics = %q, want %q", gotCheck, wantCheck)
	}
	published := false
	for _, d := range analyzeIn(t, root, nil, "a.ts").diags {
		if d.Code == "E-IMPORT" && d.Message == "duplicate export `X`" && d.Pos.Line == 2 && d.Pos.Column == 1 {
			published = true
		}
	}
	if !published {
		t.Error("a.ts does not publish the duplicate-export error")
	}

	src := directAndReExport["main.ts"]
	a := analyzeIn(t, root, nil, "main.ts")
	cases := []struct {
		name     string
		offset   int
		wantMod  string
		wantText string
	}{
		{"import X (checker: the re-export)", offsetOf(t, src, "{ X }", 0, 2), "b.ts", "Y"},
		{"use X()", offsetOf(t, src, "X()", 0, 0), "b.ts", "Y"},
		{"m.X (the export set)", offsetOf(t, src, "m.X()", 0, 2), "b.ts", "Y"},
		{"import Z (chain through a.ts: the direct fn)", offsetOf(t, src, "{ Z }", 0, 2), "a.ts", "X"},
		{"use Z()", offsetOf(t, src, "Z()", 0, 0), "a.ts", "X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, text, ok := defAt(t, a, tc.offset)
			if !ok || id != tc.wantMod || text != tc.wantText {
				t.Fatalf("definition = %s %q %v, want %s %q", id, text, ok, tc.wantMod, tc.wantText)
			}
		})
	}
	if h := hoverIn(a.mc, a.info, a.prog, a.src, offsetOf(t, src, "{ X }", 0, 2)); h == nil || !strings.Contains(h.Contents.Value, "fn(): string") {
		t.Errorf("hover on the imported X = %+v, want the re-export's fn(): string", h)
	}
	got := labels(a.mc.exportItems(a.mc.target(a.mc.mod, "./a")))
	if !strings.Contains(got["X"].Detail, "fn(): string") {
		t.Errorf("completion detail of a.ts's X = %q, want the re-export's fn(): string", got["X"].Detail)
	}

	// From c.ts itself, its re-export follows the chain rule.
	c := analyzeIn(t, root, nil, "c.ts")
	id, text, ok := defAt(t, c, strings.Index(directAndReExport["c.ts"], "X"))
	if !ok || id != "a.ts" || text != "X" {
		t.Fatalf("c.ts re-export definition = %s %q %v, want a.ts X", id, text, ok)
	}
}
