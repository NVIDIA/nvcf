/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * A 512 MiB cuMem allocation shared as a POSIX fd must survive suspend and
 * resume. Driver 610 cannot export an allocation of 512 MiB or more that a
 * restored process created before its checkpoint, so the importer's
 * re-import fails unless the shim replaces the allocation (FlashInfer's
 * all-reduce workspace is such an allocation).
 *
 * E creates the allocation, fills it and sends its fd to I, which imports
 * and maps it. suspend + resume both. Afterwards I must see E's data,
 * sharing must be live (E writes, I reads), and E's own handle must still
 * export, and unmap and release cleanly.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_large_export test_large_export.c \
 *            -L/usr/local/cuda/lib64/stubs -lcuda
 * Run:   LD_PRELOAD=.../libnvsnap_gpushare.so ./test_large_export <nvsnap-gpu-suspend> [de=0] [di=1]
 */
#include <cuda.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE (512UL << 20)
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

static int send_fd(int s, int fd)
{
    char b = 0, cb[CMSG_SPACE(sizeof(int))];
    struct iovec io = { &b, 1 };
    struct msghdr m = { .msg_iov = &io, .msg_iovlen = 1, .msg_control = cb, .msg_controllen = sizeof(cb) };
    struct cmsghdr *c = CMSG_FIRSTHDR(&m);
    c->cmsg_level = SOL_SOCKET;
    c->cmsg_type = SCM_RIGHTS;
    c->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(c), &fd, sizeof(int));
    return sendmsg(s, &m, 0) == 1 ? 0 : -1;
}

static int recv_fd(int s)
{
    char b, cb[CMSG_SPACE(sizeof(int))];
    int fd = -1;
    struct iovec io = { &b, 1 };
    struct msghdr m = { .msg_iov = &io, .msg_iovlen = 1, .msg_control = cb, .msg_controllen = sizeof(cb) };
    struct cmsghdr *c;
    if (recvmsg(s, &m, 0) == 1 && (c = CMSG_FIRSTHDR(&m))) memcpy(&fd, CMSG_DATA(c), sizeof(int));
    return fd;
}

/* Is all of [va, va+SIZE) equal to v? */
static int check(CUdeviceptr va, uint32_t v)
{
    uint32_t *h = malloc(SIZE);
    int ok = 1;
    CK(cuMemcpyDtoH(h, va, SIZE));
    for (size_t i = 0; ok && i < SIZE / 4; i++) ok = h[i] == v;
    free(h);
    return ok;
}

/* Commands on cmd ("<op><u32 arg>"), one reply byte (0 = ok) on rep:
 * 's' set up (E: create and send the fd; I: receive, import, map),
 * 'w' write arg, 'r' check arg, 'x' E: export again, unmap, release. */
static void child(int exporter, int dev, int cmd, int rep, int sock)
{
    CUdevice d;
    CUcontext ctx;
    CUmemGenericAllocationHandle h;
    CUdeviceptr va = 0;
    char m[5];
    CK(cuInit(0));
    CK(cuDeviceGet(&d, dev));
    CK(cuDevicePrimaryCtxRetain(&ctx, d));
    CK(cuCtxSetCurrent(ctx));
    while (read(cmd, m, 5) == 5) {
        uint32_t arg;
        char r = 0;
        memcpy(&arg, m + 1, 4);
        if (m[0] == 's') {
            CUmemAccessDesc a = { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
            if (exporter) {
                CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED,
                    .location = { CU_MEM_LOCATION_TYPE_DEVICE, dev },
                    .requestedHandleTypes = CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR };
                int fd;
                CK(cuMemCreate(&h, SIZE, &p, 0));
                CK(cuMemExportToShareableHandle(&fd, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0));
                r = send_fd(sock, fd) != 0;
                close(fd);
            } else {
                int fd = recv_fd(sock);
                CK(cuMemImportFromShareableHandle(&h, (void *)(uintptr_t)fd, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR));
                close(fd);
            }
            CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
            CK(cuMemMap(va, SIZE, 0, h, 0));
            CK(cuMemSetAccess(va, SIZE, &a, 1));
        } else if (m[0] == 'w') {
            CK(cuMemsetD32(va, arg, SIZE / 4));
            CK(cuCtxSynchronize());
        } else if (m[0] == 'r') {
            r = !check(va, arg);
        } else if (m[0] == 'x') {
            int fd = -1;
            if (cuMemExportToShareableHandle(&fd, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0) != CUDA_SUCCESS) r = 1;
            if (fd >= 0) close(fd);
            if (cuMemUnmap(va, SIZE) != CUDA_SUCCESS || cuMemRelease(h) != CUDA_SUCCESS) r = 2;
            CK(cuMemAddressFree(va, SIZE));
        }
        if (write(rep, &r, 1) != 1) exit(2);
    }
    exit(0);
}

static struct proc { pid_t pid; int cmd, rep; } E, I;

static int call(struct proc *p, char op, uint32_t arg)
{
    char m[5] = { op }, r = 1;
    memcpy(m + 1, &arg, 4);
    if (write(p->cmd, m, 5) != 5 || read(p->rep, &r, 1) != 1) return -1;
    return r;
}

static struct proc spawn(int exporter, int dev, int sock, int other)
{
    int c[2], r[2];
    struct proc p = { 0 };
    if (pipe(c) || pipe(r) || (p.pid = fork()) < 0) { perror("spawn"); exit(1); }
    if (!p.pid) {
        close(c[1]); close(r[0]); close(other);
        child(exporter, dev, c[0], r[1], sock);
    }
    close(c[0]); close(r[1]);
    p.cmd = c[1];
    p.rep = r[0];
    return p;
}

static const char *tool;
static int run(const char *op)
{
    char c[1024];
    snprintf(c, sizeof c, "%s %s %d %d", tool, op, E.pid, I.pid);
    printf("$ %s\n", c);
    int st = system(c);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

static int fails;
#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); \
    if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 1, sv[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (socketpair(AF_UNIX, SOCK_DGRAM, 0, sv)) return 1;
    E = spawn(1, de, sv[0], sv[1]);
    I = spawn(0, di, sv[1], sv[0]);
    close(sv[0]);
    close(sv[1]);
    printf("exporter %d (gpu %d), importer %d (gpu %d)\n", E.pid, de, I.pid, di);

    EXPECT(call(&E, 's', 0) == 0 && call(&I, 's', 0) == 0, "512 MiB allocation shared as an fd");
    EXPECT(call(&E, 'w', 0x11111111) == 0 && call(&I, 'r', 0x11111111) == 0, "importer sees exporter's data");
    if (!fails) EXPECT(run("suspend") == 0, "suspend");
    if (!fails) EXPECT(run("resume") == 0, "resume (importer re-imports)");
    if (!fails) EXPECT(call(&I, 'r', 0x11111111) == 0, "importer sees the data after resume");
    if (!fails) EXPECT(call(&E, 'w', 0x22222222) == 0 && call(&I, 'r', 0x22222222) == 0, "sharing is live");
    if (!fails) EXPECT(call(&E, 'x', 0) == 0, "exporter's handle exports, unmaps and releases");

    close(E.cmd);
    close(I.cmd);
    if (fails) { kill(E.pid, SIGKILL); kill(I.pid, SIGKILL); }
    waitpid(E.pid, NULL, 0);
    waitpid(I.pid, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}
