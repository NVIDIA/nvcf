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
