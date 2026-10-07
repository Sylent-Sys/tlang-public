fn mkErr(code: int32): Error {
    return new Error("made", code);
}

fn thrower(kind: int64, s: string): void {
    if (kind == 0) {
        throw "plain";
    }
    if (kind == 1) {
        throw new Error("one");
    }
    if (kind == 2) {
        throw new Error("two", 418);
    }
    if (kind == 3) {
        let e = new Error("three", 401);
        throw e;
    }
    if (kind == 4) {
        throw mkErr(503);
    }
    if (kind == 5) {
        throw new global Error("global", 409);
    }
    throw "dynamic: " + s;
}

fn route_dispatcher(ctx: Context): void {
    try {
        thrower(1, "x");
    } catch (e) {
        throw e;
    }
}
