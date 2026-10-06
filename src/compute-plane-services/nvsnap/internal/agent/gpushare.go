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

// gpushare: checkpoint/restore of workloads whose processes share GPU
// memory (NCCL P2P and NVLS, CUDA IPC, page-locked host memory). The workload
// runs with libnvsnap_gpushare.so preloaded (the webhook places it, see
// internal/webhook/gpushare.go). Around the criu-v2 dump, nvsnap-gpu-suspend
// releases the shared memory to chunk files and checkpoints the driver state
// of every process; CRIU then dumps the CPU side only, without the CUDA
// plugin. Restore runs CRIU, then nvsnap-gpu-suspend resume. See
// docs/GPUSHARE.md.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// gpushareLibName is the preload library a workload runs under.
	gpushareLibName = "libnvsnap_gpushare.so"
	// gpushareToolName drives it; staged into the container with criu.
	gpushareToolName = "nvsnap-gpu-suspend"
	// GPUShareStoreInContainer is where the workload sees its chunk store.
	// The webhook mounts a per-pod node-local directory here; a restore
	// placeholder mounts the checkpoint's copy at the same path, because
	// the restored processes keep the path they saved to.
	GPUShareStoreInContainer = webhook.GPUShareStorePath
	// gpushareGPUMapFile, in the store root, lists the capture's GPU UUIDs.
	gpushareGPUMapFile = "gpus"
	// GPUShareCheckpointSubdir is where a checkpoint keeps its store copy,
	// relative to the checkpoint directory.
	GPUShareCheckpointSubdir = "gpushare"
	// GPUSharePodStoresSubdir holds the per-pod stores under the node-local
	// checkpoint root (the webhook mounts <root>/<this>/<pod-uid>).
	GPUSharePodStoresSubdir = "gpushare-pods"
)

// ErrCaptureInProgress is returned when a checkpoint of the same container
// is already running.
var ErrCaptureInProgress = errors.New("a checkpoint of this workload is already in progress")

// GPUShareInfo records how a gpushare checkpoint was taken; restore needs it.
type GPUShareInfo struct {
	// PIDs are the namespace pids nvsnap-gpu-suspend suspended, leader
	// first. CRIU restores the same pids, so resume addresses the same set.
	PIDs []int `json:"pids"`
	// StorePath is the chunk store path inside the workload's mount
	// namespace (GPUShareStoreInContainer at capture time).
	StorePath string `json:"storePath"`
	// LibPath is where the processes mapped the shim from; a placeholder
	// must provide the same file at the same path.
	LibPath string `json:"libPath"`
}

// gpushareProc is what the dump needs to know about one process.
type gpushareProc struct {
	hostPID, nsPID int
	shim           bool   // maps libnvsnap_gpushare.so
	gpu            bool   // maps or holds a /dev/nvidia* device
	libPath        string // path the shim is mapped from
}

// gpushareTargets inspects every process of the session that will be dumped
// (session leader sessionHostPID, host pid numbering under procBase). It
// returns the namespace pids to suspend, leader first, and the shim path,
// with ok=false when no process loads the shim (a plain criu-v2 capture).
// A process that uses the GPU without the shim cannot be checkpointed this
// way, since the driver state it holds would reach CRIU unsuspended; that is
// an error, not a fallback.
func gpushareTargets(procBase string, sessionHostPID int) (nsPIDs []int, libPath string, ok bool, err error) {
	entries, err := os.ReadDir(procBase)
	if err != nil {
		return nil, "", false, err
	}
	procs := make([]gpushareProc, 0, 8)
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue
		}
		sid, serr := sessionID(procBase, pid)
		if serr != nil || sid != sessionHostPID {
			continue
		}
		p := gpushareProc{hostPID: pid}
		p.shim, p.gpu, p.libPath = scanMaps(filepath.Join(procBase, e.Name(), "maps"))
		if !p.gpu {
			p.gpu = holdsNvidiaFD(filepath.Join(procBase, e.Name(), "fd"))
		}
		if !p.shim && !p.gpu {
			continue
		}
		if p.nsPID, err = nsPidOf(procBase, pid); err != nil {
			return nil, "", false, fmt.Errorf("namespace pid of %d: %w", pid, err)
		}
		procs = append(procs, p)
	}
	var bare []string
	for _, p := range procs {
		if p.shim {
			ok = true
			if libPath == "" {
				libPath = p.libPath
			}
		} else if p.gpu {
			bare = append(bare, strconv.Itoa(p.hostPID))
		}
	}
	if !ok {
		return nil, "", false, nil
	}
	if len(bare) > 0 {
		return nil, "", false, fmt.Errorf("gpushare: process(es) %s use the GPU without %s; "+
			"every GPU process of the workload must run under it (LD_PRELOAD)", strings.Join(bare, ","), gpushareLibName)
	}
	sort.Slice(procs, func(i, j int) bool {
		if (procs[i].hostPID == sessionHostPID) != (procs[j].hostPID == sessionHostPID) {
			return procs[i].hostPID == sessionHostPID
		}
		return procs[i].nsPID < procs[j].nsPID
	})
	for _, p := range procs {
		if p.shim {
			nsPIDs = append(nsPIDs, p.nsPID)
		}
	}
	return nsPIDs, libPath, true, nil
}

// scanMaps reports whether a process maps the shim (and from where) and
// whether it maps an NVIDIA device.
func scanMaps(path string) (shim, gpu bool, libPath string) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, "/")
		if i < 0 {
			continue
		}
		p := strings.TrimSuffix(line[i:], " (deleted)")
		switch {
		case filepath.Base(p) == gpushareLibName:
			shim = true
			libPath = p
		case strings.HasPrefix(p, "/dev/nvidia"):
			gpu = true
		}
	}
	return shim, gpu, libPath
}

// holdsNvidiaFD reports whether a process has an NVIDIA device open.
func holdsNvidiaFD(fdDir string) bool {
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		if t, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && strings.HasPrefix(t, "/dev/nvidia") {
			return true
		}
	}
	return false
}

// gpushareTool runs nvsnap-gpu-suspend inside the namespaces of the
// container whose init is hostPID. The tool talks to each process's control
// socket and checks that the peer is the process itself, so it must run in
// the workload's pid namespace (-p) as well as its network namespace (-n,
// abstract unix sockets) and mount namespace (-m, the store path).
func gpushareTool(ctx context.Context, hostPID int, timeout time.Duration, args ...string) (string, error) {
	return gpushareToolEnv(ctx, hostPID, nil, timeout, args...)
}

// gpushareToolEnv is gpushareTool with extra environment for the tool.
func gpushareToolEnv(ctx context.Context, hostPID int, env []string, timeout time.Duration, args ...string) (string, error) {
	nsArgs := append([]string{"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/" + gpushareToolName}, args...)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "nsenter", nsArgs...) //nolint:gosec // fixed binary; args are pids and tool verbs built here
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}, env...)
	// Stdout only is returned (the GPU map is read from it); stderr joins
	// it in the error so a failure carries the tool's own explanation.
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w (output: %s)", gpushareToolName, strings.Join(args, " "), err,
			lastLines(out.String()+errOut.String(), 8))
	}
	return out.String(), nil
}

// gpushareSuspend saves the GPU memory of pids to the store and checkpoints
// their driver state, then hands them to CRIU stopped. It returns the GPU
// map (UUIDs of the GPUs the workload saw, in order) for the checkpoint:
// a restore onto other physical GPUs maps them with resume --gpu-map.
func gpushareSuspend(ctx context.Context, hostPID int, pids []int, log *logrus.Entry) (string, error) {
	store := GPUShareStoreInContainer
	gpus, err := gpushareToolEnv(ctx, hostPID, podGPUEnv(hostPID), time.Minute, "gpus")
	if err != nil {
		return "", err
	}
	// The per-pid chunk lists go in the store root: the mount guarantees it
	// exists, while a subdirectory would have to be created inside the
	// workload's mount namespace, which the agent cannot do through
	// /proc/<pid>/root (and the tool does not create --ckpt-dir).
	args := append([]string{"--timeout-ms", "120000", "--store", store, "--ckpt-dir", store, "suspend"}, intsToStrings(pids)...)
	t0 := time.Now()
	out, err := gpushareTool(ctx, hostPID, 30*time.Minute, args...)
	if err != nil {
		return "", err
	}
	log.WithFields(logrus.Fields{"pids": pids, "duration": time.Since(t0).Round(time.Millisecond).String()}).
		Info("gpushare: suspended " + lastLines(out, 1))
	if _, err := gpushareTool(ctx, hostPID, 2*time.Minute, append([]string{"stop"}, intsToStrings(pids)...)...); err != nil {
		return "", err
	}
	return gpus, nil
}

// gpushareResume restores the driver state of pids, loads their saved GPU
// memory, re-creates the shared mappings and lets them run. gpuMap, when
// set, maps the capture's GPUs onto the ones visible here.
func gpushareResume(ctx context.Context, hostPID int, pids []int, gpuMap string, log *logrus.Entry) error {
	var args []string
	if gpuMap != "" {
		args = append(args, "--gpu-map", gpuMap)
	}
	args = append(append(args, "resume"), intsToStrings(pids)...)
	t0 := time.Now()
	out, err := gpushareToolEnv(ctx, hostPID, podGPUEnv(hostPID), 60*time.Minute, args...)
	if err != nil {
		return err
	}
	log.WithFields(logrus.Fields{"pids": pids, "duration": time.Since(t0).Round(time.Millisecond).String()}).
		Info("gpushare: resumed " + lastLines(out, 1))
	return nil
}

func intsToStrings(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// gpushareRestoreMounts returns the extra volumeMounts and volumes (YAML,
// indented for the placeholder manifest) a gpushare checkpoint needs: the
// node bundle where the processes mapped the shim from, and the
// checkpoint's chunk store at the path they saved to.
func (a *Agent) gpushareRestoreMounts(m *CheckpointMetadata, checkpointID string) (mounts, volumes string) {
	gs := m.GPUShare
	if gs == nil {
		return "", ""
	}
	bundleRoot := a.config.Webhook.HostBundleRoot
	if bundleRoot == "" {
		bundleRoot = defaultHostBundleRoot
	}
	libDir := filepath.Dir(gs.LibPath)
	mounts = fmt.Sprintf(`
        - name: nvsnap-gpushare-lib
          mountPath: %s
          readOnly: true
        - name: nvsnap-gpushare-store
          mountPath: %s`, libDir, gs.StorePath)
	volumes = fmt.Sprintf(`
    - name: nvsnap-gpushare-lib
      hostPath:
        path: %s/nvsnap
        type: Directory
    - name: nvsnap-gpushare-store
      hostPath:
        path: %s
        type: Directory`, bundleRoot, filepath.Join(a.checkpointHostRoot(), checkpointID, GPUShareCheckpointSubdir))
	return mounts, volumes
}

// gpushareGPUCount is the number of GPUs the checkpoint was taken on (lines
// of its GPU map), at least 1.
func gpushareGPUCount(gpuMap string) int {
	b, err := os.ReadFile(gpuMap)
	if err != nil {
		return 1
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// defaultHostBundleRoot matches the agent DaemonSet's nvsnap-bundle-stage
// destination.
const defaultHostBundleRoot = webhook.DefaultHostBundleRoot

// checkpointHostRoot is the node path behind the agent's checkpoint
// directory: what a pod's hostPath volume must name. The agent's own
// CheckpointDir is the in-container mount and does not exist on the node.
func (a *Agent) checkpointHostRoot() string {
	if a.config.CheckpointHostDir != "" {
		return a.config.CheckpointHostDir
	}
	return a.config.CheckpointDir
}

// podGPUEnv limits the tool to the GPUs Kubernetes allocated to the pod
// whose init is hostPID. A privileged container (every restore placeholder)
// sees all of the node's GPUs, and the tool maps a checkpoint onto the GPUs
// it sees, in order; without this it would pick the node's first GPUs
// rather than the pod's. The device plugin records the allocation as UUIDs
// in NVIDIA_VISIBLE_DEVICES on the pod's processes.
func podGPUEnv(hostPID int) []string {
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	if v := visibleGPUUUIDs(filepath.Join(procBase, strconv.Itoa(hostPID), "environ")); v != "" {
		return []string{"CUDA_VISIBLE_DEVICES=" + v}
	}
	return nil
}

// visibleGPUUUIDs returns NVIDIA_VISIBLE_DEVICES from an environ file when
// it lists GPU UUIDs (not "all", "void" or indices).
func visibleGPUUUIDs(environ string) string {
	b, err := os.ReadFile(environ)
	if err != nil {
		return ""
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		v, ok := strings.CutPrefix(kv, "NVIDIA_VISIBLE_DEVICES=")
		if !ok {
			continue
		}
		for _, u := range strings.Split(v, ",") {
			if !strings.HasPrefix(strings.TrimSpace(u), "GPU-") {
				return ""
			}
		}
		return v
	}
	return ""
}

// gpushareLoadedBy reports whether any of the host pids maps the shim.
func gpushareLoadedBy(hostPIDs []int) bool {
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	for _, pid := range hostPIDs {
		if shim, _, _ := scanMaps(filepath.Join(procBase, strconv.Itoa(pid), "maps")); shim {
			return true
		}
	}
	return false
}
