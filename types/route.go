package types

import (
	"fmt"
	"strings"

	"tlang/ast"
)

// MaxRouteParams is the maximum number of dynamic segments in a ctx.match
// pattern (spec §8.1: ctx->params has 8 slots). More is a compile error.
const MaxRouteParams = 8

// RouteSegment is one "/"-separated segment of a route pattern.
type RouteSegment struct {
	// Param reports a dynamic segment ":name".
	Param bool
	// Text is the literal segment text for a static segment (matched with
	// memcmp against the raw, undecoded path), or the parameter name
	// without ":" for a dynamic segment.
	Text string
}

// Route is one ctx.match(method, pattern) call, compiled by codegen into a
// static segment table (DESIGN.md §2.11).
type Route struct {
	// ID numbers routes from 1 in source order; codegen names the static
	// table after it.
	ID int
	// Call is the ctx.match call expression.
	Call *ast.CallExpression
	// Method is the HTTP method literal, e.g. "GET" (ValidRouteMethod).
	Method string
	// Pattern is the pattern literal, e.g. "/users/:id".
	Pattern string
	// Segments is ParseRoutePattern(Pattern).
	Segments []RouteSegment
}

// Params returns the parameter names in pattern order; the matcher records
// the values into ctx->params in this order.
func (r *Route) Params() []string {
	var out []string
	for _, s := range r.Segments {
		if s.Param {
			out = append(out, s.Text)
		}
	}
	return out
}

// ValidRouteMethod reports whether method is a non-empty run of upper-case
// ASCII letters ("GET", "POST", "PATCH", ...). Matching is exact.
func ValidRouteMethod(method string) bool {
	if method == "" {
		return false
	}
	for i := 0; i < len(method); i++ {
		if method[i] < 'A' || method[i] > 'Z' {
			return false
		}
	}
	return true
}

// ParseRoutePattern splits a ctx.match pattern into segments:
//
//	"/"                 no segments (matches only "/")
//	"/api/users"        [api users]
//	"/users/:id/posts"  [users :id posts]
//
// Rules: the pattern starts with "/"; segments are separated by single
// "/"; there is no empty segment (no "//", no trailing "/" except the root
// pattern); a ":" at the start of a segment makes it a parameter whose name
// is an identifier ([A-Za-z_][A-Za-z0-9_]*), unique within the pattern;
// static segments contain only visible ASCII other than "?" and "#" (use
// percent-encoding, which is matched literally); at most MaxRouteParams
// parameters.
func ParseRoutePattern(pattern string) ([]RouteSegment, error) {
	if !strings.HasPrefix(pattern, "/") {
		return nil, fmt.Errorf("route pattern %q must start with \"/\"", pattern)
	}
	if pattern == "/" {
		return nil, nil
	}
	var segs []RouteSegment
	seen := map[string]bool{}
	for _, part := range strings.Split(pattern[1:], "/") {
		if part == "" {
			return nil, fmt.Errorf("route pattern %q has an empty segment", pattern)
		}
		if name, ok := strings.CutPrefix(part, ":"); ok {
			if !isIdent(name) {
				return nil, fmt.Errorf("route parameter %q in %q is not an identifier", part, pattern)
			}
			if seen[name] {
				return nil, fmt.Errorf("route parameter %q appears twice in %q", name, pattern)
			}
			seen[name] = true
			segs = append(segs, RouteSegment{Param: true, Text: name})
			continue
		}
		for i := 0; i < len(part); i++ {
			if c := part[i]; c <= ' ' || c >= 0x7F || c == '?' || c == '#' {
				return nil, fmt.Errorf("route pattern %q contains an invalid character %q", pattern, c)
			}
		}
		segs = append(segs, RouteSegment{Text: part})
	}
	if len(seen) > MaxRouteParams {
		return nil, fmt.Errorf("route pattern %q has %d parameters; at most %d are allowed", pattern, len(seen), MaxRouteParams)
	}
	return segs, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
