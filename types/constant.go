package types

import (
	"errors"
	"go/constant"
	gotoken "go/token"
	"math"

	"tlang/token"
)

// Constant values use go/constant (arbitrary precision). Integer literals
// are constant.Int values, float literals constant.Float, true/false
// constant.Bool, string literals constant.String. Untyped constant
// expressions are folded exactly (FoldUnary, FoldBinary) and checked
// against their final type with Representable, so "-2147483648" fits
// int32 while "2147483648" does not.

// ErrDivisionByZero is returned by FoldBinary for a constant division or
// modulo by zero (diag code E-CONST).
var ErrDivisionByZero = errors.New("division by zero")

// InInt32Range reports whether v is an integer constant within the int32
// range. Float constants never qualify, even when integral (DESIGN.md
// §2.3: floats never convert to integers implicitly).
func InInt32Range(v constant.Value) bool {
	if v == nil || v.Kind() != constant.Int {
		return false
	}
	x, exact := constant.Int64Val(v)
	return exact && x >= math.MinInt32 && x <= math.MaxInt32
}

// InInt64Range reports whether v is an integer constant within the int64
// range.
func InInt64Range(v constant.Value) bool {
	if v == nil || v.Kind() != constant.Int {
		return false
	}
	_, exact := constant.Int64Val(v)
	return exact
}

// Representable reports whether constant v can be a value of type t
// (Optional types use their element type) and returns v converted to t:
//
//   - int32, int64: v is an integer constant in range (InInt32Range,
//     InInt64Range); the value is unchanged.
//   - float64: v is an integer or float constant whose nearest float64 is
//     finite; the value is rounded to float64.
//   - untyped int: v is an integer constant. untyped float: v is numeric
//     (converted to a float constant).
//   - bool, string: v has the matching kind.
//   - Invalid: always (unchanged).
//
// Anything else is not representable.
func Representable(v constant.Value, t Type) (constant.Value, bool) {
	if v == nil || v.Kind() == constant.Unknown {
		return nil, false
	}
	b, ok := NonOptional(t).(*Basic)
	if !ok {
		return nil, false
	}
	numeric := v.Kind() == constant.Int || v.Kind() == constant.Float
	switch b.Kind {
	case Invalid:
		return v, true
	case Int32:
		if InInt32Range(v) {
			return v, true
		}
	case Int64:
		if InInt64Range(v) {
			return v, true
		}
	case Float64:
		if numeric {
			f, _ := constant.Float64Val(v)
			if !math.IsInf(f, 0) && !math.IsNaN(f) {
				return constant.MakeFloat64(f), true
			}
		}
	case UntypedInt:
		if v.Kind() == constant.Int {
			return v, true
		}
	case UntypedFloat:
		if numeric {
			return constant.ToFloat(v), true
		}
	case Bool:
		if v.Kind() == constant.Bool {
			return v, true
		}
	case String:
		if v.Kind() == constant.String {
			return v, true
		}
	}
	return nil, false
}

// FoldUnary folds a prefix operator on a constant: MINUS on a number, BANG
// on a bool. It returns nil when the operation cannot be folded.
func FoldUnary(op token.TokenType, x constant.Value) constant.Value {
	if x == nil {
		return nil
	}
	switch {
	case op == token.MINUS && (x.Kind() == constant.Int || x.Kind() == constant.Float):
		return constant.UnaryOp(gotoken.SUB, x, 0)
	case op == token.BANG && x.Kind() == constant.Bool:
		return constant.UnaryOp(gotoken.NOT, x, 0)
	}
	return nil
}

// FoldBinary folds a binary operator on two constants with exact
// arithmetic, matching runtime semantics where they overlap:
//
//   - + - * on numbers; integer / truncates toward zero and % takes the sign
//     of the dividend (as in C); / on a float operand is exact division; %
//     needs two integers. A zero divisor returns ErrDivisionByZero.
//   - == != < > <= >= on numbers give a bool constant; == != also on bools.
//   - && || on bools.
//
// Integer and float operands mix (the result is a float), as for untyped
// constants. Strings are not folded. It returns (nil, nil) when the
// operation cannot be folded.
func FoldBinary(op token.TokenType, x, y constant.Value) (constant.Value, error) {
	if x == nil || y == nil {
		return nil, nil
	}
	xk, yk := x.Kind(), y.Kind()
	isNum := func(k constant.Kind) bool { return k == constant.Int || k == constant.Float }
	if isNum(xk) && isNum(yk) {
		bothInt := xk == constant.Int && yk == constant.Int
		switch op {
		case token.PLUS:
			return constant.BinaryOp(x, gotoken.ADD, y), nil
		case token.MINUS:
			return constant.BinaryOp(x, gotoken.SUB, y), nil
		case token.ASTERISK:
			return constant.BinaryOp(x, gotoken.MUL, y), nil
		case token.SLASH:
			if constant.Sign(y) == 0 {
				return nil, ErrDivisionByZero
			}
			if bothInt {
				return constant.BinaryOp(x, gotoken.QUO_ASSIGN, y), nil // truncated integer division
			}
			return constant.BinaryOp(x, gotoken.QUO, y), nil
		case token.MOD:
			if !bothInt {
				return nil, nil
			}
			if constant.Sign(y) == 0 {
				return nil, ErrDivisionByZero
			}
			return constant.BinaryOp(x, gotoken.REM, y), nil
		case token.EQ, token.NOT_EQ, token.LT, token.GT, token.LT_EQ, token.GT_EQ:
			return constant.MakeBool(constant.Compare(x, compareOp(op), y)), nil
		}
		return nil, nil
	}
	if xk == constant.Bool && yk == constant.Bool {
		switch op {
		case token.AND:
			return constant.BinaryOp(x, gotoken.LAND, y), nil
		case token.OR:
			return constant.BinaryOp(x, gotoken.LOR, y), nil
		case token.EQ, token.NOT_EQ:
			return constant.MakeBool(constant.Compare(x, compareOp(op), y)), nil
		}
	}
	return nil, nil
}

func compareOp(op token.TokenType) gotoken.Token {
	switch op {
	case token.EQ:
		return gotoken.EQL
	case token.NOT_EQ:
		return gotoken.NEQ
	case token.LT:
		return gotoken.LSS
	case token.GT:
		return gotoken.GTR
	case token.LT_EQ:
		return gotoken.LEQ
	}
	return gotoken.GEQ
}
