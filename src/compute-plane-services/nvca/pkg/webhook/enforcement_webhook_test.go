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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

const enforcementsAnnotation = "nvca.nvcf.nvidia.io/enforcements"

func TestPodEnforcementWebhook_Default(t *testing.T) {
	// Define quantities
	one := *resource.NewQuantity(1, resource.DecimalSI)
	two := *resource.NewQuantity(2, resource.DecimalSI)
	three := *resource.NewQuantity(3, resource.DecimalSI)

	tests := []struct {
		name                    string
		obj                     runtime.Object
		enableKata              bool
		enableTimeSlicing       bool
		enablePassthroughGPU    bool
		annotations             map[string]string
		dcgmAnnotations         map[string]string
		kataNonGPURTClassExists *atomic.Bool
		containers              []v1.Container
		initContainers          []v1.Container
		expectedAnnotations     map[string]string
		expectedLabels          map[string]string
		expectedError           string
		expectedRuntimeClass    *string
		expectedResources       map[string]v1.ResourceName // map[containerName]expectedResourceName
		expectedQuantities      map[string]v1.ResourceList // map[containerName]expectedResourceList
	}{
		{
			name:       "Kata enforcement enabled with single init container",
			obj:        &v1.Pod{},
			enableKata: true,
			initContainers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				initMutatedAnnotationKey:        "true",
				enforcementMutatedAnnotationKey: "true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name:       "Kata enforcement enabled with single container",
			obj:        &v1.Pod{},
			enableKata: true,
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name:                    "Kata enforcement enabled with GPU containers and non-GPU runtimeclass",
			obj:                     &v1.Pod{},
			enableKata:              true,
			kataNonGPURTClassExists: newBoolAtomic(true),
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							gpuResourceName: resource.MustParse("1"),
						},
					},
				},
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: resource.MustParse("1"),
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:                    "Kata enforcement enabled with GPU containers and no non-GPU runtimeclass",
			obj:                     &v1.Pod{},
			enableKata:              true,
			kataNonGPURTClassExists: newBoolAtomic(false),
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: resource.MustParse("1"),
						},
						Limits: v1.ResourceList{
							gpuResourceName: resource.MustParse("1"),
						},
					},
				},
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: resource.MustParse("1"),
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:                    "Kata enforcement enabled with non-GPU containers and non-GPU runtimeclass",
			obj:                     &v1.Pod{},
			enableKata:              true,
			kataNonGPURTClassExists: newBoolAtomic(true),
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("100m"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("1000m"),
						},
					},
				},
			},
			expectedRuntimeClass: &kata.RuntimeClassNameNonGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": v1.ResourceCPU,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					v1.ResourceCPU: resource.MustParse("100m"),
				},
			},
			expectedAnnotations: map[string]string{
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:                    "Kata enforcement enabled with non-GPU containers and no non-GPU runtimeclass",
			obj:                     &v1.Pod{},
			enableKata:              true,
			kataNonGPURTClassExists: newBoolAtomic(false),
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("100m"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("1000m"),
						},
					},
				},
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": v1.ResourceCPU,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					v1.ResourceCPU: resource.MustParse("100m"),
				},
			},
			expectedAnnotations: map[string]string{
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:                 "GPU passthrough enforcement enabled with single container",
			obj:                  &v1.Pod{},
			enablePassthroughGPU: true,
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: nil,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey: "true",
			},
		},
		{
			name:              "Time slicing enforcement enabled with multiple containers",
			obj:               &v1.Pod{},
			enableTimeSlicing: true,
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: two,
						},
						Limits: v1.ResourceList{
							gpuResourceName: two,
						},
					},
				},
				{
					Name: "container2",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							pgpuResourceName: one,
						},
						Limits: v1.ResourceList{
							pgpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: nil,
			expectedResources: map[string]v1.ResourceName{
				"container1": gpuSharedResourceName,
				"container2": gpuSharedResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					gpuSharedResourceName: two,
				},
				"container2": {
					gpuSharedResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name:              "Both Kata and Time Slicing enabled, Kata takes precedence",
			obj:               &v1.Pod{},
			enableKata:        true,
			enableTimeSlicing: true,
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: three,
						},
						Limits: v1.ResourceList{
							gpuResourceName: three,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: three,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name: "No enforcement enabled",
			obj:  &v1.Pod{},
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			expectedRuntimeClass: nil,
			expectedResources: map[string]v1.ResourceName{
				"container1": gpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					gpuResourceName: one,
				},
			},
		},
		{
			name:          "Invalid object type",
			obj:           &v1.Service{},
			expectedError: "expected *v1.Pod, got: *v1.Service",
		},
		{
			name:       "Container without GPU resources",
			obj:        &v1.Pod{},
			enableKata: true,
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							"cpu": resource.MustParse("500m"),
						},
						Limits: v1.ResourceList{
							"cpu": resource.MustParse("500m"),
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": "cpu", // Should remain unchanged
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					"cpu": resource.MustParse("500m"),
				},
			},
			expectedAnnotations: map[string]string{
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:                 "Empty containers list",
			obj:                  &v1.Pod{},
			enableKata:           true,
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources:    map[string]v1.ResourceName{},
			expectedQuantities:   map[string]v1.ResourceList{},
			expectedAnnotations: map[string]string{
				enforcementMutatedAnnotationKey: "true",
			},
		},
		{
			name:       "NVCA restart with Kata disabled, while existing pod enabled in annotations",
			obj:        &v1.Pod{},
			enableKata: false,
			annotations: map[string]string{
				enforcementsAnnotation: "KataRuntimeIsolation=true",
			},
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				enforcementsAnnotation:          "KataRuntimeIsolation=true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name:              "NVCA restart with Time Slicing disabled, while existing pod enabled in annotations",
			obj:               &v1.Pod{},
			enableTimeSlicing: false,
			annotations: map[string]string{
				enforcementsAnnotation: "TimeSlicingGPUEnabled=true",
			},
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: nil,
			expectedResources: map[string]v1.ResourceName{
				"container1": gpuSharedResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					gpuSharedResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				enforcementsAnnotation:          "TimeSlicingGPUEnabled=true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name:       "NVCA restart with Kata enabled, while existing pod disabled in annotations",
			obj:        &v1.Pod{},
			enableKata: true,
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			annotations: map[string]string{
				enforcementsAnnotation: "KataRuntimeIsolation=false",
			},
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				enforcementsAnnotation:          "KataRuntimeIsolation=false",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
		{
			name: "NVCA restart with multiple enforcements in existing pod annotations",
			obj:  &v1.Pod{},
			annotations: map[string]string{
				enforcementsAnnotation: "KataRuntimeIsolation=true,TimeSlicingGPUEnabled=true",
			},
			containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							gpuResourceName: one,
						},
						Limits: v1.ResourceList{
							gpuResourceName: one,
						},
					},
				},
			},
			dcgmAnnotations: map[string]string{
				"dcgm-metrics": "true",
			},
			expectedRuntimeClass: &kata.RuntimeClassNameGPU,
			expectedResources: map[string]v1.ResourceName{
				"container1": pgpuResourceName,
			},
			expectedQuantities: map[string]v1.ResourceList{
				"container1": {
					pgpuResourceName: one,
				},
			},
			expectedAnnotations: map[string]string{
				containerMutatedAnnotationKey:   "true",
				enforcementMutatedAnnotationKey: "true",
				enforcementsAnnotation:          "KataRuntimeIsolation=true,TimeSlicingGPUEnabled=true",
				"dcgm-metrics":                  "true",
			},
			expectedLabels: map[string]string{
				dcgmMetricsPresentLabelKey: "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cast obj to pod and assign containers if applicable
			pod, ok := tt.obj.(*v1.Pod)
			if ok {
				pod.Spec.InitContainers = tt.initContainers
				pod.Spec.Containers = tt.containers
				pod.Annotations = tt.annotations
			}

			// Create webhook instance
			webhook := &podEnforcementWebhook{
				EnforcementOptions{
					AttributeFetcher: &mockAttrFetcher{
						attrEnabledFunc: func(a *featureflag.Attribute) bool {
							return (tt.enableKata && a.Key == featureflag.AttrKataRuntimeIsolation.Key) ||
								(tt.enablePassthroughGPU && a.Key == featureflag.AttrPassthroughGPUEnabled.Key) ||
								(tt.enableTimeSlicing && a.Key == featureflag.AttrTimeSlicingGPUEnabled.Key)
						},
					},
					KataNonGPURTClassExists: tt.kataNonGPURTClassExists,
				},
			}
			webhook.DCGMMetrics = DCGMMetricsConfig{
				Annotations: tt.dcgmAnnotations,
			}

			// Invoke Default method
			err := webhook.Default(context.TODO(), tt.obj)

			// Error handling
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Equal(t, tt.expectedError, err.Error())
				return
			}

			assert.NoError(t, err)

			// Skip test for non-pod obj
			if !ok {
				return
			}

			// check the expected annotations
			assert.Equal(t, tt.expectedAnnotations, pod.Annotations)
			assert.Equal(t, tt.expectedLabels, pod.Labels)

			// Check RuntimeClassNameGPU
			assert.Equal(t, tt.expectedRuntimeClass, pod.Spec.RuntimeClassName)

			// Check resources in each container
			for _, container := range pod.Spec.Containers {
				expectedResourceName, exists := tt.expectedResources[container.Name]
				assert.True(t, exists, "No expected resource specified for container %s", container.Name)

				if expectedResourceName == "cpu" {
					// Ensure CPU resources remain unchanged
					_, cpuExists := container.Resources.Requests["cpu"]
					assert.True(t, cpuExists, "CPU request should exist")
					_, gpuExists := container.Resources.Requests[gpuResourceName]
					assert.False(t, gpuExists, "GPU request should not exist")
					continue
				}

				// Check that GPU resources have been correctly substituted
				_, requestExists := container.Resources.Requests[expectedResourceName]
				assert.True(t, requestExists, "Expected GPU request resource not found for container %s", container.Name)
				_, limitExists := container.Resources.Limits[expectedResourceName]
				assert.True(t, limitExists, "Expected GPU limit resource not found for container %s", container.Name)

				// Ensure original GPU resource names are removed
				for _, originalName := range gpuResourceNames {
					if originalName == expectedResourceName {
						continue
					}
					_, requestExists := container.Resources.Requests[originalName]
					assert.False(t, requestExists, "Original GPU request resource %s should be removed for container %s", originalName, container.Name)
					_, limitExists := container.Resources.Limits[originalName]
					assert.False(t, limitExists, "Original GPU limit resource %s should be removed for container %s", originalName, container.Name)
				}

				// Check the actual quantity values in Requests and Limits
				expectedQuantities, exists := tt.expectedQuantities[container.Name]
				assert.True(t, exists, "No expected quantity specified for container %s", container.Name)

				for resourceName, expectedQuantity := range expectedQuantities {
					actualRequestQuantity := container.Resources.Requests[resourceName]
					assert.Equal(t, expectedQuantity.String(), actualRequestQuantity.String(), "Request quantity mismatch for container %s, resource %s", container.Name, resourceName)

					actualLimitQuantity := container.Resources.Limits[resourceName]
					assert.Equal(t, expectedQuantity.String(), actualLimitQuantity.String(), "Limit quantity mismatch for container %s, resource %s", container.Name, resourceName)
				}
			}
		})
	}
}

func TestEnforceDCGMMetricsPort(t *testing.T) {
	metricsPort := int32(9400)
	tests := []struct {
		name            string
		metricsPortName string
		containerPorts  []v1.ContainerPort
	}{
		{
			name:            "Container with existing metrics port",
			metricsPortName: "dcgm-metrics",
			containerPorts: []v1.ContainerPort{
				{
					Name:          "dcgm-metrics",
					ContainerPort: metricsPort,
					Protocol:      v1.ProtocolTCP,
				},
			},
		},
		{
			name:            "Container without existing metrics port",
			metricsPortName: "dcgm-metrics",
		},
		{
			name:            "Container with existing metrics port and name collision",
			metricsPortName: "dcgm-metrics",
			containerPorts: []v1.ContainerPort{
				{
					Name:          "dcgm-metrics",
					ContainerPort: metricsPort,
					Protocol:      v1.ProtocolTCP,
				},
			},
		},
		{
			name:            "Container with existing metrics port and name collision",
			metricsPortName: "dcgm-metrics-3",
			containerPorts: []v1.ContainerPort{
				{
					Name:          "dcgm-metrics",
					ContainerPort: metricsPort + 1,
					Protocol:      v1.ProtocolTCP,
				},
				{
					Name:          "dcgm-metrics-2",
					ContainerPort: metricsPort + 2,
					Protocol:      v1.ProtocolTCP,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "test-container",
							Ports: tt.containerPorts,
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									pgpuResourceName: resource.MustParse("1"),
								},
							},
						},
					},
				},
			}
			enforceDCGMMetricsPort(pod, metricsPort, v1.ProtocolTCP)
			assert.Condition(t, func() (success bool) {
				for _, container := range pod.Spec.Containers {
					for _, port := range container.Ports {
						if port.ContainerPort == metricsPort && port.Protocol == v1.ProtocolTCP {
							return port.Name == tt.metricsPortName
						}
					}
				}
				return false
			})
		})
	}
}

type mockAttrFetcher struct {
	attrEnabledFunc func(*featureflag.Attribute) bool
}

func (f *mockAttrFetcher) IsAttributeEnabled(ff *featureflag.Attribute) bool {
	return f.attrEnabledFunc(ff)
}

func newBoolAtomic(v bool) *atomic.Bool {
	b := &atomic.Bool{}
	b.Store(v)
	return b
}
