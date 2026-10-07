interface Address {
    city: string;
    zip: int32 | null;
}

interface Tag {
    name: string;
}

interface Profile {
    id: int64;
    name: string;
    score: float64;
    active: bool;
    nick: string | null;
    age: int32 | null;
    big: int64 | null;
    ratio: float64 | null;
    flag: bool | null;
    home: Address;
    work: Address | null;
    tags: Tag[];
    labels: string[] | null;
    matrix: int64[][];
    maybeTags: (Tag | null)[];
    notes: (string | null)[];
    small: int32;
}

fn route_dispatcher(ctx: Context): void {
    let p = new Profile();
    if (!ctx.bindJson(p)) {
        ctx.text(400, "bad");
        return;
    }
    ctx.json(200, p);
}
