/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

// Mirrors restore-entrypoint's wakeRestoredThreads: after CRIU restore,
// I/O threads in libuv/libzmq/io_uring resume blocked in epoll_wait /
// io_uring_enter on stale kernel state. They wait there forever unless
// interrupted with EINTR. SIGUSR2 is the convention libnvsnap_intercept
// installs a handler for; the handler triggers the post-restore
// reinit path that rebuilds epoll fds, recreates io_uring rings, and
// reconnects libzmq sockets.
//
// Without this signal the restored process appears alive but any HTTP
// request that touches an event-loop driven path hangs or crashes
// (vLLM dies on the first /v1/* request).
//
// Copied (not refactored) from cmd/restore-entrypoint/main.go.
// Pulling main into an importable package would be the right
// long-term fix; until then, keep this in sync with the source.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

func wakeRestoredThreads(pid int, log *logrus.Entry) {
	allPids := collectDescendants(pid)
	totalCount := 0
	unblocked := 0
	for _, p := range allPids {
		taskDir := fmt.Sprintf("/proc/%d/task", p)
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			continue
		}
		count := 0
		for _, entry := range entries {
			tid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			if isSignalBlocked(p, tid, 12) {
				if unblockSignal(p, tid, 12) {
					unblocked++
				}
			}
			if err := syscall.Tgkill(p, tid, syscall.SIGUSR2); err == nil {
				count++
			}
		}
		totalCount += count
	}
	log.WithFields(logrus.Fields{
		"processes": len(allPids),
		"signaled":  totalCount,
		"unblocked": unblocked,
	}).Info("Sent SIGUSR2 to restored threads (libuv/libzmq/io_uring reinit)")
}

func isSignalBlocked(pid, tid, signum int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/status", pid, tid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "SigBlk:\t") {
			continue
		}
		mask := strings.TrimPrefix(line, "SigBlk:\t")
		mask = strings.TrimSpace(mask)
		var val uint64
		_, _ = fmt.Sscanf(mask, "%x", &val)
		return val&(1<<uint(signum-1)) != 0 //nolint:gosec // bounded: signum is a small positive signal number
	}
	return false
}

func unblockSignal(_, tid, signum int) bool {
	if err := unix.PtraceSeize(tid); err != nil {
		return false
	}
	defer func() { _ = unix.PtraceDetach(tid) }()
	if err := unix.PtraceInterrupt(tid); err != nil {
		return false
	}
	// Bounded-time Wait4. A blocking Wait4 here previously hung the entire
	// wakeRestoredThreads outer loop when a thread was in
	// TASK_UNINTERRUPTIBLE post-CRIU-restore (observed on phase 5b
	// vllm-small: only 2/2 threads of root PID got SIGUSR2 because the next
	// PID's libzmq IO thread didn't respond to PTRACE_INTERRUPT). Mirror
	// the cmd/restore-entrypoint fix: poll WNOHANG up to 500ms, then give
	// up — the patched libzmq has its own marker-check fallback for IO
	// threads that miss SIGUSR2.
	var ws unix.WaitStatus
	deadline := time.Now().Add(500 * time.Millisecond)
	stopped := false
	for time.Now().Before(deadline) {
		gotPid, err := unix.Wait4(tid, &ws, unix.WNOHANG, nil)
		if err != nil {
			return false
		}
		if gotPid == tid {
			stopped = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stopped {
		return false
	}
	const ptraceGetSigmask = 0x420a
	const ptraceSetSigmask = 0x420b
	var mask [8]byte
	if _, _, errno := syscall.RawSyscall6(
		syscall.SYS_PTRACE, ptraceGetSigmask,
		uintptr(tid), 8, uintptr(unsafe.Pointer(&mask[0])), 0, 0); errno != 0 { //nolint:gosec // ptrace syscall requires unsafe.Pointer to a fixed-size mask buffer
		return false
	}
	byteIdx := (signum - 1) / 8
	bitIdx := uint((signum - 1) % 8) //nolint:gosec // bounded: signum is a small positive signal number
	if mask[byteIdx]&(1<<bitIdx) == 0 {
		return false
	}
	mask[byteIdx] &^= (1 << bitIdx)
	if _, _, errno := syscall.RawSyscall6(
		syscall.SYS_PTRACE, ptraceSetSigmask,
		uintptr(tid), 8, uintptr(unsafe.Pointer(&mask[0])), 0, 0); errno != 0 { //nolint:gosec // ptrace syscall requires unsafe.Pointer to a fixed-size mask buffer
		return false
	}
	return true
}

func collectDescendants(rootPid int) []int {
	seen := map[int]bool{rootPid: true}
	queue := []int{rootPid}
	var allPids []int
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		allPids = append(allPids, pid)
		childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
		data, err := os.ReadFile(childrenPath)
		if err != nil || len(data) == 0 {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			cpid, err := strconv.Atoi(field)
			if err != nil || seen[cpid] {
				continue
			}
			seen[cpid] = true
			queue = append(queue, cpid)
		}
	}
	return allPids
}
