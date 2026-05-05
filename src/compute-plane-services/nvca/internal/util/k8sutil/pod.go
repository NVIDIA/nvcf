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

package k8sutil

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
)

const (
	InstanceIDEnvKey              = "INSTANCE_ID"
	UnexpectedAdmissionErrReason  = "UnexpectedAdmissionError"
	ImagePullIssueReason          = "ErrImagePull"
	ImagePullIssueAlternateReason = "ImagePullBackOff"
	RestartCountToFailInstance    = int32(3)
)

// IsPodReady returns true if a pod is ready; false otherwise.
func IsPodReady(podStatus corev1.PodStatus) bool {
	return isPodReadyConditionTrue(podStatus)
}

// IsUtilsPod returns true if pod either has the static "utils" name,
// for Helm functions, or contains the worker container, for container functions.
func IsUtilsPod(pod *corev1.Pod) bool {
	if pod.Name == common.UtilsPodName && IsMiniServiceNamespaceName(pod.Namespace) {
		return true
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == common.UtilsContainerName || c.Name == function.LLMWorkerContainerName {
			for _, env := range c.Env {
				// Only the infrastructure worker container will have this env set.
				if env.Name == InstanceIDEnvKey {
					return true
				}
			}
			return false
		}
	}
	return false
}

// IsPodReadyConditionTrue returns true if a pod is ready; false otherwise.
func isPodReadyConditionTrue(status corev1.PodStatus) bool {
	condition := getPodReadyCondition(status)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// GetPodReadyCondition extracts the pod ready condition from the given status and returns that.
// Returns nil if the condition is not present.
func getPodReadyCondition(status corev1.PodStatus) *corev1.PodCondition {
	_, condition := getPodCondition(&status, corev1.PodReady)
	return condition
}

// GetPodCondition extracts the provided condition from the given status and returns that.
// Returns nil and -1 if the condition is not present, and the index of the located condition.
func getPodCondition(status *corev1.PodStatus, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	return getPodConditionFromList(status.Conditions, conditionType)
}

// GetPodConditionFromList extracts the provided condition from the given list of condition and
// returns the index of the condition and the condition. Returns -1 and nil if the condition is not present.
func getPodConditionFromList(conditions []corev1.PodCondition, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if conditions == nil {
		return -1, nil
	}
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return i, &conditions[i]
		}
	}
	return -1, nil
}

func IsPodScheduled(ps corev1.PodStatus) bool {
	for _, cond := range ps.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func IsPodAdmissionRejected(ps corev1.PodStatus) (bool, string) {
	if strings.EqualFold(ps.Reason, UnexpectedAdmissionErrReason) {
		return true, UnexpectedAdmissionErrReason
	}
	return false, ""
}

func ImagePullIssuesReported(ps corev1.PodStatus) (string, corev1.ContainerStateWaiting, bool) {
	for _, cs := range append(ps.InitContainerStatuses, ps.ContainerStatuses...) {
		if cs.State.Waiting != nil && (strings.EqualFold(cs.State.Waiting.Reason, ImagePullIssueReason) ||
			strings.EqualFold(cs.State.Waiting.Reason, ImagePullIssueAlternateReason)) {
			return cs.Image, *cs.State.Waiting, true
		}
	}
	return "", corev1.ContainerStateWaiting{}, false
}

func ParseImageRegistry(imageTag string) (reg string) {
	parts := strings.SplitN(imageTag, "/", 2)
	if len(parts) == 2 && (strings.ContainsRune(parts[0], '.') || strings.ContainsRune(parts[0], ':')) {
		// The first part of the repository is treated as the registry domain
		// iff it contains a '.' or ':' character, otherwise it is all repository
		// and the domain defaults to Docker Hub.
		reg = parts[0]
	} else if imageTag != "" {
		// No registry means it is docker.
		reg = "docker.io"
	}
	return reg
}

func IsPodDegraded(pod *corev1.Pod, k8sTimeConfig *TimeConfig) (bool, string) {
	status := pod.Status
	containersNotReady := false
	podNotReady := false
	podInitialized := false
	wdp := k8sTimeConfig.WorkerDegradationTimeout
	// If the pod is in the initial startup period
	// use the startup timeout instead of worker degradation timeout
	if IsPodInInitialStartup(status) {
		wdp = k8sTimeConfig.WorkerStartupTimeout
	}
	var containersNotReadyReason string
	var podNotReadyCond corev1.PodCondition
	for _, cond := range status.Conditions {
		if cond.Type == corev1.ContainersReady && cond.Status == corev1.ConditionFalse {
			containersNotReady = true
			containersNotReadyReason = cond.Reason
		}
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionFalse {
			podNotReady = true
			podNotReadyCond = cond
		}

		if cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue {
			podInitialized = true
		}
	}

	if containersNotReady && podNotReady && podInitialized {
		isNonRestartableContainerTerminated := pod.Spec.RestartPolicy == corev1.RestartPolicyNever &&
			slices.IndexFunc(status.ContainerStatuses, func(cs corev1.ContainerStatus) bool {
				return cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0
			}) != -1
		isWDPPassed := !podNotReadyCond.LastTransitionTime.IsZero() && time.Since(podNotReadyCond.LastTransitionTime.Time) > wdp
		if isNonRestartableContainerTerminated || isWDPPassed {
			return true, containersNotReadyReason
		}
	}
	return false, ""
}

func IsPodInInitialStartup(status corev1.PodStatus) bool {
	if status.StartTime == nil {
		return true
	}

	// Iterate through the conditions and check if the pod ready condition has not started
	// and the last transition time is within a second of the lastTransitionTime
	if podReadyCondition := getPodReadyCondition(status); podReadyCondition != nil && podReadyCondition.Status != corev1.ConditionTrue {
		maxLastTransitionTime := status.StartTime.Add(time.Second)
		return !maxLastTransitionTime.Before(podReadyCondition.LastTransitionTime.Time)
	}

	return false
}

func IsPodStuckInitializing(pod *corev1.Pod, k8sTimeConfig *TimeConfig) (bool, string) {
	podStatus := pod.Status
	initConditionTrue := false
	for _, cond := range podStatus.Conditions {
		if cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue {
			initConditionTrue = true
			break
		}
	}
	if initConditionTrue {
		for _, containerStatus := range podStatus.ContainerStatuses {
			if containerStatus.RestartCount >= RestartCountToFailInstance &&
				IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts) {
				reason := "ContainerInRestartLoop"
				switch {
				case containerStatus.LastTerminationState.Terminated != nil:
					reason = containerStatus.LastTerminationState.Terminated.Reason
				case containerStatus.LastTerminationState.Waiting != nil:
					reason = containerStatus.LastTerminationState.Waiting.Reason
				}
				return true, reason
			}
		}
	} else {
		for _, containerStatus := range podStatus.InitContainerStatuses {
			if containerStatus.RestartCount >= RestartCountToFailInstance &&
				IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts) {
				reason := "InitContainerInRestartLoop"
				switch {
				case containerStatus.LastTerminationState.Terminated != nil:
					reason = containerStatus.LastTerminationState.Terminated.Reason
				case containerStatus.LastTerminationState.Waiting != nil:
					reason = containerStatus.LastTerminationState.Waiting.Reason
				}
				return true, reason
			}
		}
	}
	// Pod seems to be good otherwise
	// Just check if the Pod's init container is stuck for > 120minutes
	if !initConditionTrue {
		if IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdMinutesOnInitFailure) {
			return true, "ContainerStuckAfterThreshold"
		}
	}
	return false, ""
}

func IsTimeSincePodLaunchedLaterThan(pod *corev1.Pod, dtc time.Duration) bool {
	ps := pod.Status
	now := time.Now()
	if ps.StartTime != nil && !ps.StartTime.IsZero() {
		return IsOverTimeout(*ps.StartTime, dtc, now)
	}

	// If a Pod is not scheduled due for whatever reason, kubelet will not set the StartTime.
	// Rely on scheduled=false condition's transition time to check if pod has launched within timeout.
	for _, cond := range ps.Conditions {
		if cond.Type == corev1.PodScheduled {
			return cond.Status == corev1.ConditionFalse &&
				!cond.LastTransitionTime.IsZero() &&
				IsOverTimeout(cond.LastTransitionTime, dtc, now)
		}
	}
	// If there is no scheduled condition, rely on creation timestamp.
	return !pod.CreationTimestamp.IsZero() && IsOverTimeout(pod.CreationTimestamp, dtc, now)
}

func IsOverTimeout(t metav1.Time, timeoutDuration time.Duration, now time.Time) bool {
	return !t.Add(timeoutDuration).After(now)
}

func AddEnvsToContainers(cs []corev1.Container, envs ...corev1.EnvVar) bool {
	return common.AddEnvsToContainers(cs, envs...)
}

func AddEnvsToContainer(c *corev1.Container, inEnvs ...corev1.EnvVar) bool {
	return common.AddEnvsToContainer(c, inEnvs...)
}

func MergePodSpecTolerations(ps *corev1.PodSpec, tolerations ...corev1.Toleration) bool {
	return common.MergeTolerations(ps, tolerations...)
}

func HasContainerNamed(containers []corev1.Container, name string) bool {
	// Search the slice of containers for one using the provided "name"
	for _, c := range containers {
		if c.Name == name {
			return true
		}
	}

	return false
}

// ValidateAllContainerResourcesSet is a sanity check to ensure all containers
// have resource limits set for required resources.
func ValidateAllContainerResourcesSet(pod *corev1.Pod) error {
	var valErrs []string
	for _, c := range pod.Spec.InitContainers {
		if badStr := validateContainerResourcesSet(c); badStr != "" {
			valErrs = append(valErrs, fmt.Sprintf("init container %q: %s", c.Name, badStr))
		}
	}
	for _, c := range pod.Spec.Containers {
		if badStr := validateContainerResourcesSet(c); badStr != "" {
			valErrs = append(valErrs, fmt.Sprintf("container %q: %s", c.Name, badStr))
		}
	}
	if len(valErrs) != 0 {
		return fmt.Errorf("resources validation failed for Pod %q: %q", pod.Name, valErrs)
	}
	return nil
}

func validateContainerResourcesSet(c corev1.Container) string {
	// Limits will be defaulted to requests inline if not set.
	if len(c.Resources.Requests) == 0 && len(c.Resources.Limits) == 0 {
		return "no requests or limits"
	}
	resources := c.Resources.Limits
	if len(resources) == 0 {
		resources = c.Resources.Requests
	}
	var noLims []corev1.ResourceName
	for _, rn := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if rq, ok := resources[rn]; !ok || rq.IsZero() {
			noLims = append(noLims, rn)
		}
	}
	if len(noLims) != 0 {
		return fmt.Sprintf("resources not set: %q", noLims)
	}
	return ""
}

// PodSpecRequestsGPU returns true if any container in the PodSpec requests GPU resources.
// If no gpuResourceNames are provided, nodefeatures.GPUResourceNames is used.
func PodSpecRequestsGPU(ps *corev1.PodSpec, gpuResourceNames ...corev1.ResourceName) bool {
	if len(gpuResourceNames) == 0 {
		gpuResourceNames = nodefeatures.GPUResourceNames
	}
	//nolint:gocritic // This is a valid use of append
	allContainers := append(ps.Containers, ps.InitContainers...)
	for _, c := range allContainers {
		for _, gpuResourceName := range gpuResourceNames {
			if qty, ok := c.Resources.Requests[gpuResourceName]; ok && !qty.IsZero() {
				return true
			}
			if qty, ok := c.Resources.Limits[gpuResourceName]; ok && !qty.IsZero() {
				return true
			}
		}
	}
	return false
}
