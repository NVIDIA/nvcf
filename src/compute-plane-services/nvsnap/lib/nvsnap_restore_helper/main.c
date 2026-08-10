/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * nvsnap-restore-helper: cross-mntns CRIU launcher for the agent-driven
 * restore path.
 *
 * Single-threaded C binary because setns(CLONE_NEWNS) refuses
 * multi-threaded callers — the Go agent's runtime is multi-threaded.
 * The agent execs this binary, which:
 *   1. open_tree(bundle-src,    CLONE|RECURSIVE) -> detached mount tree
 *   2. open_tree(checkpoints,   CLONE|RECURSIVE) -> detached mount tree
 *   3. setns(/proc/<placeholder-pid>/ns/mnt, CLONE_NEWNS)
 *   4. remount /proc tied to OUR PID namespace (placeholder's procfs
 *      can't see our host PID, so /proc/self breaks for CRIU)
 *   5. move_mount tree fds into /tmp/nvsnap-bundle and /tmp/nvsnap-checkpoints
 *   6. execve the CRIU binary (already at /tmp/nvsnap-bundle/criu)
 *
 * Agent is responsible for translating CRIU args so paths reference the
 * /tmp/nvsnap-* prefixes. ExtraFiles inherited across exec.
 *
 * Usage:
 *   nvsnap-restore-helper --placeholder-pid=N --bundle-src=PATH \
 *       --checkpoints-src=PATH -- <criu> [criu args...]
 *
 * Bundle and checkpoints are grafted at the SAME paths inside the
 * placeholder (e.g. /criu-bundle, /var/lib/nvsnap/checkpoints), so CRIU's
 * baked-in RPATH (/criu-bundle/lib) resolves and the agent doesn't need
 * to translate any path-bearing args.
 */

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <unistd.h>
#include <linux/mount.h>
#include <limits.h>

#ifndef OPEN_TREE_CLONE
#define OPEN_TREE_CLONE 1
#endif
#ifndef OPEN_TREE_CLOEXEC
#define OPEN_TREE_CLOEXEC O_CLOEXEC
#endif
#ifndef AT_RECURSIVE
#define AT_RECURSIVE 0x8000
#endif
#ifndef MOVE_MOUNT_F_EMPTY_PATH
#define MOVE_MOUNT_F_EMPTY_PATH 0x00000004
#endif
#ifndef __NR_open_tree
#define __NR_open_tree 428
#endif
#ifndef __NR_move_mount
#define __NR_move_mount 429
#endif

static int sys_open_tree(int dfd, const char *path, unsigned int flags) {
    return (int)syscall(__NR_open_tree, dfd, path, flags);
}

static int sys_move_mount(int from_dfd, const char *from_path, int to_dfd,
                          const char *to_path, unsigned int flags) {
    return (int)syscall(__NR_move_mount, from_dfd, from_path, to_dfd, to_path, flags);
}

static void die(const char *msg) {
    fprintf(stderr, "nvsnap-restore-helper: %s: %s\n", msg, strerror(errno));
    exit(1);
}

static const char *flag_value(const char *arg, const char *name) {
    size_t n = strlen(name);
    if (strncmp(arg, name, n) == 0 && arg[n] == '=')
        return arg + n + 1;
    return NULL;
}

int main(int argc, char **argv) {
    long placeholder_pid = 0;
    const char *bundle_src = NULL;
    const char *checkpoints_src = NULL;
    int sep = -1;

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--") == 0) { sep = i; break; }
        const char *v;
        if ((v = flag_value(argv[i], "--placeholder-pid")))    { placeholder_pid = strtol(v, NULL, 10); continue; }
        if ((v = flag_value(argv[i], "--bundle-src")))         { bundle_src = v; continue; }
        if ((v = flag_value(argv[i], "--checkpoints-src")))    { checkpoints_src = v; continue; }
        fprintf(stderr, "nvsnap-restore-helper: unknown arg %s\n", argv[i]);
        return 2;
    }
    if (placeholder_pid <= 0 || !bundle_src || !checkpoints_src || sep < 0 || sep + 1 >= argc) {
        fprintf(stderr,
            "usage: nvsnap-restore-helper --placeholder-pid=N --bundle-src=DIR --checkpoints-src=DIR -- <criu> [args...]\n");
        return 2;
    }

    int bundle_fd = sys_open_tree(AT_FDCWD, bundle_src,
        OPEN_TREE_CLONE | OPEN_TREE_CLOEXEC | AT_RECURSIVE);
    if (bundle_fd < 0) die("open_tree bundle");

    int ckpt_fd = sys_open_tree(AT_FDCWD, checkpoints_src,
        OPEN_TREE_CLONE | OPEN_TREE_CLOEXEC | AT_RECURSIVE);
    if (ckpt_fd < 0) die("open_tree checkpoints");

    char mntns_path[64];
    snprintf(mntns_path, sizeof(mntns_path), "/proc/%ld/ns/mnt", placeholder_pid);
    int mntns_fd = open(mntns_path, O_RDONLY | O_CLOEXEC);
    if (mntns_fd < 0) die("open placeholder mntns");

    if (setns(mntns_fd, CLONE_NEWNS) != 0) die("setns placeholder mntns");
    close(mntns_fd);

    /* Remount /proc tied to our (host) PID namespace so /proc/self and
     * /proc/<host-pid>/ns/<ns> resolve. The placeholder's /proc was tied
     * to placeholder's PID ns, where our process isn't visible. */
    (void)umount2("/proc", MNT_DETACH);
    if (mount("proc", "/proc", "proc", 0, NULL) != 0) die("mount /proc");

    /* Recursively create target dirs at the SAME paths the agent uses,
     * so CRIU's baked-in RPATH (/criu-bundle/lib) resolves and the agent
     * doesn't need to translate paths in its CRIU args. */
    char *bundle_dst = strdup(bundle_src);
    char *ckpt_dst = strdup(checkpoints_src);
    if (!bundle_dst || !ckpt_dst) die("strdup");
    for (char *p = bundle_dst + 1; *p; p++) {
        if (*p == '/') { *p = 0; (void)mkdir(bundle_dst, 0755); *p = '/'; }
    }
    if (mkdir(bundle_dst, 0755) != 0 && errno != EEXIST) die("mkdir bundle dst");
    for (char *p = ckpt_dst + 1; *p; p++) {
        if (*p == '/') { *p = 0; (void)mkdir(ckpt_dst, 0755); *p = '/'; }
    }
    if (mkdir(ckpt_dst, 0755) != 0 && errno != EEXIST) die("mkdir checkpoints dst");

    if (sys_move_mount(bundle_fd, "", AT_FDCWD, bundle_dst,
                       MOVE_MOUNT_F_EMPTY_PATH) != 0) die("move_mount bundle");
    if (sys_move_mount(ckpt_fd, "", AT_FDCWD, ckpt_dst,
                       MOVE_MOUNT_F_EMPTY_PATH) != 0) die("move_mount checkpoints");
    close(bundle_fd);
    close(ckpt_fd);

    /* The placeholder's /etc/ld.so.cache doesn't reference /criu-bundle/lib
     * (the agent's image registered it, but the placeholder's vllm image
     * does not). Set LD_LIBRARY_PATH so CRIU's dynamic linker finds its
     * non-system deps (libnftables, libprotobuf-c, libnet, etc.). */
    char ld_lib_path[PATH_MAX];
    snprintf(ld_lib_path, sizeof(ld_lib_path), "LD_LIBRARY_PATH=%s/lib", bundle_dst);
    putenv(ld_lib_path);

    /* Re-open stdin/stdout/stderr before exec. The agent's exec.Cmd
     * connects our stdio to a pipe consumed only when cmd.Run() returns;
     * once that happens those pipes close and the restore-detached
     * process tree inherits dead fds — first stdout/stderr write trips
     * SIGPIPE and the workload dies. We redirect to an append log file
     * inside the placeholder mntns so the restored workload has stable
     * fds AND the workload's post-restore output is recoverable for
     * debugging (kubectl exec into placeholder + tail). */
    int infd = open("/dev/null", O_RDONLY | O_CLOEXEC);
    if (infd >= 0) { dup2(infd, 0); if (infd != 0) close(infd); }
    int outfd = open("/tmp/nvsnap-restored.log",
                     O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0644);
    if (outfd >= 0) {
        dup2(outfd, 1);
        dup2(outfd, 2);
        if (outfd > 2) close(outfd);
    }

    /* Drop the post-restore marker file in the placeholder's mntns BEFORE
     * exec'ing CRIU. Patched libzmq checks /run/criu-restored on each
     * epoll loop pass and rebuilds its epoll FD when found; patched
     * libuv's uv__io_poll has the analogous check. Creating the marker
     * after CRIU exits (legacy's PostResume hook) leaves a race where
     * restored threads complete their first poll before the marker
     * appears and crash with stale-fd errors. Writing here — visible
     * the moment CRIU resumes any restored thread — closes the race.
     * mkdir is idempotent (placeholder's /run is overlay or tmpfs). */
    (void)mkdir("/run", 0755);
    int mfd = open("/run/criu-restored",
                   O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
    if (mfd >= 0) { (void)write(mfd, "1", 1); close(mfd); }
    (void)mkdir("/var", 0755);
    (void)mkdir("/var/run", 0755);
    (void)mkdir("/var/run/nvsnap", 0755);
    int mfd2 = open("/var/run/nvsnap/.restored",
                    O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
    if (mfd2 >= 0) { (void)write(mfd2, "1", 1); close(mfd2); }

    /* Clear /etc/ld.so.preload before exec'ing CRIU. The cross-pod restore
     * upperdir mirror replays the source workload's writes including
     * /etc/ld.so.preload (typically pointing at libnvsnap_intercept.so —
     * the LD_PRELOAD interception lib used by the workload). When the
     * dynamic linker starts CRIU and its forked helpers in the
     * placeholder mntns, it would preload that lib — which hooks
     * signals/syscalls/io_uring for the WORKLOAD and deadlocks CRIU's
     * own restore logic. Renaming defers the file out of the linker's
     * lookup path; the restored workload process keeps its own preload
     * state from the dump's mapped memory pages, so functionality is
     * unaffected. */
    (void)rename("/etc/ld.so.preload", "/etc/ld.so.preload.nvsnap-disabled");

    execv(argv[sep + 1], &argv[sep + 1]);
    die("execv criu");
    return 1; /* unreachable */
}
