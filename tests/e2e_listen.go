package tests

import (
	"regexp"
	"strconv"
)

// listeningRe matches the readiness line the runtime prints to stderr once the
// listeners are bound (runtime/src/main.c): "tlang: listening on
// http://HOST:PORT (N threads)". The host group ([^:]+) relies on the harness
// pinning the IPv4 TLANG_HOST=127.0.0.1 (an IPv6 host would contain colons and
// defeat [^:]+; IPv6 is out of scope, tests-suites design §10/§12).
var listeningRe = regexp.MustCompile(`^tlang: listening on http://([^:]+):(\d+) \((\d+) threads?\)$`)

// parseListening parses the runtime's listening line and returns the host, the
// parsed port, and ok. ok is false when the line does not match or the port is
// not a valid number. The harness consumes only the port (the host is already
// fixed by the harness via TLANG_HOST), but host is returned for completeness
// and so the helper is fully unit-testable.
func parseListening(line string) (host string, port int, ok bool) {
	m := listeningRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(m[2])
	if err != nil || p < 0 || p > 65535 {
		return "", 0, false
	}
	return m[1], p, true
}
