interface User {
    id: int64;
}

fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn mk(u: User): User {
    return u;
}

fn (u: User) plus(n: int64): int64 {
    return n + 1;
}

fn (ctx: Context) helper(n: int64): int64 {
    return n;
}

fn calls(u: User, n: int64): int64 {
    let a = u.plus(n);
    let b = mk(u).plus(div(n, 2));
    let c = u.plus(div(n, 3));
    return a + b + c;
}

fn route_dispatcher(ctx: Context): void {
    let n = ctx.helper(1);
}
