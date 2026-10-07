/*
 * net.c - listening sockets, the accept loop and non-blocking socket I/O.
 * docs/RUNTIME.md §4/§6 assigns these functions here; the contracts are in
 * tlang_internal.h §7. All waits go through the scheduler (sched.c): a
 * non-blocking syscall that returns EAGAIN parks the fiber until the fd is
 * ready or the deadline passes. SIGPIPE is ignored process-wide by
 * tlang_main, so a write to a closed peer returns EPIPE instead of a signal.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <netdb.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>

/* ------------------------------------------------------------------ *
 * Listener setup
 * ------------------------------------------------------------------ */

int tlang_net_listen(const char* host, int port, char* err, size_t errlen) {
    char portbuf[16];
    snprintf(portbuf, sizeof portbuf, "%d", port);

    struct addrinfo hints;
    memset(&hints, 0, sizeof hints);
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    hints.ai_protocol = IPPROTO_TCP;
    hints.ai_flags = AI_PASSIVE | AI_NUMERICSERV;

    struct addrinfo* res = NULL;
    int rc = getaddrinfo((host != NULL && host[0] != '\0') ? host : NULL,
                         portbuf, &hints, &res);
    if (rc != 0) {
        if (err != NULL && errlen > 0)
            snprintf(err, errlen, "cannot resolve %s:%d: %s",
                     host != NULL ? host : "", port, gai_strerror(rc));
        return -1;
    }

    int fd = -1;
    int saved_errno = 0;
    for (struct addrinfo* ai = res; ai != NULL; ai = ai->ai_next) {
        fd = socket(ai->ai_family, ai->ai_socktype | SOCK_NONBLOCK | SOCK_CLOEXEC,
                    ai->ai_protocol);
        if (fd < 0) {
            saved_errno = errno;
            continue;
        }

        int one = 1;
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
#ifdef SO_REUSEPORT
        setsockopt(fd, SOL_SOCKET, SO_REUSEPORT, &one, sizeof one);
#endif
        if (ai->ai_family == AF_INET6) {
            int off = 0;
            setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &off, sizeof off);
        }

        if (bind(fd, ai->ai_addr, ai->ai_addrlen) == 0 &&
            listen(fd, SOMAXCONN) == 0) {
            saved_errno = 0;
            break;
        }
        saved_errno = errno;
        close(fd);
        fd = -1;
    }

    freeaddrinfo(res);

    if (fd < 0) {
        if (err != NULL && errlen > 0)
            snprintf(err, errlen, "cannot bind %s:%d: %s",
                     host != NULL ? host : "", port,
                     saved_errno != 0 ? strerror(saved_errno) : "no address");
        return -1;
    }
    return fd;
}

int tlang_net_local_port(int fd) {
    struct sockaddr_storage ss;
    socklen_t len = sizeof ss;
    if (getsockname(fd, (struct sockaddr*)&ss, &len) != 0) return -1;
    if (ss.ss_family == AF_INET)
        return ntohs(((struct sockaddr_in*)&ss)->sin_port);
    if (ss.ss_family == AF_INET6)
        return ntohs(((struct sockaddr_in6*)&ss)->sin6_port);
    return -1;
}

/* ------------------------------------------------------------------ *
 * Accept loop
 * ------------------------------------------------------------------ */

void tlang_net_accept_ready(tlang_sched* s) {
    for (;;) {
        /* Stop before accepting when the fiber pool is exhausted: the
         * listener is disarmed until a fiber frees (DESIGN §4.1). */
        if (s->nlive >= s->cfg->max_fibers) {
            tlang_sched_pause_accept(s);
            return;
        }

        int fd = accept4(s->listen_fd, NULL, NULL, SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (fd < 0) {
            if (errno == EINTR) continue;
            if (errno == EAGAIN || errno == EWOULDBLOCK) return;
            /* ECONNABORTED and friends: drop this one, keep accepting. */
            if (errno == ECONNABORTED) continue;
            return;
        }

        int one = 1;
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);

        Fiber* f = tlang_spawn(s, tlang_http_conn_main, (void*)(intptr_t)fd);
        if (f == NULL) {
            /* Could not get a fiber: close this connection and pause accept so
             * the loop stops hammering the listener until a fiber frees. */
            close(fd);
            tlang_sched_pause_accept(s);
            return;
        }
    }
}

/* ------------------------------------------------------------------ *
 * Non-blocking I/O with scheduler parking
 * ------------------------------------------------------------------ */

ssize_t tlang_net_read(Fiber* f, int fd, void* buf, size_t len, uint64_t deadline_ns) {
    for (;;) {
        ssize_t n = read(fd, buf, len);
        if (n >= 0) return n;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            int w = tlang_wait_fd(f, fd, EPOLLIN, deadline_ns);
            if (w < 0) return w;        /* TIMEOUT / CANCELLED / HUP / ERR */
            continue;                   /* ready (possibly with HUP): retry */
        }
        if (errno == EPIPE || errno == ECONNRESET) return TLANG_IO_ERR;
        return TLANG_IO_ERR;
    }
}

ssize_t tlang_net_write_all(Fiber* f, int fd, const void* buf, size_t len,
                            uint64_t deadline_ns) {
    const char* p = (const char*)buf;
    size_t off = 0;
    while (off < len) {
        ssize_t n = write(fd, p + off, len - off);
        if (n > 0) {
            off += (size_t)n;
            continue;
        }
        if (n == 0) continue;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            int w = tlang_wait_fd(f, fd, EPOLLOUT, deadline_ns);
            if (w < 0) return w;
            continue;
        }
        if (errno == EPIPE || errno == ECONNRESET) return TLANG_IO_ERR;
        return TLANG_IO_ERR;
    }
    return (ssize_t)len;
}

ssize_t tlang_net_writev_all(Fiber* f, int fd, struct iovec* iov, int iovcnt,
                             uint64_t deadline_ns) {
    size_t total = 0;
    for (int i = 0; i < iovcnt; i++) total += iov[i].iov_len;

    size_t written = 0;
    int idx = 0;
    while (idx < iovcnt) {
        /* Skip fully-consumed leading iovecs. */
        while (idx < iovcnt && iov[idx].iov_len == 0) idx++;
        if (idx >= iovcnt) break;

        ssize_t n = writev(fd, iov + idx, iovcnt - idx);
        if (n > 0) {
            written += (size_t)n;
            size_t rem = (size_t)n;
            while (rem > 0 && idx < iovcnt) {
                if (iov[idx].iov_len <= rem) {
                    rem -= iov[idx].iov_len;
                    iov[idx].iov_len = 0;
                    iov[idx].iov_base = (char*)iov[idx].iov_base + 0;
                    idx++;
                } else {
                    iov[idx].iov_base = (char*)iov[idx].iov_base + rem;
                    iov[idx].iov_len -= rem;
                    rem = 0;
                }
            }
            continue;
        }
        if (n == 0) continue;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            int w = tlang_wait_fd(f, fd, EPOLLOUT, deadline_ns);
            if (w < 0) return w;
            continue;
        }
        if (errno == EPIPE || errno == ECONNRESET) return TLANG_IO_ERR;
        return TLANG_IO_ERR;
    }
    return (ssize_t)(total == written ? total : written);
}

void tlang_net_close(tlang_sched* s, int fd) {
    if (fd < 0) return;
    tlang_fd_forget(s, fd);
    close(fd);
}
