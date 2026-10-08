package checker

import (
	"testing"

	"tlang/ast"
	"tlang/types"
)

// selByString returns the Selection of the first member expression printing
// as want.
func selByString(prog *ast.Program, info *types.Info, want string) *types.Selection {
	var out *types.Selection
	ast.Inspect(prog, func(n ast.Node) bool {
		m, ok := n.(*ast.MemberExpression)
		if !ok || out != nil {
			return true
		}
		if m.String() == want {
			out = info.Selections[m]
		}
		return true
	})
	return out
}

func TestSelectUserField(t *testing.T) {
	src := `
interface User { id: int64; }
fn f(u: User): int64 { return u.id; }
`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	sel := selByString(prog, info, "u.id")
	if sel == nil || sel.Kind != types.SelField {
		t.Fatalf("u.id selection = %+v, want SelField", sel)
	}
	if !types.IsBasic(sel.Type, types.Int64) {
		t.Fatalf("u.id type = %s, want int64", sel.Type)
	}
}

func TestSelectUserMethod(t *testing.T) {
	src := `
interface User { id: int64; }
fn (u: User) bump(): int64 { return u.id + 1; }
fn f(u: User): int64 { return u.bump(); }
`
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	sel := selByString(prog, info, "u.bump")
	if sel == nil || sel.Kind != types.SelMethod {
		t.Fatalf("u.bump selection = %+v, want SelMethod", sel)
	}
}

func TestSelectBuiltinMember(t *testing.T) {
	src := wrap(`let s = "hi"; let n = s.len;`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	sel := selByString(prog, info, "s.len")
	if sel == nil || sel.Kind != types.SelBuiltin {
		t.Fatalf("s.len selection = %+v, want SelBuiltin", sel)
	}
	if !types.IsBasic(sel.Type, types.Int64) {
		t.Fatalf("s.len type = %s, want int64", sel.Type)
	}
}

func TestSelectBuiltinOnDefaultedConstant(t *testing.T) {
	// 5.toString(): MemberOf defaults the receiver.
	src := wrap(`let s = (5).toString();`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestSelectNamespace(t *testing.T) {
	src := wrap(`console.info("hi");`)
	prog, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	sel := selByString(prog, info, "console.info")
	if sel == nil || sel.Kind != types.SelBuiltin || sel.Builtin != types.BuiltinConsoleInfo {
		t.Fatalf("console.info selection = %+v, want SelBuiltin info", sel)
	}
}

func TestOptionalReceiverError(t *testing.T) {
	src := wrap(`let o: int64 | null = 5; let n = o.toString();`)
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for a member on an optional, got %s", diags.Error())
	}
	if !hasMessage(diags, "narrow first") {
		t.Fatalf("want a narrow-first message, got %s", diags.Error())
	}
}

func TestConsoleVariadic(t *testing.T) {
	src := wrap(`console.info("a", 1, true, 2.5);`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestConsoleRejectsInterface(t *testing.T) {
	src := `
interface User { id: int64; }
fn f(u: User): void { console.info(u); }
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-TYPE") {
		t.Fatalf("want E-TYPE for console.log of an interface, got %s", diags.Error())
	}
}

func TestPush(t *testing.T) {
	src := wrap(`let xs: int64[] = [1]; xs.push(2);`)
	_, diags := check(t, src)
	wantCodes(t, diags)
}

func TestPushWrongElement(t *testing.T) {
	src := wrap(`let xs: int64[] = [1]; xs.push("x");`)
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}

func TestRouteMatch(t *testing.T) {
	src := `
fn route_dispatcher(ctx: Context): void {
	if (ctx.match("GET", "/users/:id")) {
	}
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if len(info.Routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(info.Routes))
	}
	r := info.Routes[0]
	if r.ID != 1 || r.Method != "GET" || r.Pattern != "/users/:id" {
		t.Fatalf("route = %+v", r)
	}
	if len(r.Params()) != 1 || r.Params()[0] != "id" {
		t.Fatalf("route params = %v", r.Params())
	}
}

func TestRouteNonLiteral(t *testing.T) {
	src := `
fn route_dispatcher(ctx: Context): void {
	let m = "GET";
	if (ctx.match(m, "/x")) { }
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-ROUTE")
}

func TestRouteBadPattern(t *testing.T) {
	src := `
fn route_dispatcher(ctx: Context): void {
	if (ctx.match("GET", "no-slash")) { }
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-ROUTE")
}

func TestRouteTooManyParams(t *testing.T) {
	src := `
fn route_dispatcher(ctx: Context): void {
	if (ctx.match("GET", "/:a/:b/:c/:d/:e/:f/:g/:h/:i")) { }
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-ROUTE")
}

func TestBindJSON(t *testing.T) {
	src := `
interface In { name: string; }
fn route_dispatcher(ctx: Context): void {
	let in = new In();
	if (ctx.bindJson(in)) { }
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	found := false
	for _, j := range info.JSONTypes {
		if j.Parse {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a JSON parse demand")
	}
}

func TestBindJSONNotEncodable(t *testing.T) {
	src := `
interface Bad { e: Error; }
fn route_dispatcher(ctx: Context): void {
	let b = new Bad();
	if (ctx.bindJson(b)) { }
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-JSON")
}

func TestJSON(t *testing.T) {
	src := `
interface Out { ok: bool; }
fn route_dispatcher(ctx: Context): void {
	let o = new Out();
	ctx.json(200, o);
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	found := false
	for _, j := range info.JSONTypes {
		if j.Write {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a JSON write demand")
	}
}

func TestDBQuery(t *testing.T) {
	src := `
interface Row { id: int64; name: string; }
fn main(): void {
	let rows = db.query<Row>("select id, name from users");
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if !info.UsesDB {
		t.Fatal("UsesDB should be set")
	}
	if len(info.DBTypes) != 1 {
		t.Fatalf("got %d DB types, want 1", len(info.DBTypes))
	}
	v := localByName(t, info, "rows")
	arr, ok := v.Type.(*types.Array)
	if !ok {
		t.Fatalf("rows = %s, want Row[]", v.Type)
	}
	if _, ok := arr.Elem.(*types.Named); !ok {
		t.Fatalf("rows element = %s, want Row", arr.Elem)
	}
}

func TestDBQueryOne(t *testing.T) {
	src := `
interface Row { id: int64; }
fn main(): void {
	let row = db.queryOne<Row>("select id from users where id = $1", 1);
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "row")
	if !types.IsOptional(v.Type) {
		t.Fatalf("row = %s, want Row | null", v.Type)
	}
}

func TestDBExecute(t *testing.T) {
	src := `fn main(): void { let n = db.execute("delete from users"); }`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "n")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("n = %s, want int64", v.Type)
	}
}

func TestDBNonLiteralSQL(t *testing.T) {
	src := `fn main(): void { let s = "x"; let n = db.execute(s); }`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
}

func TestDBBadRowType(t *testing.T) {
	src := `
interface Row { e: Error; }
fn main(): void {
	let rows = db.query<Row>("select 1");
}
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-DB") {
		t.Fatalf("want E-DB for a non-scalar row field, got %s", diags.Error())
	}
}

func TestDBNonScalarArg(t *testing.T) {
	src := `
interface User { id: int64; }
fn f(u: User): void {
	let n = db.execute("update x", u);
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-DB")
}

func TestGenericCallInference(t *testing.T) {
	src := `
fn id<T>(x: T): T { return x; }
fn main(): void {
	let n = id(5);
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "n")
	if !types.IsBasic(v.Type, types.Int64) {
		t.Fatalf("n = %s, want int64", v.Type)
	}
	// A function instance was seeded.
	if len(info.Instances.FuncInstances()) != 1 {
		t.Fatalf("got %d func instances, want 1", len(info.Instances.FuncInstances()))
	}
}

func TestGenericCallExplicitTypeArg(t *testing.T) {
	src := `
fn id<T>(x: T): T { return x; }
fn main(): void {
	let n = id<int32>(5);
}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	v := localByName(t, info, "n")
	if !types.IsBasic(v.Type, types.Int32) {
		t.Fatalf("n = %s, want int32", v.Type)
	}
}

func TestGenericCallArgMismatch(t *testing.T) {
	src := `
fn pair<T>(a: T, b: T): T { return a; }
fn main(): void {
	let x = pair(1, "s");
}
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-GENERIC") && !hasCode(diags, "E-TYPE") {
		t.Fatalf("want an error for a conflicting generic argument, got %s", diags.Error())
	}
}

func TestCallArityError(t *testing.T) {
	src := `
fn add(a: int64, b: int64): int64 { return a + b; }
fn main(): void { let x = add(1); }
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-TYPE")
}
