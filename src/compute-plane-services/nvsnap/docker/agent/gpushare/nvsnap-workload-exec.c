// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// nvsnap-workload-exec: run a program in a workload's namespaces as the
// workload, with no capability but CAP_SYS_PTRACE and no way to gain more.
//
//   nvsnap-workload-exec --uid U --gid G --ns KIND=FD [--ns KIND=FD ...]
//       --exec-fd FD -- ARGV0 [ARG ...]
//
// KIND is mnt, pid, net, ipc or uts; FD an open /proc/<pid>/ns/<kind>.
// --exec-fd is the program, opened by the caller from its own filesystem.
//
// The agent runs the GPU tool (nvsnap-gpu-suspend) through it. The tool is
// linked dynamically and loads the workload's libcuda, so its loader and
// libraries come from the workload's image: they must run with the
// workload's rights, never root's. This helper is started by the agent in
// the agent's own filesystem and linked with -z now, so all it loads is
// loaded before it enters any namespace; it does no name lookups after.
//
// Steps: enter the namespaces; fork (a pid namespace applies to children);
// in the child, reduce the bounding set to CAP_SYS_PTRACE, drop the
// supplementary groups, set the gids and uids keeping capabilities, set
// the effective, permitted, inheritable and ambient sets to CAP_SYS_PTRACE,
// set no_new_privs, check the result, then execute the program by
// descriptor. The parent waits and exits with the child's status (128+N
// for a signal), so the caller's process-group kill reaches both.
//
// CAP_SYS_PTRACE: the tool freezes the workload's threads and the driver's
// checkpoint calls check ptrace access; with Yama ptrace_scope 1 they fail
// without it. It reaches only processes the tool can name: those of the
// workload's pid namespace.
//
// docs/design/TOOLS-OUTSIDE-THE-WORKLOAD.md, "The GPU tool as the workload".

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

#ifndef PR_CAP_AMBIENT
#define PR_CAP_AMBIENT 47
#define PR_CAP_AMBIENT_RAISE 2
#define PR_CAP_AMBIENT_IS_SET 1
#endif

#define KEEP_CAP CAP_SYS_PTRACE
#define MAX_NS 5

extern char **environ;

static const char *prog = "nvsnap-workload-exec";

static void die(const char *what) {
    fprintf(stderr, "%s: %s: %s\n", prog, what, strerror(errno));
    exit(126);
}

static void fail(const char *what) {
    fprintf(stderr, "%s: %s\n", prog, what);
    exit(126);
}

static void usage(void) {
    fprintf(stderr, "usage: %s --uid U --gid G --ns KIND=FD [--ns KIND=FD ...] --exec-fd FD -- ARGV0 [ARG ...]\n", prog);
    exit(126);
}

// number parses a non-negative decimal int.
static int number(const char *s, const char *what) {
    char *end;
    errno = 0;
    long v = strtol(s, &end, 10);
    if (errno || *s == '\0' || *end != '\0' || v < 0 || v > 0x7fffffff) {
        fprintf(stderr, "%s: bad %s %s\n", prog, what, s);
        exit(126);
    }
    return (int)v;
}

static int ns_type(const char *kind) {
    if (!strcmp(kind, "mnt")) return CLONE_NEWNS;
    if (!strcmp(kind, "pid")) return CLONE_NEWPID;
    if (!strcmp(kind, "net")) return CLONE_NEWNET;
    if (!strcmp(kind, "ipc")) return CLONE_NEWIPC;
    if (!strcmp(kind, "uts")) return CLONE_NEWUTS;
    return 0;
}

static int capget_sets(struct __user_cap_data_struct d[2]) {
    struct __user_cap_header_struct h = {_LINUX_CAPABILITY_VERSION_3, 0};
    return (int)syscall(SYS_capget, &h, d);
}

static int capset_sets(struct __user_cap_data_struct d[2]) {
    struct __user_cap_header_struct h = {_LINUX_CAPABILITY_VERSION_3, 0};
    return (int)syscall(SYS_capset, &h, d);
}

// become drops to uid/gid with only KEEP_CAP, then checks it.
static void become(uid_t uid, gid_t gid) {
    // The bounding set first: once it lacks a capability, no exec gives it
    // back, whatever the file or the uid.
    for (int c = 0; c < 64; c++) {
        if (c == KEEP_CAP) continue;
        if (prctl(PR_CAPBSET_DROP, c, 0, 0, 0) != 0) {
            if (errno == EINVAL) break; // past the kernel's last capability
            die("drop from the bounding set");
        }
    }
    if (prctl(PR_SET_KEEPCAPS, 1, 0, 0, 0) != 0) die("keep capabilities across the uid change");
    if (setgroups(0, NULL) != 0) die("drop the supplementary groups");
    if (setresgid(gid, gid, gid) != 0) die("set the gids");
    if (setresuid(uid, uid, uid) != 0) die("set the uids");

    struct __user_cap_data_struct d[2];
    memset(d, 0, sizeof(d));
    d[KEEP_CAP / 32].effective = d[KEEP_CAP / 32].permitted = d[KEEP_CAP / 32].inheritable = 1u << (KEEP_CAP % 32);
    if (capset_sets(d) != 0) die("set the capabilities");
    // Ambient: a program run as a uid other than 0 keeps the capability.
    if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_RAISE, KEEP_CAP, 0, 0) != 0) die("raise the ambient capability");
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0) die("set no_new_privs");

    // Check rather than trust each call above.
    uid_t r, e, s;
    gid_t rg, eg, sg;
    if (getresuid(&r, &e, &s) != 0 || r != uid || e != uid || s != uid) fail("uids are not the workload's");
    if (getresgid(&rg, &eg, &sg) != 0 || rg != gid || eg != gid || sg != gid) fail("gids are not the workload's");
    if (getgroups(0, NULL) != 0) fail("supplementary groups left");
    memset(d, 0, sizeof(d));
    if (capget_sets(d) != 0) die("read the capabilities");
    for (int i = 0; i < 2; i++) {
        __u32 want = i == KEEP_CAP / 32 ? 1u << (KEEP_CAP % 32) : 0;
        if (d[i].effective != want || d[i].permitted != want || d[i].inheritable != want)
            fail("capabilities other than CAP_SYS_PTRACE left");
    }
    for (int c = 0; c < 64; c++) {
        int in = prctl(PR_CAPBSET_READ, c, 0, 0, 0);
        if (in < 0) break;
        if (in != (c == KEEP_CAP)) fail("bounding set is not CAP_SYS_PTRACE alone");
    }
    if (prctl(PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) != 1) fail("no_new_privs not set");
}

int main(int argc, char **argv) {
    int uid = -1, gid = -1, exec_fd = -1, n = 0, i;
    int ns_fd[MAX_NS], ns_kind[MAX_NS];

    for (i = 1; i < argc; i++) {
        const char *a = argv[i];
        if (!strcmp(a, "--")) { i++; break; }
        if (i + 1 >= argc) usage();
        const char *v = argv[++i];
        if (!strcmp(a, "--uid")) uid = number(v, "uid");
        else if (!strcmp(a, "--gid")) gid = number(v, "gid");
        else if (!strcmp(a, "--exec-fd")) exec_fd = number(v, "descriptor");
        else if (!strcmp(a, "--ns")) {
            const char *eq = strchr(v, '=');
            char kind[8];
            if (!eq || (size_t)(eq - v) >= sizeof(kind) || n == MAX_NS) usage();
            memcpy(kind, v, (size_t)(eq - v));
            kind[eq - v] = '\0';
            if (!(ns_kind[n] = ns_type(kind))) usage();
            ns_fd[n++] = number(eq + 1, "descriptor");
        } else usage();
    }
    if (uid < 0 || gid < 0 || exec_fd < 0 || n == 0 || i >= argc) usage();

    // The mount namespace last: entering it changes root and working
    // directory to the namespace's; the descriptors are open already.
    for (int pass = 0; pass < 2; pass++)
        for (int k = 0; k < n; k++) {
            if ((ns_kind[k] == CLONE_NEWNS) != (pass == 1)) continue;
            if (setns(ns_fd[k], ns_kind[k]) != 0) die("enter a namespace");
            close(ns_fd[k]);
        }
    if (chdir("/") != 0) die("chdir /");

    pid_t child = fork();
    if (child < 0) die("fork");
    if (child == 0) {
        // The tool must not outlive a killed helper unseen. (The caller
        // kills the whole process group as well; the parent is outside the
        // child's pid namespace, so getppid cannot check it here.)
        if (prctl(PR_SET_PDEATHSIG, SIGKILL, 0, 0, 0) != 0) die("set the parent-death signal");
        become((uid_t)uid, (gid_t)gid);
        if (fcntl(exec_fd, F_SETFD, FD_CLOEXEC) != 0) die("close the program's descriptor on exec");
        fexecve(exec_fd, argv + i, environ);
        die("execute the program");
    }
    close(exec_fd);
    int st;
    while (waitpid(child, &st, 0) < 0)
        if (errno != EINTR) die("wait");
    if (WIFEXITED(st)) return WEXITSTATUS(st);
    if (WIFSIGNALED(st)) return 128 + WTERMSIG(st);
    return 126;
}
