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

package dsl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	observeWatchStargatesScript = "tests/bdd/scripts/observe-watch-stargates.sh"
	waitPylonMetricsScript      = "tests/bdd/scripts/wait-pylon-metrics.sh"
)

var prometheusMetricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// PylonMetricExpectation describes an expected count of connected metric
// series exposed by one Pylon sidecar.
type PylonMetricExpectation struct {
	Metric     string
	Comparison string
	Count      int
}

// WatchStargatesCommand builds a TLS WatchStargates observation with an
// explicit endpoint, authority, CA source, Kubernetes context, and duration.
func WatchStargatesCommand(endpoint, authority, caSecret, namespace, kubeContext, durationSeconds string) (string, error) {
	values := []*string{&endpoint, &authority, &caSecret, &namespace, &kubeContext, &durationSeconds}
	labels := []string{"endpoint", "TLS authority", "CA secret", "namespace", "kube context", "duration seconds"}
	for index := range values {
		*values[index] = strings.TrimSpace(Interpolate(*values[index]))
		if *values[index] == "" {
			return "", fmt.Errorf("%s is empty", labels[index])
		}
	}
	duration, err := strconv.Atoi(durationSeconds)
	if err != nil || duration <= 0 {
		return "", fmt.Errorf("duration seconds must be a positive integer")
	}

	return BuildCommand(
		"bash",
		observeWatchStargatesScript,
		endpoint,
		authority,
		caSecret,
		namespace,
		kubeContext,
		durationSeconds,
	), nil
}

// PylonMetricsCommand builds a Pylon metrics observation for every running
// Pylon pod selected by function name and container name in an explicit
// Kubernetes context and polling window.
func PylonMetricsCommand(functionName, containerName, kubeContext, timeout string, expectations []PylonMetricExpectation) (string, error) {
	functionName = strings.TrimSpace(Interpolate(functionName))
	containerName = strings.TrimSpace(Interpolate(containerName))
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	timeout = strings.TrimSpace(Interpolate(timeout))
	if functionName == "" {
		return "", fmt.Errorf("function name is empty")
	}
	if containerName == "" {
		return "", fmt.Errorf("container name is empty")
	}
	if kubeContext == "" {
		return "", fmt.Errorf("kube context is empty")
	}
	if timeout == "" {
		return "", fmt.Errorf("timeout is empty")
	}
	if len(expectations) == 0 {
		return "", fmt.Errorf("pylon metric expectations are empty")
	}

	args := []string{"bash", waitPylonMetricsScript, functionName, containerName, kubeContext, timeout}
	for index, expectation := range expectations {
		expectation.Metric = strings.TrimSpace(Interpolate(expectation.Metric))
		expectation.Comparison = strings.TrimSpace(Interpolate(expectation.Comparison))
		if !prometheusMetricNameRE.MatchString(expectation.Metric) {
			return "", fmt.Errorf("metric row %d has invalid metric name %q", index+1, expectation.Metric)
		}
		if expectation.Comparison != "exactly" && expectation.Comparison != "at least" {
			return "", fmt.Errorf("metric row %d comparison must be exactly or at least", index+1)
		}
		if expectation.Count < 0 {
			return "", fmt.Errorf("metric row %d count must be non-negative", index+1)
		}
		args = append(args, expectation.Metric, expectation.Comparison, strconv.Itoa(expectation.Count))
	}

	return BuildCommand(args...), nil
}
