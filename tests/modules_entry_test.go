package tests

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tlang/checker"
	"tlang/driver"
	"tlang/module"
	"tlang/types"
)

// AC-23 (whole-program zero-entry): a multi-file program with neither main nor
// route_dispatcher anywhere is a library — the checker accepts it as
// ProgramUnknown with no entry, and the driver's whole-program entry gate
// (driver.CheckEntry) is what rejects it with ErrNoEntry. This complements the
// two-entry reject cases under modules/reject (which are E-ENTRY at check
// time): the entry count is enforced across the entire module graph.
func TestModulesZeroEntryIsLibrary(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 1; }`,
		"main.ts": `
import { helper } from "./util";
fn start(): int64 { return helper(); }
`,
	}
	dir := t.TempDir()
	for name, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, "main.ts"))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build: %s", bdiags.Error())
	}
	info, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		t.Fatalf("CheckProgram rejected a library (should be ProgramUnknown): %s", cdiags.Error())
	}
	if info.Kind != types.ProgramUnknown {
		t.Fatalf("zero-entry whole program should be ProgramUnknown, got %v", info.Kind)
	}
	if err := driver.CheckEntry(info); !errors.Is(err, driver.ErrNoEntry) {
		t.Fatalf("driver.CheckEntry = %v, want ErrNoEntry", err)
	}
}
