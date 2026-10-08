import { db } from "tlang/db";


fn main(): void {
    let i: int64 = 0;
    while (i < 3) {
        db.transaction((tx) => {
            break;
        });
        i++;
    }
}
