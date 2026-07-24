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

package validate

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

func expectations(o spec.Options) Expectations {
	return Expectations{
		Image:     o.CollectorImage,
		Resources: common.GetDefaultContainerResourcesBYOO(),
	}
}

func TestValidateContainerShapePasses(t *testing.T) {
	o := spec.DefaultOptions()
	res, err := render.Render(spec.ShapeContainer, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Render(res, expectations(o)); err != nil {
		t.Fatalf("expected valid container shape, got: %v", err)
	}
}

func TestValidateHelmShapePasses(t *testing.T) {
	o := spec.DefaultOptions()
	res, err := render.Render(spec.ShapeHelm, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Render(res, expectations(o)); err != nil {
		t.Fatalf("expected valid helm shape, got: %v", err)
	}
}

func TestValidateDetectsWrongImage(t *testing.T) {
	o := spec.DefaultOptions()
	res, err := render.Render(spec.ShapeContainer, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	exp := expectations(o)
	exp.Image = "registry.invalid/wrong:0.0.1"
	if err := Render(res, exp); err == nil {
		t.Error("expected validation to detect image mismatch")
	}
}

func TestValidateDetectsMissingPort(t *testing.T) {
	o := spec.DefaultOptions()
	res, err := render.Render(spec.ShapeContainer, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Simulate translator drift: drop all but the first collector port.
	res.Collector.Ports = res.Collector.Ports[:1]
	if err := Collector(res.Collector, res.OTelVersion, expectations(o)); err == nil {
		t.Error("expected validation to detect missing collector ports")
	}
}

func TestValidateDetectsMissingReadinessProbe(t *testing.T) {
	o := spec.DefaultOptions()
	res, err := render.Render(spec.ShapeContainer, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	res.Collector.ReadinessProbe = nil
	if err := Collector(res.Collector, res.OTelVersion, expectations(o)); err == nil {
		t.Error("expected validation to detect missing readiness probe")
	}
}
