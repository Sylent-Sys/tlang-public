package ast

import (
	"fmt"
	"strings"
	"testing"

	"tlang/token"
)

// p returns a position on line 1 at the given 1-based column.
func p(col int) token.Position { return token.Position{Line: 1, Column: col, Offset: col - 1} }

func id(name string) *Identifier { return &Identifier{Name: name} }

func nt(name string, args ...TypeExpr) *NamedType { return &NamedType{Name: id(name), TypeArgs: args} }

func intLit(raw string, v uint64) *IntegerLiteral { return &IntegerLiteral{Raw: raw, Value: v} }

func str(s string) *StringLiteral { return &StringLiteral{Value: s} }

func infix(l Expression, op token.TokenType, r Expression) *InfixExpression {
	return &InfixExpression{Left: l, Operator: op, Right: r}
}

func call(fn Expression, args ...Expression) *CallExpression {
	return &CallExpression{Function: fn, Arguments: args}
}

func member(obj Expression, prop string) *MemberExpression {
	return &MemberExpression{Object: obj, Property: id(prop)}
}

func block(stmts ...Statement) *BlockStatement { return &BlockStatement{Statements: stmts} }

func exprStmt(e Expression) *ExpressionStatement { return &ExpressionStatement{Expression: e} }

func TestStrings(t *testing.T) {
	cases := []struct {
		node Node
		want string
	}{
		{
			&LetStatement{Name: id("x"), Type: &OptionalType{Elem: nt("int64")},
				Value: infix(infix(id("a"), token.PLUS, id("b")), token.ASTERISK,
					&PrefixExpression{Operator: token.MINUS, Right: id("c")})},
			"let x: int64 | null = ((a + b) * (-c));",
		},
		{&LetStatement{IsConst: true, Name: id("n"), Value: intLit("1_000", 1000)}, "const n = 1_000;"},
		{&LetStatement{Name: id("s"), Type: nt("string")}, "let s: string;"},
		{
			&FunctionStatement{
				Decorators: []*Decorator{{Name: id("Use"), Args: []*Identifier{id("logger"), id("authGuard")}}},
				Receiver:   &Parameter{Name: id("ctx"), Type: nt("Context")},
				Name:       id("handle"),
				ReturnType: nt("void"),
				Body:       block(&ReturnStatement{}),
			},
			"@Use(logger, authGuard) fn (ctx: Context) handle(): void { return; }",
		},
		{
			&FunctionStatement{
				Name:       id("first"),
				TypeParams: []*Identifier{id("T")},
				Parameters: []*Parameter{{Name: id("xs"), Type: &ArrayType{Elem: nt("T")}}},
				ReturnType: &OptionalType{Elem: nt("T")},
				Body:       block(&ReturnStatement{Value: &NullLiteral{}}),
			},
			"fn first<T>(xs: T[]): T | null { return null; }",
		},
		{&FunctionStatement{Name: id("main"), Body: block()}, "fn main() {}"},
		{
			&ForStatement{
				Init:      &LetStatement{Name: id("i"), Value: intLit("0", 0)},
				Condition: infix(id("i"), token.LT, id("n")),
				Post:      &AssignmentExpression{Target: id("i"), Operator: token.INCREMENT},
				Body:      block(exprStmt(call(member(id("xs"), "push"), id("i")))),
			},
			"for (let i = 0; (i < n); i++) { xs.push(i); }",
		},
		{&ForStatement{Body: block()}, "for (;;) {}"},
		{
			&ForOfStatement{IsConst: true, Var: id("u"), Iterable: id("users"),
				Body: block(exprStmt(call(member(id("console"), "log"), member(id("u"), "name"))))},
			"for (const u of users) { console.log(u.name); }",
		},
		{
			&TransactionStatement{Receiver: id("db"), Method: id("transaction"), Param: id("tx"),
				Body: block(exprStmt(call(member(id("tx"), "execute"), str("UPDATE a SET b = $1"), intLit("1", 1))))},
			`db.transaction((tx) => { tx.execute("UPDATE a SET b = $1", 1); });`,
		},
		{
			&TransactionStatement{Receiver: id("db"), Param: id("tx"), ParamType: nt("Transaction"), Body: block()},
			`db.transaction((tx: Transaction) => {});`,
		},
		{&ArrayType{Elem: &ParenType{Inner: &OptionalType{Elem: nt("string")}}}, "(string | null)[]"},
		{&OptionalType{Elem: &ArrayType{Elem: nt("Page", nt("User"))}}, "Page<User>[] | null"},
		{nt("Map", nt("K"), &ArrayType{Elem: nt("V")}), "Map<K, V[]>"},
		{
			member(&NonNullExpression{Left: &CallExpression{Function: id("first"), TypeArgs: []TypeExpr{nt("User")},
				Arguments: []Expression{id("xs")}}}, "name"),
			"first<User>(xs)!.name",
		},
		{&IndexExpression{Left: id("xs"), Index: infix(id("i"), token.PLUS, intLit("1", 1))}, "xs[(i + 1)]"},
		{&TernaryExpression{Condition: id("ok"), Consequence: str("y"), Alternative: infix(id("a"), token.NULLISH, str("n"))},
			`(ok ? "y" : (a ?? "n"))`},
		{&PrefixExpression{Operator: token.BANG, Right: &NonNullExpression{Left: id("a")}}, "(!a!)"},
		{&NewExpression{Global: true, Type: nt("User"), IsArray: true}, "new global User[]()"},
		{&NewExpression{Type: &ArrayType{Elem: nt("int64")}, IsArray: true}, "new int64[][]()"},
		{&NewExpression{Type: nt("Error"), Args: []Expression{str("m"), intLit("404", 404)}}, `new Error("m", 404)`},
		{&NewExpression{Type: nt("Page", nt("User"))}, "new Page<User>()"},
		{
			&InterfaceStatement{Name: id("Page"), TypeParams: []*Identifier{id("T")}, Fields: []*FieldDefinition{
				{Name: id("items"), Type: &ArrayType{Elem: nt("T")}},
				{Name: id("total"), Type: nt("int64")},
				{Name: id("next"), Optional: true, Type: nt("Page", nt("T"))},
			}},
			"interface Page<T> { items: T[]; total: int64; next?: Page<T> }",
		},
		{&InterfaceStatement{Name: id("Empty")}, "interface Empty {}"},
		{
			&TypeAliasStatement{Name: id("P"), Value: &ObjectType{Fields: []*FieldDefinition{
				{Name: id("a"), Type: nt("int32")}, {Name: id("b"), Optional: true, Type: nt("string")}}}},
			"type P = { a: int32; b?: string };",
		},
		{&TypeAliasStatement{Name: id("Ids"), Value: &ArrayType{Elem: nt("int64")}}, "type Ids = int64[];"},
		{
			&TryCatchStatement{Body: block(&ThrowStatement{Value: str("x")}), CatchParam: id("e"), CatchBody: block()},
			`try { throw "x"; } catch (e) {}`,
		},
		{&TryCatchStatement{Body: block(), CatchBody: block(&BreakStatement{}, &ContinueStatement{})},
			"try {} catch { break; continue; }"},
		{
			&ObjectLiteral{Entries: []*ObjectEntry{{Key: id("id"), Value: intLit("1", 1)}, {Key: id("name"), Value: str("a\n\"b\"")}}},
			`{ id: 1, name: "a\n\"b\"" }`,
		},
		{&ObjectLiteral{}, "{}"},
		{&ArrayLiteral{Elements: []Expression{intLit("1", 1), &FloatLiteral{Raw: "2.5e-3"}}}, "[1, 2.5e-3]"},
		{
			&IfStatement{Condition: infix(id("a"), token.EQ, id("b")), Consequence: block(&ReturnStatement{Value: &BooleanLiteral{Value: true}}),
				Alternative: &IfStatement{Condition: id("c"), Consequence: &ReturnStatement{Value: &BooleanLiteral{}}, Alternative: block()}},
			"if ((a == b)) { return true; } else if (c) return false; else {}",
		},
		{&WhileStatement{Condition: &BooleanLiteral{Value: true}, Body: block(&BreakStatement{})}, "while (true) { break; }"},
		{exprStmt(&AssignmentExpression{Target: member(id("u"), "id"), Operator: token.ASSIGN, Value: intLit("10", 10)}), "u.id = 10;"},
		{exprStmt(&AssignmentExpression{Target: &IndexExpression{Left: id("xs"), Index: intLit("0", 0)}, Operator: token.PLUS_ASSIGN,
			Value: infix(id("a"), token.MOD, id("b"))}), "xs[0] += (a % b);"},
		{exprStmt(&AssignmentExpression{Target: id("n"), Operator: token.DECREMENT}), "n--;"},
		{&BadStatement{}, "<bad statement>"},
		{&BadExpression{}, "<bad expression>"},
		{&BadType{}, "<bad type>"},
		{exprStmt(nil), "<nil>;"},
	}
	for i, c := range cases {
		if got := c.node.String(); got != c.want {
			t.Errorf("case %d (%T):\n got %s\nwant %s", i, c.node, got, c.want)
		}
	}
}

func TestProgramString(t *testing.T) {
	prog := &Program{Statements: []Statement{
		&InterfaceStatement{Name: id("A"), Fields: []*FieldDefinition{{Name: id("x"), Type: nt("int32")}}},
		&LetStatement{Name: id("g"), Value: intLit("1", 1)},
	}}
	want := "interface A { x: int32 }\nlet g = 1;\n"
	if got := prog.String(); got != want {
		t.Errorf("Program.String() = %q, want %q", got, want)
	}
}

func TestPositions(t *testing.T) {
	// let x = a + "é";
	// columns count bytes: é is two bytes, so the closing quote is at 16 and ";" at 17.
	a := &Identifier{NamePos: p(9), Name: "a"}
	s := &StringLiteral{ValuePos: p(13), Value: "é", ValueEnd: p(17)}
	sum := &InfixExpression{Left: a, OpPos: p(11), Operator: token.PLUS, Right: s}
	let := &LetStatement{LetPos: p(1), Name: &Identifier{NamePos: p(5), Name: "x"}, Value: sum, Semicolon: p(17)}
	checks := []struct {
		n          Node
		pos, end   int
		nameForErr string
	}{
		{a, 9, 10, "ident"},
		{s, 13, 17, "string"},
		{sum, 9, 17, "infix"},
		{let, 1, 18, "let"},
	}
	for _, c := range checks {
		if c.n.Pos() != p(c.pos) || c.n.End() != p(c.end) {
			t.Errorf("%s: Pos=%v End=%v, want %v %v", c.nameForErr, c.n.Pos(), c.n.End(), p(c.pos), p(c.end))
		}
	}

	// f<T>(x)!  and  new global User[]()
	f := &Identifier{NamePos: p(1), Name: "f"}
	ce := &CallExpression{Function: f, Langle: p(2), TypeArgs: []TypeExpr{&NamedType{Name: &Identifier{NamePos: p(3), Name: "T"}}},
		Rangle: p(4), Lparen: p(5), Arguments: []Expression{&Identifier{NamePos: p(6), Name: "x"}}, Rparen: p(7)}
	nn := &NonNullExpression{Left: ce, BangPos: p(8)}
	if ce.End() != p(8) || nn.Pos() != p(1) || nn.End() != p(9) {
		t.Errorf("call/nonnull positions: %v %v %v", ce.End(), nn.Pos(), nn.End())
	}
	ne := &NewExpression{NewPos: p(1), Global: true, GlobalPos: p(5), Type: &NamedType{Name: &Identifier{NamePos: p(12), Name: "User"}},
		IsArray: true, Lparen: p(18), Rparen: p(19)}
	if ne.Pos() != p(1) || ne.End() != p(20) {
		t.Errorf("new positions: %v %v", ne.Pos(), ne.End())
	}

	// Page<User>[] | null
	opt := &OptionalType{
		Elem: &ArrayType{Elem: &NamedType{Name: &Identifier{NamePos: p(1), Name: "Page"}, Langle: p(5),
			TypeArgs: []TypeExpr{&NamedType{Name: &Identifier{NamePos: p(6), Name: "User"}}}, Rangle: p(10)},
			Lbracket: p(11), Rbracket: p(12)},
		PipePos: p(14), NullPos: p(16)}
	if opt.Pos() != p(1) || opt.End() != p(20) || opt.Elem.End() != p(13) {
		t.Errorf("type positions: %v %v %v", opt.Pos(), opt.End(), opt.Elem.End())
	}

	// i++;
	inc := &ExpressionStatement{Expression: &AssignmentExpression{Target: &Identifier{NamePos: p(1), Name: "i"},
		OpPos: p(2), Operator: token.INCREMENT}, Semicolon: p(4)}
	if inc.Expression.End() != p(4) || inc.End() != p(5) {
		t.Errorf("inc positions: %v %v", inc.Expression.End(), inc.End())
	}

	// Decorated function starts at '@'.
	fn := &FunctionStatement{Decorators: []*Decorator{{AtPos: p(1), Name: id("Use"), Rparen: p(10)}}, FnPos: token.Position{Line: 2, Column: 1},
		Name: id("h"), Body: &BlockStatement{Lbrace: token.Position{Line: 2, Column: 8}, Rbrace: token.Position{Line: 3, Column: 1}}}
	if fn.Pos() != p(1) || fn.End().String() != "3:2" {
		t.Errorf("function positions: %v %v", fn.Pos(), fn.End())
	}

	prog := &Program{EOF: token.Position{Line: 4, Column: 1, Offset: 30}}
	if prog.Pos() != (token.Position{Line: 1, Column: 1}) || prog.End() != prog.EOF {
		t.Errorf("program positions: %v %v", prog.Pos(), prog.End())
	}
}

func TestBinaryOp(t *testing.T) {
	cases := map[token.TokenType]token.TokenType{
		token.ASSIGN: "", token.PLUS_ASSIGN: token.PLUS, token.MINUS_ASSIGN: token.MINUS,
		token.ASTERISK_ASSIGN: token.ASTERISK, token.SLASH_ASSIGN: token.SLASH, token.MOD_ASSIGN: token.MOD,
		token.INCREMENT: token.PLUS, token.DECREMENT: token.MINUS,
	}
	for op, want := range cases {
		a := &AssignmentExpression{Operator: op}
		if got := a.BinaryOp(); got != want {
			t.Errorf("BinaryOp(%s) = %q, want %q", op, got, want)
		}
		if a.IsIncDec() != (op == token.INCREMENT || op == token.DECREMENT) {
			t.Errorf("IsIncDec(%s) wrong", op)
		}
	}
}

func TestWalkOrder(t *testing.T) {
	// @Use(g) fn (c: Context) h<T>(x: T[]): int64 { let y: int64 = x.len; if (y > 0) { return y; } return f(x)!; }
	fn := &FunctionStatement{
		Decorators: []*Decorator{{Name: id("Use"), Args: []*Identifier{id("g")}}},
		Receiver:   &Parameter{Name: id("c"), Type: nt("Context")},
		Name:       id("h"),
		TypeParams: []*Identifier{id("T")},
		Parameters: []*Parameter{{Name: id("x"), Type: &ArrayType{Elem: nt("T")}}},
		ReturnType: nt("int64"),
		Body: block(
			&LetStatement{Name: id("y"), Type: nt("int64"), Value: member(id("x"), "len")},
			&IfStatement{Condition: infix(id("y"), token.GT, intLit("0", 0)), Consequence: block(&ReturnStatement{Value: id("y")})},
			&ReturnStatement{Value: &NonNullExpression{Left: call(id("f"), id("x"))}},
		),
	}
	var got []string
	depth := 0
	Inspect(fn, func(n Node) bool {
		if n == nil {
			depth--
			return false
		}
		label := fmt.Sprintf("%T", n)[len("*ast."):]
		if i, ok := n.(*Identifier); ok {
			label = "Ident:" + i.Name
		}
		got = append(got, strings.Repeat(".", depth)+label)
		depth++
		return true
	})
	want := []string{
		"FunctionStatement",
		".Decorator", "..Ident:Use", "..Ident:g",
		".Parameter", "..Ident:c", "..NamedType", "...Ident:Context",
		".Ident:h",
		".Ident:T",
		".Parameter", "..Ident:x", "..ArrayType", "...NamedType", "....Ident:T",
		".NamedType", "..Ident:int64",
		".BlockStatement",
		"..LetStatement", "...Ident:y", "...NamedType", "....Ident:int64", "...MemberExpression", "....Ident:x", "....Ident:len",
		"..IfStatement", "...InfixExpression", "....Ident:y", "....IntegerLiteral",
		"...BlockStatement", "....ReturnStatement", ".....Ident:y",
		"..ReturnStatement", "...NonNullExpression", "....CallExpression", ".....Ident:f", ".....Ident:x",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("walk order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if depth != 0 {
		t.Errorf("unbalanced Visit(nil) calls: depth %d", depth)
	}

	// Pruning: returning false skips children and the closing nil call.
	count := 0
	Inspect(fn, func(n Node) bool {
		if n != nil {
			count++
		}
		_, isBlock := n.(*BlockStatement)
		return !isBlock
	})
	if count != 18 {
		t.Errorf("pruned walk visited %d nodes, want 18", count)
	}
}

func TestWalkCoversAllChildren(t *testing.T) {
	// Every node kind with children, reached through one tree; count identifiers.
	tree := &Program{Statements: []Statement{
		&InterfaceStatement{Name: id("I1"), TypeParams: []*Identifier{id("I2")}, Fields: []*FieldDefinition{{Name: id("I3"), Type: nt("I4")}}},
		&TypeAliasStatement{Name: id("I5"), Value: &ObjectType{Fields: []*FieldDefinition{{Name: id("I6"), Type: &ParenType{Inner: &OptionalType{Elem: nt("I7")}}}}}},
		&FunctionStatement{Name: id("I8"), Body: block(
			&WhileStatement{Condition: id("I9"), Body: &ForOfStatement{Var: id("I10"), Iterable: id("I11"), Body: block(&BreakStatement{}, &ContinueStatement{})}},
			&ForStatement{Init: exprStmt(id("I12")), Condition: id("I13"), Post: id("I14"), Body: block()},
			&ThrowStatement{Value: id("I15")},
			&TryCatchStatement{Body: block(), CatchParam: id("I16"), CatchBody: block()},
			&TransactionStatement{Receiver: id("I17"), Method: id("I18"), Param: id("I19"), ParamType: nt("I20"), Body: block()},
			exprStmt(&TernaryExpression{Condition: id("I21"), Consequence: &IndexExpression{Left: id("I22"), Index: id("I23")},
				Alternative: &ArrayLiteral{Elements: []Expression{&ObjectLiteral{Entries: []*ObjectEntry{{Key: id("I24"), Value: id("I25")}}}}}}),
			exprStmt(&NewExpression{Type: nt("I26"), Args: []Expression{&PrefixExpression{Operator: token.MINUS, Right: id("I27")}}}),
			exprStmt(&AssignmentExpression{Target: id("I28"), Operator: token.ASSIGN, Value: &CallExpression{Function: id("I29"), TypeArgs: []TypeExpr{nt("I30")}}}),
		)},
	}}
	seen := map[string]bool{}
	Inspect(tree, func(n Node) bool {
		if i, ok := n.(*Identifier); ok {
			seen[i.Name] = true
		}
		return true
	})
	for i := 1; i <= 30; i++ {
		if name := fmt.Sprintf("I%d", i); !seen[name] {
			t.Errorf("Walk did not reach %s", name)
		}
	}
}
