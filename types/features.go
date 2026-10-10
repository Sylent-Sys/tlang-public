package types

// RuntimeAPI identifies a runtime subsystem required by reachable program
// code. Values are stable names intended for compiler and tooling consumers.
type RuntimeAPI string

const (
	RuntimeAPIDatabase    RuntimeAPI = "database"
	RuntimeAPIEnvironment RuntimeAPI = "environment"
	RuntimeAPIHTTP        RuntimeAPI = "http"
	RuntimeAPIConsole     RuntimeAPI = "console"
)

// NativeRequirement identifies a native library required by reachable
// program code.
type NativeRequirement string

const (
	NativeRequirementLibPQ NativeRequirement = "libpq"
)

// ProgramFeatures is the deterministic, entry-rooted feature summary for a
// checked program. RuntimeAPIs and NativeRequirements are deduplicated and
// returned in a stable order. Database is deliberately a generic runtime API:
// this model does not infer a database engine from the use of db/tx calls.
type ProgramFeatures struct {
	RuntimeAPIs        []RuntimeAPI
	NativeRequirements []NativeRequirement
}
