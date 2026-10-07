interface User {
    id: int64;
    name: string;
    tags: string[];
    nick?: string;
}

fn mk(n: int64): int64 {
    return n + 1;
}

let names: string[] = ["a", "b"];
let empty: int64[] = [];

fn main(): void {
    let xs = [1, 2, 3];
    let ys: int64[] = [mk(1), mk(2)];
    let zs = [[1, 2], [3]];
    let ss = ["x", "y"];
    let os: (int64 | null)[] = [1, null];
    let u: User = { id: 1, name: "a", tags: ["t"] };
    let v: User = { name: "b", id: mk(3), tags: [], nick: "n" };
    let us: User[] = [u, v];
    names.push("c");
    empty = [];
}
