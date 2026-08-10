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

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// WebhookConfig configures the in-agent admission webhook server.
// Off by default; the agent enables it when --webhook is passed at
// startup. The webhook serves /mutate over TLS, decoded as
// AdmissionReview, and applies the same Mutator the standalone
// webhook used. Cache data + manifest reads go through the same
// Backend the capture loop writes to (Local or GPDRox, both wrapped
// with ConfigMapBackend), so any agent on any node can resolve any
// capture hash via the K8s API.
type WebhookConfig struct {
	Enabled    bool
	ListenAddr string // default ":8443"
	CertFile   string // PEM cert
	KeyFile    string // PEM key
	Path       string // default "/mutate"

	// AutoInject configures the image refs the webhook stamps into
	// auto-injected init containers when a pod carries
	// nvsnap.io/auto-inject: "true". Empty fields disable that branch
	// (the webhook fails open and admits the pod unchanged).
	AutoInject webhook.AutoInjectImages

	// L2WaitImage is the nvsnap-l2-wait init-container image ref
	// (nvsnap#147). When set, restore pods admitted with
	// nvsnap.io/restore-from get a nvsnap-l2-wait init container that
	// polls nvsnap-server until the L2 PVC promote is ready.
	// Empty disables the inject — the rox PVC still gets mounted
	// but kubelet may stall in ContainerCreating until the agent
	// finishes snap+clone (whichever is faster between kubelet's
	// retry cadence and snap+clone wall time).
	L2WaitImage string

	// HostBundleRoot is the on-host path the agent DaemonSet stages
	// the restore bundle into (default /var/lib/nvsnap/bundle). Function
	// pods mount {root}/nvsnap + {root}/nvsnap-lib via hostPath. Changing
	// this requires coordinated edits in the DaemonSet template too —
	// not a per-cluster knob, but exposed here for tests / dev
	// overrides.
	HostBundleRoot string

	// RestorePrepStrategy chooses how restore-side OverlayFS mounts
	// are produced. "inline" (default) does the mount syscalls during
	// admission — fast for small captures but blows past the 10-30s
	// webhook timeout once the manifest carries hundreds of mounts.
	// "init-container" delegates mount work to a nvsnap-mount-prep
	// init container that polls the agent's async prep manager
	// (#202). The webhook only emits patches with deterministic
	// mountpoints; the init container blocks the main container until
	// the agent reports ready.
	RestorePrepStrategy string

	// MountPrepInitImage is the image ref for the nvsnap-mount-prep
	// init container. Required when RestorePrepStrategy ==
	// "init-container". The agent image satisfies this (the binary
	// lives at /nvsnap-mount-prep via symlink).
	MountPrepInitImage string

	// AgentHostPort is the port the nvsnap-mount-prep init container
	// reaches the agent on (status.hostIP:AgentHostPort). Defaults to
	// 8081 (matches the agent's --listen=:8081 default).
	AgentHostPort int
}

// startWebhook starts the agent's mutating-admission TLS server in a
// goroutine. The server uses the SAME backend the capture loop is
// writing into, so capture and restore coordinate via the shared
// ConfigMap manifests.
//
// Pre-conditions:
//   - cfg.Enabled is true
//   - The rootfs-capture loop has been started (so we have a Backend
//     reference); webhook startup happens AFTER startRootfsCapture
//   - cert/key files exist on disk
//
// Failures during setup are returned; runtime errors inside the server
// are logged but don't kill the agent.
func (a *Agent) startWebhook(ctx context.Context, cfg WebhookConfig, backend checkpointstore.Backend) error {
	if !cfg.Enabled {
		return nil
	}
	if backend == nil {
		return errors.New("webhook: backend is nil (enable --rootfs-capture first)")
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8443"
	}
	if cfg.Path == "" {
		cfg.Path = "/mutate"
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return fmt.Errorf("webhook: cert and key files required")
	}

	mut := &webhook.Mutator{
		Backend: backend,
		// nvsnap#194 + #88: per-pod OverlayFS unions over every captured
		// volume (rootfs-extract subpaths AND hostPath/emptyDir user-data
		// volumes). The Mutator calls PrepareOverlay synchronously during
		// admission for each captured volume, and substitutes the merged
		// (writable) mountpoint into the patched pod spec — workloads
		// that write into the cache at runtime (e.g. vLLM's
		// torch_compile_cache, HF Transformers' refs/main) stop
		// crashing with EROFS.
		OverlayPreparer: a,
		// nvsnap#63: L2 PVC fast path. When the agent has an L2 backend
		// configured (StorageClass set + RBAC available), the webhook
		// calls Mount() on it first; on success the pod gets a PVC
		// volumeMount + CHECKPOINT_PATH env instead of the L1 hostPath
		// + nodeAffinity. On ErrNotFound (rox-<hash> not Bound yet),
		// falls through to the L1 path below. Nil disables the fast
		// path — useful for non-L2 clusters or testing.
		L2Backend: a.l2Backend,
		// cachedir mode (ember cache-reuse path): same in-pod path the
		// agent captures, so capture-side env+emptyDir inject and
		// restore-side RO mount agree. Empty = standard paths.
		CacheDir:     a.config.RootfsCapture.PodCacheDir,
		CacheEnvFile: a.config.RootfsCapture.PodCacheEnvFile,
		Composer: &rootfsonly.HashInputComposer{
			CUDADriverMajor: a.config.RootfsCapture.CUDADriverMajor,
		},
		// EnsureLocal is intentionally left nil for the demo / for-speed
		// flow. The Mutator emits nodeAffinity = CapturedOnNodes, which
		// guarantees kubelet resolves the injected hostPath volumeMounts
		// on a node that already has the capture bytes locally — zero
		// transfer at restore time. EnsureCaptureLocal is still wired
		// behind the agent's HTTP API for future "any GPU node" flows
		// where an init container fetches the bytes asynchronously.
		Log:        a.log.WithField("subsys", "webhook.mutate"),
		AutoInject: cfg.AutoInject,
		// nvsnap#147: L2 restore gating. When L2WaitImage is set,
		// the webhook prepends a nvsnap-l2-wait init container that
		// blocks the main container on pvc_promote_state == "ready".
		// Without it, kubelet would race the agent's async snap+clone
		// and pod scheduling stalls in ContainerCreating until the
		// rox PVC binds. Wired from agent --webhook-l2-wait-image
		// (helm sets it from .Values.agent.l2.waitImage).
		L2WaitImage:     a.config.Webhook.L2WaitImage,
		NvSnapServerURL: a.config.CatalogURL,
		// nvsnap#147: restore-entrypoint hostPath inject. Empty =
		// default "/var/lib/nvsnap/bundle" (matches the agent
		// DaemonSet's nvsnap-bundle-stage initContainer destination).
		HostBundleRoot: a.config.Webhook.HostBundleRoot,
		// nvsnap#202: restore-prep strategy. When "init-container",
		// the webhook emits a nvsnap-mount-prep init container instead
		// of doing the OverlayFS mount syscalls during admission.
		RestorePrepStrategy: cfg.RestorePrepStrategy,
		MountPrepInitImage:  cfg.MountPrepInitImage,
		AgentHostPort:       cfg.AgentHostPort,
	}
	handler := &webhook.Handler{
		Mutator: mut,
		Log:     a.log.WithField("subsys", "webhook.admission"),
	}
	srv := webhook.NewServer(webhook.ServerConfig{
		ListenAddr: cfg.ListenAddr,
		CertFile:   cfg.CertFile,
		KeyFile:    cfg.KeyFile,
		Path:       cfg.Path,
		Log:        a.log.WithField("subsys", "webhook.server"),
	}, handler)

	a.log.WithField("addr", cfg.ListenAddr).WithField("path", cfg.Path).
		Info("agent admission webhook starting")

	go func() {
		err := srv.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.log.WithError(err).Error("agent admission webhook exited with error")
		}
	}()
	return nil
}
