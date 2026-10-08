import { console } from "tlang/system";


fn allow(ctx: Context): bool {
    return true;
}

fn metric(ctx: Context): void {
    console.info("done");
}

fn audit(ctx: Context): void {
    throw "boom";
}

@Use(allow) @After(metric, audit)
fn (ctx: Context) handle(): void {
    ctx.text(200, "ok");
}

@After(metric)
fn tally(c: Context, base: int64): int64 {
    return base + 1;
}

@After(metric) @After(audit)
fn (ctx: Context) split(): void {
    return;
}

@Use(allow) @After(metric)
fn (ctx: Context) risky(): void {
    throw "nope";
}

fn route_dispatcher(ctx: Context): void {
    ctx.handle();
    let n = tally(ctx, 5);
    ctx.split();
    ctx.risky();
}
