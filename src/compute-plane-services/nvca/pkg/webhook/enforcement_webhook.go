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

package webhook

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

var (
	_ admission.CustomDefaulter = (*podEnforcementWebhook)(nil)
)

const (
	pgpuResourceName                = corev1.ResourceName(nodefeatures.PGPUResourceKey)
	gpuSharedResourceName           = corev1.ResourceName(nodefeatures.GPUSharedResourceKey)
	gpuResourceName                 = corev1.ResourceName(nodefeatures.GPUResourceKey)
	enforcementMutatedAnnotationKey = "nvca.nvcf.nvidia.io/mutation-enforced"
	initMutatedAnnotationKey        = "nvca.nvcf.nvidia.io/init-mutation-enforced"
	containerMutatedAnnotationKey   = "nvca.nvcf.nvidia.io/container-mutation-enforced"
	dcgmMetricsPresentLabelKey      = "nvca.nvcf.nvidia.io/dcgm-metrics-present"
)

var gpuResourceNames = [3]corev1.ResourceName{pgpuResourceName, gpuSharedResourceName, gpuResourceName}

type EnforcementOptions struct {
	AttributeFetcher        featureflag.AttributeFetcher
	DCGMMetrics             DCGMMetricsConfig
	KataNonGPURTClassExists *atomic.Bool
}

type podEnforcementWebhook struct {
	EnforcementOptions
}

func (v *podEnforcementWebhook) Default(_ context.Context, obj runtime.Object) (err error) {
	var initMutated, contMutated, podMutated bool
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected *v1.Pod, got: %T", obj)
	}

	switch {
	case v.isEnforcementEnabled(featureflag.AttrKataRuntimeIsolation, pod):
		// kata has higher priority than other enforcements
		podMutated, initMutated, contMutated = enforceKata(pod, v.isNonGPURTClassExist())
	case v.isEnforcementEnabled(featureflag.AttrTimeSlicingGPUEnabled, pod):
		initMutated, contMutated = enforceSpecificGPUResource(pod, gpuSharedResourceName)
	case v.isEnforcementEnabled(featureflag.AttrPassthroughGPUEnabled, pod):
		initMutated, contMutated = enforceSpecificGPUResource(pod, pgpuResourceName)
	}

	// enforce DCGMMetrics annotation
	var dcgmMetadataMutated bool
	if v.isEnforcementEnabled(featureflag.AttrKataRuntimeIsolation, pod) ||
		v.isEnforcementEnabled(featureflag.AttrTimeSlicingGPUEnabled, pod) {
		dcgmMetadataMutated = enforceDCGMMetricsMetadata(pod, v.DCGMMetrics, gpuSharedResourceName)
	}
	podMutated = podMutated || dcgmMetadataMutated

	if podMutated || initMutated || contMutated {
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		if podMutated {
			pod.Annotations[enforcementMutatedAnnotationKey] = strconv.FormatBool(true)
		}
		if initMutated {
			pod.Annotations[initMutatedAnnotationKey] = strconv.FormatBool(true)
		}
		if contMutated {
			pod.Annotations[containerMutatedAnnotationKey] = strconv.FormatBool(true)
		}
	}

	return nil
}

func (v *podEnforcementWebhook) isEnforcementEnabled(attr *featureflag.Attribute, obj metav1.Object) bool {
	// Check both the fetcher, and existing pod enforcements label
	// in case NVCA was restarted with different enforcements.
	return v.AttributeFetcher.IsAttributeEnabled(attr) || enforce.GetEnforcements(obj).Enabled(attr)
}

func (v *podEnforcementWebhook) isNonGPURTClassExist() bool {
	return v.KataNonGPURTClassExists != nil && v.KataNonGPURTClassExists.Load()
}

func enforceKata(pod *corev1.Pod, hasNonGPURTClass bool) (bool, bool, bool) {
	var runtimeClassName string
	if !hasNonGPURTClass || doesPodRequestGPUResources(pod, pgpuResourceName) {
		runtimeClassName = kata.RuntimeClassNameGPU
	} else {
		runtimeClassName = kata.RuntimeClassNameNonGPU
	}
	pod.Spec.RuntimeClassName = &runtimeClassName

	initMutated, contMutated := enforceSpecificGPUResource(pod, pgpuResourceName)
	return true, initMutated, contMutated
}

// overwrite the existing map key if found as we are enforcing
func enforceDCGMMetricsMetadata(pod *corev1.Pod, dcgmMetrics DCGMMetricsConfig, specificGpuResourceName corev1.ResourceName) bool {
	mutationEnforced := false
	if !doesPodRequestGPUResources(pod, specificGpuResourceName) {
		return false
	}
	if pod.ObjectMeta.Annotations == nil { //nolint:staticcheck
		pod.ObjectMeta.Annotations = map[string]string{} //nolint:staticcheck
	}
	for k, v := range dcgmMetrics.Annotations {
		pod.ObjectMeta.Annotations[k] = v //nolint:staticcheck
		mutationEnforced = true
	}
	if len(dcgmMetrics.Annotations) > 0 {
		if pod.ObjectMeta.Labels == nil { //nolint:staticcheck
			pod.ObjectMeta.Labels = map[string]string{} //nolint:staticcheck
		}
		pod.ObjectMeta.Labels[dcgmMetricsPresentLabelKey] = strconv.FormatBool(true) //nolint:staticcheck
		// Append the dcgm-metrics port if it is not already specified
		enforceDCGMMetricsPort(pod, dcgmMetrics.ContainerPort, corev1.ProtocolTCP)
		mutationEnforced = true
	}
	return mutationEnforced
}

func enforceSpecificGPUResource(pod *corev1.Pod, specificGpuResourceName corev1.ResourceName) (bool, bool) {
	var initMutated, contMutated bool
	for _, container := range pod.Spec.InitContainers {
		if subSpecificGPUResourceList(container.Resources.Requests, specificGpuResourceName) {
			initMutated = true
		}
		if subSpecificGPUResourceList(container.Resources.Limits, specificGpuResourceName) {
			initMutated = true
		}
	}
	for _, container := range pod.Spec.Containers {
		if subSpecificGPUResourceList(container.Resources.Requests, specificGpuResourceName) {
			contMutated = true
		}
		if subSpecificGPUResourceList(container.Resources.Limits, specificGpuResourceName) {
			contMutated = true
		}
	}
	return initMutated, contMutated
}

func doesPodRequestGPUResources(pod *corev1.Pod, specificGpuResourceName corev1.ResourceName) bool {
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			if doesContainerRequestGPUResource(container, specificGpuResourceName) {
				return true
			}
		}
	}
	return false
}

func enforceDCGMMetricsPort(pod *corev1.Pod, metricsPort int32, metricsPortProtocol corev1.Protocol) {
	addDCGMMetricsTCPPort := func(container *corev1.Container) {
		// Check if the metrics port exists, if it does we're going to rename it to
		// dcgm-metrics
		dcgmMetricsPortIdx := -1
		portNames := map[string]bool{}
		for i, port := range container.Ports {
			// DCGM metrics port for 9400 and TCP
			if port.ContainerPort == metricsPort && port.Protocol == metricsPortProtocol {
				dcgmMetricsPortIdx = i
			} else {
				portNames[port.Name] = true
			}
		}

		// DCGM Port name find unique name if another port has a name collision with dcgm-metrics
		metricsPortName := "dcgm-metrics"
		var uniqueNameFound bool
		counter := 2
		for !uniqueNameFound {
			if _, ok := portNames[metricsPortName]; !ok {
				// unique name found end the loop
				uniqueNameFound = true
			} else {
				metricsPortName = fmt.Sprintf("dcgm-metrics-%d", counter)
				counter++
			}
		}

		// Replace the DCGM metrics port
		dcgmMetricsPort := corev1.ContainerPort{
			Name:          metricsPortName,
			ContainerPort: metricsPort,
			Protocol:      metricsPortProtocol,
		}
		if dcgmMetricsPortIdx >= 0 {
			container.Ports[dcgmMetricsPortIdx] = dcgmMetricsPort
		} else {
			container.Ports = append(container.Ports, dcgmMetricsPort)
		}
	}

	for i := range pod.Spec.InitContainers {
		if doesContainerRequestGPUResource(pod.Spec.InitContainers[i], pgpuResourceName) {
			addDCGMMetricsTCPPort(&pod.Spec.InitContainers[i])
		}
	}
	for i := range pod.Spec.Containers {
		if doesContainerRequestGPUResource(pod.Spec.Containers[i], pgpuResourceName) {
			addDCGMMetricsTCPPort(&pod.Spec.Containers[i])
		}
	}
}

func doesContainerRequestGPUResource(container corev1.Container, specificGpuResourceName corev1.ResourceName) bool {
	for _, resources := range []corev1.ResourceList{container.Resources.Requests, container.Resources.Limits} {
		for rk := range resources {
			if rk == specificGpuResourceName || isGpuResourceName(rk) {
				return true
			}
		}
	}
	return false
}

func isGpuResourceName(resourceKey corev1.ResourceName) bool {
	for _, v := range gpuResourceNames {
		if v == resourceKey {
			return true
		}
	}
	return false
}

// subSpecificGPUResourceList substitutes "gpu" resources for "pgpu" or "gpu.shared"
// so Pod specs originally written for non-Kata or non-shared workloads do not need modification.
// function returns boolean
//
//	true - if routine mutated
//	false - if routine DIDN'T have to mutate
func subSpecificGPUResourceList(rl corev1.ResourceList, specificGpuResourceName corev1.ResourceName) bool {
	for rk, rv := range rl {
		if rk == specificGpuResourceName {
			return false
		}

		// Assumes a homogeneous environment where each function subscribes to only one GPU resource type:
		// "gpu", "pgpu", or "gpu.shared". No mixing of resource types is considered.
		if isGpuResourceName(rk) {
			rl[specificGpuResourceName] = rv
			delete(rl, rk)
			return true
		}
	}
	return false
}
