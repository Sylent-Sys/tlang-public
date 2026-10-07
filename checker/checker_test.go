package checker

import (
	"testing"

	"tlang/types"
)

// checker_test.go is the whole-program end-to-end test: a spec §13-style
// server program (interfaces, an @Use guard, a transaction, ctx.bindJson /
// ctx.json, routing and a route_dispatcher entry) that must check with no
// errors and populate the Info output lists.

const e2eProgram = `
interface User { id: int64; name: string; }
interface CreateUser { name: string; }

fn authed(ctx: Context): bool {
  return ctx.header("authorization").len > 0;
}

fn createUser(ctx: Context): void {
  const body: CreateUser = new CreateUser();
  if (!ctx.bindJson(body)) {
    ctx.text(400, "bad request");
    return;
  }
  db.transaction((tx) => {
    tx.execute("INSERT INTO users (name) VALUES ($1)", body.name);
  });
  const u: User = new User();
  u.id = 1;
  u.name = body.name;
  ctx.json(201, u);
}

fn listUsers(ctx: Context): void {
  const users: User[] = db.query<User>("SELECT id, name FROM users");
  ctx.json(200, users);
}

@Use(authed)
fn route_dispatcher(ctx: Context): void {
  if (ctx.match("POST", "/users")) {
    createUser(ctx);
    return;
  }
  if (ctx.match("GET", "/users")) {
    listUsers(ctx);
    return;
  }
  ctx.text(404, "not found");
}
`

func TestEndToEndProgram(t *testing.T) {
	_, info, diags := checkProg(t, e2eProgram)
	if diags.HasErrors() {
		t.Fatalf("end-to-end program should check cleanly, got:\n%s", diags.Error())
	}

	if info.Kind != types.ProgramServer {
		t.Fatalf("Kind = %v, want ProgramServer", info.Kind)
	}
	if info.Entry == nil || info.Entry.Name != "route_dispatcher" {
		t.Fatalf("Entry = %v, want route_dispatcher", info.Entry)
	}
	if len(info.Entry.Guards) != 1 || info.Entry.Guards[0].Name != "authed" {
		t.Fatalf("route_dispatcher should have the authed guard, got %+v", info.Entry.Guards)
	}
	if !info.UsesDB {
		t.Fatal("UsesDB should be set (transaction and db.query)")
	}

	// Interfaces: User and CreateUser are declared (plain, non-generic).
	wantIface := map[string]bool{"User": false, "CreateUser": false}
	for _, n := range info.Interfaces {
		if _, ok := wantIface[n.Name()]; ok {
			wantIface[n.Name()] = true
		}
	}
	for name, seen := range wantIface {
		if !seen {
			t.Fatalf("interface %s missing from Info.Interfaces", name)
		}
	}

	// Funcs: the four non-generic functions and the dispatcher.
	wantFuncs := map[string]bool{"authed": false, "createUser": false, "listUsers": false, "route_dispatcher": false}
	for _, f := range info.Funcs {
		if _, ok := wantFuncs[f.Name]; ok {
			wantFuncs[f.Name] = true
		}
	}
	for name, seen := range wantFuncs {
		if !seen {
			t.Fatalf("function %s missing from Info.Funcs", name)
		}
	}

	// Routes: two ctx.match calls, numbered 1 and 2 in source order.
	if len(info.Routes) != 2 {
		t.Fatalf("want 2 routes, got %d", len(info.Routes))
	}
	if info.Routes[0].Method != "POST" || info.Routes[1].Method != "GET" {
		t.Fatalf("routes out of order: %s then %s", info.Routes[0].Method, info.Routes[1].Method)
	}

	// DBTypes: User used as a query row type.
	if len(info.DBTypes) != 1 || info.DBTypes[0].Name() != "User" {
		t.Fatalf("want DBTypes = [User], got %v", info.DBTypes)
	}

	// JSONTypes: CreateUser (parse via bindJson) and User (write via json).
	wantJSON := map[string]bool{"CreateUser": false, "User": false}
	for _, j := range info.JSONTypes {
		if n, ok := j.Type.(*types.Named); ok {
			if _, want := wantJSON[n.Name()]; want {
				wantJSON[n.Name()] = true
			}
		}
	}
	for name, seen := range wantJSON {
		if !seen {
			t.Fatalf("JSON type %s missing from Info.JSONTypes", name)
		}
	}
}
