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

package nvca

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func (c K8sComputeBackend) applyContainerTaskCreationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error { //nolint:gocyclo
	log := core.GetLogger(ctx)

	activeInstances := req.Status.Instances
	if len(activeInstances) == 0 {
		activeInstances = map[string]nvcav2beta1.InstanceStatus{}
	}

	cmInfo := req.Spec.CreationMsgInfo
	instCount := cmInfo.InstanceCount

	// If requested instances are already created, aggregate instance status.
	if int(instCount) == len(activeInstances) {
		return c.bk8s.ApplyICMSRequestStatusChange(ctx, req)
	}

	metrics := nvcametrics.FromContext(ctx)

	c.bk8s.eventRecorder.Eventf(req,
		corev1.EventTypeNormal, string(types.EventCategoryInstanceCreation),
		"Creating %d remaining requested instances", int(instCount)-len(activeInstances),
	)

	labelsForReq := types.GetLabelsForRequest(req, c.bk8s.featureFlagFetcher)
	annosForReq := types.GetAnnotationsForRequest(req)
	instanceNamespace := c.bk8s.podInstanceNamespace
	taskLaunchSpec := *cmInfo.TaskLaunchSpecification

	var termGPDuration time.Duration
	if taskLaunchSpec.TerminationGracePeriodDuration != "" {
		dur, err := translateutil.ParseISO8601Duration(taskLaunchSpec.TerminationGracePeriodDuration)
		if err != nil {
			return nvcaerrors.TerminalError(err)
		}
		termGPDuration = dur
	}

	// Overhead is not needed in reqs/lims because they are only used for the task container here.
	reqs, lims, err := c.calculatePodInstanceResourcesForInstanceType(cmInfo, nil)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}

	if !c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceContainerTaskResourceLimits) {
		// Remove non-GPU resources when enforcement is off.
		for _, l := range []corev1.ResourceList{reqs, lims} {
			delete(l, corev1.ResourceCPU)
			delete(l, corev1.ResourceMemory)
			delete(l, corev1.ResourceEphemeralStorage)
		}
	}

	tcfg := task.TranslateConfig{
		TranslateConfig: common.TranslateConfig{
			Namespace:                    instanceNamespace,
			CommonLabels:                 labelsForReq,
			CommonAnnotations:            annosForReq,
			ObjectNameBase:               req.Name,
			InstanceTypeLabelSelectorKey: nodefeatures.UniformInstanceTypeLabelKey,
			WorkloadResources: corev1.ResourceRequirements{
				Requests: reqs,
				Limits:   lims,
			},
			Tolerations:        append([]corev1.Toleration(nil), c.bk8s.cfg.Workload.Tolerations...),
			OTelResources:      k8sutil.GetContainerResourcesBYOO(c.bk8s.cfg),
			FluentbitResources: k8sutil.GetContainerResourcesFluentBit(c.bk8s.cfg),
			FluentbitEnabled:   c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOOFluentBit),
			ClusterRegion:      c.bk8s.clusterRegion,
			ClusterName:        c.bk8s.clusterName,
		},
	}

	msg := task.CreationQueueMessage{
		Details:                      req.Spec.TaskDetails,
		CreationQueueMessageMetadata: req.Spec.CreationMsgInfo.CreationQueueMessageMetadata,
		LaunchSpecification:          *req.Spec.CreationMsgInfo.TaskLaunchSpecification,
	}

	objs, err := task.Translate(msg, tcfg)
	if err != nil {
		metricLabels := metrics.WithDefaultLabelValues(EventTranslateTaskError)
		metrics.EventErrorTotal.WithLabelValues(metricLabels...).Inc()
		return err
	}

	ownerRefsForReq := getOwnerRefForRequest(req)

	mf := func(obj client.Object) {
		obj.SetNamespace(instanceNamespace)
		obj.SetLabels(mergeMaps(obj.GetLabels(), labelsForReq))
		obj.SetAnnotations(mergeMaps(obj.GetAnnotations(), annosForReq))
	}

	var (
		workloadPods     []*corev1.Pod
		bdCreateCachePVC *corev1.PersistentVolumeClaim
		initCacheJob     *batchv1.Job
	)
	for _, obj := range objs {
		obj.SetOwnerReferences(ownerRefsForReq)
		switch typedObj := obj.(type) {
		case *corev1.PersistentVolumeClaim:
			if strings.HasPrefix(typedObj.Name, "rw-pvc-") {
				bdCreateCachePVC = typedObj
			}
		case *batchv1.Job:
			if strings.HasPrefix(typedObj.Name, "writer-job-") {
				initCacheJob = typedObj
			}
		case *corev1.Pod:
			workloadPods = append(workloadPods, typedObj)
		default:
			if err := c.createObjectOnce(ctx, obj); err != nil {
				return err
			}
		}
	}

	needsBYOO := taskLaunchSpec.Telemetries != nil &&
		c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOObservability)

	// This method should retry on retry-able model caching errors,
	// which likely mean the cache is still being initialized.
	cacheMF := func(*corev1.Pod) {}
	var cachePVCName string
	if c.bk8s.cachingSupportEnabled && (initCacheJob != nil && bdCreateCachePVC != nil) {
		if cacheMF, cachePVCName, err = c.setupContainerModelCaching(ctx, req, bdCreateCachePVC, initCacheJob, mf); err != nil {
			return err
		}
	} else if !c.bk8s.cachingSupportEnabled {
		log.Debugf("ModelCaching support is disabled, creating task instance without caching")
	} else {
		log.Debug("InitCacheJob / BDCreate spec was not specified, skipping task model caching")
	}

	if requeue, err := c.initializeImageCredentialHelper(ctx, req, func(obj client.Object) {
		mf(obj)
		obj.SetOwnerReferences(ownerRefsForReq)
	}); err != nil || requeue {
		if err != nil {
			err = fmt.Errorf("ensure image credential updater objects: %w", err)
		}
		return err
	}

	podClient := c.clients.K8s.CoreV1().Pods(instanceNamespace)

	var (
		newActiveInstances []nvcav2beta1.InstanceStatus
		createdPodCount    int
		existingPodCount   int
	)
	for _, workloadPod := range workloadPods {
		plog := log.WithField("pod", workloadPod.Name)
		instanceID := workloadPod.Name

		cacheMF(workloadPod)

		// Add the INFERENCE_READY_TIMEOUT env var to the utils
		// container in the task pod.
		addEnvsToUtilsContainers(workloadPod, corev1.EnvVar{
			Name:  types.InferenceReadyTimeoutEnvKey,
			Value: c.bk8s.k8sTimeConfig.WorkerStartupTimeout.String(),
		})

		workloadPod = setEnvInPod(workloadPod, map[string]string{types.NVCTInstIDEnvKey: workloadPod.Name})

		if c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
			workloadPod.Spec.SchedulerName = kaischeduler.SchedulerName
			workloadPod.Labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.GetQName()
		}

		// Task pods must only be created once.
		if _, ok := activeInstances[instanceID]; ok {
			plog.Debug("Workload pod was already created, not recreating for task")
			continue
		}

		needsEnforcement := !c.enabledAttrs.Empty()
		if needsEnforcement {
			enforce.SetMetadata(workloadPod, c.enabledAttrs)
		}

		if termGPDuration != 0 {
			// The internal grace period will likely be shorter than this value,
			// giving container processes enough time to checkpoint before the utils container
			// sends term signals.
			workloadPod.Spec.TerminationGracePeriodSeconds = new(int64)
			*workloadPod.Spec.TerminationGracePeriodSeconds = int64(termGPDuration.Seconds())
		}

		// The utils pod for a function with BYOO enabled must be targeted
		// by the BYOO metrics egress netpol in this namespace, which uses a specific label.
		if needsBYOO {
			workloadPod.Labels[k8sutil.BYOOMetricsEgressTargetLabelKey] =
				k8sutil.BYOOMetricsEgressTargetLabelValue

			// Add BYOO OTel env vars to the task container.
			// Use 127.0.0.1 since the OTel collector runs as a sidecar in the same pod.
			byooOTelEnvs := common.MakeOTelEnvSet(taskLaunchSpec.Telemetries, "127.0.0.1")
			for i := range workloadPod.Spec.Containers {
				// Add to task containers, preserving customer-provided env vars.
				if workloadPod.Spec.Containers[i].Name != common.UtilsContainerName &&
					workloadPod.Spec.Containers[i].Name != common.ByooOTelCollectorPodNameBase &&
					workloadPod.Spec.Containers[i].Name != common.ESSContainerName {
					c := []corev1.Container{workloadPod.Spec.Containers[i]}
					common.AddOptionalEnvsToContainers(c, byooOTelEnvs...)
					workloadPod.Spec.Containers[i] = c[0]
				}
			}
		}

		// Container task utils and init resources are toggled by feature flag.
		if c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceContainerTaskResourceLimits) {
			k8sutil.SetNVCFInfraContainerResources(corev1.ResourceList(c.bk8s.cfg.Agent.UtilsResources), workloadPod)
			if err := k8sutil.ValidateAllContainerResourcesSet(workloadPod); err != nil {
				log.WithError(err).Error("Container task pod resources are invalid")
				return nvcaerrors.TerminalError(err)
			}
		}

		podCreated := true
		if _, err := podClient.Create(ctx, workloadPod, metav1.CreateOptions{}); err != nil {
			if !errors.IsAlreadyExists(err) {
				plog.WithError(err).Error("Create Pod instance")
				return err
			}
			podCreated = false
			plog.Debug("Task pod already exists")
		}

		if podCreated {
			plog.Infof("Created task Pod %s", workloadPod.GetName())
			createdPodCount++
		} else {
			existingPodCount++
		}

		newActiveInstances = append(newActiveInstances, nvcav2beta1.InstanceStatus{
			ID:                    instanceID,
			Type:                  nvcav2beta1.InstanceTypePod,
			Status:                string(types.ICMSInstanceStarted),
			LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
			LastReportedTimestamp: nil,
		})

		if podCreated {
			c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal,
				string(types.EventCategoryInstanceCreation), "Created %v Instance %v",
				nvcav2beta1.InstanceTypePod, instanceID,
			)
		}
	}

	for _, activeInstance := range newActiveInstances {
		if _, ok := activeInstances[activeInstance.ID]; !ok {
			activeInstances[activeInstance.ID] = activeInstance
		}
	}

	log.Debugf("Successfully created %v Pod instances, found %v existing Pod instances",
		createdPodCount, existingPodCount)

	// update timestamp only once for InProgress
	if req.Status.RequestStatus != nvcav2beta1.ICMSRequestStatusInProgress {
		req.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
	}
	req.Status.Instances = activeInstances
	req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusInProgress

	modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
		if cachePVCName != "" {
			sr.Status.CacheReferenceName = cachePVCName
		}
		sr.Status.Instances = req.Status.Instances
		sr.Status.RequestStatus = req.Status.RequestStatus
		sr.Status.LastStatusUpdated = req.Status.LastStatusUpdated
	}
	if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
		return fmt.Errorf("failed to update status for request %s", req.Name)
	}

	if int(instCount) != len(activeInstances) {
		return fmt.Errorf("created only %d/%d instances for request %s",
			len(activeInstances), instCount, req.Name)
	}

	log.Debugf("Successfully created all %d requested instances for request %s", instCount, req.Name)

	return nil
}

func (c K8sComputeBackend) createObjectOnce(ctx context.Context, obj metav1.Object) error {
	log := core.GetLogger(ctx)

	robj := obj.(runtime.Object)
	possibleGVKs, _, err := c.scheme.ObjectKinds(robj)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}
	if len(possibleGVKs) != 1 {
		return nvcaerrors.TerminalError(fmt.Errorf("unable to create object %s of type %T, too few or many possible type schemes (%d)",
			obj.GetName(), obj, len(possibleGVKs)))
	}
	gvk := possibleGVKs[0]
	rm, err := c.discRestMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("get REST mapping for gvk: %w", err)
	}

	resClient := c.dynClient.Resource(rm.Resource).Namespace(obj.GetNamespace())

	_, err = resClient.Get(ctx, obj.GetName(), metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall(strings.ToLower(gvk.Kind), err)
	}

	if err == nil {
		// Object already exists.
		return nil
	}
	if !errors.IsNotFound(err) {
		log.WithError(err).Errorf("Get instance object %s %s", gvk, obj.GetName())
		return err
	}

	// Create obj for the first time.

	uo, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}
	u := &unstructured.Unstructured{
		Object: uo,
	}
	if _, err = resClient.Create(ctx, u, metav1.CreateOptions{}); err != nil {
		log.WithError(err).Errorf("Create instance object %s %s", gvk, obj.GetName())
		return err
	}

	log.Infof("Created %s %s", gvk, obj.GetName())

	return nil
}

func (c K8sComputeBackend) calculatePodInstanceResourcesForInstanceType(
	cmInfo nvcav2beta1.ICMSCreationMessageInfo,
	overhead corev1.ResourceList,
) (reqs, lims corev1.ResourceList, err error) {
	instanceType, ok := c.bk8s.regITCache.Get(cmInfo.InstanceTypeName)
	if !ok {
		return nil, nil, fmt.Errorf("instance type not found with name: %s", cmInfo.InstanceTypeName)
	}
	gpuResName := nodefeatures.GetGPUResourceNameFetcher(c.bk8s.featureFlagFetcher)
	return types.CalculateResourcesForInstanceType(instanceType, gpuResName, overhead)
}

// reconcileContainerTaskPodState checks task pod state and performs actions if containers have exited
// with unexpected exit codes.
func (c K8sComputeBackend) reconcileContainerTaskPodState(ctx context.Context,
	pod *corev1.Pod,
	state types.ICMSInstanceState,
	maxQueuedDuration, maxRuntimeDuration time.Duration,
	now time.Time,
) (gracePeriodSeconds *int64, shouldTerminate bool) {
	log := core.GetLogger(ctx)
	if pod.DeletionTimestamp != nil {
		log.WithField("deletion_timestamp", pod.DeletionTimestamp.Time.String()).
			Debug("Container task pod has been deleted already, waiting for deletion grace period")
		return nil, false
	}

	utilsExitCode, isUtilsTerminated := isTaskUtilsContainerExited(pod)

	// Check instance states first.
	if _, isBadState := badICMSInstanceStates[state]; isBadState {
		if state == types.ICMSInstanceDegradedWorker && !c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.AutoPurgeDegradedWorkers) {
			log.WithFields(logrus.Fields{
				"pod":   pod.Name,
				"state": state,
			}).Warnf("Pod is in bad state but skipping delete since AutoPurgeDegradedWorkers is disabled")
			return nil, false
		}
		// The task is in a bad state and must be cleaned up.
		if isUtilsTerminated {
			// Set to 0 for immediate deletion, since utils isn't managing the task anymore
			// but may not have signaled to NVCT that the task should terminate.
			gracePeriodSeconds = new(int64)
		}
	} else {
		// An extra 5 minutes is added to max runtime duration to ensure utils has time to send
		// a heartbeat with EXCEEDED_MAX_RUNTIME_DURATION.
		maxRuntimeDuration = k8sutil.AddTaskCleanupGracePeriod(maxRuntimeDuration)
		isMaxRuntimeExceeded := k8sutil.HasTaskPodExceededTimeout(pod, maxQueuedDuration, maxRuntimeDuration, now)

		if (isUtilsTerminated && utilsExitCode == 0) || (!isUtilsTerminated && !isMaxRuntimeExceeded) {
			// If utils has terminated successfully (reported to NVCT then exited),
			// or has not terminated yet within max runtime duration (will report to NVCT on success
			// or failed heartbeat then exit), then nothing to do.
			if isUtilsTerminated {
				log.Debug("Container task has succeeded")
			}
			return nil, false
		}

		// Utils has either terminated non-zero or max runtime + buffer has been exceeded.
		// The task must be cleaned up forcefully since the worker isn't managing the task anymore
		// or is in an unrecoverable state and won't exit.
		if isUtilsTerminated || isMaxRuntimeExceeded {
			gracePeriodSeconds = new(int64)
		}
	}

	return gracePeriodSeconds, true
}

func isTaskUtilsContainerExited(pod *corev1.Pod) (exitCode int32, terminated bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == common.UtilsContainerName && cs.State.Terminated != nil {
			// Since utils pod restart policy is Never, the last termination state will be the current state.
			return cs.State.Terminated.ExitCode, true
		}
	}
	return 0, false
}
