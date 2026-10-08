import { db } from "tlang/db";


fn main(): void {
    for (let i = 0; i < 3; i++) {
        db.transaction((tx) => {
            continue;
        });
    }
}
