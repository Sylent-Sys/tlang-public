package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tlang/ast"
	"tlang/diag"
	"tlang/module"
	"tlang/token"
	"tlang/types"
)

// program_files_test.go pins the file attribution of CheckProgram's
// diagnostics: a token.Position names no file, so every diagnostic must carry
// the file (module ID) of the module its position lies in, not the first
// module's. Each case puts the offending declaration in a module that is NOT
// first in the dependency-first module order (or splits an error and its note
// across modules), so a list-default attribution would fail it.

// checkProgramList is checkProgram returning the diagnostic list itself.
func checkProgramList(t *testing.T, files map[string]string, root string) *diag.List {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, filepath.FromSlash(root)))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build %s: unexpected errors: %s", root, bdiags.Error())
	}
	_, diags := CheckProgram(graph.Modules)
	return diags
}

// fileAttribution lists the sorted diagnostics as "file:line:col: severity
// code", the part of each diagnostic this test is about.
func fileAttribution(d *diag.List) []string {
	var out []string
	for _, item := range d.Sorted() {
		out = append(out, fmt.Sprintf("%s:%s: %s %s", item.File, item.Pos, item.Severity, item.Code))
	}
	return out
}

func TestCheckProgramDiagnosticFiles(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			// The reported repro: an error in the root, which is last in
			// module order.
			name: "body error in the root",
			files: map[string]string{
				"a.ts":    "export fn a(): void {}\n",
				"main.ts": "import { a } from \"./a\";\nfn main(): void { let x = missing; a(); }\n",
			},
			want: []string{"main.ts:2:27: error E-NAME"},
		},
		{
			name: "body error in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export fn pong(): int64 {\n    return \"s\";\n}\n",
				"main.ts": "import { ping } from \"./a\";\nimport { pong } from \"./b\";\nfn main(): void { ping(); let n = pong(); }\n",
			},
			want: []string{"b.ts:2:12: error E-TYPE"},
		},
		{
			name: "warning in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export interface Opt {\n    v?: int64 | null;\n}\n",
				"main.ts": "import { ping } from \"./a\";\nimport { Opt } from \"./b\";\nfn main(): void { ping(); }\n",
			},
			want: []string{"b.ts:2:9: warning W-OPTIONAL"},
		},
		{
			name: "pass-0 second default export in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export default fn one(): void {}\nexport default fn two(): void {}\n",
				"main.ts": "import { ping } from \"./a\";\nimport d from \"./b\";\nfn main(): void { ping(); }\n",
			},
			want: []string{"b.ts:1:1: note E-IMPORT", "b.ts:2:1: error E-IMPORT"},
		},
		{
			name: "collect-time redeclaration in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export fn dup(): void {}\nfn dup(): void {}\n",
				"main.ts": "import { ping } from \"./a\";\nimport { dup } from \"./b\";\nfn main(): void { ping(); }\n",
			},
			want: []string{"b.ts:1:11: note E-NAME", "b.ts:2:4: error E-NAME"},
		},
		{
			// The re-export error is at the re-exporting module's specifier;
			// the import that reads the missing export is in the root.
			name: "re-export of a non-exported name",
			files: map[string]string{
				"a.ts":    "fn hidden(): void {}\n",
				"b.ts":    "export { hidden } from \"./a\";\n",
				"main.ts": "import { hidden } from \"./b\";\nfn main(): void {}\n",
			},
			want: []string{"b.ts:1:10: error E-IMPORT", "main.ts:1:10: error E-IMPORT"},
		},
		{
			// The second import collides with the first, whose declaration
			// lies in a.ts: the note names a.ts, the error main.ts.
			name: "import collision noted at another module's declaration",
			files: map[string]string{
				"a.ts":    "export fn x(): void {}\n",
				"b.ts":    "\nexport fn x(): void {}\n",
				"main.ts": "import { x } from \"./a\";\nimport { x } from \"./b\";\nfn main(): void { x(); }\n",
			},
			want: []string{"a.ts:1:11: note E-NAME", "main.ts:2:10: error E-NAME"},
		},
		{
			name: "Context method redeclared across modules",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\nfn (ctx: Context) greet(): void {}\n",
				"main.ts": "import { ping } from \"./a\";\nfn (ctx: Context) greet(): void {}\nfn main(): void { ping(); }\n",
			},
			want: []string{"a.ts:2:19: note E-NAME", "main.ts:2:19: error E-NAME"},
		},
		{
			name: "method on an imported interface collides with its field",
			files: map[string]string{
				"a.ts":    "export interface User {\n    name: string;\n}\n",
				"main.ts": "import { User } from \"./a\";\nfn (u: User) name(): string { return \"x\"; }\nfn main(): void {}\n",
			},
			want: []string{"a.ts:2:5: note E-NAME", "main.ts:2:14: error E-NAME"},
		},
		{
			name: "required-field cycle across modules",
			files: map[string]string{
				"a.ts":    "import { B } from \"./b\";\nexport interface A { b: B; }\n",
				"b.ts":    "import { A } from \"./a\";\n\nexport interface B { a: A; }\n",
				"main.ts": "import { A } from \"./a\";\nfn main(): void {}\n",
			},
			want: []string{"a.ts:2:18: error E-INIT", "b.ts:3:18: note E-INIT"},
		},
		{
			name: "global-init cycle across modules",
			files: map[string]string{
				"a.ts":    "import { b } from \"./b\";\nexport let a: int64 = b;\n",
				"b.ts":    "import { a } from \"./a\";\n\nexport let b: int64 = a;\n",
				"main.ts": "import { a } from \"./a\";\nfn main(): void {}\n",
			},
			want: []string{"a.ts:2:12: error E-INIT", "b.ts:3:12: note E-INIT"},
		},
		{
			name: "escape in a later dependency's global initializer",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "interface Box {\n    v: int64;\n}\nexport let g: Box = new Box();\n",
				"main.ts": "import { ping } from \"./a\";\nimport { g } from \"./b\";\nfn main(): void { ping(); }\n",
			},
			want: []string{"b.ts:4:21: error E-ESCAPE"},
		},
		{
			name: "transaction rule in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export fn work(): void {\n    db.transaction((tx: Transaction) => {\n        return;\n    });\n}\n",
				"main.ts": "import { ping } from \"./a\";\nimport { work } from \"./b\";\nfn main(): void { ping(); work(); }\n",
			},
			want: []string{"b.ts:3:9: error E-TX"},
		},
		{
			// A generic interface declared in a.ts and first instantiated in
			// main.ts: the Subst-time failure is attributed to the use site.
			name: "generic instantiation error at the instantiating module",
			files: map[string]string{
				"a.ts":    "export interface Tree<T> { kids: Tree<T[]>[]; }\n",
				"main.ts": "import { Tree } from \"./a\";\nfn main(): void { let t: Tree<int64> = new Tree<int64>(); }\n",
			},
			want: []string{"main.ts:2:26: error E-GENERIC"},
		},
		{
			// A generic function of g.ts instantiated from main.ts: the
			// closure walks the origin's body, so the failing call inside it
			// is reported in g.ts.
			name: "generic body error in the origin's module",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"g.ts":    "export fn f<T>(x: T): void { f<T[]>([x]); }\n",
				"main.ts": "import { ping } from \"./a\";\nimport { f } from \"./g\";\nfn main(): void { ping(); f<int64>(1); }\n",
			},
			want: []string{"g.ts:1:30: error E-GENERIC"},
		},
		{
			name: "entry conflict across modules",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\nfn main(): void {}\n",
				"main.ts": "import { ping } from \"./a\";\nfn main(): void { ping(); }\n",
			},
			want: []string{"a.ts:2:4: error E-ENTRY", "main.ts:2:4: note E-ENTRY"},
		},
		{
			name: "wrong entry signature in a later dependency",
			files: map[string]string{
				"a.ts":    "export fn ping(): void {}\n",
				"b.ts":    "export fn pong(): void {}\nfn main(n: int64): void {}\n",
				"main.ts": "import { ping } from \"./a\";\nimport { pong } from \"./b\";\nfn start(): void { ping(); pong(); }\n",
			},
			want: []string{"b.ts:2:4: error E-ENTRY"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := checkProgramList(t, tc.files, "main.ts")
			got := fileAttribution(d)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("diagnostics:\n got  %q\n want %q\n(%s)", got, tc.want, d.Error())
			}
		})
	}
}

// TestCNameCollisionAcrossModules drives the C-name collision pass directly
// with two same-named functions of different modules (a cross-module clash
// that correct mangling never produces from source, so it is built by hand).
// The error is at the declaration of the later module, both positions are
// file-qualified, and a declaration-name error recorded in a.ts at b.ts's
// line:col does not suppress b.ts's declaration.
func TestCNameCollisionAcrossModules(t *testing.T) {
	modA := &module.Module{ID: "a.ts", Prog: &ast.Program{File: "a.ts"}}
	modB := &module.Module{ID: "b.ts", Prog: &ast.Program{File: "b.ts"}}
	c := &checker{
		info:       types.NewInfo(),
		diags:      diag.NewList("a.ts"),
		file:       "a.ts",
		declFiles:  map[types.Object]string{},
		errDeclPos: map[filePos]bool{},
		modules:    []*moduleCtx{{mod: modA}, {mod: modB}},
	}
	sig := &types.Signature{Result: types.Typ[types.Void]}
	// b.ts's declaration comes first in the list and has the smaller
	// line:col, so a position-only order would report at a.ts's.
	fb := &types.Func{Name: "dup", Pos: token.Position{Line: 2, Column: 4}, Sig: sig}
	fa := &types.Func{Name: "dup", Pos: token.Position{Line: 5, Column: 4}, Sig: sig}
	c.declFiles[fa] = "a.ts"
	c.declFiles[fb] = "b.ts"
	c.errDeclPos[filePos{"a.ts", fb.Pos}] = true
	c.info.Funcs = []*types.Func{fb, fa}

	c.checkCNameCollisions()

	want := "b.ts:2:4: error: C name collision: tl_f_dup (function dup at a.ts:5:4 and function dup at b.ts:2:4)"
	if got := c.diags.Error(); got != want {
		t.Fatalf("diagnostics:\n got  %s\n want %s", got, want)
	}
}
