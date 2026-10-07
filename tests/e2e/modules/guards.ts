// Guard module: the auth guard imported by the handler module's decorator.

export fn authGuard(ctx: Context): bool {
    let auth = ctx.header("Authorization");
    if (auth.len == 0) {
        ctx.text(401, "Unauthorized");
        return false;
    }
    return true;
}
