// Node.js benchmark server for the comparative TLang vs Node.js vs Bun HTTP
// benchmark (design.md §5.2 Node, §5.12, §5.13). It replicates the TLang
// /bench and /healthz contract on the measured hot path using N independent
// SO_REUSEPORT worker processes (NO cluster): the faithful analog of TLang's N
// listener fds. Core modules only (http, net, child_process, os); no npm deps.
//
// Minimum Node version: 23.1 — the server.listen({ reusePort: true }) option
// landed around Node 23.1. The older cluster round-robin model is rejected
// (its primary owns the single serving socket), so this is the stated floor.
//
// Roles (selected by TLANG_BENCH_ROLE):
//   unset   -> supervisor: resolve PORT once, spawn N workers, barrier on N
//              NODE_READY tokens, print the single tlang: readiness line.
//   'worker'-> bind PORT with reusePort and serve; emit NODE_READY on stdout.
'use strict';

const http = require('http');
const net = require('net');
const { spawn } = require('child_process');

// The /bench body is byte-identical to TLang's ctx.json(200, {id:1,name:"bench"})
// serialization: 23 bytes, no spaces, field order id then name.
const BENCH_BODY = '{"id":1,"name":"bench"}';

// Real-work route builders return the OBJECT GRAPH (not the serialized string),
// so the per-request path can JSON.stringify it fresh each call — the faithful
// analog of TLang's echo_server.tl, which builds the response per request. Keys
// are inserted in the TLang interface field order (JsonResp:
// id,name,active,count,items,tags; WorkItem: id,label,score); JSON.stringify
// with no space argument yields bytes identical to TLang's ctx.json serializer.
// See tests/bench/README.md and the realwork design.
function buildJsonBody() {
  const items = [];
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

function buildWorkBody() {
  const rows = [];
  for (let i = 0; i < 32; i++) {
    rows.push({ id: i, label: 'row-' + i + '-of-32', score: i * i });
  }
  return rows;
}

// TLANG_BENCH_PERREQ=1 makes /json and /work serialize per request (fairness
// with TLang). When off, the bodies are serialized once at startup and cached.
// /bench is cached in both modes. The serialized bytes are byte-identical to
// the 716 (/json) and 1407 (/work) reference consts either way.
const PERREQ = process.env.TLANG_BENCH_PERREQ === '1';
const JSON_BODY = PERREQ ? null : JSON.stringify(buildJsonBody()); // 716 bytes
const WORK_BODY = PERREQ ? null : JSON.stringify(buildWorkBody()); // 1407 bytes

const HOST = process.env.TLANG_HOST || '127.0.0.1';
const REQ_PORT = parseInt(process.env.TLANG_PORT || '0', 10);
const ROLE = process.env.TLANG_BENCH_ROLE;

// Worker count N from TLANG_THREADS, clamped to [1,1024], default 1 on invalid.
function clampThreads(raw) {
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

// Shared request handler: the fairness contract (design.md FR-1.3).
function handler(req, res) {
  if (req.method === 'GET' && req.url === '/bench') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(BENCH_BODY);
    return;
  }
  // Real-work routes: byte-identical bodies, serialized per request under
  // PERREQ or served from the startup cache otherwise. Node's http server
  // exposes the raw request-line target on req.url (path-only here).
  if (req.method === 'GET' && req.url === '/json') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(PERREQ ? JSON.stringify(buildJsonBody()) : JSON_BODY);
    return;
  }
  if (req.method === 'GET' && req.url === '/work') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(PERREQ ? JSON.stringify(buildWorkBody()) : WORK_BODY);
    return;
  }
  if (req.method === 'GET' && req.url === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'text/plain; charset=utf-8' });
    res.end('ok');
    return;
  }
  res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
  res.end('Endpoint Not Found');
}

function runWorker() {
  const port = parseInt(process.env.TLANG_RESOLVED_PORT, 10);
  const server = http.createServer(handler);
  server.on('listening', () => {
    // Private ready token on stdout only — never the public tlang: line.
    process.stdout.write('NODE_READY\n');
  });
  server.on('error', (err) => {
    process.stderr.write('node worker bind failed: ' + err.message + '\n');
    process.exit(1);
  });
  const shutdown = () => {
    server.close(() => process.exit(0));
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
  server.listen({ port: port, host: '127.0.0.1', reusePort: true });
}

// resolvePort returns REQ_PORT when fixed, else an OS-assigned ephemeral port
// obtained via a throwaway listener (resolve-then-reuse, design.md §5.2 step 1).
function resolvePort(cb) {
  if (REQ_PORT !== 0) {
    cb(REQ_PORT);
    return;
  }
  const probe = net.createServer();
  probe.on('error', (err) => {
    process.stderr.write('node supervisor port resolve failed: ' + err.message + '\n');
    process.exit(1);
  });
  probe.listen(0, '127.0.0.1', () => {
    const port = probe.address().port;
    probe.close(() => cb(port));
  });
}

function runSupervisor() {
  resolvePort((port) => {
    const workers = [];
    let ready = 0;
    let failed = false;
    let timer = null;

    const fail = (msg) => {
      if (failed) {
        return;
      }
      failed = true;
      if (timer) {
        clearTimeout(timer);
      }
      process.stderr.write('node supervisor: ' + msg + ' (' + ready + '/' + N + ' ready)\n');
      for (const w of workers) {
        try {
          w.kill();
        } catch (e) {
          // worker already gone
        }
      }
      process.exit(1);
    };

    timer = setTimeout(() => {
      fail('readiness barrier timed out');
    }, readyBarrierTimeout);

    for (let i = 0; i < N; i++) {
      const child = spawn(process.execPath, [__filename], {
        stdio: ['ignore', 'pipe', 'inherit'],
        env: Object.assign({}, process.env, {
          TLANG_BENCH_ROLE: 'worker',
          TLANG_RESOLVED_PORT: String(port),
        }),
      });
      workers.push(child);

      let buf = '';
      child.stdout.setEncoding('utf8');
      child.stdout.on('data', (chunk) => {
        buf += chunk;
        let idx;
        while ((idx = buf.indexOf('\n')) !== -1) {
          const line = buf.slice(0, idx);
          buf = buf.slice(idx + 1);
          if (line === 'NODE_READY') {
            ready++;
            if (ready === N && !failed) {
              clearTimeout(timer);
              // The single public readiness line on STDERR, printed exactly
              // once, only after all N workers are serving (design.md §5.1).
              process.stderr.write(
                'tlang: listening on http://' + HOST + ':' + port + ' (' + N + ' threads)\n'
              );
            }
          }
        }
      });

      child.on('exit', (code) => {
        if (!failed && ready < N && code !== 0) {
          fail('worker exited with code ' + code + ' before ready');
        }
      });
    }

    const shutdown = () => {
      for (const w of workers) {
        try {
          w.kill();
        } catch (e) {
          // worker already gone
        }
      }
      process.exit(0);
    };
    process.on('SIGINT', shutdown);
    process.on('SIGTERM', shutdown);
  });
}

if (ROLE === 'worker') {
  runWorker();
} else {
  runSupervisor();
}
