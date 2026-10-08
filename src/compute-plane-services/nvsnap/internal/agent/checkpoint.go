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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/hostlibs"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

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
		"LD_LIBRARY_PATH="+hostlibs.DriverDir("/host/run/nvidia/driver")+":/usr/local/nvidia/lib64")
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

// MappedFileEntry describes a memory-mapped file discovered during checkpoint.
type MappedFileEntry struct {
	Path    string
	MapFile string
	RootPid int
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

	// GPUShareFabricSession joins the capture to a multi-node gpushare
	// session driven by another agent (see gpushare_fabric.go): the
	// suspend and resume coordinate with the workload's other pods.
	GPUShareFabricSession string `json:"gpushareFabricSession,omitempty"`
	// GPUShareGroupIndex and GPUShareGroupSize place this pod in its
	// session's group; a group restore maps each checkpoint back to a pod
	// by them.
	GPUShareGroupIndex int `json:"gpushareGroupIndex,omitempty"`
	GPUShareGroupSize  int `json:"gpushareGroupSize,omitempty"`
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
	Compression *CompressionInfo `json:"compression,omitempty"`

	// CapturePath (v1.8+) records which capture engine produced this
	// checkpoint ("criu-v2" for the in-namespace engine; empty for
	// legacy CRIU). Restore dispatches on it: criu-v2 images must be
	// restored in-namespace by the bundled CRIU, not the legacy
	// swrk/ExtMnt path.
	CapturePath string `json:"capturePath,omitempty"`

	// GPUShare is set when the workload ran under libnvsnap_gpushare.so and
	// its GPU state was saved by nvsnap-gpu-suspend (see gpushare.go).
	GPUShare *GPUShareInfo `json:"gpushare,omitempty"`
	// GPUShareGroup is set when the pod was dumped with the other pods of
	// its instance; with SourcePodIP it lets a group restore move the
	// connections between those pods to the new pods' addresses.
	GPUShareGroup *GPUShareGroupInfo `json:"gpushareGroup,omitempty"`

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
	if _, busy := a.capturing.LoadOrStore(containerInfo.ID, struct{}{}); busy {
		return nil, fmt.Errorf("%w: container %s", ErrCaptureInProgress, containerInfo.ID)
	}
	defer a.capturing.Delete(containerInfo.ID)
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
	// Plain multi-GPU CRIU cannot work: cuda-checkpoint cannot restore GPU
	// memory a process imported from another one (NCCL P2P and NVLS, CUDA
	// IPC). Reject it early and point at the rootfs-only path (nvsnap.io/
	// capture label, agent watcher, per-capture PVC).
	// Unless the workload runs under libnvsnap_gpushare.so, which releases
	// and re-creates the memory the GPUs share around the checkpoint.
	if distinctGPUs > 1 && !gpushareLoadedBy(gpuPIDs) {
		return nil, fmt.Errorf("multi-GPU CRIU is unsupported (distinctGPUs=%d, gpuPIDs=%v); use the rootfs-only path: label the source pod nvsnap.io/capture=true and apply a fresh pod with nvsnap.io/restore-from=<hash>", distinctGPUs, gpuPIDs)
	}

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
		"gpuPIDs":      gpuPIDs,
		"distinctGPUs": distinctGPUs,
	}).Info("GPU topology detected")
	_, gpuSpan := tracing.Tracer().Start(ctx, "checkpoint.gpu_state")
	// criu-v2: no pre-dump lock/checkpoint. The bundled cuda_plugin drives
	// cuda-checkpoint for every GPU process during the in-namespace dump.
	log.WithField("gpuPIDs", gpuPIDs).Info("criu-v2: GPU state handled by cuda_plugin during in-ns dump")
	gpuSpan.End()

	// Step 4.5: Copy all runtime-generated mapped files BEFORE CRIU dump
	// (Process must be alive to read /proc/<pid>/maps)
	// Step 4.7: Get pod IP for stable network identity (MUST be before CRIU dump)
	// After CRIU dump, the process may be gone and /proc/<pid> won't exist.
	podIP := a.getPodIP(int(containerInfo.PID))
	if req.GPUShareFabricSession != "" && podIP == "" {
		// A group restore maps each pod's old address to its new one; a
		// member without one would leave its peers' connections unmapped.
		return nil, fmt.Errorf("gpushare: fabric session %s: cannot determine the pod IP of %s/%s", req.GPUShareFabricSession, req.Namespace, req.PodName)
	}
	if podIP != "" {
		log.WithField("podIP", podIP).Info("Captured pod IP for restore compatibility")
	} else {
		log.Warn("Could not determine pod IP - loopback alias won't be set on restore")
	}

	// Step 4.8: Capture pipe IDs for stdout/stderr (MUST be before CRIU dump)
	// These are needed for InheritFd during restore to replace broken pipes
	// Step 4.9: Capture temp directories and files as late as possible
	// (some frameworks write temp files after startup)
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
	// criu-v2: in-namespace dump (bundle staged into the container,
	// criu exec'd via nsenter). See checkpoint_v2.go. Post-dump steps
	// (rootfs-diff mirror, metadata, upload) are shared with the
	// legacy path — the artifact contract is identical.
	_, criuSpan := tracing.Tracer().Start(ctx, "checkpoint.criu_dump")
	criuSpan.SetAttributes(attribute.String("nvsnap.criu.mode", "v2-inns"))
	gpushareInfo, err := a.dumpV2(ctx, containerInfo, checkpointDir, sourceUpperdir, gpuPIDs, req.LeaveRunning, req.GPUShareFabricSession, log)
	if err != nil {
		criuSpan.RecordError(err)
		criuSpan.SetStatus(codes.Error, "CRIU dump failed (criu-v2)")
		criuSpan.End()
		return nil, fmt.Errorf("CRIU dump failed (criu-v2): %w", err)
	}
	criuSpan.End()

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
	// Step 9: Build restore hints
	// Combine skipped socket fds into a single list for restore
	var skipFds []int
	skipFds = append(skipFds, skippedResources.UnixSocketFds...)
	skipFds = append(skipFds, skippedResources.InetSocketFds...)

	restoreHints := &RestoreHints{
		SkipFds:            skipFds,
		RestoreMappedFiles: false,
		CUDARestoreNeeded:  len(gpuPIDs) > 0,
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
		GPUShareGroup:  gpushareGroupInfo(req),
		Skipped:        skippedResources,
		CUDA: &CUDACheckpointInfo{
			// criu-v2: cuda_plugin locks, checkpoints and (leave-running) resumes
			// every GPU process during the in-namespace dump.
			Enabled:           len(gpuPIDs) > 0,
			GPUPID:            gpuPID,
			LockSuccess:       len(gpuPIDs) > 0,
			CheckpointSuccess: len(gpuPIDs) > 0,
			RestoreSuccess:    req.LeaveRunning && len(gpuPIDs) > 0,
			UnlockSuccess:     true,
		},
		CRIUOptions: &CRIUOptionsUsed{
			LeaveRunning:   req.LeaveRunning,
			TCPEstablished: true, // criu-v2 always dumps with --tcp-established; the full argv is in dump.log
		},
		Hints:        restoreHints,
		ReplayMounts: replayMounts,
		// Deprecated but kept for compatibility
		GPUPID: gpuPID,
	}
	metadata.CapturePath = CapturePathCRIUV2
	metadata.GPUShare = gpushareInfo

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
		if err := validateCheckpoint(checkpointDir, false /* GPU images are validated by cuda_plugin during the in-ns dump */, log); err != nil {
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
			req.ContainerName, containerInfo.Image, checkpointSize, duration, len(gpuPIDs) > 0, catalog); err != nil {
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

	// Phase 2c: publish to the shared filesystem if configured. Async
	// and best-effort — the hostPath
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

// CompressionInfo and CompressedFile describe the compression summary the
// retired capture streamer wrote into metadata.json. Kept so older
// checkpoints still parse; criu-v2 never sets them.
type CompressionInfo struct {
	Algorithm           string                    `json:"algorithm"`
	Level               int                       `json:"level"`
	Files               map[string]CompressedFile `json:"files"`
	TotalOriginalSize   int64                     `json:"totalOriginalSize"`
	TotalCompressedSize int64                     `json:"totalCompressedSize"`
	Ratio               float64                   `json:"ratio"`
}

// CompressedFile is one per-file entry of CompressionInfo.
type CompressedFile struct {
	OriginalSize   int64 `json:"originalSize"`
	CompressedSize int64 `json:"compressedSize"`
}
