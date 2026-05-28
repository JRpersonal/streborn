/*
 * STR LD_PRELOAD shim for taigan / SoundTouch Portable.
 *
 * Hooks accept() and accept4() on the host process. When the accepted
 * fd belongs to a TCP listener on the hijack port (17008 by default,
 * SoftwareUpdate's slot, dead post-cloud), the shim transparently
 * forwards the new connection to STR_TARGET (127.0.0.1:8888 — the STR
 * webui) and tells the host process accept() saw nothing (EAGAIN).
 *
 * Why this exists: the BCO wifi chipset firmware only routes inbound
 * external TCP to listeners bound by binaries linked against Bose's
 * libProtobufMessagingIPC / libIPC / libSoundTouchInternal. Custom
 * binaries get RST'd at the chipset level even with the same port.
 * Running SoftwareUpdate under this LD_PRELOAD keeps SoftwareUpdate
 * (and therefore the chipset whitelist slot) alive while STR
 * effectively owns the connection content.
 *
 * The shim depends only on glibc-2.x basics so it loads cleanly on
 * the box's 2.15 runtime.
 */

#define _GNU_SOURCE
#include <arpa/inet.h>
#include <dlfcn.h>
#include <errno.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

#define STR_TARGET_HOST "127.0.0.1"
#define STR_TARGET_PORT 8888
#define HIJACK_PORT     17008
#define LOG_PATH        "/tmp/str-shim.log"

static FILE *logfp = NULL;
static pthread_mutex_t log_mu = PTHREAD_MUTEX_INITIALIZER;

static void logf(const char *fmt, ...) {
    if (!logfp) {
        pthread_mutex_lock(&log_mu);
        if (!logfp) {
            logfp = fopen(LOG_PATH, "a");
            if (logfp) setvbuf(logfp, NULL, _IOLBF, 0);
        }
        pthread_mutex_unlock(&log_mu);
    }
    if (!logfp) return;
    va_list ap;
    time_t t = time(NULL);
    char ts[32];
    strftime(ts, sizeof(ts), "%Y-%m-%dT%H:%M:%S", gmtime(&t));
    pthread_mutex_lock(&log_mu);
    fprintf(logfp, "%s shim ", ts);
    va_start(ap, fmt);
    vfprintf(logfp, fmt, ap);
    va_end(ap);
    fputc('\n', logfp);
    fflush(logfp);
    pthread_mutex_unlock(&log_mu);
}

static int listener_port(int fd) {
    struct sockaddr_in addr;
    socklen_t len = sizeof(addr);
    if (getsockname(fd, (struct sockaddr *)&addr, &len) < 0) return -1;
    if (addr.sin_family != AF_INET) return -1;
    return ntohs(addr.sin_port);
}

struct fwd { int a; int b; };

static void *fwd_thread(void *arg) {
    struct fwd *f = (struct fwd *)arg;
    int a = f->a, b = f->b;
    free(f);
    char buf[8192];
    while (1) {
        ssize_t n = read(a, buf, sizeof(buf));
        if (n <= 0) break;
        ssize_t w = 0;
        while (w < n) {
            ssize_t k = write(b, buf + w, n - w);
            if (k <= 0) goto done;
            w += k;
        }
    }
done:
    shutdown(a, SHUT_RD);
    shutdown(b, SHUT_WR);
    return NULL;
}

static void hijack(int cfd) {
    int proxy = socket(AF_INET, SOCK_STREAM, 0);
    if (proxy < 0) {
        logf("hijack: socket() failed errno=%d", errno);
        close(cfd);
        return;
    }
    struct sockaddr_in t;
    memset(&t, 0, sizeof(t));
    t.sin_family = AF_INET;
    t.sin_port = htons(STR_TARGET_PORT);
    t.sin_addr.s_addr = inet_addr(STR_TARGET_HOST);
    if (connect(proxy, (struct sockaddr *)&t, sizeof(t)) < 0) {
        logf("hijack: connect %s:%d failed errno=%d", STR_TARGET_HOST, STR_TARGET_PORT, errno);
        close(proxy);
        close(cfd);
        return;
    }
    pthread_t t1, t2;
    struct fwd *f1 = (struct fwd *)malloc(sizeof(*f1));
    struct fwd *f2 = (struct fwd *)malloc(sizeof(*f2));
    f1->a = cfd;   f1->b = proxy;
    f2->a = proxy; f2->b = cfd;
    pthread_create(&t1, NULL, fwd_thread, f1);
    pthread_create(&t2, NULL, fwd_thread, f2);
    pthread_detach(t1);
    pthread_detach(t2);
    logf("hijack: cfd=%d proxy=%d threads spawned", cfd, proxy);
}

typedef int (*accept_fn_t)(int, struct sockaddr *, socklen_t *);
typedef int (*accept4_fn_t)(int, struct sockaddr *, socklen_t *, int);

int accept(int sockfd, struct sockaddr *addr, socklen_t *addrlen) {
    static accept_fn_t real = NULL;
    if (!real) real = (accept_fn_t)dlsym(RTLD_NEXT, "accept");
    int cfd = real(sockfd, addr, addrlen);
    if (cfd < 0) return cfd;
    int port = listener_port(sockfd);
    if (port == HIJACK_PORT) {
        hijack(cfd);
        errno = EAGAIN;
        return -1;
    }
    return cfd;
}

int accept4(int sockfd, struct sockaddr *addr, socklen_t *addrlen, int flags) {
    static accept4_fn_t real = NULL;
    if (!real) real = (accept4_fn_t)dlsym(RTLD_NEXT, "accept4");
    int cfd = real(sockfd, addr, addrlen, flags);
    if (cfd < 0) return cfd;
    int port = listener_port(sockfd);
    if (port == HIJACK_PORT) {
        hijack(cfd);
        errno = EAGAIN;
        return -1;
    }
    return cfd;
}

__attribute__((constructor)) static void shim_init(void) {
    logf("loaded shim, hijack=:%d -> %s:%d pid=%d", HIJACK_PORT, STR_TARGET_HOST, STR_TARGET_PORT, getpid());
}
