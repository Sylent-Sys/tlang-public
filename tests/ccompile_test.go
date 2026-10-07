//go:build ccompile

// The C-compile matrix (codegen design §15.2, §15.3, plan 1.1, F7, TOOLCHAIN-NIT-1).
// It emits every golden/*.tl and object-compiles the C on gcc, clang and tcc
// under the runtime's production flags. It is fail-closed: a missing compiler
// is a fatal error, never a skip, so a toolchain gap can never pass silently.
// Run it in the tlang-dev container: ./scripts/dev.ps1 "go test -tags ccompile ./tests/...".
//
// F7 adds three independent assertions on top of the matrix:
//   - TestMandatoryItems: each of the six design §15.2 mandatory goldens exists,
//     carries its first-line marker, contains the construct it exists to prove,
//     and object-compiles PASS on every configuration (plus the deferred
//     float-% LINK skip, design §15.3).
//   - TestGotoBearingOptimized: every unit whose C carries a generated label
//     (__fail, __guard_denied, __tlang_catch_, __tx_rollback_, __tx_fail_,
//     __tx_end_, __cont_) compiles PASS at gcc -O2 and clang -O2.
package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ccConfig is one compiler configuration of the fail-closed matrix.
type ccConfig struct {
	// name is the subtest label, e.g. "gcc-O0".
	name string
	// cc is the compiler binary, looked up on PATH (fatal when missing).
	cc string
	// args are the flags before the input file and "-o <out>".
	args []string
}

// ccConfigs is the five-configuration matrix: gcc and clang at -O0 and -O2
// with the full pedantic flag set, and tcc with its own flag set. Every
// configuration object-compiles (-c): the link step is deferred until the
// runtime's C sources exist (design §15.3; the float-% LINK check is a named
// skip until then).
func ccConfigs(repo string) []ccConfig {
	inc := filepath.Join(repo, "runtime", "include")
	gnu := func(opt string) []string {
		return []string{
			"-std=c11", "-pedantic", "-Wall", "-Wextra", "-Wno-unused-parameter",
			"-fwrapv", "-Werror", opt, "-c", "-I", inc,
		}
	}
	return []ccConfig{
		{"gcc-O0", "gcc", gnu("-O0")},
		{"gcc-O2", "gcc", gnu("-O2")},
		{"clang-O0", "clang", gnu("-O0")},
		{"clang-O2", "clang", gnu("-O2")},
		{"tcc", "tcc", []string{"-std=c11", "-Wall", "-Werror", "-c", "-I", inc}},
	}
}

// requireCompilers looks up every configuration's compiler on PATH and fails
// the test fatally when one is missing (the matrix is fail-closed, design §15.2).
func requireCompilers(t *testing.T, configs []ccConfig) {
	t.Helper()
	for i := range configs {
		if _, err := exec.LookPath(configs[i].cc); err != nil {
			t.Fatalf("compiler %q not found on PATH (the matrix is fail-closed): %v", configs[i].cc, err)
		}
	}
}

// repoRoot resolves the repository root (the parent of tests/).
func repoRoot(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// emitUnit emits the golden at path to C, asserts the shape (non-empty, starts
// with the runtime include) and writes it to a fresh .c file whose path it
// returns.
func emitUnit(t *testing.T, path string) string {
	t.Helper()
	out, err := compile(t, path)
	if err != nil {
		t.Fatalf("%s: Emit error: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s: Emit returned no bytes", path)
	}
	if !strings.HasPrefix(string(out), "#include \"tlang.h\"\n") {
		t.Fatalf("%s: output does not start with the runtime include", path)
	}
	cfile := filepath.Join(t.TempDir(), "unit.c")
	if werr := os.WriteFile(cfile, out, 0o644); werr != nil {
		t.Fatal(werr)
	}
	return cfile
}

// objCompile object-compiles cfile with one configuration and returns the
// compiler diagnostics and the error (nil on success).
func objCompile(cfg ccConfig, cfile string) ([]byte, error) {
	obj := filepath.Join(filepath.Dir(cfile), cfg.name+".o")
	args := append(append([]string{}, cfg.args...), cfile, "-o", obj)
	cmd := exec.Command(cfg.cc, args...)
	return cmd.CombinedOutput()
}

// TestCCompile object-compiles every golden on the five configurations. Each
// configuration's compiler must be present; a missing one is a fatal error.
func TestCCompile(t *testing.T) {
	repo := repoRoot(t)
	configs := ccConfigs(repo)
	requireCompilers(t, configs)

	for _, path := range goldenCases(t) {
		cfile := emitUnit(t, path)
		unit := caseName(path)
		for _, cfg := range configs {
			cfg := cfg
			t.Run(unit+"/"+cfg.name, func(t *testing.T) {
				t.Parallel()
				if diag, cerr := objCompile(cfg, cfile); cerr != nil {
					t.Fatalf("%s failed on %s:\n%s\n%s", cfg.name, unit, cerr, diag)
				}
			})
		}
	}
}

// mandatoryItem is one design §15.2 mandatory golden: its file name (no
// extension), its first-line marker, and the construct substring its C must
// contain to prove the lowering it exists for.
type mandatoryItem struct {
	name      string
	marker    string
	construct string
}

// mandatoryItems lists the six design §15.2 mandatory goldens in order.
var mandatoryItems = []mandatoryItem{
	{"self_assign_struct", "// codegen-mandatory: 15.2.1", "(void)l_"},
	{"route_match", "// codegen-mandatory: 15.2.2", "__tl_r"},
	{"db_rows", "// codegen-mandatory: 15.2.3", "__tl_td_"},
	{"long_literals", "// codegen-mandatory: 15.2.4", "static const char __tl_s"},
	{"json_top_array", "// codegen-mandatory: 15.2.5", "tlj_parse_arr_"},
	{"float_mod", "// codegen-mandatory: 15.2.6", "tlang_mod_f64"},
}

// TestMandatoryItems proves every design §15.2 mandatory golden: it exists,
// its first line is the pinned marker, its emitted C contains the construct it
// proves, and it object-compiles PASS on all five configurations. A missing
// file or marker fails the suite (the items are mandatory, never skipped).
func TestMandatoryItems(t *testing.T) {
	repo := repoRoot(t)
	configs := ccConfigs(repo)
	requireCompilers(t, configs)

	for _, item := range mandatoryItems {
		item := item
		t.Run(item.name, func(t *testing.T) {
			src := filepath.Join("golden", item.name+".tl")
			srcBytes, err := os.ReadFile(src)
			if err != nil {
				t.Fatalf("mandatory golden %s is missing: %v", item.name, err)
			}
			firstLine := string(srcBytes)
			if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
				firstLine = firstLine[:i]
			}
			if !strings.HasPrefix(firstLine, item.marker) {
				t.Fatalf("%s: first line %q does not start with marker %q", item.name, firstLine, item.marker)
			}

			out, cerr := compile(t, src)
			if cerr != nil {
				t.Fatalf("%s: Emit error: %v", item.name, cerr)
			}
			if !strings.Contains(string(out), item.construct) {
				t.Fatalf("%s: emitted C does not contain the mandatory construct %q", item.name, item.construct)
			}

			cfile := emitUnit(t, src)
			for _, cfg := range configs {
				cfg := cfg
				t.Run(cfg.name, func(t *testing.T) {
					t.Parallel()
					if diag, err := objCompile(cfg, cfile); err != nil {
						t.Fatalf("mandatory %s failed on %s:\n%s\n%s", item.name, cfg.name, err, diag)
					}
				})
			}

			// Mandatory item 6 (float %) also carries the deferred LINK check:
			// a named skip until the runtime C sources exist (design §15.3,
			// TOOLCHAIN-NIT-3). The compile checks above already proved the unit
			// object-compiles with -c on gcc, clang and tcc.
			if item.name == "float_mod" {
				t.Run("link", func(t *testing.T) {
					t.Skip("link step deferred: runtime/src/*.c is not implemented yet (design §15.2 item 6 / §15.3); the float-% unit is compile-checked with -c on gcc, clang and tcc")
				})
			}
		})
	}
}

// gotoLabels are the generated-label substrings whose presence makes a unit
// goto-bearing (design §6-§7). Any one of them triggers the -O2 assertion.
var gotoLabels = []string{
	"__fail:", "__guard_denied:", "__tlang_catch_",
	"__tx_rollback_", "__tx_fail_", "__tx_end_", "__cont_",
}

// optimizedConfigs is the subset of the matrix the goto-bearing assertion
// cares about: gcc -O2 and clang -O2 (the optimizers that reorder around
// labels, design §14 / plan F7 task 5).
func optimizedConfigs(repo string) []ccConfig {
	all := ccConfigs(repo)
	var out []ccConfig
	for _, c := range all {
		if c.name == "gcc-O2" || c.name == "clang-O2" {
			out = append(out, c)
		}
	}
	return out
}

// TestGotoBearingOptimized compiles every unit whose C carries a generated
// label at gcc -O2 and clang -O2 and requires PASS. It guards against an
// optimizer miscompiling the goto-threaded error model under optimization.
func TestGotoBearingOptimized(t *testing.T) {
	repo := repoRoot(t)
	configs := optimizedConfigs(repo)
	requireCompilers(t, configs)

	var any bool
	for _, path := range goldenCases(t) {
		out, err := compile(t, path)
		if err != nil {
			t.Fatalf("%s: Emit error: %v", path, err)
		}
		text := string(out)
		if !containsAny(text, gotoLabels) {
			continue
		}
		any = true
		cfile := emitUnit(t, path)
		unit := caseName(path)
		for _, cfg := range configs {
			cfg := cfg
			t.Run(unit+"/"+cfg.name, func(t *testing.T) {
				t.Parallel()
				if diag, err := objCompile(cfg, cfile); err != nil {
					t.Fatalf("goto-bearing %s failed on %s:\n%s\n%s", unit, cfg.name, err, diag)
				}
			})
		}
	}
	if !any {
		t.Fatal("no goto-bearing unit found in the corpus; the assertion is vacuous")
	}
}

// emitMultiUnit builds the merged C of a multi-file golden case directory,
// asserts the single-TU shape, and writes it to a fresh .c file whose path it
// returns (the multi-file analogue of emitUnit, FEAT-004 / AC-29).
func emitMultiUnit(t *testing.T, caseDir string) string {
	t.Helper()
	out, err := buildMergedC(caseDir)
	if err != nil {
		t.Fatalf("%s: front end rejected a golden case: %v", caseDir, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s: Emit returned no bytes", caseDir)
	}
	if n := strings.Count(string(out), "#include \"tlang.h\""); n != 1 {
		t.Fatalf("%s: merged C has %d #include \"tlang.h\", want exactly 1", caseDir, n)
	}
	if !strings.HasPrefix(string(out), "#include \"tlang.h\"\n") {
		t.Fatalf("%s: output does not start with the runtime include", caseDir)
	}
	cfile := filepath.Join(t.TempDir(), "unit.c")
	if werr := os.WriteFile(cfile, out, 0o644); werr != nil {
		t.Fatal(werr)
	}
	return cfile
}

// TestCCompileModules object-compiles every multi-file golden's merged C on the
// five configurations, extending the matrix to the module corpus (FEAT-004 /
// AC-29). Like TestCCompile it is fail-closed: each compiler must be present, a
// missing one is a fatal error, never a skip.
func TestCCompileModules(t *testing.T) {
	repo := repoRoot(t)
	configs := ccConfigs(repo)
	requireCompilers(t, configs)

	for _, dir := range modsCaseDirs(t, "modules") {
		cfile := emitMultiUnit(t, dir)
		unit := filepath.Base(dir)
		for _, cfg := range configs {
			cfg := cfg
			t.Run(unit+"/"+cfg.name, func(t *testing.T) {
				t.Parallel()
				if diag, cerr := objCompile(cfg, cfile); cerr != nil {
					t.Fatalf("%s failed on %s:\n%s\n%s", cfg.name, unit, cerr, diag)
				}
			})
		}
	}
}

// containsAny reports whether s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
