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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/streamer"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// nvidiaDeviceMajors returns the set of major numbers for nvidia devices
// by parsing /proc/devices. This is dynamic — works regardless of which
// major numbers the driver allocates.
func nvidiaDeviceMajors() map[uint32]bool {
	majors := make(map[uint32]bool)
	// Always include 195 (nvidia) as fallback
	majors[195] = true

	data, err := os.ReadFile("/proc/devices")
	if err != nil {
		// Try host-mounted path
		data, err = os.ReadFile("/host/proc/devices")
		if err != nil {
			return majors
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if !strings.Contains(fields[1], "nvidia") {
			continue
		}
		maj, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			continue
		}
		majors[uint32(maj)] = true
	}
	return majors
}

// scanCharDeviceFDs scans a process's file descriptors for character devices
// and returns them as CRIU external resource strings. This is generic — it
// catches nvidia, gdrdrv, and any future GPU-related drivers without needing
// to maintain a list of known device majors.
//
// CRIU can't dump character devices natively, so they must be declared external.
// The CUDA plugin's DUMP_EXT_FILE/RESTORE_EXT_FILE hooks handle save and restore
// by recording the device path + major:minor, then reopening or mknod'ing on restore.
func scanCharDeviceFDs(pid int, log *logrus.Entry) []string {
	var externals []string        //nolint:prealloc // length depends on runtime fd scan
	seen := make(map[string]bool) // Dedupe by dev[major/minor]

	// Agent runs in container with /host/proc mounted - check both paths
	fdPath := fmt.Sprintf("/host/proc/%d/fd", pid)
	if _, err := os.Stat(fdPath); os.IsNotExist(err) {
		fdPath = fmt.Sprintf("/proc/%d/fd", pid)
	}
	entries, err := os.ReadDir(fdPath)
	if err != nil {
		log.WithError(err).Warn("Failed to read process FDs for device scan")
		return externals
	}

	for _, entry := range entries {
		linkPath := filepath.Join(fdPath, entry.Name())
		target, err := os.Readlink(linkPath)
		if err != nil {
			continue
		}

		// Stat the FD to get device major/minor
		statPath := linkPath
		var stat syscall.Stat_t
		if err := syscall.Stat(statPath, &stat); err != nil {
			continue
		}

		// Only character devices — CRIU handles regular files, pipes, sockets natively
		if stat.Mode&syscall.S_IFMT != syscall.S_IFCHR {
			continue
		}
		major := uint32(stat.Rdev >> 8 & 0xfff) //nolint:gosec // bounded: masked device-number bits / known-positive value
		minor := uint32(stat.Rdev & 0xff)       //nolint:gosec // bounded: masked device-number bits / known-positive value

		// Skip well-known chardevs that CRIU handles natively:
		// major 1 = /dev/null(3), /dev/zero(5), /dev/full(7), /dev/random(8), /dev/urandom(9)
		// major 5 = /dev/tty(0), /dev/ptmx(2)
		// major 136 = /dev/pts/*
		if major == 1 || major == 5 || major == 136 {
			continue
		}

		name := filepath.Base(target)
		external := fmt.Sprintf("dev[%d/%d]:%s", major, minor, name)
		if seen[external] {
			continue
		}
		seen[external] = true

		externals = append(externals, external)

		log.WithFields(logrus.Fields{
			"fd":     entry.Name(),
			"target": target,
			"major":  major,
			"minor":  minor,
		}).Debug("Found NVIDIA device FD")
	}

	return externals
}

// getCgroupFreezePath returns the cgroup.freeze path for a container process.
// Works with cgroup v2 on GKE. Returns empty string if not found.
func getCgroupFreezePath(pid int, log *logrus.Entry) string {
	cgPath := fmt.Sprintf("/host/proc/%d/cgroup", pid)
	if _, err := os.Stat(cgPath); os.IsNotExist(err) {
		cgPath = fmt.Sprintf("/proc/%d/cgroup", pid)
	}
	data, err := os.ReadFile(cgPath)
	if err != nil {
		log.WithError(err).Debug("Could not read cgroup")
		return ""
	}
	// cgroup v2: single line "0::<path>"
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" {
			freezePath := filepath.Join("/sys", "fs", "cgroup", parts[2], "cgroup.freeze") //nolint:gocritic // leading-slash absolute root
			if _, err := os.Stat(freezePath); err == nil {
				log.WithField("path", freezePath).Debug("Found cgroup.freeze")
				return freezePath
			}
		}
	}
	return ""
}

// countDistinctGPUDevices returns the number of distinct physical GPUs used by
// the given processes. Queries nvidia-smi for the authoritative GPU-to-process
// mapping from the NVIDIA driver. This works correctly regardless of container
// privileges, CUDA_VISIBLE_DEVICES, or how many /dev/nvidiaX nodes are visible.
func countDistinctGPUDevices(pids []int, log *logrus.Entry) int {
	if len(pids) == 0 {
		return 0
	}

	// Build a set of target PIDs for fast lookup
	pidSet := make(map[int]bool, len(pids))
	for _, p := range pids {
		pidSet[p] = true
	}

	// Query nvidia-smi for all active compute processes and their GPU index.
	// Output format: "pid, gpu_bus_id" per line (csv, no header).
	// gpu_bus_id is unique per physical GPU (e.g., "00000000:04:00.0").
	// nvidia-smi lives on the host — try common paths.
	nvidiaSmi := "nvidia-smi"
	for _, p := range []string{
		"/host/run/nvidia/driver/usr/bin/nvidia-smi",
		"/host/usr/bin/nvidia-smi",
		"/host/usr/local/nvidia/bin/nvidia-smi",
	} {
		if _, err := os.Stat(p); err == nil {
			nvidiaSmi = p
			break
		}
	}
	cmd := exec.Command(nvidiaSmi,
		"--query-compute-apps=pid,gpu_bus_id",
		"--format=csv,noheader")
	// nvidia-smi needs the driver's shared libraries
	cmd.Env = append(os.Environ(),
		"LD_LIBRARY_PATH=/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu:/usr/local/nvidia/lib64")
	out, err := cmd.Output()
	if err != nil {
		log.WithError(err).WithField("path", nvidiaSmi).Warn("nvidia-smi query failed, assuming single GPU")
		return 1
	}

	gpus := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "PID, BUS_ID" (e.g., "12345, 00000000:04:00.0")
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		if pidSet[pid] {
			busID := strings.TrimSpace(parts[1])
			gpus[busID] = true
		}
	}

	if len(gpus) > 0 {
		log.WithFields(logrus.Fields{
			"gpus":     gpus,
			"gpuCount": len(gpus),
		}).Info("GPU count from nvidia-smi")
		return len(gpus)
	}

	log.Debug("No matching compute processes in nvidia-smi, assuming single GPU")
	return 1
}

// scanNvidiaDeviceNodes scans /proc/<pid>/root/dev for NVIDIA device nodes.
// Uses major number detection — catches all nvidia device types.
func scanNvidiaDeviceNodes(pid int, log *logrus.Entry) []string {
	var externals []string //nolint:prealloc // length depends on runtime fd scan
	seen := make(map[string]bool)
	nvMajors := nvidiaDeviceMajors()

	devPath := fmt.Sprintf("/proc/%d/root/dev", pid)
	if _, err := os.Stat(devPath); os.IsNotExist(err) {
		return externals
	}
	entries, err := os.ReadDir(devPath)
	if err != nil {
		log.WithError(err).Debug("Failed to read /dev for NVIDIA device scan")
		return externals
	}
	for _, entry := range entries {
		fullPath := filepath.Join(devPath, entry.Name())
		var stat syscall.Stat_t
		if err := syscall.Stat(fullPath, &stat); err != nil {
			continue
		}
		if stat.Mode&syscall.S_IFMT != syscall.S_IFCHR {
			continue
		}
		major := uint32(stat.Rdev >> 8 & 0xfff) //nolint:gosec // bounded: masked device-number bits / known-positive value
		minor := uint32(stat.Rdev & 0xff)       //nolint:gosec // bounded: masked device-number bits / known-positive value
		if !nvMajors[major] {
			continue
		}
		external := fmt.Sprintf("dev[%d/%d]:%s", major, minor, entry.Name())
		if seen[external] {
			continue
		}
		seen[external] = true
		externals = append(externals, external)
	}
	return externals
}

// getMappedFiles reads /proc/<pid>/maps and returns all file-backed mappings
// This is generic and works for any CUDA/GPU workload
// Scans ALL child processes to find JIT-compiled files (e.g., GPU worker processes)
// getChildPIDs returns all child PIDs for a given parent PID
func getChildPIDs(pid int) []int {
	pids := []int{pid}
	seen := map[int]bool{pid: true}
	queue := []int{pid}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		cmd := exec.Command("pgrep", "-P", fmt.Sprintf("%d", current)) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line == "" {
				continue
			}
			childPID, err := strconv.Atoi(line)
			if err != nil || childPID <= 0 || seen[childPID] {
				continue
			}
			seen[childPID] = true
			pids = append(pids, childPID)
			queue = append(queue, childPID)
		}
	}

	return pids
}

// getPIDsInSameCgroup finds PIDs that share the same cgroup path as pid.
func getPIDsInSameCgroup(procBase string, pid int) []int {
	cgPath := fmt.Sprintf("%s/%d/cgroup", procBase, pid)
	data, err := os.ReadFile(cgPath)
	if err != nil {
		return nil
	}

	var target string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[2] != "" {
			// Use the unified path if present.
			if parts[0] == "0" {
				target = parts[2]
				break
			}
			if target == "" {
				target = parts[2]
			}
		}
	}
	if target == "" {
		return nil
	}

	dirEntries, err := os.ReadDir(procBase)
	if err != nil {
		return nil
	}

	var pids []int
	for _, entry := range dirEntries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		p, err := strconv.Atoi(name)
		if err != nil || p <= 0 {
			continue
		}
		cg, err := os.ReadFile(fmt.Sprintf("%s/%d/cgroup", procBase, p))
		if err != nil {
			continue
		}
		if strings.Contains(string(cg), target) {
			pids = append(pids, p)
		}
	}
	return pids
}

// findTvmFfiFiles returns tvm-ffi cached .so files as mapped file entries.
func findTvmFfiFiles(rootPid int) []MappedFileEntry {
	cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", rootPid), "-m", "--", //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		"find", "/root/.cache/tvm-ffi", "-type", "f", "-name", "*.so")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	files := make([]MappedFileEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, MappedFileEntry{
			Path:    line,
			RootPid: rootPid,
		})
	}
	return files
}

// isSystemPath returns true if the path is a system file that exists in the base image
func isSystemPath(path string) bool {
	return strings.HasPrefix(path, "/usr/lib") ||
		strings.HasPrefix(path, "/lib") ||
		strings.HasPrefix(path, "/usr/local/lib/python") ||
		strings.HasPrefix(path, "/usr/local/cuda") ||
		strings.HasPrefix(path, "/usr/bin") ||
		strings.HasPrefix(path, "/bin") ||
		strings.HasPrefix(path, "/etc/") ||
		strings.HasPrefix(path, "/dev/") ||
		strings.HasPrefix(path, "/proc/") ||
		strings.HasPrefix(path, "/sys/") ||
		strings.HasPrefix(path, "/run/") ||
		strings.HasPrefix(path, "/var/run/")
}

// isRuntimeGeneratedPath returns true if the path is runtime-generated (cache, logs, etc.)
func isRuntimeGeneratedPath(path string) bool {
	return strings.Contains(path, ".cache") ||
		strings.Contains(path, ".triton") ||
		strings.Contains(path, ".nv") ||
		strings.Contains(path, "/tmp/") ||
		strings.Contains(path, "jit") ||
		strings.Contains(path, "compiled") ||
		strings.Contains(path, "/root/") ||
		strings.Contains(path, ".log") ||
		strings.Contains(path, "/logs/")
}

// MappedFileEntry describes a memory-mapped file discovered during checkpoint.
type MappedFileEntry struct {
	Path    string
	MapFile string
	RootPid int
}

func getMappedFiles(pid int, extraPIDs []int) ([]MappedFileEntry, error) { //nolint:unparam // error result kept for caller symmetry / future failure modes
	seen := make(map[string]bool)
	var files []MappedFileEntry

	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}

	seenPids := make(map[int]bool)
	roots := append([]int{pid}, extraPIDs...)
	pids := make([]int, 0, len(roots))
	for _, root := range roots {
		if root <= 0 || seenPids[root] {
			continue
		}
		for _, p := range getChildPIDs(root) {
			if seenPids[p] {
				continue
			}
			seenPids[p] = true
			pids = append(pids, p)
		}
	}
	for _, p := range getPIDsInSameCgroup(procBase, pid) {
		if p <= 0 || seenPids[p] {
			continue
		}
		seenPids[p] = true
		pids = append(pids, p)
	}
	for _, p := range getPIDsInSameMountNS(procBase, pid) {
		if p <= 0 || seenPids[p] {
			continue
		}
		seenPids[p] = true
		pids = append(pids, p)
	}

	for _, p := range pids {
		mapsPath := fmt.Sprintf("%s/%d/maps", procBase, p)
		content, err := os.ReadFile(mapsPath)
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(content), "\n") {
			// Format: address perms offset dev inode pathname
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}

			path := strings.Join(fields[5:], " ")
			path = strings.TrimSuffix(path, " (deleted)")

			// Skip non-file entries
			if !strings.HasPrefix(path, "/") {
				continue
			}

			// Skip system paths that will exist in any container
			if isSystemPath(path) {
				continue
			}

			// Focus on cache/runtime generated files
			if isRuntimeGeneratedPath(path) {
				if !seen[path] {
					seen[path] = true
					mapFile := fmt.Sprintf("%s/%d/map_files/%s", procBase, p, fields[0])
					files = append(files, MappedFileEntry{
						Path:    path,
						MapFile: mapFile,
						RootPid: p,
					})
				}
			}
		}
	}

	return files, nil
}

func getPIDsInSameMountNS(procBase string, pid int) []int {
	var pids []int
	targetNS, err := os.Readlink(fmt.Sprintf("%s/%d/ns/mnt", procBase, pid))
	if err != nil || targetNS == "" {
		return pids
	}
	entries, err := os.ReadDir(procBase)
	if err != nil {
		return pids
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := strconv.Atoi(entry.Name())
		if err != nil || p <= 0 {
			continue
		}
		ns, err := os.Readlink(fmt.Sprintf("%s/%d/ns/mnt", procBase, p))
		if err != nil {
			continue
		}
		if ns == targetNS {
			pids = append(pids, p)
		}
	}
	return pids
}

// getOpenFiles scans /proc/<pid>/fd for all open files that are runtime-generated
// and need to be copied for CRIU restore. This captures log files, cache files, etc.
// that CRIU expects to exist at restore time.
func getOpenFiles(pid int) ([]string, error) { //nolint:unparam // error result kept for caller symmetry / future failure modes
	seen := make(map[string]bool)
	var files []string

	seenPids := make(map[int]bool)
	childPIDs := getChildPIDs(pid)
	pids := make([]int, 0, len(childPIDs))
	for _, p := range childPIDs {
		if p <= 0 || seenPids[p] {
			continue
		}
		seenPids[p] = true
		pids = append(pids, p)
	}
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	for _, p := range getPIDsInSameCgroup(procBase, pid) {
		if p <= 0 || seenPids[p] {
			continue
		}
		seenPids[p] = true
		pids = append(pids, p)
	}
	for _, p := range getPIDsInSameMountNS(procBase, pid) {
		if p <= 0 || seenPids[p] {
			continue
		}
		seenPids[p] = true
		pids = append(pids, p)
	}

	for _, p := range pids {
		fdDir := fmt.Sprintf("/proc/%d/fd", p)
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			// Child process may have exited
			continue
		}

		for _, entry := range entries {
			fdPath := filepath.Join(fdDir, entry.Name())
			// Resolve symlink to get actual path
			target, err := os.Readlink(fdPath)
			if err != nil {
				continue
			}

			// Skip non-file entries (pipes, sockets, devices, etc.)
			if !strings.HasPrefix(target, "/") {
				continue
			}
			if strings.HasPrefix(target, "/dev/") ||
				strings.HasPrefix(target, "/proc/") ||
				strings.HasPrefix(target, "/sys/") ||
				strings.Contains(target, "(deleted)") {
				continue
			}

			// Skip system paths
			if isSystemPath(target) {
				continue
			}

			// Only include runtime-generated files
			if isRuntimeGeneratedPath(target) {
				if !seen[target] {
					seen[target] = true
					files = append(files, target)
				}
			}
		}
	}

	return files, nil
}

// copyMappedFiles copies all runtime-generated mapped files to checkpoint
// Uses nsenter to access files in the container's mount namespace
func copyMappedFiles(pid, gpuPID int, _, checkpointDir string, log *logrus.Entry) error {
	var extra []int
	if gpuPID > 0 && gpuPID != pid {
		extra = append(extra, gpuPID)
	}
	files, err := getMappedFiles(pid, extra)
	if err != nil {
		return fmt.Errorf("failed to get mapped files: %w", err)
	}

	if len(files) == 0 {
		fallback := findTvmFfiFiles(pid)
		if len(fallback) > 0 {
			files = append(files, fallback...)
			log.WithField("count", len(fallback)).Info("Found tvm-ffi cache files to copy")
		}
	}

	if len(files) == 0 {
		log.Debug("No runtime-generated mapped files found")
		return nil
	}

	log.WithField("count", len(files)).Info("Found mapped files to copy")

	filesDir := filepath.Join(checkpointDir, "mapped-files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return err
	}

	// Write manifest for restore
	manifest := make([]string, 0, len(files))

	for _, entry := range files {
		file := entry.Path
		// Create destination path preserving directory structure
		relPath := strings.TrimPrefix(file, "/")
		dstPath := filepath.Join(filesDir, relPath)
		dstDir := filepath.Dir(dstPath)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			log.WithError(err).WithField("file", file).Warn("Failed to create dir")
			continue
		}

		// Use nsenter to stream file from container's mount namespace
		// nsenter -t <pid> -m -- cat <file> > <dst>
		size, err := streamCopyFromCommand(exec.Command("nsenter", "-t", fmt.Sprintf("%d", entry.RootPid), "-m", "--", "cat", file), dstPath) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		if err != nil {
			log.WithError(err).WithField("file", file).Debug("Failed to stream mapped file via nsenter")
			// Try alternative method via map_files
			if entry.MapFile != "" {
				if strings.HasPrefix(entry.MapFile, "/host/proc/") || strings.HasPrefix(entry.MapFile, "/proc/") {
					if copyErr := streamCopyFile(entry.MapFile, dstPath); copyErr != nil {
						log.WithError(copyErr).WithFields(logrus.Fields{
							"file":    file,
							"mapFile": entry.MapFile,
						}).Warn("Failed to read mapped file via map_files")
						continue
					}
				} else {
					size, err = streamCopyFromCommand(exec.Command("nsenter", "-t", fmt.Sprintf("%d", entry.RootPid), "-p", "-m", "--", "cat", entry.MapFile), dstPath) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
					if err != nil {
						log.WithError(err).WithFields(logrus.Fields{
							"file":    file,
							"mapFile": entry.MapFile,
						}).Warn("Failed to read mapped file via map_files")
						continue
					}
				}
			} else {
				continue
			}
		}

		// Always add to manifest, even if 0 bytes
		// CRIU expects these files to exist during restore, even if empty
		manifest = append(manifest, file)
		if size == 0 {
			log.WithField("file", file).Debug("Copied mapped file (0 bytes - placeholder)")
		} else {
			log.WithField("file", file).Debug("Copied mapped file")
		}
	}

	// Write manifest
	if len(manifest) > 0 {
		manifestPath := filepath.Join(checkpointDir, "mapped-files.txt")
		manifestContent := strings.Join(manifest, "\n")
		if err := os.WriteFile(manifestPath, []byte(manifestContent), 0o644); err != nil {
			return err
		}
		log.WithField("count", len(manifest)).Info("Saved mapped files manifest")
	}

	return nil
}

// copyOpenFiles copies all runtime-generated open files (log files, cache files, etc.)
// to checkpoint directory. These are files that CRIU expects to exist at restore time
// but don't exist in the base container image.
func copyOpenFiles(pid int, checkpointDir string, log *logrus.Entry) ([]MappedFileInfo, error) {
	files, err := getOpenFiles(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to get open files: %w", err)
	}

	// Always include vLLM model info cache files if present.
	if extra := getVllmModelInfoFiles(pid, log); len(extra) > 0 {
		files = append(files, extra...)
	}

	if len(files) > 1 {
		seen := make(map[string]bool)
		unique := make([]string, 0, len(files))
		for _, file := range files {
			if seen[file] {
				continue
			}
			seen[file] = true
			unique = append(unique, file)
		}
		files = unique
	}

	if len(files) == 0 {
		log.Debug("No runtime-generated open files found")
		return nil, nil
	}

	log.WithField("count", len(files)).Info("Found open files to copy")

	filesDir := filepath.Join(checkpointDir, "open-files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, err
	}

	copiedFiles := make([]MappedFileInfo, 0, len(files))

	for _, file := range files {
		// Create destination path preserving directory structure
		// Remove leading slash for relative path storage
		relPath := strings.TrimPrefix(file, "/")
		dstPath := filepath.Join(filesDir, relPath)
		dstDir := filepath.Dir(dstPath)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			log.WithError(err).WithField("file", file).Warn("Failed to create dir for open file")
			continue
		}

		// Use nsenter to copy file from container's mount namespace
		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--", "cat", file) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		output, err := cmd.Output()
		if err != nil {
			log.WithError(err).WithField("file", file).Debug("Failed to read open file via nsenter, skipping")
			continue
		}

		if err := os.WriteFile(dstPath, output, 0o644); err != nil {
			log.WithError(err).WithField("file", file).Warn("Failed to write open file")
			continue
		}

		copiedFiles = append(copiedFiles, MappedFileInfo{
			SourcePath: file,
			DestPath:   filepath.Join("open-files", relPath),
		})
		log.WithField("file", file).Debug("Copied open file")
	}

	// Write manifest for restore
	if len(copiedFiles) > 0 {
		manifestPath := filepath.Join(checkpointDir, "open-files.txt")
		var manifest []string
		for _, f := range copiedFiles {
			manifest = append(manifest, f.SourcePath)
		}
		if err := os.WriteFile(manifestPath, []byte(strings.Join(manifest, "\n")), 0o644); err != nil {
			return copiedFiles, err
		}
		log.WithField("count", len(copiedFiles)).Info("Saved open files manifest")
	}

	return copiedFiles, nil
}

// getVllmModelInfoFiles returns vLLM model info cache files if present.
func getVllmModelInfoFiles(pid int, log *logrus.Entry) []string {
	cmd := exec.Command( //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		"nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--",
		"sh", "-lc", "for f in /root/.cache/vllm/modelinfos/*.json; do [ -e \"$f\" ] && echo \"$f\"; done",
	)
	output, err := cmd.Output()
	if err != nil {
		log.WithError(err).Debug("Failed to list vLLM modelinfo cache files")
		return nil
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			files = append(files, line)
		}
	}
	if len(files) > 0 {
		log.WithField("count", len(files)).Info("Found vLLM modelinfo cache files")
	}
	return files
}

// parseSkippedResources parses dump.log to extract information about skipped resources
// This enables restore to make informed decisions about which fds to skip
func parseSkippedResources(dumpLogPath string) (*SkippedResources, error) {
	content, err := os.ReadFile(dumpLogPath)
	if err != nil {
		return nil, err
	}

	skipped := &SkippedResources{}
	lines := strings.Split(string(content), "\n")

	for _, line := range lines {
		// Parse: "unix: Skipping unix socket fd 31 (--skip-unix-sockets/--portable)"
		if strings.Contains(line, "unix: Skipping unix socket fd") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "fd" && i+1 < len(parts) {
					if fd, err := strconv.Atoi(parts[i+1]); err == nil {
						skipped.UnixSocketFds = append(skipped.UnixSocketFds, fd)
					}
					break
				}
			}
		}

		// Parse: "inet: Skipping inet socket fd 33 (--skip-inet-sockets/--portable)"
		if strings.Contains(line, "inet: Skipping inet socket fd") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "fd" && i+1 < len(parts) {
					if fd, err := strconv.Atoi(parts[i+1]); err == nil {
						skipped.InetSocketFds = append(skipped.InetSocketFds, fd)
					}
					break
				}
			}
		}

		// Parse: "Skipping io_uring SQPOLL thread 1264542 (iou-sqp-1264395)"
		if strings.Contains(line, "Skipping io_uring SQPOLL thread") {
			// Extract the thread name in parentheses
			start := strings.Index(line, "(")
			end := strings.Index(line, ")")
			if start != -1 && end != -1 && end > start {
				threadName := line[start+1 : end]
				skipped.IoUringThreads = append(skipped.IoUringThreads, threadName)
			}
		}

		// Parse: "mnt: Mount 18386 ./run/nvidia-persistenced/socket has unreachable sharing, skipping (--skip-mnt-ns)"
		if strings.Contains(line, "has unreachable sharing, skipping") {
			// Extract mount path - it's after "Mount XXXXX " and before " has"
			if idx := strings.Index(line, "mnt: Mount"); idx != -1 {
				rest := line[idx+11:] // After "mnt: Mount "
				parts := strings.SplitN(rest, " ", 2)
				if len(parts) >= 2 {
					// parts[0] is mount ID, parts[1] starts with path
					pathPart := parts[1]
					if endIdx := strings.Index(pathPart, " has"); endIdx != -1 {
						mountPath := strings.TrimPrefix(pathPart[:endIdx], ".")
						skipped.Mounts = append(skipped.Mounts, mountPath)
					}
				}
			}
		}
	}

	return skipped, nil
}

// captureTempDirectories saves a list of /tmp directories that exist in the container.
// These directories may be referenced in Python's sys.path or other runtime state,
// and need to exist after restore even if they're empty.
func captureTempDirectories(pid int, checkpointDir string, log *logrus.Entry) ([]string, error) {
	// Use nsenter to list directories in /tmp
	cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--", "find", "/tmp", "-maxdepth", "1", "-type", "d") //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	output, err := cmd.Output()
	if err != nil {
		log.WithError(err).Debug("Failed to list /tmp directories")
		return nil, nil // Not fatal
	}

	outputLines := strings.Split(string(output), "\n")
	dirs := make([]string, 0, len(outputLines))
	for _, line := range outputLines {
		line = strings.TrimSpace(line)
		if line == "" || line == "/tmp" {
			continue
		}
		// Skip NvSnap GPU save directories — they're huge (76GB+/GPU)
		// and handled separately via the checkpoint GPU save path.
		if strings.Contains(line, "nvsnap-gpu-save") {
			continue
		}
		dirs = append(dirs, line)
	}

	if len(dirs) == 0 {
		return nil, nil
	}

	// Save to manifest
	manifestPath := filepath.Join(checkpointDir, "temp-directories.txt")
	content := strings.Join(dirs, "\n")
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		return nil, err
	}

	log.WithField("count", len(dirs)).Info("Captured temp directories")
	return dirs, nil
}

// copyTempFiles copies files from captured temp directories into the checkpoint.
func copyTempFiles(pid int, dirs []string, checkpointDir string, log *logrus.Entry) error {
	tempFilesDir := filepath.Join(checkpointDir, "temp-files")
	if err := os.MkdirAll(tempFilesDir, 0o755); err != nil {
		return err
	}

	var copied []string
	for _, dir := range dirs {
		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--", "find", dir, "-type", "f") //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		output, err := cmd.Output()
		if err != nil {
			log.WithError(err).WithField("dir", dir).Debug("Failed to list temp files")
			continue
		}
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			relPath := strings.TrimPrefix(line, "/")
			dstPath := filepath.Join(tempFilesDir, relPath)
			dstDir := filepath.Dir(dstPath)
			if err := os.MkdirAll(dstDir, 0o755); err != nil {
				log.WithError(err).WithField("file", line).Warn("Failed to create dir for temp file")
				continue
			}
			// Check file size first — skip files > 100MB to avoid OOM
			sizeCmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--", "stat", "-c", "%s", line) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
			sizeOut, sizeErr := sizeCmd.Output()
			if sizeErr == nil {
				if sz, _ := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64); sz > 100*1024*1024 {
					log.WithFields(logrus.Fields{"file": line, "size": sz}).Debug("Skipping large temp file")
					continue
				}
			}
			cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", pid), "-m", "--", "cat", line) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
			content, err := cmd.Output()
			if err != nil {
				log.WithError(err).WithField("file", line).Debug("Failed to read temp file via nsenter, skipping")
				continue
			}
			if err := os.WriteFile(dstPath, content, 0o644); err != nil {
				log.WithError(err).WithField("file", line).Warn("Failed to write temp file")
				continue
			}
			copied = append(copied, line)
		}
	}

	if len(copied) == 0 {
		return nil
	}

	manifestPath := filepath.Join(checkpointDir, "temp-files.txt")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(copied, "\n")), 0o644); err != nil {
		return err
	}
	log.WithField("count", len(copied)).Info("Copied temp files")
	return nil
}

func streamCopyFromCommand(cmd *exec.Cmd, dstPath string) (int64, error) {
	outFile, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = outFile.Close() }()
	var stderr bytes.Buffer
	cmd.Stdout = outFile
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		if stderr.Len() > 0 {
			return 0, fmt.Errorf("%w: %s", runErr, stderr.String())
		}
		return 0, err
	}

	info, err := outFile.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func streamCopyFile(srcPath, dstPath string) error {
	inFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = inFile.Close() }()
	outFile, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer func() { _ = outFile.Close() }()
	if _, err := io.Copy(outFile, inFile); err != nil {
		return err
	}
	return nil
}

type containerProcInfo struct {
	HostPID      int
	ContainerPID int
	Comm         string
}

func listContainerProcs(containerPid int, log *logrus.Entry) []containerProcInfo {
	// Get the PID namespace inode of the container init process.
	// All processes in the same container share this namespace.
	containerNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", containerPid))
	if err != nil {
		log.WithError(err).Warn("Failed to read container PID namespace")
		return nil
	}

	// Safety: if the container shares the host PID namespace (hostPID: true),
	// we'd match every process on the node. Sending SIGUSR1 to all host
	// processes (containerd, kubelet, etc.) crashes the node. Detect this
	// by comparing against PID 1's namespace.
	hostNS, _ := os.Readlink("/proc/1/ns/pid")
	if hostNS != "" && containerNS == hostNS {
		log.Warn("Container uses host PID namespace — cannot enumerate container processes safely, returning only the init process")
		commBytes, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", containerPid))
		return []containerProcInfo{{
			HostPID:      containerPid,
			ContainerPID: containerPid,
			Comm:         strings.TrimSpace(string(commBytes)),
		}}
	}

	// Scan the host /proc for all processes in the same PID namespace.
	// The agent runs with hostPID=true, so /proc is the host's procfs.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		log.WithError(err).Warn("Failed to list host /proc")
		return nil
	}

	procs := make([]containerProcInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		hostPID, err := strconv.Atoi(entry.Name())
		if err != nil || hostPID <= 0 {
			continue
		}

		// Check if this process is in the container's PID namespace
		ns, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", hostPID))
		if err != nil || ns != containerNS {
			continue
		}

		// Read container PID from NSpid (second field = container PID)
		statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", hostPID))
		if err != nil {
			continue
		}

		// Skip threads — only return thread group leaders (processes).
		// Threads have Tgid != Pid. Sending signals to threads is
		// redundant (signal goes to the whole thread group) and the
		// high count (2650+) can overwhelm the system.
		statusStr := string(statusBytes)
		tgid := parseTgidFromStatus(statusStr)
		if tgid > 0 && tgid != hostPID {
			continue
		}

		containerPID := parseContainerPIDFromStatus(statusStr)
		if containerPID <= 0 {
			continue
		}

		commBytes, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", hostPID))
		procs = append(procs, containerProcInfo{
			HostPID:      hostPID,
			ContainerPID: containerPID,
			Comm:         strings.TrimSpace(string(commBytes)),
		})
	}
	return procs
}

// parseTgidFromStatus extracts the Tgid (thread group leader PID) from
// /proc/<pid>/status. Returns 0 if not found.
func parseTgidFromStatus(status string) int {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "Tgid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		tgid, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return tgid
	}
	return 0
}

// parseContainerPIDFromStatus extracts the container-namespace PID from a
// host /proc/<pid>/status NSpid line. The NSpid line has the form:
//
//	NSpid: <hostPID> [<intermediatePID>...] <containerPID>
//
// The last field is the innermost (container) PID.
func parseContainerPIDFromStatus(status string) int {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		// Last field is the container PID
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			return 0
		}
		return pid
	}
	return 0
}

func hasInterceptMapped(containerPid, containerProcPid int) bool {
	mapsPath := fmt.Sprintf("/proc/%d/root/proc/%d/maps", containerPid, containerProcPid)
	data, err := os.ReadFile(mapsPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "libnvsnap_intercept.so")
}

func sendQuiesceSignalToInterceptProcs(containerPid int, log *logrus.Entry) {
	procs := listContainerProcs(containerPid, log)
	if len(procs) == 0 {
		log.Info("No container processes found for quiesce signal broadcast")
		return
	}
	log.WithField("count", len(procs)).Info("Scanning container processes for NVSNAP intercept mapping")
	sent := 0
	signaled := make([]int, 0, len(procs))
	signaledContainerPIDs := make([]int, 0, len(procs))
	for _, proc := range procs {
		if proc.HostPID == containerPid {
			continue
		}
		if !hasInterceptMapped(containerPid, proc.ContainerPID) {
			continue
		}

		// Write per-PID trigger file inside the container's /dev/shm.
		// The intercept lib's worker thread polls for this file and triggers quiesce.
		// This works even when SIGUSR1 handler isn't installed (forked processes).
		triggerPath := fmt.Sprintf("/host/proc/%d/root/dev/shm/nvsnap-quiesce-trigger-%d",
			containerPid, proc.ContainerPID)
		if _, err := os.Stat(fmt.Sprintf("/host/proc/%d/root", containerPid)); os.IsNotExist(err) {
			triggerPath = fmt.Sprintf("/proc/%d/root/dev/shm/nvsnap-quiesce-trigger-%d",
				containerPid, proc.ContainerPID)
		}
		if err := os.WriteFile(triggerPath, []byte("quiesce"), 0o644); err != nil {
			log.WithError(err).WithField("container_pid", proc.ContainerPID).Debug("Failed to write quiesce trigger file")
		}

		sent++
		signaled = append(signaled, proc.HostPID)
		signaledContainerPIDs = append(signaledContainerPIDs, proc.ContainerPID)
	}
	if sent > 0 {
		log.WithField("count", sent).Info("Wrote quiesce trigger files + sent SIGUSR1")

		// Wait for all signaled processes to complete quiesce (marker-based)
		waitForQuiesceMarkers(containerPid, signaledContainerPIDs, log)

		for _, pid := range signaled {
			sendResumeSignal(pid, log)
		}
		verifyUvloopMetadataForPids(containerPid, signaled, log)
	}
	log.WithFields(logrus.Fields{
		"scanned": len(procs),
		"sent":    sent,
	}).Info("Finished quiesce signal broadcast to NVSNAP intercept processes")
}

// waitForQuiesceMarkers polls for per-PID quiesce done markers written by
// libnvsnap_intercept.so after NCCL abort completes. This replaces the fixed
// 2s sleep, ensuring all ranks have finished NCCL quiesce before CRIU dump.
func waitForQuiesceMarkers(containerPid int, containerPIDs []int, log *logrus.Entry) {
	if len(containerPIDs) == 0 {
		return
	}

	const pollInterval = 200 * time.Millisecond
	// NvSnap GPU save takes ~110s for 70B (76GB/GPU × 4 GPUs).
	// Must wait for all ranks to complete save before proceeding to CRIU dump.
	const timeout = 180 * time.Second
	deadline := time.Now().Add(timeout)

	pending := make(map[int]bool)
	for _, cpid := range containerPIDs {
		pending[cpid] = true
	}

	log.WithField("count", len(pending)).Info("Waiting for quiesce done markers from all ranks")

	// Use /host/proc if available (agent without hostPID), fallback to /proc.
	procRoot := "/proc"
	if _, err := os.Stat(fmt.Sprintf("/host/proc/%d/root", containerPid)); err == nil {
		procRoot = "/host/proc"
	}

	for time.Now().Before(deadline) && len(pending) > 0 {
		for cpid := range pending {
			markerPath := fmt.Sprintf("%s/%d/root/dev/shm/nvsnap-quiesce-done-%d", procRoot, containerPid, cpid)
			if _, err := os.Stat(markerPath); err == nil {
				log.WithField("container_pid", cpid).Debug("Quiesce done marker found")
				delete(pending, cpid)
			}
		}
		if len(pending) > 0 {
			time.Sleep(pollInterval)
		}
	}

	if len(pending) > 0 {
		remaining := make([]int, 0, len(pending))
		for cpid := range pending {
			remaining = append(remaining, cpid)
		}
		log.WithFields(logrus.Fields{
			"remaining": remaining,
			"timeout":   timeout,
		}).Warn("Some processes did not complete quiesce in time, proceeding anyway")
	} else {
		log.Info("All ranks completed quiesce")
	}
}

func verifyUvloopMetadataForPids(containerPid int, pids []int, log *logrus.Entry) {
	if len(pids) == 0 {
		return
	}
	hostToContainer := make(map[int]int)
	for _, proc := range listContainerProcs(containerPid, log) {
		if proc.HostPID > 0 && proc.ContainerPID > 0 {
			hostToContainer[proc.HostPID] = proc.ContainerPID
		}
	}
	rootBase := fmt.Sprintf("/proc/%d/root", containerPid)
	for _, pid := range pids {
		containerPID := hostToContainer[pid]
		if containerPID == 0 {
			containerPID = pid
		}
		found := false
		for _, rel := range []string{
			fmt.Sprintf("var/run/nvsnap/uvloop_loops.%d.json", containerPID),
			fmt.Sprintf("run/nvsnap/uvloop_loops.%d.json", containerPID),
			fmt.Sprintf("nvsnap-lib/uvloop_loops.%d.json", containerPID),
		} {
			path := filepath.Join(rootBase, rel)
			if _, err := os.Stat(path); err == nil {
				found = true
				log.WithFields(logrus.Fields{
					"pid":           pid,
					"container_pid": containerPID,
					"path":          path,
				}).Info("Found per-pid uvloop metadata after quiesce")
				break
			}
		}
		if !found {
			log.WithFields(logrus.Fields{
				"pid":           pid,
				"container_pid": containerPID,
			}).Warn("Missing per-pid uvloop metadata after quiesce")
		}
	}
}

// copyUvloopMetadata copies uvloop loop pointer metadata if present.
func copyUvloopMetadata(pid int, checkpointDir string, log *logrus.Entry) error {
	dirs := []string{
		fmt.Sprintf("/proc/%d/root/var/run/nvsnap", pid),
		fmt.Sprintf("/proc/%d/root/run/nvsnap", pid),
		fmt.Sprintf("/proc/%d/root/nvsnap-lib", pid),
	}
	// Give the async metadata thread a moment to write files.
	for i := 0; i < 10; i++ {
		found := false
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, "uvloop_loops") && strings.HasSuffix(name, ".json") {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	copied := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "uvloop_loops") || !strings.HasSuffix(name, ".json") {
				continue
			}
			srcPath := filepath.Join(dir, name)
			data, err := os.ReadFile(srcPath)
			if err != nil || len(data) == 0 {
				continue
			}
			destPath := filepath.Join(checkpointDir, name)
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return err
			}
			copied++
		}
	}
	if copied == 0 {
		return nil
	}
	log.WithField("count", copied).Info("Saved uvloop metadata")
	return nil
}

// copyGPUSaveFiles copies GPU memory save files from /dev/shm/nvsnap-gpu-save-<pid>/
// for each container process into the checkpoint directory. These files are written
// by the intercept library during quiesce (nvsnap_cuda_save).
func copyGPUSaveFiles(containerPid int, checkpointDir string, log *logrus.Entry) error {
	gpuDir := filepath.Join(checkpointDir, "gpu")
	if err := os.MkdirAll(gpuDir, 0o755); err != nil {
		return fmt.Errorf("failed to create gpu dir: %w", err)
	}

	// Scan all processes in the container for GPU save directories
	procs := listContainerProcs(containerPid, log)
	copied := 0
	for _, proc := range procs {
		saveDir := fmt.Sprintf("/proc/%d/root/dev/shm/nvsnap-gpu-save-%d", containerPid, proc.ContainerPID)
		entries, err := os.ReadDir(saveDir)
		if err != nil {
			continue // Process may not have GPU allocations
		}

		// Create per-PID subdirectory
		pidDir := filepath.Join(gpuDir, fmt.Sprintf("pid-%d", proc.ContainerPID))
		if err := os.MkdirAll(pidDir, 0o755); err != nil {
			return fmt.Errorf("failed to create pid dir: %w", err)
		}

		for _, entry := range entries {
			srcPath := filepath.Join(saveDir, entry.Name())
			data, err := os.ReadFile(srcPath)
			if err != nil {
				log.WithError(err).WithField("file", srcPath).Warn("Failed to read GPU save file")
				continue
			}
			destPath := filepath.Join(pidDir, entry.Name())
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return fmt.Errorf("failed to write GPU save file %s: %w", destPath, err)
			}
			copied++
		}

		log.WithFields(logrus.Fields{
			"pid":   proc.ContainerPID,
			"files": len(entries),
		}).Debug("Copied GPU save files for process")
	}

	log.WithField("totalFiles", copied).Info("GPU save files copied to checkpoint")
	return nil
}

// getMappedFilesInfo converts the mapped files manifest to MappedFileInfo structs
func getMappedFilesInfo(checkpointDir string) []MappedFileInfo {
	manifestPath := filepath.Join(checkpointDir, "mapped-files.txt")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}

	contentLines := strings.Split(string(content), "\n")
	files := make([]MappedFileInfo, 0, len(contentLines))
	for _, line := range contentLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		relPath := strings.TrimPrefix(line, "/")
		files = append(files, MappedFileInfo{
			SourcePath: line,
			DestPath:   filepath.Join("mapped-files", relPath),
		})
	}
	return files
}

// cleanupSharedMemory removes Python multiprocessing shared memory files from
// the container's /dev/shm before checkpoint.
//
// IMPORTANT: POSIX semaphores (sem.*) are NOT deleted. vLLM's APIServer and
// EngineCore share a semaphore for IPC — deleting it causes EngineCore to die
// ~71s after restore. CRIU handles semaphore C/R when they exist as real files.
func cleanupSharedMemory(pid int, log *logrus.Entry) {
	if isTruthyEnv("NVSNAP_SKIP_SHM_CLEANUP") {
		log.Info("Skipping /dev/shm cleanup before checkpoint")
		return
	}
	shmPath := fmt.Sprintf("/proc/%d/root/dev/shm", pid)

	entries, err := os.ReadDir(shmPath)
	if err != nil {
		log.WithError(err).Debug("Could not read container /dev/shm (may not exist)")
		return
	}

	var removedPyShm []string
	var preservedSemaphores []string

	for _, entry := range entries {
		name := entry.Name()
		// Preserve POSIX semaphores (sem.*) — CRIU handles their C/R.
		// Deleting them causes vLLM EngineCore to die ~71s after restore.
		if strings.HasPrefix(name, "sem.") {
			preservedSemaphores = append(preservedSemaphores, name)
		}
		// Remove Python multiprocessing shared memory files (pym-*)
		if strings.HasPrefix(name, "pym-") {
			pymPath := filepath.Join(shmPath, name)
			if err := os.Remove(pymPath); err != nil {
				log.WithError(err).WithField("file", name).Warn("Failed to remove Python shm")
			} else {
				removedPyShm = append(removedPyShm, name)
			}
		}
	}

	if len(preservedSemaphores) > 0 {
		log.WithFields(logrus.Fields{
			"count":      len(preservedSemaphores),
			"semaphores": preservedSemaphores,
		}).Info("Preserved POSIX semaphores in container /dev/shm (CRIU handles C/R)")
	}

	if len(removedPyShm) > 0 {
		log.WithFields(logrus.Fields{
			"count": len(removedPyShm),
			"files": removedPyShm,
		}).Info("Removed Python multiprocessing shm files from container /dev/shm before checkpoint")
	}

}

// backupShmFiles copies /dev/shm files (psm_*, nccl-*, etc.) from the container
// into the checkpoint directory so they can be restored before CRIU runs.
// CRIU dumps VMA pages for these shmem files but expects the files to exist
// with the correct size at restore time.
func backupShmFiles(containerPid int, checkpointDir string, log *logrus.Entry) {
	shmPath := fmt.Sprintf("/proc/%d/root/dev/shm", containerPid)
	entries, err := os.ReadDir(shmPath)
	if err != nil {
		log.WithError(err).Debug("Could not read container /dev/shm for backup")
		return
	}

	shmBackupDir := filepath.Join(checkpointDir, "shm-backup")
	if err := os.MkdirAll(shmBackupDir, 0o755); err != nil {
		log.WithError(err).Warn("Failed to create shm-backup dir")
		return
	}

	backed := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		// Skip trigger/done markers and nvsnap-gpu-save dirs
		if strings.HasPrefix(name, "nvsnap-") {
			continue
		}
		// Skip semaphores — CRIU handles those as ghost files
		if strings.HasPrefix(name, "sem.") {
			continue
		}
		srcPath := filepath.Join(shmPath, name)
		info, err := entry.Info()
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		// Copy file to backup
		dstPath := filepath.Join(shmBackupDir, name)
		src, err := os.Open(srcPath)
		if err != nil {
			continue
		}
		dst, err := os.Create(dstPath)
		if err != nil {
			_ = src.Close()
			continue
		}
		_, err = io.Copy(dst, src)
		_ = src.Close()
		_ = dst.Close()
		if err != nil {
			_ = os.Remove(dstPath)
			continue
		}
		backed = append(backed, fmt.Sprintf("%s (%d bytes)", name, info.Size()))
	}

	if len(backed) > 0 {
		log.WithFields(logrus.Fields{
			"count": len(backed),
			"files": backed,
		}).Info("Backed up /dev/shm files for restore")
	}
}

// CheckpointRequest is the request to checkpoint a container
type CheckpointRequest struct {
	Namespace     string `json:"namespace"`
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName,omitempty"`
	ContainerID   string `json:"containerId,omitempty"`
	LeaveRunning  bool   `json:"leaveRunning,omitempty"`

	// CapturePath lets the caller override the agent's default capture
	// routing per request: "" = auto (cluster default), "rootfs" = force
	// the rootfs+OverlayFS path, "criu" = force CRIU+cuda-checkpoint even
	// when the cluster default is rootfs. An explicit "criu" is still
	// subject to the hard capability gates (Riva/Triton backend,
	// multi-GPU) — on those it returns an error rather than silently
	// falling back. See resolveCheckpointRedirect.
	CapturePath string `json:"capturePath,omitempty"`
}

// CheckpointResult is the result of a checkpoint operation
type CheckpointResult struct {
	CheckpointID   string    `json:"checkpointId"`
	CheckpointPath string    `json:"checkpointPath"`
	CheckpointRef  string    `json:"checkpointRef"` // containerd image reference
	CheckpointSize int64     `json:"checkpointSize"`
	ContainerID    string    `json:"containerID"`
	OriginalImage  string    `json:"originalImage"`
	GPUPID         int       `json:"gpuPid"`
	Duration       float64   `json:"durationSeconds"`
	Timestamp      time.Time `json:"timestamp"`

	// Hash is the content-addressed identity computed from
	// CatalogInfo (image digest + model + engine flags + driver +
	// format version, sha256-ed via checkpointstore.ComputeHash).
	// Two checkpoints with the same canonical inputs produce the
	// same Hash, regardless of which pod-instance generated them —
	// that's the dedup key for the restore-from annotation.
	// Always populated in the CRIU-only flow as of nvsnap#56. In
	// the rootfs-capture flow, this is the same value the writer
	// Job uses to name the per-capture PVC and ConfigMap.
	Hash string `json:"hash,omitempty"`

	// ReaderPVCName is the per-capture reader PVC the artifact lives
	// on. Restorers mount this PVC at /checkpoints (subPath="criu")
	// instead of hostPath. Set together with Hash when rootfs-capture
	// backend is configured; empty in the CRIU-only flow (artifact
	// stays at CheckpointPath on the agent's hostPath).
	ReaderPVCName string `json:"readerPvcName,omitempty"`

	// CatalogInfo is the rich identity for the catalog row + UI
	// (image, model, engine flags, GPU type, driver, cluster, node,
	// function-name/-version). Always populated best-effort —
	// individual fields may be empty if the underlying lookup
	// failed. The Hash field above is derived from a subset of
	// these (the dedup-relevant subset per
	// checkpointstore.HashInput).
	CatalogInfo *CatalogInfo `json:"catalogInfo,omitempty"`
}

// SkippedResources tracks resources that were intentionally skipped during dump.
type SkippedResources struct {
	UnixSocketFds  []int    `json:"unixSocketFds,omitempty"`  // Unix socket fd numbers skipped
	InetSocketFds  []int    `json:"inetSocketFds,omitempty"`  // Inet socket fd numbers skipped
	IoUringThreads []string `json:"ioUringThreads,omitempty"` // io_uring SQPOLL threads skipped
	Mounts         []string `json:"mounts,omitempty"`         // Mount paths skipped (--skip-mnt-ns)
}

// MappedFileInfo tracks a file that was copied during checkpoint
type MappedFileInfo struct {
	SourcePath string `json:"sourcePath"` // Original path in container
	DestPath   string `json:"destPath"`   // Path in checkpoint (relative)
}

// CUDACheckpointInfo tracks CUDA/GPU checkpoint state
type CUDACheckpointInfo struct {
	Enabled           bool `json:"enabled"`
	GPUPID            int  `json:"gpuPid,omitempty"`
	LockSuccess       bool `json:"lockSuccess"`
	CheckpointSuccess bool `json:"checkpointSuccess"`
	RestoreSuccess    bool `json:"restoreSuccess,omitempty"` // For leave-running mode
	UnlockSuccess     bool `json:"unlockSuccess,omitempty"`
	Interposition     bool `json:"interposition,omitempty"` // GPU memory saved by intercept library (no cuda-checkpoint)
}

// CRIUOptionsUsed tracks which CRIU options were used during dump
type CRIUOptionsUsed struct {
	SkipUnixSockets bool     `json:"skipUnixSockets"`
	SkipInFlight    bool     `json:"skipInFlight"`
	SkipFsnotify    bool     `json:"skipFsnotify"`
	LeaveRunning    bool     `json:"leaveRunning"`
	TCPEstablished  bool     `json:"tcpEstablished"`
	External        []string `json:"external,omitempty"` // External resources marked during dump
}

// RestoreHints provides guidance to the restore process
type RestoreHints struct {
	SkipFds            []int  `json:"skipFds,omitempty"`     // FDs to skip during restore (from skipped sockets)
	RestoreMappedFiles bool   `json:"restoreMappedFiles"`    // Whether to restore mapped files
	CUDARestoreNeeded  bool   `json:"cudaRestoreNeeded"`     // Whether CUDA restore is needed
	NetworkMode        string `json:"networkMode,omitempty"` // How network was handled: "empty", "external", etc.
}

// CheckpointMetadata contains all information about a checkpoint
type CheckpointMetadata struct {
	// Version for schema evolution
	Version string `json:"version"`

	// Basic identification
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	CheckpointSize  int64     `json:"checkpointSize,omitempty"`
	DurationSeconds float64   `json:"durationSeconds,omitempty"`

	// Hash is the content-addressed identity (sha256 hex) derived from
	// CatalogInfo via checkpointstore.ComputeHash. Used as the
	// nvsnap.io/restore-from annotation value by NVCA's Hook A. Two
	// checkpoints with the same canonical inputs hash equal.
	// Populated as of nvsnap#56.
	Hash string `json:"hash,omitempty"`

	// CatalogInfo is the rich identity used for catalog browsing +
	// restore-compatibility filtering. Populated best-effort at
	// capture time; consumers read it from metadata.json on disk
	// (agent peer HTTP) or from nvsnap-server's catalog DB row.
	CatalogInfo *CatalogInfo `json:"catalogInfo,omitempty"`

	// Container info
	PodName        string            `json:"podName"`
	PodNamespace   string            `json:"podNamespace"`
	NodeName       string            `json:"nodeName"`
	ContainerName  string            `json:"containerName"`
	ContainerID    string            `json:"containerID"`
	ContainerImage string            `json:"containerImage"`
	ContainerPID   uint32            `json:"containerPid"`
	RootFS         string            `json:"rootfs"`
	PodLabels      map[string]string `json:"podLabels,omitempty"`

	// Network identity for restore compatibility (v1.2+)
	SourcePodIP string `json:"source_pod_ip,omitempty"`

	// Pipe IDs for stdout/stderr (v1.3+) - needed for InheritFd restore
	StdoutPipeID string `json:"stdout_pipe_id,omitempty"` // e.g., "pipe:[12345]"
	StderrPipeID string `json:"stderr_pipe_id,omitempty"` // e.g., "pipe:[12346]"

	// Enhanced tracking (v1.1+)
	Skipped     *SkippedResources   `json:"skipped,omitempty"`
	MappedFiles []MappedFileInfo    `json:"mappedFiles,omitempty"`
	OpenFiles   []MappedFileInfo    `json:"openFiles,omitempty"` // v1.4+: open FD files (logs, etc.)
	CUDA        *CUDACheckpointInfo `json:"cuda,omitempty"`
	CRIUOptions *CRIUOptionsUsed    `json:"criuOptions,omitempty"`
	Hints       *RestoreHints       `json:"restoreHints,omitempty"`

	// Plan A (v1.3+): explicit mountpoints used to build ExtMnt during dump.
	// Restore should prefer this list to generate consistent ExtMnt mappings.
	DumpMountPoints []string `json:"dumpMountPoints,omitempty"`

	// Cross-pod mount replay (v1.7+): list of mountpoints whose contents
	// were tarred at checkpoint into <ckpt>/mounts/<sanitized>.tar and
	// must be untarred into the placeholder pod's mntns at the same path
	// before CRIU restore. See docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md.
	ReplayMounts []ReplayMount `json:"replayMounts,omitempty"`

	// Compression info (v1.5+): set when checkpoint uses transparent compression
	Compression *streamer.CompressionInfo `json:"compression,omitempty"`

	// CapturePath (v1.8+) records which capture engine produced this
	// checkpoint ("criu-v2" for the in-namespace engine; empty for
	// legacy CRIU). Restore dispatches on it: criu-v2 images must be
	// restored in-namespace by the bundled CRIU, not the legacy
	// swrk/ExtMnt path.
	CapturePath string `json:"capturePath,omitempty"`

	// Integrity (v1.6+): SHA-256 checksums for critical checkpoint files
	Integrity *CheckpointIntegrity `json:"integrity,omitempty"`

	// Deprecated fields (kept for backward compatibility)
	CheckpointRef string `json:"checkpointRef,omitempty"`
	GPUPID        int    `json:"gpuPid,omitempty"` // Use CUDA.GPUPID instead
}

// ReplayMount records a single mountpoint whose contents were captured
// at checkpoint time and must be replayed into the placeholder pod's
// mount namespace before CRIU restore. Captured by tarMount; replayed
// by untarIntoMntns. See docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md.
type ReplayMount struct {
	// Path is the mountpoint inside the source container, e.g. "/dev/shm".
	Path string `json:"path"`

	// FsType is the source filesystem type from /proc/<pid>/mountinfo,
	// recorded for diagnostic logging only — restore extracts into
	// whatever's mounted at Path in the placeholder.
	FsType string `json:"fsType,omitempty"`

	// Tarball is the relative path inside the checkpoint directory where
	// the tar was written, e.g. "mounts/dev_shm.tar".
	Tarball string `json:"tarball"`

	// Bytes is the size of the tar file at capture time, for logging.
	Bytes int64 `json:"bytes,omitempty"`
}

// CheckpointIntegrity holds SHA-256 checksums for checkpoint files.
type CheckpointIntegrity struct {
	Algorithm  string            `json:"algorithm"`  // "sha256"
	FileHashes map[string]string `json:"fileHashes"` // relative path -> hex hash
	TotalHash  string            `json:"totalHash"`  // hash of sorted concatenated hashes
}

// Checkpoint creates a GPU-aware checkpoint of a container
func (a *Agent) Checkpoint(ctx context.Context, req CheckpointRequest) (*CheckpointResult, error) {
	ctx, span := tracing.Tracer().Start(ctx, "checkpoint.full")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.namespace", req.Namespace),
		attribute.String("nvsnap.pod", req.PodName),
		attribute.String("nvsnap.container", req.ContainerName),
	)

	startTime := time.Now()
	identifier := req.PodName
	if identifier == "" {
		if req.ContainerName != "" {
			identifier = req.ContainerName
		} else if req.ContainerID != "" {
			identifier = req.ContainerID
		}
	}
	log := a.log.WithFields(logrus.Fields{
		"namespace":    req.Namespace,
		"pod":          req.PodName,
		"container":    req.ContainerName,
		"container_id": req.ContainerID,
	})
	log.Info("Starting checkpoint")

	disableCUDA := isTruthyEnv("NVSNAP_DISABLE_CUDA")

	// Step 1: Find container via the runtime abstraction (containerd or CRI-O)
	_, discoverSpan := tracing.Tracer().Start(ctx, "checkpoint.discover_container")
	var containerInfo *containerd.ContainerInfo
	var err error
	switch {
	case req.PodName != "":
		containerInfo, err = a.runtime.FindContainerByPod(ctx, req.Namespace, req.PodName, req.ContainerName)
	case req.ContainerID != "":
		containerInfo, err = a.runtime.FindContainerByID(ctx, req.ContainerID)
	case req.ContainerName != "":
		containerInfo, err = a.runtime.FindContainerByName(ctx, req.ContainerName)
	default:
		discoverSpan.SetStatus(codes.Error, "missing pod/container identifier")
		discoverSpan.End()
		return nil, fmt.Errorf("podName or containerId/containerName required for checkpoint")
	}
	if err != nil {
		discoverSpan.RecordError(err)
		discoverSpan.SetStatus(codes.Error, "container not found")
		discoverSpan.End()
		return nil, fmt.Errorf("failed to find container: %w", err)
	}
	discoverSpan.SetAttributes(
		attribute.String("nvsnap.container_id", containerInfo.ID[:12]),
		attribute.Int("nvsnap.container_pid", int(containerInfo.PID)),
		attribute.String("nvsnap.container_image", containerInfo.Image),
	)
	discoverSpan.End()
	log = log.WithFields(logrus.Fields{
		"containerID": containerInfo.ID[:12],
		"pid":         containerInfo.PID,
		"image":       containerInfo.Image,
	})

	// Resolve the source container's overlay upperdir AND its mount
	// table BEFORE the dump, while /proc/<pid>/mountinfo is still
	// readable. The upperdir directory persists on the host after the
	// dump exits the process; the mountpoint list is the set of
	// runtime/CDI/kubelet bind targets to exclude from the upperdir
	// mirror (those entries are stubs/whiteouts overlay leaves behind
	// for the bind, not workload state, and the destination's runtime
	// re-injects its own copies).
	sourceUpperdir, _ := mountinfo.ResolveOverlayUpperdir(int(containerInfo.PID))
	sourceMountPoints, _ := mountinfo.NonRootMountPoints(int(containerInfo.PID))
	log = log.WithFields(logrus.Fields{
		"upperdir":     sourceUpperdir,
		"sourceMounts": len(sourceMountPoints),
	})
	log.Info("Found container")

	// Pre-flight: classify the inference backend and refuse CRIU when
	// the workload uses Riva or Triton. cuda-checkpoint does not
	// serialize the host-pinned memory registration list (see
	// CLAUDE.md rule 20), and these backends call
	// cudaHostUnregister at teardown post-restore, which aborts the
	// process. The rootfs+OverlayFS path sidesteps this entirely.
	// Skip the check when CUDA is disabled — that path doesn't go
	// through cuda-checkpoint at all.
	if !disableCUDA {
		backend := DetectBackend(int(containerInfo.PID), "")
		log = log.WithField("backend", string(backend))
		// Decide CRIU vs rootfs, honoring the caller's per-request override
		// (req.CapturePath) on top of the detected backend and cluster
		// default. Routing rules + precedence live in
		// resolveCheckpointRedirect; the remaining hard gate (multi-GPU) is
		// enforced after GPU discovery below.
		//
		//  - Riva/Triton: cuda-checkpoint can't serialize their host-pinned
		//    memory; an explicit "criu" errors, auto/rootfs redirects.
		//  - cluster default rootfs (v0.0.48+; NVSNAP_DEFAULT_CAPTURE_PATH):
		//    redirects unless the caller explicitly requested "criu".
		// criu-v2 is an explicit CRIU-engine request for redirect purposes.
		effectivePath := req.CapturePath
		if isCRIUV2(req.CapturePath) {
			effectivePath = "criu"
		}
		redirect, rerr := resolveCheckpointRedirect(effectivePath, backend, RootfsIsDefault())
		if rerr != nil {
			log.WithField("requested_path", req.CapturePath).Warn(rerr.Error())
			return nil, rerr
		}
		if redirect {
			log.WithFields(logrus.Fields{"requested_path": req.CapturePath, "nvsnap_default_capture_path": os.Getenv("NVSNAP_DEFAULT_CAPTURE_PATH")}).Info("redirecting checkpoint to rootfs capture path")
			return nil, &BackendRedirectError{Backend: backend}
		}
	}

	// Step 2: Find GPU processes
	_, gpuDiscoverSpan := tracing.Tracer().Start(ctx, "checkpoint.discover_gpu_pids")
	var gpuPIDs []int
	gpuPID := 0
	if disableCUDA {
		gpuDiscoverSpan.SetAttributes(attribute.String("nvsnap.cuda.state", "disabled"))
		log.Info("CUDA disabled via NVSNAP_DISABLE_CUDA=1; skipping GPU checkpoint")
	} else {
		gpuPIDs, err = a.cuda.FindAllGPUPIDsForContainer(ctx, int(containerInfo.PID))
		if err != nil {
			gpuDiscoverSpan.RecordError(err)
			gpuDiscoverSpan.SetAttributes(attribute.String("nvsnap.cuda.state", "no-gpu"))
			log.WithError(err).Warn("No GPU processes found, continuing without GPU checkpoint")
		} else {
			gpuPID = gpuPIDs[0] // Primary GPU PID for CRIU externals
			log = log.WithFields(logrus.Fields{
				"gpuPID":  gpuPID,
				"gpuPIDs": gpuPIDs,
			})
			gpuDiscoverSpan.SetAttributes(attribute.Int("nvsnap.gpu_pids", len(gpuPIDs)))
			log.Info("Found GPU processes")
		}
	}
	gpuDiscoverSpan.End()

	// Step 2.5: Quiesce BEFORE cuda-checkpoint.
	// NCCL communicators must be destroyed before cuda-checkpoint runs, otherwise
	// cuda-checkpoint hangs on NCCL's internal CUDA state (proxy threads, IPC handles).
	// This sends SIGUSR1 to all processes, which triggers ncclCommDestroy in our
	// quiesce handler, then waits for all ranks to complete via done markers.
	//
	// NOTE: vLLM v0.11.2+ spawns multiple GPU processes for single-GPU (main + EngineCore),
	// so len(gpuPIDs) > 1 doesn't mean multi-GPU. Count distinct /dev/nvidiaX devices instead.
	distinctGPUs := countDistinctGPUDevices(gpuPIDs, log)
	// Multi-GPU CRIU path is a dead end (cuda-checkpoint blocks on
	// libcudart wall, D2H+intercept-lib path can't reconstruct CUDA
	// context state on restore). Multi-GPU workloads MUST use the
	// rootfs-only path (nvsnap.io/capture label → agent watcher →
	// per-capture PVC). Reject CRIU API calls for multi-GPU early.
	if distinctGPUs > 1 {
		return nil, fmt.Errorf("multi-GPU CRIU is unsupported (distinctGPUs=%d, gpuPIDs=%v); use the rootfs-only path: label the source pod nvsnap.io/capture=true and apply a fresh pod with nvsnap.io/restore-from=<hash>", distinctGPUs, gpuPIDs)
	}
	isMultiGPU := false
	useCUDAInterposition := false

	// Create checkpoint directory early so NvSnap can write GPU saves
	// directly to the final location (no temp files, no copy).
	checkpointNs := req.Namespace
	if checkpointNs == "" {
		checkpointNs = "local"
	}
	// NOTE: deliberately do NOT auto-delete prior checkpoints here.
	// Production callers (NVCF integration, retention controllers) own
	// checkpoint lifecycle policy. Test scripts that want fresh-state
	// semantics should delete via the API or ssh into the agent host
	// before invoking /v1/checkpoint.

	// Collect rich identity (CatalogInfo + content hash) up front so
	// the checkpoint id can be derived from the hash instead of the
	// ephemeral pod name. nvsnap#58: the id flows into the on-disk
	// dir name, the catalog row primary key, and the API path
	// segment; with pod name in the id, every pod-instance of the
	// same function produced a different id, which made dedup and
	// content-addressed lookup impossible. Hashing the canonical
	// identity (image + model + flags + driver) gives us one id per
	// (function, hardware tier) pair.
	//
	// Best-effort: any underlying lookup failure leaves the matching
	// field empty rather than erroring the capture. catalog.Hash is
	// always populated (computeHash handles a partially-empty input).
	catalogCtx, catalogCancel := context.WithTimeout(ctx, 5*time.Second)
	catalog := a.CollectCatalogInfo(catalogCtx, req.Namespace, req.PodName, req.ContainerName, a.config.NodeName, "")
	catalogCancel()

	checkpointID := buildCheckpointID(catalog, time.Now())
	checkpointDir := filepath.Join(a.config.CheckpointDir, checkpointID)
	if mkErr := os.MkdirAll(checkpointDir, 0o755); mkErr != nil {
		return nil, fmt.Errorf("failed to create checkpoint dir: %w", mkErr)
	}
	log.WithField("checkpointDir", checkpointDir).Info("Created checkpoint directory")
	log.WithFields(logrus.Fields{
		"gpuPIDs":       gpuPIDs,
		"distinctGPUs":  distinctGPUs,
		"isMultiGPU":    isMultiGPU,
		"interposition": useCUDAInterposition,
	}).Info("GPU topology detected")
	if isMultiGPU {
		log.Info("Multi-GPU: cgroup freeze + file-based quiesce")
		freezePath := getCgroupFreezePath(int(containerInfo.PID), log)
		if freezePath != "" {
			// Step 1: Freeze all processes atomically via cgroup
			if frErr := os.WriteFile(freezePath, []byte("1"), 0o644); frErr != nil {
				log.WithError(frErr).Warn("Failed to freeze cgroup, falling back to signal-based quiesce")
			} else {
				log.Info("Cgroup frozen")

				// Step 2: Write checkpoint path + trigger files while frozen.
				// Path: one shared file /dev/shm/nvsnap-checkpoint-dir (all processes read it)
				// Triggers: per-PID files in /dev/shm/ (just a signal, content irrelevant)
				procs := listContainerProcs(int(containerInfo.PID), log)
				containerCheckpointPath := filepath.Join("/checkpoints", checkpointID) //nolint:gocritic // leading-slash absolute root
				var triggerPIDs []int
				procRoot := fmt.Sprintf("/host/proc/%d/root", containerInfo.PID)
				if _, statErr := os.Stat(procRoot); os.IsNotExist(statErr) {
					procRoot = fmt.Sprintf("/proc/%d/root", containerInfo.PID)
				}

				// Single shared checkpoint path file — deterministic, no race
				_ = os.WriteFile(fmt.Sprintf("%s/dev/shm/nvsnap-checkpoint-dir", procRoot),
					[]byte(containerCheckpointPath), 0o644)

				// Multi-GPU marker — tells intercept library to use NCCL/D2H path
				_ = os.WriteFile(fmt.Sprintf("%s/dev/shm/nvsnap-multi-gpu", procRoot),
					[]byte("1"), 0o644)
				log.Info("Wrote multi-GPU marker to /dev/shm/nvsnap-multi-gpu")

				for _, proc := range procs {
					if proc.HostPID == int(containerInfo.PID) {
						continue
					}
					if !hasInterceptMapped(int(containerInfo.PID), proc.ContainerPID) {
						continue
					}
					triggerPath := fmt.Sprintf("%s/dev/shm/nvsnap-quiesce-trigger-%d", procRoot, proc.ContainerPID)
					_ = os.WriteFile(triggerPath, []byte("quiesce"), 0o644)
					triggerPIDs = append(triggerPIDs, proc.ContainerPID)
				}
				log.WithField("count", len(triggerPIDs)).Info("Wrote checkpoint path + trigger files while frozen")

				// Step 3: Unfreeze — all workers wake simultaneously, see triggers, run quiesce
				_ = os.WriteFile(freezePath, []byte("0"), 0o644)
				log.Info("Cgroup unfrozen — workers running quiesce")

				// Step 3.5: Send SIGUSR1 to all processes to trigger quiesce immediately.
				// Trigger files already written (step 2) with correct checkpoint path.
				// Only send signals here — do NOT call sendQuiesceSignalToInterceptProcs
				// which would overwrite trigger files with "quiesce" instead of the path.
				{
					innerProcs := listContainerProcs(int(containerInfo.PID), log)
					for _, proc := range innerProcs {
						if proc.HostPID == int(containerInfo.PID) {
							continue
						}
						_ = syscall.Kill(proc.HostPID, syscall.SIGUSR1)
					}
					log.WithField("count", len(procs)-1).Info("Sent SIGUSR1 to all processes")
				}

				// Step 4: Wait for done markers from GPU processes only.
				// Non-GPU processes (uvicorn workers, Python threads) may not
				// complete quiesce promptly — they have no GPU data to save.
				// Only the GPU processes matter for checkpoint correctness.
				gpuContainerPIDs := make([]int, 0, len(gpuPIDs))
				for _, hostPID := range gpuPIDs {
					// Find container PID for this host PID
					for _, proc := range procs {
						if proc.HostPID == hostPID {
							gpuContainerPIDs = append(gpuContainerPIDs, proc.ContainerPID)
							break
						}
					}
				}
				if len(gpuContainerPIDs) > 0 {
					waitForQuiesceMarkers(int(containerInfo.PID), gpuContainerPIDs, log)
				} else {
					waitForQuiesceMarkers(int(containerInfo.PID), triggerPIDs, log)
				}
				log.Info("Multi-GPU quiesce complete")

				// Step 5: Send SIGUSR2 to release quiesce spin-loops.
				// Without this, worker threads spin forever waiting for resume.
				sendQuiesceResumeToInterceptProcs(int(containerInfo.PID), log)
			}
		} else {
			log.Warn("No cgroup freeze path found, attempting signal-based quiesce")
			sendQuiesceSignalToInterceptProcs(int(containerInfo.PID), log)
		}
	}

	// Step 3: GPU checkpoint via CUDA Checkpoint API (multi-GPU) or cuda-checkpoint CLI (single-GPU).
	// For multi-GPU: quiesce already called nvsnap_pre_checkpoint_quiesce() (NCCL destroy + P2P disable).
	// Now call cuCheckpointProcess{Lock,Checkpoint} per worker via the nvsnap-gpu-restore binary.
	_, gpuSpan := tracing.Tracer().Start(ctx, "checkpoint.gpu_state")
	gpuSpan.SetAttributes(
		attribute.Int("nvsnap.gpu_pids", len(gpuPIDs)),
		attribute.Bool("nvsnap.cuda_interposition", useCUDAInterposition),
	)
	var checkpointedGPUPIDs []int
	if useCUDAInterposition {
		// GPU data is already saved by D2H cudaMemcpy during quiesce (nvsnap_cuda_save).
		// Each process wrote its GPU allocations to <checkpoint_dir>/gpu-<pid>/.
		// No CUDA Checkpoint API needed for save — that approach hangs on multi-GPU vLLM.
		// CRIU dump will capture host process memory (CPU-side only).
		log.WithField("gpuPIDs", gpuPIDs).Info("Multi-GPU: GPU data saved during quiesce (D2H), skipping CUDA Checkpoint API")
		checkpointedGPUPIDs = gpuPIDs
	} else if isCRIUV2(req.CapturePath) {
		// criu-v2: no pre-dump lock/checkpoint — the bundled cuda_plugin
		// drives cuda-checkpoint during the in-namespace dump itself.
		log.Info("criu-v2: GPU state handled by cuda_plugin during in-ns dump")
	} else if len(gpuPIDs) > 0 && !disableCUDA {
		// Phase 1 (serial): Lock every GPU process. Lock is fast (just
		// acquires cuCheckpointProcess "lock") so serialization here costs
		// little, while keeping the existing soft-disable semantic — if the
		// FIRST Lock fails we skip GPU checkpoint entirely. We pre-skip
		// processes that don't have a CUDA context (forked FD-holders).
		var lockedPIDs []int
		lockFailed := false
		for i, pid := range gpuPIDs {
			log.WithFields(logrus.Fields{
				"pid":      pid,
				"progress": fmt.Sprintf("%d/%d", i+1, len(gpuPIDs)),
			}).Info("Locking GPU for process")
			if !a.cuda.HasCUDAContext(ctx, pid) {
				log.WithField("pid", pid).Debug("No CUDA context, skipping")
				continue
			}
			if lockErr := a.cuda.Lock(ctx, pid); lockErr != nil {
				log.WithError(lockErr).WithField("pid", pid).Error("Failed to lock GPU for process")
				for _, prev := range lockedPIDs {
					_ = a.cuda.Unlock(ctx, prev)
				}
				lockFailed = true
				break
			}
			lockedPIDs = append(lockedPIDs, pid)
		}
		if lockFailed {
			gpuPID = 0
		} else if len(lockedPIDs) > 0 {
			// Phase 2 (parallel): cuda-checkpoint --action checkpoint per
			// PID. This is the slow operation that dominates GPU
			// checkpoint wall-time — 6+ minutes on a 38-PID NIM under
			// the old serial loop (memory entry #176). Each subprocess
			// is independent; we cap concurrency via
			// cudaParallelism() (default 8, override
			// NVSNAP_CUDA_PARALLELISM). If any Checkpoint fails we
			// roll back the entire GPU side (Restore+Unlock on
			// already-checkpointed PIDs, Unlock-only on the rest)
			// before returning, matching the original atomicity.
			parallel := cudaParallelism()
			log.WithFields(logrus.Fields{
				"count":       len(lockedPIDs),
				"parallelism": parallel,
			}).Info("Checkpointing GPU state across processes (parallel)")
			g, gctx := errgroup.WithContext(ctx)
			g.SetLimit(parallel)
			var mu sync.Mutex
			for _, pid := range lockedPIDs {

				g.Go(func() error {
					if ckErr := a.cuda.Checkpoint(gctx, pid); ckErr != nil {
						return fmt.Errorf("pid=%d: %w", pid, ckErr)
					}
					mu.Lock()
					checkpointedGPUPIDs = append(checkpointedGPUPIDs, pid)
					mu.Unlock()
					return nil
				})
			}
			if werr := g.Wait(); werr != nil {
				ckpted := make(map[int]bool, len(checkpointedGPUPIDs))
				for _, p := range checkpointedGPUPIDs {
					ckpted[p] = true
				}
				for _, p := range checkpointedGPUPIDs {
					_ = a.cuda.Restore(ctx, p)
					_ = a.cuda.Unlock(ctx, p)
				}
				for _, p := range lockedPIDs {
					if !ckpted[p] {
						_ = a.cuda.Unlock(ctx, p)
					}
				}
				return nil, fmt.Errorf("failed to checkpoint GPU: %w", werr)
			}
		}
		if len(checkpointedGPUPIDs) > 0 {
			log.WithFields(logrus.Fields{
				"count":       len(checkpointedGPUPIDs),
				"parallelism": cudaParallelism(),
			}).Info("All GPU processes checkpointed")
		}
	}
	gpuSpan.SetAttributes(attribute.Int("nvsnap.checkpointed_gpu_pids", len(checkpointedGPUPIDs)))
	gpuSpan.End()

	// Step 4: Checkpoint directory already created above (before quiesce).
	// Ensure GPU PIDs are cleaned up on error.
	if false {
		// Dead code — checkpoint dir created early for NvSnap direct writes.
		return nil, fmt.Errorf("unreachable")
	}

	// Step 4.5: Copy all runtime-generated mapped files BEFORE CRIU dump
	// (Process must be alive to read /proc/<pid>/maps)
	_, mappedSpan := tracing.Tracer().Start(ctx, "checkpoint.copy_mapped_files")
	log.Info("Copying runtime-generated mapped files")
	if cmErr := copyMappedFiles(int(containerInfo.PID), gpuPID, containerInfo.RootFS, checkpointDir, log); cmErr != nil {
		mappedSpan.RecordError(cmErr)
		log.WithError(cmErr).Warn("Failed to copy some mapped files (restore may be slower)")
	}
	mappedSpan.End()

	// Step 4.5.2: Copy all open files (log files, cache files, etc.) BEFORE CRIU dump
	// These are files CRIU expects to exist at restore time but aren't in the base image
	_, openSpan := tracing.Tracer().Start(ctx, "checkpoint.copy_open_files")
	log.Info("Copying open files (logs, caches)")
	openFiles, err := copyOpenFiles(int(containerInfo.PID), checkpointDir, log)
	if err != nil {
		openSpan.RecordError(err)
		log.WithError(err).Warn("Failed to copy some open files (restore may fail for missing files)")
	}
	openSpan.SetAttributes(attribute.Int("nvsnap.open_file_count", len(openFiles)))
	openSpan.End()

	// Step 4.5.3: Copy GPU save files from /dev/shm for multi-GPU interposition
	if useCUDAInterposition {
		if gsErr := copyGPUSaveFiles(int(containerInfo.PID), checkpointDir, log); gsErr != nil {
			return nil, fmt.Errorf("failed to copy GPU save files: %w", gsErr)
		}
	}

	// Step 4.6: Backup /dev/shm files (psm_*, nccl-*) for restore.
	// CRIU dumps VMA pages but expects these files to exist with correct size.
	_, shmSpan := tracing.Tracer().Start(ctx, "checkpoint.backup_shm_files")
	backupShmFiles(int(containerInfo.PID), checkpointDir, log)
	shmSpan.End()

	// Step 4.6.1: Clean up shared memory before checkpoint
	// Preserves POSIX semaphores (needed by vLLM IPC), removes pym-* files.
	_, cleanupSpan := tracing.Tracer().Start(ctx, "checkpoint.cleanup_shm")
	cleanupSharedMemory(int(containerInfo.PID), log)
	cleanupSpan.End()

	// Step 4.6.1: Trigger quiescence via SIGUSR1 (if libnvsnap_intercept.so is loaded)
	// This drains io_uring rings and prepares libuv for restore.
	// For multi-GPU, quiescence was already done in Step 2.5 (before cuda-checkpoint).
	_, quiesceSpan := tracing.Tracer().Start(ctx, "checkpoint.quiesce")
	if !isMultiGPU {
		quiesceSpan.SetAttributes(attribute.String("nvsnap.quiesce.path", "single-gpu"))
		log.Info("Triggering quiescence (io_uring drain, libuv prep)")
		sendQuiesceSignal(int(containerInfo.PID), log)
		sendQuiesceSignalToInterceptProcs(int(containerInfo.PID), log)
	} else {
		quiesceSpan.SetAttributes(attribute.String("nvsnap.quiesce.path", "multi-gpu-already-done"))
		log.Info("Quiescence already completed in Step 2.5 (multi-GPU)")
	}
	quiesceSpan.End()

	// Step 4.6.3: Capture uvloop metadata emitted during quiesce
	_, uvloopSpan := tracing.Tracer().Start(ctx, "checkpoint.copy_uvloop_metadata")
	if umErr := copyUvloopMetadata(int(containerInfo.PID), checkpointDir, log); umErr != nil {
		uvloopSpan.RecordError(umErr)
		log.WithError(umErr).Warn("Failed to copy uvloop metadata")
	}
	uvloopSpan.End()

	// Step 4.7: Get pod IP for stable network identity (MUST be before CRIU dump)
	// After CRIU dump, the process may be gone and /proc/<pid> won't exist.
	podIP := a.getPodIP(int(containerInfo.PID))
	if podIP != "" {
		log.WithField("podIP", podIP).Info("Captured pod IP for restore compatibility")
	} else {
		log.Warn("Could not determine pod IP - loopback alias won't be set on restore")
	}

	// Step 4.8: Capture pipe IDs for stdout/stderr (MUST be before CRIU dump)
	// These are needed for InheritFd during restore to replace broken pipes
	stdoutPipeID, stderrPipeID := getPipeIDs(int(containerInfo.PID), log)

	// Step 4.9: Capture temp directories and files as late as possible
	// (some frameworks write temp files after startup)
	_, tempSpan := tracing.Tracer().Start(ctx, "checkpoint.capture_temp_dirs")
	tempDirs, err := captureTempDirectories(int(containerInfo.PID), checkpointDir, log)
	if err != nil {
		tempSpan.RecordError(err)
		log.WithError(err).Warn("Failed to capture temp directories")
	} else if len(tempDirs) > 0 {
		tempSpan.SetAttributes(attribute.Int("nvsnap.temp_dir_count", len(tempDirs)))
		if err := copyTempFiles(int(containerInfo.PID), tempDirs, checkpointDir, log); err != nil {
			tempSpan.RecordError(err)
			log.WithError(err).Warn("Failed to copy temp files")
		}
	}
	tempSpan.End()

	// Step 4.95: Capture replay-mount snapshots for cross-pod restore.
	// For each mountpoint on the configured allowlist (default: /dev/shm),
	// tar its contents into <checkpointDir>/mounts/<sanitized>.tar so
	// untarIntoMntns can replay them into the placeholder pod's mntns
	// before CRIU restore. Required for multi-GPU NCCL/PSM workloads whose
	// inter-rank SHM segments only exist in the source pod's tmpfs.
	// See docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md.
	//
	// Timing: AFTER cuda Lock + cleanupSharedMemory + quiesce — no GPU
	// process is writing into the snapshot path at this point. Per-mount
	// failures are logged and skipped (warn-and-continue, Q6 in design).
	var replayMounts []ReplayMount
	{
		_, replaySpan := tracing.Tracer().Start(ctx, "checkpoint.capture_replay_mounts")
		allowlist := ReplayMountAllowlist()
		mountsDir := filepath.Join(checkpointDir, "mounts")
		mis, miErr := mountinfo.ParseMountinfo(mountinfo.ProcMountinfoPath(int(containerInfo.PID)))
		if miErr != nil {
			replaySpan.RecordError(miErr)
			log.WithError(miErr).Warn("Failed to parse mountinfo for replay-mount snapshot")
		}
		for _, mi := range mis {
			if classifyMount(mi.MountPoint, mi.FsType, mi.Opts, allowlist) != MountClassReplay {
				continue
			}
			tarRel := filepath.Join("mounts", SanitizeMountPath(mi.MountPoint)+".tar")
			tarAbs := filepath.Join(checkpointDir, tarRel)
			if err := tarMount(int(containerInfo.PID), mi.MountPoint, tarAbs, log); err != nil {
				log.WithError(err).WithField("mp", mi.MountPoint).
					Warn("Replay-mount snapshot failed; restore may fail for this mount")
				continue
			}
			st, _ := os.Stat(tarAbs)
			var size int64
			if st != nil {
				size = st.Size()
			}
			replayMounts = append(replayMounts, ReplayMount{
				Path:    mi.MountPoint,
				FsType:  mi.FsType,
				Tarball: tarRel,
				Bytes:   size,
			})
		}
		if len(replayMounts) > 0 {
			log.WithFields(logrus.Fields{
				"count":     len(replayMounts),
				"mountsDir": mountsDir,
				"allowlist": allowlist,
			}).Info("Captured replay-mount snapshots")
		}
		replaySpan.SetAttributes(attribute.Int("nvsnap.replay_mount_count", len(replayMounts)))
		replaySpan.End()
	}

	// Step 4.9: drop external TCP_ESTABLISHED sockets before CRIU dump.
	//
	// CRIU's restore reconstructs captured TCP_ESTABLISHED via
	// connect-back at soccr/soccr.c:529. When the captured src_addr is
	// the dump-time pod IP and the peer is a public IP, the restore
	// loopback alias is unreachable as a routable source and connect()
	// returns EADDRNOTAVAIL — bug nvsnap#187, observed today on NVCA-
	// stamped restore pods that hold an external NATS connection.
	//
	// Selective close: only sockets with peer OUTSIDE typical
	// K8s/private ranges are destroyed. Intra-pod TCP (e.g. PyTorch
	// TCPStore at the dump-time pod IP) stays captured and restores
	// fine via the loopback alias. The workload reconnects after
	// restore exactly as it would after any pod restart.
	if destroyed, err := closeExternalTCPInNS(int(containerInfo.PID), log); err != nil {
		log.WithError(err).Warn("Pre-checkpoint external-TCP close failed; continuing — restore may abort with EADDRNOTAVAIL if external peers were captured")
	} else if destroyed > 0 {
		log.WithField("count", destroyed).Info("Dropped external TCP_ESTABLISHED pre-checkpoint (nvsnap#187)")
	}

	// Step 5: Checkpoint process using CRIU directly
	log.Info("Checkpointing process with CRIU")
	// Derive plugin dir from CRIU path (e.g., /criu-bundle/criu -> /criu-bundle/plugins)
	// CRIU expects plugin .so files directly under libdir (no recursion).
	// Always load CUDA plugin — it handles NVIDIA device FDs (DUMP_EXT_FILE) which CRIU
	// cannot dump without. For interposition mode, NVSNAP_SKIP_CUDA_CHECKPOINT tells the
	// plugin to skip PAUSE_DEVICES/CHECKPOINT_DEVICES (no cuda-checkpoint calls).
	var captureResult <-chan streamer.CaptureResult
	var criuOpts criu.DumpOptions // populated by the legacy path; zero for criu-v2 (flags recorded in dump.log)
	var dumpMountPoints []string  // legacy Plan-A mountpoints; empty for criu-v2 (in-ns restore needs no ExtMnt)
	if isCRIUV2(req.CapturePath) {
		// criu-v2: in-namespace dump (bundle staged into the container,
		// criu exec'd via nsenter). See checkpoint_v2.go. Post-dump steps
		// (rootfs-diff mirror, metadata, upload) are shared with the
		// legacy path — the artifact contract is identical.
		_, criuSpan := tracing.Tracer().Start(ctx, "checkpoint.criu_dump")
		criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "v2-inns"))
		if err := a.dumpV2(ctx, containerInfo, checkpointDir, sourceUpperdir, gpuPIDs, req.LeaveRunning, log); err != nil {
			criuSpan.RecordError(err)
			criuSpan.SetStatus(codes.Error, "CRIU dump failed (criu-v2)")
			criuSpan.End()
			return nil, fmt.Errorf("CRIU dump failed (criu-v2): %w", err)
		}
		criuSpan.End()
	} else {
		pluginDir := resolveCRIUPluginDir(a.config.CRIUPath, log)

		if useCUDAInterposition {
			_ = os.Setenv("NVSNAP_SKIP_CUDA_CHECKPOINT", "1")
			defer func() { _ = os.Unsetenv("NVSNAP_SKIP_CUDA_CHECKPOINT") }()
			log.Info("CUDA interposition: NVSNAP_SKIP_CUDA_CHECKPOINT=1 (ptrace RM restore on restore side)")
		}

		// Plan A: build explicit ExtMnt mappings from mountinfo and use RPC dump (k8s-runc-bypass style).
		extMntMaps, dmp, skipMounts, rootForDump, mapErr := a.buildDumpExtMnt(int(containerInfo.PID), containerInfo.RootFS)
		dumpMountPoints = dmp

		// Keep DumpOptions for metadata reporting and as a temporary CLI fallback.
		criuOpts = criu.DumpOptions{
			PID:               int(containerInfo.PID),
			ImagesDir:         checkpointDir,
			LeaveRunning:      req.LeaveRunning,
			ShellJob:          true,
			Timeout:           1200,      // 20 minutes - cuda-checkpoint needs time for large GPU models
			PluginDir:         pluginDir, // CUDA plugin handles nvidia device FDs
			TCPEstab:          true,      // Allow dumping connected sockets (otherwise CRIU fails)
			TcpClose:          false,     // Preserve TCP connections for inter-process communication (vLLM workers)
			NetworkLockMethod: "skip",    // Skip iptables (may not be available in container)
			LinkRemap:         true,      // Required for ghost files
			FileLocks:         true,      // Many apps use file locks for coordination
			SkipUnixSockets:   false,     // Don't skip - apps often use unix sockets for worker IPC
			// Clean plan: avoid mnt[] wildcard; mounts handled via explicit ExtMnt in RPC dump.
			External:     nil, // filled after computing extNet
			SkipFsnotify: false,
			SkipInFlight: true,
			SkipMounts:   skipMounts,
			LogLevel:     4,
			// Always allow dumping processes with [uprobes] VMAs. Some kernels
			// (e.g., OCI CRI-O nodes) have uprobes attached via observability
			// tooling; without this flag, CRIU aborts with "PID has uprobes vma".
			// Safe to always set — CRIU just skips the uprobe VMA (no functional
			// impact on the dumped process or restore correctness).
			AllowUprobes: true,
		}

		var extNet string
		if inode, err := getNetnsInode(int(containerInfo.PID)); err == nil {
			extNet = fmt.Sprintf("net[%d]:extNetNs", inode)
		} else {
			log.WithError(err).Warn("Failed to determine netns inode; network may not restore cleanly")
		}
		// Build list of external resources: network namespace + NVIDIA device files
		externalResources := []string{}
		if extNet != "" {
			externalResources = append(externalResources, extNet)
		} else {
			externalResources = append(externalResources, "net[]")
		}

		// Scan for character device FDs and add them to external list.
		// This covers nvidia, gdrdrv, and any future GPU-related drivers.
		// The CUDA plugin's DUMP_EXT_FILE hook handles the actual save.
		charDevices := scanCharDeviceFDs(int(containerInfo.PID), log)
		externalResources = append(externalResources, charDevices...)

		// Also scan /dev for NVIDIA device nodes (covers cases where FDs are not visible)
		nvidiaNodes := scanNvidiaDeviceNodes(int(containerInfo.PID), log)
		externalResources = append(externalResources, nvidiaNodes...)

		// Deduplicate externals (e.g., FD + node scan overlap)
		uniqueExternals := make([]string, 0, len(externalResources))
		seenExternals := make(map[string]bool)
		for _, ext := range externalResources {
			if seenExternals[ext] {
				continue
			}
			seenExternals[ext] = true
			uniqueExternals = append(uniqueExternals, ext)
		}
		externalResources = uniqueExternals

		log.WithFields(logrus.Fields{
			"fdCount":   len(charDevices),
			"nodeCount": len(nvidiaNodes),
			"total":     len(externalResources),
		}).Debug("Found device externals to mark")

		criuOpts.External = externalResources

		// Set CWD to checkpoint directory so the CUDA plugin saves nvidia-files.img there.
		// The plugin uses CWD as fallback when opts.imgs_dir is null (RPC mode).
		// During restore, CWD is also the checkpoint directory → file found.
		if origCwd, err := os.Getwd(); err == nil {
			if err := os.Chdir(checkpointDir); err == nil {
				defer func() { _ = os.Chdir(origCwd) }()
				log.WithField("cwd", checkpointDir).Debug("Set CWD to checkpoint directory for CUDA plugin")
			}
		}

		if mapErr == nil && len(extMntMaps) > 0 && extNet != "" {
			rpcOpts := criu.DumpRPCOptions{
				PID:          int(containerInfo.PID),
				ImagesDir:    checkpointDir,
				Root:         rootForDump,
				LeaveRunning: req.LeaveRunning,
				ShellJob:     true,
				PluginDir:    pluginDir,

				ExtMnt:     extMntMaps,
				External:   externalResources,
				SkipMounts: skipMounts,

				TCPEstab:        true,
				TcpClose:        false, // Preserve inter-process TCP connections
				FileLocks:       true,
				LinkRemap:       true,
				ExtUnixSk:       true,
				ExtMasters:      true,
				OrphanPtsMaster: true,
				SkipInFlight:    true,
				AllowUprobes:    true, // safe everywhere; required on kernels with uprobes-tracing tooling (OCI CRI-O)
				Timeout:         1200,
				GhostLimit:      512 * 1024 * 1024,
				LogLevel:        4,
				Stream:          os.Getenv("NVSNAP_COMPRESS_CHECKPOINT") == "1",
			}

			// Start in-process capture streamer for transparent compression
			if rpcOpts.Stream {
				var err error
				captureResult, err = streamer.StartCapture(checkpointDir)
				if err != nil {
					log.WithError(err).Warn("Failed to start capture streamer, falling back to non-streaming")
					rpcOpts.Stream = false
				}
			}

			_, criuSpan := tracing.Tracer().Start(ctx, "checkpoint.criu_dump")
			criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "rpc"))
			if err := a.criu.DumpRPC(ctx, rpcOpts); err != nil {
				criuSpan.RecordError(err)
				criuSpan.SetStatus(codes.Error, "CRIU dump failed (rpc)")
				criuSpan.End()
				for _, pid := range checkpointedGPUPIDs {
					_ = a.cuda.Restore(ctx, pid)
					_ = a.cuda.Unlock(ctx, pid)
				}
				return nil, fmt.Errorf("CRIU dump failed (rpc): %w", err)
			}
			criuSpan.End()
		} else {
			// CLI fallback (temporary)
			if mapErr != nil {
				log.WithError(mapErr).Warn("Failed to build ExtMnt mappings; falling back to CLI dump")
			}
			_, criuSpan := tracing.Tracer().Start(ctx, "checkpoint.criu_dump")
			criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "cli"))
			if err := a.criu.Dump(ctx, criuOpts); err != nil {
				criuSpan.RecordError(err)
				criuSpan.SetStatus(codes.Error, "CRIU dump failed (cli)")
				criuSpan.End()
				for _, pid := range checkpointedGPUPIDs {
					_ = a.cuda.Restore(ctx, pid)
					_ = a.cuda.Unlock(ctx, pid)
				}
				return nil, fmt.Errorf("CRIU dump failed: %w", err)
			}
			criuSpan.End()
		}

	} // end legacy (non-criu-v2) dump path

	// Wait for capture streamer to finish (if active)
	var compressionInfo *streamer.CompressionInfo
	if captureResult != nil {
		result := <-captureResult
		if result.Err != nil {
			return nil, fmt.Errorf("capture streamer failed: %w", result.Err)
		}
		compressionInfo = result.Compression
		log.WithFields(logrus.Fields{
			"ratio":      fmt.Sprintf("%.1fx", result.Compression.Ratio),
			"original":   result.Compression.TotalOriginalSize,
			"compressed": result.Compression.TotalCompressedSize,
		}).Info("Capture streamer: compression complete")
	}

	log.Info("Process checkpointed with CRIU")

	// Snapshot the source container's overlay upperdir into the checkpoint
	// directory. The upperdir is the container's writable diff layer —
	// everything the source wrote at runtime (init-injected libraries,
	// runtime caches, IPC dirs, log files, framework JIT artefacts).
	// CRIU records file-backed VMAs and open regular fds by path; at
	// restore those paths must exist in the placeholder pod with matching
	// sizes, but the placeholder is a clean container that hasn't run the
	// workload. Mirroring the upperdir into the checkpoint and replaying
	// it onto the placeholder's upperdir at restore time is the generic,
	// workload-agnostic way to satisfy CRIU's path-based stat checks.
	// Lower-layer files come from the shared container image and exist on
	// the placeholder by construction.
	//
	// Use the upperdir we resolved BEFORE the dump — by this point the
	// source PID is gone and /proc/<pid>/mountinfo no longer exists, but
	// the directory itself persists on the host until the runtime cleans
	// up the container. The on-disk path is what we need.
	if sourceUpperdir != "" {
		_, mirrorSpan := tracing.Tracer().Start(ctx, "checkpoint.mirror_rootfs_diff")
		diffDst := filepath.Join(checkpointDir, "rootfs-diff")
		if mErr := mirrorOverlayDir(sourceUpperdir, diffDst, sourceMountPoints, rootfsDiffExcludeGlobs(), log); mErr != nil {
			mirrorSpan.RecordError(mErr)
			log.WithError(mErr).Warn("Failed to mirror overlay upperdir; restore may fail on missing files")
		}
		if fi, _ := os.Stat(diffDst); fi != nil {
			// Best-effort: report rootfs-diff size as span attribute.
			var total int64
			_ = filepath.Walk(diffDst, func(_ string, info os.FileInfo, _ error) error {
				if info != nil && !info.IsDir() {
					total += info.Size()
				}
				return nil
			})
			mirrorSpan.SetAttributes(attribute.Int64("nvsnap.rootfs_diff_bytes", total))
		}
		mirrorSpan.End()
	}

	// Step 6: Unlock GPU (if leave-running, restore first)
	for _, pid := range checkpointedGPUPIDs {
		if req.LeaveRunning {
			log.WithField("pid", pid).Info("Restoring GPU state (leave-running mode)")
			_ = a.cuda.Restore(ctx, pid)
		}
		log.WithField("pid", pid).Info("Unlocking GPU")
		_ = a.cuda.Unlock(ctx, pid)
	}

	// Step 6.1: Send resume signal if leave-running (process continues after checkpoint)
	if req.LeaveRunning {
		sendResumeSignal(int(containerInfo.PID), log)
	}

	// Step 7: Parse dump.log to extract skipped resources.
	dumpLogPath := filepath.Join(checkpointDir, "dump.log")
	skippedResources, perr := parseSkippedResources(dumpLogPath)
	if perr != nil {
		log.WithError(perr).Warn("Failed to parse skipped resources from dump.log")
		skippedResources = &SkippedResources{}
	} else {
		log.WithFields(logrus.Fields{
			"unixSockets":    len(skippedResources.UnixSocketFds),
			"inetSockets":    len(skippedResources.InetSocketFds),
			"ioUringThreads": len(skippedResources.IoUringThreads),
			"mounts":         len(skippedResources.Mounts),
		}).Info("Parsed skipped resources from dump.log")
	}

	// Step 8: Get mapped files info.
	// mapped-files.txt is staged BEFORE the dump by copyMappedFiles, so
	// it's readable from the agent's view in both legacy and phase 5b
	// paths.
	mappedFiles := getMappedFilesInfo(checkpointDir)
	log.WithField("count", len(mappedFiles)).Debug("Collected mapped files info")

	// Step 9: Build restore hints
	// Combine skipped socket fds into a single list for restore
	var skipFds []int
	skipFds = append(skipFds, skippedResources.UnixSocketFds...)
	skipFds = append(skipFds, skippedResources.InetSocketFds...)

	restoreHints := &RestoreHints{
		SkipFds:            skipFds,
		RestoreMappedFiles: len(mappedFiles) > 0,
		CUDARestoreNeeded:  len(checkpointedGPUPIDs) > 0,
		NetworkMode:        "external", // Plan A4: external netns via InheritFd
	}

	// Step 10: Save enhanced metadata
	metadata := CheckpointMetadata{
		Version:        "1.4",
		ID:             checkpointID,
		CreatedAt:      time.Now(),
		PodName:        identifier,
		PodNamespace:   checkpointNs,
		NodeName:       a.config.NodeName,
		ContainerName:  containerInfo.Name,
		ContainerID:    containerInfo.ID,
		ContainerImage: containerInfo.Image,
		ContainerPID:   containerInfo.PID,
		RootFS:         containerInfo.RootFS,
		PodLabels:      containerInfo.Labels,
		SourcePodIP:    podIP,
		StdoutPipeID:   stdoutPipeID,
		StderrPipeID:   stderrPipeID,
		Skipped:        skippedResources,
		MappedFiles:    mappedFiles,
		OpenFiles:      openFiles,
		CUDA: &CUDACheckpointInfo{
			Enabled:           len(checkpointedGPUPIDs) > 0 || useCUDAInterposition,
			GPUPID:            gpuPID,
			LockSuccess:       len(checkpointedGPUPIDs) > 0,
			CheckpointSuccess: len(checkpointedGPUPIDs) > 0 || useCUDAInterposition,
			RestoreSuccess:    req.LeaveRunning && len(checkpointedGPUPIDs) > 0,
			UnlockSuccess:     true,
			Interposition:     useCUDAInterposition,
		},
		CRIUOptions: &CRIUOptionsUsed{
			SkipUnixSockets: criuOpts.SkipUnixSockets,
			SkipInFlight:    criuOpts.SkipInFlight,
			SkipFsnotify:    criuOpts.SkipFsnotify,
			LeaveRunning:    criuOpts.LeaveRunning,
			TCPEstablished:  criuOpts.TCPEstab,
			External:        criuOpts.External,
		},
		Hints:           restoreHints,
		DumpMountPoints: dumpMountPoints,
		ReplayMounts:    replayMounts,
		Compression:     compressionInfo,
		// Deprecated but kept for compatibility
		GPUPID: gpuPID,
	}
	if isCRIUV2(req.CapturePath) {
		metadata.CapturePath = CapturePathCRIUV2
	}

	// Calculate checkpoint size (skip integrity checksums — too slow for 28 GB+)
	var checkpointSize int64
	_ = filepath.Walk(checkpointDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			checkpointSize += info.Size()
		}
		return nil
	})

	metadata.CheckpointSize = checkpointSize
	metadata.DurationSeconds = time.Since(metadata.CreatedAt).Seconds()

	// Hash + CatalogInfo were collected up-front (used to derive the
	// checkpoint id); fold them into the metadata before write.
	// (nvsnap#56)
	metadata.Hash = catalog.Hash
	metadata.CatalogInfo = &catalog

	_, metaSpan := tracing.Tracer().Start(ctx, "checkpoint.save_metadata")
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	metadataPath := filepath.Join(checkpointDir, "metadata.json")
	if err := os.WriteFile(metadataPath, metadataBytes, 0o644); err != nil {
		metaSpan.RecordError(err)
		metaSpan.SetStatus(codes.Error, "write metadata")
		metaSpan.End()
		return nil, fmt.Errorf("failed to write metadata: %w", err)
	}
	metaSpan.SetAttributes(
		attribute.Int64("nvsnap.checkpoint_size", checkpointSize),
		attribute.Float64("nvsnap.checkpoint_duration_secs", metadata.DurationSeconds),
		attribute.String("nvsnap.hash_short", catalog.ShortHash),
		attribute.String("nvsnap.gpu_type", catalog.GPUType),
		attribute.String("nvsnap.model_id", catalog.ModelID),
	)
	metaSpan.End()
	log.WithFields(logrus.Fields{
		"hash":    catalog.ShortHash,
		"gpuType": catalog.GPUType,
		"modelID": catalog.ModelID,
		"engine":  catalog.Engine,
	}).Info("Saved checkpoint metadata")

	// NOTE: Cleanup disabled during development/testing to preserve checkpoints
	// TODO: Re-enable with configurable retention policy
	// a.cleanupOldPodCheckpoints(req.Namespace, req.PodName, log)

	// Validate checkpoint contains all required files.
	// Skip when streaming — all .img files are inside stream.lz4, not individual files.
	_, validateSpan := tracing.Tracer().Start(ctx, "checkpoint.validate")
	if _, err := os.Stat(filepath.Join(checkpointDir, "stream.lz4")); os.IsNotExist(err) {
		if err := validateCheckpoint(checkpointDir, len(checkpointedGPUPIDs) > 0, log); err != nil {
			validateSpan.RecordError(err)
			validateSpan.SetStatus(codes.Error, "validation failed")
			validateSpan.End()
			return nil, fmt.Errorf("checkpoint validation failed: %w", err)
		}
	} else {
		validateSpan.SetAttributes(attribute.String("nvsnap.validate.skipped", "streaming"))
		log.Info("Streaming checkpoint — skipping individual file validation")
	}
	validateSpan.End()

	duration := time.Since(startTime).Seconds()
	log.WithField("duration", fmt.Sprintf("%.2fs", duration)).Info("Checkpoint completed")

	result := &CheckpointResult{
		CheckpointID:   checkpointID,
		CheckpointPath: checkpointDir,
		CheckpointRef:  "", // Direct CRIU checkpoint, no containerd image
		CheckpointSize: checkpointSize,
		ContainerID:    containerInfo.ID,
		OriginalImage:  containerInfo.Image,
		GPUPID:         gpuPID,
		Duration:       duration,
		Timestamp:      time.Now(),
		Hash:           catalog.Hash,
		CatalogInfo:    &catalog,
	}

	// Phase 5d: removed the legacy promoteCheckpointToBackend call. The
	// hostPath dump is the source of truth on this node; cross-node
	// fanout serves directly from the agent's /v1/checkpoints/{id}/file
	// endpoint (no local cache copy needed), and durability goes to
	// nvsnap-blobstore via UploadCheckpoint (below). The old promote
	// step was 30 GB of redundant local I/O per capture (~4 min on
	// gp3) for what's now a no-op tier.

	// Phase 5d.2: register the checkpoint in the catalog so peer-add,
	// blob-uploaded, and /sources have a row to anchor on. Synchronous
	// here because the blob-upload + peer-add goroutine below depends
	// on the row existing; best-effort error handling so a catalog
	// outage doesn't fail the capture itself.
	if a.config.CatalogURL != "" {
		regCtx, regCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := a.registerCheckpointInCatalog(regCtx, checkpointID, req.Namespace, req.PodName,
			req.ContainerName, containerInfo.Image, checkpointSize, duration, len(checkpointedGPUPIDs) > 0, catalog); err != nil {
			log.WithError(err).Warn("catalog register failed (non-fatal — peer-add and blob-uploaded callbacks will 404 until reconciled)")
		}
		regCancel()
		// Capture node advertises itself as a peer. Without this, the
		// first cross-node restore sees an empty peers list and falls
		// back to the blob store (slow path) — defeating the entire
		// cascade. Restore-side already registers on successful fetch
		// in EnsureLocal; capture-side wasn't doing the symmetric step.
		peerCtx, peerCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := a.registerAsPeer(peerCtx, checkpointID); err != nil {
			log.WithError(err).Warn("capture-side peer-add failed (non-fatal — first cross-node restore will fall back to blob store)")
		}
		peerCancel()
	}

	// nvsnap#166: L2 per-capture PVC promote runs ASYNC in a background
	// goroutine. The HTTP response to nvsnap-server is the contract for
	// "capture is durable" — CRIU dump done, validated, catalog row
	// registered, peer cascade serving. That's what earns CRD
	// Phase=Completed. L2 promote (writer Job + snapshot + rox PVC) is
	// an *optimisation* layer on top — restore-side falls back to peer
	// cascade if the rox PVC isn't ready when a restore pod is admitted
	// (nvsnap-init polls pvc_promote_state). Holding the HTTP response
	// open for snap+clone duration raced nvsnap-server's 10-min
	// httpClient.Timeout (see server.go:81) and caused false-positive
	// Failed CRDs on large checkpoints (e5-mistral 88 GB → ~6 min
	// snapshot wait → race lost ~50% of the time).
	//
	// Catalog row's pvc_promote_state column (written by the Backend's
	// CatalogStateWriter — see l2_catalog_writer.go) is the source of
	// truth for L2 progress, independent of CRD Phase or HTTP response
	// timing.
	if a.l2Backend != nil {
		hostDumpPath, hostPathErr := a.checkpointHostPath(checkpointDir)
		if hostPathErr != nil {
			log.WithError(hostPathErr).Warn("L2 promote skipped: cannot translate checkpoint dir to host path")
		} else {
			runL2PromoteAsync(a.l2Backend, log, l2PromoteInput{
				Hash:        catalog.Hash,
				HostDumpDir: hostDumpPath,
				PodMeta: map[string]string{
					"namespace":     req.Namespace,
					"pod":           req.PodName,
					"image":         containerInfo.Image,
					"engine":        "criu",
					"node":          a.config.NodeName,
					"gpu_type":      catalog.GPUType,
					"gpu_count":     fmt.Sprintf("%d", catalog.GPUCount),
					"gpu_vram_gb":   vramGBFromGPUType(catalog.GPUType),
					"checkpoint_id": checkpointID,
				},
				CapturedAt: time.Now().UTC(),
			})
		}
	}

	// Phase 5d.2: kick off the durable backstop upload to the cluster's
	// nvsnap-blobstore. Async on purpose — the source pod has already
	// resumed by here; the upload is best-effort durability that
	// shouldn't block the API response. background ctx avoids
	// HTTP-request cancellation truncating multi-minute uploads.
	if a.config.BlobStoreURL != "" {
		go func(id string) {
			uploadCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if err := a.UploadCheckpoint(uploadCtx, id); err != nil {
				a.log.WithError(err).WithField("checkpoint_id", id).
					Warn("blob-store upload failed; checkpoint remains node-pinned hostPath")
			}
		}(checkpointID)
	}

	// Phase 2c: publish to the shared filesystem if configured. Same
	// async best-effort pattern as the blob-store upload — the hostPath
	// dump is the source of truth on this node; FSStore publish is
	// what makes the dump available to peers without the network
	// cascade. A failure here just falls the cluster back to the peer
	// path.
	if a.fsStore != nil {
		go func(id, srcDir string) {
			pubCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if err := a.fsStore.publish(pubCtx, id, srcDir); err != nil {
				a.log.WithError(err).WithField("checkpoint_id", id).
					Warn("FSStore publish failed; checkpoint remains node-pinned hostPath")
			}
		}(checkpointID, filepath.Join(a.config.CheckpointDir, checkpointID))
	}

	return result, nil
}

// checkpointHostPath translates a path under CheckpointDir (in-agent-
// container) into the equivalent host path under CheckpointHostDir.
// Required because the capture-write writer Job consumes
// CaptureSource.SrcPath as a host path (joined with /host); the agent's
// in-container view diverges from the host view because the DaemonSet
// mounts a hostPath at a renamed mountpoint.
//
// Returns an error if CheckpointHostDir is unset or if local is not
// rooted under CheckpointDir — both are programmer errors that would
// otherwise surface as a silent "lstat: no such file or directory" in
// the writer Job, which is exactly what the bug we just fixed looked
// like.
func (a *Agent) checkpointHostPath(local string) (string, error) {
	if a.config.CheckpointHostDir == "" {
		return "", fmt.Errorf("CheckpointHostDir is unset; cannot translate %q for writer Job", local)
	}
	rel, err := filepath.Rel(a.config.CheckpointDir, local)
	if err != nil {
		return "", fmt.Errorf("filepath.Rel(%q, %q): %w", a.config.CheckpointDir, local, err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is not under CheckpointDir %q", local, a.config.CheckpointDir)
	}
	return filepath.Join(a.config.CheckpointHostDir, rel), nil
}

// Supports both IPv4 (from /proc/net/tcp) and IPv6 (from /proc/net/tcp6).
func (a *Agent) getPodIP(pid int) string {
	// Use /host/proc when running in a container (agent is containerized)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}

	// Try IPv4 first (more common in most clusters)
	if ip := a.getPodIPFromTable(procBase, pid, "tcp", false); ip != "" {
		return ip
	}

	// Try IPv6 if no IPv4 address found
	if ip := a.getPodIPFromTable(procBase, pid, "tcp6", true); ip != "" {
		return ip
	}

	// Fallback: parse fib_trie for local IPs even if no LISTEN sockets exist.
	if ip := getPodIPFromFibTrie(procBase, pid); ip != "" {
		return ip
	}

	return ""
}

func getPodIPFromFibTrie(procBase string, pid int) string {
	fibPath := fmt.Sprintf("%s/%d/net/fib_trie", procBase, pid)
	data, err := os.ReadFile(fibPath)
	if err != nil {
		return ""
	}

	var lastIP string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if ip := extractIPv4Token(line); ip != "" {
			lastIP = ip
			continue
		}
		if strings.Contains(line, "32 host LOCAL") && lastIP != "" {
			if strings.HasPrefix(lastIP, "127.") || strings.HasPrefix(lastIP, "0.") {
				continue
			}
			return lastIP
		}
	}
	return ""
}

// extractIPv4Token returns the last IPv4-like token in a line.
// fib_trie lines may include prefixes like "|--".
func extractIPv4Token(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	for i := len(fields) - 1; i >= 0; i-- {
		token := fields[i]
		if strings.Count(token, ".") == 3 {
			return token
		}
	}
	return ""
}

// getPodIPFromTable reads IPs from a specific TCP table (tcp or tcp6).
func (a *Agent) getPodIPFromTable(procBase string, pid int, tableName string, isIPv6 bool) string {
	tcpPath := fmt.Sprintf("%s/%d/net/%s", procBase, pid, tableName)
	tcpData, err := os.ReadFile(tcpPath)
	if err != nil {
		a.log.WithError(err).WithField("table", tableName).Debug("Could not read tcp table for pod IP")
		return ""
	}

	// Parse each line looking for a LISTEN socket (state 0A) with non-loopback IP
	for _, line := range strings.Split(string(tcpData), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[3] == "0A" { // TCP_LISTEN state
			// Parse local address (format: XXXXXXXX:PORT for IPv4, 32-char hex:PORT for IPv6)
			localAddr := fields[1]
			parts := strings.Split(localAddr, ":")
			if len(parts) == 2 {
				hexIP := parts[0]
				if isIPv6 {
					// IPv6: 32 hex chars, skip all-zeros and loopback (::1)
					if len(hexIP) == 32 && hexIP != "00000000000000000000000000000000" && hexIP != "00000000000000000000000001000000" {
						ip := parseHexIPv6(hexIP)
						if ip != "" && ip != "::1" {
							return ip
						}
					}
				} else {
					// IPv4: 8 hex chars, skip all-zeros and loopback
					if len(hexIP) == 8 && hexIP != "00000000" && hexIP != "0100007F" {
						ip := parseHexIP(hexIP)
						if ip != "" && !strings.HasPrefix(ip, "127.") {
							return ip
						}
					}
				}
			}
		}
	}

	return ""
}

// parseHexIP converts a hex IPv4 string (little endian) to dotted decimal
func parseHexIP(hexIP string) string {
	if len(hexIP) != 8 {
		return ""
	}
	// Parse as hex, noting it's stored in little-endian order
	b0, _ := strconv.ParseUint(hexIP[6:8], 16, 8)
	b1, _ := strconv.ParseUint(hexIP[4:6], 16, 8)
	b2, _ := strconv.ParseUint(hexIP[2:4], 16, 8)
	b3, _ := strconv.ParseUint(hexIP[0:2], 16, 8)
	return fmt.Sprintf("%d.%d.%d.%d", b0, b1, b2, b3)
}

// parseHexIPv6 converts a hex IPv6 string from /proc/net/tcp6 to standard notation.
// The hex string is 32 characters (16 bytes), stored as 4 little-endian 32-bit words.
func parseHexIPv6(hexIP string) string {
	if len(hexIP) != 32 {
		return ""
	}

	// IPv6 in /proc/net/tcp6 is stored as 4 little-endian 32-bit words
	// Convert each word from little-endian hex to big-endian bytes
	bytes := make([]byte, 16)
	for i := 0; i < 4; i++ {
		word := hexIP[i*8 : (i+1)*8]
		// Each 32-bit word is stored little-endian, so reverse byte order within word
		for j := 0; j < 4; j++ {
			val, _ := strconv.ParseUint(word[(3-j)*2:(3-j)*2+2], 16, 8)
			bytes[i*4+j] = byte(val)
		}
	}

	// Format as IPv6 address
	ip := net.IP(bytes)
	return ip.String()
}

// sendQuiesceSignal sends SIGUSR1 to the container process to trigger
// quiescence — IF the libnvsnap_intercept.so library is mapped into that
// process. The library, when present, installs a SIGUSR1 handler that:
//   - Drains all io_uring instances
//   - Prepares libuv loops for reinit after restore
//
// If the library is NOT mapped, SIGUSR1's default action is to TERMINATE
// the process (POSIX). The earlier code comment claimed the signal would
// be "ignored" — that was wrong. Sending SIGUSR1 to a non-intercepted
// workload (e.g., NIM, which doesn't have the auto-inject init container
// adding LD_PRELOAD) immediately kills it. Confirmed on GCP-H100-a
// 2026-06-03: NIM died on SIGUSR1, kubelet restarted it, NVCA fired a
// fresh checkpoint, repeat — a tight crash loop driven entirely by this
// signal.
//
// Gate the signal on intercept-presence. Non-intercepted workloads can
// still be checkpointed — CRIU + cgroup freezer handle the core stop;
// the SIGUSR1 path is only needed for io_uring/libuv state that the
// intercept library knows how to drain.
//
// The companion path sendQuiesceSignalToInterceptProcs (this file,
// ~line 1206) iterates ALL container processes and only signals those
// with the lib mapped — that path is safe by construction. This
// toplevel signal to the container's main PID is the legacy path that
// needs the same guard.
func sendQuiesceSignal(pid int, log *logrus.Entry) {
	if !processHasInterceptMapped(pid) {
		log.WithField("pid", pid).Info("Skipping SIGUSR1 quiesce — libnvsnap_intercept.so not mapped (workload has no io_uring/libuv drain handler; default SIGUSR1 would terminate)")
		return
	}
	// Send SIGUSR1 to trigger quiescence
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		log.WithError(err).Warn("Failed to send SIGUSR1 quiesce signal")
		return
	}
	log.WithField("pid", pid).Info("Sent SIGUSR1 quiesce signal")

	// Wait for quiescence to complete
	// In a more sophisticated implementation, we'd use a pipe or shared memory
	// for acknowledgment. For now, just wait a reasonable time.
	quiesceTimeout := 2 * time.Second
	time.Sleep(quiesceTimeout)
	log.Info("Quiescence period complete")

	// Resume after quiesce window to avoid leaving threads paused.
	sendResumeSignal(pid, log)
}

// processHasInterceptMapped checks whether libnvsnap_intercept.so is mapped
// into the given host-PID's address space — i.e., whether the process
// has the LD_PRELOAD-injected intercept library loaded. Used as a guard
// before sending SIGUSR1 (the library installs the SIGUSR1 handler;
// without it, default action is terminate).
//
// Returns false on any error reading /proc/PID/maps (process gone,
// permission denied, etc.). False-on-error is the safe default for
// the gate's purpose: if we can't confirm the lib is mapped, don't
// send a signal that could kill the workload.
func processHasInterceptMapped(hostPid int) bool {
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/maps", procBase, hostPid))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "libnvsnap_intercept.so")
}

// sendQuiesceResumeToInterceptProcs sends SIGUSR2 to all intercept-mapped processes
// to release their quiesce spin-loops. Without this, quiesce worker threads spin forever.
func sendQuiesceResumeToInterceptProcs(containerPid int, log *logrus.Entry) {
	procs := listContainerProcs(containerPid, log)
	sent := 0
	for _, proc := range procs {
		if proc.HostPID == containerPid {
			continue
		}
		if !hasInterceptMapped(containerPid, proc.ContainerPID) {
			continue
		}
		sendResumeSignal(proc.HostPID, log)
		sent++
	}
	log.WithField("count", sent).Info("Sent SIGUSR2 resume to all intercept processes")
}

// sendResumeSignal sends SIGUSR2 to the container process to resume after checkpoint.
// This is used in leave-running mode after CRIU dump completes.
func sendResumeSignal(pid int, log *logrus.Entry) {
	if err := syscall.Kill(pid, syscall.SIGUSR2); err != nil {
		log.WithError(err).Warn("Failed to send SIGUSR2 resume signal")
		return
	}
	log.WithField("pid", pid).Info("Sent SIGUSR2 resume signal")
}

// getPipeIDs reads the pipe IDs for stdout (fd 1) and stderr (fd 2) of a process.
// These are needed for CRIU's InheritFd option to replace broken pipes on restore.
// Returns pipe IDs in format "pipe:[12345]" as expected by CRIU (with colon).
func getPipeIDs(pid int, log *logrus.Entry) (stdout, stderr string) {
	// Use /host/proc when running in a container (agent is containerized)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}

	// Read stdout (fd 1)
	stdoutLink, err := os.Readlink(fmt.Sprintf("%s/%d/fd/1", procBase, pid))
	if err != nil {
		log.WithError(err).Debug("Could not read stdout fd link")
	} else if strings.HasPrefix(stdoutLink, "pipe:[") {
		// Use as-is: "pipe:[12345]" - CRIU expects this exact format
		stdout = stdoutLink
		log.WithField("stdout_pipe", stdout).Debug("Captured stdout pipe ID")
	}

	// Read stderr (fd 2)
	stderrLink, err := os.Readlink(fmt.Sprintf("%s/%d/fd/2", procBase, pid))
	if err != nil {
		log.WithError(err).Debug("Could not read stderr fd link")
	} else if strings.HasPrefix(stderrLink, "pipe:[") {
		// Use as-is: "pipe:[12346]" - CRIU expects this exact format
		stderr = stderrLink
		log.WithField("stderr_pipe", stderr).Debug("Captured stderr pipe ID")
	}

	return stdout, stderr
}

func isTruthyEnv(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// ValidateCheckpoint is the post-dump sanity check that a CRIU dump
// directory contains all the files a restore-side will need. Exported
// so the writer Job (cmd/agent capture-write) can run it before exiting,
// failing fast inside the Job rather than letting a half-baked artifact
// reach the per-capture PVC.
//
// Required files cover the always-present CRIU image set + metadata.json
// (written by agent staging, not CRIU). On GPU dumps, also check
// stats-dump (CRIU's success marker) and warn if the CUDA plugin was
// not loaded.
func ValidateCheckpoint(checkpointDir string, hasGPU bool, log *logrus.Entry) error {
	return validateCheckpoint(checkpointDir, hasGPU, log)
}

func validateCheckpoint(checkpointDir string, hasGPU bool, log *logrus.Entry) error {
	// Required CRIU files for any checkpoint
	requiredFiles := []string{
		"inventory.img",
		"pstree.img",
		"files.img",
		"metadata.json",
	}

	var missing []string

	// Check required files
	for _, f := range requiredFiles {
		path := filepath.Join(checkpointDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, f)
		}
	}

	// Core images are named core-<pid>.img per dumped task. The legacy
	// engine dumps container init (always pid 1); criu-v2 dumps the
	// workload session, whose leader pid is arbitrary. Require at least
	// one, whatever the pid.
	if cores, _ := filepath.Glob(filepath.Join(checkpointDir, "core-*.img")); len(cores) == 0 {
		missing = append(missing, "core-*.img")
	}

	// GPU validation: verify CRIU dump completed with CUDA plugin active.
	// The CUDA plugin doesn't produce separate output files — it manages GPU
	// state directly via cuda-checkpoint. So we check stats-dump (written on
	// success) and dump.log for evidence the plugin was loaded.
	if hasGPU {
		if _, err := os.Stat(filepath.Join(checkpointDir, "stats-dump")); os.IsNotExist(err) {
			missing = append(missing, "stats-dump (GPU dump completion marker)")
		}
		dumpLog := filepath.Join(checkpointDir, "dump.log")
		if data, err := os.ReadFile(dumpLog); err == nil {
			if !strings.Contains(string(data), "cuda_plugin") {
				log.Warn("GPU checkpoint: CUDA plugin was not loaded during dump — GPU state may not be captured")
			}
		}
	}

	if len(missing) > 0 {
		// Log what we found for debugging
		files, _ := os.ReadDir(checkpointDir)
		var foundFiles []string
		for _, f := range files {
			foundFiles = append(foundFiles, f.Name())
		}
		log.WithFields(logrus.Fields{
			"missing":    missing,
			"found":      foundFiles,
			"checkpoint": checkpointDir,
		}).Error("Checkpoint validation failed - missing required files")

		return fmt.Errorf("missing required checkpoint files: %v", missing)
	}

	log.Info("Checkpoint validation passed - all required files present")
	return nil
}
