package ast

// Visitor is called by Walk for each node. If Visit returns a non-nil
// Visitor w, Walk visits each child of node with w and then calls
// w.Visit(nil).
type Visitor interface {
	Visit(node Node) (w Visitor)
}

// Walk traverses the tree rooted at node in depth-first source order:
// it calls v.Visit(node), and if that returns w != nil, walks every non-nil
// child with w, then calls w.Visit(nil).
//
// Children, in order:
//
//	Program               Statements
//	InterfaceStatement    Name, TypeParams, Fields
//	TypeAliasStatement    Name, TypeParams, Value
//	FunctionStatement     Decorators, Receiver, Name, TypeParams, Parameters, ReturnType, Body
//	LetStatement          Name, Type, Value
//	BlockStatement        Statements
//	ExpressionStatement   Expression
//	ReturnStatement       Value
//	IfStatement           Condition, Consequence, Alternative
//	WhileStatement        Condition, Body
//	ForStatement          Init, Condition, Post, Body
//	ForOfStatement        Var, Iterable, Body
//	ThrowStatement        Value
//	TryCatchStatement     Body, CatchParam, CatchBody
//	TransactionStatement  Receiver, Method, Param, ParamType, Body
//	FieldDefinition       Name, Type
//	Parameter             Name, Type
//	Decorator             Name, Args
//	ObjectEntry           Key, Value
//	PrefixExpression      Right
//	InfixExpression       Left, Right
//	TernaryExpression     Condition, Consequence, Alternative
//	NonNullExpression     Left
//	CallExpression        Function, TypeArgs, Arguments
//	MemberExpression      Object, Property
//	IndexExpression       Left, Index
//	ArrayLiteral          Elements
//	ObjectLiteral         Entries
//	NewExpression         Type, Args
//	AssignmentExpression  Target, Value
//	NamedType             Qualifier, Name, TypeArgs
//	ArrayType             Elem
//	OptionalType          Elem
//	ParenType             Inner
//	ObjectType            Fields
//
// Identifiers, literals, BreakStatement, ContinueStatement and the Bad*
// nodes have no children.
func Walk(v Visitor, node Node) {
	if v = v.Visit(node); v == nil {
		return
	}
	switch n := node.(type) {
	case *Program:
		walkList(v, n.Statements)

	case *InterfaceStatement:
		walkIdent(v, n.Name)
		walkList(v, n.TypeParams)
		walkList(v, n.Fields)
	case *TypeAliasStatement:
		walkIdent(v, n.Name)
		walkList(v, n.TypeParams)
		walkOpt(v, n.Value)
	case *FunctionStatement:
		walkList(v, n.Decorators)
		if n.Receiver != nil {
			Walk(v, n.Receiver)
		}
		walkIdent(v, n.Name)
		walkList(v, n.TypeParams)
		walkList(v, n.Parameters)
		walkOpt(v, n.ReturnType)
		if n.Body != nil {
			Walk(v, n.Body)
		}
	case *LetStatement:
		walkIdent(v, n.Name)
		walkOpt(v, n.Type)
		walkOpt(v, n.Value)

	case *BlockStatement:
		walkList(v, n.Statements)
	case *ExpressionStatement:
		walkOpt(v, n.Expression)
	case *ReturnStatement:
		walkOpt(v, n.Value)
	case *IfStatement:
		walkOpt(v, n.Condition)
		walkOpt(v, n.Consequence)
		walkOpt(v, n.Alternative)
	case *WhileStatement:
		walkOpt(v, n.Condition)
		walkOpt(v, n.Body)
	case *ForStatement:
		walkOpt(v, n.Init)
		walkOpt(v, n.Condition)
		walkOpt(v, n.Post)
		walkOpt(v, n.Body)
	case *ForOfStatement:
		walkIdent(v, n.Var)
		walkOpt(v, n.Iterable)
		walkOpt(v, n.Body)
	case *BreakStatement, *ContinueStatement, *BadStatement:
	case *ThrowStatement:
		walkOpt(v, n.Value)
	case *TryCatchStatement:
		if n.Body != nil {
			Walk(v, n.Body)
		}
		walkIdent(v, n.CatchParam)
		if n.CatchBody != nil {
			Walk(v, n.CatchBody)
		}
	case *TransactionStatement:
		walkOpt(v, n.Receiver)
		walkIdent(v, n.Method)
		walkIdent(v, n.Param)
		walkOpt(v, n.ParamType)
		if n.Body != nil {
			Walk(v, n.Body)
		}

	case *FieldDefinition:
		walkIdent(v, n.Name)
		walkOpt(v, n.Type)
	case *Parameter:
		walkIdent(v, n.Name)
		walkOpt(v, n.Type)
	case *Decorator:
		walkIdent(v, n.Name)
		walkList(v, n.Args)
	case *ObjectEntry:
		walkIdent(v, n.Key)
		walkOpt(v, n.Value)

	case *Identifier, *IntegerLiteral, *FloatLiteral, *StringLiteral,
		*BooleanLiteral, *NullLiteral, *BadExpression:
	case *PrefixExpression:
		walkOpt(v, n.Right)
	case *InfixExpression:
		walkOpt(v, n.Left)
		walkOpt(v, n.Right)
	case *TernaryExpression:
		walkOpt(v, n.Condition)
		walkOpt(v, n.Consequence)
		walkOpt(v, n.Alternative)
	case *NonNullExpression:
		walkOpt(v, n.Left)
	case *CallExpression:
		walkOpt(v, n.Function)
		walkList(v, n.TypeArgs)
		walkList(v, n.Arguments)
	case *MemberExpression:
		walkOpt(v, n.Object)
		walkIdent(v, n.Property)
	case *IndexExpression:
		walkOpt(v, n.Left)
		walkOpt(v, n.Index)
	case *ArrayLiteral:
		walkList(v, n.Elements)
	case *ObjectLiteral:
		walkList(v, n.Entries)
	case *NewExpression:
		walkOpt(v, n.Type)
		walkList(v, n.Args)
	case *AssignmentExpression:
		walkOpt(v, n.Target)
		walkOpt(v, n.Value)

	case *NamedType:
		walkIdent(v, n.Qualifier)
		walkIdent(v, n.Name)
		walkList(v, n.TypeArgs)
	case *ArrayType:
		walkOpt(v, n.Elem)
	case *OptionalType:
		walkOpt(v, n.Elem)
	case *ParenType:
		walkOpt(v, n.Inner)
	case *ObjectType:
		walkList(v, n.Fields)
	case *BadType:
	}
	v.Visit(nil)
}

// walkOpt walks n unless it is a nil interface value.
func walkOpt(v Visitor, n Node) {
	if n != nil {
		Walk(v, n)
	}
}

func walkIdent(v Visitor, id *Identifier) {
	if id != nil {
		Walk(v, id)
	}
}

func walkList[N Node](v Visitor, list []N) {
	for _, n := range list {
		Walk(v, n)
	}
}

type inspector func(Node) bool

func (f inspector) Visit(node Node) Visitor {
	if f(node) {
		return f
	}
	return nil
}

// Inspect traverses the tree rooted at node in the order of Walk, calling
// f(node) for each node; when f returns true, Inspect visits the node's
// children and then calls f(nil).
func Inspect(node Node, f func(Node) bool) {
	Walk(inspector(f), node)
}
