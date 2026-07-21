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

package mscontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/name"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcalogging "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/logging"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcfdra "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/dra"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type Reconciler struct {
	ControllerOptions

	cfg nvcaconfig.Config

	Client      client.Client
	RESTConfig  *rest.Config
	Decoder     runtime.Decoder
	ReValClient ReValClient
	NFClient    nodefeatures.Client

	eventRecorder record.EventRecorder

	// Chart cache.
	chartCache chartcache.Cache
	// Instance type cache
	regITCache registrationInstanceTypeCache
	// Attributes for enforcement
	enabledAttrs featureflag.Attributes
	// Typed status checkers
	statusCheckers map[schema.GroupVersionKind]checkStatusFunc
	// GVK cache for fast type-to-GVK lookups
	gvkCache *gvkCache

	tracer oteltrace.Tracer

	now func() time.Time
	// Creates a client.Client that performs request on behalf of a specific user,
	// in this case a namespace's service account.
	newImpersonatingClient func(namespace string) (client.Client, error)
	// Creatse a function that checks verb permissions for resources, caching responses per GVK in caniCache.
	newPermissionsChecker func(caniCache map[schema.GroupVersionKind]error, verbs []string) permissionCheckerFunc

	// Cache of failed workload update attempts by revision to avoid unnecessary expensive operations like rendering.
	failedWorkloadUpdateRevisionCache     map[string]error
	failedWorkloadUpdateRevisionCacheLock sync.RWMutex
}

// permissionCheckerFunc checks if c can perform a minimal set of actions on an object
// of type gvk in namespace.
type permissionCheckerFunc func(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, namespace string) error

var (
	finalizer = v1alpha1.SchemeGroupVersion.Group + "/miniservice-finalizer"

	// miniServicePhaseToGaugeMetricValue maps a miniservice phase to some float64 to update
	// the MiniServiceReadyStatus metric gauge. Phases not in this set map to 0, which is expected.
	miniServicePhaseToGaugeMetricValue = map[v1alpha1.MiniServicePhase]float64{
		v1alpha1.MiniServiceCompleted:     2, // Completed and Running are the same external state
		v1alpha1.MiniServiceRunning:       2,
		v1alpha1.MiniServiceInstalled:     1,
		v1alpha1.MiniServiceInstallFailed: -1,
		v1alpha1.MiniServiceFailed:        -2,
	}
)

const (
	InferenceNamespaceEnvKey        = "HELM_CHART_NAMESPACE"
	miniServiceUnknownPhase         = "Unknown"
	miniServiceUnknownFailureReason = "Unknown"
	miniServicePhaseAttrKey         = "nvca.miniservice.phase"
	miniServicePrevPhaseAttrKey     = "nvca.miniservice.prev_phase"
	miniServiceNextPhaseAttrKey     = "nvca.miniservice.next_phase"
	miniServiceFailureReasonAttrKey = "nvca.miniservice.failure_reason"
)

// Reconcile implements controller-runtime Reconciler.
//
//nolint:gocyclo // reconciliation branches by resource type and state
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx = withGVKCache(ctx, r.gvkCache)
	log := logf.FromContext(ctx)

	log.V(1).Info("Reconciling")

	ms := &v1alpha1.MiniService{}
	if err := r.Client.Get(ctx, req.NamespacedName, ms); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("MiniService was deleted, skipping requeue")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if ms.Spec.Namespace == "" {
		return reconcile.Result{}, reconcile.TerminalError(
			fmt.Errorf("miniservice %s has no namespace", ms.Name))
	}
	if ms.Spec.ICMSRequestName == "" {
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("ICMS request name is not set"))
	}

	ncaID := ms.Annotations[nvcatypes.NCAIDKey]
	r.Metrics.RecordMiniServiceReconcilePhase(ncaID, string(ms.Status.Phase))
	// DEPRECATED. Will be removed once MiniServiceReadyStatus is fully migrated to use counter based metrics for failures & phase transitions
	r.Metrics.SetMiniServiceReadyStatus(
		ncaID,
		miniServicePhaseToGaugeMetricValue[ms.Status.Phase],
	)

	msCopy := ms.DeepCopy()

	icmsReq := &nvcav2beta1.ICMSRequest{}
	icmsReqKey := client.ObjectKey{Namespace: r.ICMSRequestNamespace, Name: ms.Spec.ICMSRequestName}
	srerr := r.Client.Get(ctx, icmsReqKey, icmsReq)
	if srerr != nil && !apierrors.IsNotFound(srerr) {
		return reconcile.Result{}, srerr
	}

	if srerr == nil {
		// Apply ICMSRequest identifying fields to logger for observability.
		log = log.WithValues(nvcalogging.MakeICMSRequestFields(icmsReq)...)
		ctx = logf.IntoContext(ctx, log)
		// Inject span data from the request.
		ctx = nvcaotel.ContextWithParentSpanFromICMS(ctx, icmsReq.Spec.GetTraceContext())
		// TODO: trace for reconciles like in storage request controller
		// https://github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/-/blob/b272911d/pkg/storage/reconcile.go#L223-268
	} else {
		log.Info("ICMSRequest for MiniService was deleted, cleaning up",
			"icmsRequest", ms.Spec.ICMSRequestName)
	}

	var (
		res  reconcile.Result
		rerr error
	)
	if msCopy.DeletionTimestamp == nil && srerr == nil {
		controllerutil.AddFinalizer(msCopy, finalizer)

		// Detect spec changes (e.g., helm values update) that require a re-render.
		if err := r.prepareUpdateIfNeeded(ctx, msCopy); err != nil {
			return reconcile.Result{}, err
		}

		switch msCopy.Status.Phase {
		case "", v1alpha1.MiniServiceCacheInProgress, v1alpha1.MiniServiceInstalling:
			if needsWorkloadUpdate(msCopy) {
				log.Info("Performing workload update action")
				res, rerr = r.doUpdateWorkload(ctx, msCopy, icmsReq)
			} else {
				log.Info("Performing install action")
				res, rerr = r.doInstall(ctx, msCopy, icmsReq)
			}
			if isTerminal(rerr) {
				msCopy.Status.Phase = v1alpha1.MiniServiceInstallFailed
				if !meta.IsStatusConditionFalse(msCopy.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful) {
					meta.SetStatusCondition(&msCopy.Status.Conditions, metav1.Condition{
						Type:    v1alpha1.MiniServiceConditionInstallSuccessful,
						Status:  metav1.ConditionFalse,
						Reason:  v1alpha1.MiniServiceStatusReasonUnexpectedInstallError,
						Message: rerr.Error(),
					})
				}
			}
		case v1alpha1.MiniServiceInstalled, v1alpha1.MiniServiceStarting, v1alpha1.MiniServiceRunning:
			log.Info("Performing status action")
			priorPhase := msCopy.Status.Phase
			if res, rerr = r.doStatus(ctx, msCopy, icmsReq); isTerminal(rerr) {
				// doStatus considers whether the miniservice was running in terminal error cases,
				// since pre-running is an install failure, and post-running is a runtime error.
				if priorPhase == v1alpha1.MiniServiceRunning {
					msCopy.Status.Phase = v1alpha1.MiniServiceFailed
				} else {
					msCopy.Status.Phase = v1alpha1.MiniServiceInstallFailed
				}
				if !meta.IsStatusConditionFalse(msCopy.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy) {
					meta.SetStatusCondition(&msCopy.Status.Conditions, metav1.Condition{
						Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
						Status:  metav1.ConditionFalse,
						Reason:  v1alpha1.MiniServiceStatusReasonUnexpectedRuntimeError,
						Message: rerr.Error(),
					})
				}
			}
		case v1alpha1.MiniServiceCompleted:
			log.Info("Skipping reconcile on completed MiniService")
			return reconcile.Result{}, nil
		case v1alpha1.MiniServiceInstallFailed, v1alpha1.MiniServiceFailed:
			log.Info("Skipping reconcile on failed MiniService")
			return reconcile.Result{}, nil
		default:
			return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("unknown phase: %s", msCopy.Status.Phase))
		}

		msCopy.Status.ObservedGeneration = msCopy.Generation
	} else {
		log.V(1).Info("Performing cleanup action")
		res, rerr = r.doCleanup(ctx, msCopy)
	}

	phaseChanged := ms.Status.Phase != msCopy.Status.Phase
	if phaseChanged {
		// If the status changed, update the last phase transition time for timeouts.
		msCopy.Status.LastPhaseTransitionTime = &metav1.Time{Time: r.now()}
	}
	if err := r.patchMiniService(ctx, ms, msCopy); err != nil {
		switch {
		case apierrors.IsConflict(err):
			log.V(1).Info("Conflict updating MiniService status, will requeue")
			return reconcile.Result{Requeue: true}, nil
		case apierrors.IsNotFound(err):
			// Cleanup may have removed the finalizer, resulting in not-found during some patch operation.
			return reconcile.Result{}, nil
		default:
			log.Error(err, "Failed to patch MiniService")
			return reconcile.Result{}, err
		}
	}
	if phaseChanged {
		fromPhase := normalizeMiniServicePhase(ms.Status.Phase)
		toPhase := normalizeMiniServicePhase(msCopy.Status.Phase)
		r.Metrics.RecordMiniServicePhaseTransition(ncaID, fromPhase, toPhase)
		r.emitMiniServicePhaseSpan(ctx, ms, msCopy, icmsReq, rerr, fromPhase,
			toPhase, recordMiniServiceFailures(r.Metrics, ncaID, msCopy))

		if ms.Status.Phase == "" {
			log.Info("MiniService changed phase", "new_status", msCopy.Status.Phase)
			r.eventRecorder.Eventf(ms, "Normal", "PhaseChange", "phase changed to %s",
				msCopy.Status.Phase)
		} else {
			log.Info("MiniService changed phase", "prev_status", ms.Status.Phase,
				"new_status", msCopy.Status.Phase)
			r.eventRecorder.Eventf(ms, "Normal", "PhaseChange", "phase changed from %s to %s",
				ms.Status.Phase, msCopy.Status.Phase)
		}
	}

	return res, rerr
}

func isTerminal(err error) bool { return errors.Is(err, reconcile.TerminalError(nil)) }

// unwrapTerminalError unwraps err if terminal until a non-terminal error is found.
func unwrapTerminalError(err error) error {
	for err != nil {
		if !isTerminal(err) {
			return err
		}
		err = errors.Unwrap(err)
	}
	return err
}

func normalizeMiniServicePhase(phase v1alpha1.MiniServicePhase) string {
	if phase == "" {
		return miniServiceUnknownPhase
	}
	return string(phase)
}

func recordMiniServiceFailures(m *metrics.Metrics, ncaID string, ms *v1alpha1.MiniService) []string {
	var reasons []string
	if ms.Status.Phase != v1alpha1.MiniServiceInstallFailed && ms.Status.Phase != v1alpha1.MiniServiceFailed {
		return reasons
	}

	for _, condition := range ms.Status.Conditions {
		if v1alpha1.MiniServiceStatusBadReasons[condition.Reason] {
			m.RecordMiniServiceFailure(ncaID, condition.Reason)
			reasons = append(reasons, condition.Reason)
		}
	}
	if len(reasons) == 0 {
		m.RecordMiniServiceFailure(ncaID, miniServiceUnknownFailureReason)
		reasons = []string{miniServiceUnknownFailureReason}
	}
	return reasons
}

func (r *Reconciler) emitMiniServicePhaseSpan(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	msCopy *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	rerr error,
	fromPhase string,
	toPhase string, failureReasons []string) {
	phaseStartTime := ms.Status.LastPhaseTransitionTime
	if phaseStartTime == nil {
		phaseStartTime = ms.CreationTimestamp.DeepCopy()
	}
	phaseEndTime := msCopy.Status.LastPhaseTransitionTime
	if phaseEndTime == nil {
		nowFn := r.now
		if nowFn == nil {
			nowFn = time.Now
		}
		phaseEndTime = &metav1.Time{Time: nowFn()}
	}

	attrs := []otelattr.KeyValue{
		otelattr.String(miniServicePrevPhaseAttrKey, fromPhase),
		otelattr.String(miniServiceNextPhaseAttrKey, toPhase),
		otelattr.String(miniServicePhaseAttrKey, toPhase),
	}
	if ms.Labels[nvcatypes.FunctionIDKey] != "" {
		attrs = append(attrs, otelattr.String(nvcaotel.FunctionIDAttributeKey, ms.Labels[nvcatypes.FunctionIDKey]))
	}
	if ms.Labels[nvcatypes.FunctionVersionIDKey] != "" {
		attrs = append(attrs, otelattr.String(nvcaotel.FunctionVersionIDAttributeKey, ms.Labels[nvcatypes.FunctionVersionIDKey]))
	}
	if ms.Labels[nvcatypes.TaskIDKey] != "" {
		attrs = append(attrs, otelattr.String(nvcaotel.TaskIDAttributeKey, ms.Labels[nvcatypes.TaskIDKey]))
	}
	if ms.Annotations[nvcatypes.NCAIDKey] != "" {
		attrs = append(attrs, otelattr.String(nvcaotel.NCAIDAttributeKey, ms.Annotations[nvcatypes.NCAIDKey]))
	}
	if (msCopy.Status.Phase == v1alpha1.MiniServiceInstallFailed || msCopy.Status.Phase == v1alpha1.MiniServiceFailed) &&
		len(failureReasons) > 0 {
		attrs = append(attrs, otelattr.StringSlice(miniServiceFailureReasonAttrKey, failureReasons))
	}

	spanOpts := []oteltrace.SpanStartOption{
		oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
		oteltrace.WithTimestamp(phaseStartTime.Time),
		oteltrace.WithAttributes(nvcaotel.GetDefaultAttributes()...),
		oteltrace.WithAttributes(attrs...),
		oteltrace.WithAttributes(cmnotel.GetSpanCodeAttributes(1)...),
	}
	if icmsReq != nil {
		spanOpts = append(spanOpts, oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(icmsReq)...))
	}

	_, phaseSpan := r.tracer.Start(
		ctx,
		"nvca.miniservice.reconcile",
		spanOpts...,
	)
	if rerr != nil {
		phaseSpan.RecordError(rerr)
		phaseSpan.SetStatus(otelcodes.Error, rerr.Error())
		phaseSpan.SetAttributes(otelattr.Bool("error", true))
	}
	phaseSpan.End(oteltrace.WithTimestamp(phaseEndTime.Time))
}

func (r *Reconciler) patchMiniService(ctx context.Context, oldObj, newObj *v1alpha1.MiniService) error {
	// Patch updates the new object so need to copy and send that in to keep a copy of the original
	newObjWithStatus := newObj.DeepCopy()
	if err := r.Client.Status().Patch(ctx, newObjWithStatus, client.MergeFrom(oldObj)); err != nil {
		return fmt.Errorf("patch miniservice %s status: %w", oldObj.Name, err)
	}

	// Only patch finalizers if they are different. Spec and other metadata must not be modified by the controller.
	if !slices.Equal(newObj.Finalizers, oldObj.Finalizers) {
		if err := r.Client.Patch(ctx, &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name:            newObj.Name,
				Finalizers:      newObj.Finalizers,
				ResourceVersion: newObjWithStatus.ResourceVersion,
			},
		}, client.MergeFrom(&v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name:            newObjWithStatus.Name,
				Finalizers:      newObjWithStatus.Finalizers,
				ResourceVersion: newObjWithStatus.ResourceVersion,
			},
		})); err != nil {
			return fmt.Errorf("patch miniservice %s: %w", oldObj.Name, err)
		}
	}

	return nil
}

// saveWorkloadConfig persists the workload config decoded from the rendered workload config
// ConfigMap onto the MiniService spec, patching only that field inline. It is a no-op when the
// config is unchanged. On success ms is updated in place so callers see the persisted value
// and current ResourceVersion.
func (r *Reconciler) saveWorkloadConfig(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	desired *v1alpha1.WorkloadConfig,
) error {
	if cmp.Equal(ms.Spec.WorkloadConfig, desired, cmpopts.EquateEmpty()) {
		return nil
	}

	base := &v1alpha1.MiniService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "MiniService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            ms.Name,
			ResourceVersion: ms.ResourceVersion,
		},
		Spec: v1alpha1.MiniServiceSpec{
			WorkloadConfig: desired.DeepCopy(),
		},
	}
	if err := r.Client.Patch(ctx, base, client.Apply, client.ForceOwnership, client.FieldOwner(managedByValue)); err != nil {
		return fmt.Errorf("patch miniservice %s workload config: %w", ms.Name, err)
	}

	ms.Spec.WorkloadConfig = desired
	ms.ResourceVersion = base.ResourceVersion
	return nil
}

// prepareUpdateIfNeeded detects spec changes by comparing metadata.generation
// to status.observedGeneration. Because generation increments on any spec field change,
// this method additionally compares helm values against the latest revision ConfigMap.
// If only non-values fields changed, it returns without triggering an update
// (observedGeneration is still updated at the end of Reconcile).
func (r *Reconciler) prepareUpdateIfNeeded(ctx context.Context, ms *v1alpha1.MiniService) error {
	if ms.Status.ObservedGeneration == 0 || ms.Generation == ms.Status.ObservedGeneration {
		return nil
	}

	log := logf.FromContext(ctx)
	log.Info("Spec change detected, checking if helm values changed",
		"generation", ms.Generation,
		"observedGeneration", ms.Status.ObservedGeneration,
		"currentRevision", ms.Status.Revision,
	)

	changed, err := r.helmValuesChanged(ctx, ms)
	if err != nil {
		return fmt.Errorf("check if helm values changed: %w", err)
	}
	if !changed {
		log.Info("Helm values unchanged, skipping update")
		return nil
	}

	oldCacheKey := getCacheKey(ms)
	if err := r.chartCache.Delete(oldCacheKey); err != nil {
		log.V(1).Info("Failed to delete old chart cache entry (may not exist)", "error", err)
	}

	ms.Status.RenderDetails = nil
	ms.Status.Revision++
	ms.Status.Phase = v1alpha1.MiniServiceInstalling

	log.Info("Update prepared", "newRevision", ms.Status.Revision)

	return nil
}

//nolint:gocyclo
func (r *Reconciler) doInstall(ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	var (
		byooLaunchSpec *common.TelemetriesLaunchSpecification
	)
	funcLaunchSpec := icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification
	taskLaunchSpec := icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification
	if funcLaunchSpec != nil {
		byooLaunchSpec = funcLaunchSpec.Telemetries
	} else if taskLaunchSpec != nil {
		byooLaunchSpec = taskLaunchSpec.Telemetries
	} else {
		return reconcile.Result{}, reconcile.TerminalError(
			fmt.Errorf("both function and task launch specs are empty in ICMSRequest %s", icmsReq.Name))
	}

	functionName, taskName, err := getFunctionNameAndTaskName(
		icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification,
		icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification)
	if err != nil {
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("failed to get function name and task name: %w", err))
	}

	// Input for building the miniservice metadata ConfigMap.
	metaInput := MetadataInput{
		FunctionName:   functionName,
		TaskName:       taskName,
		Tolerations:    r.cfg.Workload.Tolerations,
		PodAnnotations: make(map[string]string),
		PodLabels:      make(map[string]string),
	}

	// Enable NVIDIA Nsight GPU profiling for this function's workload pods when it is in
	// the profiling allowlist. The label is injected onto admitted pods by the miniservice
	// mutating webhook via metaInput.PodLabels.
	if r.NsightProfilingAllowlist.ShouldProfile(icmsReq.Spec.FunctionDetails.FunctionID) {
		metaInput.PodLabels[r.NsightProfilingAllowlist.LabelKey()] = r.NsightProfilingAllowlist.LabelValue()
	}

	ms.Status.Phase = v1alpha1.MiniServiceInstalling

	unfilteredInfraObjs, err := r.translateWorkload(ms, icmsReq)
	if err != nil {
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("translate launch specification: %w", err))
	}

	// getArtifacts categorized from icmsReq
	utilsPod,
		workerPullSecrets, workloadPullSecrets,
		cacheInitJob, cacheInitPVC,
		byooSvc,
		infraObjs,
		err := findWellKnownObjects(unfilteredInfraObjs)
	if err != nil {
		return reconcile.Result{}, err
	}

	objsData, isRendered, err := r.getRenderedData(ctx, ms)
	if err != nil {
		return reconcile.Result{}, err
	}

	if !isRendered {
		if objsData, err = r.render(ctx, ms, icmsReq); err != nil {
			return reconcile.Result{}, err
		}

		if err := r.saveRenderedData(ctx, ms, objsData); err != nil {
			return reconcile.Result{}, err
		}
	}

	workloadObjs, resources, workloadConfig, err := decodeObjects(ctx, r.Decoder, objsData)
	if err != nil {
		return reconcile.Result{}, err
	}
	// Persist workload config decoded from the rendered workload config ConfigMap so later
	// reconciles (e.g. status) can source it from the spec.
	if err := r.saveWorkloadConfig(ctx, ms, workloadConfig); err != nil {
		return reconcile.Result{}, err
	}
	// Update the resources status in the MiniService status.
	updateResourcesStatus(ms, resources)
	// ReVal may render filtered workload pull secrets, which should be used if possible.
	workloadPullSecrets, err = dedupeWorkloadImagePullSecrets(workloadObjs, workloadPullSecrets)
	if err != nil {
		log.Error(err, "Failed to deduplicate workload image pull secrets")
		return reconcile.Result{}, reconcile.TerminalError(err)
	}

	metaInput.ImagePullSecrets = append(metaInput.ImagePullSecrets, workerPullSecrets...)
	metaInput.ImagePullSecrets = append(metaInput.ImagePullSecrets, workloadPullSecrets...)

	// Now that all decoding and validating is complete, create objects.
	infraObjectMutators := newEmptyObjectMutators()
	infraObjectMutators.setGeneralObjectMutatorsForRequest(r.FeatureFlagFetcher, ms, icmsReq,
		r.ClusterRegion, r.ClusterName, functionName, taskName, true)
	infraObjectMutators.setImagePullSecretMutators(workerPullSecrets)

	// If KAI Scheduler is enabled, set schedulerName and queue label on workload pods
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		infraObjectMutators.setKAISchedulerMutators()
	}

	genericInfraMutator := newGenericMutator(r.FeatureFlagFetcher, ms, icmsReq,
		r.ClusterRegion, r.ClusterName, functionName, taskName, true)

	// Only add labels and annotations to workload objects. The pod webhook will handle applying metadata to pods.
	genericWorkloadMutator := newGenericMutator(r.FeatureFlagFetcher, ms, icmsReq,
		r.ClusterRegion, r.ClusterName, functionName, taskName, false)

	if err := r.ensureInstanceNamespace(ctx, ms, icmsReq); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.prepareTransportTLSForWorkloads(ctx, ms, workloadObjs); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.ensureApplyPrerequisites(ctx,
		ms, icmsReq,
		infraObjectMutators, genericInfraMutator,
		workerPullSecrets, workloadPullSecrets,
	); err != nil {
		return reconcile.Result{}, err
	}

	// 3rd party registry cred updater objects.
	// Must be done after default service account is provisioned with pull secrets.
	// Prerequisites may need some time to complete, so requeue informs doInstall to return
	// and wait for completion events.
	if requeue, err := r.ensureImageCredentialUpdaterObjects(ctx, ms, icmsReq, infraObjectMutators); err != nil || requeue {
		if err != nil {
			err = fmt.Errorf("ensure image credential updater objects: %w", err)
		}
		return reconcile.Result{}, err
	}

	needsBYOO := byooLaunchSpec != nil &&
		r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOObservability)

	// Ensure the utils pod is present prior to instance creation,
	// since the instance relies on utils for a variety of tasks.
	if utilsPod.Labels == nil {
		utilsPod.Labels = map[string]string{}
	}
	if utilsPod.Annotations == nil {
		utilsPod.Annotations = map[string]string{}
	}

	// Skip gxcache webhook injection - utils pod doesn't need shader cache
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.GXCache) {
		utilsPod.Annotations[nvcatypes.GXCacheSkipInjectionAnnotationKey] = nvcatypes.GXCacheSkipInjectionAnnotationValue
	}

	// Apply custom annotations from BackendK8sCache
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmCustomAnnotations) {
		k8sutil.ApplyCustomAnnotations(utilsPod, r.CustomAnnotations)
	}

	// All Pods created by NVCA must use the same scheduler.
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		utilsPod.Spec.SchedulerName = kaischeduler.SchedulerName
		utilsPod.Labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.DefaultQueue
	}

	// Set required worker env vars.
	workerEnvs := []corev1.EnvVar{
		{Name: nvcatypes.InstanceIDEnvKey, Value: ms.Name},
		{Name: InferenceNamespaceEnvKey, Value: ms.Spec.Namespace},
		{Name: nvcatypes.InferenceReadyTimeoutEnvKey, Value: r.K8sTimeConfig.WorkerStartupTimeout.String()},
	}
	k8sutil.MergePodSpecTolerations(&utilsPod.Spec, r.cfg.Workload.Tolerations...)
	k8sutil.AddEnvsToContainers(utilsPod.Spec.InitContainers, workerEnvs...)
	k8sutil.AddEnvsToContainers(utilsPod.Spec.Containers, workerEnvs...)

	// The utils pod for a function with BYOO enabled must be targeted
	// by the BYOO metrics egress netpol in this namespace, which uses a specific label.
	if needsBYOO {
		utilsPod.Labels[k8sutil.BYOOMetricsEgressTargetLabelKey] =
			k8sutil.BYOOMetricsEgressTargetLabelValue
	}

	// Select the model cache backend ONCE per reconcile and use the same value
	// for both StorageRequest creation (below) and the ephemeral annotation
	// (further down). Selecting twice would race a StorageClass change between
	// the two calls and could both create a ModelCacheRequest and inject the
	// ephemeral init container, or neither.
	// Skip the lookup entirely when the launch does not request caching: the
	// selection needs a StorageClass API call, and a transient API error must
	// not fail or delay installs that never cache.
	// Non-terminal on purpose: this is a StorageClass API lookup, so a
	// transient error (timeout, rate-limit) must be retried.
	cacheBackend := nvcastorage.HelmCacheBackendNone
	if cacheLaunchRequested(icmsReq) {
		cacheBackend, err = nvcastorage.SelectHelmCacheBackend(ctx, r.Client, r.FeatureFlagFetcher)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("select helm cache backend: %w", err)
		}
	}

	// Create storage requests if configured for the cluster.
	stDone, err := r.doStorageRequests(ctx,
		ms, icmsReq, infraObjectMutators,
		workerPullSecrets, cacheInitJob, cacheInitPVC, cacheBackend,
	)
	if err != nil || !stDone {
		return reconcile.Result{}, err
	}

	stList := &nvcav2beta1.StorageRequestList{}
	if err := r.Client.List(ctx, stList, client.InNamespace(ms.Spec.Namespace)); err != nil {
		return reconcile.Result{}, (err)
	}
	instanceStorageAnnos, utilsStorageAnnos := getAnnotationsForReadyStorageRequests(stList)
	if len(instanceStorageAnnos) != 0 {
		maps.Copy(metaInput.PodAnnotations, instanceStorageAnnos)
	}
	if len(utilsStorageAnnos) != 0 {
		utilsPod.Annotations = mergeMaps(utilsPod.Annotations, utilsStorageAnnos)
		icmsReq.Status.CacheReferenceName = utilsStorageAnnos[nvcastorage.WebhookModelCachePVCNameAnnotationKey]
		icmsReqWithStatus := icmsReq.DeepCopy()
		if err := r.Client.Status().Patch(ctx, icmsReqWithStatus, client.MergeFrom(icmsReq)); err != nil {
			if !apierrors.IsConflict(err) {
				log.Error(err, "Failed to patch ICMSRequest status with cache reference")
				return reconcile.Result{}, err
			}
			log.V(1).Info("Conflict updating ICMSRequest status with cache reference, will requeue")
			return reconcile.Result{Requeue: true}, nil
		}
	}

	// The NVMesh, shared-FS, and Samba backends all populate via a
	// ModelCacheRequest handled by the storage controller (see
	// doStorageRequests / makeStorageRequests), mounted through the
	// ModelCacheRequest ROPVCName annotation. Only the ephemeral backend is
	// handled here: inject a per-pod model-cache-init init container via the
	// webhook (no shared cache backend available).
	if cacheLaunchRequested(icmsReq) && cacheBackend == nvcastorage.HelmCacheBackendEphemeral {
		initEnv, initImage, err := ephemeralModelCacheInitEnv(ms, icmsReq)
		if err != nil {
			return reconcile.Result{}, err
		}
		metaInput.PodAnnotations[nvcastorage.WebhookEphemeralModelCacheInitImageAnnotationKey] = initImage
		metaInput.ModelCacheInitEnv = initEnv
	}

	// BYOO-specific mutators.
	if needsBYOO {
		var err error
		byooEnvs, err := newWorkloadTelemetriesEnvVars(byooLaunchSpec, byooSvc)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("set telemetries object mutators: %w", err)
		}
		// Apply BYOO telemetry annotations to workload objects for Helm-rendered pods
		metaInput.EnvVars = append(metaInput.EnvVars, byooEnvs...)
		collectorEnvs := r.cfg.Agent.BYOOOTelCollectorEnvVars()
		metaInput.OTelCollectorEnvVars = append(metaInput.OTelCollectorEnvVars, collectorEnvs...)
		// The webhook special-cases Helm utils pods and skips metadata-carried collector
		// env injection, so inject the envs directly into the BYOO collector sidecar.
		k8sutil.AddBYOOEnvVarsToPodSpec(&utilsPod.Spec, collectorEnvs)
	}

	// Task-specific mutators.
	if taskLaunchSpec != nil {
		// Enforce termination grace period on workload pods.
		var termGPDuration time.Duration
		if taskLaunchSpec.TerminationGracePeriodDuration != "" {
			dur, err := translateutil.ParseISO8601Duration(taskLaunchSpec.TerminationGracePeriodDuration)
			if err != nil {
				return reconcile.Result{}, reconcile.TerminalError(err)
			}
			termGPDuration = dur
		}
		metaInput.TerminationGracePeriodSeconds = new(int64)
		*metaInput.TerminationGracePeriodSeconds = int64(termGPDuration.Seconds())

		// Task storage PVC mutation for all workload pods and utils.
		utilsPod.Annotations[nvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey] =
			nvcastorage.SharedStorageTaskDataReadWritePVCName
		metaInput.PodAnnotations[nvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey] =
			nvcastorage.SharedStorageTaskDataReadWritePVCName

		// Utils pod poll progress configuration.
		setUtilsHelmTaskPollProgressEnv(log, utilsPod, taskLaunchSpec.MaxRuntimeDuration)
	}

	// Utils and init container resource limits are toggled by feature flag.
	setResourceLimits := (taskLaunchSpec != nil && r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceHelmTaskResourceLimits)) ||
		(funcLaunchSpec != nil && r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceHelmFunctionResourceLimits))
	k8sutil.SetNVCFInfraContainerResources(corev1.ResourceList(r.cfg.Agent.UtilsResources), utilsPod, setResourceLimits)
	if err := k8sutil.ValidateAllContainerResourcesSet(utilsPod); err != nil {
		log.Error(err, "Helm utils pod resources are invalid")
		return reconcile.Result{}, reconcile.TerminalError(err)
	}

	infraObjs = append(infraObjs, utilsPod)

	if r.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrNVLinkOptimized) {
		infraObjs = append(infraObjs, nvcfdra.NewSingleChannelComputeDomain())
	}

	// Create the miniservice metadata ConfigMap before any objects are created
	// in the namespace. The mutating webhook reads this ConfigMap at admission
	// time to inject NVCF metadata into all admitted objects.
	if err := r.ensureMiniserviceMetadataConfigMap(ctx, ms, icmsReq, metaInput); err != nil {
		return reconcile.Result{}, err
	}

	err = r.applyInfra(ctx, ms, infraObjectMutators, genericInfraMutator, infraObjs...)
	if err != nil {
		return reconcile.Result{}, err
	}

	// The utils pod has several init containers that may write files needed by workload pods,
	// like secrets.json. These init containers should complete before applying workload objects.
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(utilsPod), utilsPod); err == nil {
		// Ensure pod is in a healthy state so an initialized but errored pod
		// does not result in workload apply.
		schedulingTimeout := getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig)
		if status := getPodStatus(utilsPod, r.K8sTimeConfig, schedulingTimeout); status.TerminalBad {
			return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("utils pod is %s: %s", status.Status, status.Reason))
		}
		if !isInitContainersComplete(utilsPod) {
			log.V(1).Info("Utils pod init containers have not completed, waiting for initialization event")
			return reconcile.Result{}, nil
		}
		log.V(1).Info("Utils pod init containers have completed, proceeding with install")
	} else if apierrors.IsNotFound(err) {
		// Utils pod not found yet - may still be creating, wait for it.
		log.V(1).Info("Utils pod not found yet, waiting for creation")
		return reconcile.Result{Requeue: true}, nil
	} else if k8sutil.IsTransientK8sError(err) {
		log.V(1).Info("Transient error getting utils pod, will retry", "error", err)
		return reconcile.Result{Requeue: true}, nil
	} else {
		log.Error(err, "Non-transient error getting utils pod")
		return reconcile.Result{}, err
	}

	err = r.applyWorkload(ctx, ms, genericWorkloadMutator, workloadObjs...)
	if err != nil {
		return reconcile.Result{}, err
	}

	if err := r.saveRevisionHistory(ctx, ms); err != nil {
		log.Error(err, "Failed to save revision history after initial install")
		return reconcile.Result{}, err
	}

	ms.Status.Phase = v1alpha1.MiniServiceInstalled
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
		Status: metav1.ConditionTrue,
		Reason: "AllObjectsApplied",
	})

	return reconcile.Result{}, nil
}

// needsWorkloadUpdate checks if a workload update is needed for ms.
// It returns true if ms is in the Installing phase and has a revision greater than 0,
// which can only be true if the controller has explicitly transitioned ms to
// the Installing phase post-Running.
func needsWorkloadUpdate(ms *v1alpha1.MiniService) bool {
	return ms.Status.Phase == v1alpha1.MiniServiceInstalling &&
		ms.Status.Revision > 0
}

func (r *Reconciler) prepareUpdateWorkload(ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) ([]client.Object, []v1alpha1.ResourceStatus, string, string, error) {
	log := logf.FromContext(ctx)

	log.Info("Preparing MiniService workload update", "revision", ms.Status.Revision)

	funcLaunchSpec := icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification
	taskLaunchSpec := icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification
	if funcLaunchSpec == nil && taskLaunchSpec == nil {
		return nil, nil, "", "", reconcile.TerminalError(
			fmt.Errorf("both function and task launch specs are empty in ICMSRequest %s", icmsReq.Name))
	}

	functionName, taskName, err := getFunctionNameAndTaskName(funcLaunchSpec, taskLaunchSpec)
	if err != nil {
		return nil, nil, "", "", reconcile.TerminalError(fmt.Errorf("failed to get function name and task name: %w", err))
	}

	objsData, isRendered, err := r.getRenderedData(ctx, ms)
	if err != nil {
		return nil, nil, "", "", err
	}

	if !isRendered {
		// To avoid unnecessary re-renders, cache failed render attempts by helm values hash.
		// (which is not prone to races like revision is).
		helmValuesHash := sha256.Sum256(ms.Spec.HelmChartConfig.Values)
		helmValuesHashShort := hex.EncodeToString(helmValuesHash[:])[:8]
		failedWorkloadUpdateRevisionCacheKey := fmt.Sprintf("%s-%s", ms.Name, helmValuesHashShort)

		r.failedWorkloadUpdateRevisionCacheLock.RLock()
		failed := r.failedWorkloadUpdateRevisionCache[failedWorkloadUpdateRevisionCacheKey]
		r.failedWorkloadUpdateRevisionCacheLock.RUnlock()
		if failed != nil {
			return nil, nil, "", "", failed
		}

		if objsData, err = r.render(ctx, ms, icmsReq); err != nil {
			r.failedWorkloadUpdateRevisionCacheLock.Lock()
			r.failedWorkloadUpdateRevisionCache[failedWorkloadUpdateRevisionCacheKey] = err
			r.failedWorkloadUpdateRevisionCacheLock.Unlock()
			return nil, nil, "", "", err
		}

		// Clear the cache on a successful render for prior revisions to this MiniService
		// so entries don't accumulate indefinitely.
		r.failedWorkloadUpdateRevisionCacheLock.Lock()
		maps.DeleteFunc(r.failedWorkloadUpdateRevisionCache, func(k string, _ error) bool {
			return strings.HasPrefix(k, ms.Name+"-")
		})
		r.failedWorkloadUpdateRevisionCacheLock.Unlock()

		if err := r.saveRenderedData(ctx, ms, objsData); err != nil {
			return nil, nil, "", "", err
		}
	}

	workloadObjs, resources, _, err := decodeObjects(ctx, r.Decoder, objsData)
	if err != nil {
		return nil, nil, "", "", err
	}

	return workloadObjs, resources, functionName, taskName, nil
}

// doUpdateWorkload performs a workload update for a MiniService, doing almost the same operations as doInstall,
// but without infra install steps. It assumes that the MiniService is already in the Running phase,
// but a recent change to Helm values has been detected and MiniService transitioned to the Installing phase.
func (r *Reconciler) doUpdateWorkload(ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	workloadObjs, resources, functionName, taskName, err := r.prepareUpdateWorkload(ctx, ms, icmsReq)
	if err != nil {
		// Workload preparation may result in terminal errors that would cause workload cleanup.
		// To let the un-updated workload continue running, warn the user with a non-terminal error.
		if isTerminal(err) {
			err = unwrapTerminalError(err)
			log.Error(err, "Failed to prepare update workload with terminal error. MiniService must be updated with new values to progress update")
		}
		return reconcile.Result{}, err
	}

	log.Info("Updating MiniService workload", "revision", ms.Status.Revision)

	// Update the resources status in the MiniService status.
	updateResourcesStatus(ms, resources)

	// Only add labels and annotations to workload objects. The pod webhook will handle applying metadata to pods.
	genericWorkloadMutator := newGenericMutator(r.FeatureFlagFetcher, ms, icmsReq,
		r.ClusterRegion, r.ClusterName, functionName, taskName, false)

	// The utils pod has several init containers that may write files needed by workload pods,
	// like secrets.json. These init containers should complete before updating workload objects.
	utilsPod := &corev1.Pod{}
	utilsPodKey := client.ObjectKey{Namespace: ms.Spec.Namespace, Name: common.UtilsPodName}
	switch err := r.Client.Get(ctx, utilsPodKey, utilsPod); {
	case err == nil:
		// Ensure pod is in a healthy state so an initialized but errored pod
		// does not result in workload apply.
		schedulingTimeout := getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig)
		if status := getPodStatus(utilsPod, r.K8sTimeConfig, schedulingTimeout); status.TerminalBad {
			return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("utils pod is %s: %s", status.Status, status.Reason))
		}
		if !isInitContainersComplete(utilsPod) {
			log.V(1).Info("Utils pod init containers have not completed, waiting for initialization event to update workload")
			return reconcile.Result{}, nil
		}
		log.V(1).Info("Utils pod init containers have completed, proceeding with workload update")
	case apierrors.IsNotFound(err):
		// The utils pod must exist at this point in the instance's lifecycle,
		// so NotFound errors are considered non-transient.
		log.Error(err, "Utils pod expected to be found but was not during workload update")
		return reconcile.Result{}, reconcile.TerminalError(err)
	case k8sutil.IsTransientK8sError(err):
		log.V(1).Info("Transient error getting utils pod, will retry", "error", err)
		return reconcile.Result{Requeue: true}, nil
	default:
		log.Error(err, "Non-transient error getting utils pod during workload update")
		return reconcile.Result{}, err
	}

	if err := r.applySSAWorkload(ctx, ms, genericWorkloadMutator, workloadObjs...); err != nil {
		if isTerminal(err) {
			err = unwrapTerminalError(err)
			log.Error(err, "Failed to apply workload objects with terminal error. MiniService must be updated with new values to progress update; "+
				"successfully applied objects prior to this error may need to be cleaned up manually")
		}
		return reconcile.Result{}, err
	}

	if err := r.saveRevisionHistory(ctx, ms); err != nil {
		log.Error(err, "Failed to save revision history after workload update")
		return reconcile.Result{}, err
	}

	ms.Status.Phase = v1alpha1.MiniServiceInstalled
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:    v1alpha1.MiniServiceConditionInstallSuccessful,
		Status:  metav1.ConditionTrue,
		Reason:  "WorkloadObjectsUpdated",
		Message: fmt.Sprintf("Workload update to revision %d completed successfully", ms.Status.Revision),
	})

	return reconcile.Result{}, nil
}

func (r *Reconciler) ensureInstanceNamespace(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) error {
	namespaceName := ms.Spec.Namespace

	log := logf.FromContext(ctx).WithValues("namespace", namespaceName)

	namespace := &corev1.Namespace{}
	namespace.Name = namespaceName
	namespace.Labels = map[string]string{
		nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValueMiniService,
		miniserviceNameLabel:                ms.Name,
	}
	namespace.Annotations = map[string]string{}

	maps.Copy(namespace.Labels, r.InstanceNamespaceLabels)

	icmsReqLabels := nvcatypes.GetLabelsForRequest(icmsReq, r.FeatureFlagFetcher)
	maps.Copy(namespace.Labels, icmsReqLabels)

	icmsReqAnnotations := nvcatypes.GetAnnotationsForRequest(icmsReq)
	maps.Copy(namespace.Annotations, icmsReqAnnotations)

	needsEnforcement := !r.enabledAttrs.Empty()
	if needsEnforcement {
		enforce.SetMetadata(namespace, r.enabledAttrs)
	}

	existingNamespace := &corev1.Namespace{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(namespace), existingNamespace); err == nil {
		if existingNamespace.Status.Phase == corev1.NamespaceTerminating {
			log.V(1).Info("Namespace is terminating, will requeue and wait for deletion to complete", "namespace", namespaceName)
			return nil
		}

		if !mapContainsAll(namespace.Labels, existingNamespace.Labels) ||
			!mapContainsAll(namespace.Annotations, existingNamespace.Annotations) {
			log.Info("Updating namespace with missing metadata")
			if err := r.Client.Update(ctx, namespace); err != nil {
				return err
			}
		}
	} else if apierrors.IsNotFound(err) {
		log.Info("Creating namespace")

		if err := r.Client.Create(ctx, namespace); err != nil {
			return err
		}
	} else {
		return err
	}

	return nil
}

func mapContainsAll(src, tgt map[string]string) bool {
	if src == nil || tgt == nil {
		return false
	}
	for sk, sv := range src {
		if tv, ok := tgt[sk]; !ok || tv != sv {
			return false
		}
	}
	return true
}

func (r *Reconciler) render(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) ([]byte, error) {
	log := logf.FromContext(ctx)

	log.Info("Helm Chart is not rendered, calling ReVal service")

	apiVersions, err := r.getClusterAPIVersions(ctx)
	if err != nil {
		log.Error(err, "Failed to look up API versions, proceeding with an empty list")
	}

	values, err := setInfraValues(ms.Spec.HelmChartConfig.Values, icmsReq)
	if err != nil {
		log.Error(err, "Failed to set infrastructure values")
		return nil, reconcile.TerminalError(err)
	}

	allEnvSet, err := parseWorkloadEnvSet(icmsReq)
	if err != nil {
		log.Error(err, "Failed to decode workload env set")
		return nil, reconcile.TerminalError(err)
	}
	imgRegAuthCfg, _, err := common.DecodeWorkloadImageRegistryAuthConfig(allEnvSet)
	if err != nil {
		log.Error(err, "Failed to decode workload image registry auth config")
		return nil, reconcile.TerminalError(err)
	}

	rvInput := HelmReValRenderInput{
		HelmChartURL:            ms.Spec.HelmChartConfig.URL,
		ReleaseName:             ms.Spec.Namespace,
		HelmChartServicePort:    ms.Spec.HelmChartConfig.ServicePort,
		HelmChartServiceName:    ms.Spec.HelmChartConfig.ServiceName,
		HelmRegistryAuthConfig:  common.RegistryAuthConfig{K8sSecrets: ms.Spec.HelmChartConfig.AuthConfig.K8sSecrets},
		ImageRegistryAuthConfig: imgRegAuthCfg,
		Values:                  values,
		InstanceType:            icmsReq.Spec.CreationMsgInfo.GetInstanceTypeLabelSelValue(),
		GPUName:                 icmsReq.Spec.CreationMsgInfo.GPUType,
		K8sVersion:              r.K8sVersion,
		APIVersions:             apiVersions,
		NCAID:                   icmsReq.Spec.NCAId,
		ValidationPolicy:        r.cfg.Cluster.ValidationPolicy,
	}

	output, err := r.ReValClient.Render(ctx, rvInput)
	if err != nil {
		log.Error(err, "Failed to run ReVal render")
		return nil, err
	}

	if output.Valid == nil || !*output.Valid {
		log.Error(nil, "Helm Chart is invalid")
		msg := ""
		if len(output.ValidationErrors) != 0 {
			msg += fmt.Sprintf("validation errors: %q", output.ValidationErrors)
		}
		if len(output.InternalErrors) != 0 {
			if msg != "" {
				msg += "\n"
			}
			msg += fmt.Sprintf("internal errors: %q", output.InternalErrors)
		}
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionInstallSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonReValResultInvalid,
			Message: msg,
		})
		return nil, reconcile.TerminalError(fmt.Errorf("reval found validation errors"))
	}

	return output.Output, nil
}

func parseWorkloadEnvSet(icmsReq *nvcav2beta1.ICMSRequest) (map[string]string, error) {
	var envB64 string
	if icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil {
		envB64 = icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64
	} else {
		envB64 = icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification.EnvironmentB64
	}
	return common.DecodeEnvironmentB64(envB64, common.EnvDecoderText)
}

func findWellKnownObjects(objs []client.Object) (
	utilsPod *corev1.Pod,
	workerPullSecrets, workloadPullSecrets []*corev1.Secret,
	cacheInitJob *batchv1.Job,
	cacheInitPVC *corev1.PersistentVolumeClaim,
	byooSvc *corev1.Service,
	objsToCreate []client.Object,
	err error,
) {
	// Get the artifacts and perform type checks.
	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.Pod:
			if k8sutil.IsUtilsPod(t) {
				utilsPod = t
				continue
			}
		case *corev1.PersistentVolumeClaim:
			if strings.HasPrefix(t.Name, "rw-pvc-") {
				cacheInitPVC = t
				continue
			}
		case *batchv1.Job:
			if strings.HasPrefix(t.Name, "writer-job-") {
				cacheInitJob = t
				continue
			}
		case *corev1.Secret:
			if k8sutil.IsNVCFWorkerImagePullSecretObject(t) {
				workerPullSecrets = append(workerPullSecrets, t)
				continue
			} else if k8sutil.IsNVCFWorkloadImagePullSecretObject(t) {
				workloadPullSecrets = append(workloadPullSecrets, t)
				continue
			}
		case *corev1.Service:
			if t.Name == common.ByooOTelCollectorPodNameBase {
				byooSvc = t
			}
		}
		objsToCreate = append(objsToCreate, obj)
	}

	var missingObjs []string
	if utilsPod == nil {
		missingObjs = append(missingObjs, "utilsPod")
	}
	if len(workerPullSecrets) == 0 {
		missingObjs = append(missingObjs, "workerPullSecrets")
	}
	if len(workloadPullSecrets) == 0 {
		missingObjs = append(missingObjs, "workloadPullSecrets")
	}
	if len(missingObjs) != 0 {
		err = reconcile.TerminalError(fmt.Errorf("missing required objects: %q", missingObjs))
	}

	//nolint:nakedret
	return
}

func (r *Reconciler) doCleanup(ctx context.Context, //nolint:gocyclo
	ms *v1alpha1.MiniService,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	// Task pods may need to be deleted forcefully if the utils pod is not found or has exited non-zero.
	var taskPodsToDelete []*corev1.Pod
	var forceDeleteTaskPods bool
	if isTaskObject(ms) {
		podList := &corev1.PodList{}
		if err := r.Client.List(ctx, podList, client.InNamespace(ms.Spec.Namespace)); err != nil {
			log.Error(err, "Failed to list task pods")
			return reconcile.Result{}, err
		}
		taskPodsToDelete, forceDeleteTaskPods = getTaskPodsToDelete(ms, podList)
	}

	// List objects for deletion and delete before the namespace.
	var objs []client.Object
	if objsData, isRendered, err := r.getRenderedData(ctx, ms); err != nil || !isRendered {
		if err != nil {
			log.V(1).Error(err, "Failed to get rendered object data, falling back on list in cleanup")
		} else {
			log.V(1).Info("Rendered object data not cached, falling back on list in cleanup")
		}
		gvks := knownGKVs.UnsortedList()
		if r.cfg.Cluster.ValidationPolicy != nil {
			for _, gvk := range r.cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes {
				gvks = append(gvks, schema.GroupVersionKind{
					Group:   gvk.Group,
					Version: gvk.Version,
					Kind:    gvk.Kind,
				})
			}
		}
		for _, gvk := range gvks {
			lobjs, err := r.list(ctx, ms, gvk)
			if err != nil {
				return reconcile.Result{}, err
			}
			for i := 0; i < len(lobjs); i++ {
				if nvcatypes.IsInfraOwnedObject(lobjs[i]) {
					lobjs = slices.Delete(lobjs, i, i+1)
					i--
				} else if t, ok := lobjs[i].(*corev1.Pod); ok && (t.Name == nvcastorage.SMBServerPodName || k8sutil.IsUtilsPod(t)) {
					lobjs = slices.Delete(lobjs, i, i+1)
					i--
				}
			}
			objs = append(objs, lobjs...)
		}
	} else {
		log.V(1).Info("Using cached rendered object data in cleanup")

		if objs, _, _, err = decodeObjects(ctx, r.Decoder, objsData); err != nil {
			return reconcile.Result{}, err
		}
		for i := 0; i < len(objs); i++ {
			obj := objs[i]
			if isNamespaced, err := r.Client.IsObjectNamespaced(obj); isNamespaced {
				obj.SetNamespace(ms.Spec.Namespace)
			} else if err != nil {
				log.Error(err, "Failed to check if object is namespaced")
				return reconcile.Result{}, err
			}
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), objs[i]); apierrors.IsNotFound(err) {
				objs = slices.Delete(objs, i, i+1)
				i--
			} else if err != nil {
				log.Error(err, "Failed to get object for deletion")
				return reconcile.Result{}, err
			}
		}
	}

	var objectsPendingDeletion []client.Object
	for _, obj := range objs {
		if obj.GetDeletionTimestamp() == nil {
			pp := client.PropagationPolicy(metav1.DeletePropagationForeground)
			if err := r.Client.Delete(ctx, obj, pp); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Delete object on miniservice deletion")

				objectsPendingDeletion = append(objectsPendingDeletion, obj)
				continue
			}
		} else {
			objectsPendingDeletion = append(objectsPendingDeletion, obj)
		}
	}

	for _, pod := range taskPodsToDelete {
		var deleteOpts []client.DeleteOption
		if forceDeleteTaskPods {
			log.V(1).Info("Deleting pod with 0 termination grace period", "pod", pod.Name)
			deleteOpts = append(deleteOpts, client.GracePeriodSeconds(0))
		} else {
			log.V(1).Info("Deleting pod with existing termination grace period", "pod", pod.Name)
		}
		if err := r.Client.Delete(ctx, pod, deleteOpts...); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			log.Error(err, "Failed to delete task pod")
		}
		objectsPendingDeletion = append(objectsPendingDeletion, pod)
	}

	// The utils pod will have some termination grace period set for whichever workload it monitors.
	// This will determine how long to allow workload objects to delete before infra is deleted.
	var termGP time.Duration
	utilsPod := &corev1.Pod{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: common.UtilsPodName}, utilsPod); err == nil &&
		utilsPod.Spec.TerminationGracePeriodSeconds != nil {
		termGP = time.Duration(*utilsPod.Spec.TerminationGracePeriodSeconds) * time.Second
	}

	// Wait for workload objects to terminate before terminating infra objects unless a grace period is reached.
	cleanupCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionCleanupSuccessful)
	if len(objectsPendingDeletion) != 0 &&
		(cleanupCond == nil ||
			(cleanupCond.Status == metav1.ConditionFalse && cleanupCond.LastTransitionTime.Add(termGP).After(r.now()))) {
		log.V(1).Info("Waiting on pending workload objects to delete")

		msgb := &strings.Builder{}
		for _, obj := range objectsPendingDeletion {
			gvk := r.getObjectGVKOrUnknown(ctx, obj)
			fmt.Fprintf(msgb, "%s, %s;", gvk.String(), obj.GetName())
		}
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  "SomeObjectsPendingDeletion",
			Message: fmt.Sprintf("Objects: %q", msgb.String()),
		})
		return reconcile.Result{}, nil
	}

	// Force terminate remaining objects.
	delOpts := []client.DeleteOption{
		client.PropagationPolicy(metav1.DeletePropagationForeground),
		client.GracePeriodSeconds(0),
	}
	for _, obj := range objs {
		if err := r.Client.Delete(ctx, obj, delOpts...); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete object forcefully on miniservice deletion")
		}
	}

	log.V(1).Info("Deleting namespace")

	// Clean up the namespace created for the miniservice to clean up remaining objects.
	ns := &corev1.Namespace{}
	ns.Name = ms.Spec.Namespace
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(ns), ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("get miniservice %s namespace: %w",
				ms.Name, err)
		}
	} else if ns.DeletionTimestamp == nil {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, ns)); err != nil {
			return reconcile.Result{}, fmt.Errorf("delete miniservice %s namespace: %w",
				ms.Name, err)
		}
		log.Info("Miniservice namespace terminated, waiting for deletion", "namespace", ms.Spec.Namespace)
	}

	// Clean up function from cache after namespace termination to prevent resource leakage.
	if err := r.chartCache.Delete(getCacheKey(ms)); err != nil {
		return reconcile.Result{}, err
	}

	stList := &nvcav2beta1.StorageRequestList{}
	if err := r.Client.List(ctx, stList, client.InNamespace(ms.Spec.Namespace)); err != nil {
		log.Error(err, "Failed to list storage requests for miniservice cleanup, proceeding with cleanup")
	}
	if len(stList.Items) != 0 {
		stNames := make([]string, 0, len(stList.Items))
		for _, st := range stList.Items {
			stNames = append(stNames, st.Name)
		}

		// Use namespace termination timeout to stop reconciling on terminating storage requests.
		if _, nsStuck := k8sutil.IsNamespaceStuckTerminating(ns, r.K8sTimeConfig); !nsStuck {
			log.V(1).Info("Waiting for storage requests to terminate before removing finalizer")
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionCleanupSuccessful,
				Status:  metav1.ConditionFalse,
				Reason:  "SomeObjectsPendingDeletion",
				Message: fmt.Sprintf("StorageRequests: %s", strings.Join(stNames, ", ")),
			})
			return reconcile.Result{}, nil
		}

		log.Info("Storage request deletion timed out, removing finalizer")
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  "ObjectDeletionTimeout",
			Message: fmt.Sprintf("StorageRequests: %s", strings.Join(stNames, ", ")),
		})
	} else {
		log.Info("All objects deleted, removing finalizer")
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:   v1alpha1.MiniServiceConditionCleanupSuccessful,
			Status: metav1.ConditionTrue,
			Reason: "AllObjectsDeleted",
		})
	}

	log.Info("Removing finalizer")

	controllerutil.RemoveFinalizer(ms, finalizer)

	return reconcile.Result{}, nil
}

// getTaskPodsToDelete returns pods from podList that need to be deleted.
// If the task needs explicit cleanup, ex. it is in a failed state,
// force will be true.
func getTaskPodsToDelete(ms *v1alpha1.MiniService, podList *corev1.PodList) (taskPodsToDelete []*corev1.Pod, force bool) {
	var utilsPod *corev1.Pod
	for _, pod := range podList.Items {
		if pod.Name == nvcastorage.SMBServerPodName {
			continue
		}
		if k8sutil.IsUtilsPod(&pod) {
			utilsPod = &pod
			continue
		}
		taskPodsToDelete = append(taskPodsToDelete, &pod)
	}
	// If utils is present and has exited successfully, do not terminate forcefully.
	if utilsPod != nil && isTaskUtilsContainerExitedSuccessfully(utilsPod) {
		return taskPodsToDelete, false
	}
	// If cleanup has not occurred, consider utils pod untouched by cleanup logic and act on its state.
	cleanupCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionCleanupSuccessful)
	if cleanupCond == nil &&
		(utilsPod == nil || // Utils pod not found (deleted or some error occur)
			isTaskUtilsContainerExited(utilsPod)) { // Non-zero utils exit code
		return taskPodsToDelete, true
	}
	healthyCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy)
	if healthyCond == nil || // Status has not yet run, assume install failed or early termination
		healthyCond.Status == metav1.ConditionFalse { // Unhealthy miniservice
		return taskPodsToDelete, true
	}
	return taskPodsToDelete, false
}

func getCacheKey(ms *v1alpha1.MiniService) chartcache.ChartCacheInput {
	return chartcache.ChartCacheInput{
		HelmChartURL:         ms.Spec.HelmChartConfig.URL,
		HelmChartServicePort: ms.Spec.HelmChartConfig.ServicePort,
		HelmChartServiceName: ms.Spec.HelmChartConfig.ServiceName,
		Values:               ms.Spec.HelmChartConfig.Values,
		APIVersions:          nil, // Not used right now.
		// Namespace must be included in cache key because Helm templates using
		// .Release.Namespace render namespace-specific values (e.g., service URLs).
		// Without this, cached output from namespace A is incorrectly returned for B.
		Namespace: ms.Spec.Namespace,
	}
}

func (r *Reconciler) applyInfra(ctx context.Context,
	ms *v1alpha1.MiniService,
	objectMutators objectMutatorSet,
	genericMutator objectMutator,
	objs ...client.Object,
) error {
	return r.create(ctx, ms, objectMutators, genericMutator, r.Client, objs...)
}

// applyWorkload is used to create or update workload (Helm chart) objects.
// It may use a client impersonating the service account created for the miniservice
// to enforce RBAC.
func (r *Reconciler) applyWorkload(ctx context.Context,
	ms *v1alpha1.MiniService,
	genericMutator objectMutator,
	objs ...client.Object,
) (err error) {
	var c client.Client
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmRBACEnforcement) {
		if c, err = r.newImpersonatingClient(ms.Spec.Namespace); err != nil {
			return fmt.Errorf("create impersonating client for workload objects: %w", err)
		}
	} else {
		c = r.Client
	}
	return r.create(ctx, ms, objectMutatorSet{}, genericMutator, c, objs...)
}

// applySSAWorkload applies workload objects using server-side apply for updates.
// Like applyWorkload, it may use an impersonating client for RBAC enforcement.
func (r *Reconciler) applySSAWorkload(ctx context.Context,
	ms *v1alpha1.MiniService,
	genericMutator objectMutator,
	objs ...client.Object,
) (err error) {
	var c client.Client
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmRBACEnforcement) {
		if c, err = r.newImpersonatingClient(ms.Spec.Namespace); err != nil {
			return fmt.Errorf("create impersonating client for workload objects: %w", err)
		}
	} else {
		c = r.Client
	}
	return r.applySSA(ctx, ms, objectMutatorSet{}, genericMutator, c, objs...)
}

// applySSA uses server-side apply to create or update objects.
// Unlike create(), which skips existing objects, applySSA applies desired state
// to both new and existing objects, making it suitable for helm values updates.
func (r *Reconciler) applySSA(ctx context.Context,
	ms *v1alpha1.MiniService,
	objectMutators objectMutatorSet,
	genericMutator objectMutator,
	c client.Client,
	objs ...client.Object,
) error {
	log := logf.FromContext(ctx)

	sort.SliceStable(objs, func(i, j int) bool {
		return weighObject(objs[i]) < weighObject(objs[j])
	})

	caniCache := map[schema.GroupVersionKind]error{}
	checkPermissions := r.newPermissionsChecker(caniCache, requiredRBACVerbsWrite)

	rm := c.RESTMapper()
	for _, obj := range objs {
		gvk, err := r.getObjectGVK(ctx, obj)
		if err != nil {
			return reconcile.TerminalError(err)
		}

		if isNamespaced, err := apiutil.IsGVKNamespaced(gvk, rm); isNamespaced {
			obj.SetNamespace(ms.Spec.Namespace)
		} else if err != nil {
			log.Error(err, "Failed to check if object is namespaced")
			return reconcile.TerminalError(err)
		}

		if err := checkPermissions(ctx, c, gvk, obj.GetNamespace()); err != nil {
			return err
		}

		mutators, ok := objectMutators[gvk]
		if !ok && genericMutator != nil {
			mutators = []objectMutator{genericMutator}
		}
		for _, mutator := range mutators {
			if err := mutator.mutate(ctx, obj); err != nil {
				return reconcile.TerminalError(err)
			}
		}
		if gvk == storageRequestGVK {
			if err := controllerutil.SetControllerReference(ms, obj, c.Scheme()); err != nil {
				return reconcile.TerminalError(err)
			}
		}

		obj.SetManagedFields(nil)
		obj.SetResourceVersion("")
		obj.GetObjectKind().SetGroupVersionKind(gvk)

		if err := c.Patch(ctx, obj, client.Apply, client.FieldOwner(managedByValue), client.ForceOwnership); err != nil {
			if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
				err = reconcile.TerminalError(err)
			}
			return err
		}
		log.V(1).Info("Applied object via Server-Side Apply", "gvk", gvk, "name", obj.GetName())
	}

	return nil
}

func (r *Reconciler) create(ctx context.Context,
	ms *v1alpha1.MiniService,
	objectMutators objectMutatorSet,
	genericMutator objectMutator,
	c client.Client,
	objs ...client.Object,
) error {
	log := logf.FromContext(ctx)

	sort.SliceStable(objs, func(i, j int) bool {
		return weighObject(objs[i]) < weighObject(objs[j])
	})

	// Cache permissions errors per GVK to reduce requests to the API server.
	caniCache := map[schema.GroupVersionKind]error{}
	checkPermissions := r.newPermissionsChecker(caniCache, requiredRBACVerbsWrite)

	rm := c.RESTMapper()
	for _, obj := range objs {
		gvk, err := r.getObjectGVK(ctx, obj)
		if err != nil {
			return reconcile.TerminalError(err)
		}

		if isNamespaced, err := apiutil.IsGVKNamespaced(gvk, rm); isNamespaced {
			obj.SetNamespace(ms.Spec.Namespace)
		} else if err != nil {
			log.Error(err, "Failed to check if object is namespaced")
			return reconcile.TerminalError(err)
		}

		if err := checkPermissions(ctx, c, gvk, obj.GetNamespace()); err != nil {
			return err
		}

		switch err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); {
		case err == nil:
			continue
		case apierrors.IsNotFound(err):
			mutators, ok := objectMutators[gvk]
			if !ok && genericMutator != nil {
				mutators = []objectMutator{genericMutator}
			}
			for _, mutator := range mutators {
				if err := mutator.mutate(ctx, obj); err != nil {
					return reconcile.TerminalError(err)
				}
			}
			if gvk == storageRequestGVK {
				if err := controllerutil.SetControllerReference(ms, obj, c.Scheme()); err != nil {
					return reconcile.TerminalError(err)
				}
			}
			if err := c.Create(ctx, obj); err != nil {
				if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
					err = reconcile.TerminalError(err)
				}
				return err
			}
		default:
			return err
		}
	}
	return nil
}

var (
	// The list of verbs to check prior to using the caching client for read operations.
	// This list is intentionally not exhaustive; other client methods will return more descriptive errors
	// if the corresponding verb is not permitted for a resource.
	requiredRBACVerbsRead = []string{"get", "list", "watch"}

	// The list of verbs to check prior to using the caching client for write operations.
	requiredRBACVerbsWrite = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
)

type resourceAccesssDeniedError struct {
	gvr schema.GroupVersionResource
}

func (e resourceAccesssDeniedError) Error() string {
	return fmt.Sprintf("access to resource %q apiVersion %q is denied, please check miniservice Role configuration",
		e.gvr.Resource, e.gvr.GroupVersion(),
	)
}

// newSelfSubjectAccessReviewPermissionsChecker verifies controller permissions before caching a GVK,
// preventing unrecoverable cache errors when RBAC is missing.
// It checks permissions with Kubernetes SelfSubjectAccessReview objects.
// See https://github.com/kubernetes-sigs/controller-runtime/issues/550
func newSelfSubjectAccessReviewPermissionsChecker(caniCache map[schema.GroupVersionKind]error, verbs []string) permissionCheckerFunc {
	return func(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, namespace string) error {
		log := logf.FromContext(ctx).WithValues("gvk", gvk.String(), "namespace", namespace)

		if err, ok := caniCache[gvk]; ok {
			return err
		}

		rm, err := c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			log.Error(err, "Failed to get REST mapping for GVK")
			return reconcile.TerminalError(err)
		}

		hasBadVerbs := false
		for _, verb := range verbs {
			ssar := &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: namespace,
						Verb:      verb,
						Group:     rm.Resource.Group,
						Version:   rm.Resource.Version,
						Resource:  rm.Resource.Resource,
					},
				},
			}
			if err := c.Create(ctx, ssar); err != nil {
				log.Error(err, "Failed to create subject access review")
				return err
			}
			if !ssar.Status.Allowed {
				log.Error(fmt.Errorf("access to resource denied"), "Client does not have correct permissions to manage GVK",
					"resource", rm.Resource.Resource, "verb", verb,
					"evaluation_error", ssar.Status.EvaluationError, "reason", ssar.Status.Reason,
				)
				hasBadVerbs = true
			}
		}
		var rerr error
		if hasBadVerbs {
			rerr = reconcile.TerminalError(resourceAccesssDeniedError{gvr: rm.Resource})
		}
		caniCache[gvk] = rerr
		return rerr
	}
}

func (r *Reconciler) list(ctx context.Context, ms *v1alpha1.MiniService, gvk schema.GroupVersionKind) (objs []client.Object, err error) {
	log := logf.FromContext(ctx)

	scheme := r.Client.Scheme()
	var robj runtime.Object
	if scheme.Recognizes(gvk) {
		if robj, err = scheme.New(gvk); err != nil {
			return nil, reconcile.TerminalError(err)
		}
	} else {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		robj = u
	}

	var listOpts []client.ListOption
	if isNamespaced, err := apiutil.IsGVKNamespaced(gvk, r.Client.RESTMapper()); isNamespaced {
		listOpts = append(listOpts, client.InNamespace(ms.Spec.Namespace))
	} else if err != nil {
		log.Error(err, "Failed to check if object is namespaced")
		return nil, err
	} else {
		listOpts = append(listOpts, client.MatchingLabels{
			miniserviceNameLabel: ms.Name,
		})
	}

	listGVK := gvk
	listGVK.Kind += "List"
	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(listGVK)
	if err := r.Client.List(ctx, ul, listOpts...); err != nil {
		return nil, err
	}

	for i, u := range ul.Items {
		// If the original object is an unstructured, use the list item directly.
		var obj client.Object
		if _, ok := robj.(*unstructured.Unstructured); ok {
			obj = &ul.Items[i]
		} else {
			obj = robj.DeepCopyObject().(client.Object)
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), obj)
			if err != nil {
				return nil, err
			}
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		objs = append(objs, obj)
	}

	return objs, nil
}

func weighObject(obj client.Object) int8 {
	switch obj.(type) {
	case *corev1.Service, *corev1.Secret, *corev1.ConfigMap, *corev1.PersistentVolumeClaim:
		return 0
	case *corev1.Pod, *appsv1.Deployment, *appsv1.ReplicaSet, *appsv1.StatefulSet:
		return 2
	default:
		return 1
	}
}

// decodeObjects decodes the ReVal-rendered object data into typed client objects and a
// per-GVK resource summary. The workload config ConfigMap
// (featureflag.WorkloadConfigConfigMapName) is a control signal from the chart and is never
// created on-cluster: it is extracted and returned as a decoded WorkloadConfig, and excluded
// from the returned objects and resource summary so it is never applied or status-checked.
func decodeObjects(ctx context.Context, decoder runtime.Decoder, objsData []byte) (
	[]client.Object, []v1alpha1.ResourceStatus, *v1alpha1.WorkloadConfig, error,
) {
	log := logf.FromContext(ctx)

	var rawObjs []json.RawMessage
	if err := json.Unmarshal(objsData, &rawObjs); err != nil {
		return nil, nil, nil, err
	}
	objs := make([]client.Object, 0, len(rawObjs))
	resourcesByGVK := map[string]v1alpha1.ResourceStatus{}
	var workloadConfig *v1alpha1.WorkloadConfig
	for i, rawObj := range rawObjs {
		robj, _, err := decoder.Decode(rawObj, nil, nil)
		if err != nil {
			log.Error(err, "Error decoding object from ReVal", "index", i)
			return nil, nil, nil, reconcile.TerminalError(err)
		}

		cobj, ok := robj.(client.Object)
		if !ok {
			log.Error(nil, "Object does not implement client.Object", "index", i)
			return nil, nil, nil, reconcile.TerminalError(fmt.Errorf("bad object type"))
		}

		// Extract the workload config ConfigMap. It must never be applied on-cluster,
		// so drop it from the objects to create and status-check.
		if cm, ok := cobj.(*corev1.ConfigMap); ok && cm.Name == featureflag.WorkloadConfigConfigMapName {
			if workloadConfig, err = featureflag.DecodeWorkloadConfig(ctx, cm); err != nil {
				return nil, nil, nil, err
			}
			continue
		}

		objs = append(objs, cobj)
		gvk := cobj.GetObjectKind().GroupVersionKind().String()
		if _, ok := resourcesByGVK[gvk]; ok {
			resource := resourcesByGVK[gvk]
			resource.Names = append(resource.Names, cobj.GetName())
			resource.Count++
			resourcesByGVK[gvk] = resource
		} else {
			resourcesByGVK[gvk] = v1alpha1.ResourceStatus{
				GVK:   gvk,
				Names: []string{cobj.GetName()},
				Count: 1,
			}
		}
	}

	resources := make([]v1alpha1.ResourceStatus, 0, len(resourcesByGVK))
	for _, resource := range resourcesByGVK {
		resources = append(resources, resource)
		log.V(1).Info("Decoded object", "gvk", resource.GVK, "names", resource.Names, "count", resource.Count)
	}

	return objs, resources, workloadConfig, nil
}

// getObjectGVK returns the GVK for an object using the gvkCache from context for better performance.
// This method caches GVK lookups by reflect.Type, reducing allocations for repeated lookups
// of the same object types (e.g., during status checks with many Pods/ReplicaSets).
func (r *Reconciler) getObjectGVK(ctx context.Context, obj client.Object) (schema.GroupVersionKind, error) {
	return getObjectGVK(ctx, r.Client.Scheme(), obj)
}

// getObjectGVKOrUnknown returns the GVK for the object using the cache from context,
// or unknownGVK if it cannot be determined.
func (r *Reconciler) getObjectGVKOrUnknown(ctx context.Context, obj client.Object) schema.GroupVersionKind {
	return getObjectGVKOrUnknown(ctx, r.Client.Scheme(), obj)
}

func (r *Reconciler) getClusterAPIVersions(_ context.Context) ([]string, error) {
	// TODO: use dynamic client to find API versions available to functions.
	return nil, nil
}

func (r *Reconciler) saveRenderedData(ctx context.Context,
	ms *v1alpha1.MiniService,
	data []byte,
) error {
	logf.FromContext(ctx).Info("Saving rendered Helm Chart data")

	h, err := r.chartCache.Put(getCacheKey(ms), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("put cache item: %v", err)
	}

	ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{
		Hash: h,
	}
	return nil
}

func (r *Reconciler) getRenderedData(ctx context.Context,
	ms *v1alpha1.MiniService,
) ([]byte, bool, error) {
	log := logf.FromContext(ctx)

	rd := ms.Status.RenderDetails
	if rd == nil {
		log.V(1).Info("Rendered data not found")
		return nil, false, nil
	}

	// TODO: reuse buffer for efficiency.
	buf := &bytes.Buffer{}
	found, err := r.chartCache.Get(getCacheKey(ms), buf)
	if err == nil && found {
		return buf.Bytes(), true, nil
	}
	return nil, found, err
}

func getFunctionNameAndTaskName(
	fnLaunchSpec *function.LaunchSpecification,
	taskLaunchSpec *task.LaunchSpecification,
) (functionName string, taskName string, err error) {
	if fnLaunchSpec != nil {
		fn, err := common.GetEncodedVarByKey(
			fnLaunchSpec.EnvironmentB64,
			common.FunctionNameEncodedEnvKey)
		if err != nil {
			return "", "", fmt.Errorf("failed to get function name: %w", err)
		}
		return fn, "", nil
	}
	if taskLaunchSpec != nil {
		tn, err := common.GetEncodedVarByKey(
			taskLaunchSpec.EnvironmentB64,
			common.TaskNameEncodedEnvKey)
		if err != nil {
			return "", "", fmt.Errorf("failed to get task name: %w", err)
		}
		return "", tn, nil
	}
	return "", "", nil
}

// dedupeWorkloadImagePullSecrets removes 3rd party registry secrets not rendered by ReVal
// if any were rendered, so that the MiniService can use the reduced set of secrets needed
// to pull images.
func dedupeWorkloadImagePullSecrets( //nolint:gocyclo
	workloadObjs []client.Object,
	imagePullSecrets []*corev1.Secret,
) (filtered []*corev1.Secret, err error) {
	filteredNames := sets.New[string]()
	for _, obj := range workloadObjs {
		// Specifically target 3rd party registry secrets.
		if t, ok := obj.(*corev1.Secret); ok &&
			t.Type == corev1.SecretTypeDockerConfigJson && strings.HasPrefix(t.Name, "workload-") {
			filteredNames.Insert(t.Name)
		}
	}
	if len(filteredNames) != 0 {
		for _, secret := range imagePullSecrets {
			if strings.HasPrefix(secret.Name, "workload-") && !filteredNames.Has(secret.Name) {
				continue
			}
			filtered = append(filtered, secret)
		}
		return filtered, nil
	}

	// Fall back to filtering secrets by image tag.
	imageTagSet := sets.New[string]()
	for _, obj := range workloadObjs {
		switch t := obj.(type) {
		case *corev1.Pod:
			collectImages(imageTagSet, t.Spec)
		case *appsv1.Deployment:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *appsv1.ReplicaSet:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *appsv1.StatefulSet:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *batchv1.CronJob:
			collectImages(imageTagSet, t.Spec.JobTemplate.Spec.Template.Spec)
		case *batchv1.Job:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		}
	}

	var errs []error
	imageRegSrvs := sets.New[string]()
	for _, image := range imageTagSet.UnsortedList() {
		if image == "" {
			continue
		}
		regStr, err := parseImageToRegistry(image)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		imageRegSrvs.Insert(regStr)
	}

	if len(errs) != 0 {
		return nil, errors.Join(errs...)
	}

	if len(imageRegSrvs) == 0 {
		return imagePullSecrets, nil
	}

	imagePullSecretIdxs := sets.New[int]()
	for i, secret := range imagePullSecrets {
		if secret.Data == nil && secret.StringData == nil {
			continue
		}
		data := secret.Data[corev1.DockerConfigJsonKey]
		if len(data) == 0 {
			if data = []byte(secret.StringData[corev1.DockerConfigJsonKey]); len(data) == 0 {
				continue
			}
		}
		dbuf := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
		if _, err := base64.StdEncoding.Decode(dbuf, data); err == nil {
			data = dbuf
		}
		auths := common.RegistryAuthSecret{}
		if err := json.Unmarshal(data, &auths); err != nil {
			return nil, err
		}
		for reg := range auths.Auths {
			if imageRegSrvs.Has(reg) {
				imagePullSecretIdxs.Insert(i)
				break
			}
		}
	}

	if len(imagePullSecretIdxs) == 0 {
		return imagePullSecrets, nil
	}

	idxs := imagePullSecretIdxs.UnsortedList()
	sort.Ints(idxs)
	for _, i := range idxs {
		filtered = append(filtered, imagePullSecrets[i])
	}
	return filtered, nil
}

func collectImages(set sets.Set[string], podSpec corev1.PodSpec) {
	for _, c := range append(podSpec.InitContainers, podSpec.Containers...) {
		if img := strings.TrimSpace(c.Image); img != "" {
			set.Insert(c.Image)
		}
	}
}

func parseImageToRegistry(image string) (string, error) {
	const (
		defaultRegistryServer = "https://index.docker.io/v1/"
		defaultRegistryAlias  = "docker.io"
	)
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	regStr := ref.Context().Registry.RegistryStr() //nolint:staticcheck
	if regStr == name.DefaultRegistry || regStr == defaultRegistryAlias {
		return defaultRegistryServer, nil
	}
	return regStr, nil
}

func updateResourcesStatus(ms *v1alpha1.MiniService, resources []v1alpha1.ResourceStatus) {
	if ms.Status.RenderDetails == nil {
		ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{}
	}
	ms.Status.RenderDetails.Resources = resources
}

// cacheLaunchRequested reports whether the request asks for model caching
// (a CacheLaunchSpecification with a positive size).
func cacheLaunchRequested(icmsReq *nvcav2beta1.ICMSRequest) bool {
	if icmsReq == nil {
		return false
	}
	var cls *common.CacheLaunchSpecification
	switch {
	case icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil:
		cls = icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification
	case icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification != nil:
		cls = icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification.CacheLaunchSpecification
	default:
		return false
	}
	return cls != nil && cls.CacheSize > 0
}

// ephemeralModelCacheInitEnv materializes the launch env (plus INSTANCE_ID)
// consumed by the ephemeral model-cache-init container and returns it with
// the init image. The env travels to the webhook inside the miniservice
// metadata ConfigMap (MiniserviceMetadata.ModelCacheInitEnv) instead of a
// dedicated per-instance ConfigMap.
func ephemeralModelCacheInitEnv(
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (map[string]string, string, error) {
	envSet, err := parseWorkloadEnvSet(icmsReq)
	if err != nil {
		return nil, "", fmt.Errorf("decode launch env for model cache init: %w", err)
	}

	initImage := envSet[common.InitImageEnv]
	if initImage == "" {
		return nil, "", fmt.Errorf("missing %s in launch environment", common.InitImageEnv)
	}

	data := maps.Clone(envSet)
	if data == nil {
		data = map[string]string{}
	}
	data["INSTANCE_ID"] = ms.Name

	return data, initImage, nil
}
