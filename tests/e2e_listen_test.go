package tests

import "testing"

// TestParseListening pins the readiness-line parser against the runtime's exact
// stderr format. It documents the out-of-scope IPv6 limitation (tests-suites
// design §10): the host group ([^:]+) only accepts an IPv4/hostname, so an
// IPv6-shaped line does not parse — the harness pins TLANG_HOST=127.0.0.1.
func TestParseListening(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantHost string
		wantPort int
		wantOK   bool
	}{
		{
			name:     "ipv4 success",
			line:     "tlang: listening on http://127.0.0.1:54321 (1 thread)",
			wantHost: "127.0.0.1",
			wantPort: 54321,
			wantOK:   true,
		},
		{
			name:     "ipv4 plural threads",
			line:     "tlang: listening on http://127.0.0.1:8080 (4 threads)",
			wantHost: "127.0.0.1",
			wantPort: 8080,
			wantOK:   true,
		},
		{
			// IPv6 host contains colons and defeats [^:]+ (design §10/§12).
			name:   "ipv6 shaped line unsupported",
			line:   "tlang: listening on http://::1:8080 (1 thread)",
			wantOK: false,
		},
		{
			name:   "unrelated line",
			line:   "tlang: starting up",
			wantOK: false,
		},
		{
			name:   "trailing junk",
			line:   "tlang: listening on http://127.0.0.1:8080 (1 thread) extra",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, ok := parseListening(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (line %q)", ok, tc.wantOK, tc.line)
			}
			if !tc.wantOK {
				return
			}
			if host != tc.wantHost {
				t.Fatalf("host = %q, want %q", host, tc.wantHost)
			}
			if port != tc.wantPort {
				t.Fatalf("port = %d, want %d", port, tc.wantPort)
			}
		})
	}
}
