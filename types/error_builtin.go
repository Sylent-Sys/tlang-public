package types

// Error members live outside the historical builtin table so adding fields
// to Error does not renumber existing BuiltinIDs.
const (
	BuiltinErrorCategory BuiltinID = 1000 + iota
	BuiltinErrorCode
)

var errorBuiltins = [...]BuiltinInfo{
	{Name: "category", Recv: RecvError, Result: tString},
	{Name: "code", Recv: RecvError, Result: tString},
}

// ErrorBuiltinInfo describes a stable Error member added without renumbering
// the historical builtin table. The returned table entry is read-only.
func ErrorBuiltinInfo(id BuiltinID) *BuiltinInfo {
	index := int(id - BuiltinErrorCategory)
	if index < 0 || index >= len(errorBuiltins) {
		return nil
	}
	return &errorBuiltins[index]
}

func errorBuiltinInfo(id BuiltinID) *BuiltinInfo { return ErrorBuiltinInfo(id) }

// ErrorMemberOf resolves the source-visible Error ABI fields.
func ErrorMemberOf(name string) BuiltinID {
	switch name {
	case "message":
		return BuiltinErrorMessage
	case "status":
		return BuiltinErrorStatus
	}
	for id := BuiltinErrorCategory; id <= BuiltinErrorCode; id++ {
		if ErrorBuiltinInfo(id).Name == name {
			return id
		}
	}
	return BuiltinInvalid
}
