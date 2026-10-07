/*
 * test_main.c - unit tests for src/main.c (tlang_main). Program validation
 * and script mode run in-process. Server mode (signal handling, startup
 * failures) runs tlang_main in a forked child bound to 127.0.0.1 on an
 * ephemeral port: readiness is the child's "tlang: listening on" stderr line,
 * a consumed signal is its bit leaving /proc/<pid>/status ShdPnd, and a drain
 * is held open by a dispatcher blocked on a pipe. No sleep is used for
 * synchronisation; every wait has a deadline (the child is SIGKILLed and the
 * test fails on timeout).
 */
#include "tlang_internal.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static int failures;

#define TL_DEADLINE_MS 30000
static long long now_ms(void);

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* ---- program-description validation (exactly one entry -> else exit 2) ---- */
static void dummy_dispatcher(tlang_fiber* fib, tlang_ctx* ctx) { (void)fib; (void)ctx; }
static void dummy_main(tlang_fiber* fib) { (void)fib; }

static void test_validation(void) {
    tlang_program neither;
    tlang_program both;

    memset(&neither, 0, sizeof neither);
    neither.dispatcher = NULL;
    neither.main = NULL;
    CHECK(tlang_main(0, NULL, &neither) == 2);

    memset(&both, 0, sizeof both);
    both.dispatcher = dummy_dispatcher;
    both.main = dummy_main;
    CHECK(tlang_main(0, NULL, &both) == 2);
}

/* ---- script mode: init_globals then main, exit 0 ---- */
struct tl_globals { int value; };

static int g_init_ran;
static int g_main_ran;
static int g_main_saw_global;

static void script_init_globals(tlang_fiber* fib, void* globals) {
    struct tl_globals* g = (struct tl_globals*)globals;
    (void)fib;
    g_init_ran++;
    g->value = 42;
}

static void script_main_ok(tlang_fiber* fib) {
    g_main_ran++;
    /* init_globals ran first and the globals are reachable. */
    if (fib->globals != NULL && fib->globals->value == 42) g_main_saw_global = 1;
}

static void test_script_success(void) {
    tlang_program prog;
    int rc;
    memset(&prog, 0, sizeof prog);
    prog.init_globals = script_init_globals;
    prog.globals_size = sizeof(struct tl_globals);
    prog.main = script_main_ok;
    g_init_ran = g_main_ran = g_main_saw_global = 0;

    rc = tlang_main(0, NULL, &prog);
    CHECK(rc == 0);
    CHECK(g_init_ran == 1);
    CHECK(g_main_ran == 1);
    CHECK(g_main_saw_global == 1);  /* init ran before main */
}

/* Script-mode signals retain their default terminating action. */
static void test_script_signal(int sig);
static int g_script_ready_fd = -1;
static void script_wait_signal(tlang_fiber* fib) {
    char b = 'r';
    (void)fib;
    (void)write(g_script_ready_fd, &b, 1);
    for (;;) pause();
}

static void test_script_signal(int sig) {
    int ready[2], status;
    long long deadline;
    pid_t pid;
    tlang_program prog;
    memset(&prog, 0, sizeof prog);
    prog.main = script_wait_signal;
    if (pipe(ready) != 0) {
        CHECK(!"pipe failed");
        return;
    }
    if (fcntl(ready[1], F_SETFD, FD_CLOEXEC) != 0) {
        close(ready[0]);
        close(ready[1]);
        CHECK(!"fcntl failed");
        return;
    }
    pid = fork();
    if (pid < 0) {
        close(ready[0]);
        close(ready[1]);
        CHECK(!"fork failed");
        return;
    }
    if (pid == 0) {
        close(ready[0]);
        g_script_ready_fd = ready[1];
        exit(tlang_main(0, NULL, &prog));
    }
    close(ready[1]);
    deadline = now_ms() + TL_DEADLINE_MS;
    for (;;) {
        struct pollfd pfd;
        char b;
        long long remaining = deadline - now_ms();
        if (remaining <= 0) {
            CHECK(!"script child readiness timed out");
            kill(pid, SIGKILL);
            while (waitpid(pid, &status, 0) < 0 && errno == EINTR) { }
            close(ready[0]);
            return;
        }
        pfd.fd = ready[0];
        pfd.events = POLLIN;
        pfd.revents = 0;
        if (poll(&pfd, 1, remaining > 2147483647LL ? 2147483647 : (int)remaining) > 0) {
            if (read(ready[0], &b, 1) == 1) break;
        } else if (errno != EINTR) {
            CHECK(!"script child readiness pipe failed");
            kill(pid, SIGKILL);
            while (waitpid(pid, &status, 0) < 0 && errno == EINTR) { }
            close(ready[0]);
            return;
        }
    }
    close(ready[0]);
    CHECK(kill(pid, sig) == 0);
    CHECK(waitpid(pid, &status, 0) == pid);
    CHECK(WIFSIGNALED(status) && WTERMSIG(status) == sig);
}

/* ---- script mode: an error escaping main -> exit 1 ---- */
static void script_main_throws(tlang_fiber* fib) {
    tlang_throw(fib, 500, TLANG_STR("boom"));
}

static void test_script_error(void) {
    tlang_program prog;
    int rc;
    memset(&prog, 0, sizeof prog);
    prog.init_globals = NULL;
    prog.globals_size = 0;
    prog.main = script_main_throws;
    rc = tlang_main(0, NULL, &prog);
    CHECK(rc == 1);  /* uncaught error -> exit 1 */
}

/* ---- script mode: an error in init_globals -> exit 1, main never runs ---- */
static int g_err_main_ran;
static void init_throws(tlang_fiber* fib, void* globals) {
    (void)globals;
    tlang_throw(fib, 500, TLANG_STR("init failed"));
}
static void never_main(tlang_fiber* fib) { (void)fib; g_err_main_ran++; }

static void test_script_init_error(void) {
    tlang_program prog;
    int rc;
    memset(&prog, 0, sizeof prog);
    prog.init_globals = init_throws;
    prog.globals_size = sizeof(struct tl_globals);
    prog.main = never_main;
    g_err_main_ran = 0;
    rc = tlang_main(0, NULL, &prog);
    CHECK(rc == 1);
    CHECK(g_err_main_ran == 0);  /* main is skipped after an init error */
}

/* ------------------------------------------------------------------ */
/* Server mode in a forked child                                      */
/* ------------------------------------------------------------------ */

static long long now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* Polling interval of the deadline loops (not used for synchronisation). */
static void nap_ms(int ms) {
    struct timespec ts;
    ts.tv_sec = 0;
    ts.tv_nsec = (long)ms * 1000000L;
    nanosleep(&ts, NULL);
}

/* Held-drain plumbing, created before fork and inherited by the child: the
 * dispatcher writes one byte to g_entered[1], then blocks its scheduler thread
 * reading one byte from g_release[0]. It writes no response (-> 404). */
static int g_entered[2] = {-1, -1};
static int g_release[2] = {-1, -1};

static void held_dispatcher(tlang_fiber* fib, tlang_ctx* ctx) {
    char b = 'e';
    ssize_t r;
    (void)fib;
    (void)ctx;
    do {
        r = write(g_entered[1], &b, 1);
    } while (r < 0 && errno == EINTR);
    do {
        r = read(g_release[0], &b, 1);
    } while (r < 0 && errno == EINTR);
}

/* Like held_dispatcher, but parks the request FIBER (not the thread) on the
 * non-blocking release pipe with a non-interruptible tlang_wait_fd, so the
 * scheduler keeps running and processes the stop (closes the listener, sets
 * stopping) while the request is in flight: a real graceful drain. */
static void parked_dispatcher(tlang_fiber* fib, tlang_ctx* ctx) {
    Fiber* f = tlang_fiber_of(fib);
    char b = 'e';
    ssize_t r;
    (void)ctx;
    do {
        r = write(g_entered[1], &b, 1);
    } while (r < 0 && errno == EINTR);
    for (;;) {
        r = read(g_release[0], &b, 1);
        if (r >= 0) break;
        if (errno == EINTR) continue;
        if (errno != EAGAIN) break;
        if (tlang_wait_fd(f, g_release[0], EPOLLIN, 0) < 0) break;
    }
}

static void close_fd(int* fd) {
    if (*fd >= 0) close(*fd);
    *fd = -1;
}

/* Creates both pipes; nonblock_release makes the child's release end
 * non-blocking (parked_dispatcher needs it, held_dispatcher must not have it). */
static int held_pipes_open(int nonblock_release) {
    if (pipe(g_entered) != 0) return -1;
    if (pipe(g_release) != 0) {
        close_fd(&g_entered[0]);
        close_fd(&g_entered[1]);
        return -1;
    }
    if (nonblock_release &&
        fcntl(g_release[0], F_SETFL, fcntl(g_release[0], F_GETFL) | O_NONBLOCK) != 0) {
        return -1;
    }
    return 0;
}

static void held_pipes_close(void) {
    close_fd(&g_entered[0]);
    close_fd(&g_entered[1]);
    close_fd(&g_release[0]);
    close_fd(&g_release[1]);
}

typedef struct {
    pid_t pid;
    int errfd;          /* parent's non-blocking read end of the child's fd 2 */
    int status;         /* waitpid status once reaped */
    char err[32768];    /* child's stderr collected so far */
    size_t errlen;
} tl_child;

/* Appends whatever the child's stderr has to c->err; closes errfd at EOF. */
static void child_drain(tl_child* c) {
    char buf[4096];
    while (c->errfd >= 0) {
        ssize_t r = read(c->errfd, buf, sizeof buf);
        if (r > 0) {
            size_t room = sizeof c->err - 1 - c->errlen;
            size_t n = (size_t)r < room ? (size_t)r : room;
            memcpy(c->err + c->errlen, buf, n);
            c->errlen += n;
            c->err[c->errlen] = '\0';
            continue;
        }
        if (r < 0 && errno == EINTR) continue;
        if (r == 0) close_fd(&c->errfd);
        return;  /* EAGAIN (nothing more yet), EOF or error */
    }
}

/* Forks a child that runs tlang_main(prog) in server mode on 127.0.0.1:port
 * with `threads` schedulers, stderr into a pipe. The child exits with
 * tlang_main's code, or 3 if SIGINT/SIGTERM are still blocked after it. */
static int child_start(tl_child* c, const tlang_program* prog, const char* port,
                       const char* threads) {
    int ep[2];

    memset(c, 0, sizeof *c);
    c->pid = -1;
    c->errfd = -1;
    if (pipe(ep) != 0) return -1;
    fflush(stdout);
    fflush(stderr);
    c->pid = fork();
    if (c->pid < 0) {
        close(ep[0]);
        close(ep[1]);
        return -1;
    }
    if (c->pid == 0) {
        sigset_t cur;
        int rc;
        close(ep[0]);
        if (dup2(ep[1], 2) < 0) _exit(4);
        close(ep[1]);
        close_fd(&g_entered[0]);  /* parent's ends of the held-drain pipes */
        close_fd(&g_release[1]);
        unsetenv("TLANG_DATABASE_URL");
        unsetenv("DATABASE_URL");
        setenv("TLANG_HOST", "127.0.0.1", 1);
        setenv("TLANG_PORT", port, 1);
        setenv("TLANG_THREADS", threads, 1);
        setenv("TLANG_SHUTDOWN_TIMEOUT_MS", "10000", 1);
        rc = tlang_main(0, NULL, prog);
        sigemptyset(&cur);
        pthread_sigmask(SIG_BLOCK, NULL, &cur);
        if (sigismember(&cur, SIGINT) || sigismember(&cur, SIGTERM)) exit(3);
        /* exit, not _exit: LeakSanitizer checks the graceful child. */
        exit(rc);
    }
    close(ep[1]);
    close_fd(&g_entered[1]);  /* child's ends */
    close_fd(&g_release[0]);
    c->errfd = ep[0];
    fcntl(c->errfd, F_SETFL, fcntl(c->errfd, F_GETFL) | O_NONBLOCK);
    return 0;
}

/* Waits for the child's "tlang: listening on" line; returns its port or -1. */
static int child_wait_ready(tl_child* c) {
    static const char marker[] = "tlang: listening on http://127.0.0.1:";
    long long deadline = now_ms() + TL_DEADLINE_MS;
    while (now_ms() < deadline) {
        const char* p;
        child_drain(c);
        p = strstr(c->err, marker);
        if (p != NULL && strchr(p, '\n') != NULL) {
            return atoi(p + sizeof marker - 1);
        }
        if (c->errfd < 0) return -1;  /* EOF: the child is gone */
        {
            struct pollfd pfd;
            pfd.fd = c->errfd;
            pfd.events = POLLIN;
            pfd.revents = 0;
            poll(&pfd, 1, 50);
        }
    }
    return -1;
}

/* Reaps the child within the deadline (SIGKILL + -1 on timeout), collecting
 * its stderr meanwhile. Returns 0 with c->status set. */
static int child_wait(tl_child* c) {
    long long deadline = now_ms() + TL_DEADLINE_MS;
    for (;;) {
        pid_t r;
        child_drain(c);
        r = waitpid(c->pid, &c->status, WNOHANG);
        if (r == c->pid) {
            child_drain(c);
            close_fd(&c->errfd);
            return 0;
        }
        if (r < 0 && errno != EINTR) {
            close_fd(&c->errfd);
            return -1;
        }
        if (now_ms() >= deadline) {
            fprintf(stderr, "test_main: child %d timed out; killing it\n", (int)c->pid);
            kill(c->pid, SIGKILL);
            while (waitpid(c->pid, &c->status, 0) < 0 && errno == EINTR) { }
            child_drain(c);
            close_fd(&c->errfd);
            return -1;
        }
        if (c->errfd >= 0) {
            struct pollfd pfd;
            pfd.fd = c->errfd;
            pfd.events = POLLIN;
            pfd.revents = 0;
            poll(&pfd, 1, 20);
        } else {
            nap_ms(5);
        }
    }
}

/* Waits until `sig` is no longer pending on the child process (the runtime
 * read it from its signalfd). Returns 0, or -1 on timeout. */
static int child_wait_consumed(const tl_child* c, int sig) {
    char path[64];
    unsigned long long bit = 1ull << (sig - 1);
    long long deadline = now_ms() + TL_DEADLINE_MS;
    snprintf(path, sizeof path, "/proc/%d/status", (int)c->pid);
    while (now_ms() < deadline) {
        FILE* f = fopen(path, "r");
        char line[256];
        unsigned long long pend = 0;
        if (f == NULL) return 0;  /* gone; the exit status will tell */
        while (fgets(line, sizeof line, f) != NULL) {
            if (strncmp(line, "ShdPnd:", 7) == 0) {
                pend = strtoull(line + 7, NULL, 16);
                break;
            }
        }
        fclose(f);
        if ((pend & bit) == 0) return 0;
        nap_ms(1);
    }
    return -1;
}

/* Exit status of a reaped child that exited normally, else -1. */
static int child_exit_code(const tl_child* c) {
    return WIFEXITED(c->status) ? WEXITSTATUS(c->status) : -1;
}

/* Reports the child's stderr when this case added failures; also fails on
 * any sanitizer report in it. */
static void child_report(const tl_child* c, const char* name, int failures_before) {
    CHECK(strstr(c->err, "Sanitizer") == NULL);
    CHECK(strstr(c->err, "runtime error") == NULL);
    if (failures != failures_before) {
        fprintf(stderr, "---- %s: child status 0x%x, stderr:\n%s\n---- end %s\n",
                name, (unsigned)c->status, c->err, name);
    }
}

static int read_byte_by(int fd, long long deadline) {
    while (now_ms() < deadline) {
        struct pollfd pfd;
        char b;
        pfd.fd = fd;
        pfd.events = POLLIN;
        pfd.revents = 0;
        if (poll(&pfd, 1, 50) > 0) {
            ssize_t r = read(fd, &b, 1);
            if (r == 1) return 0;
            if (r == 0) return -1;
        }
    }
    return -1;
}

static int write_byte(int fd) {
    char b = 'r';
    ssize_t r;
    do {
        r = write(fd, &b, 1);
    } while (r < 0 && errno == EINTR);
    return r == 1 ? 0 : -1;
}

/* Connects to 127.0.0.1:port and sends one GET request; returns the socket. */
static int send_request(int port) {
    static const char req[] = "GET / HTTP/1.1\r\nHost: t\r\n\r\n";
    struct sockaddr_in sa;
    size_t off = 0;
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;
    memset(&sa, 0, sizeof sa);
    sa.sin_family = AF_INET;
    sa.sin_port = htons((unsigned short)port);
    sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (connect(fd, (struct sockaddr*)&sa, sizeof sa) != 0) {
        close(fd);
        return -1;
    }
    while (off < sizeof req - 1) {
        ssize_t r = write(fd, req + off, sizeof req - 1 - off);
        if (r < 0 && errno == EINTR) continue;
        if (r <= 0) {
            close(fd);
            return -1;
        }
        off += (size_t)r;
    }
    return fd;
}

/* Retries connecting to 127.0.0.1:port until it is refused (the listener was
 * closed), within the deadline. Connections that still succeed are closed at
 * once. Returns 0 once refused, -1 on timeout or another error. */
static int wait_listener_closed(int port) {
    long long deadline = now_ms() + TL_DEADLINE_MS;
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof sa);
    sa.sin_family = AF_INET;
    sa.sin_port = htons((unsigned short)port);
    sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    while (now_ms() < deadline) {
        int rc, err;
        int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
        if (fd < 0) return -1;
        rc = connect(fd, (struct sockaddr*)&sa, sizeof sa);
        err = errno;
        close(fd);
        if (rc != 0 && err == ECONNREFUSED) return 0;
        if (rc != 0 && err != EINTR) return -1;
        nap_ms(1);
    }
    return -1;
}

/* Reads until `want` bytes, EOF or the deadline; returns the count. */
static size_t read_response(int fd, char* buf, size_t cap, size_t want) {
    long long deadline = now_ms() + TL_DEADLINE_MS;
    size_t len = 0;
    while (len < want && len < cap - 1 && now_ms() < deadline) {
        struct pollfd pfd;
        pfd.fd = fd;
        pfd.events = POLLIN;
        pfd.revents = 0;
        if (poll(&pfd, 1, 50) > 0) {
            ssize_t r = read(fd, buf + len, cap - 1 - len);
            if (r < 0 && errno == EINTR) continue;
            if (r <= 0) break;
            len += (size_t)r;
        }
    }
    buf[len] = '\0';
    return len;
}

static void server_prog(tlang_program* prog, void (*disp)(tlang_fiber*, tlang_ctx*)) {
    memset(prog, 0, sizeof *prog);
    prog->dispatcher = disp;
}

/* ---- first SIGINT on an idle server: graceful stop, exit 0, mask restored ---- */
static void test_server_graceful_idle(void) {
    tlang_program prog;
    tl_child c;
    int before = failures;

    server_prog(&prog, dummy_dispatcher);
    if (child_start(&c, &prog, "0", "2") != 0) {
        CHECK(!"fork failed");
        return;
    }
    CHECK(child_wait_ready(&c) > 0);
    CHECK(kill(c.pid, SIGINT) == 0);
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 0);
    child_report(&c, "graceful_idle", before);
}

/* ---- SIGINT during an in-flight request: the scheduler stops (listener
 * closed) while the request fiber is parked, and its response is still
 * delivered ---- */
static void test_server_graceful_inflight(void) {
    tlang_program prog;
    tl_child c;
    char resp[256];
    int before = failures, port, sock = -1;

    server_prog(&prog, parked_dispatcher);
    if (held_pipes_open(1) != 0 || child_start(&c, &prog, "0", "1") != 0) {
        held_pipes_close();
        CHECK(!"pipe/fork failed");
        return;
    }
    port = child_wait_ready(&c);
    CHECK(port > 0);
    if (port > 0) sock = send_request(port);
    CHECK(sock >= 0);
    CHECK(read_byte_by(g_entered[0], now_ms() + TL_DEADLINE_MS) == 0);
    CHECK(kill(c.pid, SIGINT) == 0);
    CHECK(child_wait_consumed(&c, SIGINT) == 0);
    /* The scheduler handled the stop while the request is still parked. */
    if (port > 0) CHECK(wait_listener_closed(port) == 0);
    CHECK(write_byte(g_release[1]) == 0);
    if (sock >= 0) {
        CHECK(read_response(sock, resp, sizeof resp, 9) >= 9);
        CHECK(strncmp(resp, "HTTP/1.1 ", 9) == 0);
        close(sock);
    }
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 0);
    held_pipes_close();
    child_report(&c, "graceful_inflight", before);
}

/* ---- a second signal during a held drain force-exits with status 1 ---- */
static void test_server_forced(int second, const char* name) {
    tlang_program prog;
    tl_child c;
    int before = failures, port, sock = -1;

    /* Blocks the scheduler thread: the drain cannot end (nor time out), so
     * only the second signal can stop the child. */
    server_prog(&prog, held_dispatcher);
    if (held_pipes_open(0) != 0 || child_start(&c, &prog, "0", "1") != 0) {
        held_pipes_close();
        CHECK(!"pipe/fork failed");
        return;
    }
    port = child_wait_ready(&c);
    CHECK(port > 0);
    if (port > 0) sock = send_request(port);
    CHECK(sock >= 0);
    CHECK(read_byte_by(g_entered[0], now_ms() + TL_DEADLINE_MS) == 0);
    CHECK(kill(c.pid, SIGINT) == 0);
    CHECK(child_wait_consumed(&c, SIGINT) == 0);
    CHECK(kill(c.pid, second) == 0);
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 1);
    if (sock >= 0) close(sock);
    held_pipes_close();
    child_report(&c, name, before);
}

/* ---- init_globals fails on every scheduler: exit 1 without any signal ---- */
static void test_server_all_init_fail(void) {
    tlang_program prog;
    tl_child c;
    int before = failures;

    server_prog(&prog, dummy_dispatcher);
    prog.init_globals = init_throws;
    prog.globals_size = sizeof(struct tl_globals);
    if (child_start(&c, &prog, "0", "2") != 0) {
        CHECK(!"fork failed");
        return;
    }
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 1);
    CHECK(strstr(c.err, "global initialisation failed") != NULL);
    child_report(&c, "all_init_fail", before);
}

/* ---- the listener cannot bind (port taken without SO_REUSEPORT): exit 1 ---- */
static void test_server_bind_failure(void) {
    tlang_program prog;
    tl_child c;
    struct sockaddr_in sa;
    socklen_t salen = sizeof sa;
    char port[16];
    int before = failures;
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);

    CHECK(fd >= 0);
    if (fd < 0) return;
    memset(&sa, 0, sizeof sa);
    sa.sin_family = AF_INET;
    sa.sin_port = 0;
    sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (bind(fd, (struct sockaddr*)&sa, sizeof sa) != 0 || listen(fd, 1) != 0 ||
        getsockname(fd, (struct sockaddr*)&sa, &salen) != 0) {
        CHECK(!"could not occupy a port");
        close(fd);
        return;
    }
    snprintf(port, sizeof port, "%d", (int)ntohs(sa.sin_port));

    server_prog(&prog, dummy_dispatcher);
    if (child_start(&c, &prog, port, "1") != 0) {
        CHECK(!"fork failed");
        close(fd);
        return;
    }
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 1);
    close(fd);
    child_report(&c, "bind_failure", before);
}

/* ---- missing DB URL fails after scheduler init; exit() runs LSan on child ---- */
static void test_server_pg_startup_failure(void) {
#ifdef TLANG_NO_PG
    (void)failures;
#else
    tlang_program prog;
    tl_child c;
    int before = failures;

    server_prog(&prog, dummy_dispatcher);
    prog.uses_db = 1;
    if (child_start(&c, &prog, "0", "1") != 0) {
        CHECK(!"fork failed");
        return;
    }
    CHECK(child_wait(&c) == 0);
    CHECK(child_exit_code(&c) == 1);
    CHECK(strstr(c.err, "database") != NULL);
    CHECK(strstr(c.err, "ERROR: LeakSanitizer") == NULL);
    CHECK(strstr(c.err, "SUMMARY: AddressSanitizer") == NULL);
    child_report(&c, "pg_startup_failure", before);
#endif
}

int main(void) {
    const char* focused = getenv("TLANG_TEST_PG_STARTUP_FAILURE_ONLY");

    /* A write to a dead child's pipe must fail, not kill the test. */
    signal(SIGPIPE, SIG_IGN);

    if (focused != NULL && focused[0] == '1') {
        test_server_pg_startup_failure();
        if (failures != 0) return 1;
        puts("PASS test_main pg_startup_failure");
        return 0;
    }

    test_validation();
    test_script_success();
    test_script_error();
    test_script_init_error();
    test_script_signal(SIGINT);
    test_script_signal(SIGTERM);

    test_server_graceful_idle();
    test_server_graceful_inflight();
    test_server_forced(SIGINT, "forced_sigint_sigint");
    test_server_forced(SIGTERM, "forced_sigint_sigterm");
    test_server_all_init_fail();
    test_server_bind_failure();
    test_server_pg_startup_failure();

    if (failures != 0) {
        printf("FAIL test_main (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_main\n");
    return 0;
}
