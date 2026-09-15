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

func (a *Agent) restoreV2(ctx context.Context, metadata *CheckpointMetadata, checkpointDir string, placeholderInfo *containerd.ContainerInfo, startTime time.Time, log *logrus.Entry) (*RestoreResult, error) {
	if placeholderInfo == nil {
		return nil, fmt.Errorf("criu-v2 restore requires a placeholder pod (placeholderPodName/placeholderNamespace)")
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
		return nil, fmt.Errorf("criu-v2: target pod is running a GPU workload (pid %d) — refusing to restore into a non-placeholder", gpuPID)
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

	log.WithFields(logrus.Fields{
		"placeholderPID": hostPID,
		"imagesDir":      imgsInContainer,
	}).Info("criu-v2: restoring in-namespace")

	// Same execution shape as dumpV2: minimal env, PATH covers the bundle
	// (cuda_plugin execs cuda-checkpoint and CRIU network-lock execs
	// iptables-restore from PATH), no LD_LIBRARY_PATH.
	args := []string{
		"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/criu", "restore",
		"-D", imgsInContainer,
		"-o", "restore.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk", "--link-remap",
		"--libdir", v2BinDirInContainer,
		// Match the dump: unlock TCP in-process via libnftables (workload
		// images ship no iptables binary).
		"--network-lock", "nftables",
		// Never restore cgroup membership: the dumped paths are the SOURCE
		// pod's kubepods slice, which kubelet has already torn down.
		// Recreating it puts the restored tree into an orphaned pod cgroup
		// that kubelet housekeeping SIGKILLs (instantly on quick retries,
		// ~90s in otherwise — killing the target mid GPU-resume, which
		// surfaced as CUDA "OS call failed"). Same setting the legacy
		// engine uses (ManageCgroups: ignore).
		"--manage-cgroups=ignore",
		"--restore-detached",
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	_, criuSpan := tracing.Tracer().Start(ctx, "restore.criu")
	criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "v2-inns"))
	cmd := exec.CommandContext(rctx, "nsenter", args...)
	// No LD_LIBRARY_PATH: the bundle's libraries carry RPATH=$ORIGIN (see
	// Dockerfile.base), so criu resolves its dependency graph from
	// /criu-bundle/lib on its own. Setting it here would leak the bundle's
	// glibc into cuda-checkpoint and abort restore into newer-glibc containers.
	cmd.Env = []string{
		"PATH=" + v2BinDirInContainer + ":/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	// Place criu — and therefore the restored tree, which inherits its
	// cgroup (--manage-cgroups=ignore) — into the PLACEHOLDER's cgroup via
	// clone3(CLONE_INTO_CGROUP). Without this the restored workload lives
	// in the agent's cgroup: wrong accounting, and an agent restart or
	// rollout kills it. In the placeholder's cgroup its lifecycle follows
	// the placeholder pod (deleting the pod kills the restored tree).
	if cgFD, cerr := placeholderCgroupDirFD(procBase, hostPID); cerr != nil {
		log.WithError(cerr).Warn("criu-v2: could not open placeholder cgroup; restored tree will live in the agent's cgroup")
	} else {
		defer func() { _ = syscall.Close(cgFD) }()
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cgFD}
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

// gpuProcessInSamePidNS returns the host pid of any GPU-using process that
// shares the target container's pid namespace, or 0 if none.
func (a *Agent) gpuProcessInSamePidNS(ctx context.Context, procBase string, containerPID int) (int, error) {
	targetNS, err := os.Readlink(filepath.Join(procBase, strconv.Itoa(containerPID), "ns", "pid"))
	if err != nil {
		return 0, err
	}
	gpuPIDs, err := a.cuda.FindGPUProcesses(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range gpuPIDs {
		ns, rerr := os.Readlink(filepath.Join(procBase, strconv.Itoa(p), "ns", "pid"))
		if rerr == nil && ns == targetNS {
			return p, nil
		}
	}
	return 0, nil
}
