package driver

import (
	"errors"
	"testing"
)

func TestDiscoverPQFromPgConfig(t *testing.T) {
	pq := DiscoverPQ(func(args ...string) (string, error) {
		if len(args) != 1 || args[0] != "--includedir" {
			t.Fatalf("unexpected pg_config args: %v", args)
		}
		return "  /custom/pg/include  ", nil
	})
	if pq.IncludeDir != "/custom/pg/include" {
		t.Errorf("IncludeDir = %q, want trimmed %q", pq.IncludeDir, "/custom/pg/include")
	}
	if !pq.FromPgConfig {
		t.Error("FromPgConfig = false, want true")
	}
	if !pq.Linkable {
		t.Error("Linkable = false, want true")
	}
}

func TestDiscoverPQErrorFallback(t *testing.T) {
	pq := DiscoverPQ(func(args ...string) (string, error) {
		return "", errors.New("pg_config: not found")
	})
	if pq.IncludeDir != DefaultPGIncludeDir {
		t.Errorf("IncludeDir = %q, want fallback %q", pq.IncludeDir, DefaultPGIncludeDir)
	}
	if pq.FromPgConfig {
		t.Error("FromPgConfig = true, want false")
	}
	if pq.Linkable {
		t.Error("Linkable = true, want false")
	}
}

func TestDiscoverPQEmptyOutputFallback(t *testing.T) {
	for _, out := range []string{"", "   ", "\n\t "} {
		pq := DiscoverPQ(func(args ...string) (string, error) {
			return out, nil
		})
		if pq.IncludeDir != DefaultPGIncludeDir {
			t.Errorf("output %q: IncludeDir = %q, want fallback", out, pq.IncludeDir)
		}
		if pq.FromPgConfig || pq.Linkable {
			t.Errorf("output %q: FromPgConfig=%v Linkable=%v, want both false", out, pq.FromPgConfig, pq.Linkable)
		}
	}
}
