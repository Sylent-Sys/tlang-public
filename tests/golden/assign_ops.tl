interface Node {
    n: int64;
    f: float64;
    s: string;
    xs: int64[];
    child: Node | null;
}

let g: int64 = 0;
let gs: string = "";

fn getArr(xs: int64[]): int64[] {
    return xs;
}

fn idx(i: int64): int64 {
    return i;
}

fn val(v: int64): int64 {
    return v * 2;
}

fn obj(nd: Node): Node {
    return nd;
}

fn mkErr(): Error {
    return new Error("e", 400);
}

fn assigns(nd: Node, xs: int64[], d: int64): void {
    let x: int64 = 1;
    x = 2;
    x += val(3);
    let f: float64 = 1.0;
    f %= 2.0;
    let s = "a";
    s += "b";
    g = g + 1;
    g += val(2);
    g++;
    gs += "z";
    nd.n = 5;
    nd.n += d;
    nd.n++;
    nd.f *= 2.0;
    nd.s += "!";
    nd.n /= d;
    xs[0] = 1;
    xs[1] += 2;
    xs[d] = val(d);
    xs[d] %= d;
    getArr(xs)[idx(0)] = val(1);
    getArr(xs)[idx(1)] += val(2);
    obj(nd).n = val(3);
    obj(nd).n -= val(4);
    nd.xs[0] = 9;
    let e = new Error("m", 500);
    e.status = 404;
    e.status += 1;
    e.status++;
    e.status /= 2;
    e.message = "x";
    e.message += "!";
    mkErr().status += 1;
    mkErr().message = "discarded";
}
