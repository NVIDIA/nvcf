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
	"strings"
)

// KubernetesResource identifies one resource by kind and name.
type KubernetesResource struct {
	Kind string
	Name string
}

// KubernetesResourceGetCommand builds an explicit-context kubectl get for one
// resource. ignoreNotFound makes a missing resource produce empty name output.
func KubernetesResourceGetCommand(namespace, kubeContext string, resource KubernetesResource, ignoreNotFound bool) (string, error) {
	namespace = strings.TrimSpace(Interpolate(namespace))
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	kind := strings.TrimSpace(Interpolate(resource.Kind))
	name := strings.TrimSpace(Interpolate(resource.Name))
	if namespace == "" {
		return "", fmt.Errorf("namespace is empty")
	}
	if kubeContext == "" {
		return "", fmt.Errorf("kube context is empty")
	}
	if kind == "" {
		return "", fmt.Errorf("kubernetes resource kind is empty")
	}
	if name == "" {
		return "", fmt.Errorf("kubernetes resource name is empty")
	}

	args := []string{
		"kubectl", "get", quoteCommandArg(strings.ToLower(kind) + "/" + name),
		"--namespace", quoteCommandArg(namespace),
		"--context", quoteCommandArg(kubeContext),
	}
	if ignoreNotFound {
		args = append(args, "--ignore-not-found")
	}
	args = append(args, "-o", "name")
	return strings.Join(args, " "), nil
}

// KubernetesResourceAbsent requires empty output from an ignore-not-found get.
func KubernetesResourceAbsent(raw string, resource KubernetesResource) error {
	if strings.TrimSpace(raw) != "" {
		return fmt.Errorf("kubernetes resource %s/%s exists, want absent", resource.Kind, resource.Name)
	}
	return nil
}

// KubectlApplyCommand builds a kubectl apply command for a manifest file.
// When kubeContext is set, the command always targets that context instead of
// relying on the caller's ambient kubeconfig selection.
// For example: kubectl --context k3d-ncp-local-compute-1 apply -f secret.yaml.
func KubectlApplyCommand(manifestPath, kubeContext string) (string, error) {
	manifestPath = strings.TrimSpace(manifestPath)
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	if manifestPath == "" {
		return "", fmt.Errorf("manifest path is empty")
	}

	args := []string{"kubectl"}
	if kubeContext != "" {
		args = append(args, "--context", quoteCommandArg(kubeContext))
	}
	args = append(args, "apply", "-f", quoteCommandArg(manifestPath))
	return strings.Join(args, " "), nil
}

func quoteCommandArg(value string) string {
	if isCommandArgSafe(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isCommandArgSafe(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			strings.ContainsRune("_./:@%+=,-", char) {
			continue
		}
		return false
	}
	return true
}
