// codegen-mandatory: 15.2.5 (tlj_parse_arr_<m> via an array field, tlj_write_arr_<m> top level)
interface Item {
    id: int64;
    name: string;
}

interface Batch {
    items: Item[];
    ids: int64[];
}

fn route_dispatcher(ctx: Context): void {
    let b = new Batch();
    if (!ctx.bindJson(b)) {
        ctx.text(400, "bad");
        return;
    }
    if (ctx.match("GET", "/items")) {
        ctx.json(200, b.items);
        return;
    }
    let ids: int64[] = [1, 2];
    ctx.json(200, ids);
}
