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

// Per-capture PVC backend for nvsnap#63 L2 storage tier.
//
// PerCapturePVCBackend implements checkpointstore.Backend by:
//
//   1. Acquiring a hash-keyed K8s Lease (coordination.k8s.io/v1) so
//      concurrent captures of the same content converge through a
//      single writer. Losers poll the catalog row and return the
//      winner's manifest without doing any work.
//   2. Provisioning a per-capture RWX PVC named "rwx-<short-hash>"
//      on the configured StorageClass (Hyperdisk ML in production).
//   3. Spawning a one-shot capture-write Job that copies the agent's
//      hostPath CRIU dump into the PVC mount.
//   4. Taking a VolumeSnapshot of the populated rwx PVC and creating
//      a sibling PVC "rox-<short-hash>" from the snapshot. Then
//      deleting the rwx PVC. The rox PVC is the durable, restore-
//      side artifact — only ever mounted ReadOnly.
//   5. Writing pvc_promote_state transitions (pending → writing →
//      snapshotting → ready) on the catalog row at each step, so the
//      restore-side resolver can poll and decide L1 → L2 → L3 → L4.
//
// Failure semantics: any error inside Put transitions the row to
// "failed" and returns the error. Capture itself isn't fatal because
// the caller (agent CRIU path) treats L2 promote as best-effort with
// a fall-through to L3/L4 — see internal/agent/checkpoint.go.
//
// Design: docs/L2-PVC-CRIU-DESIGN.md.

package checkpointstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// CatalogStateWriter is the minimal interface PerCapturePVCBackend
// needs from the catalog layer. On the agent side it's satisfied by
// internal/agent.pvcStateHTTPWriter, which POSTs to nvsnap-server's
// /api/v1/checkpoints/by-hash/{hash}/pvc-state endpoint; the server
// fans the write out across every catalog row sharing that hash.
// Defined here (not in the db or agent package) so the Backend can
// be unit-tested without importing either.
type CatalogStateWriter interface {
	// UpdatePVCPromoteState writes the given state and (optionally)
	// pvcName onto every catalog row sharing the content hash. The
	// hash is the only identifier the producer knows — multiple
	// rows can share it, and they all reference the same L2 artifact
	// rox-<short-hash>, so the state-machine writes fan out across
	// them. Empty pvcName preserves any prior value (COALESCE
	// semantics).
	//
	// Implementations MUST return ErrCatalogHashNotFound (wrapped) when
	// no catalog row exists for the given hash yet. PerCapturePVCBackend
	// uses this to distinguish "nothing to update, try later" from real
	// errors — the rootfs path's chain runs Backend.Put BEFORE nvsnap-
	// server has populated the hash field on the catalog row (the row
	// is created by Hook B's POST, hash is filled by a later nvsnap-server
	// observation of the manifest CM). The L2 artifacts are still safe
	// to create; the state field is reported separately by nvsnap-server
	// after the hash gets populated.
	UpdatePVCPromoteState(hash, state, pvcName string) error
}

// ErrCatalogHashNotFound is the typed error CatalogStateWriter
// implementations must return when no catalog row exists for the
// given hash. PerCapturePVCBackend.setState tolerates this by
// continuing the L2 promote; the catalog state field stays empty
// until nvsnap-server populates the hash and then re-issues a state
// write. Other errors from the writer (transport, 5xx, etc.) abort
// the Put.
var ErrCatalogHashNotFound = errors.New("catalog: no checkpoint row with hash")

// PVC promote-state strings. Duplicated here from internal/db (not
// imported) so the Backend stays SQL-layer-free. Wire contract — must
// match db.PVCPromoteState* constants.
const (
	pvcStatePending      = "pending"
	pvcStateWriting      = "writing"
	pvcStateSnapshotting = "snapshotting"
	pvcStateReady        = "ready"
	pvcStateFailed       = "failed"
)

// PerCapturePVCBackend is the L2 backend (nvsnap#63). See file header.
type PerCapturePVCBackend struct {
	// KubeClient drives PVC, Job, Lease operations.
	KubeClient kubernetes.Interface

	// DynClient drives VolumeSnapshot operations (CSI snapshotter is
	// a CRD, not in client-go core).
	DynClient dynamic.Interface

	// Catalog receives state-machine writes. The agent's
	// internal/db.DB satisfies this.
	Catalog CatalogStateWriter

	// Namespace is where the per-capture PVCs, snapshots, Jobs, and
	// leases live. Should be nvsnap-system in production.
	Namespace string

	// StorageClass is the RWX-capable StorageClass name (e.g.,
	// "hyperdisk-ml"). Required — empty means "no L2 backend
	// configured" and the agent should skip promote.
	StorageClass string

	// SnapshotClass is the VolumeSnapshotClass used for the
	// rwx-<hash> → rox-<hash> transition. Required when StorageClass
	// is set; on GKE this defaults to the CSI snapshotter class
	// "csi-gce-pd-snapshot-class".
	SnapshotClass string

	// Promoter owns the storage-specific rwx→durable-artifact transition
	// and the restore-side MountSpec (nvsnap#171). nil ⇒ applyDefaults
	// constructs a SnapshotClonePromoter from StorageClass/SnapshotClass
	// (the original Hyperdisk-ML behavior). Wiring code sets this from the
	// resolved StorageProfile to select snapshot-clone / shared-volume /
	// per-pod for the cluster's L2 storage.
	Promoter Promoter

	// WriterImage is the container image for the mount-holder pod
	// (v0.0.51 single-copy L2 writer). Reuse the agent image — it is
	// already on every node, satisfies the Kyverno registry allowlist,
	// and provides /bin/sleep + /usr/bin/test for the readiness probe.
	WriterImage string

	// WriterPullSecrets are imagePullSecret names stamped on the
	// mount-holder pod. The holder runs in the source/workload
	// namespace where NVCA's admission webhook injects an init
	// container whose image may not be cached on a fresh cluster; the
	// pull secret lets that init container (and the holder) pull.
	// Empty = none.
	WriterPullSecrets []string

	// HostPathRoot is unused after v0.0.51 — the legacy writer Job
	// mounted the host fs at this path. The mount-holder design has
	// no host mount on the holder pod (it does no I/O); the agent's
	// own /host mount (HostRoot below) is what reaches node-local
	// PV mount paths. Kept on the struct for backwards-compat with
	// callers that set it; ignored by the new code path.
	HostPathRoot string

	// HostRoot is the agent's view of the node's root filesystem,
	// typically "/host" (DaemonSet `mountPath: /host`). Joined with
	// /var/lib/kubelet/pods/<holder-uid>/... to reach the rwx PVC's
	// mount on disk. Required for the v0.0.51 mount-holder path.
	HostRoot string

	// NodeName is the node this backend's mount-holder pods get
	// pinned to (via Pod.Spec.NodeName). MUST be the node the
	// capturing agent runs on, so the PV mount lands where the
	// agent's /host can reach it. Required for the v0.0.51 path;
	// production wires this from a.config.NodeName.
	NodeName string

	// Copier streams source rootfs bytes into the rwx PV mount path
	// on the agent's node. Production: internal/agent.AgentCopier
	// (in-process, treecopy-based). Required for the v0.0.51 path.
	Copier Copier

	// SizeBytesFor returns the PVC size to provision for a given
	// hash + manifest. nvsnap#63 design: gpu_vram_bytes × gpu_count
	// × 1.2 with a 10 GiB floor. Pluggable so tests can override.
	SizeBytesFor func(hash string, m Manifest) int64

	// LeaseDuration is how long a Put holds the hash-keyed lease.
	// Should be bounded by 2× estimated writer-Job duration. Default
	// 15 min. A lost holder (Put goroutine crashed) frees the lease
	// after this elapses, allowing a fresh attempt.
	LeaseDuration time.Duration

	// PollInterval is the cadence at which loser Puts (those that
	// don't hold the lease) re-read the catalog row. Default 2s.
	PollInterval time.Duration

	// PollTimeout is the max wait for a loser before falling back
	// to peer cascade. Default 8 min (writer + snapshot at Hyperdisk
	// ML's published throughput on a 96 GB PVC).
	PollTimeout time.Duration

	// JobTimeout is the max wait for the writer Job's pod to reach
	// Succeeded. Default 30 min.
	JobTimeout time.Duration

	// SnapshotTimeout is the max wait for VolumeSnapshot.ReadyToUse
	// + the cloned rox PVC to reach Bound. Default 5 min.
	SnapshotTimeout time.Duration

	// DetachTimeout is the max wait for the rwx PV to detach from the
	// node (no VolumeAttachment references it) before snapshotting.
	// This is the durability barrier against nvsnap#105 — a block-level
	// snapshot taken before the writer's unmount flushes captures
	// truncated files. Default 3 min.
	DetachTimeout time.Duration

	// HolderID identifies this backend instance in the Lease's
	// HolderIdentity. Should be set to something unique per agent
	// pod (e.g. nodeName). Empty means a random UUID is generated
	// per Put.
	HolderID string

	Log logrus.FieldLogger
}

var _ Backend = (*PerCapturePVCBackend)(nil)

// targetNamespace returns the namespace in which to create / look up
// L2 artifacts for the given manifest. K8s PVCs are namespace-scoped
// and the consuming pod can only mount same-namespace PVCs, so all
// L2 artifacts (PVCs, snapshots, Jobs, Leases) must land in the
// source pod's namespace — not in a global nvsnap-system.
//
// Resolution order:
//
//  1. manifest.SourcePodMeta["namespace"] (the source pod's
//     namespace, set by the agent's CRIU flow)
//  2. b.Namespace as fallback (admin / sweep ops that aren't tied
//     to a specific capture; also used during early bootstrap
//     before SourcePodMeta is populated)
//
// Empty result is acceptable here; callers that demand a namespace
// will surface the empty value back to the K8s client as the
// "default" namespace, which is wrong but visible — better than a
// silent cross-namespace mount failure.
func (b *PerCapturePVCBackend) targetNamespace(m Manifest) string {
	if ns := m.SourcePodMeta["namespace"]; ns != "" {
		return ns
	}
	return b.Namespace
}

// applyDefaults fills in unset timing knobs.
func (b *PerCapturePVCBackend) applyDefaults() {
	if b.LeaseDuration == 0 {
		b.LeaseDuration = 15 * time.Minute
	}
	if b.PollInterval == 0 {
		b.PollInterval = 2 * time.Second
	}
	if b.PollTimeout == 0 {
		b.PollTimeout = 8 * time.Minute
	}
	if b.JobTimeout == 0 {
		b.JobTimeout = 30 * time.Minute
	}
	if b.SnapshotTimeout == 0 {
		b.SnapshotTimeout = 5 * time.Minute
	}
	if b.DetachTimeout == 0 {
		b.DetachTimeout = 3 * time.Minute
	}
	if b.HostPathRoot == "" {
		b.HostPathRoot = "/"
	}
	if b.HostRoot == "" {
		b.HostRoot = "/host"
	}
	if b.Log == nil {
		b.Log = logrus.NewEntry(logrus.New()).WithField("subsys", "checkpointstore.percapture_pvc")
	}
	if b.SizeBytesFor == nil {
		// Default: 96 GiB (matches vram × 1.2 for an H100 80 GB). The
		// real production wiring overrides this with a hash-driven
		// derivation in internal/agent.
		b.SizeBytesFor = func(string, Manifest) int64 { return 96 * 1024 * 1024 * 1024 }
	}
	if b.Promoter == nil {
		// Back-compat default: snapshot-clone on the configured SC with
		// ReadOnlyMany (the original Hyperdisk-ML behavior). Wiring code
		// overrides this via the resolved StorageProfile (nvsnap#171).
		b.Promoter = &SnapshotClonePromoter{
			KubeClient:      b.KubeClient,
			DynClient:       b.DynClient,
			StorageClass:    b.StorageClass,
			SnapshotClass:   b.SnapshotClass,
			ReadOnlyMany:    true,
			SnapshotTimeout: b.SnapshotTimeout,
			Log:             b.Log,
		}
	}
}

// Put materializes the dump into the per-capture PVC pipeline (see
// file header). hash is the content-addressed identity; sources are
// the CaptureSource list to copy in; m is the manifest carried
// through. Returns the manifest with PVCName set on success.
func (b *PerCapturePVCBackend) Put(ctx context.Context, hash string, sources []CaptureSource, m Manifest) (Manifest, error) {
	b.applyDefaults()
	if b.StorageClass == "" {
		return Manifest{}, errors.New("PerCapturePVCBackend: StorageClass is required")
	}
	if hash == "" {
		return Manifest{}, errors.New("PerCapturePVCBackend.Put: hash is required")
	}

	ns := b.targetNamespace(m)
	log := b.Log.WithFields(logrus.Fields{
		"hash":      ShortHash(hash),
		"namespace": ns,
	})

	// Step 1: hash-keyed cross-node serialization. Lease name:
	// nvsnap-promote-<short-hash>. First caller wins; others poll.
	// Scoped to the workload's namespace so two workloads in
	// different namespaces with the same hash each get their own
	// L2 copy (K8s PVCs can't cross namespaces).
	leaseName := "nvsnap-promote-" + ShortHash(hash)
	acquired, holderID, err := b.acquireLease(ctx, ns, leaseName)
	if err != nil {
		return Manifest{}, fmt.Errorf("acquire lease %s: %w", leaseName, err)
	}
	if !acquired {
		log.WithField("lease", leaseName).Info("lost lease race; waiting on catalog state")
		return b.waitForReady(ctx, ns, hash)
	}
	// Lease release is correct as a defer: it is registered immediately
	// after we win the lease and before any error-returning step, so
	// every exit path that holds the lease releases it. The two earlier
	// returns above (acquire error, lost race) never held the lease, so
	// they correctly skip release. The LeaseDuration TTL is only a
	// backstop for a crashed holder, not the primary release mechanism.
	defer func() {
		if relErr := b.releaseLease(ctx, ns, leaseName, holderID); relErr != nil {
			log.WithError(relErr).Warn("lease release failed; lease will expire on its own")
		}
	}()

	// Step 2: state pending → writing. Create rwx PVC + writer Job.
	if stateErr := b.setState(hash, pvcStatePending, ""); stateErr != nil {
		return Manifest{}, stateErr
	}
	rwxName := "rwx-" + ShortHash(hash)
	roxName := "rox-" + ShortHash(hash)

	// Idempotency: if rox already exists (a prior Put completed but the
	// caller didn't see the catalog row), short-circuit. Avoids
	// reprovisioning storage on a retry. Existence is enough — under
	// WaitForFirstConsumer (Hyperdisk-ML) the rox stays Pending until
	// the first restore-target pod mounts it, so gating on Bound would
	// deadlock.
	if existing, getErr := b.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, roxName, metav1.GetOptions{}); getErr == nil && existing != nil {
		log.WithField("pvc", roxName).WithField("phase", existing.Status.Phase).Info("rox PVC already exists; treating as idempotent success")
		_ = b.setState(hash, pvcStateReady, roxName)
		m.Hash = hash
		return m, nil
	}

	rwxPVC, err := b.createRWXPVC(ctx, ns, rwxName, b.SizeBytesFor(hash, m))
	if err != nil {
		_ = b.setState(hash, pvcStateFailed, "")
		return Manifest{}, fmt.Errorf("create rwx PVC: %w", err)
	}

	if err := b.setState(hash, pvcStateWriting, ""); err != nil {
		return Manifest{}, err
	}
	if err := b.runMountHolderCopy(ctx, ns, hash, rwxName, rwxPVC.UID, sources); err != nil {
		_ = b.setState(hash, pvcStateFailed, "")
		return Manifest{}, fmt.Errorf("mount-holder copy: %w", err)
	}

	// Durability barrier before snapshot (nvsnap#105). The mount-holder
	// copy wrote through the node page cache with no fsync, and
	// snapshotAndClone takes a BLOCK-level VolumeSnapshot. If the snapshot
	// fires before writeback completes, it captures truncated files and
	// the rox (plus every fan-out pod) gets corrupt weights. Deleting the
	// holder pod only waits for the pod to be gone; the CSI detach (which
	// is what guarantees kubelet's unmount flushed the filesystem to disk)
	// lags that. Wait for the rwx PV to fully detach before snapshotting.
	if err := b.waitForRWXDetach(ctx, ns, rwxName); err != nil {
		_ = b.setState(hash, pvcStateFailed, "")
		return Manifest{}, fmt.Errorf("wait rwx detach before snapshot: %w", err)
	}

	// Step 3: state promoting (legacy string "snapshotting" — wire
	// contract, unchanged). Delegate the storage-specific rwx→durable
	// transition + writer disposal to the Promoter (nvsnap#171).
	if err := b.setState(hash, pvcStateSnapshotting, ""); err != nil {
		return Manifest{}, err
	}
	res, promoteErr := b.Promoter.Promote(ctx, PromoteInput{
		Hash:          hash,
		Namespace:     ns,
		WriterPVCName: rwxName,
		SizeBytes:     b.SizeBytesFor(hash, m),
	})
	if promoteErr != nil {
		_ = b.setState(hash, pvcStateFailed, "")
		return Manifest{}, fmt.Errorf("promote: %w", promoteErr)
	}

	// Step 4: state ready. publishName is the shared claim when the
	// strategy produced one; otherwise the logical rox-<hash> handle
	// (per-pod strategies create the actual claim lazily at mount).
	publishName := res.SharedClaimName
	if publishName == "" {
		publishName = roxName
	}
	if err := b.setState(hash, pvcStateReady, publishName); err != nil {
		return Manifest{}, fmt.Errorf("publish ready: %w", err)
	}

	log.WithFields(logrus.Fields{
		"artifact": publishName, "per_pod": res.PerPod, "reused_writer": res.ReusedWriterVolume,
	}).Info("L2 promote complete")
	m.Hash = hash
	return m, nil
}

// setState writes the catalog row. ErrCatalogHashNotFound is tolerated:
// the rootfs path's Chain runs Backend.Put BEFORE nvsnap-server populates
// the hash field on the catalog row, so a 404-by-hash lookup is expected
// on early state writes. The L2 artifact creation continues unaffected;
// nvsnap-server's runRootfsCheckpoint issues the final state=ready write
// after writing the hash. Other errors abort the Put.
func (b *PerCapturePVCBackend) setState(hash, state, pvcName string) error {
	if b.Catalog == nil {
		return nil
	}
	if err := b.Catalog.UpdatePVCPromoteState(hash, state, pvcName); err != nil {
		if errors.Is(err, ErrCatalogHashNotFound) {
			// The hash lands on the row shortly after, so dropping the
			// write here loses it permanently unless someone else
			// re-issues it. nvsnap-server does try, but only once at
			// end-of-capture, which races this promote and loses for
			// any capture large enough to matter -- a 14 GB cachedir
			// capture promoted ~2min after that check had already run,
			// leaving pvc_promote_state empty forever. Retry the
			// terminal state in the background until the row catches
			// up; intermediate states stay best-effort because a later
			// write supersedes them anyway.
			if state == pvcStateReady {
				go b.retryTerminalState(hash, state, pvcName)
				return nil
			}
			if b.Log != nil {
				b.Log.WithFields(logrus.Fields{
					"hash":     hash,
					"state":    state,
					"pvc_name": pvcName,
				}).Debug("catalog row not yet populated with hash; skipping intermediate state write")
			}
			return nil
		}
		return fmt.Errorf("set pvc_promote_state=%s: %w", state, err)
	}
	return nil
}

// stateRetryAttempts and stateRetryInterval bound the catch-up window for a
// terminal state write. The gap being covered is "capture finished but the
// server has not written the hash yet", which is seconds; the ceiling is
// generous so a slow catalog does not silently drop the write, and bounded so
// a genuinely absent row does not leak a goroutine.
var (
	stateRetryAttempts = 60
	stateRetryInterval = 5 * time.Second
)

// retryTerminalState re-issues a state write until the catalog row carries the
// hash. Consumers that gate on pvc_promote_state (NVCA warm-start) never
// observe readiness if this write is lost, so it is worth chasing.
func (b *PerCapturePVCBackend) retryTerminalState(hash, state, pvcName string) {
	for i := 0; i < stateRetryAttempts; i++ {
		time.Sleep(stateRetryInterval)
		err := b.Catalog.UpdatePVCPromoteState(hash, state, pvcName)
		if err == nil {
			if b.Log != nil {
				b.Log.WithFields(logrus.Fields{
					"hash": hash, "state": state, "pvc_name": pvcName,
					"attempts": i + 1,
				}).Info("pvc_promote_state written after catalog row caught up")
			}
			return
		}
		if !errors.Is(err, ErrCatalogHashNotFound) {
			if b.Log != nil {
				b.Log.WithError(err).WithField("hash", hash).
					Warn("pvc_promote_state retry failed; consumers gating on it will not see ready")
			}
			return
		}
	}
	if b.Log != nil {
		b.Log.WithFields(logrus.Fields{
			"hash": hash, "state": state,
		}).Warn("gave up writing pvc_promote_state: catalog row never carried the hash")
	}
}

// acquireLease tries to take ownership of a hash-keyed Lease. Returns
// (true, holderID) on success, (false, "") if another holder owns it
// (caller should poll the catalog instead).
//
// Holder identity is set from b.HolderID (if non-empty) or a per-Put
// UUID otherwise. Lease duration is b.LeaseDuration.
func (b *PerCapturePVCBackend) acquireLease(ctx context.Context, _, name string) (acquired bool, holderID string, err error) {
	holder := b.HolderID
	if holder == "" {
		holder = fmt.Sprintf("nvsnap-put-%d", time.Now().UnixNano())
	}
	durationSeconds := int32(b.LeaseDuration.Seconds())

	// Try Create first (lease doesn't exist).
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/lease-kind":         "pvc-promote",
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &durationSeconds,
			AcquireTime:          &metav1.MicroTime{Time: time.Now()},
			RenewTime:            &metav1.MicroTime{Time: time.Now()},
		},
	}
	_, err = b.KubeClient.CoordinationV1().Leases(b.Namespace).Create(ctx, lease, metav1.CreateOptions{})
	if err == nil {
		return true, holder, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, "", err
	}

	// Lease exists. Try to take it over IF it's expired.
	existing, getErr := b.KubeClient.CoordinationV1().Leases(b.Namespace).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil {
		return false, "", fmt.Errorf("get existing lease: %w", getErr)
	}
	if existing.Spec.RenewTime != nil && existing.Spec.LeaseDurationSeconds != nil {
		exp := existing.Spec.RenewTime.Add(time.Duration(*existing.Spec.LeaseDurationSeconds) * time.Second)
		if time.Now().Before(exp) {
			// Still held by someone else.
			return false, "", nil
		}
	}
	// Take it over.
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &durationSeconds
	existing.Spec.AcquireTime = &metav1.MicroTime{Time: time.Now()}
	existing.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	if _, err := b.KubeClient.CoordinationV1().Leases(b.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		// Lost the race to another taker.
		if apierrors.IsConflict(err) {
			return false, "", nil
		}
		return false, "", err
	}
	return true, holder, nil
}

// releaseLease deletes the lease so the next Put doesn't have to wait
// for the duration to expire. Safe if the lease was already deleted
// or taken over by someone else.
func (b *PerCapturePVCBackend) releaseLease(ctx context.Context, ns, name, holder string) error {
	existing, err := b.KubeClient.CoordinationV1().Leases(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if existing.Spec.HolderIdentity == nil || *existing.Spec.HolderIdentity != holder {
		// Someone else took it over (or we crashed and another holder
		// inherited). Don't delete it.
		return nil
	}
	return b.KubeClient.CoordinationV1().Leases(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// waitForReady is the loser path. Polls the catalog row until state
// reaches ready (success) or failed (give up). Returns the manifest
// with PVCName from the row on ready, or an error on failed/timeout.
func (b *PerCapturePVCBackend) waitForReady(ctx context.Context, ns, hash string) (Manifest, error) {
	// The Backend can't read the catalog directly (CatalogStateWriter
	// is write-only by design — keeps the cross-package contract
	// minimal). The catalog row's terminal state instead is observed
	// via the Lease lifecycle: the winner releases the lease only
	// after setState(ready) or setState(failed). So loser polls the
	// PVC existence directly.
	roxName := "rox-" + ShortHash(hash)
	timeout := b.PollTimeout
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pvc, err := b.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, roxName, metav1.GetOptions{})
		if err == nil && pvc.Status.Phase == corev1.ClaimBound {
			return Manifest{Hash: hash}, nil
		}
		select {
		case <-ctx.Done():
			return Manifest{}, ctx.Err()
		case <-time.After(b.PollInterval):
		}
	}
	return Manifest{}, fmt.Errorf("timed out waiting for rox-%s to become Bound after %s", ShortHash(hash), timeout)
}

// createRWXPVC provisions the writer-mounted PVC. The "rwx-" name
// prefix is historical — it predates the design's pivot to a
// snapshot-and-clone reader path. The PVC is mounted only by the
// mount-holder pod (a single pod, lease-serialized one-per-hash), so
// the access mode is **ReadWriteOnce** — NOT ReadWriteMany. Until
// this fix the code requested RWX, which excluded every block-storage
// CSI (pd.csi, ebs.csi, disk.csi); only Filestore/EFS/etc. could
// satisfy it. With RWO any standard CSI works — Hyperdisk ML is
// supported again. See nvsnap#81 + the GCP-H100-a 2026-06-02
// post-mortem on the L2 silent-loop.
//
// Idempotent: if the PVC already exists, returns the existing object.
// Returns the (post-API-server) PVC so callers can use its UID for
// OwnerReference back-pointers (e.g., MountHolder's GC-safety net).
func (b *PerCapturePVCBackend) createRWXPVC(ctx context.Context, ns, name string, sizeBytes int64) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/per-capture":        "true",
				"nvsnap.io/role":               "writer",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &b.StorageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(sizeBytes, resource.BinarySI),
				},
			},
		},
	}
	created, err := b.KubeClient.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	// Idempotent re-attach: someone already created it (this Put is a
	// retry, or the crash-recovery path). Fetch the existing object so
	// the caller has the UID for OwnerReference.
	existing, getErr := b.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil {
		return nil, fmt.Errorf("rwx PVC %s/%s exists but Get failed: %w", ns, name, getErr)
	}
	return existing, nil
}

// runMountHolderCopy implements the v0.0.51 single-copy L2 writer
// (docs/design/ROOTFS-EVERYWHERE.md). Replaces the legacy writer Job
// which couldn't run privileged in Kyverno-enforced source namespaces.
//
// Flow:
//  1. Create a tiny mount-holder pod (Kyverno-compliant, does no I/O)
//     in the source pod's namespace. Pod's OwnerReference points at
//     the rwx PVC so a stray agent crash leaves the pod GC'd when
//     the PVC is reclaimed.
//  2. Wait for the pod to be Running + Ready — at that point kubelet
//     has attached the PV and mounted it at
//     /var/lib/kubelet/pods/<holder-uid>/volumes/kubernetes.io~csi/<pv>/mount
//     on the agent's node.
//  3. Resolve that mount path through the agent's /host hostPath view
//     and call b.Copier.Copy directly. Single bandwidth pass, no
//     staging hop, no privileged pod in the source namespace.
//  4. Delete the mount-holder; wait for kubelet's PV unmount before
//     returning so the caller can immediately snapshot.
//
// Errors at any step: returns the error; caller (Put) transitions
// pvc_promote_state=failed and cleans up the rwx PVC. The holder
// pod's OwnerReference back to the rwx PVC means its eventual GC is
// handled regardless of which step failed.
func (b *PerCapturePVCBackend) runMountHolderCopy(ctx context.Context, ns, hash, rwxName string, rwxUID types.UID, sources []CaptureSource) error {
	if b.Copier == nil {
		return fmt.Errorf("PerCapturePVCBackend: Copier is nil (v0.0.51 requires it; see internal/agent.AgentCopier)")
	}
	if b.NodeName == "" {
		return fmt.Errorf("PerCapturePVCBackend: NodeName is required (v0.0.51 mount-holder must be pinned to capturing agent's node)")
	}
	holderName := "mh-" + ShortHash(hash)
	log := b.Log.WithFields(logrus.Fields{
		"hash":      ShortHash(hash),
		"namespace": ns,
		"holder":    holderName,
		"rwx_pvc":   rwxName,
		"node":      b.NodeName,
	})

	// b.Log.WithFields returns *logrus.Entry directly — no cast needed.
	holder := NewMountHolder(b.KubeClient, log,
		ns, holderName, b.NodeName, rwxName, rwxUID,
		b.WriterImage, b.HostRoot, b.WriterPullSecrets)

	if err := holder.Create(ctx); err != nil {
		return fmt.Errorf("create mount-holder: %w", err)
	}
	// Best-effort cleanup: even if we error out mid-way, leaving the
	// pod around just costs a sleeping pause container until its
	// OwnerReference (rwx PVC) is GC'd.
	defer func() {
		// Use a fresh, short-lived context for cleanup — the outer
		// ctx may already be cancelled/timed-out by this point.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), MountHolderDeleteTimeout+5*time.Second)
		defer cancel()
		if delErr := holder.Delete(cleanupCtx); delErr != nil {
			log.WithError(delErr).Warn("mount-holder delete failed; OwnerReference will GC on rwx PVC reclaim")
		}
	}()

	if err := holder.WaitRunning(ctx); err != nil {
		return fmt.Errorf("wait mount-holder Running: %w", err)
	}
	destPath, err := holder.PVMountPath()
	if err != nil {
		return fmt.Errorf("resolve PV mount path: %w", err)
	}
	log.WithField("dest_path", destPath).Info("starting agent-direct copy to rwx PV")

	bytes, files, err := b.Copier.Copy(ctx, destPath, sources)
	if err != nil {
		return fmt.Errorf("agent copy → %s: %w", destPath, err)
	}
	log.WithFields(logrus.Fields{
		"bytes": bytes, "files": files,
	}).Info("agent-direct copy complete")
	return nil
}

// waitForRWXDetach resolves the rwx PVC's bound PV and blocks until no
// VolumeAttachment references it — i.e. kubelet has unmounted the volume
// from the node (flushing the filesystem to the underlying disk) and the
// CSI attacher has detached it. This is the durability barrier before the
// block-level snapshot (nvsnap#105): close()/sendfile leave dirty pages in
// the node page cache, and a snapshot taken before writeback completes
// captures truncated files. Mirrors NVCA model-cache's waitForVolumeDetach
// and is storage-agnostic (VolumeAttachment is a core CSI object), so it is
// needed for every backend, not just Hyperdisk-ML.
func (b *PerCapturePVCBackend) waitForRWXDetach(ctx context.Context, ns, rwxName string) error {
	pvc, err := b.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, rwxName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get rwx PVC %s: %w", rwxName, err)
	}
	pvName := pvc.Spec.VolumeName
	if pvName == "" {
		// Unbound: nothing ever attached, so nothing to flush/wait on.
		b.Log.WithField("rwx_pvc", rwxName).Warn("rwx PVC has no bound PV; skipping detach wait")
		return nil
	}
	log := b.Log.WithFields(logrus.Fields{"rwx_pvc": rwxName, "pv": pvName})
	log.Info("waiting for rwx volume to detach before snapshot")
	return wait.PollUntilContextTimeout(ctx, time.Second, b.DetachTimeout, true,
		func(ctx context.Context) (bool, error) {
			vaList, err := b.KubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Errorf("list VolumeAttachments: %w", err)
			}
			for i := range vaList.Items {
				if src := vaList.Items[i].Spec.Source.PersistentVolumeName; src != nil && *src == pvName {
					return false, nil // still attached; keep polling
				}
			}
			log.Info("rwx volume detached; safe to snapshot")
			return true, nil
		})
}

// Stat returns the manifest if the rox PVC exists and is Bound in
// the backend's default namespace (b.Namespace). Used today by admin /
// sweep paths that don't have a specific workload namespace in hand.
// For namespace-aware lookups (webhook restore-side), use Mount with
// vol.Namespace populated, which scopes the rox lookup to the pod's
// own namespace — the only namespace it could legally mount from.
//
// Returns ErrNotFound when the rox PVC is missing.
func (b *PerCapturePVCBackend) Stat(ctx context.Context, hash string) (Manifest, error) {
	b.applyDefaults()
	return b.StatInNamespace(ctx, hash, b.Namespace)
}

// StatInNamespace checks for rox-<hash> in a specific namespace rather
// than b.Namespace. The cross-cluster replicate path needs this: rox
// PVCs are created in the source pod's namespace (per targetNamespace,
// nvsnap#82), NOT b.Namespace (nvsnap-system), so a b.Namespace-scoped
// Stat would never see a replicated rox and the idempotency check
// would mis-report. The replicate path passes the workload namespace
// from the pulled manifest's SourcePodMeta.
//
// Existence is enough. WaitForFirstConsumer storage classes
// (Hyperdisk-ML on GKE) keep the rox PVC Pending until the first
// restore-target pod mounts it — gating on ClaimBound would lock
// restore out of an already-promoted checkpoint. Mount() follows the
// same rule (see nvsnap#147).
func (b *PerCapturePVCBackend) StatInNamespace(ctx context.Context, hash, namespace string) (Manifest, error) {
	b.applyDefaults()
	roxName := "rox-" + ShortHash(hash)
	_, err := b.KubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, roxName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	return Manifest{Hash: hash}, nil
}

// Get is a no-op for this backend — restore-side mounts the rox PVC
// directly via the webhook pod-spec mutation; no agent-side copy is
// needed. Returning ErrNotFound here would short-circuit the cascade
// resolver in a confusing way; instead we return the manifest if rox
// exists (same as Stat).
func (b *PerCapturePVCBackend) Get(ctx context.Context, hash, _ string) (Manifest, error) {
	return b.Stat(ctx, hash)
}

// Mount returns the pod-spec fragments needed to mount the rox-<hash>
// PVC ReadOnly at vol.MountPath. Called by the admission webhook
// when stamping a restore-from pod.
//
// vol.Namespace MUST be the consuming pod's namespace — K8s only
// lets a pod mount PVCs in its own namespace, and L2 creates each
// rox PVC in the source pod's namespace per nvsnap#82. Falls back
// to b.Namespace if vol.Namespace is empty (compat path for older
// callers; production webhook always sets it).
//
// Returns ErrNotFound only when rox-<hash> PVC does not exist at all
// (no capture, or capture pre-dates L2). Pending PVCs ARE returned
// successfully — under WaitForFirstConsumer the PVC binds only when
// a pod consumes it, so requiring Bound here creates a deadlock
// (webhook needs Bound to inject; PVC needs a pod with the inject
// to bind). The webhook pairs this Mount with a nvsnap-l2-wait init
// container that gates the main container on the catalog's
// pvc_promote_state == "ready" — by which point the rox PVC has
// been created and kubelet's WFC binder can complete the bind.
func (b *PerCapturePVCBackend) Mount(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	b.applyDefaults()
	if vol.Namespace == "" {
		vol.Namespace = b.Namespace
	}
	// Storage-specific: shared-ROX returns the one rox-<hash> claim;
	// per-pod clones a fresh RWO PVC; shared-volume binds a static PV.
	return b.Promoter.MountSpec(ctx, hash, vol)
}

// Delete removes the rox PVC + any leftover rwx PVC + snapshot from
// the backend's default namespace. Called by the retention controller
// / cascade-delete (nvsnap#74); idempotent.
//
// Today's signature takes only hash because callers don't pass a
// namespace yet. L2 artifacts now live per-workload-namespace
// (nvsnap#82), so this default-namespace cleanup is a partial-impl —
// it won't reach PVCs in other namespaces. Tracked as a follow-up;
// the retention controller needs to enumerate catalog rows per
// namespace and pass it in.
func (b *PerCapturePVCBackend) Delete(ctx context.Context, hash string) error {
	b.applyDefaults()
	ns := b.Namespace

	var errs []string
	// Storage-specific artifact teardown (rox/snapshot/secondary PVs/…).
	if err := b.Promoter.Delete(ctx, hash, ns); err != nil {
		errs = append(errs, err.Error())
	}
	// Lease cleanup is storage-agnostic (a crashed Put may have left it).
	leaseName := "nvsnap-promote-" + ShortHash(hash)
	if err := b.KubeClient.CoordinationV1().Leases(ns).Delete(ctx, leaseName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, fmt.Sprintf("lease %s: %v", leaseName, err))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// encodeCapturePlan is intentionally removed. v0.0.49 and earlier
// used it to serialize a NVSNAP_CAPTURE_PLAN env-var for the writer
// Job's capture-write subcommand; v0.0.51 calls into the in-process
// Copier directly (see runMountHolderCopy). The capture-write
// subcommand itself still exists in cmd/agent for backwards-compat
// with any in-flight Jobs from the legacy code path; that subcommand
// is no longer reachable from production callers and is scheduled
// for removal in a follow-up cleanup commit.
