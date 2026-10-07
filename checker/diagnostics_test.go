package checker

import "testing"

// diagnostics_test.go asserts one representative case per diagnostic code the
// checker emits, so the whole matrix stays wired as the passes evolve.

func TestDiagnosticsMatrix(t *testing.T) {
	cases := []struct {
		name string
		code string
		src  string
	}{
		{
			name: "E-NAME undefined",
			code: "E-NAME",
			src:  `fn main(): void { let x: int64 = missing; }`,
		},
		{
			name: "E-TYPE mismatch",
			code: "E-TYPE",
			src:  `fn main(): void { let x: int64 = "s"; }`,
		},
		{
			name: "E-CONST overflow",
			code: "E-CONST",
			src:  `fn main(): void { let x: int32 = 99999999999999999999; }`,
		},
		{
			name: "E-INIT missing field",
			code: "E-INIT",
			src: `
interface Point { x: int64; y: int64; }
fn main(): void { const p: Point = { x: 1 }; }
`,
		},
		{
			name: "E-ESCAPE request into global",
			code: "E-ESCAPE",
			src: `
interface Box { n: int64; }
let g: Box = new Box();
`,
		},
		{
			name: "E-TX return leaving block",
			code: "E-TX",
			src:  `fn main(): void { db.transaction((tx) => { return; }); }`,
		},
		{
			name: "E-DECORATOR unknown",
			code: "E-DECORATOR",
			src: `
@Cache()
fn route_dispatcher(ctx: Context): void {}
`,
		},
		{
			name: "E-ROUTE non-literal",
			code: "E-ROUTE",
			src: `
fn route_dispatcher(ctx: Context): void {
  const m: string = "GET";
  if (ctx.match(m, "/x")) { }
}
`,
		},
		{
			name: "E-DB non-literal SQL",
			code: "E-DB",
			src: `
interface Row { id: int64; }
fn main(): void {
  const q: string = "SELECT id FROM t";
  const rows: Row[] = db.query<Row>(q);
}
`,
		},
		{
			name: "E-JSON not encodable",
			code: "E-JSON",
			src: `
interface Bad { e: Error; }
fn route_dispatcher(ctx: Context): void {
  const b: Bad = new Bad();
  ctx.json(200, b);
}
`,
		},
		{
			name: "E-ENTRY both",
			code: "E-ENTRY",
			src: `
fn main(): void {}
fn route_dispatcher(ctx: Context): void {}
`,
		},
		{
			name: "E-GENERIC recursive depth",
			code: "E-GENERIC",
			src: `
interface Tree<T> { kids: Tree<T[]>[]; }
fn main(): void { let t: Tree<int64> = new Tree<int64>(); }
`,
		},
		{
			name: "E-UNSUPPORTED generic object-type alias",
			code: "E-UNSUPPORTED",
			src:  `type Pair<T> = { a: T; b: T; };`,
		},
		{
			name: "W-OPTIONAL redundant optional",
			code: "W-OPTIONAL",
			src:  `fn main(): void { let x: (int64 | null) | null = null; }`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check(t, tc.src)
			if !hasCode(diags, tc.code) {
				t.Fatalf("want %s, got %s", tc.code, diags.Error())
			}
		})
	}
}
