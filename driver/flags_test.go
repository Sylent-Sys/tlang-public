package driver

import (
	"strings"
	"testing"
)

// containsSubseq reports whether want appears as an ordered subsequence of got.
func containsSubseq(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func contains(got []string, s string) bool { return hasFlag(got, s) }

func TestAlwaysFlagsPresentAndOrdered(t *testing.T) {
	for _, kind := range []CompilerKind{CompilerGCC, CompilerClang, CompilerTCC} {
		for _, mode := range []BuildMode{BuildDebug, BuildRelease} {
			for _, db := range []bool{false, true} {
				fs := AssembleFlags("/root", kind, mode, db, PQConfig{IncludeDir: "/pg"})
				want := []string{"-std=c11", "-D_GNU_SOURCE", "-fwrapv"}
				if !containsSubseq(fs.Compile, want) {
					t.Errorf("kind=%v mode=%v db=%v: missing/misordered standard flags in %v", kind, mode, db, fs.Compile)
				}
				if !contains(fs.Compile, "-fwrapv") {
					t.Errorf("kind=%v mode=%v db=%v: -fwrapv absent", kind, mode, db)
				}
				if !contains(fs.Link, "-lm") {
					t.Errorf("kind=%v mode=%v db=%v: -lm absent from link", kind, mode, db)
				}
				if !contains(fs.Link, "-lpthread") {
					t.Errorf("kind=%v mode=%v db=%v: -lpthread absent from link", kind, mode, db)
				}
				// Include dirs present.
				if !containsSubseq(fs.Compile, []string{"-fwrapv"}) {
					t.Error("sanity")
				}
				var haveInc bool
				for _, a := range fs.Compile {
					if strings.HasPrefix(a, "-I") && strings.Contains(a, "include") {
						haveInc = true
					}
				}
				if !haveInc {
					t.Errorf("kind=%v: no include -I flag in %v", kind, fs.Compile)
				}
				// No -Werror of any kind, no -pedantic.
				for _, a := range fs.Compile {
					if a == "-Werror" || strings.HasPrefix(a, "-Werror=") || a == "-pedantic" {
						t.Errorf("kind=%v mode=%v db=%v: forbidden flag %q present", kind, mode, db, a)
					}
				}
			}
		}
	}
}

func TestDBBranchMutuallyExclusive(t *testing.T) {
	// usesDB == true.
	db := AssembleFlags("/root", CompilerGCC, BuildDebug, true, PQConfig{IncludeDir: "/pg/inc"})
	if !contains(db.Link, "-lpq") {
		t.Error("usesDB: -lpq absent from link")
	}
	if !contains(db.Compile, "-I/pg/inc") {
		t.Errorf("usesDB: -I/pg/inc absent from compile: %v", db.Compile)
	}
	if contains(db.Compile, "-DTLANG_NO_PG") {
		t.Error("usesDB: -DTLANG_NO_PG present, want absent")
	}

	// usesDB == false.
	no := AssembleFlags("/root", CompilerGCC, BuildDebug, false, PQConfig{IncludeDir: "/pg/inc"})
	if contains(no.Link, "-lpq") {
		t.Error("no DB: -lpq present, want absent")
	}
	if contains(no.Compile, "-I/pg/inc") {
		t.Error("no DB: Postgres include present, want absent")
	}
	if !contains(no.Compile, "-DTLANG_NO_PG") {
		t.Error("no DB: -DTLANG_NO_PG absent, want present")
	}
}

func TestWarningFlags(t *testing.T) {
	for _, kind := range []CompilerKind{CompilerGCC, CompilerClang, CompilerTCC} {
		fs := AssembleFlags("/root", kind, BuildDebug, false, PQConfig{})
		if !contains(fs.Compile, "-Wall") {
			t.Errorf("kind=%v: -Wall absent", kind)
		}
		hasExtra := contains(fs.Compile, "-Wextra")
		wantExtra := kind == CompilerGCC || kind == CompilerClang
		if hasExtra != wantExtra {
			t.Errorf("kind=%v: -Wextra present=%v, want %v", kind, hasExtra, wantExtra)
		}
	}
}

func TestOptimizationFlags(t *testing.T) {
	dbg := AssembleFlags("/root", CompilerGCC, BuildDebug, false, PQConfig{})
	if !contains(dbg.Compile, "-O2") || !contains(dbg.Compile, "-g") {
		t.Errorf("debug: want -O2 -g, got %v", dbg.Compile)
	}
	if contains(dbg.Compile, "-O3") || contains(dbg.Compile, "-DNDEBUG") {
		t.Error("debug: release flags present")
	}

	rel := AssembleFlags("/root", CompilerGCC, BuildRelease, false, PQConfig{})
	if !contains(rel.Compile, "-O3") || !contains(rel.Compile, "-DNDEBUG") {
		t.Errorf("release: want -O3 -DNDEBUG, got %v", rel.Compile)
	}
	if contains(rel.Compile, "-O2") || contains(rel.Compile, "-g") {
		t.Error("release: debug flags present")
	}
}

func TestLTOOnlyClangRelease(t *testing.T) {
	cases := []struct {
		kind CompilerKind
		mode BuildMode
		want bool
	}{
		{CompilerClang, BuildRelease, true},
		{CompilerClang, BuildDebug, false},
		{CompilerGCC, BuildRelease, false},
		{CompilerGCC, BuildDebug, false},
		{CompilerTCC, BuildRelease, false},
		{CompilerTCC, BuildDebug, false},
	}
	for _, c := range cases {
		fs := AssembleFlags("/root", c.kind, c.mode, false, PQConfig{})
		got := contains(fs.Compile, "-flto")
		if got != c.want {
			t.Errorf("kind=%v mode=%v: -flto present=%v, want %v", c.kind, c.mode, got, c.want)
		}
	}
}
