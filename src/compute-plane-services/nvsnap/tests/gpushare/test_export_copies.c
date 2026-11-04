/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * An app that keeps the fds of its own exports must still be dumpable.
 *
 * FlashInfer (vLLM's all-reduce fusion on H100) exports its buffer as a
 * POSIX fd, sends that fd to itself along with its peers', and closes
 * neither copy. Both are /dev/nvidiactl fds that pin the memory, and CRIU
 * cannot dump them. The child does the same: it exports an allocation,
 * sends the fd to itself over a socket and keeps both. After
 * nvsnap-gpu-suspend suspend the child must hold no NVIDIA fds; after
 * resume, its memory must be intact, both fd numbers must still close
 * without error, and the allocation must export again.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_export_copies test_export_copies.c \
 *            -L/usr/local/cuda/lib64/stubs -lcuda
 * Run:   LD_PRELOAD=.../libnvsnap_gpushare.so ./test_export_copies <nvsnap-gpu-suspend>
 */
#include <cuda.h>
#include <dirent.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE (64UL << 20)
#define PAT 0x5A5A5A5Au
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

/* Send fd to ourselves through a socketpair; returns the received copy. */
static int self_copy(int fd)
{
    int sv[2], out = -1;
    char b = 0, cb[CMSG_SPACE(sizeof(int))];
    struct iovec io = { &b, 1 };
    struct msghdr m = { .msg_iov = &io, .msg_iovlen = 1, .msg_control = cb, .msg_controllen = sizeof(cb) };
    if (socketpair(AF_UNIX, SOCK_DGRAM, 0, sv)) return -1;
    struct cmsghdr *c = CMSG_FIRSTHDR(&m);
    c->cmsg_level = SOL_SOCKET;
    c->cmsg_type = SCM_RIGHTS;
    c->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(c), &fd, sizeof(int));
    if (sendmsg(sv[0], &m, 0) == 1 && recvmsg(sv[1], &m, 0) == 1 && (c = CMSG_FIRSTHDR(&m)))
        memcpy(&out, CMSG_DATA(c), sizeof(int));
    close(sv[0]);
    close(sv[1]);
    return out;
}

/* Commands on cmd, one reply byte (0 = ok) on rep:
 * 's' set up, 'c' check memory, close both fds, export again, 'q' quit. */
static void child(int cmd, int rep)
{
    CUdevice d;
    CUcontext ctx;
    CUmemGenericAllocationHandle h;
    CUdeviceptr va = 0;
    int fd = -1, copy = -1;
    char op;
    while (read(cmd, &op, 1) == 1) {
        char r = 0;
        if (op == 's') {
            CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED,
                .location = { CU_MEM_LOCATION_TYPE_DEVICE, 0 },
                .requestedHandleTypes = CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR };
            CUmemAccessDesc a = { { CU_MEM_LOCATION_TYPE_DEVICE, 0 }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
            CK(cuInit(0));
            CK(cuDeviceGet(&d, 0));
            CK(cuDevicePrimaryCtxRetain(&ctx, d));
            CK(cuCtxSetCurrent(ctx));
            CK(cuMemCreate(&h, SIZE, &p, 0));
            CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
            CK(cuMemMap(va, SIZE, 0, h, 0));
            CK(cuMemSetAccess(va, SIZE, &a, 1));
            CK(cuMemsetD32(va, PAT, SIZE / 4));
            CK(cuCtxSynchronize());
            CK(cuMemExportToShareableHandle(&fd, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0));
            copy = self_copy(fd);
            r = copy < 0;
        } else if (op == 'c') {
            uint32_t *hb = malloc(SIZE);
            CK(cuMemcpyDtoH(hb, va, SIZE));
            for (size_t i = 0; !r && i < SIZE / 4; i++) r = hb[i] != PAT;
            free(hb);
            if (close(fd) || close(copy)) r = 2;
            int fd2 = -1;
            if (cuMemExportToShareableHandle(&fd2, h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0) != CUDA_SUCCESS) r = 3;
            if (fd2 >= 0) close(fd2);
        } else if (op == 'q') {
            exit(0);
        }
        if (write(rep, &r, 1) != 1) exit(2);
    }
    exit(2);
}

static int cmd[2], rep[2];
static pid_t pid;

static int call(char op)
{
    char r = 1;
    if (write(cmd[1], &op, 1) != 1 || read(rep[0], &r, 1) != 1) return -1;
    return r;
}

/* /dev/nvidia* fds the child holds. */
static int nvidia_fds(void)
{
    char dir[64], path[600], target[256];
    snprintf(dir, sizeof dir, "/proc/%d/fd", pid);
    DIR *d = opendir(dir);
    struct dirent *e;
    int n = 0;
    while (d && (e = readdir(d))) {
        snprintf(path, sizeof path, "%s/%s", dir, e->d_name);
        ssize_t k = readlink(path, target, sizeof target - 1);
        if (k > 0 && (target[k] = 0, !strncmp(target, "/dev/nvidia", 11))) {
            printf("  still open: fd %s -> %s\n", e->d_name, target);
            n++;
        }
    }
    if (d) closedir(d);
    return n;
}

static const char *tool;
static int run(const char *args)
{
    char c[1024];
    snprintf(c, sizeof c, "%s %s %d", tool, args, pid);
    printf("$ %s\n", c);
    int st = system(c);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

static int fails;
#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); \
    if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend>\n", argv[0]); return 1; }
    tool = argv[1];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(cmd) || pipe(rep)) return 1;
    if ((pid = fork()) < 0) { perror("fork"); return 1; }
    if (!pid) { close(cmd[1]); close(rep[0]); child(cmd[0], rep[1]); }
    close(cmd[0]);
    close(rep[1]);
    printf("pid %d\n", pid);

    EXPECT(call('s') == 0, "allocation exported, fd sent to itself, both copies kept");
    EXPECT(run("suspend") == 0, "suspend");
    if (!fails) EXPECT(nvidia_fds() == 0, "no NVIDIA fds left after suspend (CRIU can dump it)");
    if (!fails) EXPECT(run("resume") == 0, "resume");
    if (!fails) EXPECT(call('c') == 0, "memory intact, both fds close, allocation exports again");

    if (fails || call('q') < 0) kill(pid, SIGKILL);
    int st = 0;
    waitpid(pid, &st, 0);
    if (!fails) EXPECT(WIFEXITED(st) && WEXITSTATUS(st) == 0, "child exited cleanly");
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}
