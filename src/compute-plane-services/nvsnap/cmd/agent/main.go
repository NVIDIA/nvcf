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

// Package main implements the nvsnap-agent entrypoint, a DaemonSet/Job
// binary that orchestrates GPU checkpoint and restore on Kubernetes nodes.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/agent"
)

func main() {
	// Subcommand dispatch: the per-capture writer Job invokes the agent
	// binary with one of these names. Both flow through runCaptureWrite,
	// which dispatches per-source by Kind (rootfs → treecopy.Copier;
	// criu → criu dump). The Job container has the agent image so the
	// criu binary + cuda-checkpoint are already on disk.
	//
	// "capture-copy" is the legacy name (v0.17.x rootfs Jobs); kept as
	// an alias so a stale Job in flight during a rolling agent upgrade
	// still works. New code paths use "capture-write".
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "capture-write", "capture-copy":
			log := logrus.New()
			log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				cancel()
			}()
			if err := runCaptureWrite(ctx, log.WithField("subsys", os.Args[1])); err != nil {
				log.WithError(err).Fatalf("%s failed", os.Args[1])
			}
			return
		}
	}

	var config agent.Config

	flag.StringVar(&config.NodeName, "node-name", os.Getenv("NODE_NAME"), "Node name")
	// Phase 5d.1: catalog URL + node IP for peer-fanout cascade.
	flag.StringVar(&config.CatalogURL, "catalog-url", os.Getenv("NVSNAP_CATALOG_URL"),
		"NvSnap-server base URL for peer-fanout catalog lookups (e.g. http://nvsnap-server.nvsnap-system.svc.cluster.local:8080). Empty disables cross-node cascade.")
	flag.StringVar(&config.NodeIP, "node-ip", os.Getenv("HOST_IP"),
		"This agent's reachable address from peers (downward API status.hostIP when hostNetwork:true). Empty disables peer registration.")
	flag.StringVar(&config.BlobStoreURL, "blob-store-url", os.Getenv("NVSNAP_BLOB_STORE_URL"),
		"NvSnap-blobstore base URL for Phase 5d.2 durable backstop (e.g. http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000). Empty disables capture-side upload AND cascade tier-3 fallback.")
	// Cross-cluster replication (docs/design/cross-cluster-replication.md).
	flag.StringVar(&config.Replication.ObjectStore.Provider, "replication-provider", os.Getenv("NVSNAP_REPLICATION_PROVIDER"),
		"Object-store provider for cross-cluster replication: gcs | s3. Empty (default) disables replication. Replication is enabled only when both --replication-provider and --replication-home-bucket are set.")
	flag.StringVar(&config.Replication.ObjectStore.HomeBucket, "replication-home-bucket", os.Getenv("NVSNAP_REPLICATION_HOME_BUCKET"),
		"This cluster's home bucket (bare name, no scheme prefix). On capture commit the tree+manifest are pushed here; POST /v1/replicate/{hash} probes this bucket first. Empty disables replication.")
	var replicationPeerBuckets string
	flag.StringVar(&replicationPeerBuckets, "replication-peer-buckets", os.Getenv("NVSNAP_REPLICATION_PEER_BUCKETS"),
		"Comma-separated peer bucket names probed (in order) on a pull miss against the home bucket. A capture pulled from a peer is cached into the home bucket.")
	flag.DurationVar(&config.Replication.PollInterval, "replication-poll-interval", parseDurationEnv("NVSNAP_REPLICATION_POLL_INTERVAL"),
		"Interval for the cross-cluster auto-pull poller: an elected agent lists the home bucket every interval and pulls every GPU/driver-compatible capture it finds (no manual POST /v1/replicate needed). 0 (default) disables it; a sane value is 60s. Requires --replication-home-bucket.")
	// Phase 2c of the 16-node distribution plan: shared-filesystem
	// backend for clusters with a DFS mounted on every node. When set,
	// captures are published into <FSStorePath>/<id>/ async, and
	// EnsureLocal copies from there before trying peers.
	flag.StringVar(&config.FSStorePath, "fsstore-path", os.Getenv("NVSNAP_FSSTORE_PATH"),
		"Path to a shared filesystem mounted on every node (Lustre/Weka/EFS/Filestore/NFS). When set, captures are published here and the restore cascade copies from this path before peer fanout. Empty disables.")
	flag.StringVar(&config.ListenAddr, "listen", ":8081", "Listen address")
	flag.StringVar(&config.CheckpointDir, "checkpoint-dir", "/var/lib/nvsnap/checkpoints", "Checkpoint storage directory (in-agent-container path)")
	flag.StringVar(&config.CheckpointHostDir, "checkpoint-host-dir", "/var/lib/containerd/nvsnap-checkpoints", "Host path that backs --checkpoint-dir (must match the DaemonSet hostPath mount; used to translate paths for the capture-write writer Job)")
	flag.StringVar(&config.CRIUPath, "criu-path", "/usr/local/sbin/criu", "Path to CRIU binary (on host filesystem)")
	flag.StringVar(&config.CudaCheckpointPath, "cuda-checkpoint-path", "/usr/local/bin/cuda-checkpoint", "Path to cuda-checkpoint binary (on host filesystem)")
	flag.StringVar(&config.ContainerdSocket, "containerd-socket", "/run/containerd/containerd.sock", "containerd socket path")
	flag.StringVar(&config.ContainerdNamespace, "containerd-namespace", "k8s.io", "containerd namespace")
	flag.StringVar(&config.LogLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	flag.BoolVar(&config.UseNsenter, "use-nsenter", false, "Run CRIU/cuda-checkpoint in host mount namespace (auto-detected if in container)")
	flag.BoolVar(&config.Prewarm, "prewarm", os.Getenv("NVSNAP_PREWARM") != "0",
		"On restore, sequentially read the rox-backed overlay lowerdir into the page cache ahead of the engine's mmap weight-load (default on; set NVSNAP_PREWARM=0 to disable). Requires the agent memory limit >= model working set.")

	// Rootfs-only capture loop (off by default). Backend is always Local
	// post-5d (gpdrox/PVC-fanout was removed; see PHASE5D-PEER-FANOUT-BLOB-STORE.md).
	flag.BoolVar(&config.RootfsCapture.Enabled, "rootfs-capture", false,
		"Enable rootfs-only multi-GPU capture loop (watches pods labeled nvsnap.io/capture=true)")
	flag.StringVar(&config.RootfsCapture.CacheDir, "rootfs-cache-dir", "/var/lib/nvsnap/cache",
		"Directory the Local backend writes captures into")
	flag.StringVar(&config.RootfsCapture.PodCacheDir, "pod-cache-dir", os.Getenv("NVSNAP_POD_CACHE_DIR"),
		"In-pod cache mount path (e.g. /opt/nvsnap) for cachedir mode: capture ONLY this dir as the PVC root, restore RO-mounts the rox here (no overlayfs). Empty = standard whole-rootfs capture. Must match the webhook's cacheDir.")
	flag.StringVar(&config.RootfsCapture.PodCacheEnvFile, "cachedir-env-file", os.Getenv("NVSNAP_CACHEDIR_ENV_FILE"),
		"Path to a mounted ConfigMap file with the cachedir env template (NAME=value lines; {root}/{cache}/{model} placeholders). Read on capture inject only — edit the ConfigMap to add/remove cache env vars without an agent rebuild. Empty/unreadable = built-in default. Restore replays the env stamped in the manifest.")
	flag.StringVar(&config.OverlayRoot, "overlay-root", "/var/lib/nvsnap/overlays",
		"Per-pod OverlayFS scratch root (nvsnap#194). MUST be identical in the agent container AND on the host — kubelet binds the merged path via the host namespace.")
	flag.StringVar(&config.RootfsCapture.CMNamespace, "rootfs-cm-namespace", "nvsnap-system",
		"K8s namespace ConfigMaps are written to so any node's webhook can resolve a hash")
	flag.IntVar(&config.RootfsCapture.CUDADriverMajor, "cuda-driver-major", 580,
		"NVIDIA driver major on this node (used in rootfs-only HashInput composition)")
	var rootfsWarmupSec int
	flag.IntVar(&rootfsWarmupSec, "rootfs-warmup-seconds", 5,
		"Seconds to wait after pod becomes Ready before triggering rootfs capture (0 = capture immediately)")
	flag.IntVar(&config.RootfsCapture.Concurrency, "rootfs-concurrency", 2,
		"Max concurrent rootfs captures on this node")

	// In-agent admission webhook (replaces the standalone nvsnap-webhook
	// Deployment). Off by default. When --webhook is on, the agent's
	// existing process additionally listens on --webhook-listen with TLS
	// for AdmissionReview requests, sharing the rootfs-only Backend so
	// manifests + cache + injection stay consistent.
	flag.BoolVar(&config.Webhook.Enabled, "webhook", false,
		"Enable in-agent mutating-admission webhook server")
	flag.StringVar(&config.Webhook.ListenAddr, "webhook-listen", ":8443",
		"Address the in-agent webhook TLS server binds to")
	flag.StringVar(&config.Webhook.CertFile, "webhook-cert", "/etc/nvsnap/webhook/tls.crt",
		"PEM cert for the in-agent webhook TLS server")
	flag.StringVar(&config.Webhook.KeyFile, "webhook-key", "/etc/nvsnap/webhook/tls.key",
		"PEM key for the in-agent webhook TLS server")
	flag.StringVar(&config.Webhook.Path, "webhook-path", "/mutate",
		"HTTP path the in-agent webhook listens on")

	// BYOC auto-inject: image refs the webhook stamps into the four
	// init containers when a pod has nvsnap.io/auto-inject: "true". All
	// four must be set for the auto-inject branch to fire; otherwise
	// the webhook fails open (admits the pod unchanged).
	flag.StringVar(&config.Webhook.AutoInject.Uvloop, "webhook-image-uvloop", "", //nolint:staticcheck // deprecated field intentionally bound for flag back-compat
		"Image ref for the auto-inject get-uvloop init container (multi-python uvloop wheels)")
	flag.StringVar(&config.Webhook.AutoInject.LibUV, "webhook-image-libuv", "",
		"Image ref for the auto-inject get-libuv init container")
	flag.StringVar(&config.Webhook.AutoInject.LibZMQ, "webhook-image-libzmq", "",
		"Image ref for the auto-inject get-libzmq init container")
	flag.StringVar(&config.Webhook.AutoInject.Agent, "webhook-image-agent", "",
		"Image ref for the auto-inject get-nvsnap init container (nvsnap-agent — must match running agent)")

	// nvsnap#147: nvsnap-l2-wait init container ref. When set, the
	// webhook prepends a nvsnap-l2-wait init container on restore pods
	// that polls nvsnap-server until the L2 PVC promote is ready.
	// Empty disables the inject (back-compat: PVC mount still works
	// once the rox PVC binds; kubelet may stall in
	// ContainerCreating in the meantime).
	flag.StringVar(&config.Webhook.L2WaitImage, "webhook-l2-wait-image", "",
		"Image ref for the nvsnap-l2-wait init container injected onto restore pods (nvsnap#147)")

	// nvsnap#147 second half: on-host directory where the
	// nvsnap-agent DaemonSet stages the restore bundle. Function pods
	// mount {root}/nvsnap + {root}/nvsnap-lib via hostPath. Empty →
	// default "/var/lib/nvsnap/bundle" (matches the DaemonSet's
	// nvsnap-bundle-stage initContainer destination). Tests / dev
	// overrides only — production should keep the default.
	flag.StringVar(&config.Webhook.HostBundleRoot, "webhook-host-bundle-root", "",
		"On-host path the nvsnap-agent DaemonSet stages the restore bundle into (default /var/lib/nvsnap/bundle). Function pods mount {root}/nvsnap + {root}/nvsnap-lib via hostPath.")

	// nvsnap#202: restore-prep strategy. "inline" (default) keeps the
	// pre-#202 behavior — Mutate() does the OverlayFS mount syscalls
	// during admission, which blows past the 10–30s webhook timeout
	// for workloads with hundreds of mounts (e.g. DeepSeek-V4-Flash
	// with 355). "init-container" delegates mount work to a
	// nvsnap-mount-prep init container on the restored pod; admission
	// only emits patches with the deterministic merged mountpoints,
	// and the init container polls the agent's async prep manager
	// until ready.
	flag.StringVar(&config.Webhook.RestorePrepStrategy, "webhook-restore-prep-strategy", "inline",
		"Strategy for restore-side overlay mount prep: inline (do mounts during admission, default) or init-container (delegate to nvsnap-mount-prep init container on the restored pod)")
	flag.StringVar(&config.Webhook.MountPrepInitImage, "webhook-mount-prep-init-image", "",
		"Image ref for the nvsnap-mount-prep init container injected when --webhook-restore-prep-strategy=init-container. Must contain /nvsnap-mount-prep (the agent image satisfies this).")
	flag.IntVar(&config.Webhook.AgentHostPort, "webhook-agent-host-port", 8081,
		"Port the nvsnap-mount-prep init container reaches the agent on (matches --listen and the agent DaemonSet's hostPort).")

	// nvsnap#63: L2 per-capture PVC backend. When --l2-storage-class is
	// set, the agent provisions a rwx-<hash> PVC + writer Job +
	// VolumeSnapshot + rox-<hash> reader PVC after every successful CRIU
	// dump. Restored pods mount rox-<hash> ReadOnly via the webhook —
	// multi-node fan-out at storage-tier speed. Empty disables L2; CRIU
	// dumps stay node-pinned on L1 hostpath.
	flag.StringVar(&config.L2.StorageClass, "l2-storage-class", os.Getenv("NVSNAP_L2_STORAGE_CLASS"),
		"RWX StorageClass for per-capture PVCs (e.g. hyperdisk-ml). Empty disables L2; CRIU dumps stay node-pinned on L1 hostpath.")
	flag.StringVar(&config.L2.SnapshotClass, "l2-snapshot-class", os.Getenv("NVSNAP_L2_SNAPSHOT_CLASS"),
		"VolumeSnapshotClass for rwx-<hash> → rox-<hash> transition (e.g. csi-gce-pd-snapshot-class). Required when --l2-storage-class is set.")
	flag.StringVar(&config.L2.Namespace, "l2-namespace", os.Getenv("NVSNAP_L2_NAMESPACE"),
		"Namespace where per-capture PVCs, snapshots, Jobs, and Leases are created. Defaults to nvsnap-system.")
	flag.StringVar(&config.L2.WriterImage, "l2-writer-image", os.Getenv("NVSNAP_L2_WRITER_IMAGE"),
		"Image for the capture-write writer Job (typically same as the running agent image).")
	flag.StringVar(&config.L2.WriterPullSecret, "l2-writer-pull-secret", os.Getenv("NVSNAP_L2_WRITER_PULL_SECRET"),
		"imagePullSecret name for the mount-holder pod (created by operators in the workload namespace). Defaults to nvsnap-agent-pull; set to '-' to disable.")

	flag.Parse()
	config.RootfsCapture.WarmupDelay = time.Duration(rootfsWarmupSec) * time.Second
	for _, b := range strings.Split(replicationPeerBuckets, ",") {
		if b = strings.TrimSpace(b); b != "" {
			config.Replication.ObjectStore.PeerBuckets = append(config.Replication.ObjectStore.PeerBuckets, b)
		}
	}

	// Auto-detect node name from hostname if not provided
	if config.NodeName == "" {
		hostname, err := os.Hostname()
		if err == nil {
			config.NodeName = hostname
		}
	}

	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	// v0.0.88 instrumentation: confirm at boot whether prewarm is enabled.
	log.WithField("prewarm", config.Prewarm).Info("nvsnap-agent config: page-cache prewarm")

	a, err := agent.New(config)
	if err != nil {
		log.WithError(err).Fatal("Failed to create agent")
	}
	defer func() { _ = a.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("Shutting down...")
		cancel()
	}()

	if err := a.Run(ctx); err != nil {
		log.WithError(err).Fatal("Agent failed")
	}
}

// parseDurationEnv reads a Go duration string (e.g. "60s") from env key,
// returning 0 if unset or unparseable. Used as the default for the
// --replication-poll-interval flag so the env var can configure it directly.
func parseDurationEnv(key string) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 0
}
