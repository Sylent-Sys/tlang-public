package tests

import (
	"os"
	"path/filepath"
	"testing"

	"tlang/checker"
	"tlang/codegen"
	"tlang/module"
)

// AC-27: the emitted C of a multi-file program is deterministic. module.Build
// orders modules topologically with an ID-lexicographic tie-break (a pure
// function of the graph, independent of filesystem iteration order), and the
// mangling tag is a pure function of each module's ID. So the same fixture
// written into fresh directories — and built across repeated runs that create
// the files in different orders — must emit byte-identical C.

// detFiles is a multi-file fixture that touches the order-sensitive surfaces:
// a shared type, a re-export chain, a namespace import, two same-named globals
// in two modules, and a generic instantiated in more than one module.
var detFiles = map[string]string{
	"main.ts": `
import { User } from "./models";
import * as box from "./box";
import { reDeep } from "./reexport";
let total: int64 = 0;
fn main(): void {
	let u = new User();
	u.id = box.wrap<int64>(1);
	total = reDeep();
}
`,
	"models.ts": `
export interface User { id: int64; name: string; }
export let seed: int64 = 3;
`,
	"box.ts": `
export fn wrap<T>(x: T): T { return x; }
export let seed: int64 = 7;
`,
	"deep.ts": `export fn reDeep(): int64 { return inner(); }
export fn inner(): int64 { return 1; }`,
	"reexport.ts": `export { reDeep } from "./deep";`,
}

// buildDetMergedC writes detFiles into a fresh temp directory in the given
// creation order and returns the merged C. The module order is a pure function
// of the graph, so the creation order must not affect the output — which is
// exactly what the caller asserts.
func buildDetMergedC(t *testing.T, order []string) []byte {
	t.Helper()
	dir := t.TempDir()
	for _, name := range order {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(detFiles[name]), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, "main.ts"))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build: %s", bdiags.Error())
	}
	info, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		t.Fatalf("CheckProgram: %s", cdiags.Error())
	}
	out, err := codegen.Emit(mergeGraph(graph), info)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return out
}

// TestModulesDeterminism builds the same multi-file fixture repeatedly and
// under several file-creation orders and asserts the merged C is byte-identical
// every time (AC-27).
func TestModulesDeterminism(t *testing.T) {
	orders := [][]string{
		{"main.ts", "models.ts", "box.ts", "deep.ts", "reexport.ts"},
		{"reexport.ts", "deep.ts", "box.ts", "models.ts", "main.ts"},
		{"box.ts", "reexport.ts", "main.ts", "deep.ts", "models.ts"},
	}

	var want []byte
	for i, order := range orders {
		for rep := 0; rep < 3; rep++ {
			got := buildDetMergedC(t, order)
			if want == nil {
				want = got
				continue
			}
			if string(got) != string(want) {
				t.Fatalf("non-deterministic C: order %d rep %d differs\n%s",
					i, rep, diffHint(want, got))
			}
		}
	}
}
