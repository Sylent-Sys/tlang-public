package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandardImportsStayVirtual(t *testing.T) {
	root := filepath.Join(writeTree(t, map[string]string{"main.ts": `import { db } from "tlang/db"; fn main(): void {}`}), "main.ts")
	g, diags := Build(root)
	if diags.HasErrors() {
		t.Fatal(diags.Error())
	}
	if len(g.Modules) != 1 || g.Root.Tag != "" {
		t.Fatalf("virtual import changed source graph: modules=%d tag=%q", len(g.Modules), g.Root.Tag)
	}
	if len(g.Root.Imports) != 1 || g.Root.Imports[0].Target.Kind != ImportTargetStandard || g.Root.Imports[0].Target.Standard.ID != StandardDB {
		t.Fatalf("unexpected virtual edge: %+v", g.Root.Imports)
	}
}

func TestReservedStandardSpecifiersFailBeforeProbe(t *testing.T) {
	for _, spec := range []string{"tlang", "tlang/", "tlang//db", "tlang/./db", "tlang/../db", "tlang/unknown"} {
		if _, err := resolve(noProbeFS{}, ".", ".", spec); err == nil {
			t.Errorf("resolve(%q) unexpectedly succeeded", spec)
		}
	}
}

type noProbeFS struct{}

func (noProbeFS) ReadFile(string) ([]byte, error) { panic("unexpected read") }
func (noProbeFS) IsFile(string) bool              { panic("unexpected probe") }

// writeTree writes files (keyed by slash-relative path -> contents) under a
// fresh temp dir and returns the dir.
func writeTree(t *testing.T, files map[string]string) string {
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

func ids(g *Graph) []string {
	out := make([]string, len(g.Modules))
	for i, m := range g.Modules {
		out[i] = m.ID
	}
	return out
}

func importErrors(g *Graph) []string {
	var out []string
	for _, d := range g.Diags.Items {
		if d.Code == codeImport {
			out = append(out, d.Message)
		}
	}
	return out
}

// TestSingleFileFastPath: an import-free root is one Module with Tag "".
func TestSingleFileFastPath(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "fn main(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	if len(g.Modules) != 1 {
		t.Fatalf("got %d modules, want 1", len(g.Modules))
	}
	if g.Root.ID != "main.ts" {
		t.Errorf("root ID = %q, want main.ts", g.Root.ID)
	}
	if g.Root.Tag != "" {
		t.Errorf("single-file Tag = %q, want empty", g.Root.Tag)
	}
}

// TestClosureAndTags: the graph reaches every transitive import, parses each
// once, and gives every module a non-empty tag in a multi-module graph.
func TestClosureAndTags(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts":  "import { a } from \"./a\";\nfn main(): void {}\n",
		"a.ts":     "import { b } from \"./lib/b\";\nexport fn a(): void {}\n",
		"lib/b.ts": "export fn b(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	want := map[string]bool{"main.ts": true, "a.ts": true, "lib/b.ts": true}
	if len(g.Modules) != len(want) {
		t.Fatalf("got %d modules %v, want %d", len(g.Modules), ids(g), len(want))
	}
	for _, m := range g.Modules {
		if !want[m.ID] {
			t.Errorf("unexpected module %q", m.ID)
		}
		if m.Tag == "" {
			t.Errorf("module %q has empty tag in multi-module graph", m.ID)
		}
	}
}

// TestTopoOrderDependenciesFirst: an imported module precedes its importer,
// with an ID-lexicographic tie-break.
func TestTopoOrderDependenciesFirst(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "import { a } from \"./a\";\nimport { z } from \"./z\";\nfn main(): void {}\n",
		"a.ts":    "export fn a(): void {}\n",
		"z.ts":    "export fn z(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	pos := map[string]int{}
	for i, m := range g.Modules {
		pos[m.ID] = i
	}
	if pos["a.ts"] > pos["main.ts"] || pos["z.ts"] > pos["main.ts"] {
		t.Errorf("dependencies must precede importer: %v", ids(g))
	}
	// Tie-break: a.ts before z.ts (both only depended on by main).
	if pos["a.ts"] > pos["z.ts"] {
		t.Errorf("ID tie-break failed: %v", ids(g))
	}
}

// TestDeterministicOrder: Build gives the same order across runs regardless
// of directory iteration order (NFR-7). We rebuild many times and compare.
func TestDeterministicOrder(t *testing.T) {
	files := map[string]string{
		"main.ts": "import { a } from \"./a\";\nimport { b } from \"./b\";\nimport { c } from \"./c\";\nfn main(): void {}\n",
		"a.ts":    "export fn a(): void {}\n",
		"b.ts":    "export fn b(): void {}\n",
		"c.ts":    "export fn c(): void {}\n",
	}
	root := writeTree(t, files)
	var first []string
	for i := 0; i < 20; i++ {
		g, diags := Build(filepath.Join(root, "main.ts"))
		if diags.HasErrors() {
			t.Fatalf("unexpected errors: %v", diags.Error())
		}
		order := ids(g)
		if first == nil {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("non-deterministic order: %v vs %v", first, order)
		}
	}
}

// TestIdentityDedup: ./a from the root and ../a from a sibling dir reach the
// same file and share one Module.
func TestIdentityDedup(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts":  "import { a } from \"./a\";\nimport { s } from \"./sub/s\";\nfn main(): void {}\n",
		"a.ts":     "export fn a(): void {}\n",
		"sub/s.ts": "import { a } from \"../a\";\nexport fn s(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	count := 0
	for _, m := range g.Modules {
		if m.ID == "a.ts" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("a.ts loaded %d times, want 1 (identity dedup): %v", count, ids(g))
	}
}

// TestAllowedCycle: an import cycle is not an error (FR-21); the order stays
// total and deterministic.
func TestAllowedCycle(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "import { a } from \"./a\";\nfn main(): void {}\n",
		"a.ts":    "import { b } from \"./b\";\nexport fn a(): void {}\n",
		"b.ts":    "import { a } from \"./a\";\nexport fn b(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("import cycle must not error: %v", diags.Error())
	}
	if len(g.Modules) != 3 {
		t.Fatalf("got %d modules, want 3: %v", len(g.Modules), ids(g))
	}
	// Deterministic across runs even with a cycle.
	ref := strings.Join(ids(g), ",")
	for i := 0; i < 10; i++ {
		g2, _ := Build(filepath.Join(root, "main.ts"))
		if strings.Join(ids(g2), ",") != ref {
			t.Fatalf("cycle order not deterministic: %v vs %v", ref, ids(g2))
		}
	}
}

// TestResolveErrors covers each E-IMPORT shape with a safe (root-relative)
// message.
func TestResolveErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "bare specifier",
			files: map[string]string{"main.ts": "import { x } from \"strings\";\nfn main(): void {}\n"},
			want:  "bare import specifier",
		},
		{
			name:  "absolute specifier",
			files: map[string]string{"main.ts": "import { x } from \"/etc/passwd\";\nfn main(): void {}\n"},
			want:  "absolute import specifier",
		},
		{
			name:  "root escape",
			files: map[string]string{"main.ts": "import { x } from \"../secret\";\nfn main(): void {}\n"},
			want:  "escapes the project root",
		},
		{
			name:  "missing file",
			files: map[string]string{"main.ts": "import { x } from \"./nope\";\nfn main(): void {}\n"},
			want:  "cannot resolve import",
		},
		{
			name: "ambiguous extension",
			files: map[string]string{
				"main.ts":   "import { x } from \"./dup\";\nfn main(): void {}\n",
				"dup.ts":    "export fn x(): void {}\n",
				"dup.tlang": "export fn x(): void {}\n",
			},
			want: "ambiguous import",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, tc.files)
			g, _ := Build(filepath.Join(root, "main.ts"))
			errs := importErrors(g)
			found := false
			for _, e := range errs {
				if strings.Contains(e, tc.want) {
					found = true
				}
				// NFR-9: never echo an OS path outside the root.
				if strings.Contains(e, root) || strings.Contains(e, "etc") && tc.name != "absolute specifier" {
					t.Errorf("diagnostic leaks a path: %q", e)
				}
			}
			if !found {
				t.Fatalf("want an E-IMPORT containing %q, got %v", tc.want, errs)
			}
		})
	}
}

// TestExplicitExtension: a specifier that already carries an extension is used
// as written (no .ts/.tlang probing).
func TestExplicitExtension(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.ts": "import { a } from \"./a.tlang\";\nfn main(): void {}\n",
		"a.tlang": "export fn a(): void {}\n",
	})
	g, diags := Build(filepath.Join(root, "main.ts"))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %v", diags.Error())
	}
	if _, ok := func() (*Module, bool) {
		for _, m := range g.Modules {
			if m.ID == "a.tlang" {
				return m, true
			}
		}
		return nil, false
	}(); !ok {
		t.Fatalf("explicit-extension import not resolved: %v", ids(g))
	}
}

// TestTagPureAndNoDoubleUnderscore: Tag is a pure function of ID and never
// contains "__" (the mangling injectivity property).
func TestTagPureAndNoDoubleUnderscore(t *testing.T) {
	samples := []string{
		"main.ts", "a.ts", "lib/b.ts", "sub/dir/c.tlang",
		"1leading-digit.ts", "weird name!.ts", "a--b.ts", "a..b/c.ts",
	}
	for _, id := range samples {
		t1 := Tag(id)
		t2 := Tag(id)
		if t1 != t2 {
			t.Errorf("Tag(%q) not pure: %q vs %q", id, t1, t2)
		}
		if strings.Contains(t1, "__") {
			t.Errorf("Tag(%q) = %q contains \"__\"", id, t1)
		}
		if t1 == "" {
			t.Errorf("Tag(%q) is empty", id)
		}
		// First char must be a valid C-identifier start.
		c := t1[0]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == 'm') {
			t.Errorf("Tag(%q) = %q starts with invalid char", id, t1)
		}
	}
}

// TestTagInjectivity: distinct IDs yield distinct tags (or a detected hash
// collision, which the base36 disambiguator makes astronomically unlikely
// for these inputs).
func TestTagInjectivity(t *testing.T) {
	ids := []string{}
	// A dense set of IDs that stress sanitize (same sanitized stem, different
	// raw bytes -> the fnv disambiguator must separate them).
	for _, a := range []string{"a", "a_", "a-", "a.", "a!"} {
		for _, b := range []string{"x.ts", "y.ts", "x.tlang"} {
			ids = append(ids, a+"/"+b)
		}
	}
	seen := map[string]string{}
	for _, id := range ids {
		tag := Tag(id)
		if prev, ok := seen[tag]; ok && prev != id {
			t.Errorf("tag collision: %q and %q both -> %q", prev, id, tag)
		}
		seen[tag] = id
	}
}
