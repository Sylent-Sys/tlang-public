package types

// BuiltinID identifies one builtin namespace, function, conversion or
// member (DESIGN.md §2.6, §2.7, §2.8, §2.11). Codegen switches on it to
// emit the matching runtime call. The zero value BuiltinInvalid means
// "none".
type BuiltinID int

// Builtin IDs. The comment gives the TLang form and its result type.
const (
	BuiltinInvalid BuiltinID = iota

	// Universe namespaces (objects of kind *Builtin; not values).
	BuiltinConsole // console
	BuiltinDB      // db

	// console (§2.11): any number of string/number/bool arguments.
	BuiltinConsoleLog   // reserved legacy ID; no longer source-visible
	BuiltinConsoleError // reserved legacy ID; structured API has appended ID

	// Explicit numeric conversions (§2.3); the callee identifier resolves to
	// the universe TypeName int32/int64/float64.
	BuiltinConvInt32   // int32(x): int32, x numeric
	BuiltinConvInt64   // int64(x): int64, x numeric
	BuiltinConvFloat64 // float64(x): float64, x numeric

	// toString on numbers and bools (§2.7), arena allocation.
	BuiltinInt32ToString   // n.toString(): string, n int32
	BuiltinInt64ToString   // n.toString(): string, n int64
	BuiltinFloat64ToString // n.toString(): string, n float64
	BuiltinBoolToString    // b.toString(): string

	// string members (§2.7).
	BuiltinStringLen         // s.len: int64 (field-like)
	BuiltinStringEq          // s.eq(t: string): bool
	BuiltinStringSlice       // s.slice(a: int64, b: int64): string; negative indices count from the end, then clamp to [0, len]
	BuiltinStringStartsWith  // s.startsWith(t: string): bool
	BuiltinStringEndsWith    // s.endsWith(t: string): bool
	BuiltinStringIndexOf     // s.indexOf(t: string): int64, -1 if absent
	BuiltinStringClone       // s.clone(): string, arena copy
	BuiltinStringCloneGlobal // s.clone_global(): string, global heap copy (region global)
	BuiltinStringToInt       // s.toInt(): int64, throws Error{400, "invalid integer"}

	// array members (§2.6).
	BuiltinArrayLen  // xs.len: int64 (field-like)
	BuiltinArrayPush // xs.push(v: T): void

	// Error members (§2.8), field-like.
	BuiltinErrorMessage // e.message: string
	BuiltinErrorStatus  // e.status: int32

	// Context members (§2.11).
	BuiltinCtxMethod    // ctx.method: string (field-like)
	BuiltinCtxPath      // ctx.path: string (field-like)
	BuiltinCtxRawQuery  // ctx.rawQuery: string (field-like)
	BuiltinCtxBody      // ctx.body: string (field-like)
	BuiltinCtxHeader    // ctx.header(name: string): string, "" if absent
	BuiltinCtxQuery     // ctx.query(name: string): string, lazily decoded, "" if absent
	BuiltinCtxParam     // ctx.param(name: string): string
	BuiltinCtxParamInt  // ctx.paramInt(name: string): int64, throws BadRequest (400)
	BuiltinCtxMatch     // ctx.match(method, pattern): bool, both string literals (Info.Routes)
	BuiltinCtxBindJSON  // ctx.bindJson(obj: interface): bool (Info.JSONTypes, Parse)
	BuiltinCtxText      // ctx.text(status: int32, body: string): void
	BuiltinCtxJSON      // ctx.json(status: int32, v: interface or array): void (Info.JSONTypes, Write)
	BuiltinCtxSetHeader // ctx.setHeader(name: string, value: string): void, may fail (500 invalid header)

	// db members (§2.11, §11); sql is a string literal, args are numbers,
	// bools, strings or optionals of these.
	BuiltinDBExecute     // db.execute(sql, args...): int64 rows affected
	BuiltinDBQuery       // db.query<T>(sql, args...): T[] (Info.DBTypes)
	BuiltinDBQueryOne    // db.queryOne<T>(sql, args...): T | null (Info.DBTypes)
	BuiltinDBTransaction // db.transaction((tx) => { ... }); only as ast.TransactionStatement

	// Transaction members, same rules as the db methods.
	BuiltinTxExecute  // tx.execute(sql, args...): int64
	BuiltinTxQuery    // tx.query<T>(sql, args...): T[]
	BuiltinTxQueryOne // tx.queryOne<T>(sql, args...): T | null

	// Appended to preserve every existing BuiltinID numeric value.
	BuiltinConsoleInfo // historical ID; source API is appended below
	BuiltinEnv         // env namespace
	BuiltinEnvGet      // env.get(name: string): string | null, may fail
	BuiltinConsoleDebug
	BuiltinConsoleWarn
	// Structured log variants append IDs; the old entry points above remain reserved.
	BuiltinJsonFromNull
	BuiltinJsonBool
	BuiltinJsonNumber
	BuiltinJsonString
	BuiltinJsonArray
	BuiltinJsonObject
	BuiltinJsonKind
	BuiltinJsonAsBool
	BuiltinJsonAsNumber
	BuiltinJsonAsString
	BuiltinJsonArrayValues
	BuiltinJsonObjectKeys
	BuiltinJsonObjectValues
	BuiltinJsonGet
	BuiltinConsoleDebugFields
	BuiltinConsoleInfoFields
	BuiltinConsoleWarnFields
	BuiltinConsoleErrorFields

	numBuiltins
)

// StandardExportDescriptor is a stable semantic bridge from the module
// registry without importing module into types.
type StandardExportDescriptor uint8

const (
	StandardExportNone StandardExportDescriptor = iota
	StandardExportDatabase
	StandardExportSystemConsole
	StandardExportSystemEnv
)

// BuiltinRecv says what a builtin belongs to.
type BuiltinRecv int

const (
	// RecvNone: a universe namespace or a conversion function.
	RecvNone BuiltinRecv = iota
	// RecvConsole: member of the console namespace.
	RecvConsole
	// RecvDB: member of the db namespace.
	RecvDB
	// RecvInt32, RecvInt64, RecvFloat64, RecvBool, RecvString: member of a
	// value of that type.
	RecvInt32
	RecvInt64
	RecvFloat64
	RecvBool
	RecvString
	// RecvArray: member of any array.
	RecvArray
	// RecvError: member of an Error value.
	RecvError
	// RecvContext: member of a Context value.
	RecvContext
	// RecvTransaction: member of a Transaction value.
	RecvTransaction
	// RecvEnv: member of the env namespace.
	RecvEnv
	// RecvJsonValue: member of a JsonValue.
	RecvJsonValue
	// RecvJsonType: static JsonValue constructor.
	RecvJsonType
)

// BuiltinInfo describes one builtin. The table is read-only.
type BuiltinInfo struct {
	// Name is the TLang spelling of the member or function ("startsWith",
	// "clone_global", "bindJson", "int32", "console").
	Name string
	// Recv says what the builtin is a member of.
	Recv BuiltinRecv
	// Method reports a builtin that must be called ("s.eq(t)"); false for
	// field-like members ("s.len", "ctx.path", "e.status") and namespaces.
	Method bool
	// Fails reports that the builtin may set the fiber error (the runtime
	// function is documented "May fail" in tlang.h): s.toInt and
	// ctx.paramInt (400), ctx.setHeader (500 invalid response header), and
	// every db/tx operation (500, 503, 499). Codegen tests __fib->err after
	// such a call.
	Fails bool
	// Allocates reports that the builtin may allocate (arena or global
	// heap). This does not make it fallible: per tlang.h, running out of
	// memory aborts the whole request with 503 through the runtime's
	// request-abort path (not an Error, not catchable), so no error check
	// is needed after an allocation.
	Allocates bool
	// Params are the parameter types when they are fixed; nil when Special.
	Params []Type
	// Result is the result type when it is fixed; nil when it depends on
	// the call (db.query<T>: T[]) or the builtin is a namespace.
	Result Type
	// Special reports that the checker must apply builtin-specific rules
	// instead of (or in addition to) Params: variadic printing arguments,
	// string-literal-only arguments, interface or serializable arguments,
	// type arguments, the element type of push, numeric conversion
	// operands, the transaction form, namespaces.
	Special bool
}

var (
	tInt32   = Typ[Int32]
	tInt64   = Typ[Int64]
	tFloat64 = Typ[Float64]
	tBool    = Typ[Bool]
	tString  = Typ[String]
	tVoid    = Typ[Void]
	tJson    = Typ[JsonValue]
)

var builtins = [numBuiltins]BuiltinInfo{
	BuiltinInvalid: {Name: "invalid builtin"},

	BuiltinConsole: {Name: "console", Special: true},
	BuiltinDB:      {Name: "db", Special: true},

	BuiltinConsoleLog:   {Name: "legacy log", Recv: RecvNone, Special: true},
	BuiltinConsoleError: {Name: "legacy error", Recv: RecvNone, Special: true},

	BuiltinConvInt32:   {Name: "int32", Method: true, Result: tInt32, Special: true},
	BuiltinConvInt64:   {Name: "int64", Method: true, Result: tInt64, Special: true},
	BuiltinConvFloat64: {Name: "float64", Method: true, Result: tFloat64, Special: true},

	BuiltinInt32ToString:   {Name: "toString", Recv: RecvInt32, Method: true, Allocates: true, Params: []Type{}, Result: tString},
	BuiltinInt64ToString:   {Name: "toString", Recv: RecvInt64, Method: true, Allocates: true, Params: []Type{}, Result: tString},
	BuiltinFloat64ToString: {Name: "toString", Recv: RecvFloat64, Method: true, Allocates: true, Params: []Type{}, Result: tString},
	BuiltinBoolToString:    {Name: "toString", Recv: RecvBool, Method: true, Allocates: true, Params: []Type{}, Result: tString},

	BuiltinStringLen:         {Name: "len", Recv: RecvString, Result: tInt64},
	BuiltinStringEq:          {Name: "eq", Recv: RecvString, Method: true, Params: []Type{tString}, Result: tBool},
	BuiltinStringSlice:       {Name: "slice", Recv: RecvString, Method: true, Params: []Type{tInt64, tInt64}, Result: tString},
	BuiltinStringStartsWith:  {Name: "startsWith", Recv: RecvString, Method: true, Params: []Type{tString}, Result: tBool},
	BuiltinStringEndsWith:    {Name: "endsWith", Recv: RecvString, Method: true, Params: []Type{tString}, Result: tBool},
	BuiltinStringIndexOf:     {Name: "indexOf", Recv: RecvString, Method: true, Params: []Type{tString}, Result: tInt64},
	BuiltinStringClone:       {Name: "clone", Recv: RecvString, Method: true, Allocates: true, Params: []Type{}, Result: tString},
	BuiltinStringCloneGlobal: {Name: "clone_global", Recv: RecvString, Method: true, Allocates: true, Params: []Type{}, Result: tString},
	BuiltinStringToInt:       {Name: "toInt", Recv: RecvString, Method: true, Fails: true, Params: []Type{}, Result: tInt64},

	BuiltinArrayLen:  {Name: "len", Recv: RecvArray, Result: tInt64},
	BuiltinArrayPush: {Name: "push", Recv: RecvArray, Method: true, Allocates: true, Result: tVoid, Special: true},

	BuiltinErrorMessage: {Name: "message", Recv: RecvError, Result: tString},
	BuiltinErrorStatus:  {Name: "status", Recv: RecvError, Result: tInt32},

	BuiltinCtxMethod:    {Name: "method", Recv: RecvContext, Result: tString},
	BuiltinCtxPath:      {Name: "path", Recv: RecvContext, Result: tString},
	BuiltinCtxRawQuery:  {Name: "rawQuery", Recv: RecvContext, Result: tString},
	BuiltinCtxBody:      {Name: "body", Recv: RecvContext, Result: tString},
	BuiltinCtxHeader:    {Name: "header", Recv: RecvContext, Method: true, Params: []Type{tString}, Result: tString},
	BuiltinCtxQuery:     {Name: "query", Recv: RecvContext, Method: true, Allocates: true, Params: []Type{tString}, Result: tString},
	BuiltinCtxParam:     {Name: "param", Recv: RecvContext, Method: true, Params: []Type{tString}, Result: tString},
	BuiltinCtxParamInt:  {Name: "paramInt", Recv: RecvContext, Method: true, Fails: true, Params: []Type{tString}, Result: tInt64},
	BuiltinCtxMatch:     {Name: "match", Recv: RecvContext, Method: true, Params: []Type{tString, tString}, Result: tBool, Special: true},
	BuiltinCtxBindJSON:  {Name: "bindJson", Recv: RecvContext, Method: true, Allocates: true, Result: tBool, Special: true},
	BuiltinCtxText:      {Name: "text", Recv: RecvContext, Method: true, Allocates: true, Params: []Type{tInt32, tString}, Result: tVoid},
	BuiltinCtxJSON:      {Name: "json", Recv: RecvContext, Method: true, Allocates: true, Result: tVoid, Special: true},
	BuiltinCtxSetHeader: {Name: "setHeader", Recv: RecvContext, Method: true, Fails: true, Allocates: true, Params: []Type{tString, tString}, Result: tVoid},

	BuiltinDBExecute:     {Name: "execute", Recv: RecvDB, Method: true, Fails: true, Allocates: true, Result: tInt64, Special: true},
	BuiltinDBQuery:       {Name: "query", Recv: RecvDB, Method: true, Fails: true, Allocates: true, Special: true},
	BuiltinDBQueryOne:    {Name: "queryOne", Recv: RecvDB, Method: true, Fails: true, Allocates: true, Special: true},
	BuiltinDBTransaction: {Name: "transaction", Recv: RecvDB, Method: true, Fails: true, Allocates: true, Result: tVoid, Special: true},

	BuiltinTxExecute:  {Name: "execute", Recv: RecvTransaction, Method: true, Fails: true, Allocates: true, Result: tInt64, Special: true},
	BuiltinTxQuery:    {Name: "query", Recv: RecvTransaction, Method: true, Fails: true, Allocates: true, Special: true},
	BuiltinTxQueryOne: {Name: "queryOne", Recv: RecvTransaction, Method: true, Fails: true, Allocates: true, Special: true},

	BuiltinEnv:                {Name: "env", Special: true},
	BuiltinEnvGet:             {Name: "get", Recv: RecvEnv, Method: true, Fails: true, Allocates: true, Params: []Type{tString}, Result: NewOptional(tString)},
	BuiltinConsoleInfo:        {Name: "legacy info", Recv: RecvNone, Special: true},
	BuiltinConsoleDebug:       {Name: "legacy debug", Recv: RecvNone, Special: true},
	BuiltinConsoleWarn:        {Name: "legacy warn", Recv: RecvNone, Special: true},
	BuiltinJsonFromNull:       {Name: "nullValue", Recv: RecvJsonType, Method: true, Allocates: true, Params: []Type{}, Result: tJson},
	BuiltinJsonBool:           {Name: "bool", Recv: RecvJsonType, Method: true, Allocates: true, Params: []Type{tBool}, Result: tJson},
	BuiltinJsonNumber:         {Name: "number", Recv: RecvJsonType, Method: true, Allocates: true, Params: []Type{tFloat64}, Result: tJson},
	BuiltinJsonString:         {Name: "string", Recv: RecvJsonType, Method: true, Allocates: true, Params: []Type{tString}, Result: tJson},
	BuiltinJsonArray:          {Name: "array", Recv: RecvJsonType, Method: true, Allocates: true, Params: []Type{NewArray(tJson)}, Result: tJson},
	BuiltinJsonObject:         {Name: "object", Recv: RecvJsonType, Method: true, Allocates: true, Fails: true, Params: []Type{NewArray(tString), NewArray(tJson)}, Result: tJson},
	BuiltinJsonKind:           {Name: "kind", Recv: RecvJsonValue, Result: tString},
	BuiltinJsonAsBool:         {Name: "asBool", Recv: RecvJsonValue, Method: true, Params: []Type{}, Result: NewOptional(tBool)},
	BuiltinJsonAsNumber:       {Name: "asNumber", Recv: RecvJsonValue, Method: true, Params: []Type{}, Result: NewOptional(tFloat64)},
	BuiltinJsonAsString:       {Name: "asString", Recv: RecvJsonValue, Method: true, Params: []Type{}, Result: NewOptional(tString)},
	BuiltinJsonArrayValues:    {Name: "arrayValues", Recv: RecvJsonValue, Method: true, Allocates: true, Params: []Type{}, Result: NewOptional(NewArray(tJson))},
	BuiltinJsonObjectKeys:     {Name: "objectKeys", Recv: RecvJsonValue, Method: true, Allocates: true, Params: []Type{}, Result: NewOptional(NewArray(tString))},
	BuiltinJsonObjectValues:   {Name: "objectValues", Recv: RecvJsonValue, Method: true, Allocates: true, Params: []Type{}, Result: NewOptional(NewArray(tJson))},
	BuiltinJsonGet:            {Name: "get", Recv: RecvJsonValue, Method: true, Allocates: true, Params: []Type{tString}, Result: NewOptional(tJson)},
	BuiltinConsoleDebugFields: {Name: "debug-fields", Recv: RecvConsole, Method: true, Params: []Type{tString, NewOptional(tJson)}, Result: tVoid, Special: true},
	BuiltinConsoleInfoFields:  {Name: "info-fields", Recv: RecvConsole, Method: true, Params: []Type{tString, NewOptional(tJson)}, Result: tVoid, Special: true},
	BuiltinConsoleWarnFields:  {Name: "warn-fields", Recv: RecvConsole, Method: true, Params: []Type{tString, NewOptional(tJson)}, Result: tVoid, Special: true},
	BuiltinConsoleErrorFields: {Name: "error-fields", Recv: RecvConsole, Method: true, Params: []Type{tString, NewOptional(tJson)}, Result: tVoid, Special: true},
}

// Info returns the table entry for id (the BuiltinInvalid entry for
// out-of-range values). The result must not be modified.
func (id BuiltinID) Info() *BuiltinInfo {
	if id < 0 || id >= numBuiltins {
		return &builtins[BuiltinInvalid]
	}
	return &builtins[id]
}

// MayFail reports whether a use of the builtin may leave the fiber error
// set (Info().Fails). Allocation is not a failure (see
// BuiltinInfo.Allocates). Conversions never fail: float-to-int conversions
// truncate toward zero and saturate, NaN gives 0 (tlang.h).
func (id BuiltinID) MayFail() bool { return id.Info().Fails }

// IsDB reports whether id is a database operation (db.* or tx.*), i.e.
// the program needs libpq (Info.UsesDB).
func (id BuiltinID) IsDB() bool {
	r := id.Info().Recv
	return id == BuiltinDB || r == RecvDB || r == RecvTransaction
}

var recvPrefix = [...]string{
	RecvNone: "", RecvConsole: "console.", RecvDB: "db.", RecvInt32: "int32.", RecvInt64: "int64.",
	RecvFloat64: "float64.", RecvBool: "bool.", RecvString: "string.", RecvArray: "T[].",
	RecvError: "Error.", RecvContext: "Context.", RecvTransaction: "Transaction.", RecvEnv: "env.",
}

// String returns a qualified name for diagnostics and debugging:
// "console.log", "string.startsWith", "T[].push", "Context.header",
// "db.query", "Transaction.execute", "int32" (conversion), "db".
func (id BuiltinID) String() string {
	in := id.Info()
	return recvPrefix[in.Recv] + in.Name
}

// MemberOf returns the builtin member name of a value of type recv, or
// BuiltinInvalid. recv is defaulted first (5.toString() is int64's), so
// untyped constants work. Optional receivers have no members (narrow
// first). User fields and methods are looked up by the checker, not here.
func MemberOf(recv Type, name string) BuiltinID {
	var r BuiltinRecv
	switch t := Default(recv).(type) {
	case *Array:
		r = RecvArray
	case *Basic:
		switch t.Kind {
		case Int32:
			r = RecvInt32
		case Int64:
			r = RecvInt64
		case Float64:
			r = RecvFloat64
		case Bool:
			r = RecvBool
		case String:
			r = RecvString
		case Error:
			r = RecvError
		case Context:
			r = RecvContext
		case Transaction:
			r = RecvTransaction
		case JsonValue:
			r = RecvJsonValue
		default:
			return BuiltinInvalid
		}
	default:
		return BuiltinInvalid
	}
	return lookupMember(r, name)
}

// NamespaceMember returns the member name of the namespace ns
// (BuiltinConsole, BuiltinDB or BuiltinEnv), or BuiltinInvalid.
func NamespaceMember(ns BuiltinID, name string) BuiltinID {
	switch ns {
	case BuiltinConsole:
		switch name {
		case "log":
			return BuiltinInvalid
		}
		if legacy := DeprecatedBuiltinName(lookupMember(RecvConsole, name)); legacy != "" || name == "warn" || name == "error" {
			return BuiltinInvalid
		}
		if member := StructuredConsoleMember(name); member != BuiltinInvalid {
			return member
		}
		return lookupMember(RecvConsole, name)
	case BuiltinDB:
		return lookupMember(RecvDB, name)
	case BuiltinEnv:
		return lookupMember(RecvEnv, name)
	}
	return BuiltinInvalid
}

func StructuredConsoleMember(name string) BuiltinID {
	switch name {
	case "debug":
		return BuiltinConsoleDebugFields
	case "info":
		return BuiltinConsoleInfoFields
	case "warn":
		return BuiltinConsoleWarnFields
	}
	return BuiltinInvalid
}

func DeprecatedBuiltinName(id BuiltinID) string {
	switch id {
	case BuiltinConsoleLog:
		return "legacy log"
	case BuiltinConsoleError:
		return "legacy error"
	}
	return ""
}

func StaticConsoleMember(name string) BuiltinID {
	switch name {
	case "info":
		return BuiltinConsoleInfoFields
	}
	return BuiltinInvalid
}

// StandardNamespaceMember returns the source-visible member of an imported
// standard namespace. Historical builtin IDs may remain available through
// NamespaceMember for ABI/tooling compatibility without being source-visible.
func StandardNamespaceMember(ns BuiltinID, name string) BuiltinID {
	if ns == BuiltinConsole && DeprecatedBuiltinName(lookupMember(RecvConsole, name)) != "" {
		return BuiltinInvalid
	}
	return NamespaceMember(ns, name)
}

// JsonValueConstructor resolves one constructor exposed on the JsonValue type.
func JsonValueConstructor(name string) BuiltinID {
	switch name {
	case "nullValue":
		return BuiltinJsonFromNull
	case "bool":
		return BuiltinJsonBool
	case "number":
		return BuiltinJsonNumber
	case "string":
		return BuiltinJsonString
	case "array":
		return BuiltinJsonArray
	case "object":
		return BuiltinJsonObject
	}
	return BuiltinInvalid
}

var standardDB = &Builtin{Name: "db", ID: BuiltinDB}
var standardConsole = &Builtin{Name: "console", ID: BuiltinConsole}
var standardEnv = &Builtin{Name: "env", ID: BuiltinEnv}

// StandardExportObject maps a stable front-end export descriptor to its
// canonical semantic object.
func StandardExportObject(id StandardExportDescriptor) Object {
	switch id {
	case StandardExportDatabase:
		return standardDB
	case StandardExportSystemConsole:
		return standardConsole
	case StandardExportSystemEnv:
		return standardEnv
	}
	return nil
}

// StandardBuiltin returns the canonical object exported by a standard
// module. Repeated imports and re-exports preserve pointer identity.
func StandardBuiltin(id BuiltinID) *Builtin {
	switch id {
	case BuiltinDB:
		return standardDB
	case BuiltinConsole:
		return standardConsole
	case BuiltinEnv:
		return standardEnv
	}
	return nil
}

// ConversionOf returns the conversion builtin whose callee denotes type t
// (int32, int64, float64), or BuiltinInvalid.
func ConversionOf(t Type) BuiltinID {
	switch {
	case IsBasic(t, Int32):
		return BuiltinConvInt32
	case IsBasic(t, Int64):
		return BuiltinConvInt64
	case IsBasic(t, Float64):
		return BuiltinConvFloat64
	}
	return BuiltinInvalid
}

func lookupMember(r BuiltinRecv, name string) BuiltinID {
	for id := BuiltinInvalid + 1; id < numBuiltins; id++ {
		if in := &builtins[id]; in.Recv == r && in.Name == name {
			return id
		}
	}
	return BuiltinInvalid
}
