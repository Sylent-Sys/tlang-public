//go:build e2e

// Multi-file e2e scenario (FEAT-004 / AC-31, AC-33): a module-split server —
// a root route_dispatcher that imports its data types through a namespace
// module and a handler through a re-export module, dispatching to Context
// receiver methods declared in a handler module that itself named-imports an
// auth guard — built on every compiler leg (clang-release, clang-asan-ubsan,
// tcc) and driven over real HTTP exactly like the single-file section-13
// server. It proves the whole multi-file front end produces a single native
// binary whose runtime behavior matches the single-file echo server, and that
// the sanitizer leg finds no memory/UB error in the merged translation unit.
// Behind //go:build e2e so it runs only in the tlang-dev container.

package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tlang/driver"
	"tlang/runtime"
)

// modulesAppFixture is the multi-file e2e root module (its imports reach
// tests/e2e/modules/*.ts). It is resolved relative to tests/e2e like the
// single-file fixtures.
const modulesAppFixture = "modules_app.ts"

// buildBinaryGraph builds the multi-file program rooted at rootTS to a native
// executable at outBin for the release and tcc legs, mirroring buildBinary but
// routing through the multi-file front end (frontEndGraph).
func buildBinaryGraph(t *testing.T, ctx context.Context, rootTS string, kind driver.CompilerKind, mode driver.BuildMode, outBin string) error {
	t.Helper()
	info, cbytes := frontEndGraph(t, rootTS)
	programC := writeProgramC(t, filepath.Dir(outBin), rootTS, cbytes)

	d := driver.New(driver.Options{})
	plan, err := d.Plan(info, mode, kind, programC, outBin)
	if err != nil {
		return fmt.Errorf("driver.Plan: %w", err)
	}
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	if err := plan.Run(bctx); err != nil {
		return fmt.Errorf("build %s (%s): %w", filepath.Base(rootTS), kind, err)
	}
	return nil
}

// buildBinarySanGraph builds the multi-file program rooted at rootTS with clang
// ASan + UBSan, mirroring buildBinarySan but routing through frontEndGraph. It
// is the leg that runs the merged translation unit under the sanitizer, so a
// memory/UB bug introduced by the module merge would surface here.
func buildBinarySanGraph(t *testing.T, ctx context.Context, rootTS, outBin string) error {
	t.Helper()
	info, cbytes := frontEndGraph(t, rootTS)
	programC := writeProgramC(t, filepath.Dir(outBin), rootTS, cbytes)

	d := driver.New(driver.Options{})
	c, ok := d.Toolchain.Lookup(driver.CompilerClang)
	if !ok {
		return fmt.Errorf("buildBinarySanGraph: clang not found on PATH")
	}
	if err := d.Cache.Extract(); err != nil {
		return fmt.Errorf("buildBinarySanGraph: extract runtime: %w", err)
	}
	srcRoot := d.Cache.SourceRoot()
	flags := driver.AssembleFlags(srcRoot, driver.CompilerClang, driver.BuildDebug, info.UsesDB, d.PQ)

	sanCompile := []string{
		"-fsanitize=address,undefined",
		"-fno-sanitize-recover=all",
		"-fno-omit-frame-pointer",
		"-g",
		"-O1",
	}
	sanLink := []string{"-fsanitize=address,undefined"}

	argv := []string{}
	argv = append(argv, flags.Compile...)
	argv = append(argv, sanCompile...)
	for _, s := range runtime.CSources() {
		argv = append(argv, filepath.Join(srcRoot, filepath.FromSlash(s)))
	}
	argv = append(argv, programC)
	argv = append(argv, "-o", outBin)
	argv = append(argv, flags.Link...)
	argv = append(argv, sanLink...)

	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(bctx, c.Path, argv...)
	cmd.Dir = srcRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s (clang-asan-ubsan): %w\n%s", filepath.Base(rootTS), err, out)
	}
	return nil
}

// graphBinCache caches one multi-file build per (fixture, leg) so the three
// scenarios share three binaries rather than rebuilding per scenario.
var (
	graphBinCacheMu sync.Mutex
	graphBinCache   = map[binKey]*cachedBin{}
)

// buildFixtureGraph builds the multi-file fixture rooted at rootTS on leg lg
// (once, cached) and returns the executable path. It mirrors buildFixture but
// dispatches to the graph build functions by leg name.
func buildFixtureGraph(t *testing.T, ctx context.Context, lg leg, rootTS string) (string, error) {
	t.Helper()
	if err := ensureRuntimeExtracted(); err != nil {
		return "", fmt.Errorf("extract runtime: %w", err)
	}
	key := binKey{fixture: rootTS, leg: lg.name, usesDB: false}
	graphBinCacheMu.Lock()
	cb := graphBinCache[key]
	if cb == nil {
		cb = &cachedBin{}
		graphBinCache[key] = cb
	}
	graphBinCacheMu.Unlock()

	cb.once.Do(func() {
		dir, err := os.MkdirTemp("", "tlang-e2e-mod-")
		if err != nil {
			cb.err = fmt.Errorf("create build dir: %w", err)
			return
		}
		base := strings.TrimSuffix(filepath.Base(rootTS), filepath.Ext(rootTS))
		out := filepath.Join(dir, base+"-"+lg.name)
		switch lg.name {
		case legClangRelease:
			cb.err = buildBinaryGraph(t, ctx, rootTS, driver.CompilerClang, driver.BuildRelease, out)
		case legClangASanUBSan:
			cb.err = buildBinarySanGraph(t, ctx, rootTS, out)
		case legTCC:
			cb.err = buildBinaryGraph(t, ctx, rootTS, driver.CompilerTCC, driver.BuildDebug, out)
		default:
			cb.err = fmt.Errorf("unknown leg %q", lg.name)
		}
		if cb.err == nil {
			cb.path = out
		}
	})
	return cb.path, cb.err
}

// withMultiServer builds the multi-file fixture on leg lg (cached), launches
// it, and invokes fn with a ready HTTP driver, handling startup and idempotent
// shutdown. The fixture is DB-free.
func withMultiServer(t *testing.T, lg leg, fn func(t *testing.T, d *httpDriver, h *serverHandle)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout+startTimeout+shutdownTimeout+requestTimeout)
	defer cancel()

	root := e2eFixturePath(t, modulesAppFixture)
	bin, err := buildFixtureGraph(t, ctx, lg, root)
	if err != nil {
		t.Fatalf("build %s on %s: %v", modulesAppFixture, lg.name, err)
	}

	h := startServer(t, ctx, bin, nil, lg.sanitizer)
	defer h.stop()

	d := newHTTPDriver(h, 2)
	fn(t, d, h)
}

// TestE2E_Modules_Echo_201 drives the module-split server's POST /echo on every
// leg: auth + valid JSON into the handler (a decorated Context method that
// named-imports its guard and namespace-imports its types) returns 201 echoing
// id/name. This is the central AC-31/AC-33 assertion — the merged multi-file
// program behaves exactly like the single-file echo server.
func TestE2E_Modules_Echo_201(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withMultiServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			const wantID = int64(9001)
			const wantName = "modular"
			res, err := d.postJSON(ctx, "/echo", nil, []byte(`{"id":9001,"name":"modular"}`))
			if err != nil {
				t.Fatalf("POST /echo: %v", err)
			}
			if res.status != 201 {
				t.Fatalf("status = %d, want 201 (body %q)", res.status, res.body)
			}
			var got struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(res.body, &got); err != nil {
				t.Fatalf("decode response %q: %v", res.body, err)
			}
			if got.ID != wantID || got.Name != wantName {
				t.Fatalf("response = {id:%d name:%q}, want {id:%d name:%q}", got.ID, got.Name, wantID, wantName)
			}
		})
	})
}

// TestE2E_Modules_AuthGuard_401 drives POST /echo with no Authorization on
// every leg: the guard imported by name from guards.ts rejects it with 401,
// proving a named cross-module import drives real request behavior.
func TestE2E_Modules_AuthGuard_401(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withMultiServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			res, err := d.postJSON(ctx, "/echo", map[string]string{authHeaderKey: ""}, []byte(`{"id":1,"name":"x"}`))
			if err != nil {
				t.Fatalf("POST /echo: %v", err)
			}
			if res.status != 401 {
				t.Fatalf("status = %d, want 401", res.status)
			}
			if got := string(res.body); got != "Unauthorized" {
				t.Fatalf("body = %q, want %q", got, "Unauthorized")
			}
		})
	})
}

// TestE2E_Modules_NotFound_404 drives GET /nope on every leg: the dispatcher
// falls through to notFound, which the root imported through the re-export
// module routes.ts -> handlers.ts. The 404 body proves the re-export leg is
// wired end to end.
func TestE2E_Modules_NotFound_404(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withMultiServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			res, err := d.get(ctx, "/nope")
			if err != nil {
				t.Fatalf("GET /nope: %v", err)
			}
			if res.status != 404 {
				t.Fatalf("status = %d, want 404", res.status)
			}
			if got := string(res.body); got != "Endpoint Not Found" {
				t.Fatalf("body = %q, want %q", got, "Endpoint Not Found")
			}
		})
	})
}
