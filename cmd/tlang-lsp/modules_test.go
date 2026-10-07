package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tlang/checker"
	"tlang/module"
	"tlang/types"
)

// fixture is a small multi-module project: app/main.ts imports from lib/ with
// every import form (default, named, aliased, namespace, a re-export) and uses
// imported @Use/@After functions. api/reexp.ts re-exports through a chain.
var fixture = map[string]string{
	"lib/models.ts": `export interface User {
    id: int64;
    name: string;
}

export fn greet(u: User): string {
    return u.name;
}

export let counter: int64 = 0;

export default fn makeId(): int64 {
    return 1;
}

fn hidden(): int64 {
    return 2;
}
`,
	"lib/near.ts": `export { greet as hello } from "./models";
`,
	"lib/hooks.ts": `export fn audit(ctx: Context): void {}

export fn allow(ctx: Context): bool {
    return true;
}
`,
	"api/reexp.ts": `export { hello } from "../lib/near";
`,
	"app/main.ts": `import makeId, { User, greet as hi, counter } from "../lib/models";
import * as m from "../lib/models";
import { hello } from "../lib/near";
import { audit, allow } from "../lib/hooks";

@Use(allow)
@After(audit)
fn handle(ctx: Context): void {
    let u: m.User = new m.User();
    let s = hi(u);
    let t = m.greet(u);
    let n = counter + m.counter + makeId();
    let w = hello(u);
    let v: User = u;
}
`,
}

// writeProject writes files (slash path -> contents) under a fresh temp dir
// and returns the dir.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// analyzeIn analyzes the document doc (a slash path under root) the way the
// server does, with buffers (slash path -> text) open in the overlay. The
// document's text is its buffer when open, else its file.
func analyzeIn(t *testing.T, root string, buffers map[string]string, doc string) analysis {
	t.Helper()
	fsys := &overlayFS{files: map[string][]byte{}}
	for rel, text := range buffers {
		fsys.files[pathKey(filepath.Join(root, filepath.FromSlash(rel)))] = []byte(text)
	}
	p := filepath.Join(root, filepath.FromSlash(doc))
	text, ok := buffers[doc]
	if !ok {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		text = string(b)
	}
	return analyzeModule(p, root, fsys, text)
}

// offsetOf returns the offset of the n-th (0-based) occurrence of marker in
// src plus delta.
func offsetOf(t *testing.T, src, marker string, n, delta int) int {
	t.Helper()
	from := 0
	for i := 0; ; i++ {
		idx := strings.Index(src[from:], marker)
		if idx < 0 {
			t.Fatalf("occurrence %d of %q not found", n, marker)
		}
		if i == n {
			return from + idx + delta
		}
		from += idx + len(marker)
	}
}

// TestFixtureChecksClean validates the fixture itself: the whole graph builds
// and checks with no diagnostic at all, so the query tests below run on a
// well-formed program.
func TestFixtureChecksClean(t *testing.T) {
	root := writeProject(t, fixture)
	for _, doc := range []string{"app/main.ts", "api/reexp.ts"} {
		g, bd := module.BuildWith(filepath.Join(root, filepath.FromSlash(doc)), module.BuildOptions{RootDir: root})
		if bd.Len() > 0 {
			t.Fatalf("%s: build diagnostics: %s", doc, bd.Error())
		}
		if _, cd := checker.CheckProgram(g.Modules); cd.Len() > 0 {
			t.Fatalf("%s: check diagnostics: %s", doc, cd.Error())
		}
	}
}

// TestAnalyzeModuleResolvesImports: names imported from other files resolve
// to the exporting module's objects, where single-file analysis reports them
// as undefined.
func TestAnalyzeModuleResolvesImports(t *testing.T) {
	root := writeProject(t, fixture)
	src := fixture["app/main.ts"]

	single := analyze("file:///main.ts", src)
	if !hasDiagCode(single, "E-NAME") {
		t.Fatalf("single-file analysis should not see imports, got %v", diagCodes(single))
	}

	a := analyzeIn(t, root, nil, "app/main.ts")
	if a.mc == nil || a.mc.mod.ID != "app/main.ts" {
		t.Fatalf("module context = %+v, want root app/main.ts", a.mc)
	}
	if len(a.diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diagCodes(a))
	}
	c := locate(a.prog, offsetOf(t, src, "hi(u)", 0, 0))
	fn, ok := a.info.ObjectOf(c.id).(*types.Func)
	if !ok || fn.Name != "greet" {
		t.Fatalf("hi resolves to %v, want func greet", a.info.ObjectOf(c.id))
	}
}

// TestAnalyzeModuleOverlayBuffer: an open buffer is analyzed instead of the
// file on disk, both for the document and for the modules it imports.
func TestAnalyzeModuleOverlayBuffer(t *testing.T) {
	root := writeProject(t, map[string]string{
		"main.ts": "fn main(): void {}\n",
	})
	buffers := map[string]string{
		"main.ts":    "import { fresh } from \"./unsaved\";\nfn main(): void { fresh(); }\n",
		"unsaved.ts": "export fn fresh(): void {}\n",
	}
	a := analyzeIn(t, root, buffers, "main.ts")
	if len(a.diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", a.diags)
	}
	if a.mc.target(a.mc.mod, "./unsaved") == nil {
		t.Fatal("overlay-only import not resolved")
	}
}

// TestAnalyzeModuleBuildDiagnosticsPerFile: a document gets its own build
// diagnostics (parse errors, unresolved imports) and not those of the modules
// it imports; and, as in cmd/tlang, checker diagnostics are withheld while the
// graph has build errors.
func TestAnalyzeModuleBuildDiagnosticsPerFile(t *testing.T) {
	root := writeProject(t, map[string]string{
		"main.ts": "import { a } from \"./a\";\nimport { b } from \"./nope\";\nfn main(): void { let x = missing; let y = ; }\n",
		"a.ts":    "export fn a(): void { let z = ; }\n",
	})
	main := analyzeIn(t, root, nil, "main.ts")
	var codes []string
	for _, d := range main.diags {
		if d.File != "main.ts" {
			t.Errorf("main.ts got a diagnostic of %q: %s", d.File, d.Message)
		}
		codes = append(codes, d.Code)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, "E-IMPORT") || !strings.Contains(joined, "E-PARSE") {
		t.Fatalf("main.ts diagnostics = %v, want its E-IMPORT and E-PARSE", codes)
	}
	if strings.Contains(joined, "E-NAME") {
		t.Fatalf("checker diagnostics published despite build errors: %v", codes)
	}

	a := analyzeIn(t, root, nil, "a.ts")
	if len(a.diags) == 0 {
		t.Fatal("a.ts got no diagnostics, want its E-PARSE")
	}
	for _, d := range a.diags {
		if d.File != "a.ts" || d.Code != "E-PARSE" {
			t.Errorf("a.ts got %s %s %q, want only its own E-PARSE", d.File, d.Code, d.Message)
		}
	}
}

// TestAnalyzeModuleCheckerDiagnosticsPerFile: each document gets the checker
// diagnostics of its own module only.
func TestAnalyzeModuleCheckerDiagnosticsPerFile(t *testing.T) {
	root := writeProject(t, map[string]string{
		"main.ts": "import { a } from \"./a\";\nfn main(): void { let x = missing; a(); }\n",
		"a.ts":    "export fn a(): void { let y = alsomissing; }\n",
	})
	main := analyzeIn(t, root, nil, "main.ts")
	if len(main.diags) != 1 || !strings.Contains(main.diags[0].Message, "undefined: missing") {
		t.Fatalf("main.ts diagnostics = %+v, want only its undefined: missing", main.diags)
	}
	a := analyzeIn(t, root, nil, "a.ts")
	if len(a.diags) != 1 || !strings.Contains(a.diags[0].Message, "undefined: alsomissing") {
		t.Fatalf("a.ts diagnostics = %+v, want only its undefined: alsomissing", a.diags)
	}
}

// TestCompleteModuleScope: plain completion offers the document's own names
// and every import binding, and not the names other modules keep to
// themselves (or that are imported only under another name).
func TestCompleteModuleScope(t *testing.T) {
	root := writeProject(t, fixture)
	src := fixture["app/main.ts"]
	a := analyzeIn(t, root, nil, "app/main.ts")
	got := labels(completeIn(a.mc, a.info, a.prog, a.src, offsetOf(t, src, "let s", 0, 0)))

	want := map[string]int{
		"makeId": completionKindFunction, "User": completionKindInterface,
		"hi": completionKindFunction, "counter": completionKindVariable,
		"m": completionKindModule, "hello": completionKindFunction,
		"audit": completionKindFunction, "allow": completionKindFunction,
		"handle": completionKindFunction, "u": completionKindVariable,
		"import": completionKindKeyword,
	}
	for name, kind := range want {
		it, ok := got[name]
		if !ok {
			t.Errorf("completion missing %q", name)
			continue
		}
		if it.Kind != kind {
			t.Errorf("%q kind = %d, want %d", name, it.Kind, kind)
		}
	}
	if !strings.Contains(got["hi"].Detail, "fn(u: User): string") {
		t.Errorf("hi detail = %q, want the signature", got["hi"].Detail)
	}
	for _, name := range []string{"greet", "hidden"} {
		if _, ok := got[name]; ok {
			t.Errorf("completion offers %q, which is not in this module's scope", name)
		}
	}
}

// TestCompleteNamespaceMembers: after "m." the target module's exports are
// listed, in an expression and in a type annotation, while still typing.
func TestCompleteNamespaceMembers(t *testing.T) {
	root := writeProject(t, fixture)
	for _, tc := range []struct{ name, body string }{
		{"value", "    let q = m."},
		{"type", "    let q: m."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(fixture["app/main.ts"], "    let v: User = u;", tc.body, 1)
			a := analyzeIn(t, root, map[string]string{"app/main.ts": src}, "app/main.ts")
			got := labels(completeIn(a.mc, a.info, a.prog, a.src, offsetOf(t, src, tc.body, 0, len(tc.body))))
			for _, name := range []string{"User", "greet", "counter", "makeId"} {
				if _, ok := got[name]; !ok {
					t.Errorf("m. completion missing %q, got %v", name, keys(got))
				}
			}
			if _, ok := got["hidden"]; ok {
				t.Error("m. completion offers the unexported hidden")
			}
			if _, ok := got["let"]; ok {
				t.Error("m. completion fell back to plain completion")
			}
		})
	}
}

// TestCompleteImportList: inside the braces of an import or re-export list,
// the specifier's exports are offered, including for an empty list (which
// does not parse yet).
func TestCompleteImportList(t *testing.T) {
	root := writeProject(t, fixture)
	for _, line := range []string{
		`import { | } from "../lib/models";`,
		`import { User, | } from "../lib/models";`,
		`import makeId, { | } from "../lib/models";`,
		`export { | } from "../lib/near";`,
	} {
		t.Run(line, func(t *testing.T) {
			src := strings.Replace(line, "|", "", 1) + "\nfn f(): void {}\n"
			a := analyzeIn(t, root, map[string]string{"app/doc.ts": src}, "app/doc.ts")
			items := completeIn(a.mc, a.info, a.prog, a.src, strings.Index(line, "|"))
			got := labels(items)
			want := []string{"User", "greet", "counter"}
			if strings.Contains(line, "near") {
				want = []string{"hello"}
			}
			for _, name := range want {
				if _, ok := got[name]; !ok {
					t.Errorf("missing %q, got %v", name, keys(got))
				}
			}
			if _, ok := got["let"]; ok {
				t.Error("import-list completion fell back to plain completion")
			}
		})
	}
}

// TestCompleteDecorators: after "@" the decorators are offered.
func TestCompleteDecorators(t *testing.T) {
	src := "@U\nfn h(ctx: Context): void {}\n"
	a := analyze("untitled:Untitled-1", src)
	got := labels(complete(a.info, a.prog, a.src, 2))
	if _, ok := got["Use"]; !ok {
		t.Errorf("missing Use, got %v", keys(got))
	}
	if _, ok := got["After"]; !ok {
		t.Errorf("missing After, got %v", keys(got))
	}
	if len(got) != len(decorators) {
		t.Errorf("decorator completion = %v, want only decorators", keys(got))
	}
}

// TestCompleteDecoratorsNotInStringOrComment: an "@" inside a string literal
// or a comment is not a decorator.
func TestCompleteDecoratorsNotInStringOrComment(t *testing.T) {
	for _, tc := range []struct {
		src    string
		marker string
		want   bool
	}{
		{"fn f(): void { let e = \"user@ex\"; }\n", "@ex", false},
		{"fn f(): void { let e = 'it\\'s @me'; }\n", "@me", false},
		{"// mail me @home\nfn f(): void {}\n", "@home", false},
		{"/* @Use(g)\n   @Aft */\nfn f(): void {}\n", "@Aft", false},
		{"let s = \"a\\\"b\";\n@Us\nfn h(ctx: Context): void {}\n", "@Us", true},
		{"/* c */ @Af\nfn h(ctx: Context): void {}\n", "@Af", true},
		{"let u = \"x//y\";\nexport @Use(g) @Af\nfn h(ctx: Context): void {}\n", "@Af", true},
	} {
		a := analyze("untitled:Untitled-1", tc.src)
		off := strings.Index(tc.src, tc.marker) + len(tc.marker)
		_, gotUse := labels(complete(a.info, a.prog, a.src, off))["Use"]
		if gotUse != tc.want {
			t.Errorf("%q at %q: decorators offered = %v, want %v", tc.src, tc.marker, gotUse, tc.want)
		}
	}
}

// TestHoverModules covers hover on imported names, namespaces, qualified
// members and types, decorator arguments, and the parts of import
// declarations.
func TestHoverModules(t *testing.T) {
	root := writeProject(t, fixture)
	src := fixture["app/main.ts"]
	a := analyzeIn(t, root, nil, "app/main.ts")
	cases := []struct {
		name   string
		offset int
		want   string
	}{
		{"imported alias use", offsetOf(t, src, "hi(u)", 0, 0), "fn(u: User): string"},
		{"imported via re-export", offsetOf(t, src, "hello(u)", 0, 0), "fn(u: User): string"},
		{"default import use", offsetOf(t, src, "makeId()", 0, 0), "fn(): int64"},
		{"namespace use", offsetOf(t, src, "m.greet", 0, 0), "module m (lib/models.ts)"},
		{"namespace function", offsetOf(t, src, "m.greet", 0, 2), "fn(u: User): string"},
		{"namespace value", offsetOf(t, src, "m.counter", 0, 2), "int64"},
		{"qualified type", offsetOf(t, src, "m.User", 0, 2), "type User"},
		{"@Use guard", offsetOf(t, src, "@Use(allow)", 0, 5), "fn(ctx: Context): bool"},
		{"@After hook", offsetOf(t, src, "@After(audit)", 0, 7), "fn(ctx: Context): void"},
		{"import name", offsetOf(t, src, "greet as hi", 0, 0), "fn(u: User): string"},
		{"import alias", offsetOf(t, src, "greet as hi", 0, 9), "fn(u: User): string"},
		{"import default", offsetOf(t, src, "import makeId", 0, 7), "fn(): int64"},
		{"import type", offsetOf(t, src, "{ User", 0, 2), "type User"},
		{"import namespace", offsetOf(t, src, "* as m", 0, 5), "module m (lib/models.ts)"},
		{"import specifier", offsetOf(t, src, "\"../lib/near\"", 0, 3), "module \"lib/near.ts\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := hoverIn(a.mc, a.info, a.prog, a.src, tc.offset)
			if h == nil {
				t.Fatalf("hover returned nil, want %q", tc.want)
			}
			if !strings.Contains(h.Contents.Value, tc.want) {
				t.Fatalf("hover = %q, want %q", h.Contents.Value, tc.want)
			}
		})
	}
}

// defAt resolves a definition in the analysis and returns the target's module
// ID and the source text the range covers ("" for a start-of-file target).
func defAt(t *testing.T, a analysis, offset int) (id, text string, ok bool) {
	t.Helper()
	d, ok := definitionIn(a.mc, a.info, a.prog, a.src, offset)
	if !ok {
		return "", "", false
	}
	if !d.pos.IsValid() {
		return d.mod.ID, "", true
	}
	b, err := a.mc.fsys.ReadFile(d.mod.AbsPath)
	if err != nil {
		t.Fatalf("read %s: %v", d.mod.AbsPath, err)
	}
	return d.mod.ID, string(b[d.pos.Offset : d.pos.Offset+d.n]), true
}

// TestDefinitionModules covers go-to-definition for local and imported uses,
// every part of an import or re-export declaration (following re-export
// chains), namespaces, qualified members and types, and decorator arguments.
func TestDefinitionModules(t *testing.T) {
	root := writeProject(t, fixture)
	src := fixture["app/main.ts"]
	a := analyzeIn(t, root, nil, "app/main.ts")
	cases := []struct {
		name     string
		offset   int
		wantMod  string
		wantText string // "" means the start of the file
	}{
		{"local use", offsetOf(t, src, "hi(u)", 0, 3), "app/main.ts", "u"},
		{"imported alias use", offsetOf(t, src, "hi(u)", 0, 0), "lib/models.ts", "greet"},
		{"re-exported use", offsetOf(t, src, "hello(u)", 0, 0), "lib/models.ts", "greet"},
		{"default import use", offsetOf(t, src, "makeId()", 0, 0), "lib/models.ts", "makeId"},
		{"imported type use", offsetOf(t, src, "v: User", 0, 3), "lib/models.ts", "User"},
		{"import name", offsetOf(t, src, "greet as hi", 0, 0), "lib/models.ts", "greet"},
		{"import alias", offsetOf(t, src, "greet as hi", 0, 9), "lib/models.ts", "greet"},
		{"import through re-export", offsetOf(t, src, "{ hello }", 0, 2), "lib/models.ts", "greet"},
		{"import default", offsetOf(t, src, "import makeId", 0, 7), "lib/models.ts", "makeId"},
		{"import namespace", offsetOf(t, src, "* as m", 0, 5), "lib/models.ts", ""},
		{"import specifier", offsetOf(t, src, "\"../lib/hooks\"", 0, 4), "lib/hooks.ts", ""},
		{"namespace use", offsetOf(t, src, "m.greet", 0, 0), "lib/models.ts", ""},
		{"namespace function", offsetOf(t, src, "m.greet", 0, 2), "lib/models.ts", "greet"},
		{"namespace value", offsetOf(t, src, "m.counter", 0, 2), "lib/models.ts", "counter"},
		{"qualified type", offsetOf(t, src, "u: m.User", 0, 5), "lib/models.ts", "User"},
		{"qualified type qualifier", offsetOf(t, src, "u: m.User", 0, 3), "lib/models.ts", ""},
		{"@Use guard", offsetOf(t, src, "@Use(allow)", 0, 5), "lib/hooks.ts", "allow"},
		{"@After hook", offsetOf(t, src, "@After(audit)", 0, 7), "lib/hooks.ts", "audit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, text, ok := defAt(t, a, tc.offset)
			if !ok {
				t.Fatalf("no definition, want %s %q", tc.wantMod, tc.wantText)
			}
			if id != tc.wantMod || text != tc.wantText {
				t.Fatalf("definition = %s %q, want %s %q", id, text, tc.wantMod, tc.wantText)
			}
		})
	}

	// A builtin has no declaration in source.
	if _, _, ok := defAt(t, a, offsetOf(t, src, "Context)", 0, 0)); ok {
		t.Error("definition of the builtin Context type should be null")
	}

	// A re-export chain followed from the re-exporting module itself.
	re := analyzeIn(t, root, nil, "api/reexp.ts")
	id, text, ok := defAt(t, re, offsetOf(t, fixture["api/reexp.ts"], "hello", 0, 0))
	if !ok || id != "lib/models.ts" || text != "greet" {
		t.Fatalf("re-export chain definition = %s %q %v, want lib/models.ts greet", id, text, ok)
	}
}

// TestDefinitionSingleFile: the single-file path resolves names within the
// document.
func TestDefinitionSingleFile(t *testing.T) {
	src := "fn greet(name: string): void {}\nfn main(): void {\n\tgreet(\"x\");\n}\n"
	a := analyze("untitled:Untitled-1", src)
	d, ok := definitionIn(nil, a.info, a.prog, a.src, strings.Index(src, "greet(\"x\")"))
	if !ok || d.mod != nil || d.pos.Offset != strings.Index(src, "greet") || d.n != len("greet") {
		t.Fatalf("definition = %+v %v, want the greet declaration in the document", d, ok)
	}
}
