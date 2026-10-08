package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tlang/ast"
	"tlang/checker"
	"tlang/module"
)

// buildGraphC writes files to a temp dir, builds the module graph rooted at
// root, type-checks it whole-program, merges the declaration statements into
// one synthetic program (as cmd/tlang does), and emits the C. It fails the
// test on any build/check/emit error.
func buildGraphC(t *testing.T, files map[string]string, root string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, filepath.FromSlash(root)))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build: %s", bdiags.Error())
	}
	info, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		t.Fatalf("CheckProgram: %s", cdiags.Error())
	}
	merged := &ast.Program{}
	if graph.Root != nil {
		merged.File = graph.Root.Prog.File
		merged.EOF = graph.Root.Prog.EOF
	}
	for _, m := range graph.Modules {
		for _, s := range m.Prog.Statements {
			switch s.(type) {
			case *ast.ImportDecl, *ast.ReExportDecl:
			default:
				merged.Statements = append(merged.Statements, s)
			}
		}
	}
	out, err := Emit(merged, info)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return string(out)
}

// AC-28: a single-file program compiled through the multi-file front end
// (module.Build + CheckProgram + merge + Emit) produces byte-identical C to
// the single-file path (Check + Emit). Every tag is "" for one import-free
// module, so the merged program is the same tree with the same empty-tag
// names.
func TestModulesSingleFileByteIdentical(t *testing.T) {
	src := `
interface User { id: int64; name: string; }
let count: int64 = 0;
fn greet(u: User): string { return u.name; }
fn main(): void {
	let u = new User();
	console.info(greet(u));
}
`
	single := mustEmit(t, src)
	graphC := buildGraphC(t, map[string]string{"main.ts": "import { console } from \"tlang/system\";\n" + src}, "main.ts")
	if single != graphC {
		t.Fatalf("single-file C differs between Check and CheckProgram paths:\n--- Check ---\n%s\n--- CheckProgram ---\n%s", single, graphC)
	}
}

// A two-module program emits one translation unit in which the imported
// function is called by its module-tagged C name (qualified-access lowering),
// and the cross-module call compiles to a direct call.
func TestModulesCrossModuleCallEmits(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 7; }`,
		"main.ts": `
import { helper } from "./util";
fn main(): void { let x: int64 = helper(); }
`,
	}
	c := buildGraphC(t, files, "main.ts")
	// The imported function keeps its module tag in its C name, so the call
	// resolves to the tagged symbol, not a bare tl_f_helper.
	if !strings.Contains(c, "tl_f_util_") {
		t.Fatalf("want a module-tagged helper name (tl_f_util_...), got:\n%s", c)
	}
}

// A namespace import lowers m.foo() to a direct tagged call with no trace of
// the namespace receiver.
func TestModulesNamespaceCallEmits(t *testing.T) {
	files := map[string]string{
		"models.ts": `export fn make(): int64 { return 1; }`,
		"main.ts": `
import * as m from "./models";
fn main(): void { let x: int64 = m.make(); }
`,
	}
	c := buildGraphC(t, files, "main.ts")
	if !strings.Contains(c, "tl_f_models_") {
		t.Fatalf("want a module-tagged make name (tl_f_models_...), got:\n%s", c)
	}
}
