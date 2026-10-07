package tests

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"tlang/checker"
	"tlang/codegen"
	"tlang/parser"
)

// update rewrites the .c.golden files from the current Emit output instead of
// comparing against them: go test ./tests -run Golden -update.
var update = flag.Bool("update", false, "rewrite the .c.golden files")

// appSource is the examples/app.ts program, whose golden is golden/app.c.golden
// (codegen design §15.3, plan F6). It is the one golden whose source lives
// outside golden/, so both rigs special-case it: the source is examples/app.ts
// and the golden keeps the reserved name "app".
var appSource = filepath.Join("..", "examples", "app.ts")

// goldenCases returns the sorted .tl inputs under golden/ (not golden/reject/)
// followed by the examples/app.ts special case (plan F6).
func goldenCases(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("golden", "*.tl"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return append(paths, appSource)
}

// goldenPath returns the .c.golden path of a source case: the sibling
// <name>.c.golden in golden/, with examples/app.ts mapped to golden/app.c.golden.
func goldenPath(path string) string {
	if path == appSource {
		return filepath.Join("golden", "app.c.golden")
	}
	return strings.TrimSuffix(path, ".tl") + ".c.golden"
}

// rejectCases returns the sorted .tl inputs under golden/reject/.
func rejectCases(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("golden", "reject", "*.tl"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

// compile runs the full front end then codegen on the .tl file at path. It
// fails if the parser or checker reports an error (warnings are allowed), and
// returns the generated C and the Emit error.
func compile(t *testing.T, path string) ([]byte, error) {
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
	return codegen.Emit(prog, info)
}

// compileAllowReject is the permissive compile path for TestReject: unlike
// compile it does not fail on a checker error. A program the checker rejects
// (e.g. a C-name collision or an embedded-NUL SQL literal, now caught in the
// checker) yields (nil, checker error) so TestReject can substring-match the
// checker diagnostic; a codegen-only reject case has a clean front end and
// proceeds to codegen.Emit exactly as before. Either way no C is emitted on a
// rejection, so TestReject's len(out) == 0 assertion holds.
func compileAllowReject(t *testing.T, path string) ([]byte, error) {
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
		return nil, cdiags.Err()
	}
	return codegen.Emit(prog, info)
}

// TestGolden emits every golden/*.tl and compares the bytes against its
// .c.golden; with -update it rewrites the goldens instead.
func TestGolden(t *testing.T) {
	for _, path := range goldenCases(t) {
		t.Run(caseName(path), func(t *testing.T) {
			out, err := compile(t, path)
			if err != nil {
				t.Fatalf("Emit error: %v", err)
			}
			gpath := goldenPath(path)
			if *update {
				if err := os.WriteFile(gpath, out, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(gpath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(out, want) {
				t.Fatalf("output differs from %s\n%s", gpath, diffHint(want, out))
			}
		})
	}
}

// TestReject emits every golden/reject/*.tl and checks that the front end is
// clean, Emit returns no bytes, and the error contains the .err text.
func TestReject(t *testing.T) {
	for _, path := range rejectCases(t) {
		t.Run(caseName(path), func(t *testing.T) {
			out, err := compileAllowReject(t, path)
			if err == nil {
				t.Fatalf("Emit succeeded, want a rejection")
			}
			if len(out) != 0 {
				t.Fatalf("Emit returned %d bytes with error %v, want none", len(out), err)
			}
			epath := strings.TrimSuffix(path, ".tl") + ".err"
			wantBytes, rerr := os.ReadFile(epath)
			if rerr != nil {
				t.Fatalf("read .err: %v", rerr)
			}
			want := strings.TrimRight(string(wantBytes), "\n")
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err.Error(), want)
			}
		})
	}
}

// caseName is the subtest name of a golden path: its base without the
// extension, with "reject/" kept for a rejection case. examples/app.ts is
// the "app" case.
func caseName(path string) string {
	ext := filepath.Ext(path)
	name := strings.TrimSuffix(filepath.Base(path), ext)
	if filepath.Base(filepath.Dir(path)) == "reject" {
		return "reject/" + name
	}
	return name
}

// diffHint returns the first differing line of want vs got, for a readable
// failure without a full diff.
func diffHint(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n want: " + w + "\n  got: " + g
		}
	}
	return "(lengths differ with no differing line)"
}
