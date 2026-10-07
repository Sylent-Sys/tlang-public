interface C {
    x: int64;
}

interface B {
    c: C;
    tags: string[];
    maybeTags: string[] | null;
}

interface A {
    b: B;
    optB: B | null;
    name: string;
    ctx: Context;
    err: Error;
    list: C[];
}

interface Box<T> {
    v: T;
}

fn takeA(a: A): int64 {
    return a.b.c.x;
}

let shared: A = new global A();
let sharedList: C[] = new global C[]();

fn main(): void {
    let a = new A();
    let n = takeA(new A());
    let c = new C();
    let cs = new C[]();
    let nested = new int64[][]();
    let e1 = new Error("m");
    let e2 = new Error("m", 404);
    let e3 = new global Error("g");
    let bx = new Box<C>();
    let bo = new Box<C | null>();
    let bi = new Box<int64>();
}
