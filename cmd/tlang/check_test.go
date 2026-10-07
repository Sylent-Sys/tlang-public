package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

const validProgram = "fn main(): void {}\n"

// invalidProgram has a syntax error so the parser reports an E-PARSE
// diagnostic (rendered as "error:").
const invalidProgram = "fn main(): void { let x = ; }\n"

func TestRunCheck(t *testing.T) {
	valid := writeFixture(t, "ok.ts", validProgram)
	invalid := writeFixture(t, "bad.ts", invalidProgram)
	missing := filepath.Join(t.TempDir(), "nope.ts")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "valid program checks ok",
			args:       []string{"check", valid},
			wantCode:   exitOK,
			wantStdout: "ok:",
		},
		{
			name:       "invalid program reports an error diagnostic",
			args:       []string{"check", invalid},
			wantCode:   exitFail,
			wantStderr: "error:",
		},
		{
			name:       "missing file is a usage error",
			args:       []string{"check", missing},
			wantCode:   exitUsage,
			wantStderr: "cannot read",
		},
		{
			name:       "no file arg is a usage error",
			args:       []string{"check"},
			wantCode:   exitUsage,
			wantStderr: "usage:",
		},
		{
			name:       "extra args is a usage error",
			args:       []string{"check", valid, "extra.ts"},
			wantCode:   exitUsage,
			wantStderr: "usage:",
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
