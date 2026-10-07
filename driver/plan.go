package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// StepTool classifies a Step's external tool for invariant checking.
type StepTool int

const (
	// StepCompile is a compiler invocation; Argv[0] is the chosen compiler path.
	StepCompile StepTool = iota
	// StepArchive is the one archiver (ar) invocation on the gcc/clang archive
	// branch; Argv[0] is the archiver path.
	StepArchive
)

// Step is one external command the build would run, assembled but not executed.
// It is fully inspectable: Argv[0] is the external tool (the chosen compiler, or
// the archiver "ar" for the single archive step), the rest are discrete
// arguments. Nothing is shell-interpolated.
type Step struct {
	// Argv is the full command line as discrete elements.
	Argv []string
	// Dir is the working directory the command runs in.
	Dir string
	// Desc is a human description, e.g. "compile runtime src/arena.c".
	Desc string
	// Tool classifies the step so the single-compiler invariant can be asserted.
	Tool StepTool
}

// BuildPlan is the complete, ordered, non-executing plan for one build.
type BuildPlan struct {
	// Compiler is the single compiler used for every compile step (ABI rule).
	Compiler Compiler
	// Archiver is the resolved path of "ar", set only when the plan has an
	// archive step (gcc/clang non-LTO); "" otherwise.
	Archiver string
	// Mode is the build mode.
	Mode BuildMode
	// Flags is the assembled flag set.
	Flags FlagSet
	// Cache is the runtime cache model.
	Cache RuntimeCache
	// Steps are the runtime compile steps, the optional archive step, then the
	// program compile+link step, in order.
	Steps []Step
	// Output is the path of the executable the plan would produce ("" for the
	// tcc -run in-place path, which produces no artifact).
	Output string
}

// assemblePlan builds the per-compiler Step list. It is the internal assembler
// so csources (today empty from runtime.CSources()) and lookPath are injectable
// for argv tests. It executes nothing.
//
// Branch selection keys solely on whether flags.Compile contains -flto (the
// single source of truth decided in AssembleFlags):
//
//   - tcc: single-invocation -run (dev) form, or an archive (build) form.
//   - clang + -flto: single clang compile+link invocation (no archive step).
//   - gcc / clang non-LTO: one compile step per source into ArtifactDir, one
//     archive step (ar), then the program compile+link step.
func assemblePlan(c Compiler, mode BuildMode, flags FlagSet, cache RuntimeCache, csources []string, programC, out string, lookPath LookPathFunc, tccRun bool) (BuildPlan, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	plan := BuildPlan{
		Compiler: c,
		Mode:     mode,
		Flags:    flags,
		Cache:    cache,
	}
	srcRoot := cache.SourceRoot()

	// Absolute runtime source paths as discrete argv elements (no glob).
	absSources := make([]string, len(csources))
	for i, s := range csources {
		absSources[i] = filepath.Join(srcRoot, filepath.FromSlash(s))
	}

	if c.Kind == CompilerTCC {
		if tccRun {
			// tcc fast dev path: one invocation, -run is the FINAL token and
			// nothing follows it (run in place, no artifact).
			argv := []string{c.Path}
			argv = append(argv, flags.Compile...)
			argv = append(argv, absSources...)
			argv = append(argv, programC)
			argv = append(argv, flags.Link...)
			argv = append(argv, "-run")
			plan.Steps = append(plan.Steps, Step{
				Argv: argv,
				Dir:  srcRoot,
				Desc: "tcc run program in place",
				Tool: StepCompile,
			})
			plan.Output = ""
			return plan, nil
		}
		// tcc archive (build) form mirrors the gcc/clang archive branch with
		// tcc as the compiler.
		return assembleArchivePlan(plan, c, flags, cache, absSources, programC, out, lookPath)
	}

	if hasFlag(flags.Compile, "-flto") {
		// clang LTO single invocation: runtime sources + program + link in one
		// clang command carrying -flto; no archive step, no archiver lookup.
		argv := []string{c.Path}
		argv = append(argv, flags.Compile...)
		argv = append(argv, absSources...)
		argv = append(argv, programC)
		argv = append(argv, "-o", out)
		argv = append(argv, flags.Link...)
		plan.Steps = append(plan.Steps, Step{
			Argv: argv,
			Dir:  srcRoot,
			Desc: "clang LTO compile and link program with runtime",
			Tool: StepCompile,
		})
		plan.Output = out
		return plan, nil
	}

	// gcc / clang non-LTO archive form.
	return assembleArchivePlan(plan, c, flags, cache, absSources, programC, out, lookPath)
}

// assembleArchivePlan builds the archive-form Step list: one compile step per
// runtime source into ArtifactDir, one ar archive step, then the program
// compile+link step. Resolves "ar" through lookPath; returns ErrNoArchiver when
// absent.
func assembleArchivePlan(plan BuildPlan, c Compiler, flags FlagSet, cache RuntimeCache, absSources []string, programC, out string, lookPath LookPathFunc) (BuildPlan, error) {
	artDir := cache.ArtifactDir(c.Kind)

	var objects []string
	for i, src := range absSources {
		obj := filepath.Join(artDir, fmt.Sprintf("rt_%d.o", i))
		objects = append(objects, obj)
		argv := []string{c.Path}
		argv = append(argv, flags.Compile...)
		argv = append(argv, "-c", src, "-o", obj)
		plan.Steps = append(plan.Steps, Step{
			Argv: argv,
			Dir:  cache.SourceRoot(),
			Desc: "compile runtime " + filepath.Base(src),
			Tool: StepCompile,
		})
	}

	if len(objects) > 0 {
		arPath, err := lookPath("ar")
		if err != nil || arPath == "" {
			return BuildPlan{}, ErrNoArchiver
		}
		plan.Archiver = arPath
		archive := filepath.Join(artDir, "libtlangrt.a")
		argv := []string{arPath, "rcs", archive}
		argv = append(argv, objects...)
		plan.Steps = append(plan.Steps, Step{
			Argv: argv,
			Dir:  cache.SourceRoot(),
			Desc: "archive runtime objects into libtlangrt.a",
			Tool: StepArchive,
		})
	}

	// Program compile+link step with the same compiler.
	argv := []string{c.Path}
	argv = append(argv, flags.Compile...)
	argv = append(argv, programC)
	argv = append(argv, objects...)
	argv = append(argv, "-o", out)
	argv = append(argv, flags.Link...)
	plan.Steps = append(plan.Steps, Step{
		Argv: argv,
		Dir:  cache.SourceRoot(),
		Desc: "compile and link program with runtime",
		Tool: StepCompile,
	})
	plan.Output = out
	return plan, nil
}

// Run is the execution layer: it extracts the runtime, then runs each Step as an
// *exec.Cmd built from Argv/Dir, stopping at the first failure and wrapping it.
// It is NOT exercised by tests and is expected to fail until the runtime .c
// files exist (there is nothing to compile). No logging happens inside the
// driver; the CLI presents the returned error.
func (p BuildPlan) Run(ctx context.Context) error {
	if err := p.Cache.Extract(); err != nil {
		return fmt.Errorf("driver: extract runtime: %w", err)
	}
	// The archive-form branch (gcc/clang non-LTO, and tcc build) compiles
	// runtime objects and the libtlangrt.a archive into the per-compiler
	// ArtifactDir, which Extract does not create. Ensure it exists before any
	// compile step tries to write an object into it. The archive step's
	// presence (Archiver != "") is exactly the archive-form marker; the tcc
	// -run and clang-LTO single-invocation branches have no archive step and
	// write only the final executable, so this is a no-op for them.
	if p.Archiver != "" {
		artDir := p.Cache.ArtifactDir(p.Compiler.Kind)
		if err := os.MkdirAll(artDir, 0o755); err != nil {
			return fmt.Errorf("driver: create artifact dir: %w", err)
		}
	}
	for _, step := range p.Steps {
		if len(step.Argv) == 0 {
			continue
		}
		cmd := exec.CommandContext(ctx, step.Argv[0], step.Argv[1:]...)
		cmd.Dir = step.Dir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("driver: %s: %w", step.Desc, err)
		}
	}
	return nil
}
