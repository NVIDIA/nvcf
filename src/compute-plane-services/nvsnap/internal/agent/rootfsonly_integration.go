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
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// RootfsCaptureConfig configures the optional rootfs-only capture
// loop. The backend is always Local — gpdrox/PVC-fanout was removed
// in 5d when peer-to-peer fanout (#57/#60) replaced it. Disabled by
// default — operators opt in per node.
type RootfsCaptureConfig struct {
	// Enabled gates the whole subsystem.
	Enabled bool

	// CacheDir is the root the Local Backend writes captured trees into.
	// Default "/var/lib/nvsnap/cache".
	CacheDir string

	// PodCacheDir, when non-empty (e.g. "/opt/nvsnap"), switches capture to
	// "cachedir" mode: capture ONLY the volume the webhook injects at
	// this in-pod path (HF_HOME / TORCHINDUCTOR / HOME are redirected
	// into it) as the entire PVC root; restore mounts the rox read-only
	// there directly, no overlayfs. Must match the webhook's cacheDir
	// setting. Empty = standard whole-rootfs path. Default "".
	PodCacheDir string

	// PodCacheEnvFile is the path to a mounted ConfigMap file holding the
	// cachedir env template, passed through to the webhook Mutator.
	// Capture-side only; empty → built-in default template. Restore
	// replays the env stamped into the manifest.
	PodCacheEnvFile string

	// CMNamespace is the K8s namespace ConfigMaps are written to so
	// any node's webhook can resolve a hash. Default "nvsnap-system".
	CMNamespace string

	// CUDADriverMajor is the node's NVIDIA driver major version (e.g.
	// 580). Used as a HashInput field so a capture made on one driver
	// version isn't replayed on an incompatible one. Default 580.
	CUDADriverMajor int

	// WarmupDelay is the wait after Pod becomes Ready before capturing.
	// Default 60s — gives engines time to compile graphs / warm caches.
	WarmupDelay time.Duration

	// Concurrency caps simultaneous in-flight captures on this node.
	// Default 2 (captures are I/O-heavy).
	Concurrency int
}

// startRootfsCapture builds a rootfs-only Watcher and runs it in a
// goroutine bound to ctx. Returns the Backend the agent built (so the
// in-process admission webhook can share it), or nil if Capture is
// disabled. Errors during setup are returned; runtime errors inside
// the watcher are logged but don't kill the agent (capture is optional).
func (a *Agent) startRootfsCapture(ctx context.Context, cfg RootfsCaptureConfig) (checkpointstore.Backend, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = "/var/lib/nvsnap/cache"
	}
	if cfg.CMNamespace == "" {
		cfg.CMNamespace = "nvsnap-system"
	}
	if cfg.CUDADriverMajor == 0 {
		cfg.CUDADriverMajor = 580
	}
	// WarmupDelay is honored as-is: the flag default (cmd/agent:
	// --rootfs-warmup-seconds, default 5s) provides the wait, and an
	// explicit 0 means capture immediately at Ready. (No ==0→60 override:
	// that silently made 0 unreachable. watcher.runCapture skips the wait
	// when WarmupDelay<=0.)

	kubeClient, err := buildKubeClient()
	if err != nil {
		return nil, fmt.Errorf("rootfsonly: build kube client: %w", err)
	}
	// Cache the client on the agent so the admission-webhook
	// cascade-fetch path (EnsureCaptureLocal) can read the per-capture
	// ConfigMaps + resolve peer node InternalIPs without rebuilding it.
	a.kubeClient = kubeClient

	backend, err := buildAgentBackend(cfg, kubeClient, a)
	if err != nil {
		return nil, fmt.Errorf("rootfsonly: init local backend: %w", err)
	}
	// Retain the full chain backend so the cross-cluster pull path
	// (ReplicateFromObjectStore) can replay a pulled capture's commit —
	// same manifest CM + L2 rox PVC promote a native capture produces.
	a.captureBackend = backend

	// Open the cross-cluster replication object store (no-op unless
	// Replication provider + HomeBucket are set).
	if err := a.startReplication(ctx); err != nil {
		return nil, fmt.Errorf("rootfsonly: start replication: %w", err)
	}

	capturer := &rootfsonly.Capturer{
		Backend:     backend,
		PIDResolver: rootfsonly.NewPIDResolver(),
		NodeName:    a.config.NodeName,
		// /proc/1/root lets the agent (running in a container with
		// hostPID=true) read host paths through PID 1's root view.
		HostFSRoot:        "/proc/1/root",
		MountinfoProcRoot: "", // empty → mountinfo.DefaultProcRoot()
		CacheDir:          a.config.RootfsCapture.PodCacheDir,
		Log:               a.log.WithField("subsys", "rootfsonly.capture"),
		// Push each successful capture to nvsnap-blobstore (same-cluster
		// tier-3 fallback) AND, when cross-cluster replication is enabled,
		// to this cluster's home bucket (the L4 cross-cluster tier).
		// Both are best-effort; failures are logged and don't roll back
		// the capture.
		PostCommit: a.postCaptureCommit,
	}
	watcher := &rootfsonly.Watcher{
		Capturer:    capturer,
		Composer:    &rootfsonly.HashInputComposer{CUDADriverMajor: cfg.CUDADriverMajor},
		KubeClient:  kubeClient,
		NodeName:    a.config.NodeName,
		WarmupDelay: cfg.WarmupDelay,
		Concurrency: cfg.Concurrency,
		Log:         a.log.WithField("subsys", "rootfsonly.watcher"),
	}

	a.log.WithFields(map[string]any{
		"cache_dir":    cfg.CacheDir,
		"driver_major": cfg.CUDADriverMajor,
		"warmup":       cfg.WarmupDelay,
		"concurrency":  cfg.Concurrency,
		"node":         a.config.NodeName,
	}).Info("rootfs-only capture watcher starting")

	go func() {
		err := watcher.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.log.WithError(err).Error("rootfs-only watcher exited with error")
		}
	}()
	return backend, nil
}

// buildAgentBackend constructs the checkpointstore backend the agent
// writes captures to. Chain layout (per docs/design/ROOTFS-EVERYWHERE.md):
//
//   - LocalBackend         — per-node hostPath cache (restore-fast on same node)
//   - ConfigMapBackend     — manifest CM (cluster-wide hash → metadata lookup)
//   - PerCapturePVCBackend — rwx → snapshot → rox PVC (cluster-wide fan-out
//     for N restored pods on one read-only disk)
//
// PerCapturePVCBackend is appended only if the L2 config (a.l2Backend) is
// wired — it requires a working RWX-capable StorageClass + VolumeSnapshotClass.
// When missing, the chain degrades to Local + ConfigMap and restore falls
// back to L3 (peer cascade) / L4 (nvsnap-blobstore).
func buildAgentBackend(cfg RootfsCaptureConfig, kubeClient kubernetes.Interface, a *Agent) (checkpointstore.Backend, error) {
	inner, err := checkpointstore.NewLocal(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	cm := checkpointstore.NewConfigMapBackend(
		inner,
		kubeClient,
		cfg.CMNamespace,
		a.log.WithField("subsys", "checkpointstore.cmregistry"),
	)
	// L2 PVC is optional. If startL2Backend wasn't called yet (or returned
	// nil because StorageClass is unset), we just return the Local+ConfigMap
	// pair — same as pre-v0.0.49 behavior.
	if a.l2Backend == nil {
		return cm, nil
	}
	a.log.Info("rootfs Backend chain: Local → ConfigMap → PerCapturePVC (L2 fan-out enabled)")
	return &checkpointstore.Chain{
		Members: []checkpointstore.Backend{cm, a.l2Backend},
		Log:     a.log.WithField("subsys", "checkpointstore.chain"),
	}, nil
}

// buildKubeClient prefers in-cluster ServiceAccount config and falls back
// to KUBECONFIG / ~/.kube/config for dev runs outside a cluster.
func buildKubeClient() (kubernetes.Interface, error) {
	cfg, err := loadKubeConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func loadKubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
}
