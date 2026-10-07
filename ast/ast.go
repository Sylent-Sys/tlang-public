// Package ast defines the abstract syntax tree of TLang (spec §5.2, grammar
// of DESIGN.md §2.2).
//
// # Node kinds
//
// Every node implements Node. Declarations and statements implement
// Statement, value expressions implement Expression, and type syntax
// implements TypeExpr. FieldDefinition, Parameter, Decorator and ObjectEntry
// are plain Nodes. All nodes are pointer types: the checker keys its side
// tables (types.Info) by node pointer, so the parser creates every node
// exactly once and never shares a node between two parents.
//
// # Positions
//
// Pos returns the position of the first byte of a node and End the position
// immediately after its last byte. Statements that end with ";" include it.
// Positions of individual tokens (keywords, operators, delimiters) are kept
// in fields named XxxPos or after the delimiter (Lparen, Rbrace, Semicolon).
// A zero position means the token is absent.
//
// # Parser invariants (relied upon by the checker and codegen)
//
//   - Program.Statements contains only *FunctionStatement,
//     *InterfaceStatement, *TypeAliasStatement, *LetStatement (globals) and
//     *BadStatement. Function, interface and type declarations are top-level
//     only. Decorators appear only on functions. Statements that are not
//     declarations at top level are a syntax error.
//   - Required children are never nil. When the parser cannot build a
//     construct it reports a diagnostic and substitutes BadStatement,
//     BadExpression or BadType for the smallest enclosing statement,
//     expression or type it can. Optional children that are absent are
//     untyped nil interface values or nil pointers, as documented per field
//     (never a typed nil pointer stored in an interface).
//   - Parentheses around expressions are not represented: grouping is
//     captured by the tree shape. Parentheses around types are kept
//     (ParenType) because "(T | null)[]" needs them.
//   - Assignment is a statement-level construct. An *AssignmentExpression
//     appears only as ExpressionStatement.Expression (statement position or
//     ForStatement.Init) or as ForStatement.Post. An assignment operator
//     anywhere else, including chained "a = b = c" and "f(x = 1)", is a
//     syntax error. Compound operators (+= -= *= /= %=) and postfix ++/--
//     are kept as written, not desugared, so that diagnostics and golden
//     files show the source form; the checker and codegen desugar them
//     (see AssignmentExpression.BinaryOp). Prefix ++x/--x is a syntax
//     error.
//   - ObjectType appears only as TypeAliasStatement.Value.
//   - Arrow functions are accepted only in the transaction statement form:
//     a statement that starts with IDENT "." "transaction" "(" followed by
//     "(" IDENT ")" "=>", "(" IDENT ":" type ")" "=>" or IDENT "=>", and
//     continues with a block, ")" and ";", is parsed as one
//     *TransactionStatement (Receiver = that IDENT). The parser does not
//     look at what the receiver denotes; the checker requires the builtin
//     db. Every other occurrence of "=>" is a syntax error, and
//     db.transaction called with a non-arrow argument is an ordinary call
//     that the checker rejects.
//   - Explicit type arguments of calls are parsed speculatively
//     (DESIGN.md §2.2): f<T>(x) is a CallExpression with TypeArgs only when
//     "<" starts a well-formed type-argument list followed directly by "(".
//
// # String
//
// String returns a deterministic single-line rendering used by parser golden
// tests. Infix, prefix and ternary expressions are fully parenthesized
// ("((a + b) * c)", "(-x)", "(c ? a : b)"); postfix forms are not
// ("a.b", "xs[0]", "f(x)", "x!"). String literals are printed with Go
// quoting (strconv.Quote of the decoded value); number literals as written.
// Blocks print as "{ s1 s2 }" ("{}" when empty), and a Program prints one
// top-level statement per line.
package ast

import (
	"strconv"
	"strings"

	"tlang/token"
)

// Node is implemented by every AST node.
type Node interface {
	// Pos returns the position of the first byte of the node.
	Pos() token.Position
	// End returns the position immediately after the last byte of the node.
	End() token.Position
	// String returns the deterministic rendering described in the package
	// comment.
	String() string
}

// Statement is a declaration or statement node.
type Statement interface {
	Node
	statementNode()
}

// Expression is a value expression node.
type Expression interface {
	Node
	expressionNode()
}

// TypeExpr is a type syntax node.
type TypeExpr interface {
	Node
	typeExprNode()
}

// ---------------------------------------------------------------------------
// Program

// Program is the root of one parsed source file.
type Program struct {
	// File is the source file name given to the parser.
	File string
	// Statements are the top-level declarations in source order (see the
	// package comment for the allowed node types).
	Statements []Statement
	// EOF is the position of the EOF token (just past the last byte).
	EOF token.Position
}

// Pos returns line 1, column 1.
func (p *Program) Pos() token.Position { return token.Position{Line: 1, Column: 1} }

// End returns the EOF position, or the end of the last statement if EOF is
// not set.
func (p *Program) End() token.Position {
	if p.EOF.IsValid() {
		return p.EOF
	}
	if n := len(p.Statements); n > 0 {
		return p.Statements[n-1].End()
	}
	return p.Pos()
}

// String prints each top-level statement followed by "\n".
func (p *Program) String() string {
	var b strings.Builder
	for _, s := range p.Statements {
		b.WriteString(nodeString(s))
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Type expressions

// NamedType is a type name with optional type arguments: int64, string,
// User, Context, Page<User>, T (a type parameter).
type NamedType struct {
	// Qualifier is the module namespace of a dotted type "m.User"; nil for
	// an unqualified type (DESIGN-modules.md §2.3). When set, Name is the
	// member after the ".".
	Qualifier *Identifier
	// Name is the type name.
	Name *Identifier
	// Langle is the position of "<"; zero when there are no type arguments.
	Langle token.Position
	// TypeArgs are the type arguments; nil when there are none. "Page<>"
	// is a syntax error.
	TypeArgs []TypeExpr
	// Rangle is the position of ">"; zero when there are no type arguments.
	Rangle token.Position
}

// ArrayType is "Elem[]".
type ArrayType struct {
	// Elem is the element type.
	Elem TypeExpr
	// Lbracket and Rbracket are the positions of "[" and "]".
	Lbracket, Rbracket token.Position
}

// OptionalType is "Elem | null". The grammar allows "| null" only once, at
// the end of a type: "T[] | null" is an optional array, "(T | null)[]" an
// array of optionals (with a ParenType).
type OptionalType struct {
	// Elem is the non-null type.
	Elem TypeExpr
	// PipePos is the position of "|"; NullPos the position of "null".
	PipePos, NullPos token.Position
}

// ParenType is "(Inner)".
type ParenType struct {
	// Lparen and Rparen are the positions of "(" and ")".
	Lparen, Rparen token.Position
	// Inner is the parenthesized type.
	Inner TypeExpr
}

// ObjectType is the body "{ field; ... }" of "type P = { ... };", which
// declares a new nominal interface named P (DESIGN.md §2.3). It appears only
// as TypeAliasStatement.Value.
type ObjectType struct {
	// Lbrace and Rbrace are the positions of "{" and "}".
	Lbrace, Rbrace token.Position
	// Fields are the field definitions in source order.
	Fields []*FieldDefinition
}

// BadType replaces a type the parser could not parse.
type BadType struct {
	// From is the first byte and To the position after the last byte of
	// the skipped source.
	From, To token.Position
}

func (*NamedType) typeExprNode()    {}
func (*ArrayType) typeExprNode()    {}
func (*OptionalType) typeExprNode() {}
func (*ParenType) typeExprNode()    {}
func (*ObjectType) typeExprNode()   {}
func (*BadType) typeExprNode()      {}

// Pos implements Node.
func (t *NamedType) Pos() token.Position {
	if t.Qualifier != nil {
		return t.Qualifier.Pos()
	}
	return t.Name.Pos()
}

// End implements Node.
func (t *NamedType) End() token.Position {
	if len(t.TypeArgs) > 0 && t.Rangle.IsValid() {
		return t.Rangle.Advance(1)
	}
	return t.Name.End()
}

// String returns "Name", "Name<A, B>", "m.Name" or "m.Name<A, B>". An
// unqualified type renders byte-identically to before the Qualifier field
// existed.
func (t *NamedType) String() string {
	s := ""
	if t.Qualifier != nil {
		s = identString(t.Qualifier) + "."
	}
	s += identString(t.Name)
	if len(t.TypeArgs) > 0 {
		s += "<" + joinNodes(t.TypeArgs, ", ") + ">"
	}
	return s
}

// Pos implements Node.
func (t *ArrayType) Pos() token.Position { return nodePos(t.Elem) }

// End implements Node.
func (t *ArrayType) End() token.Position { return t.Rbracket.Advance(1) }

// String returns "Elem[]".
func (t *ArrayType) String() string { return nodeString(t.Elem) + "[]" }

// Pos implements Node.
func (t *OptionalType) Pos() token.Position { return nodePos(t.Elem) }

// End implements Node.
func (t *OptionalType) End() token.Position { return t.NullPos.Advance(len("null")) }

// String returns "Elem | null".
func (t *OptionalType) String() string { return nodeString(t.Elem) + " | null" }

// Pos implements Node.
func (t *ParenType) Pos() token.Position { return t.Lparen }

// End implements Node.
func (t *ParenType) End() token.Position { return t.Rparen.Advance(1) }

// String returns "(Inner)".
func (t *ParenType) String() string { return "(" + nodeString(t.Inner) + ")" }

// Pos implements Node.
func (t *ObjectType) Pos() token.Position { return t.Lbrace }

// End implements Node.
func (t *ObjectType) End() token.Position { return t.Rbrace.Advance(1) }

// String returns "{ a: T; b?: U }", or "{}" without fields.
func (t *ObjectType) String() string { return fieldsString(t.Fields) }

// Pos implements Node.
func (t *BadType) Pos() token.Position { return t.From }

// End implements Node.
func (t *BadType) End() token.Position { return t.To }

// String returns "<bad type>".
func (t *BadType) String() string { return "<bad type>" }

// ---------------------------------------------------------------------------
// Auxiliary nodes

// FieldDefinition is one interface or object-type field: "name: T" or
// "name?: T".
type FieldDefinition struct {
	// Name is the field name.
	Name *Identifier
	// Optional reports whether the field was written with "?" ("name?: T").
	// This is the syntax only; semantically a field is optional when its
	// type is T | null, whether written "name?: T" or "name: T | null"
	// (types.Var.Optional).
	Optional bool
	// QuestionPos is the position of "?"; zero when Optional is false.
	QuestionPos token.Position
	// Type is the declared type, without the implicit "| null" of "?".
	Type TypeExpr
}

// Pos implements Node.
func (f *FieldDefinition) Pos() token.Position { return f.Name.Pos() }

// End implements Node. The separator ";" or "," is not part of the field.
func (f *FieldDefinition) End() token.Position { return nodeEnd(f.Type, f.Name.End()) }

// String returns "name: T" or "name?: T".
func (f *FieldDefinition) String() string {
	s := identString(f.Name)
	if f.Optional {
		s += "?"
	}
	return s + ": " + nodeString(f.Type)
}

// Parameter is a function parameter or method receiver: "name: T".
type Parameter struct {
	// Name is the parameter name.
	Name *Identifier
	// Type is the declared type (always present: parameters need a type).
	Type TypeExpr
}

// Pos implements Node.
func (p *Parameter) Pos() token.Position { return p.Name.Pos() }

// End implements Node.
func (p *Parameter) End() token.Position { return nodeEnd(p.Type, p.Name.End()) }

// String returns "name: T".
func (p *Parameter) String() string { return identString(p.Name) + ": " + nodeString(p.Type) }

// Decorator is "@Name(arg, ...)", resolved at compile time (spec §10). The
// parser accepts any name; the checker knows only "Use".
type Decorator struct {
	// AtPos is the position of "@".
	AtPos token.Position
	// Name is the decorator name ("Use").
	Name *Identifier
	// Lparen and Rparen are the positions of "(" and ")".
	Lparen, Rparen token.Position
	// Args are the identifier arguments (guard function names for @Use),
	// possibly empty.
	Args []*Identifier
}

// Pos implements Node.
func (d *Decorator) Pos() token.Position { return d.AtPos }

// End implements Node.
func (d *Decorator) End() token.Position { return d.Rparen.Advance(1) }

// String returns "@Name(a, b)".
func (d *Decorator) String() string {
	return "@" + identString(d.Name) + "(" + joinIdents(d.Args, ", ") + ")"
}

// ObjectEntry is one "key: value" entry of an ObjectLiteral.
type ObjectEntry struct {
	// Key is the field name. Only identifier keys are accepted; quoted keys
	// and shorthand entries ("{ id }") are syntax errors.
	Key *Identifier
	// ColonPos is the position of ":".
	ColonPos token.Position
	// Value is the field value.
	Value Expression
}

// Pos implements Node.
func (e *ObjectEntry) Pos() token.Position { return e.Key.Pos() }

// End implements Node.
func (e *ObjectEntry) End() token.Position { return nodeEnd(e.Value, e.ColonPos.Advance(1)) }

// String returns "key: value".
func (e *ObjectEntry) String() string { return identString(e.Key) + ": " + nodeString(e.Value) }

// ---------------------------------------------------------------------------
// Declarations (top-level statements)

// InterfaceStatement declares a nominal interface type (a C struct):
// "interface Name<T, U> { field; ... }". Fields are separated by ";" or ","
// and a trailing separator is optional.
type InterfaceStatement struct {
	// Exported reports whether the declaration was prefixed with "export"
	// (DESIGN-modules.md §2.2).
	Exported bool
	// ExportPos is the position of the "export" keyword; zero when Exported
	// is false.
	ExportPos token.Position
	// IsDefault reports whether the declaration was "export default".
	IsDefault bool
	// InterfacePos is the position of the "interface" keyword.
	InterfacePos token.Position
	// Name is the interface name.
	Name *Identifier
	// TypeParams are the type parameter names; nil if not generic.
	TypeParams []*Identifier
	// Lbrace and Rbrace are the positions of "{" and "}".
	Lbrace, Rbrace token.Position
	// Fields are the field definitions in declaration order (the C struct
	// field order).
	Fields []*FieldDefinition
}

// TypeAliasStatement is "type Name<T> = Value;". When Value is an
// *ObjectType the statement declares a new nominal interface named Name;
// otherwise Name is a transparent alias of Value (DESIGN.md §2.3).
type TypeAliasStatement struct {
	// Exported reports whether the declaration was prefixed with "export"
	// (DESIGN-modules.md §2.2).
	Exported bool
	// ExportPos is the position of the "export" keyword; zero when Exported
	// is false.
	ExportPos token.Position
	// IsDefault reports whether the declaration was "export default".
	IsDefault bool
	// TypePos is the position of the "type" keyword.
	TypePos token.Position
	// Name is the declared name.
	Name *Identifier
	// TypeParams are the type parameter names; nil if not generic.
	TypeParams []*Identifier
	// AssignPos is the position of "=".
	AssignPos token.Position
	// Value is the aliased type, or an *ObjectType body.
	Value TypeExpr
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// FunctionStatement is a top-level function or receiver method:
//
//	@Use(g1, g2)
//	fn (recv: T) name<T>(a: A, b: B): R { ... }
type FunctionStatement struct {
	// Exported reports whether the declaration was prefixed with "export"
	// (DESIGN-modules.md §2.2).
	Exported bool
	// ExportPos is the position of the "export" keyword; zero when Exported
	// is false.
	ExportPos token.Position
	// IsDefault reports whether the declaration was "export default".
	IsDefault bool
	// Decorators are the decorators in source order; nil if none.
	Decorators []*Decorator
	// FnPos is the position of the "fn" keyword.
	FnPos token.Position
	// Receiver is the receiver parameter of a method "fn (u: User) m()",
	// nil for a plain function.
	Receiver *Parameter
	// Name is the function or method name.
	Name *Identifier
	// TypeParams are the type parameter names; nil if not generic.
	TypeParams []*Identifier
	// Parameters are the parameters in order; nil or empty if none.
	Parameters []*Parameter
	// ReturnType is the declared result type; nil when omitted, which
	// means void.
	ReturnType TypeExpr
	// Body is the function body.
	Body *BlockStatement
}

// LetStatement declares a variable: "let x: T = v;" or "const x = v;". At
// top level it declares a global; inside a block, a local. In a C-style
// for loop header it is the Init statement and its Semicolon is the first
// ";" of the header.
type LetStatement struct {
	// Exported reports whether the declaration was prefixed with "export"
	// (DESIGN-modules.md §2.2). It is set only on top-level globals.
	Exported bool
	// ExportPos is the position of the "export" keyword; zero when Exported
	// is false.
	ExportPos token.Position
	// IsDefault reports whether the declaration was "export default".
	IsDefault bool
	// LetPos is the position of the "let" or "const" keyword.
	LetPos token.Position
	// IsConst reports whether the keyword was "const".
	IsConst bool
	// Name is the variable name.
	Name *Identifier
	// Type is the declared type; nil when omitted (inferred from Value).
	Type TypeExpr
	// Value is the initializer; nil when omitted.
	Value Expression
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

func (*InterfaceStatement) statementNode() {}
func (*TypeAliasStatement) statementNode() {}
func (*FunctionStatement) statementNode()  {}
func (*LetStatement) statementNode()       {}

// Pos implements Node.
func (s *InterfaceStatement) Pos() token.Position { return s.InterfacePos }

// End implements Node.
func (s *InterfaceStatement) End() token.Position { return s.Rbrace.Advance(1) }

// String returns "interface Name<T> { a: T; b?: U }".
func (s *InterfaceStatement) String() string {
	return "interface " + identString(s.Name) + typeParamsString(s.TypeParams) + " " + fieldsString(s.Fields)
}

// Pos implements Node.
func (s *TypeAliasStatement) Pos() token.Position { return s.TypePos }

// End implements Node.
func (s *TypeAliasStatement) End() token.Position {
	return semiEnd(s.Semicolon, nodeEnd(s.Value, s.Name.End()))
}

// String returns "type Name<T> = Value;".
func (s *TypeAliasStatement) String() string {
	return "type " + identString(s.Name) + typeParamsString(s.TypeParams) + " = " + nodeString(s.Value) + ";"
}

// Pos returns the position of the first decorator, or of "fn".
func (s *FunctionStatement) Pos() token.Position {
	if len(s.Decorators) > 0 {
		return s.Decorators[0].Pos()
	}
	return s.FnPos
}

// End implements Node.
func (s *FunctionStatement) End() token.Position {
	if s.Body != nil {
		return s.Body.End()
	}
	return s.Name.End()
}

// String returns "@Use(a) fn (r: T) name<U>(x: X): R { ... }"; the ": R"
// part is omitted when ReturnType is nil.
func (s *FunctionStatement) String() string {
	var b strings.Builder
	for _, d := range s.Decorators {
		b.WriteString(nodeString(d))
		b.WriteByte(' ')
	}
	b.WriteString("fn ")
	if s.Receiver != nil {
		b.WriteString("(" + s.Receiver.String() + ") ")
	}
	b.WriteString(identString(s.Name))
	b.WriteString(typeParamsString(s.TypeParams))
	b.WriteString("(")
	for i, p := range s.Parameters {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(nodeString(p))
	}
	b.WriteString(")")
	if s.ReturnType != nil {
		b.WriteString(": " + s.ReturnType.String())
	}
	b.WriteString(" ")
	b.WriteString(blockString(s.Body))
	return b.String()
}

// Pos implements Node.
func (s *LetStatement) Pos() token.Position { return s.LetPos }

// End implements Node.
func (s *LetStatement) End() token.Position {
	fallback := s.Name.End()
	if s.Type != nil {
		fallback = s.Type.End()
	}
	if s.Value != nil {
		fallback = s.Value.End()
	}
	return semiEnd(s.Semicolon, fallback)
}

// String returns "let x: T = v;", "const x = v;" or "let x: T;".
func (s *LetStatement) String() string {
	kw := "let "
	if s.IsConst {
		kw = "const "
	}
	out := kw + identString(s.Name)
	if s.Type != nil {
		out += ": " + s.Type.String()
	}
	if s.Value != nil {
		out += " = " + s.Value.String()
	}
	return out + ";"
}

// ---------------------------------------------------------------------------
// Statements

// BlockStatement is "{ statements }". It opens a lexical scope.
type BlockStatement struct {
	// Lbrace and Rbrace are the positions of "{" and "}".
	Lbrace, Rbrace token.Position
	// Statements are the statements in source order.
	Statements []Statement
}

// ExpressionStatement is "expr;": a call, an assignment, or (rejected by
// the checker) any other expression evaluated for effect.
type ExpressionStatement struct {
	// Expression is the expression.
	Expression Expression
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// ReturnStatement is "return;" or "return value;".
type ReturnStatement struct {
	// ReturnPos is the position of the "return" keyword.
	ReturnPos token.Position
	// Value is the returned value; nil for "return;".
	Value Expression
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// IfStatement is "if (Condition) Consequence else Alternative". An
// "else if" chain nests an *IfStatement as Alternative.
type IfStatement struct {
	// IfPos is the position of the "if" keyword.
	IfPos token.Position
	// Condition is the condition (must be bool).
	Condition Expression
	// Consequence is the statement run when Condition is true (usually a
	// *BlockStatement, but any statement is allowed).
	Consequence Statement
	// ElsePos is the position of "else"; zero when there is no else.
	ElsePos token.Position
	// Alternative is the else statement; nil when there is no else.
	Alternative Statement
}

// WhileStatement is "while (Condition) Body".
type WhileStatement struct {
	// WhilePos is the position of the "while" keyword.
	WhilePos token.Position
	// Condition is the loop condition (must be bool).
	Condition Expression
	// Body is the loop body.
	Body Statement
}

// ForStatement is the C-style loop "for (Init Condition; Post) Body".
type ForStatement struct {
	// ForPos is the position of the "for" keyword.
	ForPos token.Position
	// Init is nil (header starts with ";"), a *LetStatement or an
	// *ExpressionStatement; either includes the first ";" of the header.
	// Variables it declares are scoped to the loop.
	Init Statement
	// Condition is the loop condition; nil when omitted (loop forever).
	Condition Expression
	// Post is the expression run after each iteration (usually an
	// *AssignmentExpression such as i++ or a call); nil when omitted.
	Post Expression
	// Body is the loop body.
	Body Statement
}

// ForOfStatement is "for (const x of xs) Body" or "for (let x of xs) Body".
type ForOfStatement struct {
	// ForPos is the position of the "for" keyword.
	ForPos token.Position
	// IsConst reports whether the loop variable was declared with "const".
	IsConst bool
	// Var is the loop variable, scoped to the loop body.
	Var *Identifier
	// Iterable is the array being iterated.
	Iterable Expression
	// Body is the loop body.
	Body Statement
}

// BreakStatement is "break;".
type BreakStatement struct {
	// BreakPos is the position of the "break" keyword.
	BreakPos token.Position
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// ContinueStatement is "continue;".
type ContinueStatement struct {
	// ContinuePos is the position of the "continue" keyword.
	ContinuePos token.Position
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// ThrowStatement is "throw value;". Value is an Error ("new Error(...)" or
// a caught error) or a string ("throw "msg"" is sugar for
// "throw new Error("msg")", DESIGN.md §2.8).
type ThrowStatement struct {
	// ThrowPos is the position of the "throw" keyword.
	ThrowPos token.Position
	// Value is the thrown value.
	Value Expression
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// TryCatchStatement is "try Body catch (CatchParam) CatchBody", with the
// "(CatchParam)" part optional (DESIGN.md §2.8).
type TryCatchStatement struct {
	// TryPos is the position of the "try" keyword.
	TryPos token.Position
	// Body is the protected block.
	Body *BlockStatement
	// CatchPos is the position of the "catch" keyword.
	CatchPos token.Position
	// CatchParam is the error binding (type Error), scoped to CatchBody;
	// nil for "catch { ... }".
	CatchParam *Identifier
	// CatchBody is the handler block.
	CatchBody *BlockStatement
}

// TransactionStatement is the statement
//
//	db.transaction((tx) => { ... });
//
// which the compiler unrolls into a goto structure without a closure
// (spec §11.4). The exact parse rule is in the package comment.
type TransactionStatement struct {
	// Receiver is the expression before ".transaction" (an *Identifier;
	// the checker requires it to resolve to the builtin db).
	Receiver Expression
	// Method is the "transaction" identifier.
	Method *Identifier
	// Lparen is the position of the call's "(" and Rparen of its ")".
	Lparen, Rparen token.Position
	// Param is the arrow function parameter (the transaction handle, type
	// Transaction), scoped to Body.
	Param *Identifier
	// ParamType is the optional annotation of Param ("(tx: Transaction)");
	// nil when absent. The checker requires Transaction if present.
	ParamType TypeExpr
	// Body is the arrow function body; it is a lexical scope.
	Body *BlockStatement
	// Semicolon is the position of the terminating ";".
	Semicolon token.Position
}

// BadStatement replaces a statement or declaration the parser could not
// parse.
type BadStatement struct {
	// From is the first byte and To the position after the last byte of
	// the skipped source.
	From, To token.Position
}

func (*BlockStatement) statementNode()       {}
func (*ExpressionStatement) statementNode()  {}
func (*ReturnStatement) statementNode()      {}
func (*IfStatement) statementNode()          {}
func (*WhileStatement) statementNode()       {}
func (*ForStatement) statementNode()         {}
func (*ForOfStatement) statementNode()       {}
func (*BreakStatement) statementNode()       {}
func (*ContinueStatement) statementNode()    {}
func (*ThrowStatement) statementNode()       {}
func (*TryCatchStatement) statementNode()    {}
func (*TransactionStatement) statementNode() {}
func (*BadStatement) statementNode()         {}

// Pos implements Node.
func (s *BlockStatement) Pos() token.Position { return s.Lbrace }

// End implements Node.
func (s *BlockStatement) End() token.Position { return s.Rbrace.Advance(1) }

// String returns "{ s1 s2 }", or "{}" when empty.
func (s *BlockStatement) String() string {
	if len(s.Statements) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{")
	for _, st := range s.Statements {
		b.WriteByte(' ')
		b.WriteString(nodeString(st))
	}
	b.WriteString(" }")
	return b.String()
}

// Pos implements Node.
func (s *ExpressionStatement) Pos() token.Position { return nodePos(s.Expression) }

// End implements Node.
func (s *ExpressionStatement) End() token.Position {
	return semiEnd(s.Semicolon, nodeEnd(s.Expression, token.Position{}))
}

// String returns "expr;".
func (s *ExpressionStatement) String() string { return nodeString(s.Expression) + ";" }

// Pos implements Node.
func (s *ReturnStatement) Pos() token.Position { return s.ReturnPos }

// End implements Node.
func (s *ReturnStatement) End() token.Position {
	return semiEnd(s.Semicolon, nodeEnd(s.Value, s.ReturnPos.Advance(len("return"))))
}

// String returns "return;" or "return value;".
func (s *ReturnStatement) String() string {
	if s.Value == nil {
		return "return;"
	}
	return "return " + s.Value.String() + ";"
}

// Pos implements Node.
func (s *IfStatement) Pos() token.Position { return s.IfPos }

// End implements Node.
func (s *IfStatement) End() token.Position {
	if s.Alternative != nil {
		return s.Alternative.End()
	}
	return nodeEnd(s.Consequence, nodeEnd(s.Condition, s.IfPos.Advance(len("if"))))
}

// String returns "if (cond) stmt" or "if (cond) stmt else stmt".
func (s *IfStatement) String() string {
	out := "if (" + nodeString(s.Condition) + ") " + nodeString(s.Consequence)
	if s.Alternative != nil {
		out += " else " + s.Alternative.String()
	}
	return out
}

// Pos implements Node.
func (s *WhileStatement) Pos() token.Position { return s.WhilePos }

// End implements Node.
func (s *WhileStatement) End() token.Position {
	return nodeEnd(s.Body, nodeEnd(s.Condition, s.WhilePos.Advance(len("while"))))
}

// String returns "while (cond) stmt".
func (s *WhileStatement) String() string {
	return "while (" + nodeString(s.Condition) + ") " + nodeString(s.Body)
}

// Pos implements Node.
func (s *ForStatement) Pos() token.Position { return s.ForPos }

// End implements Node.
func (s *ForStatement) End() token.Position { return nodeEnd(s.Body, s.ForPos.Advance(len("for"))) }

// String returns "for (init cond; post) stmt", where init prints with its
// own ";" (or as ";" when nil), and a missing cond or post prints as
// nothing: "for (;;) {}", "for (let i = 0; (i < n); i++) { ... }".
func (s *ForStatement) String() string {
	var b strings.Builder
	b.WriteString("for (")
	if s.Init == nil {
		b.WriteString(";")
	} else {
		b.WriteString(s.Init.String())
	}
	if s.Condition != nil {
		b.WriteString(" " + s.Condition.String())
	}
	b.WriteString(";")
	if s.Post != nil {
		b.WriteString(" " + s.Post.String())
	}
	b.WriteString(") ")
	b.WriteString(nodeString(s.Body))
	return b.String()
}

// Pos implements Node.
func (s *ForOfStatement) Pos() token.Position { return s.ForPos }

// End implements Node.
func (s *ForOfStatement) End() token.Position { return nodeEnd(s.Body, s.ForPos.Advance(len("for"))) }

// String returns "for (const x of xs) stmt" (or "let").
func (s *ForOfStatement) String() string {
	kw := "let "
	if s.IsConst {
		kw = "const "
	}
	return "for (" + kw + identString(s.Var) + " of " + nodeString(s.Iterable) + ") " + nodeString(s.Body)
}

// Pos implements Node.
func (s *BreakStatement) Pos() token.Position { return s.BreakPos }

// End implements Node.
func (s *BreakStatement) End() token.Position {
	return semiEnd(s.Semicolon, s.BreakPos.Advance(len("break")))
}

// String returns "break;".
func (s *BreakStatement) String() string { return "break;" }

// Pos implements Node.
func (s *ContinueStatement) Pos() token.Position { return s.ContinuePos }

// End implements Node.
func (s *ContinueStatement) End() token.Position {
	return semiEnd(s.Semicolon, s.ContinuePos.Advance(len("continue")))
}

// String returns "continue;".
func (s *ContinueStatement) String() string { return "continue;" }

// Pos implements Node.
func (s *ThrowStatement) Pos() token.Position { return s.ThrowPos }

// End implements Node.
func (s *ThrowStatement) End() token.Position {
	return semiEnd(s.Semicolon, nodeEnd(s.Value, s.ThrowPos.Advance(len("throw"))))
}

// String returns "throw value;".
func (s *ThrowStatement) String() string { return "throw " + nodeString(s.Value) + ";" }

// Pos implements Node.
func (s *TryCatchStatement) Pos() token.Position { return s.TryPos }

// End implements Node.
func (s *TryCatchStatement) End() token.Position {
	if s.CatchBody != nil {
		return s.CatchBody.End()
	}
	return s.CatchPos.Advance(len("catch"))
}

// String returns "try { ... } catch (e) { ... }" or "try { ... } catch { ... }".
func (s *TryCatchStatement) String() string {
	out := "try " + blockString(s.Body) + " catch "
	if s.CatchParam != nil {
		out += "(" + s.CatchParam.Name + ") "
	}
	return out + blockString(s.CatchBody)
}

// Pos implements Node.
func (s *TransactionStatement) Pos() token.Position { return nodePos(s.Receiver) }

// End implements Node.
func (s *TransactionStatement) End() token.Position {
	if s.Semicolon.IsValid() {
		return s.Semicolon.Advance(1)
	}
	if s.Rparen.IsValid() {
		return s.Rparen.Advance(1)
	}
	if s.Body != nil {
		return s.Body.End()
	}
	return nodeEnd(s.Receiver, token.Position{})
}

// String returns "db.transaction((tx) => { ... });", with ": T" after the
// parameter when ParamType is set.
func (s *TransactionStatement) String() string {
	method := "transaction"
	if s.Method != nil {
		method = s.Method.Name
	}
	param := identString(s.Param)
	if s.ParamType != nil {
		param += ": " + s.ParamType.String()
	}
	return nodeString(s.Receiver) + "." + method + "((" + param + ") => " + blockString(s.Body) + ");"
}

// Pos implements Node.
func (s *BadStatement) Pos() token.Position { return s.From }

// End implements Node.
func (s *BadStatement) End() token.Position { return s.To }

// String returns "<bad statement>".
func (s *BadStatement) String() string { return "<bad statement>" }

// ---------------------------------------------------------------------------
// Expressions

// Identifier is a name used as an expression or declared by a node:
// variables, parameters, functions, types, fields, type parameters, and
// the builtin namespaces db and console.
type Identifier struct {
	// NamePos is the position of the first byte of the name.
	NamePos token.Position
	// Name is the identifier text.
	Name string
}

// IntegerLiteral is an integer literal. It has no sign: "-1" is a
// PrefixExpression. The checker treats it as an untyped constant.
type IntegerLiteral struct {
	// ValuePos is the position of the first byte.
	ValuePos token.Position
	// Raw is the exact source text ("42", "1_000", "0xFF").
	Raw string
	// Value is the value of Raw with "_" removed, read as decimal, or as
	// hexadecimal after a 0x/0X prefix. (Do not use strconv base 0: it
	// would accept octal and binary forms.) Value is 0 when Overflow is set.
	Value uint64
	// Overflow reports that the literal does not fit in 64 bits unsigned.
	// The parser does not report it; the checker reports E-CONST.
	Overflow bool
}

// FloatLiteral is a floating-point literal ("1.5", "1e9", "2.5e-3").
type FloatLiteral struct {
	// ValuePos is the position of the first byte.
	ValuePos token.Position
	// Raw is the exact source text.
	Raw string
	// Value is strconv.ParseFloat(Raw with "_" removed, 64). A literal
	// beyond the float64 range parses as +Inf; the checker reports E-CONST
	// for it.
	Value float64
}

// StringLiteral is a string literal.
type StringLiteral struct {
	// ValuePos is the position of the opening quote.
	ValuePos token.Position
	// Value is the decoded content as UTF-8 bytes (token.Token.Literal).
	Value string
	// ValueEnd is the position just past the closing quote (Token.End).
	ValueEnd token.Position
}

// BooleanLiteral is "true" or "false".
type BooleanLiteral struct {
	// ValuePos is the position of the keyword.
	ValuePos token.Position
	// Value is the literal value.
	Value bool
}

// NullLiteral is "null".
type NullLiteral struct {
	// NullPos is the position of the keyword.
	NullPos token.Position
}

// PrefixExpression is "!Right" or "-Right".
type PrefixExpression struct {
	// OpPos is the position of the operator.
	OpPos token.Position
	// Operator is token.BANG or token.MINUS.
	Operator token.TokenType
	// Right is the operand.
	Right Expression
}

// InfixExpression is a binary operation "Left Operator Right". Operator is
// one of PLUS MINUS ASTERISK SLASH MOD EQ NOT_EQ LT GT LT_EQ GT_EQ AND OR
// NULLISH.
type InfixExpression struct {
	// Left is the left operand.
	Left Expression
	// OpPos is the position of the operator.
	OpPos token.Position
	// Operator is the operator token type.
	Operator token.TokenType
	// Right is the right operand.
	Right Expression
}

// TernaryExpression is "Condition ? Consequence : Alternative".
type TernaryExpression struct {
	// Condition is the condition (must be bool).
	Condition Expression
	// QuestionPos is the position of "?".
	QuestionPos token.Position
	// Consequence is the value when Condition is true.
	Consequence Expression
	// ColonPos is the position of ":".
	ColonPos token.Position
	// Alternative is the value when Condition is false.
	Alternative Expression
}

// NonNullExpression is the postfix non-null assertion "Left!", which
// throws Error{500, "null value"} when Left is null (DESIGN.md §2.5).
type NonNullExpression struct {
	// Left is the operand (of type T | null).
	Left Expression
	// BangPos is the position of "!".
	BangPos token.Position
}

// CallExpression is "Function<TypeArgs>(Arguments)": a function call
// f(x), a method or builtin member call x.m(y), a generic call
// first<User>(xs) or db.query<User>(sql), or a conversion int32(x).
type CallExpression struct {
	// Function is the callee: an *Identifier, a *MemberExpression, or any
	// other expression (rejected by the checker).
	Function Expression
	// Langle and Rangle are the positions of "<" and ">" around explicit
	// type arguments; zero when there are none.
	Langle, Rangle token.Position
	// TypeArgs are the explicit type arguments; nil when there are none.
	TypeArgs []TypeExpr
	// Lparen and Rparen are the positions of "(" and ")".
	Lparen, Rparen token.Position
	// Arguments are the arguments in order; nil or empty when there are none.
	Arguments []Expression
}

// MemberExpression is "Object.Property": a field (u.id), a builtin member
// (s.len, ctx.path, err.message), or, as CallExpression.Function, a method
// or builtin method (u.greet(), ctx.text(...), console.log(...)).
type MemberExpression struct {
	// Object is the receiver expression.
	Object Expression
	// DotPos is the position of ".".
	DotPos token.Position
	// Property is the member name.
	Property *Identifier
}

// IndexExpression is "Left[Index]" (array element, bounds checked).
type IndexExpression struct {
	// Left is the indexed array.
	Left Expression
	// Lbracket and Rbracket are the positions of "[" and "]".
	Lbracket, Rbracket token.Position
	// Index is the index expression.
	Index Expression
}

// ArrayLiteral is "[a, b, ...]".
type ArrayLiteral struct {
	// Lbracket and Rbracket are the positions of "[" and "]".
	Lbracket, Rbracket token.Position
	// Elements are the elements in order; possibly empty. A trailing comma
	// is allowed.
	Elements []Expression
}

// ObjectLiteral is "{ k: v, ... }", allowed only where an interface type is
// expected (DESIGN.md §2.4).
type ObjectLiteral struct {
	// Lbrace and Rbrace are the positions of "{" and "}".
	Lbrace, Rbrace token.Position
	// Entries are the entries in source order. Duplicate keys are kept;
	// the checker reports them. A trailing comma is allowed.
	Entries []*ObjectEntry
}

// NewExpression is an allocation (DESIGN.md §2.4, §2.6, §2.8, §2.10):
//
//	new T()              Type = T, IsArray = false
//	new T<A>()           Type = T<A>
//	new T[]()            Type = T (the element type), IsArray = true
//	new T[][]()          Type = T[] (an ArrayType), IsArray = true
//	new global T()       Global = true
//	new Error(msg)       Type = Error, Args = [msg]
//	new Error(msg, 404)  Args = [msg, 404]
//
// The parser reads the type after "new" [global] as a primaryType followed
// by "[]" pairs; when at least one pair is present, the last pair sets
// IsArray and the rest stay in Type. "| null" is not accepted there.
type NewExpression struct {
	// NewPos is the position of the "new" keyword.
	NewPos token.Position
	// Global reports "new global ...": allocation in the global heap
	// instead of the request arena.
	Global bool
	// GlobalPos is the position of "global"; zero when Global is false.
	GlobalPos token.Position
	// Type is the allocated type, or the element type when IsArray.
	Type TypeExpr
	// IsArray reports "new T[]()": an empty array of Type.
	IsArray bool
	// Lparen and Rparen are the positions of "(" and ")".
	Lparen, Rparen token.Position
	// Args are the constructor arguments (only new Error accepts any).
	Args []Expression
}

// AssignmentExpression is an assignment statement's expression:
//
//	Target = Value     Operator ASSIGN
//	Target += Value    PLUS_ASSIGN (also MINUS_, ASTERISK_, SLASH_, MOD_ASSIGN)
//	Target++           INCREMENT, Value nil
//	Target--           DECREMENT, Value nil
//
// The operator is kept as written (see the package comment); BinaryOp
// gives the arithmetic operator for the compound forms. Target is an
// *Identifier, *MemberExpression or *IndexExpression (the checker rejects
// anything else). It appears only at statement level.
type AssignmentExpression struct {
	// Target is the assigned location.
	Target Expression
	// OpPos is the position of the operator.
	OpPos token.Position
	// Operator is ASSIGN, a compound *_ASSIGN, INCREMENT or DECREMENT.
	Operator token.TokenType
	// Value is the right-hand side; nil for INCREMENT and DECREMENT.
	Value Expression
}

// BadExpression replaces an expression the parser could not parse.
type BadExpression struct {
	// From is the first byte and To the position after the last byte of
	// the skipped source.
	From, To token.Position
}

func (*Identifier) expressionNode()           {}
func (*IntegerLiteral) expressionNode()       {}
func (*FloatLiteral) expressionNode()         {}
func (*StringLiteral) expressionNode()        {}
func (*BooleanLiteral) expressionNode()       {}
func (*NullLiteral) expressionNode()          {}
func (*PrefixExpression) expressionNode()     {}
func (*InfixExpression) expressionNode()      {}
func (*TernaryExpression) expressionNode()    {}
func (*NonNullExpression) expressionNode()    {}
func (*CallExpression) expressionNode()       {}
func (*MemberExpression) expressionNode()     {}
func (*IndexExpression) expressionNode()      {}
func (*ArrayLiteral) expressionNode()         {}
func (*ObjectLiteral) expressionNode()        {}
func (*NewExpression) expressionNode()        {}
func (*AssignmentExpression) expressionNode() {}
func (*BadExpression) expressionNode()        {}

// Pos implements Node.
func (x *Identifier) Pos() token.Position { return x.NamePos }

// End implements Node.
func (x *Identifier) End() token.Position { return x.NamePos.Advance(len(x.Name)) }

// String returns the name.
func (x *Identifier) String() string { return x.Name }

// Pos implements Node.
func (x *IntegerLiteral) Pos() token.Position { return x.ValuePos }

// End implements Node.
func (x *IntegerLiteral) End() token.Position { return x.ValuePos.Advance(len(x.Raw)) }

// String returns Raw.
func (x *IntegerLiteral) String() string { return x.Raw }

// Pos implements Node.
func (x *FloatLiteral) Pos() token.Position { return x.ValuePos }

// End implements Node.
func (x *FloatLiteral) End() token.Position { return x.ValuePos.Advance(len(x.Raw)) }

// String returns Raw.
func (x *FloatLiteral) String() string { return x.Raw }

// Pos implements Node.
func (x *StringLiteral) Pos() token.Position { return x.ValuePos }

// End returns ValueEnd, or an estimate (decoded length plus two quotes)
// when ValueEnd is not set.
func (x *StringLiteral) End() token.Position {
	if x.ValueEnd.IsValid() {
		return x.ValueEnd
	}
	return x.ValuePos.Advance(len(x.Value) + 2)
}

// String returns strconv.Quote(Value).
func (x *StringLiteral) String() string { return strconv.Quote(x.Value) }

// Pos implements Node.
func (x *BooleanLiteral) Pos() token.Position { return x.ValuePos }

// End implements Node.
func (x *BooleanLiteral) End() token.Position { return x.ValuePos.Advance(len(x.String())) }

// String returns "true" or "false".
func (x *BooleanLiteral) String() string {
	if x.Value {
		return "true"
	}
	return "false"
}

// Pos implements Node.
func (x *NullLiteral) Pos() token.Position { return x.NullPos }

// End implements Node.
func (x *NullLiteral) End() token.Position { return x.NullPos.Advance(len("null")) }

// String returns "null".
func (x *NullLiteral) String() string { return "null" }

// Pos implements Node.
func (x *PrefixExpression) Pos() token.Position { return x.OpPos }

// End implements Node.
func (x *PrefixExpression) End() token.Position { return nodeEnd(x.Right, x.OpPos.Advance(1)) }

// String returns "(" + operator + operand + ")", e.g. "(-x)", "(!ok)".
func (x *PrefixExpression) String() string {
	return "(" + string(x.Operator) + nodeString(x.Right) + ")"
}

// Pos implements Node.
func (x *InfixExpression) Pos() token.Position { return nodePos(x.Left) }

// End implements Node.
func (x *InfixExpression) End() token.Position {
	return nodeEnd(x.Right, x.OpPos.Advance(len(x.Operator)))
}

// String returns "(left op right)", e.g. "((a + b) * c)", "(x ?? 0)".
func (x *InfixExpression) String() string {
	return "(" + nodeString(x.Left) + " " + string(x.Operator) + " " + nodeString(x.Right) + ")"
}

// Pos implements Node.
func (x *TernaryExpression) Pos() token.Position { return nodePos(x.Condition) }

// End implements Node.
func (x *TernaryExpression) End() token.Position {
	return nodeEnd(x.Alternative, x.ColonPos.Advance(1))
}

// String returns "(cond ? a : b)".
func (x *TernaryExpression) String() string {
	return "(" + nodeString(x.Condition) + " ? " + nodeString(x.Consequence) + " : " + nodeString(x.Alternative) + ")"
}

// Pos implements Node.
func (x *NonNullExpression) Pos() token.Position { return nodePos(x.Left) }

// End implements Node.
func (x *NonNullExpression) End() token.Position { return x.BangPos.Advance(1) }

// String returns "left!".
func (x *NonNullExpression) String() string { return nodeString(x.Left) + "!" }

// Pos implements Node.
func (x *CallExpression) Pos() token.Position { return nodePos(x.Function) }

// End implements Node.
func (x *CallExpression) End() token.Position { return x.Rparen.Advance(1) }

// String returns "f(a, b)" or "f<T>(a)".
func (x *CallExpression) String() string {
	s := nodeString(x.Function)
	if len(x.TypeArgs) > 0 {
		s += "<" + joinNodes(x.TypeArgs, ", ") + ">"
	}
	return s + "(" + joinNodes(x.Arguments, ", ") + ")"
}

// Pos implements Node.
func (x *MemberExpression) Pos() token.Position { return nodePos(x.Object) }

// End implements Node.
func (x *MemberExpression) End() token.Position {
	if x.Property != nil {
		return x.Property.End()
	}
	return x.DotPos.Advance(1)
}

// String returns "object.property".
func (x *MemberExpression) String() string {
	return nodeString(x.Object) + "." + identString(x.Property)
}

// Pos implements Node.
func (x *IndexExpression) Pos() token.Position { return nodePos(x.Left) }

// End implements Node.
func (x *IndexExpression) End() token.Position { return x.Rbracket.Advance(1) }

// String returns "left[index]".
func (x *IndexExpression) String() string {
	return nodeString(x.Left) + "[" + nodeString(x.Index) + "]"
}

// Pos implements Node.
func (x *ArrayLiteral) Pos() token.Position { return x.Lbracket }

// End implements Node.
func (x *ArrayLiteral) End() token.Position { return x.Rbracket.Advance(1) }

// String returns "[a, b]".
func (x *ArrayLiteral) String() string { return "[" + joinNodes(x.Elements, ", ") + "]" }

// Pos implements Node.
func (x *ObjectLiteral) Pos() token.Position { return x.Lbrace }

// End implements Node.
func (x *ObjectLiteral) End() token.Position { return x.Rbrace.Advance(1) }

// String returns "{ k: v, k2: v2 }", or "{}" without entries.
func (x *ObjectLiteral) String() string {
	if len(x.Entries) == 0 {
		return "{}"
	}
	return "{ " + joinNodes(x.Entries, ", ") + " }"
}

// Pos implements Node.
func (x *NewExpression) Pos() token.Position { return x.NewPos }

// End implements Node.
func (x *NewExpression) End() token.Position { return x.Rparen.Advance(1) }

// String returns "new T(args)", "new global T[]()", etc.
func (x *NewExpression) String() string {
	s := "new "
	if x.Global {
		s += "global "
	}
	s += nodeString(x.Type)
	if x.IsArray {
		s += "[]"
	}
	return s + "(" + joinNodes(x.Args, ", ") + ")"
}

// Pos implements Node.
func (x *AssignmentExpression) Pos() token.Position { return nodePos(x.Target) }

// End implements Node.
func (x *AssignmentExpression) End() token.Position {
	return nodeEnd(x.Value, x.OpPos.Advance(len(x.Operator)))
}

// String returns "target = value", "target += value" or "target++". It is
// not parenthesized: assignments never nest.
func (x *AssignmentExpression) String() string {
	if x.Value == nil {
		return nodeString(x.Target) + string(x.Operator)
	}
	return nodeString(x.Target) + " " + string(x.Operator) + " " + x.Value.String()
}

// BinaryOp returns the arithmetic operator a compound assignment applies:
// PLUS for += and ++, MINUS for -= and --, ASTERISK for *=, SLASH for /=,
// MOD for %=, and "" for plain "=". "x op= v" means "x = x op v" with the
// location x evaluated once; "x++" means "x += 1".
func (x *AssignmentExpression) BinaryOp() token.TokenType {
	switch x.Operator {
	case token.PLUS_ASSIGN, token.INCREMENT:
		return token.PLUS
	case token.MINUS_ASSIGN, token.DECREMENT:
		return token.MINUS
	case token.ASTERISK_ASSIGN:
		return token.ASTERISK
	case token.SLASH_ASSIGN:
		return token.SLASH
	case token.MOD_ASSIGN:
		return token.MOD
	}
	return ""
}

// IsIncDec reports whether the operator is ++ or --.
func (x *AssignmentExpression) IsIncDec() bool {
	return x.Operator == token.INCREMENT || x.Operator == token.DECREMENT
}

// Pos implements Node.
func (x *BadExpression) Pos() token.Position { return x.From }

// End implements Node.
func (x *BadExpression) End() token.Position { return x.To }

// String returns "<bad expression>".
func (x *BadExpression) String() string { return "<bad expression>" }

// ---------------------------------------------------------------------------
// Helpers

func nodeString(n Node) string {
	if n == nil {
		return "<nil>"
	}
	return n.String()
}

func nodePos(n Node) token.Position {
	if n == nil {
		return token.Position{}
	}
	return n.Pos()
}

// nodeEnd returns n.End(), or fallback when n is nil.
func nodeEnd(n Node, fallback token.Position) token.Position {
	if n == nil {
		return fallback
	}
	return n.End()
}

// semiEnd returns the position after semi, or fallback when semi is absent.
func semiEnd(semi, fallback token.Position) token.Position {
	if semi.IsValid() {
		return semi.Advance(1)
	}
	return fallback
}

func identString(id *Identifier) string {
	if id == nil {
		return "<nil>"
	}
	return id.Name
}

func blockString(b *BlockStatement) string {
	if b == nil {
		return "<nil>"
	}
	return b.String()
}

func joinNodes[N Node](list []N, sep string) string {
	parts := make([]string, len(list))
	for i, n := range list {
		parts[i] = nodeString(n)
	}
	return strings.Join(parts, sep)
}

func joinIdents(list []*Identifier, sep string) string {
	parts := make([]string, len(list))
	for i, id := range list {
		parts[i] = identString(id)
	}
	return strings.Join(parts, sep)
}

func typeParamsString(tps []*Identifier) string {
	if len(tps) == 0 {
		return ""
	}
	return "<" + joinIdents(tps, ", ") + ">"
}

func fieldsString(fields []*FieldDefinition) string {
	if len(fields) == 0 {
		return "{}"
	}
	return "{ " + joinNodes(fields, "; ") + " }"
}
