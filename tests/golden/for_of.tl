interface Item {
    id: int64;
}

interface Bag {
    items: Item[];
}

fn items(b: Bag): Item[] {
    return b.items;
}

fn sum(xs: int64[], b: Bag): int64 {
    let total: int64 = 0;
    for (const x of xs) {
        total += x;
    }
    for (const it of items(b)) {
        total += it.id;
    }
    for (let it of b.items) {
        total = total + it.id;
    }
    for (const unused of xs) {
        total++;
    }
    for (const row of [[1, 2], [3]]) {
        for (const v of row) {
            total += v;
        }
    }
    for (const x of xs) {
        if (x > 10) {
            xs.push(x - 1);
            break;
        }
    }
    return total;
}
