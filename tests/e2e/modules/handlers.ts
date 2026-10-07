// Handler module: a decorated Context receiver method and a plain handler. It
// imports the auth guard by name and the data model as a namespace, so the
// e2e fixture exercises a named import (guard) and a namespace import (m.*)
// across the module boundary.

import { authGuard } from "./guards";
import * as m from "./models";

export @Use(authGuard) fn (ctx: Context) handleEcho(): void {
    let req = new m.EchoReq();
    if (!ctx.bindJson(req)) {
        ctx.text(400, "Invalid JSON Payload");
        return;
    }
    let echo = new m.EchoResp();
    echo.id = req.id;
    echo.name = req.name;
    ctx.json(201, echo);
}

export fn (ctx: Context) handleBench(): void {
    let res = new m.BenchResp();
    res.id = 1;
    res.name = "bench";
    ctx.json(200, res);
}

// notFound is a plain exported function (not a method), re-exported by
// routes.ts and imported by the root dispatcher — the re-export leg of the
// fixture.
export fn notFound(ctx: Context): void {
    ctx.text(404, "Endpoint Not Found");
}
