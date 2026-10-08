package module

// StandardModuleID identifies a compiler-provided module. Values are stable
// front-end descriptors; semantic objects are owned by the types package.
type StandardModuleID uint8

const (
	StandardInvalid StandardModuleID = iota
	StandardDB
	StandardSystem
	StandardHTTP
)

// StandardExportID identifies a compiler-provided export.
type StandardExportID uint8

const (
	StandardExportInvalid StandardExportID = iota
	StandardExportDB
	StandardExportConsole
)

// StandardExport describes one named export of a standard module.
type StandardExport struct {
	ID   StandardExportID
	Name string
}

// StandardModule describes a virtual module. Available is false for reserved
// modules whose declarations have not shipped yet.
type StandardModule struct {
	ID        StandardModuleID
	Specifier string
	Available bool
	Exports   []StandardExport
}

var standardModules = [...]StandardModule{
	{ID: StandardDB, Specifier: "tlang/db", Available: true, Exports: []StandardExport{{ID: StandardExportDB, Name: "db"}}},
	{ID: StandardSystem, Specifier: "tlang/system", Available: true, Exports: []StandardExport{{ID: StandardExportConsole, Name: "console"}}},
	{ID: StandardHTTP, Specifier: "tlang/http", Available: false},
}

// LookupStandardModule returns the descriptor for an exact reserved
// specifier, including unavailable modules.
func LookupStandardModule(spec string) (*StandardModule, bool) {
	for i := range standardModules {
		if standardModules[i].Specifier == spec {
			return &standardModules[i], true
		}
	}
	return nil, false
}
