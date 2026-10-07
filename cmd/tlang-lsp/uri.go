package main

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// fileScheme prefixes every file: URI the server emits.
const fileScheme = "file://"

// isWindows selects the Windows path rules for this process.
var isWindows = runtime.GOOS == "windows"

// uriToPath converts a file: URI to an OS path using this platform's rules.
// ok is false for any other scheme (untitled:, etc.) or a malformed URI.
func uriToPath(uri string) (string, bool) { return uriToPathOS(uri, isWindows) }

// pathToURI converts an absolute OS path to a file: URI using this platform's
// rules.
func pathToURI(p string) string { return pathToURIOS(p, isWindows) }

// uriToPathOS is uriToPath with the platform made explicit, so both rule sets
// are testable on any host. It is pure string work (no filepath calls).
//
// Both "file:///path" and the authority-less "file:/path" (sent by some
// non-VS Code clients) are accepted. Windows: the drive form "/c:/x" or
// "/c%3A/x" becomes `C:\x` (the drive letter is upper-cased so every path the
// server builds agrees), and a host other than localhost becomes a UNC path
// `\\host\share\x`. POSIX: the decoded path is used as is; a remote host is
// rejected.
func uriToPathOS(uri string, windows bool) (string, bool) {
	const scheme = "file:"
	if len(uri) < len(scheme) || !strings.EqualFold(uri[:len(scheme)], scheme) {
		return "", false
	}
	rest := uri[len(scheme):]
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	var host, p string
	switch {
	case strings.HasPrefix(rest, "//"):
		rest = rest[2:]
		host = rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			host, p = rest[:i], rest[i:]
		}
	case strings.HasPrefix(rest, "/"):
		p = rest
	default:
		return "", false
	}
	p, err := url.PathUnescape(p)
	if err != nil || p == "" {
		return "", false
	}
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	if !windows {
		if host != "" {
			return "", false
		}
		return p, true
	}
	if host != "" {
		return `\\` + host + strings.ReplaceAll(p, "/", `\`), true
	}
	if len(p) >= 3 && p[0] == '/' && isASCIILetter(p[1]) && p[2] == ':' {
		p = strings.ToUpper(p[1:2]) + p[2:]
		if len(p) == 2 {
			p += "/" // "C:" alone is the drive's current directory, not its root
		}
	}
	return strings.ReplaceAll(p, "/", `\`), true
}

// pathToURIOS is pathToURI with the platform made explicit. It emits the form
// VS Code produces for a local file: "file:///c%3A/dir/a.tlang" on Windows
// (lower-case drive, encoded colon), "file:///dir/a.tlang" elsewhere, with
// each path segment percent-escaped.
func pathToURIOS(p string, windows bool) string {
	if windows {
		p = strings.ReplaceAll(p, `\`, "/")
		if strings.HasPrefix(p, "//") {
			rest := p[2:]
			host, tail := rest, ""
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				host, tail = rest[:i], rest[i:]
			}
			return fileScheme + host + escapePath(tail)
		}
		if len(p) >= 2 && isASCIILetter(p[0]) && p[1] == ':' {
			return fileScheme + "/" + strings.ToLower(p[:1]) + "%3A" + escapePath(p[2:])
		}
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return fileScheme + escapePath(p)
}

// escapePath percent-escapes each segment of a slash path.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// docPath returns the absolute OS path of a file: document URI.
func docPath(uri string) (string, bool) {
	p, ok := uriToPath(uri)
	if !ok {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	return abs, true
}

// pathKey is the identity used to compare OS paths: cleaned, and case-folded
// on Windows, whose file system is case-insensitive (it also absorbs the
// drive-letter case editors disagree on).
func pathKey(p string) string {
	p = filepath.Clean(p)
	if isWindows {
		p = strings.ToLower(p)
	}
	return p
}

// withinDir reports whether p is dir itself or lies below it.
func withinDir(dir, p string) bool {
	kd, kp := pathKey(dir), pathKey(p)
	if kp == kd {
		return true
	}
	prefix := strings.TrimSuffix(kd, string(filepath.Separator)) + string(filepath.Separator)
	return strings.HasPrefix(kp, prefix)
}
