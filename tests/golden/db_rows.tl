import { db } from "tlang/db";


// codegen-mandatory: 15.2.3 (row descriptors __tl_td_<m> + __tl_fd_<m>)
interface User {
    id: int64;
    name: string;
    email: string | null;
    age: int32 | null;
    score: float64;
    ratio: float64 | null;
    active: bool;
    verified: bool | null;
    small: int32;
    big: int64 | null;
}

interface Row<T> {
    v: T;
}

interface Marker {}

interface Unused {
    id: int64;
}

fn neverCalled<T>(x: T): int64 {
    let rows = db.query<Unused>("SELECT id FROM unused");
    return rows.len;
}

fn route_dispatcher(ctx: Context): void {
    let marks = db.query<Marker>("SELECT 1");
    let id: int64 = 1;
    let name = "n";
    let email: string | null = null;
    let age: int32 | null = 30;
    let n = db.execute("UPDATE users SET name = $1 WHERE id = $2", name, id);
    let n0 = db.execute("DELETE FROM users");
    let all = db.query<User>("SELECT * FROM users");
    let one = db.queryOne<User>("SELECT * FROM users WHERE id = $1", id);
    let opt = db.execute("UPDATE users SET email = $1, age = $2 WHERE id = $3", email, age, ctx.paramInt("id"));
    let typed = db.query<Row<int64>>("SELECT 1 AS " + "v");
    db.transaction((tx) => {
        let k = tx.execute("UPDATE users SET active = $1, score = $2", true, 2.5);
        let us = tx.query<User>("SELECT * FROM users WHERE small = $1", int32(3));
        let u1 = tx.queryOne<User>("SELECT * FROM users LIMIT 1");
    });
    if (one != null) {
        ctx.text(200, one.name);
        return;
    }
    ctx.text(200, n.toString());
}
