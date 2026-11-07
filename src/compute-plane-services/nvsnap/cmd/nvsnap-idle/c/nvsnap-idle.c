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
 * its workload the way the pod would have without nvsnap. The agent creates
 * MARKER, on a volume that outlives the container, then kills this program;
 * killing a pid namespace's init ends everything in it, the restored processes
 * included. When the container restarts, this program finds MARKER and execs
 * COMMAND, which becomes the container's init as in a fresh start; so does
 * every later restart.
 */
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define TICK_SECONDS 2

static volatile sig_atomic_t stop;

static void on_term(int sig) { (void)sig; stop = 1; }

int main(int argc, char **argv) {
	if (argc > 1) {
		if (argc < 5 || strcmp(argv[1], "--cold-start") != 0 || argv[2][0] == '\0' ||
		    strcmp(argv[3], "--") != 0 || argv[4][0] == '\0') {
			fprintf(stderr, "usage: nvsnap-idle [--cold-start MARKER -- COMMAND [ARG...]]\n");
			return 2;
		}
		if (access(argv[2], F_OK) == 0) {
			execvp(argv[4], argv + 4);
			fprintf(stderr, "nvsnap-idle: cannot start %s: %s\n", argv[4], strerror(errno));
			return 127;
		}
	}

	struct sigaction sa = {0};
	sa.sa_handler = on_term;
	sigemptyset(&sa.sa_mask);
	/* No SA_RESTART: a term must interrupt the wait below. */
	sigaction(SIGTERM, &sa, NULL);
	sigaction(SIGINT, &sa, NULL);

	while (!stop) {
		pid_t child = fork();
		if (child == 0) {
			struct timespec ts = {TICK_SECONDS, 0};
			signal(SIGTERM, SIG_DFL);
			signal(SIGINT, SIG_DFL);
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
			if (got < 0 && errno == EINTR && stop) {
				kill(child, SIGKILL);
				waitpid(child, NULL, 0);
				break;
			}
		}
	}
	return 0;
}
