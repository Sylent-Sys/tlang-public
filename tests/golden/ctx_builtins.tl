fn route_dispatcher(ctx: Context): void {
    let m = ctx.method;
    let p = ctx.path;
    let q = ctx.rawQuery;
    let b = ctx.body;
    let h = ctx.header("X-Id");
    let qv = ctx.query("page");
    if (ctx.match("GET", "/users/:id")) {
        let id = ctx.param("id");
        let n = ctx.paramInt("id");
        ctx.setHeader("X-Id", id);
        ctx.text(200, n.toString());
        return;
    }
    ctx.text(404, m + p + q + b + h + qv);
}
