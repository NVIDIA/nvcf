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
