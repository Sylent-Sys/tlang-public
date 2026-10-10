import { db } from "tlang/db";
import { console } from "tlang/system";


fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn txs(n: int64): void {
    db.transaction((tx) => {
        tx.execute("UPDATE a SET x = 1");
    });
    try {
        db.transaction((tx: Transaction) => {
            let rows = tx.execute("UPDATE a SET x = $1", n);
            if (rows == 0) {
                throw "nothing updated";
            }
        });
    } catch (e) {
        console.error(e.message, JsonValue.string(e.message));
    }
    let i: int64 = 0;
    while (i < n) {
        db.transaction((tx) => {
            tx.execute("INSERT INTO log VALUES ($1)", i);
            let k: int64 = 0;
            while (div(k, 1) < i) {
                k++;
                if (k == 3) {
                    break;
                }
            }
        });
        i++;
    }
    db.transaction((tx) => {
        let total = db.execute("SELECT 1");
    });
}

fn main(): void {
    txs(3);
}
