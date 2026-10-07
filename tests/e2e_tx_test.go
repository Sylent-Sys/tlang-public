//go:build e2e

// E2E non-DB scenarios (tests-suites design §4.8 scenarios 1-7), each run on
// every compiler leg (clang-release, clang-asan-ubsan, tcc) against the DB-free
// tests/e2e/echo_server.tl fixture. Each builds the fixture once per leg
// (cached), launches a child on an ephemeral port, drives it over net/http, and
// asserts real HTTP behavior including the runtime's 404/500 bodies and the
// keep-alive reuse observed via net/http/httptrace. Behind //go:build e2e so it
// runs only in the tlang-dev container.

package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// echoServerFixture is the DB-free fixture every non-DB scenario targets.
const echoServerFixture = "echo_server.tl"

// withEchoServer builds echo_server.tl on leg lg (cached), launches it, and
// invokes fn with a ready HTTP driver. It handles build, startup, and
// idempotent shutdown. The fixture is DB-free (usesDB=false).
func withEchoServer(t *testing.T, lg leg, fn func(t *testing.T, d *httpDriver, h *serverHandle)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout+startTimeout+shutdownTimeout+requestTimeout)
	defer cancel()

	src := e2eFixturePath(t, echoServerFixture)
	bin, err := buildFixture(t, ctx, lg, src, false)
	if err != nil {
		t.Fatalf("build %s on %s: %v", echoServerFixture, lg.name, err)
	}

	h := startServer(t, ctx, bin, nil, lg.sanitizer)
	defer h.stop()

	d := newHTTPDriver(h, 2)
	fn(t, d, h)
}

// forEachLeg runs fn as a parallel subtest per compiler leg.
func forEachLeg(t *testing.T, fn func(t *testing.T, lg leg)) {
	t.Helper()
	for _, lg := range legs {
		lg := lg
		t.Run(lg.name, func(t *testing.T) {
			t.Parallel()
			fn(t, lg)
		})
	}
}

// reqCtx returns a per-request bounded context.
func reqCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), requestTimeout)
}

// TestE2E_UnknownRoute_404 (scenario 1): GET /nope -> 404 with the app-written
// body "Endpoint Not Found" (the dispatcher writes it, so the runtime's generic
// 404 page is never reached).
func TestE2E_UnknownRoute_404(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
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

// TestE2E_AuthGuard_401 (scenario 2): POST /echo with no Authorization -> 401,
// body "Unauthorized". The headers map explicitly omits Authorization so the
// 401 assertion is deliberate (design §4.7).
func TestE2E_AuthGuard_401(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
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

// TestE2E_InvalidJSON_400 (scenario 3): POST /echo with auth + malformed JSON
// -> 400, body "Invalid JSON Payload".
func TestE2E_InvalidJSON_400(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			res, err := d.postJSON(ctx, "/echo", nil, []byte(`{not valid json`))
			if err != nil {
				t.Fatalf("POST /echo: %v", err)
			}
			if res.status != 400 {
				t.Fatalf("status = %d, want 400", res.status)
			}
			if got := string(res.body); got != "Invalid JSON Payload" {
				t.Fatalf("body = %q, want %q", got, "Invalid JSON Payload")
			}
		})
	})
}

// TestE2E_Echo_201 (scenario 4): POST /echo with auth + valid JSON -> 201, and
// the response id/name equal to the request.
func TestE2E_Echo_201(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			const wantID = int64(4242)
			const wantName = "echo-me"
			res, err := d.postJSON(ctx, "/echo", nil, []byte(`{"id":4242,"name":"echo-me"}`))
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

// TestE2E_NothingWritten_404 (scenario 5): GET /silent (handler writes nothing)
// -> 404 with the runtime-generated body "404 Not Found\n".
func TestE2E_NothingWritten_404(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			res, err := d.get(ctx, "/silent")
			if err != nil {
				t.Fatalf("GET /silent: %v", err)
			}
			if res.status != 404 {
				t.Fatalf("status = %d, want 404", res.status)
			}
			if got := string(res.body); got != "404 Not Found\n" {
				t.Fatalf("body = %q, want %q", got, "404 Not Found\n")
			}
		})
	})
}

// TestE2E_UncaughtError_Generic (scenario 6): GET /boom (bare non-Error throw)
// -> status exactly 500, body "500 Internal Server Error\n", and the stderr
// ring buffer contains a "tlang: request error: 500 " line (the message is
// logged to stderr, not leaked in the body).
func TestE2E_UncaughtError_Generic(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			res, err := d.get(ctx, "/boom")
			if err != nil {
				t.Fatalf("GET /boom: %v", err)
			}
			if res.status != 500 {
				t.Fatalf("status = %d, want 500", res.status)
			}
			if got := string(res.body); got != "500 Internal Server Error\n" {
				t.Fatalf("body = %q, want %q", got, "500 Internal Server Error\n")
			}
			if !waitForStderr(h, "tlang: request error: 500 ") {
				t.Fatalf("stderr ring buffer does not contain the request-error log line\n--- server stderr ---\n%s", h.stderrSnapshot())
			}
		})
	})
}

// TestE2E_DateHeader_KeepAlive (scenario 7): a 200 response carries a Date
// header, and a second request reuses the keep-alive connection, observed via
// net/http/httptrace GotConn.Reused == true on the 2nd request after the 1st
// body is fully read and closed.
func TestE2E_DateHeader_KeepAlive(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			// Request 1 warms the idle connection (get fully reads+closes body).
			ctx1, cancel1 := reqCtx(t)
			defer cancel1()
			res1, err := d.get(ctx1, "/healthz")
			if err != nil {
				t.Fatalf("GET /healthz (1): %v", err)
			}
			if res1.status != 200 {
				t.Fatalf("status (1) = %d, want 200", res1.status)
			}
			if res1.header.Get("Date") == "" {
				t.Fatalf("response is missing the Date header")
			}

			// Request 2 observes keep-alive reuse via httptrace.
			var reused bool
			trace := &httptrace.ClientTrace{
				GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused },
			}
			ctx2, cancel2 := reqCtx(t)
			defer cancel2()
			ctx2 = httptrace.WithClientTrace(ctx2, trace)
			req2, err := http.NewRequestWithContext(ctx2, http.MethodGet, d.base+"/healthz", nil)
			if err != nil {
				t.Fatalf("new request (2): %v", err)
			}
			res2, err := d.do(req2)
			if err != nil {
				t.Fatalf("GET /healthz (2): %v", err)
			}
			if res2.status != 200 {
				t.Fatalf("status (2) = %d, want 200", res2.status)
			}
			if !reused {
				t.Fatalf("expected keep-alive connection reuse on 2nd request")
			}
		})
	})
}

// ---------------------------------------------------------------------------
// DB-backed scenarios (design §4.8 scenarios 8-11). All DB-gated via requireDB
// (self-skip-and-pass when TLANG_TEST_DATABASE_URL is unset, mirroring the C
// test_pg_* convention). State is observed purely through the server's HTTP
// contract; psql is used only for idempotent schema DDL (design §7.2), never
// for assertions. No Go pg driver is used.
// ---------------------------------------------------------------------------

// dbServerFixture is the DB-backed fixture for the fault-injection scenarios
// (empty-pool, client-disconnect). It uses_db, so it only starts with a URL.
const dbServerFixture = "db_server.tl"

// appTSFixture is examples/app.ts, reused IN PLACE as the DB happy-path/rollback
// fixture (design §3.1); it is never copied or edited.
func appTSFixture(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "examples", "app.ts"))
	if err != nil {
		t.Fatalf("resolve examples/app.ts: %v", err)
	}
	return p
}

// dbHappyLegs is the leg set for the DB happy path (scenario 8/9): all three
// legs, matching the §4.8 decision to run the happy path on
// clang-release + clang-asan-ubsan + tcc.
func dbHappyLegs() []leg { return legs }

// dbFaultLegs is the leg set for the fault-injection scenarios (10/11): the two
// clang legs only (release + sanitizer); tcc is skipped to bound runtime per the
// §4.8 decision.
func dbFaultLegs() []leg {
	out := make([]leg, 0, len(legs))
	for _, lg := range legs {
		if lg.name == legTCC {
			continue
		}
		out = append(out, lg)
	}
	return out
}

// forEachDBLeg runs fn as a parallel subtest per leg in the given set.
func forEachDBLeg(t *testing.T, set []leg, fn func(t *testing.T, lg leg)) {
	t.Helper()
	for _, lg := range set {
		lg := lg
		t.Run(lg.name, func(t *testing.T) {
			t.Parallel()
			fn(t, lg)
		})
	}
}

// legIDBase returns a per-leg offset so the parallel legs of a DB scenario use
// disjoint id ranges on the shared users table (a fixed id would collide across
// legs as a dup PK). clang-release=0, clang-asan-ubsan=100, tcc=200.
func legIDBase(lg leg) int64 {
	switch lg.name {
	case legClangASanUBSan:
		return 100
	case legTCC:
		return 200
	default: // legClangRelease and any other
		return 0
	}
}

// happyUserID is the (per-leg-unique) id scenario 8 inserts. The 8000 band is
// disjoint from scenario 9's 9000 band so Happy and Rollback never collide on
// the shared users table even when both run in the same process.
func happyUserID(lg leg) int64 { return 8000 + legIDBase(lg) }

// rollbackUserID is the (per-leg-unique) base id scenario 9 uses; it inserts id
// and id+1, both in the 9000 band disjoint from scenario 8's 8000 band.
func rollbackUserID(lg leg) int64 { return 9000 + legIDBase(lg) }

// withDBServer builds a DB-backed fixture on leg lg (cached), sets up the shared
// users schema, launches the child wired to the runtime DB URL, and invokes fn
// with a ready HTTP driver. extraEnv carries scenario-specific child vars (e.g.
// pool knobs). concurrency sizes the keep-alive pool. The fixture uses_db=true,
// so buildFixture keys the cache on the DB build.
func withDBServer(t *testing.T, lg leg, srcTS, url string, extraEnv []string, concurrency int, fn func(t *testing.T, d *httpDriver, h *serverHandle)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout+startTimeout+shutdownTimeout+4*requestTimeout)
	defer cancel()

	setupSchema(t, ctx, url)

	bin, err := buildFixture(t, ctx, lg, srcTS, true)
	if err != nil {
		t.Fatalf("build %s on %s: %v", filepath.Base(srcTS), lg.name, err)
	}

	env := append(dbChildEnv(url), extraEnv...)
	h := startServer(t, ctx, bin, env, lg.sanitizer)
	defer h.stop()

	d := newHTTPDriver(h, concurrency)
	fn(t, d, h)
}

// userResp is the app.ts/db_server.tl JSON response shape (id/name, plus the
// optional status app.ts returns).
type userResp struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// TestE2E_CreateUser_Happy (scenario 8): POST /api/users to examples/app.ts with
// Authorization + valid {id,name} -> 201, body status "SUCCESS" and id/name
// echoing the request. Runs on all three legs.
func TestE2E_CreateUser_Happy(t *testing.T) {
	url := requireDB(t)
	forEachDBLeg(t, dbHappyLegs(), func(t *testing.T, lg leg) {
		withDBServer(t, lg, appTSFixture(t), url, nil, 2, func(t *testing.T, d *httpDriver, h *serverHandle) {
			ctx, cancel := reqCtx(t)
			defer cancel()
			// Per-leg unique id: the three legs run as parallel subtests against
			// the shared users table, so a fixed id would collide (dup PK -> 500)
			// across legs. The id is otherwise immaterial to the happy path.
			wantID := happyUserID(lg)
			const wantName = "happy"
			body := fmt.Sprintf(`{"id":%d,"name":%q}`, wantID, wantName)
			res, err := d.postJSON(ctx, "/api/users", nil, []byte(body))
			if err != nil {
				t.Fatalf("POST /api/users: %v", err)
			}
			if res.status != 201 {
				t.Fatalf("status = %d, want 201 (body %q)", res.status, res.body)
			}
			var got userResp
			if err := json.Unmarshal(res.body, &got); err != nil {
				t.Fatalf("decode response %q: %v", res.body, err)
			}
			if got.ID != wantID || got.Name != wantName || got.Status != "SUCCESS" {
				t.Fatalf("response = %+v, want {id:%d name:%q status:SUCCESS}", got, wantID, wantName)
			}
		})
	})
}

// TestE2E_Rollback (scenario 9): against app.ts, first POST -> 201; a second
// POST with the SAME id -> 500 "Database Transaction Failed" (duplicate PK
// throws inside db.transaction, the transaction rolls back and catch runs); a
// third POST with a NEW id -> 201, proving the pool/connection recovered. Every
// POST carries Authorization: Bearer test (the guard runs before the handler).
// State is observed only through the HTTP responses. Runs on all three legs.
func TestE2E_Rollback(t *testing.T) {
	url := requireDB(t)
	forEachDBLeg(t, dbHappyLegs(), func(t *testing.T, lg leg) {
		withDBServer(t, lg, appTSFixture(t), url, nil, 2, func(t *testing.T, d *httpDriver, h *serverHandle) {
			// Unique id base per leg so the three parallel legs do not collide
			// on the shared users table (the dup PK must be self-inflicted).
			base := rollbackUserID(lg)

			post := func(id int64, name string) httpResult {
				ctx, cancel := reqCtx(t)
				defer cancel()
				body := fmt.Sprintf(`{"id":%d,"name":%q}`, id, name)
				res, err := d.postJSON(ctx, "/api/users", nil, []byte(body))
				if err != nil {
					t.Fatalf("POST /api/users id=%d: %v", id, err)
				}
				return res
			}

			// First insert of base -> 201.
			if res := post(base, "first"); res.status != 201 {
				t.Fatalf("first POST status = %d, want 201 (body %q)", res.status, res.body)
			}
			// Duplicate PK -> 500 "Database Transaction Failed".
			if res := post(base, "dup"); res.status != 500 {
				t.Fatalf("duplicate POST status = %d, want 500 (body %q)", res.status, res.body)
			} else if got := string(res.body); got != "Database Transaction Failed" {
				t.Fatalf("duplicate POST body = %q, want %q", got, "Database Transaction Failed")
			}
			// New id after rollback -> 201 (pool recovered).
			if res := post(base+1, "recovered"); res.status != 201 {
				t.Fatalf("post-rollback POST status = %d, want 201 (body %q)", res.status, res.body)
			}
		})
	})
}

// TestE2E_EmptyPool (scenario 10): db_server.tl with TLANG_DB_POOL_SIZE=1,
// TLANG_DB_POOL_TIMEOUT_MS=250, TLANG_THREADS=1 so there is exactly one pooled
// connection. N>=3 concurrent GET /slow (a fixed 2s pg_sleep): the first
// acquires the connection and returns 200 "slept"; the rest wait on the FIFO
// queue and receive 503 once the 250ms pool timeout elapses. The timeout-ordering
// invariant 250 < 2000 < 5000 < >=8000 (pool < sleep < statement < go-request)
// must hold, so this scenario raises requestTimeout to >=8s. Non-parallel.
func TestE2E_EmptyPool(t *testing.T) {
	url := requireDB(t)

	for _, lg := range dbFaultLegs() {
		lg := lg
		t.Run(lg.name, func(t *testing.T) {
			// Non-parallel: deliberately stresses a single small pool.
			poolEnv := []string{
				"TLANG_DB_POOL_SIZE=1",
				"TLANG_DB_POOL_TIMEOUT_MS=250",
				"TLANG_THREADS=1",
			}
			const concurrency = 4 // N >= 3 concurrent /slow requests
			withDBServer(t, lg, e2eFixturePath(t, dbServerFixture), url, poolEnv, concurrency, func(t *testing.T, d *httpDriver, h *serverHandle) {
				// requestTimeout raised to >= 8s so the server's own timeouts
				// (pool 250ms, statement 5000ms) fire before the Go client gives
				// up (invariant 250 < 2000 < 5000 < >=8000).
				const slowTimeout = 9 * time.Second

				type outcome struct {
					status int
					body   string
					err    error
				}
				results := make([]outcome, concurrency)
				var wg sync.WaitGroup
				wg.Add(concurrency)
				start := make(chan struct{})
				for i := 0; i < concurrency; i++ {
					// Each concurrent client uses its OWN transport (a fresh TCP
					// connection, no keep-alive reuse) so all N requests are truly
					// in flight against the server at once — mirroring N distinct
					// clients. A shared keep-alive transport could otherwise
					// serialize requests onto a single reused connection, which
					// would never exhaust a one-connection pool.
					cl := &http.Client{
						Transport: &http.Transport{DisableKeepAlives: true},
						Timeout:   slowTimeout,
					}
					go func(i int, cl *http.Client) {
						defer wg.Done()
						defer cl.CloseIdleConnections()
						<-start // release all requests as simultaneously as possible
						ctx, cancel := context.WithTimeout(context.Background(), slowTimeout)
						defer cancel()
						req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/slow", nil)
						if err != nil {
							results[i] = outcome{err: err}
							return
						}
						resp, err := cl.Do(req)
						if err != nil {
							results[i] = outcome{err: err}
							return
						}
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						results[i] = outcome{status: resp.StatusCode, body: string(body)}
					}(i, cl)
				}
				close(start)
				wg.Wait()

				var ok200, got503, other int
				for i, r := range results {
					switch {
					case r.err != nil:
						t.Errorf("request %d errored (want 200 or 503): %v", i, r.err)
						other++
					case r.status == 200 && r.body == "slept":
						ok200++
					case r.status == 503:
						got503++
					default:
						t.Errorf("request %d: status=%d body=%q, want 200 \"slept\" or 503", i, r.status, r.body)
						other++
					}
				}
				if ok200 != 1 {
					t.Errorf("exactly one in-flight 200 \"slept\" expected, got %d\n--- server stderr ---\n%s", ok200, h.stderrSnapshot())
				}
				if got503 != concurrency-1 {
					t.Errorf("expected %d pool-timeout 503s, got %d\n--- server stderr ---\n%s", concurrency-1, got503, h.stderrSnapshot())
				}
			})
		})
	}
}

// TestE2E_ClientDisconnect (scenario 11): against db_server.tl, open a raw
// net.Conn, send POST /tx (with Authorization: Bearer test, since /tx is
// guarded) initiating a DB round-trip, then close the connection mid-request
// before reading the response. The runtime observes the half-close, cancels the
// in-flight query, and returns the connection to the pool (the request throws
// 499 and writes nothing to the closed socket). A subsequent fresh POST /tx must
// still return its expected 201/500, proving the pool recovered. Non-parallel,
// small pool so a wedged connection would surface as the next request hanging.
func TestE2E_ClientDisconnect(t *testing.T) {
	url := requireDB(t)
	for _, lg := range dbFaultLegs() {
		lg := lg
		t.Run(lg.name, func(t *testing.T) {
			// Non-parallel.
			poolEnv := []string{
				"TLANG_DB_POOL_SIZE=1",
				"TLANG_THREADS=1",
			}
			withDBServer(t, lg, e2eFixturePath(t, dbServerFixture), url, poolEnv, 2, func(t *testing.T, d *httpDriver, h *serverHandle) {
				// Open a raw connection and send a POST /tx, then close it before
				// reading the response (mid-round-trip abandonment).
				conn, err := dialServer(d.dialAddr())
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				// Per-leg-unique id in the 11000 band (disjoint from scenarios 8
				// and 9) so repeated/non-parallel legs never collide on the
				// shared users table.
				base := 11000 + legIDBase(lg)
				body := fmt.Sprintf(`{"id":%d,"name":%q}`, base, "disconnect")
				req := "POST /tx HTTP/1.1\r\n" +
					"Host: " + d.dialAddr() + "\r\n" +
					"Authorization: Bearer test\r\n" +
					"Content-Type: application/json\r\n" +
					fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
					"Connection: close\r\n" +
					"\r\n" +
					body
				if _, err := conn.Write([]byte(req)); err != nil {
					t.Fatalf("write raw POST /tx: %v", err)
				}
				// Close immediately, mid-round-trip, without reading the response.
				_ = conn.Close()

				// Give the runtime a moment to observe the half-close and return
				// the connection to the pool before the recovery probe.
				time.Sleep(200 * time.Millisecond)

				// A fresh POST /tx on a new connection must still complete,
				// proving the pool recovered. A unique id avoids a dup-PK 500 so
				// the healthy path returns 201.
				ctx, cancel := reqCtx(t)
				defer cancel()
				freshBody := fmt.Sprintf(`{"id":%d,"name":%q}`, base+1, "fresh")
				res, err := d.postJSON(ctx, "/tx", nil, []byte(freshBody))
				if err != nil {
					t.Fatalf("fresh POST /tx after disconnect: %v\n--- server stderr ---\n%s", err, h.stderrSnapshot())
				}
				if res.status != 201 && res.status != 500 {
					t.Fatalf("fresh POST /tx status = %d, want 201 or 500 (pool must have recovered); body %q", res.status, res.body)
				}
			})
		})
	}
}
