// Test-only DB-free e2e fixture (tests-suites design §3). A single
// route_dispatcher program with no db usage (uses_db false), so it starts
// without a database. It provides every route the non-DB e2e scenarios and the
// benchmark need. Uses only constructs present in the green golden corpus:
// route_dispatcher, @Use guards, ctx.header/text/json/bindJson, new T() + field
// assignment, and a bare throw.

interface BenchResp {
    id: int64;
    name: string;
}

// WorkItem is the shared element type for the real-work routes /json and /work.
// Scalar int64/string fields only (no floats, printable-ASCII strings) so the
// serialized bytes are byte-identical to JSON.stringify on Node/Bun.
interface WorkItem {
    id: int64;
    label: string;
    score: int64;
}

// JsonResp is the nested, array-bearing response for GET /json.
interface JsonResp {
    id: int64;
    name: string;
    active: bool;
    count: int64;
    items: WorkItem[];
    tags: string[];
}

interface EchoReq {
    id: int64;
    name: string;
}

interface EchoResp {
    id: int64;
    name: string;
}

// authGuard mirrors examples/app.ts: 401 "Unauthorized" when the Authorization
// header is empty, otherwise the request passes through to the handler body.
fn authGuard(ctx: Context): bool {
    let auth = ctx.header("Authorization");
    if (auth.len == 0) {
        ctx.text(401, "Unauthorized");
        return false;
    }
    return true;
}

@Use(authGuard)
fn (ctx: Context) handleEcho(): void {
    let req = new EchoReq();
    if (!ctx.bindJson(req)) {
        ctx.text(400, "Invalid JSON Payload");
        return;
    }

    let echo = new EchoResp();
    echo.id = req.id;
    echo.name = req.name;
    ctx.json(201, echo);
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.method.eq("GET") && ctx.path.eq("/healthz")) {
        ctx.text(200, "ok");
        return;
    }
    if (ctx.method.eq("GET") && ctx.path.eq("/bench")) {
        let res = new BenchResp();
        res.id = 1;
        res.name = "bench";
        ctx.json(200, res);
        return;
    }
    if (ctx.method.eq("POST") && ctx.path.eq("/echo")) {
        ctx.handleEcho();
        return;
    }
    if (ctx.method.eq("GET") && ctx.path.eq("/silent")) {
        return;
    }
    if (ctx.method.eq("GET") && ctx.path.eq("/boom")) {
        throw "boom";
    }
    // /json: nested object with 16 WorkItems built per request (object + array
    // writer path + nested-struct allocation). 716 bytes.
    if (ctx.method.eq("GET") && ctx.path.eq("/json")) {
        let resp = new JsonResp();
        resp.id = 7;
        resp.name = "payload";
        resp.active = true;
        resp.count = 16;
        let items = new WorkItem[]();
        for (let i: int64 = 0; i < 16; i++) {
            let it = new WorkItem();
            it.id = i;
            it.label = "item-" + i.toString();
            it.score = i * 100;
            items.push(it);
        }
        resp.items = items;
        let tags = new string[]();
        tags.push("alpha");
        tags.push("beta");
        tags.push("gamma");
        resp.tags = tags;
        ctx.json(200, resp);
        return;
    }
    // /work: top-level array of 32 rows, each with a built string, built per
    // request (allocation churn + string building + array serialization). 1407
    // bytes — the primary real-work signal.
    if (ctx.method.eq("GET") && ctx.path.eq("/work")) {
        let rows = new WorkItem[]();
        for (let i: int64 = 0; i < 32; i++) {
            let row = new WorkItem();
            row.id = i;
            row.label = "row-" + i.toString() + "-of-32";
            row.score = i * i;
            rows.push(row);
        }
        ctx.json(200, rows);
        return;
    }
    ctx.text(404, "Endpoint Not Found");
}
