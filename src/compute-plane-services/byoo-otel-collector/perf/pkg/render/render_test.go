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

package render

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

func TestRenderContainerExtractsSidecar(t *testing.T) {
	res, err := Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Collector.Name != common.ByooOTelCollectorPodNameBase {
		t.Errorf("collector name = %q, want %q", res.Collector.Name, common.ByooOTelCollectorPodNameBase)
	}
	if res.Collector.Image != spec.DefaultCollectorImage {
		t.Errorf("collector image = %q, want %q", res.Collector.Image, spec.DefaultCollectorImage)
	}
	if res.OTelVersion != "v2" {
		t.Errorf("otel version = %q, want v2 for the default (>0.119.4) image", res.OTelVersion)
	}
	if res.OwnerPod != "0-perf" {
		t.Errorf("owner pod = %q, want %q", res.OwnerPod, "0-perf")
	}
	if !res.HasContainer("inference") {
		t.Error("container shape must include an inference container")
	}
	if res.Service != nil {
		t.Error("container shape should not produce an OTLP Service")
	}
}

func TestRenderHelmPlacesOnUtilsPodWithService(t *testing.T) {
	res, err := Render(spec.ShapeHelm, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.OwnerPod != common.UtilsPodName {
		t.Errorf("owner pod = %q, want %q", res.OwnerPod, common.UtilsPodName)
	}
	if res.Service == nil {
		t.Fatal("helm shape must produce an OTLP Service")
	}
	if res.Service.Name != common.ByooOTelCollectorPodNameBase {
		t.Errorf("service name = %q, want %q", res.Service.Name, common.ByooOTelCollectorPodNameBase)
	}
}

func TestBenchPodSuppliesVolumesForEveryMount(t *testing.T) {
	res, err := Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	pod := res.BenchPod("byoo-perf")
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("bench pod containers = %d, want 1 (authentic collector only)", len(pod.Spec.Containers))
	}

	vols := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		vols[v.Name] = true
	}
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if !vols[m.Name] {
			t.Errorf("collector mount %q has no backing volume in the bench pod", m.Name)
		}
	}
}

// BenchPod must carry the host pod's identity metadata (so the collector's
// downward-API env resolves), overlay only the suite's own label keys, and not
// alias Result's maps (mutating the pod must not mutate Result).
func TestBenchPodPropagatesOwnerMetadata(t *testing.T) {
	res, err := Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(res.OwnerLabels) == 0 {
		t.Fatal("expected owner labels captured from the translated pod")
	}

	pod := res.BenchPod("byoo-perf")

	// Every owner label survives unless a suite key intentionally overrides it.
	suiteKeys := map[string]bool{
		common.K8sAppNameLabelKey:              true,
		"app.kubernetes.io/part-of":            true,
		common.BYOOMetricsEgressTargetLabelKey: true,
	}
	for k, v := range res.OwnerLabels {
		if suiteKeys[k] {
			continue
		}
		if pod.Labels[k] != v {
			t.Errorf("owner label %q = %q on pod, want %q", k, pod.Labels[k], v)
		}
	}
	for k, v := range res.OwnerAnnotations {
		if pod.Annotations[k] != v {
			t.Errorf("owner annotation %q = %q on pod, want %q", k, pod.Annotations[k], v)
		}
	}

	// Suite labels are applied.
	if pod.Labels["app.kubernetes.io/part-of"] != "byoo-perf" {
		t.Errorf("suite part-of label = %q, want byoo-perf", pod.Labels["app.kubernetes.io/part-of"])
	}
	if pod.Labels[common.K8sAppNameLabelKey] != common.ByooOTelCollectorPodNameBase {
		t.Errorf("suite app-name label = %q, want %q", pod.Labels[common.K8sAppNameLabelKey], common.ByooOTelCollectorPodNameBase)
	}

	// Mutating the returned pod must not leak back into Result.
	pod.Labels["mutation-probe"] = "x"
	if _, leaked := res.OwnerLabels["mutation-probe"]; leaked {
		t.Error("mutating pod.Labels mutated Result.OwnerLabels (maps are aliased)")
	}
	if len(res.OwnerAnnotations) > 0 {
		pod.Annotations["mutation-probe"] = "x"
		if _, leaked := res.OwnerAnnotations["mutation-probe"]; leaked {
			t.Error("mutating pod.Annotations mutated Result.OwnerAnnotations (maps are aliased)")
		}
	}
}
