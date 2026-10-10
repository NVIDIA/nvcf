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
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// precreateUnixSocketDirs parses the checkpoint's files.img, extracts every
// path-bound unix socket's bind path, and mkdir's the parent directory
// inside the placeholder's mount namespace. Without this, CRIU's unix
// socket restore can succeed (the fd table is reconstructed), but the
// workload's libraries (zmq, framework IPC) re-resolve those paths at
// runtime and fail with "No such file or directory" because the parent
// dir lives in a per-container tmpfs that the placeholder has fresh.
//
// Generic — workload-agnostic. Any unix socket bound to a real path in
// the source's tmpfs gets its parent dir recreated. Abstract sockets
// (path begins with NUL byte = "=00...") are skipped: they don't need
// a filesystem entry.
func precreateUnixSocketDirs(filesImg string, placeholderPID int, log *logrus.Entry) {
	cmd := exec.Command("crit", "decode", "-i", filesImg, "--pretty")
	out, err := cmd.Output()
	if err != nil {
		log.WithError(err).Debug("crit decode files.img failed; skipping unix-socket dir precreate")
		return
	}
	var doc struct {
		Entries []struct {
			Type string `json:"type"`
			Usk  struct {
				Name string `json:"name"`
			} `json:"usk"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		log.WithError(err).Debug("crit decode JSON parse failed")
		return
	}
	dirs := make(map[string]struct{})
	for _, e := range doc.Entries {
		if e.Type != "UNIXSK" {
			continue
		}
		name := e.Usk.Name
		// Skip abstract sockets (encoded as =00... in CRIU output).
		if name == "" || strings.HasPrefix(name, "=00") || !strings.HasPrefix(name, "/") {
			continue
		}
		// Strip CRIU's null-byte trailer encoding (=00).
		if i := strings.Index(name, "=00"); i >= 0 {
			name = name[:i]
		}
		parent := filepath.Dir(name)
		if parent == "/" || parent == "." {
			continue
		}
		dirs[parent] = struct{}{}
	}
	if len(dirs) == 0 {
		return
	}
	mntnsPath := fmt.Sprintf("/proc/%d/ns/mnt", placeholderPID)
	created := 0
	for d := range dirs {
		mk := exec.Command("nsenter", "--mount="+mntnsPath, "--", "mkdir", "-p", d) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		if err := mk.Run(); err != nil {
			log.WithError(err).WithField("dir", d).Warn("Failed to mkdir unix-socket parent dir in placeholder")
			continue
		}
		created++
	}
	log.WithField("dirs", created).Info("Pre-created unix-socket parent directories in placeholder mntns")
}

// addSourcePodIPToPlaceholderLo adds the dump-time pod IP as a /32 (or /128
// for IPv6) alias on the placeholder's loopback interface. CRIU's TCP
// restore re-binds sockets to the addresses recorded at dump time;
// without this alias, bind(2) returns EADDRNOTAVAIL because the placeholder
// got a different pod IP from the K8s CNI. Loopback (rather than the
// pod's main interface) avoids ARP/route conflicts; the egress path is
// unaffected because non-local packets won't use a /32 lo address as
// source.
func addSourcePodIPToPlaceholderLo(placeholderPID int, podIP string) error {
	if podIP == "" {
		return nil
	}
	ip := net.ParseIP(podIP)
	if ip == nil {
		return fmt.Errorf("parse source pod IP %q", podIP)
	}
	prefix := "32"
	if ip.To4() == nil {
		prefix = "128"
	}
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", placeholderPID)
	cmd := exec.Command("nsenter", "--net="+netnsPath, "--", //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		"ip", "addr", "add", podIP+"/"+prefix, "dev", "lo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		// Idempotent: already-added is fine if a previous restore set it up.
		if !strings.Contains(s, "File exists") {
			return fmt.Errorf("ip addr add %s/%s dev lo (netns of %d): %w (%s)",
				podIP, prefix, placeholderPID, err, s)
		}
	}
	return nil
}

// RestoreRequest is the request to restore a container from checkpoint
type RestoreRequest struct {
	CheckpointID           string `json:"checkpointId"`
	CheckpointPath         string `json:"checkpointPath,omitempty"`
	NewPodName             string `json:"newPodName,omitempty"`
	PlaceholderContainerID string `json:"placeholderContainerId,omitempty"` // Container to restore into
	PlaceholderPodName     string `json:"placeholderPodName,omitempty"`     // Pod name of placeholder
	PlaceholderNamespace   string `json:"placeholderNamespace,omitempty"`   // Namespace of placeholder

	// GPUShareFabricSession joins the restore to a group restore driven by
	// another agent (see gpushare_fabric.go). InetAddrMap ("OLD=NEW,...",
	// every pod of the instance) moves the connections between the pods
	// to their new addresses; the restore then holds them locked until the
	// driver reports every pod restored.
	GPUShareFabricSession string `json:"gpushareFabricSession,omitempty"`
	InetAddrMap           string `json:"inetAddrMap,omitempty"`

	// PlaceholderContainerName picks the container in a multi-container
	// placeholder pod; empty takes the first one.
	PlaceholderContainerName string `json:"placeholderContainerName,omitempty"`
	// ReservePIDs raises the placeholder's next pid above the dumped range
	// before the restore. Needed for a placeholder the webhook made from
	// the workload's own pod: it is not privileged, so it cannot do it.
	ReservePIDs bool `json:"reservePids,omitempty"`
}

// RestoreResult is the result of a restore operation
type RestoreResult struct {
	NewContainerID string    `json:"newContainerId"`
	NewPodName     string    `json:"newPodName"`
	RestoredPID    uint32    `json:"restoredPid"`
	GPUPID         int       `json:"gpuPid"`
	Duration       float64   `json:"durationSeconds"`
	Timestamp      time.Time `json:"timestamp"`
}

// PlaceholderManifestRequest returns a pod manifest for restoring from a checkpoint
type PlaceholderManifestRequest struct {
	CheckpointID   string `json:"checkpointId"`
	TargetPodName  string `json:"targetPodName"`
	TargetNodeName string `json:"targetNodeName,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
}

// GeneratePlaceholderManifest generates a K8s pod manifest for restoring
func (a *Agent) GeneratePlaceholderManifest(ctx context.Context, req PlaceholderManifestRequest) (string, error) {
	// Phase 5d.1: cascade-fetch if metadata.json isn't local. Without
	// this, cross-node restore breaks at metadata read because the
	// dump dir was only ever materialized on the capture-source node.
	if a.config.CatalogURL != "" {
		if err := a.EnsureLocal(ctx, req.CheckpointID); err != nil {
			return "", fmt.Errorf("ensure-local cascade: %w", err)
		}
	}

	// Load checkpoint metadata
	checkpointDir := filepath.Join(a.config.CheckpointDir, req.CheckpointID)
	metadataPath := filepath.Join(checkpointDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", fmt.Errorf("failed to read checkpoint metadata: %w", err)
	}

	var metadata CheckpointMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return "", fmt.Errorf("failed to parse checkpoint metadata: %w", err)
	}

	namespace := req.Namespace
	if namespace == "" {
		namespace = metadata.PodNamespace
	}

	podName := req.TargetPodName
	if podName == "" {
		podName = fmt.Sprintf("%s-restored", metadata.PodName)
	}

	targetNode := req.TargetNodeName
	if targetNode == "" {
		targetNode = metadata.NodeName
	}

	// Use the ORIGINAL container image - no special placeholder needed
	// The placeholder runs a bash reaper; the agent restores into it on POST /v1/restore
	containerImage := metadata.ContainerImage

	gpus := 1
	if metadata.GPUShare != nil {
		gpus = gpushareGPUCount(filepath.Join(checkpointDir, GPUShareCheckpointSubdir, gpushareGPUMapFile))
	}
	gsMounts, gsVolumes := a.gpushareRestoreMounts(&metadata, req.CheckpointID)

	// criu-v2 placeholder: same image as the source (CRIU's path-based file
	// checks resolve against an identical rootfs), bash pid1 reaps orphans,
	// the pod keeps its own fresh pid namespace and bumps ns_last_pid so the
	// dumped PIDs are free, and the node checkpoint dir is mounted at
	// /checkpoints. The caller waits for Running and then POSTs /v1/restore
	// with the pod name; nothing inside the pod drives the restore.
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: restored
    nvsnap.io/checkpoint-id: %s
    nvsnap.io/original-pod: %s
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  nodeSelector:
    kubernetes.io/hostname: %s
  containers:
    - name: restored
      image: %s
      imagePullPolicy: IfNotPresent
      command: ["/bin/bash", "-lc"]
      args:
        - |
          echo 100000 > /proc/sys/kernel/ns_last_pid || echo "(ns_last_pid bump failed; restore may collide)"
          while true; do sleep 30; done
      securityContext:
        privileged: true
      env:
        - name: CHECKPOINT_ID
          value: %q
      volumeMounts:
        - name: checkpoints
          mountPath: /checkpoints
        - name: dev-shm
          mountPath: /dev/shm%s
      resources:
        limits:
          nvidia.com/gpu: %d
  volumes:
    - name: checkpoints
      hostPath:
        path: %s
        type: Directory
    - name: dev-shm
      emptyDir:
        medium: Memory%s
`,
		podName,
		namespace,
		req.CheckpointID,
		metadata.PodName,
		targetNode,
		containerImage,
		req.CheckpointID,
		gsMounts,
		gpus,
		a.checkpointHostRoot(),
		gsVolumes,
	)

	return manifest, nil
}

// Restore restores a checkpointed process using CRIU directly.
// This uses the host's CRIU binary to restore the process.
func (a *Agent) Restore(ctx context.Context, req RestoreRequest) (*RestoreResult, error) {
	// The ID is joined onto CheckpointDir below and reaches os.Stat,
	// os.ReadFile and os.WriteFile from there. Reject anything that is not
	// a single path component before it becomes a path.
	if err := validPathSegment("checkpointId", req.CheckpointID); err != nil {
		return nil, err
	}

	ctx, span := tracing.Tracer().Start(ctx, "restore.full")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.checkpoint_id", req.CheckpointID),
		attribute.String("nvsnap.placeholder_namespace", req.PlaceholderNamespace),
		attribute.String("nvsnap.placeholder_pod", req.PlaceholderPodName),
	)

	startTime := time.Now()
	log := a.log.WithFields(logrus.Fields{
		"checkpointId": req.CheckpointID,
	})
	log.Info("Starting restore (direct CRIU mode)")

	// Phase 5d.1: ensure the dump dir exists locally before touching
	// metadata.json. If this is the capture-source node, EnsureLocal
	// short-circuits (inventory.img already there); otherwise it
	// cascades through peer agents → blob store. Skipped when
	// CatalogURL is unset (legacy single-node mode).
	if a.config.CatalogURL != "" {
		ensureStart := time.Now()
		if err := a.EnsureLocal(ctx, req.CheckpointID); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "ensure-local cascade failed")
			return nil, fmt.Errorf("%w: ensure-local cascade failed: %w", errCheckpointUnavailable, err)
		}
		log.WithField("ensureLocalElapsed", time.Since(ensureStart).String()).
			Info("Checkpoint materialized locally (same-node or cascade)")
	}

	// Step 1: Load checkpoint metadata
	checkpointDir := filepath.Join(a.config.CheckpointDir, req.CheckpointID)

	metadataPath := filepath.Join(checkpointDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint metadata: %w", err)
	}

	var metadata CheckpointMetadata
	if umErr := json.Unmarshal(metadataBytes, &metadata); umErr != nil {
		return nil, fmt.Errorf("failed to parse checkpoint metadata: %w", umErr)
	}

	log = log.WithFields(logrus.Fields{
		"originalPod":   metadata.PodName,
		"originalPID":   metadata.ContainerPID,
		"originalImage": metadata.ContainerImage,
	})
	log.Info("Loaded checkpoint metadata")

	// Step 2: Find placeholder container if specified
	var placeholderInfo *containerd.ContainerInfo
	joinNs := make(map[string]string)
	placeholderMntnsPID := 0

	if req.PlaceholderPodName != "" && req.PlaceholderNamespace != "" {
		log.Info("Looking for placeholder container to restore into")
		var findErr error
		placeholderInfo, findErr = a.runtime.FindContainerByPod(ctx, req.PlaceholderNamespace, req.PlaceholderPodName, req.PlaceholderContainerName)
		if findErr != nil {
			return nil, fmt.Errorf("failed to find placeholder container: %w", findErr)
		}
		log = log.WithFields(logrus.Fields{
			"placeholderId":  placeholderInfo.ID[:12],
			"placeholderPid": placeholderInfo.PID,
		})
		log.Info("Found placeholder container")
		if req.ReservePIDs {
			if out, err := exec.CommandContext(ctx, "nsenter", criuReservePIDArgs(int(placeholderInfo.PID))...).CombinedOutput(); err != nil { //nolint:gosec // fixed command; the pid comes from the runtime
				return nil, fmt.Errorf("reserve the pid range: %w (%s)", err, strings.TrimSpace(string(out)))
			}
		}

		// Run CRIU inside the placeholder's mount namespace. The helper
		// subcommand grafts the criu bundle + checkpoints dir into the
		// placeholder's mntns via open_tree + setns + move_mount, then execs
		// CRIU there. This gives CRIU a correct view of the container:
		//   - placeholder's overlay rootfs (python3.12, image content)
		//   - nvidia-CDI bind submounts (libcuda.so, firmware, nvidia-smi)
		//   - pod volume mounts (nvsnap-lib emptyDir, secrets, etc.)
		// all via --root=/ inside the placeholder's own mntns.
		//
		// Previous attempts failed because:
		//   - `mount --rbind /proc/PID/root` via util-linux readlinks to "/"
		//     and binds the AGENT's rootfs (python3.10), not the placeholder's.
		//   - Raw mount(2) with magic-link source is rejected with EINVAL
		//     (cross-mntns bind restriction).
		//   - Overlay merged path is only visible in the host mntns and
		//     doesn't include container submounts.
		placeholderMntnsPID = int(placeholderInfo.PID)

		// Join IPC/UTS via CRIU's --join-ns. Mnt is handled by the helper's
		// setns before CRIU runs. Net is handled via --inherit-fd (below).
		joinNs["ipc"] = fmt.Sprintf("/proc/%d/ns/ipc", placeholderMntnsPID)
		joinNs["uts"] = fmt.Sprintf("/proc/%d/ns/uts", placeholderMntnsPID)

		// Add the dump-time pod IP as a /32 alias on the placeholder's
		// loopback so CRIU's TCP socket restore can re-bind to it.
		// Without this, bind(2) returns EADDRNOTAVAIL — the placeholder
		// was assigned a different pod IP by the K8s CNI.
		if metadata.SourcePodIP != "" {
			if aerr := addSourcePodIPToPlaceholderLo(placeholderMntnsPID, metadata.SourcePodIP); aerr != nil {
				log.WithError(aerr).Warn("Failed to alias source pod IP on placeholder lo; TCP restore may fail")
			} else {
				log.WithField("sourcePodIP", metadata.SourcePodIP).Info("Aliased source pod IP on placeholder lo")
			}
		}

		// Pre-create parent dirs of every path-bound unix socket the dump
		// captured. Their tmpfs (/var/run, /tmp) is inside the container's
		// mntns, not the overlay upperdir, so the mirror doesn't carry
		// them. Without this, the restored workload's IPC libraries
		// fail re-resolving socket paths at runtime ("No such file or
		// directory" in zmq poll etc.).
		filesImg := filepath.Join(checkpointDir, "files.img")
		if _, fErr := os.Stat(filesImg); fErr == nil {
			precreateUnixSocketDirs(filesImg, placeholderMntnsPID, log)
		}

		// Replay the source container's overlay diff into the placeholder's
		// mount namespace. This is the workload-agnostic counterpart to the
		// snapshot taken at checkpoint time: every file the source wrote at
		// runtime — patched libraries copied by the startup script, JIT
		// artefacts, framework caches, IPC directories — gets the matching
		// path + size on the placeholder before CRIU's path-based stat
		// checks run.
		//
		// We must write through the placeholder's overlay merged view
		// (nsenter into its mntns and untar at /), not directly to its
		// upperdir: overlayfs caches the merged-view dentries at mount
		// time and doesn't see external upperdir mutations.
		diffSrc := filepath.Join(checkpointDir, "rootfs-diff")
		if _, dErr := os.Stat(diffSrc); dErr == nil {
			// Read the placeholder's current mountinfo: every non-"/"
			// entry is a runtime/CDI/kubelet bind that's already
			// established (often read-only) on the destination. tar's
			// extract must skip those paths, otherwise it hits EROFS
			// on the bind targets.
			plMounts, _ := mountinfo.NonRootMountPoints(placeholderMntnsPID)
			if mErr := mirrorIntoMntns(diffSrc, placeholderMntnsPID, plMounts, log); mErr != nil {
				log.WithError(mErr).Warn("Failed to replay rootfs-diff into placeholder mntns; restore may fail")
			}
		}

		// Step 2.5b: Replay captured non-rootfs writable mounts (default
		// allowlist: /dev/shm) into the placeholder's mntns. CRIU's
		// path-based file restoration walks /dev/shm/<file> at restore
		// time; without this, multi-GPU NCCL/PSM segments captured at
		// checkpoint can't be reopened. See
		// docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md.
		//
		// Per-mount failures are logged and skipped — CRIU may still
		// succeed if the workload didn't depend on the missing content
		// (Q6 in design). Rootfs mirror failure above is fatal; this is
		// not.
		var replayed, replayFailed int
		for _, rm := range metadata.ReplayMounts {
			tarAbs := filepath.Join(checkpointDir, rm.Tarball)
			if rErr := untarIntoMntns(placeholderMntnsPID, rm.Path, tarAbs, log); rErr != nil {
				log.WithError(rErr).WithField("mp", rm.Path).
					Warn("Failed to replay mount into placeholder; CRIU may fail for this mount")
				replayFailed++
				continue
			}
			replayed++
		}
		if replayed > 0 || replayFailed > 0 {
			log.WithFields(logrus.Fields{
				"replayed": replayed,
				"failed":   replayFailed,
				"total":    len(metadata.ReplayMounts),
			}).Info("Cross-pod mount replay complete")
		}
	}

	// criu-v2 checkpoints restore in-namespace with the bundled CRIU —
	// none of the legacy ExtMnt/JoinNs/inherit-fd choreography below
	// applies. The shared placeholder prep above (pod-IP alias, unix-sk
	// dirs, rootfs-diff + mount replay) has already run.
	if metadata.CapturePath == CapturePathCRIUV2 {
		return a.restoreV2(ctx, &metadata, checkpointDir, placeholderInfo, startTime, restoreV2Group{
			Session: req.GPUShareFabricSession, InetAddrMap: req.InetAddrMap,
			Namespace: req.PlaceholderNamespace, Pod: req.PlaceholderPodName,
		}, log)
	}

	// Every checkpoint this agent writes is criu-v2 (Checkpoint stamps
	// metadata.CapturePath). Images from the retired go-criu engine are not
	// restorable by this build.
	return nil, fmt.Errorf("checkpoint %s has capturePath %q; only %q is supported", req.CheckpointID, metadata.CapturePath, CapturePathCRIUV2)
}
