//go:build e2e

// E2E harness (tests-suites design §4): build real native binaries from TLang
// fixtures on three compiler legs (clang-release, clang-asan-ubsan, tcc),
// launch each as a child process on an ephemeral port, and drive it over
// net/http. All of this is behind //go:build e2e so the default host build
// stays toolchain-free; it runs only in the tlang-dev container.
//
// Nothing here modifies a locked package: binaries are produced through the
// front-end helpers (parser/checker/codegen) and the locked driver (Plan/Run,
// AssembleFlags, the runtime cache), exactly as cmd/tlang/build_run.go does.
// The sanitizer leg mirrors driver.LinkCheckFloatMod with the SAN=1 flag set
// appended, the only way to get sanitizers without touching locked code.

package tests

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tlang/ast"
	"tlang/checker"
	"tlang/codegen"
	"tlang/driver"
	"tlang/module"
	"tlang/parser"
	"tlang/runtime"
	"tlang/types"
)

// Timeout constants (design §4.9). clang+runtime+sanitizer compiles are the
// slowest, so the build timeout is generous.
const (
	buildTimeout    = 120 * time.Second
	startTimeout    = 15 * time.Second
	requestTimeout  = 5 * time.Second
	shutdownTimeout = 10 * time.Second
)

// Leg names (design §4.1). Exposed as constants so scenarios and later features
// name the same legs.
const (
	legClangRelease   = "clang-release"
	legClangASanUBSan = "clang-asan-ubsan"
	legTCC            = "tcc"
)

// leg is one compiler configuration. build produces a native executable at
// outBin from the TLang source srcTS, or returns an error with the compiler
// diagnostics. sanitizer reports whether the leg runs with ASan/UBSan (its
// child needs the sanitizer env of §4.4 and the LSan interpretation of §4.3).
type leg struct {
	name      string
	sanitizer bool
	build     func(t *testing.T, ctx context.Context, srcTS, outBin string) error
}

// legs is the three-leg table (design §4.1). clang-release and tcc go through
// buildBinary (the direct front-end+driver pipeline); clang-asan-ubsan goes
// through buildBinarySan (direct clang mirroring driver.LinkCheckFloatMod).
var legs = []leg{
	{
		name: legClangRelease,
		build: func(t *testing.T, ctx context.Context, srcTS, outBin string) error {
			return buildBinary(t, ctx, srcTS, driver.CompilerClang, driver.BuildRelease, outBin)
		},
	},
	{
		name:      legClangASanUBSan,
		sanitizer: true,
		build: func(t *testing.T, ctx context.Context, srcTS, outBin string) error {
			return buildBinarySan(t, ctx, srcTS, outBin)
		},
	},
	{
		name: legTCC,
		build: func(t *testing.T, ctx context.Context, srcTS, outBin string) error {
			return buildBinary(t, ctx, srcTS, driver.CompilerTCC, driver.BuildDebug, outBin)
		},
	},
}

// extractOnce guards the single runtime extract performed before any parallel
// leg build (design §4.9: the cache is content-addressed and Extract is
// idempotent, but extracting once up front avoids a build race).
var extractOnce sync.Once
var extractErr error

// ensureRuntimeExtracted extracts the embedded runtime into the shared,
// content-addressed cache exactly once.
func ensureRuntimeExtracted() error {
	extractOnce.Do(func() {
		cache := driver.NewRuntimeCache(driver.DefaultCacheRoot())
		extractErr = cache.Extract()
	})
	return extractErr
}

// frontEnd runs parser.ParseSource -> checker.Check on srcTS, failing the test
// on any front-end error, and returns the checked program's type info plus the
// emitted C bytes. It mirrors cmd/tlang/build_run.go's load+parse+check+emit.
func frontEnd(t *testing.T, srcTS string) (info *types.Info, cbytes []byte) {
	t.Helper()
	src, err := os.ReadFile(srcTS)
	if err != nil {
		t.Fatalf("read %s: %v", srcTS, err)
	}
	prog, pdiags := parser.ParseSource(filepath.Base(srcTS), src)
	if pdiags.HasErrors() {
		t.Fatalf("%s: parser errors:\n%s", srcTS, pdiags.Error())
	}
	i, cdiags := checker.Check(prog)
	if cdiags.HasErrors() {
		t.Fatalf("%s: checker errors:\n%s", srcTS, cdiags.Error())
	}
	out, eerr := codegen.Emit(prog, i)
	if eerr != nil {
		t.Fatalf("%s: codegen.Emit error: %v", srcTS, eerr)
	}
	return i, out
}

// frontEndGraph runs the multi-file front end on the root module at rootTS —
// module.Build -> checker.CheckProgram -> the merged declaration program ->
// codegen.Emit — exactly as cmd/tlang's runFrontendGraph does, failing the
// test on any build/check/emit error. It is the multi-file analogue of
// frontEnd (FEAT-004 / AC-31, AC-33).
func frontEndGraph(t *testing.T, rootTS string) (info *types.Info, cbytes []byte) {
	t.Helper()
	graph, bdiags := module.Build(rootTS)
	if bdiags.HasErrors() {
		t.Fatalf("%s: module.Build errors:\n%s", rootTS, bdiags.Error())
	}
	i, cdiags := checker.CheckProgram(graph.Modules)
	if cdiags.HasErrors() {
		t.Fatalf("%s: checker errors:\n%s", rootTS, cdiags.Error())
	}
	merged := &ast.Program{}
	if graph.Root != nil && graph.Root.Prog != nil {
		merged.File = graph.Root.Prog.File
		merged.EOF = graph.Root.Prog.EOF
	}
	for _, mod := range graph.Modules {
		for _, s := range mod.Prog.Statements {
			switch s.(type) {
			case *ast.ImportDecl, *ast.ReExportDecl:
			default:
				merged.Statements = append(merged.Statements, s)
			}
		}
	}
	out, eerr := codegen.Emit(merged, i)
	if eerr != nil {
		t.Fatalf("%s: codegen.Emit error: %v", rootTS, eerr)
	}
	return i, out
}

// writeProgramC writes the emitted C translation unit to <dir>/<base>.c and
// returns its path.
func writeProgramC(t *testing.T, dir, srcTS string, cbytes []byte) string {
	t.Helper()
	base := strings.TrimSuffix(filepath.Base(srcTS), filepath.Ext(srcTS))
	if base == "" {
		base = "program"
	}
	programC := filepath.Join(dir, base+".c")
	if err := os.WriteFile(programC, cbytes, 0o644); err != nil {
		t.Fatalf("write %s: %v", programC, err)
	}
	return programC
}

// buildBinary builds srcTS to a native executable at outBin for the release and
// tcc legs, mirroring cmd/tlang/build_run.go: ParseSource -> Check -> Emit ->
// write temp .c -> driver.New(Options{}).Plan(info, mode, kind, programC,
// outBin) -> plan.Run(ctx) under a bounded context. For tcc, outBin is
// non-empty so the driver selects the archive/build form (a real artifact to
// exec), not the -run form.
func buildBinary(t *testing.T, ctx context.Context, srcTS string, kind driver.CompilerKind, mode driver.BuildMode, outBin string) error {
	t.Helper()
	info, cbytes := frontEnd(t, srcTS)
	programC := writeProgramC(t, filepath.Dir(outBin), srcTS, cbytes)

	d := driver.New(driver.Options{})
	plan, err := d.Plan(info, mode, kind, programC, outBin)
	if err != nil {
		return fmt.Errorf("driver.Plan: %w", err)
	}

	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	if err := plan.Run(bctx); err != nil {
		return fmt.Errorf("build %s (%s): %w", filepath.Base(srcTS), kind, err)
	}
	return nil
}

// buildBinarySan builds srcTS to a native executable at outBin with clang ASan
// + UBSan, via a direct clang invocation mirroring driver.LinkCheckFloatMod
// (the CLI/driver cannot emit -fsanitize). It reuses AssembleFlags for the base
// compile/link flags and the runtime source set, appending the SAN=1 flag set
// (design §4.3). info.UsesDB flows through AssembleFlags, so a DB fixture links
// -lpq and the DB-free echo_server yields -DTLANG_NO_PG, matching the other
// legs.
func buildBinarySan(t *testing.T, ctx context.Context, srcTS, outBin string) error {
	t.Helper()
	info, cbytes := frontEnd(t, srcTS)
	programC := writeProgramC(t, filepath.Dir(outBin), srcTS, cbytes)

	d := driver.New(driver.Options{})
	c, ok := d.Toolchain.Lookup(driver.CompilerClang)
	if !ok {
		return fmt.Errorf("buildBinarySan: clang not found on PATH")
	}
	if err := d.Cache.Extract(); err != nil {
		return fmt.Errorf("buildBinarySan: extract runtime: %w", err)
	}
	srcRoot := d.Cache.SourceRoot()
	flags := driver.AssembleFlags(srcRoot, driver.CompilerClang, driver.BuildDebug, info.UsesDB, d.PQ)

	// Sanitizer flags match runtime/Makefile SAN=1. -O1 and -g are appended
	// after flags.Compile so they win over BuildDebug's -O2 -g (clang honors
	// the last optimization flag).
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
		return fmt.Errorf("build %s (clang-asan-ubsan): %w\n%s", filepath.Base(srcTS), err, out)
	}
	return nil
}

// binCache caches one built binary per (fixture, leg, dbUsage) so the non-DB
// scenarios share three binaries rather than rebuilding per scenario (design
// §4.9). Keyed by fixture path + leg name + a uses-DB marker.
type binKey struct {
	fixture string
	leg     string
	usesDB  bool
}

var (
	binCacheMu sync.Mutex
	binCache   = map[binKey]*cachedBin{}
)

// cachedBin is one build result, guarded so concurrent legs block on a single
// build rather than racing. Each entry has its own build dir (not a t.TempDir,
// since it outlives any single test) so the binary persists for the suite.
type cachedBin struct {
	once sync.Once
	path string
	err  error
}

// buildFixture builds srcTS on leg lg (once, cached) and returns the executable
// path. usesDB keys the cache so a DB and non-DB build of the same fixture do
// not collide. The build runs under the given context with a bounded timeout
// inside buildBinary/buildBinarySan.
func buildFixture(t *testing.T, ctx context.Context, lg leg, srcTS string, usesDB bool) (string, error) {
	t.Helper()
	if err := ensureRuntimeExtracted(); err != nil {
		return "", fmt.Errorf("extract runtime: %w", err)
	}

	key := binKey{fixture: srcTS, leg: lg.name, usesDB: usesDB}
	binCacheMu.Lock()
	cb := binCache[key]
	if cb == nil {
		cb = &cachedBin{}
		binCache[key] = cb
	}
	binCacheMu.Unlock()

	cb.once.Do(func() {
		dir, err := os.MkdirTemp("", "tlang-e2e-")
		if err != nil {
			cb.err = fmt.Errorf("create build dir: %w", err)
			return
		}
		base := strings.TrimSuffix(filepath.Base(srcTS), filepath.Ext(srcTS))
		out := filepath.Join(dir, base+"-"+lg.name)
		if lg.sanitizer && os.Getenv("TLANG_KEEP_ASAN_BINARIES") == "1" {
			diagDir := os.Getenv("TLANG_ASAN_ARTIFACT_DIR")
			if diagDir == "" {
				diagDir = filepath.Join("/src", ".asan-diagnostics")
			}
			if err := os.MkdirAll(filepath.Join(diagDir, "binaries"), 0o755); err != nil {
				cb.err = fmt.Errorf("create ASan binary directory: %w", err)
				return
			}
			if goruntime.GOOS == "windows" {
				cb.err = fmt.Errorf("ASan binary retention expects the Linux e2e runner")
				return
			}
			out = filepath.Join(diagDir, "binaries", base+"-"+lg.name)
		}
		if err := lg.build(t, ctx, srcTS, out); err != nil {
			cb.err = err
			return
		}
		cb.path = out
	})
	return cb.path, cb.err
}

// e2eFixturePath resolves a fixture under tests/e2e/ to an absolute path (the
// front end and driver take real paths).
func e2eFixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("e2e", name))
	if err != nil {
		t.Fatalf("resolve fixture %s: %v", name, err)
	}
	return p
}

// ringSize bounds the stderr ring buffer kept for failure diagnostics.
const ringSize = 256

// serverHandle is a launched child server. It owns the child process, the
// parsed listen address, the single cmd.Wait() result channel, and a ring
// buffer of recent stderr lines for failure diagnostics.
type serverHandle struct {
	t    *testing.T
	cmd  *exec.Cmd
	addr string // "127.0.0.1:<port>"
	port int

	sanitizer bool
	asanDir   string // per-child ASan log_path directory (sanitizer leg only)

	waitResult chan error // the single cmd.Wait() result (buffered, size 1)

	stderrMu  sync.Mutex
	stderrBuf []string // ring buffer of recent stderr lines

	stopOnce sync.Once
	stopped  bool
	reaped   bool // the waitResult value has been received: the child is gone

	asanLogOnce sync.Once
}

// recordStderr appends a line to the bounded ring buffer.
func (h *serverHandle) recordStderr(line string) {
	h.stderrMu.Lock()
	defer h.stderrMu.Unlock()
	if len(h.stderrBuf) == ringSize {
		copy(h.stderrBuf, h.stderrBuf[1:])
		h.stderrBuf[len(h.stderrBuf)-1] = line
	} else {
		h.stderrBuf = append(h.stderrBuf, line)
	}
}

// stderrSnapshot returns a copy of the current stderr ring buffer joined by
// newlines, for failure diagnostics.
func (h *serverHandle) stderrSnapshot() string {
	h.stderrMu.Lock()
	defer h.stderrMu.Unlock()
	return strings.Join(h.stderrBuf, "\n")
}

// stderrContains reports whether any recorded stderr line contains sub.
func (h *serverHandle) stderrContains(sub string) bool {
	h.stderrMu.Lock()
	defer h.stderrMu.Unlock()
	for _, l := range h.stderrBuf {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// waitForStderr polls the stderr ring buffer for a line containing sub, up to a
// short bounded deadline. The runtime logs the request-error line asynchronously
// relative to the HTTP response, so a brief poll avoids a flaky race.
func waitForStderr(h *serverHandle, sub string) bool {
	deadline := time.Now().Add(requestTimeout)
	for {
		if h.stderrContains(sub) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sanitizerEnv is the child environment the sanitizer leg adds (design §4.4):
// any diagnostic aborts with a non-zero status, UBSan prints a stack trace, and
// LSan leak detection is on (interpreted per §4.3's first-SIGINT-graceful rule).
// ASan/LSan reports go to <logDir>/asan.<pid> (log_path), not stderr, so CI can
// upload them. log_path is a sanitizer_common flag, so in the combined runtime
// UBSan reports most likely land there too; checkSanitizerLogs reports any
// non-empty file, and checkExit's stderr scan stays as a backstop.
func sanitizerEnv(logDir string) []string {
	return []string{
		"ASAN_OPTIONS=abort_on_error=1:detect_leaks=1:symbolize=1:fast_unwind_on_fatal=0:malloc_context_size=50:log_path=" + filepath.Join(logDir, "asan"),
		"ASAN_SYMBOLIZER_PATH=/usr/bin/llvm-symbolizer",
		"UBSAN_OPTIONS=halt_on_error=1:print_stacktrace=1",
	}
}

// defaultASanLogRoot is where ASan log directories live unless
// TLANG_ASAN_LOG_DIR overrides it. CI and scripts/dev.{ps1,sh} mount
// <repo>/.asan-diagnostics here, and CI uploads every asan.* file under it.
const defaultASanLogRoot = "/tmp/tlang-asan"

// asanLogCap bounds how much of one ASan log is copied into a test error.
const asanLogCap = 64 * 1024

// asanLogRoot returns the root under which per-child ASan log directories are
// created.
func asanLogRoot() string {
	if root := os.Getenv("TLANG_ASAN_LOG_DIR"); root != "" {
		return root
	}
	return defaultASanLogRoot
}

// safeTestName turns a test name (which may contain '/', spaces and other
// subtest characters) into a short filesystem-safe directory-name prefix.
func safeTestName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "._")
	if len(s) > 100 {
		s = s[:100]
	}
	if s == "" {
		s = "test"
	}
	return s
}

// newASanLogDir creates a fresh per-child log directory under asanLogRoot(),
// so one child's report is never attributed to another (parallel legs and
// scenarios share the root).
func newASanLogDir(t *testing.T) (string, error) {
	root := asanLogRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create ASan log root %s: %w", root, err)
	}
	dir, err := os.MkdirTemp(root, safeTestName(t.Name())+"-")
	if err != nil {
		return "", fmt.Errorf("create ASan log directory under %s: %w", root, err)
	}
	return dir, nil
}

// sanitizerLog is one non-empty file found in an ASan log directory.
type sanitizerLog struct {
	path      string
	content   []byte // at most asanLogCap bytes
	truncated bool
}

// scanSanitizerLogs returns every non-empty regular file in dir. ASan names its
// reports asan.<pid>, but any file is reported so nothing is silently missed. A
// zero-byte file (ASan may create one without writing a report) is clean.
func scanSanitizerLogs(dir string) ([]sanitizerLog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []sanitizerLog
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			return found, err
		}
		data, err := io.ReadAll(io.LimitReader(f, asanLogCap+1))
		f.Close()
		if err != nil {
			return found, fmt.Errorf("read %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}
		l := sanitizerLog{path: path, content: data}
		if len(data) > asanLogCap {
			l.content = data[:asanLogCap]
			l.truncated = true
		}
		found = append(found, l)
	}
	return found, nil
}

// checkSanitizerLogs fails the test for every non-empty ASan log the child
// wrote (design §4.4). With log_path set, ASan/LSan reports never reach stderr,
// so checkExit's stderr scan cannot see them; without this file check a real
// report would leave the run green. It must run only after the child has been
// reaped (the waitResult value received), since ASan writes the report while
// exiting; it runs at most once. The directory is kept when anything was found
// (CI uploads it) and removed when clean.
func (h *serverHandle) checkSanitizerLogs() {
	h.t.Helper()
	if !h.sanitizer || h.asanDir == "" {
		return
	}
	h.asanLogOnce.Do(func() {
		found, err := scanSanitizerLogs(h.asanDir)
		for _, l := range found {
			note := ""
			if l.truncated {
				note = fmt.Sprintf("\n... (truncated at %d bytes; full log kept at %s)", asanLogCap, l.path)
			}
			h.t.Errorf("sanitizer diagnostic in %s\n%s%s\n--- server stderr ---\n%s", l.path, l.content, note, h.stderrSnapshot())
		}
		if err != nil {
			h.t.Errorf("scan ASan log directory %s: %v", h.asanDir, err)
			return
		}
		if len(found) == 0 {
			_ = os.RemoveAll(h.asanDir)
		}
	})
}

// reap records a received waitResult value and runs the post-exit log check.
func (h *serverHandle) reap() {
	h.t.Helper()
	h.reaped = true
	h.checkSanitizerLogs()
}

// startServer launches bin as a child on an ephemeral port and returns a ready
// serverHandle (design §4.5). env carries any scenario-specific vars (e.g. a DB
// URL / pool knobs); the harness always sets TLANG_PORT=0, TLANG_HOST=127.0.0.1
// and TLANG_THREADS=1. sanitizer=true adds the sanitizer child env and selects
// the §4.3/§4.4 interpretation at shutdown.
//
// Readiness is the three-part condition of §4.5 for the DB-free fixture: the
// listening line is parsed AND the child is still alive AND a GET /healthz
// probe returns a response. A child that printed the line then exited (e.g. a
// bad DB URL) is reported as a startup failure, not left to hang.
func startServer(t *testing.T, ctx context.Context, bin string, env []string, sanitizer bool) *serverHandle {
	t.Helper()

	// Sanitizer leg: a fresh per-child ASan log directory. Failing to create it
	// fails the test rather than silently losing ASan detection.
	asanDir := ""
	if sanitizer {
		dir, err := newASanLogDir(t)
		if err != nil {
			t.Fatalf("startServer: %v", err)
		}
		asanDir = dir
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"TLANG_PORT=0",
		"TLANG_HOST=127.0.0.1",
		"TLANG_THREADS=1",
	)
	if sanitizer {
		cmd.Env = append(cmd.Env, sanitizerEnv(asanDir)...)
	}
	cmd.Env = append(cmd.Env, env...)

	// removeUnusedASanDir drops the log directory when no child ever ran.
	removeUnusedASanDir := func() {
		if asanDir != "" {
			_ = os.RemoveAll(asanDir)
		}
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		removeUnusedASanDir()
		t.Fatalf("stderr pipe: %v", err)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	h := &serverHandle{
		t:          t,
		cmd:        cmd,
		sanitizer:  sanitizer,
		asanDir:    asanDir,
		waitResult: make(chan error, 1),
	}

	if err := cmd.Start(); err != nil {
		removeUnusedASanDir()
		t.Fatalf("start server: %v", err)
	}

	// Backstop for every path that did not reap the child (a test that never
	// calls stop, or a forceKill that timed out): stop it, then check its ASan
	// logs once it is gone. Cleanups run after the test's deferred stop, so on
	// the normal path this finds the child already reaped and checked.
	t.Cleanup(h.finalize)

	// The single cmd.Wait() watcher goroutine (design §4.5 step 5 / §4.6): it is
	// the ONLY caller of Wait, publishing the result on waitResult. It must run
	// after the stderr pipe is fully drained, so it waits for the scanner.
	scannerDone := make(chan struct{})
	portCh := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		sentPort := false
		for scanner.Scan() {
			line := scanner.Text()
			h.recordStderr(line)
			if !sentPort {
				if _, port, ok := parseListening(line); ok {
					sentPort = true
					portCh <- port
				}
			}
		}
		close(scannerDone)
	}()
	go func() {
		<-scannerDone
		h.waitResult <- cmd.Wait()
	}()

	// Readiness (design §4.5 step 6), bounded by startTimeout.
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()

	var port int
	select {
	case port = <-portCh:
		h.port = port
		h.addr = fmt.Sprintf("127.0.0.1:%d", port)
	case werr := <-h.waitResult:
		// Child exited before printing the listening line: startup failure.
		// The ASan log check (via reap) runs before failStartup's Fatalf.
		h.stopped = true
		h.reap()
		h.failStartup(fmt.Sprintf("server exited before listening: %v", werr))
		return h
	case <-deadline.C:
		h.forceKill()
		h.failStartup("timed out waiting for the listening line")
		return h
	}

	// Three-part readiness: line parsed (above) AND child alive AND a GET
	// /healthz probe returns a response. Probe tolerates ECONNREFUSED on a
	// short bounded retry but NOT the child having exited.
	if ok := h.waitHealthy(deadline); !ok {
		return h
	}
	return h
}

// waitHealthy performs the §4.5 probe loop: retry GET /healthz until it returns
// a response, failing on child exit or the startTimeout deadline. It returns
// false (after failing the test) on failure.
func (h *serverHandle) waitHealthy(deadline *time.Timer) bool {
	client := &http.Client{Timeout: requestTimeout}
	url := "http://" + h.addr + "/healthz"
	for {
		// Fail fast if the child already exited (e.g. DB pool creation failed
		// after the listening line printed).
		select {
		case werr := <-h.waitResult:
			h.stopped = true
			h.reap()
			h.failStartup(fmt.Sprintf("server exited during readiness probe: %v", werr))
			return false
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}

		select {
		case werr := <-h.waitResult:
			h.stopped = true
			h.reap()
			h.failStartup(fmt.Sprintf("server exited during readiness probe: %v", werr))
			return false
		case <-deadline.C:
			h.forceKill()
			h.failStartup("timed out waiting for a healthy /healthz probe")
			return false
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// failStartup dumps diagnostics and fails the test for a startup failure.
func (h *serverHandle) failStartup(msg string) {
	h.t.Helper()
	h.t.Fatalf("startServer: %s\n--- server stderr ---\n%s", msg, h.stderrSnapshot())
}

// forceKill kills the child and drains its wait result (best-effort), used on a
// readiness timeout. It never calls Wait itself (the watcher goroutine owns it).
// The ASan logs are checked only if the child was actually reaped; a child
// still not gone after shutdownTimeout is reported instead (finalize retries).
func (h *serverHandle) forceKill() {
	h.t.Helper()
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	select {
	case <-h.waitResult:
		h.reap()
	case <-time.After(shutdownTimeout):
		h.t.Errorf("server not reaped %v after Kill; sanitizer logs not yet checked", shutdownTimeout)
	}
	h.stopped = true
}

// finalize is the t.Cleanup backstop registered by startServer: it stops a
// child the test never stopped, waits (bounded) for one a timed-out forceKill
// left unreaped, and then runs the post-exit ASan log check (once).
func (h *serverHandle) finalize() {
	h.t.Helper()
	if !h.reaped && !h.stopped {
		h.stop()
	}
	if !h.reaped {
		select {
		case <-h.waitResult:
			h.reap()
		case <-time.After(shutdownTimeout):
			if h.sanitizer {
				h.t.Errorf("server never reaped; sanitizer logs in %s not checked", h.asanDir)
			} else {
				h.t.Errorf("server never reaped")
			}
			return
		}
	}
	h.checkSanitizerLogs()
}

// stop shuts the server down gracefully and is idempotent (deferred by the test
// and possibly also called on a failure path): SIGINT -> SIGTERM -> Kill
// (design §4.6). It never calls cmd.Wait() itself; it reads the shared
// waitResult the watcher goroutine publishes. On a shutdown escalation past the
// first SIGINT it fails the test (a child ignoring the first signal is a bug).
// For the sanitizer leg a non-zero exit other than the expected SIGINT status
// is a failure, and the LSan interpretation of §4.3 applies: a leak report is
// meaningful only on the first-signal graceful path.
func (h *serverHandle) stop() {
	h.stopOnce.Do(func() {
		if h.stopped || h.cmd.Process == nil {
			return
		}

		// Already exited on its own?
		select {
		case werr := <-h.waitResult:
			h.reap()
			h.checkExit(werr, false)
			return
		default:
		}

		// First signal: SIGINT (graceful stop).
		_ = h.cmd.Process.Signal(syscall.SIGINT)
		select {
		case werr := <-h.waitResult:
			h.reap()
			h.checkExit(werr, true)
			return
		case <-time.After(shutdownTimeout):
		}

		// Escalate: SIGTERM.
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case werr := <-h.waitResult:
			h.reap()
			h.t.Errorf("server did not stop on first SIGINT (stopped after SIGTERM): %v\n--- server stderr ---\n%s", werr, h.stderrSnapshot())
			return
		case <-time.After(shutdownTimeout):
		}

		// Last resort: Kill.
		_ = h.cmd.Process.Kill()
		werr := <-h.waitResult
		h.reap()
		h.t.Errorf("server ignored SIGINT and SIGTERM, killed: %v\n--- server stderr ---\n%s", werr, h.stderrSnapshot())
	})
}

// checkExit validates the child's exit status at shutdown. graceful reports
// whether the exit followed the first SIGINT (the only path on which an LSan
// report is meaningful, §4.3). For the sanitizer leg, any ERROR:
// AddressSanitizer / "runtime error:" text in stderr is a failure, and a
// non-zero exit that is not the expected clean SIGINT status is a failure
// (§4.4). ASan reports themselves go to log files, checked by reap (which every
// caller runs first); the stderr scan still catches UBSan.
func (h *serverHandle) checkExit(werr error, graceful bool) {
	h.t.Helper()
	if h.sanitizer {
		if h.stderrContains("ERROR: AddressSanitizer") || h.stderrContains("runtime error:") {
			h.t.Errorf("sanitizer diagnostic in server stderr\n--- server stderr ---\n%s", h.stderrSnapshot())
			return
		}
	}
	if werr == nil {
		return // clean exit (drain completed before/at the signal)
	}
	// A clean graceful shutdown may surface as exit-by-SIGINT. Accept the
	// first-SIGINT signal path; anything else is a real failure.
	if graceful && isSignalExit(werr, syscall.SIGINT) {
		return
	}
	h.t.Errorf("server exited abnormally: %v\n--- server stderr ---\n%s", werr, h.stderrSnapshot())
}

// isSignalExit reports whether err is an *exec.ExitError whose process was
// terminated by signal sig.
func isSignalExit(err error, sig syscall.Signal) bool {
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return ws.Signaled() && ws.Signal() == sig
}

// httpDriver is a per-server HTTP client on a dedicated keep-alive transport
// (design §4.7). DisableKeepAlives is false and MaxIdleConnsPerHost is set so
// keep-alive reuse (scenario 7) is observable.
type httpDriver struct {
	base      string
	client    *http.Client
	transport *http.Transport
}

// newHTTPDriver builds a keep-alive HTTP driver for the server. concurrency
// sizes MaxIdleConnsPerHost so the benchmark's connection pool can be reused.
func newHTTPDriver(h *serverHandle, concurrency int) *httpDriver {
	if concurrency < 1 {
		concurrency = 1
	}
	tr := &http.Transport{
		DisableKeepAlives:   false,
		MaxIdleConns:        concurrency,
		MaxIdleConnsPerHost: concurrency,
	}
	return &httpDriver{
		base:      "http://" + h.addr,
		transport: tr,
		client:    &http.Client{Transport: tr, Timeout: requestTimeout},
	}
}

// httpResult is the outcome of one request: status, headers, body.
type httpResult struct {
	status int
	header http.Header
	body   []byte
}

// get issues GET path and returns the result.
func (d *httpDriver) get(ctx context.Context, path string) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return httpResult{}, err
	}
	return d.do(req)
}

// authHeaderKey is the header name whose presence/absence selects the auth
// guard outcome.
const authHeaderKey = "Authorization"

// defaultAuth is the non-empty Authorization value postJSON sends to guarded
// routes unless the caller overrides or clears it (design §4.7). Any non-empty
// value satisfies the guard; the guard only checks for emptiness.
const defaultAuth = "Bearer test"

// postJSON issues POST path with a JSON body. headers overrides defaults; it
// defaults to sending Authorization: Bearer test for guarded routes unless the
// caller sets Authorization (to another value) or clears it by mapping it to
// the empty string (which omits the header, exercising the 401 path).
func (d *httpDriver) postJSON(ctx context.Context, path string, headers map[string]string, body []byte) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+path, bytes.NewReader(body))
	if err != nil {
		return httpResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Default Authorization unless the caller explicitly provided one.
	if _, ok := headers[authHeaderKey]; !ok {
		req.Header.Set(authHeaderKey, defaultAuth)
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	return d.do(req)
}

// postRaw issues POST path with an arbitrary body and header set, applying no
// Authorization default (the caller controls every header).
func (d *httpDriver) postRaw(ctx context.Context, path string, headers map[string]string, body []byte) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+path, bytes.NewReader(body))
	if err != nil {
		return httpResult{}, err
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	return d.do(req)
}

// do executes req, fully reads and closes the body, and returns the result.
func (d *httpDriver) do(req *http.Request) (httpResult, error) {
	resp, err := d.client.Do(req)
	if err != nil {
		return httpResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpResult{}, err
	}
	return httpResult{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// dial opens a bare TCP connection to the server (used by the client-disconnect
// scenario and transport-level readiness).
func (d *httpDriver) dialAddr() string {
	return strings.TrimPrefix(d.base, "http://")
}

// dialServer opens a raw TCP connection to addr, for scenarios that drive the
// socket directly.
func dialServer(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, requestTimeout)
}
