import { db } from "tlang/db";


fn main(): void {
    db.transaction(5);
}
