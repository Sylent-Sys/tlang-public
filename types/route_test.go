package types

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseRoutePattern(t *testing.T) {
	ok := map[string]string{
		"/":                        "",
		"/api/users":               "api users",
		"/users/:id":               "users :id",
		"/users/:id/posts/:postId": "users :id posts :postId",
		"/a%20b/x-y_z.json":        "a%20b x-y_z.json",
		"/v1/:_x9":                 "v1 :_x9",
	}
	for pattern, want := range ok {
		segs, err := ParseRoutePattern(pattern)
		if err != nil {
			t.Errorf("ParseRoutePattern(%q): %v", pattern, err)
			continue
		}
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = s.Text
			if s.Param {
				parts[i] = ":" + s.Text
			}
		}
		if got := strings.Join(parts, " "); got != want {
			t.Errorf("ParseRoutePattern(%q) = %q, want %q", pattern, got, want)
		}
	}

	nine := ""
	for i := range 9 {
		nine += fmt.Sprintf("/:p%d", i)
	}
	bad := []string{"", "users", "//", "/users/", "/a//b", "/:", "/:1x", "/:a-b", "/:id/:id", "/a b", "/a?x=1", "/a#f", "/é", nine}
	for _, pattern := range bad {
		if _, err := ParseRoutePattern(pattern); err == nil {
			t.Errorf("ParseRoutePattern(%q) accepted", pattern)
		}
	}
	eight := strings.TrimSuffix(nine, "/:p8")
	if segs, err := ParseRoutePattern(eight); err != nil || len(segs) != MaxRouteParams {
		t.Errorf("8 parameters must be accepted: %v", err)
	}

	segs, _ := ParseRoutePattern("/users/:id/posts/:postId")
	r := &Route{ID: 1, Method: "GET", Pattern: "/users/:id/posts/:postId", Segments: segs}
	if got := strings.Join(r.Params(), ","); got != "id,postId" {
		t.Errorf("Params = %s", got)
	}
}

func TestValidRouteMethod(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PATCH", "OPTIONS"} {
		if !ValidRouteMethod(m) {
			t.Errorf("%s rejected", m)
		}
	}
	for _, m := range []string{"", "get", "GET ", "*", "M-SEARCH"} {
		if ValidRouteMethod(m) {
			t.Errorf("%q accepted", m)
		}
	}
}
