let counter: int64 = 0;

fn bump(): int64 {
    counter = counter + 1;
    return counter;
}

fn pair(a: int64, b: int64): int64 {
    return a * 10 + b;
}

fn order(): int64 {
    let x = pair(counter, bump());
    let y = counter + bump();
    let z = bump() + counter;
    return x + y + z;
}

fn main(): void {
    let r = order();
}
