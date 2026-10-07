// Bun benchmark server for the comparative TLang vs Node.js vs Bun HTTP
// benchmark (design.md §5.2 Bun, §5.12, §5.13). Structurally identical to the
// Node leg: it replicates the TLang /bench and /healthz contract on the
// measured hot path using N independent SO_REUSEPORT processes
// (N-1 spawned children + 1 primary = N serving processes). Bun built-ins
// only (Bun.serve, Bun.spawn, Bun.write); no external packages.
//
// Roles (selected by TLANG_BENCH_ROLE):
//   unset   -> primary: resolve PORT once, serve as the Nth process, spawn
//              N-1 children, barrier on N-1 BUN_READY tokens, print the single
//              tlang: readiness line.
//   'worker'-> bind PORT with reusePort and serve; emit BUN_READY on stdout.

declare const Bun: any;
declare const process: any;

// Byte-identical to TLang's ctx.json(200, {id:1,name:"bench"}): 23 bytes.
const BENCH_BODY = '{"id":1,"name":"bench"}';

// Real-work route builders return the OBJECT GRAPH (not the serialized string),
// mirroring the Node leg, so the per-request path can JSON.stringify it fresh
// each call — the faithful analog of TLang's echo_server.tl. Keys inserted in
// TLang field order (JsonResp: id,name,active,count,items,tags; WorkItem:
// id,label,score); JSON.stringify with no space argument yields bytes identical
// across all three runtimes.
function buildJsonBody(): any {
  const items: any[] = [];
  for (let i = 0; i < 16; i++) {
    items.push({ id: i, label: 'item-' + i, score: i * 100 });
  }
  const resp = {
    id: 7,
    name: 'payload',
    active: true,
    count: 16,
    items: items,
    tags: ['alpha', 'beta', 'gamma'],
  };
  return resp;
}

function buildWorkBody(): any {
  const rows: any[] = [];
  for (let i = 0; i < 32; i++) {
    rows.push({ id: i, label: 'row-' + i + '-of-32', score: i * i });
  }
  return rows;
}

// TLANG_BENCH_PERREQ=1 makes /json and /work serialize per request (fairness
// with TLang). When off, the bodies are serialized once at startup and cached.
// /bench is cached in both modes. The serialized bytes are byte-identical to
// the 716 (/json) and 1407 (/work) reference consts either way.
const PERREQ = Bun.env.TLANG_BENCH_PERREQ === '1';
const JSON_BODY = PERREQ ? null : JSON.stringify(buildJsonBody()); // 716 bytes
const WORK_BODY = PERREQ ? null : JSON.stringify(buildWorkBody()); // 1407 bytes

const HOST = process.env.TLANG_HOST || '127.0.0.1';
const REQ_PORT = parseInt(process.env.TLANG_PORT || '0', 10);
const ROLE = process.env.TLANG_BENCH_ROLE;

// Worker count N from TLANG_THREADS, clamped to [1,1024], default 1 on invalid.
function clampThreads(raw: string | undefined): number {
  const n = parseInt(raw || '1', 10);
  if (!Number.isFinite(n) || Number.isNaN(n)) {
    return 1;
  }
  if (n < 1) {
    return 1;
  }
  if (n > 1024) {
    return 1024;
  }
  return n;
}
const N = clampThreads(process.env.TLANG_THREADS);

const readyBarrierTimeout = 10000;

// Shared fetch handler: the fairness contract (design.md FR-1.3).
function fetchHandler(req: Request): Response {
  const pathname = new URL(req.url).pathname;
  if (req.method === 'GET' && pathname === '/bench') {
    return new Response(BENCH_BODY, {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }
  // Real-work routes: byte-identical bodies, serialized per request under
  // PERREQ or served from the startup cache otherwise. Bun's fetch hands the
  // handler a full URL on req.url, so route on `pathname` (computed above) —
  // NOT on req.url, which would always be false and break parity.
  if (req.method === 'GET' && pathname === '/json') {
    return new Response(PERREQ ? JSON.stringify(buildJsonBody()) : JSON_BODY, {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }
  if (req.method === 'GET' && pathname === '/work') {
    return new Response(PERREQ ? JSON.stringify(buildWorkBody()) : WORK_BODY, {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }
  if (req.method === 'GET' && pathname === '/healthz') {
    return new Response('ok', {
      status: 200,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  }
  return new Response('Endpoint Not Found', {
    status: 404,
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  });
}

function runWorker(): void {
  const port = parseInt(process.env.TLANG_RESOLVED_PORT, 10);
  let server: any;
  try {
    server = Bun.serve({ port: port, hostname: HOST, reusePort: true, fetch: fetchHandler });
  } catch (err: any) {
    Bun.write(Bun.stderr, 'bun worker bind failed: ' + String(err && err.message ? err.message : err) + '\n');
    process.exit(1);
    return;
  }
  // Private ready token on stdout only — never the public tlang: line.
  process.stdout.write('BUN_READY\n');
  const shutdown = () => {
    server.stop();
    process.exit(0);
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
}

// resolvePort returns REQ_PORT when fixed, else an OS-assigned ephemeral port
// via a throwaway Bun.serve (resolve-then-reuse, design.md §5.2 step 1).
function resolvePort(): number {
  if (REQ_PORT !== 0) {
    return REQ_PORT;
  }
  const probe = Bun.serve({ port: 0, hostname: HOST, fetch: fetchHandler });
  const port = probe.port;
  probe.stop();
  return port;
}

async function runPrimary(): Promise<void> {
  const port = resolvePort();

  // The primary is the Nth serving process.
  let server: any;
  try {
    server = Bun.serve({ port: port, hostname: HOST, reusePort: true, fetch: fetchHandler });
  } catch (err: any) {
    Bun.write(Bun.stderr, 'bun primary bind failed: ' + String(err && err.message ? err.message : err) + '\n');
    process.exit(1);
    return;
  }

  const children: any[] = [];
  const want = N - 1;
  let ready = 0;
  let failed = false;

  const fail = (msg: string): void => {
    if (failed) {
      return;
    }
    failed = true;
    Bun.write(Bun.stderr, 'bun primary: ' + msg + ' (' + ready + '/' + want + ' ready)\n');
    for (const c of children) {
      try {
        c.kill();
      } catch (e) {
        // child already gone
      }
    }
    process.exit(1);
  };

  const printReady = (): void => {
    // The single public readiness line on STDERR, printed exactly once, only
    // after all serving processes are live (design.md §5.1).
    Bun.write(
      Bun.stderr,
      'tlang: listening on http://' + HOST + ':' + port + ' (' + N + ' threads)\n'
    );
  };

  const shutdown = () => {
    server.stop();
    for (const c of children) {
      try {
        c.kill();
      } catch (e) {
        // child already gone
      }
    }
    process.exit(0);
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  if (want === 0) {
    // N === 1: primary serves alone, print immediately (zero tokens to read).
    printReady();
    return;
  }

  for (let i = 0; i < want; i++) {
    const child = Bun.spawn([process.execPath, import.meta.path], {
      stdout: 'pipe',
      stderr: 'inherit',
      env: Object.assign({}, process.env, {
        TLANG_BENCH_ROLE: 'worker',
        TLANG_RESOLVED_PORT: String(port),
      }),
    });
    children.push(child);

    // A nonzero child exit before readiness fails the barrier fast.
    child.exited.then((code: number) => {
      if (!failed && ready < want && code !== 0) {
        fail('child exited with code ' + code + ' before ready');
      }
    });
  }

  const timer = setTimeout(() => {
    fail('readiness barrier timed out');
  }, readyBarrierTimeout);

  // Drain each child's stdout for its BUN_READY token.
  const decoder = new TextDecoder();
  await Promise.all(
    children.map(async (child) => {
      let buf = '';
      const reader = child.stdout.getReader();
      while (true) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        buf += decoder.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.indexOf('\n')) !== -1) {
          const line = buf.slice(0, idx);
          buf = buf.slice(idx + 1);
          if (line === 'BUN_READY') {
            ready++;
            if (ready === want && !failed) {
              clearTimeout(timer);
              printReady();
            }
            return;
          }
        }
      }
    })
  );
}

if (ROLE === 'worker') {
  runWorker();
} else {
  void runPrimary();
}
