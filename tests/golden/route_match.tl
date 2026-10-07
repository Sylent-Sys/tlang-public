// codegen-mandatory: 15.2.2 (route tables __tl_r<ID> + __tl_r<ID>_segs)
fn check(b: bool): bool {
    return b;
}

fn neverCalled<T>(ctx: Context, x: T): bool {
    return ctx.match("GET", "/never");
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.match("GET", "/")) {
        ctx.text(200, "root");
        return;
    }
    if (ctx.match("GET", "/users/:id/posts/:post")) {
        ctx.text(200, ctx.param("id") + ctx.param("post"));
        return;
    }
    if (ctx.match("POST", "/a\\b/\"q\"")) {
        ctx.text(201, "escaped");
        return;
    }
    if (check(ctx.match("DELETE", "/api/" + "items"))) {
        ctx.text(204, "");
        return;
    }
    ctx.text(404, "nope");
}
