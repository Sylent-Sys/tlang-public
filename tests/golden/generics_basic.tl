fn id<T>(x: T): T {
    return x;
}

fn twice<T>(x: T): T {
    let y = id<T>(x);
    return id(y);
}

fn swap<A, B>(a: A, b: B): B {
    return b;
}

fn main(): void {
    let a = twice<int64>(1);
    let b = twice<string>("s");
    let c = id(2.5);
    let d = swap<int32, bool>(int32(1), true);
}
