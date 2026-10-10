package types

import (
	"strings"
	"testing"
)

func TestBuiltinTable(t *testing.T) {
	if BuiltinConsoleInfo != 45 || BuiltinEnv != BuiltinConsoleInfo+1 || BuiltinEnvGet != BuiltinEnv+1 {
		t.Fatalf("appended builtin IDs = %d, %d, %d; existing ID layout changed", BuiltinConsoleInfo, BuiltinEnv, BuiltinEnvGet)
	}
	seen := map[[2]any]BuiltinID{}
	for id := BuiltinInvalid + 1; id < numBuiltins; id++ {
		in := id.Info()
		if in.Name == "" {
			t.Errorf("builtin %d has no name", id)
		}
		key := [2]any{in.Recv, in.Name}
		if prev, dup := seen[key]; dup {
			t.Errorf("%v and %v share receiver and name", prev, id)
		}
		seen[key] = id
		if !in.Method && (in.Allocates || in.Fails || in.Params != nil) && in.Recv != RecvNone {
			t.Errorf("%v: field-like members take no arguments and cannot fail", id)
		}
		if !in.Method && in.Recv != RecvNone && in.Result == nil {
			t.Errorf("%v: field-like member without a result type", id)
		}
		if in.Method && !in.Special && (in.Params == nil || in.Result == nil) {
			t.Errorf("%v: non-special method needs Params and Result", id)
		}
	}
	if BuiltinID(-1).Info().Name != "invalid builtin" || numBuiltins.Info().Name != "invalid builtin" {
		t.Error("out-of-range Info")
	}
}

func TestStructuredConsoleAndJsonValueSignatures(t *testing.T) {
	for name, id := range map[string]BuiltinID{"debug": BuiltinConsoleDebugFields, "info": BuiltinConsoleInfoFields, "warn": BuiltinConsoleWarnFields} {
		in := id.Info()
		if in.Name != name+"-fields" || len(in.Params) != 2 || !IsString(in.Params[0]) || !IsOptional(in.Params[1]) || !IsBasic(NonOptional(in.Params[1]), JsonValue) || !IsVoid(in.Result) {
			t.Errorf("console.%s signature = %+v", name, in)
		}
	}
	if !IsBasic(BuiltinJsonFromNull.Info().Result, JsonValue) || !IsOptional(BuiltinJsonAsString.Info().Result) {
		t.Fatal("JsonValue constructor/accessor signatures")
	}
	if StandardNamespaceMember(BuiltinConsole, "log") != BuiltinInvalid {
		t.Fatal("legacy console methods must not be source-visible")
	}
}

func TestBuiltinLookup(t *testing.T) {
	user := iface("User")
	members := []struct {
		recv Type
		name string
		want BuiltinID
	}{
		{str, "len", BuiltinStringLen},
		{str, "startsWith", BuiltinStringStartsWith},
		{str, "clone_global", BuiltinStringCloneGlobal},
		{str, "toInt", BuiltinStringToInt},
		{str, "toString", BuiltinInvalid},
		{i32, "toString", BuiltinInt32ToString},
		{uint, "toString", BuiltinInt64ToString}, // 5.toString() defaults to int64
		{ufl, "toString", BuiltinFloat64ToString},
		{bl, "toString", BuiltinBoolToString},
		{arr(user), "len", BuiltinArrayLen},
		{arr(user), "push", BuiltinArrayPush},
		{errT, "message", BuiltinErrorMessage},
		{errT, "status", BuiltinErrorStatus},
		{ctx, "rawQuery", BuiltinCtxRawQuery},
		{ctx, "query", BuiltinCtxQuery},
		{ctx, "paramInt", BuiltinCtxParamInt},
		{ctx, "bindJson", BuiltinCtxBindJSON},
		{ctx, "json", BuiltinCtxJSON},
		{ctx, "setHeader", BuiltinCtxSetHeader},
		{ctx, "handleCreateUser", BuiltinInvalid}, // user methods are not builtins
		{tx, "query", BuiltinTxQuery},
		{tx, "queryOne", BuiltinTxQueryOne},
		{tx, "transaction", BuiltinInvalid},
		{opt(str), "len", BuiltinInvalid}, // narrow first
		{user, "len", BuiltinInvalid},
		{iface("P", "T").TypeParams[0], "len", BuiltinInvalid},
	}
	for _, m := range members {
		if got := MemberOf(m.recv, m.name); got != m.want {
			t.Errorf("MemberOf(%v, %q) = %v, want %v", m.recv, m.name, got, m.want)
		}
	}
	if got := ErrorMemberOf("category"); got != BuiltinErrorCategory {
		t.Errorf("ErrorMemberOf(category) = %v, want %v", got, BuiltinErrorCategory)
	}
	if got := ErrorMemberOf("code"); got != BuiltinErrorCode {
		t.Errorf("ErrorMemberOf(code) = %v, want %v", got, BuiltinErrorCode)
	}
	if ErrorBuiltinInfo(BuiltinErrorCategory).Result != tString || ErrorBuiltinInfo(BuiltinErrorCode).Result != tString {
		t.Fatal("Error.category and Error.code must have string type")
	}
	ns := []struct {
		ns   BuiltinID
		name string
		want BuiltinID
	}{
		{BuiltinConsole, "log", BuiltinInvalid},
		{BuiltinConsole, "error", BuiltinInvalid},
		{BuiltinConsole, "warn", BuiltinInvalid},
		{BuiltinDB, "execute", BuiltinDBExecute},
		{BuiltinDB, "query", BuiltinDBQuery},
		{BuiltinDB, "queryOne", BuiltinDBQueryOne},
		{BuiltinDB, "transaction", BuiltinDBTransaction},
		{BuiltinEnv, "get", BuiltinEnvGet},
		{BuiltinStringLen, "x", BuiltinInvalid},
	}
	for _, m := range ns {
		if got := NamespaceMember(m.ns, m.name); got != m.want {
			t.Errorf("NamespaceMember(%v, %q) = %v, want %v", m.ns, m.name, got, m.want)
		}
	}
	if ConversionOf(i32) != BuiltinConvInt32 || ConversionOf(i64) != BuiltinConvInt64 ||
		ConversionOf(f64) != BuiltinConvFloat64 || ConversionOf(str) != BuiltinInvalid {
		t.Error("ConversionOf")
	}
	if StandardNamespaceMember(BuiltinEnv, "get") != BuiltinEnvGet {
		t.Error("standard env namespace must expose get")
	}
	if StandardBuiltin(BuiltinEnv) != StandardExportObject(StandardExportSystemEnv) {
		t.Error("env standard export must preserve canonical builtin identity")
	}
}

func TestBuiltinProperties(t *testing.T) {
	// Exactly the operations tlang.h documents as "May fail".
	mayFail := []BuiltinID{BuiltinStringToInt, BuiltinCtxParamInt, BuiltinCtxSetHeader, BuiltinEnvGet, BuiltinDBExecute,
		BuiltinDBQuery, BuiltinDBQueryOne, BuiltinDBTransaction, BuiltinTxExecute, BuiltinTxQuery, BuiltinTxQueryOne}
	for _, id := range mayFail {
		if !id.MayFail() {
			t.Errorf("%v.MayFail() = false", id)
		}
	}
	count := 0
	for id := BuiltinInvalid + 1; id < numBuiltins; id++ {
		if id.MayFail() {
			count++
		}
	}
	if count != len(mayFail)+1 {
		t.Errorf("%d builtins may fail, want %d", count, len(mayFail))
	}
	// Allocation does not make a builtin fallible (OOM aborts the request).
	never := []BuiltinID{BuiltinStringLen, BuiltinStringEq, BuiltinStringSlice, BuiltinConsoleLog, BuiltinConvInt32,
		BuiltinConvInt64, BuiltinCtxHeader, BuiltinCtxParam, BuiltinCtxMatch, BuiltinErrorMessage, BuiltinArrayLen,
		BuiltinInt64ToString, BuiltinArrayPush, BuiltinStringClone, BuiltinCtxBindJSON, BuiltinCtxJSON, BuiltinCtxText,
		BuiltinCtxQuery}
	for _, id := range never {
		if id.MayFail() {
			t.Errorf("%v.MayFail() = true", id)
		}
	}
	for _, id := range []BuiltinID{BuiltinDB, BuiltinDBExecute, BuiltinDBTransaction, BuiltinTxQueryOne} {
		if !id.IsDB() {
			t.Errorf("%v.IsDB() = false", id)
		}
	}
	if BuiltinCtxQuery.IsDB() || BuiltinConsoleLog.IsDB() {
		t.Error("IsDB false positives")
	}
	names := map[BuiltinID]string{
		BuiltinConsoleLog: "legacy log", BuiltinConsoleError: "legacy error", BuiltinConsoleDebug: "legacy debug", BuiltinConsoleWarn: "legacy warn", BuiltinConsoleInfo: "legacy info", BuiltinStringStartsWith: "string.startsWith", BuiltinArrayPush: "T[].push",
		BuiltinCtxHeader: "Context.header", BuiltinDBQuery: "db.query", BuiltinTxExecute: "Transaction.execute",
		BuiltinConvInt32: "int32", BuiltinDB: "db", BuiltinInt64ToString: "int64.toString",
	}
	for id, want := range names {
		if id.String() != want {
			t.Errorf("String() = %q, want %q", id.String(), want)
		}
	}
	if in := BuiltinStringSlice.Info(); len(in.Params) != 2 || in.Params[0] != i64 || in.Result != str {
		t.Error("string.slice signature")
	}
	if in := BuiltinCtxText.Info(); len(in.Params) != 2 || in.Params[0] != i32 || in.Result != void {
		t.Error("Context.text signature")
	}
	if in := BuiltinEnvGet.Info(); len(in.Params) != 1 || in.Params[0] != str || !IsOptional(in.Result) || !IsString(NonOptional(in.Result)) || !in.Fails {
		t.Error("env.get signature must be (string) -> string | null and fallible")
	}
}

func TestUniverse(t *testing.T) {
	for _, name := range []string{"int32", "int64", "float64", "bool", "string", "void", "Context", "Error", "Transaction", "JsonValue"} {
		tn, ok := LookupUniverse(name).(*TypeName)
		if !ok || tn.Name != name || tn.Type.String() != name {
			t.Errorf("universe type %s = %v", name, LookupUniverse(name))
		}
	}
	if LookupUniverse("db") != nil {
		t.Error("db must not be in universe")
	}
	if LookupUniverse("console") != nil {
		t.Error("console must not be in universe")
	}
	for _, name := range []string{"null", "true", "number", "any", "Use"} {
		if LookupUniverse(name) != nil {
			t.Errorf("%s must not be in the universe", name)
		}
	}
	if got := strings.Join(UniverseNames(), " "); got != "Context Error JsonValue Transaction bool float64 int32 int64 string void" {
		t.Errorf("UniverseNames = %s", got)
	}
	if LookupUniverse("int32").(*TypeName).Type != Typ[Int32] {
		t.Error("universe types must be the Typ singletons")
	}
}
