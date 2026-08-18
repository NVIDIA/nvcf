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

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
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
	// OwnerLabels and OwnerAnnotations are the host pod's metadata. The
	// collector reads its identity env (NVCF_FUNCTION_ID, nca-id, etc.) via
	// downward-API fieldRefs into these, so the bench pod must carry them or
	// the collector's startup config generator fails on empty IDs.
	OwnerLabels      map[string]string
	OwnerAnnotations map[string]string
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
					if found {
						return nil, fmt.Errorf("found multiple %q containers in translated %s workload", CollectorContainerName, shape)
					}
					res.Collector = *t.Spec.Containers[i].DeepCopy()
					res.OwnerPod = t.Name
					res.OwnerLabels = copyStringMap(t.Labels)
					res.OwnerAnnotations = copyStringMap(t.Annotations)
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

// copyStringMap returns a shallow copy of m, always non-nil so callers can
// safely write into the result.
func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
	}
	pod.Name = r.Options.ObjectNameBase + "-collector"
	pod.Namespace = namespace
	// Seed with the host pod's identity metadata so the collector's downward-API
	// env (function-id/version-id labels, nca-id/function-name annotations)
	// resolves, then overlay the suite's own labels.
	pod.Labels = copyStringMap(r.OwnerLabels)
	pod.Annotations = copyStringMap(r.OwnerAnnotations)
	pod.Labels[common.K8sAppNameLabelKey] = common.ByooOTelCollectorPodNameBase
	pod.Labels["app.kubernetes.io/part-of"] = "byoo-perf"
	pod.Labels[common.BYOOMetricsEgressTargetLabelKey] = common.BYOOMetricsEgressTargetLabelValue

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
