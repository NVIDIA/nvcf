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
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcainternaltranslate "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/translate"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// statusContext holds pre-loaded list results for a single reconcile cycle to avoid redundant API calls.
// Pods, ReplicaSets, and Events are loaded once at the start of doStatus() and embedded in context.
// The list functions (listPods, listReplicaSets, listEvents) check for cached data transparently.
type statusContext struct {
	namespace   string
	pods        []*corev1.Pod
	replicaSets []*appsv1.ReplicaSet
	events      []corev1.Event
}

type statusContextKey struct{}

// withStatusContext embeds statusContext in the context for access by list functions.
func withStatusContext(ctx context.Context, sc *statusContext) context.Context {
	return context.WithValue(ctx, statusContextKey{}, sc)
}

// getStatusContext retrieves the statusContext from context. Returns nil if not present.
func getStatusContext(ctx context.Context) *statusContext {
	if sc, ok := ctx.Value(statusContextKey{}).(*statusContext); ok {
		return sc
	}
	return nil
}

// buildStatusContext loads pods, replicaSets, and events for the given namespace and embeds
// the result in the returned context. Subsequent calls to listPods, listReplicaSets, listEvents,
// and listEvents will return cached data if the namespace matches.
func (r *Reconciler) buildStatusContext(ctx context.Context, namespace string) (context.Context, error) {
	sc := &statusContext{namespace: namespace}
	var err error
	if sc.pods, err = r.doListPods(ctx, namespace); err != nil {
		return ctx, err
	}
	if sc.replicaSets, err = r.doListReplicaSets(ctx, namespace); err != nil {
		return ctx, err
	}
	if sc.events, err = r.doListAllEvents(ctx, namespace); err != nil {
		return ctx, err
	}
	return withStatusContext(ctx, sc), nil
}

// filterEventsForObject returns events from the pre-loaded list that match the given object.
func filterEventsForObject(ctx context.Context, sch *runtime.Scheme, events []corev1.Event, obj client.Object) ([]corev1.Event, error) {
	if obj == nil {
		return nil, nil
	}
	gvk, err := getObjectGVK(ctx, sch, obj)
	if err != nil {
		return nil, err
	}
	apiVersion, kind := gvk.ToAPIVersionAndKind()
	var result []corev1.Event
	for i := range events {
		e := &events[i]
		if e.InvolvedObject.Name == obj.GetName() &&
			e.InvolvedObject.Namespace == obj.GetNamespace() &&
			e.InvolvedObject.Kind == kind &&
			e.InvolvedObject.APIVersion == apiVersion {
			result = append(result, *e)
		}
	}
	return result, nil
}

type checkStatusFunc func(context.Context, client.Object, *nvcav2beta1.ICMSRequest) (ObjectStatus, error)

//nolint:gocyclo
func (r *Reconciler) collectObjectStatuses(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (ObjectStatuses, []error, error) {
	log := logf.FromContext(ctx)

	objsData, isRendered, err := r.getRenderedData(ctx, ms)
	if err != nil {
		return nil, nil, err
	}
	if !isRendered {
		if objsData, err = r.render(ctx, ms, icmsReq); err != nil {
			return nil, nil, err
		}
		if err := r.saveRenderedData(ctx, ms, objsData); err != nil {
			return nil, nil, err
		}
	}

	objs, resources, _, err := decodeObjects(ctx, r.Decoder, objsData)
	if err != nil {
		return nil, nil, err
	}
	updateResourcesStatus(ms, resources)

	var (
		serrs    []error
		statuses ObjectStatuses
	)

	// Cache permissions errors per GVK to reduce requests to the API server.
	caniCache := map[schema.GroupVersionKind]error{}
	checkPermissions := r.newPermissionsChecker(caniCache, requiredRBACVerbsRead)

	// Pre-load pods, replicaSets, and events once for the duration of this reconcile.
	// The list functions will transparently return cached data when statusContext is in context.
	ctx, err = r.buildStatusContext(ctx, ms.Spec.Namespace)
	if err != nil {
		return nil, nil, err
	}

	// Use the main client for type info since impersonated clients will not have access
	// to all required APIs or all necessary permissions on APIs.
	rm := r.Client.RESTMapper()

	// Observe the status of only rendered objects. Owned objects created by controllers
	// will be retrieved by owner reference.
	// Track pods that have explicit status checks so they are skipped in the general pod check below.
	// Let StorageRequest controller handle the SMB pod, and caller handle the utils pod.
	seenPods := sets.New(common.UtilsPodName, nvcastorage.SMBServerPodName)
	for _, o := range objs {
		gvk, err := r.getObjectGVK(ctx, o)
		if err != nil {
			return nil, serrs, reconcile.TerminalError(err)
		}

		if isNamespaced, err := apiutil.IsGVKNamespaced(gvk, rm); isNamespaced {
			o.SetNamespace(ms.Spec.Namespace)
		} else if err != nil {
			log.Error(err, "Failed to check if object is namespaced")
			return nil, serrs, reconcile.TerminalError(err)
		}

		if err := checkPermissions(ctx, r.Client, gvk, o.GetNamespace()); err != nil {
			return nil, serrs, err
		}

		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
			if apierrors.IsNotFound(err) {
				statuses = append(statuses, ObjectStatus{
					Object:      o,
					Reason:      err.Error(),
					TerminalBad: true,
				})
			} else {
				serrs = append(serrs, err)
				log.Error(err, "Failed to get live object from rendered")
			}
			continue
		}

		o.GetObjectKind().SetGroupVersionKind(gvk)
		check, ok := r.statusCheckers[gvk]
		if !ok {
			// Some objects created by the controller don't have associated status handlers,
			// so mark the object as successful with a related message.
			statuses = append(statuses, ObjectStatus{
				Object: o,
				Status: statusSucceeded,
				Reason: "No status handler, assuming object is functioning as expected",
			})
			continue
		}
		objStatus, err := check(ctx, o, icmsReq)
		if err != nil {
			serrs = append(serrs, err)
			continue
		}
		statuses = append(statuses, objStatus)

		if pod, ok := o.(*corev1.Pod); ok {
			seenPods.Insert(pod.Name)
		}
	}

	// Also check pods for non-degraded terminal issues, which may not be caught
	// for awhile by their owning object.
	allPods, err := r.listPods(ctx, ms.Spec.Namespace)
	if err != nil {
		return nil, serrs, err
	}
	for _, pod := range allPods {
		if seenPods.Has(pod.Name) || nvcatypes.IsInfraOwnedObject(pod) {
			continue
		}
		podStatus, err := r.checkPodStatus(ctx, pod, icmsReq)
		if err != nil {
			serrs = append(serrs, err)
			continue
		}
		switch podStatus.Status {
		case podFailedImagePullIssues, podFailedContainerStuck, podFailedInitContainerStuck:
			statuses = append(statuses, podStatus)
		}
	}

	return statuses, serrs, nil
}

func (r *Reconciler) doStatus(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	// The workload feature-flags ConfigMap may opt a workload into worker-readiness-based
	// status, which only considers worker container readiness rather than aggressively
	// accounting for the health of all workload objects.
	if ms.Spec.WorkloadConfig.IsFeatureFlagEnabled(featureflag.StatusByWorkerReadiness) {
		return r.doStatusByWorkerReadiness(ctx, ms, icmsReq)
	}
	return r.doStatusAggressive(ctx, ms, icmsReq)
}

// doStatusAggressive checks the status of all workload objects and the utils pod.
// If any object has a terminal bad status, the function will return a terminal error.
func (r *Reconciler) doStatusAggressive(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	statuses, serrs, err := r.collectObjectStatuses(ctx, ms, icmsReq)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Check utils pod too.
	utilsPodFound := true
	utilsPod := &corev1.Pod{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: common.UtilsPodName}, utilsPod); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		utilsPodFound = false
	}
	if utilsPodFound {
		utilsStatus, err := r.checkPodStatus(ctx, utilsPod, icmsReq)
		if err != nil {
			serrs = append(serrs, err)
		} else {
			statuses = append(statuses, utilsStatus)
		}
	}

	var (
		res  reconcile.Result
		rerr error
	)
	switch {
	case !utilsPodFound:
		// By the time doStatus is called, the utils pod must exist.
		rerr = reconcile.TerminalError(fmt.Errorf("utils pod not found"))
		log.Error(rerr, "Workload is unhealthy")
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
			Message: "Infrastructure objects not found",
		})

		if err := errors.Join(serrs...); err != nil {
			log.Error(err, "Errors were encountered while checking object status")
		}
	case statuses.AnyTerminal():
		if icmsReq.Spec.Action == common.RequestICMSInstancesForTask || icmsReq.Spec.Action == common.TaskCreationAction {
			// Check utils pod separately for tasks. The utils pod's status may determine the status of the entire task.
			mrd := icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxRuntimeDuration
			mqd := icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxQueuedDuration
			res, rerr = r.doTerminalTaskStatus(ctx, ms, utilsPod, statuses, mqd, mrd)
		} else if statuses.OnlyTerminalDegraded() {
			// If the only reason is worker degradation, set a condition and let the agent handle cleanup.
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.MiniServiceStatusReasonDegradedWorker,
				Message: writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterTerminal),
			})
			rerr = reconcile.TerminalError(fmt.Errorf("at least one object is degraded"))
			log.Error(rerr, "Function is degraded",
				"errorObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterTerminal))
		} else {
			// Check if there is an existing MiniServiceConditionObjectsHealthy to
			// and set to a backoff timeout
			reason := v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout
			existingCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy)
			if existingCond != nil && existingCond.Reason == v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout {
				elapsed := r.now().Sub(existingCond.LastTransitionTime.Time)
				// If the elapsed time is less than the retry transient failure timeout,
				// then the objects are failing without a backoff and we shouldn't
				if elapsed > r.K8sTimeConfig.FailingObjectsBackoffTimeout {
					reason = v1alpha1.MiniServiceStatusReasonObjectsFailed
				}
			}

			// Set the condition (value will be false no need to set it again)
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  reason,
				Message: writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterTerminal),
			})

			// If we are in backoff requeue based on configured interval.
			if reason == v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout {
				res.RequeueAfter = r.K8sTimeConfig.FailingObjectsBackoffRequeueInterval
				rerr = nil
				log.Info("Objects are failing with backoff, requeuing",
					"after", r.K8sTimeConfig.FailingObjectsBackoffRequeueInterval,
					"terminalObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterTerminal))
			} else {
				rerr = reconcile.TerminalError(fmt.Errorf("some objects failed"))
				log.Error(rerr, "At least one object has a bad terminal status",
					"terminalObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterTerminal))
			}
		}

		if err := errors.Join(serrs...); err != nil {
			log.Error(err, "Errors were encountered while checking object status")
		}
	case statuses.AnyPending():
		healthyCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy)
		if healthyCond != nil && healthyCond.Status != metav1.ConditionTrue &&
			healthyCond.LastTransitionTime.Add(r.K8sTimeConfig.MaxRunningTimeout).Before(r.now()) {
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.MiniServiceStatusReasonPendingTimeout,
				Message: writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterPending),
			})
			rerr = reconcile.TerminalError(fmt.Errorf("some resources timed out pending"))
			log.Error(rerr, "Pending object timeout reached",
				"pendingObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterPending))
		} else {
			var message string
			if !slices.ContainsFunc(statuses, func(os ObjectStatus) bool {
				return os.Scheduling
			}) {
				// Once all pods are scheduled after installation, the miniservice can transition to Starting.
				if ms.Status.Phase != v1alpha1.MiniServiceRunning {
					ms.Status.Phase = v1alpha1.MiniServiceStarting
				}
				message = "All objects are scheduled, waiting on readiness"
			} else {
				log.V(1).Info("Waiting on more objects to schedule before transitioning to Starting")
				message = "Some objects are not scheduled"
			}
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				Message: fmt.Sprintf("%s\n%s", message, writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterPending)),
			})
			log.Info(message, "pendingObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterPending))

			// Requeue at least once after the scheduling timeout has passed in case no progress is made.
			res.RequeueAfter = getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig)
		}
		if err := errors.Join(serrs...); err != nil {
			log.Error(err, "Errors were encountered while checking object status")
		}
	default:
		if rerr = errors.Join(serrs...); rerr != nil {
			log.Error(rerr, "Found errors while collecting object statuses")
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.MiniServiceStatusReasonObjectStatusErrors,
				Message: rerr.Error(),
			})
		} else {
			log.Info("All objects have good statuses, MiniService is now running")
			ms.Status.Phase = v1alpha1.MiniServiceRunning
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
				Reason: "ObjectsReady",
			})
		}
	}

	return res, rerr
}

func (r *Reconciler) doStatusByWorkerReadiness(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
) (res reconcile.Result, rerr error) {
	log := logf.FromContext(ctx)

	statuses, serrs, err := r.collectObjectStatuses(ctx, ms, icmsReq)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Handle all non-worker objects separately from the worker pod, in all cases.
	// Health does not depend on these object's statuses, so they are purely informational.
	// NVCA will report the condition's message to ICMS for users to debug.
	{
		anyObjectsNotReady := false
		objectStatusMsg := writeObjectStatuses(ctx, r.Client.Scheme(), statuses, func(os ObjectStatus) bool {
			v := os.TerminalBad || os.Scheduling || os.Pending
			anyObjectsNotReady = anyObjectsNotReady || v
			return v
		})
		if anyObjectsNotReady {
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				Message: objectStatusMsg,
			})
		} else {
			meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
				Reason: "ObjectsReady",
			})
		}
	}

	// Below here, only the worker pod's status is checked for MiniService health.

	utilsPod := &corev1.Pod{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: common.UtilsPodName}, utilsPod); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		// By the time doStatus* functions are called, the utils pod must exist.
		rerr = reconcile.TerminalError(fmt.Errorf("utils pod not found"))
		log.Error(rerr, "Workload is unhealthy")
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionWorkersHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  "WorkerNotFound",
			Message: "Worker pod not found",
		})

		if err := errors.Join(serrs...); err != nil {
			log.Error(err, "Errors were encountered while checking object statuses")
		}
		return reconcile.Result{}, rerr
	}

	utilsStatus, err := r.checkPodStatus(ctx, utilsPod, icmsReq)
	if err != nil {
		return reconcile.Result{}, errors.Join(append(serrs, err)...)
	}

	workerContainerStatus, statusPresent, err := getFunctionWorkerContainerStatus(utilsPod)
	if err != nil || !statusPresent {
		// Utils pod events will requeue this once container statuses are present.
		return reconcile.Result{}, err
	}

	// If the worker container is ready, then the miniservice is running.
	if workerContainerStatus.Ready {
		ms.Status.Phase = v1alpha1.MiniServiceRunning
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:   v1alpha1.MiniServiceConditionWorkersHealthy,
			Status: metav1.ConditionTrue,
			Reason: "WorkersReady",
		})
		return reconcile.Result{}, nil
	}

	// At this point the worker container is either progressing towards a ready state,
	// or is in a terminal bad state.

	var workerIssueReasons []string
	if utilsStatus.AdditionalPodStatus != nil {
		aps := utilsStatus.AdditionalPodStatus
		// Check if the worker container is degraded.
		if aps.Degraded != nil && slices.ContainsFunc(aps.Degraded.Containers, func(cd nvcak8sutil.ContainerDegraded) bool {
			return isWorkerContainerName(cd.Name)
		}) {
			workerIssueReasons = append(workerIssueReasons, v1alpha1.MiniServiceStatusReasonDegradedWorker)
		}
		// Check if the worker container is stuck initializing.
		if aps.StuckInitializing != nil && slices.ContainsFunc(aps.StuckInitializing.Containers, func(csi nvcak8sutil.ContainerStuckInitializing) bool {
			return isWorkerContainerName(csi.Name)
		}) {
			workerIssueReasons = append(workerIssueReasons, "WorkerStuckInitializing")
		}
		// Check if the worker container has image pull issues.
		if aps.ImagePullIssues != nil && slices.ContainsFunc(aps.ImagePullIssues, func(cips nvcak8sutil.ContainerImagePullIssues) bool {
			return isWorkerContainerName(cips.Name)
		}) {
			workerIssueReasons = append(workerIssueReasons, "WorkerImagePullIssues")
		}
	}

	workersHealthyCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionWorkersHealthy)
	workerStatusMsg := writeObjectStatuses(ctx, r.Client.Scheme(), []ObjectStatus{utilsStatus}, nil)

	if len(workerIssueReasons) > 0 {
		rerr = reconcile.TerminalError(fmt.Errorf("worker container is in a terminal bad state"))
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionWorkersHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  "WorkerTerminalBadState",
			Message: workerStatusMsg,
		})
		log.Error(rerr, "Worker container is in a terminal bad state",
			"workerIssueReasons", workerIssueReasons,
			"workerStatus", workerStatusMsg)
	} else if workersHealthyCond != nil && workersHealthyCond.Status != metav1.ConditionTrue &&
		workersHealthyCond.LastTransitionTime.Add(r.K8sTimeConfig.MaxRunningTimeout).Before(r.now()) {
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionWorkersHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonPendingTimeout,
			Message: workerStatusMsg,
		})
		rerr = reconcile.TerminalError(fmt.Errorf("worker did not become ready within %s", r.K8sTimeConfig.MaxRunningTimeout))
		log.Error(rerr, "Pending worker timeout reached",
			"workerStatus", workerStatusMsg)
	} else {
		var message string
		if utilsStatus.Scheduling {
			// Once the worker pod is scheduled after installation, the miniservice can transition to Starting.
			if ms.Status.Phase != v1alpha1.MiniServiceRunning {
				ms.Status.Phase = v1alpha1.MiniServiceStarting
			}
			message = "Worker pod is scheduled, waiting on readiness"
		} else {
			log.V(1).Info("Waiting on worker pod to schedule")
			message = "Worker pod is not scheduled"
		}
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionWorkersHealthy,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
			Message: fmt.Sprintf("%s\n%s", message, workerStatusMsg),
		})
		log.V(1).Info(message)

		// Requeue at least once after the scheduling timeout has passed in case no progress is made.
		res.RequeueAfter = getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig)
	}
	if err := errors.Join(serrs...); err != nil {
		log.Error(err, "Errors were encountered while checking object status")
	}

	return res, rerr
}

func (r *Reconciler) doTerminalTaskStatus(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	utilsPod *corev1.Pod,
	statuses ObjectStatuses,
	maxQueuedDurationStr, maxRuntimeDurationStr string,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	// If the utils pod completed successfully then the task completed regardless of other objects' statuses.
	if isTaskUtilsContainerExitedSuccessfully(utilsPod) {
		ms.Status.Phase = v1alpha1.MiniServiceCompleted
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionTrue,
			Reason:  "UtilsPodCompletedSuccessfully",
			Message: "Task completed successfully",
		})
		log.V(1).Info("Task has succeeded")
		return reconcile.Result{}, nil
	}
	// Wait for the utils container to exit, or for max runtime duration to have passed.
	mrd, err := nvcainternaltranslate.ParseMaxRuntimeDuration(maxRuntimeDurationStr)
	if err != nil {
		log.Error(err, "Error parsing max runtime duration for helm task in status check", "duration", maxRuntimeDurationStr)
		return reconcile.Result{}, reconcile.TerminalError(err)
	}
	mqd, err := translateutil.ParseISO8601Duration(maxQueuedDurationStr)
	if err != nil {
		log.Error(err, "Error parsing max queued duration for helm task in status check", "duration", maxQueuedDurationStr)
		return reconcile.Result{}, reconcile.TerminalError(err)
	}
	// An extra 5 minutes is added to max runtime duration to ensure utils has time to send
	// a heartbeat with EXCEEDED_MAX_RUNTIME_DURATION.
	maxRuntimeDurationWithCleanup := nvcak8sutil.AddTaskCleanupGracePeriod(mrd)
	isMaxRuntimeExceeded := nvcak8sutil.HasTaskPodExceededTimeout(utilsPod, mqd, maxRuntimeDurationWithCleanup, r.now())
	var (
		reason, message string
		res             reconcile.Result
		rerr            error
	)
	switch {
	case isTaskUtilsContainerExited(utilsPod):
		// Utils has exited non-zero, task needs cleanup.
		rerr = reconcile.TerminalError(fmt.Errorf("task utils container has exited non-zero"))
		message = fmt.Sprintf("task infrastructure detected an error in the task, check task logs for more information\n%s",
			writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterTerminal))
	case isMaxRuntimeExceeded:
		rerr = reconcile.TerminalError(fmt.Errorf("task max runtime duration of %s has been exceeded", mrd))
		message = fmt.Sprintf("max runtime duration %s has been exceeded", mrd)
	case statuses.OnlyTerminalDegraded():
		// If the only reason is worker degradation, set a condition and let the agent handle cleanup.
		reason = v1alpha1.MiniServiceStatusReasonDegradedWorker
		rerr = reconcile.TerminalError(fmt.Errorf("at least one object is degraded"))
	default:
		// Check if there is an existing MiniServiceConditionObjectsHealthy to
		// and set to a backoff timeout
		reason = v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout
		existingCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionObjectsHealthy)
		if existingCond != nil && existingCond.Reason == v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout {
			elapsed := r.now().Sub(existingCond.LastTransitionTime.Time)
			// If the elapsed time is less than the retry transient failure timeout,
			// then the objects are failing without a backoff and we shouldn't
			if elapsed > r.K8sTimeConfig.FailingObjectsBackoffTimeout {
				reason = v1alpha1.MiniServiceStatusReasonObjectsFailed
			}
		}
		rerr = reconcile.TerminalError(fmt.Errorf("some objects failed"))
	}
	if reason == "" {
		reason = v1alpha1.MiniServiceStatusReasonObjectsFailed
	}
	if message == "" {
		message = writeObjectStatuses(ctx, r.Client.Scheme(), statuses, filterTerminal)
	}

	// Set the condition (value will be false no need to set it again)
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})

	// If we are in backoff requeue based on configured interval.
	if reason == v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout {
		res.RequeueAfter = r.K8sTimeConfig.FailingObjectsBackoffRequeueInterval
		rerr = nil
		log.Info("Objects are failing with backoff, requeuing",
			"after", r.K8sTimeConfig.FailingObjectsBackoffRequeueInterval,
			"terminalObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterTerminal))
	} else if rerr != nil {
		log.Error(rerr, "Task has failed",
			"terminalObjects", collectObjectIDs(ctx, r.Client.Scheme(), statuses, filterTerminal))
	}

	return res, rerr
}

// getFunctionWorkerContainerStatus returns the worker container's status from the utils pod for the given MiniService,
// either utils for normal functions/tasks or the llm worker (pylon) for LLM-type functions.
func getFunctionWorkerContainerStatus(utilsPod *corev1.Pod) (corev1.ContainerStatus, bool, error) {
	for _, cs := range utilsPod.Status.ContainerStatuses {
		if isWorkerContainerName(cs.Name) {
			return cs, true, nil
		}
	}
	// The container statuses are not yet available, indicating the pod hasn't been initialized by kubelet yet.
	for _, c := range utilsPod.Spec.Containers {
		if isWorkerContainerName(c.Name) {
			return corev1.ContainerStatus{}, false, nil
		}
	}
	return corev1.ContainerStatus{}, false, reconcile.TerminalError(fmt.Errorf("worker container not found"))
}

func isWorkerContainerName(name string) bool {
	return name == common.UtilsContainerName || name == function.LLMWorkerContainerName
}

func (r *Reconciler) makeStatusCheckers() map[schema.GroupVersionKind]checkStatusFunc {
	return map[schema.GroupVersionKind]checkStatusFunc{
		podGVK:         r.checkPodStatus,
		deploymentGVK:  r.checkDeploymentStatus,
		replicaSetGVK:  r.checkReplicaSetStatus,
		statefulSetGVK: r.checkStatefulSetStatus,
		jobGVK:         r.checkJobStatus,
		cronJobGVK:     r.checkCronJobStatus,
	}
}

func makeObjectIdentifierString(ctx context.Context, sch *runtime.Scheme, obj client.Object) string {
	if obj == nil {
		return "<nil>"
	}
	gvk := getObjectGVKOrUnknown(ctx, sch, obj)
	idStr := fmt.Sprintf("%s.%s %s", gvk.Version, gvk.Kind, obj.GetName())
	if gvk.Group != "" {
		idStr = fmt.Sprintf("%s/%s", gvk.Group, idStr)
	}
	return idStr
}

type ObjectStatus struct {
	Object      client.Object
	Status      string
	Reason      string
	Pending     bool
	Scheduling  bool
	TerminalBad bool
	// ChildObjects is a list of children objects that are owned by this object,
	// ex. Deployment -> ReplicaSet -> Pod(s)
	ChildObjects ObjectStatuses
	// AbnormalEvents are events on an object with type != "Normal".
	// Some of these event types indicate a terminal status, based on truthiness of parseErrorEventMessage;
	// all others should only be reported when the object is terminal from conditions/phase.
	AbnormalEvents []corev1.Event
	// AdditionalPodStatus contains additional status data for the pod, if any.
	AdditionalPodStatus *AdditionalPodStatus
}

type AdditionalPodStatus struct {
	Degraded          *nvcak8sutil.PodDegraded
	StuckInitializing *nvcak8sutil.PodStuckInitializing
	ImagePullIssues   []nvcak8sutil.ContainerImagePullIssues
}

func (s ObjectStatus) toTerminalEventStatus() (cs ObjectStatus) {
	// Shallow copy objects.
	cs.Object = s.Object
	cs.ChildObjects = s.ChildObjects
	// Error fields.
	cs.TerminalBad = true
	cs.Status = statusFailed
	cs.Reason = terminalErrorEventReason
	return cs
}

// Visit traverses the object status tree s in DFS pre-order, calling f on each node.
func (s *ObjectStatus) Visit(f func(*ObjectStatus)) {
	f(s)
	for i := range s.ChildObjects {
		s.ChildObjects[i].Visit(f)
	}
}

type ObjectStatuses []ObjectStatus

func (oss ObjectStatuses) AnyPending() bool {
	return slices.ContainsFunc(oss, func(os ObjectStatus) bool {
		return os.Pending
	})
}

func (oss ObjectStatuses) AnyTerminal() bool {
	return slices.ContainsFunc(oss, func(os ObjectStatus) bool {
		return os.TerminalBad
	})
}

func (oss ObjectStatuses) OnlyTerminalDegraded() bool {
	var isDegraded bool
	for _, objStatus := range oss {
		if objStatus.TerminalBad {
			if objStatus.Status != statusDegradedWorker {
				return false
			}
			isDegraded = true
		}
	}
	return isDegraded
}

const (
	// Set when an object is starting or initializing.
	statusStarting = "starting"
	// Set when an object is running.
	statusRunning = "running"
	// Set when an object is in an installed state.
	statusSucceeded = "succeeded"
	// Set when an object has unrecoverably failed for any reason.
	statusFailed = "failed"
	// Set when an object is degraded.
	statusDegradedWorker = "degraded-worker"
	// Set as object status reason when an object failed because of an event
	// indicating a terminal error.
	terminalErrorEventReason = "terminal error event(s)"
)

func (r *Reconciler) checkPodStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	pod := obj.(*corev1.Pod)
	objStatus := getPodStatus(pod, r.K8sTimeConfig, getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig))
	objStatus.Object = obj
	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}
	return objStatus, nil
}

const (
	podScheduling               = "scheduling"
	podFailedContainerStuck     = "container-stuck"
	podFailedInitContainerStuck = "init-" + podFailedContainerStuck
	podFailedImagePullIssues    = "image-pull-issues"
)

func getPodStatus(pod *corev1.Pod, k8sTimeConfig *nvcak8sutil.TimeConfig, schedulingTimeout time.Duration) ObjectStatus {
	ps := pod.Status
	switch ps.Phase {
	case corev1.PodPending, corev1.PodUnknown, "":
		isScheduled := nvcak8sutil.IsPodScheduled(ps)
		if isScheduled {
			if imgPullIssues, imgIssues := nvcak8sutil.ImagePullIssuesReported(ps); imgIssues &&
				nvcak8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.MaxImagePullErrorThreshold) {
				return ObjectStatus{
					Status:      podFailedImagePullIssues,
					Reason:      "ImagePullIssues",
					TerminalBad: true,
					AdditionalPodStatus: &AdditionalPodStatus{
						ImagePullIssues: imgPullIssues,
					},
				}
			}
			if podStuckInitializing, stuck := nvcak8sutil.IsPodStuckInitializing(pod, k8sTimeConfig); stuck {
				return ObjectStatus{
					Status:      podFailedContainerStuck,
					Reason:      podStuckInitializing.Reason,
					TerminalBad: true,
					AdditionalPodStatus: &AdditionalPodStatus{
						StuckInitializing: &podStuckInitializing,
					},
				}
			}
			if nvcak8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdMinutesOnInitFailure) {
				return ObjectStatus{
					Status:      podFailedInitContainerStuck,
					Reason:      ps.Reason,
					TerminalBad: true,
				}
			}
		} else if nvcak8sutil.IsTimeSincePodLaunchedLaterThan(pod, schedulingTimeout) {
			// Pod is getting stuck scheduling, and should be killed
			return ObjectStatus{
				Status:      podFailedContainerStuck,
				Reason:      "PodStuckScheduling",
				TerminalBad: true,
			}
		}
		if isScheduled {
			return ObjectStatus{
				Status:  statusStarting,
				Pending: true,
			}
		}
		return ObjectStatus{
			Status:     podScheduling,
			Scheduling: true,
			Pending:    true,
		}
	case corev1.PodRunning:
		// NVCF-125 Check PodReady & Not in Stuck Initializing state
		if nvcak8sutil.IsPodReady(ps) {
			return ObjectStatus{
				Status: statusRunning,
			}
		}
		if podStuckInitializing, stuck := nvcak8sutil.IsPodStuckInitializing(pod, k8sTimeConfig); stuck {
			return ObjectStatus{
				Status:      podFailedContainerStuck,
				Reason:      podStuckInitializing.Reason,
				TerminalBad: true,
				AdditionalPodStatus: &AdditionalPodStatus{
					StuckInitializing: &podStuckInitializing,
				},
			}
		}
		if degradedStatus, degraded := nvcak8sutil.IsPodDegraded(pod, k8sTimeConfig); degraded {
			return ObjectStatus{
				Status:      statusDegradedWorker,
				Reason:      degradedStatus.Reason,
				TerminalBad: true,
				AdditionalPodStatus: &AdditionalPodStatus{
					Degraded: &degradedStatus,
				},
			}
		}
		return ObjectStatus{
			Status:  statusStarting,
			Pending: true,
		}
	case corev1.PodSucceeded:
		return ObjectStatus{
			Status: statusSucceeded,
		}
	case corev1.PodFailed:
		// Pod is getting rejected for admission it will be killed
		if rejected, reason := nvcak8sutil.IsPodAdmissionRejected(ps); rejected {
			return ObjectStatus{
				Status:      podFailedContainerStuck,
				Reason:      reason,
				TerminalBad: true,
			}
		}
		return ObjectStatus{
			Status:      statusFailed,
			Reason:      ps.Reason,
			TerminalBad: true,
		}
	}
	// Unreachable
	return ObjectStatus{
		Status:  "unknown",
		Reason:  ps.Reason,
		Pending: true,
	}
}

// Tasks can remain enqueued up to their max queued duration, so a non-default value must be used
// to check for a scheduling timeout.
func getPodSchedulingTimeout(ctx context.Context, req *nvcav2beta1.ICMSRequest, k8sTimeConfig *nvcak8sutil.TimeConfig) time.Duration {
	schedulingTimeout := k8sTimeConfig.PodScheduledThreshold
	if req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction {
		mqd, err := translateutil.ParseISO8601Duration(req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxQueuedDuration)
		if err != nil {
			logf.FromContext(ctx).Error(err, "Failed to parse max queued duration for task Pod")
		} else {
			schedulingTimeout = mqd
		}
	}
	return schedulingTimeout
}

func isTaskUtilsContainerExitedSuccessfully(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == common.UtilsContainerName {
			// Since utils pod restart policy is Never, the last termination state will be the current state.
			return cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0
		}
	}
	return false
}

func isTaskUtilsContainerExited(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == common.UtilsContainerName {
			// Since utils pod restart policy is Never, the last termination state will be the current state.
			return cs.State.Terminated != nil
		}
	}
	return false
}

// isInitContainersComplete returns true if pod's non-restartable init containers have terminated.
// Termination status is not considered (ex. exit code).
func isInitContainersComplete(pod *corev1.Pod) bool {
	// Ensure conditions indicate the pod's init containers have been started.
	// The "initialized" condition will be set if it has no init containers
	// once the pod is running.
	if !slices.ContainsFunc(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue
	}) {
		return false
	}
	// All non-restartable init containers must have terminated, if any.
	nonRestartableInitContainers := sets.New[string]()
	for _, initContainer := range pod.Spec.InitContainers {
		if initContainer.RestartPolicy == nil {
			nonRestartableInitContainers.Insert(initContainer.Name)
		}
	}
	for _, initContainerStatus := range pod.Status.InitContainerStatuses {
		if nonRestartableInitContainers.Has(initContainerStatus.Name) {
			if initContainerStatus.State.Terminated == nil {
				return false
			}
			nonRestartableInitContainers.Delete(initContainerStatus.Name)
		}
	}
	// Return false if some non-restartable init container has no status.
	return nonRestartableInitContainers.Len() == 0
}

func (r *Reconciler) checkDeploymentStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	dep := obj.(*appsv1.Deployment)
	objStatus := ObjectStatus{
		Object: dep,
	}
	badReasons := sets.New[string]()
	for _, cond := range dep.Status.Conditions {
		switch {
		case cond.Reason == "ProgressDeadlineExceeded":
			// If pods take too long to come up, the dep has failed.
			badReasons.Insert(cond.Reason)
		case cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue:
			badReasons.Insert(cond.Reason)
		case cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse:
			badReasons.Insert(cond.Reason)
		}
	}
	if len(badReasons) != 0 {
		objStatus.TerminalBad = true
		reasons := badReasons.UnsortedList()
		sort.Strings(reasons)
		objStatus.Reason = strings.Join(reasons, ",")
	} else {
		var wantReplicas int32 = 1
		if dep.Spec.Replicas != nil {
			wantReplicas = *dep.Spec.Replicas
		}
		if dep.Status.AvailableReplicas < wantReplicas {
			objStatus.Pending = true
			// Assume the deployment's replicas are still scheduling
			// until one replicaset is found with scheduled replicas.
			objStatus.Scheduling = dep.Status.Replicas < wantReplicas
		}
	}

	replicaSets, err := r.listReplicaSets(ctx, obj.GetNamespace())
	if err != nil {
		return objStatus, err
	}
	replicaSetChecker := r.statusCheckers[replicaSetGVK]
	for _, repset := range replicaSets {
		// There may be multiple ReplicaSets, only one of which will have spec.replicas unset or > 0
		// to denote it as the non-historical one.
		if r.isOwner(ctx, obj, repset) && (repset.Spec.Replicas == nil || *repset.Spec.Replicas > 0) {
			replicaSetStatus, err := replicaSetChecker(ctx, repset, icmsReq)
			if err != nil {
				return objStatus, err
			}
			objStatus.ChildObjects = append(objStatus.ChildObjects, replicaSetStatus)
			// Some replicaset is done scheduling all replicas.
			if !replicaSetStatus.Scheduling {
				objStatus.Scheduling = false
			}
			break
		}
	}

	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}

	return objStatus, nil
}

func (r *Reconciler) checkReplicaSetStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	rs := obj.(*appsv1.ReplicaSet)
	objStatus := ObjectStatus{
		Object: rs,
	}

	for _, cond := range rs.Status.Conditions {
		if cond.Type == appsv1.ReplicaSetReplicaFailure && cond.Status == corev1.ConditionTrue {
			objStatus.TerminalBad = true
			objStatus.Reason = cond.Reason
			break
		}
	}

	var wantReplicas int32 = 1
	if !objStatus.TerminalBad {
		if rs.Spec.Replicas != nil {
			wantReplicas = *rs.Spec.Replicas
		}
		objStatus.Pending = rs.Status.AvailableReplicas < wantReplicas
	}

	pods, err := r.listPods(ctx, obj.GetNamespace())
	if err != nil {
		return objStatus, err
	}
	podChecker := r.statusCheckers[podGVK]
	unscheduledReplicas := 0
	for _, pod := range pods {
		if r.isOwner(ctx, obj, pod) {
			podStatus, err := podChecker(ctx, pod, icmsReq)
			if err != nil {
				return objStatus, err
			}
			objStatus.ChildObjects = append(objStatus.ChildObjects, podStatus)
			// Some replica is still scheduling.
			if podStatus.Scheduling {
				unscheduledReplicas++
			}
		}
	}

	if objStatus.Pending {
		// The replicaset is done scheduling if there are no unscheduled pods
		// and all replicas exist (but are not necessarily running).
		objStatus.Scheduling = unscheduledReplicas != 0 || rs.Status.Replicas < wantReplicas
	}

	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}

	return objStatus, nil
}

func (r *Reconciler) checkStatefulSetStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	ss := obj.(*appsv1.StatefulSet)
	objStatus := ObjectStatus{
		Object: ss,
	}
	// StatefulSets do not use conditions.
	var wantReplicas int32 = 1
	if ss.Spec.Replicas != nil {
		wantReplicas = *ss.Spec.Replicas
	}
	if ss.Status.AvailableReplicas < wantReplicas {
		objStatus.Pending = true
	}

	pods, err := r.listPods(ctx, obj.GetNamespace())
	if err != nil {
		return objStatus, err
	}
	podChecker := r.statusCheckers[podGVK]
	unscheduledReplicas := 0
	for _, pod := range pods {
		if r.isOwner(ctx, obj, pod) {
			podStatus, err := podChecker(ctx, pod, icmsReq)
			if err != nil {
				return objStatus, err
			}
			objStatus.ChildObjects = append(objStatus.ChildObjects, podStatus)
			// Some replica is still scheduling.
			if podStatus.Scheduling {
				unscheduledReplicas++
			}
		}
	}

	if objStatus.Pending {
		wantScheduledReplicas := wantReplicas
		// The statefulset may create pods slowly if done in ordered-ready mode.
		// It is better to assume the rest will be scheduled and fail later than to avoid timeouts
		// waiting for all to be scheduled.
		if ss.Spec.PodManagementPolicy == appsv1.OrderedReadyPodManagement {
			wantScheduledReplicas = 1
		}
		// The statefulset is done scheduling if there are no unscheduled pods
		// and all replicas exist (but are not necessarily running).
		objStatus.Scheduling = unscheduledReplicas != 0 || ss.Status.Replicas < wantScheduledReplicas
	}

	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}

	return objStatus, nil
}

func (r *Reconciler) checkJobStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	job := obj.(*batchv1.Job)
	objStatus := ObjectStatus{
		Object: job,
	}
	failedIdx, completeIdx := -1, -1
	isJobCompleted := job.Status.CompletionTime != nil
	for i, jobCond := range job.Status.Conditions {
		if jobCond.Type == batchv1.JobFailed && jobCond.Status == corev1.ConditionTrue {
			failedIdx = i
			break
		} else if jobCond.Type == batchv1.JobComplete && jobCond.Status == corev1.ConditionTrue {
			completeIdx = i
			break
		}
	}
	if failedIdx != -1 {
		objStatus.TerminalBad = true
		objStatus.Status = statusFailed
		objStatus.Reason = job.Status.Conditions[failedIdx].Reason
	} else if isJobCompleted || completeIdx != -1 {
		objStatus.Status = statusSucceeded
		if completeIdx > -1 {
			objStatus.Reason = job.Status.Conditions[completeIdx].Reason
		}
	} else {
		objStatus.Pending = true
	}

	pods, err := r.listPods(ctx, obj.GetNamespace())
	if err != nil {
		return objStatus, err
	}
	podChecker := r.statusCheckers[podGVK]
	unscheduledWorkers := 0
	for _, pod := range pods {
		if r.isOwner(ctx, job, pod) {
			podStatus, err := podChecker(ctx, pod, icmsReq)
			if err != nil {
				return objStatus, err
			}
			objStatus.ChildObjects = append(objStatus.ChildObjects, podStatus)
			// Some worker is still scheduling.
			if podStatus.Scheduling {
				unscheduledWorkers++
			}
		}
	}

	if objStatus.Pending {
		// A non-succeeded job can make progress towards spec.completions with one pod running at once,
		// so only wait for one pod to schedule regardless of parallelism/completions.
		objStatus.Scheduling = unscheduledWorkers != 0 || job.Status.Active <= 0
	}
	// Tasks should not wait for jobs to complete.
	// Functions use jobs to initialize so should not be marked as running until they succeed.
	if isTaskObject(obj) && objStatus.Pending && !objStatus.Scheduling {
		objStatus.Status = statusSucceeded
		objStatus.Reason = "not waiting for task Job to complete"
		objStatus.Pending = false
	}

	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}

	return objStatus, nil
}

func isTaskObject(obj client.Object) bool {
	if obj.GetLabels() == nil {
		return false
	}
	return obj.GetLabels()[nvcatypes.TaskIDKey] != ""
}

func (r *Reconciler) checkCronJobStatus(ctx context.Context, obj client.Object, icmsReq *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
	cronJob := obj.(*batchv1.CronJob)
	objStatus := ObjectStatus{
		Object: cronJob,
	}

	// Since CronJobs may be scheduled at any time and their failure doesn't necessarily mean
	// that the miniservice has failed, always mark it as succeeded.
	objStatus.Status = statusSucceeded
	if cs := cronJob.Status; cs.LastSuccessfulTime != nil && cs.LastScheduleTime != nil {
		if cs.LastSuccessfulTime.Before(cs.LastScheduleTime) {
			objStatus.Reason = "Jobs scheduled at " + cs.LastScheduleTime.String()
		} else {
			objStatus.Reason = "Job last succeeded at " + cs.LastSuccessfulTime.String()
		}
	} else if cs.LastScheduleTime != nil {
		objStatus.Reason = "Jobs scheduled at " + cs.LastScheduleTime.String()
	} else if cs.LastSuccessfulTime != nil {
		objStatus.Reason = "Job last succeeded at " + cs.LastSuccessfulTime.String()
	}

	jobs, err := r.listJobs(ctx, obj.GetNamespace())
	if err != nil {
		return objStatus, err
	}
	jobChecker := r.statusCheckers[jobGVK]
	for _, job := range jobs {
		if r.isOwner(ctx, obj, job) {
			jobStatus, err := jobChecker(ctx, job, icmsReq)
			if err != nil {
				return objStatus, err
			}
			objStatus.ChildObjects = append(objStatus.ChildObjects, jobStatus)
		}
	}

	// Scheduling is irrelevant to CronJob's since they may be delayed in running.
	abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
	objStatus.AbnormalEvents = abnormalEvents
	if isError && !objStatus.TerminalBad {
		objStatus = objStatus.toTerminalEventStatus()
	}

	return objStatus, nil
}

func (r *Reconciler) getUnexpectedEventsForObject(
	ctx context.Context,
	obj client.Object,
) (dedupedEvents []corev1.Event, anyError bool) {
	if obj == nil {
		return nil, false
	}
	log := logf.FromContext(ctx)

	events, err := r.listEvents(ctx, obj)
	if err != nil {
		log.Error(err, "Failed to list events for object status")
		return nil, false
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].FirstTimestamp.Before(&events[j].FirstTimestamp)
	})

	dedupByMessage := map[string]struct{}{}
	for _, event := range events {
		if _, ok := dedupByMessage[event.Message]; !ok {
			dedupByMessage[event.Message] = struct{}{}
			dedupedEvents = append(dedupedEvents, event)
		}
	}

	for i := 0; i < len(dedupedEvents); i++ {
		message, includeEvent, isErrorEvent := parseErrorEventMessage(&dedupedEvents[i])
		if !includeEvent {
			// Remove the event since it does not appear to be related to any error.
			dedupedEvents = slices.Delete(dedupedEvents, i, i+1)
			i--
			continue
		}
		anyError = anyError || isErrorEvent
		dedupedEvents[i].Message = message
	}

	return dedupedEvents, anyError
}

// parseErrorEventMessage parses event.Message to make it more human-readable and remove
// internal cluster details when possible. It returns false when event is not an error message.
func parseErrorEventMessage(event *corev1.Event) (message string, include, isError bool) {
	switch event.Reason {
	case "FailedCreate", "FailedUpdate":
		// FailedCreate/Update is added to an event and/or in a ReplicaSet or StatefulSet condition
		// when a replica pod cannot be created.
		//
		// Most creation failure event messages look something like:
		//	"Error creating: <error message>"
		// or
		//   "create <kind> <name> in <owner kind> <owner name> failed error: <error message prefix>: ..."
		// where error message prefix is something like:
		//   "<resource> <name> is forbidden"
		//
		// Refs:
		// https://github.com/kubernetes/kubernetes/blob/25f1248/pkg/controller/controller_utils.go#L596C75-L596C89
		// https://github.com/kubernetes/kubernetes/blob/25f1248/pkg/controller/statefulset/stateful_pod_control.go#L297-L300
		// https://github.com/kubernetes/kubernetes/blob/25f1248/pkg/controller/statefulset/stateful_pod_control.go#L314-L317
		isError = true
	case "ReplicaSetCreateError":
		// ReplicaSetCreateError is set when Deployments fail to create their ReplicaSet's.
		//
		// Messages will have the format:
		//   "Failed to create new replica set <name>: <error message>"
		//
		// Ref: https://github.com/kubernetes/kubernetes/blob/25f1248/pkg/controller/deployment/sync.go#L267-L269
		isError = true
	case "PolicyViolation":
		// PolicyViolation events from Kyverno and other policy engines are informational
		// and should not be included in error messages as they don't indicate workload failures.
		return event.Message, false, false
	default:
		// Controllers for objects that create Pods may observe "forbidden" errors,
		// but these do not necessarily fail the controller object.
		const emStrForbidden = "forbidden:"
		isError = isError || strings.Contains(event.Message, emStrForbidden)
	}

	return event.Message, isError || event.Type == corev1.EventTypeWarning, isError
}

var (
	filterTerminal = func(os ObjectStatus) bool { return os.TerminalBad }
	filterPending  = func(os ObjectStatus) bool { return os.Pending }
)

func writeObjectStatuses(ctx context.Context, sch *runtime.Scheme, objStatuses ObjectStatuses, filter func(ObjectStatus) bool) string {
	messageBuilder := &strings.Builder{}
	for _, objStatus := range objStatuses {
		// Let top-level object determine whether children may be in filtered state.
		if filter != nil && !filter(objStatus) {
			continue
		}
		objStatus.Visit(func(os *ObjectStatus) {
			if filter == nil || filter(*os) {
				writeObjectStatus(ctx, sch, messageBuilder, *os)
				messageBuilder.WriteByte('\n')
			}
		})
	}
	return messageBuilder.String()
}

func writeObjectStatus(ctx context.Context, sch *runtime.Scheme, sb *strings.Builder, objStatus ObjectStatus) {
	sb.WriteString(makeObjectIdentifierString(ctx, sch, objStatus.Object))
	if objStatus.Reason != "" {
		sb.WriteString(": ")
		sb.WriteString(objStatus.Reason)
	}
	if len(objStatus.AbnormalEvents) != 0 {
		sb.WriteString("\nevents:\n")
		for _, event := range objStatus.AbnormalEvents {
			sb.WriteByte('\t')
			sb.WriteString(event.Reason)
			sb.WriteString(": ")
			sb.WriteString(event.Message)
			sb.WriteByte('\n')
		}
	}
}

// collectObjectIDs collects IDs of the objects in objStatuses that match the filter, including children.
func collectObjectIDs(ctx context.Context, sch *runtime.Scheme, objStatuses ObjectStatuses, filter func(ObjectStatus) bool) (ids []string) {
	for _, objStatus := range objStatuses {
		// Let top-level object determine whether children may be in filtered state.
		if !filter(objStatus) {
			continue
		}
		objStatus.Visit(func(os *ObjectStatus) {
			if filter(*os) {
				ids = append(ids, makeObjectIdentifierString(ctx, sch, os.Object))
			}
		})
	}
	return ids
}

func (r *Reconciler) isOwner(ctx context.Context, owner, target client.Object) bool {
	gvk := r.getObjectGVKOrUnknown(ctx, owner)
	ownerRefObj := metav1.OwnerReference{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
	}
	for _, ownerRef := range target.GetOwnerReferences() {
		// Ignore other fields, irrelevant here.
		if cmp.Equal(ownerRefObj, metav1.OwnerReference{
			APIVersion: ownerRef.APIVersion,
			Kind:       ownerRef.Kind,
			Name:       ownerRef.Name,
			UID:        ownerRef.UID,
		}) {
			return true
		}
	}
	return false
}

// listReplicaSets returns cached replicaSets if statusContext is in context for the same namespace,
// otherwise performs the API call.
func (r *Reconciler) listReplicaSets(ctx context.Context, namespace string) ([]*appsv1.ReplicaSet, error) {
	if sc := getStatusContext(ctx); sc != nil && sc.namespace == namespace {
		return sc.replicaSets, nil
	}
	return r.doListReplicaSets(ctx, namespace)
}

func (r *Reconciler) doListReplicaSets(ctx context.Context, namespace string) ([]*appsv1.ReplicaSet, error) {
	rsList := &appsv1.ReplicaSetList{}
	// UnsafeDisableDeepCopy avoids deep-copying objects from the cache.
	// This is safe because status checking is read-only. See DGXCINC-3086.
	if err := r.Client.List(ctx, rsList,
		client.InNamespace(namespace),
		client.UnsafeDisableDeepCopy,
	); err != nil {
		return nil, err
	}
	rss := make([]*appsv1.ReplicaSet, len(rsList.Items))
	for i := range rsList.Items {
		rss[i] = &rsList.Items[i]
	}
	return rss, nil
}

// listPods returns cached pods if statusContext is in context for the same namespace,
// otherwise performs the API call.
func (r *Reconciler) listPods(ctx context.Context, namespace string) ([]*corev1.Pod, error) {
	if sc := getStatusContext(ctx); sc != nil && sc.namespace == namespace {
		return sc.pods, nil
	}
	return r.doListPods(ctx, namespace)
}

func (r *Reconciler) doListPods(ctx context.Context, namespace string) ([]*corev1.Pod, error) {
	podList := &corev1.PodList{}
	// UnsafeDisableDeepCopy avoids deep-copying objects from the cache.
	// This is safe because status checking is read-only. See DGXCINC-3086.
	if err := r.Client.List(ctx, podList,
		client.InNamespace(namespace),
		client.UnsafeDisableDeepCopy,
	); err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, len(podList.Items))
	for i := range podList.Items {
		pods[i] = &podList.Items[i]
	}
	return pods, nil
}

func (r *Reconciler) listJobs(ctx context.Context, namespace string) ([]*batchv1.Job, error) {
	jobList := &batchv1.JobList{}
	// UnsafeDisableDeepCopy avoids deep-copying objects from the cache.
	// This is safe because status checking is read-only. See DGXCINC-3086.
	if err := r.Client.List(ctx, jobList,
		client.InNamespace(namespace),
		client.UnsafeDisableDeepCopy,
	); err != nil {
		return nil, err
	}
	jobs := make([]*batchv1.Job, len(jobList.Items))
	for i := range jobList.Items {
		jobs[i] = &jobList.Items[i]
	}
	return jobs, nil
}

func (r *Reconciler) doListAllEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	eventList := &corev1.EventList{}
	// UnsafeDisableDeepCopy avoids deep-copying objects from the cache.
	// This is safe because status checking is read-only. See DGXCINC-3086.
	if err := r.Client.List(ctx, eventList,
		client.InNamespace(namespace),
		client.MatchingFields{eventTypeFieldPath: corev1.EventTypeWarning},
		client.UnsafeDisableDeepCopy,
	); err != nil {
		return nil, err
	}
	return eventList.Items, nil
}

// listEvents returns warning events for the given object. Uses cached events if statusContext
// is in context for the same namespace, otherwise performs the API call with field selectors.
func (r *Reconciler) listEvents(ctx context.Context, obj client.Object) ([]corev1.Event, error) {
	if sc := getStatusContext(ctx); sc != nil && sc.namespace == obj.GetNamespace() {
		return filterEventsForObject(ctx, r.Client.Scheme(), sc.events, obj)
	}
	return r.doListEvents(ctx, obj)
}

func (r *Reconciler) doListEvents(ctx context.Context, obj client.Object) ([]corev1.Event, error) {
	gvk, err := r.getObjectGVK(ctx, obj)
	if err != nil {
		return nil, err
	}
	apiVersion, kind := gvk.ToAPIVersionAndKind()
	fieldSel := fields.Set{
		eventInvObjNameFieldPath:       obj.GetName(),
		eventInvObjKindFieldPath:       kind,
		eventInvObjAPIVersionFieldPath: apiVersion,
		eventTypeFieldPath:             corev1.EventTypeWarning,
	}.AsSelector()

	eventList := &corev1.EventList{}
	// UnsafeDisableDeepCopy avoids deep-copying objects from the cache.
	// This is safe because status checking is read-only. See DGXCINC-3086.
	if err := r.Client.List(ctx, eventList,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFieldsSelector{Selector: fieldSel},
		client.UnsafeDisableDeepCopy,
	); err != nil {
		return nil, err
	}
	return eventList.Items, nil
}
