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
	"strings"
	"testing"
)

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
