package driver

import (
	"errors"
	"testing"

	"tlang/types"
)

func TestCheckEntry(t *testing.T) {
	cases := []struct {
		name    string
		info    *types.Info
		wantErr bool
	}{
		{"nil info", nil, true},
		{"unknown no entry", &types.Info{Kind: types.ProgramUnknown, Entry: nil}, true},
		{"server with entry", &types.Info{Kind: types.ProgramServer, Entry: &types.Func{}}, false},
		{"script with entry", &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}}, false},
		{"server no entry (defensive)", &types.Info{Kind: types.ProgramServer, Entry: nil}, true},
		{"script no entry (defensive)", &types.Info{Kind: types.ProgramScript, Entry: nil}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckEntry(c.info)
			if c.wantErr {
				if !errors.Is(err, ErrNoEntry) {
					t.Errorf("err = %v, want ErrNoEntry", err)
				}
			} else if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

func fullToolchainOpts(root string) Options {
	return Options{
		LookPath: func(name string) (string, error) {
			switch name {
			case "gcc":
				return "/usr/bin/gcc", nil
			case "clang":
				return "/usr/bin/clang", nil
			case "tcc":
				return "/usr/bin/tcc", nil
			case "ar":
				return "/usr/bin/ar", nil
			}
			return "", errors.New("absent")
		},
		PgConfig:  func(args ...string) (string, error) { return "/pg/include", nil },
		CacheRoot: root,
	}
}

func TestNewPopulatesWithoutCompiler(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	if !d.Toolchain.Has(CompilerGCC) {
		t.Error("New did not discover gcc")
	}
	if !d.PQ.FromPgConfig || d.PQ.IncludeDir != "/pg/include" {
		t.Errorf("PQ = %+v, want from pg_config", d.PQ)
	}
	if d.Cache.Hash == "" {
		t.Error("Cache.Hash empty")
	}
}

func TestPlanNoEntry(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	_, err := d.Plan(&types.Info{Kind: types.ProgramUnknown}, BuildDebug, 0, "/tmp/p.c", "/tmp/p")
	if !errors.Is(err, ErrNoEntry) {
		t.Fatalf("err = %v, want ErrNoEntry", err)
	}
}

func TestPlanNoCompiler(t *testing.T) {
	d := New(Options{
		LookPath:  func(name string) (string, error) { return "", errors.New("absent") },
		PgConfig:  func(args ...string) (string, error) { return "", errors.New("absent") },
		CacheRoot: t.TempDir(),
	})
	info := &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}}
	_, err := d.Plan(info, BuildDebug, 0, "/tmp/p.c", "/tmp/p")
	if !errors.Is(err, ErrNoCompiler) {
		t.Fatalf("err = %v, want ErrNoCompiler", err)
	}
}

func TestPlanExplicitKindHonored(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	info := &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}}
	p, err := d.Plan(info, BuildDebug, CompilerClang, "/tmp/p.c", "/tmp/p")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Compiler.Kind != CompilerClang {
		t.Errorf("Compiler.Kind = %v, want clang", p.Compiler.Kind)
	}
}

func TestPlanReleasePicksReleaseCompiler(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	info := &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}}
	// Release prefers gcc.
	p, err := d.Plan(info, BuildRelease, 0, "/tmp/p.c", "/tmp/p")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Compiler.Kind != CompilerGCC {
		t.Errorf("release Compiler.Kind = %v, want gcc", p.Compiler.Kind)
	}
}

func TestPlanDefaultModePicksDevCompiler(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	info := &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}}
	// Dev prefers tcc; default mode + out set -> tcc archive form.
	p, err := d.Plan(info, BuildDebug, 0, "/tmp/p.c", "/tmp/p")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if p.Compiler.Kind != CompilerTCC {
		t.Errorf("dev Compiler.Kind = %v, want tcc", p.Compiler.Kind)
	}
}

func TestPlanFlagsConsistent(t *testing.T) {
	d := New(fullToolchainOpts(t.TempDir()))
	info := &types.Info{Kind: types.ProgramScript, Entry: &types.Func{}, UsesDB: true}
	p, err := d.Plan(info, BuildDebug, CompilerGCC, "/tmp/p.c", "/tmp/p")
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if !hasFlag(p.Flags.Link, "-lpq") {
		t.Error("UsesDB plan missing -lpq")
	}
	if !hasFlag(p.Flags.Compile, "-fwrapv") {
		t.Error("plan missing -fwrapv")
	}
	if len(p.Steps) == 0 {
		t.Error("plan has no steps")
	}
}
