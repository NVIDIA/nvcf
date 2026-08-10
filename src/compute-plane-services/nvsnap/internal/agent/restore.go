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
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu"
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

// streamReadEnd drains the read end of a pipe (CRIU restored the write end
// to the workload's stdout/stderr) into a log file. Runs until EOF, which
// happens when the restored process closes its stdio. Best-effort — errors
// are logged but don't propagate.
func streamReadEnd(r, dst *os.File, log *logrus.Entry) {
	defer func() { _ = r.Close() }()
	if dst == nil {
		// Drain into the void to keep the pipe from filling and blocking
		// the writer. Better than dropping the read end.
		dst, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if dst != nil {
			defer func() { _ = dst.Close() }()
		}
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 && dst != nil {
			_, _ = dst.Write(buf[:n])
		}
		if err != nil {
			if log != nil {
				log.WithError(err).Debug("Restored stdio stream ended")
			}
			return
		}
	}
}

// addSourcePodIPToPlaceholderLo adds the dump-time pod IP as a /32 (or /128
// for IPv6) alias on the placeholder's loopback interface. CRIU's TCP
// restore re-binds sockets to the addresses recorded at dump time;
// without this alias, bind(2) returns EADDRNOTAVAIL because the placeholder
// got a different pod IP from the K8s CNI. Loopback (rather than the
// pod's main interface) avoids ARP/route conflicts; the egress path is
// unaffected because non-local packets won't use a /32 lo address as
// source. Mirrors restore-entrypoint's setupLoopbackAlias.
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

// TriggerRestoreRequest is the request to trigger restore in a placeholder pod
type TriggerRestoreRequest struct {
	CheckpointID           string `json:"checkpointId"`
	PlaceholderContainerID string `json:"placeholderContainerId"`
}

// TriggerRestoreResult is the result of triggering a restore
type TriggerRestoreResult struct {
	TriggerPath string    `json:"triggerPath"`
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
}

// PlaceholderManifestRequest returns a pod manifest for restoring from a checkpoint
type PlaceholderManifestRequest struct {
	CheckpointID   string `json:"checkpointId"`
	TargetPodName  string `json:"targetPodName"`
	TargetNodeName string `json:"targetNodeName,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
}

// TriggerRestore writes a trigger file for a placeholder pod to start restoring
func (a *Agent) TriggerRestore(ctx context.Context, req TriggerRestoreRequest) (*TriggerRestoreResult, error) {
	if err := validPathSegment("checkpointId", req.CheckpointID); err != nil {
		return nil, err
	}

	log := a.log.WithFields(logrus.Fields{
		"checkpointId":  req.CheckpointID,
		"placeholderId": req.PlaceholderContainerID,
	})
	log.Info("Triggering restore in placeholder pod")

	// Phase 5d.1: cascade-fetch the dump if it's not local. Same
	// invariant as Restore() — the placeholder reads from
	// CheckpointDir/<id>/, so the dir must exist first.
	if a.config.CatalogURL != "" {
		if err := a.EnsureLocal(ctx, req.CheckpointID); err != nil {
			return nil, fmt.Errorf("ensure-local cascade: %w", err)
		}
	}

	// Find the checkpoint directory
	checkpointDir := filepath.Join(a.config.CheckpointDir, req.CheckpointID)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("checkpoint not found: %s", req.CheckpointID)
	}

	// Write trigger file with checkpoint path
	// The placeholder container's restore-entrypoint will detect this
	triggerPath := filepath.Join(checkpointDir, "restore-trigger")
	if err := os.WriteFile(triggerPath, []byte(checkpointDir), 0o644); err != nil {
		return nil, fmt.Errorf("failed to write trigger file: %w", err)
	}

	log.WithField("triggerPath", triggerPath).Info("Restore trigger written")

	return &TriggerRestoreResult{
		TriggerPath: triggerPath,
		Status:      "triggered",
		Timestamp:   time.Now(),
	}, nil
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
	// We override the command to run restore-entrypoint (mounted from host)
	containerImage := metadata.ContainerImage

	// OpenTelemetry plumbing — propagated to restore-entrypoint via env
	// vars so its spans nest under the agent's restore.full span (when
	// active) and ship to the same Jaeger collector. Empty values are
	// fine — restore-entrypoint's tracing.Init is a no-op when the
	// endpoint is unset.
	otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	traceparent, tracestate := tracing.InjectTraceparent(ctx)
	otelEnvBlock := ""
	if otlpEndpoint != "" {
		otelEnvBlock += fmt.Sprintf(`
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: %q`, otlpEndpoint)
	}
	if traceparent != "" {
		otelEnvBlock += fmt.Sprintf(`
        - name: OTEL_TRACE_PARENT
          value: %q`, traceparent)
	}
	if tracestate != "" {
		otelEnvBlock += fmt.Sprintf(`
        - name: OTEL_TRACE_STATE
          value: %q`, tracestate)
	}

	// Generate manifest - uses original image with overridden entrypoint
	// The NVSNAP bundle is mounted at /nvsnap and contains:
	// - criu-wrapper, criu, cuda-checkpoint, restore-entrypoint
	// - lib/ (shared libraries), plugins/ (CRIU plugins)
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
  nodeSelector:
    kubernetes.io/hostname: %s
  containers:
    - name: restored
      image: %s
      imagePullPolicy: IfNotPresent
      command: ["/nvsnap/restore-entrypoint"]
      securityContext:
        privileged: true
      env:
        - name: PATH
          value: "/nvsnap:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
        - name: CHECKPOINT_PATH
          value: "/checkpoints"
        - name: CHECKPOINT_ID
          value: "%s"
        - name: CRIU_BUNDLE_PATH
          value: "/nvsnap"
        - name: DEBUG
          value: "1"%s
      volumeMounts:
        - name: checkpoints
          mountPath: /checkpoints
        - name: nvsnap-bundle
          mountPath: /nvsnap
          readOnly: true
      resources:
        limits:
          nvidia.com/gpu: 1
  volumes:
    - name: checkpoints
      hostPath:
        path: %s
        type: Directory
    - name: nvsnap-bundle
      hostPath:
        path: /usr/local/nvsnap
        type: Directory
`,
		podName,
		namespace,
		req.CheckpointID,
		metadata.PodName,
		targetNode,
		containerImage,
		req.CheckpointID,
		otelEnvBlock,
		a.config.CheckpointDir,
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
			return nil, fmt.Errorf("ensure-local cascade failed: %w", err)
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
		placeholderInfo, findErr = a.runtime.FindContainerByPod(ctx, req.PlaceholderNamespace, req.PlaceholderPodName, "")
		if findErr != nil {
			return nil, fmt.Errorf("failed to find placeholder container: %w", findErr)
		}
		log = log.WithFields(logrus.Fields{
			"placeholderId":  placeholderInfo.ID[:12],
			"placeholderPid": placeholderInfo.PID,
		})
		log.Info("Found placeholder container")

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
		return a.restoreV2(ctx, &metadata, checkpointDir, placeholderInfo, startTime, log)
	}

	// Step 3: Configure CRIU restore
	log.Info("Restoring process using CRIU")
	pluginDir := resolveCRIUPluginDir(a.config.CRIUPath, log)

	// Build ExtMounts from checkpoint metadata to match the dump's external
	// mount entries. Inside the placeholder's mntns, the restored mount
	// points exist at their original paths — Val == Key.
	extMounts := make([]criu.ExtMountMap, 0, len(metadata.DumpMountPoints))
	for _, mp := range metadata.DumpMountPoints {
		extMounts = append(extMounts, criu.ExtMountMap{Key: mp, Val: mp})
	}
	if len(extMounts) > 0 {
		log.WithField("count", len(extMounts)).Info("Applying ExtMount mappings")
	}

	// RootFS "/": inside the placeholder's mntns, "/" IS the container root.
	// For non-placeholder (legacy) restore, callers can still pass a custom
	// rootfs, but that path is unused for CRI-O placeholder restore.
	rootfsPath := "/"

	criuOpts := criu.RestoreOptions{
		ImagesDir:           checkpointDir,
		RootFS:              rootfsPath,
		ShellJob:            true,
		Detached:            true,
		MntnsCompatMode:     true,
		TcpClose:            false,
		TcpEstablished:      true, // dump used --tcp-established; restore must too
		ManageCgroups:       "ignore",
		JoinNamespace:       joinNs,
		PluginDir:           pluginDir,
		ExtMounts:           extMounts,
		AllowUprobes:        true,
		PlaceholderMntnsPID: placeholderMntnsPID,
	}
	if placeholderMntnsPID > 0 {
		criuOpts.HelperBundlePath = filepath.Dir(a.config.CRIUPath)
		criuOpts.HelperCheckpointRoot = a.config.CheckpointDir
	}

	// Network namespace handling.
	//
	// Dump always records the netns as external (--external net[]:extNetNs)
	// so CRIU skips serializing veth/iptables/etc. state. Restore MUST
	// satisfy that external key, either via --inherit-fd (a real netns fd)
	// or --empty-ns (CRIU creates one). We choose based on whether a
	// placeholder pod is available:
	//
	//  - Placeholder available: inherit its netns fd. The restored process
	//    lives in the placeholder's pod network (normal Kubernetes CNI,
	//    pod IP, SDN). Works for GPU workloads too — CRIU doesn't try to
	//    deserialize veth state because the dump marked it external.
	//  - No placeholder: use --empty-ns net as a fallback. Restored process
	//    has no network, but GPU ops still work. CRIU's --empty-ns also
	//    satisfies the external key.
	if placeholderInfo != nil {
		placeholderPID := int(placeholderInfo.PID)
		criuOpts.InheritFds = append(criuOpts.InheritFds, criu.InheritFd{
			Path: fmt.Sprintf("/proc/%d/ns/net", placeholderPID),
			Key:  "extNetNs",
		})
	} else {
		criuOpts.EmptyNs = []string{"net"}
	}

	// Replace the dump's stdout/stderr pipes with fresh local pipes so the
	// restored workload has live writers after CRIU exits (no SIGPIPE on
	// first stdout/stderr write). The metadata captured at dump time
	// recorded the source pipes' identifiers ("pipe:[<inode>]"); CRIU's
	// --inherit-fd replays our fresh pipe write ends in their place.
	// Drain the read ends in goroutines to a local log file inside the
	// agent's checkpoint dir (so kubectl logs of agent doesn't fill).
	// Mirrors restore-entrypoint::setupInheritedFds + streamPipe.
	var pipeWriters []*os.File
	defer func() {
		for _, w := range pipeWriters {
			_ = w.Close()
		}
	}()
	// Write the streamed restored-workload stdio into the PLACEHOLDER's
	// mntns at /tmp/nvsnap-restored.log so the placeholder's PID 1
	// (`tail -F /tmp/nvsnap-restored.log`) picks it up and forwards via
	// kubelet to `kubectl logs -f <placeholder>`. Without this, prod
	// debugging required nsenter into the placeholder — unworkable when
	// the operator only has Pod-level RBAC. Path is opened through
	// /proc/<placeholderPID>/root so the agent stays in its own mntns.
	streamLogPath := fmt.Sprintf("/proc/%d/root/tmp/nvsnap-restored.log", placeholderMntnsPID)
	if placeholderMntnsPID <= 0 {
		// Legacy non-placeholder restore (shouldn't hit on agent-driven
		// path, but guard anyway). Fall back to checkpoint dir so the
		// log doesn't get lost.
		streamLogPath = filepath.Join(checkpointDir, "restored-stdio.log")
	}
	streamFile, _ := os.OpenFile(streamLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if streamFile != nil {
		defer func() { _ = streamFile.Close() }()
	} else {
		log.WithField("path", streamLogPath).Warn("Could not open restored-stdio log; kubectl logs will be empty")
	}
	addPipe := func(key string) {
		if key == "" {
			return
		}
		r, w, perr := os.Pipe()
		if perr != nil {
			log.WithError(perr).Warn("Failed to create stdio pipe for restored process")
			return
		}
		// Bump kernel pipe buffer to 1 MiB so high-volume bursts (e.g.
		// NCCL_DEBUG=INFO during init) don't block the writer if our
		// reader falls behind briefly. F_SETPIPE_SZ is silently capped by
		// /proc/sys/fs/pipe-max-size; failure is non-fatal.
		_, _ = unix.FcntlInt(w.Fd(), unix.F_SETPIPE_SZ, 1<<20)
		criuOpts.InheritFiles = append(criuOpts.InheritFiles, criu.InheritFile{File: w, Key: key})
		pipeWriters = append(pipeWriters, w)
		go streamReadEnd(r, streamFile, log.WithField("pipeKey", key))
	}
	addPipe(metadata.StdoutPipeID)
	addPipe(metadata.StderrPipeID)

	_, criuSpan := tracing.Tracer().Start(ctx, "restore.criu")
	restoredPID, err := a.criu.Restore(criuOpts)
	if err != nil {
		criuSpan.RecordError(err)
		criuSpan.SetStatus(codes.Error, "CRIU restore failed")
		criuSpan.End()
		span.RecordError(err)
		span.SetStatus(codes.Error, "CRIU restore failed")
		return nil, fmt.Errorf("CRIU restore failed: %w", err)
	}
	criuSpan.SetAttributes(attribute.Int("nvsnap.restored_pid", restoredPID))
	criuSpan.End()
	log = log.WithField("restoredPID", restoredPID)
	log.Info("Process restored successfully")

	// Step 4: Wait for process to initialize
	time.Sleep(1 * time.Second)

	// Step 5: Find GPU process and restore GPU state
	gpuPID := restoredPID
	if metadata.GPUPID > 0 {
		log.Info("Looking for GPU process")
		gpuProcesses, err := a.cuda.FindGPUProcesses(ctx)
		if err == nil {
			for _, pid := range gpuProcesses {
				if pid == restoredPID || pid > int(metadata.ContainerPID) {
					gpuPID = pid
					break
				}
			}
		}

		log = log.WithField("gpuPID", gpuPID)
		// CRIU's cuda_plugin re-establishes the CUDA context and replays
		// GPU memory pages, but it does NOT advance the cuda-checkpoint
		// state machine — every restored GPU process is left in the
		// "checkpointed" state. Subsequent CUDA driver calls hit
		// pthread_rwlock_wrlock on libcuda's user-space rwlocks, which
		// were captured in "held" state at dump (cuda-checkpoint lock
		// pauses CUDA threads but doesn't release these user-space
		// locks before CRIU's image is taken). Result: the first
		// tensor.clone() / cuMemcpyDtoDAsync after restore blocks
		// forever waiting on a lock no live thread will release.
		//
		// vllm-small avoids this empirically because its inference path
		// doesn't trigger contended libcuda rwlocks. sglang's radix
		// cache _split_node calls .clone() during prefill scheduling
		// and hangs deterministically.
		//
		// Generic fix: walk the restored process tree, find every
		// process in cuda-checkpoint state "checkpointed", and run
		// --action restore + --action unlock to advance the state
		// machine to "running" — this releases the held rwlocks.
		// Try restore+unlock on every descendant. Processes without a CUDA
		// context (or already running) return "operation cannot be performed
		// in the present state" — harmless. We can't usefully gate on
		// HasCUDAContext: cuda-checkpoint exposes process state via the
		// `--get-state` flag, not `--action state`. Since failures are
		// expected and benign, just attempt blindly and tally outcomes.
		_, cudaSpan := tracing.Tracer().Start(ctx, "restore.cuda_replay")
		descendants := collectDescendants(restoredPID)
		parallel := cudaParallelism()
		cudaSpan.SetAttributes(
			attribute.Int("nvsnap.descendant_count", len(descendants)),
			attribute.Int("nvsnap.cuda_parallelism", parallel),
		)

		// Parallelize the per-PID cuda-checkpoint --action restore + --action
		// unlock calls. The previous serial loop took O(N × per-PID-time);
		// on a 38-PID NIM that was 6+ minutes (memory entry #176) which is
		// long enough for Riva's internal supervisor to TERM the gRPC
		// backend after its HTTP frontend exhausts its connection-refused
		// patience. Each cuda-checkpoint call is an independent subprocess
		// (no shared state in our code); the only risk is NVIDIA-side
		// driver serialization, which we cap exposure to via the
		// cudaParallelism() bound (default 8, override via
		// NVSNAP_CUDA_PARALLELISM). Errors are independently tolerated:
		// processes without a CUDA context return a benign error string.
		var unlocked, skipped atomic.Int64
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(parallel)
		for _, pid := range descendants {

			g.Go(func() error {
				if err := a.cuda.RestoreAndUnlock(gctx, pid); err != nil {
					skipped.Add(1)
					return nil // tolerated; do not cancel siblings
				}
				unlocked.Add(1)
				return nil
			})
		}
		_ = g.Wait()
		nUnlocked := int(unlocked.Load())
		nSkipped := int(skipped.Load())
		cudaSpan.SetAttributes(
			attribute.Int("nvsnap.cuda_unlocked", nUnlocked),
			attribute.Int("nvsnap.cuda_skipped", nSkipped),
		)
		cudaSpan.End()
		log.WithFields(logrus.Fields{
			"unlocked":    nUnlocked,
			"skipped":     nSkipped,
			"descendants": len(descendants),
			"parallelism": parallel,
		}).Info("GPU state restored + unlocked by cuda_plugin + cuda-checkpoint")
	}

	// Create the post-restore marker file in the restored process's
	// mntns. libnvsnap_intercept and the patched libzmq/libuv check this
	// path to detect "I'm running in a restored process" and trigger
	// their post-restore reinit hooks (zmq epoll re-poll, libuv
	// uv_loop_fork, io_uring re-arm). Without this, the workload's
	// event-loop-bound IO threads operate on stale fd state and crash
	// on first activity ("ZMQError: No such file or directory" on
	// zmq_poll, etc.). Same path legacy restore-entrypoint creates
	// in its CRIU PostRestore notify hook.
	{
		markerDir := fmt.Sprintf("/proc/%d/root/run", restoredPID)
		marker := fmt.Sprintf("/proc/%d/root/run/criu-restored", restoredPID)
		_ = os.MkdirAll(markerDir, 0o755)
		if werr := os.WriteFile(marker, []byte("1"), 0o644); werr != nil {
			log.WithError(werr).Warn("Failed to create /run/criu-restored marker; libnvsnap_intercept reinit will not trigger")
		} else {
			log.Info("Created /run/criu-restored in restored mntns")
		}
		// Legacy fallback path checked by older builds of the intercept lib.
		legacyDir := fmt.Sprintf("/proc/%d/root/var/run/nvsnap", restoredPID)
		legacyMarker := fmt.Sprintf("/proc/%d/root/var/run/nvsnap/.restored", restoredPID)
		_ = os.MkdirAll(legacyDir, 0o755)
		_ = os.WriteFile(legacyMarker, []byte("1"), 0o644)
	}

	// Wake I/O threads stuck in epoll_wait / io_uring_enter on stale
	// kernel state from before the dump. SIGUSR2 -> libnvsnap_intercept's
	// post-restore reinit path (now that the marker is present).
	// Mirrors restore-entrypoint::wakeRestoredThreads.
	go wakeRestoredThreads(restoredPID, log)

	duration := time.Since(startTime).Seconds()
	log.WithField("duration", fmt.Sprintf("%.2fs", duration)).Info("Restore completed")

	return &RestoreResult{
		NewContainerID: fmt.Sprintf("restored-%d", restoredPID),
		NewPodName:     req.NewPodName,
		RestoredPID:    uint32(restoredPID), //nolint:gosec // bounded: restoredPID is a positive PID
		GPUPID:         gpuPID,
		Duration:       duration,
		Timestamp:      time.Now(),
	}, nil
}
