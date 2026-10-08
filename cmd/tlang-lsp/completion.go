package main

import (
	"bytes"
	"strings"

	"tlang/ast"
	"tlang/module"
	"tlang/types"
)

// keywords is the fixed TLang keyword set, offered by plain completion. The
// token package's keyword map is unexported, so the list is carried here;
// keywords_test.go asserts it equals the lexer's set.
var keywords = []string{
	"let", "const", "fn", "interface", "type", "new", "return",
	"if", "else", "for", "while", "break", "continue",
	"try", "catch", "throw", "true", "false", "null", "of", "global",
	"import", "export", "from", "as", "default",
}

// decorators are the decorator names the checker accepts (checker/entry.go
// resolveFuncGuards), offered after "@". keywords_test.go guards them.
var decorators = []struct{ name, detail string }{
	{"Use", "@Use(guard, ...) with guard: fn(ctx: Context): bool"},
	{"After", "@After(hook, ...) with hook: fn(ctx: Context): void"},
}

// Builtin-member probe lists. The types package exposes member resolution by
// name only (the builtins table is unexported), so these mirror its entries,
// grouped by receiver. completion_test.go guards them: every name must resolve
// to a non-invalid builtin, so a stale or mistyped entry fails the build.
var (
	consoleMembers = []string{"info", "error"}
	dbMembers      = []string{"execute", "query", "queryOne", "transaction"}
	txMembers      = []string{"execute", "query", "queryOne"}
	stringMembers  = []string{"len", "eq", "slice", "startsWith", "endsWith",
		"indexOf", "clone", "clone_global", "toInt"}
	arrayMembers   = []string{"len", "push"}
	errorMembers   = []string{"message", "status"}
	contextMembers = []string{"method", "path", "rawQuery", "body", "header",
		"query", "param", "paramInt", "match", "bindJson", "text", "json", "setHeader"}
	// numberMembers has one name mapping to four builtin IDs (Int32/Int64/
	// Float64/Bool toString), resolved via MemberOf on each numeric kind.
	numberMembers = []string{"toString"}
)

// complete returns completion items for the cursor at a byte offset into src
// on the single-file path (no module context).
func complete(info *types.Info, prog *ast.Program, src []byte, offset int) []CompletionItem {
	return completeIn(nil, info, prog, src, offset)
}

// completeIn returns completion items for the cursor at a byte offset into
// src. mc is the document's module context, or nil on the single-file path. It
// classifies the request by scanning backwards over identifier characters: a
// decorator name (after '@'), an import/re-export list (inside the braces of
// `import { } from "./y"`), a member (after '.'), or plain. It never errors:
// an unresolvable member request degrades to plain completion.
func completeIn(mc *moduleContext, info *types.Info, prog *ast.Program, src []byte, offset int) []CompletionItem {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	// Scan back over the identifier run immediately left of the cursor.
	idStart := offset
	for idStart > 0 && isIdentByte(src[idStart-1]) {
		idStart--
	}
	if idStart > 0 && src[idStart-1] == '@' {
		if inStringOrComment(src, idStart-1) {
			return nil // "user@host" in a string, or an @ in a comment
		}
		return decoratorItems()
	}
	if mc != nil {
		if spec, ok := importListSpec(src, offset); ok {
			return mc.exportItems(mc.resolveSpec(spec))
		}
	}
	// A member request has a '.' immediately before that identifier run.
	if idStart > 0 && src[idStart-1] == '.' {
		dotOffset := idStart - 1
		if items := completeMember(mc, info, prog, src, dotOffset); items != nil {
			return items
		}
	}
	return completePlain(mc, info, prog, offset)
}

// inStringOrComment reports whether offset lies inside a string literal or a
// comment. It scans src from the start with the lexer's rules: "//" line
// comments, "/* */" block comments, and "..." or '...' strings with backslash
// escapes, which cannot span a newline.
func inStringOrComment(src []byte, offset int) bool {
	const (
		code = iota
		lineComment
		blockComment
		str
	)
	state := code
	var quote byte
	for i := 0; i < offset && i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = lineComment
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = blockComment
				i++
			case c == '"' || c == '\'':
				state, quote = str, c
			}
		case lineComment:
			if c == '\n' {
				state = code
			}
		case blockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state = code
				i++
			}
		case str:
			switch c {
			case '\\':
				i++
			case quote, '\n':
				state = code
			}
		}
	}
	return state != code
}

// decoratorItems lists the known decorators.
func decoratorItems() []CompletionItem {
	items := make([]CompletionItem, 0, len(decorators))
	for _, d := range decorators {
		items = append(items, CompletionItem{Label: d.name, Kind: completionKindFunction, Detail: d.detail})
	}
	return items
}

// exportItems lists a module's exports (values and types alike: the same
// list serves "m." in an expression and in a type annotation, and an import
// list). A nil module yields an empty, non-nil list so the caller does not
// fall back to unrelated names.
func (mc *moduleContext) exportItems(mod *module.Module) []CompletionItem {
	items := []CompletionItem{}
	for _, e := range mc.exports(mod) {
		obj := mc.objectOf(e.site)
		items = append(items, CompletionItem{Label: e.name, Kind: siteKind(obj, e.site.stmt), Detail: detailOf(obj)})
	}
	return items
}

// siteKind is the completion kind of a declaration: from its checked object
// when there is one, else from the declaring statement.
func siteKind(obj types.Object, stmt ast.Statement) int {
	if obj != nil {
		return objectKind(obj)
	}
	switch stmt.(type) {
	case *ast.FunctionStatement:
		return completionKindFunction
	case *ast.InterfaceStatement, *ast.TypeAliasStatement:
		return completionKindInterface
	}
	return completionKindVariable
}

// importListSpec reports whether offset lies inside the braces of an import
// or re-export list and returns the statement's specifier. It scans the text
// rather than the AST because the statement is usually incomplete while it is
// being typed (an empty "{ }" does not parse at all):
//
//	import { A, | } from "./y"
//	import D, { | } from "./y"
//	export { | } from "./y"
func importListSpec(src []byte, offset int) (string, bool) {
	listByte := func(c byte) bool { return isIdentByte(c) || c == ',' || isSpaceByte(c) }
	open := offset - 1
	for open >= 0 && listByte(src[open]) {
		open--
	}
	if open < 0 || src[open] != '{' {
		return "", false
	}
	before := skipSpaceBack(src, open)
	if word := wordBefore(src, before); word != "import" && word != "export" {
		// The default-plus-named form: "import D, {".
		if before == 0 || src[before-1] != ',' {
			return "", false
		}
		defEnd := skipSpaceBack(src, before-1)
		def := wordBefore(src, defEnd)
		if def == "" || wordBefore(src, skipSpaceBack(src, defEnd-len(def))) != "import" {
			return "", false
		}
	}

	end := offset
	for end < len(src) && listByte(src[end]) {
		end++
	}
	if end >= len(src) || src[end] != '}' {
		return "", false
	}
	k := skipSpace(src, end+1)
	if !bytes.HasPrefix(src[k:], []byte("from")) || (k+4 < len(src) && isIdentByte(src[k+4])) {
		return "", false
	}
	k = skipSpace(src, k+4)
	if k >= len(src) || (src[k] != '"' && src[k] != '\'') {
		return "", false
	}
	closeQuote := stringEnd(src, k)
	if closeQuote < 0 {
		return "", false
	}
	return string(src[k+1 : closeQuote]), true
}

// skipSpace returns the first offset at or after i that is not white space.
func skipSpace(src []byte, i int) int {
	for i < len(src) && isSpaceByte(src[i]) {
		i++
	}
	return i
}

// skipSpaceBack returns the offset just past the last non-space byte before
// i (so src[result-1] is that byte; 0 when there is none).
func skipSpaceBack(src []byte, i int) int {
	for i > 0 && isSpaceByte(src[i-1]) {
		i--
	}
	return i
}

// wordBefore returns the identifier run ending just before offset end.
func wordBefore(src []byte, end int) string {
	start := end
	for start > 0 && isIdentByte(src[start-1]) {
		start--
	}
	return string(src[start:end])
}

// stringEnd returns the offset of the quote closing the string literal that
// opens at src[open], or -1 when it is unterminated on that line.
func stringEnd(src []byte, open int) int {
	q := src[open]
	for i := open + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case q:
			return i
		case '\n':
			return -1
		}
	}
	return -1
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// completePlain offers keywords, universe names, and in-scope user names. On
// the module path the top-level names are the document module's own
// declarations plus its import bindings (Info.Globals/Funcs span every module
// of the graph, including names other modules keep private).
func completePlain(mc *moduleContext, info *types.Info, prog *ast.Program, offset int) []CompletionItem {
	seen := map[string]bool{}
	var items []CompletionItem
	add := func(label string, kind int, detail string) {
		if seen[label] {
			return
		}
		seen[label] = true
		items = append(items, CompletionItem{Label: label, Kind: kind, Detail: detail})
	}

	for _, kw := range keywords {
		add(kw, completionKindKeyword, "")
	}
	for _, name := range types.UniverseNames() {
		obj := types.LookupUniverse(name)
		add(name, objectKind(obj), "")
	}
	if info != nil {
		if mc != nil {
			mc.moduleScope(add)
		} else {
			for _, g := range info.Globals {
				add(g.Name, objectKind(g), typeStringOf(g))
			}
			for _, f := range info.Funcs {
				add(f.Name, completionKindFunction, sigDetail(f))
			}
		}
		for _, obj := range enclosingScopeNames(info, prog, offset) {
			add(obj.ObjectName(), objectKind(obj), detailOf(obj))
		}
	}
	return items
}

// moduleScope reports the document module's top-level names to add: its own
// functions, globals and types, then its import bindings (named, aliased,
// default and namespace).
func (mc *moduleContext) moduleScope(add func(label string, kind int, detail string)) {
	for _, s := range mc.mod.Prog.Statements {
		d, ok := s.(*ast.ImportDecl)
		if !ok {
			if id, _, _ := topDecl(s); id != nil {
				obj := mc.info.Defs[id]
				add(id.Name, siteKind(obj, s), detailOf(obj))
			}
			continue
		}
		target := mc.target(mc.mod, d.From)
		if d.Namespace != nil {
			add(d.Namespace.Name, completionKindModule, moduleLabel(target, d.From))
		}
		if d.Default != nil {
			site, _ := mc.defaultExport(target)
			obj := mc.objectOf(site)
			add(d.Default.Name, siteKind(obj, site.stmt), detailOf(obj))
		}
		exports := mc.exportSet(target)
		for _, sp := range d.Named {
			site := exports[sp.Name.Name]
			obj := mc.objectOf(site)
			add(specLocalName(sp), siteKind(obj, site.stmt), detailOf(obj))
		}
	}
}

// enclosingScopeNames collects the params and locals of the function whose
// body contains offset, for bindings declared textually before the cursor.
// It is an intentional over-approximation of block scoping.
func enclosingScopeNames(info *types.Info, prog *ast.Program, offset int) []types.Object {
	if prog == nil {
		return nil
	}
	var fn *ast.FunctionStatement
	ast.Inspect(prog, func(n ast.Node) bool {
		f, ok := n.(*ast.FunctionStatement)
		if !ok {
			return true
		}
		if covers(f, offset) {
			fn = f // keep the innermost covering function
		}
		return true
	})
	if fn == nil {
		return nil
	}
	var out []types.Object
	collect := func(id *ast.Identifier) {
		if id == nil {
			return
		}
		if obj := info.Defs[id]; obj != nil {
			out = append(out, obj)
		}
	}
	// Parameters (and receiver) are in scope throughout the body.
	if fn.Receiver != nil {
		collect(fn.Receiver.Name)
	}
	for _, p := range fn.Parameters {
		collect(p.Name)
	}
	// Local bindings declared before the cursor.
	ast.Inspect(fn, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.LetStatement:
			if s.Name != nil && s.Name.Pos().Offset < offset {
				collect(s.Name)
			}
		case *ast.ForOfStatement:
			if s.Var != nil && s.Var.Pos().Offset < offset {
				collect(s.Var)
			}
		case *ast.TryCatchStatement:
			if s.CatchParam != nil && s.CatchParam.Pos().Offset < offset {
				collect(s.CatchParam)
			}
		}
		return true
	})
	return out
}

// completeMember enumerates the members of the receiver left of the dot at
// dotOffset. It returns nil when no resolvable non-optional receiver type is
// found, signaling the caller to degrade to plain completion. With a module
// context, a receiver naming a namespace import ("m.", in an expression or a
// type annotation) lists the target module's exports.
func completeMember(mc *moduleContext, info *types.Info, prog *ast.Program, src []byte, dotOffset int) []CompletionItem {
	if info == nil || prog == nil {
		return nil
	}
	// The identifier run immediately left of the dot.
	idEnd := dotOffset
	idStart := idEnd
	for idStart > 0 && isIdentByte(src[idStart-1]) {
		idStart--
	}
	if idStart == idEnd {
		return nil
	}

	// Prefer a complete MemberExpression whose dot matches, else resolve via
	// the identifier left of the dot (handles the incomplete-parse case).
	recvID := innermostIdentCovering(prog, idStart, idEnd)
	if mc != nil && (idStart == 0 || src[idStart-1] != '.') {
		if target := mc.namespaceOf(recvID, string(src[idStart:idEnd])); target != nil {
			return mc.exportItems(target)
		}
	}
	var me *ast.MemberExpression
	ast.Inspect(prog, func(n ast.Node) bool {
		if m, ok := n.(*ast.MemberExpression); ok && m.DotPos.Offset == dotOffset {
			me = m
		}
		return true
	})

	// Namespace receiver (console/db): keyed on the identifier text, resolves
	// even while the dot expression is still being typed (the incomplete
	// member expression may not be recorded in Info.Uses).
	recvText := string(src[idStart:idEnd])
	if recvID != nil {
		if b, ok := info.Uses[recvID].(*types.Builtin); ok {
			return namespaceItems(b.ID)
		}
		if obj := info.ObjectOf(recvID); obj != nil {
			if b, ok := obj.(*types.Builtin); ok {
				return namespaceItems(b.ID)
			}
		}
	}
	if strings.Contains(string(src[:dotOffset]), `"tlang/db"`) && recvText == "db" {
		return namespaceItems(types.BuiltinDB)
	}
	if strings.Contains(string(src[:dotOffset]), `"tlang/system"`) && recvText == "console" {
		return namespaceItems(types.BuiltinConsole)
	}
	if b, ok := types.LookupUniverse(recvText).(*types.Builtin); ok {
		return namespaceItems(b.ID)
	}
	if mc == nil && recvText == "db" && strings.Contains(string(src[:dotOffset]), `"tlang/db"`) {
		return namespaceItems(types.BuiltinDB)
	}

	var recv types.Type
	switch {
	case me != nil && info.TypeOf(me.Object) != nil:
		recv = info.TypeOf(me.Object)
	case recvID != nil:
		if t := info.TypeOf(recvID); t != nil {
			recv = t
		} else if obj := info.ObjectOf(recvID); obj != nil {
			recv = obj.ObjectType()
		}
	}
	if recv == nil {
		return nil
	}
	// Unwrap one optional layer: user. on a User | null yields User's members.
	if o, ok := recv.(*types.Optional); ok {
		recv = o.Elem
	}
	return memberItems(recv)
}

// namespaceItems lists the members of the console or db namespace.
func namespaceItems(ns types.BuiltinID) []CompletionItem {
	var names []string
	switch ns {
	case types.BuiltinConsole:
		names = consoleMembers
	case types.BuiltinDB:
		names = dbMembers
	default:
		return nil
	}
	var items []CompletionItem
	for _, name := range names {
		if id := types.StandardNamespaceMember(ns, name); id != types.BuiltinInvalid {
			items = append(items, CompletionItem{Label: name, Kind: completionKindFunction, Detail: id.String()})
		}
	}
	return items
}

// memberItems lists the members of a value receiver type.
func memberItems(recv types.Type) []CompletionItem {
	if named, ok := recv.(*types.Named); ok {
		var items []CompletionItem
		for _, f := range named.Fields() {
			items = append(items, CompletionItem{Label: f.Name, Kind: completionKindField, Detail: typeStringOf(f)})
		}
		for _, m := range named.Methods {
			items = append(items, CompletionItem{Label: m.Name, Kind: completionKindFunction, Detail: sigDetail(m)})
		}
		return items
	}

	names := probeNames(recv)
	if names == nil {
		return nil
	}
	var items []CompletionItem
	for _, name := range names {
		if id := types.MemberOf(recv, name); id != types.BuiltinInvalid {
			items = append(items, CompletionItem{Label: name, Kind: builtinMemberKind(id), Detail: id.String()})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

// probeNames returns the fixed member list matching a value receiver type, or
// nil when the type has no builtin members.
func probeNames(recv types.Type) []string {
	switch t := types.Default(recv).(type) {
	case *types.Array:
		return arrayMembers
	case *types.Basic:
		switch t.Kind {
		case types.Int32, types.Int64, types.Float64, types.Bool:
			return numberMembers
		case types.String:
			return stringMembers
		case types.Error:
			return errorMembers
		case types.Context:
			return contextMembers
		case types.Transaction:
			return txMembers
		}
	}
	return nil
}

// innermostIdentCovering returns the smallest *ast.Identifier whose offset
// range covers [start, end), or nil.
//
// ast.Inspect does not descend into import or re-export declarations, so the
// identifiers there are never returned (callers handle those statements).
func innermostIdentCovering(prog *ast.Program, start, end int) *ast.Identifier {
	var best *ast.Identifier
	ast.Inspect(prog, func(n ast.Node) bool {
		id, ok := n.(*ast.Identifier)
		if !ok {
			return true
		}
		p, e := id.Pos().Offset, id.End().Offset
		if p <= start && end <= e {
			if best == nil || (p >= best.Pos().Offset && e <= best.End().Offset) {
				best = id
			}
		}
		return true
	})
	return best
}

// covers reports whether node's offset range contains offset.
func covers(n ast.Node, offset int) bool {
	return n.Pos().Offset <= offset && offset < n.End().Offset
}

// objectKind maps an Object to a CompletionItemKind.
func objectKind(obj types.Object) int {
	switch o := obj.(type) {
	case *types.Func:
		return completionKindFunction
	case *types.Var:
		if o.IsConst {
			return completionKindConstant
		}
		return completionKindVariable
	case *types.TypeName:
		return completionKindInterface
	case *types.Builtin, *types.ModuleNS:
		return completionKindModule
	}
	return completionKindVariable
}

// builtinMemberKind classifies a builtin member as a field or a method.
func builtinMemberKind(id types.BuiltinID) int {
	if id.Info().Method {
		return completionKindFunction
	}
	return completionKindField
}

func detailOf(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Var:
		return typeStringOf(o)
	case *types.Func:
		return sigDetail(o)
	}
	return ""
}

func typeStringOf(v *types.Var) string {
	if v == nil || v.Type == nil {
		return ""
	}
	return v.Type.String()
}

func sigDetail(f *types.Func) string {
	if f == nil || f.Sig == nil {
		return ""
	}
	return f.Sig.String()
}
