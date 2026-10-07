package tests

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	ttoken "tlang/token"
)

// grammarPath is the VS Code TextMate grammar, relative to this package.
var grammarPath = filepath.Join("..", "editors", "vscode", "syntaxes", "tlang.tmLanguage.json")

// lexerKeywordSet reads the lexer's keyword set from the keys of the
// unexported `keywords` map literal in token/token.go (token exports only
// LookupIdent for a known word, so the source is the authority).
func lexerKeywordSet(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "token", "token.go")
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
		if lit, ok := vs.Values[0].(*ast.CompositeLit); ok {
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.BasicLit); ok {
						if s, err := strconv.Unquote(key.Value); err == nil {
							out = append(out, s)
						}
					}
				}
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatalf("no keywords map found in %s", path)
	}
	for _, kw := range out {
		if ttoken.LookupIdent(kw) == ttoken.IDENT {
			t.Fatalf("%q from the keywords map is not a keyword to token.LookupIdent", kw)
		}
	}
	sort.Strings(out)
	return out
}

// scopedPattern is a grammar rule that names its whole match.
type scopedPattern struct {
	scope string
	re    *regexp.Regexp
}

// keywordPatterns collects every rule anywhere in the grammar whose "name" is
// a keyword-like scope (keyword.*, storage.*, constant.language.*) and whose
// "match" Go's regexp can compile. Rules using Oniguruma-only syntax such as
// lookarounds are skipped; keyword rules do not need it.
func keywordPatterns(node any, out []scopedPattern) []scopedPattern {
	switch v := node.(type) {
	case map[string]any:
		name, _ := v["name"].(string)
		match, _ := v["match"].(string)
		if match != "" && (strings.HasPrefix(name, "keyword.") || strings.HasPrefix(name, "storage.") ||
			strings.HasPrefix(name, "constant.language.")) {
			if re, err := regexp.Compile(`^(?:` + match + `)$`); err == nil {
				out = append(out, scopedPattern{scope: name, re: re})
			}
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = keywordPatterns(v[k], out)
		}
	case []any:
		for _, e := range v {
			out = keywordPatterns(e, out)
		}
	}
	return out
}

// TestEditorGrammarKeywords guards the VS Code grammar against drifting from
// the lexer: the grammar must be valid JSON, and every lexer keyword must be
// matched in full by a keyword-scoped rule.
func TestEditorGrammarKeywords(t *testing.T) {
	raw, err := os.ReadFile(grammarPath)
	if err != nil {
		t.Fatalf("read grammar: %v", err)
	}
	var grammar map[string]any
	if err := json.Unmarshal(raw, &grammar); err != nil {
		t.Fatalf("grammar is not valid JSON: %v", err)
	}
	if grammar["scopeName"] != "source.tlang" {
		t.Fatalf("scopeName = %v, want source.tlang", grammar["scopeName"])
	}
	patterns := keywordPatterns(grammar, nil)
	if len(patterns) == 0 {
		t.Fatal("no keyword-scoped rules found in the grammar")
	}
	for _, kw := range lexerKeywordSet(t) {
		found := false
		for _, p := range patterns {
			if p.re.MatchString(kw) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("lexer keyword %q is not highlighted by any keyword rule in %s", kw, grammarPath)
		}
	}
}

// TestEditorModuleKeywordScopes pins the TS-style scopes of the module
// keywords.
func TestEditorModuleKeywordScopes(t *testing.T) {
	raw, err := os.ReadFile(grammarPath)
	if err != nil {
		t.Fatalf("read grammar: %v", err)
	}
	var grammar map[string]any
	if err := json.Unmarshal(raw, &grammar); err != nil {
		t.Fatalf("grammar is not valid JSON: %v", err)
	}
	patterns := keywordPatterns(grammar, nil)
	for _, kw := range []string{"import", "export", "from", "as", "default"} {
		want := "keyword.control." + kw + ".tlang"
		found := false
		for _, p := range patterns {
			if p.scope == want && p.re.MatchString(kw) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not scoped %s", kw, want)
		}
	}
}
