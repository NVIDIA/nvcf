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

// Package agent implements the node-level nvsnap agent: process discovery,
// CRIU + cuda-checkpoint orchestration, rootfs capture, and the L2/L3
// checkpoint distribution paths.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/cuda"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime"
	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime/crio" // register CRI-O factory
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Config holds agent configuration
type Config struct {
	ListenAddr    string
	CheckpointDir string
	// CheckpointHostDir is the host filesystem path that backs CheckpointDir
	// inside the agent container. The capture-write writer Job consumes
	// CaptureSource.SrcPath as a host path (joined with /host), so the
	// promote step must translate paths from CheckpointDir → CheckpointHostDir
	// before handing them to Backend.Put. Default matches the agent
	// DaemonSet's hostPath mount (`/var/lib/containerd/nvsnap-checkpoints`).
	CheckpointHostDir   string
	ContainerdSocket    string
	ContainerdNamespace string
	CudaCheckpointPath  string
	CRIUPath            string
	NodeName            string
	LogLevel            string

	// AuthToken is the shared bearer token callers must present on the
	// agent API. Sourced from a Secret rather than a flag so it does not
	// land in the pod spec or in `ps` output. Empty disables the check.
	AuthToken string
	// AuthMode is disabled (default), permissive, or required. See auth.go
	// for why the rollout needs a permissive state.
	AuthMode AuthMode
	UseNsenter          bool // Run CRIU/cuda-checkpoint in host mount namespace (for containerized agents)

	// Prewarm enables agent-side page-cache prewarm of the rox-backed
	// overlay lowerdir on restore (--prewarm, default true). Reads the
	// captured model tree sequentially into the page cache ahead of the
	// engine's mmap load so first-touch faults hit RAM, not the
	// ~350 MB/s HDML random-read path. Requires the agent DaemonSet
	// memory limit >= working set (else the warmed cache is reclaimed).
	// See prewarm.go.
	Prewarm bool

	// RootfsCapture is the optional rootfs-only capture loop config. Off
	// by default; opt in per node via cmd flags. See rootfsonly_integration.go.
	RootfsCapture RootfsCaptureConfig

	// OverlayRoot is the per-pod OverlayFS scratch root (nvsnap#194).
	// MUST be a path that is identical in the agent container and on
	// the host — the webhook patches kubelet's pod spec with the
	// merged mountpoint built off this root, and kubelet runs in host
	// namespace. Wired to --overlay-root; defaults to
	// /var/lib/nvsnap/overlays. On clusters where helm relocates
	// .Values.agent.hostPaths.nvsnapOverlays (e.g. GCP-H100-a uses
	// /mnt/stateful_partition/.../nvsnap/overlays because the boot disk
	// is small), both the hostPath mount and this flag must point at
	// the same path.
	OverlayRoot string

	// Webhook is the optional in-agent admission-webhook server config.
	// Off by default; enabled with --webhook (requires --rootfs-capture
	// since the webhook reuses the capture-loop's Backend). See
	// webhook_integration.go.
	Webhook WebhookConfig

	// CatalogURL is the nvsnap-server base URL used by the restore-side
	// cascading-fetch (#104) to query /api/v1/checkpoints/{id}/sources
	// and POST peer-add. Empty disables the cascade — agent only does
	// same-node restore. Default in cluster:
	// http://nvsnap-server.nvsnap-system.svc.cluster.local:8080
	CatalogURL string

	// NodeIP is this agent's reachable address from peers. With
	// hostNetwork:true, this is the K8s node's host IP (downward API
	// status.hostIP). Used to advertise the agent's HTTP endpoint
	// when registering as a peer in the catalog.
	NodeIP string

	// BlobStoreURL is the base URL of the cluster's nvsnap-blobstore
	// (Phase 5d.2 durable backstop). Empty disables capture-side
	// upload AND cascade tier-3 fallback — agents fall back to
	// peer-only fanout. Default in cluster:
	// http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000
	BlobStoreURL string

	// FSStorePath is the agent-container path to a distributed
	// filesystem mounted on every node — Lustre, Weka, EFS,
	// Filestore, NFS, etc. Phase 2c of the 16-node distribution
	// plan (docs/TRANSPORT-ARCHITECTURE.md). When set, every
	// successful CRIU capture is asynchronously published into
	// <FSStorePath>/<id>/ and EnsureLocal probes that path before
	// trying peer agents. Empty disables both halves — agents fall
	// back to peer-then-blobstore fanout.
	FSStorePath string

	// L2 holds the per-capture PVC backend config (nvsnap#63).
	// When L2.StorageClass is non-empty, the agent constructs a
	// checkpointstore.PerCapturePVCBackend at startup and promotes
	// every successful CRIU dump into a rwx-<hash> → rox-<hash>
	// PVC pair so multi-node restore can mount the read PVC in
	// parallel. Disabled (no L2 promote) when StorageClass is
	// empty.
	L2 L2BackendConfig

	// Replication is the opt-in cross-cluster replication config (the L4
	// tier). See docs/design/cross-cluster-replication.md. When
	// Replication.ObjectStore.Provider AND HomeBucket are both non-empty,
	// every committed rootfs capture is pushed to the home bucket keyed by
	// content hash, and POST /v1/replicate can pull a hash on demand by
	// probing [HomeBucket]+PeerBuckets. Empty provider OR empty home bucket
	// disables replication entirely.
	Replication ReplicationConfig
}

// ReplicationConfig configures opt-in cross-cluster replication: the L4
// tier that replaces the single-cluster nvsnap-blobstore for cross-cluster
// reach. Replication is enabled only when ObjectStore.Provider and
// ObjectStore.HomeBucket are both set.
type ReplicationConfig struct {
	// ObjectStore is the provider-neutral object-store backend (gcs today,
	// s3 planned).
	ObjectStore ObjectStoreConfig

	// PollInterval enables the cross-cluster auto-pull poller: when > 0 and
	// replication is enabled, a single elected agent periodically lists the
	// home bucket and pulls every GPU/driver-compatible capture it finds
	// (via ReplicateFromObjectStore), so a remote cluster warms itself
	// without an operator firing POST /v1/replicate/{hash} per hash.
	// 0 (default) disables it. See replication_poll.go.
	PollInterval time.Duration
}

// ObjectStoreConfig configures the provider-neutral object-store backend
// the replication object store uses.
type ObjectStoreConfig struct {
	// Provider selects the object-store implementation: "gcs" or "s3".
	// Empty disables replication.
	Provider string

	// HomeBucket is THIS cluster's bucket: the push target on capture
	// commit and the first bucket probed on a pull. Empty disables
	// replication. Bare bucket name, no scheme prefix
	// ("GCP-H100-a-captures").
	HomeBucket string

	// PeerBuckets are other clusters' home buckets, probed in order on a
	// pull miss against HomeBucket. A capture pulled from a peer is also
	// cached into HomeBucket so the next local restore reads home.
	PeerBuckets []string
}

// L2BackendConfig is the per-capture PVC L2 backend (nvsnap#63). See
// docs/L2-PVC-CRIU-DESIGN.md.
type L2BackendConfig struct {
	// StorageClass is the RWX-capable StorageClass name. Required
	// to enable L2 — empty disables. On GKE production we set this
	// to "hyperdisk-ml" via helm values; install docs list it as a
	// cluster prerequisite.
	StorageClass string

	// SnapshotClass is the VolumeSnapshotClass used for the
	// rwx → rox transition. Required when StorageClass is set. On
	// GKE the default class is "csi-gce-pd-snapshot-class".
	SnapshotClass string

	// Namespace where PVCs, snapshots, Jobs, and Leases live.
	// Defaults to "nvsnap-system" if empty.
	Namespace string

	// WriterImage is the image for the capture-write writer Job
	// (typically the same agent image — it runs the capture-write
	// subcommand). Defaults to the agent's own image at startup
	// if unset and the agent can introspect it.
	WriterImage string

	// WriterPullSecret is the imagePullSecret name stamped on the
	// mount-holder pod. The holder runs in the source/workload
	// namespace where NVCA's admission webhook injects an init
	// container whose image may not be cached on a fresh cluster
	// (ImagePullBackOff → the L2 copy times out). Operators create
	// this secret (from a docker config) in the workload namespace.
	// Defaults to "nvsnap-agent-pull"; empty string disables.
	WriterPullSecret string
}

// DefaultWriterPullSecret is the imagePullSecret name the mount-holder
// pod references unless overridden via --l2-writer-pull-secret /
// NVSNAP_L2_WRITER_PULL_SECRET. Operators create this secret in the
// workload namespace from a docker config.
const DefaultWriterPullSecret = "nvsnap-agent-pull" //nolint:gosec // secret name, not a credential value

// Agent is the NVSNAP node agent
type Agent struct {
	config     Config
	log        *logrus.Logger
	runtime    runtime.Runtime
	containerd *containerd.Client // non-nil only when runtime is containerd (for containerd-specific calls)
	cuda       *cuda.Manager
	criu       *criu.Manager
	server     *http.Server

	// captureBackend is the rootfs-only auto-capture loop's backend
	// (Local hostpath + ConfigMap manifest registry). Non-nil only
	// when --rootfs-capture is enabled. Despite the comment in the
	// pre-nvsnap#63 era, the CRIU path does NOT promote into this
	// backend — that's what l2Backend is for now.
	captureBackend checkpointstore.Backend

	// l2Backend is the per-capture PVC backend (PerCapturePVCBackend,
	// nvsnap#63) that the CRIU path promotes its hostPath dump into.
	// Non-nil only when L2 is configured (agent.L2.StorageClass set,
	// CatalogURL set, in-cluster K8s config available). Empty cluster
	// → CRIU dumps stay on L1 hostpath + L4 blobstore; multi-node
	// restore falls back to L3 peer cascade.
	l2Backend checkpointstore.Backend

	// kubeClient is the shared K8s API client used by the rootfs-only
	// capture watcher AND the admission-webhook cascade-fetch path
	// (EnsureCaptureLocal). Non-nil only when --rootfs-capture is
	// enabled; populated by startRootfsCapture before the webhook
	// server starts.
	kubeClient kubernetes.Interface

	// peerLoad counts this agent's in-flight peer fetches per peer
	// URL. Used by EnsureLocal to pick the least-loaded peer when
	// several peers have the same checkpoint. Phase 1 of the 16-node
	// distribution plan (docs/TRANSPORT-ARCHITECTURE.md).
	peerLoad *peerLoadTracker

	// fsStore is the shared-filesystem backend. Non-nil when
	// Config.FSStorePath is set. Phase 2c of the 16-node distribution
	// plan: customers with a DFS get a fast path that bypasses the
	// peer cascade entirely.
	fsStore *fsStore

	// overlay owns per-restore-pod OverlayFS unions on top of the
	// rootfs capture's read-only tree (nvsnap#194). Always non-nil;
	// the webhook calls Prepare() during admission for each
	// RootfsExtractPath, and a pod-delete watcher calls Cleanup() at
	// pod teardown. Disabled clusters get a no-op only if Sweep is
	// never invoked — the manager itself is cheap to construct.
	overlay *OverlayManager

	// prepJobs is the async overlay-prep manager that backs the
	// nvsnap-mount-prep init container (nvsnap#202). When non-nil, the
	// agent serves POST/GET/DELETE /v1/restore/prep[/{pod-uid}].
	// Init containers POST a PrepRequest, poll until ready, exit.
	// This moves OverlayFS mount work off the K8s admission hot
	// path so admission stays <200 ms even on captures with
	// hundreds of extract paths.
	prepJobs *PrepJobManager

	// objectStoreHome is this cluster's home bucket and objectStorePeers
	// the peer buckets, for cross-cluster replication (Config.Replication).
	// Non-nil only when replication is enabled (provider + HomeBucket set);
	// objectStoreClose releases the shared client on shutdown. Push
	// (UploadCaptureToObjectStore) and pull (ReplicateFromObjectStore) use
	// these. See docs/design/cross-cluster-replication.md.
	objectStoreHome  objectstore.Bucket
	objectStorePeers []objectstore.Bucket
	objectStoreClose func() error

	// prewarmed tracks rox-backed lowerdirs already page-cache-warmed
	// this agent lifetime, so the fan-out of pods sharing one per-node
	// rox mount triggers exactly one sequential prewarm. See prewarm.go.
	prewarmed sync.Map
}

// New creates a new agent
func New(cfg Config) (*Agent, error) {
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	level, _ := logrus.ParseLevel(cfg.LogLevel)
	log.SetLevel(level)

	// Connect to the container runtime. Auto-detects containerd or CRI-O.
	// ContainerdSocket field retained for backwards compatibility — when set,
	// used as an explicit hint; otherwise well-known paths are probed.
	rt, err := runtime.New(runtime.Config{
		SocketPath: cfg.ContainerdSocket,
		Namespace:  cfg.ContainerdNamespace,
		Logger:     log,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to container runtime: %w", err)
	}
	log.WithField("runtime", rt.Type()).Info("Connected to container runtime")

	// If the runtime is containerd, retain the concrete client for
	// containerd-only operations (e.g., GetImageSize).
	var containerdClient *containerd.Client
	if c, ok := rt.(*containerd.Client); ok {
		containerdClient = c
	}

	// Determine if we should use nsenter (auto-detect if running in container)
	useNsenter := cfg.UseNsenter
	if !useNsenter {
		// Auto-detect: if we're running in a container, use nsenter
		// Multiple detection methods since not all containers have /.dockerenv
		containerDetected := false
		detectionMethod := ""

		// Method 1: Docker creates /.dockerenv
		if _, statErr := os.Stat("/.dockerenv"); statErr == nil {
			containerDetected = true
			detectionMethod = "/.dockerenv"
		}

		// Method 2: Podman creates /run/.containerenv
		if !containerDetected {
			if _, statErr := os.Stat("/run/.containerenv"); statErr == nil {
				containerDetected = true
				detectionMethod = "/run/.containerenv"
			}
		}

		// Method 3: Check cgroup for container patterns (docker, kubepods, containerd)
		if !containerDetected {
			if cgroupData, readErr := os.ReadFile("/proc/1/cgroup"); readErr == nil {
				cgroupStr := string(cgroupData)
				if strings.Contains(cgroupStr, "docker") ||
					strings.Contains(cgroupStr, "kubepods") ||
					strings.Contains(cgroupStr, "containerd") {
					containerDetected = true
					detectionMethod = "/proc/1/cgroup"
				}
			}
		}

		// Method 4: If KUBERNETES_SERVICE_HOST is set, we're in k8s
		if !containerDetected {
			if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
				containerDetected = true
				detectionMethod = "KUBERNETES_SERVICE_HOST env"
			}
		}

		if containerDetected {
			useNsenter = true
			log.WithField("detectionMethod", detectionMethod).Info("Detected containerized environment, enabling nsenter mode")
		}
	}
	log.WithField("useNsenter", useNsenter).Info("Initializing CUDA and CRIU managers")

	// Initialize CUDA manager (with nsenter support for containerized agents)
	cudaManager := cuda.New(cfg.CudaCheckpointPath, useNsenter, log)

	// Initialize CRIU manager (with nsenter support for containerized agents)
	criuManager, err := criu.New(log, cfg.CRIUPath, useNsenter)
	if err != nil {
		log.WithError(err).Warn("CRIU initialization failed (optional)")
	}

	// Create checkpoint directory
	if err := os.MkdirAll(cfg.CheckpointDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint dir: %w", err)
	}

	agent := &Agent{
		config:     cfg,
		log:        log,
		runtime:    rt,
		containerd: containerdClient,
		cuda:       cudaManager,
		criu:       criuManager,
		peerLoad:   newPeerLoadTracker(),
		fsStore:    newFSStore(cfg.FSStorePath, log),
		overlay:    NewOverlayManager(cfg.OverlayRoot, log, nil),
	}
	// nvsnap#202: prep manager closes over Agent.PrepareOverlay so
	// the per-job goroutines share the same peer-routing + overlay
	// machinery as the legacy inline webhook path. Adding it
	// unconditionally is safe — the manager is cheap when idle and
	// the HTTP endpoints respond 503 when prepJobs is nil (so no
	// behavior change for clusters that haven't switched the
	// webhook to init-container mode yet).
	agent.prepJobs = NewPrepJobManager(agent, log)

	return agent, nil
}

// Run starts the agent
func (a *Agent) Run(ctx context.Context) error {
	metrics.RegisterAgent()

	// OpenTelemetry. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset,
	// so prod clusters without Jaeger pay zero overhead.
	tracingShutdown, err := tracing.Init(ctx, "nvsnap-agent")
	if err != nil {
		a.log.WithError(err).Warn("tracing init failed; continuing without OTel")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutErr := tracingShutdown(shutdownCtx); shutErr != nil {
			a.log.WithError(shutErr).Warn("tracing shutdown error")
		}
	}()

	router := mux.NewRouter()

	// Every {id}/{hash}/{pod-uid} below names a directory under a hostPath
	// mount. Validate them in one place so a route added later is covered
	// without remembering to. See pathVarGuard in pathsafe.go.
	router.Use(pathVarGuard)

	// Authenticate before anything else runs. Installed only when a token is
	// configured, so an upgrade that has not been given one behaves exactly
	// as before -- but say so loudly, since an agent silently serving an
	// unauthenticated privileged API is the state this guards against.
	if guard := tokenGuard(a.config.AuthMode, a.config.AuthToken, a.log); guard != nil {
		router.Use(guard)
		a.log.WithField("mode", a.config.AuthMode).Info("Agent API authentication enabled")
	} else {
		a.log.Warn("Agent API is UNAUTHENTICATED: set NVSNAP_AGENT_TOKEN and " +
			"--auth-mode to require a bearer token (GH #486)")
	}

	// Metrics endpoint
	router.Handle("/metrics", metrics.Handler()).Methods("GET")

	// API routes
	router.HandleFunc("/health", a.healthHandler).Methods("GET")
	router.HandleFunc("/v1/checkpoint", a.checkpointHandler).Methods("POST")
	router.HandleFunc("/v1/restore", a.restoreHandler).Methods("POST")
	router.HandleFunc("/v1/restore/trigger", a.triggerRestoreHandler).Methods("POST")
	router.HandleFunc("/v1/restore/manifest", a.getPlaceholderManifestHandler).Methods("POST")
	router.HandleFunc("/v1/checkpoints", a.listCheckpointsHandler).Methods("GET")
	router.HandleFunc("/v1/containers", a.listContainersHandler).Methods("GET")
	router.HandleFunc("/v1/gpu/processes", a.gpuProcessesHandler).Methods("GET")
	router.HandleFunc("/v1/gpu/restore", a.gpuRestoreHandler).Methods("POST")
	router.HandleFunc("/v1/checkpoints/{id}", a.deleteCheckpointHandler).Methods("DELETE")
	router.HandleFunc("/v1/checkpoints/{id}/files", a.listCheckpointFilesHandler).Methods("GET")
	router.HandleFunc("/v1/checkpoints/{id}/file", a.readCheckpointFileHandler).Methods("GET")
	// Phase 5d.1: peer-fanout manifest endpoint. Returns a flat,
	// recursive list of every file in the checkpoint dir with size +
	// modtime. Receivers iterate this list to drive parallel
	// per-file downloads via /v1/checkpoints/{id}/file?path=...
	router.HandleFunc("/v1/checkpoints/{id}/manifest", a.checkpointManifestHandler).Methods("GET")
	// Phase 5d.1: cascading fetch entrypoint. Restore caller hits
	// this BEFORE applying the restore manifest so the dump is
	// guaranteed to exist on this node's hostPath. Tier order:
	// same-node → peer agents → blob store (5d.2).
	router.HandleFunc("/v1/checkpoints/{id}/ensure-local", a.ensureLocalHandler).Methods("POST")

	// pprof — for diagnosing cascade throughput on the actual cluster.
	// Off-the-shelf net/http/pprof handlers registered on the agent's
	// existing mux. Profiles accessible via curl/go-tool from inside
	// the agent pod, e.g.
	//   kubectl exec nvsnap-agent-X -- curl -sS \
	//     localhost:8081/debug/pprof/profile?seconds=30 > cpu.pb
	router.HandleFunc("/debug/pprof/", pprof.Index)
	router.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	router.HandleFunc("/debug/pprof/profile", pprof.Profile)
	router.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	router.HandleFunc("/debug/pprof/trace", pprof.Trace)
	router.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	router.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	router.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	router.Handle("/debug/pprof/block", pprof.Handler("block"))
	router.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	router.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))

	// Rootfs-only multi-GPU peer endpoints. Mirror the CRIU
	// manifest/file pair above but root in <RootfsCapture.CacheDir>/<hash>.
	// EnsureCaptureLocal (capture_cascade.go) drives these from a peer
	// agent when the in-agent admission webhook resolves a hash that
	// landed on a different node than the one answering the webhook.
	router.HandleFunc("/v1/captures/{hash}/manifest", a.captureManifestHandler).Methods("GET")
	router.HandleFunc("/v1/captures/{hash}/file", a.captureFileHandler).Methods("GET")
	// Rootfs cascade fan-out trigger (GH #114). The CRIU sibling
	// (/v1/checkpoints/{id}/ensure-local) exists; this is the
	// rootfs analog. Synchronously calls EnsureCaptureLocal:
	// returns when the per-hash rootfs cache is populated on this
	// node (or all peer tiers failed). The eventual webhook fix
	// for #114 will inject an init container that POSTs here on
	// each receiver, instead of pinning the pod via nodeAffinity
	// to the source node.
	router.HandleFunc("/v1/captures/{hash}/ensure-local", a.ensureCaptureLocalHandler).Methods("POST")

	// Cross-cluster pull-through (docs/design/cross-cluster-replication.md).
	// Probes the replication home + peer buckets for {hash}, downloads the
	// capture, and replays the commit locally (manifest CM + L2 rox PVC) so
	// NVCA and the webhook find it warm. 404 if no configured bucket holds
	// the hash; 501 if replication isn't enabled on this agent.
	router.HandleFunc("/v1/replicate/{hash}", a.replicateHandler).Methods("POST")

	// nvsnap#194: per-restore-pod OverlayFS unions on top of the
	// rootfs capture's read-only tree, so workloads that write into
	// captured cache dirs (vLLM compile cache, etc.) stop crashing
	// with EROFS. Routes are an out-of-band trigger for the webhook
	// (in-process prep is the primary path) and a manual cleanup hook
	// for tests / debugging.
	router.HandleFunc("/v1/restore/overlay", a.prepareRestoreOverlayHandler).Methods("POST")
	router.HandleFunc("/v1/restore/overlay/{pod-uid}", a.cleanupRestoreOverlayHandler).Methods("DELETE")
	// nvsnap#202: async overlay-prep for nvsnap-mount-prep init container.
	// Webhook patches a tiny init container into the restored pod; the
	// init container POSTs /v1/restore/prep then polls
	// /v1/restore/prep/{pod-uid} until state=ready. Moves the mount
	// work out of K8s admission (where the 10–30s budget is too tight
	// for hundreds of OverlayFS mounts) into pod-lifecycle.
	router.HandleFunc("/v1/restore/prep", a.startRestorePrepHandler).Methods("POST")
	router.HandleFunc("/v1/restore/prep/{pod-uid}", a.statusRestorePrepHandler).Methods("GET")
	router.HandleFunc("/v1/restore/prep/{pod-uid}", a.cleanupRestorePrepHandler).Methods("DELETE")

	a.server = &http.Server{
		Addr:              a.config.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Handle shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		a.log.Info("Shutting down...")
		_ = a.server.Shutdown(context.Background())
	}()

	// nvsnap#63: L2 per-capture PVC backend. Constructed when
	// agent.L2.StorageClass is set in the helm values. Drives the
	// rwx-<hash> writer Job + snapshot + rox-<hash> reader PVC pipeline
	// after every successful CRIU dump (see internal/agent/checkpoint.go
	// for the call site). Empty StorageClass → l2Backend stays nil
	// and the CRIU path skips the promote step.
	//
	// MUST run BEFORE startRootfsCapture (v0.0.49): the rootfs Backend
	// chain in buildAgentBackend appends a.l2Backend when non-nil so
	// every rootfs capture also produces the L2 PVC for fan-out. The
	// previous order (rootfs first, L2 second) silently dropped L2 from
	// the rootfs path — see docs/design/ROOTFS-EVERYWHERE.md gap #1.
	l2, err := a.startL2Backend(ctx, a.config.L2)
	if err != nil {
		a.log.WithError(err).Error("L2 backend failed to start; multi-node restore will use L3 peer cascade only")
	}
	a.l2Backend = l2

	// Optional rootfs-only capture loop (off by default; opt-in via Config.RootfsCapture).
	backend, err := a.startRootfsCapture(ctx, a.config.RootfsCapture)
	if err != nil {
		a.log.WithError(err).Error("rootfs-only capture failed to start; continuing without it")
	}
	a.captureBackend = backend
	// Optional in-agent admission webhook (off by default; opt-in via
	// Config.Webhook). Reuses the capture-loop's Backend so manifests
	// + cache data + injected pod fragments stay consistent.
	if err := a.startWebhook(ctx, a.config.Webhook, backend); err != nil {
		a.log.WithError(err).Error("agent admission webhook failed to start; continuing without it")
	}

	// nvsnap#194: OverlayFS cleanup-on-pod-delete + startup sweep. Safe
	// to call regardless of whether the webhook is enabled — if no
	// pod ever triggers Prepare, the watcher is a no-op. kubeClient
	// is populated by startRootfsCapture when --rootfs-capture is on;
	// nil-tolerated below (Sweep still runs).
	if err := a.startOverlayWatcher(ctx, a.kubeClient); err != nil {
		a.log.WithError(err).Error("overlay watcher failed to start; orphan overlays will accumulate until next agent restart")
	}

	// Cross-cluster auto-pull poller (off by default; opt-in via
	// --replication-poll-interval). No-op unless replication is enabled
	// and the interval is > 0. Only the elected leader pulls per tick.
	a.startReplicationPoller(ctx)

	a.log.WithField("addr", a.config.ListenAddr).Info("Starting NVSNAP agent")
	if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Close cleans up resources
func (a *Agent) Close() error {
	if a.runtime != nil {
		_ = a.runtime.Close()
	}
	if a.objectStoreClose != nil {
		_ = a.objectStoreClose()
	}
	return nil
}

// Health check handler
func (a *Agent) healthHandler(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "healthy",
		"nodeName": a.config.NodeName,
	})
}

// Checkpoint handler
func (a *Agent) checkpointHandler(w http.ResponseWriter, r *http.Request) {
	var req CheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	metrics.ActiveOperations.WithLabelValues("checkpoint").Inc()
	start := time.Now()

	result, err := a.Checkpoint(r.Context(), req)

	metrics.ActiveOperations.WithLabelValues("checkpoint").Dec()
	duration := time.Since(start).Seconds()
	workload := req.PodName // best available label
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	if err != nil {
		var redirect *BackendRedirectError
		if errors.As(err, &redirect) {
			// Not a failure — the agent is telling the caller to re-issue
			// the capture via the rootfs path (label the pod nvsnap.io/capture=true).
			// 422 is the right status: request was valid but we won't act on it.
			metrics.CheckpointTotal.WithLabelValues("redirected", workload, ns).Inc()
			a.log.WithField("backend", string(redirect.Backend)).
				Warn("Checkpoint redirected to rootfs capture path")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"redirect": "rootfs",
				"backend":  string(redirect.Backend),
				"reason":   redirect.Error(),
			})
			return
		}
		metrics.CheckpointTotal.WithLabelValues("failed", workload, ns).Inc()
		a.log.WithError(err).Error("Checkpoint failed")
		http.Error(w, fmt.Sprintf("checkpoint failed: %v", err), http.StatusInternalServerError)
		return
	}

	metrics.CheckpointTotal.WithLabelValues("success", workload, ns).Inc()
	metrics.CheckpointDuration.WithLabelValues(workload).Observe(duration)
	if result != nil {
		metrics.CheckpointSize.WithLabelValues(workload).Observe(float64(result.CheckpointSize))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// Restore handler
func (a *Agent) restoreHandler(w http.ResponseWriter, r *http.Request) {
	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	metrics.ActiveOperations.WithLabelValues("restore").Inc()
	start := time.Now()

	result, err := a.Restore(r.Context(), req)

	metrics.ActiveOperations.WithLabelValues("restore").Dec()
	duration := time.Since(start).Seconds()
	workload := req.CheckpointID

	if err != nil {
		metrics.RestoreTotal.WithLabelValues("failed", workload, "default").Inc()
		a.log.WithError(err).Error("Restore failed")
		http.Error(w, fmt.Sprintf("restore failed: %v", err), http.StatusInternalServerError)
		return
	}

	metrics.RestoreTotal.WithLabelValues("success", workload, "default").Inc()
	metrics.RestoreDuration.WithLabelValues(workload).Observe(duration)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// List checkpoints handler
func (a *Agent) deleteCheckpointHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	dir := filepath.Join(a.config.CheckpointDir, id)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		http.Error(w, "checkpoint not found", http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete: %v", err), http.StatusInternalServerError)
		return
	}
	a.log.WithField("id", id).Info("Deleted checkpoint")
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) listCheckpointsHandler(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(a.config.CheckpointDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list checkpoints: %v", err), http.StatusInternalServerError)
		return
	}

	checkpoints := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := fmt.Sprintf("%s/%s/metadata.json", a.config.CheckpointDir, entry.Name())
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}

		var metadata map[string]interface{}
		if err := json.Unmarshal(data, &metadata); err != nil {
			continue
		}

		// Add directory size if not in metadata
		if _, ok := metadata["checkpointSize"]; !ok {
			dirPath := filepath.Join(a.config.CheckpointDir, entry.Name())
			var totalSize int64
			_ = filepath.WalkDir(dirPath, func(_ string, d os.DirEntry, _ error) error {
				if d != nil && !d.IsDir() {
					if info, err := d.Info(); err == nil {
						totalSize += info.Size()
					}
				}
				return nil
			})
			metadata["checkpointSize"] = totalSize
		}

		checkpoints = append(checkpoints, metadata)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"checkpoints": checkpoints,
	})
}

// List containers handler
func (a *Agent) listContainersHandler(w http.ResponseWriter, r *http.Request) {
	containers, err := a.runtime.ListContainers(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list containers: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(containers)
}

// GPU processes handler
func (a *Agent) gpuProcessesHandler(w http.ResponseWriter, r *http.Request) {
	pids, err := a.cuda.FindGPUProcesses(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get GPU processes: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"gpuProcesses": pids,
		"timestamp":    time.Now(),
	})
}

// Trigger restore in a placeholder pod
func (a *Agent) triggerRestoreHandler(w http.ResponseWriter, r *http.Request) {
	var req TriggerRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	result, err := a.TriggerRestore(r.Context(), req)
	if err != nil {
		a.log.WithError(err).Error("Trigger restore failed")
		http.Error(w, fmt.Sprintf("trigger restore failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// Get placeholder pod manifest
func (a *Agent) getPlaceholderManifestHandler(w http.ResponseWriter, r *http.Request) {
	var req PlaceholderManifestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	manifest, err := a.GeneratePlaceholderManifest(r.Context(), req)
	if err != nil {
		a.log.WithError(err).Error("Generate manifest failed")
		http.Error(w, fmt.Sprintf("generate manifest failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/yaml")
	_, _ = w.Write([]byte(manifest))
}

// GPU restore handler - called by restore-entrypoint after CRIU restore
func (a *Agent) gpuRestoreHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CheckpointID string `json:"checkpointId"`
		PID          int    `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	// Body-supplied, unlike the {id} routes the middleware covers.
	if err := validPathSegment("checkpointId", req.CheckpointID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log := a.log.WithFields(logrus.Fields{
		"checkpointId": req.CheckpointID,
		"pid":          req.PID,
	})
	log.Info("GPU restore requested")

	// Load checkpoint metadata to check if GPU was used
	checkpointDir := filepath.Join(a.config.CheckpointDir, req.CheckpointID)
	metadataPath := filepath.Join(checkpointDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		log.WithError(err).Warn("Could not read metadata, skipping GPU restore")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "no metadata"})
		return
	}

	var metadata CheckpointMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		log.WithError(err).Warn("Invalid metadata, skipping GPU restore")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "invalid metadata"})
		return
	}

	if metadata.GPUPID == 0 {
		log.Info("No GPU process in checkpoint, skipping GPU restore")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "no GPU"})
		return
	}

	// Restore GPU state
	log.Info("Restoring GPU state")
	if err := a.cuda.Restore(r.Context(), req.PID); err != nil {
		log.WithError(err).Error("GPU restore failed")
		http.Error(w, fmt.Sprintf("GPU restore failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Unlock GPU
	log.Info("Unlocking GPU")
	if err := a.cuda.Unlock(r.Context(), req.PID); err != nil {
		log.WithError(err).Error("GPU unlock failed")
		http.Error(w, fmt.Sprintf("GPU unlock failed: %v", err), http.StatusInternalServerError)
		return
	}

	log.Info("GPU restore completed")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// List files in a checkpoint directory
func (a *Agent) listCheckpointFilesHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	relPath := r.URL.Query().Get("path")

	checkpointDir := filepath.Join(a.config.CheckpointDir, id)

	// Validate checkpoint exists
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		http.Error(w, "checkpoint not found", http.StatusNotFound)
		return
	}

	// Resolve and validate the target path stays within checkpoint dir
	targetDir := checkpointDir
	if relPath != "" {
		cleaned := filepath.Clean(relPath)
		if strings.Contains(cleaned, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		targetDir = filepath.Join(checkpointDir, cleaned)
	}

	// Double-check the resolved path is still under checkpointDir
	absTarget, err := filepath.Abs(targetDir)
	if err != nil || !strings.HasPrefix(absTarget, checkpointDir) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read directory: %v", err), http.StatusInternalServerError)
		return
	}

	type fileEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	files := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"files": files,
		"path":  relPath,
	})
}

// checkpointManifestHandler returns a flat, recursive listing of every
// file under /var/lib/nvsnap/checkpoints/<id>/ with its relative path,
// size, and modtime. Used by the phase 5d.1 peer-fanout receiver to
// drive parallel per-file fetches via the /file endpoint.
//
// Response shape:
//
//	{
//	  "checkpoint_id": "vllm-small__nvsnap-system__20260509-192743",
//	  "total_size":    31356736325,
//	  "file_count":    1234,
//	  "files": [
//	    {"path":"inventory.img",    "size":99,           "mtime":"..."},
//	    {"path":"pages-1.img",      "size":4096,         "mtime":"..."},
//	    {"path":"pages-16.img",     "size":28271087616,  "mtime":"..."},
//	    {"path":"rootfs-diff/etc/passwd", "size":1024,   "mtime":"..."},
//	    ...
//	  ]
//	}
//
// SHA256 is intentionally not computed here — for peer fanout we trust
// the source agent and verify only by file size. Content-addressed
// hashing is added in stage 5d.2 for the nvsnap-blobstore upload path.
func (a *Agent) checkpointManifestHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	checkpointDir := filepath.Join(a.config.CheckpointDir, id)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		http.Error(w, "checkpoint not found", http.StatusNotFound)
		return
	}

	type manifestFile struct {
		Path  string `json:"path"`
		Size  int64  `json:"size"`
		Mtime string `json:"mtime"`
	}

	var files []manifestFile
	var totalSize int64

	err := filepath.Walk(checkpointDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(checkpointDir, path)
		if err != nil {
			return err
		}
		files = append(files, manifestFile{
			Path:  rel,
			Size:  info.Size(),
			Mtime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
		totalSize += info.Size()
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("walk: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"checkpoint_id": id,
		"total_size":    totalSize,
		"file_count":    len(files),
		"files":         files,
	})
}

// ensureLocalHandler runs EnsureLocal synchronously and returns once
// the checkpoint dir is materialized on this node (or all tiers
// failed). The restore caller polls /v1/checkpoints/{id}/files to
// confirm before launching the placeholder. We deliberately block
// here rather than streaming progress so the caller's wait semantics
// stay simple — total time is bounded by per-file timeouts inside
// the cascade.
func (a *Agent) ensureLocalHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id == "" {
		http.Error(w, "checkpoint id required", http.StatusBadRequest)
		return
	}
	if err := a.EnsureLocal(r.Context(), id); err != nil {
		a.log.WithError(err).WithField("checkpoint_id", id).Warn("ensure-local cascade failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ensureCaptureLocalHandler is the rootfs analog of ensureLocalHandler.
// Wraps EnsureCaptureLocal (capture_cascade.go) so external callers
// (webhook init container, bench tooling) can trigger a cross-node
// rootfs cascade fetch on demand. Synchronous: returns 204 after the
// per-hash rootfs cache is populated on this node, or 502 if all peer
// tiers failed.
//
// GH #114: today's webhook pins restore pods to CapturedOnNodes via
// nodeAffinity, blocking multi-node fan-out. Wiring an init container
// (or webhook side-effect) to POST here is the next step.
func (a *Agent) ensureCaptureLocalHandler(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if hash == "" {
		http.Error(w, "capture hash required", http.StatusBadRequest)
		return
	}
	if err := a.EnsureCaptureLocal(r.Context(), hash); err != nil {
		a.log.WithError(err).WithField("hash", hash).Warn("ensure-capture-local cascade failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Read a file from a checkpoint directory
func (a *Agent) readCheckpointFileHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}

	checkpointDir := filepath.Join(a.config.CheckpointDir, id)

	// Validate checkpoint exists
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		http.Error(w, "checkpoint not found", http.StatusNotFound)
		return
	}

	// Resolve + boundary-check the path, following symlinks so a link
	// committed inside the checkpoint tree can't escape it (nvsnap#92).
	targetFile, err := resolveWithinRoot(checkpointDir, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			http.Error(w, "invalid path", http.StatusBadRequest)
		}
		return
	}

	info, err := os.Stat(targetFile)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	// Stream the file. Phase 5d.1 uses this endpoint for peer-to-peer
	// CRIU image fetches, so we MUST handle multi-GB files
	// (pages-*.img can be 28+ GB on vLLM/sglang). Use http.ServeContent
	// to get streaming + Range support (parallel range fetches let the
	// receiver saturate network bandwidth on a single file).
	f, err := os.Open(targetFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to open file: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	// Content type based on extension. Most CRIU images are
	// application/octet-stream; small textual files get text/plain so
	// the UI viewer (CheckpointDetail.tsx isViewable() set) and any
	// axios/fetch client without explicit responseType: 'text' handles
	// them as text. Keep this list in sync with ui/src/pages/
	// CheckpointDetail.tsx's isViewable regex.
	ext := strings.ToLower(filepath.Ext(targetFile))
	switch ext {
	case ".json", ".log", ".txt", ".cfg", ".conf",
		".yaml", ".yml", ".md", ".toml", ".sh":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// http.ServeContent handles Last-Modified, ETag, Range, conditional
	// GET, etc. For peer fanout, Range support is the critical feature —
	// receivers can pull one large pages-*.img via parallel ranges.
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
