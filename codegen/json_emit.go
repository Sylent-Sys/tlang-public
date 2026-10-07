package codegen

import (
	"strconv"
	"strings"

	"tlang/types"
)

// JSON parsers and writers (codegen design §10.4, §3.5, plan D20; P1, P2,
// P11). The demand closure is recomputed from Info.JSONTypes to a fixed
// point (the Info flags are not propagated to field/element dependencies and
// an array inside its own element type is listed twice, map-types §9.8), so
// every called tlj_* is defined and no nonexistent tlj_parse_str/tlj_write_str
// is ever emitted. Region C holds the prototypes (after the user functions),
// region E the bodies, both in the closure's driving-list order (parser
// before writer per type); correctness does not depend on the order because
// every function is forward-prototyped.
//
// Scanners are the runtime json_scan_int32/int64/float64/bool/string/null/key
// and json_skip_value/json_skip_ws (P1); writers json_write_i32/i64/f64/str/
// bool/null and TLANG_BUF_PUT_LIT. JSON functions are not TLang functions, so
// they use fixed local names (__q cursor, __seen, __key, __o fresh object,
// __a parsed array, __e scanned element, __s slice being built, __i index)
// and no per-function counters.

// jsonType is one entry of the recomputed demand closure: a concrete *Named
// or *Array type and the directions demanded of it.
type jsonType struct {
	t     types.Type
	parse bool
	write bool
}

// jsonClosure recomputes the JSON demand closure (plan D20): seed from
// Info.JSONTypes with their flags, then propagate Parse/Write to every
// field/element dependency to a fixed point, deduplicating by Mangle. The
// result is in first-appearance order (seeds in Info.JSONTypes order, then
// newly discovered types in discovery order). The set contains only *Named
// and *Array types; a primitive/bool/string field or element contributes no
// generated function, an optional contributes its non-null type, and an
// array of arrays contributes the inner array. A demanded *Named wider than
// maxJSONFields is §12 item 10.
func (g *generator) jsonClosure() []jsonType {
	var order []jsonType
	index := map[string]int{} // Mangle -> position in order (lookup only)

	// demand records that t needs the parse and/or write direction, adding
	// it to the order on first appearance and reporting whether a flag was
	// newly set (so the fixed point keeps iterating).
	demand := func(t types.Type, parse, write bool) bool {
		m := g.mangle(t)
		if i, ok := index[m]; ok {
			changed := false
			if parse && !order[i].parse {
				order[i].parse = true
				changed = true
			}
			if write && !order[i].write {
				order[i].write = true
				changed = true
			}
			return changed
		}
		index[m] = len(order)
		order = append(order, jsonType{t: t, parse: parse, write: write})
		return true
	}

	for _, jt := range g.info.JSONTypes {
		demand(jt.Type, jt.Parse, jt.Write)
	}

	for {
		changed := false
		for i := 0; i < len(order); i++ {
			cur := order[i]
			for _, dep := range g.jsonDeps(cur.t) {
				if demand(dep, cur.parse, cur.write) {
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	for _, jt := range order {
		if n, ok := jt.t.(*types.Named); ok {
			g.checkJSONWidth(n)
		}
	}
	return order
}

// jsonDeps returns the generated-function dependencies of a demand-closure
// type (plan D20): for a *Named, every field whose generated category is
// *Named or *Array; for an *Array, its element. An optional contributes its
// non-null type; a primitive/bool/string contributes nothing.
func (g *generator) jsonDeps(t types.Type) []types.Type {
	var deps []types.Type
	add := func(ft types.Type) {
		if d := jsonGenDep(ft); d != nil {
			deps = append(deps, d)
		}
	}
	switch u := t.(type) {
	case *types.Named:
		for _, f := range u.Fields() {
			add(f.Type)
		}
	case *types.Array:
		add(u.Elem)
	}
	return deps
}

// jsonGenDep returns the generated-function type a field/element of type ft
// contributes, or nil when it is inlined as a runtime primitive.
func jsonGenDep(ft types.Type) types.Type {
	ft = types.NonOptional(ft)
	switch ft.(type) {
	case *types.Named, *types.Array:
		return ft
	}
	return nil
}

// emitJSONProtos writes the JSON prototypes into region C, after the user
// functions (codegen design §3.5, §10.4). Each demanded direction gets one
// prototype, parser before writer per type, in closure order.
func (g *generator) emitJSONProtos(w *cwriter) {
	for _, jt := range g.jsonDemand {
		if jt.parse {
			w.line(g.jsonParseSig(jt.t) + ";")
		}
		if jt.write {
			w.line(g.jsonWriteSig(jt.t) + ";")
		}
	}
}

// emitJSON writes region E: the body of every demanded parser and writer, in
// closure order (codegen design §10.4). An empty region emits nothing.
func (g *generator) emitJSON() {
	w := g.section(secJSON)
	for _, jt := range g.jsonDemand {
		if jt.parse {
			w.gap()
			g.emitJSONParser(w, jt.t)
		}
		if jt.write {
			w.gap()
			g.emitJSONWriter(w, jt.t)
		}
	}
}

// jsonParseSig returns the parser signature header for a *Named or *Array
// type, without the trailing " {" or ";" (codegen design §10.4; P2). The
// array variant fills a tlang_slice_<m>** because it creates the slice.
func (g *generator) jsonParseSig(t types.Type) string {
	name := g.jsonParseName(t)
	if arr, ok := t.(*types.Array); ok {
		return "static bool " + name + "(tlang_fiber* __fib, const char** __p, const char* __end, " +
			g.sliceName(arr.Elem) + "** __out, int __depth)"
	}
	return "static bool " + name + "(tlang_fiber* __fib, const char** __p, const char* __end, " +
		g.ctype(t) + " __out, int __depth)"
}

// jsonWriteSig returns the writer signature header for a *Named or *Array
// type (codegen design §10.4; P2). The scalar writer takes tl_<m>*, the
// array variant tlang_slice_<m>*.
func (g *generator) jsonWriteSig(t types.Type) string {
	name := g.jsonWriteName(t)
	if arr, ok := t.(*types.Array); ok {
		return "static void " + name + "(tlang_fiber* __fib, tlang_buf* __b, " +
			g.sliceName(arr.Elem) + "* __v)"
	}
	return "static void " + name + "(tlang_fiber* __fib, tlang_buf* __b, " + g.ctype(t) + " __v)"
}

// emitJSONParser writes a parser body (codegen design §10.4): the array
// variant for an *Array, the scalar object parser for a *Named.
func (g *generator) emitJSONParser(w *cwriter, t types.Type) {
	if arr, ok := t.(*types.Array); ok {
		g.emitArrayParser(w, arr)
		return
	}
	g.emitScalarParser(w, t.(*types.Named))
}

// emitJSONWriter writes a writer body (codegen design §10.4): the array
// variant for an *Array, the scalar object writer for a *Named.
func (g *generator) emitJSONWriter(w *cwriter, t types.Type) {
	if arr, ok := t.(*types.Array); ok {
		g.emitArrayWriter(w, arr)
		return
	}
	g.emitScalarWriter(w, t.(*types.Named))
}

// emitScalarParser writes an object parser (codegen design §10.4, the probe
// q1 shape with a uint64_t seen-mask, plan D20). Each known field is a
// key.len+memcmp branch scanning by category; an unknown key is skipped;
// absence or JSON null gives null for an optional field; at __done the
// required-mask test is returned (true when there are no required fields). An
// empty interface has no seen-mask and parses an object that it fully skips.
func (g *generator) emitScalarParser(w *cwriter, n *types.Named) {
	fields := n.Fields()
	hasFields := len(fields) > 0

	w.open(g.jsonParseSig(n))
	w.line("const char* __q = *__p;")
	if hasFields {
		w.line("uint64_t __seen = 0;")
	}
	w.line("if (__depth >= json_max_depth()) return false;")
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q >= __end || *__q != '{') return false;")
	w.line("__q++;")
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q < __end && *__q == '}') { __q++; goto __done; }")

	w.open("for (;;)")
	w.line("tlang_string __key;")
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (!json_scan_key(&__q, __end, &__key)) return false;")
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q >= __end || *__q != ':') return false;")
	w.line("__q++;")
	w.line("__q = json_skip_ws(__q, __end);")

	if hasFields {
		for i, f := range fields {
			cond := g.jsonKeyTest(f.Name)
			if i == 0 {
				w.open("if (" + cond + ")")
			} else {
				w.reopen("} else if (" + cond + ") {")
			}
			g.emitFieldParse(w, f, i)
		}
		w.reopen("} else {")
		w.line("if (!json_skip_value(&__q, __end, __depth)) return false;")
		w.close("")
	} else {
		w.line("if (!json_skip_value(&__q, __end, __depth)) return false;")
	}

	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q >= __end) return false;")
	w.line("if (*__q == ',') { __q++; continue; }")
	w.line("if (*__q == '}') { __q++; break; }")
	w.line("return false;")
	w.close("")

	w.label("__done")
	w.line("*__p = __q;")
	req := g.requiredMask(fields)
	if req == "" {
		w.line("return true;")
	} else {
		w.linef("return (__seen & %s) == %s;", req, req)
	}
	w.close("")
}

// jsonKeyTest returns the C condition matching the object key name:
// __key.len == <n> && memcmp(__key.data, "...", <n>) == 0, using a long
// string constant for a key over 4095 bytes (plan D15).
func (g *generator) jsonKeyTest(name string) string {
	n := len(name)
	lenEq := "__key.len == " + strconv.Itoa(n)
	if n == 0 {
		// An empty key cannot name a field (field names are non-empty), but
		// keep the shape well-formed.
		return lenEq + " && memcmp(__key.data, " + cQuote(name) + ", 0) == 0"
	}
	ref := cQuote(name)
	if n > 4095 {
		ref = g.longKeyRef(name)
	}
	return lenEq + " && memcmp(__key.data, " + ref + ", " + strconv.Itoa(n) + ") == 0"
}

// longKeyRef registers a long key as a static char array and returns its C
// name for memcmp (plan D15). strExpr yields "((tlang_string){ __tl_sN,
// len })"; the array name token is extracted for the raw memcmp argument.
func (g *generator) longKeyRef(name string) string {
	expr := g.strExpr(name)
	start := strings.Index(expr, "__tl_s")
	end := strings.IndexByte(expr[start:], ',')
	return expr[start : start+end]
}

// emitFieldParse writes the branch body for one object field (codegen design
// §10.4): scan the value by category into the struct member, then mark the
// seen bit. An optional field is null on JSON null, else the present value.
func (g *generator) emitFieldParse(w *cwriter, f *types.Var, idx int) {
	member := "__out->" + fieldName(f.Name)
	ft := f.Type
	base := types.NonOptional(ft)

	if types.IsOptional(ft) {
		w.open("if (json_scan_null(&__q, __end))")
		w.linef("%s = %s;", member, g.jsonNull(ft))
		w.reopen("} else {")
		g.emitScan(w, base, member, true)
		w.close("")
	} else {
		g.emitScan(w, base, member, false)
	}
	w.linef("__seen |= UINT64_C(1) << %d;", idx)
}

// emitScan writes the scan of one non-null value of type base into the
// struct member (codegen design §10.4). optional says the member is an
// optional whose present form must be set. A primitive/bool/string scans
// inline with the runtime scanner; a *Named parses into a freshly zeroed
// object; an array calls the array parser.
func (g *generator) emitScan(w *cwriter, base types.Type, member string, optional bool) {
	switch u := base.(type) {
	case *types.Named:
		ct := g.ctype(u)
		st := g.structName(u)
		w.linef("%s __o = (%s)tlang_alloc_zeroed(__fib, sizeof(%s));", ct, ct, st)
		w.linef("if (!%s(__fib, &__q, __end, __o, __depth + 1)) return false;", g.jsonParseName(u))
		w.linef("%s = __o;", member)
	case *types.Array:
		slice := g.sliceName(u.Elem)
		w.linef("%s* __a = NULL;", slice)
		w.linef("if (!%s(__fib, &__q, __end, &__a, __depth + 1)) return false;", g.jsonParseName(u))
		w.linef("%s = __a;", member)
	default:
		g.emitPrimScan(w, base, member, optional)
	}
}

// emitPrimScan writes the inline runtime scan of a primitive/bool/string
// value into the struct member (codegen design §10.4). A string stores into
// the member directly (a present string|null is the string itself); a
// primitive optional scans into the .v payload and sets .has.
func (g *generator) emitPrimScan(w *cwriter, base types.Type, member string, optional bool) {
	scanner := jsonScanner(base)
	if types.IsString(base) {
		// A string member and a string|null member share the tlang_string
		// representation; scan directly into the member.
		w.linef("if (!%s(&__q, __end, __fib->arena, &%s)) return false;", scanner, member)
		return
	}
	if optional {
		// A primitive optional member is tlang_opt_<m>{ has, v }: scan into
		// .v, then mark it present.
		w.linef("if (!%s(&__q, __end, &%s.v)) return false;", scanner, member)
		w.linef("%s.has = true;", member)
		return
	}
	w.linef("if (!%s(&__q, __end, &%s)) return false;", scanner, member)
}

// jsonScanner returns the runtime scanner name for a primitive/bool/string
// base type (codegen design §10.4; P1): json_scan_int32/int64/float64/bool/
// string.
func jsonScanner(base types.Type) string {
	switch {
	case types.IsBasic(base, types.Int32):
		return "json_scan_int32"
	case types.IsBasic(base, types.Int64):
		return "json_scan_int64"
	case types.IsFloat(base):
		return "json_scan_float64"
	case types.IsBool(base):
		return "json_scan_bool"
	case types.IsString(base):
		return "json_scan_string"
	}
	return "json_scan_int64"
}

// emitArrayParser writes an array parser (codegen design §10.4, plan D20):
// depth check, '[', elements separated by ',', ']'; it creates the slice
// with TLANG_SLICE_NEW and pushes one scanned element at a time, storing the
// slice through __out on success. Containers inside get __depth + 1.
func (g *generator) emitArrayParser(w *cwriter, arr *types.Array) {
	elem := arr.Elem
	slice := g.sliceName(elem)
	mangle := g.mangle(elem)

	w.open(g.jsonParseSig(arr))
	w.line("const char* __q = *__p;")
	w.line("if (__depth >= json_max_depth()) return false;")
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q >= __end || *__q != '[') return false;")
	w.line("__q++;")
	w.linef("%s* __s = TLANG_SLICE_NEW(__fib, %s);", slice, mangle)
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q < __end && *__q == ']') { __q++; goto __done; }")

	w.open("for (;;)")
	w.line("__q = json_skip_ws(__q, __end);")
	g.emitElemScan(w, elem)
	w.line("__q = json_skip_ws(__q, __end);")
	w.line("if (__q >= __end) return false;")
	w.line("if (*__q == ',') { __q++; continue; }")
	w.line("if (*__q == ']') { __q++; break; }")
	w.line("return false;")
	w.close("")

	w.label("__done")
	w.line("*__out = __s;")
	w.line("*__p = __q;")
	w.line("return true;")
	w.close("")
}

// emitElemScan scans one array element of type elem into a local __e and
// pushes it onto __s (codegen design §10.4). The element category mirrors a
// field: an optional element is null on JSON null, else present; a primitive/
// bool/string scans inline; a *Named/*Array uses the generated parser.
func (g *generator) emitElemScan(w *cwriter, elem types.Type) {
	ct := g.ctype(elem)
	w.linef("%s __e;", ct)
	base := types.NonOptional(elem)

	if types.IsOptional(elem) {
		w.open("if (json_scan_null(&__q, __end))")
		w.linef("__e = %s;", g.jsonNull(elem))
		w.reopen("} else {")
		g.emitScan(w, base, "__e", true)
		w.close("")
	} else {
		g.emitScan(w, base, "__e", false)
	}
	w.linef("TLANG_SLICE_PUSH(__fib, __s, %s);", "__e")
}

// emitScalarWriter writes an object writer (codegen design §10.4, plan D20):
// the "{\"field\":" punctuation via TLANG_BUF_PUT_LIT, each field by category,
// then '}'. An optional field branches on presence; an empty interface writes
// "{}".
func (g *generator) emitScalarWriter(w *cwriter, n *types.Named) {
	fields := n.Fields()
	w.open(g.jsonWriteSig(n))
	if len(fields) == 0 {
		w.line(`TLANG_BUF_PUT_LIT(__b, "{}");`)
		w.close("")
		return
	}
	for i, f := range fields {
		if i == 0 {
			w.linef(`TLANG_BUF_PUT_LIT(__b, "{%s:");`, jsonKeyPunct(f.Name))
		} else {
			w.linef(`TLANG_BUF_PUT_LIT(__b, ",%s:");`, jsonKeyPunct(f.Name))
		}
		g.emitFieldWrite(w, f, "__v->"+fieldName(f.Name))
	}
	w.line("tlang_buf_putc(__b, '}');")
	w.close("")
}

// emitFieldWrite writes one object field value (codegen design §10.4): an
// optional field branches on presence (present writes the payload, absent
// writes null); a non-optional writes by category.
func (g *generator) emitFieldWrite(w *cwriter, f *types.Var, member string) {
	ft := f.Type
	base := types.NonOptional(ft)
	if types.IsOptional(ft) {
		w.open("if (" + g.jsonPresent(ft, member) + ")")
		g.emitValueWrite(w, base, g.jsonPayload(ft, member))
		w.reopen("} else {")
		w.line("json_write_null(__b);")
		w.close("")
		return
	}
	g.emitValueWrite(w, base, member)
}

// emitValueWrite writes a present value of non-optional type base from the C
// expression src (codegen design §10.4): a primitive/bool/string inline, a
// *Named/*Array through the generated writer.
func (g *generator) emitValueWrite(w *cwriter, base types.Type, src string) {
	switch u := base.(type) {
	case *types.Named:
		w.linef("%s(__fib, __b, %s);", g.jsonWriteName(u), src)
	case *types.Array:
		w.linef("%s(__fib, __b, %s);", g.jsonWriteName(u), src)
	default:
		w.linef("%s(__b, %s);", jsonWriter(base), src)
	}
}

// jsonWriter returns the runtime writer name for a primitive/bool/string
// base type (codegen design §10.4): json_write_i32/i64/f64/str/bool.
func jsonWriter(base types.Type) string {
	switch {
	case types.IsBasic(base, types.Int32):
		return "json_write_i32"
	case types.IsBasic(base, types.Int64):
		return "json_write_i64"
	case types.IsFloat(base):
		return "json_write_f64"
	case types.IsBool(base):
		return "json_write_bool"
	case types.IsString(base):
		return "json_write_str"
	}
	return "json_write_i64"
}

// jsonPresent returns the C present-test of an optional member (plan D20,
// D28): .has for a primitive optional, .data != NULL for a string optional,
// != NULL for a reference optional.
func (g *generator) jsonPresent(ft types.Type, member string) string {
	elem := types.NonOptional(ft)
	switch elem.(type) {
	case *types.Named, *types.Array:
		return member + " != NULL"
	}
	if types.IsString(elem) {
		return member + ".data != NULL"
	}
	return member + ".has"
}

// jsonPayload returns the C expression that extracts the present payload of
// an optional member (plan D28): .v for a primitive optional, the member
// itself for a string or reference optional (same representation).
func (g *generator) jsonPayload(ft types.Type, member string) string {
	elem := types.NonOptional(ft)
	switch elem.(type) {
	case *types.Named, *types.Array:
		return member
	}
	if types.IsString(elem) {
		return member
	}
	return member + ".v"
}

// emitArrayWriter writes an array writer (codegen design §10.4, plan D20):
// '[', each element preceded by ',' except the first, ']'. Elements are
// written by category, mirroring the object writer.
func (g *generator) emitArrayWriter(w *cwriter, arr *types.Array) {
	elem := arr.Elem
	base := types.NonOptional(elem)

	w.open(g.jsonWriteSig(arr))
	w.line("tlang_buf_putc(__b, '[');")
	w.open("for (int64_t __i = 0; __i < __v->len; __i++)")
	w.line("if (__i > 0) tlang_buf_putc(__b, ',');")
	w.linef("%s __e = __v->items[__i];", g.ctype(elem))
	if types.IsOptional(elem) {
		w.open("if (" + g.jsonPresent(elem, "__e") + ")")
		g.emitValueWrite(w, base, g.jsonPayload(elem, "__e"))
		w.reopen("} else {")
		w.line("json_write_null(__b);")
		w.close("")
	} else {
		g.emitValueWrite(w, base, "__e")
	}
	w.close("")
	w.line("tlang_buf_putc(__b, ']');")
	w.close("")
}

// requiredMask returns the UINT64_C(0x...) mask of the required (non-optional)
// fields, or "" when there are none (plan D20).
func (g *generator) requiredMask(fields []*types.Var) string {
	var mask uint64
	for i, f := range fields {
		if !types.IsOptional(f.Type) {
			mask |= uint64(1) << uint(i)
		}
	}
	if mask == 0 {
		return ""
	}
	return "UINT64_C(0x" + strconv.FormatUint(mask, 16) + ")"
}

// jsonNull returns the C null form of an optional field/element type (plan
// D20, D28): NULL for a reference optional (interface or array),
// TLANG_STR_NULL for a string optional, TLANG_NONE(<m>) for a primitive
// optional.
func (g *generator) jsonNull(ft types.Type) string {
	elem := types.NonOptional(ft)
	switch elem.(type) {
	case *types.Named, *types.Array:
		return "NULL"
	}
	if types.IsString(elem) {
		return "TLANG_STR_NULL"
	}
	return "TLANG_NONE(" + g.mangle(elem) + ")"
}

// jsonKeyPunct returns the escaped-for-C key text used in a writer's
// TLANG_BUF_PUT_LIT: the JSON key (a quoted string) with the C escaping of
// the surrounding "field" punctuation. Field names are plain identifiers in
// the goldens, so the key is the name wrapped in escaped quotes.
func jsonKeyPunct(name string) string {
	return `\"` + jsonKeyPrefix(name) + `\"`
}

// jsonKeyPrefix returns the field name escaped for a C string literal inside
// TLANG_BUF_PUT_LIT (a '"' or '\\' in the name would break the literal). TLang
// field names are identifiers, so this is almost always the name verbatim.
func jsonKeyPrefix(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
