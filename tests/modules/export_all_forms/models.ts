// AC-5: export on all four forms — an interface, a type alias, a function,
// and a global — plus a receiver method, a generic function, and a decorated
// handler, all exported and consumed across the module boundary.

export interface User {
    id: int64;
    name: string;
}

export type UserId = int64;

export let seed: int64 = 7;

export fn greet(u: User): string {
    return u.name;
}

export fn (u: User) label(): string {
    return u.name;
}

export fn identity<T>(x: T): T {
    return x;
}

fn allow(ctx: Context): bool {
    return true;
}

export @Use(allow) fn (ctx: Context) ping(): void {
    ctx.text(200, "ok");
}
