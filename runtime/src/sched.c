/*
 * sched.c - the per-thread scheduler, netpoller and waits (tlang_internal.h
 * §6, spec §7.2/§7.3). docs/RUNTIME.md §6.
 *
 * One epoll set per scheduler. fd registrations use EPOLLONESHOT, ADD the
 * first time and MOD afterwards (the fd table remembers). The listener and the
 * wake eventfd are level-triggered. Events are validated against the fd table
 * before a fiber is woken (stale one-shot events are dropped). A deadline
 * min-heap over Fiber.deadline_ns gives the epoll_wait timeout; now_ns is
 * cached once per loop iteration. A stop request closes the listener, cancels
 * interruptible waits and wait queues with TLANG_WAIT_CANCELLED, and returns
 * once no fiber is live or cfg->shutdown_timeout_ms has passed.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/epoll.h>
#include <sys/eventfd.h>

/* ------------------------------------------------------------------ */
/* Time                                                               */
/* ------------------------------------------------------------------ */
uint64_t tlang_now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

/* ------------------------------------------------------------------ */
/* Run queue (FIFO via Fiber.next_ready)                              */
/* ------------------------------------------------------------------ */
void tlang_sched_ready(tlang_sched* s, Fiber* f) {
    f->state = FIBER_READY;
    f->next_ready = NULL;
    if (s->runq_tail != NULL) {
        s->runq_tail->next_ready = f;
        s->runq_tail = f;
    } else {
        s->runq_head = s->runq_tail = f;
    }
}

static Fiber* runq_pop(tlang_sched* s) {
    Fiber* f = s->runq_head;
    if (f == NULL) return NULL;
    s->runq_head = f->next_ready;
    if (s->runq_head == NULL) s->runq_tail = NULL;
    f->next_ready = NULL;
    return f;
}

/* ------------------------------------------------------------------ */
/* Deadline min-heap (1-based, heap_index stored in each fiber)       */
/* ------------------------------------------------------------------ */
static void heap_swap(tlang_sched* s, size_t i, size_t j) {
    Fiber* a = s->heap[i];
    Fiber* b = s->heap[j];
    s->heap[i] = b;
    s->heap[j] = a;
    a->heap_index = j + 1;
    b->heap_index = i + 1;
}

static void heap_up(tlang_sched* s, size_t i) {
    while (i > 0) {
        size_t parent = (i - 1) / 2;
        if (s->heap[parent]->deadline_ns <= s->heap[i]->deadline_ns) break;
        heap_swap(s, parent, i);
        i = parent;
    }
}

static void heap_down(tlang_sched* s, size_t i) {
    for (;;) {
        size_t l = 2 * i + 1, r = 2 * i + 2, m = i;
        if (l < s->heap_len && s->heap[l]->deadline_ns < s->heap[m]->deadline_ns) m = l;
        if (r < s->heap_len && s->heap[r]->deadline_ns < s->heap[m]->deadline_ns) m = r;
        if (m == i) break;
        heap_swap(s, i, m);
        i = m;
    }
}

/* Adds f to the deadline heap (f->deadline_ns must be set and != 0). Returns
 * -1 if it could not grow the heap. */
static int heap_push(tlang_sched* s, Fiber* f) {
    if (s->heap_len == s->heap_cap) {
        size_t cap = s->heap_cap == 0 ? 16 : s->heap_cap * 2;
        Fiber** h = (Fiber**)realloc(s->heap, cap * sizeof *h);
        if (h == NULL) return -1;
        s->heap = h;
        s->heap_cap = cap;
    }
    s->heap[s->heap_len] = f;
    f->heap_index = s->heap_len + 1;
    s->heap_len++;
    heap_up(s, s->heap_len - 1);
    return 0;
}

static void heap_remove(tlang_sched* s, Fiber* f) {
    size_t i;
    if (f->heap_index == 0) return;
    i = f->heap_index - 1;
    f->heap_index = 0;
    s->heap_len--;
    if (i == s->heap_len) return;
    s->heap[i] = s->heap[s->heap_len];
    s->heap[i]->heap_index = i + 1;
    heap_up(s, i);
    heap_down(s, i);
}

/* ------------------------------------------------------------------ */
/* fd table                                                           */
/* ------------------------------------------------------------------ */
static int fds_ensure(tlang_sched* s, int fd) {
    if ((size_t)fd >= s->nfds) {
        size_t cap = s->nfds == 0 ? 64 : s->nfds;
        tlang_fd_slot* t;
        while ((size_t)fd >= cap) cap *= 2;
        t = (tlang_fd_slot*)realloc(s->fds, cap * sizeof *t);
        if (t == NULL) return -1;
        memset(t + s->nfds, 0, (cap - s->nfds) * sizeof *t);
        s->fds = t;
        s->nfds = cap;
    }
    return 0;
}

/* ------------------------------------------------------------------ */
/* Scheduler lifecycle                                                */
/* ------------------------------------------------------------------ */
int tlang_sched_init(tlang_sched* s, int id, const tlang_config* cfg,
                     const tlang_program* prog, int listen_fd, char* err, size_t errlen) {
    struct epoll_event ev;

    s->id = id;
    s->cfg = cfg;
    s->prog = prog;
    s->epfd = -1;
    s->listen_fd = listen_fd;
    s->wake_fd = -1;
    s->stopping = false;
    s->accept_armed = false;
    s->accept_paused = false;
    s->exit_code = 0;
    s->current = NULL;
    s->runq_head = s->runq_tail = NULL;
    s->heap = NULL;
    s->heap_len = s->heap_cap = 0;
    s->free_list = NULL;
    s->nfibers = 0;
    s->nlive = 0;
    s->next_fiber_id = 0;
    s->fds = NULL;
    s->nfds = 0;
    s->globals = NULL;
    s->db_pool = NULL;
    s->now_ns = tlang_now_ns();
    s->http_date[0] = '\0';
    s->http_date_sec = -1;
    memset(&s->globals_arena, 0, sizeof s->globals_arena);

    s->epfd = epoll_create1(EPOLL_CLOEXEC);
    if (s->epfd < 0) {
        snprintf(err, errlen, "epoll_create1 failed: %s", strerror(errno));
        return -1;
    }
    s->wake_fd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    if (s->wake_fd < 0) {
        snprintf(err, errlen, "eventfd failed: %s", strerror(errno));
        close(s->epfd);
        s->epfd = -1;
        return -1;
    }
    /* The wake eventfd is level-triggered (no EPOLLONESHOT). */
    memset(&ev, 0, sizeof ev);
    ev.events = EPOLLIN;
    ev.data.fd = s->wake_fd;
    if (epoll_ctl(s->epfd, EPOLL_CTL_ADD, s->wake_fd, &ev) != 0) {
        snprintf(err, errlen, "epoll_ctl(wake) failed: %s", strerror(errno));
        close(s->wake_fd);
        close(s->epfd);
        s->wake_fd = s->epfd = -1;
        return -1;
    }
    return 0;
}

void tlang_sched_destroy(tlang_sched* s) {
    tlang_pg_pool_destroy(s);
    tlang_fiber_pool_destroy(s);
    if (s->listen_fd >= 0) {
        close(s->listen_fd);
        s->listen_fd = -1;
    }
    if (s->wake_fd >= 0) {
        close(s->wake_fd);
        s->wake_fd = -1;
    }
    if (s->epfd >= 0) {
        close(s->epfd);
        s->epfd = -1;
    }
    free(s->fds);
    s->fds = NULL;
    s->nfds = 0;
    free(s->heap);
    s->heap = NULL;
    s->heap_len = s->heap_cap = 0;
    tlang_arena_destroy(&s->globals_arena);
}

void tlang_sched_request_stop(tlang_sched* s) {
    uint64_t one = 1;
    ssize_t r;
    do {
        r = write(s->wake_fd, &one, sizeof one);
    } while (r < 0 && errno == EINTR);
}

/* ------------------------------------------------------------------ */
/* Accept arming                                                      */
/* ------------------------------------------------------------------ */
void tlang_sched_resume_accept(tlang_sched* s) {
    struct epoll_event ev;
    s->accept_paused = false;
    if (s->stopping || s->listen_fd < 0) return;
    memset(&ev, 0, sizeof ev);
    ev.events = EPOLLIN;
    ev.data.fd = s->listen_fd;
    if (!s->accept_armed) {
        if (epoll_ctl(s->epfd, EPOLL_CTL_ADD, s->listen_fd, &ev) == 0) {
            s->accept_armed = true;
        }
    }
}

void tlang_sched_pause_accept(tlang_sched* s) {
    s->accept_paused = true;
    if (s->accept_armed && s->listen_fd >= 0) {
        epoll_ctl(s->epfd, EPOLL_CTL_DEL, s->listen_fd, NULL);
        s->accept_armed = false;
    }
}

/* ------------------------------------------------------------------ */
/* fd waits                                                           */
/* ------------------------------------------------------------------ */

/* Arms fd for `events` with EPOLLONESHOT in the epoll set, ADD the first time
 * and MOD after. Returns 0 or -1 (epoll_ctl failed). */
static int fd_arm(tlang_sched* s, int fd, uint32_t events, Fiber* waiter) {
    struct epoll_event ev;
    int op;
    if (fds_ensure(s, fd) != 0) return -1;
    memset(&ev, 0, sizeof ev);
    ev.events = events | EPOLLONESHOT;
    ev.data.fd = fd;
    op = s->fds[fd].registered ? EPOLL_CTL_MOD : EPOLL_CTL_ADD;
    if (epoll_ctl(s->epfd, op, fd, &ev) != 0) return -1;
    s->fds[fd].registered = true;
    s->fds[fd].events = events;
    s->fds[fd].waiter = waiter;
    return 0;
}

void tlang_fd_forget(tlang_sched* s, int fd) {
    if (fd < 0 || (size_t)fd >= s->nfds) return;
    if (s->fds[fd].registered) {
        epoll_ctl(s->epfd, EPOLL_CTL_DEL, fd, NULL);
    }
    s->fds[fd].registered = false;
    s->fds[fd].events = 0;
    s->fds[fd].waiter = NULL;
}

/* Parks the current fiber on fd (and optional watch_fd) until an event, the
 * deadline, or a stop request. Returns the result code. */
static int wait_fd_impl(Fiber* f, int fd, uint32_t events, int watch_fd,
                        uint64_t deadline_ns) {
    tlang_sched* s = f->sched;
    uint32_t arm_events = events;

    if (f->interruptible && s->stopping) return TLANG_WAIT_CANCELLED;

    /* Add EPOLLRDHUP when EPOLLIN is requested (notice half-close), not for a
     * pure EPOLLOUT wait. */
    if (events & EPOLLIN) arm_events |= EPOLLRDHUP;

    if (fd_arm(s, fd, arm_events, f) != 0) return TLANG_WAIT_ERR;

    if (watch_fd >= 0 && watch_fd != fd) {
        if (fd_arm(s, watch_fd, EPOLLRDHUP, f) != 0) {
            tlang_fd_forget(s, fd);
            return TLANG_WAIT_ERR;
        }
    }

    f->waiting_fd = fd;
    f->waiting_fd2 = (watch_fd >= 0 && watch_fd != fd) ? watch_fd : -1;
    f->wait_events = events;
    f->deadline_ns = deadline_ns;
    f->wait_result = TLANG_WAIT_ERR;
    f->state = FIBER_WAITING_IO;

    if (deadline_ns != 0) {
        if (heap_push(s, f) != 0) {
            tlang_fd_forget(s, fd);
            if (f->waiting_fd2 >= 0) tlang_fd_forget(s, f->waiting_fd2);
            return TLANG_WAIT_ERR;
        }
    }

    s->current = NULL;
    tlang_fctx_switch(&f->ctx, &s->loop_ctx);
    /* Resumed. The waker forgot the fds and cleared the deadline. */
    s->current = f;
    f->state = FIBER_RUNNING;
    return f->wait_result;
}

int tlang_wait_fd(Fiber* f, int fd, uint32_t events, uint64_t deadline_ns) {
    return wait_fd_impl(f, fd, events, -1, deadline_ns);
}

int tlang_wait_fd2(Fiber* f, int fd, uint32_t events, int watch_fd, uint64_t deadline_ns) {
    return wait_fd_impl(f, fd, events, watch_fd, deadline_ns);
}

int tlang_sleep_until(Fiber* f, uint64_t deadline_ns) {
    tlang_sched* s = f->sched;

    if (f->interruptible && s->stopping) return TLANG_WAIT_CANCELLED;

    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    f->wait_events = 0;
    f->deadline_ns = deadline_ns;
    f->wait_result = TLANG_WAIT_TIMEOUT;
    f->state = FIBER_WAITING_IO;

    if (deadline_ns != 0) {
        if (heap_push(s, f) != 0) {
            /* Cannot track the deadline: ready immediately as a timeout. */
            f->state = FIBER_RUNNING;
            return TLANG_WAIT_TIMEOUT;
        }
    }

    s->current = NULL;
    tlang_fctx_switch(&f->ctx, &s->loop_ctx);
    s->current = f;
    f->state = FIBER_RUNNING;
    return f->wait_result;
}

void tlang_yield(Fiber* f) {
    tlang_sched* s = f->sched;
    f->deadline_ns = 0;
    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    tlang_sched_ready(s, f);
    s->current = NULL;
    tlang_fctx_switch(&f->ctx, &s->loop_ctx);
    s->current = f;
    f->state = FIBER_RUNNING;
}

/* ------------------------------------------------------------------ */
/* Wait queues (direct hand-off FIFO)                                 */
/* ------------------------------------------------------------------ */
void tlang_waitq_init(tlang_waitq* q) {
    q->head = q->tail = NULL;
    q->len = 0;
}

static void waitq_unlink(tlang_waitq* q, Fiber* f) {
    if (f->wq_prev != NULL) f->wq_prev->wq_next = f->wq_next;
    else q->head = f->wq_next;
    if (f->wq_next != NULL) f->wq_next->wq_prev = f->wq_prev;
    else q->tail = f->wq_prev;
    f->wq_prev = f->wq_next = NULL;
    f->waitq = NULL;
    if (q->len > 0) q->len--;
}

int tlang_waitq_wait(tlang_waitq* q, Fiber* f, uint64_t deadline_ns, void** value) {
    tlang_sched* s = f->sched;
    int res;

    /* Append at the tail. */
    f->wq_prev = q->tail;
    f->wq_next = NULL;
    if (q->tail != NULL) q->tail->wq_next = f;
    else q->head = f;
    q->tail = f;
    q->len++;
    f->waitq = q;

    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    f->wait_events = 0;
    f->deadline_ns = deadline_ns;
    f->wake_value = NULL;
    f->wait_result = TLANG_WAIT_OK;
    f->state = FIBER_WAITING_IO;

    if (deadline_ns != 0) {
        if (heap_push(s, f) != 0) {
            waitq_unlink(q, f);
            f->state = FIBER_RUNNING;
            return TLANG_WAIT_TIMEOUT;
        }
    }

    s->current = NULL;
    tlang_fctx_switch(&f->ctx, &s->loop_ctx);
    s->current = f;
    f->state = FIBER_RUNNING;

    res = f->wait_result;
    if (res == TLANG_WAIT_OK && value != NULL) *value = f->wake_value;
    return res;
}

bool tlang_waitq_wake_one(tlang_waitq* q, void* value) {
    Fiber* f = q->head;
    if (f == NULL) return false;
    waitq_unlink(q, f);
    if (f->heap_index != 0) heap_remove(f->sched, f);
    f->wake_value = value;
    f->wait_result = TLANG_WAIT_OK;
    tlang_sched_ready(f->sched, f);
    return true;
}

void tlang_waitq_cancel_all(tlang_waitq* q) {
    while (q->head != NULL) {
        Fiber* f = q->head;
        waitq_unlink(q, f);
        if (f->heap_index != 0) heap_remove(f->sched, f);
        f->wait_result = TLANG_WAIT_CANCELLED;
        tlang_sched_ready(f->sched, f);
    }
}

/* ------------------------------------------------------------------ */
/* Waking parked fibers                                               */
/* ------------------------------------------------------------------ */

/* Wakes f that was parked on an fd, handing it `mask` or a negative code. */
static void wake_fd_waiter(tlang_sched* s, Fiber* f, int result) {
    if (f->waiting_fd >= 0) tlang_fd_forget(s, f->waiting_fd);
    if (f->waiting_fd2 >= 0) tlang_fd_forget(s, f->waiting_fd2);
    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    if (f->heap_index != 0) heap_remove(s, f);
    f->deadline_ns = 0;
    f->wait_result = result;
    tlang_sched_ready(s, f);
}

/* Computes the result mask/code for an fd event delivered on `fd`. */
static int fd_event_result(Fiber* f, int fd, uint32_t revents) {
    if (f->waiting_fd2 == fd) {
        /* The watched fd fired: peer gone. */
        return TLANG_WAIT_PEER_GONE;
    }
    /* The primary fd. */
    if (revents & (f->wait_events & (EPOLLIN | EPOLLOUT))) {
        return (int)revents;
    }
    if (revents & (EPOLLIN | EPOLLOUT)) {
        /* A readiness we did not request (shouldn't happen under ONESHOT), but
         * still return the mask so the caller does the I/O. */
        return (int)revents;
    }
    if (revents & (EPOLLRDHUP | EPOLLHUP)) return TLANG_WAIT_HUP;
    if (revents & EPOLLERR) return TLANG_WAIT_ERR;
    return (int)revents;
}

/* ------------------------------------------------------------------ */
/* The event loop                                                     */
/* ------------------------------------------------------------------ */

/* Computes the epoll_wait timeout in milliseconds from the deadline heap. */
static int compute_timeout_ms(tlang_sched* s) {
    uint64_t earliest, now;
    if (s->heap_len == 0) return -1;  /* block indefinitely */
    earliest = s->heap[0]->deadline_ns;
    now = s->now_ns;
    if (earliest <= now) return 0;
    {
        uint64_t diff = earliest - now;
        uint64_t ms = (diff + 999999ull) / 1000000ull;
        if (ms > 2147483647ull) ms = 2147483647ull;
        return (int)ms;
    }
}

/* Wakes every fiber whose deadline has passed. */
static void fire_deadlines(tlang_sched* s) {
    while (s->heap_len > 0 && s->heap[0]->deadline_ns <= s->now_ns) {
        Fiber* f = s->heap[0];
        heap_remove(s, f);
        if (f->waitq != NULL) {
            /* Timed out while parked on a wait queue. */
            tlang_waitq* q = f->waitq;
            /* Unlink from the queue. */
            if (f->wq_prev != NULL) f->wq_prev->wq_next = f->wq_next;
            else q->head = f->wq_next;
            if (f->wq_next != NULL) f->wq_next->wq_prev = f->wq_prev;
            else q->tail = f->wq_prev;
            f->wq_prev = f->wq_next = NULL;
            f->waitq = NULL;
            if (q->len > 0) q->len--;
            f->wait_result = TLANG_WAIT_TIMEOUT;
            f->deadline_ns = 0;
            tlang_sched_ready(s, f);
        } else {
            if (f->waiting_fd >= 0) tlang_fd_forget(s, f->waiting_fd);
            if (f->waiting_fd2 >= 0) tlang_fd_forget(s, f->waiting_fd2);
            f->waiting_fd = -1;
            f->waiting_fd2 = -1;
            f->deadline_ns = 0;
            f->wait_result = TLANG_WAIT_TIMEOUT;
            tlang_sched_ready(s, f);
        }
    }
}

/* Cancels interruptible fd/sleep waits on a stop request (not wait queues:
 * those are handled by the DB pool via tlang_waitq_cancel_all). */
static void cancel_interruptible(tlang_sched* s) {
    /* Walk the deadline heap and the fd table for interruptible waiters. The
     * fd table is authoritative for fd waits; sleeps without an fd are only in
     * the heap. */
    size_t i;
    /* fd waiters */
    for (i = 0; i < s->nfds; i++) {
        Fiber* f = s->fds[i].waiter;
        if (f != NULL && f->state == FIBER_WAITING_IO && f->interruptible &&
            f->waiting_fd == (int)i) {
            wake_fd_waiter(s, f, TLANG_WAIT_CANCELLED);
        }
    }
    /* interruptible sleeps (in the heap, no fd, no waitq) */
    i = 0;
    while (i < s->heap_len) {
        Fiber* f = s->heap[i];
        if (f->interruptible && f->waiting_fd < 0 && f->waitq == NULL) {
            heap_remove(s, f);
            f->deadline_ns = 0;
            f->wait_result = TLANG_WAIT_CANCELLED;
            tlang_sched_ready(s, f);
            i = 0;  /* heap reshuffled */
        } else {
            i++;
        }
    }
}

int tlang_sched_run(tlang_sched* s) {
    struct epoll_event events[256];
    uint64_t stop_deadline = 0;

    s->now_ns = tlang_now_ns();

    for (;;) {
        int n, i, timeout;
        Fiber* f;

        /* Run every ready fiber. */
        while ((f = runq_pop(s)) != NULL) {
            s->current = f;
            f->state = FIBER_RUNNING;
            tlang_fctx_switch(&s->loop_ctx, &f->ctx);
            s->current = NULL;
        }

        /* Termination: stopping and no fiber live, or past the shutdown
         * deadline, or no listener and nothing to do. */
        if (s->stopping) {
            if (s->nlive == 0) break;
            if (stop_deadline != 0 && s->now_ns >= stop_deadline) break;
        } else if (s->listen_fd < 0 && s->nlive == 0 && s->runq_head == NULL &&
                   s->heap_len == 0) {
            /* Script mode: the only fiber finished. */
            break;
        }

        timeout = compute_timeout_ms(s);
        if (s->stopping && stop_deadline != 0) {
            uint64_t now = s->now_ns;
            int left = stop_deadline <= now ? 0
                     : (int)((stop_deadline - now + 999999ull) / 1000000ull);
            if (timeout < 0 || left < timeout) timeout = left;
        }

        n = epoll_wait(s->epfd, events, (int)(sizeof events / sizeof events[0]), timeout);
        s->now_ns = tlang_now_ns();
        if (n < 0) {
            if (errno == EINTR) continue;
            tlang_log_error("epoll_wait failed: %s", strerror(errno));
            s->exit_code = 1;
            break;
        }

        for (i = 0; i < n; i++) {
            int fd = events[i].data.fd;
            uint32_t re = events[i].events;

            if (fd == s->wake_fd) {
                uint64_t drain;
                while (read(s->wake_fd, &drain, sizeof drain) > 0) { }
                if (!s->stopping) {
                    s->stopping = true;
                    /* Close the listener first. */
                    if (s->listen_fd >= 0) {
                        tlang_fd_forget(s, s->listen_fd);
                        close(s->listen_fd);
                        s->listen_fd = -1;
                        s->accept_armed = false;
                    }
                    cancel_interruptible(s);
                    stop_deadline = s->cfg->shutdown_timeout_ms == 0
                        ? s->now_ns
                        : s->now_ns + (uint64_t)s->cfg->shutdown_timeout_ms * 1000000ull;
                }
                continue;
            }

            if (fd == s->listen_fd) {
                /* Level-triggered: accept until EAGAIN. */
                tlang_net_accept_ready(s);
                continue;
            }

            /* Validate against the fd table (drop stale one-shot events). */
            if ((size_t)fd < s->nfds && s->fds[fd].registered &&
                s->fds[fd].waiter != NULL) {
                Fiber* w = s->fds[fd].waiter;
                int result;
                if (w->waiting_fd != fd && w->waiting_fd2 != fd) {
                    /* Stale: the waiter moved on. */
                    continue;
                }
                result = fd_event_result(w, fd, re);
                wake_fd_waiter(s, w, result);
            }
        }

        fire_deadlines(s);
    }

    return s->exit_code;
}
