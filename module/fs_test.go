package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tlang/ast"
)

// overlayFS serves files keyed by absolute path before falling back to disk,
// the way the language server serves unsaved editor buffers.
type overlayFS map[string]string

func (o overlayFS) ReadFile(absPath string) ([]byte, error) {
	if s, ok := o[filepath.Clean(absPath)]; ok {
		return []byte(s), nil
	}
	return os.ReadFile(absPath)
}

func (o overlayFS) IsFile(absPath string) bool {
	if _, ok := o[filepath.Clean(absPath)]; ok {
		return true
	}
	return osFS{}.IsFile(absPath)
}

func moduleByID(g *Graph, id string) *Module {
	for _, m := range g.Modules {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// firstFuncName returns the name of the first function declared in m.
func firstFuncName(m *Module) string {
	for _, s := range m.Prog.Statements {
		if fn, ok := s.(*ast.FunctionStatement); ok {
			return fn.Name.Name
		}
	}
	return ""
}

// TestBuildWithZeroOptionsMatchesBuild: the zero BuildOptions reproduces Build
// (module IDs, order, tags and diagnostics).
func TestBuildWithZeroOptionsMatchesBuild(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts":  "import { a } from \"./a\";\nimport { x } from \"./missing\";\nfn main(): void {}\n",
		"a.ts":     "import { b } from \"./lib/b\";\nexport fn a(): void {}\n",
		"lib/b.ts": "export fn b(): void {}\n",
	})
	g1, d1 := Build(filepath.Join(root, "main.ts"))
	g2, d2 := BuildWith(filepath.Join(root, "main.ts"), BuildOptions{})
	if strings.Join(ids(g1), ",") != strings.Join(ids(g2), ",") {
		t.Fatalf("module order differs: %v vs %v", ids(g1), ids(g2))
	}
	for i := range g1.Modules {
		if g1.Modules[i].Tag != g2.Modules[i].Tag || g1.Modules[i].AbsPath != g2.Modules[i].AbsPath {
			t.Errorf("module %d differs: %+v vs %+v", i, g1.Modules[i], g2.Modules[i])
		}
	}
	if d1.Error() != d2.Error() {
		t.Fatalf("diagnostics differ:\n%s\nvs\n%s", d1.Error(), d2.Error())
	}
}

// TestBuildWithOverlayShadowsDisk: an overlay entry wins over the file on
// disk, for the root and for an imported module.
func TestBuildWithOverlayShadowsDisk(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "fn main(): void {}\n",
		"a.ts":    "export fn onDisk(): void {}\n",
	})
	fsys := overlayFS{
		filepath.Join(root, "main.ts"): "import { inBuffer } from \"./a\";\nfn main(): void {}\n",
		filepath.Join(root, "a.ts"):    "export fn inBuffer(): void {}\n",
	}
	g, diags := BuildWith(filepath.Join(root, "main.ts"), BuildOptions{FS: fsys})
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	a := moduleByID(g, "a.ts")
	if a == nil {
		t.Fatalf("overlay root's import not followed: %v", ids(g))
	}
	if got := firstFuncName(a); got != "inBuffer" {
		t.Fatalf("a.ts declares %q, want the overlay's inBuffer", got)
	}
}

// TestBuildWithOverlayOnlyFile: a file that exists only in the overlay
// resolves as an import target.
func TestBuildWithOverlayOnlyFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "import { ghost } from \"./ghost\";\nfn main(): void {}\n",
	})
	fsys := overlayFS{filepath.Join(root, "ghost.tlang"): "export fn ghost(): void {}\n"}
	g, diags := BuildWith(filepath.Join(root, "main.ts"), BuildOptions{FS: fsys})
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	if moduleByID(g, "ghost.tlang") == nil {
		t.Fatalf("overlay-only module not resolved: %v", ids(g))
	}
}

// TestBuildWithRootDirParentImport: with RootDir set to the project root, a
// root file in a subdirectory may import "../x" inside the project, its ID is
// relative to RootDir, and an import escaping RootDir is still rejected.
func TestBuildWithRootDirParentImport(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app/main.ts": "import { util } from \"../lib/util\";\nfn main(): void {}\n",
		"lib/util.ts": "export fn util(): void {}\n",
		"app/esc.ts":  "import { x } from \"../../outside\";\nfn main(): void {}\n",
	})

	// Without RootDir the root's own directory bounds resolution.
	_, plain := Build(filepath.Join(root, "app", "main.ts"))
	if !strings.Contains(plain.Error(), "escapes the project root") {
		t.Fatalf("Build without RootDir: want a root escape, got %v", plain.Error())
	}

	g, diags := BuildWith(filepath.Join(root, "app", "main.ts"), BuildOptions{RootDir: root})
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	if g.Root == nil || g.Root.ID != "app/main.ts" {
		t.Fatalf("root ID = %v, want app/main.ts", g.Root)
	}
	if moduleByID(g, "lib/util.ts") == nil {
		t.Fatalf("../lib/util not resolved inside RootDir: %v", ids(g))
	}

	g2, diags2 := BuildWith(filepath.Join(root, "app", "esc.ts"), BuildOptions{RootDir: root})
	errs := importErrors(g2)
	if len(errs) != 1 || !strings.Contains(errs[0], "escapes the project root") {
		t.Fatalf("want one root-escape E-IMPORT, got %v", diags2.Error())
	}
	for _, e := range errs {
		if strings.Contains(e, root) {
			t.Errorf("diagnostic leaks a path: %q", e)
		}
	}
}

// TestBuildWithRootOutsideRootDir: a root file outside RootDir is a single
// E-IMPORT with no root module and no path in the message.
func TestBuildWithRootOutsideRootDir(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts":       "fn main(): void {}\n",
		"project/a.ts":  "export fn a(): void {}\n",
		"projectX/b.ts": "fn main(): void {}\n",
	})
	for _, rootFile := range []string{
		filepath.Join(root, "main.ts"),
		filepath.Join(root, "projectX", "b.ts"), // shares a name prefix with RootDir
	} {
		g, diags := BuildWith(rootFile, BuildOptions{RootDir: filepath.Join(root, "project")})
		if g.Root != nil || len(g.Modules) != 0 {
			t.Fatalf("%s: want no root and no modules, got root=%v modules=%v", rootFile, g.Root, ids(g))
		}
		errs := importErrors(g)
		if len(errs) != 1 || !strings.Contains(errs[0], "outside the project root") {
			t.Fatalf("%s: want one outside-root E-IMPORT, got %v", rootFile, diags.Error())
		}
		if strings.Contains(errs[0], root) {
			t.Errorf("diagnostic leaks a path: %q", errs[0])
		}
	}
}
