/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * cuMem host memory shared between processes across nvsnap-gpu-suspend
 * suspend/resume, with LD_PRELOAD=libnvsnap_gpushare.so.
 *
 * NCCL's SHM transport (between GPUs without P2P, e.g. across a PCIe host
 * bridge) shares its buffers this way: cuMemCreate on CU_MEM_LOCATION_TYPE_
 * HOST_NUMA, exported as a POSIX fd, mapped by both processes with access
 * for their GPU and for the CPU. The driver checkpoints such memory but
 * cannot restore it (CUDA_ERROR_UNKNOWN), so the shim must save it at
 * "release" and create it again at "load".
 *
 * E creates and fills it through the CPU and passes an fd to I, which
 * imports it. suspend + resume both. Afterwards both must read E's data
 * through the CPU and through their GPU, and sharing must be live (E writes
 * through the CPU, I reads).
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_host_shared \
 *            test_host_shared.c -lcuda
 * Run:   LD_PRELOAD=libnvsnap_gpushare.so ./test_host_shared <nvsnap-gpu-suspend> [de=0] [di=1] [cycles=2]
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

#define SIZE (4UL << 20)
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

struct chan { int cmd[2], rep[2]; };
static void put(int fd, const void *p, size_t n) { if (write(fd, p, n) != (ssize_t)n) exit(2); }
static void get(int fd, void *p, size_t n) { if (read(fd, p, n) != (ssize_t)n) exit(2); }

static uint64_t call(struct chan *c, char op, uint32_t arg)
{
    uint64_t r;
    put(c->cmd[1], &op, 1); put(c->cmd[1], &arg, 4); get(c->rep[0], &r, 8);
    return r;
}

static void send_fd(int sock, int fd)
{
    char c = 0, buf[CMSG_SPACE(sizeof(int))] = {0};
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    struct cmsghdr *h = CMSG_FIRSTHDR(&m);
    h->cmsg_level = SOL_SOCKET; h->cmsg_type = SCM_RIGHTS; h->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(h), &fd, sizeof(int));
    if (sendmsg(sock, &m, 0) != 1) exit(2);
}

static int recv_fd(int sock)
{
    char c, buf[CMSG_SPACE(sizeof(int))];
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    int fd;
    if (recvmsg(sock, &m, 0) != 1) exit(2);
    memcpy(&fd, CMSG_DATA(CMSG_FIRSTHDR(&m)), sizeof(int));
    return fd;
}

static CUdeviceptr va;
static int dev, numa;

/* Map h at a new VA with access for our GPU and for the CPU, as NCCL does. */
static void map(CUmemGenericAllocationHandle h)
{
    CUmemAccessDesc d[2] = { { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE },
                             { { CU_MEM_LOCATION_TYPE_HOST_NUMA, numa }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE } };
    CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
    CK(cuMemMap(va, SIZE, 0, h, 0));
    CK(cuMemSetAccess(va, SIZE, d, 2));
}

/* Words that are not v, read through the CPU and through the GPU. */
static uint64_t bad(uint32_t v)
{
    static uint32_t buf[SIZE / 4];
    const volatile uint32_t *cpu = (const uint32_t *)(uintptr_t)va;
    uint64_t nb = 0;
    for (size_t k = 0; k < SIZE / 4; k++) nb += cpu[k] != v;
    CK(cuMemcpyDtoH(buf, va, SIZE));
    for (size_t k = 0; k < SIZE / 4; k++) nb += buf[k] != v;
    if (nb) fprintf(stderr, "[%d] %llu word(s) not %#x (cpu %#x, gpu %#x at 0)\n", getpid(),
                    (unsigned long long)nb, v, cpu[0], buf[0]);
    return nb;
}

/* E: 's' create, fill (arg) and send the fd; 'w' write arg through the CPU.
 * I: 'i' import. Both: 'c' count words not arg. */
static void child(struct chan *c, int sock, int d)
{
    CUdevice cd; CUcontext ctx;
    dev = d;
    CK(cuInit(0)); CK(cuDeviceGet(&cd, d)); CK(cuDevicePrimaryCtxRetain(&ctx, cd)); CK(cuCtxSetCurrent(ctx));
    CK(cuDeviceGetAttribute(&numa, CU_DEVICE_ATTRIBUTE_HOST_NUMA_ID, cd));
    if (numa < 0) numa = 0;
    for (;;) {
        char op; uint32_t arg; uint64_t r = 0;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 's': {
            CUmemAllocationProp p = {0};
            CUmemGenericAllocationHandle h;
            int fd;
            p.type = CU_MEM_ALLOCATION_TYPE_PINNED;
            p.location.type = CU_MEM_LOCATION_TYPE_HOST_NUMA;
            p.location.id = numa;
            p.requestedHandleTypes = CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
            CK(cuMemCreate(&h, SIZE, &p, 0));
            map(h);
            for (size_t k = 0; k < SIZE / 4; k++) ((uint32_t *)(uintptr_t)va)[k] = arg;
            CK(cuMemExportToShareableHandle(&fd, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0));
            send_fd(sock, fd);
            close(fd);
            break;
        }
        case 'i': {
            CUmemGenericAllocationHandle h;
            int fd = recv_fd(sock);
            CK(cuMemImportFromShareableHandle(&h, (void *)(uintptr_t)fd, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR));
            close(fd);
            map(h);
            break;
        }
        case 'w': for (size_t k = 0; k < SIZE / 4; k++) ((volatile uint32_t *)(uintptr_t)va)[k] = arg; break;
        case 'c': r = bad(arg); break;
        case 'q': put(c->rep[1], &r, 8); exit(0);
        }
        put(c->rep[1], &r, 8);
    }
}

static const char *tool;
static int run(const char *action, pid_t x, pid_t y)
{
    char cmd[512];
    snprintf(cmd, sizeof cmd, "%s %s %d %d", tool, action, x, y);
    printf("$ %s\n", cmd); fflush(stdout);
    int st = system(cmd);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di] [cycles]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 1;
    int cycles = argc > 4 ? atoi(argv[4]) : 2, fails = 0;
    struct chan ce, ci;
    int sv[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || socketpair(AF_UNIX, SOCK_STREAM, 0, sv)) return 1;
    pid_t pe = fork();
    if (!pe) child(&ce, sv[0], de);
    pid_t pi = fork();
    if (!pi) child(&ci, sv[1], di);
    printf("exporter pid %d (dev %d), importer pid %d (dev %d)\n", pe, de, pi, di);

    uint32_t v = 0x11111111;
    call(&ce, 's', v);
    call(&ci, 'i', 0);
    EXPECT(call(&ce, 'c', v) == 0 && call(&ci, 'c', v) == 0, "contents before suspend");
    for (int c = 1; c <= cycles && !fails; c++) {
        printf("--- cycle %d/%d\n", c, cycles);
        EXPECT(run("suspend", pe, pi) == 0, "suspend E I");
        EXPECT(run("resume", pe, pi) == 0, "resume E I");
        if (fails) break;
        EXPECT(call(&ce, 'c', v) == 0 && call(&ci, 'c', v) == 0, "contents after resume (CPU and GPU)");
        v += 0x11111111;
        call(&ce, 'w', v);
        EXPECT(call(&ci, 'c', v) == 0, "sharing live: exporter's CPU write visible to importer");
    }
    if (fails) {  /* left suspended, and traced by the holder: no waitpid */
        kill(pe, SIGKILL); kill(pi, SIGKILL);
    } else {
        call(&ce, 'q', 0); call(&ci, 'q', 0);
        waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    }
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}
