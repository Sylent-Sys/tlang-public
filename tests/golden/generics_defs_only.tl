interface Item {
    id: int64;
}

interface Page<T> {
    items: T[];
    total: int64;
}

fn mk<T>(x: T): void {
    let p: Page<T> | null;
}

fn useMk(it: Item): void {
    mk<Item>(it);
}

fn main(): void {
}
