package driver

import "path/filepath"

// BuildMode selects the optimization level.
type BuildMode int

const (
	// BuildDebug is the default: -O2 -g.
	BuildDebug BuildMode = iota
	// BuildRelease is -O3 -DNDEBUG (plus -flto for clang).
	BuildRelease
)

// FlagSet is the assembled, inspectable flag set for one build. Fields are
// ordered argv fragments; nothing is shell-interpolated.
type FlagSet struct {
	// Compile holds the compile-side flags in deterministic order.
	Compile []string
	// Link holds the link-side flags.
	Link []string
}

// AssembleFlags builds the FlagSet for compiling the generated program together
// with the runtime, per docs/RUNTIME.md §7.1/§7.2.
//
//	srcRoot = RuntimeCache.SourceRoot(): adds -I<srcRoot>/include -I<srcRoot>/src
//	kind    = chosen compiler: all get -Wall; gcc/clang also get -Wextra;
//	          clang in BuildRelease also gets -flto
//	mode    = BuildDebug (-O2 -g) | BuildRelease (-O3 -DNDEBUG)
//	usesDB  = info.UsesDB
//	pq      = PQConfig (its IncludeDir is added only when usesDB)
//
// Compile order: standard flags, include dirs, Postgres include (DB only),
// warnings, optimization (incl. -flto when applicable), -DTLANG_NO_PG (no-DB).
// -fwrapv and -lm are always present. No -Werror of any kind and no -pedantic
// are ever emitted for the program+runtime build.
func AssembleFlags(srcRoot string, kind CompilerKind, mode BuildMode, usesDB bool, pq PQConfig) FlagSet {
	var fs FlagSet

	// Standard flags (always).
	fs.Compile = append(fs.Compile,
		"-std=c11",
		"-D_GNU_SOURCE",
		"-fwrapv",
		"-I"+filepath.Join(srcRoot, "include"),
		"-I"+filepath.Join(srcRoot, "src"),
	)

	// Postgres include dir, DB only.
	if usesDB {
		fs.Compile = append(fs.Compile, "-I"+pq.IncludeDir)
	}

	// Warnings: -Wall for every compiler, -Wextra for gcc/clang only.
	fs.Compile = append(fs.Compile, "-Wall")
	if kind == CompilerGCC || kind == CompilerClang {
		fs.Compile = append(fs.Compile, "-Wextra")
	}

	// Optimization.
	switch mode {
	case BuildRelease:
		fs.Compile = append(fs.Compile, "-O3", "-DNDEBUG")
		if kind == CompilerClang {
			fs.Compile = append(fs.Compile, "-flto")
		}
	default:
		fs.Compile = append(fs.Compile, "-O2", "-g")
	}

	// DB define, no-DB only (mutually exclusive and total with the DB branch).
	if !usesDB {
		fs.Compile = append(fs.Compile, "-DTLANG_NO_PG")
	}

	// Link: always -lpthread -lm; add -lpq when DB.
	fs.Link = append(fs.Link, "-lpthread", "-lm")
	if usesDB {
		fs.Link = append(fs.Link, "-lpq")
	}

	return fs
}

// hasFlag reports whether args contains the exact flag.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
