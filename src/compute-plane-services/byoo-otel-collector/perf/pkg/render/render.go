/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Package render drives the shared SIS translation library to produce the
// production workload shape, then extracts the authentic BYOO collector for
// the performance suite. Rendering touches no cluster; deployment happens
// later (S5/S6).
package render

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

// CollectorContainerName is the container name the translator assigns the BYOO
// collector in every workload shape.
const CollectorContainerName = common.ByooOTelCollectorPodNameBase

// Result is the outcome of rendering a workload shape through the translator.
type Result struct {
	Shape   spec.Shape
	Options spec.Options

	// Objects is the full set of translated Kubernetes objects.
	Objects []metav1.Object
	// Collector is the authentic BYOO collector container, exactly as the
	// translator emitted it.
	Collector corev1.Container
	// OwnerPod is the name of the pod that hosts the collector.
	OwnerPod string
	// Service is the OTLP ClusterIP Service (Helm shape only; nil otherwise).
	Service *corev1.Service
	// OTelVersion is the collector config code path ("v1" or "v2") the
	// translator selected from the collector image tag.
	OTelVersion string
}

// Render builds the synthetic spec for the shape, runs it through
// function.Translate, and extracts the authentic collector container.
func Render(shape spec.Shape, o spec.Options) (*Result, error) {
	msg, err := spec.Message(shape, o)
	if err != nil {
		return nil, err
	}
	tcfg := spec.TranslateConfig(shape, o)

	objs, err := function.Translate(msg, tcfg)
	if err != nil {
		return nil, fmt.Errorf("translate %s function: %w", shape, err)
	}

	res := &Result{
		Shape:       shape,
		Options:     o,
		Objects:     objs,
		OTelVersion: common.OTelVersion(o.CollectorImage),
	}

	found := false
	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.Pod:
			for i := range t.Spec.Containers {
				if t.Spec.Containers[i].Name == CollectorContainerName {
					res.Collector = *t.Spec.Containers[i].DeepCopy()
					res.OwnerPod = t.Name
					found = true
				}
			}
		case *corev1.Service:
			if t.Name == common.ByooOTelCollectorPodNameBase {
				res.Service = t.DeepCopy()
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no %q container found in translated %s workload (are telemetries enabled?)", CollectorContainerName, shape)
	}
	return res, nil
}

// HasContainer reports whether any translated pod contains a container with the
// given name (init containers included).
func (r *Result) HasContainer(name string) bool {
	for _, obj := range r.Objects {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			continue
		}
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == name {
				return true
			}
		}
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == name {
				return true
			}
		}
	}
	return false
}

// BenchPod produces the deployable performance workload: the authentic
// collector container plus lightweight emptyDir stand-ins for every volume it
// mounts. The non-collector function containers (init, GPU inference, utils,
// ESS) are intentionally omitted since they cannot run on k3d and are not what
// the suite measures. Full deployment orchestration is added in S5/S6.
func (r *Result) BenchPod(namespace string) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Name = r.Options.ObjectNameBase + "-collector"
	pod.Namespace = namespace
	pod.Labels = map[string]string{
		common.K8sAppNameLabelKey:              common.ByooOTelCollectorPodNameBase,
		"app.kubernetes.io/part-of":            "byoo-perf",
		common.BYOOMetricsEgressTargetLabelKey: common.BYOOMetricsEgressTargetLabelValue,
	}

	collector := *r.Collector.DeepCopy()
	pod.Spec.Containers = []corev1.Container{collector}
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever

	seen := map[string]bool{}
	for _, vm := range collector.VolumeMounts {
		if seen[vm.Name] {
			continue
		}
		seen[vm.Name] = true
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name:         vm.Name,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}
	return pod
}
