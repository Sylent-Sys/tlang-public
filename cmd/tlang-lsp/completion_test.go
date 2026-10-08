package main

import (
	"strings"
	"testing"

	"tlang/types"
)

// labels returns the set of completion labels.
func labels(items []CompletionItem) map[string]CompletionItem {
	m := make(map[string]CompletionItem, len(items))
	for _, it := range items {
		m[it.Label] = it
	}
	return m
}

func TestCompletePlainTopLevel(t *testing.T) {
	src := "fn greet(name: string): void {}\nlet counter = 1;\n"
	a := analyze("file:///app.tlang", src)
	// Cursor at end of file (top level, outside any body).
	items := complete(a.info, a.prog, a.src, len(a.src))
	got := labels(items)

	for _, kw := range []string{"let", "fn", "return", "interface"} {
		if _, ok := got[kw]; !ok {
			t.Fatalf("plain completion missing keyword %q", kw)
		}
	}
	// At least one in-scope user identifier (a global or a function).
	if _, ok := got["counter"]; !ok {
		if _, ok := got["greet"]; !ok {
			t.Fatal("plain completion missing in-scope identifier (counter/greet)")
		}
	}
}

func TestCompleteMemberNamespace(t *testing.T) {
	// "db." with nothing after the dot: resolved via the identifier text.
	src := "import { db } from \"tlang/db\";\nfn main(): void {\n\tdb.\n}\n"
	a := analyze("file:///app.tlang", src)
	offset := strings.Index(src, "db.") + len("db.")
	items := complete(a.info, a.prog, a.src, offset)
	got := labels(items)
	for _, name := range dbMembers {
		if _, ok := got[name]; !ok {
			t.Fatalf("db. completion missing %q, got %v", name, keys(got))
		}
	}
}

func TestCompleteMemberNamed(t *testing.T) {
	src := "interface User { id: int64; name: string; }\n" +
		"fn main(): void {\n\tlet u: User = { id: 1, name: \"a\" };\n\tu.\n}\n"
	a := analyze("file:///app.tlang", src)
	offset := strings.LastIndex(src, "u.") + len("u.")
	items := complete(a.info, a.prog, a.src, offset)
	got := labels(items)
	for _, name := range []string{"id", "name"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("u. completion missing field %q, got %v", name, keys(got))
		}
	}
}

func TestCompleteMemberOptionalReceiver(t *testing.T) {
	src := "interface User { id: int64; }\n" +
		"fn main(): void {\n\tlet u: User | null = null;\n\tu.\n}\n"
	a := analyze("file:///app.tlang", src)
	offset := strings.LastIndex(src, "u.") + len("u.")
	items := complete(a.info, a.prog, a.src, offset)
	got := labels(items)
	if _, ok := got["id"]; !ok {
		t.Fatalf("optional-receiver completion missing field id, got %v", keys(got))
	}
}

// TestProbeListDriftGuard asserts every probe-list name resolves to a
// non-invalid builtin, so a stale or mistyped mirror of the unexported
// builtins table fails the build.
func TestProbeListDriftGuard(t *testing.T) {
	for _, name := range consoleMembers {
		if types.StandardNamespaceMember(types.BuiltinConsole, name) == types.BuiltinInvalid {
			t.Errorf("consoleMembers: %q does not resolve", name)
		}
	}
	for _, name := range dbMembers {
		if types.NamespaceMember(types.BuiltinDB, name) == types.BuiltinInvalid {
			t.Errorf("dbMembers: %q does not resolve", name)
		}
	}

	valueLists := []struct {
		name  string
		recv  types.Type
		names []string
	}{
		{"txMembers", types.Typ[types.Transaction], txMembers},
		{"stringMembers", types.Typ[types.String], stringMembers},
		{"arrayMembers", types.NewArray(types.Typ[types.Int64]), arrayMembers},
		{"errorMembers", types.Typ[types.Error], errorMembers},
		{"contextMembers", types.Typ[types.Context], contextMembers},
	}
	for _, vl := range valueLists {
		for _, name := range vl.names {
			if types.MemberOf(vl.recv, name) == types.BuiltinInvalid {
				t.Errorf("%s: %q does not resolve on %s", vl.name, name, vl.recv)
			}
		}
	}

	// numberMembers ("toString") maps to four distinct builtin IDs.
	for _, recv := range []types.Type{
		types.Typ[types.Int32], types.Typ[types.Int64],
		types.Typ[types.Float64], types.Typ[types.Bool],
	} {
		for _, name := range numberMembers {
			if types.MemberOf(recv, name) == types.BuiltinInvalid {
				t.Errorf("numberMembers: %q does not resolve on %s", name, recv)
			}
		}
	}
}

func keys(m map[string]CompletionItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
