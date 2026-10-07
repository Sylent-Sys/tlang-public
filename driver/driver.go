package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"tlang/runtime"
	"tlang/types"
)

// CheckEntry enforces the exactly-one-entry rule against the checker's output.
// It returns nil for a buildable program (Kind ProgramServer or ProgramScript
// with a non-nil Entry) and ErrNoEntry otherwise. It is a pure function of info
// and never panics; a nil info and an Entry==nil are treated as no entry.
//
// The checker accepts a program with neither main nor route_dispatcher silently
// (Kind ProgramUnknown, Entry nil, no E-ENTRY), and codegen emits a compilable
// entry-less unit for it. This gate is where the driver rejects that case
// (HANDOVER gap #2). The defensive Entry!=nil check keeps the invariant "a
// buildable program has a non-nil Entry" owned at the driver layer.
func CheckEntry(info *types.Info) error {
	if info == nil || info.Entry == nil {
		return ErrNoEntry
	}
	switch info.Kind {
	case types.ProgramServer, types.ProgramScript:
		return nil
	default:
		return ErrNoEntry
	}
}

// Driver holds the discovered toolchain, libpq config, and cache, plus the
// injection seams. The zero value is not usable; build one with New.
type Driver struct {
	// Toolchain is the discovered set of C compilers.
	Toolchain Toolchain
	// PQ is the discovered libpq configuration.
	PQ PQConfig
	// Cache is the runtime cache model.
	Cache RuntimeCache

	// lookPath is the PATH-lookup seam used by AssemblePlan (archiver "ar").
	lookPath LookPathFunc
}

// Options carry the injection seams and overrides for New. A nil field uses the
// real implementation.
type Options struct {
	// LookPath defaults to exec.LookPath.
	LookPath LookPathFunc
	// PgConfig defaults to the real pg_config runner.
	PgConfig PgConfigFunc
	// CacheRoot defaults to DefaultCacheRoot().
	CacheRoot string
}

// New performs discovery (toolchain + libpq) through the seams in opts and
// returns a Driver. It never invokes a C compiler and touches the filesystem
// only to compute the cache root (not to extract). It is fully hermetic under
// injected seams.
func New(opts Options) *Driver {
	root := opts.CacheRoot
	if root == "" {
		root = DefaultCacheRoot()
	}
	return &Driver{
		Toolchain: Discover(opts.LookPath),
		PQ:        DiscoverPQ(opts.PgConfig),
		Cache:     NewRuntimeCache(root),
		lookPath:  opts.LookPath,
	}
}

// Plan is the high-level entry: enforce the entry gate, choose/validate the
// compiler for the mode, and assemble the BuildPlan. It executes nothing.
//
//  1. CheckEntry(info): reject ProgramUnknown with ErrNoEntry.
//  2. Pick the compiler: explicit kind if present in the toolchain; else
//     Toolchain.Release() for BuildRelease or Toolchain.Dev() otherwise;
//     ErrNoCompiler if none.
//  3. AssemblePlan(compiler, mode, info, programC, out).
func (d *Driver) Plan(info *types.Info, mode BuildMode, kind CompilerKind, programC, out string) (BuildPlan, error) {
	if err := CheckEntry(info); err != nil {
		return BuildPlan{}, err
	}

	var c Compiler
	var ok bool
	if kind != 0 {
		c, ok = d.Toolchain.Lookup(kind)
	} else if mode == BuildRelease {
		c, ok = d.Toolchain.Release()
	} else {
		c, ok = d.Toolchain.Dev()
	}
	if !ok {
		return BuildPlan{}, ErrNoCompiler
	}

	return d.AssemblePlan(c, mode, info, programC, out)
}

// AssemblePlan builds the BuildPlan for a program using compiler c. It calls
// AssembleFlags, resolves runtime.CSources() under Cache.SourceRoot(), and
// emits the per-compiler step list. It executes nothing.
//
// programC is the path of the codegen.Emit output the CLI has written (the
// path, not the bytes, enters the argv); out is the desired executable path.
// For tcc, the dev (-run) form is selected when out is empty, and the archive
// (build) form otherwise. When the gcc/clang archive branch is selected and
// there are runtime sources to archive but "ar" cannot be resolved, it returns
// ErrNoArchiver and no plan.
func (d *Driver) AssemblePlan(c Compiler, mode BuildMode, info *types.Info, programC, out string) (BuildPlan, error) {
	flags := AssembleFlags(d.Cache.SourceRoot(), c.Kind, mode, info.UsesDB, d.PQ)
	tccRun := c.Kind == CompilerTCC && out == ""
	return assemblePlan(c, mode, flags, d.Cache, runtime.CSources(), programC, out, d.lookPath, tccRun)
}

// LinkCheckFloatMod compiles and links a tiny program that calls tlang_mod_f64
// against the real runtime, using the driver's own FlagSet (which always
// includes -lm), and reports whether linking succeeds. It is the driver's home
// for the float-% link check deferred from the codegen stage.
//
// It resolves the compiler of kind in d.Toolchain (ErrNoCompiler if absent),
// extracts the runtime into the cache, writes a tiny tlang_mod_f64 user to a
// temp file, and compiles+links it together with runtime.CSources() into a temp
// executable, mirroring the compile pattern BuildPlan.Run uses. It executes no
// build steps beyond this single compile+link invocation and logs nothing; the
// caller presents the returned error.
func LinkCheckFloatMod(d *Driver, kind CompilerKind) error {
	c, ok := d.Toolchain.Lookup(kind)
	if !ok {
		return ErrNoCompiler
	}
	if err := d.Cache.Extract(); err != nil {
		return fmt.Errorf("driver: extract runtime: %w", err)
	}

	dir, err := os.MkdirTemp("", "tlang-linkcheck-")
	if err != nil {
		return fmt.Errorf("driver: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	programC := filepath.Join(dir, "floatmod_check.c")
	const user = "#include \"tlang.h\"\n" +
		"int main(void) {\n" +
		"\treturn tlang_mod_f64(5.0, 2.0) == 1.0 ? 0 : 1;\n" +
		"}\n"
	if err := os.WriteFile(programC, []byte(user), 0o644); err != nil {
		return fmt.Errorf("driver: write link-check user: %w", err)
	}

	srcRoot := d.Cache.SourceRoot()
	flags := AssembleFlags(srcRoot, kind, BuildDebug, false, d.PQ)

	out := filepath.Join(dir, "floatmod_check")
	argv := []string{}
	argv = append(argv, flags.Compile...)
	for _, s := range runtime.CSources() {
		argv = append(argv, filepath.Join(srcRoot, filepath.FromSlash(s)))
	}
	argv = append(argv, programC)
	argv = append(argv, "-o", out)
	argv = append(argv, flags.Link...)

	cmd := exec.CommandContext(context.Background(), c.Path, argv...)
	cmd.Dir = srcRoot
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("driver: float-mod link check: %w", err)
	}
	return nil
}
