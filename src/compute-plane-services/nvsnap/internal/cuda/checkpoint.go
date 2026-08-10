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

// Package cuda manages cuda-checkpoint operations and GPU process discovery.
package cuda

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// Manager handles cuda-checkpoint operations
type Manager struct {
	cudaCheckpointPath string
	inContainer        bool
	log                *logrus.Logger
}

// New creates a Manager for running cuda-checkpoint operations.
func New(cudaCheckpointPath string, inContainer bool, log *logrus.Logger) *Manager {
	if cudaCheckpointPath == "" {
		cudaCheckpointPath = "/app/bin/cuda-checkpoint"
	}
	return &Manager{
		cudaCheckpointPath: cudaCheckpointPath,
		inContainer:        inContainer,
		log:                log,
	}
}

// Lock pauses the GPU work of a process so it can be checkpointed.
func (m *Manager) Lock(ctx context.Context, pid int) error {
	m.log.WithField("pid", pid).Info("Locking GPU")
	return m.runCudaCheckpoint(ctx, pid, "lock", "--timeout", "30000")
}

// Checkpoint dumps the GPU state of a locked process to host memory.
func (m *Manager) Checkpoint(ctx context.Context, pid int) error {
	m.log.WithField("pid", pid).Info("Checkpointing GPU state")
	// Use a generous timeout independent of the HTTP request context.
	// GPU memory dump for large models (70B, ~35GB/GPU) can take 10+ minutes.
	ckptCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return m.runCudaCheckpoint(ckptCtx, pid, "checkpoint")
}

// Restore reloads previously checkpointed GPU state into a process.
func (m *Manager) Restore(ctx context.Context, pid int) error {
	m.log.WithField("pid", pid).Info("Restoring GPU state")
	return m.runCudaCheckpoint(ctx, pid, "restore")
}

// Unlock resumes the GPU work of a process after checkpoint or restore.
func (m *Manager) Unlock(ctx context.Context, pid int) error {
	m.log.WithField("pid", pid).Info("Unlocking GPU")
	return m.runCudaCheckpoint(ctx, pid, "unlock")
}

// LockAndCheckpoint locks the GPU then checkpoints its state.
func (m *Manager) LockAndCheckpoint(ctx context.Context, pid int) error {
	if err := m.Lock(ctx, pid); err != nil {
		return err
	}
	return m.Checkpoint(ctx, pid)
}

// RestoreAndUnlock restores the GPU state then unlocks it.
func (m *Manager) RestoreAndUnlock(ctx context.Context, pid int) error {
	if err := m.Restore(ctx, pid); err != nil {
		return err
	}
	return m.Unlock(ctx, pid)
}

// HasCUDAContext checks if a process has an active CUDA context by querying
// cuda-checkpoint state. Returns true if the process can be checkpointed,
// false if it has no CUDA context (inherited nvidia FDs but never called cuInit).
func (m *Manager) HasCUDAContext(ctx context.Context, pid int) bool {
	err := m.runCudaCheckpoint(ctx, pid, "state")
	return err == nil
}

// FindGPUProcesses returns the PIDs of all processes using a GPU on the node.
func (m *Manager) FindGPUProcesses(ctx context.Context) ([]int, error) {
	var cmd *exec.Cmd

	if m.inContainer {
		cmd = exec.CommandContext(ctx, "nsenter", "-t", "1", "-m", "--",
			"nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader")
	} else {
		cmd = exec.CommandContext(ctx, "nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader")
	}

	m.log.Debug("Running nvidia-smi to find GPU processes")
	output, err := cmd.Output()
	if err != nil {
		m.log.WithError(err).Debug("nvidia-smi failed, trying /proc scan")
		return m.findGPUProcessesViaProcScan(ctx)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	pids := make([]int, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "[No running processes found]" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}

	m.log.WithField("pids", pids).Debug("Found GPU processes")
	return pids, nil
}

func (m *Manager) findGPUProcessesViaProcScan(_ context.Context) ([]int, error) {
	var pids []int

	procPath := "/proc"
	if m.inContainer {
		procPath = "/host/proc"
	}

	entries, err := os.ReadDir(procPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", procPath, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		fdPath := filepath.Join(procPath, entry.Name(), "fd")
		fds, err := os.ReadDir(fdPath)
		if err != nil {
			continue
		}

		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdPath, fd.Name()))
			if err != nil {
				continue
			}
			if strings.Contains(link, "/dev/nvidia") {
				pids = append(pids, pid)
				break
			}
		}
	}

	return pids, nil
}

// FindAllGPUPIDsForContainer returns all GPU process PIDs belonging to a container.
// For multi-GPU workloads (e.g. vLLM TP=4), multiple processes share the container.
func (m *Manager) FindAllGPUPIDsForContainer(ctx context.Context, containerPID int) ([]int, error) {
	gpuPIDs, err := m.FindGPUProcesses(ctx)
	if err != nil {
		return nil, err
	}

	var containerGPUPIDs []int
	for _, gpuPID := range gpuPIDs {
		if gpuPID == containerPID || isChildOf(gpuPID, containerPID) || inSameCgroup(gpuPID, containerPID) {
			containerGPUPIDs = append(containerGPUPIDs, gpuPID)
		}
	}

	if len(containerGPUPIDs) == 0 {
		return nil, fmt.Errorf("no GPU processes found for container PID %d (GPU PIDs: %v)", containerPID, gpuPIDs)
	}

	m.log.WithFields(logrus.Fields{
		"containerPID": containerPID,
		"gpuPIDs":      containerGPUPIDs,
	}).Debug("Found GPU processes for container")
	return containerGPUPIDs, nil
}

// FindGPUPIDForContainer returns a single GPU process PID belonging to the container.
func (m *Manager) FindGPUPIDForContainer(ctx context.Context, containerPID int) (int, error) {
	gpuPIDs, err := m.FindGPUProcesses(ctx)
	if err != nil {
		return 0, err
	}

	m.log.WithFields(logrus.Fields{
		"containerPID": containerPID,
		"gpuPIDs":      gpuPIDs,
	}).Debug("Looking for GPU process belonging to container")

	for _, gpuPID := range gpuPIDs {
		if isChildOf(gpuPID, containerPID) {
			m.log.WithField("gpuPID", gpuPID).Debug("Found GPU process as child of container")
			return gpuPID, nil
		}
	}

	for _, gpuPID := range gpuPIDs {
		if gpuPID == containerPID {
			return gpuPID, nil
		}
	}

	for _, gpuPID := range gpuPIDs {
		if inSameCgroup(gpuPID, containerPID) {
			m.log.WithField("gpuPID", gpuPID).Debug("Found GPU process in same cgroup as container")
			return gpuPID, nil
		}
	}

	return 0, fmt.Errorf("no GPU process found for container PID %d (GPU PIDs: %v)", containerPID, gpuPIDs)
}

// runCudaCheckpoint runs cuda-checkpoint
// When in container mode:
// 1. Copy cuda-checkpoint to a hostPath location
// 2. Run nsenter into HOST mount namespace
// 3. Execute cuda-checkpoint with LD_LIBRARY_PATH set to host's NVIDIA driver libs
func (m *Manager) runCudaCheckpoint(ctx context.Context, targetPID int, action string, extraArgs ...string) error {
	args := []string{"--action", action, "--pid", strconv.Itoa(targetPID)}
	args = append(args, extraArgs...)

	var cmd *exec.Cmd
	if m.inContainer {
		// Container path: /var/lib/nvsnap/checkpoints (what we see — agent's checkpoint-dir flag)
		// Host path: set via NVSNAP_CHECKPOINT_HOST_PATH env (daemonset hostPath mount).
		// Defaults to containerd's path for backwards compat; CRI-O daemonset
		// sets it to /var/lib/nvsnap/checkpoints so nsenter can find the binary.
		containerPath := "/var/lib/nvsnap/checkpoints/cuda-checkpoint"
		containerPathReal := "/var/lib/nvsnap/checkpoints/cuda-checkpoint.real"
		hostDir := os.Getenv("NVSNAP_CHECKPOINT_HOST_PATH")
		if hostDir == "" {
			hostDir = "/var/lib/containerd/nvsnap-checkpoints"
		}
		hostPath := hostDir + "/cuda-checkpoint"

		// Copy cuda-checkpoint wrapper script to the shared hostPath
		if err := copyFile(m.cudaCheckpointPath, containerPath); err != nil {
			return fmt.Errorf("failed to copy cuda-checkpoint: %w", err)
		}

		// Copy the real cuda-checkpoint binary (wrapper calls this)
		realBinaryPath := m.cudaCheckpointPath + ".real"
		if _, err := os.Stat(realBinaryPath); err == nil {
			if err := copyFile(realBinaryPath, containerPathReal); err != nil {
				m.log.WithError(err).Warn("Failed to copy cuda-checkpoint.real, wrapper may not work")
			}
		}

		// Find the libcuda.so directory. Prefer runtime discovery from the
		// workload's own /proc/<pid>/maps (works on any cluster regardless
		// of where the driver-installer put libs) and fall back to a
		// hardcoded probe of known paths if the workload hasn't dlopen'd
		// libcuda yet. See docs/architecture/06-CUDA-CHECKPOINT-CALL-FLOW.md.
		var cudaLibsDir string
		containerView, hostView, derr := DiscoverCudaLibDir("/host/proc", "/host", targetPID)
		if derr == nil && hostView != "" {
			cudaLibsDir = hostView
			// Tell the CRIU-plugin path (which runs inside the agent container
			// and uses the /host-prefixed view) where to look — the wrapper
			// script consumes NVSNAP_CUDA_LIB_DIR and prepends to its
			// LD_LIBRARY_PATH. CRIU swrk inherits this when spawned later.
			_ = os.Setenv("NVSNAP_CUDA_LIB_DIR", containerView)
			m.log.WithFields(logrus.Fields{
				"hostView":      hostView,
				"containerView": containerView,
				"targetPID":     targetPID,
			}).Debug("Discovered libcuda directory from /proc/maps")
		} else {
			if derr != nil {
				m.log.WithError(derr).Debug("libcuda discovery from /proc/maps failed; falling back to known paths")
			}
			cudaLibsDir = m.findHostNvidiaLibs()
		}

		// Run via nsenter into HOST mount namespace, using the HOST path
		nsenterArgs := []string{"-t", "1", "-m", "--"}
		if cudaLibsDir != "" {
			nsenterArgs = append(nsenterArgs, "env", "LD_LIBRARY_PATH="+cudaLibsDir)
		}
		nsenterArgs = append(nsenterArgs, hostPath) // Use host path, not container path
		nsenterArgs = append(nsenterArgs, args...)
		cmd = exec.CommandContext(ctx, "nsenter", nsenterArgs...)
		cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	} else {
		//nolint:gosec // args are internally constructed, not user input
		cmd = exec.CommandContext(ctx, m.cudaCheckpointPath, args...)
		cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	}

	m.log.WithFields(logrus.Fields{
		"action":      action,
		"targetPID":   targetPID,
		"inContainer": m.inContainer,
	}).Debug("Running cuda-checkpoint")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cuda-checkpoint %s failed: %w\nOutput: %s", action, err, string(output))
	}

	m.log.WithField("output", strings.TrimSpace(string(output))).Debug("cuda-checkpoint completed")
	return nil
}

// setupCudaLibs copies ONLY libcuda.so to a temporary directory to avoid GLIBC conflicts
// findHostNvidiaLibs finds NVIDIA libs on the host filesystem
// This is called when running inside a container with hostPID/hostNetwork
// Returns the path as it would be on the HOST (for nsenter)
func (m *Manager) findHostNvidiaLibs() string {
	// Define paths to check: (agentPath to check, hostPath to return)
	// agentPath is where the agent can stat the file
	// hostPath is what we pass to LD_LIBRARY_PATH for nsenter'd command
	type pathPair struct {
		agentPath string // Path visible from agent container
		hostPath  string // Path on actual host for LD_LIBRARY_PATH
	}

	var paths []pathPair
	if m.inContainer {
		paths = []pathPair{
			// GKE: NVIDIA driver is under /run/nvidia/driver on host
			// Agent has /host/run mounted as /host/run
			{"/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu", "/run/nvidia/driver/usr/lib/x86_64-linux-gnu"},
			{"/host/run/nvidia/driver/usr/lib64", "/run/nvidia/driver/usr/lib64"},
			// Standard paths on bare-metal (mounted at /host/usr)
			{"/host/usr/local/nvidia/lib64", "/usr/local/nvidia/lib64"},
			{"/host/usr/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu"},
		}
	} else {
		// Not in container - paths are direct
		paths = []pathPair{
			{"/run/nvidia/driver/usr/lib/x86_64-linux-gnu", "/run/nvidia/driver/usr/lib/x86_64-linux-gnu"},
			{"/usr/local/nvidia/lib64", "/usr/local/nvidia/lib64"},
			{"/usr/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu"},
		}
	}

	for _, p := range paths {
		libcudaPath := filepath.Join(p.agentPath, "libcuda.so.1")
		if _, err := os.Stat(libcudaPath); err == nil {
			m.log.WithFields(logrus.Fields{
				"agentPath": p.agentPath,
				"hostPath":  p.hostPath,
			}).Debug("Found NVIDIA libs on host")
			return p.hostPath
		}
	}

	m.log.Warn("Could not find NVIDIA libs on host, cuda-checkpoint may fail")
	return ""
}

func isChildOf(pid, parentPID int) bool {
	currentPID := pid
	for currentPID > 1 {
		ppid := getParentPID(currentPID)
		if ppid == parentPID {
			return true
		}
		if ppid <= 1 {
			break
		}
		currentPID = ppid
	}
	return false
}

func getParentPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		data, err = os.ReadFile(fmt.Sprintf("/host/proc/%d/stat", pid))
		if err != nil {
			return 0
		}
	}

	str := string(data)
	start := strings.Index(str, "(")
	end := strings.LastIndex(str, ")")
	if start == -1 || end == -1 {
		return 0
	}

	parts := strings.Fields(str[end+2:])
	if len(parts) < 2 {
		return 0
	}

	ppid, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return ppid
}

func inSameCgroup(pid1, pid2 int) bool {
	cgroup1 := getCgroup(pid1)
	cgroup2 := getCgroup(pid2)
	if cgroup1 == "" || cgroup2 == "" {
		return false
	}
	return strings.Contains(cgroup1, cgroup2) || strings.Contains(cgroup2, cgroup1)
}

func getCgroup(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		data, err = os.ReadFile(fmt.Sprintf("/host/proc/%d/cgroup", pid))
		if err != nil {
			return ""
		}
	}
	return string(data)
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}
