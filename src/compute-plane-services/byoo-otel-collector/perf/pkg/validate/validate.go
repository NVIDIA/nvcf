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

// Package validate is the render-shape validation gate. It asserts that the
// translated workload contains the BYOO collector with the expected placement,
// image, resources, ports, volumes, environment, probes, and config wiring. If
// the translator output drifts in a way that would break the test shape, this
// fails clearly so the suite never deploys or measures the wrong thing.
package validate

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

// Expectations describes what the collector container must look like.
type Expectations struct {
	// Image, when non-empty, is the exact collector image reference expected.
	Image string
	// Resources, when set, is the expected collector resource requirements.
	Resources corev1.ResourceRequirements
}

// requiredPorts are the collector container ports (name -> containerPort).
var requiredPorts = map[string]int32{
	"otlp-grpc":    14357,
	"otlp-http":    14358,
	"metrics":      18888,
	"health":       13133,
	"byoo-metrics": 19090,
}

// requiredMounts are the collector volume mounts (name -> mountPath).
var requiredMounts = map[string]string{
	"otel-config-data":           "/etc/otel",
	"otel-collector-secret-data": "/etc/byoo-otel-collector/secrets",
	"ess-data":                   "/config/ess-agent",
	"secret-data":                "/var/secrets",
}

const (
	healthProbePath = "/health"
	healthProbePort = 13133
)

// Render validates placement and the collector container for a rendered result.
// It returns a single aggregated error describing every problem found, or nil.
func Render(r *render.Result, exp Expectations) error {
	var errs []string

	switch r.Shape {
	case spec.ShapeContainer:
		if !r.HasContainer("inference") {
			errs = append(errs, `container shape: expected an "inference" container alongside the collector`)
		}
		if r.OwnerPod == common.UtilsPodName {
			errs = append(errs, fmt.Sprintf("container shape: collector unexpectedly placed on %q pod", common.UtilsPodName))
		}
	case spec.ShapeHelm:
		if r.OwnerPod != common.UtilsPodName {
			errs = append(errs, fmt.Sprintf("helm shape: collector expected on %q pod, found on %q", common.UtilsPodName, r.OwnerPod))
		}
		if r.Service == nil {
			errs = append(errs, "helm shape: expected a byoo-otel-collector OTLP Service")
		}
	default:
		errs = append(errs, fmt.Sprintf("unknown shape %q", r.Shape))
	}

	if err := Collector(r.Collector, r.OTelVersion, exp); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("render-shape validation failed for %s shape:\n  - %s", r.Shape, strings.Join(errs, "\n  - "))
	}
	return nil
}

// Collector validates a single BYOO collector container against expectations.
func Collector(c corev1.Container, otelVersion string, exp Expectations) error {
	var errs []string

	if c.Name != common.ByooOTelCollectorPodNameBase {
		errs = append(errs, fmt.Sprintf("container name = %q, want %q", c.Name, common.ByooOTelCollectorPodNameBase))
	}
	if exp.Image != "" && c.Image != exp.Image {
		errs = append(errs, fmt.Sprintf("image = %q, want %q", c.Image, exp.Image))
	}

	gotPorts := map[string]int32{}
	for _, p := range c.Ports {
		gotPorts[p.Name] = p.ContainerPort
	}
	for _, name := range sortedKeys(requiredPorts) {
		if gotPorts[name] != requiredPorts[name] {
			errs = append(errs, fmt.Sprintf("port %q = %d, want %d", name, gotPorts[name], requiredPorts[name]))
		}
	}

	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		errs = append(errs, "missing readiness probe HTTPGet handler")
	} else {
		if c.ReadinessProbe.HTTPGet.Path != healthProbePath {
			errs = append(errs, fmt.Sprintf("readiness probe path = %q, want %q", c.ReadinessProbe.HTTPGet.Path, healthProbePath))
		}
		if c.ReadinessProbe.HTTPGet.Port.IntValue() != healthProbePort {
			errs = append(errs, fmt.Sprintf("readiness probe port = %d, want %d", c.ReadinessProbe.HTTPGet.Port.IntValue(), healthProbePort))
		}
	}

	gotMounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		gotMounts[m.Name] = m.MountPath
	}
	for _, name := range sortedKeys(requiredMounts) {
		if gotMounts[name] != requiredMounts[name] {
			errs = append(errs, fmt.Sprintf("volume mount %q = %q, want %q", name, gotMounts[name], requiredMounts[name]))
		}
	}

	if exp.Resources.Requests != nil || exp.Resources.Limits != nil {
		errs = append(errs, resourceDiffs(c.Resources, exp.Resources)...)
	}

	envKeys := map[string]bool{}
	for _, e := range c.Env {
		envKeys[e.Name] = true
	}
	if !envKeys["OTEL_EXPORTER_OTLP_ENDPOINT"] {
		errs = append(errs, "missing env OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	switch otelVersion {
	case "v2":
		if !argsContain(c.Args, "--telemetries") {
			errs = append(errs, "v2 collector args missing --telemetries")
		} else if argValue(c.Args, "--telemetries") == "" {
			errs = append(errs, "v2 collector --telemetries payload is empty")
		}
		if !argsContain(c.Args, "--otel-config-path") {
			errs = append(errs, "v2 collector args missing --otel-config-path")
		}
	default:
		if !argsContain(c.Args, "--config") {
			errs = append(errs, "v1 collector args missing --config")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("collector container invalid:\n    - %s", strings.Join(errs, "\n    - "))
	}
	return nil
}

func resourceDiffs(got, want corev1.ResourceRequirements) []string {
	var errs []string
	for _, kind := range []struct {
		name string
		g, w corev1.ResourceList
	}{
		{"requests", got.Requests, want.Requests},
		{"limits", got.Limits, want.Limits},
	} {
		for _, res := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			wv, wok := kind.w[res]
			if !wok {
				continue
			}
			gv, gok := kind.g[res]
			if !gok {
				errs = append(errs, fmt.Sprintf("resources.%s.%s missing, want %s", kind.name, res, wv.String()))
				continue
			}
			if gv.Cmp(wv) != 0 {
				errs = append(errs, fmt.Sprintf("resources.%s.%s = %s, want %s", kind.name, res, gv.String(), wv.String()))
			}
		}
	}
	return errs
}

func argsContain(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// argValue returns the token following the given flag in a "--flag value" args
// slice, or "" if not present or has no value.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
