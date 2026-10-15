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

// GatewayAPIRoute identifies one Gateway API route and the Gateway parent
// whose status conditions must be ready. The kind remains caller-supplied so
// the helper works with every route kind supported by the installed Gateway
// API implementation.
type GatewayAPIRoute struct {
	Kind      string
	Name      string
	Namespace string
	Parent    string
}

// GatewayAPIRouteWait describes one command in a route-readiness plan.
type GatewayAPIRouteWait struct {
	Row       int
	Route     GatewayAPIRoute
	Condition string
	Command   string
}

type kubernetesWaitTarget struct {
	name        string
	namespace   string
	kubeContext string
	timeout     string
}

// KubernetesResourceGetCommand builds an explicit-context kubectl get for one
// resource. ignoreNotFound makes a missing resource produce empty name output.
func KubernetesResourceGetCommand(namespace, kubeContext string, resource KubernetesResource, ignoreNotFound bool) (string, error) {
	namespace, kubeContext, resource, err := resolveKubernetesResource(namespace, kubeContext, resource)
	if err != nil {
		return "", err
	}

	args := []string{
		"kubectl", "get", quoteCommandArg(strings.ToLower(resource.Kind) + "/" + resource.Name),
		"--namespace", quoteCommandArg(namespace),
		"--context", quoteCommandArg(kubeContext),
	}
	if ignoreNotFound {
		args = append(args, "--ignore-not-found")
	}
	args = append(args, "-o", "name")
	return strings.Join(args, " "), nil
}

// KubernetesResourceYAMLGetCommand builds an explicit-context kubectl get
// whose stdout is the named resource serialized as YAML.
func KubernetesResourceYAMLGetCommand(namespace, kubeContext string, resource KubernetesResource) (string, error) {
	namespace, kubeContext, resource, err := resolveKubernetesResource(namespace, kubeContext, resource)
	if err != nil {
		return "", err
	}
	args := []string{
		"kubectl", "get", quoteCommandArg(strings.ToLower(resource.Kind) + "/" + resource.Name),
		"--namespace", quoteCommandArg(namespace),
		"--context", quoteCommandArg(kubeContext),
		"-o", "yaml",
	}
	return strings.Join(args, " "), nil
}

func resolveKubernetesResource(namespace, kubeContext string, resource KubernetesResource) (string, string, KubernetesResource, error) {
	namespace = strings.TrimSpace(Interpolate(namespace))
	kubeContext = strings.TrimSpace(Interpolate(kubeContext))
	resource.Kind = strings.TrimSpace(Interpolate(resource.Kind))
	resource.Name = strings.TrimSpace(Interpolate(resource.Name))
	if namespace == "" {
		return "", "", KubernetesResource{}, fmt.Errorf("namespace is empty")
	}
	if kubeContext == "" {
		return "", "", KubernetesResource{}, fmt.Errorf("kube context is empty")
	}
	if resource.Kind == "" {
		return "", "", KubernetesResource{}, fmt.Errorf("kubernetes resource kind is empty")
	}
	if resource.Name == "" {
		return "", "", KubernetesResource{}, fmt.Errorf("kubernetes resource name is empty")
	}
	return namespace, kubeContext, resource, nil
}

// KubernetesResourceAbsent requires empty output from an ignore-not-found get.
func KubernetesResourceAbsent(raw string, resource KubernetesResource) error {
	if strings.TrimSpace(raw) != "" {
		return fmt.Errorf("kubernetes resource %s/%s exists, want absent", resource.Kind, resource.Name)
	}
	return nil
}

// KubernetesDeploymentRolloutCommand builds an explicit-context rollout wait
// for one deployment.
func KubernetesDeploymentRolloutCommand(name, namespace, kubeContext, timeout string) (string, error) {
	target, err := resolveKubernetesWaitTarget("deployment", name, namespace, kubeContext, timeout)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"kubectl", "rollout", "status", quoteCommandArg("deployment/" + target.name),
		"-n", quoteCommandArg(target.namespace),
		"--context", quoteCommandArg(target.kubeContext),
		quoteCommandArg("--timeout=" + target.timeout),
	}, " "), nil
}

// NVCFBackendAgentStatusCommand builds an explicit-context wait for one
// backend's agent status.
func NVCFBackendAgentStatusCommand(name, namespace, kubeContext, agentStatus, timeout string) (string, error) {
	target, err := resolveKubernetesWaitTarget("NVCFBackend", name, namespace, kubeContext, timeout)
	if err != nil {
		return "", err
	}
	agentStatus = strings.TrimSpace(Interpolate(agentStatus))
	if agentStatus == "" {
		return "", fmt.Errorf("NVCFBackend agent status is empty")
	}
	return strings.Join([]string{
		"kubectl", "wait", "nvcfbackend", quoteCommandArg(target.name),
		"-n", quoteCommandArg(target.namespace),
		"--context", quoteCommandArg(target.kubeContext),
		"--for=jsonpath={.status.agentStatus}=" + quoteCommandArg(agentStatus),
		quoteCommandArg("--timeout=" + target.timeout),
	}, " "), nil
}

// GatewayAPIRouteConditionWaitCommand builds an explicit-context wait for one
// condition on one Gateway API route.
func GatewayAPIRouteConditionWaitCommand(route GatewayAPIRoute, kubeContext, condition, timeout string) (string, error) {
	target, err := resolveKubernetesWaitTarget(route.Kind, route.Name, route.Namespace, kubeContext, timeout)
	if err != nil {
		return "", err
	}
	route.Kind = strings.TrimSpace(Interpolate(route.Kind))
	route.Parent = strings.TrimSpace(Interpolate(route.Parent))
	condition = strings.TrimSpace(condition)
	if route.Kind == "" {
		return "", fmt.Errorf("gateway API route kind is empty")
	}
	if route.Parent == "" {
		return "", fmt.Errorf("gateway API route parent is empty")
	}
	if condition == "" {
		return "", fmt.Errorf("gateway API route condition is empty")
	}

	conditionExpression := fmt.Sprintf(
		`--for=jsonpath={.status.parents[?(@.parentRef.name==%q)].conditions[?(@.type==%q)].status}=True`,
		route.Parent,
		condition,
	)
	return BuildCommand(
		"kubectl", "wait", strings.ToLower(route.Kind)+"/"+target.name,
		"-n", target.namespace,
		"--context", target.kubeContext,
		conditionExpression,
		"--timeout="+target.timeout,
	), nil
}

// GatewayAPIRouteReadinessWaits plans the Accepted and ResolvedRefs waits for
// every route. Conditions remain grouped so every route is accepted before
// backend-reference resolution is checked.
func GatewayAPIRouteReadinessWaits(routes []GatewayAPIRoute, kubeContext, timeout string) ([]GatewayAPIRouteWait, error) {
	waits := make([]GatewayAPIRouteWait, 0, len(routes)*2)
	for _, condition := range []string{"Accepted", "ResolvedRefs"} {
		for index, route := range routes {
			command, err := GatewayAPIRouteConditionWaitCommand(route, kubeContext, condition, timeout)
			if err != nil {
				return nil, fmt.Errorf("route row %d condition %s: %w", index+1, condition, err)
			}
			waits = append(waits, GatewayAPIRouteWait{
				Row:       index + 1,
				Route:     route,
				Condition: condition,
				Command:   command,
			})
		}
	}
	return waits, nil
}

func resolveKubernetesWaitTarget(resourceType, name, namespace, kubeContext, timeout string) (kubernetesWaitTarget, error) {
	target := kubernetesWaitTarget{
		name:        strings.TrimSpace(Interpolate(name)),
		namespace:   strings.TrimSpace(Interpolate(namespace)),
		kubeContext: strings.TrimSpace(Interpolate(kubeContext)),
		timeout:     strings.TrimSpace(Interpolate(timeout)),
	}
	if target.name == "" {
		return kubernetesWaitTarget{}, fmt.Errorf("%s name is empty", resourceType)
	}
	if target.namespace == "" {
		return kubernetesWaitTarget{}, fmt.Errorf("namespace is empty")
	}
	if target.kubeContext == "" {
		return kubernetesWaitTarget{}, fmt.Errorf("kube context is empty")
	}
	if target.timeout == "" {
		return kubernetesWaitTarget{}, fmt.Errorf("timeout is empty")
	}
	return target, nil
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
