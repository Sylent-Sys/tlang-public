fn allow(ctx: Context): bool {
    return true;
}

fn risky(ctx: Context): bool {
    let d: int64 = 0;
    return 10 / d > 0;
}

@Use(allow, risky)
fn (ctx: Context) guarded(): void {
    let n: int64 = 1;
    return;
}

@Use(allow)
fn count(c: Context, base: int64): int64 {
    return base + 1;
}

@Use(risky)
fn (ctx: Context) value(): string {
    return "v";
}

fn route_dispatcher(ctx: Context): void {
    ctx.guarded();
    let n = count(ctx, 5);
    let s = ctx.value();
}
