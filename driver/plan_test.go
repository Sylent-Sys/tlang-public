package driver

import (
	"errors"
	"path/filepath"
	"testing"
)

var fakeSources = []string{"src/arena.c", "src/strings.c"}

func gccCompiler() Compiler   { return Compiler{Kind: CompilerGCC, Path: "/usr/bin/gcc"} }
func clangCompiler() Compiler { return Compiler{Kind: CompilerClang, Path: "/usr/bin/clang"} }
func tccCompiler() Compiler   { return Compiler{Kind: CompilerTCC, Path: "/usr/bin/tcc"} }

func cache() RuntimeCache { return NewRuntimeCache("/cacheroot") }

// arPresent/arAbsent are LookPathFuncs for the archiver seam.
func arPresent(name string) (string, error) {
	if name == "ar" {
		return "/usr/bin/ar", nil
	}
	return "", errors.New("absent")
}

func arAbsent(name string) (string, error) { return "", errors.New("absent") }

func lastStep(p BuildPlan) Step { return p.Steps[len(p.Steps)-1] }

// assertSingleCompiler checks the ABI invariant: every StepCompile uses the
// plan's compiler path, and any StepArchive uses the plan's archiver.
func assertSingleCompiler(t *testing.T, p BuildPlan) {
	t.Helper()
	for i, s := range p.Steps {
		switch s.Tool {
		case StepCompile:
			if s.Argv[0] != p.Compiler.Path {
				t.Errorf("step %d (compile) Argv[0]=%q, want compiler %q", i, s.Argv[0], p.Compiler.Path)
			}
		case StepArchive:
			if s.Argv[0] != p.Archiver {
				t.Errorf("step %d (archive) Argv[0]=%q, want archiver %q", i, s.Argv[0], p.Archiver)
			}
		}
	}
}

func TestPlanGCCArchiveForm(t *testing.T) {
	flags := AssembleFlags(cache().SourceRoot(), CompilerGCC, BuildDebug, false, PQConfig{})
	p, err := assemblePlan(gccCompiler(), BuildDebug, flags, cache(), fakeSources, "/tmp/prog.c", "/tmp/prog", arPresent, false)
	if err != nil {
		t.Fatalf("assemblePlan error: %v", err)
	}
	// 2 compile steps + 1 archive + 1 program compile+link = 4.
	if len(p.Steps) != 4 {
		t.Fatalf("got %d steps, want 4: %+v", len(p.Steps), p.Steps)
	}
	var archives int
	for _, s := range p.Steps {
		if s.Tool == StepArchive {
			archives++
		}
	}
	if archives != 1 {
		t.Errorf("got %d archive steps, want 1", archives)
	}
	if p.Archiver != "/usr/bin/ar" {
		t.Errorf("Archiver = %q, want /usr/bin/ar", p.Archiver)
	}
	if p.Output != "/tmp/prog" {
		t.Errorf("Output = %q, want /tmp/prog", p.Output)
	}
	for _, s := range p.Steps {
		if s.Dir != cache().SourceRoot() {
			t.Errorf("step Dir = %q, want SourceRoot", s.Dir)
		}
	}
	assertSingleCompiler(t, p)
}

func TestPlanClangLTOSingleInvocation(t *testing.T) {
	flags := AssembleFlags(cache().SourceRoot(), CompilerClang, BuildRelease, false, PQConfig{})
	// -flto present -> single invocation, no archiver needed even if absent.
	p, err := assemblePlan(clangCompiler(), BuildRelease, flags, cache(), fakeSources, "/tmp/prog.c", "/tmp/prog", arAbsent, false)
	if err != nil {
		t.Fatalf("assemblePlan error: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("got %d steps, want 1: %+v", len(p.Steps), p.Steps)
	}
	s := p.Steps[0]
	if s.Tool != StepCompile {
		t.Error("LTO step is not StepCompile")
	}
	if !hasFlag(s.Argv, "-flto") {
		t.Errorf("LTO step argv missing -flto: %v", s.Argv)
	}
	for _, src := range fakeSources {
		// each runtime source is resolved to an absolute path ending in its
		// OS-native base name (e.g. "arena.c").
		base := filepath.Base(filepath.FromSlash(src))
		found := false
		for _, a := range s.Argv {
			if filepath.Base(a) == base {
				found = true
			}
		}
		if !found {
			t.Errorf("LTO argv missing source %q: %v", src, s.Argv)
		}
	}
	if p.Archiver != "" {
		t.Errorf("Archiver = %q, want empty on LTO branch", p.Archiver)
	}
	if p.Output != "/tmp/prog" {
		t.Errorf("Output = %q, want /tmp/prog", p.Output)
	}
	assertSingleCompiler(t, p)
}

func TestPlanTCCRunForm(t *testing.T) {
	flags := AssembleFlags(cache().SourceRoot(), CompilerTCC, BuildDebug, false, PQConfig{})
	// tccRun = true; ar absent must not matter.
	p, err := assemblePlan(tccCompiler(), BuildDebug, flags, cache(), fakeSources, "/tmp/prog.c", "", arAbsent, true)
	if err != nil {
		t.Fatalf("assemblePlan error: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(p.Steps))
	}
	argv := p.Steps[0].Argv
	if argv[len(argv)-1] != "-run" {
		t.Errorf("last token = %q, want -run", argv[len(argv)-1])
	}
	if p.Output != "" {
		t.Errorf("Output = %q, want empty for tcc -run", p.Output)
	}
	if p.Archiver != "" {
		t.Error("Archiver set on tcc -run branch")
	}
	assertSingleCompiler(t, p)
}

func TestPlanTCCArchiveForm(t *testing.T) {
	flags := AssembleFlags(cache().SourceRoot(), CompilerTCC, BuildDebug, false, PQConfig{})
	p, err := assemblePlan(tccCompiler(), BuildDebug, flags, cache(), fakeSources, "/tmp/prog.c", "/tmp/prog", arPresent, false)
	if err != nil {
		t.Fatalf("assemblePlan error: %v", err)
	}
	var archives int
	for _, s := range p.Steps {
		if s.Tool == StepArchive {
			archives++
		}
	}
	if archives != 1 {
		t.Errorf("tcc archive form: got %d archive steps, want 1", archives)
	}
	if p.Output != "/tmp/prog" {
		t.Errorf("Output = %q, want /tmp/prog", p.Output)
	}
	assertSingleCompiler(t, p)
}

func TestPlanArchiverAbsentFails(t *testing.T) {
	flags := AssembleFlags(cache().SourceRoot(), CompilerGCC, BuildDebug, false, PQConfig{})
	_, err := assemblePlan(gccCompiler(), BuildDebug, flags, cache(), fakeSources, "/tmp/prog.c", "/tmp/prog", arAbsent, false)
	if !errors.Is(err, ErrNoArchiver) {
		t.Fatalf("err = %v, want ErrNoArchiver", err)
	}
}

func TestPlanEmptySourcesStillAssemblesProgramStep(t *testing.T) {
	// Today's reality: runtime.CSources() is empty. The gcc branch should still
	// produce the program compile+link step and need no archiver.
	flags := AssembleFlags(cache().SourceRoot(), CompilerGCC, BuildDebug, false, PQConfig{})
	p, err := assemblePlan(gccCompiler(), BuildDebug, flags, cache(), nil, "/tmp/prog.c", "/tmp/prog", arAbsent, false)
	if err != nil {
		t.Fatalf("assemblePlan error: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("got %d steps, want 1 (program only)", len(p.Steps))
	}
	if p.Steps[0].Tool != StepCompile || p.Steps[0].Argv[0] != gccCompiler().Path {
		t.Error("program step is not a gcc compile step")
	}
	if p.Archiver != "" {
		t.Error("Archiver set with no sources to archive")
	}
	if p.Output != "/tmp/prog" {
		t.Errorf("Output = %q", p.Output)
	}
}
