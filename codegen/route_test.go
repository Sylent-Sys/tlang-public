package codegen

import (
	"strings"
	"testing"
)

// TestRouteTables checks the route table emission (codegen design §10.5, plan
// D18): ctx.match lowers to tlang_ctx_match on &__tl_r<ID>, a "/" pattern has
// NULL, 0 segments, static and param segments are emitted with their kinds,
// and escaped segment text goes through the octal encoder.
func TestRouteTables(t *testing.T) {
	out := mustEmit(t, `fn route_dispatcher(ctx: Context): void {
    if (ctx.match("GET", "/")) {
        ctx.text(200, "root");
        return;
    }
    if (ctx.match("GET", "/users/:id")) {
        ctx.text(200, ctx.param("id"));
        return;
    }
    if (ctx.match("POST", "/a\\b")) {
        ctx.text(201, "e");
        return;
    }
    ctx.text(404, "no");
}
`)
	// "/" has no segments.
	if !strings.Contains(out, `static const tlang_route __tl_r1 = { TLANG_STR_INIT("GET"), NULL, 0, 0 };`) {
		t.Error(`"/" route table wrong`)
	}
	// A static + param pattern with one param.
	if !strings.Contains(out, `{ TLANG_SEG_STATIC, TLANG_STR_INIT("users") },`) {
		t.Error("missing static segment")
	}
	if !strings.Contains(out, `{ TLANG_SEG_PARAM, TLANG_STR_INIT("id") }`) {
		t.Error("missing param segment")
	}
	if !strings.Contains(out, `static const tlang_route __tl_r2 = { TLANG_STR_INIT("GET"), __tl_r2_segs, 2, 1 };`) {
		t.Error("param route table wrong")
	}
	// Escaped segment text: the backslash is a literal in the segment.
	if !strings.Contains(out, `{ TLANG_SEG_STATIC, TLANG_STR_INIT("a\\b") }`) {
		t.Error("escaped segment text wrong")
	}
	// The call site references the table by address.
	if !strings.Contains(out, "tlang_ctx_match(l_ctx, &__tl_r1)") {
		t.Error("ctx.match call site wrong")
	}
}

// TestRouteOrphanNotEmitted checks that a route reached only from a
// never-instantiated generic body is not emitted (plan D18, P9), while the
// routes the dispatcher references are.
func TestRouteOrphanNotEmitted(t *testing.T) {
	out := mustEmit(t, `fn neverCalled<T>(ctx: Context, x: T): bool {
    return ctx.match("GET", "/never");
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.match("GET", "/here")) {
        ctx.text(200, "ok");
        return;
    }
    ctx.text(404, "no");
}
`)
	if !strings.Contains(out, `TLANG_STR_INIT("here")`) {
		t.Error("the referenced route should be emitted")
	}
	if strings.Contains(out, `TLANG_STR_INIT("never")`) {
		t.Error("a route reached only from a never-instantiated generic should not be emitted")
	}
}
