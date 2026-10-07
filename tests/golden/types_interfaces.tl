interface Empty {}

interface Address {
    street: string;
    zip: int32;
}

type Point = { x: float64; y: float64; };

interface User {
    id: int64;
    name: string;
    email?: string;
    score: float64 | null;
    active: bool;
    age: int32 | null;
    flag: bool | null;
    big: int64 | null;
    addr: Address;
    prev: Address | null;
    tags: string[];
    nums: int64[] | null;
    friends: User[];
    last: Error;
    where: Point;
    nothing: Empty;
}

fn main(): void {
}
