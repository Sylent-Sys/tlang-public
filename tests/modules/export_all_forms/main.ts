import { User, UserId, seed, greet, identity } from "./models";

fn run(): int64 {
    let u = new User();
    u.id = seed;
    u.name = "ada";
    let who = greet(u);
    let n = identity<int64>(u.id);
    let m = u.label();
    return n;
}

fn route_dispatcher(ctx: Context): void {
    ctx.ping();
}
