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
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/go-logr/logr"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/logging"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var (
	// Ensure the Reconcile implements the reconciler.Reconciler interface
	_ reconcile.Reconciler = (*Reconciler)(nil)
)

const (
	defaultRequeueDelay                       = 100 * time.Millisecond
	ConditionTypeSMBCSIDriverInstalled        = "SMBCSIDriverInstalled"
	ConditionTypeCleanupSuccessful            = "CleanupSuccessful"
	ConditionReasonSomeObjectsPendingDeletion = "SomeObjectsPendingDeletion"
	ConditionReasonAllObjectsDeleted          = "AllObjectsDeleted"
)

var (
	labelPrefix = nvcav1new.SchemeGroupVersion.Group

	// Finalizer to block deletion until dependent resources are cleaned up.
	StorageRequestFinalizer = labelPrefix + "/storage-request-finalizer"
	// Labels to filter cluster-wide resources in event handlers.
	StorageRequestOwnerKey     = labelPrefix + "/storage-request-name"
	StorageRequestNamespaceKey = labelPrefix + "/storage-request-namespace"
)

type ReconcilerOption func(*Reconciler)

func WithICMSRequestNamespace(icmsRequestNamespace string) ReconcilerOption {
	return func(r *Reconciler) {
		if icmsRequestNamespace != "" {
			r.ICMSRequestNamespace = icmsRequestNamespace
		}
	}
}

func WithNowFunc(nowFunc func() time.Time) ReconcilerOption {
	return func(r *Reconciler) {
		if nowFunc != nil {
			r.nowFunc = nowFunc
		}
	}
}

func WithCSIVolumeMountOptions(mntOptions []string) ReconcilerOption {
	return func(r *Reconciler) {
		if len(mntOptions) > 0 {
			r.csiVolumeMountOptions = mntOptions
		}
	}
}

func WithRandReader(randReader io.Reader) ReconcilerOption {
	return func(r *Reconciler) {
		if randReader != nil {
			r.randReader = randReader
		}
	}
}

func WithFeatureFlagFetcher(fff featureflag.Fetcher) ReconcilerOption {
	return func(r *Reconciler) {
		if fff != nil {
			r.fff = fff
		}
	}
}

func WithMetrics(m *metrics.Metrics) ReconcilerOption {
	return func(r *Reconciler) {
		r.metrics = m
	}
}

// WithStorageRequestAPI sets the API used to get/update StorageRequests (v1 and v2beta1).
// When set, Reconcile uses it instead of the controller-runtime client for get/update.
func WithStorageRequestAPI(api StorageRequestAPI) ReconcilerOption {
	return func(r *Reconciler) {
		r.storageRequestAPI = api
	}
}

func NewReconciler(
	cfg nvcaconfig.Config,
	client client.Client,
	decoder runtime.Decoder,
	eventRecorder record.EventRecorder,
	clusterName string,
	clusterRegion string,
	k8sTimeConfig *k8sutil.TimeConfig,
	opts ...ReconcilerOption,
) *Reconciler {
	reconciler := &Reconciler{
		cfg:                  cfg,
		Client:               client,
		Decoder:              decoder,
		clusterName:          clusterName,
		clusterRegion:        clusterRegion,
		eventRecorder:        eventRecorder,
		ICMSRequestNamespace: nvcatypes.DefaultICMSRequestNamespace,
		k8sTimeConfig:        k8sTimeConfig,
		tracer:               nvcaotel.NewTracer(),
		nowFunc:              time.Now,
		randReader:           rand.Reader,
		fff:                  featureflag.DefaultFetcher,
		initStatuses:         newInitStatusCache(client),
	}

	for _, opt := range opts {
		opt(reconciler)
	}

	return reconciler
}

type Reconciler struct {
	cfg nvcaconfig.Config

	Client               client.Client
	ICMSRequestNamespace string
	Decoder              runtime.Decoder
	clusterName          string
	clusterRegion        string
	eventRecorder        record.EventRecorder

	// eventRecorder                record.EventRecorder
	k8sTimeConfig         *k8sutil.TimeConfig
	csiVolumeMountOptions []string

	tracer            oteltrace.Tracer
	nowFunc           func() time.Time
	randReader        io.Reader
	fff               featureflag.Fetcher
	metrics           *metrics.Metrics
	initStatuses      statusCache
	storageRequestAPI StorageRequestAPI
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx, "namespace", req.Namespace, "storage", req.Name)
	ctx = logf.IntoContext(ctx, log)

	st, ref, err := r.getStorageRequest(ctx, req)
	if err != nil || st == nil {
		return reconcile.Result{}, err
	}
	if err := r.validateStorageRequest(st, ref); err != nil {
		return reconcile.Result{}, reconcile.TerminalError(err)
	}

	if done, res, err := r.tryRemoveFinalizerInTerminatingNamespace(ctx, req.Namespace, st, ref, log); done {
		return res, err
	}

	icmsReq, res, err := r.getOrCleanupICMSRequest(ctx, st, ref, log)
	if err != nil || icmsReq == nil {
		return res, err
	}

	ctx = nvcaotel.ContextWithParentSpanFromICMS(ctx, icmsReq.Spec.GetTraceContext())
	log = log.WithValues(logging.MakeICMSRequestFields(icmsReq)...)
	ctx = logf.IntoContext(ctx, log)
	stCopy := st.DeepCopy()

	res, rerr := r.doReconcile(ctx, icmsReq, st, stCopy, ref)
	r.logPhaseChangeAndRecordMetrics(ctx, st, stCopy, icmsReq, rerr)

	return res, rerr
}

func (r *Reconciler) getStorageRequest(ctx context.Context, req reconcile.Request) (
	*nvcav1new.StorageRequest, *StorageRequestRef, error) {
	if r.storageRequestAPI != nil {
		ref, err := r.storageRequestAPI.Get(ctx, req.Namespace, req.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, nil
			}
			return nil, nil, err
		}
		return ref.V1(), ref, nil
	}
	st := &nvcav1new.StorageRequest{}
	if err := r.Client.Get(ctx, req.NamespacedName, st); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return st, nil, nil
}

func (r *Reconciler) tryRemoveFinalizerInTerminatingNamespace(ctx context.Context, namespace string,
	st *nvcav1new.StorageRequest, ref *StorageRequestRef, log logr.Logger) (
	done bool, res reconcile.Result, err error) {
	if !controllerutil.ContainsFinalizer(st, StorageRequestFinalizer) {
		return false, reconcile.Result{}, nil
	}
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil || ns.DeletionTimestamp == nil {
		return false, reconcile.Result{}, nil
	}
	stCopy := st.DeepCopy()
	controllerutil.RemoveFinalizer(stCopy, StorageRequestFinalizer)
	var updateErr error
	if r.storageRequestAPI != nil && ref != nil {
		updateErr = r.storageRequestAPI.Update(ctx, ref, stCopy)
	} else {
		updateErr = r.Client.Patch(ctx, stCopy, client.MergeFrom(st))
	}
	if updateErr != nil {
		return true, reconcile.Result{}, updateErr
	}
	log.V(1).Info("Removed finalizer from StorageRequest in terminating namespace")
	return true, reconcile.Result{}, nil
}

// getICMSRequestName returns the ICMS request name from the StorageRequest.
// It prefers RequestName from the spec (storage API field); for instance namespaces
// (where the namespace name equals the ICMS request name), falls back to st.Namespace.
// This handles StorageRequests from migration or CRD version mismatches where
// the field may be stored under a different JSON key.
func (r *Reconciler) getICMSRequestName(st *nvcav1new.StorageRequest, ref *StorageRequestRef) string {
	if ref != nil && ref.v2Beta1Obj != nil && ref.v2Beta1Obj.Spec.RequestName != "" {
		return ref.v2Beta1Obj.Spec.RequestName
	}
	if st.Spec.ICMSRequestName != "" {
		return st.Spec.ICMSRequestName
	}
	// Instance namespace fallback: for helm/miniservice, instance ns name = ICMSRequest name.
	if st.Namespace != "" && st.Namespace != r.ICMSRequestNamespace {
		return st.Namespace
	}
	return ""
}

func (r *Reconciler) getOrCleanupICMSRequest(ctx context.Context, st *nvcav1new.StorageRequest,
	ref *StorageRequestRef, log logr.Logger) (
	*nvcav2beta1.ICMSRequest, reconcile.Result, error) {
	icmsReqNamespace := r.ICMSRequestNamespace
	if st.Spec.ICMSRequestNamespace != "" {
		icmsReqNamespace = st.Spec.ICMSRequestNamespace
	}
	icmsReqName := r.getICMSRequestName(st, ref)
	icmsReq := &nvcav2beta1.ICMSRequest{}
	if err := r.Client.Get(ctx, client.ObjectKey{
		Namespace: icmsReqNamespace,
		Name:      icmsReqName,
	}, icmsReq); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, reconcile.Result{}, err
		}
		patchFunc := r.patchStorageRequest
		if ref != nil {
			patchFunc = func(ctx context.Context, _, stCopy *nvcav1new.StorageRequest) error {
				return r.storageRequestAPI.Update(ctx, ref, stCopy)
			}
		}
		res, err := cleanupDanglingStorageRequest(ctx, st, r.doCleanup, patchFunc)
		if err != nil {
			log.Error(err, "failed cleaning up dangling storage request")
			return nil, res, err
		}
		return nil, res, nil
	}
	return icmsReq, reconcile.Result{}, nil
}

func (r *Reconciler) logPhaseChangeAndRecordMetrics(ctx context.Context, st, stCopy *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest, rerr error) {
	log := logf.FromContext(ctx)
	terminalErr := rerr
	if !isTerminal(rerr) {
		terminalErr = nil
	}
	phaseChanged := st.Status.Phase != stCopy.Status.Phase
	if !phaseChanged && terminalErr == nil {
		return
	}
	phaseStartTime := st.Status.LastPhaseTransitionTime
	if phaseStartTime == nil {
		phaseStartTime = icmsReq.CreationTimestamp.DeepCopy()
	}
	phaseEndTime := stCopy.Status.LastPhaseTransitionTime
	if phaseEndTime == nil || terminalErr != nil {
		phaseEndTime = &metav1.Time{Time: r.nowFunc()}
	}
	r.emitPhaseChangeEvent(st, stCopy, terminalErr, log)
	_, prevPhaseSpan := r.tracer.Start(
		ctx,
		fmt.Sprintf("nvca.storage.%s.%s", st.Spec.Type.Name(), st.Status.Phase),
		oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
		oteltrace.WithTimestamp(phaseStartTime.Time),
		oteltrace.WithAttributes(nvcaotel.GetDefaultAttributes()...),
		oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(icmsReq)...),
		oteltrace.WithAttributes(cmnotel.GetSpanCodeAttributes(1)...))
	if terminalErr != nil {
		prevPhaseSpan.RecordError(rerr)
		prevPhaseSpan.SetStatus(otelcodes.Error, rerr.Error())
		prevPhaseSpan.SetAttributes(otelattr.Bool("error", true))
	}
	prevPhaseSpan.End(oteltrace.WithTimestamp(phaseEndTime.Time))
	if stCopy.Status.Phase.IsEndState() || terminalErr != nil {
		phase := stCopy.Status.Phase
		if terminalErr != nil {
			phase = nvcav1new.StorageFailed
		}
		r.metrics.RecordStorageRequestDuration(phase.String(), phaseEndTime.Time.Sub(st.CreationTimestamp.Time).Seconds())
	}
}

func (r *Reconciler) emitPhaseChangeEvent(st, stCopy *nvcav1new.StorageRequest, terminalErr error, log logr.Logger) {
	if terminalErr != nil {
		log.Error(terminalErr, "storage request failed with terminal error")
		r.eventRecorder.Eventf(st, "Warning", "StorageRequestFailure", "storage request failed with terminal error: %v", terminalErr)
		return
	}
	if st.Status.Phase == "" {
		log.Info("storage request changed phase", "phase", stCopy.Status.Phase)
		r.eventRecorder.Eventf(st, "Normal", "PhaseChange", "phase changed to %s", stCopy.Status.Phase)
		return
	}
	log.Info("storage request changed phase", "prev_phase", st.Status.Phase, "new_phase", stCopy.Status.Phase)
	r.eventRecorder.Eventf(st, "Normal", "PhaseChange", "phase changed from %s to %s",
		st.Status.Phase, stCopy.Status.Phase)
}

func isTerminal(err error) bool {
	return errors.Is(err, reconcile.TerminalError(nil))
}

func cleanupDanglingStorageRequest(ctx context.Context,
	st *nvcav1new.StorageRequest,
	doCleanupFunc func(ctx context.Context, st *nvcav1new.StorageRequest) (reconcile.Result, error),
	patchStorageRequestFunc func(ctx context.Context, st *nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest) error,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	// copy the storage request, drop the finalizer, and update the resource
	stCopy := st.DeepCopy()

	// Ensure all resources are deleted
	log.V(1).Info("Performing cleanup action on dangling storage request before removing finalizer")
	res, rerr := doCleanupFunc(ctx, stCopy)
	// If we have success let's remove the finalizer
	cleanupCondition := meta.FindStatusCondition(stCopy.Status.Conditions, ConditionTypeCleanupSuccessful)
	if res == (reconcile.Result{}) &&
		rerr == nil &&
		cleanupCondition != nil &&
		cleanupCondition.Status == metav1.ConditionTrue {
		log.V(1).Info("removing finalizer removed from dangling storage request")
		controllerutil.RemoveFinalizer(stCopy, StorageRequestFinalizer)
	}

	// Update the storage request object with the latest status
	if err := patchStorageRequestFunc(ctx, st, stCopy); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("StorageRequest not found, will not patch, no more cleanup needed")
			res, rerr = reconcile.Result{}, nil
		} else if apierrors.IsConflict(err) {
			log.V(1).Error(nil, "Conflict updating storagerequest status, will requeue")
			if res == (reconcile.Result{}) {
				res = reconcile.Result{RequeueAfter: defaultRequeueDelay}
			}
		} else {
			if rerr != nil {
				rerr = fmt.Errorf("%w (%s)", rerr, err)
			} else {
				rerr = err
			}
		}
	}

	return res, rerr
}

func (r *Reconciler) doReconcile(
	ctx context.Context,
	icmsReq *nvcav2beta1.ICMSRequest,
	st, stCopy *nvcav1new.StorageRequest,
	ref *StorageRequestRef,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	var (
		res  reconcile.Result
		rerr error
	)
	if stCopy.DeletionTimestamp == nil {
		controllerutil.AddFinalizer(stCopy, StorageRequestFinalizer)

		switch stCopy.Spec.Type {
		case nvcav1new.ModelCacheRequest:
			res, rerr = r.doModelCache(ctx, *st, stCopy, icmsReq)
		case nvcav1new.SharedStorageRequest:
			res, rerr = r.doSharedStorageSMB(ctx, st, stCopy)
		case nvcav1new.InternalPersistentStorageRequest:
			res, rerr = r.doInternalPersistentStorage(ctx, st, stCopy)
		default:
			return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("unknown storage type %q", st.Spec.Type))
		}
	} else if controllerutil.ContainsFinalizer(stCopy, StorageRequestFinalizer) {
		log.V(1).Info("Performing cleanup action")
		res, rerr = r.doCleanup(ctx, stCopy)
		// If we have success let's remove the finalizer
		cleanupCondition := meta.FindStatusCondition(stCopy.Status.Conditions, ConditionTypeCleanupSuccessful)
		if res == (reconcile.Result{}) &&
			rerr == nil &&
			cleanupCondition != nil &&
			cleanupCondition.Status == metav1.ConditionTrue {
			controllerutil.RemoveFinalizer(stCopy, StorageRequestFinalizer)
		}
	}

	// If the phase changed mark the last phase transition time for update
	if st.Status.Phase != stCopy.Status.Phase {
		// If the status changed, update the last phase transition time for timeouts.
		stCopy.Status.LastPhaseTransitionTime = &metav1.Time{Time: r.nowFunc()}
	}

	if r.storageRequestAPI != nil {
		if err := r.storageRequestAPI.Update(ctx, ref, stCopy); err != nil {
			return res, err
		}
	} else if err := r.patchStorageRequest(ctx, st, stCopy); err != nil {
		if !apierrors.IsConflict(err) {
			if rerr != nil {
				err = fmt.Errorf("%w (%s)", rerr, err)
			}
			return res, err
		}
		log.V(1).Error(nil, "Conflict updating storagerequest status, will requeue")
		if res == (reconcile.Result{}) {
			res = reconcile.Result{Requeue: true}
		}
	}

	return res, rerr
}

func (r *Reconciler) patchStorageRequest(ctx context.Context, oldObj, newObj *nvcav1new.StorageRequest) error {
	// Patch updates the new object so need to copy and send that in to keep a copy of the original
	newObjWithStatus := newObj.DeepCopy()
	if err := r.Client.Status().Patch(ctx, newObjWithStatus, client.MergeFrom(oldObj)); err != nil {
		return fmt.Errorf("patch storage request %s status: %w", oldObj.Name, err)
	}

	newObj.ResourceVersion = newObjWithStatus.ResourceVersion
	if err := r.Client.Patch(ctx, newObj, client.MergeFrom(newObjWithStatus)); err != nil {
		return fmt.Errorf("patch storage request %s: %w", oldObj.Name, err)
	}

	return nil
}

func (r *Reconciler) doCleanup(ctx context.Context, st *nvcav1new.StorageRequest) (res reconcile.Result, err error) {
	switch st.Spec.Type {
	case nvcav1new.ModelCacheRequest:
		err = r.doCleanupModelCacheNVMesh(ctx, st)
		// Do not clean up primary PV. The periodic runner that invokes cleanupIdleModelCaches
		// will handle those.
	default:
		res, err = doCleanupNamespaced(ctx, r.Client, st)
	}
	return res, err
}

type cleanupK8sClient interface {
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error
	Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
	Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

//nolint:gocyclo
func doCleanupNamespaced(ctx context.Context, k8sClient cleanupK8sClient, owner *nvcav1new.StorageRequest) (reconcile.Result, error) {
	var errs []error
	log := logf.FromContext(ctx)

	cleanupCondition := meta.FindStatusCondition(owner.Status.Conditions, ConditionTypeCleanupSuccessful)
	if cleanupCondition == nil {
		log.Info("Initiating first cleanup action")
		meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
			Type:   ConditionTypeCleanupSuccessful,
			Status: metav1.ConditionFalse,
			Reason: ConditionReasonSomeObjectsPendingDeletion,
		})
	} else if cleanupCondition.Status == metav1.ConditionTrue {
		// If already complete, we can skip this work
		return reconcile.Result{}, nil
	}

	// Cleanup owned jobs
	var childJobs batchv1.JobList
	err := k8sClient.List(ctx, &childJobs, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else {
		for _, item := range childJobs.Items {
			if err := deleteNamespacedObjIfOwned(ctx, k8sClient, owner, &item); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	// Cleanup owned pods after jobs so the controller has time to delete them.
	var childPods corev1.PodList
	err = k8sClient.List(ctx, &childPods, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else {
		for _, item := range childPods.Items {
			if err := deleteNamespacedObjIfOwned(ctx, k8sClient, owner, &item); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	// Cleanup owned ResourceQuotas
	var childResourceQuotas corev1.ResourceQuotaList
	err = k8sClient.List(ctx, &childResourceQuotas, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else {
		for _, item := range childResourceQuotas.Items {
			if err := deleteNamespacedObjIfOwned(ctx, k8sClient, owner, &item); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	// Cleanup owned PVCs
	var childPVCs corev1.PersistentVolumeClaimList
	err = k8sClient.List(ctx, &childPVCs, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else {
		for _, item := range childPVCs.Items {
			if err := deleteNamespacedObjIfOwned(ctx, k8sClient, owner, &item); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	// Cleanup owned PVs
	err = k8sClient.DeleteAllOf(ctx, &corev1.PersistentVolume{}, client.MatchingLabels(getClusterWideResourceLabels(owner)))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}

	// Cleanup secrets
	var childSecrets corev1.SecretList
	err = k8sClient.List(ctx, &childSecrets, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	} else {
		for _, item := range childSecrets.Items {
			if err := deleteNamespacedObjIfOwned(ctx, k8sClient, owner, &item); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	// Cleanup owned StorageClasses
	err = k8sClient.DeleteAllOf(ctx, &storagev1.StorageClass{}, client.MatchingLabels(getClusterWideResourceLabels(owner)))
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		mergedErr := errors.Join(errs...)
		if k8sutil.AnyNonTransientK8sError(errs) != nil {
			log.Error(mergedErr, "non-transient errors during cleanup")
			return reconcile.Result{}, mergedErr
		}
		log.V(1).Info("transient errors during cleanup, will retry")
		return reconcile.Result{Requeue: true}, nil
	}

	// Ensure PVCs and PVs no longer exist, if so remove the storage class finalizer
	childPVCs = corev1.PersistentVolumeClaimList{}
	err = k8sClient.List(ctx, &childPVCs, client.InNamespace(owner.GetNamespace()))
	if err != nil && !apierrors.IsNotFound(err) {
		if k8sutil.IsTransientK8sError(err) {
			log.V(1).Info("transient error listing PVCs during cleanup, will retry")
			return reconcile.Result{Requeue: true}, nil
		}
		log.Error(err, "non-transient error listing PVCs during cleanup")
		return reconcile.Result{}, err
	}
	if ownedPVCs := filterOwnedPVCs(childPVCs.Items, owner); len(ownedPVCs) != 0 {
		// Requeue to wait for object deletion
		log.V(1).Info("child PersistentVolumeClaims still exist... Requeueing...", "child_pv_count", len(ownedPVCs))
		childPVCNames := func() []string {
			var names []string
			for _, v := range ownedPVCs {
				names = append(names, v.Name)
			}
			return names
		}()
		meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  ConditionReasonSomeObjectsPendingDeletion,
			Message: fmt.Sprintf("PVCs: %q", childPVCNames),
		})
		return reconcile.Result{Requeue: true}, nil
	}

	// Check the PVs as well to ensure they are deleted
	childPVs := corev1.PersistentVolumeList{}
	err = k8sClient.List(ctx, &childPVs, client.MatchingLabels(getClusterWideResourceLabels(owner)))
	if err != nil && !apierrors.IsNotFound(err) {
		if k8sutil.IsTransientK8sError(err) {
			log.V(1).Info("transient error listing PVs during cleanup, will retry")
			return reconcile.Result{Requeue: true}, nil
		}
		log.Error(err, "non-transient error listing PVs during cleanup")
		return reconcile.Result{}, err
	} else if len(childPVs.Items) > 0 {
		// Requeue to wait for object deletion
		log.V(1).Info("child PersistentVolumes still exist... Requeueing...", "child_pv_count", len(childPVs.Items))
		childPVNames := func() []string {
			var names []string
			for _, v := range childPVs.Items {
				names = append(names, v.Name)
			}
			return names
		}()
		meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  ConditionReasonSomeObjectsPendingDeletion,
			Message: fmt.Sprintf("PVs: %q", childPVNames),
		})
		return reconcile.Result{Requeue: true}, nil
	}

	// Last but not least ensure the associated storage class (if any) has it's finalizer removed
	childStorageClasses := storagev1.StorageClassList{}
	err = k8sClient.List(ctx, &childStorageClasses, client.MatchingLabels(getClusterWideResourceLabels(owner)))
	if err != nil && !apierrors.IsNotFound(err) {
		if k8sutil.IsTransientK8sError(err) {
			log.V(1).Info("transient error listing StorageClasses during cleanup, will retry")
			return reconcile.Result{Requeue: true}, nil
		}
		log.Error(err, "non-transient error listing StorageClasses during cleanup")
		return reconcile.Result{}, err
	} else if len(childStorageClasses.Items) > 0 {
		var scErrs []error
		// Patch each storage class and remove the finalizer
		for _, sc := range childStorageClasses.Items {
			scCopy := sc.DeepCopy()
			// If the storage class has a finalizer on it, remove it, if not move along
			if controllerutil.RemoveFinalizer(scCopy, StorageRequestFinalizer) {
				if err := k8sClient.Patch(ctx, scCopy, client.MergeFrom(&sc)); err != nil {
					log.Error(err, "removing finalizer failed. requeueing", "storage_class", sc.Name)
					scErrs = append(scErrs, fmt.Errorf("remove finalizer from storage class %s status: %w", sc.Name, err))
				}
			}
		}
		if len(scErrs) > 0 {
			meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
				Type:    ConditionTypeCleanupSuccessful,
				Status:  metav1.ConditionFalse,
				Reason:  ConditionReasonSomeObjectsPendingDeletion,
				Message: fmt.Sprintf("storage class errors: %q", scErrs),
			})
			if k8sutil.AnyNonTransientK8sError(scErrs) != nil {
				return reconcile.Result{}, errors.Join(scErrs...)
			}
			return reconcile.Result{Requeue: true}, nil
		}
	}

	// On success set the final type
	meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
		Type:   ConditionTypeCleanupSuccessful,
		Status: metav1.ConditionTrue,
		Reason: ConditionReasonAllObjectsDeleted,
	})
	return reconcile.Result{}, nil
}

func deleteNamespacedObjIfOwned(ctx context.Context, k8sClient cleanupK8sClient, owner, item client.Object) error {
	for _, ownerRef := range item.GetOwnerReferences() {
		if ownerRef.UID == owner.GetUID() {
			if err := k8sClient.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func filterOwnedPVCs(pvcs []corev1.PersistentVolumeClaim, owner client.Object) (filtered []corev1.PersistentVolumeClaim) {
	for _, pvc := range pvcs {
		for _, ownerRef := range pvc.OwnerReferences {
			if ownerRef.UID == owner.GetUID() {
				filtered = append(filtered, pvc)
			}
		}
	}
	return filtered
}

func (r *Reconciler) validateStorageRequest(st *nvcav1new.StorageRequest, ref *StorageRequestRef) error {
	var errs []error
	if r.getICMSRequestName(st, ref) == "" {
		errs = append(errs, fmt.Errorf("ICMS request name is not set"))
	}
	switch st.Spec.Type {
	case nvcav1new.ModelCacheRequest:
		if st.Spec.SharedStorage != nil || st.Spec.InternalPersistentStorage != nil {
			errs = append(errs, fmt.Errorf("type is %s but other request specs are set", st.Spec.Type))
		}
		if st.Spec.ModelCache == nil || st.Spec.ModelCache.CacheHandle == "" {
			errs = append(errs, fmt.Errorf("spec.modelCache.cacheHandle must be set"))
		}
	case nvcav1new.SharedStorageRequest:
		if st.Spec.SharedStorage == nil {
			errs = append(errs, fmt.Errorf("type is %s and spec.sharedStorage is not set", st.Spec.Type))
		}
		if st.Spec.ModelCache != nil || st.Spec.InternalPersistentStorage != nil {
			errs = append(errs, fmt.Errorf("type is %s but other request specs are set", st.Spec.Type))
		}
	case nvcav1new.InternalPersistentStorageRequest:
		if st.Spec.InternalPersistentStorage == nil {
			errs = append(errs, fmt.Errorf("type is %s and spec.internalPersistentStorage is not set", st.Spec.Type))
		}
		if st.Spec.ModelCache != nil || st.Spec.SharedStorage != nil {
			errs = append(errs, fmt.Errorf("type is %s but other request specs are set", st.Spec.Type))
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) applyControlled(ctx context.Context,
	st *nvcav1new.StorageRequest,
	objs ...client.Object,
) error {
	for _, obj := range objs {
		if _, err := r.applyControlledOne(ctx, st, obj); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) setControlledObjectMeta(_ context.Context,
	st *nvcav1new.StorageRequest,
	obj client.Object,
) error {
	// Use an labels to refer event handlers to the correct StorageRequest
	// for cluster-wide objects.
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, getWorkloadLabels(st))
	// Use an labels to refer event handlers to the correct StorageRequest
	// for cluster-wide objects.
	maps.Copy(labels, getClusterWideResourceLabels(st))
	// Store labels on the resource
	obj.SetLabels(labels)

	if isNamespaced, err := r.Client.IsObjectNamespaced(obj); isNamespaced {
		obj.SetNamespace(st.Namespace)
		if err := controllerutil.SetControllerReference(st, obj, r.Client.Scheme()); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("check if object is namespaced: %v", err)
	}
	return nil
}

func (r *Reconciler) applyControlledOne(ctx context.Context,
	st *nvcav1new.StorageRequest,
	obj client.Object,
) (controllerutil.OperationResult, error) {
	if err := r.setControlledObjectMeta(ctx, st, obj); err != nil {
		return controllerutil.OperationResultNone, err
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error { return nil })
	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("create or update %s %s: %v",
			obj.GetObjectKind().GroupVersionKind(), client.ObjectKeyFromObject(obj), err)
	}
	return op, nil
}

func getWorkloadLabels(obj client.Object) map[string]string {
	labels := obj.GetLabels()
	wlLabels := map[string]string{
		nvcatypes.NCAIDKey: labels[nvcatypes.NCAIDKey],
	}
	if labels[nvcatypes.FunctionIDKey] != "" {
		wlLabels[nvcatypes.FunctionIDKey] = labels[nvcatypes.FunctionIDKey]
		wlLabels[nvcatypes.FunctionVersionIDKey] = labels[nvcatypes.FunctionVersionIDKey]
	} else {
		wlLabels[nvcatypes.TaskIDKey] = labels[nvcatypes.TaskIDKey]
	}
	return wlLabels
}

func getClusterWideResourceLabels(st *nvcav1new.StorageRequest) map[string]string {
	return map[string]string{
		StorageRequestOwnerKey:     st.Name,
		StorageRequestNamespaceKey: st.Namespace,
	}
}
