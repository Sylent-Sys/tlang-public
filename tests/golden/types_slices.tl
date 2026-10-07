interface Item {
    id: int64;
}

interface Bag {
    items: Item[];
    grid: int64[][];
    maybe: (Item | null)[];
    names: (string | null)[];
    ratios: (float64 | null)[];
    counts: (int32 | null)[];
    deep: Item[][];
    errs: Error[];
    flags: bool[];
}

fn main(): void {
}
