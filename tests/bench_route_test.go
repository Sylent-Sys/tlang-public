//go:build e2e

// Unit tests for the benchmark route table and TLANG_BENCH_PATH parsing added
// for the real-work routes (/json, /work). These are pure-function checks of
// the config/route plumbing in bench_shared.go — no server is launched — so
// they run in the normal e2e `go test` without TLANG_BENCH / TLANG_BENCH_COMPARE.

package tests

import (
	"encoding/json"
	"testing"
)

// TestBenchRouteResolve asserts resolveBenchRoute returns the matching
// reference body for each known route and reports not-ok for an unknown route
// (the pure helper backing the loud parseBenchConfig t.Fatalf path).
func TestBenchRouteResolve(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/bench", benchRefBody},
		{"/json", jsonRefBody},
		{"/work", workRefBody},
	}
	for _, c := range cases {
		got, ok := resolveBenchRoute(c.path)
		if !ok {
			t.Fatalf("resolveBenchRoute(%q): ok=false, want true", c.path)
		}
		if got != c.want {
			t.Fatalf("resolveBenchRoute(%q): body mismatch", c.path)
		}
	}
	if _, ok := resolveBenchRoute("/does-not-exist"); ok {
		t.Fatalf("resolveBenchRoute(unknown): ok=true, want false")
	}
}

// TestBenchConfigPath asserts parseBenchConfig honors TLANG_BENCH_PATH: unset
// leaves cfg.path == "" (the "unset" sentinel), and each known value sets the
// matching path + reference body. t.Setenv isolates the env mutation.
func TestBenchConfigPath(t *testing.T) {
	t.Run("unset is empty sentinel", func(t *testing.T) {
		t.Setenv("TLANG_BENCH_PATH", "")
		cfg := parseBenchConfig(t)
		if cfg.path != "" {
			t.Fatalf("unset TLANG_BENCH_PATH: cfg.path=%q, want \"\"", cfg.path)
		}
		if cfg.refBody != "" {
			t.Fatalf("unset TLANG_BENCH_PATH: cfg.refBody non-empty, want empty")
		}
	})
	for _, path := range []string{"/bench", "/json", "/work"} {
		t.Run("set "+path, func(t *testing.T) {
			t.Setenv("TLANG_BENCH_PATH", path)
			cfg := parseBenchConfig(t)
			if cfg.path != path {
				t.Fatalf("TLANG_BENCH_PATH=%q: cfg.path=%q", path, cfg.path)
			}
			want, _ := resolveBenchRoute(path)
			if cfg.refBody != want {
				t.Fatalf("TLANG_BENCH_PATH=%q: cfg.refBody mismatch", path)
			}
		})
	}
}

// TestBenchRouteBodies guards against a transcription typo in the reference
// constants: every route body must be valid JSON, and the two real-work bodies
// must be exactly 716 / 1407 bytes (the measured byte-identity targets).
func TestBenchRouteBodies(t *testing.T) {
	for path, body := range benchRouteBodies {
		if !json.Valid([]byte(body)) {
			t.Fatalf("benchRouteBodies[%q] is not valid JSON", path)
		}
	}
	if len(jsonRefBody) != 716 {
		t.Fatalf("len(jsonRefBody)=%d, want 716", len(jsonRefBody))
	}
	if len(workRefBody) != 1407 {
		t.Fatalf("len(workRefBody)=%d, want 1407", len(workRefBody))
	}
}
