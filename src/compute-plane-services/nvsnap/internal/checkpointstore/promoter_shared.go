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

// SharedVolumePromoter — the zero-copy L2 strategy (nvsnap#171). For
// storage where the same backing volume can be ReadOnlyMany-attached to
// many pods (NVMesh, EFS, Filestore), there is no snapshot and no copy:
// the populated writer volume is retained and re-referenced read-only.
//
// Mirrors NVCA's model-cache NVMesh path (pkg/storage/modelcache.go,
// doModelCacheNVMesh):
//
//   Promote:
//     1. resolve the writer PVC's bound (primary) PV
//     2. set the primary PV's reclaim policy to Retain so deleting the
//        writer PVC never destroys the data
//     3. create a secondary PV = DeepCopy(primary) with AccessModes=ROX,
//        ClaimRef pre-bound to ro-<hash>, and CSI.VolumeHandle rewritten
//        by the configured VolumeHandleTransform (a shared RO attach of
//        the SAME underlying volume)
//     4. create the shared ro-<hash> ROX PVC (binds statically to the
//        secondary PV via the pre-set claimRef)
//     5. delete the writer PVC (primary PV → Released, data retained)
//
//   MountSpec: return the shared ro-<hash> ROX claim (N pods mount it).
//
// Net: one backing volume, RO-attached to N pods, zero bytes copied.

package checkpointstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// VolumeHandleTransform rewrites a primary PV's CSI volumeHandle into the
// handle a secondary (read-only) PV should use to attach the SAME backing
// volume for the given consumer namespace. Returns the input verbatim for
// shared filesystems (EFS/Filestore) that need no per-attach change.
type VolumeHandleTransform func(handle, namespace string) (string, error)

// volumeHandleTransforms maps a profile's volumeHandleTransform name to
// its implementation. New shared-volume backends register here.
var volumeHandleTransforms = map[string]VolumeHandleTransform{
	// none: reuse the handle verbatim — shared filesystems (EFS,
	// Filestore) RO-mount the same handle from many pods directly.
	"none": func(h, _ string) (string, error) { return h, nil },
	// nvmesh: NVMesh volume handles are colon-delimited with the
	// consumer namespace as the last segment; a read-only secondary
	// attach of the same volume rewrites that last segment. Ported from
	// NVCA updateSecondaryPVVolumeHandle (pkg/storage/modelcache.go).
	"nvmesh": func(h, ns string) (string, error) {
		i := strings.LastIndex(h, ":")
		if i == -1 {
			return "", fmt.Errorf("nvmesh volume handle %q has no colon segment", h)
		}
		return h[:i+1] + ns, nil
	},
}

// LookupVolumeHandleTransform returns the named transform, or an error if
// unknown. Empty name defaults to "none".
func LookupVolumeHandleTransform(name string) (VolumeHandleTransform, error) {
	if name == "" {
		name = "none"
	}
	t, ok := volumeHandleTransforms[name]
	if !ok {
		return nil, fmt.Errorf("unknown volumeHandleTransform %q", name)
	}
	return t, nil
}

// SharedVolumePromoter implements Promoter via static RO PVs over a
// retained primary volume (zero copy).
type SharedVolumePromoter struct {
	KubeClient kubernetes.Interface

	// StorageClass is the L2 SC (used to match the static ro PVC bind).
	StorageClass string
	// Transform rewrites the secondary PV's volumeHandle. nil ⇒ "none".
	Transform VolumeHandleTransform
	// MountOptions, if set, replace the minted RO PV's mount options.
	// NVMesh (xfs) needs "ro,norecovery,nouuid" so N pods can mount the
	// same backing filesystem read-only. Empty ⇒ inherit primary's.
	MountOptions []string

	Log logrus.FieldLogger
}

var _ Promoter = (*SharedVolumePromoter)(nil)

func (p *SharedVolumePromoter) applyDefaults() {
	if p.Transform == nil {
		p.Transform = volumeHandleTransforms["none"]
	}
	if p.Log == nil {
		p.Log = logrus.NewEntry(logrus.New()).WithField("subsys", "checkpointstore.shared_promoter")
	}
}

// Caps reports shared-volume capabilities (zero-copy ReadOnlyMany via a
// shared backing volume; no snapshots).
func (p *SharedVolumePromoter) Caps() StorageCaps {
	return StorageCaps{
		Snapshots:    false,
		ReadOnlyMany: true,
		SharedVolume: true,
		Strategy:     "shared-volume",
	}
}

func secondaryPVName(hash string) string { return "nvsnap-ro-pv-" + ShortHash(hash) }
func sharedROXName(hash string) string   { return "rox-" + ShortHash(hash) }

// Promote retains the writer's backing volume and exposes it as a shared
// ROX claim via a static secondary PV — no snapshot, no copy.
func (p *SharedVolumePromoter) Promote(ctx context.Context, in PromoteInput) (PromoteResult, error) {
	p.applyDefaults()
	ns := in.Namespace
	log := p.Log.WithFields(logrus.Fields{"hash": ShortHash(in.Hash), "namespace": ns})

	// 1. Resolve the writer PVC's bound primary PV.
	writer, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, in.WriterPVCName, metav1.GetOptions{})
	if err != nil {
		return PromoteResult{}, fmt.Errorf("get writer PVC %s: %w", in.WriterPVCName, err)
	}
	pvName := writer.Spec.VolumeName
	if pvName == "" {
		return PromoteResult{}, fmt.Errorf("writer PVC %s has no bound PV", in.WriterPVCName)
	}
	primary, err := p.KubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return PromoteResult{}, fmt.Errorf("get primary PV %s: %w", pvName, err)
	}
	if primary.Spec.CSI == nil || primary.Spec.CSI.VolumeHandle == "" {
		return PromoteResult{}, fmt.Errorf("primary PV %s has no CSI volumeHandle (shared-volume needs a CSI volume)", pvName)
	}

	// 2. Retain the primary PV so deleting the writer PVC keeps the data.
	if primary.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		primary.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		if _, err := p.KubeClient.CoreV1().PersistentVolumes().Update(ctx, primary, metav1.UpdateOptions{}); err != nil {
			return PromoteResult{}, fmt.Errorf("set primary PV %s reclaim=Retain: %w", pvName, err)
		}
	}

	// 3. Create the secondary RO PV (DeepCopy of primary, ROX, claimRef
	//    pre-bound to the shared ro PVC, transformed volumeHandle).
	roxName := sharedROXName(in.Hash)
	secName := secondaryPVName(in.Hash)
	if err := p.ensureSecondaryPV(ctx, primary, secName, roxName, ns); err != nil {
		return PromoteResult{}, err
	}

	// 4. Create the shared ro-<hash> ROX PVC (binds statically to the
	//    secondary PV via the pre-set claimRef + explicit volumeName).
	if err := p.ensureSharedROXPVC(ctx, ns, in.Hash, roxName, secName, primary); err != nil {
		return PromoteResult{}, err
	}

	// 5. Delete the writer PVC — the primary PV is Retain, so its backing
	//    volume (and the data) survive; the secondary PV references the
	//    same volume independently.
	if err := p.deletePVC(ctx, ns, in.WriterPVCName); err != nil {
		log.WithError(err).Warn("writer PVC delete failed; orphan-gc will reclaim")
	}

	log.WithField("rox", roxName).Info("L2 promote complete (shared-volume, zero copy)")
	return PromoteResult{SharedClaimName: roxName, ReusedWriterVolume: true}, nil
}

func (p *SharedVolumePromoter) ensureSecondaryPV(ctx context.Context, primary *corev1.PersistentVolume, secName, roxName, ns string) error {
	if _, err := p.KubeClient.CoreV1().PersistentVolumes().Get(ctx, secName, metav1.GetOptions{}); err == nil {
		return nil // idempotent
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get secondary PV %s: %w", secName, err)
	}
	newHandle, err := p.Transform(primary.Spec.CSI.VolumeHandle, ns)
	if err != nil {
		return fmt.Errorf("transform volumeHandle: %w", err)
	}
	sec := primary.DeepCopy()
	sec.ObjectMeta = metav1.ObjectMeta{
		Name: secName,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "nvsnap",
			"nvsnap.io/per-capture":        "true",
			"nvsnap.io/role":               "reader-shared",
		},
	}
	sec.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}
	sec.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	sec.Spec.CSI = primary.Spec.CSI.DeepCopy()
	sec.Spec.CSI.VolumeHandle = newHandle
	sec.Spec.CSI.ReadOnly = true
	// Read-only mount options for the shared fan-out. NVMesh xfs needs
	// "ro,norecovery,nouuid" — without nouuid the 2nd pod mounting the
	// same filesystem UUID fails ("Filesystem has duplicate UUID").
	if len(p.MountOptions) > 0 {
		sec.Spec.MountOptions = append([]string(nil), p.MountOptions...)
	}
	// Pre-bind to the shared ro PVC so it binds statically (no dynamic
	// provisioning) once that PVC is created.
	sec.Spec.ClaimRef = &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "PersistentVolumeClaim",
		Name:       roxName,
		Namespace:  ns,
	}
	sec.Status = corev1.PersistentVolumeStatus{}
	if _, err := p.KubeClient.CoreV1().PersistentVolumes().Create(ctx, sec, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create secondary PV %s: %w", secName, err)
	}
	return nil
}

func (p *SharedVolumePromoter) ensureSharedROXPVC(ctx context.Context, ns, hash, roxName, secName string, primary *corev1.PersistentVolume) error {
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, roxName, metav1.GetOptions{}); err == nil {
		return nil // idempotent
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get rox PVC %s: %w", roxName, err)
	}
	sizeQty := primary.Spec.Capacity[corev1.ResourceStorage]
	scName := p.StorageClass
	pvc := &corev1.PersistentVolumeClaim{
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
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
			// Static bind to the pre-claimed secondary PV. volumeName +
			// the PV's claimRef are bidirectional; storageClassName matches
			// the PV so the binder doesn't dynamically provision.
			VolumeName:       secName,
			StorageClassName: &scName,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: sizeQty}},
		},
	}
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create shared rox PVC %s: %w", roxName, err)
	}
	return nil
}

// MountSpec returns the shared ro-<hash> ROX claim. N restore pods mount
// it read-only; no per-pod artifact.
func (p *SharedVolumePromoter) MountSpec(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	p.applyDefaults()
	ns := vol.Namespace
	roxName := sharedROXName(hash)
	if _, err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Get(ctx, roxName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return PodMount{}, ErrNotFound
		}
		return PodMount{}, err
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
					ClaimName: roxName,
					ReadOnly:  true,
				},
			},
		},
		VolumeMount: corev1.VolumeMount{Name: volName, MountPath: vol.MountPath, ReadOnly: true},
	}, nil
}

// Delete removes the shared ro PVC + secondary PV + the retained primary
// PV (releasing the backing volume). Driven by the retention controller.
func (p *SharedVolumePromoter) Delete(ctx context.Context, hash, ns string) error {
	p.applyDefaults()
	roxName := sharedROXName(hash)
	secName := secondaryPVName(hash)
	var errs []string

	// ro PVC first (frees the secondary PV's claimRef).
	if err := p.deletePVC(ctx, ns, roxName); err != nil {
		errs = append(errs, fmt.Sprintf("rox %s: %v", roxName, err))
	}
	// Resolve the primary PV (the backing volume) BEFORE deleting the
	// secondary, so we can release it.
	var primaryPVName string
	if sec, err := p.KubeClient.CoreV1().PersistentVolumes().Get(ctx, secName, metav1.GetOptions{}); err == nil {
		if sec.Spec.CSI != nil {
			primaryPVName = p.primaryPVForHandle(ctx, sec.Spec.CSI.VolumeHandle)
		}
	}
	if err := p.deletePV(ctx, secName); err != nil {
		errs = append(errs, fmt.Sprintf("secondary PV %s: %v", secName, err))
	}
	// Delete the Retain'd primary PV last — this releases the backing
	// CSI volume (its reclaim is Retain, so it would otherwise orphan).
	if primaryPVName != "" {
		if err := p.deletePV(ctx, primaryPVName); err != nil {
			errs = append(errs, fmt.Sprintf("primary PV %s: %v", primaryPVName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("shared-volume delete: %s", strings.Join(errs, "; "))
	}
	return nil
}

// primaryPVForHandle finds a Retain'd primary PV whose (untransformed)
// volumeHandle the secondary's handle derives from. Best-effort: returns
// "" when not found (the primary may already be gone). We match by the
// pre-transform prefix (everything up to the last ':') so it works for
// both the nvmesh transform and the none (identity) case.
func (p *SharedVolumePromoter) primaryPVForHandle(ctx context.Context, secHandle string) string {
	prefix := secHandle
	if i := strings.LastIndex(secHandle, ":"); i != -1 {
		prefix = secHandle[:i+1]
	}
	pvs, err := p.KubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if pv.Name == "" || pv.Spec.CSI == nil {
			continue
		}
		if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
			continue
		}
		if strings.HasPrefix(pv.Spec.CSI.VolumeHandle, prefix) && pv.Labels["nvsnap.io/role"] != "reader-shared" {
			return pv.Name
		}
	}
	return ""
}

func (p *SharedVolumePromoter) deletePVC(ctx context.Context, ns, name string) error {
	err := p.KubeClient.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (p *SharedVolumePromoter) deletePV(ctx context.Context, name string) error {
	err := p.KubeClient.CoreV1().PersistentVolumes().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
