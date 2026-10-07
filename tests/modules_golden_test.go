package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"tlang/ast"
	"tlang/checker"
	"tlang/codegen"
	"tlang/module"
)

// The multi-file acceptance harness (FEAT-004 / DESIGN-modules.md §10). It
// mirrors tests/golden_test.go TestGolden/TestReject but drives the WHOLE
// multi-file front end — module.Build + checker.CheckProgram + the merged
// *ast.Program + codegen.Emit — exactly as cmd/tlang's runFrontendGraph does.
//
// Layout: one directory per case under tests/modules/ (golden) and
// tests/modules/reject/ (reject). Each case directory holds a root file
// (modsRootFile, "main.ts") plus any imported .ts files it reaches, and either
// an expected.c.golden (one merged C translation unit) or an expected.err
// substring. The NEW multi-file goldens are generated with
// `go test ./tests -run Golden -update`; the single-file goldens in golden/
// are untouched (AC-28).

// modsRootFile is the entry module every multi-file case is rooted at.
const modsRootFile = "main.ts"

// modsGoldenName is the merged-C golden file inside each golden case directory.
const modsGoldenName = "expected.c.golden"

// modsErrName is the expected-error substring file inside each reject case
// directory.
const modsErrName = "expected.err"

// modsCaseDirs returns the sorted case directories directly under root (each
// an absolute path), skipping the reject/ subtree when root is tests/modules.
func modsCaseDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "reject" {
			continue
		}
		dirs = append(dirs, filepath.Join(root, e.Name()))
	}
	sort.Strings(dirs)
	return dirs
}

// buildMergedC runs the multi-file front end on the case rooted at
// caseDir/modsRootFile and returns the emitted merged C and any build/check
// error. It never fails the test itself (so reject cases can inspect the
// error); a clean front end proceeds to codegen.Emit.
func buildMergedC(caseDir string) ([]byte, error) {
	root := filepath.Join(caseDir, modsRootFile)
	graph, bdiags := module.Build(root)
	if bdiags.HasErrors() {
		return nil, bdiags.Err()
	}
	info, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		return nil, cdiags.Err()
	}
	merged := mergeGraph(graph)
	return codegen.Emit(merged, info)
}

// mergeGraph assembles the synthetic merged program codegen consumes, matching
// cmd/tlang's mergePrograms: the declaration statements of every module in
// graph order, excluding the import/re-export statements pass 0 consumed, with
// merged.File set to the root module's file.
func mergeGraph(graph *module.Graph) *ast.Program {
	merged := &ast.Program{}
	if graph.Root != nil && graph.Root.Prog != nil {
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
	return merged
}

// TestModulesGolden emits every multi-file golden case and compares the merged
// C against its expected.c.golden; with -update it rewrites the goldens. The
// single-file goldens under golden/ are not touched by this test (AC-28).
func TestModulesGolden(t *testing.T) {
	root := "modules"
	for _, dir := range modsCaseDirs(t, root) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			out, err := buildMergedC(dir)
			if err != nil {
				t.Fatalf("front end rejected a golden case: %v", err)
			}
			if len(out) == 0 {
				t.Fatalf("Emit returned no bytes")
			}
			// NFR-4: one translation unit, one runtime include.
			if n := strings.Count(string(out), "#include \"tlang.h\""); n != 1 {
				t.Fatalf("merged C has %d #include \"tlang.h\", want exactly 1", n)
			}
			if !strings.HasPrefix(string(out), "#include \"tlang.h\"\n") {
				t.Fatalf("merged C does not start with the runtime include")
			}
			gpath := filepath.Join(dir, modsGoldenName)
			if *update {
				if werr := os.WriteFile(gpath, out, 0o644); werr != nil {
					t.Fatal(werr)
				}
				return
			}
			want, rerr := os.ReadFile(gpath)
			if rerr != nil {
				t.Fatalf("read golden (run with -update to create it): %v", rerr)
			}
			if !bytes.Equal(out, want) {
				t.Fatalf("output differs from %s\n%s", gpath, diffHint(want, out))
			}
		})
	}
}

// TestModulesReject builds every multi-file reject case and asserts the front
// end rejects it with an error containing the expected.err substring and emits
// no C.
func TestModulesReject(t *testing.T) {
	root := filepath.Join("modules", "reject")
	for _, dir := range modsCaseDirs(t, root) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			out, err := buildMergedC(dir)
			if err == nil {
				t.Fatalf("front end accepted a reject case, want a rejection")
			}
			if len(out) != 0 {
				t.Fatalf("Emit returned %d bytes with error %v, want none", len(out), err)
			}
			epath := filepath.Join(dir, modsErrName)
			wantBytes, rerr := os.ReadFile(epath)
			if rerr != nil {
				t.Fatalf("read %s: %v", modsErrName, rerr)
			}
			want := strings.TrimRight(string(wantBytes), "\n")
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err.Error(), want)
			}
		})
	}
}
