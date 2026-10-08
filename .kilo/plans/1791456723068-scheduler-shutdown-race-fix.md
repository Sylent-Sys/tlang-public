# Scheduler Shutdown Race Fix Plan

## Goal

Make server shutdown reliably stop accepting new connections promptly under sustained listener load, while allowing already accepted request fibers to drain within the configured timeout. The main signal loop must distinguish “SIGINT/SIGTERM consumed” from “each scheduler has actually closed its listener.” Preserve scheduler-thread ownership of listener/epoll/fiber state.

## Evidence and Root-Cause Hypothesis

- `runtime/tests/test_main.c:607-610` sends SIGINT, waits until it is no longer pending (the main thread read it from `signalfd`), then polls TCP connections until the listener refuses them. That signal wait does not prove that the scheduler consumed its `wake_fd` event and closed its listener.
- `runtime/src/main.c:181-194` reads a signal and calls `tlang_sched_request_stop`; `runtime/src/sched.c:220-226` only writes the scheduler wake eventfd.
- `runtime/src/sched.c:617-659` dispatches epoll events in returned order. A listener event invokes `tlang_net_accept_ready` before a later wake event in the same batch. `runtime/src/net.c:103-133` accepts in a loop to EAGAIN. Under the test's repeated-connect loop, listener readiness may remain continuously asserted and defer stop handling.
- Failure reproduces intermittently with TCC and GCC forced to `TLANG_USE_UCONTEXT`, while ordinary GCC/Clang runs often pass. Prior ad-hoc fixes (wake-first for the current batch, accept batch limits, listener `shutdown` from main) did not establish correctness; cross-thread listener operations also broke graceful-drain tests. No experimental runtime edits remain in the tree.
- Treat accept-loop starvation as the leading hypothesis; instrument and verify before considering it proven.

## Agreed Design

Use both a scheduler close acknowledgment and bounded/control-prioritized accept processing:

1. The main signal loop requests stop by writing the existing per-scheduler `wake_fd`; it does not close or `shutdown()` listener sockets.
2. A scheduler consumes the wake on its owning thread, marks itself stopping, unregisters/closes its listener, cancels interruptible waits, and publishes exactly one listener-closed acknowledgment to a shared main-owned eventfd.
3. `tl_wait_schedulers` polls `signalfd`, scheduler-completion `done_fd`, and listener-close acknowledgment `ack_fd`. It continues processing signals while waiting for all scheduler listener-close acknowledgments. A second signal retains force-exit behavior. Stop requests caused internally (e.g. startup fiber failure) use the same scheduler close/ack path.
4. Scheduler event processing prioritizes control events in each returned epoll batch. In addition, listener accepts are bounded per readiness dispatch and check for a pending `wake_fd` between accept operations. A pending wake returns control to the scheduler loop without consuming it from a helper; the ordinary wake-event handler performs the authoritative stop transition. Level-triggered epoll makes unaccepted connections visible on the next iteration when not stopping.
5. A close acknowledgment is emitted only after the listener `close()` completes, with scheduler state disarmed first. It is emitted at most once per scheduler, including if stop was already requested, and never for script mode or a scheduler that has no listener.

## Ordered Implementation Tasks

1. **Instrument first:** add temporary or test-only timestamps/counters around signal consumption, stop wake writes, scheduler wake consumption, accept batch entry/exit, listener close, and close acknowledgment. Reproduce with the TCC and GCC-ucontext builds. Keep diagnostics out of normal user output; remove temporary instrumentation after evidence is captured or convert only useful assertions into tests.
2. **Add close acknowledgment plumbing:** create/initialize/close one `ack_fd` in server mode; pass it to each scheduler through `tl_thread_arg` / `tlang_sched`; document ownership and lifetime in `runtime/src/tlang_internal.h`. Scheduler threads may write tokens; only main reads. Do not close `ack_fd` until all scheduler threads have joined.
3. **Make stop transition single-shot:** factor existing wake-fd handling into one scheduler-thread-only helper. It closes and disarms listener, cancels interruptible waits, sets the shutdown deadline, then writes one acknowledgment token. Handle eventfd write errors deterministically (retry EINTR; log unexpected failure and fail closed without touching listener from another thread).
4. **Update main signal/completion loop:** extend `tl_wait_schedulers` to poll signal, done, and ack fds. Track total listener acknowledgments separately from exited scheduler count. Signal receipt sends one stop request per scheduler; additional signals force-exit even while acknowledgments are pending. Handle ack and done events in either order and drain aggregate eventfd counts without losing tokens. Startup-failure cleanup uses the same path for every started scheduler; unstarted listeners remain main-owned and are closed by main as today.
5. **Prevent listener starvation:** change `tlang_net_accept_ready` to a documented finite accept budget and check for a readable scheduler wake fd between accepts. Return to `tlang_sched_run` when the wake is pending. Ensure stale listener events in the current epoll batch are ignored after the wake transition sets `listen_fd = -1` / `stopping = true`; do not accept more work after stopping. Keep listener thread affinity; do not call `close`, `shutdown`, mutate `listen_fd`, or touch epoll state from the main signal thread.
6. **Clarify completion semantics:** distinguish scheduler thread completion from listener-close acknowledgment. `done_fd` continues to mean the thread's last action before return; it is not a substitute for close acknowledgment. Define handling for a scheduler that exits before startup or fails before arming accept, ensuring every started server scheduler either acknowledges “no listener/closed” or is explicitly excluded from the ack count.
7. **Add regression coverage:** retain the external TCP refusal assertion in `test_server_graceful_inflight`, but wait for/observe the runtime acknowledgment through a test-safe mechanism or bounded helper so it no longer conflates signalfd consumption with scheduler progress. Add a busy-accept variant that keeps connections arriving during SIGINT and proves the listener closes promptly while the parked request still completes. Cover multiple scheduler threads, init failure, idle shutdown, second-signal forced exit, simultaneous ack/done readiness, and no-listener startup/error paths.
8. **Remove diagnostics, document invariants, then validate.** Update runtime comments describing ownership, event ordering, and acknowledgment lifecycle.

## Safety and Failure Constraints

- Main is the only `signalfd` consumer and remains responsive to a second signal until scheduler completion; never block indefinitely in `pthread_join` before all signal handling is done.
- Only the scheduler thread mutates its `listen_fd`, epoll registration, `stopping`, wait queues, and fibers. The main thread communicates only through eventfd requests/acknowledgments.
- Ack occurs after listener close, not after SIGINT receipt or wake write. An acknowledgment cannot be confused with a thread-completion token.
- Ack count must be exact-once per started scheduler; eventfd counter aggregation is allowed, but no scheduler may emit duplicate acknowledgments or be omitted.
- If the main poll/read loop fails, request stops once, continue the established bounded join/cleanup behavior, and preserve redacted/error reporting. Do not create a path where signal handling silently disappears before a possible forced exit.
- Preserve graceful draining: listener closure must not cancel non-interruptible in-flight fibers; interruptible waits keep existing cancellation rules; configured shutdown timeout still bounds drain.
- Do not weaken or remove the test's external listener refusal contract to hide the race.

## Validation

1. Focused: `test_main` repeatedly under Docker with TCC/PG=0, GCC/PG=0, and GCC `CPPFLAGS=-DTLANG_USE_UCONTEXT`; run at least 100 iterations per backend or until the prior failure rate is statistically meaningfully exceeded without failures. Include the busy-accept regression.
2. Runtime: `make -C runtime clean && make -C runtime test CC=gcc PG=0`; repeat for `clang`, `tcc`, and `clang SAN=1 PG=0`. Run PG-enabled matrix where configured/available.
3. Compiler/app regression: `go test -count=1 ./...`; `go vet ./...`; `git diff --check`.
4. Stress concurrent lifecycle cases: multi-thread server shutdown, parked in-flight request, signal arriving during accept processing, startup failure, and second signal during drain. Confirm no new connection is accepted after close acknowledgment and accepted requests still finish or hit the configured shutdown timeout.
5. Review traces/counters to confirm ordering: stop request write -> scheduler wake handled -> listener closed -> ack published; verify no cross-thread close and no duplicate/missing acknowledgments.

## Out of Scope

- Replacing signalfd/eventfd or the existing signal policy.
- Changing public TLang APIs, generated program ABI, listener semantics outside graceful stop, or general connection admission/fairness policy beyond bounded dispatch needed for control responsiveness.
- Treating TCC itself as defective without a minimal independent reproduction; GCC-ucontext is part of the required matrix.
