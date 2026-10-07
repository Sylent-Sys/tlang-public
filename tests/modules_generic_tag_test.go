package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"tlang/checker"
	"tlang/module"
	"tlang/types"
)

// TestModulesSameNamedGenericDistinctCNames: two modules that each declare a
// generic fn id<T> and use it at int64 produce two id<int64> instances, each
// tagged with its own module, under distinct C names (no "C name collision:
// tl_f_id__i64"), and the merged C defines both.
func TestModulesSameNamedGenericDistinctCNames(t *testing.T) {
	dir := filepath.Join("modules", "generic_same_name_two_modules")
	graph, bdiags := module.Build(filepath.Join(dir, modsRootFile))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build: %v", bdiags.Err())
	}
	info, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		t.Fatalf("CheckProgram: %v", cdiags.Err())
	}

	tagOf := map[string]string{} // module file -> tag
	for _, m := range graph.Modules {
		tagOf[m.Prog.File] = m.Tag
	}
	byTag := map[string]string{} // instance tag -> C name
	for _, f := range info.Instances.FuncInstances() {
		if f.Name != "id" || len(f.TypeArgs) != 1 || !types.Identical(f.TypeArgs[0], types.Typ[types.Int64]) {
			continue
		}
		if f.Tag != f.Origin.Tag {
			t.Errorf("instance %s has tag %q, origin has %q", f.FullName(), f.Tag, f.Origin.Tag)
		}
		byTag[f.Tag] = types.FuncCName(f)
	}
	if len(byTag) != 2 {
		t.Fatalf("got %d distinctly tagged id<int64> instances %v, want 2", len(byTag), byTag)
	}
	var names []string
	for _, file := range []string{"a.ts", "main.ts"} {
		tag := tagOf[file]
		if tag == "" {
			t.Fatalf("module %s has an empty tag (tags: %v)", file, tagOf)
		}
		cname, ok := byTag[tag]
		if !ok {
			t.Fatalf("no id<int64> instance tagged %q (%s); have %v", tag, file, byTag)
		}
		if want := "tl_f_" + tag + "__id__i64"; cname != want {
			t.Errorf("%s instance C name = %q, want %q", file, cname, want)
		}
		names = append(names, cname)
	}
	if names[0] == names[1] {
		t.Fatalf("both modules' id<int64> mangle to %q", names[0])
	}

	out, err := buildMergedC(dir)
	if err != nil {
		t.Fatalf("buildMergedC: %v", err)
	}
	for _, n := range names {
		if def := "int64_t " + n + "(tlang_fiber* __fib, int64_t l_x) {"; !strings.Contains(string(out), def) {
			t.Errorf("merged C does not define %s", n)
		}
	}
}
