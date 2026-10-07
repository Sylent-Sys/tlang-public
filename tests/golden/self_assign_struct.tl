// codegen-mandatory: 15.2.1 (struct self-assignment lowers to (void)x;)
fn selfAssign(s: string, o: int64 | null, so: string | null, e: Error): string {
    s = s;
    o = o;
    so = so;
    e = e;
    let local = "x";
    local = local;
    let err = new Error("m");
    err = err;
    return s + local + err.message;
}
