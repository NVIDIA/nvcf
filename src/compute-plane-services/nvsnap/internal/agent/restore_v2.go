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
// Restored stdio: the workload's stdout and stderr pipes are replaced with
// the placeholder's own, so it logs to the new pod (restore_stdio.go).

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
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

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

	// The agent's CRIU reads the images where the agent keeps them.
	if _, err := os.Stat(filepath.Join(checkpointDir, "inventory.img")); err != nil {
		return nil, fmt.Errorf("criu-v2: checkpoint images: %w", err)
	}

	// The tools run from the node bundle mounted read-only in the
	// placeholder (bundle_tools.go).
	tools, err := bundleToolsOf(procBase, hostPID, a.bundleVersionsDir())
	if err != nil {
		return nil, fmt.Errorf("criu-v2: %w", err)
	}
	// What the restore writes (CRIU's log, a group restore's session) goes
	// to the placeholder's own work volume; the checkpoint is read-only
	// there (confined_fs.go).
	work, err := openWorkloadDir(procBase, hostPID, webhook.RestoreWorkMountPath)
	if err != nil {
		return nil, fmt.Errorf("criu-v2: the placeholder has no restore work volume at %s (admitted before it was added: recreate it): %w", webhook.RestoreWorkMountPath, err)
	}
	defer func() { _ = work.Close() }()
	// From here the restore changes the placeholder; a marker names it until
	// the restore ends, so the next run of an agent that stops mid-restore
	// replaces it rather than leave a tree half restored (capture_marker.go).
	if uid := placeholderInfo.Labels["io.kubernetes.pod.uid"]; uid != "" {
		clearMarker, merr := a.markCapturing(captureMarker{Namespace: placeholderInfo.Labels["io.kubernetes.pod.namespace"],
			Pod: placeholderInfo.Labels["io.kubernetes.pod.name"], UID: uid})
		if merr != nil {
			return nil, merr
		}
		defer clearMarker()
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
	// Every capture dumps the container's pid namespace, restored nested
	// in the placeholder's, where every pid is free (pidns_capture.go), or
	// in a pod that shares one pid namespace the container's process tree,
	// restored at its pids in the placeholder's (pidns_subtree.go). A
	// checkpoint taken inside the container's namespaces, before that, is
	// not restored: a new capture replaces it.
	pidns, pidnsCapture, err := readPIDNSMarkerOrFalse(checkpointDir)
	if err != nil {
		return nil, err
	}
	if !pidns {
		return nil, fmt.Errorf("%w: it was captured inside the container's namespaces, which this agent no longer restores; a new capture replaces it", errCheckpointUnavailable)
	}
	if !tools.has(procBase, pidNSRestoreHelperName) {
		return nil, fmt.Errorf("pid-namespace restore: %s is not in the placeholder's node bundle (version %s)", pidNSRestoreHelperName, tools.version)
	}
	if pidnsCapture.Subtree {
		// The tree comes back at its dumped pids in the placeholder's own
		// pid namespace, shared with the pod's other containers
		// (pidns_subtree.go): wait until no placeholder task holds one.
		need, nerr := dumpedTaskIDs(checkpointDir)
		if nerr != nil {
			// Without the dumped ids every low pid counts as a clash.
			log.WithError(nerr).Warn("criu-v2: cannot read the dumped task ids; any placeholder pid below the reserved range blocks the restore")
			need = nil
		}
		maxPID, perr := awaitPlaceholderPIDReservation(procBase, hostPID, need, log)
		if perr != nil {
			return nil, perr
		}
		log.WithField("maxNSPID", maxPID).Info("criu-v2: placeholder reserved its pid range")
	}
	// CRIU's log and stats, in a directory of the agent's.
	criuWorkDir := filepath.Join(a.config.CheckpointDir, ".restore-work", strconv.Itoa(hostPID))
	_ = os.RemoveAll(criuWorkDir)
	if merr := os.MkdirAll(criuWorkDir, 0o700); merr != nil {
		return nil, fmt.Errorf("criu-v2: restore work directory: %w", merr)
	}
	defer func() { _ = os.RemoveAll(criuWorkDir) }()
	criuWork, err := openConfinedDir(criuWorkDir)
	if err != nil {
		return nil, fmt.Errorf("criu-v2: restore work directory: %w", err)
	}
	defer func() { _ = criuWork.Close() }()

	log.WithFields(logrus.Fields{
		"placeholderPID": hostPID,
		"imagesDir":      checkpointDir,
	}).Info("criu-v2: restoring the container's pid namespace")

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
		if !tools.has(procBase, gpushareToolName) {
			return nil, fmt.Errorf("gpushare: %s is not in the placeholder's node bundle (version %s predates gpushare)", gpushareToolName, tools.version)
		}
	}
	// A group restore's session: the driver learns from it that this pod
	// is restored, and answers with the unlock (and, for gpushare, the
	// handle exchange of the resume).
	var sess *restoreSession
	if group.Session != "" {
		if group.InetAddrMap != "" && !criuSupportsNetMigration(a.agentCRIUHelp()) {
			return nil, errors.New("criu-v2: the agent's CRIU has no --inet-addr-map/net-unlock")
		}
		s, serr := a.openRestoreSession(group, work, gs != nil)
		if serr != nil {
			return nil, serr
		}
		sess = s
		defer sess.close()
		log = log.WithField("fabricSession", group.Session)
	}
	directIO := gs != nil && criuSupportsDirectImageIO(a.agentCRIUHelp())
	args := pidNSRestoreArgs(checkpointDir, criuWorkDir, PIDNSRestoreRoot, pidnsCapture.Mounts, pidnsCapture.UnixListeners,
		gs != nil, directIO, group.InetAddrMap, resolveCRIUPluginDir(a.config.CRIUPath, log))
	args = append(args, stdioInheritFDs(pidnsCapture)...)
	// The restore does not count as time the restored tasks ran
	// (restore_clock.go). With a GPU reload, that is the whole restore up to
	// the GPU resume: every task stays stopped until the clocks are there
	// (restore_clock_gpu.go).
	clockThroughGPU := gs != nil && clockThroughGPUSupported(a.agentCRIUHelp(), tools.gpuToolText())
	if clockArgs, budget := restoreClockArgs(checkpointDir, criuSupportsTimensBudget(a.agentCRIUHelp()), clockThroughGPU); clockArgs != nil {
		args = append(args, clockArgs...)
		msg := "criu-v2: the restored clocks continue from the dump once the restore has taken its budget"
		if clockThroughGPU {
			msg = "criu-v2: the restored clocks continue from the dump when the GPU processes resume"
		}
		log.WithField("budget", budget.Round(time.Second).String()).Info(msg)
	}
	criuStart := time.Now()
	// What the helper hands CRIU: the placeholder's root and network
	// namespace, its listening sockets (bound in its own mount namespace)
	// and its stdout and stderr.
	files, fds, err := a.pidNSRestoreFiles(ctx, procBase, hostPID, pidnsCapture, tools, log)
	if err != nil {
		return nil, fmt.Errorf("pid-namespace restore: %w", err)
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("pid-namespace restore: the agent binary: %w", err)
	}
	if pidnsCapture.Subtree {
		log.WithFields(logrus.Fields{"externalMounts": len(pidnsCapture.Mounts), "rootNSPID": pidnsCapture.RootNSPID}).
			Info("criu-v2: restoring the container's process tree into the pod's shared pid namespace")
	} else {
		log.WithField("externalMounts", len(pidnsCapture.Mounts)).Info("criu-v2: restoring into a nested pid namespace")
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	_, criuSpan := tracing.Tracer().Start(ctx, "restore.criu")
	criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "v2-pidns"))
	// The agent's own CRIU, entered only into the placeholder's pid and
	// network namespaces: CRIU's TCP lock and unlock act on the network
	// namespace it runs in, which must be the pod's
	// (docs/design/TOOLS-OUTSIDE-THE-WORKLOAD.md).
	nsArgs := append([]string{"-t", strconv.Itoa(hostPID), "-p", "-n", "--", self, "pidns-restore-exec", a.config.CRIUPath}, args...)
	cmd := exec.CommandContext(rctx, "nsenter", nsArgs...) //nolint:gosec // the agent's own binaries, arguments it builds
	cmd.ExtraFiles = files
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
	go cancelWhenRestoreFailed(rctx, criuWork, restoreFailureGrace, cancel, log)
	cmd.Env = []string{
		"PATH=" + filepath.Dir(a.config.CRIUPath) + ":/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		PIDNSFDsEnv + "=" + fds.String(),
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
		tail := "(no restore.log)"
		if b, terr := criuWork.readTail("restore.log", restoreLogTail); terr == nil {
			tail = tailOfText(string(b), 8)
		}
		return nil, fmt.Errorf("criu-v2 restore: %w (output: %s; restore.log tail: %s)", err, strings.TrimSpace(string(out)), tail)
	}
	criuSpan.End()
	// Through a GPU reload the whole restore is recorded at the GPU resume
	// (continueTasks), not CRIU's part of it.
	if spent, ok := recordRestoreClock(checkpointDir); ok && !clockThroughGPU {
		log.WithField("criuRestore", spent.Round(time.Millisecond).String()).Info("criu-v2: recorded the restore's length for the next one's clock budget")
	}
	if group.InetAddrMap != "" {
		// Before the unlock: a connection to a peer outside the group goes
		// to an address of the captured instance (criu_companion.go).
		// The pod's own captured address stays a peer when the pod kept
		// it (restore_address.go).
		resetDeadPeers(hostPID, keepAddresses(metadata.SourcePodIP, group.InetAddrMap), log)
		if err := natInstanceAddresses(hostPID, group.InetAddrMap, log); err != nil {
			return nil, fmt.Errorf("criu-v2: lead the instance's captured addresses to its restored pods: %w", err)
		}
	}

	fabricDir := ""
	if sess != nil {
		fabricDir = sess.containerDir
	}
	// resumeGPU reloads the restored processes' GPU memory (gpushare);
	// holdGate keeps their GPU launches waiting until openGPU. toolPID is
	// the pid the resume tool entered, once it ran, for openGPU.
	toolPID := 0
	var clockAt int64
	// clockWait is how long the GPU resume waited for the dumped clock:
	// not part of the restore's work, so not of the next one's budget.
	var clockWait time.Duration
	if clockThroughGPU {
		var err error
		if clockAt, err = dumpedMonotonic(ctx, checkpointDir); err != nil {
			return nil, fmt.Errorf("criu-v2: read the dumped clock the restored tasks resume at: %w", err)
		}
	}
	// continueTasks resumes the restored tasks the GPU resume did not, once
	// it has: CRIU left them all stopped for the clocks (clockThroughGPU).
	continueTasks := func() error {
		if !clockThroughGPU {
			return nil
		}
		b, err := criuWork.readFile(timensResumePIDsFile, fabricInLimit)
		if err != nil {
			return fmt.Errorf("criu-v2: read the tasks to resume: %w", err)
		}
		pids, err := parseTimensResumePIDs(b)
		if err != nil {
			return fmt.Errorf("criu-v2: read the tasks to resume: %w", err)
		}
		n, err := continueRestoredTasks(procBase, toolPID, hostPID, pids, gs.PIDs)
		if err != nil {
			return fmt.Errorf("criu-v2: resume the restored tasks: %w", err)
		}
		spent := time.Since(criuStart) - clockWait
		if err := recordRestoreSpan(checkpointDir, spent); err != nil {
			log.WithError(err).Warn("criu-v2: cannot record the restore's length; the next one estimates its clock budget")
		}
		log.WithFields(logrus.Fields{"tasks": n, "restoreSpan": spent.Round(time.Millisecond).String()}).
			Info("criu-v2: the restored tasks resumed with their clocks at the dumped values; recorded the restore's length for the next one's clock budget")
		return nil
	}
	resumeGPU := func(holdGate bool) error {
		_, gsSpan := tracing.Tracer().Start(ctx, "restore.gpushare_resume")
		gpuMap := gs.StorePath + "/" + gpushareGPUMapFile
		resumePID := hostPID
		{
			// The restored processes live in the nested pid namespace (or,
			// for a subtree, the placeholder's own); their saved pids are
			// pids there. They resume on the placeholder's GPUs, whose
			// entries the dump left out.
			p, err := restoredRootHostPID(procBase, hostPID, pidnsCapture)
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
			if err := sess.dir.own(procBase, resumePID); err != nil {
				gsSpan.End()
				return fmt.Errorf("gpushare resume: hand the fabric session to the restored processes' user: %w", err)
			}
		}
		toolPID = resumePID
		// Run to its end under its own deadline: cut off by the agent
		// stopping, it would leave the tree stopped without its GPU memory.
		waited, err := gpushareResumeWithGPUsOf(context.WithoutCancel(ctx), resumePID, hostPID, gs.PIDs, gpuMap, fabricDir, holdGate, clockAt, log)
		clockWait = waited
		if err != nil {
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
		//
		// Its GPU launches are held until every pod of the instance has
		// resumed (gpushareOpen). The unlock below is that point: a pod
		// reports restored only after its resume, and the unlock comes once
		// every pod has. Once held, the gate is opened on every path, or
		// the workload never launches again.
		held := false
		openGPU := func() error {
			if !held || toolPID == 0 {
				return nil
			}
			held = false
			return gpushareOpen(context.WithoutCancel(ctx), toolPID, gs.PIDs, log)
		}
		if gs != nil {
			held = true
			if err := resumeGPU(true); err != nil {
				if oerr := openGPU(); oerr != nil {
					log.WithError(oerr).Warn("gpushare: could not open the GPU launches of a failed resume")
				}
				return nil, err
			}
			if err := continueTasks(); err != nil {
				if oerr := openGPU(); oerr != nil {
					log.WithError(oerr).Warn("gpushare: could not open the GPU launches of a failed resume")
				}
				return nil, err
			}
		}
		// Every pod of the instance must be restored before any of them
		// sends: hold the lock until the driver says so, then release it in
		// this pod's network namespace.
		verdict, err := sess.awaitUnlock(ctx, log)
		if oerr := openGPU(); oerr != nil {
			if err == nil && verdict == "unlock" {
				return nil, fmt.Errorf("gpushare open: %w", oerr)
			}
			log.WithError(oerr).Warn("gpushare: could not open the GPU launches of an aborted group restore")
		}
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
		if err := resumeGPU(false); err != nil {
			return nil, err
		}
		if err := continueTasks(); err != nil {
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

// restoreFailureGrace is how long a criu that has logged "Restoring FAILED"
// gets to exit on its own before the agent kills its process group.
const restoreFailureGrace = 30 * time.Second

// restoreLogReportsFailure reports whether a CRIU restore log says the
// restore failed. CRIU writes the line once, after it has given up.
func restoreLogReportsFailure(content string) bool {
	return strings.Contains(content, "Restoring FAILED")
}

// restoreLogPoll is how often the agent reads the restore log for a
// failure.
var restoreLogPoll = 2 * time.Second

// restoreLogTail is how much of the end of CRIU's restore log the agent
// reads: its last lines say whether and how the restore failed.
const restoreLogTail = 1 << 20

// cancelWhenRestoreFailed polls the restore log in work until ctx ends.
// Once the log reports a failed restore it waits grace for criu to exit,
// then cancels, which kills the criu process group through cmd.Cancel.
func cancelWhenRestoreFailed(ctx context.Context, work *confinedDir, grace time.Duration, cancel context.CancelFunc, log *logrus.Entry) {
	tick := time.NewTicker(restoreLogPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		b, err := work.readTail("restore.log", restoreLogTail)
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
