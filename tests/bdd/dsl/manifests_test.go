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
