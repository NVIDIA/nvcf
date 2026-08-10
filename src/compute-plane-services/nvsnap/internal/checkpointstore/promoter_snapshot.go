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

// SnapshotClonePromoter — the CSI VolumeSnapshot + clone L2 strategy
// (nvsnap#171). This is the original Hyperdisk-ML behavior, extracted
// verbatim from PerCapturePVCBackend so other strategies (shared-volume,
// per-pod) can slot in beside it.
//
// Promote: take a VolumeSnapshot of the populated writer PVC, wait for
// readyToUse, clone a rox-<hash> PVC from the snapshot, delete the
// writer PVC. The snapshot is the rox's lifelong dataSource (never
// deleted until the rox is GC'd). When the SC supports ReadOnlyMany
// (Hyperdisk-ML) the rox is a single shared ROX claim N pods mount; when
// it does not (EBS), each restore pod gets its own RWO clone in
// MountSpec (PerPod) — see ReadOnlyMany below.

package checkpointstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// volumeSnapshotGVR is the snapshot.storage.k8s.io GVR — a CRD, accessed
// via dynamic.Interface (not in client-go core).
var volumeSnapshotGVR = schema.GroupVersionResource{
	Group:    "snapshot.storage.k8s.io",
	Version:  "v1",
	Resource: "volumesnapshots",
}

// SnapshotClonePromoter implements Promoter via CSI VolumeSnapshot+clone.
type SnapshotClonePromoter struct {
	KubeClient kubernetes.Interface
	DynClient  dynamic.Interface

	// StorageClass is the L2 SC the rox clone is provisioned on (same SC
	// the writer PVC used).
	StorageClass string
	// SnapshotClass is the VolumeSnapshotClass for the rwx→rox snapshot.
	SnapshotClass string

	// ReadOnlyMany reflects whether StorageClass can bind a single volume
	// ReadOnlyMany across nodes. True (Hyperdisk-ML) ⇒ one shared rox
	// claim. False (EBS) ⇒ per-pod RWO clone in MountSpec. Set from the
	// resolved StorageProfile.
	ReadOnlyMany bool

	// SnapshotTimeout caps the wait for VolumeSnapshot.ReadyToUse.
	SnapshotTimeout time.Duration

	Log logrus.FieldLogger
}

var _ Promoter = (*SnapshotClonePromoter)(nil)

func (p *SnapshotClonePromoter) applyDefaults() {
	if p.SnapshotTimeout == 0 {
		p.SnapshotTimeout = 5 * time.Minute
	}
	if p.Log == nil {
		p.Log = logrus.NewEntry(logrus.New()).WithField("subsys", "checkpointstore.snapshot_promoter")
	}
}

// Caps reports snapshot-clone capabilities (snapshots + ReadOnlyMany per
// the resolved profile).
func (p *SnapshotClonePromoter) Caps() StorageCaps {
	return StorageCaps{
		Snapshots:    true,
		ReadOnlyMany: p.ReadOnlyMany,
		SharedVolume: false,
		Strategy:     "snapshot-clone",
	}
}

// Promote snapshots the writer PVC and clones a rox-<hash> PVC from the
// snapshot, then deletes the writer PVC (best-effort). For ROX-capable
// SCs the rox is the single shared claim (SharedClaimName); for RWO-only
// SCs the rox clone is deferred per-pod and Promote only ensures the
// snapshot exists (PerPod).
func (p *SnapshotClonePromoter) Promote(ctx context.Context, in PromoteInput) (PromoteResult, error) {
	p.applyDefaults()
	ns := in.Namespace
	rwxName := in.WriterPVCName
	roxName := "rox-" + ShortHash(in.Hash)
	log := p.Log.WithFields(logrus.Fields{"hash": ShortHash(in.Hash), "namespace": ns})

	if err := p.snapshotReady(ctx, ns, in.Hash, rwxName); err != nil {
		return PromoteResult{}, err
	}

	// RWO-only storage can't bind a shared ROX claim; defer to per-pod
	// clones created in MountSpec. The snapshot (the per-pod dataSource)
	// is ready; the writer PVC can be reclaimed.
	if !p.ReadOnlyMany {
		if err := p.deletePVC(ctx, ns, rwxName); err != nil {
			log.WithError(err).Warn("writer PVC delete failed; orphan-gc will reclaim")
		}
		log.Info("L2 promote complete (per-pod clone; snapshot is the dataSource)")
		return PromoteResult{PerPod: true}, nil
	}

	if err := p.cloneROX(ctx, ns, in.Hash, rwxName, roxName); err != nil {
		return PromoteResult{}, err
	}

	// Writer PVC is disposable now: the rox clones from the SNAPSHOT, not
	// the writer. The snapshot must NOT be deleted here — under
	// WaitForFirstConsumer the rox doesn't actually clone from it until a
	// restore pod first mounts it; it is the rox's lifelong dataSource
	// (deleted only when the rox is GC'd, in Delete()).
	if err := p.deletePVC(ctx, ns, rwxName); err != nil {
		log.WithError(err).Warn("writer PVC delete failed; orphan-gc will reclaim")
	}
	log.WithField("rox", roxName).Info("L2 promote complete (shared ROX clone)")
	return PromoteResult{SharedClaimName: roxName}, nil
}

// snapshotReady creates the VolumeSnapshot of rwxName and blocks until
// readyToUse. Idempotent (AlreadyExists tolerated).
func (p *SnapshotClonePromoter) snapshotReady(ctx context.Context, ns, hash, rwxName string) error {
	snapName := "snap-" + ShortHash(hash)
	snap := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata": map[string]any{
			"name":      snapName,
			"namespace": ns,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/per-capture":        "true",
			},
		},
		"spec": map[string]any{
			"volumeSnapshotClassName": p.SnapshotClass,
			"source": map[string]any{
				"persistentVolumeClaimName": rwxName,
			},
		},
	}}
	if _, err := p.DynClient.Resource(volumeSnapshotGVR).Namespace(ns).Create(ctx, snap, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create VolumeSnapshot: %w", err)
		}
	}
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, p.SnapshotTimeout, true, func(ctx context.Context) (bool, error) {
		got, err := p.DynClient.Resource(volumeSnapshotGVR).Namespace(ns).Get(ctx, snapName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		ready, _, _ := unstructured.NestedBool(got.Object, "status", "readyToUse")
		return ready, nil
	}); err != nil {
		return fmt.Errorf("snapshot %s not ready: %w", snapName, err)
	}
	return nil
}

// cloneROX creates the shared rox-<hash> ReadOnlyMany PVC from the
// snapshot. Sized at max(writer request, writer PV capacity,
// snapshot.restoreSize) so the clone is never smaller than its
// dataSource (PD-CSI rejects that — GCP-H100-a 2026-06-15).
func (p *SnapshotClonePromoter) cloneROX(ctx context.Context, ns, hash, rwxName, roxName string) error {
	snapName := "snap-" + ShortHash(hash)
	sizeQty, err := p.cloneSize(ctx, ns, rwxName, snapName)
	if err != nil {
		return err
	}
	apiGroup := "snapshot.storage.k8s.io"
	roxPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roxName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/per-capture":        "true",
				"nvsnap.io/role":               "reader",
				"nvsnap.io/hash-short":         ShortHash(hash),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
			StorageClassName: &p.StorageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: sizeQty},
			},
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "VolumeSnapshot",
				Name:     snapName,
			},
		},
	}
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Create(ctx, roxPVC, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create rox PVC: %w", err)
		}
	}
	// Do NOT block on rox Bound — under WaitForFirstConsumer the rox stays
	// Pending until the first restore pod mounts it. Gating on Bound here
	// deadlocks (Hook A won't deploy the consumer until the
	// NvSnapFunctionState reaches Warm, which waits on
	// pvc_promote_state=ready, set after this returns).
	return nil
}

// cloneSize returns the rox PVC size: max(writer request, writer PV
// capacity, snapshot.restoreSize).
func (p *SnapshotClonePromoter) cloneSize(ctx context.Context, ns, rwxName, snapName string) (resource.Quantity, error) {
	rwx, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, rwxName, metav1.GetOptions{})
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("get rwx PVC %s: %w", rwxName, err)
	}
	sizeQty := rwx.Spec.Resources.Requests[corev1.ResourceStorage]
	if capQty, ok := rwx.Status.Capacity[corev1.ResourceStorage]; ok && capQty.Cmp(sizeQty) > 0 {
		sizeQty = capQty
	}
	if snapGot, gerr := p.DynClient.Resource(volumeSnapshotGVR).Namespace(ns).Get(ctx, snapName, metav1.GetOptions{}); gerr == nil {
		if rs, found, _ := unstructured.NestedString(snapGot.Object, "status", "restoreSize"); found && rs != "" {
			if rsq, perr := resource.ParseQuantity(rs); perr == nil && rsq.Cmp(sizeQty) > 0 {
				sizeQty = rsq
			}
		}
	}
	return sizeQty, nil
}

// MountSpec returns the pod-spec fragment to read the promoted artifact.
// Shared-ROX: the one rox-<hash> claim. Per-pod (RWO-only): a fresh
// rox-<hash>-<podUID> RWO clone from the snapshot, created on demand.
func (p *SnapshotClonePromoter) MountSpec(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	p.applyDefaults()
	ns := vol.Namespace
	claimName := "rox-" + ShortHash(hash)

	if !p.ReadOnlyMany {
		// Per-pod RWO clone. PodUID keys the per-pod claim so each
		// restore replica gets its own copy from the shared snapshot.
		var err error
		claimName, err = p.ensurePerPodClone(ctx, ns, hash, vol)
		if err != nil {
			return PodMount{}, err
		}
	} else {
		// Shared ROX: the claim must already exist (Promote created it).
		if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claimName, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return PodMount{}, ErrNotFound
			}
			return PodMount{}, err
		}
	}

	volName := "nvsnap-checkpoint"
	if vol.Name != "" {
		volName = vol.Name
	}
	return PodMount{
		Volume: corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
					ReadOnly:  true,
				},
			},
		},
		VolumeMount: corev1.VolumeMount{
			Name:      volName,
			MountPath: vol.MountPath,
			ReadOnly:  true,
		},
	}, nil
}

// ensurePerPodClone creates (idempotently) a per-pod RWO PVC cloned from
// the hash's snapshot. Used only on RWO-only storage (EBS). Each restore
// pod gets its own copy; the PodUID keys the claim name. Requires
// vol.PodUID-style identity — derived from vol.Name suffix the webhook
// stamps; falls back to the shared snapshot name if unset (single reader).
func (p *SnapshotClonePromoter) ensurePerPodClone(ctx context.Context, ns, hash string, vol VolumeMeta) (string, error) {
	snapName := "snap-" + ShortHash(hash)
	// The webhook stamps a per-pod-unique suffix on vol.Name; if absent,
	// fall back to the hash (degrades to one shared clone, RWO single-reader).
	suffix := vol.Name
	if suffix == "" {
		suffix = ShortHash(hash)
	}
	claimName := "rox-" + ShortHash(hash) + "-" + suffix
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claimName, metav1.GetOptions{}); err == nil {
		return claimName, nil // already cloned for this pod
	}
	sizeQty, err := p.cloneSize(ctx, ns, "", snapName)
	if err != nil {
		// rwxName is gone on the per-pod path; fall back to snapshot restoreSize only.
		sizeQty = resource.Quantity{}
		if snapGot, gerr := p.DynClient.Resource(volumeSnapshotGVR).Namespace(ns).Get(ctx, snapName, metav1.GetOptions{}); gerr == nil {
			if rs, found, _ := unstructured.NestedString(snapGot.Object, "status", "restoreSize"); found && rs != "" {
				if rsq, perr := resource.ParseQuantity(rs); perr == nil {
					sizeQty = rsq
				}
			}
		}
	}
	apiGroup := "snapshot.storage.k8s.io"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvsnap",
				"nvsnap.io/per-capture":        "true",
				"nvsnap.io/role":               "reader-perpod",
				"nvsnap.io/hash-short":         ShortHash(hash),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &p.StorageClass,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: sizeQty}},
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "VolumeSnapshot",
				Name:     snapName,
			},
		},
	}
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create per-pod rox clone %s: %w", claimName, err)
	}
	return claimName, nil
}

// Delete removes the rox PVC + the snapshot (its dataSource) + any
// leftover writer PVC for hash. Idempotent.
func (p *SnapshotClonePromoter) Delete(ctx context.Context, hash, ns string) error {
	roxName := "rox-" + ShortHash(hash)
	rwxName := "rwx-" + ShortHash(hash)
	snapName := "snap-" + ShortHash(hash)
	var errs []string
	if err := p.deletePVC(ctx, ns, roxName); err != nil {
		errs = append(errs, fmt.Sprintf("rox %s: %v", roxName, err))
	}
	if err := p.deletePVC(ctx, ns, rwxName); err != nil {
		errs = append(errs, fmt.Sprintf("rwx %s: %v", rwxName, err))
	}
	if err := p.deleteSnapshot(ctx, ns, snapName); err != nil {
		errs = append(errs, fmt.Sprintf("snap %s: %v", snapName, err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("snapshot-clone delete: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (p *SnapshotClonePromoter) deletePVC(ctx context.Context, ns, name string) error {
	err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (p *SnapshotClonePromoter) deleteSnapshot(ctx context.Context, ns, name string) error {
	err := p.DynClient.Resource(volumeSnapshotGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
