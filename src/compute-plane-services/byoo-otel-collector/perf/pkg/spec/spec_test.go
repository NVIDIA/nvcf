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

package spec

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestContainerMessageEnvironment(t *testing.T) {
	o := DefaultOptions()
	msg, err := Message(ShapeContainer, o)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if msg.LaunchSpecification == nil {
		t.Fatal("launch specification is nil")
	}
	if msg.LaunchSpecification.HelmChartLaunchSpecification != nil {
		t.Error("container message should not carry a helm chart launch spec")
	}
	if msg.LaunchSpecification.Telemetries == nil {
		t.Error("telemetries should be set so the collector is injected")
	}

	env := msg.LaunchSpecification.EnvironmentB64
	for _, key := range []string{
		common.ContainerFunctionImageEnv,
		common.UtilsImageEnv,
		common.InitImageEnv,
		common.BYOOOTelCollectorImageEnv,
	} {
		v, err := common.GetEncodedVarByKey(env, key)
		if err != nil {
			t.Fatalf("decode env %s: %v", key, err)
		}
		if v == "" {
			t.Errorf("env %s is empty", key)
		}
	}

	got, _ := common.GetEncodedVarByKey(env, common.BYOOOTelCollectorImageEnv)
	if got != o.CollectorImage {
		t.Errorf("collector image env = %q, want %q", got, o.CollectorImage)
	}
}

func TestHelmMessageEnvironment(t *testing.T) {
	o := DefaultOptions()
	msg, err := Message(ShapeHelm, o)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if msg.LaunchSpecification.HelmChartLaunchSpecification == nil {
		t.Fatal("helm message must carry a helm chart launch spec")
	}
	if msg.LaunchSpecification.HelmChartLaunchSpecification.HelmChartURL == "" {
		t.Error("helm chart URL is empty")
	}

	// Helm functions do not carry an inference container image.
	env := msg.LaunchSpecification.EnvironmentB64
	if v, _ := common.GetEncodedVarByKey(env, common.ContainerFunctionImageEnv); v != "" {
		t.Errorf("helm message unexpectedly set inference image = %q", v)
	}
	if v, _ := common.GetEncodedVarByKey(env, common.BYOOOTelCollectorImageEnv); v == "" {
		t.Error("helm message missing collector image env")
	}
}

func TestUnknownShapeErrors(t *testing.T) {
	if _, err := Message("bogus", DefaultOptions()); err == nil {
		t.Error("expected error for unknown shape")
	}
}

func TestTranslateConfigGPUOnlyForContainer(t *testing.T) {
	o := DefaultOptions()

	c := TranslateConfig(ShapeContainer, o)
	if len(c.WorkloadResources.Limits) == 0 {
		t.Error("container translate config must request a GPU resource")
	}

	h := TranslateConfig(ShapeHelm, o)
	if len(h.WorkloadResources.Limits) != 0 {
		t.Error("helm translate config should not set workload GPU resources")
	}
}
