package driver

import (
	"os/exec"
	"strings"
)

// PQConfig is the result of libpq discovery.
type PQConfig struct {
	// IncludeDir is the Postgres header directory to add with -I when the
	// program uses the database. It is always set to a usable value: the output
	// of `pg_config --includedir`, or the fallback when pg_config is absent.
	IncludeDir string
	// FromPgConfig reports whether IncludeDir came from pg_config (true) or from
	// the fallback (false).
	FromPgConfig bool
	// Linkable reports whether libpq appears usable for linking (-lpq). It gates
	// whether a DB build can be attempted; it does not change the flag set
	// (flags are driven by info.UsesDB, see AssembleFlags).
	Linkable bool
}

// DefaultPGIncludeDir is the fallback include dir when pg_config is absent.
// Linux fallback by design: the DB build path only runs on Linux (run/build
// need Linux). On other hosts this value is only ever compared as an opaque
// string in hermetic tests, never dereferenced as a real path, so the POSIX
// form is not a portability bug.
const DefaultPGIncludeDir = "/usr/include/postgresql"

// PgConfigFunc runs `pg_config` with the given args and returns its trimmed
// stdout, or an error if pg_config is absent or fails. The real implementation
// is exec.Command("pg_config", args...).Output with the output trimmed of
// trailing whitespace.
type PgConfigFunc func(args ...string) (string, error)

// defaultPgConfig runs the real pg_config and trims the output.
func defaultPgConfig(args ...string) (string, error) {
	out, err := exec.Command("pg_config", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DiscoverPQ determines the Postgres include dir and whether libpq is linkable.
// A nil pgConfig uses the real pg_config runner.
//
//   - It runs pgConfig("--includedir"); on success with a non-empty trimmed
//     result IncludeDir is that path, FromPgConfig is true, and Linkable is
//     true. On any error (pg_config absent or non-zero) OR an empty /
//     whitespace-only result it falls back to DefaultPGIncludeDir with
//     FromPgConfig false and Linkable false.
//
// Linkability is intentionally conservative: a genuine link probe needs a C
// compiler and real libpq, which the host may lack, so the default derives
// Linkable from pg_config success. A caller that wants a real probe injects
// one. The driver never fabricates a link probe against a missing runtime.
func DiscoverPQ(pgConfig PgConfigFunc) PQConfig {
	if pgConfig == nil {
		pgConfig = defaultPgConfig
	}
	out, err := pgConfig("--includedir")
	if err == nil {
		if dir := strings.TrimSpace(out); dir != "" {
			return PQConfig{IncludeDir: dir, FromPgConfig: true, Linkable: true}
		}
	}
	return PQConfig{IncludeDir: DefaultPGIncludeDir, FromPgConfig: false, Linkable: false}
}
