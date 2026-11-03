/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * Probe: can cross-process cuMem (VMM) sharing survive cuda-checkpoint if
 * the importer releases its mapping before checkpoint and re-maps it at
 * the SAME virtual address after restore?
 *
 * Two processes: E (exporter) allocates with cuMemCreate (POSIX fd handle)
 * on device de, fills it, and sends the fd to I (importer, device di),
 * which maps it — the same pattern NCCL uses with NCCL_CUMEM_ENABLE=1.
 *
 *   A. (only with "a" as 4th arg) both mapped → full-save + full-restore:
 *      restore of I fails NOT_SUPPORTED and leaves I unusable, so A ends
 *      the run.
 *   B. I unmaps + releases (keeps the VA reservation)
 *                            → full-save E I, full-restore E I,
 *      E re-exports, I re-imports at the old VA, checks the data and that
 *      sharing is live again (E writes, I reads).
 *   C. if B fails: checkpoint E alone and I alone, to see which side fails.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_cumem_release \
 *            test_cumem_release.c -lcuda
 * Run:   ./test_cumem_release <nvsnap-gpu-suspend> [de=0] [di=1] [a]
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

#define SIZE (64UL << 20)
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

static void send_fd(int sock, int fd)
{
    char c = 0, buf[CMSG_SPACE(sizeof(int))] = {0};
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    struct cmsghdr *h = CMSG_FIRSTHDR(&m);
    h->cmsg_level = SOL_SOCKET; h->cmsg_type = SCM_RIGHTS; h->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(h), &fd, sizeof(int));
    if (sendmsg(sock, &m, 0) != 1) { perror("sendmsg"); exit(2); }
}

static int recv_fd(int sock)
{
    char c, buf[CMSG_SPACE(sizeof(int))];
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    int fd;
    if (recvmsg(sock, &m, 0) != 1) { perror("recvmsg"); exit(2); }
    memcpy(&fd, CMSG_DATA(CMSG_FIRSTHDR(&m)), sizeof(int));
    return fd;
}

/* Command channel: parent sends one char + one uint32 arg, child replies one int. */
struct chan { int cmd[2], rep[2]; };
static void put(int fd, const void *p, size_t n) { if (write(fd, p, n) != (ssize_t)n) exit(2); }
static void get(int fd, void *p, size_t n) { if (read(fd, p, n) != (ssize_t)n) exit(2); }

static int call(struct chan *c, char op, uint32_t arg)
{
    int r;
    put(c->cmd[1], &op, 1); put(c->cmd[1], &arg, 4); get(c->rep[0], &r, 4);
    return r;
}

static CUmemAllocationProp prop_for(int dev)
{
    CUmemAllocationProp p = {0};
    p.type = CU_MEM_ALLOCATION_TYPE_PINNED;
    p.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    p.location.id = dev;
    p.requestedHandleTypes = CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
    return p;
}

static void map_rw(CUdeviceptr va, CUmemGenericAllocationHandle h, int dev)
{
    CUmemAccessDesc a = { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
    CK(cuMemMap(va, SIZE, 0, h, 0));
    CK(cuMemSetAccess(va, SIZE, &a, 1));
}

/* Returns 0 if every word of va == want. */
static int check(CUdeviceptr va, uint32_t want)
{
    uint32_t *h = malloc(SIZE);
    int bad = 0;
    CK(cuMemcpyDtoH(h, va, SIZE));
    for (size_t i = 0; i < SIZE / 4; i++)
        if (h[i] != want) { bad = 1; break; }
    free(h);
    return bad;
}

/* E: 'x' export fd → sock, 'w' write arg, 'c' check arg */
static void exporter(struct chan *c, int sock, int dev)
{
    CUdevice d; CUcontext ctx; CUdeviceptr va; CUmemGenericAllocationHandle h;
    CUmemAllocationProp p = prop_for(dev);
    CK(cuInit(0)); CK(cuDeviceGet(&d, dev)); CK(cuDevicePrimaryCtxRetain(&ctx, d)); CK(cuCtxSetCurrent(ctx));
    CK(cuMemCreate(&h, SIZE, &p, 0));
    CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
    map_rw(va, h, dev);
    for (;;) {
        char op; uint32_t arg; int r = 0, fd;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'x':
            CK(cuMemExportToShareableHandle(&fd, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0));
            send_fd(sock, fd); close(fd); break;
        case 'w': CK(cuMemsetD32(va, arg, SIZE / 4)); CK(cuCtxSynchronize()); break;
        case 'c': r = check(va, arg); break;
        case 'q': put(c->rep[1], &r, 4); exit(0);
        }
        put(c->rep[1], &r, 4);
    }
}

/* I: 'm' import fd from sock + map at the reserved VA, 'r' unmap + release,
 * 'c' check arg, 'a' report VA (low 32 bits). */
static void importer(struct chan *c, int sock, int dev, int edev)
{
    CUdevice d; CUcontext ctx; CUdeviceptr va; CUmemGenericAllocationHandle h = 0;
    CK(cuInit(0)); CK(cuDeviceGet(&d, dev)); CK(cuDevicePrimaryCtxRetain(&ctx, d)); CK(cuCtxSetCurrent(ctx));
    CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
    for (;;) {
        char op; uint32_t arg; int r = 0, fd;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'm':
            fd = recv_fd(sock);
            CK(cuMemImportFromShareableHandle(&h, (void *)(uintptr_t)fd, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR));
            close(fd);
            map_rw(va, h, dev);
            break;
        case 'r': CK(cuMemUnmap(va, SIZE)); CK(cuMemRelease(h)); h = 0; break;
        case 'c': r = check(va, arg); break;
        case 'a': r = (int)(uint32_t)va; break;
        case 'q': put(c->rep[1], &r, 4); exit(0);
        }
        put(c->rep[1], &r, 4);
    }
    (void)edev;
}

static const char *tool;
static int run(const char *action, pid_t a, pid_t b)
{
    char cmd[512];
    if (b) snprintf(cmd, sizeof cmd, "%s %s %d %d", tool, action, a, b);
    else   snprintf(cmd, sizeof cmd, "%s %s %d", tool, action, a);
    printf("$ %s\n", cmd); fflush(stdout);
    int st = system(cmd);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 1, fails = 0;
    struct chan ce, ci;
    int sv[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || socketpair(AF_UNIX, SOCK_DGRAM, 0, sv)) return 1;
    pid_t pe = fork();
    if (!pe) exporter(&ce, sv[0], de);
    pid_t pi = fork();
    if (!pi) importer(&ci, sv[1], di, de);
    printf("exporter pid %d (dev %d), importer pid %d (dev %d)\n", pe, de, pi, di);

    call(&ce, 'w', 0x11111111);
    call(&ce, 'x', 0); call(&ci, 'm', 0);
    int va0 = call(&ci, 'a', 0);
    EXPECT(call(&ci, 'c', 0x11111111) == 0, "importer sees exporter's data");

    if (argc > 4 && !strcmp(argv[4], "a")) {
        printf("A. checkpoint with the import mapped\n");
        printf("  full-save rc=%d\n", run("full-save", pe, pi));
        printf("  full-restore rc=%d\n", run("full-restore", pe, pi));
        kill(pe, SIGKILL); kill(pi, SIGKILL);
        return 0;
    }

    printf("B. importer releases its mapping, keeps the VA reservation\n");
    call(&ci, 'r', 0);
    int b = run("full-save", pe, pi);
    EXPECT(b == 0, "full-save E I after release");
    if (b == 0) {
        EXPECT(run("full-restore", pe, pi) == 0, "full-restore E I");
        EXPECT(call(&ce, 'c', 0x11111111) == 0, "exporter data intact after restore");
        call(&ce, 'x', 0); call(&ci, 'm', 0);
        EXPECT(call(&ci, 'a', 0) == va0, "re-mapped at the same VA");
        EXPECT(call(&ci, 'c', 0x11111111) == 0, "importer sees data after re-map");
        call(&ce, 'w', 0x22222222);
        EXPECT(call(&ci, 'c', 0x22222222) == 0, "sharing live: exporter write visible to importer");
    } else {
        printf("C. which side fails?\n");
        int ri = run("full-save", pi, 0);
        printf("  importer alone: rc=%d\n", ri); if (!ri) run("full-restore", pi, 0);
        int re = run("full-save", pe, 0);
        printf("  exporter alone: rc=%d\n", re); if (!re) run("full-restore", pe, 0);
    }
    call(&ce, 'q', 0); call(&ci, 'q', 0);
    waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}
