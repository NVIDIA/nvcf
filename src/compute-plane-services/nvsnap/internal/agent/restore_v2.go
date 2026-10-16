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

// criu-v2 restore path: in-namespace CRIU restore.
//
// The counterpart of dumpV2. The placeholder pod is a dumb reaper (bash
// pid1 loop, same image as the source) that mounts the agent checkpoint
// dir at /checkpoints; the criu bundle normally arrives in its rootfs via
// the rootfs-diff replay (dumpV2 left /criu-bundle in the upperdir), and
// is re-staged from the agent bundle if missing. Restore is then a single
// nsenter into the placeholder's mnt/pid/net/ipc/uts namespaces running the
// bundled CRIU with --restore-detached. No ExtMounts, no JoinNamespace,
// no helper setns, no inherit-fd pipes, no marker files, no post-restore
// cuda-checkpoint unlock walk — the bundled cuda_plugin resumes GPU state
// during the restore itself (proven by the in-container PoC: inference works
// immediately after criu restore returns).
//
// Restored stdio: the PoC-convention source manifest launches the
// workload via setsid with stdio redirected to a file in the container
// rootfs, so CRIU restores those fds as plain files — the placeholder's
// pid1 tails that file to surface logs via kubelet.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// v2CheckpointsMountInContainer is where the placeholder pod must mount
// the agent's checkpoint dir (hostPath) for the in-namespace CRIU to
// read the images.
const v2CheckpointsMountInContainer = "/checkpoints"

// errNotPlaceholder marks a restore refused because the target already runs
// a GPU workload: a pod restored before, or not a placeholder at all.
var errNotPlaceholder = errors.New("refusing to restore into a non-placeholder")

func (a *Agent) restoreV2(ctx context.Context, metadata *CheckpointMetadata, checkpointDir string, placeholderInfo *containerd.ContainerInfo, startTime time.Time, group restoreV2Group, log *logrus.Entry) (*RestoreResult, error) {
	if placeholderInfo == nil {
		return nil, fmt.Errorf("criu-v2 restore requires a placeholder pod (placeholderPodName/placeholderNamespace)")
	}
	if group.InetAddrMap != "" && group.Session == "" {
		// --keep-network-lock with nobody to release it would leave the pod
		// unable to send.
		return nil, errors.New("criu-v2: an address map needs a group restore session to unlock the network")
	}
	hostPID := int(placeholderInfo.PID)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	root := filepath.Join(procBase, strconv.Itoa(hostPID), "root")

	// Refuse to restore into a container that is running a GPU workload.
	// A restore's prep replays the rootfs-diff into the target — pointed at
	// a live source pod, that corrupts it (observed: pid1 SIGSEGV). A real
	// placeholder only runs the reaper shell. Treat a failed check as
	// ambiguous state and abort rather than proceed: an NVML hiccup or a
	// not-yet-ready pid-ns must not silently disarm this guard.
	gpuPID, gerr := a.gpuProcessInSamePidNS(ctx, procBase, hostPID)
	if gerr != nil {
		return nil, fmt.Errorf("criu-v2: cannot verify target is a placeholder (GPU-process check failed): %w", gerr)
	}
	if gpuPID != 0 {
		return nil, fmt.Errorf("criu-v2: target pod is running a GPU workload (pid %d): %w", gpuPID, errNotPlaceholder)
	}

	// The images must be visible inside the placeholder at
	// /checkpoints/<id> (hostPath mount in the restore manifest).
	imgsInContainer := v2CheckpointsMountInContainer + "/" + metadata.ID
	if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(imgsInContainer, "/"), "inventory.img")); err != nil {
		return nil, fmt.Errorf("criu-v2: checkpoint images not visible at %s inside placeholder — restore manifest must mount the checkpoint hostPath at %s: %w", imgsInContainer, v2CheckpointsMountInContainer, err)
	}

	// The bundle normally rides the rootfs-diff replay into the
	// placeholder; re-stage from the agent bundle if it didn't.
	if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(v2BinDirInContainer, "/"), "criu")); err != nil {
		log.Info("criu-v2: bundle not delivered by rootfs-diff; staging into placeholder")
		if serr := a.stageV2Bundle(root, log); serr != nil {
			return nil, fmt.Errorf("criu-v2: stage bundle into placeholder: %w", serr)
		}
	}

	// Refuse to restore into a placeholder that never pushed its own pid
	// allocations clear of the dumped range. CRIU recreates the dumped tree at
	// its exact original pids, so any long-lived process the placeholder parked
	// in that range makes the restore fail with
	//
	//	Error (criu/cr-restore.c:1242): Can't fork for 363: File exists
	//
	// The placeholder bumps ns_last_pid for exactly this reason. When that line
	// went missing the failure rate was 79% across the single-GPU suite, and it
	// read as flakiness because whether it fires depends on where the shell's
	// own forks happened to land. Failing here names the cause instead.
	//
	// Wait rather than sample once: the pod is Running as soon as its shell
	// starts, but the reservation only lands after that shell finishes sourcing
	// its login profile, a few hundred forks in these images. Sampling once
	// races that window and rejects a placeholder that was about to be fine.
	// A checkpoint that carries its pid namespace is restored into a nested
	// one, where every pid is free (pidns_capture.go).
	pidns, pidnsCapture, err := readPIDNSMarkerOrFalse(checkpointDir)
	if err != nil {
		return nil, err
	}
	if !pidns {
		need, nerr := dumpedTaskIDs(checkpointDir)
		if nerr != nil {
			// Without the dumped ids every low pid counts as a clash.
			log.WithError(nerr).Warn("criu-v2: cannot read the dumped task ids; any placeholder pid below the reserved range blocks the restore")
			need = nil
		}
		if maxPID, perr := awaitPlaceholderPIDReservation(procBase, hostPID, need, log); perr != nil {
			return nil, perr
		} else if maxPID > 0 {
			log.WithField("maxNSPID", maxPID).Info("criu-v2: placeholder reserved its pid range")
		}
	} else if err := a.stagePIDNSRestoreHelper(root); err != nil {
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"placeholderPID": hostPID,
		"imagesDir":      imgsInContainer,
	}).Info("criu-v2: restoring in-namespace")

	// Same execution shape as dumpV2: minimal env, PATH covers the bundle
	// (cuda_plugin execs cuda-checkpoint and CRIU network-lock execs
	// iptables-restore from PATH), no LD_LIBRARY_PATH.
	gs := metadata.GPUShare
	if gs != nil {
		// The restored processes re-map the shim from the path they had and
		// load their GPU memory from the store path they saved to; the
		// placeholder has to provide both (its manifest mounts them).
		if gs.LibPath != "" && !fileExists(filepath.Join(root, strings.TrimPrefix(gs.LibPath, "/"))) {
			return nil, fmt.Errorf("gpushare: placeholder has no %s; mount the node bundle there", gs.LibPath)
		}
		if !fileExists(filepath.Join(root, strings.TrimPrefix(gs.StorePath, "/"), "chunks")) {
			return nil, fmt.Errorf("gpushare: placeholder has no chunk store at %s; mount the checkpoint's %s directory there",
				gs.StorePath, GPUShareCheckpointSubdir)
		}
		if !fileExists(filepath.Join(root, strings.TrimPrefix(v2BinDirInContainer, "/"), gpushareToolName)) {
			if serr := a.stageV2Bundle(root, log); serr != nil {
				return nil, fmt.Errorf("gpushare: stage %s into placeholder: %w", gpushareToolName, serr)
			}
		}
	}
	// A group restore's session: the driver learns from it that this pod
	// is restored, and answers with the unlock (and, for gpushare, the
	// handle exchange of the resume).
	var sess *restoreSession
	if group.Session != "" {
		if group.InetAddrMap != "" && !bundledSupportsNetMigration() {
			return nil, errors.New("criu-v2: the bundled CRIU has no --inet-addr-map/net-unlock; the agent base image predates them")
		}
		s, err := a.openRestoreSession(group, checkpointDir, gs != nil)
		if err != nil {
			return nil, err
		}
		sess = s
		defer sess.close()
		log = log.WithField("fabricSession", group.Session)
	}
	args := restoreV2Args(hostPID, imgsInContainer, gs != nil, gs != nil && bundledSupportsDirectImageIO(), group.InetAddrMap)
	if pidns {
		// -n: CRIU's TCP lock and unlock act on the network namespace it
		// runs in, which must be the pod's, never the node's (the agent
		// runs on the node's network).
		args = append([]string{"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-r", "-w", "--",
			v2BinDirInContainer + "/" + pidNSRestoreHelperName, "pidns-restore-exec", v2BinDirInContainer + "/criu"},
			append(pidNSRestoreArgs(imgsInContainer, pidnsCapture.Mounts, pidnsCapture.UnixListeners, gs != nil, gs != nil && bundledSupportsDirectImageIO(), group.InetAddrMap),
				stdioInheritFDs(pidnsCapture)...)...)
		log.WithField("externalMounts", len(pidnsCapture.Mounts)).Info("criu-v2: restoring into a nested pid namespace")
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	_, criuSpan := tracing.Tracer().Start(ctx, "restore.criu")
	criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "v2-inns"))
	cmd := exec.CommandContext(rctx, "nsenter", args...)
	// criu forks the restorer and the restored tree below it. A failed
	// restore has been seen leave criu waiting on its restorer tasks for
	// good ("Restoring FAILED" logged, process alive 20 min later,
	// 2026-10-04), and killing only nsenter would orphan that tree. Run
	// the whole thing in its own process group, kill the group on cancel,
	// and cancel as soon as the restore log reports failure.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second
	go cancelWhenRestoreFailed(rctx, filepath.Join(checkpointDir, "restore.log"), restoreFailureGrace, cancel, log)
	// No LD_LIBRARY_PATH: the bundle's libraries carry RPATH=$ORIGIN (see
	// Dockerfile.base), so criu resolves its dependency graph from
	// /criu-bundle/lib on its own. Setting it here would leak the bundle's
	// glibc into cuda-checkpoint and abort restore into newer-glibc containers.
	cmd.Env = []string{
		"PATH=" + v2BinDirInContainer + ":/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	if pidns && len(pidnsCapture.UnixListeners) > 0 {
		cmd.Env = append(cmd.Env, PIDNSUnixListenersEnv+"="+encodeUnixListeners(pidnsCapture.UnixListeners))
	}
	if pidns && (pidnsCapture.StdoutPipe != 0 || pidnsCapture.StderrPipe != 0) {
		cmd.Env = append(cmd.Env, PIDNSStdioEnv+"=1")
	}
	// Place criu — and therefore the restored tree, which inherits its
	// cgroup (--manage-cgroups=ignore) — into the PLACEHOLDER's cgroup via
	// clone3(CLONE_INTO_CGROUP). Without this the restored workload lives
	// in the agent's cgroup: wrong accounting, and an agent restart or
	// rollout kills it. In the placeholder's cgroup its lifecycle follows
	// the placeholder pod (deleting the pod kills the restored tree).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cgFD, cerr := placeholderCgroupDirFD(procBase, hostPID); cerr != nil {
		log.WithError(cerr).Warn("criu-v2: could not open placeholder cgroup; restored tree will live in the agent's cgroup")
	} else {
		defer func() { _ = syscall.Close(cgFD) }()
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = cgFD
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		criuSpan.RecordError(err)
		criuSpan.SetStatus(codes.Error, "CRIU restore failed (criu-v2)")
		criuSpan.End()
		tail := tailOfFile(filepath.Join(checkpointDir, "restore.log"), 8)
		return nil, fmt.Errorf("criu-v2 restore: %w (output: %s; restore.log tail: %s)", err, strings.TrimSpace(string(out)), tail)
	}
	criuSpan.End()
	if group.InetAddrMap != "" {
		// Before the unlock: a connection to a peer outside the group goes
		// to an address of the captured instance (criu_companion.go).
		resetDeadPeers(hostPID, keepAddresses("", group.InetAddrMap), log)
		if err := natInstanceAddresses(hostPID, group.InetAddrMap, log); err != nil {
			return nil, fmt.Errorf("criu-v2: lead the instance's captured addresses to its restored pods: %w", err)
		}
	}

	fabricDir := ""
	if sess != nil {
		fabricDir = sess.containerDir
	}
	// resumeGPU reloads the restored processes' GPU memory (gpushare).
	resumeGPU := func() error {
		_, gsSpan := tracing.Tracer().Start(ctx, "restore.gpushare_resume")
		gpuMap := gs.StorePath + "/" + gpushareGPUMapFile
		resumePID := hostPID
		if pidns {
			// The restored processes live in the nested pid namespace; their
			// saved pids are pids there. They resume on the placeholder's
			// GPUs, whose entries the dump left out.
			p, err := nestedInitHostPID(procBase, hostPID)
			if err != nil {
				gsSpan.End()
				return fmt.Errorf("gpushare resume: %w", err)
			}
			resumePID = p
			made, err := installPlaceholderGPUs(procBase, hostPID, p)
			if err != nil {
				gsSpan.End()
				return fmt.Errorf("gpushare resume: give the restored tree the pod's GPUs: %w", err)
			}
			log.WithField("gpuEntries", made).Info("criu-v2: the restored tree has the pod's GPUs")
		}
		if sess != nil {
			// The restored processes write their handle lists into the
			// session directory, as the user they run as; the agent made it
			// as root. The capture side hands its session over the same way.
			if err := ownSessionDir(procBase, resumePID, sess.hostDir); err != nil {
				gsSpan.End()
				return fmt.Errorf("gpushare resume: hand the fabric session to the restored processes' user: %w", err)
			}
		}
		if err := gpushareResumeWithGPUsOf(ctx, resumePID, hostPID, gs.PIDs, gpuMap, fabricDir, log); err != nil {
			gsSpan.RecordError(err)
			gsSpan.SetStatus(codes.Error, "gpushare resume failed")
			gsSpan.End()
			return fmt.Errorf("gpushare resume: %w", err)
		}
		gsSpan.End()
		return nil
	}

	if sess != nil {
		// A GPU rank of a group reloads its GPU memory before it reports
		// restored, its network still locked: the instance's members
		// without GPU state are restored only once every rank has, so an
		// etcd among them does not count the reload against its leases
		// (gpushare_group_restore.go).
		if gs != nil {
			if err := resumeGPU(); err != nil {
				return nil, err
			}
		}
		// Every pod of the instance must be restored before any of them
		// sends: hold the lock until the driver says so, then release it in
		// this pod's network namespace.
		verdict, err := sess.awaitUnlock(ctx, log)
		if err != nil {
			return nil, err
		}
		if verdict != "unlock" {
			return nil, fmt.Errorf("criu-v2: group restore %s aborted: another pod of the instance failed to restore; this pod stays locked until it is deleted", group.Session)
		}
		if group.InetAddrMap != "" {
			if err := netUnlock(ctx, hostPID, checkpointDir, log); err != nil {
				return nil, err
			}
		}
	} else if gs != nil {
		if err := resumeGPU(); err != nil {
			return nil, err
		}
	}

	duration := time.Since(startTime).Seconds()
	log.WithField("duration", fmt.Sprintf("%.2fs", duration)).Info("criu-v2: restore completed")

	return &RestoreResult{
		NewContainerID: placeholderInfo.ID,
		NewPodName:     metadata.PodName,
		Duration:       duration,
		Timestamp:      time.Now(),
	}, nil
}

// placeholderCgroupDirFD opens the placeholder container's cgroup v2
// directory (host view) for clone3(CLONE_INTO_CGROUP).
func placeholderCgroupDirFD(procBase string, pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return -1, err
	}
	// cgroup v2 unified: single line "0::<path>".
	var cgPath string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			cgPath = rest
			break
		}
	}
	if cgPath == "" {
		return -1, fmt.Errorf("no cgroup v2 entry for pid %d", pid)
	}
	for _, base := range []string{"/host/sys/fs/cgroup", "/sys/fs/cgroup"} {
		fd, oerr := syscall.Open(filepath.Join(base, cgPath), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if oerr == nil {
			return fd, nil
		}
		err = oerr
	}
	return -1, fmt.Errorf("open cgroup dir %s: %w", cgPath, err)
}

// gpuProcessInSamePidNS returns the host pid of any GPU-using process in
// the target container, or 0 if none: one in its pid namespace, or one
// descended from it in a nested namespace (where a pid-1 workload's
// restore puts the restored tree).
func (a *Agent) gpuProcessInSamePidNS(ctx context.Context, procBase string, containerPID int) (int, error) {
	targetNS, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(containerPID), "ns", "pid"))
	if err != nil {
		return 0, err
	}
	gpuPIDs, err := a.cuda.FindGPUProcesses(ctx)
	if err != nil {
		return 0, err
	}
	var tree map[int]bool
	for _, p := range gpuPIDs {
		ns, rerr := os.Readlink(filepath.Join(procBase, strconv.Itoa(p), "ns", "pid"))
		if rerr == nil && ns == targetNS {
			return p, nil
		}
		if tree == nil {
			tree = map[int]bool{}
			for _, q := range processTree(procBase, containerPID) {
				tree[q] = true
			}
		}
		if tree[p] {
			return p, nil
		}
	}
	return 0, nil
}

// reservedPIDFloor is the lowest highest-pid we accept in a placeholder before
// restoring into it. The manifest bumps ns_last_pid to 100000, so a correctly
// prepared placeholder sits just above that; a placeholder that skipped the
// bump sits in the hundreds. Anything in between is not a case we produce, so
// the floor is set well clear of both rather than tuned.
const reservedPIDFloor = 50000

// placeholderPIDs returns the highest in-container pid live in the
// placeholder's pid namespace, and the lowest one other than its init's
// (0 when init is alone). Threads count: CRIU recreates every thread at its
// dumped id, so a placeholder thread holding a low id collides as surely as
// a process does.
//
// Read from the host rather than by exec'ing into the pod: entering the
// namespace to measure it would itself allocate a pid there, which is the very
// resource under test.
func placeholderPIDs(procBase string, hostPID int) (max, low int, err error) {
	max, held, err := placeholderTaskIDs(procBase, hostPID)
	for _, id := range held {
		if id != 1 && (low == 0 || id < low) {
			low = id
		}
	}
	return max, low, err
}

// placeholderTaskIDs returns the highest in-container task id live in the
// placeholder's pid namespace and every task id held there (processes and
// threads).
func placeholderTaskIDs(procBase string, hostPID int) (highest int, held []int, err error) {
	want, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(hostPID), "ns", "pid"))
	if err != nil {
		return 0, nil, fmt.Errorf("read placeholder pid namespace: %w", err)
	}

	entries, err := os.ReadDir(procBase)
	if err != nil {
		return 0, nil, fmt.Errorf("read %s: %w", procBase, err)
	}

	see := func(nspid int) {
		if nspid > highest {
			highest = nspid
		}
		held = append(held, nspid)
	}
	for _, e := range entries {
		pid, aerr := strconv.Atoi(e.Name())
		if aerr != nil {
			continue // not a pid directory
		}
		// Processes come and go while we walk; a vanished one is not an error.
		ns, rerr := os.Readlink(filepath.Join(procBase, e.Name(), "ns", "pid"))
		if rerr != nil || ns != want {
			continue
		}
		tasks, terr := os.ReadDir(filepath.Join(procBase, e.Name(), "task"))
		if terr != nil || len(tasks) == 0 {
			if nspid, nerr := nsPIDOf(procBase, pid); nerr == nil {
				see(nspid)
			}
			continue
		}
		for _, task := range tasks {
			if nspid, nerr := nsPIDOf(filepath.Join(procBase, e.Name(), "task"), atoiOr0(task.Name())); nerr == nil {
				see(nspid)
			}
		}
	}
	if highest == 0 {
		return 0, nil, fmt.Errorf("no processes found in the placeholder's pid namespace")
	}
	return highest, held, nil
}

func atoiOr0(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// nsPIDOf returns a process's pid as seen from the innermost namespace it
// belongs to -- the last field of NSpid in /proc/<pid>/status.
func nsPIDOf(procBase string, pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "NSpid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, fmt.Errorf("empty NSpid for %d", pid)
		}
		return strconv.Atoi(fields[len(fields)-1])
	}
	return 0, fmt.Errorf("no NSpid line for %d", pid)
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

// restoreFailureGrace is how long a criu that has logged "Restoring FAILED"
// gets to exit on its own before the agent kills its process group.
const restoreFailureGrace = 30 * time.Second

// restoreLogReportsFailure reports whether a CRIU restore log says the
// restore failed. CRIU writes the line once, after it has given up.
func restoreLogReportsFailure(content string) bool {
	return strings.Contains(content, "Restoring FAILED")
}

// cancelWhenRestoreFailed polls the restore log until ctx ends. Once the log
// reports a failed restore it waits grace for criu to exit, then cancels,
// which kills the criu process group through cmd.Cancel.
func cancelWhenRestoreFailed(ctx context.Context, logPath string, grace time.Duration, cancel context.CancelFunc, log *logrus.Entry) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		b, err := os.ReadFile(logPath)
		if err != nil || !restoreLogReportsFailure(string(b)) {
			continue
		}
		log.WithField("grace", grace.String()).Warn("criu-v2: restore log reports failure; waiting for criu to exit before killing it")
		select {
		case <-ctx.Done():
			return
		case <-time.After(grace):
			log.Error("criu-v2: criu did not exit after a failed restore; killing its process group")
			cancel()
			return
		}
	}
}

// restoreV2Args builds the nsenter + criu restore argv. A gpushare restore
// omits the CUDA plugin: nvsnap-gpu-suspend restores the GPU state after
// CRIU. directIO reads the pages image with O_DIRECT.
func restoreV2Args(hostPID int, imgsInContainer string, gpushare, directIO bool, inetAddrMap string) []string {
	args := []string{
		"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/criu", "restore",
		"-D", imgsInContainer,
		"-o", "restore.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk", "--link-remap",
	}
	if !gpushare {
		args = append(args, "--libdir", v2BinDirInContainer)
	}
	args = append(args,
		// Match the dump: unlock TCP in-process via libnftables (workload
		// images ship no iptables binary).
		"--network-lock", "nftables",
		// Never restore cgroup membership: the dumped paths are the SOURCE
		// pod's kubepods slice, which kubelet has already torn down; the
		// restored tree would land in an orphaned cgroup kubelet kills.
		"--manage-cgroups=ignore",
		"--restore-detached",
	)
	if directIO {
		args = append(args, "--image-io-mode", "direct")
	}
	if inetAddrMap != "" {
		// A group restore: the connections between the instance's pods
		// move to the new pods' addresses, and stay locked after CRIU
		// exits, so a pod restored early cannot reach a peer that is not
		// restored yet (its kernel would answer with a reset). The driver
		// unlocks every pod once all of them are restored.
		args = append(args, "--inet-addr-map", inetAddrMap, "--keep-network-lock")
	}
	return args
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

// parsePstreeIDs reads the task ids out of crit's JSON of a pstree.img.
func parsePstreeIDs(b []byte) (map[int]bool, error) {
	var doc struct {
		Entries []struct {
			PID     int   `json:"pid"`
			PGID    int   `json:"pgid"`
			SID     int   `json:"sid"`
			Threads []int `json:"threads"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse pstree: %w", err)
	}
	if len(doc.Entries) == 0 {
		return nil, fmt.Errorf("pstree has no tasks")
	}
	ids := map[int]bool{}
	for _, e := range doc.Entries {
		for _, id := range append([]int{e.PID, e.PGID, e.SID}, e.Threads...) {
			if id > 0 {
				ids[id] = true
			}
		}
	}
	return ids, nil
}
