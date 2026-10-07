fn greet(name: string): string {
    let s = "Hello, " + name + "!";
    let t = "tab\there \"quoted\" back\\slash";
    let u = "nul\0byte and é and ??= trigraph ??!";
    let folded = "a" + "b" + "c";
    let eq = s == t;
    let ne = s != u;
    if (eq || ne) {
        return folded;
    }
    return s + t + u;
}
