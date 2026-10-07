interface CreateUserReq {
    id: int64;
    name: string;
}

interface UserResponse {
    id: int64;
    name: string;
    status: string;
}

fn logger(ctx: Context): bool {
    return true;
}

fn authGuard(ctx: Context): bool {
    let auth = ctx.header("Authorization");
    if (auth.len == 0) {
        ctx.text(401, "Unauthorized");
        return false;
    }
    return true;
}

@Use(logger, authGuard)
fn (ctx: Context) handleCreateUser(): void {
    let req = new CreateUserReq();
    if (!ctx.bindJson(req)) {
        ctx.text(400, "Invalid JSON Payload");
        return;
    }

    try {
        db.transaction((tx) => {
            tx.execute("INSERT INTO users(id, name) VALUES ($1, $2)", req.id, req.name);
        });

        let res = new UserResponse();
        res.id = req.id;
        res.name = req.name;
        res.status = "SUCCESS";

        ctx.json(201, res);
    } catch (err) {
        ctx.text(500, "Database Transaction Failed");
    }
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.method.eq("POST") && ctx.path.eq("/api/users")) {
        ctx.handleCreateUser();
        return;
    }
    ctx.text(404, "Endpoint Not Found");
}
