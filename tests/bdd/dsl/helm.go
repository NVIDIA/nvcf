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
	"encoding/json"
	"fmt"
	"strings"
)

// HelmReleaseExpectation identifies a deployed Helm release and optionally
// pins the expected revision.
type HelmReleaseExpectation struct {
	Name      string
	Namespace string
	Revision  string
}

type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  any    `json:"revision"`
	Status    string `json:"status"`
}

// HelmRegistryLoginCommand builds a Helm OCI registry login command whose
// password must be supplied on stdin by the caller.
func HelmRegistryLoginCommand(registry string) (string, error) {
	registry = strings.TrimSpace(Interpolate(registry))
	if registry == "" {
		return "", fmt.Errorf("OCI registry is empty")
	}
	return strings.Join([]string{
		"helm", "registry", "login", quoteCommandArg(registry),
		"--username", quoteCommandArg("$oauthtoken"),
		"--password-stdin",
	}, " "), nil
}

// HelmListCommand builds an explicit-context, all-namespaces Helm list command.
func HelmListCommand(kubeContext string) (string, error) {
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	if kubeContext == "" {
		return "", fmt.Errorf("kube context is empty")
	}
	return "helm list --all-namespaces --kube-context " + quoteCommandArg(kubeContext) + " -o json", nil
}

// HelmReleaseValuesCommand builds an explicit-context Helm values read whose
// stdout is YAML suitable for subset matching.
func HelmReleaseValuesCommand(release, namespace, kubeContext string) (string, error) {
	release = strings.TrimSpace(Interpolate(release))
	namespace = strings.TrimSpace(Interpolate(namespace))
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	if release == "" {
		return "", fmt.Errorf("helm release name is empty")
	}
	if namespace == "" {
		return "", fmt.Errorf("helm release %q namespace is empty", release)
	}
	if kubeContext == "" {
		return "", fmt.Errorf("kube context is empty")
	}
	return strings.Join([]string{
		"helm", "get", "values", quoteCommandArg(release),
		"--namespace", quoteCommandArg(namespace),
		"--kube-context", quoteCommandArg(kubeContext),
		"-o", "yaml",
	}, " "), nil
}

// HelmReleasesDeployed asserts that every expected release exists in Helm's
// JSON output with status deployed and, when provided, the expected revision.
func HelmReleasesDeployed(raw string, expected []HelmReleaseExpectation) error {
	var actual []helmRelease
	if err := json.Unmarshal([]byte(raw), &actual); err != nil {
		return fmt.Errorf("parse helm list json: %w", err)
	}
	if len(expected) == 0 {
		return fmt.Errorf("expected Helm releases are empty")
	}

	for _, rawExpectation := range expected {
		expectation := HelmReleaseExpectation{
			Name:      strings.TrimSpace(Interpolate(rawExpectation.Name)),
			Namespace: strings.TrimSpace(Interpolate(rawExpectation.Namespace)),
			Revision:  strings.TrimSpace(Interpolate(rawExpectation.Revision)),
		}
		if expectation.Name == "" {
			return fmt.Errorf("helm release name is empty")
		}
		if expectation.Namespace == "" {
			return fmt.Errorf("helm release %q namespace is empty", expectation.Name)
		}

		release, found := findHelmRelease(actual, expectation.Name, expectation.Namespace)
		if !found {
			return describeMissingHelmRelease(actual, expectation)
		}
		if release.Status != "deployed" {
			return fmt.Errorf("helm release %q in namespace %q status = %q, want %q", expectation.Name, expectation.Namespace, release.Status, "deployed")
		}
		if expectation.Revision != "" && fmt.Sprint(release.Revision) != expectation.Revision {
			return fmt.Errorf("helm release %q in namespace %q revision = %q, want %q", expectation.Name, expectation.Namespace, fmt.Sprint(release.Revision), expectation.Revision)
		}
	}
	return nil
}

func findHelmRelease(releases []helmRelease, name, namespace string) (helmRelease, bool) {
	for _, release := range releases {
		if release.Name == name && release.Namespace == namespace {
			return release, true
		}
	}
	return helmRelease{}, false
}

func describeMissingHelmRelease(releases []helmRelease, expected HelmReleaseExpectation) error {
	for _, release := range releases {
		if release.Name == expected.Name {
			return fmt.Errorf("helm release %q namespace = %q, want %q", expected.Name, release.Namespace, expected.Namespace)
		}
	}
	return fmt.Errorf("helm release %q in namespace %q is missing", expected.Name, expected.Namespace)
}
