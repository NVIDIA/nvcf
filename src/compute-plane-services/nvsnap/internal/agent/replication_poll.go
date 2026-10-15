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
	"regexp"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	coordv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
)

// replicationPollerLeaseName is the K8s Lease that elects a single poller
// across the DaemonSet. Only the holder lists the home bucket + pulls on a
// tick, so all N agents don't stampede the object store + the L2 promote
// path simultaneously.
const replicationPollerLeaseName = "nvsnap-replication-poller"

// manifestKeyRe matches "<64-hex-hash>/manifest.json" — the per-capture
// manifest object. Tree files ("<hash>/tree/...") and any other key are
// ignored, so listing the whole bucket and filtering yields exactly the
// set of capture hashes present.
var manifestKeyRe = regexp.MustCompile(`^([0-9a-f]{64})/manifest\.json$`)

// startReplicationPoller launches the cross-cluster auto-pull loop in a
// goroutine and returns immediately. On each tick the lease leader lists
// this cluster's home bucket, and for every capture it finds that this
// cluster can actually run (GPU/driver compatible) calls
// ReplicateFromObjectStore — the same idempotent pull the manual POST
// /v1/replicate/{hash} uses. This is "Option B": a remote cluster warms
// itself from its home bucket without an operator firing a replicate per
// hash.
//
// No-op (logs once) when replication is disabled (a.objectStoreHome == nil)
// or the interval is <= 0 (the default — the poller is opt-in via
// --replication-poll-interval / NVSNAP_REPLICATION_POLL_INTERVAL).
func (a *Agent) startReplicationPoller(ctx context.Context) {
	interval := a.config.Replication.PollInterval
	if a.objectStoreHome == nil || interval <= 0 {
		a.log.WithFields(map[string]any{
			"replication_enabled": a.objectStoreHome != nil,
			"interval":            interval.String(),
		}).Info("replication auto-pull poller disabled")
		return
	}
	log := a.log.WithField("subsys", "replication-poller")
	log.WithField("interval", interval.String()).Info("replication auto-pull poller starting")

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.replicationPollTick(ctx)
			}
		}
	}()
}

// replicationPollTick runs one poll cycle. Only the lease leader does work; a
// non-leader (or a lease-acquire error) skips the tick. Errors on
// individual hashes are non-fatal — the next hash is still tried, and a
// transient miss is retried on the next tick.
func (a *Agent) replicationPollTick(ctx context.Context) {
	log := a.log.WithField("subsys", "replication-poller")

	leader, err := a.acquireReplicationPollerLease(ctx)
	if err != nil {
		log.WithError(err).Debug("replication poller: lease acquire failed; skipping tick")
		return
	}
	if !leader {
		log.Debug("replication poller: not leader; skipping tick")
		return
	}

	objs, err := a.objectStoreHome.List(ctx, "")
	if err != nil {
		log.WithError(err).Warn("replication poller: list home bucket failed; skipping tick")
		return
	}
	hashes := extractCaptureHashes(objs)
	log.WithField("captures", len(hashes)).Debug("replication poller: listed home bucket")

	for _, hash := range hashes {
		if ctx.Err() != nil {
			return
		}
		a.replicateOne(ctx, hash)
	}
}

// extractCaptureHashes returns the set of capture hashes present in a
// bucket listing, by matching "<hash>/manifest.json" keys. Non-manifest
// keys (tree files, partial uploads) are ignored. The result is
// deduplicated (a listing has one manifest per hash anyway, but the
// filter is robust to duplicates).
func extractCaptureHashes(objs []objectstore.ObjectInfo) []string {
	seen := make(map[string]struct{}, len(objs))
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		m := manifestKeyRe.FindStringSubmatch(o.Key)
		if m == nil {
			continue
		}
		h := m[1]
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// replicateOne fetches a capture's manifest, applies the GPU/driver compat
// gate, and (if compatible) pulls it via ReplicateFromObjectStore.
// ReplicateFromObjectStore is itself idempotent — it returns fast when the
// L2 rox PVC is already present — so this is safe to call every tick. All
// outcomes are logged; errors are swallowed (returned to no one) so the
// caller continues to the next hash.
func (a *Agent) replicateOne(ctx context.Context, hash string) {
	log := a.log.WithField("subsys", "replication-poller").WithField("hash", hash)

	mf, err := a.fetchManifest(ctx, a.objectStoreHome, hash)
	if err != nil {
		log.WithError(err).Warn("replication poller: fetch manifest failed; skipping")
		return
	}
	if !a.captureIsCompatible(mf.SourcePodMeta, log) {
		return
	}

	if err := a.ReplicateFromObjectStore(ctx, hash); err != nil {
		log.WithError(err).Warn("replication poller: replicate failed")
		return
	}
	log.Info("replication poller: capture pulled (or already present)")
}

// captureIsCompatible is the safety gate: never auto-pull a capture this
// cluster can't run. It compares the capture's source driver major +
// GPU compute capability (from the manifest's SourcePodMeta) to this
// node's local values.
//
// Fail-open per dimension: if the LOCAL value is unknown (zero driver
// major / empty compute capability) we don't gate on that dimension —
// we can't prove an incompatibility we can't measure, and the existing
// manual replicate path has no such gate either. We only SKIP when both
// sides of a dimension are known AND mismatch. Malformed manifest values
// (e.g. a non-numeric driver_major) also fail open.
func (a *Agent) captureIsCompatible(meta map[string]string, log *logrus.Entry) bool {
	localDriver := a.config.RootfsCapture.CUDADriverMajor
	localCC := a.localGPUComputeCapability()

	srcDriverStr := meta["driver_major"]
	srcCC := meta["gpu_compute_capability"]

	if localDriver > 0 && srcDriverStr != "" {
		if srcDriver, err := strconv.Atoi(srcDriverStr); err == nil && srcDriver != localDriver {
			log.WithFields(map[string]any{
				"source_driver_major": srcDriver,
				"local_driver_major":  localDriver,
			}).Info("replication poller: skipping incompatible capture: driver mismatch")
			return false
		}
	}

	if localCC != "" && srcCC != "" && srcCC != localCC {
		log.WithFields(map[string]any{
			"source_gpu_compute_capability": srcCC,
			"local_gpu_compute_capability":  localCC,
		}).Info("replication poller: skipping incompatible capture: gpu mismatch")
		return false
	}

	return true
}

// localGPUComputeCapability reads this node's GPU compute capability
// ("9.0" for H100) from its K8s node labels — the same source the
// catalog uses (populateFromNode). Returns "" when the node can't be
// read or the label is absent, which makes the compat gate fail open on
// the GPU dimension.
func (a *Agent) localGPUComputeCapability() string {
	if a.kubeClient == nil || a.config.NodeName == "" {
		return ""
	}
	node, err := a.kubeClient.CoreV1().Nodes().Get(context.Background(), a.config.NodeName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	var info CatalogInfo
	populateFromNode(&info, node)
	return info.GPUComputeCapability
}

// acquireReplicationPollerLease tries to take the poller's leader Lease.
// Returns (true) if this agent holds it for the current period, (false) if
// another agent holds a non-expired lease. Mirrors the per-capture PVC
// backend's lease dance: Create, else take over an expired lease.
func (a *Agent) acquireReplicationPollerLease(ctx context.Context) (bool, error) {
	ns := a.replicationPollerNamespace()
	holder := a.config.NodeName
	if holder == "" {
		holder = "nvsnap-agent"
	}
	// The lease must outlive a tick so a slow leader keeps the lease
	// across a long pull; tie it to the poll interval (2x, min 60s).
	leaseSeconds := int32(a.config.Replication.PollInterval.Seconds() * 2)
	if leaseSeconds < 60 {
		leaseSeconds = 60
	}
	now := metav1.MicroTime{Time: time.Now()}

	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicationPollerLeaseName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/lease-kind":         "replication-poller",
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &leaseSeconds,
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}
	_, err := a.kubeClient.CoordinationV1().Leases(ns).Create(ctx, lease, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}

	existing, getErr := a.kubeClient.CoordinationV1().Leases(ns).Get(ctx, replicationPollerLeaseName, metav1.GetOptions{})
	if getErr != nil {
		return false, getErr
	}
	// If we already hold it, renew and keep going (a steady leader).
	if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity == holder {
		existing.Spec.LeaseDurationSeconds = &leaseSeconds
		existing.Spec.RenewTime = &now
		if _, uerr := a.kubeClient.CoordinationV1().Leases(ns).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return false, nil
			}
			return false, uerr
		}
		return true, nil
	}
	// Held by someone else — only take over if expired.
	if existing.Spec.RenewTime != nil && existing.Spec.LeaseDurationSeconds != nil {
		exp := existing.Spec.RenewTime.Add(time.Duration(*existing.Spec.LeaseDurationSeconds) * time.Second)
		if time.Now().Before(exp) {
			return false, nil
		}
	}
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &leaseSeconds
	existing.Spec.AcquireTime = &now
	existing.Spec.RenewTime = &now
	if _, uerr := a.kubeClient.CoordinationV1().Leases(ns).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
		if apierrors.IsConflict(uerr) {
			return false, nil
		}
		return false, uerr
	}
	return true, nil
}

// replicationPollerNamespace is where the poller Lease lives — the L2
// namespace (PVCs/Leases/Jobs), defaulting to nvsnap-system.
func (a *Agent) replicationPollerNamespace() string {
	if a.config.L2.Namespace != "" {
		return a.config.L2.Namespace
	}
	return "nvsnap-system"
}
