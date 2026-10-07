package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"tlang/checker"
	"tlang/codegen"
	"tlang/parser"
)

// emitFresh parses, checks and emits path from scratch, failing the test on any
// front-end error (warnings allowed). It returns the generated C bytes.
func emitFresh(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	prog, pdiags := parser.ParseSource(filepath.Base(path), src)
	if pdiags.HasErrors() {
		t.Fatalf("%s: parser errors:\n%s", path, pdiags.Error())
	}
	info, cdiags := checker.Check(prog)
	if cdiags.HasErrors() {
		t.Fatalf("%s: checker errors:\n%s", path, cdiags.Error())
	}
	out, eerr := codegen.Emit(prog, info)
	if eerr != nil {
		t.Fatalf("%s: Emit error: %v", path, eerr)
	}
	return out
}

// TestDeterminismFreshRuns emits every golden and examples/app.ts twice, each
// time from a fresh parse+check, and requires byte-identical C (design §14,
// plan F7 task 4). Nothing in the pipeline may depend on map iteration order,
// time, the environment or pointer values.
func TestDeterminismFreshRuns(t *testing.T) {
	for _, path := range goldenCases(t) {
		path := path
		t.Run(caseName(path), func(t *testing.T) {
			first := emitFresh(t, path)
			second := emitFresh(t, path)
			if !bytes.Equal(first, second) {
				t.Fatalf("two fresh emits differ\n%s", diffHint(first, second))
			}
		})
	}
}

// TestDeterminismSameInfo emits twice from a single (prog, info) pair for every
// golden and requires byte-identical C (plan F7 task 4). The second run sees
// the same instances already in NamedInstances() from the first, so the output
// must not drift when type collection re-walks an already-populated cache.
func TestDeterminismSameInfo(t *testing.T) {
	for _, path := range goldenCases(t) {
		path := path
		t.Run(caseName(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			prog, pdiags := parser.ParseSource(filepath.Base(path), src)
			if pdiags.HasErrors() {
				t.Fatalf("parser errors:\n%s", pdiags.Error())
			}
			info, cdiags := checker.Check(prog)
			if cdiags.HasErrors() {
				t.Fatalf("checker errors:\n%s", cdiags.Error())
			}
			first, err := codegen.Emit(prog, info)
			if err != nil {
				t.Fatalf("first Emit error: %v", err)
			}
			second, err := codegen.Emit(prog, info)
			if err != nil {
				t.Fatalf("second Emit error: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("two emits from the same (prog, info) differ\n%s", diffHint(first, second))
			}
		})
	}
}
