fn route_dispatcher(ctx: Context): void {
    let n = db.execute("UPDATE t SET a = 1\0 WHERE b = 2");
    ctx.text(200, n.toString());
}
