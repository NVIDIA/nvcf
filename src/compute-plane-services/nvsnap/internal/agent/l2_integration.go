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

// Agent-side wiring for the L2 per-capture PVC backend (nvsnap#63).
//
// The backend lives in internal/checkpointstore.PerCapturePVCBackend
// and drives K8s PVCs + Jobs + VolumeSnapshots. This file builds the
// agent's instance at startup, plumbing in the nvsnap-server HTTP
// adapter as the catalog state writer.

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// storageProfilesConfigMap is the optional per-cluster overlay that
// extends/overrides the built-in provisioner→strategy table (nvsnap#171).
const storageProfilesConfigMap = "nvsnap-storage-profiles"

// resolveL2Promoter reads the L2 StorageClass's provisioner +
// parameters.type, resolves a StorageProfile (built-in table overlaid by
// the nvsnap-storage-profiles ConfigMap), and constructs the matching
// Promoter. Returns nil on ANY failure (SC unreadable, no profile match,
// bad ConfigMap, construct error) — the backend then falls back to its
// default snapshot-clone ROX promoter. Always logs the resolved strategy
// (or the reason for fallback) so an operator can see what L2 will do.
func resolveL2Promoter(ctx context.Context, kc kubernetes.Interface, dyn dynamic.Interface, scName, namespace string, log logrus.FieldLogger) checkpointstore.Promoter {
	sc, err := kc.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).WithField("sc", scName).
			Warn("L2 storage profile: cannot read StorageClass; falling back to default snapshot-clone ROX promoter")
		return nil
	}
	provisioner := sc.Provisioner
	volType := sc.Parameters["type"] // disambiguates pd.csi (hyperdisk-ml vs pd-ssd)

	// Optional ConfigMap overlay.
	var overlay map[string]checkpointstore.StorageProfile
	if cm, cmErr := kc.CoreV1().ConfigMaps(namespace).Get(ctx, storageProfilesConfigMap, metav1.GetOptions{}); cmErr == nil {
		if overlay, cmErr = checkpointstore.ParseConfigMapProfiles(cm.Data["profiles.yaml"]); cmErr != nil {
			log.WithError(cmErr).Warn("L2 storage profile: bad nvsnap-storage-profiles ConfigMap; ignoring overlay")
			overlay = nil
		}
	} else if !apierrors.IsNotFound(cmErr) {
		log.WithError(cmErr).Warn("L2 storage profile: ConfigMap read failed; using built-in table only")
	}

	profile, key, ok := checkpointstore.ResolveStorageProfile(provisioner, volType, overlay)
	if !ok {
		log.WithFields(logrus.Fields{"sc": scName, "provisioner": provisioner, "type": volType}).
			Warn("L2 storage profile: no profile for provisioner[/type]; falling back to default snapshot-clone ROX promoter (add an entry to the nvsnap-storage-profiles ConfigMap to support this backend)")
		return nil
	}
	promoter, err := checkpointstore.NewPromoterFromProfile(profile, scName, kc, dyn, log)
	if err != nil {
		log.WithError(err).WithField("strategy", profile.Strategy).
			Warn("L2 storage profile: cannot construct promoter; falling back to default")
		return nil
	}
	log.WithFields(logrus.Fields{
		"sc": scName, "provisioner": provisioner, "type": volType,
		"profile_key": key, "strategy": profile.Strategy,
		"read_only_many": profile.ReadOnlyMany, "vh_transform": profile.VolumeHandleTransform,
	}).Info("L2 storage profile resolved")
	return promoter
}

// startL2Backend constructs the PerCapturePVCBackend if L2 is enabled
// in the agent config. Returns nil (and no error) when the feature is
// disabled — agents on clusters without an RWX StorageClass simply
// skip the promote step and fall back to L1+L3+L4. RWX is a
// documented cluster prerequisite (install docs); operating without
// it leaves multi-node restore degraded but doesn't fail the agent.
//
// Required for L2 to be active:
//   - cfg.L2.StorageClass non-empty (the documented prereq)
//   - cfg.L2.SnapshotClass non-empty (paired with StorageClass)
//   - cfg.CatalogURL non-empty (the state writer's target)
//   - K8s in-cluster config available (PVC + Job + Lease + Snapshot RBAC)
//
// Missing any of these is logged at INFO and returns (nil, nil) so the
// agent boots cleanly without L2 — the operator sees one log line at
// startup and can fix the deployment.
func (a *Agent) startL2Backend(_ context.Context, cfg L2BackendConfig) (checkpointstore.Backend, error) {
	log := a.log.WithField("subsys", "l2")
	if cfg.StorageClass == "" {
		log.Info("L2 disabled: agent.L2.StorageClass not set (cluster prerequisite missing — restore fan-out will fall back to L3 peer cascade)")
		return nil, nil
	}
	if a.config.CatalogURL == "" {
		return nil, errors.New("L2: CatalogURL required for pvc-state HTTP writes")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "nvsnap-system"
	}
	if cfg.WriterImage == "" {
		// In production the helm chart sets this to the agent image
		// (matching the agent itself, so capture-write subcommand
		// version stays in lockstep). If unset at runtime, refuse —
		// silently falling back would produce confusing failures
		// inside the Job pod with "exec: /nvsnap-agent: not found".
		return nil, errors.New("L2: WriterImage required (set agent.L2.WriterImage in helm values to the agent image)")
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("L2: in-cluster config: %w", err)
	}
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("L2: kube client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("L2: dynamic client: %w", err)
	}

	// Validate the configured StorageClass actually supports RWX
	// before we accept ourselves as an active L2 backend. Otherwise
	// every capture's promote sits in a silent provisioning loop
	// (the symptom on GCP-H100-a 2026-06-02 with the default
	// `hyperdisk-ml` choice — pd.csi rejects ReadWriteMany with
	// "specified multi writer with mount access type" and the PVC
	// stays Pending forever). Caller already established this agent
	// is supposed to run L2; fail-fast with an actionable error.
	if err := validateL2StorageClass(context.Background(), kc, cfg.StorageClass, log); err != nil {
		return nil, err
	}

	// Default the mount-holder pull secret unless the operator
	// explicitly cleared it. The "-" sentinel disables (no secret).
	pullSecret := cfg.WriterPullSecret
	if pullSecret == "" {
		pullSecret = DefaultWriterPullSecret
	}
	var pullSecrets []string
	if pullSecret != "" && pullSecret != "-" {
		pullSecrets = []string{pullSecret}
	}

	// Resolve the storage strategy from the L2 SC's provisioner +
	// parameters.type (nvsnap#171). nil ⇒ the backend's applyDefaults
	// falls back to the snapshot-clone ROX promoter (Hyperdisk-ML
	// behavior) — back-compat for clusters with no profile match.
	promoter := resolveL2Promoter(context.Background(), kc, dyn, cfg.StorageClass, cfg.Namespace, log)

	// SnapshotClass is only meaningful for the snapshot-clone strategy.
	// Shared-volume backends (NVMesh/EFS/Filestore) never snapshot — they
	// retain the writer's volume and mint a RO PV. A nil promoter means
	// the backend falls back to its default snapshot-clone, so require it
	// then too.
	if (promoter == nil || promoter.Caps().Strategy == checkpointstore.StrategySnapshotClone) && cfg.SnapshotClass == "" {
		return nil, errors.New("L2: SnapshotClass required for snapshot-clone storage (set agent.L2.SnapshotClass; not needed for NVMesh/EFS shared-volume)")
	}

	backend := &checkpointstore.PerCapturePVCBackend{
		KubeClient:        kc,
		DynClient:         dyn,
		Catalog:           NewPVCStateHTTPWriter(a.config.CatalogURL),
		Namespace:         cfg.Namespace,
		StorageClass:      cfg.StorageClass,
		SnapshotClass:     cfg.SnapshotClass,
		Promoter:          promoter,
		WriterImage:       cfg.WriterImage,
		WriterPullSecrets: pullSecrets,
		HostPathRoot:      "/", // legacy, unused after v0.0.51 (kept for back-compat)
		HostRoot:          "/host",
		NodeName:          a.config.NodeName, // v0.0.51 — mount-holder pinning
		HolderID:          a.config.NodeName, // identifies this node in the Lease
		Copier:            NewAgentCopier("/host", log),
		SizeBytesFor:      defaultL2Size,
		Log:               log,
	}
	log.WithFields(logrus.Fields{
		"storage_class":  cfg.StorageClass,
		"snapshot_class": cfg.SnapshotClass,
		"namespace":      cfg.Namespace,
		"writer_image":   cfg.WriterImage,
	}).Info("L2 backend enabled")
	return backend, nil
}

// defaultL2Size returns the PVC size to provision for a capture of the
// given manifest. The right dimension depends on the capture path:
//
//   - rootfs: the PVC holds the captured on-disk tree (model weights as
//     safetensors, code, caches). That has NO relation to vRAM, so we
//     size from what the capture actually measured —
//     manifest.TotalSizeBytes × 1.2, with a 10 GiB floor.
//   - CRIU/GPU-state: the dump ≈ vRAM, so we use the nvsnap#63 formula
//     gpu_vram_bytes × gpu_count × 1.2.
//
// Why this matters: every rootfs capture used to fall through to the
// vRAM formula's default (80 GB × 1 × 1.2 = 96 GiB) because the rootfs
// path never populates the gpu_count/gpu_vram_gb hints. Models whose
// on-disk weights exceed ~96 GB (fp16 70B, DeepSeek-class) overflowed
// the writer Job (OOSpace) and silently fell back to cold.
//
// The 1.2× multiplier covers filesystem overhead (block rounding,
// inodes, fs metadata) on top of the summed file sizes. A capture that
// still blows past it is by design: the writer Job fails OOSpace, the
// capture row keeps pvc_name="", restore falls back to peer cascade,
// and the operator notices via nvsnap_l2_promote_failed_total.
func defaultL2Size(_ string, m checkpointstore.Manifest) int64 {
	const oneGiB int64 = 1 << 30

	// Rootfs and cachedir paths both measure what they captured, so size
	// from that rather than guessing. cachedir was added after this
	// function and did not match the rootfs-only check, so every cachedir
	// capture fell through to the vRAM estimate below: a 14 GB capture
	// asked for 96 GiB (80 GiB H100 default x 1.2). On a Retain
	// StorageClass that over-allocation is permanent and per-capture.
	if (m.CaptureMethod == "rootfs" || m.CaptureMethod == "cachedir") && m.TotalSizeBytes > 0 {
		const floor = 10 * oneGiB
		return max(m.TotalSizeBytes*12/10, floor)
	}

	// CRIU/GPU-state path: dump ≈ vRAM. Manifest doesn't carry vRAM
	// directly; the agent's CollectCatalogInfo populates SourcePodMeta
	// with hints. Use them when present, else fall back to 80 GB × 1.
	vramGB := int64(80) // H100 default
	gpuCount := int64(1)
	if m.SourcePodMeta != nil {
		if v, ok := m.SourcePodMeta["gpu_vram_gb"]; ok {
			if n, err := parseInt64(v); err == nil && n > 0 {
				vramGB = n
			}
		}
		if v, ok := m.SourcePodMeta["gpu_count"]; ok {
			if n, err := parseInt64(v); err == nil && n > 0 {
				gpuCount = n
			}
		}
	}
	// vRAM × count × 1.2 (expressed as ×12/10 to stay in int).
	return vramGB * gpuCount * oneGiB * 12 / 10
}

// parseInt64 is a tiny helper that wraps strconv to keep callers
// inline-readable.
func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// validateL2StorageClass checks the configured StorageClass exists at
// agent startup and logs the provisioner/type so the operator's boot
// log shows what L2 is actually backed by.
//
// Previous versions of this function denied pd.csi.storage.gke.io
// outright because the L2 backend was requesting ReadWriteMany for
// the writer PVC — which pd.csi can't satisfy. That denial was
// wrong: the writer Job is a single pod (lease-serialized), so the
// writer PVC only ever needs ReadWriteOnce. nvsnap#81 fixed the
// access modes to RWO writer + ReadOnlyMany reader; every standard
// CSI now works, including Hyperdisk ML. We keep this function as a
// no-deny startup probe so operators (a) see L2 is enabled and what
// SC was picked, and (b) get a clear "SC doesn't exist" error
// rather than a silent provisioning hang.
func validateL2StorageClass(ctx context.Context, kc kubernetes.Interface, scName string, log logrus.FieldLogger) error {
	sc, err := kc.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("L2: StorageClass %q lookup failed (does it exist in the cluster?): %w", scName, err)
	}

	log.WithFields(logrus.Fields{
		"storage_class": scName,
		"provisioner":   sc.Provisioner,
		"type":          sc.Parameters["type"],
	}).Info("L2 StorageClass resolved (strategy logged separately by storage-profile resolution)")
	return nil
}

// vramGBFromGPUType extracts the vRAM size in GiB from a GKE
// gpu.product label string. Examples:
//
//	"NVIDIA-H100-80GB-HBM3"  → "80"
//	"NVIDIA-A100-40GB"       → "40"
//	"NVIDIA-L4"              → ""   (no GB token, caller defaults)
//
// Returns a string so it round-trips cleanly through Manifest.SourcePodMeta
// (which is map[string]string). Callers parse to int64 via parseInt64
// when needed.
func vramGBFromGPUType(gpuType string) string {
	// Walk tokens delimited by "-" looking for one ending in "GB".
	parts := strings.Split(gpuType, "-")
	for _, p := range parts {
		if len(p) > 2 && strings.HasSuffix(p, "GB") {
			// Strip the "GB" suffix and return the prefix if it
			// parses as a positive integer.
			prefix := p[:len(p)-2]
			if n, err := parseInt64(prefix); err == nil && n > 0 {
				return fmt.Sprintf("%d", n)
			}
		}
	}
	return ""
}
