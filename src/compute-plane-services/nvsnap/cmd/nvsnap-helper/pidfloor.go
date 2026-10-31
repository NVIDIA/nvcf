// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// await-pid-floor holds a restore placeholder's start until the agent has
// raised the next pid of the pod's pid namespace above the range the
// restore recreates. In a pod whose containers share one pid namespace,
// every process started after it (a native sidecar, a sidecar, the
// placeholder) then gets a pid the restored tree does not need. The
// placeholder runs unprivileged and cannot raise it itself; the agent does,
// once it sees this step running. It fails open: past the deadline it
// exits 0, and the agent's own check before the restore names any clash.

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func awaitPIDFloorCmd(args []string, lastPIDFile string) {
	fs := flag.NewFlagSet("await-pid-floor", flag.ExitOnError)
	floor := fs.Int("floor", 100000, "next pid to wait for")
	deadline := fs.Duration("deadline", 5*time.Minute, "give up (and exit 0) after this long")
	_ = fs.Parse(args)
	if pidFloorReached(lastPIDFile, *floor, *deadline, 200*time.Millisecond) {
		fmt.Println("nvsnap: the pod's pid range is reserved")
		return
	}
	fmt.Fprintf(os.Stderr, "nvsnap: the pod's next pid did not reach %d within %s; going on\n", *floor, *deadline)
}

// pidFloorReached polls lastPIDFile until it reads at least floor - 1 (the
// last pid handed out), or deadline passes.
func pidFloorReached(lastPIDFile string, floor int, deadline, poll time.Duration) bool {
	end := time.Now().Add(deadline)
	for {
		if b, err := os.ReadFile(lastPIDFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= floor-1 {
				return true
			}
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(poll)
	}
}
