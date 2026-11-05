/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * nvsnap-idle keeps a container alive until it is stopped: the CRIU restore
 * placeholder and the model staging hold. It needs nothing from the image it
 * runs in (static, no shell) and is single-threaded on purpose.
 *
 * A CRIU restore recreates every dumped process and thread at its original
 * id, so the placeholder must hold no id the dump may use. The agent raises
 * the pid namespace's next pid well above the dumped range, then waits until
 * every task here other than init sits above it. This program helps that
 * along like `while true; do sleep 2; done`: one child at a time, each living
 * TICK_SECONDS, so after the raise the next child lands above the range and
 * the last one below it is gone within a tick. A multi-threaded runtime would
 * hold low thread ids for its whole life.
 *
 * Cold start in place:
 *
 *   nvsnap-idle --cold-start MARKER -- COMMAND [ARG...]
 *
 * A placeholder whose restore failed, in a pod nothing would recreate, starts
 * its workload the way the pod would have without nvsnap: on SIGUSR1 this
 * program creates MARKER and execs COMMAND, which becomes the container's
 * init as in a fresh start. MARKER sits on a volume that outlives the
 * container, so when the workload exits and the container restarts, this
 * program finds it and execs COMMAND straight away.
 */
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define TICK_SECONDS 2

static volatile sig_atomic_t stop;
static volatile sig_atomic_t cold;

static void on_term(int sig) { (void)sig; stop = 1; }
static void on_cold(int sig) { (void)sig; cold = 1; }

/* start_cold execs argv; it returns only if the exec fails. */
static int start_cold(const char *marker, char **argv) {
	int fd = open(marker, O_WRONLY | O_CREAT | O_CLOEXEC, 0644);
	if (fd < 0) {
		fprintf(stderr, "nvsnap-idle: cannot create %s: %s\n", marker, strerror(errno));
		return 1;
	}
	close(fd);
	signal(SIGUSR1, SIG_DFL);
	signal(SIGTERM, SIG_DFL);
	signal(SIGINT, SIG_DFL);
	execvp(argv[0], argv);
	fprintf(stderr, "nvsnap-idle: cannot start %s: %s\n", argv[0], strerror(errno));
	return 127;
}

int main(int argc, char **argv) {
	const char *marker = NULL;
	char **cold_argv = NULL;
	if (argc > 1) {
		if (argc < 5 || strcmp(argv[1], "--cold-start") != 0 || argv[2][0] == '\0' ||
		    strcmp(argv[3], "--") != 0 || argv[4][0] == '\0') {
			fprintf(stderr, "usage: nvsnap-idle [--cold-start MARKER -- COMMAND [ARG...]]\n");
			return 2;
		}
		marker = argv[2];
		cold_argv = argv + 4;
	}

	struct sigaction sa = {0};
	sa.sa_handler = on_term;
	sigemptyset(&sa.sa_mask);
	/* No SA_RESTART: a signal must interrupt the wait below. */
	sigaction(SIGTERM, &sa, NULL);
	sigaction(SIGINT, &sa, NULL);
	if (cold_argv != NULL) {
		sa.sa_handler = on_cold;
		sigaction(SIGUSR1, &sa, NULL);
		if (access(marker, F_OK) == 0)
			return start_cold(marker, cold_argv);
	}

	while (!stop && !cold) {
		pid_t child = fork();
		if (child == 0) {
			struct timespec ts = {TICK_SECONDS, 0};
			signal(SIGTERM, SIG_DFL);
			signal(SIGINT, SIG_DFL);
			signal(SIGUSR1, SIG_DFL);
			while (nanosleep(&ts, &ts) == -1 && errno == EINTR) {
			}
			_exit(0);
		}
		if (child < 0) {
			struct timespec ts = {TICK_SECONDS, 0};
			nanosleep(&ts, NULL);
			continue;
		}
		/* As a container's init this also collects orphans re-parented here;
		 * keep waiting until our own child is gone. */
		for (;;) {
			pid_t got = waitpid(-1, NULL, 0);
			if (got == child || (got < 0 && errno == ECHILD))
				break;
			if (got < 0 && errno == EINTR && (stop || cold)) {
				kill(child, SIGKILL);
				waitpid(child, NULL, 0);
				break;
			}
		}
	}
	if (cold && !stop)
		return start_cold(marker, cold_argv);
	return 0;
}
