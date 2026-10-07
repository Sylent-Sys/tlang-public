// Multi-file e2e root (FEAT-004 / AC-31, AC-33): a route_dispatcher that
// imports types from a namespace module and a handler from a re-export module,
// and dispatches to Context receiver methods declared in the handler module.
// Driven over real HTTP exactly like the single-file echo_server fixture.

import * as m from "./modules/models";
import { notFound } from "./modules/routes";

fn route_dispatcher(ctx: Context): void {
    if (ctx.method.eq("GET") && ctx.path.eq("/healthz")) {
        ctx.text(200, "ok");
        return;
    }
    if (ctx.method.eq("GET") && ctx.path.eq("/bench")) {
        ctx.handleBench();
        return;
    }
    if (ctx.method.eq("POST") && ctx.path.eq("/echo")) {
        ctx.handleEcho();
        return;
    }
    if (ctx.method.eq("GET") && ctx.path.eq("/boom")) {
        throw "boom";
    }
    notFound(ctx);
}
