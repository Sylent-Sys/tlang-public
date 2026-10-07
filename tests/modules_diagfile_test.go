package tests

import (
	"os"
	"path/filepath"
	"testing"

	"tlang/checker"
	"tlang/module"
)

// TestModulesDiagnosticFileRender renders the diag_file_* reject cases exactly
// as cmd/tlang's runFrontendGraph prints them (module.Build, then
// checker.CheckProgram, then List.Render(nil)) and requires the whole output
// to equal the case's expected.err byte for byte. TestModulesReject only
// checks a substring; this pins that each diagnostic names the module its
// position lies in (not the first module in module order) and that nothing
// else is printed.
func TestModulesDiagnosticFileRender(t *testing.T) {
	for _, name := range []string{"diag_file_root", "diag_file_dependency", "diag_file_note"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("modules", "reject", name)
			graph, bdiags := module.Build(filepath.Join(dir, modsRootFile))
			if bdiags.Len() > 0 {
				t.Fatalf("module.Build: %s", bdiags.Error())
			}
			_, cdiags := checker.CheckProgram(graph.Modules)
			want, err := os.ReadFile(filepath.Join(dir, modsErrName))
			if err != nil {
				t.Fatalf("read %s: %v", modsErrName, err)
			}
			if got := cdiags.Render(nil); got != string(want) {
				t.Fatalf("rendered diagnostics:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
