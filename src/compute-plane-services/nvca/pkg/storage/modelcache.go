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

package storage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage/cacheprobe"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ModelCacheInitNamespace is the kubernetes namespace containing all cross-namespace
	// model cache initialization objects.
	ModelCacheInitNamespace = "nvca-modelcache-init"
	// ModelCachePodVolumeName is the static name for the NVMesh PVC-backed model cache volume on all pod specs.
	ModelCachePodVolumeName         = "model-data"
	ModelCachePodModelMountPath     = "/config/models"
	ModelCachePodResourcesMountPath = "/config/resources"
)

type initCacheJobState int

const (
	initCacheJobInProgress initCacheJobState = iota + 1
	initCacheJobCompleted
	initCacheJobFailed
)

type pvcState int

const (
	pvcBound pvcState = iota + 1
	pvcUnbound
	pvcBindFailed
)

const (
	fqdnPrefix = "nvca.nvcf.nvidia.io"

	// This label is set on all primary PVs to differentiate them from secondaries,
	// which are tied to a specific instance.
	primaryPVLabelKey   = fqdnPrefix + "/modelcache-primary-pv"
	primaryPVLabelValue = "true"

	// cachePopulatedLabelKey marks a per-handle backing PVC as holding a fully
	// populated cache. It is the durable, restart-safe "cache populated" signal
	// for the backends that key reuse on a PVC rather than an NVMesh primary PV:
	// the Samba per-handle backing PVC (samba-<handle>) and the shared-FS retained
	// writer PVC. Consumers check it before deciding to run the single-writer
	// init, so a cold reconcile (e.g. after an agent restart, when the in-memory
	// init-status fan-out is empty) never re-runs the writer or hangs on the
	// lease. It is the PVC analogue of the primaryPVLabelKey marker.
	cachePopulatedLabelKey   = fqdnPrefix + "/modelcache-populated"
	cachePopulatedLabelValue = "true"

	// The annotation applied to primary PV's that denotes the last time
	// a function or task referenced it.
	// If now + modelCacheIdlePeriod > this value and no model cache storage requests
	// reference this PV, then it should be cleaned up.
	primaryPVLastReferencedAnnotationKey = fqdnPrefix + "/modelcache-last-referenced"
	// The time format of the value for primaryPVLastReferencedAnnotationKey.
	primaryPVLastReferencedTimeFormat = time.RFC3339

	// This label must be applied to all primary/init model cache objects prior to creation.
	// It is used to select existing objects and reconcile them.
	modelCacheHandleLabelKey = fqdnPrefix + "/modelcache-handle"
)

// terminalErrorWithMetric records a model cache failure metric and returns a terminal error.
// These cover early/validation failures where the backend is not necessarily
// known, so the backend label is left empty.
func (r *Reconciler) terminalErrorWithMetric(reason, msg string) error {
	r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, reason, "")
	return reconcile.TerminalError(errors.New(msg))
}

// terminalErrorWithMetricErr records a model cache failure metric and returns a terminal error wrapping the given error.
func (r *Reconciler) terminalErrorWithMetricErr(reason string, err error) error {
	r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, reason, "")
	return reconcile.TerminalError(err)
}

// mapPodIssuesToFailureReason maps pod issues to a failure reason for metrics.
// Returns the most specific reason based on priority order.
func mapPodIssuesToFailureReason(podIssues sets.Set[string]) string {
	// Priority order - return most specific reason
	if podIssues.Has("image pull issues") {
		return modelcachetypes.ReasonImagePull
	}
	if podIssues.Has("init stuck initializing") {
		return modelcachetypes.ReasonInitStuck
	}
	if podIssues.Has("timed out waiting to be scheduled") {
		return modelcachetypes.ReasonSchedulingTimeout
	}
	if podIssues.Has("admission rejected") {
		return modelcachetypes.ReasonAdmissionRejected
	}
	return modelcachetypes.ReasonInitJobFailed
}

// modelCacheTracer traces model cache reconciliation.
var modelCacheTracer = nvcaotel.NewTracer(nvcaotel.WithName(
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage/modelcache"))

// doModelCache wraps the model cache reconcile in an inbound span (nesting under
// the per-request parent span established from the ICMS trace context in
// Reconcile) and delegates to doModelCacheRouted.
func (r *Reconciler) doModelCache(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	var res reconcile.Result
	err := nvcaotel.InvokeWithSpan(ctx, modelCacheTracer, "nvca.modelcache.reconcile",
		func(ctx context.Context) error {
			var e error
			res, e = r.doModelCacheRouted(ctx, st, stCopy, icmsReq)
			return e
		},
		oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(icmsReq)...),
	)
	return res, err
}

func (r *Reconciler) doModelCacheRouted(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	// Select the populate path by the backend stamped on the request by the
	// miniservice reconciler (SelectHelmCacheBackend). Empty backend defaults to
	// NVMesh for backward compatibility.
	var res reconcile.Result
	var err error
	backend := ""
	if stCopy.Spec.ModelCache != nil {
		backend = stCopy.Spec.ModelCache.Backend
	}
	selectedBackend := backend
	if selectedBackend == "" {
		selectedBackend = string(HelmCacheBackendNVMesh)
	}
	// Tag the reconcile span with the selected backend for filtering.
	oteltrace.SpanFromContext(ctx).SetAttributes(otelattr.String("nvcf.modelcache.backend", selectedBackend))
	// Record the selected backend once, on the first reconcile of the request.
	if st.Status.Phase == nvcav1new.StorageUnknown {
		r.metrics.RecordModelCacheBackendSelected(selectedBackend)
	}
	switch backend {
	case string(HelmCacheBackendSharedFS):
		res, err = r.doModelCacheSharedFS(ctx, st, stCopy, icmsReq)
	case string(HelmCacheBackendSamba):
		res, err = r.doModelCacheSamba(ctx, st, stCopy, icmsReq)
	case "", string(HelmCacheBackendNVMesh):
		// Empty preserves requests created before the Backend field existed.
		res, err = r.doModelCacheNVMesh(ctx, st, stCopy, icmsReq)
	default:
		// A typo or a non-provisioning value (e.g. "ephemeral") must not fall
		// through to NVMesh provisioning against an unavailable backend.
		err = r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid,
			fmt.Sprintf("unsupported model cache backend %q", backend))
	}
	if isTerminal(err) {
		stCopy.Status.Phase = nvcav1new.StorageFailed
	}

	if stCopy.Status.Phase == nvcav1new.StorageFailed {
		if errs := r.cleanupInitModelCache(ctx, stCopy, false); len(errs) == 0 {
			meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
				Type:    ConditionTypeCleanupSuccessful,
				Status:  metav1.ConditionTrue,
				Reason:  ConditionReasonAllObjectsDeleted,
				Message: "All init and secondary model cache objects were cleaned up",
			})
		} else {
			meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
				Type:   ConditionTypeCleanupSuccessful,
				Status: metav1.ConditionFalse,
				Reason: ConditionReasonSomeObjectsPendingDeletion,
			})
		}
	}

	return res, err
}

// sharedFSProbeTTLSeconds is how long a cached CSI ROX/RWX probe result for
// nvcf-miniservice-sc is reused before re-probing.
const sharedFSProbeTTLSeconds = 3600

// resolveSharedFSStrategy returns the cached probe strategy for nvcf-miniservice-sc,
// re-probing (and persisting the result) when no valid cached strategy exists.
func (r *Reconciler) resolveSharedFSStrategy(ctx context.Context) (cacheprobe.AccessModeStrategy, error) {
	log := logf.FromContext(ctx)
	store := cacheprobe.NewStateStore(r.Client, ModelCacheInitNamespace)
	strategy, err := store.GetStrategy(ctx, HelmCacheSharedStorageClassName)
	if err != nil {
		return cacheprobe.StrategyFallback, err
	}
	if strategy != cacheprobe.StrategyFallback {
		return strategy, nil
	}
	// GetStrategy returns Fallback both when results are missing/expired and
	// when a fresh probe found the class unsupported. Honour the TTL of a fresh
	// negative result: if the class was recently probed and found unusable, do
	// not re-probe (which would create a PVC+pod) on every reconcile.
	fresh, err := store.HasFreshResult(ctx, HelmCacheSharedStorageClassName)
	if err != nil {
		return cacheprobe.StrategyFallback, err
	}
	if fresh {
		return cacheprobe.StrategyFallback, nil
	}
	// No valid cached result: probe ROX then RWX and persist.
	prober := cacheprobe.NewProber(r.Client, ModelCacheInitNamespace, HelmCacheSharedStorageClassName, sharedFSProbeTTLSeconds)
	strategy, results := prober.DetermineStrategy(ctx)
	if err := store.Save(ctx, results); err != nil {
		log.Error(err, "Failed to persist shared-FS probe results")
	}
	return strategy, nil
}

// doModelCacheSharedFS populates and exposes the model cache on a shared
// filesystem storage class (nvcf-miniservice-sc), used when NVMesh is not present. The
// cache is populated once (single-writer via the init lease/job, writing to the
// shared backend), and each workload namespace gets a read-only PVC on the same
// class that the existing webhook mounts via ModelCacheStatus.ROPVCName. The
// reader access mode (ROX preferred, RWX fallback) is chosen by a CSI probe.
//
// Unlike the NVMesh path it does not create primary/secondary PVs or rewrite
// CSI volume handles: cross-namespace sharing is a property of the shared class.
func (r *Reconciler) doModelCacheSharedFS(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	if stCopy.Spec.ModelCache == nil {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache field is not set")
	}
	cacheHandle := stCopy.Spec.ModelCache.CacheHandle
	if cacheHandle == "" {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache.cacheHandle field is not set")
	}
	if stCopy.Labels == nil {
		stCopy.Labels = map[string]string{}
	}
	if stCopy.Labels[modelCacheHandleLabelKey] == "" {
		stCopy.Labels[modelCacheHandleLabelKey] = cacheHandle
	}

	rwPVC, initJob, pullSecrets, err := r.findAndDecodeCacheArtifacts(icmsReq, st.Namespace)
	if err != nil {
		return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonCacheSpecInvalid, fmt.Errorf("find and decode artifacts: %w", err))
	}
	sharedSC := HelmCacheSharedStorageClassName
	rwPVC.Spec.StorageClassName = &sharedSC

	// Gate on the durable populated marker, mirroring the NVMesh/Samba
	// getPrimaryPV gate. Shared storage shares data across namespaces natively
	// so there is no primary PV, but consumers still need a restart-safe signal
	// that the cache is populated; the in-memory init-status fan-out is lost on
	// agent restart. The marker is a label on the retained writer RW PVC.
	writerPVC, populated, err := r.sharedFSCachePopulated(ctx, rwPVC.Name)
	if err != nil {
		return reconcile.Result{}, err
	}
	switch st.Status.Phase {
	case nvcav1new.StorageUnknown, nvcav1new.StoragePending, nvcav1new.StorageInitRunning:
		if !populated {
			// Not yet populated: populate the cache on the shared backend via
			// the single-writer init path. Populating the writer PVC does not
			// need the reader access-mode probe.
			return r.doInitModelCacheNVMesh(ctx, st, stCopy, rwPVC, initJob, pullSecrets, HelmCacheBackendSharedFS)
		}
		// Cache already populated (durable marker present): skip init and bind
		// this namespace's reader below.
		r.metrics.RecordModelCacheReuse(string(HelmCacheBackendSharedFS))
		stCopy.Status.Phase = nvcav1new.StorageCreating
	case nvcav1new.StorageFailed, nvcav1new.StorageRuntimeError:
		log.V(1).Error(fmt.Errorf("storage request is failed"), "Ignoring failed shared-FS model cache")
		return reconcile.Result{}, nil
	case nvcav1new.StorageCreating, nvcav1new.StorageReady:
		// Cache populated; ensure the per-namespace reader PVC below.
	}

	// Record last-referenced on the retained writer PVC so idle GC
	// (reclaimIdleSharedFSModelCaches) can reclaim it once no function
	// references the handle. Best effort.
	if writerPVC != nil {
		if err := r.touchCacheReferenced(ctx, writerPVC); err != nil {
			log.V(1).Error(err, "Failed to update shared-FS writer PVC last-referenced", "pvc", writerPVC.Name)
		}
	}

	// Reader RO PVC in the workload namespace on the shared class. With a
	// shared-capable class all consumers see the same published cache data.
	roPVCName := "ro-pvc-" + cacheHandle
	roPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: roPVCName, Namespace: stCopy.Namespace}, roPVC); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		// The reader PVC does not exist yet: probe the shared class once to
		// choose its access mode. The probe is intentionally scoped to reader
		// creation; once the reader exists we never re-probe, so a transient
		// probe failure after the TTL expires can never mark an already-healthy
		// cache as failed.
		strategy, err := r.resolveSharedFSStrategy(ctx)
		if err != nil {
			return reconcile.Result{}, err
		}
		if strategy == cacheprobe.StrategyFallback {
			return reconcile.Result{}, r.terminalErrorWithMetric("shared_fs_unusable",
				fmt.Sprintf("%s supports neither ROX nor RWX; shared caching disabled", HelmCacheSharedStorageClassName))
		}
		accessMode := corev1.ReadWriteMany
		if strategy == cacheprobe.StrategyROX {
			accessMode = corev1.ReadOnlyMany
		}
		roPVC = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        roPVCName,
				Namespace:   stCopy.Namespace,
				Labels:      types.GetLabelsForRequest(icmsReq, r.fff),
				Annotations: types.GetAnnotationsForRequest(icmsReq),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
				StorageClassName: &sharedSC,
				Resources:        rwPVC.Spec.Resources,
			},
		}
		maps.Copy(roPVC.Labels, getClusterWideResourceLabels(stCopy))
		if err := r.setControlledObjectMeta(ctx, stCopy, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Client.Create(ctx, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("Shared-FS reader RO PVC created", "pvc", roPVCName, "accessMode", accessMode)
	}

	switch r.getPVCState(roPVC) {
	case pvcBound:
		if stCopy.Status.Phase != nvcav1new.StorageReady || stCopy.Status.ModelCache == nil {
			log.Info("Shared-FS reader RO PVC bound, storage is ready")
			stCopy.Status.Phase = nvcav1new.StorageReady
			stCopy.Status.ModelCache = &nvcav1new.ModelCacheStatus{ROPVCName: roPVC.Name}
			r.metrics.RecordModelCacheResult(modelcachetypes.ResultSuccess, "", string(HelmCacheBackendSharedFS))
		}
	case pvcBindFailed:
		log.Error(fmt.Errorf("pvc bind failed"), "Shared-FS reader RO PVC failed", "pvc", roPVC.Name)
		stCopy.Status.Phase = nvcav1new.StorageFailed
		r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCBindFailed, string(HelmCacheBackendSharedFS))
	case pvcUnbound:
		// PVC events will requeue the storage request.
	}

	return reconcile.Result{}, nil
}

// doModelCacheSamba populates and exposes the model cache on an NVCA-managed
// per-cacheHandle Samba server (backend 3, "Samba over NVMesh"). Each handle gets
// its OWN Samba server + nvcf-sc data PVC (samba-<cacheHandle>, sized to the
// request's cacheSize) exporting one SMB share; there is no shared/global data
// PVC. The per-handle backing PVC is the durable reuse marker: if it already
// exists the cache is reused, and a cachePopulatedLabelKey label on it (stamped
// by the writer on success) signals readers may safely attach. A single-writer
// init job populates the share; on success the writer (job/RW SMB PV/PVC/lease)
// is torn down and the backing PVC is marked populated. Each workload namespace
// then binds its own RO SMB PV/PVC to the share. It creates no StorageClass
// (nvcf-miniservice-sc is the operator's branch-2 class) and needs no CSI probe (SMB is
// RWX/ROX). Idle per-handle servers/PVCs are reclaimed by cleanupIdleModelCaches.
func (r *Reconciler) doModelCacheSamba(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	if stCopy.Spec.ModelCache == nil {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache field is not set")
	}
	cacheHandle := stCopy.Spec.ModelCache.CacheHandle
	if cacheHandle == "" {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache.cacheHandle field is not set")
	}
	if stCopy.Labels == nil {
		stCopy.Labels = map[string]string{}
	}
	if stCopy.Labels[modelCacheHandleLabelKey] == "" {
		stCopy.Labels[modelCacheHandleLabelKey] = cacheHandle
	}

	rwPVC, initJob, pullSecrets, err := r.findAndDecodeCacheArtifacts(icmsReq, st.Namespace)
	if err != nil {
		return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonCacheSpecInvalid, fmt.Errorf("find and decode artifacts: %w", err))
	}
	// cacheSize comes from the request payload (the translator sizes the cache
	// PVC from CacheLaunchSpecification.CacheSize): the per-handle backing volume
	// is sized to it, not a global guess.
	capacity := rwPVC.Spec.Resources.Requests[corev1.ResourceStorage]

	// Ensure the per-handle Samba server + nvcf-sc backing PVC (samba-<handle>,
	// sized to cacheSize). Idempotent: an existing backing PVC is reused.
	smbResources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList(r.cfg.Agent.SharedStorage.Server.ContainerResources.Limits),
		Requests: corev1.ResourceList(r.cfg.Agent.SharedStorage.Server.ContainerResources.Requests),
		Claims:   r.cfg.Agent.SharedStorage.Server.ContainerResources.Claims,
	}
	var ready bool
	err = nvcaotel.InvokeWithSpan(ctx, modelCacheTracer, "nvca.modelcache.samba.ensure_infra",
		func(ctx context.Context) error {
			var e error
			ready, e = EnsureSambaModelCacheInfra(ctx, r.Client, cacheHandle, r.cfg.Agent.SharedStorage.Server.Image, smbResources, capacity)
			return e
		},
		oteltrace.WithAttributes(otelattr.String("nvcf.modelcache.handle", cacheHandle)),
	)
	if err != nil {
		// This runs on EVERY reconcile of a Samba request (including ones whose
		// cache is already Ready), so a transient API error (timeout, 429, 503)
		// must requeue, never terminally fail the request. Only genuinely
		// non-transient errors (misconfiguration, Forbidden, Invalid) are
		// terminal.
		if k8sutil.IsTransientK8sError(err) {
			log.V(1).Info("Transient error ensuring Samba model cache infra, requeuing", "error", err.Error())
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, r.terminalErrorWithMetricErr("samba_infra_failed", fmt.Errorf("ensure samba model cache infra: %w", err))
	}
	if !ready {
		log.V(1).Info("Samba model cache server not ready, requeuing")
		return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// The per-handle backing PVC is the durable reuse marker. Its existence means
	// the cache is being provisioned; its cachePopulatedLabelKey label (stamped by
	// the writer on success) means readers may attach. This is restart-safe and
	// cross-namespace: it does not rely on the init lease or in-memory fan-out.
	backingPVCName := SambaModelCacheBackingPVCName(cacheHandle)
	backingPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: backingPVCName}, backingPVC); err != nil {
		return reconcile.Result{}, err
	}
	populated := backingPVC.Labels[cachePopulatedLabelKey] == cachePopulatedLabelValue

	switch st.Status.Phase {
	case nvcav1new.StorageUnknown, nvcav1new.StoragePending, nvcav1new.StorageInitRunning:
		if !populated {
			// Writer RW SMB PV bound to the per-handle Samba share root. Each
			// handle has its own server/share, so no per-handle subdir (and no
			// subPath) is needed. It is plumbing, not data, and is created only
			// while population is required: cleanupInitModelCache deletes it
			// with the writer PVC after population (static PVs are never
			// reclaimed by the PV controller), and recreating it afterwards
			// would leave an orphaned Released static PV per handle.
			emptySC := ""
			rwPVName := sambaModelCacheWriterPVName(cacheHandle)
			rwPVC.Spec.StorageClassName = &emptySC
			rwPVC.Spec.VolumeName = rwPVName
			if err := ensureCreated(ctx, r.Client,
				newSambaModelCachePV(rwPVName, rwPVC.Name, ModelCacheInitNamespace, cacheHandle, false, capacity)); err != nil {
				return reconcile.Result{}, err
			}
			// Not yet populated: single-writer populate via the init lease/job.
			// On success reconcileInitModelCacheNVMesh marks the backing PVC
			// populated and tears down the writer (backend-aware).
			return r.doInitModelCacheNVMesh(ctx, st, stCopy, rwPVC, initJob, pullSecrets, HelmCacheBackendSamba)
		}
		// Cache populated: skip init and bind the per-namespace reader below.
		r.metrics.RecordModelCacheReuse(string(HelmCacheBackendSamba))
		stCopy.Status.Phase = nvcav1new.StorageCreating
	case nvcav1new.StorageFailed, nvcav1new.StorageRuntimeError:
		log.V(1).Error(fmt.Errorf("storage request is failed"), "Ignoring failed Samba model cache")
		return reconcile.Result{}, nil
	case nvcav1new.StorageCreating, nvcav1new.StorageReady:
		if !populated {
			return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonPVCSetupFailed,
				fmt.Errorf("samba backing PVC %s not marked populated after init", backingPVCName))
		}
		// Cache populated; ensure the per-namespace reader PV/PVC below.
	}

	// Record last-referenced on the backing PVC so idle GC can reclaim the
	// per-handle server/PVC once no function references the handle. Best effort.
	if err := r.touchCacheReferenced(ctx, backingPVC); err != nil {
		log.V(1).Error(err, "Failed to update Samba backing PVC last-referenced", "pvc", backingPVCName)
	}

	// Reader RO PV + PVC in the workload namespace, bound statically to the same
	// per-handle Samba share, mounted read-only.
	roPVCName := "ro-pvc-" + cacheHandle
	roPVName := "samba-ro-pv-" + stCopy.Namespace + "-" + cacheHandle
	roPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: roPVCName, Namespace: stCopy.Namespace}, roPVC); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		// Carry the cluster-wide resource labels so the per-request cleanup
		// (doCleanupModelCacheNVMesh lists PVs by these labels) deletes this
		// static PV when the StorageRequest goes away; the PV controller never
		// reclaims a Released static PV on its own.
		roPV := newSambaModelCachePV(roPVName, roPVCName, stCopy.Namespace, cacheHandle, true, capacity)
		maps.Copy(roPV.Labels, getClusterWideResourceLabels(stCopy))
		if err := ensureCreated(ctx, r.Client, roPV); err != nil {
			return reconcile.Result{}, err
		}
		roPVC = newSambaModelCachePVC(roPVCName, stCopy.Namespace, roPVName, true, capacity)
		roPVC.Labels = types.GetLabelsForRequest(icmsReq, r.fff)
		roPVC.Annotations = types.GetAnnotationsForRequest(icmsReq)
		maps.Copy(roPVC.Labels, getClusterWideResourceLabels(stCopy))
		if err := r.setControlledObjectMeta(ctx, stCopy, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Client.Create(ctx, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("Samba reader RO PVC created", "pvc", roPVCName)
	}

	switch r.getPVCState(roPVC) {
	case pvcBound:
		if stCopy.Status.Phase != nvcav1new.StorageReady || stCopy.Status.ModelCache == nil {
			log.Info("Samba reader RO PVC bound, storage is ready")
			stCopy.Status.Phase = nvcav1new.StorageReady
			stCopy.Status.ModelCache = &nvcav1new.ModelCacheStatus{ROPVCName: roPVC.Name}
			r.metrics.RecordModelCacheResult(modelcachetypes.ResultSuccess, "", string(HelmCacheBackendSamba))
		}
	case pvcBindFailed:
		log.Error(fmt.Errorf("pvc bind failed"), "Samba reader RO PVC failed", "pvc", roPVC.Name)
		stCopy.Status.Phase = nvcav1new.StorageFailed
		r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCBindFailed, string(HelmCacheBackendSamba))
	case pvcUnbound:
		// PVC events will requeue the storage request.
	}

	return reconcile.Result{}, nil
}

var accessModesRO = []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}

func (r *Reconciler) doModelCacheNVMesh(ctx context.Context, //nolint:gocyclo
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	log.V(1).Info("Model cache storage request reconciliation started", "phase", st.Status.Phase)

	if stCopy.Spec.ModelCache == nil {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache field is not set")
	}
	if stCopy.Spec.ModelCache.CacheHandle == "" {
		return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonCacheSpecInvalid, "modelCache.cacheHandle field is not set")
	}

	if stCopy.Labels == nil {
		stCopy.Labels = map[string]string{}
	}
	// TODO: remove once the controller migrates to selecting StorageRequests on CRD "spec.fields[*].selectableFields".
	if stCopy.Labels[modelCacheHandleLabelKey] == "" {
		stCopy.Labels[modelCacheHandleLabelKey] = stCopy.Spec.ModelCache.CacheHandle
	}

	rwPVC, initJob, workerPullSecrets, err := r.findAndDecodeCacheArtifacts(icmsReq, st.Namespace)
	if err != nil {
		return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonCacheSpecInvalid, fmt.Errorf("find and decode artifacts: %w", err))
	}

	if enc := stCopy.Spec.ModelCache.Encryption; enc != nil {
		scName, err := r.doEncryptedStorageClassNVMesh(ctx, stCopy, icmsReq.Spec.CreationMsgInfo.NCAID)
		if err != nil {
			return reconcile.Result{}, err
		}
		rwPVC.Spec.StorageClassName = &scName
	}

	// The presence or absence of the primary PV will depend on which stage model caching is in.
	primaryPV, ppvErr := r.getPrimaryPV(ctx, stCopy)
	switch st.Status.Phase {
	case nvcav1new.StorageUnknown, nvcav1new.StoragePending, nvcav1new.StorageInitRunning:
		if apierrors.IsNotFound(ppvErr) {
			return r.doInitModelCacheNVMesh(ctx, st, stCopy, rwPVC, initJob, workerPullSecrets, HelmCacheBackendNVMesh)
		} else if ppvErr != nil {
			return reconcile.Result{}, ppvErr
		}
		// Fallthrough and finalize secondary storage objects since the primary PV exists.
		r.metrics.RecordModelCacheReuse(string(HelmCacheBackendNVMesh))
		stCopy.Status.Phase = nvcav1new.StorageCreating
	case nvcav1new.StorageCreating, nvcav1new.StorageReady:
		if ppvErr != nil {
			// If the primary PV data is not found at this point, something went wrong during initialization
			// or state is outside of the storage controller's control.
			if apierrors.IsNotFound(ppvErr) {
				return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("primary PV not found after init: %w", ppvErr))
			}
			return reconcile.Result{}, ppvErr
		}
		// Fallthrough and finalize secondary storage objects since there were no issues retrieving.
	case nvcav1new.StorageFailed, nvcav1new.StorageRuntimeError:
		log.V(1).Error(fmt.Errorf("storage request is failed"), "Ignoring failed storage request")
		return r.doCleanupModelCacheNVMesh(ctx, stCopy)
	}

	switch primaryPV.Status.Phase {
	case corev1.VolumeFailed:
		log.Info("Primary PV is failed", "pv", primaryPV.Name)
		stCopy.Status.Phase = nvcav1new.StorageFailed
		r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCSetupFailed, string(HelmCacheBackendNVMesh))
		return reconcile.Result{}, nil
	case corev1.VolumePending:
		log.V(1).Info("Primary PV is pending", "pv", primaryPV.Name)
		// Recheck pending volumes after a minute.
		// Phase changes will trigger a reconcile earlier.
		return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
	default:
		log.V(1).Info("Primary PV is ready to be bound", "pv", primaryPV.Name, "phase", primaryPV.Status.Phase)
	}

	// Update the primary PV's last-referenced annotation to show at least an attempt
	// was made to use it.
	if primaryPV.Annotations == nil {
		primaryPV.Annotations = map[string]string{}
	}
	primaryPV.Annotations[primaryPVLastReferencedAnnotationKey] =
		r.nowFunc().Format(primaryPVLastReferencedTimeFormat)

	if err := r.Client.Update(ctx, primaryPV); err != nil {
		log.Error(err, "Failed to update primary PV annotation, may retry")
		return reconcile.Result{}, err
	}

	// PV found, create PV/PVC for this namespace.
	if primaryPV.Spec.CSI == nil {
		return reconcile.Result{}, r.terminalErrorWithMetric("pvc_setup_failed",
			fmt.Sprintf("primary PV %s has no csi data", primaryPV.Name))
	}
	volHandle := primaryPV.Spec.CSI.VolumeHandle
	if volHandle == "" {
		return reconcile.Result{}, r.terminalErrorWithMetric("pvc_setup_failed",
			fmt.Sprintf("primary PV %s has no volumeHandle", primaryPV.Name))
	}

	// The name must be unique and related to the storage request that owns it.
	secondaryPVName := "secondary-pv-" + stCopy.Spec.ICMSRequestName
	roPVCName := "ro-pvc-" + stCopy.Spec.ModelCache.CacheHandle

	// Create the PV first, which will be locked to the PVC by claim ref.
	secondaryPV := &corev1.PersistentVolume{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: secondaryPVName}, secondaryPV); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		secondaryPV = primaryPV.DeepCopy()
		secondaryPV.ObjectMeta = metav1.ObjectMeta{
			Name:        secondaryPVName,
			Labels:      types.GetLabelsForRequest(icmsReq, r.fff),
			Annotations: types.GetAnnotationsForRequest(icmsReq),
		}
		maps.Copy(secondaryPV.Labels, getClusterWideResourceLabels(stCopy))
		secondaryPV.Spec.AccessModes = accessModesRO
		secondaryPV.Spec.MountOptions = r.csiVolumeMountOptions
		secondaryPV.Spec.ClaimRef = &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
			Name:       roPVCName,
			Namespace:  stCopy.Namespace,
		}
		secondaryPV.Spec.CSI.VolumeHandle, err = updateSecondaryPVVolumeHandle(secondaryPV.Spec.CSI.VolumeHandle, st.Namespace)
		if err != nil {
			return reconcile.Result{}, r.terminalErrorWithMetricErr("pvc_setup_failed",
				fmt.Errorf("update secondary PV volume handle: %w", err))
		}
		secondaryPV.Status = corev1.PersistentVolumeStatus{}

		if err := r.setControlledObjectMeta(ctx, stCopy, secondaryPV); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Client.Create(ctx, secondaryPV); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("Secondary PV created", "pv", secondaryPV.Name)
	} else {
		log.V(1).Info("Secondary PV already exists, checking status", "pv", secondaryPV.Name)
	}
	// Next the RO PVC.
	roPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: roPVCName, Namespace: stCopy.Namespace}, roPVC); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		roPVC = rwPVC.DeepCopy()
		roPVC.ObjectMeta = metav1.ObjectMeta{
			Name:        roPVCName,
			Namespace:   stCopy.Namespace,
			Labels:      types.GetLabelsForRequest(icmsReq, r.fff),
			Annotations: types.GetAnnotationsForRequest(icmsReq),
		}
		maps.Copy(roPVC.Labels, getClusterWideResourceLabels(stCopy))
		roPVC.Spec.AccessModes = accessModesRO
		// Set VolumeName to specify this particular PV to bind.
		roPVC.Spec.VolumeName = secondaryPVName
		roPVC.Status = corev1.PersistentVolumeClaimStatus{}

		if err := r.setControlledObjectMeta(ctx, stCopy, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Client.Create(ctx, roPVC); err != nil {
			return reconcile.Result{}, err
		}
		log.Info("RO PVC created", "pvc", roPVC.Name)
	} else {
		log.V(1).Info("RO PVC already exists, checking status", "pvc", roPVC.Name)
	}

	pvcState := r.getPVCState(roPVC)
	switch pvcState {
	case pvcBound:
		if stCopy.Status.Phase != nvcav1new.StorageReady || stCopy.Status.ModelCache == nil {
			log.Info("RO PVC is bound, storage is ready")
			stCopy.Status.Phase = nvcav1new.StorageReady
			stCopy.Status.ModelCache = &nvcav1new.ModelCacheStatus{
				ROPVCName:    roPVC.Name,
				VolumeHandle: secondaryPV.Spec.CSI.VolumeHandle,
			}
			r.metrics.RecordModelCacheResult(modelcachetypes.ResultSuccess, "", string(HelmCacheBackendNVMesh))
		}
	case pvcBindFailed:
		log.Error(fmt.Errorf("pvc bind failed"), "RO PVC failed", "pvc", roPVC.Name)
		stCopy.Status.Phase = nvcav1new.StorageFailed
		r.metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCBindFailed, string(HelmCacheBackendNVMesh))
	case pvcUnbound:
		// PVC events will requeue the storage request.
		//
		// TODO: handle timeout and error on/log bad events.
	}

	return reconcile.Result{}, nil
}

func (r *Reconciler) findAndDecodeCacheArtifacts(
	icmsReq *nvcav2beta1.ICMSRequest,
	namespace string,
) (
	pvc *corev1.PersistentVolumeClaim,
	job *batchv1.Job,
	pullSecrets []*corev1.Secret,
	err error,
) {
	objs, err := r.translateWorkload(namespace, icmsReq)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("translate request: %v", err)
	}
	job, pvc, pullSecrets = findModelCacheObjects(objs)
	if pvc == nil {
		return nil, nil, nil, fmt.Errorf("cache PVC not found")
	}
	if job == nil {
		return nil, nil, nil, fmt.Errorf("cache init Job not found")
	}
	if len(pullSecrets) == 0 {
		return nil, nil, nil, fmt.Errorf("no worker pull Secrets found")
	}

	return pvc, job, pullSecrets, nil
}

var noMutF = func() error { return nil }

// doInitModelCacheNVMesh assumes the primary PV has not been created yet
// and checks to see if another thread is in the process of creating it via lease.
// If this thread is the lease holder or the lease does not exist,
// it updates/creates the lease then proceeds/starts with the cache init process,
// respectively.
// Else, it requeues so the caller can check for the primary PV
// created by another thread.
func (r *Reconciler) doInitModelCacheNVMesh(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	pullSecrets []*corev1.Secret,
	backend HelmCacheBackend,
) (res reconcile.Result, err error) {
	logf.IntoContext(ctx, logf.FromContext(ctx, "namespace", ModelCacheInitNamespace))

	// Use a lease to lock initialization.
	lres, holdsLease, err := r.handleLease(ctx, newInitLease(stCopy))
	if err != nil {
		return reconcile.Result{}, err
	}

	cacheHandle := st.Spec.ModelCache.CacheHandle
	statusCmpOpts := []cmp.Option{cmpopts.EquateEmpty(), cmpopts.EquateApproxTime(100 * time.Millisecond)}
	// Non-lease holders should respect the status of the lease holder in this phase.
	// Relying on job/PVC statuses while not holding the lease may result in a race condition.
	if !holdsLease {
		r.initStatuses.RLock()
		defer r.initStatuses.RUnlock()
		// Only update if the phase has changed.
		if status, ok := r.initStatuses.get(cacheHandle); ok &&
			!cmp.Equal(status, stCopy.Status, statusCmpOpts...) {
			if status.Phase != stCopy.Status.Phase {
				status.LastPhaseTransitionTime = &metav1.Time{Time: r.nowFunc()}
			}
			stCopy.Status = status
		}
		return lres, nil
	}

	// Prevent other workers from updating their status while the holder does.
	r.initStatuses.Lock()
	defer r.initStatuses.Unlock()

	res, err = r.reconcileInitModelCacheNVMesh(ctx, st, stCopy, rwPVC, initJob, pullSecrets, backend)

	// The lease holder updates the status for all non-holders (fan-out).
	if existingStatus, ok := r.initStatuses.get(cacheHandle); !ok ||
		!cmp.Equal(existingStatus, stCopy.Status, statusCmpOpts...) {
		if existingStatus.Phase != stCopy.Status.Phase {
			stCopy.Status.LastPhaseTransitionTime = &metav1.Time{Time: r.nowFunc()}
		}
		r.initStatuses.put(cacheHandle, stCopy.Status)
	}

	// Consolidate result.
	res.Requeue = res.Requeue || lres.Requeue //nolint:staticcheck
	if lres.RequeueAfter > res.RequeueAfter {
		res.RequeueAfter = lres.RequeueAfter
	}
	return res, err
}

func (r *Reconciler) reconcileInitModelCacheNVMesh(ctx context.Context,
	st nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	pullSecrets []*corev1.Secret,
	backend HelmCacheBackend,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	switch st.Status.Phase {
	case nvcav1new.StorageUnknown:
		log.V(1).Info("Creating objects for pending model cache")

		objsToCreate := []client.Object{rwPVC}
		initJob.Spec.Template.Spec.ImagePullSecrets = make([]corev1.LocalObjectReference, len(pullSecrets))
		for i, pullSecret := range pullSecrets {
			// Rename the secret so it is consistent across all storage requests for the volume handle.
			pullSecret.Name = fmt.Sprintf("%s-%d-pull-worker", initJob.Name, i)
			objsToCreate = append(objsToCreate, pullSecret)
			initJob.Spec.Template.Spec.ImagePullSecrets[i].Name = pullSecret.Name
		}
		// Add labels to Job pods for scheduling/observability.
		if initJob.Spec.Template.Labels == nil {
			initJob.Spec.Template.Labels = map[string]string{}
		}
		initJob.Spec.Template.Labels[modelCacheHandleLabelKey] = stCopy.Spec.ModelCache.CacheHandle
		// NVMesh client readiness scheduling applies only to the NVMesh backend;
		// the shared-FS backend (nvcf-miniservice-sc) has no NVMesh node requirement.
		if backend == HelmCacheBackendNVMesh {
			SetNVMeshClientStatusSchedulingRequirement(&initJob.Spec.Template.Spec)
		}
		objsToCreate = append(objsToCreate, initJob)
		for _, obj := range objsToCreate {
			obj.SetNamespace(ModelCacheInitNamespace)
			labels := obj.GetLabels()
			if labels == nil {
				labels = map[string]string{}
				obj.SetLabels(labels)
			}
			// Set the model cache handle label for association and deletion.
			labels[modelCacheHandleLabelKey] = stCopy.Spec.ModelCache.CacheHandle

			if err := r.Client.Create(ctx, obj); err != nil {
				if apierrors.IsAlreadyExists(err) {
					log.V(1).Error(err, "Init cache object already exists, either prior create failed "+
						"or model cache not cleaned up on previous run")
					continue
				}
				log.Error(err, "Failed to create cache init object")
				return reconcile.Result{}, err
			}
		}
		stCopy.Status.Phase = nvcav1new.StoragePending
		return reconcile.Result{Requeue: true}, nil
	case nvcav1new.StoragePending:
		log.V(1).Info("Handling pending model cache objects")

		return r.handlePendingModelCache(ctx, stCopy, initJob)
	case nvcav1new.StorageInitRunning:
		log.V(1).Info("Handling initializing model cache objects")

		jobKey := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: initJob.Name}
		if err := r.Client.Get(ctx, jobKey, initJob); err != nil {
			if apierrors.IsNotFound(err) {
				return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonJobNotFound, fmt.Errorf("init job not found: %w", err))
			}
			return reconcile.Result{}, err
		}
		switch r.getInitCacheJobState(ctx, initJob) {
		case initCacheJobCompleted:
			// check the RWPVC state next
			log.V(1).Info("Cache init job completed")
		case initCacheJobFailed:
			// The caller's cleanup method will delete cache resources.
			reason := r.getInitCacheJobFailureReason(initJob)
			return reconcile.Result{}, r.terminalErrorWithMetric(reason, "init job failed")
		case initCacheJobInProgress:
			// Job events will requeue the storage request.
			return reconcile.Result{}, nil
		}

		rwPVCKey := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: rwPVC.Name}
		if err := r.Client.Get(ctx, rwPVCKey, rwPVC); err != nil {
			if apierrors.IsNotFound(err) {
				return reconcile.Result{}, r.terminalErrorWithMetricErr(modelcachetypes.ReasonRWPVCBindFailed, fmt.Errorf("rw pvc not found: %w", err))
			}
			return reconcile.Result{}, err
		}
		switch r.getPVCState(rwPVC) {
		case pvcBound:
			switch backend {
			case HelmCacheBackendNVMesh:
				// NVMesh: retain the bound dynamic PV as the cross-namespace
				// "primary" PV (it carries the data volume handle that secondary
				// PVs copy) and tear down the writer job/RW PVC.
				log.Info("Cache init RW PVC bound, finalizing primary PV")
				if err := r.finalizePrimaryPVOnSuccessfulInit(ctx, stCopy, rwPVC); err != nil {
					log.Error(err, "Failed to finalize primary PV")
					return reconcile.Result{}, err
				}
				_ = r.cleanupInitModelCache(ctx, stCopy, false)
			case HelmCacheBackendSamba:
				// Samba: the cache data lives in the per-handle backing PVC
				// (samba-<handle>), populated via the writer's SMB mount. Stamp
				// the populated marker on that durable PVC and tear down the
				// ephemeral writer (job/RW SMB PV/PVC/lease). The backing PVC and
				// its label are the cross-namespace, restart-safe reuse marker.
				if err := r.markSambaCachePopulated(ctx, stCopy.Spec.ModelCache.CacheHandle); err != nil {
					log.Error(err, "Failed to mark Samba cache populated")
					return reconcile.Result{}, err
				}
				_ = r.cleanupInitModelCache(ctx, stCopy, false)
			default:
				// Shared-FS: the cache data lives on the shared backend
				// (nvcf-miniservice-sc). Keep the writer RW PVC/job as the persistent
				// writer-side claim; readers mount the same backend RO. There is
				// no primary PV, but stamp a durable "populated" marker on the
				// retained RW PVC so any namespace, including after an agent
				// restart (when the in-memory init-status fan-out is empty), can
				// detect the cache is populated and skip init.
				log.Info("Cache init RW PVC bound (shared-FS), marking cache populated")
				if err := r.markCachePopulated(ctx, rwPVC); err != nil {
					log.Error(err, "Failed to mark shared-FS cache populated")
					return reconcile.Result{}, err
				}
				// Tear down the init job and lease but retain the writer PVC:
				// without this, one completed Job and one Lease would accumulate
				// per cache handle with no other cleanup path (idle GC only
				// reclaims writer PVCs).
				_ = r.cleanupInitModelCache(ctx, stCopy, true)
			}

			// The single-writer download completed: count one populate for the backend.
			r.metrics.RecordModelCachePopulate(string(backend))
			stCopy.Status.Phase = nvcav1new.StorageCreating
			return reconcile.Result{Requeue: true}, nil
		case pvcBindFailed:
			// The caller's cleanup method will delete cache resources.
			return reconcile.Result{}, r.terminalErrorWithMetric(modelcachetypes.ReasonRWPVCBindFailed, "rw pvc bind failed")
		case pvcUnbound:
			// PVC events will requeue the storage request.
			return reconcile.Result{}, nil
		}
	case nvcav1new.StorageCreating, nvcav1new.StorageReady,
		nvcav1new.StorageFailed, nvcav1new.StorageRuntimeError:
	default:
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("unknown phase: %s", st.Status.Phase))
	}

	return reconcile.Result{}, nil
}

// handleLease will attempt to create lease for a cache handle.
// If the lease already exists and is held by this storage request, it will renew the lease.
// Else if it is expired, it will attempt to acquire it, and return true if so.
// handleLease will requeue only if is not the lease holder and cannot acquire the lease;
// otherwise other object updates will trigger storage request requeues.
func (r *Reconciler) handleLease(ctx context.Context,
	lease *coordv1.Lease,
) (res reconcile.Result, holdsLease bool, err error) {
	log := logf.FromContext(ctx).WithValues("lease", lease.Name)

	now := r.nowFunc()
	currLease := &coordv1.Lease{}
	leaseKey := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: lease.Name}
	switch err := r.Client.Get(ctx, leaseKey, currLease); {
	case err == nil:
		// The lease was already created by another thread, proceed with handler.
	case apierrors.IsNotFound(err):
		// The lease can be acquired.
		log.Info("Creating lease, starting model cache initialization")
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now}
		lease.Spec.AcquireTime = &metav1.MicroTime{Time: now}
		if err := r.Client.Create(ctx, lease); err != nil {
			log.Error(err, "Failed to create lease")
			return reconcile.Result{}, false, err
		}
		return reconcile.Result{}, true, nil
	default:
		return reconcile.Result{}, false, err
	}

	if currLease.Spec.HolderIdentity == nil {
		currLease.Spec.HolderIdentity = lease.Spec.HolderIdentity
	}
	if currLease.Spec.LeaseDurationSeconds == nil {
		currLease.Spec.LeaseDurationSeconds = lease.Spec.LeaseDurationSeconds
	}
	if currLease.Spec.AcquireTime == nil {
		currLease.Spec.AcquireTime = &metav1.MicroTime{}
	}

	if *currLease.Spec.HolderIdentity != *lease.Spec.HolderIdentity {
		// The least may exist but after a controller restart and/or ICMS request cleanup,
		// the owner might not exist anymore to proceed with caching.
		// Instead of waiting for timeout, acquire the lease and continue.
		icmsReq := &nvcav2beta1.ICMSRequest{}
		srerr := r.Client.Get(ctx, client.ObjectKey{
			Namespace: r.ICMSRequestNamespace,
			Name:      *currLease.Spec.HolderIdentity,
		}, icmsReq)

		// Some other thread is initializing the cache.
		// Check if lease is expired.
		leaseDur := time.Duration(*currLease.Spec.LeaseDurationSeconds) * time.Second
		if apierrors.IsNotFound(srerr) ||
			(currLease.Spec.RenewTime == nil && currLease.Spec.AcquireTime.Add(leaseDur).Before(now)) ||
			currLease.Spec.RenewTime.Add(leaseDur).Before(now) {
			log.V(1).Info("Lease has expired or holder is gone, attempting to acquire",
				"oldHolder", *currLease.Spec.HolderIdentity,
				"newHolder", *lease.Spec.HolderIdentity,
			)
			// The lease has expired, and the caller has checked for PV existence.
			// Attempt to acquire the lease then proceed with init.
			// Conflict apierrors mean that the lease was acquired by another storage request, so requeue.
			oldHolderID := *currLease.Spec.HolderIdentity
			currLease.Spec.HolderIdentity = lease.Spec.HolderIdentity
			currLease.Spec.RenewTime = &metav1.MicroTime{Time: now}
			currLease.Spec.AcquireTime = &metav1.MicroTime{Time: now}
			if err := r.Client.Update(ctx, currLease); err != nil {
				return reconcile.Result{}, false, err
			}
			// No conflict, this storage request has acquired the lease.
			log.Info("Acquired lease from old holder",
				"oldHolder", oldHolderID,
				"newHolder", *lease.Spec.HolderIdentity,
			)
			holdsLease = true
		} else {
			// Requeue and backoff until other storage request finishes or fails.
			log.V(1).Info("Requeuing while waiting for lease holder to finish cache init",
				"holder", *currLease.Spec.HolderIdentity,
			)
			// Lease or other primary object storage events will requeue the storage request.
			// To avoid the situation where the storage request owning model cache init fails,
			// requeue after expiration would occur.
			var requeueAfter time.Duration
			if currLease.Spec.RenewTime != nil {
				requeueAfter = currLease.Spec.RenewTime.Add(leaseDur).Sub(now)
			} else {
				requeueAfter = currLease.Spec.AcquireTime.Add(leaseDur).Sub(now)
			}
			res.RequeueAfter = requeueAfter
		}
	} else {
		holdsLease = true
		// If the lease holder is this storage request and less than half the lease duration is left
		// to expiry then renew the lease and proceed with initialization.
		leaseDurHalf := time.Duration(*currLease.Spec.LeaseDurationSeconds) * time.Second / 2
		var shouldRenew bool
		if currLease.Spec.RenewTime != nil {
			shouldRenew = currLease.Spec.RenewTime.Add(leaseDurHalf).Before(now)
		} else {
			shouldRenew = currLease.Spec.AcquireTime.Add(leaseDurHalf).Before(now)
		}
		if shouldRenew {
			log.V(1).Info("Renewing lease", "holder", *currLease.Spec.HolderIdentity)
			currLease.Spec.RenewTime = &metav1.MicroTime{Time: now}
			if err := r.Client.Update(ctx, currLease); err != nil {
				return reconcile.Result{}, false, err
			}
		}
		res.RequeueAfter = leaseDurHalf
	}

	return res, holdsLease, nil
}

func (r *Reconciler) getPrimaryPV(ctx context.Context, st *nvcav1new.StorageRequest) (*corev1.PersistentVolume, error) {
	log := logf.FromContext(ctx)
	// No primary PV will be found for a cache handle unless finalizePrimaryPVOnSuccessfulInit
	// has been invoked by some storage request's reconciliation.
	ppvLabels := map[string]string{
		primaryPVLabelKey:        primaryPVLabelValue,
		modelCacheHandleLabelKey: st.Spec.ModelCache.CacheHandle,
	}
	pvs := &corev1.PersistentVolumeList{}
	if err := r.Client.List(ctx, pvs, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(ppvLabels),
	}); err != nil {
		return nil, err
	}
	switch l := len(pvs.Items); l {
	case 0:
		return nil, apierrors.NewNotFound(corev1.Resource("persistentvolumes"), "primary-pv")
	case 1:
	default:
		log.Error(nil, "Unexpected number of pimary PVs for function", "want", 1, "got", l)
		return nil, fmt.Errorf("invariant invalidated: expected 1 primary PV for function, got %d", l)
	}

	return &pvs.Items[0], nil
}

func (r *Reconciler) finalizePrimaryPVOnSuccessfulInit(ctx context.Context,
	stCopy *nvcav1new.StorageRequest,
	rwPVC *corev1.PersistentVolumeClaim,
) error {
	primaryPVName := rwPVC.Spec.VolumeName
	if primaryPVName == "" {
		return reconcile.TerminalError(fmt.Errorf("bound PV name not set"))
	}

	primaryPV := &corev1.PersistentVolume{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: primaryPVName}, primaryPV); err != nil {
		return err
	}

	primaryPVOld := primaryPV.DeepCopy()
	if primaryPV.Labels == nil {
		primaryPV.Labels = map[string]string{}
	}
	if primaryPV.Annotations == nil {
		primaryPV.Annotations = map[string]string{}
	}
	primaryPV.Labels[primaryPVLabelKey] = primaryPVLabelValue
	primaryPV.Labels[modelCacheHandleLabelKey] = stCopy.Spec.ModelCache.CacheHandle
	timeStr := r.nowFunc().Format(primaryPVLastReferencedTimeFormat)
	primaryPV.Annotations[primaryPVLastReferencedAnnotationKey] = timeStr
	// Ensure PV data is retained for reuse by secondary PV's.
	primaryPV.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	if err := r.Client.Patch(ctx, primaryPV, client.MergeFrom(primaryPVOld)); err != nil {
		return fmt.Errorf("patch primary PV on successful init: %v", err)
	}

	return nil
}

// markCachePopulated stamps the durable populated marker (cachePopulatedLabelKey)
// on a per-handle backing PVC (the Samba samba-<handle> PVC or the shared-FS
// retained writer PVC). Patching the label is also a PVC update event, which
// fans out to every StorageRequest with the same cache-handle label, waking
// namespaces waiting on the populate.
func (r *Reconciler) markCachePopulated(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	old := pvc.DeepCopy()
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	pvc.Labels[cachePopulatedLabelKey] = cachePopulatedLabelValue
	if err := r.Client.Patch(ctx, pvc, client.MergeFrom(old)); err != nil {
		return fmt.Errorf("mark cache populated: %w", err)
	}
	return nil
}

// markSambaCachePopulated stamps the populated marker on the per-handle Samba
// backing PVC (samba-<cacheHandle>) once the writer has finished populating the
// share over SMB.
func (r *Reconciler) markSambaCachePopulated(ctx context.Context, cacheHandle string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: SambaModelCacheBackingPVCName(cacheHandle)}
	if err := r.Client.Get(ctx, key, pvc); err != nil {
		return fmt.Errorf("get samba backing pvc: %w", err)
	}
	return r.markCachePopulated(ctx, pvc)
}

// touchCacheReferenced records the last-referenced time on a per-handle backing
// PVC so cleanupIdleModelCaches can reclaim it (and, for Samba, the per-handle
// server) once no function has referenced the handle for the idle period.
func (r *Reconciler) touchCacheReferenced(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	// Patching the watched PVC requeues every request sharing its handle, and
	// each of those reconciles would patch it again. Skip the patch while the
	// existing stamp is fresh (a quarter of the idle period keeps idle GC
	// accurate enough) so steady state does not fan out into a requeue loop.
	now := r.nowFunc()
	if prev, err := time.Parse(primaryPVLastReferencedTimeFormat,
		pvc.Annotations[primaryPVLastReferencedAnnotationKey]); err == nil &&
		now.Sub(prev) < r.k8sTimeConfig.ModelCacheIdlePeriod/4 {
		return nil
	}
	old := pvc.DeepCopy()
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	pvc.Annotations[primaryPVLastReferencedAnnotationKey] = now.Format(primaryPVLastReferencedTimeFormat)
	if err := r.Client.Patch(ctx, pvc, client.MergeFrom(old)); err != nil {
		return fmt.Errorf("touch cache referenced: %w", err)
	}
	return nil
}

// sharedFSCachePopulated reports whether the retained shared-FS writer RW PVC
// (rwPVCName, in the model-cache init namespace) carries the durable populated
// marker, returning the PVC so callers can record last-referenced on it. A
// missing PVC means the cache has not been populated yet and is not an error.
// It is the shared-FS analogue of getPrimaryPV.
func (r *Reconciler) sharedFSCachePopulated(ctx context.Context, rwPVCName string) (*corev1.PersistentVolumeClaim, bool, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: rwPVCName}
	if err := r.Client.Get(ctx, key, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return pvc, pvc.Labels[cachePopulatedLabelKey] == cachePopulatedLabelValue, nil
}

func newInitLease(st *nvcav1new.StorageRequest) *coordv1.Lease {
	// Multiple ICMS requests may be trying to initialize the cache.
	leaseHolderID := st.Spec.ICMSRequestName
	// Set to an hour so the model has time to download,
	// and the reconciler can sufficiently back off the request.
	var leaseDurSeconds int32 = 3600

	lease := &coordv1.Lease{}
	lease.Name = buildInitLeaseName(st.Spec.ModelCache.CacheHandle)
	// These leases need to be checked across all functions, so should be in a constant namespace.
	lease.Namespace = ModelCacheInitNamespace
	lease.Labels = map[string]string{
		// The lease must have the cache handle to coordinate fan-out events,
		// and for association and deletion.
		modelCacheHandleLabelKey: st.Spec.ModelCache.CacheHandle,
	}

	lease.Spec.HolderIdentity = &leaseHolderID
	lease.Spec.LeaseDurationSeconds = &leaseDurSeconds

	return lease
}

func buildInitLeaseName(cacheHandle string) string {
	return "modelcache-init-" + cacheHandle
}

/*
Returns:
	PVCUpdateFailed -> ROPVC, but failed to update the OwnerReferences, Caller should re-attempt update
	pvcUnbound -> ROPVC, OwnerReference Updated but PVC is Still Unbound, not usable
	pvcBound -> ROPVC and Usable, Workers Can be created with this PVC Name for volume Name
*/

func (r *Reconciler) getPVCState(pvc *corev1.PersistentVolumeClaim) pvcState {
	return getPVCState(pvc, r.k8sTimeConfig.ModelCacheROPVCBindTimeGracePeriod)
}

// getPVCState is mocked in tests
var getPVCState = func(pvc *corev1.PersistentVolumeClaim, gracePeriod time.Duration) pvcState {
	// reference already added return, job should also exist
	if pvc.Status.Phase == corev1.ClaimBound {
		return pvcBound
	}
	if time.Since(pvc.ObjectMeta.CreationTimestamp.Time) > gracePeriod {
		return pvcBindFailed
	}
	return pvcUnbound
}

// handlePendingModelCache checks if initJob has succeeded or is making progress.
// If the job has failed or its pods are stuck/failing for unrecoverable reasons,
// the request is marked failed.
func (r *Reconciler) handlePendingModelCache(
	ctx context.Context,
	stCopy *nvcav1new.StorageRequest,
	initJob *batchv1.Job,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	jobKey := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: initJob.Name}
	if err := r.Client.Get(ctx, jobKey, initJob); err != nil {
		if apierrors.IsNotFound(err) {
			// The cache may not have received the creation event yet,
			// so requeue for up to 30 seconds before failing.
			if stCopy.Status.LastPhaseTransitionTime != nil &&
				stCopy.Status.LastPhaseTransitionTime.Add(30*time.Second).After(r.nowFunc()) {
				return reconcile.Result{Requeue: true}, nil
			}
			log.Error(err, "Failed to get cache init job for pending request")
			return reconcile.Result{}, r.terminalErrorWithMetricErr("job_not_found", fmt.Errorf("init job not found after timeout: %w", err))
		}
		log.Error(err, "Failed to get cache init job for pending request")
		return reconcile.Result{}, err
	}
	jobState := r.getInitCacheJobState(ctx, initJob)
	if jobState == initCacheJobCompleted {
		log.V(1).Info("Init job completed, transition to init running")
		// While unlikely to have happened by this point, the Job may have completed.
		stCopy.Status.Phase = nvcav1new.StorageInitRunning
		return reconcile.Result{Requeue: true}, nil
	}

	if initJob.Spec.Selector == nil {
		if initJob.Status.StartTime != nil {
			return reconcile.Result{}, r.terminalErrorWithMetric("init_job_failed", "init job selector is not set after job started")
		}
		log.V(1).Info("Init job selector is empty, waiting for job controller to set")
		return reconcile.Result{Requeue: true}, nil
	}
	podSel, err := metav1.LabelSelectorAsSelector(initJob.Spec.Selector)
	if err != nil {
		return reconcile.Result{}, r.terminalErrorWithMetricErr("init_job_failed", fmt.Errorf("invalid label selector: %w", err))
	}
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, client.MatchingLabelsSelector{Selector: podSel}); err != nil {
		log.Error(err, "Failed to list cache init job pods for pending request")
		return reconcile.Result{}, err
	}
	anyPodProgressing := false
	podIssues := sets.Set[string]{}
	for _, pod := range podList.Items {
		log.V(1).Info("Checking init job pod", "pod", pod.Name)
		podIssueReason, isProg := isInitJobPodProgressing(&pod, r.k8sTimeConfig)
		if isProg {
			anyPodProgressing = true
			break
		}
		if podIssueReason != "" {
			podIssues.Insert(podIssueReason)
		}
	}
	if jobState == initCacheJobInProgress && (anyPodProgressing || podIssues.Len() == 0) {
		// Transition phase once the job has completed or a job pod is running.
		if anyPodProgressing {
			log.V(1).Info("Init job is progressing, transition to init running")
			stCopy.Status.Phase = nvcav1new.StorageInitRunning
			return reconcile.Result{Requeue: true}, nil
		}
		log.V(1).Info("Init job pods have not progressed, remaining in current phase")
		// Requeue should happen by pod events. In case no pod begins running, requeue after a minute.
		return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	// The job has failed or there are pod issues.
	if podIssues.Len() != 0 {
		log.Error(fmt.Errorf("issues with init job pods"), "Init job pods are failing",
			"issues", strings.Join(podIssues.UnsortedList(), ","))
	}
	// The caller's cleanup method will delete cache resources.
	reason := mapPodIssuesToFailureReason(podIssues)
	return reconcile.Result{}, r.terminalErrorWithMetric(reason, "init job failed")
}

func (r *Reconciler) getInitCacheJobState(ctx context.Context, job *batchv1.Job) initCacheJobState {
	log := logf.FromContext(ctx).WithValues("job", job.Name)

	if job.Status.CompletionTime != nil && job.Status.Succeeded > 0 {
		return initCacheJobCompleted
	}

	var backoffLimit int32
	if job.Spec.BackoffLimit != nil {
		backoffLimit = *job.Spec.BackoffLimit
	} else {
		// Defaults to 6 on Job specs.
		backoffLimit = 6
	}
	if job.Status.Failed > backoffLimit ||
		(job.Status.Active != 0 && time.Since(job.ObjectMeta.CreationTimestamp.Time) >= r.k8sTimeConfig.InitCacheJobFailureThreshold) {
		if job.Status.Failed > backoffLimit {
			log.Error(fmt.Errorf("init job failed more than backoff limit"),
				"Init cache job has failed more than the backoff limit", "limit", backoffLimit)
		} else {
			log.Error(fmt.Errorf("init job failed more than failure threshold timeout"),
				"init cache job has failed or not completed over threshold duration since launch",
				"timeout", r.k8sTimeConfig.InitCacheJobFailureThreshold)
		}
		return initCacheJobFailed
	}

	return initCacheJobInProgress
}

// getInitCacheJobFailureReason returns the failure reason for a failed init cache job.
// This mirrors the logic in getInitCacheJobState to determine why the job failed.
func (r *Reconciler) getInitCacheJobFailureReason(job *batchv1.Job) string {
	var backoffLimit int32
	if job.Spec.BackoffLimit != nil {
		backoffLimit = *job.Spec.BackoffLimit
	} else {
		backoffLimit = 6
	}
	if job.Status.Failed > backoffLimit {
		return modelcachetypes.ReasonJobBackoffExceeded
	}
	return modelcachetypes.ReasonJobTimeout
}

func isInitJobPodProgressing(pod *corev1.Pod, k8sTimeConfig *k8sutil.TimeConfig) (string, bool) {
	ps := pod.Status
	switch ps.Phase {
	case corev1.PodPending, corev1.PodUnknown:
		if k8sutil.IsPodScheduled(ps) {
			if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.MaxImagePullErrorThreshold) {
				if _, _, hasIssues := k8sutil.ImagePullIssuesReported(ps); hasIssues {
					return "image pull issues", false
				}
			}
			stuck, _ := k8sutil.IsPodStuckInitializing(pod, k8sTimeConfig)
			if stuck {
				return "init stuck initializing", false
			}
			if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodScheduledThreshold) {
				return "init stuck initializing", false
			}
		} else if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodScheduledThreshold) {
			// Pod is getting stuck for > 10mins and not getting scheduled it will be killed
			return "timed out waiting to be scheduled", false
		}
		return "", false
	case corev1.PodRunning, corev1.PodSucceeded:
		return "", true
	case corev1.PodFailed:
		// Pod is getting rejected for admission it will be killed
		if rejected, _ := k8sutil.IsPodAdmissionRejected(ps); rejected {
			return "admission rejected", false
		}
		return "failed", false
	}
	// Unreachable
	return fmt.Sprintf("code bug: unreachable pod phase %s", ps.Phase), false
}

// isVolumeDetached performs a SINGLE, non-blocking check of whether volumeName
// is still attached read-write. The model-cache controller has one reconcile
// worker; polling for detachment inside a reconcile (the previous behavior)
// would block that worker for up to the detach timeout, starving every other
// StorageRequest, including the terminating-namespace finalizer escape hatch.
// Callers instead requeue when this returns false, so the worker is never held.
func (r *Reconciler) isVolumeDetached(ctx context.Context, volumeName string) (bool, error) {
	log := logf.FromContext(ctx).WithValues("pv", volumeName)

	vaList := &storagev1.VolumeAttachmentList{}
	if err := r.Client.List(ctx, vaList); err != nil {
		return false, fmt.Errorf("list volume attachments: %w", err)
	}
	for _, va := range vaList.Items {
		if va.Spec.Source.PersistentVolumeName == nil || *va.Spec.Source.PersistentVolumeName != volumeName {
			continue
		}
		pv := &corev1.PersistentVolume{}
		if err := r.Client.Get(ctx, client.ObjectKey{Name: volumeName}, pv); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, fmt.Errorf("get PV %v to check attachment status: %w", volumeName, err)
		}
		// A read-only attachment does not block deleting the writer PVC.
		if len(pv.Spec.AccessModes) == 1 && pv.Spec.AccessModes[0] == corev1.ReadOnlyMany {
			return true, nil
		}
		log.V(1).Info("PV still attached read-write")
		return false, nil
	}
	return true, nil
}

// Cross-namespace NVMesh volumes (in NVMesh 3.2) are a required feature for this controller's
// model caching implementation. This feature requires that the volumeHandle's 4-element encoding
// contains the target PVC's namespace as the last element.
func updateSecondaryPVVolumeHandle(volumeHandle, namespace string) (string, error) {
	lastColonIdx := strings.LastIndex(volumeHandle, ":")
	if lastColonIdx == -1 {
		return "", fmt.Errorf("volume handle %q has no colons", volumeHandle)
	}
	return string(append([]byte(volumeHandle[:lastColonIdx+1]), namespace...)), nil
}
