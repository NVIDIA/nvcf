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

// Package server provides the K8s-aware NVSNAP API server.
// It discovers GPU nodes/pods via K8s API and proxies checkpoint/restore
// operations to nvsnap-agent instances running on each node.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

var (
	checkpointGVR = schema.GroupVersionResource{Group: "nvsnap.io", Version: "v1alpha1", Resource: "gpucheckpoints"}
	restoreGVR    = schema.GroupVersionResource{Group: "nvsnap.io", Version: "v1alpha1", Resource: "gpurestores"}
	// snapshot.storage.k8s.io VolumeSnapshot — not in client-go core,
	// must go through the dynamic client. Used by the L2 cascade in
	// deleteCheckpoint to clean up snap-<short-hash> after a row's
	// content artifacts are gone (nvsnap#137).
	volumeSnapshotGVR = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
	gpuResource       = corev1.ResourceName("nvidia.com/gpu")
)

// Config holds server configuration.
type Config struct {
	Address      string
	AgentPort    int    // Port of nvsnap-agent on nodes (default: 8081)
	BlobstoreURL string // Base URL of nvsnap-blobstore (default: in-cluster service)
	// ManifestNamespace is where the agent's ConfigMapBackend writes
	// rootfs capture manifest CMs. The agent writes them in its own
	// namespace (nvsnap-system), NOT the source pod's namespace, so
	// dispatchRootfsCapture must poll here — not the source pod ns —
	// to correlate a capture. Default "nvsnap-system". (Before this was
	// wired, nvsnap-server polled the source pod ns, never found the CM,
	// and every NVCA rootfs capture timed out at 15m → Failed → no
	// pvc_promote_state=ready → no restore. GCP-H100-a 2026-06-10.)
	ManifestNamespace string
}

// Server is the K8s-aware NVSNAP API server.
type Server struct {
	config     Config
	kubeClient kubernetes.Interface
	dynClient  dynamic.Interface
	httpClient *http.Client
	router     *mux.Router
	log        *logrus.Entry
	demo       *demoSession
	hub        *Hub
	catalog    *db.DB
	// obsCache TTL-caches the observability discovery (Grafana /
	// Jaeger / Prometheus Service existence) so the UI's nav refresh
	// doesn't hammer the K8s API. See internal/server/observability_proxy.go.
	obsCache *observabilityCache
}

// New creates a new server.
func New(cfg Config, kubeClient kubernetes.Interface, dynClient dynamic.Interface, catalog *db.DB) *Server {
	if cfg.AgentPort == 0 {
		cfg.AgentPort = 8081
	}
	if cfg.BlobstoreURL == "" {
		cfg.BlobstoreURL = "http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000"
	}
	if cfg.ManifestNamespace == "" {
		cfg.ManifestNamespace = "nvsnap-system"
	}
	log := logrus.WithField("component", "server")
	s := &Server{
		config:     cfg,
		kubeClient: kubeClient,
		dynClient:  dynClient,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		log:        log,
		demo:       newDemoSession(),
		hub:        newHub(log),
		catalog:    catalog,
		obsCache:   &observabilityCache{},
	}
	s.setupRoutes()
	return s
}

// Router returns the HTTP router for embedding UI static files.
func (s *Server) Router() *mux.Router {
	return s.router
}

// Handler returns the full HTTP handler chain (CORS + router).
func (s *Server) Handler() http.Handler {
	return s.corsMiddleware(s.router)
}

// SetUI configures the server to serve an embedded filesystem as the UI.
// The fsys should contain a "dist" subdirectory with the built React app.
// All non-API paths will serve static files with SPA fallback to index.html.
func (s *Server) SetUI(fsys fs.FS) {
	// Strip the "dist" prefix from the embedded FS
	distFS, err := fs.Sub(fsys, "dist")
	if err != nil {
		s.log.WithError(err).Warn("Failed to open embedded UI dist directory")
		return
	}

	fileServer := http.FileServer(http.FS(distFS))

	// SPA fallback: serve static files, fall back to index.html for unknown paths
	s.router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try to open the file
		if f, err := distFS.Open(path); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routing
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	s.log.Info("Embedded UI configured at /")
}

func (s *Server) setupRoutes() {
	metrics.RegisterServer()
	s.router = mux.NewRouter()

	// Prometheus metrics endpoint
	s.router.Handle("/metrics", metrics.Handler()).Methods("GET")

	// Single-pane observability — reverse proxy the in-cluster
	// Grafana/Jaeger/Prometheus subcharts under one external URL.
	// Mounted BEFORE the /api/v1 subrouter so /observability/ paths
	// aren't shadowed by API routes. See observability_proxy.go.
	s.router.PathPrefix("/observability/").Handler(s.observabilityProxyHandler())

	api := s.router.PathPrefix("/api/v1").Subrouter()
	// /api/v1/observability — UI nav discovery
	api.HandleFunc("/observability", s.listObservabilityHandler).Methods("GET")

	// Nodes
	api.HandleFunc("/nodes", s.listNodes).Methods("GET")
	api.HandleFunc("/nodes/{name}/pods", s.listNodePods).Methods("GET")

	// Pods
	api.HandleFunc("/pods", s.listPods).Methods("GET")

	// Checkpoints
	api.HandleFunc("/checkpoints", s.createCheckpoint).Methods("POST")
	api.HandleFunc("/checkpoints", s.listCheckpoints).Methods("GET")
	api.HandleFunc("/checkpoints/{id}", s.getCheckpoint).Methods("GET")
	api.HandleFunc("/checkpoints/{id}", s.deleteCheckpoint).Methods("DELETE")
	// nvsnap#137: hash-keyed delete for admin tooling. Same cascade
	// shape as DELETE by id but lets the caller target every sibling
	// row at once without a round-trip to look up an id first.
	api.HandleFunc("/checkpoints/by-hash/{hash}", s.deleteCheckpointByHash).Methods("DELETE")
	api.HandleFunc("/checkpoints/{id}/files", s.listCheckpointFiles).Methods("GET")
	api.HandleFunc("/checkpoints/{id}/file", s.readCheckpointFile).Methods("GET")

	// Phase 5d.1: peer fanout catalog routing.
	// Restore-side cascade reads /sources to learn where to fetch from.
	// Agents POST peer-add / peer-remove as their local cache changes.
	api.HandleFunc("/checkpoints/{id}/sources", s.getCheckpointSources).Methods("GET")
	api.HandleFunc("/checkpoints/{id}/peer-add", s.peerAddCheckpoint).Methods("POST")
	api.HandleFunc("/checkpoints/{id}/peer-remove", s.peerRemoveCheckpoint).Methods("POST")
	api.HandleFunc("/checkpoints/{id}/peers", s.listPeersCheckpoint).Methods("GET")
	// Phase 5d.2: agent registers a checkpoint in the catalog after
	// a direct API-driven capture. Anchors all later catalog ops
	// (peer-add, blob-uploaded, sources routing).
	api.HandleFunc("/checkpoints/register", s.registerCheckpoint).Methods("POST")
	// nvsnap#59: content-addressed lookup. NVCA's Hook A POSTs the
	// canonical workload identity (imageRef + modelID + flags +
	// driverMajor) here to find a restorable artifact across fvIDs.
	// Returns matches sorted freshest-first. Indexed by hash + image_ref
	// in the catalog DB.
	api.HandleFunc("/checkpoints/lookup", s.lookupCheckpoint).Methods("POST")
	// Phase 5d.2: agent uploader reports successful blob-store upload.
	// Sets the s3_uri column so /sources can return it as the tier-3
	// fallback for cross-node restore.
	api.HandleFunc("/checkpoints/{id}/blob-uploaded", s.blobUploadedCheckpoint).Methods("POST")
	// nvsnap#63 / nvsnap#76: agent-driven L2 PVC state machine. The
	// PerCapturePVCBackend on the agent walks pending → writing →
	// snapshotting → ready (or → failed) and POSTs each transition
	// here so restore-side resolvers can poll lookup?hash=... and
	// observe the L2 state. Agent doesn't have direct DB access — this
	// is the only write path for pvc_promote_state / pvc_name.
	//
	// Keyed by content hash, not catalog id: the L2 artifact
	// (rox-<short-hash>) is hash-keyed, so the state-machine writes
	// fan out across every row sharing that hash.
	api.HandleFunc("/checkpoints/by-hash/{hash}/pvc-state", s.updatePVCPromoteStateByHash).Methods("POST")

	// nvsnap#147: GET symmetric to the POST above. The nvsnap-init init
	// container that the mutating webhook injects on restore pods polls
	// this endpoint until state == "ready" before exec'ing the
	// inference container — that's how we block on the snap+clone of
	// rox-<short-hash> without polling K8s API from the customer pod
	// (nvsnap-init has no K8s creds; nvsnap-server does).
	api.HandleFunc("/checkpoints/by-hash/{hash}/pvc-state", s.getPVCPromoteStateByHash).Methods("GET")

	// Restores
	api.HandleFunc("/restores", s.createRestore).Methods("POST")
	api.HandleFunc("/restores", s.listRestores).Methods("GET")
	api.HandleFunc("/restores/{id}", s.getRestore).Methods("GET")

	// Retention policies
	api.HandleFunc("/retention-policies", s.listRetentionPolicies).Methods("GET")
	api.HandleFunc("/retention-policies", s.createRetentionPolicy).Methods("POST")
	api.HandleFunc("/retention-policies/{id}", s.getRetentionPolicy).Methods("GET")
	api.HandleFunc("/retention-policies/{id}", s.updateRetentionPolicy).Methods("PUT")
	api.HandleFunc("/retention-policies/{id}", s.deleteRetentionPolicy).Methods("DELETE")
	api.HandleFunc("/retention-policies/{id}/preview", s.previewRetentionPolicy).Methods("GET")

	// Audit log
	api.HandleFunc("/audit", s.listAuditLog).Methods("GET")

	// WebSocket
	api.HandleFunc("/ws", s.handleWebSocket)

	// Health
	api.HandleFunc("/health", s.health).Methods("GET")

	// OpenAPI 3.1 spec + Scalar-rendered docs. Source of truth is
	// internal/server/openapi.yaml; this serves the spec in YAML/JSON
	// and a one-page reference UI.
	api.HandleFunc("/openapi.yaml", s.openapiYAML).Methods("GET")
	api.HandleFunc("/openapi.json", s.openapiJSON).Methods("GET")
	api.HandleFunc("/docs", s.docs).Methods("GET")

	// Demo
	api.HandleFunc("/demo/workloads", s.demoWorkloads).Methods("GET")
	api.HandleFunc("/demo/state", s.demoGetState).Methods("GET")
	api.HandleFunc("/demo/deploy", s.demoDeploy).Methods("POST")
	api.HandleFunc("/demo/inference", s.demoInference).Methods("POST")
	api.HandleFunc("/demo/checkpoint", s.demoCheckpoint).Methods("POST")
	api.HandleFunc("/demo/restore", s.demoRestore).Methods("POST")
	api.HandleFunc("/demo/scale-out", s.demoScaleOut).Methods("POST")
	api.HandleFunc("/demo/workload", s.demoCleanup).Methods("DELETE")
	api.HandleFunc("/demo/checkpoint/files", s.demoCheckpointFiles).Methods("GET")
	api.HandleFunc("/demo/checkpoint/file", s.demoCheckpointFile).Methods("GET")
	api.HandleFunc("/demo/pods", s.demoPods).Methods("GET")
	api.HandleFunc("/demo/manifest", s.demoManifest).Methods("GET")
	api.HandleFunc("/demo/test-pods", s.demoCleanTestPods).Methods("DELETE")

	// Blobstore: proxy aggregation endpoints from nvsnap-blobstore for
	// the UI. Read-only; raw blob/manifest endpoints are not exposed.
	api.HandleFunc("/blobstore/stats", s.blobstoreStats).Methods("GET")
	api.HandleFunc("/blobstore/captures", s.blobstoreListCaptures).Methods("GET")

	// Middleware (applied to matched routes)
	s.router.Use(metrics.InstrumentRoute())
	s.router.Use(s.loggingMiddleware)
}

// getUser extracts the user identity from the X-User header.
// Falls back to "anonymous" if not set.
func getUser(r *http.Request) string {
	if u := r.Header.Get("X-User"); u != "" {
		return u
	}
	return "anonymous"
}

// Run starts the HTTP server.
func (s *Server) Run(ctx context.Context) error {
	// CORS wraps the entire router so it applies to all requests including
	// OPTIONS preflight requests that don't match specific route methods.
	// otelhttp emits one server span per request (no-op unless tracing is
	// enabled), so every API route is traced without per-handler work.
	handler := otelhttp.NewHandler(s.corsMiddleware(s.router), "nvsnap-server")

	srv := &http.Server{
		Addr:         s.config.Address,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 10 * time.Minute, // Long timeout for checkpoint proxy
		IdleTimeout:  60 * time.Second,
	}

	s.log.WithField("address", s.config.Address).Info("Starting NVSNAP API server")

	// Restore demo state from DB before starting
	s.LoadDemoState()

	go s.hub.Run()
	go s.demoPodPoller(ctx)
	go s.retentionEnforcer(ctx)

	// Reconciler: ingest rootfs-only captures from K8s ConfigMaps into
	// the catalog DB. The cluster (CMs labelled nvsnap.io/kind=
	// rootfs-capture-manifest) is the source of truth; this loop keeps
	// the UI's Checkpoints list in sync with captures created outside
	// the server (agent watcher auto-captures, direct API calls).
	rec := &Reconciler{
		KubeClient: s.kubeClient,
		Catalog:    s.catalog,
		Namespace:  captureManifestNamespace,
		Interval:   30 * time.Second,
		Log:        s.log.WithField("subsys", "reconciler"),
	}
	go rec.Run(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return srv.ListenAndServe()
}

// --- Node handlers ---

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.kubeClient.CoreV1().Nodes().List(r.Context(), metav1.ListOptions{
		LabelSelector: "nvidia.com/gpu.present=true",
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]map[string]interface{}, 0, len(nodes.Items))
	for i := range nodes.Items {
		node := &nodes.Items[i]
		ip := nodeInternalIP(node)

		gpuCount := 0
		if q, ok := node.Status.Allocatable[gpuResource]; ok {
			gpuCount = int(q.Value())
		}

		pods, _ := s.gpuPodsOnNode(r.Context(), node.Name)

		result = append(result, map[string]interface{}{
			"name":       node.Name,
			"status":     nodeConditionStatus(node),
			"gpuCount":   gpuCount,
			"gpuModel":   node.Labels["nvidia.com/gpu.product"],
			"agentReady": s.checkAgentHealth(r.Context(), ip),
			"podCount":   len(pods),
			"internalIP": ip,
			"createdAt":  node.CreationTimestamp.Time,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": result,
		"count": len(result),
	})
}

func (s *Server) listNodePods(w http.ResponseWriter, r *http.Request) {
	nodeName := mux.Vars(r)["name"]
	pods, err := s.gpuPodsOnNode(r.Context(), nodeName)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods":  pods,
		"count": len(pods),
	})
}

// --- Pod handlers ---

func (s *Server) listPods(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace") // empty = all namespaces
	pods, err := s.gpuPods(r.Context(), ns)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods":  pods,
		"count": len(pods),
	})
}

// --- Checkpoint handlers ---

func (s *Server) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PodName       string `json:"podName"`
		Namespace     string `json:"namespace"`
		ContainerName string `json:"containerName,omitempty"`
		LeaveRunning  bool   `json:"leaveRunning,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.PodName == "" || req.Namespace == "" {
		s.writeError(w, http.StatusBadRequest, "podName and namespace required")
		return
	}

	// Look up pod to find its node
	pod, err := s.kubeClient.CoreV1().Pods(req.Namespace).Get(r.Context(), req.PodName, metav1.GetOptions{})
	if err != nil {
		s.writeError(w, http.StatusNotFound, "pod not found: "+err.Error())
		return
	}
	if pod.Spec.NodeName == "" {
		s.writeError(w, http.StatusBadRequest, "pod not scheduled to a node")
		return
	}

	// Create GPUCheckpoint CRD
	name := fmt.Sprintf("%s-%d", req.PodName, time.Now().Unix())
	crd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvsnap.io/v1alpha1",
			"kind":       "GPUCheckpoint",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": req.Namespace,
			},
			"spec": map[string]interface{}{
				"podName":       req.PodName,
				"containerName": req.ContainerName,
				"leaveRunning":  req.LeaveRunning,
			},
		},
	}
	created, err := s.dynClient.Resource(checkpointGVR).Namespace(req.Namespace).Create(r.Context(), crd, metav1.CreateOptions{})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "create CRD: "+err.Error())
		return
	}

	s.setCheckpointStatus(r.Context(), created, "InProgress", pod.Spec.NodeName, "", "", 0, "")

	// Dual-write to DB catalog
	now := time.Now().UTC()
	if err := s.catalog.CreateCheckpoint(&db.Checkpoint{
		ID:            name,
		CheckpointID:  name,
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		ContainerName: req.ContainerName,
		NodeName:      pod.Spec.NodeName,
		Status:        "InProgress",
		HasGPU:        true,
		StartedAt:     &now,
	}); err != nil {
		s.log.WithError(err).Warn("Failed to write checkpoint to DB")
	}

	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action: "checkpoint.create", Resource: "checkpoint", ResourceID: name,
		Actor: getUser(r), Message: fmt.Sprintf("Checkpoint started for pod %s/%s on %s", req.Namespace, req.PodName, pod.Spec.NodeName),
	})

	// Route by GPU count. Multi-GPU pods can't be checkpointed via
	// CRIU + cuda-checkpoint (blocked on the libcudart wall) — they
	// use the rootfs-only path instead. Single-GPU goes through CRIU.
	// Same API contract for the caller; auto-routing here keeps every
	// upstream (UI, nvsnap CLI, kubectl-nvsnap, NVCA Hook B) agnostic.
	if podGPUCount(pod) >= 2 {
		go s.runRootfsCheckpoint(name, req.Namespace, req.PodName, string(pod.UID), pod.Spec.NodeName)
	} else {
		go s.runCheckpoint(name, req.Namespace, pod.Spec.NodeName, req.PodName, req.ContainerName, req.LeaveRunning)
	}

	s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"id":        name,
		"namespace": req.Namespace,
		"phase":     "InProgress",
		"message":   "Checkpoint started",
	})
}

func (s *Server) runCheckpoint(name, namespace, nodeName, podName, containerName string, leaveRunning bool) {
	ctx := context.Background()
	log := s.log.WithFields(logrus.Fields{
		"checkpoint": name,
		"pod":        podName,
		"node":       nodeName,
	})

	ip, err := s.nodeIP(ctx, nodeName)
	if err != nil {
		log.WithError(err).Error("Failed to find node IP")
		s.updateCheckpointCRD(ctx, name, namespace, "Failed", nodeName, "", "", 0, err.Error())
		_ = s.catalog.UpdateCheckpointStatus(name, "Failed", err.Error(), "", "", 0, 0)
		return
	}

	// Proxy to agent
	agentReq, _ := json.Marshal(map[string]interface{}{
		"namespace":     namespace,
		"podName":       podName,
		"containerName": containerName,
		"leaveRunning":  leaveRunning,
	})
	url := fmt.Sprintf("http://%s:%d/v1/checkpoint", ip, s.config.AgentPort)
	log.WithField("url", url).Info("Proxying checkpoint to agent")

	agentHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(agentReq))
	if err != nil {
		log.WithError(err).Error("Failed to build agent request")
		s.updateCheckpointCRD(ctx, name, namespace, "Failed", nodeName, "", "", 0, err.Error())
		_ = s.catalog.UpdateCheckpointStatus(name, "Failed", err.Error(), "", "", 0, 0)
		return
	}
	agentHTTPReq.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(agentHTTPReq)
	if err != nil {
		log.WithError(err).Error("Agent request failed")
		s.updateCheckpointCRD(ctx, name, namespace, "Failed", nodeName, "", "", 0, err.Error())
		_ = s.catalog.UpdateCheckpointStatus(name, "Failed", err.Error(), "", "", 0, 0)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		msg := string(respBody)
		// Agent returns HTTP 422 with body {"redirect":"rootfs",...} when
		// it wants the caller to switch capture paths. Two reasons fire
		// this today (internal/agent/nim_backend.go + checkpoint.go):
		//
		//  a) Per-backend hard-required — Riva or Triton NIMs (their
		//     teardown abort post-restore makes CRIU unusable).
		//  b) Cluster-wide rootfs default (v0.0.48+) — operator set
		//     NVSNAP_DEFAULT_CAPTURE_PATH=rootfs (now the default).
		//
		// nvsnap-server already implements the rootfs path via
		// runRootfsCheckpoint for the multi-GPU auto-route case. Reuse
		// it here so callers (NVCA Hook B, kubectl-nvsnap, UI, anyone
		// hitting /api/v1/checkpoints) get a single contract: POST,
		// poll for Completed. No client needs to learn the redirect.
		if resp.StatusCode == http.StatusUnprocessableEntity && isRootfsRedirect(respBody) {
			log.WithField("status", resp.StatusCode).Info("Agent requested rootfs redirect — handing off to runRootfsCheckpoint")
			s.runRootfsCheckpoint(name, namespace, podName, "", nodeName)
			return
		}
		log.WithField("status", resp.StatusCode).Error("Agent returned error: " + msg)
		s.updateCheckpointCRD(ctx, name, namespace, "Failed", nodeName, "", "", 0, msg)
		_ = s.catalog.UpdateCheckpointStatus(name, "Failed", msg, "", "", 0, 0)
		return
	}

	// Parse agent response. Hash is the new (nvsnap#61) field — the
	// content-addressed identity of the capture derived by the agent
	// from CatalogInfo. Without it the catalog row stays hash-less and
	// NVCA Hook B can't mark Warm.
	var result struct {
		CheckpointID   string  `json:"checkpointId"`
		CheckpointPath string  `json:"checkpointPath"`
		CheckpointSize int64   `json:"checkpointSize"`
		Duration       float64 `json:"durationSeconds"`
		Hash           string  `json:"hash"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.WithError(err).Warn("decode agent checkpoint response; status fields may be empty")
	}

	log.WithFields(logrus.Fields{
		"path":     result.CheckpointPath,
		"size":     result.CheckpointSize,
		"duration": result.Duration,
		"hash":     result.Hash,
	}).Info("Checkpoint completed")

	msg := fmt.Sprintf("Completed in %.1fs, checkpoint ID: %s", result.Duration, result.CheckpointID)
	s.updateCheckpointCRD(ctx, name, namespace, "Completed", nodeName,
		result.CheckpointPath, result.Hash, result.CheckpointSize, msg)
	_ = s.catalog.UpdateCheckpointStatus(name, "Completed", msg,
		result.CheckpointPath, result.Hash, result.CheckpointSize, result.Duration)
	// Same authoritative pvc_promote_state=ready finalize as rootfs (see
	// runRootfsCheckpoint). CRIU's PerCapturePVCBackend.setState during
	// agent-side promote already covers this in normal cases, but the
	// 404-tolerant setState in v0.0.50+ means we shouldn't rely on it
	// having succeeded — this is the belt-and-suspenders write.
	s.maybeMarkPVCPromoteReady(ctx, namespace, result.Hash, log)
}

// runRootfsCheckpoint is the multi-GPU counterpart to runCheckpoint.
// CRIU + cuda-checkpoint can't handle TP>1 (libcudart wall), so we
// fall back to rootfs-only capture: label the pod with
// nvsnap.io/capture=true so the agent's rootfs watcher acts on it, then
// poll for the manifest ConfigMap keyed by pod UID. Returns the
// content-hash + total size on success.
//
// Same catalog/CRD shape as runCheckpoint so every API caller (UI,
// generic /checkpoints, NVCA Hook B) sees one contract regardless of
// path.
func (s *Server) runRootfsCheckpoint(name, namespace, podName, podUID, nodeName string) {
	ctx := context.Background()
	log := s.log.WithFields(logrus.Fields{
		"checkpoint": name,
		"pod":        podName,
		"path":       "rootfs",
	})

	hash, size, err := s.dispatchRootfsCapture(ctx, namespace, podName, podUID, log)
	if err != nil {
		log.WithError(err).Error("Rootfs capture failed")
		s.updateCheckpointCRD(ctx, name, namespace, "Failed", nodeName, "", "", 0, err.Error())
		_ = s.catalog.UpdateCheckpointStatus(name, "Failed", err.Error(), "", "", 0, 0)
		return
	}

	// Pre-nvsnap#61 this passed `hash` into the `path` parameter
	// (positional bug). The rootfs path has no on-disk file path on
	// the server — the content is keyed by hash and lives in the
	// per-capture PVC. So path stays empty; hash is the new explicit
	// param.
	msg := fmt.Sprintf("Rootfs capture committed, hash %s, size %d bytes", checkpointstore.ShortHash(hash), size)
	log.WithFields(logrus.Fields{"hash": hash, "size": size}).Info("Rootfs capture completed")
	s.updateCheckpointCRD(ctx, name, namespace, "Completed", nodeName, "", hash, size, msg)
	_ = s.catalog.UpdateCheckpointStatus(name, "Completed", msg, "", hash, size, 0)
	// Authoritative pvc_promote_state=ready write. The agent-side chain
	// (PerCapturePVCBackend in nvsnap-agent v0.0.49+) tries to set this
	// during Put, but its setState calls fire BEFORE the catalog row
	// has this hash populated — they hit 404 and are tolerated. Once
	// UpdateCheckpointStatus above writes the hash, the row is keyed
	// by hash and we can finalize the L2 state if a rox PVC exists.
	s.maybeMarkPVCPromoteReady(ctx, namespace, hash, log)
}

// maybeMarkPVCPromoteReady sets pvc_promote_state=ready on the catalog
// row when the L2 rox PVC exists in the source pod's namespace. Called
// once at end-of-capture by runRootfsCheckpoint and runCheckpoint after
// the catalog row has its hash populated; the corresponding agent-side
// writes during Backend.Put are tolerated as 404s because the hash
// isn't there yet.
//
// No-op if the rox PVC doesn't exist — captures that bypass L2 (no
// StorageClass configured, agent on a node without L2 enabled, etc.)
// stay with empty pvc_promote_state and the restore-side resolver
// falls back to L3/L4.
func (s *Server) maybeMarkPVCPromoteReady(ctx context.Context, namespace, hash string, log *logrus.Entry) {
	if hash == "" {
		return
	}
	roxName := "rox-" + checkpointstore.ShortHash(hash)
	pvc, err := s.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, roxName, metav1.GetOptions{})
	if err != nil {
		// NotFound is the no-L2 case; expected and not an error.
		// Other errors get logged but don't fail the capture.
		log.WithError(err).WithField("rox_pvc", roxName).Debug("rox PVC lookup for promote-state finalize")
		return
	}
	n, err := s.catalog.UpdatePVCPromoteStateByHash(hash, "ready", pvc.Name)
	if err != nil {
		log.WithError(err).WithField("rox_pvc", roxName).Warn("pvc_promote_state=ready write failed; restore may fall back to L3/L4")
		return
	}
	log.WithFields(logrus.Fields{"rox_pvc": roxName, "rows_updated": n}).Info("pvc_promote_state=ready set after capture finalize")
}

// dispatchRootfsCapture is the path-shared rootfs helper: idempotently
// labels the pod for the agent's watcher, then polls the ConfigMap
// registry for the resulting manifest. Returns (hash, totalBytes, err).
//
// Callable from both the generic /api/v1/checkpoints flow (via
// runRootfsCheckpoint) and the Demo state machine — one
// implementation of "what is a rootfs checkpoint?".
func (s *Server) dispatchRootfsCapture(ctx context.Context, namespace, podName, podUID string, log *logrus.Entry) (hash string, totalBytes int64, err error) {
	// Step 1: ensure the capture label is present. Idempotent — the
	// canonical demo manifests already carry it, but arbitrary BYOC
	// pods (NVCA, kubectl-nvsnap) may not.
	pod, err := s.kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("get pod: %w", err)
	}
	if podUID == "" {
		podUID = string(pod.UID)
	}
	if pod.Labels[rootfsCaptureLabel] != "true" {
		patch := []byte(`{"metadata":{"labels":{"nvsnap.io/capture":"true"}}}`)
		if _, err := s.kubeClient.CoreV1().Pods(namespace).Patch(
			ctx, podName, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return "", 0, fmt.Errorf("label pod %s/%s: %w", namespace, podName, err)
		}
		log.Info("Labeled pod nvsnap.io/capture=true")
	}

	// Step 2: poll for the manifest ConfigMap whose SourcePodMeta.uid
	// matches this pod. The agent's watcher waits a warmup window
	// (default 1min) before capturing, plus the capture itself runs
	// for minutes on large workloads — keep the deadline generous.
	//
	// CRITICAL: list in the MANIFEST namespace (nvsnap-system), NOT the
	// source pod's namespace. The agent's ConfigMapBackend writes
	// manifest CMs in its own namespace, not the workload's — so
	// polling `namespace` here never finds the CM and every NVCA
	// rootfs capture timed out at 15m → Failed → no restore.
	// GCP-H100-a 2026-06-10.
	cmNamespace := s.config.ManifestNamespace
	deadline := time.Now().Add(rootfsCaptureTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		cms, listErr := s.kubeClient.CoreV1().ConfigMaps(cmNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: checkpointstore.CMLabelKind + "=" + checkpointstore.CMLabelKindCapture,
		})
		if listErr == nil {
			for i := range cms.Items {
				cm := &cms.Items[i]
				manifestJSON, ok := cm.Data[checkpointstore.CMDataKey]
				if !ok {
					continue
				}
				var m checkpointstore.Manifest
				if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
					continue
				}
				if m.SourcePodMeta["uid"] != podUID {
					continue
				}
				var total int64
				for _, v := range m.Volumes {
					total += v.SizeBytes
				}
				return m.Hash, total, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(rootfsPollInterval):
		}
	}
	return "", 0, errors.New("rootfs capture not committed within " + rootfsCaptureTimeout.String() + " (watcher timeout or capture failed — check agent logs)")
}

const (
	rootfsCaptureLabel   = "nvsnap.io/capture"
	rootfsCaptureTimeout = 15 * time.Minute
	rootfsPollInterval   = 5 * time.Second
)

// isRootfsRedirect reports whether an agent error response is the
// rootfs-redirect HTTP 422 (`{"redirect":"rootfs",...}`). The agent emits
// this body from BackendRedirectError when DetectBackend identifies a
// Riva/Triton workload OR RootfsIsDefault() returns true (v0.0.48+). The
// caller of /v1/checkpoint should hand the work to runRootfsCheckpoint
// instead of marking the catalog row Failed.
//
// Parses the JSON loosely — any body that contains a top-level "redirect"
// field equal to "rootfs" counts. Robust against agent versions that add
// new fields like "reason" or "backend".
func isRootfsRedirect(body []byte) bool {
	var v struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return strings.EqualFold(v.Redirect, "rootfs")
}

func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")

	if source == "db" {
		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				limit = n
			}
		}
		filter := db.CheckpointFilter{
			Namespace:    r.URL.Query().Get("namespace"),
			PodName:      r.URL.Query().Get("podName"),
			NodeName:     r.URL.Query().Get("nodeName"),
			Status:       r.URL.Query().Get("status"),
			WorkloadType: r.URL.Query().Get("workloadType"),
			Cursor:       r.URL.Query().Get("cursor"),
			Limit:        limit,
			SortOrder:    r.URL.Query().Get("sort"),
		}
		paged, err := s.catalog.ListCheckpointsPaged(filter)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"checkpoints": paged.Items,
			"count":       len(paged.Items),
			"total":       paged.Total,
			"nextCursor":  paged.NextCursor,
			"hasMore":     paged.HasMore,
			"source":      "db",
		})
		return
	}

	// Explicit CRD-backed listing (debug): the raw GPUCheckpoint CRDs.
	if source == "crd" {
		ns := r.URL.Query().Get("namespace") // empty = all namespaces
		list, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var checkpoints []map[string]interface{}
		for _, item := range list.Items {
			checkpoints = append(checkpoints, flattenCRD(&item))
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"checkpoints": checkpoints,
			"count":       len(checkpoints),
		})
		return
	}

	// Default (and source=agent): the unified, tier-agnostic capture view.
	// The DB catalog is the cross-tier index — one row per capture hash,
	// pointing at wherever the bytes live (L1 node / L2 PVC / L4 blobstore) —
	// so we show it deduped (collapsing the historical pod-id + hash-id
	// double-write), then fold in any live agent-local checkpoints not yet
	// cataloged. Net: the UI shows every capture regardless of where it lives,
	// once per capture.
	rows, err := s.catalog.ListCheckpointsDeduped(db.CheckpointFilter{
		Namespace: r.URL.Query().Get("namespace"),
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]interface{}, 0, len(rows))
	seen := make(map[string]bool, len(rows)*2)
	for i := range rows {
		c := &rows[i]
		out = append(out, rows[i])
		if c.ID != "" {
			seen[c.ID] = true
		}
		if c.Hash != "" {
			seen[c.Hash] = true
		}
	}
	for _, a := range s.listAgentCheckpoints(r.Context()) {
		id, _ := a["id"].(string)
		h, _ := a["hash"].(string)
		if (id != "" && seen[id]) || (h != "" && seen[h]) {
			continue
		}
		out = append(out, a)
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"checkpoints": out,
		"count":       len(out),
		"source":      "unified",
	})
}

func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Try agent source first — most checkpoints are created via agent
	agentCheckpoints := s.listAgentCheckpoints(r.Context())
	for _, ckpt := range agentCheckpoints {
		if ckptID, ok := ckpt["id"].(string); ok && ckptID == id {
			s.writeJSON(w, http.StatusOK, ckpt)
			return
		}
	}

	// DB catalog — where rootfs captures live (under rootfs-everywhere the
	// agent source is empty). Match on id OR content hash against the same
	// deduped view the list endpoint serves, so detail and list agree (the
	// list shows the hash-normalized id; the URL carries that id). Without
	// this, a cataloged rootfs capture 404s on detail despite showing in
	// the list (nvsnap: "shows in catalog but checkpoint not found").
	ns := r.URL.Query().Get("namespace")
	if rows, err := s.catalog.ListCheckpointsDeduped(db.CheckpointFilter{Namespace: ns}); err == nil {
		if c, ok := db.MatchCheckpointByIDOrHash(rows, id); ok {
			// Enrich from the capture manifest: capture method + any
			// container/GPU metadata the agent recorded that the catalog
			// row doesn't carry.
			s.enrichFromManifest(r.Context(), &c)
			s.writeJSON(w, http.StatusOK, c)
			return
		}
	}

	// Fallback to CRD
	if ns == "" {
		item, found := s.findCRD(r.Context(), checkpointGVR, id)
		if !found {
			s.writeError(w, http.StatusNotFound, "checkpoint not found")
			return
		}
		s.writeJSON(w, http.StatusOK, flattenCRD(item))
		return
	}

	item, err := s.dynClient.Resource(checkpointGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, flattenCRD(item))
}

// deleteCheckpoint removes a checkpoint from every tier the cluster
// might hold a copy on, AND from every sibling catalog row sharing
// the same content hash. The unit of deletion is the hash — leaving
// any sibling row alive would let NVCA Hook A's content-addressed
// lookup keep resolving the now-bytes-gone hash and stamp restore-from
// on new pods (nvsnap#137).
//
//	L1 origin agent's hostpath  — DELETE on the agent's /v1/checkpoints
//	L1 peer agent hostpaths     — same DELETE on every node from the
//	                              checkpoint_peers table that cached
//	                              this id via cascade-fetch
//	L2 per-capture PVCs         — rox-<short> + rwx-<short> in the
//	                              source pod's namespace (one set per
//	                              hash, not per row)
//	L2 VolumeSnapshot           — snap-<short> in the source pod's
//	                              namespace (also per-hash)
//	L2 promote Lease            — nvsnap-promote-<short> in the source
//	                              pod's namespace
//	Capture manifest CM         — nvsnap-capture-<short> in nvsnap-system,
//	                              the Reconciler's source of truth;
//	                              deleted BEFORE the catalog rows so the
//	                              row isn't resurrected on the next tick
//	L3 nvsnap-blobstore           — DELETE /v1/capture/{agent-id} for
//	                              EACH catalog row (rows sharing a
//	                              hash dedup blobs via the CAS
//	                              refcount; deleting all rows lets
//	                              the GC reclaim every byte)
//	GPUCheckpoint CRD           — per row
//	Catalog DB rows             — every row WHERE hash = <hash>
//
// All cascade deletes are best-effort. Returns 404 only when nothing
// matched anywhere.
func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	actor := getUser(r)
	ctx := r.Context()

	row, _ := s.catalog.GetCheckpoint(id)
	agentID := id
	if row != nil && row.CheckpointPath != "" {
		agentID = filepath.Base(row.CheckpointPath)
	}

	result := s.cascadeDeleteCheckpoint(ctx, id, agentID, row)

	if !result.AnySuccess {
		s.writeError(w, http.StatusNotFound, "checkpoint not found in any tier (agent, peers, blobstore, catalog)")
		return
	}

	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action:     "checkpoint.delete",
		Resource:   "checkpoint",
		ResourceID: id,
		Actor:      actor,
		Status:     result.Status(),
		Message:    result.Summary(),
	})
	w.WriteHeader(http.StatusNoContent)
}

// deleteCheckpointByHash exposes the cascade at the hash unit
// directly — for admin tooling that has a hash but not a row id.
func (s *Server) deleteCheckpointByHash(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	actor := getUser(r)
	ctx := r.Context()

	rows, err := s.catalog.ListByHash(hash)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "catalog lookup: "+err.Error())
		return
	}
	if len(rows) == 0 {
		s.writeError(w, http.StatusNotFound, "no catalog rows for hash "+hash)
		return
	}
	first := rows[0]
	agentID := first.ID
	if first.CheckpointPath != "" {
		agentID = filepath.Base(first.CheckpointPath)
	}
	result := s.cascadeDeleteCheckpoint(ctx, first.ID, agentID, &first)

	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action:     "checkpoint.delete.by-hash",
		Resource:   "checkpoint-hash",
		ResourceID: hash,
		Actor:      actor,
		Status:     result.Status(),
		Message:    result.Summary(),
	})
	if !result.AnySuccess {
		s.writeError(w, http.StatusNotFound, "no artifacts found for hash "+hash)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cascadeDeleteResult tracks per-tier outcomes for the audit row +
// the response status decision. Failures are accumulated in Errors
// so the audit message lists exactly what didn't clean up.
type cascadeDeleteResult struct {
	OriginAgents int // count of agent host paths DELETE-d successfully (origin + peers merged)
	PeerAgents   int // back-compat audit detail — peers subset of OriginAgents
	Blobstores   int // count of blobstore captures DELETE-d (per row)
	CRDsDeleted  int // count of GPUCheckpoint CRDs deleted (per row)
	CatalogRows  int // count of catalog rows deleted (one per sibling sharing the hash)
	L2PVCs       int // count of L2 PVCs deleted (rox + rwx, per hash)
	L2Snapshots  int // count of L2 VolumeSnapshots deleted (per hash)
	L2Leases     int // count of L2 promote Leases deleted (per hash)
	CaptureCMs   int // count of rootfs-capture-manifest ConfigMaps deleted (per hash)
	Errors       []string
	AnySuccess   bool
}

// Status returns "success" when every attempted sub-delete succeeded
// (no errors collected), else "partial".
func (r *cascadeDeleteResult) Status() string {
	if len(r.Errors) == 0 {
		return "success"
	}
	return "partial"
}

// Summary returns a one-line human description of what got deleted.
// Lands in the audit message column.
func (r *cascadeDeleteResult) Summary() string {
	parts := []string{}
	if r.OriginAgents > 0 {
		parts = append(parts, fmt.Sprintf("%d agent path(s)", r.OriginAgents))
	}
	if r.Blobstores > 0 {
		parts = append(parts, fmt.Sprintf("%d blobstore capture(s)", r.Blobstores))
	}
	if r.CRDsDeleted > 0 {
		parts = append(parts, fmt.Sprintf("%d GPUCheckpoint CRD(s)", r.CRDsDeleted))
	}
	if r.L2PVCs > 0 {
		parts = append(parts, fmt.Sprintf("%d L2 PVC(s)", r.L2PVCs))
	}
	if r.L2Snapshots > 0 {
		parts = append(parts, fmt.Sprintf("%d L2 snapshot(s)", r.L2Snapshots))
	}
	if r.L2Leases > 0 {
		parts = append(parts, fmt.Sprintf("%d L2 lease(s)", r.L2Leases))
	}
	if r.CaptureCMs > 0 {
		parts = append(parts, fmt.Sprintf("%d capture manifest CM(s)", r.CaptureCMs))
	}
	if r.CatalogRows > 0 {
		parts = append(parts, fmt.Sprintf("%d catalog row(s)", r.CatalogRows))
	}
	msg := "deleted: " + strings.Join(parts, ", ")
	if len(parts) == 0 {
		msg = "deleted: (nothing)"
	}
	if len(r.Errors) > 0 {
		msg += "; errors: " + strings.Join(r.Errors, "; ")
	}
	return msg
}

func (s *Server) cascadeDeleteCheckpoint(ctx context.Context, id, agentID string, row *db.Checkpoint) cascadeDeleteResult {
	var result cascadeDeleteResult
	client := &http.Client{Timeout: 10 * time.Second}

	// Targeted row, synthesizing missing fields from (id, agentID)
	// so the per-row helper has what it needs even when row is nil
	// or sparsely populated (orphan agent registrations whose catalog
	// row was already pruned, or unit-test fixtures with empty
	// Checkpoint{}).
	target := db.Checkpoint{}
	if row != nil {
		target = *row
	}
	if target.ID == "" {
		target.ID = id
	}
	if target.CheckpointPath == "" {
		target.CheckpointPath = "/var/lib/nvsnap/checkpoints/" + agentID
	}
	rowsByHash := []db.Checkpoint{target}

	// Sibling rows for the per-row cascade. Only when hash is known.
	if row != nil && row.Hash != "" {
		siblings, err := s.catalog.ListByHash(row.Hash)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("catalog ListByHash(%s): %v", row.Hash[:12], err))
		} else {
			seenID := map[string]bool{row.ID: true}
			for i := range siblings {
				sib := &siblings[i]
				if !seenID[sib.ID] {
					rowsByHash = append(rowsByHash, siblings[i])
					seenID[sib.ID] = true
				}
			}
		}
	}

	// Per-row cascade across agent paths + blobstore + CRD.
	seenNodes := map[string]bool{}
	for i := range rowsByHash {
		s.cascadeDeletePerRow(ctx, client, &rowsByHash[i], seenNodes, &result)
	}

	// L2 artifacts — one set per hash. Skipped when hash or namespace
	// is unknown (legacy rows without the column, or nil row).
	if row != nil && row.Hash != "" && row.Namespace != "" {
		s.deleteL2Artifacts(ctx, row.Namespace, row.Hash, &result)
	}

	// Capture manifest CM — delete BEFORE the catalog rows, else the
	// Reconciler can re-ingest the row from the still-present CM between
	// here and DeleteByHash (resurrection bug). Per-hash.
	if row != nil && row.Hash != "" {
		s.deleteCaptureManifestCM(ctx, row.Hash, &result)
	}

	// Catalog rows — DeleteByHash takes everything sharing this hash
	// in one statement. Falls back to single-id delete when hash is
	// unknown.
	if row != nil && row.Hash != "" {
		n, err := s.catalog.DeleteByHash(row.Hash)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("catalog DeleteByHash(%s): %v", row.Hash[:12], err))
		} else {
			result.CatalogRows = int(n)
			if n > 0 {
				result.AnySuccess = true
			}
		}
	} else if row != nil {
		if err := s.catalog.DeleteCheckpoint(id); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("catalog DeleteCheckpoint(%s): %v", id, err))
		} else {
			result.CatalogRows = 1
			result.AnySuccess = true
		}
	}

	return result
}

// cascadeDeletePerRow handles the per-row cleanup (agent host paths,
// blobstore capture, GPUCheckpoint CRD). Called once per catalog row
// sharing a hash. Mutates result + seenNodes in place. Does NOT
// touch the catalog row itself — DeleteByHash above handles that
// in one statement.
func (s *Server) cascadeDeletePerRow(
	ctx context.Context,
	client *http.Client,
	row *db.Checkpoint,
	seenNodes map[string]bool,
	result *cascadeDeleteResult,
) {
	rowID := row.ID
	agentID := rowID
	if row.CheckpointPath != "" {
		agentID = filepath.Base(row.CheckpointPath)
	}

	// 1. agents currently caching this row (origin + peers). Live
	//    hostpath scan first; then checkpoint_peers for nodes that
	//    cascade-fetched it. Dedup by (node, agentID) so we don't
	//    double-DELETE across sibling rows.
	agentCheckpoints := s.listAgentCheckpoints(ctx)
	for _, ckpt := range agentCheckpoints {
		ckptID, _ := ckpt["id"].(string)
		nodeName, _ := ckpt["nodeName"].(string)
		if (ckptID != rowID && ckptID != agentID) || nodeName == "" {
			continue
		}
		key := nodeName + "|" + ckptID
		if seenNodes[key] {
			continue
		}
		seenNodes[key] = true
		if s.deleteOnAgentNode(ctx, client, nodeName, ckptID) {
			result.OriginAgents++
			result.AnySuccess = true
		} else {
			result.Errors = append(result.Errors, "agent("+nodeName+") DELETE failed")
		}
	}
	peers, _ := s.catalog.ListPeers(rowID)
	for _, p := range peers {
		key := p.NodeName + "|" + agentID
		if seenNodes[key] {
			continue
		}
		seenNodes[key] = true
		if s.deleteOnAgentNode(ctx, client, p.NodeName, agentID) {
			result.PeerAgents++
			result.OriginAgents++
			result.AnySuccess = true
		} else {
			result.Errors = append(result.Errors, "peer("+p.NodeName+") DELETE failed")
		}
	}

	// 2. Blobstore capture for this row's agentID. CAS refcount on
	//    shared blobs drops to zero only when every sibling row's
	//    capture has been deleted; that's why this runs per row.
	if s.config.BlobstoreURL != "" {
		url := fmt.Sprintf("%s/v1/capture/%s", s.config.BlobstoreURL, urlPathEscape(agentID))
		req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, http.NoBody)
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent {
				result.Blobstores++
				result.AnySuccess = true
			} else if resp.StatusCode != http.StatusNotFound {
				result.Errors = append(result.Errors,
					fmt.Sprintf("blobstore DELETE %s returned %d", agentID, resp.StatusCode))
			}
		} else {
			result.Errors = append(result.Errors,
				"blobstore DELETE "+agentID+" failed: "+err.Error())
		}
	}

	// 3. GPUCheckpoint CRD — per row. NotFound is fine (CRD already
	//    evicted, or never created — agent-register rows have no CRD).
	if row.Namespace != "" {
		err := s.dynClient.Resource(checkpointGVR).Namespace(row.Namespace).
			Delete(ctx, rowID, metav1.DeleteOptions{})
		switch {
		case err == nil:
			result.CRDsDeleted++
			result.AnySuccess = true
		case apierrors.IsNotFound(err):
			// already gone
		default:
			result.Errors = append(result.Errors,
				fmt.Sprintf("GPUCheckpoint CRD %s DELETE: %v", rowID, err))
		}
	}
}

// deleteL2Artifacts cleans up per-capture PVCs, VolumeSnapshot, and
// promote Lease named from ShortHash(hash) in the source pod's
// namespace. One set per hash regardless of how many catalog rows
// share the hash, so this runs once per hash. All deletes are
// best-effort + NotFound-tolerant.
// captureManifestNamespace is where the agent's ConfigMapBackend writes
// rootfs-capture-manifest CMs and where the Reconciler reads them. The
// delete cascade MUST purge the CM from this same namespace, or the
// Reconciler re-ingests the deleted row within one tick (the resurrection
// bug). Keep in sync with the Reconciler's Namespace.
const captureManifestNamespace = "nvsnap-system"

// deleteCaptureManifestCM removes the rootfs capture's manifest ConfigMap
// (nvsnap-capture-<short-hash> in captureManifestNamespace). This is the
// Reconciler's source of truth for rootfs captures — without deleting it,
// a deleted catalog row is re-created on the next reconcile tick. Per-hash
// (one CM per hash, not per sibling row). Best-effort; NotFound is a no-op.
func (s *Server) deleteCaptureManifestCM(ctx context.Context, hash string, result *cascadeDeleteResult) {
	name := checkpointstore.CMNameFor(hash)
	err := s.kubeClient.CoreV1().ConfigMaps(captureManifestNamespace).
		Delete(ctx, name, metav1.DeleteOptions{})
	switch {
	case err == nil:
		result.CaptureCMs++
		result.AnySuccess = true
	case apierrors.IsNotFound(err):
		// already gone — CRIU captures have no manifest CM, and a
		// re-delete of a rootfs capture lands here too.
	default:
		result.Errors = append(result.Errors,
			fmt.Sprintf("capture manifest CM %s/%s DELETE: %v", captureManifestNamespace, name, err))
	}
}

func (s *Server) deleteL2Artifacts(ctx context.Context, namespace, hash string, result *cascadeDeleteResult) {
	short := checkpointstore.ShortHash(hash)
	roxName := "rox-" + short
	rwxName := "rwx-" + short
	snapName := "snap-" + short
	leaseName := "nvsnap-promote-" + short

	for _, name := range []string{roxName, rwxName} {
		err := s.kubeClient.CoreV1().PersistentVolumeClaims(namespace).
			Delete(ctx, name, metav1.DeleteOptions{})
		switch {
		case err == nil:
			result.L2PVCs++
			result.AnySuccess = true
		case apierrors.IsNotFound(err):
			// already gone
		default:
			result.Errors = append(result.Errors,
				fmt.Sprintf("L2 PVC %s/%s DELETE: %v", namespace, name, err))
		}
	}

	err := s.dynClient.Resource(volumeSnapshotGVR).Namespace(namespace).
		Delete(ctx, snapName, metav1.DeleteOptions{})
	switch {
	case err == nil:
		result.L2Snapshots++
		result.AnySuccess = true
	case apierrors.IsNotFound(err):
	default:
		result.Errors = append(result.Errors,
			fmt.Sprintf("L2 VolumeSnapshot %s/%s DELETE: %v", namespace, snapName, err))
	}

	err = s.kubeClient.CoordinationV1().Leases(namespace).
		Delete(ctx, leaseName, metav1.DeleteOptions{})
	switch {
	case err == nil:
		result.L2Leases++
		result.AnySuccess = true
	case apierrors.IsNotFound(err):
	default:
		result.Errors = append(result.Errors,
			fmt.Sprintf("L2 Lease %s/%s DELETE: %v", namespace, leaseName, err))
	}
}

func (s *Server) deleteOnAgentNode(ctx context.Context, client *http.Client, nodeName, ckptID string) bool {
	node, err := s.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	ip := nodeInternalIP(node)
	if ip == "" {
		return false
	}
	url := fmt.Sprintf("http://%s:%d/v1/checkpoints/%s", ip, s.config.AgentPort, urlPathEscape(ckptID))
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	// 204 deleted, 404 already gone — both count as "agent has no copy now".
	return resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound
}

// urlPathEscape escapes a path segment for safe interpolation into a
// URL template. Thin alias for url.PathEscape for callsite clarity.
func urlPathEscape(s string) string {
	return url.PathEscape(s)
}

// proxyToAgentCheckpoint finds which agent has the checkpoint and proxies the request.
func (s *Server) proxyToAgentCheckpoint(w http.ResponseWriter, r *http.Request, agentPath string) {
	id := mux.Vars(r)["id"]
	agentCheckpoints := s.listAgentCheckpoints(r.Context())
	for _, ckpt := range agentCheckpoints {
		ckptID, _ := ckpt["id"].(string)
		nodeName, _ := ckpt["nodeName"].(string)
		if ckptID != id || nodeName == "" {
			continue
		}
		node, err := s.kubeClient.CoreV1().Nodes().Get(r.Context(), nodeName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		ip := nodeInternalIP(node)
		if ip == "" {
			continue
		}
		url := fmt.Sprintf("http://%s:%d%s?%s", ip, s.config.AgentPort, agentPath, r.URL.RawQuery)
		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, http.NoBody)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		_ = resp.Body.Close()
		return
	}
	s.writeError(w, http.StatusNotFound, "checkpoint not found on any agent")
}

func (s *Server) listCheckpointFiles(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Rootfs captures have no agent-local CRIU dump dir — their file
	// inventory lives in the capture manifest (captured volumes + the
	// engine-cache extract paths the webhook shadow-mounts). Serve that
	// so the UI's "files" view isn't empty for rootfs captures. CRIU
	// captures fall through to the agent (which has the real dump dir).
	if m, ok := s.readManifestCM(r.Context(), id); ok {
		type fileEntry struct {
			Path      string `json:"path"`
			Type      string `json:"type"`
			Category  string `json:"category,omitempty"`
			SizeBytes int64  `json:"sizeBytes"`
			FileCount int64  `json:"fileCount"`
		}
		entries := make([]fileEntry, 0, len(m.Volumes)+len(m.RootfsExtractPaths))
		for _, v := range m.Volumes {
			entries = append(entries, fileEntry{
				Path: v.MountPath, Type: v.Type, SizeBytes: v.SizeBytes, FileCount: v.FileCount,
			})
		}
		for _, p := range m.RootfsExtractPaths {
			entries = append(entries, fileEntry{
				Path: p.Path, Type: "extract", Category: p.Category, SizeBytes: p.SizeBytes, FileCount: p.FileCount,
			})
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"files":         entries,
			"count":         len(entries),
			"totalBytes":    m.TotalSizeBytes,
			"totalFiles":    m.FileCount,
			"captureMethod": "rootfs",
		})
		return
	}

	s.proxyToAgentCheckpoint(w, r, fmt.Sprintf("/v1/checkpoints/%s/files", id))
}

// readManifestCM fetches the capture manifest ConfigMap for a hash
// (nvsnap-capture-<short> in the manifest namespace). Returns ok=false
// when there's no manifest (CRIU captures, or a hash that never had a
// rootfs capture). The id may be the full hash; ShortHash handles both.
func (s *Server) readManifestCM(ctx context.Context, hash string) (checkpointstore.Manifest, bool) {
	if s.kubeClient == nil || hash == "" {
		return checkpointstore.Manifest{}, false
	}
	cm, err := s.kubeClient.CoreV1().ConfigMaps(s.config.ManifestNamespace).
		Get(ctx, checkpointstore.CMNameFor(hash), metav1.GetOptions{})
	if err != nil {
		return checkpointstore.Manifest{}, false
	}
	raw, ok := cm.Data[checkpointstore.CMDataKey]
	if !ok {
		return checkpointstore.Manifest{}, false
	}
	var m checkpointstore.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return checkpointstore.Manifest{}, false
	}
	return m, true
}

// enrichFromManifest fills read-time fields the catalog row doesn't carry
// (capture method) and backfills container/GPU metadata from the manifest's
// SourcePodMeta when the row left them empty. No-op when no manifest exists
// (CRIU captures keep their catalog-sourced fields). Keys mirror what the
// agent's rootfsonly.Capture records.
func (s *Server) enrichFromManifest(ctx context.Context, c *db.Checkpoint) {
	hash := c.Hash
	if hash == "" {
		hash = c.ID
	}
	m, ok := s.readManifestCM(ctx, hash)
	if !ok {
		return
	}
	c.CaptureMethod = m.CaptureMethod
	meta := m.SourcePodMeta
	if c.ContainerName == "" {
		c.ContainerName = meta["container"]
	}
	if c.ModelName == "" {
		c.ModelName = meta["model_id"]
	}
	if c.GPUType == "" {
		c.GPUType = meta["gpu_type"]
	}
	if c.DriverVersion == "" {
		c.DriverVersion = meta["driver_version"]
	}
	if c.GPUCount == 0 {
		if n, err := strconv.Atoi(meta["gpu_count"]); err == nil {
			c.GPUCount = n
		}
	}
	if c.DriverMajor == 0 {
		if n, err := strconv.Atoi(meta["driver_major"]); err == nil {
			c.DriverMajor = n
		}
	}
}

func (s *Server) readCheckpointFile(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	s.proxyToAgentCheckpoint(w, r, fmt.Sprintf("/v1/checkpoints/%s/file", id))
}

// --- Restore handlers ---

func (s *Server) createRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CheckpointName string `json:"checkpointName"`
		CheckpointID   string `json:"checkpointId,omitempty"` // Agent-level checkpoint ID
		NewPodName     string `json:"newPodName,omitempty"`
		NodeName       string `json:"nodeName,omitempty"`
		Namespace      string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.CheckpointName == "" {
		s.writeError(w, http.StatusBadRequest, "checkpointName required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	// Look up checkpoint CRD
	ckpt, err := s.dynClient.Resource(checkpointGVR).Namespace(req.Namespace).Get(r.Context(), req.CheckpointName, metav1.GetOptions{})
	if err != nil {
		s.writeError(w, http.StatusNotFound, "checkpoint not found: "+err.Error())
		return
	}

	spec, _ := ckpt.Object["spec"].(map[string]interface{})
	status, _ := ckpt.Object["status"].(map[string]interface{})
	podName, _ := spec["podName"].(string)
	checkpointPath, _ := status["checkpointPath"].(string)
	nodeName, _ := status["nodeName"].(string)

	if req.NodeName != "" {
		nodeName = req.NodeName
	}
	if req.NewPodName == "" {
		req.NewPodName = podName + "-restored"
	}

	// Extract agent checkpoint ID from path or message
	agentCheckpointID := req.CheckpointID
	if agentCheckpointID == "" && checkpointPath != "" {
		parts := strings.Split(strings.TrimRight(checkpointPath, "/"), "/")
		if len(parts) > 0 {
			agentCheckpointID = parts[len(parts)-1]
		}
	}

	// Create GPURestore CRD
	name := fmt.Sprintf("restore-%s-%d", req.CheckpointName, time.Now().Unix())
	restoreCRD := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvsnap.io/v1alpha1",
			"kind":       "GPURestore",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": req.Namespace,
			},
			"spec": map[string]interface{}{
				"checkpointName": req.CheckpointName,
				"checkpointPath": checkpointPath,
				"newPodName":     req.NewPodName,
				"nodeName":       nodeName,
			},
		},
	}
	_, err = s.dynClient.Resource(restoreGVR).Namespace(req.Namespace).Create(r.Context(), restoreCRD, metav1.CreateOptions{})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "create restore CRD: "+err.Error())
		return
	}

	// Dual-write to DB catalog
	if err := s.catalog.CreateRestore(&db.Restore{
		ID:            name,
		CheckpointID:  agentCheckpointID,
		CheckpointRef: req.CheckpointName,
		Namespace:     req.Namespace,
		NodeName:      nodeName,
		NewPodName:    req.NewPodName,
		Status:        "Pending",
	}); err != nil {
		s.log.WithError(err).Warn("Failed to write restore to DB")
	}

	// Run restore async
	go s.runRestore(name, req.Namespace, nodeName, agentCheckpointID, req.NewPodName)

	s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"id":        name,
		"namespace": req.Namespace,
		"phase":     "Pending",
		"message":   "Restore started",
	})
}

func (s *Server) runRestore(name, namespace, nodeName, checkpointID, newPodName string) {
	ctx := context.Background()
	log := s.log.WithFields(logrus.Fields{
		"restore":    name,
		"checkpoint": checkpointID,
		"node":       nodeName,
	})

	s.updateRestoreCRD(ctx, name, namespace, "CreatingPod", newPodName, "Getting manifest from agent")
	_ = s.catalog.UpdateRestoreStatus(name, "CreatingPod", newPodName, "Getting manifest from agent")

	ip, err := s.nodeIP(ctx, nodeName)
	if err != nil {
		log.WithError(err).Error("Failed to find node IP")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", err.Error())
		return
	}

	// Step 1: Get placeholder manifest from agent
	log.Info("Requesting restore manifest from agent")
	manifestReq, _ := json.Marshal(map[string]interface{}{
		"checkpointId":   checkpointID,
		"targetPodName":  newPodName,
		"targetNodeName": nodeName,
		"namespace":      namespace,
	})
	manifestHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s:%d/v1/restore/manifest", ip, s.config.AgentPort),
		bytes.NewReader(manifestReq))
	if err != nil {
		log.WithError(err).Error("Failed to build manifest request")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", err.Error())
		return
	}
	manifestHTTPReq.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(manifestHTTPReq)
	if err != nil {
		log.WithError(err).Error("Failed to get manifest")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	manifestData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", string(manifestData))
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", string(manifestData))
		return
	}

	// Step 2: Parse manifest and create placeholder pod
	log.Info("Creating placeholder pod")
	var pod corev1.Pod
	if uerr := sigsyaml.Unmarshal(manifestData, &pod); uerr != nil {
		log.WithError(uerr).Error("Failed to decode manifest")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", "decode manifest: "+uerr.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", "decode manifest: "+uerr.Error())
		return
	}
	if pod.Namespace == "" {
		pod.Namespace = namespace
	}
	_, err = s.kubeClient.CoreV1().Pods(pod.Namespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to create placeholder pod")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", "", "create pod: "+err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", "", "create pod: "+err.Error())
		return
	}

	// Step 3: Wait for pod to be running
	s.updateRestoreCRD(ctx, name, namespace, "Restoring", newPodName, "Waiting for placeholder pod")
	_ = s.catalog.UpdateRestoreStatus(name, "Restoring", newPodName, "Waiting for placeholder pod")
	if werr := s.waitForPodRunning(ctx, pod.Namespace, newPodName, 5*time.Minute); werr != nil {
		log.WithError(werr).Error("Pod didn't become ready")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", newPodName, werr.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", newPodName, werr.Error())
		return
	}

	// Step 4: Get placeholder container ID
	placeholderPod, err := s.kubeClient.CoreV1().Pods(pod.Namespace).Get(ctx, newPodName, metav1.GetOptions{})
	if err != nil {
		s.updateRestoreCRD(ctx, name, namespace, "Failed", newPodName, err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", newPodName, err.Error())
		return
	}
	containerID := ""
	if len(placeholderPod.Status.ContainerStatuses) > 0 {
		cid := placeholderPod.Status.ContainerStatuses[0].ContainerID
		containerID = strings.TrimPrefix(cid, "containerd://")
	}

	// Step 5: Trigger restore
	log.Info("Triggering restore in placeholder pod")
	triggerReq, _ := json.Marshal(map[string]interface{}{
		"checkpointId":           checkpointID,
		"placeholderContainerId": containerID,
	})
	triggerHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s:%d/v1/restore/trigger", ip, s.config.AgentPort),
		bytes.NewReader(triggerReq))
	if err != nil {
		log.WithError(err).Error("Trigger restore build failed")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", newPodName, err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", newPodName, err.Error())
		return
	}
	triggerHTTPReq.Header.Set("Content-Type", "application/json")
	resp2, err := s.httpClient.Do(triggerHTTPReq)
	if err != nil {
		log.WithError(err).Error("Trigger restore failed")
		s.updateRestoreCRD(ctx, name, namespace, "Failed", newPodName, err.Error())
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", newPodName, err.Error())
		return
	}
	defer func() { _ = resp2.Body.Close() }()
	triggerBody, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		s.updateRestoreCRD(ctx, name, namespace, "Failed", newPodName, string(triggerBody))
		_ = s.catalog.UpdateRestoreStatus(name, "Failed", newPodName, string(triggerBody))
		return
	}

	log.Info("Restore completed")
	s.updateRestoreCRD(ctx, name, namespace, "Completed", newPodName, "Restore successful")
	_ = s.catalog.UpdateRestoreStatus(name, "Completed", newPodName, "Restore successful")
}

func (s *Server) listRestores(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	list, err := s.dynClient.Resource(restoreGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	restores := make([]map[string]interface{}, 0, len(list.Items))
	for i := range list.Items {
		restores = append(restores, flattenCRD(&list.Items[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"restores": restores,
		"count":    len(restores),
	})
}

func (s *Server) getRestore(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	ns := r.URL.Query().Get("namespace")

	if ns == "" {
		item, found := s.findCRD(r.Context(), restoreGVR, id)
		if !found {
			s.writeError(w, http.StatusNotFound, "restore not found")
			return
		}
		s.writeJSON(w, http.StatusOK, flattenCRD(item))
		return
	}

	item, err := s.dynClient.Resource(restoreGVR).Namespace(ns).Get(r.Context(), id, metav1.GetOptions{})
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, flattenCRD(item))
}

// --- Health ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// --- Agent helpers ---

// listAgentCheckpoints queries all healthy agent nodes for checkpoint data on disk.
func (s *Server) listAgentCheckpoints(ctx context.Context) []map[string]interface{} {
	nodes, err := s.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "nvidia.com/gpu.present=true",
	})
	if err != nil {
		s.log.WithError(err).Warn("Failed to list nodes for agent checkpoints")
		return nil
	}

	var (
		mu     sync.Mutex
		result []map[string]interface{}
		wg     sync.WaitGroup
	)

	client := &http.Client{Timeout: 5 * time.Second}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		ip := nodeInternalIP(node)
		if ip == "" {
			continue
		}
		nodeName := node.Name

		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("http://%s:%d/v1/checkpoints", ip, s.config.AgentPort)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return
			}
			body, _ := io.ReadAll(resp.Body)
			var agentResp struct {
				Checkpoints []map[string]interface{} `json:"checkpoints"`
			}
			if err := json.Unmarshal(body, &agentResp); err != nil {
				return
			}
			mu.Lock()
			for _, ckpt := range agentResp.Checkpoints {
				ckpt["nodeName"] = nodeName
				ckpt["source"] = "agent"
				result = append(result, ckpt)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return result
}

func (s *Server) nodeIP(ctx context.Context, nodeName string) (string, error) {
	node, err := s.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	ip := nodeInternalIP(node)
	if ip == "" {
		return "", fmt.Errorf("no internal IP for node %s", nodeName)
	}
	return ip, nil
}

func (s *Server) checkAgentHealth(ctx context.Context, nodeIP string) bool {
	if nodeIP == "" {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/health", nodeIP, s.config.AgentPort), http.NoBody)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// --- K8s helpers ---

func (s *Server) gpuPodsOnNode(ctx context.Context, nodeName string) ([]map[string]interface{}, error) {
	pods, err := s.kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, err
	}
	var result []map[string]interface{}
	for i := range pods.Items {
		if gpuCount := podGPUCount(&pods.Items[i]); gpuCount > 0 {
			result = append(result, podInfo(&pods.Items[i], gpuCount))
		}
	}
	return result, nil
}

func (s *Server) gpuPods(ctx context.Context, namespace string) ([]map[string]interface{}, error) {
	pods, err := s.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var result []map[string]interface{}
	for i := range pods.Items {
		if gpuCount := podGPUCount(&pods.Items[i]); gpuCount > 0 {
			result = append(result, podInfo(&pods.Items[i], gpuCount))
		}
	}
	return result, nil
}

func podGPUCount(pod *corev1.Pod) int {
	var total int
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if q, ok := c.Resources.Limits[gpuResource]; ok {
			total += int(q.Value())
		}
	}
	return total
}

func podInfo(pod *corev1.Pod, gpuCount int) map[string]interface{} {
	image := ""
	if len(pod.Spec.Containers) > 0 {
		image = pod.Spec.Containers[0].Image
	}
	return map[string]interface{}{
		"name":      pod.Name,
		"namespace": pod.Namespace,
		"nodeName":  pod.Spec.NodeName,
		"status":    string(pod.Status.Phase),
		"gpuCount":  gpuCount,
		"image":     image,
		"createdAt": pod.CreationTimestamp.Time,
	}
}

func nodeInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

func nodeConditionStatus(node *corev1.Node) string {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

// --- CRD helpers ---

// setCheckpointStatus writes the per-phase status snapshot onto a
// GPUCheckpoint CR. hash carries the content-addressed identity from
// the agent's response (nvsnap#61) — empty for InProgress / Failed
// phases, populated on Completed when the agent returned one.
//
// Preserves existing .status.checkpointHash on a partial update (when
// the caller passes empty) so a Failed-after-Completed retry can't
// wipe a hash that was already persisted by an earlier success.
func (s *Server) setCheckpointStatus(ctx context.Context, obj *unstructured.Unstructured, phase, nodeName, path, hash string, size int64, message string) {
	status := map[string]interface{}{
		"phase":   phase,
		"message": message,
	}
	if nodeName != "" {
		status["nodeName"] = nodeName
	}
	if path != "" {
		status["checkpointPath"] = path
	}
	if size > 0 {
		status["checkpointSize"] = size
	}
	if hash != "" {
		status["checkpointHash"] = hash
	} else if existing, ok := obj.Object["status"].(map[string]interface{}); ok {
		// Preserve a prior hash through a Failed retry — only happens
		// when the GPUCheckpoint reconciles a second time after a
		// successful capture (e.g., manual phase reset).
		if prior, ok := existing["checkpointHash"].(string); ok && prior != "" {
			status["checkpointHash"] = prior
		}
	}
	now := time.Now().Format(time.RFC3339)
	if phase == "InProgress" {
		status["startTime"] = now
	}
	if phase == "Completed" || phase == "Failed" {
		status["completionTime"] = now
	}
	obj.Object["status"] = status
	_, err := s.dynClient.Resource(checkpointGVR).Namespace(obj.GetNamespace()).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		s.log.WithError(err).Warn("Failed to update checkpoint CRD status")
	}
}

func (s *Server) updateCheckpointCRD(ctx context.Context, name, namespace, phase, nodeName, path, hash string, size int64, message string) {
	obj, err := s.dynClient.Resource(checkpointGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		s.log.WithError(err).Error("Failed to get checkpoint CRD for status update")
		return
	}
	s.setCheckpointStatus(ctx, obj, phase, nodeName, path, hash, size, message)
}

func (s *Server) updateRestoreCRD(ctx context.Context, name, namespace, phase, newPodName, message string) {
	obj, err := s.dynClient.Resource(restoreGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		s.log.WithError(err).Error("Failed to get restore CRD for status update")
		return
	}
	status := map[string]interface{}{
		"phase":   phase,
		"message": message,
	}
	if newPodName != "" {
		status["newPodName"] = newPodName
	}
	now := time.Now().Format(time.RFC3339)
	if phase == "CreatingPod" || phase == "Restoring" {
		if _, exists := obj.Object["status"].(map[string]interface{})["startTime"]; !exists {
			status["startTime"] = now
		}
	}
	if phase == "Completed" || phase == "Failed" {
		status["completionTime"] = now
	}
	obj.Object["status"] = status
	_, err = s.dynClient.Resource(restoreGVR).Namespace(namespace).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		s.log.WithError(err).Warn("Failed to update restore CRD status")
	}
}

func (s *Server) findCRD(ctx context.Context, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, bool) {
	list, err := s.dynClient.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false
	}
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			return &list.Items[i], true
		}
	}
	return nil, false
}

func flattenCRD(obj *unstructured.Unstructured) map[string]interface{} {
	spec, _ := obj.Object["spec"].(map[string]interface{})
	status, _ := obj.Object["status"].(map[string]interface{})

	result := map[string]interface{}{
		"id":        obj.GetName(),
		"namespace": obj.GetNamespace(),
		"createdAt": obj.GetCreationTimestamp().Time,
	}
	for k, v := range spec {
		result[k] = v
	}
	for k, v := range status {
		result[k] = v
	}
	// NVCA's checkpoint-poll loop reads the field as "hash" (per its
	// nvsnap.Checkpoint type's `json:"hash"` tag), but the CRD status
	// field is "checkpointHash" (the K8s-idiomatic name). Without
	// this alias NVCA decodes the response, finds hash="", and
	// concludes the agent's hash propagation is broken — it then
	// "refuses to mark Warm" and re-fires the checkpoint forever
	// (loop reproduced on GCP-H100-a 2026-06-02 with NVCA <nvca#144).
	// Emit both so old + new NVCA decoders agree.
	if h, ok := status["checkpointHash"].(string); ok && h != "" {
		result["hash"] = h
	}
	return result
}

// --- Wait helpers ---

func (s *Server) waitForPodRunning(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for pod %s/%s to be running", namespace, name)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := s.kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning {
				return nil
			}
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				return fmt.Errorf("pod %s/%s in terminal phase: %s", namespace, name, pod.Status.Phase)
			}
		}
	}
}

// --- Retention enforcement ---

// retentionEnforcer runs every 5 minutes and applies enabled retention policies.
func (s *Server) retentionEnforcer(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.enforceRetentionPolicies()
		}
	}
}

func (s *Server) enforceRetentionPolicies() {
	policies, err := s.catalog.ListRetentionPolicies()
	if err != nil {
		s.log.WithError(err).Warn("Failed to list retention policies")
		return
	}

	for i := range policies {
		p := &policies[i]
		if !p.Enabled {
			continue
		}

		expired, err := s.catalog.FindExpiredCheckpoints(p)
		if err != nil {
			s.log.WithError(err).WithField("policy", p.Name).Warn("Failed to find expired checkpoints")
			continue
		}
		if len(expired) == 0 {
			continue
		}

		s.log.WithFields(logrus.Fields{
			"policy": p.Name,
			"count":  len(expired),
		}).Info("Retention policy: deleting expired checkpoints")

		for i := range expired {
			c := &expired[i]
			// Delete from DB
			if err := s.catalog.DeleteCheckpoint(c.ID); err != nil {
				s.log.WithError(err).WithField("checkpoint", c.ID).Warn("Failed to delete checkpoint from DB")
				continue
			}
			// Best-effort delete from CRD
			_ = s.dynClient.Resource(checkpointGVR).Namespace(c.Namespace).Delete(
				context.Background(), c.ID, metav1.DeleteOptions{})

			_ = s.catalog.LogAudit(&db.AuditEntry{
				Action:     "checkpoint.delete",
				Resource:   "checkpoint",
				ResourceID: c.ID,
				Actor:      "policy:" + p.Name,
				Message:    fmt.Sprintf("Deleted by retention policy %q", p.Name),
			})
		}
	}
}

// --- Retention policy handlers ---

func (s *Server) listRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := s.catalog.ListRetentionPolicies()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"policies": policies, "count": len(policies)})
}

func (s *Server) createRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	var p db.RetentionPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Name == "" {
		s.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if p.Namespace == "" {
		p.Namespace = "*"
	}
	if p.WorkloadType == "" {
		p.WorkloadType = "*"
	}
	if err := s.catalog.CreateRetentionPolicy(&p); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action: "policy.create", Resource: "policy", ResourceID: p.Name,
		Actor: getUser(r), Message: fmt.Sprintf("Created retention policy %q", p.Name),
	})
	s.writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	p, err := s.catalog.GetRetentionPolicy(id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	var p db.RetentionPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.ID = id
	if err := s.catalog.UpdateRetentionPolicy(&p); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action: "policy.update", Resource: "policy", ResourceID: p.Name,
		Actor: getUser(r), Message: fmt.Sprintf("Updated retention policy %q", p.Name),
	})
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	if err := s.catalog.DeleteRetentionPolicy(id); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.catalog.LogAudit(&db.AuditEntry{
		Action: "policy.delete", Resource: "policy", ResourceID: strconv.FormatInt(id, 10),
		Actor: getUser(r), Message: "Deleted retention policy",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	p, err := s.catalog.GetRetentionPolicy(id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	expired, err := s.catalog.FindExpiredCheckpoints(p)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var totalSize int64
	for i := range expired {
		totalSize += expired[i].CheckpointSize
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"policy":      p,
		"wouldDelete": len(expired),
		"totalSize":   totalSize,
		"checkpoints": expired,
	})
}

// --- Audit log handler ---

func (s *Server) listAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.catalog.ListAuditLog(db.AuditFilter{
		Action:     r.URL.Query().Get("action"),
		Resource:   r.URL.Query().Get("resource"),
		ResourceID: r.URL.Query().Get("resourceId"),
		Actor:      r.URL.Query().Get("actor"),
		Since:      r.URL.Query().Get("since"),
		Limit:      limit,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

// --- Blobstore proxy ---

func (s *Server) blobstoreStats(w http.ResponseWriter, r *http.Request) {
	s.proxyBlobstore(w, r, "/v1/stats")
}

func (s *Server) blobstoreListCaptures(w http.ResponseWriter, r *http.Request) {
	s.proxyBlobstore(w, r, "/v1/captures")
}

// proxyBlobstore forwards a GET to nvsnap-blobstore and streams the
// response body back. Read-only — UI only consumes aggregation
// endpoints, not raw blob/manifest reads.
func (s *Server) proxyBlobstore(w http.ResponseWriter, r *http.Request, path string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.config.BlobstoreURL+path, http.NoBody)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "blobstore unreachable: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// --- HTTP helpers ---

func (s *Server) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.WithFields(logrus.Fields{
			"method":   r.Method,
			"path":     r.URL.Path,
			"duration": time.Since(start),
		}).Debug("Request")
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CORS wrapping for WebSocket — Hijack() needs the raw ResponseWriter
		if r.Header.Get("Upgrade") == "websocket" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
