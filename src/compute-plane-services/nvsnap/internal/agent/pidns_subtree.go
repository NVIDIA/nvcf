// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A container whose pod shares one pid namespace across its containers
// (shareProcessNamespace) is not its namespace's init: the pod's pause
// process is, and the other containers' processes live beside it. Its
// capture dumps the container's own process tree from inside that
// namespace (a subtree, pidNSCapture.Subtree), with the agent's CRIU all
// the same (pidns-dump-exec in cmd/agent). CRIU then records no pid
// namespace, and the restore recreates the tree at its dumped pids in the
// placeholder's own shared namespace: the placeholder first moves its pid
// allocations clear of them (it bumps ns_last_pid), and the restore waits
// for that and for no placeholder task to hold one of them.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// restoredRootHostPID returns the host pid of the restored tree's root:
// the nested pid namespace's init, or for a subtree the task at its dumped
// pid in the placeholder's pid namespace.
func restoredRootHostPID(procBase string, placeholderHostPID int, c pidNSCapture) (int, error) {
	if c.Subtree {
		return subtreeRootHostPID(procBase, placeholderHostPID, c.RootNSPID)
	}
	return nestedInitHostPID(procBase, placeholderHostPID)
}

// subtreeRootHostPID returns the host pid of the restored tree's root: the
// task of the placeholder's pid namespace whose pid there is rootNSPID.
func subtreeRootHostPID(procBase string, placeholderHostPID, rootNSPID int) (int, error) {
	want, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(placeholderHostPID), "ns", "pid"))
	if err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(procBase)
	if err != nil {
		return 0, err
	}
	for _, e := range ents {
		pid, aerr := strconv.Atoi(e.Name())
		if aerr != nil {
			continue
		}
		if ns, rerr := os.Readlink(filepath.Join(procBase, e.Name(), "ns", "pid")); rerr != nil || ns != want {
			continue
		}
		if n, nerr := nsPIDOf(procBase, pid); nerr == nil && n == rootNSPID {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no restored pid %d in the pid namespace of %d", rootNSPID, placeholderHostPID)
}

// pidReservationTimeout bounds how long we wait for the placeholder to push its
// pid range up. The reservation itself is one write; the wait is for the login
// shell ahead of it, which forks a few hundred times sourcing profile.d in
// these images. Generous on purpose: waiting a few extra seconds costs far less
// than rejecting a placeholder that was seconds from ready.
const pidReservationTimeout = 90 * time.Second

// awaitPlaceholderPIDReservation blocks until the placeholder's pid allocations
// clear the dumped range: one live task at or above the floor shows the
// ns_last_pid bump landed, and none other than init below it shows nothing
// the placeholder started before the bump still holds a pid CRIU may need.
// It returns the highest pid it saw.
//
// Returns an error only when the reservation never lands, which means the
// restore would fail partway through with a clone3 EEXIST that reads as
// flakiness. Failing here names the cause instead.
//
// A procfs read error is not fatal: the pid namespace may still be settling,
// and treating a transient read as a missing reservation would reintroduce
// exactly the false negative this function exists to avoid.
//
// need, when known, is the set of task ids the dump recreates
// (dumpedTaskIDs): a placeholder task below the floor is then a collision
// only when it holds one of them. In a pod that shares its pid namespace
// across containers, a sidecar started before the placeholder (a native
// sidecar, which starts ahead of every container) holds a low pid, as it
// did beside the captured process; only an exact clash matters.
func awaitPlaceholderPIDReservation(procBase string, hostPID int, need map[int]bool, log *logrus.Entry) (int, error) {
	return awaitPlaceholderPIDReservationFor(procBase, hostPID, pidReservationTimeout, need, log)
}

// placeholderClash returns a task id held in the placeholder that the
// restore needs: one of need, or with need unknown, any but init's below
// reservedPIDFloor; 0 when none.
func placeholderClash(held []int, need map[int]bool) int {
	clash := 0
	for _, id := range held {
		hit := false
		if need != nil {
			hit = need[id]
		} else {
			hit = id != 1 && id < reservedPIDFloor
		}
		if hit && (clash == 0 || id < clash) {
			clash = id
		}
	}
	return clash
}

// awaitPlaceholderPIDReservationFor is the body, with the wait injectable so
// tests can exercise the timeout path without waiting it out.
func awaitPlaceholderPIDReservationFor(procBase string, hostPID int, timeout time.Duration, need map[int]bool, log *logrus.Entry) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastSeen, lastLow int
	var lastErr error
	warned := false

	for {
		maxPID, held, err := placeholderTaskIDs(procBase, hostPID)
		if err == nil {
			lastSeen, lastLow = maxPID, placeholderClash(held, need)
			if maxPID >= reservedPIDFloor && lastLow == 0 {
				return maxPID, nil
			}
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			break
		}
		if !warned {
			// One line, not one per poll: this is the normal startup window.
			log.WithFields(logrus.Fields{"maxNSPID": lastSeen, "lowNSPID": lastLow, "want": reservedPIDFloor}).
				Info("criu-v2: waiting for the placeholder to reserve its pid range")
			warned = true
		}
		time.Sleep(500 * time.Millisecond)
	}

	if lastSeen == 0 && lastErr != nil {
		return 0, fmt.Errorf("criu-v2: could not read the placeholder's pid namespace "+
			"to verify its pid range was reserved: %w", lastErr)
	}
	if lastSeen >= reservedPIDFloor && need != nil {
		return lastSeen, fmt.Errorf(
			"criu-v2: placeholder holds pid %d, which the checkpoint restores, after %s: "+
				"CRIU's exact-pid fork would collide with it", lastLow, timeout)
	}
	if lastSeen >= reservedPIDFloor {
		return lastSeen, fmt.Errorf(
			"criu-v2: placeholder still holds pid %d below its reserved range after %s: "+
				"CRIU's exact-pid forks could collide with it", lastLow, timeout)
	}
	return lastSeen, fmt.Errorf(
		"criu-v2: placeholder never reserved its pid range (highest pid %d < %d after %s): "+
			"the ns_last_pid bump is missing or failed, and CRIU's exact-pid forks would "+
			"collide with this pod's own processes", lastSeen, reservedPIDFloor, timeout)
}

// dumpedTaskIDs returns the task ids a restore of the checkpoint in dir
// recreates at their dumped values: every process and thread of its
// pstree.img, and the session and process-group ids, which CRIU recreates
// as helper tasks when their leader is not in the tree.
func dumpedTaskIDs(dir string) (map[int]bool, error) {
	img := ""
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "pstree.img" && img == "" {
			img = p
			return filepath.SkipAll
		}
		return nil
	})
	if img == "" {
		return nil, fmt.Errorf("no pstree.img under %s", dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "crit", "decode", "-i", img).Output()
	if err != nil {
		return nil, fmt.Errorf("crit decode %s: %w", img, err)
	}
	return parsePstreeIDs(out)
}

// rootNSPIDOf is the marker's RootNSPID: the subtree root's pid in the
// pod's pid namespace, 0 for a whole namespace.
func rootNSPIDOf(subtree bool, nsPID int) int {
	if subtree {
		return nsPID
	}
	return 0
}

// runSubtreeDump runs the agent's CRIU with args inside the pod's pid and
// network namespaces of hostPID, entered by descriptor, behind the
// pidns-dump-exec helper (cmd/agent). nsenter --pid forks: the deadline
// kills the whole process group, CRIU with it.
func (a *Agent) runSubtreeDump(ctx context.Context, procBase string, hostPID int, env, args []string, log *logrus.Entry) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("the agent binary: %w", err)
	}
	nsArgs, files, err := nsenterByFD(procBase, hostPID, []string{"pid", "net"}, false, 3)
	if err != nil {
		return nil, fmt.Errorf("enter the pod's pid namespace: %w", err)
	}
	defer closeFiles(files)
	log.Info("criu-v2: the container shares its pod's pid namespace; dumping its process tree from inside that namespace")
	argv := append(append(nsArgs, "--", self, "pidns-dump-exec", a.config.CRIUPath), args...)
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pidNSDumpDeadline)
	defer cancel()
	cmd := exec.CommandContext(dctx, "nsenter", argv...) //nolint:gosec // the agent's own binaries, arguments it builds
	cmd.Env = env
	cmd.ExtraFiles = files
	killGroupOnCancel(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && dctx.Err() != nil {
		return out, fmt.Errorf("%w (after %s): %w", errSourceLeftLocked, pidNSDumpDeadline, err)
	}
	return out, err
}
