package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tlang/driver"
)

// A minimal valid script program: fn main(): void {} gives Kind ProgramScript
// with a non-nil Entry, so CheckEntry passes and Plan assembles.
const scriptSrc = "fn main(): void {}\n"

// A program with neither main nor route_dispatcher: the checker accepts it as
// Kind ProgramUnknown with a nil Entry, which drives the driver's ErrNoEntry.
const noEntrySrc = "fn helper(): void {}\n"

// fakeLookPath returns fake absolute paths for the named tools and an error for
// everything else, so the driver's Discover/AssemblePlan succeed hermetically
// (no real compiler, archiver, or libpq is touched).
func fakeLookPath(present ...string) driver.LookPathFunc {
	set := make(map[string]bool, len(present))
	for _, name := range present {
		set[name] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("absent")
	}
}

// withDriver swaps the newDriver seam for the duration of the test so the CLI
// builds a hermetic driver with the given Options, then restores it.
func withDriver(t *testing.T, opts driver.Options) {
	t.Helper()
	prev := newDriver
	newDriver = func() *driver.Driver { return driver.New(opts) }
	t.Cleanup(func() { newDriver = prev })
}

// hermeticOpts builds driver.Options with a cache root under t.TempDir and a
// fake pg_config, exposing which tools LookPath reports present.
func hermeticOpts(t *testing.T, present ...string) driver.Options {
	t.Helper()
	return driver.Options{
		LookPath:  fakeLookPath(present...),
		PgConfig:  func(args ...string) (string, error) { return "/pg/include", nil },
		CacheRoot: t.TempDir(),
	}
}

// TestBuildAssemblesPlan asserts the CLI's `build` wiring assembles a correct,
// inspectable archive-form plan via the same d.Plan seam the dispatcher uses,
// with NO real toolchain execution (hermetic fake LookPath). End-to-end
// execution is covered by TestRealToolchainBuildEndToEnd when a real compiler
// is present. This stays hermetic because the fake LookPath's paths may or may
// not be real executables on the host, so the test does not execute the plan.
func TestBuildAssemblesPlan(t *testing.T) {
	path := writeFixture(t, "app.ts", scriptSrc)
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check")
	}

	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc", "ar"))
	p, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerGCC, "/tmp/app.c", "/tmp/app")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Compiler.Kind != driver.CompilerGCC {
		t.Errorf("Compiler.Kind = %v, want gcc", p.Compiler.Kind)
	}
	if len(p.Steps) == 0 {
		t.Fatal("expected a non-empty step list")
	}
	if p.Output != "/tmp/app" {
		t.Errorf("plan.Output = %q, want /tmp/app", p.Output)
	}
}

// TestPlanAssemblyArgvFragments inspects the assembled BuildPlan directly
// through the same seam the CLI uses, asserting compiler selection and the
// expected argv fragments. This mirrors the driver's hermetic plan tests and
// proves the CLI's wiring assembles a correct, inspectable plan with NO real
// toolchain.
// TestRunAssemblesPlan asserts the `run` wiring assembles a tcc -run plan with
// an empty output via the d.Plan seam, mirroring TestBuildAssemblesPlan and
// staying hermetic (no execution of a possibly-real fake compiler path).
// End-to-end run execution is covered by TestRealToolchainBuildEndToEnd.
func TestRunAssemblesPlan(t *testing.T) {
	path := writeFixture(t, "app.ts", scriptSrc)
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check")
	}

	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc", "ar"))
	// run passes out="" so tcc selects the in-place -run form.
	p, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerTCC, "/tmp/app.c", "")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Compiler.Kind != driver.CompilerTCC {
		t.Errorf("Compiler.Kind = %v, want tcc", p.Compiler.Kind)
	}
	if p.Output != "" {
		t.Errorf("plan.Output = %q, want empty for tcc -run", p.Output)
	}
}

// TestBuildDefaultOutput covers the -o-omitted default output path for build so
// defaultOutput/baseName are exercised. It confirms the derived default output
// name ("app" from "app.ts") reaches the assembled plan's Output via the same
// d.Plan seam the CLI uses.
func TestBuildDefaultOutput(t *testing.T) {
	path := writeFixture(t, "app.ts", scriptSrc)

	// The default output derived from the source base name must reach the
	// assembled plan. "app.ts" -> "app"; feeding that default to Plan (as
	// buildOrRun does when -o is omitted) yields plan.Output == "app". Use an
	// explicit --cc gcc so the archive (build) form records the output path.
	wantOut := defaultOutput(path)
	if wantOut != "app" {
		t.Fatalf("defaultOutput(%q) = %q, want \"app\"", path, wantOut)
	}
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check")
	}
	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc", "ar"))
	p, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerGCC, "/tmp/app.c", wantOut)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Output != wantOut {
		t.Errorf("plan.Output = %q, want %q", p.Output, wantOut)
	}
	last := p.Steps[len(p.Steps)-1].Argv
	if !argvHas(last, wantOut) {
		t.Errorf("program step argv missing default output %q: %v", wantOut, last)
	}
}

// TestBaseNameEmptyFallback covers the empty-base "program" fallback in
// baseName, which the default output path uses when the source base reduces to
// nothing after stripping its extension (e.g. a bare ".ts").
func TestBaseNameEmptyFallback(t *testing.T) {
	if got := baseName(".ts"); got != "program" {
		t.Errorf("baseName(\".ts\") = %q, want \"program\"", got)
	}
	if got := defaultOutput("dir/.ts"); got != "program" {
		t.Errorf("defaultOutput(\"dir/.ts\") = %q, want \"program\"", got)
	}
}

func TestPlanAssemblyArgvFragments(t *testing.T) {
	path := writeFixture(t, "app.ts", scriptSrc)
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check without errors")
	}

	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc", "ar"))

	t.Run("release auto-selects gcc with release flags", func(t *testing.T) {
		p, err := d.Plan(res.info, driver.BuildRelease, 0, "/tmp/app.c", "/tmp/app")
		if err != nil {
			t.Fatalf("Plan error: %v", err)
		}
		if p.Compiler.Kind != driver.CompilerGCC {
			t.Errorf("Compiler.Kind = %v, want gcc", p.Compiler.Kind)
		}
		last := p.Steps[len(p.Steps)-1]
		for _, want := range []string{"-std=c11", "-D_GNU_SOURCE", "-fwrapv", "-Wall", "-O3", "-DNDEBUG", "/tmp/app.c", "-o", "/tmp/app"} {
			if !argvHas(last.Argv, want) {
				t.Errorf("program step argv missing %q: %v", want, last.Argv)
			}
		}
	})

	t.Run("dev auto-selects tcc", func(t *testing.T) {
		p, err := d.Plan(res.info, driver.BuildDebug, 0, "/tmp/app.c", "/tmp/app")
		if err != nil {
			t.Fatalf("Plan error: %v", err)
		}
		if p.Compiler.Kind != driver.CompilerTCC {
			t.Errorf("Compiler.Kind = %v, want tcc", p.Compiler.Kind)
		}
	})

	t.Run("explicit --cc clang honored with debug flags", func(t *testing.T) {
		p, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerClang, "/tmp/app.c", "/tmp/app")
		if err != nil {
			t.Fatalf("Plan error: %v", err)
		}
		if p.Compiler.Kind != driver.CompilerClang {
			t.Errorf("Compiler.Kind = %v, want clang", p.Compiler.Kind)
		}
		last := p.Steps[len(p.Steps)-1]
		for _, want := range []string{"-O2", "-g"} {
			if !argvHas(last.Argv, want) {
				t.Errorf("program step argv missing %q: %v", want, last.Argv)
			}
		}
	})
}

func TestBuildNoCompiler(t *testing.T) {
	// No tools present at all -> the driver finds no compiler.
	withDriver(t, hermeticOpts(t /* nothing present */))
	path := writeFixture(t, "app.ts", scriptSrc)

	var out, errb bytes.Buffer
	code := run([]string{"build", path}, &out, &errb)
	if code != exitFail {
		t.Fatalf("exit = %d, want exitFail; stderr: %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no C compiler found on PATH") {
		t.Errorf("stderr missing no-compiler message: %q", errb.String())
	}
}

func TestBuildNoEntry(t *testing.T) {
	withDriver(t, hermeticOpts(t, "gcc", "clang", "tcc", "ar"))
	// Program with neither entry point: the real checker yields ProgramUnknown
	// / nil Entry, so the driver returns ErrNoEntry.
	path := writeFixture(t, "lib.ts", noEntrySrc)

	var out, errb bytes.Buffer
	code := run([]string{"build", path}, &out, &errb)
	if code != exitFail {
		t.Fatalf("exit = %d, want exitFail; stderr: %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no entry point") {
		t.Errorf("stderr missing no-entry message: %q", errb.String())
	}
}

func TestBuildNoArchiver(t *testing.T) {
	// gcc present but no `ar`: now that runtime.CSources() is non-empty, the gcc
	// archive branch consults `ar` and returns ErrNoArchiver when it is absent.
	// Drive this hermetically through the d.Plan seam with a fake LookPath that
	// omits "ar", so the archiver-absent branch is reachable without a real
	// toolchain or runtime execution.
	path := writeFixture(t, "app.ts", scriptSrc)
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check")
	}
	// gcc present, ar absent.
	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc"))
	_, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerGCC, "/tmp/app.c", "/tmp/app")
	if !errors.Is(err, driver.ErrNoArchiver) {
		t.Fatalf("Plan error = %v, want ErrNoArchiver", err)
	}
}

func TestRunUsesRunFormForTCC(t *testing.T) {
	// run with --cc tcc should assemble the tcc -run form (empty output). We
	// assert the plan directly through the driver seam rather than executing.
	path := writeFixture(t, "app.ts", scriptSrc)
	src, ok := loadSource(path, &bytes.Buffer{})
	if !ok {
		t.Fatal("loadSource failed")
	}
	res := runFrontend(path, src, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("fixture should type-check")
	}
	d := driver.New(hermeticOpts(t, "gcc", "clang", "tcc", "ar"))
	// run passes out="" so tcc selects the in-place -run form.
	p, err := d.Plan(res.info, driver.BuildDebug, driver.CompilerTCC, "/tmp/app.c", "")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Output != "" {
		t.Errorf("Output = %q, want empty for tcc -run", p.Output)
	}
	last := p.Steps[len(p.Steps)-1].Argv
	if last[len(last)-1] != "-run" {
		t.Errorf("last argv token = %q, want -run", last[len(last)-1])
	}
}

func TestRuntimeGrantArgumentForwarding(t *testing.T) {
	want := []string{"--tlang-grants", "C:\\secure\\runtime grants.json"}
	if got := runtimeGrantArgs(want[1]); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtimeGrantArgs() = %q, want %q", got, want)
	}
	if got := runtimeGrantArgs(""); len(got) != 0 {
		t.Fatalf("runtimeGrantArgs(empty) = %q, want no args", got)
	}
}

func TestBadCCFlag(t *testing.T) {
	path := writeFixture(t, "app.ts", scriptSrc)
	var out, errb bytes.Buffer
	code := run([]string{"build", path, "--cc", "msvc"}, &out, &errb)
	if code != exitUsage {
		t.Fatalf("exit = %d, want exitUsage; stderr: %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "unknown --cc value") {
		t.Errorf("stderr missing bad-cc message: %q", errb.String())
	}
}

// realCC returns a --cc value for a C compiler actually present on PATH (gcc
// preferred, then clang), or ok=false when neither is available.
func realCC() (string, bool) {
	for _, name := range []string{"gcc", "clang"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, true
		}
	}
	return "", false
}

// TestRealToolchainBuildEndToEnd drives `build` and `run` end to end against a
// real C compiler and the embedded runtime. It is the one test that actually
// executes the assembled plan; it skips cleanly when no gcc/clang is on PATH so
// the suite stays green on a bare host. It uses the production newDriver (no
// hermetic seam) so the real toolchain and runtime are exercised.
func TestRealToolchainBuildEndToEnd(t *testing.T) {
	cc, ok := realCC()
	if !ok {
		t.Skip("end-to-end build/run: no C compiler (gcc/clang) on PATH")
	}

	path := writeFixture(t, "app.ts", scriptSrc)

	t.Run("build produces an executable", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "app")
		var stdout, stderr bytes.Buffer
		code := run([]string{"build", path, "-o", out, "--cc", cc}, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("build exit = %d, want exitOK; stderr: %q", code, stderr.String())
		}
		if _, err := os.Stat(out); err != nil {
			t.Fatalf("output binary not found at %q: %v", out, err)
		}
		// The produced binary must run (fn main(): void {} returns 0).
		if err := exec.Command(out).Run(); err != nil {
			t.Fatalf("produced binary failed to run: %v", err)
		}
	})

	t.Run("run executes the program", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"run", path, "--cc", cc}, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("run exit = %d, want exitOK; stderr: %q", code, stderr.String())
		}
	})
}

// argvHas reports whether argv contains the exact token.
func argvHas(argv []string, token string) bool {
	for _, a := range argv {
		if a == token {
			return true
		}
	}
	return false
}
