fn compute(n: int64): int64 {
    return n * 2;
}

fn risky(n: int64): int64 {
    return 100 / n;
}

let base: int64 = 10;
let derived: int64 = base * 3 + 1;
let fromCall: int64 = compute(base);
let guarded: int64 = risky(derived);
let flag: bool = derived > base && fromCall != 0;
let greeting: string = "hi there";
let maybe: int64 | null = 5;
let none: string | null = null;
let copy: string = greeting;

fn main(): void {
    base = base + derived;
}
