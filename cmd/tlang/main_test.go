package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes src to a real file under t.TempDir() and returns its
// path, so subcommands exercise os.ReadFile against the filesystem.
func writeFixture(t *testing.T, name, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring, "" => no assertion
		wantStderr string // substring, "" => no assertion
	}{
		{
			name:       "no args prints usage to stdout",
			args:       nil,
			wantCode:   exitOK,
			wantStdout: "Usage:",
		},
		{
			name:       "help flag prints usage to stdout",
			args:       []string{"-h"},
			wantCode:   exitOK,
			wantStdout: "Commands:",
		},
		{
			name:       "help subcommand prints usage to stdout",
			args:       []string{"help"},
			wantCode:   exitOK,
			wantStdout: "emit-c",
		},
		{
			name:       "unknown subcommand is a usage error on stderr",
			args:       []string{"frobnicate"},
			wantCode:   exitUsage,
			wantStderr: "unknown command",
		},
		{
			name:       "version prints the version line to stdout",
			args:       []string{"version"},
			wantCode:   exitOK,
			wantStdout: "tlang " + version,
		},
		{
			name:       "version rejects extra args",
			args:       []string{"version", "extra"},
			wantCode:   exitUsage,
			wantStderr: "unexpected arguments",
		},
		{
			name:       "run on an unreadable file is a usage error",
			args:       []string{"run", "does-not-exist.ts"},
			wantCode:   exitUsage,
			wantStderr: "cannot read",
		},
		{
			name:       "build on an unreadable file is a usage error",
			args:       []string{"build", "does-not-exist.ts"},
			wantCode:   exitUsage,
			wantStderr: "cannot read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, errb.String())
			}
			if tt.wantStdout != "" && !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", errb.String(), tt.wantStderr)
			}
		})
	}
}
