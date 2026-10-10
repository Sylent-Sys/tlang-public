package checker

import (
	"reflect"
	"testing"

	"tlang/types"
)

func TestProgramFeaturesIgnoreUnreachableDatabaseCalls(t *testing.T) {
	_, info, diags := checkProg(t, `
fn unreachable(): void { db.execute("SELECT 1"); }
fn main(): void {}
`)
	wantCodes(t, diags)
	if info.UsesDB || len(info.Features.RuntimeAPIs) != 0 || len(info.Features.NativeRequirements) != 0 {
		t.Fatalf("unreachable db call features = %+v, UsesDB = %v", info.Features, info.UsesDB)
	}
}

func TestProgramFeaturesReachableDatabaseCall(t *testing.T) {
	_, info, diags := checkProg(t, `
fn usesDatabase(): void { db.execute("SELECT 1"); }
fn main(): void { usesDatabase(); }
`)
	wantCodes(t, diags)
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
	if !info.UsesDB {
		t.Fatal("UsesDB should project reachable database use")
	}
}

func TestProgramFeaturesReachableTransaction(t *testing.T) {
	_, info, diags := checkProg(t, `
fn unreachable(): void { db.transaction((tx) => {}); }
fn main(): void { db.transaction((tx) => {}); }
`)
	wantCodes(t, diags)
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestProgramFeaturesGlobalInitializerIsRoot(t *testing.T) {
	_, info, diags := checkProg(t, `
fn initialize(): int64 { return db.execute("SELECT 1"); }
let initialized = initialize();
fn main(): void {}
`)
	wantCodes(t, diags)
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestProgramFeaturesReachableGuardsAndAfterHooks(t *testing.T) {
	_, info, diags := checkProg(t, `
fn guard(ctx: Context): bool { console.info("guard", JsonValue.string("checking")); return true; }
fn hook(ctx: Context): void { db.execute("SELECT 1"); }
@Use(guard) @After(hook)
fn route_dispatcher(ctx: Context): void {}
`)
	wantCodes(t, diags)
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIConsole, types.RuntimeAPIDatabase, types.RuntimeAPIHTTP},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestProgramFeaturesFollowMethodsAndConcreteGenericCallees(t *testing.T) {
	_, info, diags := checkProg(t, `
interface Worker { id: int64; }
fn (w: Worker) work(): void { genericOuter<int64>(w.id); }
fn genericOuter<T>(value: T): void { genericInner<T>(value); }
fn genericInner<T>(value: T): void { db.execute("SELECT 1"); }
fn main(): void { const worker: Worker = new Worker(); worker.work(); }
`)
	wantCodes(t, diags)
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestProgramFeaturesNoEntryUsesOnlyGlobalInitializerRoots(t *testing.T) {
	noFeatures, _ := check(t, `fn unused(): void { db.execute("SELECT 1"); }`)
	if noFeatures.Entry != nil || noFeatures.UsesDB || len(noFeatures.Features.RuntimeAPIs) != 0 {
		t.Fatalf("no-entry features = %+v, UsesDB = %v, entry = %v", noFeatures.Features, noFeatures.UsesDB, noFeatures.Entry)
	}

	globalFeatures, _ := check(t, `
fn initialize(): int64 { return db.execute("SELECT 1"); }
let initialized = initialize();
`)
	if globalFeatures.Entry != nil {
		t.Fatalf("Entry = %v, want nil", globalFeatures.Entry)
	}
	assertProgramFeatures(t, globalFeatures,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestProgramFeaturesDeterministic(t *testing.T) {
	src := `
fn route_dispatcher(ctx: Context): void {
	console.info("ready", JsonValue.string("ready"));
	db.execute("SELECT 1");
	ctx.text(200, "ok");
}
`
	var first *types.ProgramFeatures
	for i := 0; i < 5; i++ {
		_, info, diags := checkProg(t, src)
		wantCodes(t, diags)
		if first == nil {
			copy := info.Features
			first = &copy
			continue
		}
		if !reflect.DeepEqual(info.Features, *first) {
			t.Fatalf("feature order changed: got %+v, want %+v", info.Features, *first)
		}
	}
	assertProgramFeatures(t, &types.Info{Features: *first},
		[]types.RuntimeAPI{types.RuntimeAPIConsole, types.RuntimeAPIDatabase, types.RuntimeAPIHTTP},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
}

func TestCheckProgramFeatures(t *testing.T) {
	info, codes := checkProgram(t, map[string]string{
		"main.ts": `import { db } from "tlang/db";
fn query(): void { db.execute("SELECT 1"); }
fn main(): void { query(); }
`,
	}, "main.ts")
	if len(codes) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes)
	}
	assertProgramFeatures(t, info,
		[]types.RuntimeAPI{types.RuntimeAPIDatabase},
		[]types.NativeRequirement{types.NativeRequirementLibPQ},
	)
	if !info.UsesDB {
		t.Fatal("UsesDB should project reachable database use in CheckProgram")
	}
}

func assertProgramFeatures(t *testing.T, info *types.Info, wantRuntime []types.RuntimeAPI, wantNative []types.NativeRequirement) {
	t.Helper()
	if !reflect.DeepEqual(info.Features.RuntimeAPIs, wantRuntime) {
		t.Errorf("RuntimeAPIs = %v, want %v", info.Features.RuntimeAPIs, wantRuntime)
	}
	if !reflect.DeepEqual(info.Features.NativeRequirements, wantNative) {
		t.Errorf("NativeRequirements = %v, want %v", info.Features.NativeRequirements, wantNative)
	}
}
