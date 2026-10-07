package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tlang/codegen"
	"tlang/driver"
)

// newDriver is the seam tests use to substitute a hermetic driver without
// touching the locked driver package. Production builds a driver with nil
// Options fields so the driver uses the real exec.LookPath / pg_config /
// DefaultCacheRoot. Tests overwrite this var to inject a driver constructed
// with driver.Options{LookPath, PgConfig} that reports a fake toolchain, so
// plan assembly and the error paths are exercised with NO real C compiler,
// libpq, or runtime .c files.
var newDriver = func() *driver.Driver {
	return driver.New(driver.Options{})
}

// parseCC maps a --cc value to a driver.CompilerKind. The empty string means
// "auto-select" (the zero CompilerKind). Any other value is a usage error.
func parseCC(cc string) (driver.CompilerKind, bool) {
	switch cc {
	case "":
		return 0, true
	case "gcc":
		return driver.CompilerGCC, true
	case "clang":
		return driver.CompilerClang, true
	case "tcc":
		return driver.CompilerTCC, true
	default:
		return 0, false
	}
}

// runRun implements `tlang run <file.ts> [--release] [--cc gcc|clang|tcc]`.
// run produces no persistent artifact: for a tcc toolchain an empty output path
// selects the driver's -run form; otherwise a temporary executable path is
// used. See buildOrRun for the shared pipeline.
func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	release := fs.Bool("release", false, "build with release optimizations (-O3 -DNDEBUG)")
	cc := fs.String("cc", "", "C compiler to use: gcc, clang, or tcc (default: auto-select)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: tlang run <file.ts> [--release] [--cc gcc|clang|tcc]")
		fs.PrintDefaults()
	}

	file, flagArgs := splitPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if file == "" || fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}

	return buildOrRun(false, file, "", *release, *cc, stdout, stderr)
}

// runBuild implements
// `tlang build <file.ts> [-o out] [--release] [--cc gcc|clang|tcc]`.
// build produces an executable at the -o path; when -o is omitted the output
// defaults to the source base name without its extension. See buildOrRun for
// the shared pipeline.
func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output executable path (default: source base name without extension)")
	release := fs.Bool("release", false, "build with release optimizations (-O3 -DNDEBUG)")
	cc := fs.String("cc", "", "C compiler to use: gcc, clang, or tcc (default: auto-select)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: tlang build <file.ts> [-o out] [--release] [--cc gcc|clang|tcc]")
		fs.PrintDefaults()
	}

	file, flagArgs := splitPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if file == "" || fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}

	return buildOrRun(true, file, *out, *release, *cc, stdout, stderr)
}

// buildOrRun is the shared body of `run` and `build`. isBuild distinguishes the
// two: build produces an artifact at outFlag (or a default), run has no
// persistent artifact. Both share load+parse+check, codegen.Emit, writing the C
// to a temp file, assembling a driver plan, and executing it.
//
// The assembled plan is executed via BuildPlan.Run: it extracts the embedded C
// runtime and runs each compile/archive/link step. On success build has written
// the executable at plan.Output and run has executed the program (tcc -run in
// place, or the compiled artifact otherwise); exitOK is returned. On a driver
// execution failure the error is surfaced and exitFail is returned.
func buildOrRun(isBuild bool, path, outFlag string, release bool, cc string, stdout, stderr io.Writer) int {
	kind, ok := parseCC(cc)
	if !ok {
		fmt.Fprintf(stderr, "tlang: unknown --cc value %q (want gcc, clang, or tcc)\n", cc)
		return exitUsage
	}

	if _, ok := loadSource(path, stderr); !ok {
		return exitUsage
	}

	res := runFrontendGraph(path, stderr)
	if res.hasErrs {
		return exitFail
	}

	cbytes, err := codegen.Emit(res.prog, res.info)
	if err != nil {
		fmt.Fprintf(stderr, "tlang: %v\n", err)
		return exitFail
	}

	programC, cleanup, err := writeTempC(path, cbytes)
	if err != nil {
		fmt.Fprintf(stderr, "tlang: %v\n", err)
		return exitFail
	}
	defer cleanup()

	mode := driver.BuildDebug
	if release {
		mode = driver.BuildRelease
	}

	// Resolve the desired output path.
	//
	//   - build writes a persistent artifact at outFlag (or a derived default).
	//   - run has no persistent artifact. For tcc (explicit --cc tcc or the dev
	//     auto-selection) an empty out selects tcc's in-place -run form, which
	//     executes the program and produces no file. For gcc/clang, run instead
	//     compiles to a temporary executable (in the same temp dir as the C
	//     file, cleaned up on return) and executes it; an empty out would make
	//     the gcc/clang program step carry an invalid empty `-o`.
	out := ""
	if isBuild {
		out = outFlag
		if out == "" {
			out = defaultOutput(path)
		}
	} else if kind == driver.CompilerGCC || kind == driver.CompilerClang {
		out = filepath.Join(filepath.Dir(programC), baseName(path)+".run")
	}

	d := newDriver()
	plan, err := d.Plan(res.info, mode, kind, programC, out)
	if err != nil {
		return reportPlanError(err, stderr)
	}

	// Execute the assembled plan: extract the runtime and run each step. On
	// success build has produced the executable at plan.Output and run has
	// executed the program (tcc -run in place, or the compiled artifact).
	verb := "run"
	if isBuild {
		verb = "build"
	}
	if err := plan.Run(context.Background()); err != nil {
		fmt.Fprintf(stderr, "tlang: %s failed: %v\n", verb, err)
		return exitFail
	}

	// For a gcc/clang run the plan produced an executable at plan.Output but did
	// not execute it (only tcc's -run form runs in place). Execute it now.
	if !isBuild && plan.Output != "" {
		runCmd := exec.CommandContext(context.Background(), plan.Output)
		runCmd.Stdout = stdout
		runCmd.Stderr = stderr
		if err := runCmd.Run(); err != nil {
			fmt.Fprintf(stderr, "tlang: run failed: %v\n", err)
			return exitFail
		}
	}

	if isBuild {
		fmt.Fprintf(stderr, "tlang: built %s\n", plan.Output)
	}
	return exitOK
}

// reportPlanError maps a driver.Plan error to a user-facing stderr message and
// the exitFail code. The three driver sentinels are each matched via errors.Is
// and given a distinct, actionable message; anything else is reported
// generically.
func reportPlanError(err error, stderr io.Writer) int {
	switch {
	case errors.Is(err, driver.ErrNoEntry):
		fmt.Fprintln(stderr, "tlang: the program has no entry point: define exactly one of `fn main(): void` or `fn route_dispatcher(ctx: Context): void`.")
	case errors.Is(err, driver.ErrNoCompiler):
		fmt.Fprintln(stderr, "tlang: no C compiler found on PATH (looked for gcc, clang, tcc).")
	case errors.Is(err, driver.ErrNoArchiver):
		fmt.Fprintln(stderr, "tlang: no `ar` archiver found on PATH (required for the gcc/clang archive build).")
	default:
		fmt.Fprintf(stderr, "tlang: driver: %v\n", err)
	}
	return exitFail
}

// writeTempC writes the emitted C translation unit to a temporary .c file whose
// base name derives from the source, and returns the file path (programC, which
// the driver takes as a path, not bytes) and a cleanup func the caller defers.
func writeTempC(srcPath string, cbytes []byte) (programC string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "tlang-build-")
	if err != nil {
		return "", func() {}, fmt.Errorf("cannot create temp dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(dir) }

	programC = filepath.Join(dir, baseName(srcPath)+".c")
	if err := os.WriteFile(programC, cbytes, 0o644); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("cannot write %s: %w", programC, err)
	}
	return programC, cleanup, nil
}

// defaultOutput derives the default build output path from the source path: the
// base name without its extension (e.g. "app.ts" -> "app").
func defaultOutput(srcPath string) string {
	return baseName(srcPath)
}

// baseName returns the file's base name with any extension removed.
func baseName(srcPath string) string {
	base := filepath.Base(srcPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" {
		base = "program"
	}
	return base
}
