package checker

import (
	"slices"

	"tlang/ast"
	"tlang/types"
)

// analyzeProgramFeatures derives runtime and native requirements from the
// functions reachable from the selected entry and calls made by global
// initializers. It runs after entry selection and generic closure so Calls
// can be resolved to concrete generic instances.
func (c *checker) analyzeProgramFeatures() {
	features := featureCollector{
		info:        c.info,
		runtimeAPIs: map[types.RuntimeAPI]bool{},
		native:      map[types.NativeRequirement]bool{},
		seenFuncs:   map[*types.Func]bool{},
	}

	if c.info.Entry != nil && c.info.Kind == types.ProgramServer {
		features.runtimeAPIs[types.RuntimeAPIHTTP] = true
	}
	features.visitFunc(c.info.Entry)

	for _, global := range c.info.Globals {
		let, ok := global.Decl.(*ast.LetStatement)
		if ok && let.Value != nil {
			features.visitNode(let.Value, nil)
		}
	}

	c.info.Features = features.result()
	c.info.UsesDB = slices.Contains(c.info.Features.RuntimeAPIs, types.RuntimeAPIDatabase)
}

type featureCollector struct {
	info        *types.Info
	runtimeAPIs map[types.RuntimeAPI]bool
	native      map[types.NativeRequirement]bool
	seenFuncs   map[*types.Func]bool
}

func (f *featureCollector) visitFunc(fn *types.Func) {
	if fn == nil || f.seenFuncs[fn] {
		return
	}
	f.seenFuncs[fn] = true

	origin := fn
	if fn.Origin != nil {
		origin = fn.Origin
	}
	if origin.Decl != nil && origin.Decl.Body != nil {
		f.visitNode(origin.Decl.Body, fn)
	}
	for _, guard := range fn.Guards {
		f.visitFunc(f.concreteFunc(guard, fn))
	}
	for _, hook := range fn.AfterHooks {
		f.visitFunc(f.concreteFunc(hook, fn))
	}
}

func (f *featureCollector) visitNode(root ast.Node, inst *types.Func) {
	if root == nil {
		return
	}
	ast.Inspect(root, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpression:
			call := f.info.Calls[node]
			if call == nil {
				break
			}
			if call.Kind == types.CallBuiltin {
				f.addBuiltin(call.Builtin)
			}
			if call.Func != nil {
				f.visitFunc(f.concreteFunc(call.Func, inst))
			}
		case *ast.MemberExpression:
			selection := f.info.Selections[node]
			if selection != nil && selection.Kind == types.SelBuiltin && !selection.Builtin.Info().Method {
				f.addBuiltin(selection.Builtin)
			}
		case *ast.TransactionStatement:
			if id, ok := node.Receiver.(*ast.Identifier); ok {
				builtin, ok := f.info.Uses[id].(*types.Builtin)
				if ok && builtin.ID == types.BuiltinDB {
					f.addBuiltin(types.BuiltinDB)
				}
			}
		}
		return true
	})
}

func (f *featureCollector) concreteFunc(callee, inst *types.Func) *types.Func {
	if callee == nil || inst == nil || callee.Origin == nil || inst.Origin == nil {
		return callee
	}
	return f.info.ConcreteFunc(callee, inst)
}

func (f *featureCollector) addBuiltin(id types.BuiltinID) {
	if id.IsDB() {
		f.runtimeAPIs[types.RuntimeAPIDatabase] = true
		f.native[types.NativeRequirementLibPQ] = true
		return
	}
	switch id {
	case types.BuiltinCtxMethod, types.BuiltinCtxPath, types.BuiltinCtxRawQuery,
		types.BuiltinCtxBody, types.BuiltinCtxHeader, types.BuiltinCtxQuery,
		types.BuiltinCtxParam, types.BuiltinCtxParamInt, types.BuiltinCtxMatch,
		types.BuiltinCtxBindJSON, types.BuiltinCtxText, types.BuiltinCtxJSON,
		types.BuiltinCtxSetHeader:
		f.runtimeAPIs[types.RuntimeAPIHTTP] = true
	case types.BuiltinConsoleErrorFields, types.BuiltinConsoleInfoFields,
		types.BuiltinConsoleDebugFields, types.BuiltinConsoleWarnFields:
		f.runtimeAPIs[types.RuntimeAPIConsole] = true
	case types.BuiltinEnvGet:
		f.runtimeAPIs[types.RuntimeAPIEnvironment] = true
	}
}

func (f *featureCollector) result() types.ProgramFeatures {
	features := types.ProgramFeatures{
		RuntimeAPIs:        []types.RuntimeAPI{},
		NativeRequirements: []types.NativeRequirement{},
	}
	for api := range f.runtimeAPIs {
		features.RuntimeAPIs = append(features.RuntimeAPIs, api)
	}
	for requirement := range f.native {
		features.NativeRequirements = append(features.NativeRequirements, requirement)
	}
	slices.Sort(features.RuntimeAPIs)
	slices.Sort(features.NativeRequirements)
	return features
}
