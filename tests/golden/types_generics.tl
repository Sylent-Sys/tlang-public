interface Item {
    id: int64;
}

interface Pair<A, B> {
    first: A;
    second: B;
}

interface Box<T> {
    value: T;
    items: T[];
}

interface Holder {
    b: Box<int64>;
    p: Pair<string, Box<Item>>;
}

let cache: Box<string> | null;

fn take(p: Pair<int32, bool>, q: Box<Item | null>): void {
}

fn main(): void {
}
