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
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/workloadtypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcav2beta1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type podState struct {
	pod   *corev1.Pod
	state nvcatypes.ICMSInstanceState
}

const (
	miniserviceNameSuffix = "-miniservice"

	helmChartInstanceRBACConfigMapName = "nvca-miniservice-rbac"
	helmChartInstanceRoleName          = "mini-service-restrictions"
)

func getMiniServiceInstanceID(srName string) string {
	return trimDNS1123Label(srName, len(miniserviceNameSuffix)) + miniserviceNameSuffix
}

func isMiniServiceInstance(name string) bool {
	return strings.HasSuffix(name, miniserviceNameSuffix)
}

func writePodLogLine(sw io.StringWriter, line string, idx int, size, max int64) int64 {
	sw.WriteString(fmt.Sprintf("POD %d\n===\n", idx))
	sw.WriteString(line)
	if max >= size {
		max -= size
	} else {
		max = 0
	}
	return max
}

func (c K8sComputeBackend) applyMiniServiceCreationMessage(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) error {
	log := core.GetLogger(ctx)

	var (
		hcLaunchSpec    *common.HelmChartLaunchSpecification
		cacheLaunchSpec *common.CacheLaunchSpecification
		envB64          string
	)
	if req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil {
		hcLaunchSpec = req.Spec.CreationMsgInfo.FunctionLaunchSpecification.HelmChartLaunchSpecification
		cacheLaunchSpec = req.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification
		envB64 = req.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64
	} else if req.Spec.CreationMsgInfo.TaskLaunchSpecification != nil {
		hcLaunchSpec = req.Spec.CreationMsgInfo.TaskLaunchSpecification.HelmChartLaunchSpecification
		cacheLaunchSpec = req.Spec.CreationMsgInfo.TaskLaunchSpecification.CacheLaunchSpecification
		envB64 = req.Spec.CreationMsgInfo.TaskLaunchSpecification.EnvironmentB64
	}
	if hcLaunchSpec == nil {
		return nvcaerrors.TerminalError(fmt.Errorf("no Helm chart launch specification found in ICMS request %s",
			req.Name))
	}

	activeInstances := map[string]nvcav2beta1.InstanceStatus{}
	if len(req.Status.Instances) != 0 {
		activeInstances = req.Status.Instances
	}
	instCount := req.Spec.CreationMsgInfo.InstanceCount

	// if requested instances are already created, return
	if int(instCount) == len(activeInstances) {
		return c.bk8s.ApplyICMSRequestStatusChange(ctx, req)
	}

	c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal, string(nvcatypes.EventCategoryInstanceCreation),
		"Creating %v requested instances", instCount)

	labelsForReq := nvcatypes.GetLabelsForRequest(req, c.bk8s.featureFlagFetcher)
	annosForReq := nvcatypes.GetAnnotationsForRequest(req)

	instanceID := getMiniServiceInstanceID(req.Name)

	hcCfg, err := common.ExtractHelmConfiguration(envB64, hcLaunchSpec)
	if err != nil {
		return nvcaerrors.TerminalError(fmt.Errorf("extract helm config: %v", err))
	}

	ms := &v1alpha1.MiniService{}
	ms.Name = instanceID
	ms.Labels = labelsForReq
	ms.Annotations = annosForReq
	ms.Spec = v1alpha1.MiniServiceSpec{
		Namespace:       req.Name,
		ICMSRequestName: req.Name,
		HelmChartConfig: hcCfg,
	}

	log.Debugln("Creating MiniService")

	if err := c.clients.HelmV2.Create(ctx, ms); err != nil && !apierrors.IsAlreadyExists(err) {
		log.WithError(err).Error("Create MiniService")
		return err
	}

	instance := nvcav2beta1.InstanceStatus{
		ID:                 instanceID,
		Type:               nvcav2beta1.InstanceTypeMiniService,
		Status:             string(nvcatypes.ICMSInstanceStarted),
		LastReportedStatus: string(nvcatypes.ICMSInstanceStateNoStatus),
	}

	if _, ok := activeInstances[instance.ID]; !ok {
		activeInstances[instance.ID] = instance
	}

	log.Debugf("Successfully created MiniService instance %s", instanceID)
	c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal,
		string(nvcatypes.EventCategoryInstanceCreation), "Created %v Instance %v", instance.Type, instance.ID)

	// update timestamp only once for InProgress
	if req.Status.RequestStatus != nvcav2beta1.ICMSRequestStatusInProgress &&
		req.Status.RequestStatus != nvcav2beta1.ICMSRequestStatusCachingInProgress {
		req.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
	}
	if cacheLaunchSpec != nil && cacheLaunchSpec.CacheArtifacts {
		// The miniservice will attempt to cache a model, so update ICMS request status.
		req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCachingInProgress
	} else {
		req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusInProgress
	}
	req.Status.Instances = activeInstances

	modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
		sr.Status.Instances = req.Status.Instances
		sr.Status.RequestStatus = req.Status.RequestStatus
		sr.Status.LastStatusUpdated = req.Status.LastStatusUpdated
	}
	if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
		log.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
	}
	if int(instCount) != len(activeInstances) {
		return fmt.Errorf("created only %v of requested %v instances for request %v/%v", len(activeInstances), instCount, req.Namespace, req.Name)
	}
	log.Debugf("successfully created %v of requested %v instances for request %v/%v", len(activeInstances), instCount, req.Namespace, req.Name)
	return nil
}

//nolint:gocyclo
func (c K8sComputeBackend) GetICMSRequestUpdatesForMiniServiceRequest(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	st nvcav2beta1.InstanceStatus,
) (nvcatypes.ICMSRequestUpdateInfo, error) {
	log := core.GetLogger(ctx).WithField("instance", st.ID)
	metrics := nvcametrics.FromContext(ctx)

	updateInfo := nvcatypes.ICMSRequestUpdateInfo{
		RequestID:  req.Spec.RequestID,
		InstanceID: st.ID,
	}

	ms := &v1alpha1.MiniService{}
	msKey := client.ObjectKey{Name: st.ID}
	if err := c.clients.HelmV2.Get(ctx, msKey, ms); err != nil {
		if !apierrors.IsNotFound(err) {
			return nvcatypes.ICMSRequestUpdateInfo{}, err
		}

		if c.bk8s.shouldReportInstanceStatusHeartbeat(ctx, req, st.ID,
			string(nvcatypes.ICMSInstanceTerminated), st.LastReportedStatus, st.LastReportedTimestamp) {
			log.WithError(err).Warnf("Instance is not running, report it as killed")
			updateInfo.Payload = nvcatypes.ICMSInstanceStatusUpdateRequest{
				Status:           nvcatypes.ICMSRequestInstanceTerminatedByService,
				InstanceState:    nvcatypes.ICMSInstanceTerminated,
				Action:           common.TerminationAction,
				RequestState:     nvcatypes.ICMSInstanceRequestClosed,
				TerminationCause: nvcatypes.ICMSInstanceFailedNotFound,
				SystemFailure:    string(nvcatypes.ICMSInstanceFailedNotFound),
			}
			if metrics != nil {
				metrics.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeHelm,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusFailure,
					nvcametrics.ICMSInstanceStateToFailureCategory(nvcatypes.ICMSInstanceFailedNotFound),
				)
			}
			return updateInfo, nil
		}
		return nvcatypes.ICMSRequestUpdateInfo{}, nil
	}

	var pods []*corev1.Pod
	msErrMsgBuf := &bytes.Buffer{}
	// failureCategory tracks the root cause for workload result metrics.
	// Set inside failure cases, used in the heartbeat gate below.
	failureCategory := workloadtypes.FailureCategoryNone
	// The miniservice controller handles status aggregation so its phase is all that is needed.
	switch ms.Status.Phase {
	case "":
		log.Debug("Skipping MiniService request update on empty phase")
		return nvcatypes.ICMSRequestUpdateInfo{}, nil
	case v1alpha1.MiniServiceCacheInProgress, v1alpha1.MiniServiceInstalling,
		v1alpha1.MiniServiceInstalled, v1alpha1.MiniServiceStarting:
		updateInfo.Payload.RequestState = nvcatypes.ICMSInstanceRequestActive
		updateInfo.Payload.InstanceState = nvcatypes.ICMSInstanceStarted
		updateInfo.Payload.Status = nvcatypes.ICMSRequestFulfilled
		updateInfo.Payload.Action = toLegacyAction(req.Spec.Action)
	case v1alpha1.MiniServiceRunning, v1alpha1.MiniServiceCompleted:
		updateInfo.Payload.RequestState = nvcatypes.ICMSInstanceRequestActive
		updateInfo.Payload.InstanceState = nvcatypes.ICMSInstanceRunning
		updateInfo.Payload.Status = nvcatypes.ICMSRequestFulfilled
		updateInfo.Payload.Action = toLegacyAction(req.Spec.Action)
	case v1alpha1.MiniServiceInstallFailed, v1alpha1.MiniServiceFailed:
		updateInfo.Payload.RequestState = nvcatypes.ICMSInstanceRequestClosed
		updateInfo.Payload.InstanceState = nvcatypes.ICMSInstanceTerminated
		updateInfo.Payload.Status = nvcatypes.ICMSRequestInstanceTerminatedByService
		updateInfo.Payload.Action = common.TerminationAction
		updateInfo.Payload.SystemFailure = string(nvcatypes.ICMSInstanceFailed)

		// The instance should be purged on failure unless the reason is a degraded state.
		purgeInstance := true
		// Collect bad conditions.
		msConds := ms.Status.Conditions
		seenMsgs := map[string]struct{}{}
		for i, relCond := range msConds {
			if !v1alpha1.MiniServiceStatusBadReasons[relCond.Reason] {
				continue
			}
			if relCond.Reason == v1alpha1.MiniServiceStatusReasonDegradedWorkerPods &&
				!c.bk8s.autoPurgeDegradedWorkers {
				purgeInstance = false
			}
			msg := strings.TrimSpace(relCond.Message)
			if _, ok := seenMsgs[msg]; ok {
				continue
			}
			seenMsgs[msg] = struct{}{}
			msErrMsgBuf.WriteString(msg)
			if i < len(msConds)-1 {
				msErrMsgBuf.WriteByte('\n')
			}
		}

		// Collect bad pod logs.
		var err error
		pods, err = c.bk8s.podLister.Pods(ms.Spec.Namespace).List(labels.Everything())
		if err != nil {
			log.WithError(err).Warnf("failed to get pods for MiniService instance %s", st.ID)
		}
		sort.Slice(pods, func(i, j int) bool {
			return pods[i].Name < pods[j].Name
		})

		var badPods []podState
		for _, pod := range pods {
			instStateFromPod, _ := podPhaseToInstanceState(pod, c.bk8s.k8sTimeConfig)

			if _, ok := badICMSInstanceStates[instStateFromPod]; !ok {
				continue
			}

			badPods = append(badPods, podState{pod: pod, state: instStateFromPod})
		}

		// Derive failure category from the first bad pod's state for the workload result metric.
		failureCategory = workloadtypes.FailureCategoryUnknown
		for _, ps := range badPods {
			if fc := nvcametrics.ICMSInstanceStateToFailureCategory(ps.state); fc != "" {
				failureCategory = fc
				break
			}
		}

		if len(badPods) > 0 && msErrMsgBuf.Len() > 0 {
			msErrMsgBuf.WriteString("\n\n")
		}
		maxWriteable := MaxBytesForPodLogs
		for i, ps := range badPods {
			prependLog := fmt.Sprintf("pod in state %v", ps.state)
			if podLogs, written, perr := c.GetErroredPodLogs(ctx, ps.pod, prependLog, maxWriteable); perr != nil {
				log.WithError(perr).WithField("pod", ps.pod.Name).
					Error("Get failed MiniService instance Pod logs")
			} else {
				maxWriteable = writePodLogLine(msErrMsgBuf, podLogs, i, written, maxWriteable)
			}
		}

		// Handle cleanup. The miniservice controller "understands" both function and task cleanup procedures.
		if purgeInstance {
			log.Warn("MiniService instance is in a bad state and will be force-purged")
			// Let the miniservice controller handle top-level resource deletion.
			if err := c.clients.HelmV2.Delete(ctx, ms); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).Error("Failed to delete MiniService")
			}
		} else {
			log.Warn("MiniService instance is in bad state, skipping purge since autoPurgeDegradedWorkers is disabled")
		}

		updateInfo.Payload.HealthInfo = nvcatypes.HealthInfo{
			ErrorLog: msErrMsgBuf.String(),
		}

		// TODO: source from which container failed.
		if req.Spec.TaskDetails.TaskID != "" {
			updateInfo.Payload.HealthInfo.ErrorSource = nvcatypes.ErrorSourceTaskContainer
		}
	}

	if c.bk8s.shouldReportInstanceStatusHeartbeat(ctx, req, st.ID,
		string(updateInfo.Payload.InstanceState), st.LastReportedStatus, st.LastReportedTimestamp) {
		// Only increment metric once for each image registry with issues.
		imgRegSet := sets.New[string]()
		for _, pod := range pods {
			if imgTag, _, ok := k8sutil.ImagePullIssuesReported(pod.Status); ok {
				reg := k8sutil.ParseImageRegistry(imgTag)
				if imgRegSet.Has(reg) {
					continue
				}
				metrics.ImagePullIssueTotal.WithLabelValues(metrics.WithDefaultLabelValues(reg)...).Inc()
				imgRegSet.Insert(reg)
			}
		}

		// Record workload result metric on terminal state transitions.
		if metrics != nil {
			if updateInfo.Payload.InstanceState == nvcatypes.ICMSInstanceTerminated && failureCategory != workloadtypes.FailureCategoryNone {
				metrics.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeHelm,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusFailure,
					failureCategory,
				)
			} else if updateInfo.Payload.InstanceState == nvcatypes.ICMSInstanceRunning &&
				st.LastReportedStatus != string(nvcatypes.ICMSInstanceRunning) {
				metrics.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeHelm,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusSuccess,
					workloadtypes.FailureCategoryNone,
				)
			}
		}

		return updateInfo, nil
	}

	// If the status is running ensure the storage requests are not in a failed state
	if updateInfo.Payload.InstanceState == nvcatypes.ICMSInstanceRunning {
		storageReqState, err := getStorageRequestsInstanceState(ctx, func() ([]*nvcav2beta1new.StorageRequest, error) {
			return c.bk8s.storageRequestLister.StorageRequests(ms.Spec.Namespace).List(labels.Everything())
		})
		if err != nil {
			return updateInfo, err
		}
		if storageReqState != nvcatypes.ICMSInstanceRunning {
			log.WithField("state", storageReqState).Info("Cleaning up MiniService on unexpected storage request instance state")

			updateInfo.Payload.Status = nvcatypes.ICMSRequestInstanceTerminatedByService
			updateInfo.Payload.InstanceState = nvcatypes.ICMSInstanceTerminated
			updateInfo.Payload.Action = common.TerminationAction
			updateInfo.Payload.RequestState = nvcatypes.ICMSInstanceRequestClosed
			updateInfo.Payload.TerminationCause = storageReqState

			// Let the miniservice controller handle top-level resource deletion.
			if err := c.clients.HelmV2.Delete(ctx, ms); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).Error("Failed to delete MiniService")
			}
		}
	}

	return updateInfo, nil
}

func getStorageRequestsInstanceState(
	ctx context.Context,
	storageReqLister func() ([]*nvcav2beta1new.StorageRequest, error),
) (nvcatypes.ICMSInstanceState, error) {
	log := core.GetLogger(ctx)
	storageRequests, err := storageReqLister()
	if err != nil {
		log.WithError(err).Error("failed to list StorageRequest resources")
		return nvcatypes.ICMSInstanceStateNoStatus, err
	}

	// Return first non-running failed state
	for _, req := range storageRequests {
		if req.Status.Phase == nvcav2beta1new.StorageFailed || req.Status.Phase == nvcav2beta1new.StorageRuntimeError {
			switch req.Spec.Type {
			case nvcav2beta1new.SharedStorageRequest:
				return nvcatypes.ICMSInstanceSharedStorageFailure, nil
			case nvcav2beta1new.InternalPersistentStorageRequest:
				return nvcatypes.ICMSInstanceInternalPersistentStorageFailure, nil
			case nvcav2beta1new.ModelCacheRequest:
				// This is not an error.
			}
		}
	}
	// If this is empty or all succeeded
	return nvcatypes.ICMSInstanceRunning, nil
}

func (c *BackendK8sCache) ensureHelmChartRBAC(ctx context.Context, namespace string) (string, error) {
	log := core.GetLogger(ctx)

	rbacCM, err := c.clients.K8s.CoreV1().ConfigMaps(c.systemNamespace).
		Get(ctx, helmChartInstanceRBACConfigMapName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Errorf("Get Helm instance RBAC ConfigMap %s in namespace %s",
			helmChartInstanceRBACConfigMapName, c.systemNamespace)
		return "", err
	}

	return c.ensureHelmChartRBACFromConfigMap(ctx, namespace, rbacCM)
}

func (c *BackendK8sCache) ensureHelmChartRBACFromConfigMap(ctx context.Context, namespace string,
	rbacCM *corev1.ConfigMap) (string, error) {
	log := core.GetLogger(ctx)

	instanceSAName := miniservice.HelmChartInstanceServiceAccountName
	sa, err := c.clients.K8s.CoreV1().ServiceAccounts(namespace).Get(ctx, instanceSAName, metav1.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			log.WithError(err).Errorf("Get instance ServiceAccount %s in namespace %s", instanceSAName, namespace)
			return "", fmt.Errorf("get instance ServiceAccount in namespace %s: %v", namespace, err)
		}

		log.Debugf("Instance ServiceAccount %s in namespace %s does not exist, creating", instanceSAName, namespace)

		newSA := &corev1.ServiceAccount{}
		newSA.Name = instanceSAName
		newSA.Namespace = namespace
		sa, err = c.clients.K8s.CoreV1().ServiceAccounts(namespace).Create(ctx, newSA, metav1.CreateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Create instance ServiceAccount %s in namespace %s", instanceSAName, namespace)
			return "", fmt.Errorf("create instance ServiceAccount %s in namespace %s: %w", instanceSAName, namespace, err)
		}
	}

	rbacIface := c.clients.K8s.RbacV1()

	log.Debugf("Creating or updating instance Role %s in namespace %s", helmChartInstanceRoleName, namespace)

	roleYAML, ok := rbacCM.Data[helmChartInstanceRoleName]
	if !ok {
		return "", fmt.Errorf("expected ConfigMap %s key %s to exist, was empty",
			rbacCM.Name, helmChartInstanceRoleName)
	}

	role := &rbacv1.Role{}
	if err := yaml.Unmarshal([]byte(roleYAML), role); err != nil {
		return "", fmt.Errorf("unmarshal instance Role %s: %v", helmChartInstanceRoleName, err)
	}
	role.Name = helmChartInstanceRoleName
	role.Namespace = namespace

	newRole, err := rbacIface.Roles(namespace).Create(ctx, role, metav1.CreateOptions{})
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			log.WithError(err).Errorf("Create instance Role %s in namespace %s", helmChartInstanceRoleName, namespace)
			return "", fmt.Errorf("create instance Role in namespace %s: %v", namespace, err)
		}

		log.Debugf("Instance Role %s already exists in namespace %s, updating", helmChartInstanceRoleName, namespace)

		newRole, err = rbacIface.Roles(namespace).Update(ctx, role, metav1.UpdateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Update instance Role %s in namespace %s", helmChartInstanceRoleName, namespace)
			return "", fmt.Errorf("update existing instance Role in namespace %s: %v",
				namespace, err)
		}

		log.Debugf("Updated instance Role in namespace %s", namespace)
	} else {
		log.Debugf("Created instance Role in namespace %s", namespace)
	}

	role = newRole

	const instanceRBName = "helm-instance-permissions"
	_, err = c.clients.K8s.RbacV1().RoleBindings(namespace).Get(ctx, instanceRBName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.WithError(err).Errorf("Get instance RoleBinding %s in namespace %s", instanceRBName, namespace)
			return "", fmt.Errorf("get instance RoleBinding in namespace %s: %v", namespace, err)
		}

		log.Debugf("Instance RoleBinding %s in namespace %s does not exist, creating", instanceRBName, namespace)

		newRB := &rbacv1.RoleBinding{}
		newRB.Name = instanceRBName
		newRB.Namespace = namespace
		newRB.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		}
		newRB.Subjects = []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.Name,
			Namespace: namespace,
		}}
		newRB, err = c.clients.K8s.RbacV1().RoleBindings(namespace).Create(ctx, newRB, metav1.CreateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Create instance RoleBinding %s in namespace %s", newRB.Name, namespace)
			return "", fmt.Errorf("create instance RoleBinding in namespace %s: %v", namespace, err)
		}
	}

	return sa.Name, nil
}
