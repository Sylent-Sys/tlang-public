package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"tlang/checker"
	tparser "tlang/parser"
	ttoken "tlang/token"
)

// lexerKeywords reads the lexer's keyword set from the keys of the `keywords`
// map literal in token/token.go (the map is unexported, and token offers only
// LookupIdent for a known word, so the source is the authority).
func lexerKeywords(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "token", "token.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "keywords" || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.BasicLit)
			if !ok {
				continue
			}
			if s, err := strconv.Unquote(key.Value); err == nil {
				out = append(out, s)
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatalf("no keywords map found in %s", path)
	}
	sort.Strings(out)
	return out
}

// TestKeywordsMatchLexer: the completion keyword list is exactly the lexer's
// keyword set.
func TestKeywordsMatchLexer(t *testing.T) {
	want := lexerKeywords(t)
	got := append([]string(nil), keywords...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("completion keywords\n  %v\nlexer keywords\n  %v", got, want)
	}
	for _, kw := range keywords {
		if ttoken.LookupIdent(kw) == ttoken.IDENT {
			t.Errorf("%q is not a keyword to token.LookupIdent", kw)
		}
	}
}

// TestDecoratorsAccepted: every offered decorator is one the checker knows,
// and the probe does tell an unknown one apart.
func TestDecoratorsAccepted(t *testing.T) {
	unknown := func(name string) bool {
		src := "@" + name + "() fn h(ctx: Context): void {}\n"
		prog, _ := tparser.ParseSource("d.ts", []byte(src))
		_, diags := checker.Check(prog)
		for _, d := range diags.Items {
			if strings.Contains(d.Message, "unknown decorator") {
				return true
			}
		}
		return false
	}
	for _, d := range decorators {
		if unknown(d.name) {
			t.Errorf("the checker rejects offered decorator @%s", d.name)
		}
	}
	if !unknown("NoSuchDecorator") {
		t.Fatal("probe did not detect an unknown decorator")
	}
}
