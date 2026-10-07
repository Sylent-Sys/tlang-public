package codegen

import (
	"strconv"

	"tlang/ast"
	"tlang/types"
)

// Routes (codegen design §10.5, plan D18). ctx.match lowers to a call on a
// static const route table named by the route id; lowering registers the id
// so region D emits exactly the referenced tables (Info.Routes carries
// orphans from the checker's double argument evaluation, and an unused
// static const is -Werror=unused-const-variable). emitRoutes writes the
// registered tables in Info.Routes order.

// ctxMatch lowers ctx.match(method, pattern):
// tlang_ctx_match(<ctx>, &__tl_r<ID>) and registers the route id (codegen
// design §10.5, plan D18). The method and pattern are compile-time constants
// held in Call.Route, so the arguments are not lowered; only the receiver is.
// The result is a bool, pure (no check).
func (fc *funcCtx) ctxMatch(e *ast.CallExpression, c *types.Call, discard bool) cval {
	if c.Route == nil {
		fc.g.fail(internalErr(e.Pos(), "ctx.match has no route"))
	}
	recv, _ := fc.ctxOperands(e, c)
	fc.g.routeUsed[c.Route.ID] = true
	code := "tlang_ctx_match(" + recv.code + ", &" + routeName(c.Route.ID) + ")"
	return fc.builtinResult(opVal(code, recv.stable).atomize(), discard)
}

// emitRoutes writes region D's route tables: for every Info.Routes entry
// whose id was referenced by an emitted ctx.match, the segment array (unless
// the pattern is "/", which has no segments) and the route struct, as one
// entity (codegen design §10.5, plan D18, D19). All four tlang_route members
// are written positionally; "/" uses NULL, 0 for its segments.
func (g *generator) emitRoutes(w *cwriter) {
	for _, r := range g.info.Routes {
		if !g.routeUsed[r.ID] {
			continue
		}
		w.gap()
		g.emitRoute(w, r)
	}
}

// emitRoute writes one route's segment array and route struct.
func (g *generator) emitRoute(w *cwriter, r *types.Route) {
	nseg := len(r.Segments)
	nparam := len(r.Params())
	segs := "NULL"
	if nseg > 0 {
		segs = routeSegsName(r.ID)
		w.open("static const tlang_route_seg " + segs + "[" + strconv.Itoa(nseg) + "] =")
		for i, seg := range r.Segments {
			kind := "TLANG_SEG_STATIC"
			if seg.Param {
				kind = "TLANG_SEG_PARAM"
			}
			line := "{ " + kind + ", " + g.strInit(seg.Text) + " }"
			if i < nseg-1 {
				line += ","
			}
			w.line(line)
		}
		w.close(";")
	}
	w.linef("static const tlang_route %s = { %s, %s, %d, %d };",
		routeName(r.ID), g.strInit(r.Method), segs, nseg, nparam)
}
