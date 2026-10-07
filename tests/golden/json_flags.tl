interface C {
    v: int64;
}

interface A {
    c: C;
}

interface B {
    a: A;
}

interface N {
    name: string;
    kids: N[];
}

fn route_dispatcher(ctx: Context): void {
    let a = new A();
    ctx.json(200, a);
    let b = new B();
    if (ctx.bindJson(b)) {
        ctx.text(200, "b");
    }
    let n = new N();
    if (ctx.bindJson(n)) {
        ctx.json(200, n);
    }
}
