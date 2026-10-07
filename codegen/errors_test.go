package codegen

import (
	"strconv"
	"testing"

	"tlang/token"
	"tlang/types"
)

func TestErrorFormat(t *testing.T) {
	cases := []struct {
		err  *cgError
		want string
	}{
		{&cgError{file: "app.ts", pos: pos(3, 7), msg: "unsupported input: x"}, "app.ts:3:7: unsupported input: x"},
		{&cgError{file: "app.ts", msg: "internal error: y"}, "app.ts: internal error: y"},
		{&cgError{pos: pos(1, 2), msg: "internal error: z"}, "1:2: internal error: z"},
		{&cgError{msg: "internal error: w"}, "internal error: w"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

// TestReasonPhrases pins the reason of every §12 item (plan D1): the
// rejection goldens match these texts, so a change here must be deliberate.
func TestReasonPhrases(t *testing.T) {
	p := pos(4, 9)
	user := testNamed("User", pos(1, 11))
	callee := &types.Func{Name: "first", TypeArgs: []types.Type{types.Typ[types.String]}}
	inst := &types.Func{Name: "twice", TypeArgs: []types.Type{types.Typ[types.Int64]}}
	cases := []struct {
		err  *cgError
		want string
	}{
		{errBad(p, badStatement), "unsupported input: bad statement"},
		{errBad(p, badExpression), "unsupported input: bad expression"},
		{errBad(p, badType), "unsupported input: bad type"},
		{errBad(p, invalidType), "unsupported input: invalid type"},
		{errGlobalNoType(p, "a"), "unsupported input: global a has no type"},
		{errExprNoType(p), "unsupported input: expression has no type"},
		{errVoidValue(p, "field X.v"), "unsupported input: void value in field X.v"},
		{errVoidValue(token.Position{}, "array of void"), "unsupported input: void value in array of void"},
		{errLeavesTx(p, "break"), "unsupported input: break leaves a transaction body"},
		{errLeavesTx(p, "continue"), "unsupported input: continue leaves a transaction body"},
		{errTxCall(p), "unsupported input: db.transaction needs an arrow function argument"},
		{errBuiltinStore(p, types.BuiltinArrayLen), "unsupported input: cannot assign to builtin member T[].len"},
		{errBuiltinStore(p, types.BuiltinCtxPath), "unsupported input: cannot assign to builtin member Context.path"},
		{errDupParam(p, "a"), "unsupported input: duplicate parameter name a"},
		{errDunderName(p, "parameter", "a__2"), `unsupported input: parameter name a__2 contains "__"`},
		{errDunderName(p, "receiver", "u__x"), `unsupported input: receiver name u__x contains "__"`},
		{
			errCollision("tl_f_logger", nameDecl{"interface f_logger", pos(1, 11)}, nameDecl{"function logger", pos(5, 4)}),
			"unsupported input: C name collision: tl_f_logger (interface f_logger at 1:11 and function logger at 5:4)",
		},
		{
			errReservedName("tl_globals", nameDecl{"interface globals", pos(1, 11)}),
			"unsupported input: C name collision: tl_globals is reserved for generated code (interface globals at 1:11)",
		},
		{errMissingInstance(p, callee, inst), "internal error: missing instance of first<string> in twice<int64>"},
		{errJSONTooWide(p, user, 65), "unsupported input: JSON type User has 65 fields; at most 64 are supported"},
		{errSQLNul(p), "unsupported input: SQL string contains a NUL byte"},
		{notImplemented(p, "for-of"), "internal error: not implemented yet: for-of"},
		{internalErr(p, "bad %s", "thing"), "internal error: bad thing"},
	}
	for _, c := range cases {
		if c.err.msg != c.want {
			t.Errorf("reason = %q, want %q", c.err.msg, c.want)
		}
	}
	// Positions: the collision is reported at the later declaration, a
	// reserved name at the user declaration, the rest where given.
	if got := errCollision("x", nameDecl{"a", pos(1, 1)}, nameDecl{"b", pos(2, 2)}).pos; got != pos(2, 2) {
		t.Errorf("collision reported at %v, want 2:2", got)
	}
	if got := errReservedName("x", nameDecl{"a", pos(1, 3)}).pos; got != pos(1, 3) {
		t.Errorf("reserved-name collision reported at %v, want 1:3", got)
	}
	if got := errDupParam(p, "a").pos; got != p {
		t.Errorf("duplicate parameter reported at %v, want %v", got, p)
	}
}

func TestCheckJSONWidth(t *testing.T) {
	fields := func(n int) []*types.Var {
		vs := make([]*types.Var, n)
		for i := range vs {
			vs[i] = &types.Var{Name: "f" + strconv.Itoa(i), Type: types.Typ[types.Int64]}
		}
		return vs
	}
	g := testGenerator()
	if err := catch(g, func() { g.checkJSONWidth(testNamed("Wide", pos(3, 11), fields(maxJSONFields)...)) }); err != nil {
		t.Fatalf("64 fields: %v", err)
	}
	err := catch(g, func() { g.checkJSONWidth(testNamed("Huge", pos(3, 11), fields(maxJSONFields+1)...)) })
	want := "test.tl:3:11: unsupported input: JSON type Huge has 65 fields; at most 64 are supported"
	if err == nil || err.Error() != want {
		t.Fatalf("65 fields: got %v, want %q", err, want)
	}
}

func TestFailAndRecovered(t *testing.T) {
	g := testGenerator()
	g.cur = pos(7, 3)

	// fail stamps the file and keeps the position of the error.
	err := catch(g, func() { g.fail(errTxCall(pos(2, 5))) })
	if err == nil || err.Error() != "test.tl:2:5: unsupported input: db.transaction needs an arrow function argument" {
		t.Fatalf("fail: got %v", err)
	}
	// An error that names its own file keeps it.
	err = catch(g, func() { g.fail(&cgError{file: "other.tl", msg: "internal error: x"}) })
	if err == nil || err.Error() != "other.tl: internal error: x" {
		t.Fatalf("fail with a file: got %v", err)
	}
	// Any other panic is an internal error at the current position.
	err = catch(g, func() { panic("boom") })
	if err == nil || err.Error() != "test.tl:7:3: internal error: boom" {
		t.Fatalf("recovered string panic: got %v", err)
	}
	// A *cgError panicked directly is returned with the file filled in.
	err = catch(g, func() { panic(internalErr(pos(1, 1), "direct")) })
	if err == nil || err.Error() != "test.tl:1:1: internal error: direct" {
		t.Fatalf("recovered *cgError panic: got %v", err)
	}
	// A bailout without an error does not crash the recovery.
	err = catch(g, func() { panic(bailout{}) })
	if err == nil || err.msg != prefixInternal+"{<nil>}" {
		t.Fatalf("recovered empty bailout: got %v", err)
	}
	if err := catch(g, func() {}); err != nil {
		t.Fatalf("no panic: got %v", err)
	}
}
