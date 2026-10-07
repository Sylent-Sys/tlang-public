// Package codegen is the C code generation stage of the TLang compiler
// (DESIGN.md §3): it lowers a type-checked program, the parser's
// *ast.Program together with the *types.Info the checker produced for it,
// to one C11 translation unit that includes only the runtime header
// "tlang.h" and links against the TLang runtime.
//
// The output is deterministic: the same program always gives the same bytes.
// Every list is emitted in the order of the ordered Info lists or of the
// source; Go maps are only looked up, never ranged over to produce output;
// temporaries and labels are numbered per function in emission order; and
// no time, path, environment or pointer value appears in the output.
//
// The generated C is clean only under the compiler flags the runtime is
// built with: -std=c11 -pedantic -Wall -Wextra -Wno-unused-parameter -fwrapv
// -Werror on gcc and clang (-fwrapv is load-bearing: signed overflow must
// wrap), and -std=c11 -Wall -Werror on tcc. Float % lowers to the runtime's
// tlang_mod_f64, so linking needs -lm.
package codegen

import (
	"bytes"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// Emit lowers a type-checked TLang program to a single C11 translation unit.
// prog must be the exact *ast.Program that was passed to checker.Check and
// info the *types.Info that call returned, because every map in info is
// keyed by node pointers of that tree. Neither the parser nor the checker
// may have reported an error (warnings are fine).
//
// Emit never panics. On success it returns the complete C source. Otherwise
// it returns empty bytes and an error "<file>:<line>:<col>: <reason>" (the
// position when known), whose reason starts with "unsupported input: " for a
// construct the front end accepts but C cannot express, or with "internal
// error: " for a codegen bug.
func Emit(prog *ast.Program, info *types.Info) (out []byte, err error) {
	if prog == nil || info == nil {
		return nil, internalErr(token.Position{}, "Emit needs a program and its type information")
	}
	g := &generator{
		prog:      prog,
		info:      info,
		file:      prog.File,
		routeUsed: map[int]bool{},
		dbUsed:    map[string]bool{},
	}
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, g.recovered(r)
		}
	}()
	g.run()
	return g.assemble(), nil
}

// generator holds the state of one Emit call (codegen design §2.1). It is
// single-use and not safe for concurrent use.
type generator struct {
	prog *ast.Program
	info *types.Info
	// file is prog.File, the file name every error carries.
	file string
	// cur is the position of the construct being processed, reported with an
	// error recovered from a panic.
	cur token.Position
	// sec holds one writer per output section; assemble concatenates them.
	sec [numSections]cwriter
	// names is the program-level C-name collision table (codegen design §4.4).
	names *nameTable
	// structs is the concrete interface and interface-instance list of type
	// collection, in SortByFieldDeps order (codegen design §3.1 step 2, plan
	// D4): the struct bodies and forward typedefs of region A.
	structs []*types.Named
	// slices is the non-deduplicated array-type list of type collection, in
	// discovery order (SliceTypes order, then the fixed-point extras); its
	// non-predefined elements get a TLANG_SLICE_DEFINE in region A.
	slices []*types.Array
	// sliceSet holds the element mangle of every collected array (lookup
	// only, never ranged over), so discovery never adds a duplicate.
	sliceSet map[string]bool
	// instCount is len(NamedInstances()) right after type collection, the
	// baseline of the plan D4 step 6 post-emission guard.
	instCount int
	// longStrs are the overlength string constants registered by emitted
	// code, in __tl_s<N> order (codegen design §9.3).
	longStrs []longString
	// routeUsed holds the Route.ID of every ctx.match reached while lowering
	// emitted functions (plan D18, lookup only). Region D emits Info.Routes
	// filtered by this set, so orphan routes are not emitted.
	routeUsed map[int]bool
	// dbUsed holds the Mangle of every db/tx row type reached while lowering
	// emitted functions (plan D18, lookup only). Region D emits Info.DBTypes
	// filtered by this set.
	dbUsed map[string]bool
	// jsonDemand is the recomputed JSON demand closure (plan D20), in
	// driving-list order: region C prototypes and region E bodies both read
	// it. Computed once by emitProgram.
	jsonDemand []jsonType
}

// run executes the pass sequence of codegen design §3.1: pass 1 validates
// the program and indexes its C names; the later passes collect the types
// and write the sections.
func (g *generator) run() {
	g.validate()
	g.emitProgram()
}

// emitProgram is passes 2-9 of codegen design §3.1 (plan 1.3): type
// collection, then the skeleton regions in any internal order (the sections
// are concatenated in the fixed include..G order by assemble). F2 wires every
// region; F3+ fill in the function bodies and the demand-registered tables of
// regions D and E.
func (g *generator) emitProgram() {
	g.collectTypes()
	g.jsonDemand = g.jsonClosure()
	g.emitInclude()
	g.emitTypes()
	g.emitGlobals()
	g.emitProtos()
	g.emitFuncs()
	g.emitJSON()
	g.emitInitGlobals()
	g.emitProgramStruct()
	// Region D (plan D3): long strings in __tl_s<N> order, then the route
	// tables and row descriptors the emitted functions referenced, each in
	// its Info list order. Written after the functions so the demand sets
	// routeUsed/dbUsed are complete (plan D18).
	w := g.section(secTables)
	g.writeLongStrings(w)
	g.emitRoutes(w)
	g.emitRowDescriptors(w)
	g.checkInstanceGuard()
}

// section returns the writer of section s.
func (g *generator) section(s section) *cwriter { return &g.sec[s] }

// assemble concatenates the non-empty sections in skeleton order, one empty
// line between two sections (plan D2). Each section must consist of complete
// lines and must not end with an empty line, so the unit ends with exactly
// one line feed.
func (g *generator) assemble() []byte {
	var out bytes.Buffer
	for i := range g.sec {
		b := g.sec[i].bytes()
		if len(b) == 0 {
			continue
		}
		if !bytes.HasSuffix(b, []byte("\n")) || bytes.HasSuffix(b, []byte("\n\n")) {
			g.fail(internalErr(token.Position{}, "section %d does not end with exactly one line feed", i))
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.Write(b)
	}
	return out.Bytes()
}
