/*
 * main.c - tlang_main, the C entry of every generated program (tlang.h §17,
 * tlang_internal.h). docs/RUNTIME.md §6.
 *
 * Validates the program (exactly one of dispatcher/main), loads the
 * configuration, sets the JSON depth limit and ignores SIGPIPE. Server mode
 * blocks SIGINT/SIGTERM before any thread exists and polls signalfd/eventfd on
 * the main thread; the established shutdown protocol is unchanged. Script mode
 * leaves SIGINT/SIGTERM unblocked, so their default actions apply. It runs one
 * scheduler and fiber on the main thread.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <sys/signalfd.h>
#include <unistd.h>

/* Shared startup argument for a spawned startup fiber. */
typedef struct {
    tlang_sched* s;
    const tlang_program* prog;
    int failed;
} tl_startup_arg;

/* ------------------------------------------------------------------ */
/* Startup / script fibers                                            */
/* ------------------------------------------------------------------ */

/* Server-mode startup fiber: runs init_globals (on the never-reset
 * globals_arena) then arms the listener. Stops the process on error. */
static void tl_startup_fiber(Fiber* f, void* arg) {
    tl_startup_arg* a = (tl_startup_arg*)arg;
    const tlang_program* prog = a->prog;

    f->pub.arena = &a->s->globals_arena;
    if (prog->init_globals != NULL) {
        prog->init_globals(&f->pub, a->s->globals);
        if (f->pub.err) {
            tlang_log_error("tlang: global initialisation failed: %d %.*s",
                            (int)f->pub.error.status,
                            (int)f->pub.error.message.len,
                            f->pub.error.message.data);
            a->failed = 1;
            a->s->exit_code = 1;
            tlang_sched_request_stop(a->s);
            return;
        }
    }
    tlang_sched_resume_accept(a->s);
}

/* Script-mode fiber: init_globals then main. Sets exit_code 1 on an escaped
 * error. */
static void tl_script_fiber(Fiber* f, void* arg) {
    tl_startup_arg* a = (tl_startup_arg*)arg;
    const tlang_program* prog = a->prog;

    f->pub.arena = &a->s->globals_arena;
    if (prog->init_globals != NULL) {
        prog->init_globals(&f->pub, a->s->globals);
        if (f->pub.err) {
            tlang_log_error("tlang: uncaught error: %d %.*s",
                            (int)f->pub.error.status,
                            (int)f->pub.error.message.len,
                            f->pub.error.message.data);
            a->s->exit_code = 1;
            return;
        }
    }
    if (prog->main != NULL) {
        prog->main(&f->pub);
        if (f->pub.err) {
            tlang_log_error("tlang: uncaught error: %d %.*s",
                            (int)f->pub.error.status,
                            (int)f->pub.error.message.len,
                            f->pub.error.message.data);
            a->s->exit_code = 1;
        }
    }
}

/* ------------------------------------------------------------------ */
/* Per-scheduler thread set-up (server mode)                          */
/* ------------------------------------------------------------------ */
typedef struct {
    tlang_sched* s;
    const tlang_config* cfg;
    const tlang_program* prog;
    int listen_fd;
    int done_fd;        /* eventfd: the thread adds 1 as its very last action */
    int ack_fd;         /* main-owned eventfd: scheduler adds 1 after listener close */
#ifdef TLANG_TEST_HOOKS
    int test_ack_fd;    /* parent pipe to observe scheduler close in test_main */
#endif
    int ok;             /* set 1 on successful init before the thread starts */
    char err[256];
} tl_thread_arg;

/* Allocates and zeroes the per-scheduler globals. */
static void* tl_make_globals(const tlang_program* prog) {
    if (prog->globals_size == 0) return NULL;
    {
        void* g = calloc(1, prog->globals_size);
        if (g == NULL) {
            tlang_log_error("tlang: out of memory allocating globals");
            exit(1);
        }
        return g;
    }
}

/* Tells the main thread this scheduler thread is finished: adds 1 to the
 * done eventfd. */
static void tl_signal_done(int done_fd) {
    uint64_t one = 1;
    ssize_t r;
    do {
        r = write(done_fd, &one, sizeof one);
    } while (r < 0 && errno == EINTR);
}

/* The function each scheduler thread runs. Signals done_fd as its very last
 * action on every return path. */
static void* tl_sched_thread(void* argp) {
    tl_thread_arg* ta = (tl_thread_arg*)argp;
    tlang_sched* s = ta->s;
    int done_fd = ta->done_fd;
    tl_startup_arg sa;

    tlang_fctx_init_thread(&s->loop_ctx);
    s->ack_fd = ta->ack_fd;
#ifdef TLANG_TEST_HOOKS
    s->test_ack_fd = ta->test_ack_fd;
#else
    s->test_ack_fd = -1;
#endif

    sa.s = s;
    sa.prog = ta->prog;
    sa.failed = 0;
    if (tlang_spawn(s, tl_startup_fiber, &sa) == NULL) {
        tlang_log_error("tlang: could not spawn startup fiber");
        s->exit_code = 1;
        s->now_ns = tlang_now_ns();
        {
            uint64_t stop_deadline = 0;
            tlang_sched_stop(s, &stop_deadline);
        }
#ifdef TLANG_TEST_HOOKS
        if (s->test_ack_fd >= 0) {
            char byte = 'a';
            (void)write(s->test_ack_fd, &byte, 1);
        }
#endif
        tl_signal_done(done_fd);
        return NULL;
    }
    tlang_sched_run(s);
#ifdef TLANG_TEST_HOOKS
    if (s->test_ack_fd >= 0) {
        char byte = 'a';
        ssize_t notified;
        do {
            notified = write(s->test_ack_fd, &byte, 1);
        } while (notified < 0 && errno == EINTR);
    }
#endif
    tl_signal_done(done_fd);
    return NULL;
}

/* Main-thread wait polls signals, thread completion, and listener-close
 * acknowledgments independently. It returns only after every started
 * scheduler has closed its listener and exited. A second signal during that
 * interval force-exits. On poll/read failure it requests stop once and returns
 * so callers can perform bounded cleanup. Returns 1 if stopping. */
static int tl_wait_schedulers(int sfd, int done_fd, int ack_fd,
                              tlang_sched* scheds, int n, int stopping) {
    uint64_t done = 0, acks = 0;
    int i;

    while (done < (uint64_t)n || acks < (uint64_t)n) {
        struct pollfd pfd[3];
        int failed = 0;

        pfd[0].fd = sfd;
        pfd[0].events = POLLIN;
        pfd[0].revents = 0;
        pfd[1].fd = done_fd;
        pfd[1].events = POLLIN;
        pfd[1].revents = 0;
        pfd[2].fd = ack_fd;
        pfd[2].events = POLLIN;
        pfd[2].revents = 0;
        if (poll(pfd, 3, -1) < 0) {
            if (errno == EINTR) continue;
            tlang_log_error("tlang: poll failed: %s", strerror(errno));
            failed = 1;
        } else if (((pfd[0].revents | pfd[1].revents | pfd[2].revents) &
                    (POLLERR | POLLHUP | POLLNVAL)) != 0) {
            tlang_log_error("tlang: poll reported an error on the signal/done/ack fds");
            failed = 1;
        }

        if (!failed && (pfd[0].revents & POLLIN)) {
            struct signalfd_siginfo si[8];
            ssize_t r = read(sfd, si, sizeof si);
            if (r < 0 && errno != EINTR && errno != EAGAIN) {
                tlang_log_error("tlang: signalfd read failed: %s", strerror(errno));
                failed = 1;
            } else if (r > 0) {
                size_t k, cnt = (size_t)r / sizeof si[0];
                /* Each queued signal in order: first stops, the next forces. */
                for (k = 0; k < cnt; k++) {
                    if (stopping) _exit(1);
                    for (i = 0; i < n; i++) tlang_sched_request_stop(&scheds[i]);
                    stopping = 1;
                }
            }
        }

        if (!failed && (pfd[2].revents & POLLIN)) {
            uint64_t count;
            ssize_t r;
            do {
                r = read(ack_fd, &count, sizeof count);
                if (r == (ssize_t)sizeof count) acks += count;
            } while (r == (ssize_t)sizeof count);
            if (r < 0 && errno != EINTR && errno != EAGAIN) {
                tlang_log_error("tlang: listener acknowledgment eventfd read failed: %s",
                                strerror(errno));
                failed = 1;
            }
            if (!failed && acks > (uint64_t)n) {
                tlang_log_error("tlang: received too many listener-close acknowledgments");
                failed = 1;
            }
        }

        if (!failed && (pfd[1].revents & POLLIN)) {
            uint64_t count;
            ssize_t r;
            do {
                r = read(done_fd, &count, sizeof count);
                if (r == (ssize_t)sizeof count) done += count;
            } while (r == (ssize_t)sizeof count);
            if (r < 0 && errno != EINTR && errno != EAGAIN) {
                tlang_log_error("tlang: completion eventfd read failed: %s", strerror(errno));
                failed = 1;
            }
            if (!failed && done > (uint64_t)n) {
                tlang_log_error("tlang: received too many scheduler completion notifications");
                failed = 1;
            }
        }

        if (failed) {
            if (!stopping) {
                for (i = 0; i < n; i++) tlang_sched_request_stop(&scheds[i]);
            }
            return 1;
        }
    }
    if (done == (uint64_t)n && acks < (uint64_t)n) {
        tlang_log_error("tlang: missing %llu scheduler listener-close acknowledgment(s)",
                        (unsigned long long)((uint64_t)n - acks));
        return 1;
    }
    return stopping;
}

/* Teardown: drains signals that arrived on sfd after the wait (during joins
 * and destroys) without blocking, so none is left pending for the mask
 * restore. After a stop request one force-exits, as during the drain; with
 * no stop requested the process is already returning and they are dropped. */
static void tl_drain_signals(int sfd, int stopping) {
    for (;;) {
        struct pollfd pfd;
        struct signalfd_siginfo si[8];
        ssize_t r;
        int pr;

        pfd.fd = sfd;
        pfd.events = POLLIN;
        pfd.revents = 0;
        pr = poll(&pfd, 1, 0);
        if (pr < 0 && errno == EINTR) continue;
        if (pr <= 0 || (pfd.revents & POLLIN) == 0) return;
        r = read(sfd, si, sizeof si);
        if (r < 0 && errno == EINTR) continue;
        if (r <= 0) return;
        if (stopping) _exit(1);
    }
}

/* ------------------------------------------------------------------ */
/* tlang_main                                                         */
/* ------------------------------------------------------------------ */
int tlang_main(int argc, char** argv, const tlang_program* prog) {
    tlang_config cfg;
    char err[256];
    sigset_t block, old;
    int mask_saved = 0;

    (void)argc;
    (void)argv;

    /* Validate the program description: exactly one entry. */
    if ((prog->dispatcher != NULL) == (prog->main != NULL)) {
        tlang_log_error("tlang: program must have exactly one of dispatcher and main");
        return 2;
    }

    if (tlang_config_load(&cfg, err, sizeof err) != 0) {
        tlang_log_error("tlang: %s", err);
        return 2;
    }

    json_set_max_depth(cfg.json_max_depth);

    signal(SIGPIPE, SIG_IGN);

    /* Script mode leaves SIGINT/SIGTERM unblocked for their default actions. */
    if (prog->dispatcher != NULL) {
        /* Server threads inherit this mask; main consumes signals via signalfd. */
        sigemptyset(&block);
        sigaddset(&block, SIGINT);
        sigaddset(&block, SIGTERM);
        if (pthread_sigmask(SIG_BLOCK, &block, &old) != 0) {
            tlang_log_error("tlang: could not block server signals");
            return 1;
        }
        mask_saved = 1;
    }

    if (prog->main != NULL) {
        /* ---- Script mode: one scheduler on the main thread, no listener. */
        tlang_sched sched;
        tl_startup_arg sa;
        int rc;

        if (tlang_sched_init(&sched, 0, &cfg, prog, -1, err, sizeof err) != 0) {
            tlang_log_error("tlang: %s", err);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }
        sched.globals = (struct tl_globals*)tl_make_globals(prog);
        if (tlang_arena_init(&sched.globals_arena) != 0) {
            tlang_log_error("tlang: out of memory");
            tlang_sched_destroy(&sched);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }

        sa.s = &sched;
        sa.prog = prog;
        sa.failed = 0;
        tlang_fctx_init_thread(&sched.loop_ctx);
        if (tlang_spawn(&sched, tl_script_fiber, &sa) == NULL) {
            tlang_log_error("tlang: could not spawn main fiber");
            free(sched.globals);
            tlang_sched_destroy(&sched);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }
        tlang_sched_run(&sched);
        rc = sched.exit_code;
        free(sched.globals);
        tlang_sched_destroy(&sched);
        if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
        return rc;
    }

    /* ---- Server mode. */
    {
        int nthreads = cfg.threads;
        tlang_sched* scheds;
        tl_thread_arg* targs;
        pthread_t* threads;
        int i, port, started = 0, initialized = 0, startup_failed = 0;
        int sfd, done_fd, ack_fd, stopping = 0;

        /* The signals arrive on sfd; each finished scheduler thread adds 1 to
         * done_fd. Both exist before any listener or thread. */
        sfd = signalfd(-1, &block, SFD_CLOEXEC);
        if (sfd < 0) {
            tlang_log_error("tlang: signalfd failed: %s", strerror(errno));
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }
        done_fd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
        if (done_fd < 0) {
            tlang_log_error("tlang: eventfd failed: %s", strerror(errno));
            close(sfd);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }
        ack_fd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
        if (ack_fd < 0) {
            tlang_log_error("tlang: acknowledgment eventfd failed: %s", strerror(errno));
            close(done_fd);
            close(sfd);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }
        scheds = (tlang_sched*)calloc((size_t)nthreads, sizeof *scheds);
        targs = (tl_thread_arg*)calloc((size_t)nthreads, sizeof *targs);
        threads = (pthread_t*)calloc((size_t)nthreads, sizeof *threads);
        if (scheds == NULL || targs == NULL || threads == NULL) {
            tlang_log_error("tlang: out of memory");
            free(scheds); free(targs); free(threads);
            close(sfd);
            close(done_fd);
            close(ack_fd);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }

        /* Scheduler 0's listener binds first; with port 0 it decides the
         * shared ephemeral port for the others. */
#ifdef TLANG_TEST_HOOKS
        for (i = 0; i < nthreads; i++) {
            targs[i].listen_fd = -1;
            scheds[i].listen_fd = -1;
        }
        {
            const char* test_ack = getenv("TLANG_TEST_LISTENER_ACK_FD");
            int64_t parsed;
            int test_ack_fd = -1;
            if (test_ack != NULL && tlang_parse_i64(test_ack, strlen(test_ack), &parsed) &&
                parsed >= 0 && parsed <= 2147483647) {
                test_ack_fd = (int)parsed;
            }
            for (i = 0; i < nthreads; i++) targs[i].test_ack_fd = test_ack_fd;
        }
#else
        for (i = 0; i < nthreads; i++) {
            targs[i].listen_fd = -1;
            scheds[i].listen_fd = -1;
        }
#endif
        port = cfg.port;
        for (i = 0; i < nthreads; i++) {
            int lfd = tlang_net_listen(cfg.host, port, err, sizeof err);
            if (lfd < 0) {
                tlang_log_error("tlang: %s", err);
                startup_failed = 1;
                break;
            }
            if (i == 0 && cfg.port == 0) {
                port = tlang_net_local_port(lfd);
                if (port < 0) {
                    tlang_log_error("tlang: could not read listener port");
                    close(lfd);
                    startup_failed = 1;
                    break;
                }
            }
            targs[i].listen_fd = lfd;
        }

        if (!startup_failed) {
            fprintf(stderr, "tlang: listening on http://%s:%d (%d threads)\n",
                    cfg.host, port, nthreads);

            for (i = 0; i < nthreads; i++) {
                tlang_sched* s = &scheds[i];
                if (tlang_sched_init(s, i, &cfg, prog, targs[i].listen_fd,
                                     err, sizeof err) != 0) {
                    tlang_log_error("tlang: %s", err);
                    startup_failed = 1;
                    break;
                }
                initialized++;
                s->globals = (struct tl_globals*)tl_make_globals(prog);
                if (tlang_arena_init(&s->globals_arena) != 0) {
                    tlang_log_error("tlang: out of memory");
                    startup_failed = 1;
                    break;
                }
                if (prog->uses_db) {
                    if (tlang_pg_pool_create(s, err, sizeof err) != 0) {
                        tlang_log_error("tlang: %s", err);
                        startup_failed = 1;
                        break;
                    }
                }
                targs[i].s = s;
                targs[i].cfg = &cfg;
                targs[i].prog = prog;
                targs[i].done_fd = done_fd;
                targs[i].ack_fd = ack_fd;
                targs[i].ok = 1;
                if (pthread_create(&threads[i], NULL, tl_sched_thread, &targs[i]) != 0) {
                    tlang_log_error("tlang: pthread_create failed");
                    startup_failed = 1;
                    break;
                }
                started++;
            }
        }

        if (startup_failed) {
            /* Stop whatever started (a signal meanwhile force-exits), close
             * unused listeners. */
            if (started > 0) {
                for (i = 0; i < started; i++) tlang_sched_request_stop(&scheds[i]);
                stopping = tl_wait_schedulers(sfd, done_fd, ack_fd, scheds, started, 1);
            }
            for (i = 0; i < started; i++) pthread_join(threads[i], NULL);
            for (i = 0; i < nthreads; i++) {
                if (i < initialized) {
                    free(scheds[i].globals);
                    tlang_sched_destroy(&scheds[i]);
                } else if (targs[i].listen_fd >= 0) {
                    close(targs[i].listen_fd);
                }
            }
            free(scheds); free(targs); free(threads);
            tl_drain_signals(sfd, stopping);
            close(sfd);
            close(done_fd);
            close(ack_fd);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return 1;
        }

        /* Run until every scheduler thread has finished: the first signal
         * stops them, a second force-exits; if they all exit on their own
         * (e.g. init_globals failed everywhere) the wait ends too. */
        stopping = tl_wait_schedulers(sfd, done_fd, ack_fd, scheds, nthreads, 0);
        for (i = 0; i < nthreads; i++) pthread_join(threads[i], NULL);

        {
            int rc = 0;
            for (i = 0; i < nthreads; i++) {
                if (scheds[i].exit_code != 0) rc = scheds[i].exit_code;
                free(scheds[i].globals);
                tlang_sched_destroy(&scheds[i]);
            }
            free(scheds); free(targs); free(threads);
            /* sfd stays open through teardown so a late signal is honoured. */
            tl_drain_signals(sfd, stopping);
            close(sfd);
            close(done_fd);
            close(ack_fd);
            if (mask_saved) pthread_sigmask(SIG_SETMASK, &old, NULL);
            return rc;
        }
    }
}
