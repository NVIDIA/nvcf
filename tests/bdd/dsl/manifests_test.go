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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderedManifestsContainResourceRejectsIssuerRefFragments(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: llm-router-serving-cert
spec:
  issuerRef:
    kind: ClusterIssuer
    name: nvcf-openbao-pki
`
	if err := os.WriteFile(filepath.Join(root, "certificate.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered Certificate: %v", err)
	}

	err := RenderedManifestsContainResource(root, KubernetesResource{
		Kind: "ClusterIssuer",
		Name: "nvcf-openbao-pki",
	})
	if err == nil {
		t.Fatal("issuerRef fragments were mistaken for a rendered ClusterIssuer resource")
	}
}

func TestRenderedManifestsContainResourceFindsTopLevelResource(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: llm-router-serving-cert
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: nvcf-openbao-pki
`
	if err := os.WriteFile(filepath.Join(root, "pki.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered PKI resources: %v", err)
	}

	err := RenderedManifestsContainResource(root, KubernetesResource{
		Kind: "ClusterIssuer",
		Name: "nvcf-openbao-pki",
	})
	if err != nil {
		t.Fatalf("find rendered ClusterIssuer: %v", err)
	}
}

// TestRenderedWorkloadImagesAreValidAcceptsAllContainerTypes covers regular,
// init, and ephemeral containers across direct and nested Pod specs.
func TestRenderedWorkloadImagesAreValidAcceptsAllContainerTypes(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      initContainers:
        - name: initialize
          image: nvcr.io/nvidia/nvcf/init:1.0.0
      containers:
        - name: api
          image: nvcr.io/nvidia/nvcf/api:1.0.0
      ephemeralContainers:
        - name: debug
          image: busybox
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: cleanup
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: cleanup
              image: nvcr.io/nvidia/nvcf/cleanup@sha256:1234
`
	if err := os.WriteFile(filepath.Join(root, "workloads.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered workloads: %v", err)
	}

	if err := RenderedWorkloadImagesAreValid(root); err != nil {
		t.Fatalf("validate rendered workload images: %v", err)
	}
}

// TestRenderedWorkloadImagesAreValidRejectsTagOnlyImage covers the image shape
// that caused the NATS auth-callout installation failure.
func TestRenderedWorkloadImagesAreValidRejectsTagOnlyImage(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nats-auth-callout-service
spec:
  template:
    spec:
      containers:
        - name: auth-callout
          image: ":0.8.3"
`
	if err := os.WriteFile(filepath.Join(root, "deployment.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered Deployment: %v", err)
	}

	err := RenderedWorkloadImagesAreValid(root)
	if err == nil {
		t.Fatal("tag-only image reference was accepted")
	}
	for _, want := range []string{"Deployment/nats-auth-callout-service", `containers[0] "auth-callout"`, `image reference ":0.8.3"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want detail %q", err, want)
		}
	}
}

// TestRenderedWorkloadImagesAreValidRejectsMissingInitContainerImage verifies
// that init containers receive the same validation as regular containers.
func TestRenderedWorkloadImagesAreValidRejectsMissingInitContainerImage(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: v1
kind: Pod
metadata:
  name: migrate
spec:
  initContainers:
    - name: prepare
  containers:
    - name: migrate
      image: nvcr.io/nvidia/nvcf/migrate:1.0.0
`
	if err := os.WriteFile(filepath.Join(root, "pod.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered Pod: %v", err)
	}

	err := RenderedWorkloadImagesAreValid(root)
	if err == nil || !strings.Contains(err.Error(), `initContainers[0] "prepare" has invalid image reference ""`) {
		t.Fatalf("error = %v, want missing init container image detail", err)
	}
}

// TestRenderedWorkloadImagesAreValidRequiresAWorkload prevents an empty render
// or unrelated YAML from satisfying the assertion.
func TestRenderedWorkloadImagesAreValidRequiresAWorkload(t *testing.T) {
	root := t.TempDir()
	body := `apiVersion: v1
kind: ConfigMap
metadata:
  name: image-config
data:
  image: :0.8.3
`
	if err := os.WriteFile(filepath.Join(root, "configmap.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered ConfigMap: %v", err)
	}

	err := RenderedWorkloadImagesAreValid(root)
	if err == nil || !strings.Contains(err.Error(), "contain no Kubernetes workloads") {
		t.Fatalf("error = %v, want no-workloads detail", err)
	}
}

func TestNamespaceManifestShape(t *testing.T) {
	body, err := NamespaceManifest("nvcf")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	out := string(body)
	for _, want := range []string{"apiVersion: v1", "kind: Namespace", "name: nvcf"} {
		if !strings.Contains(out, want) {
			t.Fatalf("manifest missing %q:\n%s", want, out)
		}
	}
}

func TestDockerConfigJSONSecretManifestEncodesAPIKey(t *testing.T) {
	body, err := DockerConfigJSONSecretManifest("nvcr-pull-secret", "nvcf", "secret-token")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	out := string(body)
	// The raw API key must never appear in the manifest text; only the
	// base64-encoded forms are acceptable.
	if strings.Contains(out, "secret-token") {
		t.Fatalf("manifest leaks raw api key:\n%s", out)
	}
	for _, want := range []string{
		"kind: Secret",
		"type: kubernetes.io/dockerconfigjson",
		"name: nvcr-pull-secret",
		"namespace: nvcf",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("manifest missing %q:\n%s", want, out)
		}
	}
}
