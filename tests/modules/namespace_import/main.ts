import * as m from "./models";

fn use(u: m.User): string {
    return m.greet(u);
}

fn main(): void {
    let u = new m.User();
    u.name = "grace";
    let s = use(u);
}
