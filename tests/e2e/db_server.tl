// Test-only DB-backed e2e fixture (tests-suites design §3.1). A single
// route_dispatcher program that USES db (uses_db true), so — like the §13
// server — it cannot start without a database and only runs on the DB legs.
// It supplies the two capabilities examples/app.ts lacks for the fault-injection
// scenarios: a *slow* query that pins the single pooled connection (GET /slow),
// and a guarded route that starts a DB round-trip a client can abandon
// mid-flight (POST /tx). It shares app.ts's users(id bigint primary key, name
// text) schema, created idempotently by the harness (design §7.2).
//
// Uses only constructs present in the green golden corpus: route_dispatcher,
// @Use guards, ctx.header/text, db.execute with a bare SQL literal, and a
// db.transaction INSERT (see tests/golden/transactions.tl, tests/golden/db_rows.tl).

interface TxReq {
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
fn (ctx: Context) handleTx(): void {
    let req = new TxReq();
    if (!ctx.bindJson(req)) {
        ctx.text(400, "Invalid JSON Payload");
        return;
    }

    try {
        db.transaction((tx) => {
            tx.execute("INSERT INTO users(id, name) VALUES ($1, $2)", req.id, req.name);
        });

        let res = new TxReq();
        res.id = req.id;
        res.name = req.name;
        ctx.json(201, res);
    } catch (err) {
        ctx.text(500, "Database Transaction Failed");
    }
}

fn route_dispatcher(ctx: Context): void {
    // GET /slow: a FIXED 2s sleep via db.execute (design §3.1 — db.execute, not
    // db.queryOne; a fixed literal, not a tunable ?ms). The sleep holds the one
    // pooled connection for the whole duration so TestE2E_EmptyPool can exhaust
    // the pool; 2s sits below the default statement timeout (5000ms).
    if (ctx.method.eq("GET") && ctx.path.eq("/slow")) {
        let n = db.execute("SELECT pg_sleep(2)");
        ctx.text(200, "slept");
        return;
    }
    // POST /tx: guarded like app.ts, runs a db.transaction INSERT so a client
    // can disconnect mid-round-trip (TestE2E_ClientDisconnect).
    if (ctx.method.eq("POST") && ctx.path.eq("/tx")) {
        ctx.handleTx();
        return;
    }
    ctx.text(404, "Endpoint Not Found");
}
