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

import "testing"

func TestKubernetesResourceGetCommandBuildsExplicitExistenceGet(t *testing.T) {
	resource := KubernetesResource{Kind: "ServiceMonitor", Name: "nvcf-default-monitors-state-metrics"}
	got, err := KubernetesResourceGetCommand("monitoring", "k3d-ncp-local", resource, false)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl get servicemonitor/nvcf-default-monitors-state-metrics --namespace monitoring --context k3d-ncp-local -o name"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubernetesResourceGetCommandBuildsIgnoreNotFoundGet(t *testing.T) {
	resource := KubernetesResource{Kind: "PodMonitor", Name: "nvcf-default-monitors-worker"}
	got, err := KubernetesResourceGetCommand("monitoring", "k3d-ncp-local", resource, true)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl get podmonitor/nvcf-default-monitors-worker --namespace monitoring --context k3d-ncp-local --ignore-not-found -o name"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubernetesResourceYAMLGetCommandBuildsExplicitGet(t *testing.T) {
	resource := KubernetesResource{Kind: "OpenTelemetryCollector", Name: "nvcf-observability"}
	got, err := KubernetesResourceYAMLGetCommand("monitoring", "k3d-ncp-local", resource)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl get opentelemetrycollector/nvcf-observability --namespace monitoring --context k3d-ncp-local -o yaml"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubernetesResourceYAMLGetCommandInterpolatesExplicitTargets(t *testing.T) {
	t.Setenv("BDD_TEST_CONTEXT", "k3d-ncp-local-compute-1")
	resource := KubernetesResource{Kind: "ConfigMap", Name: "nvcf-api-env"}
	got, err := KubernetesResourceYAMLGetCommand("nvcf", "${BDD_TEST_CONTEXT}", resource)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl get configmap/nvcf-api-env --namespace nvcf --context k3d-ncp-local-compute-1 -o yaml"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubernetesResourceGetCommandRejectsMissingTargets(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		kubeContext string
		resource    KubernetesResource
	}{
		{name: "namespace", kubeContext: "k3d-ncp-local", resource: KubernetesResource{Kind: "Secret", Name: "pull-secret"}},
		{name: "context", namespace: "monitoring", resource: KubernetesResource{Kind: "Secret", Name: "pull-secret"}},
		{name: "kind", namespace: "monitoring", kubeContext: "k3d-ncp-local", resource: KubernetesResource{Name: "pull-secret"}},
		{name: "name", namespace: "monitoring", kubeContext: "k3d-ncp-local", resource: KubernetesResource{Kind: "Secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := KubernetesResourceGetCommand(test.namespace, test.kubeContext, test.resource, false); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestKubernetesResourceAbsentRejectsNameOutput(t *testing.T) {
	resource := KubernetesResource{Kind: "Secret", Name: "nvcr-pull-secret"}
	if err := KubernetesResourceAbsent("secret/nvcr-pull-secret\n", resource); err == nil {
		t.Fatal("expected existing resource error")
	}
	if err := KubernetesResourceAbsent("\n", resource); err != nil {
		t.Fatalf("empty output should prove absence: %v", err)
	}
}

func TestKubernetesDeploymentRolloutCommandBuildsExplicitWait(t *testing.T) {
	t.Setenv("BDD_KUBE_CONTEXT", "k3d-ncp-local")
	got, err := KubernetesDeploymentRolloutCommand("nvca-operator", "nvca-operator", "${BDD_KUBE_CONTEXT}", "10m")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl rollout status deployment/nvca-operator -n nvca-operator --context k3d-ncp-local --timeout=10m"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestNVCFBackendAgentStatusCommandBuildsExplicitWait(t *testing.T) {
	t.Setenv("BDD_BACKEND_NAME", "ncp-local-compute-1")
	got, err := NVCFBackendAgentStatusCommand("${BDD_BACKEND_NAME}", "nvca-operator", "k3d-ncp-local-compute-1", "healthy", "10m")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl wait nvcfbackend ncp-local-compute-1 -n nvca-operator --context k3d-ncp-local-compute-1 --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubernetesWaitCommandsRejectMissingInputs(t *testing.T) {
	tests := []struct {
		name        string
		resource    string
		namespace   string
		kubeContext string
		status      string
		timeout     string
	}{
		{name: "resource", namespace: "nvca-operator", kubeContext: "k3d-ncp-local", status: "healthy", timeout: "10m"},
		{name: "namespace", resource: "ncp-local", kubeContext: "k3d-ncp-local", status: "healthy", timeout: "10m"},
		{name: "context", resource: "ncp-local", namespace: "nvca-operator", status: "healthy", timeout: "10m"},
		{name: "status", resource: "ncp-local", namespace: "nvca-operator", kubeContext: "k3d-ncp-local", timeout: "10m"},
		{name: "timeout", resource: "ncp-local", namespace: "nvca-operator", kubeContext: "k3d-ncp-local", status: "healthy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NVCFBackendAgentStatusCommand(test.resource, test.namespace, test.kubeContext, test.status, test.timeout); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := KubernetesDeploymentRolloutCommand("", "nvca-operator", "k3d-ncp-local", "10m"); err == nil {
		t.Fatal("expected empty deployment name error")
	}
}

func TestKubernetesWaitCommandsQuoteArguments(t *testing.T) {
	got, err := NVCFBackendAgentStatusCommand("backend name", "operator namespace", "context name", "not ready", "10 m")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl wait nvcfbackend 'backend name' -n 'operator namespace' --context 'context name' --for=jsonpath={.status.agentStatus}='not ready' '--timeout=10 m'"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubectlApplyCommandTargetsExplicitContext(t *testing.T) {
	got, err := KubectlApplyCommand("/tmp/bdd manifests/secret.yaml", "k3d-ncp-local-compute-1")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl --context k3d-ncp-local-compute-1 apply -f '/tmp/bdd manifests/secret.yaml'"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestKubectlApplyCommandAllowsAmbientContext(t *testing.T) {
	got, err := KubectlApplyCommand("/tmp/secret.yaml", "")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl apply -f /tmp/secret.yaml"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
